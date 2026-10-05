package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
	"unikraft.com/cloud/sdk/platform"
	"unikraft.com/x/ptr"

	"github.com/utibeabasi6/ukc-runner-controller/internal/config"
	"github.com/utibeabasi6/ukc-runner-controller/internal/store"
)

const (
	StateStarting  = "starting"
	StateListening = "listening"
	StateError     = "error"
)

type ScaleSetStatus struct {
	Config     config.RunnerConfig
	ID         int
	State      string
	Error      string
	ErrorAt    time.Time
	LastPollAt time.Time
	Statistics scaleset.RunnerScaleSetStatistic
}

type Options struct {
	Config *config.Config
	GitHub *scaleset.Client
	UKC    platform.Client
	Store  *store.Store
	Logger *slog.Logger
	// Owner names the message sessions this controller opens in GitHub.
	Owner string
}

type Controller struct {
	cfg   *config.Config
	gh    *scaleset.Client
	ukc   platform.Client
	store *store.Store
	log   *slog.Logger
	owner string

	mu       sync.Mutex
	statuses map[string]*ScaleSetStatus
	quota    *platform.Quotas

	retries retryQueue
}

func New(opts Options) *Controller {
	statuses := make(map[string]*ScaleSetStatus, len(opts.Config.Runners))
	for _, rc := range opts.Config.Runners {
		statuses[rc.Name] = &ScaleSetStatus{Config: rc, State: StateStarting}
	}
	return &Controller{
		cfg:      opts.Config,
		gh:       opts.GitHub,
		ukc:      opts.UKC,
		store:    opts.Store,
		log:      opts.Logger,
		owner:    opts.Owner,
		statuses: statuses,
	}
}

// Run blocks until ctx is done. Runners that are busy when Run returns keep
// working: their instances stop on their own once the job finishes, and
// autokill deletes them if no controller is running by then.
func (c *Controller) Run(ctx context.Context) error {
	quota, err := c.fetchQuota(ctx)
	if err != nil {
		return fmt.Errorf("reading Unikraft Cloud quotas: %w", err)
	}
	if err := validateLimits(c.cfg.Runners, quota.Limits); err != nil {
		return err
	}
	c.setQuota(quota)

	var wg sync.WaitGroup
	for _, rc := range c.cfg.Runners {
		wg.Go(func() { c.runScaleSet(ctx, rc) })
	}
	wg.Go(func() { c.reconcileLoop(ctx) })
	wg.Go(func() { c.retryLoop(ctx) })
	wg.Wait()
	return nil
}

func (c *Controller) Statuses() []ScaleSetStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ScaleSetStatus, 0, len(c.cfg.Runners))
	for _, rc := range c.cfg.Runners {
		out = append(out, *c.statuses[rc.Name])
	}
	return out
}

func (c *Controller) Quota() *platform.Quotas {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.quota
}

func (c *Controller) Config() *config.Config {
	return c.cfg
}

func (c *Controller) runScaleSet(ctx context.Context, rc config.RunnerConfig) {
	const minBackoff, maxBackoff = 5 * time.Second, 5 * time.Minute
	backoff := minBackoff
	for {
		started := time.Now()
		err := c.listen(ctx, rc)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > maxBackoff {
			backoff = minBackoff
		}

		c.updateStatus(rc.Name, func(s *ScaleSetStatus) {
			s.State = StateError
			s.Error = err.Error()
			s.ErrorAt = time.Now()
		})
		c.log.Error("scale set listener stopped", "scale_set", rc.Name, "retry_in", backoff, "error", err)
		c.event(ctx, rc.Name, store.LevelError, fmt.Sprintf("listener stopped, retrying in %s: %v", backoff, err))

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (c *Controller) listen(ctx context.Context, rc config.RunnerConfig) error {
	c.updateStatus(rc.Name, func(s *ScaleSetStatus) { s.State = StateStarting })

	groupID := 1
	if !strings.EqualFold(c.cfg.GitHub.RunnerGroup, scaleset.DefaultRunnerGroup) {
		group, err := c.gh.GetRunnerGroupByName(ctx, c.cfg.GitHub.RunnerGroup)
		if err != nil {
			return fmt.Errorf("looking up runner group %q: %w", c.cfg.GitHub.RunnerGroup, err)
		}
		groupID = group.ID
	}

	want := &scaleset.RunnerScaleSet{
		Name:          rc.Name,
		RunnerGroupID: groupID,
		RunnerSetting: scaleset.RunnerSetting{DisableUpdate: true},
	}
	for _, l := range rc.Labels {
		want.Labels = append(want.Labels, scaleset.Label{Name: l})
	}

	ss, err := c.gh.GetRunnerScaleSet(ctx, groupID, rc.Name)
	switch {
	case err != nil:
		return fmt.Errorf("looking up scale set: %w", err)
	case ss == nil:
		ss, err = c.gh.CreateRunnerScaleSet(ctx, want)
	default:
		ss, err = c.gh.UpdateRunnerScaleSet(ctx, ss.ID, want)
	}
	if err != nil {
		return fmt.Errorf("registering scale set: %w", err)
	}

	session, err := c.gh.MessageSessionClient(ctx, ss.ID, c.owner)
	if err != nil {
		return fmt.Errorf("opening message session: %w", err)
	}
	defer session.Close(context.WithoutCancel(ctx))

	l, err := listener.New(session, listener.Config{
		ScaleSetID: ss.ID,
		MaxRunners: rc.MaxRunners,
		Logger:     c.log.With("scale_set", rc.Name),
	})
	if err != nil {
		return err
	}

	c.updateStatus(rc.Name, func(s *ScaleSetStatus) {
		s.ID = ss.ID
		s.State = StateListening
		s.Error = ""
	})
	c.log.Info("listening for jobs", "scale_set", rc.Name, "scale_set_id", ss.ID, "labels", rc.Labels)
	c.event(ctx, rc.Name, store.LevelInfo, "listening for jobs")

	err = l.Run(ctx, &scaler{
		c:       c,
		rc:      rc,
		id:      ss.ID,
		session: session,
		log:     c.log.With("scale_set", rc.Name),
	})
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (c *Controller) fetchQuota(ctx context.Context) (*platform.Quotas, error) {
	resp, err := c.ukc.GetUser(ctx)
	if err != nil {
		return nil, err
	}
	if resp.Data == nil || len(resp.Data.Quotas) == 0 {
		return nil, errors.New("no quotas in response")
	}
	return &resp.Data.Quotas[0], nil
}

func validateLimits(runners []config.RunnerConfig, limits *platform.QuotasLimits) error {
	if limits == nil {
		return nil
	}
	check := func(name, field string, v int, lo, hi *int64) error {
		if (lo != nil && int64(v) < *lo) || (hi != nil && int64(v) > *hi) {
			return fmt.Errorf("runner %q: %s %d is outside the account limits [%d, %d]", name, field, v, ptr.ZeroIfNil(lo), ptr.ZeroIfNil(hi))
		}
		return nil
	}

	var errs []error
	for _, rc := range runners {
		errs = append(errs,
			check(rc.Name, "vcpus", rc.VCPUs, limits.MinVcpus, limits.MaxVcpus),
			check(rc.Name, "memory_mb", rc.MemoryMB, limits.MinMemoryMb, limits.MaxMemoryMb),
			check(rc.Name, "disk_mb", rc.DiskMB, limits.MinVolumeMb, limits.MaxVolumeMb),
		)
	}
	return errors.Join(errs...)
}

func (c *Controller) setQuota(q *platform.Quotas) {
	c.mu.Lock()
	c.quota = q
	c.mu.Unlock()
}

func (c *Controller) updateStatus(name string, f func(*ScaleSetStatus)) {
	c.mu.Lock()
	f(c.statuses[name])
	c.mu.Unlock()
}

func (c *Controller) event(ctx context.Context, scaleSet, level, msg string) {
	err := c.store.AddEvent(context.WithoutCancel(ctx), store.Event{ScaleSet: scaleSet, Level: level, Message: msg})
	if err != nil {
		c.log.Error("recording event", "error", err)
	}
}
