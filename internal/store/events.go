package store

import (
	"context"
	"time"
)

const (
	LevelInfo  = "info"
	LevelError = "error"
)

type Event struct {
	ID       int64
	At       time.Time
	ScaleSet string
	Level    string
	Message  string
}

func (s *Store) AddEvent(ctx context.Context, e Event) error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	_, err := s.rw.ExecContext(ctx, `INSERT INTO events (at, scale_set, level, message) VALUES (?, ?, ?, ?)`,
		e.At.UTC(), e.ScaleSet, e.Level, e.Message)
	return err
}

func (s *Store) Events(ctx context.Context, limit int) ([]Event, error) {
	rows, err := s.ro.QueryContext(ctx, `
		SELECT id, at, scale_set, level, message FROM events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.At, &e.ScaleSet, &e.Level, &e.Message); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
