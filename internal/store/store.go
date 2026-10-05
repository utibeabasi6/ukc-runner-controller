package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

//go:embed migrations/*.sql
var migrations embed.FS

// Store keeps one writer connection and a pool of readers. SQLite allows a
// single writer at a time, so funnelling writes through one connection avoids
// SQLITE_BUSY between goroutines of this process.
type Store struct {
	rw *sql.DB
	ro *sql.DB
}

func Open(ctx context.Context, path string) (*Store, error) {
	pragmas := []string{
		"busy_timeout(5000)",
		"journal_mode(WAL)",
		"foreign_keys(1)",
		"synchronous(NORMAL)",
	}

	rwq := url.Values{"_pragma": pragmas, "_txlock": {"immediate"}}
	rw, err := sql.Open("sqlite", "file:"+path+"?"+rwq.Encode())
	if err != nil {
		return nil, err
	}
	rw.SetMaxOpenConns(1)

	if err := migrate(ctx, rw); err != nil {
		return nil, errors.Join(err, rw.Close())
	}

	roq := url.Values{"_pragma": append(pragmas, "query_only(1)")}
	ro, err := sql.Open("sqlite", "file:"+path+"?"+roq.Encode())
	if err != nil {
		return nil, errors.Join(err, rw.Close())
	}

	return &Store{rw: rw, ro: ro}, nil
}

func (s *Store) Close() error {
	return errors.Join(s.ro.Close(), s.rw.Close())
}

func migrate(ctx context.Context, db *sql.DB) error {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}

	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}

	for i := version; i < len(names); i++ {
		stmt, err := migrations.ReadFile(names[i])
		if err != nil {
			return err
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(stmt)); err != nil {
			return errors.Join(fmt.Errorf("applying %s: %w", names[i], err), tx.Rollback())
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			return errors.Join(err, tx.Rollback())
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Prune(ctx context.Context, before time.Time) error {
	for _, q := range []string{
		`DELETE FROM runners WHERE created_at < ? AND finished_at IS NOT NULL`,
		`DELETE FROM jobs WHERE queued_at < ?`,
		`DELETE FROM events WHERE at < ?`,
	} {
		if _, err := s.rw.ExecContext(ctx, q, before.UTC()); err != nil {
			return err
		}
	}
	return nil
}
