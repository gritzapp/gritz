// Package runnerstore is the runner's local SQLite database. It holds the
// runner's replica of its tasks' event logs: the replicated events, in the
// order they arrived from the server, the replication cursor that follows
// them, and a projection of each task's archive state. See
// proposals/draft/runner-event-replica.md.
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

CREATE TABLE IF NOT EXISTS tasks (
    id       INTEGER PRIMARY KEY,
    archived INTEGER NOT NULL DEFAULT 0  -- the fold of the task's archive events
);
`

// eventTypeUnknown is the type stored for an event whose payload arm this
// runner doesn't know: one added by a newer server.
const eventTypeUnknown = "unknown"

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

// TaskChange is a task becoming active or archived.
type TaskChange struct {
	ID       int64
	Archived bool
}

// AppendEvents stores a page of replicated events, the cursor that follows
// it, and the page's effect on the tasks table in one transaction. It returns
// the page's task changes in order of each task's first event in the page:
// new tasks that end the page unarchived, and tasks whose archive state
// flipped. A task that is new and archived within one page was never active,
// so it is not returned.
func (s *Store) AppendEvents(ctx context.Context, events []*gritzv1.Event, cursor string) ([]TaskChange, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("runnerstore: begin: %w", err)
	}
	defer tx.Rollback()
	type task struct{ known, before, archived bool }
	var ids []int64
	tasks := map[int64]*task{}
	for _, e := range events {
		data, err := proto.Marshal(e)
		if err != nil {
			return nil, fmt.Errorf("runnerstore: marshal event %d: %w", e.GetId(), err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO events (id, task_id, type, event) VALUES (?, ?, ?, ?)`,
			e.GetId(), e.GetTaskId(), eventType(e), data,
		); err != nil {
			return nil, fmt.Errorf("runnerstore: insert event %d: %w", e.GetId(), err)
		}
		t, ok := tasks[e.GetTaskId()]
		if !ok {
			t = &task{}
			err := tx.QueryRowContext(ctx,
				`SELECT archived FROM tasks WHERE id = ?`, e.GetTaskId(),
			).Scan(&t.before)
			switch {
			case err == nil:
				t.known = true
			case !errors.Is(err, sql.ErrNoRows):
				return nil, fmt.Errorf("runnerstore: read task %d: %w", e.GetTaskId(), err)
			}
			t.archived = t.before
			tasks[e.GetTaskId()] = t
			ids = append(ids, e.GetTaskId())
		}
		t.archived = foldArchived(t.archived, e)
	}
	var changes []TaskChange
	for _, id := range ids {
		t := tasks[id]
		if t.known && t.before == t.archived {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO tasks (id, archived) VALUES (?, ?)
			 ON CONFLICT (id) DO UPDATE SET archived = excluded.archived`,
			id, t.archived,
		); err != nil {
			return nil, fmt.Errorf("runnerstore: set task %d: %w", id, err)
		}
		if !t.known && t.archived {
			continue
		}
		changes = append(changes, TaskChange{ID: id, Archived: t.archived})
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO replication (id, cursor) VALUES (1, ?)
		 ON CONFLICT (id) DO UPDATE SET cursor = excluded.cursor`,
		cursor,
	); err != nil {
		return nil, fmt.Errorf("runnerstore: set cursor: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("runnerstore: commit: %w", err)
	}
	return changes, nil
}

// eventType returns the event's type column. An arm this runner doesn't know
// is stored as eventTypeUnknown rather than rejected: the server and runner
// are deployed separately, and readers skip what they don't understand.
func eventType(e *gritzv1.Event) string {
	payload := model.EventPayloadFromProto(e)
	if payload == nil {
		return eventTypeUnknown
	}
	return payload.Type()
}

// foldArchived returns whether a task is archived after e, given whether it
// was before.
func foldArchived(archived bool, e *gritzv1.Event) bool {
	switch e.GetLifecycle().GetKind() {
	case gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED, gritzv1.LifecycleKind_LIFECYCLE_KIND_AUTO_ARCHIVED:
		return true
	case gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED:
		return false
	default:
		return archived
	}
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

// UnarchivedTaskIDs returns the ids of every stored task that isn't archived,
// ascending.
func (s *Store) UnarchivedTaskIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM tasks WHERE NOT archived ORDER BY id`)
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
