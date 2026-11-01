package runnerstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/runnerstore/sqlc"
)

// SandboxRecord is the persisted task→sandbox-handle mapping.
type SandboxRecord struct {
	TaskID int64
	// Version is the task.Version of the run the sandbox is hosting. It is
	// expected to go once the driver owns its runs (runner-event-replica.md
	// slices 7–8).
	Version int64
	// Handle is stored as the type, id and data columns. Its Data is never
	// decoded.
	Handle backend.Handle
}

// WriteSandbox inserts or replaces the record for rec.TaskID.
func (s *Store) WriteSandbox(ctx context.Context, rec SandboxRecord) error {
	if err := s.q.UpsertSandbox(ctx, sqlc.UpsertSandboxParams{
		TaskID:  rec.TaskID,
		Version: rec.Version,
		Type:    rec.Handle.Type,
		ID:      rec.Handle.ID,
		Data:    rec.Handle.Data,
	}); err != nil {
		return fmt.Errorf("runnerstore: write sandbox %d: %w", rec.TaskID, err)
	}
	return nil
}

// Sandbox returns the record for taskID; ok is false when there is none.
func (s *Store) Sandbox(ctx context.Context, taskID int64) (rec SandboxRecord, ok bool, err error) {
	row, err := s.q.GetSandbox(ctx, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return SandboxRecord{}, false, nil
	}
	if err != nil {
		return SandboxRecord{}, false, fmt.Errorf("runnerstore: read sandbox %d: %w", taskID, err)
	}
	return sandboxRecord(row), true, nil
}

// RemoveSandbox deletes the record for taskID, if any.
func (s *Store) RemoveSandbox(ctx context.Context, taskID int64) error {
	if err := s.q.DeleteSandbox(ctx, taskID); err != nil {
		return fmt.Errorf("runnerstore: remove sandbox %d: %w", taskID, err)
	}
	return nil
}

// Sandboxes returns every record, in ascending task id order.
func (s *Store) Sandboxes(ctx context.Context) ([]SandboxRecord, error) {
	rows, err := s.q.ListSandboxes(ctx)
	if err != nil {
		return nil, fmt.Errorf("runnerstore: read sandboxes: %w", err)
	}
	var recs []SandboxRecord
	for _, row := range rows {
		recs = append(recs, sandboxRecord(row))
	}
	return recs, nil
}

// sandboxRecord returns the record a sandboxes row holds. A handle stored
// without data reads back with nil Data, as it was written.
func sandboxRecord(row sqlc.Sandbox) SandboxRecord {
	h := backend.Handle{Type: row.Type, ID: row.ID}
	if len(row.Data) > 0 {
		h.Data = row.Data
	}
	return SandboxRecord{TaskID: row.TaskID, Version: row.Version, Handle: h}
}
