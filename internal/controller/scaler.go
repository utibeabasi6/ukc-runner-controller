package controller

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/actions/scaleset"
	"unikraft.com/cloud/sdk/platform"
	"unikraft.com/x/ptr"

	"github.com/utibeabasi6/ukc-runner-controller/internal/config"
	"github.com/utibeabasi6/ukc-runner-controller/internal/store"
)

const (
	instanceTag = "ukc-runner-controller"

	// The runner resolves its work folder relative to its install directory
	// in the image, so the volume mount point and the folder must agree.
	runnerWorkFolder = "_work"
	runnerWorkDir    = "/home/runner/_work"

	jitConfigEnv = "ACTIONS_RUNNER_INPUT_JITCONFIG"

	// autokillAfter deletes a stopped instance when no controller is running
	// to collect it.
	autokillAfter = 30 * time.Minute

	// idleGracePeriod keeps a fresh runner alive long enough to pick up the
	// job it was created for before scale down considers it.
	idleGracePeriod = 2 * time.Minute
)

type scaler struct {
	c       *Controller
	rc      config.RunnerConfig
	id      int
	session *scaleset.MessageSessionClient
	log     *slog.Logger
	stats   *scaleset.RunnerScaleSetStatistic
}

// Scale is called once per poll. A nil message means the long poll timed out,
// so the scaler converges on the statistics of the previous message.
func (s *scaler) Scale(ctx context.Context, msg *scaleset.RunnerScaleSetMessage) error {
	if msg != nil {
		if err := s.handle(ctx, msg); err != nil {
			return err
		}
		if msg.Statistics != nil {
			s.stats = msg.Statistics
		}
	}

	s.c.updateStatus(s.rc.Name, func(st *ScaleSetStatus) {
		st.LastPollAt = time.Now()
		if s.stats != nil {
			st.Statistics = *s.stats
		}
	})

	if s.stats == nil {
		return nil
	}
	return s.converge(ctx, min(s.rc.MaxRunners, s.stats.TotalAssignedJobs))
}

func (s *scaler) handle(ctx context.Context, msg *scaleset.RunnerScaleSetMessage) error {
	if len(msg.JobAvailableMessages) > 0 {
		ids := make([]int64, 0, len(msg.JobAvailableMessages))
		for _, m := range msg.JobAvailableMessages {
			ids = append(ids, m.RunnerRequestID)
		}
		if _, err := s.session.AcquireJobs(ctx, ids); err != nil {
			return fmt.Errorf("acquiring jobs: %w", err)
		}
	}

	var jobs []store.Job
	for _, m := range msg.JobAvailableMessages {
		jobs = append(jobs, s.job(m.JobMessageBase))
	}
	for _, m := range msg.JobAssignedMessages {
		j := s.job(m.JobMessageBase)
		j.AssignedAt = &m.ScaleSetAssignTime
		jobs = append(jobs, j)
	}
	now := time.Now()
	for _, m := range msg.JobStartedMessages {
		j := s.job(m.JobMessageBase)
		j.RunnerName = m.RunnerName
		j.StartedAt = &now
		jobs = append(jobs, j)
	}
	for _, m := range msg.JobCompletedMessages {
		j := s.job(m.JobMessageBase)
		j.RunnerName = m.RunnerName
		j.Result = m.Result
		j.CompletedAt = &m.FinishTime
		if m.FinishTime.IsZero() {
			j.CompletedAt = &now
		}
		jobs = append(jobs, j)
	}
	for _, j := range jobs {
		if j.JobID == "" {
			s.log.Debug("skipping job message without a job ID", "request_id", j.RequestID)
			continue
		}
		if err := s.c.store.RecordJob(ctx, j); err != nil {
			return fmt.Errorf("recording job %s: %w", j.JobID, err)
		}
	}

	for _, m := range msg.JobStartedMessages {
		if err := s.c.store.MarkRunnerBusy(ctx, m.RunnerName, now); err != nil {
			return fmt.Errorf("marking runner %s busy: %w", m.RunnerName, err)
		}
	}
	for _, m := range msg.JobCompletedMessages {
		if m.RunnerName == "" {
			continue
		}
		if err := s.c.store.FinishRunner(ctx, m.RunnerName, store.RunnerFinished, "", now); err != nil {
			return fmt.Errorf("finishing runner %s: %w", m.RunnerName, err)
		}
	}
	return nil
}

func (s *scaler) job(m scaleset.JobMessageBase) store.Job {
	return store.Job{
		RequestID:     m.RunnerRequestID,
		ScaleSet:      s.rc.Name,
		JobID:         m.JobID,
		Owner:         m.OwnerName,
		Repository:    m.RepositoryName,
		WorkflowRef:   m.JobWorkflowRef,
		DisplayName:   m.JobDisplayName,
		EventName:     m.EventName,
		WorkflowRunID: m.WorkflowRunID,
		QueuedAt:      m.QueueTime,
	}
}

// converge never scales down a busy runner: those finish their job and exit.
// Errors that a later poll can retry are logged instead of returned, because
// returning an error tears down the message session.
func (s *scaler) converge(ctx context.Context, target int) error {
	active, err := s.c.store.CountActiveRunners(ctx, s.rc.Name)
	if err != nil {
		return fmt.Errorf("counting runners: %w", err)
	}

	switch {
	case target > active:
		s.log.Info("scaling up", "active", active, "target", target)
		var wg sync.WaitGroup
		for range target - active {
			wg.Go(func() {
				if err := s.startRunner(ctx); err != nil {
					s.log.Error("starting runner", "error", err)
					s.c.event(ctx, s.rc.Name, store.LevelError, "starting runner: "+err.Error())
				}
			})
		}
		wg.Wait()

	case target < active:
		queued, err := s.c.store.Runners(ctx, store.RunnerFilter{
			ScaleSet: s.rc.Name,
			States:   []store.RunnerState{store.RunnerQueued},
			Limit:    active - target,
		})
		if err != nil {
			return fmt.Errorf("listing queued runners: %w", err)
		}
		remove := queued
		if n := active - target - len(queued); n > 0 {
			idle, err := s.c.store.Runners(ctx, store.RunnerFilter{
				ScaleSet:      s.rc.Name,
				States:        []store.RunnerState{store.RunnerIdle},
				CreatedBefore: time.Now().Add(-idleGracePeriod),
				Limit:         n,
			})
			if err != nil {
				return fmt.Errorf("listing idle runners: %w", err)
			}
			remove = append(remove, idle...)
		}

		for _, r := range remove {
			s.c.retries.remove(r.Name)
			err := s.c.gh.RemoveRunner(ctx, int64(r.GitHubRunnerID))
			switch {
			case errors.Is(err, scaleset.JobStillRunningError):
				continue
			case err != nil && !isRunnerGone(err):
				s.log.Error("removing idle runner", "runner", r.Name, "error", err)
				continue
			}
			s.log.Info("scaled down runner", "runner", r.Name, "state", r.State)
			if err := s.c.store.FinishRunner(ctx, r.Name, store.RunnerRemoved, "scaled down before taking a job", time.Now()); err != nil {
				return fmt.Errorf("finishing runner %s: %w", r.Name, err)
			}
		}
	}
	return nil
}

func (s *scaler) startRunner(ctx context.Context) error {
	name := s.rc.Name + "-" + strings.ToLower(rand.Text()[:8])

	jit, err := s.c.gh.GenerateJitRunnerConfig(ctx, &scaleset.RunnerScaleSetJitRunnerSetting{
		Name:       name,
		WorkFolder: runnerWorkFolder,
	}, s.id)
	if err != nil {
		return fmt.Errorf("generating JIT config: %w", err)
	}

	err = s.c.store.CreateRunner(ctx, store.Runner{
		Name:           name,
		ScaleSet:       s.rc.Name,
		GitHubRunnerID: jit.Runner.ID,
		Image:          s.rc.Image,
		VCPUs:          s.rc.VCPUs,
		MemoryMB:       s.rc.MemoryMB,
		DiskMB:         s.rc.DiskMB,
		State:          store.RunnerIdle,
		CreatedAt:      time.Now(),
	})
	if err != nil {
		s.c.removeRunner(ctx, jit.Runner.ID)
		return fmt.Errorf("recording runner: %w", err)
	}

	req := platform.CreateInstanceRequest{
		Name:          &name,
		Image:         &s.rc.Image,
		MemoryMb:      new(int64(s.rc.MemoryMB)),
		Vcpus:         new(int32(s.rc.VCPUs)),
		Env:           map[string]string{jitConfigEnv: jit.EncodedJITConfig},
		Autostart:     new(true),
		RestartPolicy: new(platform.CreateInstanceRequestRestartPolicyNever),
		Autokill:      &platform.CreateInstanceRequestAutokill{TimeMs: new(uint64(autokillAfter.Milliseconds()))},
		Tags:          []string{instanceTag, "scale-set=" + s.rc.Name},
		Volumes: []platform.CreateInstanceRequestVolume{{
			At:     runnerWorkDir,
			SizeMb: new(uint64(s.rc.DiskMB)),
		}},
	}

	uuid, err := s.c.createInstance(ctx, req)
	var later *retryLaterError
	if errors.As(err, &later) {
		if err := s.c.store.QueueRunner(ctx, name); err != nil {
			s.c.removeRunner(ctx, jit.Runner.ID)
			return fmt.Errorf("queueing runner %s: %w", name, err)
		}
		s.c.retries.push(pendingInstance{scaleSet: s.rc.Name, runnerID: jit.Runner.ID, req: req}, later.retryAfter)
		s.log.Warn("queued runner for retry", "runner", name, "reason", later)
		return nil
	}
	if err != nil {
		s.c.removeRunner(ctx, jit.Runner.ID)
		if ferr := s.c.store.FinishRunner(ctx, name, store.RunnerFailed, err.Error(), time.Now()); ferr != nil {
			s.log.Error("recording failed runner", "runner", name, "error", ferr)
		}
		return fmt.Errorf("creating instance %s: %w", name, err)
	}

	if err := s.c.store.SetRunnerInstance(ctx, name, uuid); err != nil {
		return fmt.Errorf("recording instance of %s: %w", name, err)
	}
	s.log.Info("started runner", "runner", name)
	return nil
}

func (c *Controller) createInstance(ctx context.Context, req platform.CreateInstanceRequest) (string, error) {
	resp, err := c.ukc.CreateInstance(ctx, req)
	switch {
	case platform.ErrorContains(err, platform.APIHTTPErrorQuota):
		return "", &retryLaterError{reason: err.Error()}
	case err != nil:
		return "", err
	case resp.Data == nil || len(resp.Data.Instances) == 0:
		return "", errors.New("no instance in response")
	}

	inst := resp.Data.Instances[0]
	if ptr.ZeroIfNil(inst.Status) == platform.ResponseStatusERROR {
		msg := ptr.ZeroIfNil(inst.Message)
		if ptr.ZeroIfNil(inst.Error) == int32(platform.APIHTTPErrorQuota) {
			return "", &retryLaterError{reason: msg}
		}
		return "", errors.New(msg)
	}
	return ptr.ZeroIfNil(inst.Uuid), nil
}

func (c *Controller) removeRunner(ctx context.Context, id int) {
	if err := c.gh.RemoveRunner(context.WithoutCancel(ctx), int64(id)); err != nil && !isRunnerGone(err) {
		c.log.Error("removing runner registration", "runner_id", id, "error", err)
	}
}

func isRunnerGone(err error) bool {
	return errors.Is(err, scaleset.RunnerNotFoundError) || errors.Is(err, scaleset.NotFoundError)
}
