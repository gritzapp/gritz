// Package runnerstore is the runner's local SQLite database. It holds the
// runner's replica of its tasks' event logs: the replicated events, in the
// order they arrived from the server, the replication cursor that follows
// them, and a projection of each task's archive state. See
// proposals/draft/runner-event-replica.md.
package runnerstore

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"google.golang.org/protobuf/proto"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/runnerstore/sqlc"
	"github.com/gritzapp/gritz/internal/x/sqlite"
)

// migrations holds the schema, one file per version, applied in name order.
//
//go:embed sql/migrations/*.sql
var migrations embed.FS

// Store is the runner's SQLite database.
type Store struct {
	db *sql.DB
	q  *sqlc.Queries
}

// Open opens the database at path, creating it and its tables if absent.
func Open(path string) (*Store, error) {
	db, err := sqlite.Open(path, sqlite.Options{
		JournalMode: sqlite.JournalModeWAL,
		Synchronous: sqlite.SynchronousFull,
		BusyTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("runnerstore: %w", err)
	}
	// One connection: the runner is the database's only user, and SQLite
	// allows one writer at a time anyway.
	db.SetMaxOpenConns(1)
	fsys, err := fs.Sub(migrations, "sql/migrations")
	if err != nil {
		panic(fmt.Sprintf("runnerstore: embedded migrations: %v", err))
	}
	if err := sqlite.Migrate(db, fsys); err != nil {
		return nil, errors.Join(fmt.Errorf("runnerstore: %w", err), db.Close())
	}
	return &Store{db: db, q: sqlc.New(db)}, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// TaskChange is a task's state after a page that had a command or archive
// event for it. It may repeat the state the task was already in.
type TaskChange struct {
	ID int64
	// Command and Version are those of the task's last command event, or
	// zero when it has had none.
	Command  gritzv1.TaskCommand
	Version  int64
	Archived bool
}

// AppendEvents stores a page of replicated events, the cursor that follows
// it, and the page's effect on the tasks table in one transaction. It returns
// the state after the page of every task with a command or archive event in
// it, in order of each task's first such event. A report may repeat the
// task's current state.
func (s *Store) AppendEvents(ctx context.Context, events []*gritzv1.Event, cursor string) ([]TaskChange, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("runnerstore: begin: %w", err)
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)
	for _, e := range events {
		data, err := proto.Marshal(e)
		if err != nil {
			return nil, fmt.Errorf("runnerstore: marshal event %d: %w", e.GetId(), err)
		}
		if err := q.InsertEvent(ctx, sqlc.InsertEventParams{
			ID:     e.GetId(),
			TaskID: e.GetTaskId(),
			Event:  data,
		}); err != nil {
			return nil, fmt.Errorf("runnerstore: insert event %d: %w", e.GetId(), err)
		}
	}
	var changes []TaskChange
	for _, t := range TaskChanges(events) {
		params := sqlc.UpsertTaskParams{ID: t.ID}
		if t.Command != nil {
			params.Command = sql.NullInt64{Int64: int64(t.Command.GetCommand()), Valid: true}
			params.Version = sql.NullInt64{Int64: t.Command.GetVersion(), Valid: true}
		}
		if t.Archived != nil {
			params.Archived = sql.NullBool{Bool: *t.Archived, Valid: true}
		}
		row, err := q.UpsertTask(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("runnerstore: upsert task %d: %w", t.ID, err)
		}
		changes = append(changes, TaskChange{
			ID:       row.ID,
			Command:  gritzv1.TaskCommand(row.Command.Int64),
			Version:  row.Version.Int64,
			Archived: row.Archived.Bool,
		})
	}
	if err := q.SetEventReplicationCursor(ctx, cursor); err != nil {
		return nil, fmt.Errorf("runnerstore: set cursor: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("runnerstore: commit: %w", err)
	}
	return changes, nil
}

// EventReplicationCursor returns the replication cursor: the single position
// in the runner's whole event log, across all of its tasks, stored as the
// last appended page's next_page_token. It returns "" when no page is stored.
func (s *Store) EventReplicationCursor(ctx context.Context) (string, error) {
	cursor, err := s.q.GetEventReplicationCursor(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("runnerstore: read cursor: %w", err)
	}
	return cursor, nil
}

// TaskEventsAfter returns the task's events after the event with id afterID,
// in log order; afterID 0 returns all of them. An afterID that isn't stored
// yet returns no events rather than the whole log: once that event is
// replicated, the next call returns what follows it. That happens when a
// reader's position outlives the database, such as a driver's last event id
// after the runner's database was rebuilt.
func (s *Store) TaskEventsAfter(ctx context.Context, taskID, afterID int64) ([]*gritzv1.Event, error) {
	rows, err := s.q.ListTaskEventsAfter(ctx, sqlc.ListTaskEventsAfterParams{
		TaskID:  taskID,
		AfterID: afterID,
	})
	if err != nil {
		return nil, fmt.Errorf("runnerstore: read task %d events: %w", taskID, err)
	}
	var events []*gritzv1.Event
	for _, data := range rows {
		e := &gritzv1.Event{}
		if err := proto.Unmarshal(data, e); err != nil {
			return nil, fmt.Errorf("runnerstore: unmarshal task %d event: %w", taskID, err)
		}
		events = append(events, e)
	}
	return events, nil
}

// UnarchivedTaskIDs returns the ids of every stored task that isn't archived,
// ascending.
func (s *Store) UnarchivedTaskIDs(ctx context.Context) ([]int64, error) {
	ids, err := s.q.ListUnarchivedTaskIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("runnerstore: read task ids: %w", err)
	}
	return ids, nil
}
