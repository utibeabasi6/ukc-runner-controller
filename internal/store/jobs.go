package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"
)

type Job struct {
	JobID         string
	RequestID     int64
	ScaleSet      string
	Owner         string
	Repository    string
	WorkflowRef   string
	DisplayName   string
	EventName     string
	WorkflowRunID int64
	RunnerName    string
	Result        string
	QueuedAt      time.Time
	AssignedAt    *time.Time
	StartedAt     *time.Time
	CompletedAt   *time.Time
}

const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	JobCanceled  = "canceled"
)

func (j Job) Status() string {
	switch {
	case j.CompletedAt != nil && j.Result != "":
		return j.Result
	case j.StartedAt != nil:
		return JobRunning
	default:
		return JobQueued
	}
}

func (j Job) Wait() time.Duration {
	if j.StartedAt == nil {
		return 0
	}
	return j.StartedAt.Sub(j.QueuedAt)
}

func (j Job) Duration() time.Duration {
	if j.StartedAt == nil || j.CompletedAt == nil {
		return 0
	}
	return j.CompletedAt.Sub(*j.StartedAt)
}

type JobFilter struct {
	ScaleSet   string
	Status     string
	RunnerName string
	Limit      int
}

type Summary struct {
	Started        int
	Succeeded      int
	Failed         int
	MedianWait     time.Duration
	MedianDuration time.Duration
}

const jobColumns = `job_id, request_id, scale_set, owner, repository, workflow_ref, display_name,
	event_name, workflow_run_id, runner_name, result, queued_at, assigned_at, started_at, completed_at`

func scanJob(row interface{ Scan(...any) error }) (Job, error) {
	var j Job
	err := row.Scan(&j.JobID, &j.RequestID, &j.ScaleSet, &j.Owner, &j.Repository, &j.WorkflowRef, &j.DisplayName,
		&j.EventName, &j.WorkflowRunID, &j.RunnerName, &j.Result, &j.QueuedAt, &j.AssignedAt, &j.StartedAt, &j.CompletedAt)
	return j, err
}

// RecordJob inserts a job or merges the fields of a later message into it.
// Messages can arrive out of order or more than once, so empty fields never
// overwrite stored values and a timestamp keeps the first value recorded.
func (s *Store) RecordJob(ctx context.Context, j Job) error {
	if j.QueuedAt.IsZero() {
		j.QueuedAt = time.Now()
	}
	_, err := s.rw.ExecContext(ctx, `
		INSERT INTO jobs (`+jobColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (job_id) DO UPDATE SET
			request_id   = coalesce(nullif(excluded.request_id, 0), jobs.request_id),
			runner_name  = coalesce(nullif(excluded.runner_name, ''), jobs.runner_name),
			result       = coalesce(nullif(excluded.result, ''), jobs.result),
			assigned_at  = coalesce(jobs.assigned_at, excluded.assigned_at),
			started_at   = coalesce(jobs.started_at, excluded.started_at),
			completed_at = coalesce(jobs.completed_at, excluded.completed_at)`,
		j.JobID, j.RequestID, j.ScaleSet, j.Owner, j.Repository, j.WorkflowRef, j.DisplayName,
		j.EventName, j.WorkflowRunID, j.RunnerName, strings.ToLower(j.Result),
		j.QueuedAt.UTC(), utc(j.AssignedAt), utc(j.StartedAt), utc(j.CompletedAt))
	return err
}

func (s *Store) Job(ctx context.Context, jobID string) (Job, error) {
	j, err := scanJob(s.ro.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE job_id = ?`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	return j, err
}

func (s *Store) Jobs(ctx context.Context, f JobFilter) ([]Job, error) {
	var (
		where []string
		args  []any
	)
	if f.ScaleSet != "" {
		where = append(where, "scale_set = ?")
		args = append(args, f.ScaleSet)
	}
	if f.RunnerName != "" {
		where = append(where, "runner_name = ?")
		args = append(args, f.RunnerName)
	}
	switch f.Status {
	case "":
	case JobQueued:
		where = append(where, "started_at IS NULL AND completed_at IS NULL")
	case JobRunning:
		where = append(where, "started_at IS NOT NULL AND completed_at IS NULL")
	default:
		where = append(where, "completed_at IS NOT NULL AND result = ?")
		args = append(args, f.Status)
	}

	q := `SELECT ` + jobColumns + ` FROM jobs`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY queued_at DESC"
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.ro.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) Summary(ctx context.Context, since time.Time) (Summary, error) {
	rows, err := s.ro.QueryContext(ctx, `
		SELECT `+jobColumns+` FROM jobs WHERE started_at >= ? ORDER BY started_at`, since.UTC())
	if err != nil {
		return Summary{}, err
	}
	defer rows.Close()

	var (
		sum              Summary
		waits, durations []time.Duration
	)
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return Summary{}, err
		}
		sum.Started++
		waits = append(waits, j.Wait())
		switch j.Status() {
		case JobSucceeded:
			sum.Succeeded++
			durations = append(durations, j.Duration())
		case JobFailed:
			sum.Failed++
			durations = append(durations, j.Duration())
		}
	}
	if err := rows.Err(); err != nil {
		return Summary{}, err
	}

	sum.MedianWait = median(waits)
	sum.MedianDuration = median(durations)
	return sum, nil
}

func median(d []time.Duration) time.Duration {
	if len(d) == 0 {
		return 0
	}
	slices.Sort(d)
	return d[len(d)/2]
}

func utc(t *time.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}
