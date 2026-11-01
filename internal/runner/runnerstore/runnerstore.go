// Package runnerstore is the runner's local SQLite database. It holds the
// runner's replica of its tasks' event logs: the replicated events, in the
// order they arrived from the server, and the replication cursor that follows
// them. See proposals/draft/runner-event-replica.md.
package runnerstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"

	_ "github.com/ncruces/go-sqlite3/driver" // registers the "sqlite3" database/sql driver
	_ "github.com/ncruces/go-sqlite3/embed"  // the SQLite build the driver runs

	"google.golang.org/protobuf/proto"

	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
)

const schema = `
CREATE TABLE IF NOT EXISTS events (
    seq     INTEGER PRIMARY KEY,  -- local arrival order
    id      INTEGER NOT NULL UNIQUE,
    task_id INTEGER NOT NULL,
    type    TEXT    NOT NULL,
    event   BLOB    NOT NULL      -- gritz.v1.Event, protobuf-encoded
);
CREATE INDEX IF NOT EXISTS events_task_seq ON events (task_id, seq);

CREATE TABLE IF NOT EXISTS replication (
    id     INTEGER PRIMARY KEY CHECK (id = 1),
    cursor TEXT NOT NULL          -- opaque; ListRunnerEventsResponse.next_page_token
);
`

// Store is the runner's SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens the database at path, creating it and its tables if absent.
func Open(path string) (*Store, error) {
	// The pragmas go in the DSN so they apply to every connection the pool
	// opens, not only the first.
	dsn := "file:" + (&url.URL{Path: path}).EscapedPath() + "?" + url.Values{
		"_pragma": {"journal_mode(wal)", "synchronous(full)", "busy_timeout(5000)"},
	}.Encode()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("runnerstore: open %s: %w", path, err)
	}
	// One connection: the runner is the database's only user, and SQLite
	// allows one writer at a time anyway.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, errors.Join(fmt.Errorf("runnerstore: create schema: %w", err), db.Close())
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// AppendEvents stores a page of replicated events and the cursor that
// follows it in one transaction.
func (s *Store) AppendEvents(ctx context.Context, events []*gritzv1.Event, cursor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("runnerstore: begin: %w", err)
	}
	defer tx.Rollback()
	for _, e := range events {
		data, err := proto.Marshal(e)
		if err != nil {
			return fmt.Errorf("runnerstore: marshal event %d: %w", e.GetId(), err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO events (id, task_id, type, event) VALUES (?, ?, ?, ?)`,
			e.GetId(), e.GetTaskId(), model.EventPayloadFromProto(e).Type(), data,
		); err != nil {
			return fmt.Errorf("runnerstore: insert event %d: %w", e.GetId(), err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO replication (id, cursor) VALUES (1, ?)
		 ON CONFLICT (id) DO UPDATE SET cursor = excluded.cursor`,
		cursor,
	); err != nil {
		return fmt.Errorf("runnerstore: set cursor: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("runnerstore: commit: %w", err)
	}
	return nil
}

// Cursor returns the stored replication cursor, or "" when none is stored.
func (s *Store) Cursor(ctx context.Context) (string, error) {
	var cursor string
	err := s.db.QueryRowContext(ctx, `SELECT cursor FROM replication WHERE id = 1`).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("runnerstore: read cursor: %w", err)
	}
	return cursor, nil
}

// TaskEventsAfter returns the task's events after the event with id afterID,
// in log order; afterID 0 returns all of them. afterID must be the id of a
// stored event: every position a reader holds came from this table, and
// nothing is deleted.
func (s *Store) TaskEventsAfter(ctx context.Context, taskID, afterID int64) ([]*gritzv1.Event, error) {
	var after int64
	if afterID != 0 {
		err := s.db.QueryRowContext(ctx, `SELECT seq FROM events WHERE id = ?`, afterID).Scan(&after)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("runnerstore: event %d is not stored", afterID)
		}
		if err != nil {
			return nil, fmt.Errorf("runnerstore: resolve event %d: %w", afterID, err)
		}
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT event FROM events WHERE task_id = ? AND seq > ? ORDER BY seq`,
		taskID, after,
	)
	if err != nil {
		return nil, fmt.Errorf("runnerstore: read task %d events: %w", taskID, err)
	}
	defer rows.Close()
	var events []*gritzv1.Event
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("runnerstore: scan task %d event: %w", taskID, err)
		}
		e := &gritzv1.Event{}
		if err := proto.Unmarshal(data, e); err != nil {
			return nil, fmt.Errorf("runnerstore: unmarshal task %d event: %w", taskID, err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("runnerstore: read task %d events: %w", taskID, err)
	}
	return events, nil
}

// TaskIDs returns the ids of every task with stored events, ascending.
func (s *Store) TaskIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT task_id FROM events ORDER BY task_id`)
	if err != nil {
		return nil, fmt.Errorf("runnerstore: read task ids: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("runnerstore: scan task id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("runnerstore: read task ids: %w", err)
	}
	return ids, nil
}
