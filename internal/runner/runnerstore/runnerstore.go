// Package runnerstore is the runner's local SQLite database. It holds the
// runner's replica of its tasks' event logs: the replicated events, in the
// order they arrived from the server, and the replication cursor that follows
// them. See proposals/draft/runner-event-replica.md.
package runnerstore

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"

	_ "github.com/ncruces/go-sqlite3/driver" // registers the "sqlite3" database/sql driver
	_ "github.com/ncruces/go-sqlite3/embed"  // the SQLite build the driver runs

	"google.golang.org/protobuf/proto"

	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/runnerstore/sqlc"
)

// migrations holds the schema, one file per version, applied in name order.
//
//go:embed sql/migrations/*.sql
var migrations embed.FS

// eventTypeUnknown is the type stored for an event whose payload arm this
// runner doesn't know: one added by a newer server.
const eventTypeUnknown = "unknown"

// Store is the runner's SQLite database.
type Store struct {
	db *sql.DB
	q  *sqlc.Queries
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
	if err := migrate(db); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return &Store{db: db, q: sqlc.New(db)}, nil
}

// migrate applies the migrations the database lacks, each in its own
// transaction. PRAGMA user_version holds how many are applied.
func migrate(db *sql.DB) error {
	files, err := fs.Glob(migrations, "sql/migrations/*.sql")
	if err != nil {
		return fmt.Errorf("runnerstore: list migrations: %w", err)
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("runnerstore: read schema version: %w", err)
	}
	if version > len(files) {
		return fmt.Errorf("runnerstore: schema version %d is newer than this runner's %d", version, len(files))
	}
	for i, name := range files[version:] {
		if err := applyMigration(db, name, version+i+1); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(db *sql.DB, name string, version int) error {
	data, err := migrations.ReadFile(name)
	if err != nil {
		return fmt.Errorf("runnerstore: read %s: %w", name, err)
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("runnerstore: begin %s: %w", name, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(string(data)); err != nil {
		return fmt.Errorf("runnerstore: apply %s: %w", name, err)
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version)); err != nil {
		return fmt.Errorf("runnerstore: set schema version after %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("runnerstore: commit %s: %w", name, err)
	}
	return nil
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
	q := s.q.WithTx(tx)
	for _, e := range events {
		data, err := proto.Marshal(e)
		if err != nil {
			return fmt.Errorf("runnerstore: marshal event %d: %w", e.GetId(), err)
		}
		if err := q.InsertEvent(ctx, sqlc.InsertEventParams{
			ID:     e.GetId(),
			TaskID: e.GetTaskId(),
			Type:   eventType(e),
			Event:  data,
		}); err != nil {
			return fmt.Errorf("runnerstore: insert event %d: %w", e.GetId(), err)
		}
	}
	if err := q.SetCursor(ctx, cursor); err != nil {
		return fmt.Errorf("runnerstore: set cursor: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("runnerstore: commit: %w", err)
	}
	return nil
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

// Cursor returns the stored replication cursor, or "" when none is stored.
func (s *Store) Cursor(ctx context.Context) (string, error) {
	cursor, err := s.q.GetCursor(ctx)
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
		var err error
		after, err = s.q.GetEventSeq(ctx, afterID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("runnerstore: event %d is not stored", afterID)
		}
		if err != nil {
			return nil, fmt.Errorf("runnerstore: resolve event %d: %w", afterID, err)
		}
	}
	rows, err := s.q.ListTaskEventsAfter(ctx, sqlc.ListTaskEventsAfterParams{
		TaskID: taskID,
		Seq:    after,
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
