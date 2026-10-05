package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type RunnerState string

const (
	RunnerQueued   RunnerState = "queued"
	RunnerIdle     RunnerState = "idle"
	RunnerBusy     RunnerState = "busy"
	RunnerFinished RunnerState = "finished"
	RunnerFailed   RunnerState = "failed"
	RunnerRemoved  RunnerState = "removed"
)

type Runner struct {
	Name              string
	ScaleSet          string
	GitHubRunnerID    int
	InstanceUUID      string
	Image             string
	VCPUs             int
	MemoryMB          int
	DiskMB            int
	State             RunnerState
	Error             string
	ExitCode          *int
	StopReason        string
	BootTimeMS        *int
	LogTail           string
	CreatedAt         time.Time
	JobStartedAt      *time.Time
	FinishedAt        *time.Time
	InstanceDeletedAt *time.Time
}

// Active reports whether the runner still counts against its scale set. A
// runner is active until it reaches a terminal state, which sets FinishedAt.
func (r Runner) Active() bool {
	return r.FinishedAt == nil
}

type RunnerFilter struct {
	ScaleSet       string
	Active         bool
	States         []RunnerState
	CreatedBefore  time.Time
	InstanceExists bool
	Limit          int
}

type InstanceExit struct {
	ExitCode   *int
	StopReason string
	BootTimeMS *int
	LogTail    string
	DeletedAt  time.Time
}

const runnerColumns = `name, scale_set, github_runner_id, instance_uuid, image, vcpus,
	memory_mb, disk_mb, state, error, exit_code, stop_reason, boot_time_ms, log_tail,
	created_at, job_started_at, finished_at, instance_deleted_at`

func scanRunner(row interface{ Scan(...any) error }) (Runner, error) {
	var r Runner
	err := row.Scan(&r.Name, &r.ScaleSet, &r.GitHubRunnerID, &r.InstanceUUID, &r.Image, &r.VCPUs,
		&r.MemoryMB, &r.DiskMB, &r.State, &r.Error, &r.ExitCode, &r.StopReason, &r.BootTimeMS, &r.LogTail,
		&r.CreatedAt, &r.JobStartedAt, &r.FinishedAt, &r.InstanceDeletedAt)
	return r, err
}

func (s *Store) CreateRunner(ctx context.Context, r Runner) error {
	_, err := s.rw.ExecContext(ctx, `
		INSERT INTO runners (name, scale_set, github_runner_id, image, vcpus, memory_mb, disk_mb, state, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Name, r.ScaleSet, r.GitHubRunnerID, r.Image, r.VCPUs, r.MemoryMB, r.DiskMB, r.State, r.CreatedAt.UTC())
	return err
}

// QueueRunner marks a runner whose instance is waiting for the API rate limit.
func (s *Store) QueueRunner(ctx context.Context, name string) error {
	_, err := s.rw.ExecContext(ctx, `UPDATE runners SET state = 'queued' WHERE name = ? AND state = 'idle'`, name)
	return err
}

func (s *Store) SetRunnerInstance(ctx context.Context, name, uuid string) error {
	_, err := s.rw.ExecContext(ctx, `
		UPDATE runners SET instance_uuid = ?, state = iif(state = 'queued', 'idle', state)
		WHERE name = ?`, uuid, name)
	return err
}

func (s *Store) MarkRunnerBusy(ctx context.Context, name string, at time.Time) error {
	_, err := s.rw.ExecContext(ctx, `
		UPDATE runners SET state = 'busy', job_started_at = ?
		WHERE name = ? AND state = 'idle'`, at.UTC(), name)
	return err
}

// FinishRunner moves an active runner to a terminal state. A runner that has
// already finished keeps its first state and error.
func (s *Store) FinishRunner(ctx context.Context, name string, state RunnerState, reason string, at time.Time) error {
	_, err := s.rw.ExecContext(ctx, `
		UPDATE runners SET state = ?, error = ?, finished_at = ?
		WHERE name = ? AND finished_at IS NULL`, state, reason, at.UTC(), name)
	return err
}

func (s *Store) RecordInstanceExit(ctx context.Context, name string, exit InstanceExit) error {
	_, err := s.rw.ExecContext(ctx, `
		UPDATE runners SET exit_code = ?, stop_reason = ?, boot_time_ms = ?, log_tail = ?, instance_deleted_at = ?
		WHERE name = ?`,
		exit.ExitCode, exit.StopReason, exit.BootTimeMS, exit.LogTail, exit.DeletedAt.UTC(), name)
	return err
}

func (s *Store) CountActiveRunners(ctx context.Context, scaleSet string) (int, error) {
	var n int
	err := s.ro.QueryRowContext(ctx, `
		SELECT count(*) FROM runners WHERE scale_set = ? AND finished_at IS NULL`, scaleSet).Scan(&n)
	return n, err
}

func (s *Store) Runner(ctx context.Context, name string) (Runner, error) {
	r, err := scanRunner(s.ro.QueryRowContext(ctx, `SELECT `+runnerColumns+` FROM runners WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

func (s *Store) Runners(ctx context.Context, f RunnerFilter) ([]Runner, error) {
	var (
		where []string
		args  []any
	)
	if f.ScaleSet != "" {
		where = append(where, "scale_set = ?")
		args = append(args, f.ScaleSet)
	}
	if f.Active {
		where = append(where, "finished_at IS NULL")
	}
	if len(f.States) > 0 {
		where = append(where, "state IN (?"+strings.Repeat(", ?", len(f.States)-1)+")")
		for _, st := range f.States {
			args = append(args, st)
		}
	}
	if !f.CreatedBefore.IsZero() {
		where = append(where, "created_at < ?")
		args = append(args, f.CreatedBefore.UTC())
	}
	if f.InstanceExists {
		where = append(where, "instance_deleted_at IS NULL")
	}

	q := `SELECT ` + runnerColumns + ` FROM runners`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at DESC"
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.ro.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Runner
	for rows.Next() {
		r, err := scanRunner(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
