package controller

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"unikraft.com/cloud/sdk/platform"
	"unikraft.com/x/ptr"

	"github.com/utibeabasi6/ukc-runner-controller/internal/store"
)

const (
	reconcileInterval = 30 * time.Second
	pruneInterval     = time.Hour
	retention         = 30 * 24 * time.Hour

	// exitGracePeriod lets a runner that reported its job as completed exit
	// on its own, so the controller can record its exit code.
	exitGracePeriod = 2 * time.Minute

	// createGracePeriod covers the gap between recording a runner and its
	// instance appearing in the instance list.
	createGracePeriod = time.Minute

	// logTailBytes is the largest log page the platform API returns.
	logTailBytes = 4096*4 - 1
)

func (c *Controller) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()

	var lastPrune time.Time
	for {
		if err := c.reconcile(ctx); err != nil && ctx.Err() == nil {
			c.log.Error("reconciling instances", "error", err)
		}
		if time.Since(lastPrune) > pruneInterval {
			if err := c.store.Prune(ctx, time.Now().Add(-retention)); err != nil && ctx.Err() == nil {
				c.log.Error("pruning history", "error", err)
			}
			lastPrune = time.Now()
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Controller) reconcile(ctx context.Context) error {
	if quota, err := c.fetchQuota(ctx); err != nil {
		c.log.Warn("reading quotas", "error", err)
	} else {
		c.setQuota(quota)
	}

	runners, err := c.store.Runners(ctx, store.RunnerFilter{InstanceExists: true})
	if err != nil {
		return fmt.Errorf("listing runners: %w", err)
	}
	if len(runners) == 0 {
		return nil
	}

	resp, err := c.ukc.GetInstances(ctx, nil, platform.GetInstancesOpts{
		Details: new(true),
		Tags:    []string{instanceTag},
	})
	if err != nil {
		return fmt.Errorf("listing instances: %w", err)
	}
	instances := make(map[string]*platform.Instance)
	if resp.Data != nil {
		for i := range resp.Data.Instances {
			inst := &resp.Data.Instances[i]
			instances[ptr.ZeroIfNil(inst.Name)] = inst
		}
	}

	now := time.Now()
	for _, r := range runners {
		if r.State == store.RunnerQueued {
			if !c.retries.has(r.Name) {
				if err := c.collect(ctx, r, nil); err != nil {
					c.log.Error("collecting queued runner", "runner", r.Name, "error", err)
				}
			}
			continue
		}

		inst, ok := instances[r.Name]
		if !ok {
			if now.Sub(r.CreatedAt) <= createGracePeriod {
				continue
			}
			// The list may not return every instance, so confirm by name
			// before treating the instance as gone.
			resp, err := c.ukc.GetInstances(ctx, []platform.NameOrUUID{{Name: &r.Name}}, platform.GetInstancesOpts{Details: new(true)})
			switch {
			case platform.ErrorContains(err, platform.APIHTTPErrorNotFound):
			case err != nil:
				c.log.Error("looking up instance", "runner", r.Name, "error", err)
				continue
			case resp.Data != nil && len(resp.Data.Instances) > 0:
				found := resp.Data.Instances[0]
				switch {
				case ptr.ZeroIfNil(found.Status) != platform.ResponseStatusERROR:
					inst = &found
				case ptr.ZeroIfNil(found.Error) != int32(platform.APIHTTPErrorNotFound):
					c.log.Error("looking up instance", "runner", r.Name, "error", ptr.ZeroIfNil(found.Message))
					continue
				}
			}
		}

		var collect bool
		switch {
		case inst == nil:
			collect = true
		case ptr.ZeroIfNil(inst.State) == platform.InstanceStateStopped:
			collect = true
		case r.State == store.RunnerRemoved || r.State == store.RunnerFailed:
			collect = true
		case r.State == store.RunnerFinished:
			collect = r.FinishedAt != nil && now.Sub(*r.FinishedAt) > exitGracePeriod
		}
		if !collect {
			continue
		}
		if err := c.collect(ctx, r, inst); err != nil {
			c.log.Error("collecting instance", "runner", r.Name, "error", err)
		}
	}
	return nil
}

// collect records what is left of a runner's instance and deletes it. The
// instance is nil when it no longer exists on the platform.
func (c *Controller) collect(ctx context.Context, r store.Runner, inst *platform.Instance) error {
	exit := store.InstanceExit{DeletedAt: time.Now()}

	if inst != nil {
		uuid := ptr.ZeroIfNil(inst.Uuid)
		if inst.ExitCode != nil {
			exit.ExitCode = new(int(*inst.ExitCode))
		}
		if inst.BootTimeUs != nil {
			exit.BootTimeMS = new(int(*inst.BootTimeUs / 1000))
		}
		exit.StopReason = inst.DescribeStop()

		logs, err := c.InstanceLogs(ctx, uuid)
		if err != nil {
			c.log.Warn("reading instance logs", "runner", r.Name, "error", err)
		}
		exit.LogTail = logs

		resp, err := c.ukc.DeleteInstanceByUUID(ctx, uuid, platform.DeleteInstanceByUUIDRequestBody{})
		if err == nil && resp.Data != nil && len(resp.Data.Instances) > 0 {
			if item := resp.Data.Instances[0]; ptr.ZeroIfNil(item.Status) == platform.ResponseStatusERROR &&
				ptr.ZeroIfNil(item.Error) != int32(platform.APIHTTPErrorNotFound) {
				err = errors.New(ptr.ZeroIfNil(item.Message))
			}
		}
		if err != nil && !platform.ErrorContains(err, platform.APIHTTPErrorNotFound) {
			return fmt.Errorf("deleting instance: %w", err)
		}
	}

	if r.Active() {
		reason := "instance stopped before the runner took a job"
		switch {
		case r.State == store.RunnerQueued:
			reason = "controller restarted while the instance was queued"
		case inst == nil:
			reason = "instance disappeared"
		}
		state := store.RunnerFailed
		if r.State == store.RunnerBusy || (exit.ExitCode != nil && *exit.ExitCode == 0) {
			state, reason = store.RunnerFinished, ""
		} else {
			c.removeRunner(ctx, r.GitHubRunnerID)
			c.event(ctx, r.ScaleSet, store.LevelError, fmt.Sprintf("runner %s failed: %s %s", r.Name, reason, exit.StopReason))
		}
		if err := c.store.FinishRunner(ctx, r.Name, state, reason, time.Now()); err != nil {
			return err
		}
	}

	return c.store.RecordInstanceExit(ctx, r.Name, exit)
}

func (c *Controller) InstanceLogs(ctx context.Context, uuid string) (string, error) {
	resp, err := c.ukc.GetInstanceLogsByUUID(ctx, uuid, platform.GetInstanceLogsByUUIDRequestBody{
		Offset: new(int64(-logTailBytes)),
		Limit:  new(int64(logTailBytes)),
	})
	if err != nil {
		return "", err
	}
	if resp.Data == nil || len(resp.Data.Instances) == 0 {
		return "", nil
	}
	out, err := base64.StdEncoding.DecodeString(ptr.ZeroIfNil(resp.Data.Instances[0].Output))
	return string(out), err
}
