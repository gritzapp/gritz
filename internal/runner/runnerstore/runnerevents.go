package runnerstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/runner/runnerstore/sqlc"
)

// AddRunnerEvent appends ev to the runner event queue. ev.Reconcile is not
// stored: the runner never sets it.
func (s *Store) AddRunnerEvent(ctx context.Context, ev model.RunnerEvent) error {
	if err := s.q.InsertRunnerEvent(ctx, sqlc.InsertRunnerEventParams{
		TaskID:  ev.TaskID,
		Event:   string(ev.Event),
		Version: ev.Version,
		Reason:  ev.Reason,
	}); err != nil {
		return fmt.Errorf("runnerstore: add runner event for task %d: %w", ev.TaskID, err)
	}
	return nil
}

// OldestRunnerEvent returns the oldest pending event and its seq; ok is false
// when none is pending.
func (s *Store) OldestRunnerEvent(ctx context.Context) (seq int64, ev model.RunnerEvent, ok bool, err error) {
	row, err := s.q.GetOldestRunnerEvent(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, model.RunnerEvent{}, false, nil
	}
	if err != nil {
		return 0, model.RunnerEvent{}, false, fmt.Errorf("runnerstore: read oldest runner event: %w", err)
	}
	return row.Seq, model.RunnerEvent{
		TaskID:  row.TaskID,
		Event:   model.RunnerEventType(row.Event),
		Version: row.Version,
		Reason:  row.Reason,
	}, true, nil
}

// DeleteRunnerEvent deletes the event at seq.
func (s *Store) DeleteRunnerEvent(ctx context.Context, seq int64) error {
	if err := s.q.DeleteRunnerEvent(ctx, seq); err != nil {
		return fmt.Errorf("runnerstore: delete runner event %d: %w", seq, err)
	}
	return nil
}

// MarkRunnerEventDead dead-letters the event at seq: it stays in the table but
// is no longer pending.
func (s *Store) MarkRunnerEventDead(ctx context.Context, seq int64) error {
	if err := s.q.MarkRunnerEventDead(ctx, seq); err != nil {
		return fmt.Errorf("runnerstore: mark runner event %d dead: %w", seq, err)
	}
	return nil
}

// CountRunnerEvents returns the number of pending events.
func (s *Store) CountRunnerEvents(ctx context.Context) (int, error) {
	n, err := s.q.CountRunnerEvents(ctx)
	if err != nil {
		return 0, fmt.Errorf("runnerstore: count runner events: %w", err)
	}
	return int(n), nil
}
