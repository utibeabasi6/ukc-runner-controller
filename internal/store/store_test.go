package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	for range 2 {
		s, err := Open(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
}

func TestRecordJobMergesMessages(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	queued := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	started := queued.Add(3 * time.Second)
	completed := started.Add(time.Minute)

	steps := []Job{
		{RequestID: 7, ScaleSet: "small", Repository: "repo", QueuedAt: queued},
		{RequestID: 7, ScaleSet: "small", Result: "Succeeded", CompletedAt: &completed, RunnerName: "small-1"},
		{RequestID: 7, ScaleSet: "small", StartedAt: &started, RunnerName: "small-1"},
	}
	for _, j := range steps {
		if err := s.RecordJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.Job(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status() != "succeeded" {
		t.Errorf("status = %q, want succeeded", got.Status())
	}
	if !got.QueuedAt.Equal(queued) {
		t.Errorf("queued_at = %v, want %v", got.QueuedAt, queued)
	}
	if got.Wait() != 3*time.Second || got.Duration() != time.Minute {
		t.Errorf("wait = %v, duration = %v", got.Wait(), got.Duration())
	}
	if got.Repository != "repo" || got.RunnerName != "small-1" {
		t.Errorf("unexpected job %+v", got)
	}

	for status, want := range map[string]int{"succeeded": 1, JobQueued: 0, JobRunning: 0} {
		jobs, err := s.Jobs(ctx, JobFilter{Status: status})
		if err != nil {
			t.Fatal(err)
		}
		if len(jobs) != want {
			t.Errorf("Jobs(status=%s) = %d, want %d", status, len(jobs), want)
		}
	}
}

func TestRunnerLifecycle(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	now := time.Now()

	for _, name := range []string{"small-a", "small-b"} {
		err := s.CreateRunner(ctx, Runner{Name: name, ScaleSet: "small", Image: "img", VCPUs: 1, MemoryMB: 1024, DiskMB: 2048, State: RunnerIdle, CreatedAt: now})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MarkRunnerBusy(ctx, "small-a", now); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRunner(ctx, "small-a", RunnerFinished, "", now); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRunner(ctx, "small-a", RunnerFailed, "late", now); err != nil {
		t.Fatal(err)
	}

	r, err := s.Runner(ctx, "small-a")
	if err != nil {
		t.Fatal(err)
	}
	if r.State != RunnerFinished || r.Error != "" || r.JobStartedAt == nil {
		t.Errorf("unexpected runner %+v", r)
	}

	n, err := s.CountActiveRunners(ctx, "small")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("active = %d, want 1", n)
	}

	exit := 0
	if err := s.RecordInstanceExit(ctx, "small-a", InstanceExit{ExitCode: &exit, DeletedAt: now}); err != nil {
		t.Fatal(err)
	}
	live, err := s.Runners(ctx, RunnerFilter{InstanceExists: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].Name != "small-b" {
		t.Errorf("runners with instances = %+v", live)
	}

	if _, err := s.Runner(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestPruneKeepsActiveRunners(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	old := time.Now().Add(-48 * time.Hour)

	for _, name := range []string{"old-idle", "old-done"} {
		err := s.CreateRunner(ctx, Runner{Name: name, ScaleSet: "small", Image: "img", State: RunnerIdle, CreatedAt: old})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.FinishRunner(ctx, "old-done", RunnerFinished, "", old); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune(ctx, time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	rs, err := s.Runners(ctx, RunnerFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || rs[0].Name != "old-idle" {
		t.Errorf("runners after prune = %+v", rs)
	}
}

func TestRecordJobKeepsFirstTimestamp(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()

	first := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	redelivered := first.Add(time.Minute)
	for _, at := range []time.Time{first, redelivered} {
		if err := s.RecordJob(ctx, Job{RequestID: 1, ScaleSet: "small", QueuedAt: first, StartedAt: &at}); err != nil {
			t.Fatal(err)
		}
	}

	j, err := s.Job(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !j.StartedAt.Equal(first) {
		t.Errorf("started_at = %v, want the first value %v", j.StartedAt, first)
	}
}
