package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store/sqlc"
)

func (s *Store) CreateSchedule(ctx context.Context, tx *sql.Tx, sched *model.Schedule) error {
	now := time.Now().UTC()
	instructions, err := marshalInstructions(sched.Instructions)
	if err != nil {
		return err
	}
	id, err := s.q(tx).CreateSchedule(ctx, sqlc.CreateScheduleParams{
		OrgID:        sched.OrgID,
		CreatedBy:    sched.CreatedBy,
		Name:         sched.Name,
		Workspace:    sched.Workspace,
		Runner:       sched.Runner,
		Namespace:    sched.Namespace,
		Instructions: instructions,
		AutoArchive:  sched.AutoArchive.Microseconds(),
		CronExpr:     sched.CronExpr,
		Timezone:     sched.Timezone,
		Enabled:      sched.Enabled,
		NextRunAt:    nullTime(sched.NextRunAt),
		LastRunAt:    nullTime(sched.LastRunAt),
		LastTaskID:   nullInt64(sched.LastTaskID),
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	if err != nil {
		return err
	}
	sched.ID = id
	sched.CreatedAt = now
	sched.UpdatedAt = now
	return nil
}

func (s *Store) GetSchedule(ctx context.Context, tx *sql.Tx, id int64, orgID int64) (*model.Schedule, error) {
	row, err := s.q(tx).GetSchedule(ctx, sqlc.GetScheduleParams{
		ID:    id,
		OrgID: orgID,
	})
	if err != nil {
		return nil, err
	}
	return toModelSchedule(row)
}

func (s *Store) GetScheduleForUpdate(ctx context.Context, tx *sql.Tx, id int64, orgID int64) (*model.Schedule, error) {
	row, err := s.q(tx).GetScheduleForUpdate(ctx, sqlc.GetScheduleForUpdateParams{
		ID:    id,
		OrgID: orgID,
	})
	if err != nil {
		return nil, err
	}
	return toModelSchedule(row)
}

func (s *Store) ListSchedules(ctx context.Context, tx *sql.Tx, orgID int64) ([]*model.Schedule, error) {
	rows, err := s.q(tx).ListSchedules(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return toModelSchedules(rows)
}

func (s *Store) UpdateSchedule(ctx context.Context, tx *sql.Tx, sched *model.Schedule) error {
	sched.UpdatedAt = time.Now().UTC()
	instructions, err := marshalInstructions(sched.Instructions)
	if err != nil {
		return err
	}
	return s.q(tx).UpdateSchedule(ctx, sqlc.UpdateScheduleParams{
		Name:         sched.Name,
		Workspace:    sched.Workspace,
		Runner:       sched.Runner,
		Namespace:    sched.Namespace,
		Instructions: instructions,
		AutoArchive:  sched.AutoArchive.Microseconds(),
		CronExpr:     sched.CronExpr,
		Timezone:     sched.Timezone,
		Enabled:      sched.Enabled,
		NextRunAt:    nullTime(sched.NextRunAt),
		LastRunAt:    nullTime(sched.LastRunAt),
		LastTaskID:   nullInt64(sched.LastTaskID),
		UpdatedAt:    sched.UpdatedAt,
		ID:           sched.ID,
		OrgID:        sched.OrgID,
	})
}

func (s *Store) DeleteSchedule(ctx context.Context, tx *sql.Tx, id int64, orgID int64) error {
	return s.q(tx).DeleteSchedule(ctx, sqlc.DeleteScheduleParams{
		ID:    id,
		OrgID: orgID,
	})
}

// ClaimDueSchedules locks and returns up to limit due, enabled schedules,
// skipping rows another server instance already holds (FOR UPDATE SKIP LOCKED).
// The locks are held until tx commits, so a caller must pass a real transaction:
// two schedulers ticking at once partition the due set instead of both firing the
// same schedule.
func (s *Store) ClaimDueSchedules(ctx context.Context, tx *sql.Tx, limit int) ([]*model.Schedule, error) {
	rows, err := s.q(tx).ClaimDueSchedules(ctx, int32(limit))
	if err != nil {
		return nil, err
	}
	return toModelSchedules(rows)
}

// ScheduleAdvance carries the bookkeeping a fire writes back to a schedule: the
// recomputed next occurrence, the instant it just fired, and the task that fire
// created. Grouping the two *time.Time and the *int64 into named fields keeps
// callers from transposing them.
type ScheduleAdvance struct {
	NextRunAt  *time.Time
	LastRunAt  *time.Time
	LastTaskID *int64
}

// AdvanceSchedule records a fire: it sets next_run_at to the next occurrence,
// last_run_at/last_task_id to the run just created, and bumps version. Called in
// the same transaction as the fire so the advance and the task creation commit
// together (exactly-once).
func (s *Store) AdvanceSchedule(ctx context.Context, tx *sql.Tx, id int64, orgID int64, adv ScheduleAdvance) error {
	return s.q(tx).AdvanceSchedule(ctx, sqlc.AdvanceScheduleParams{
		NextRunAt:  nullTime(adv.NextRunAt),
		LastRunAt:  nullTime(adv.LastRunAt),
		LastTaskID: nullInt64(adv.LastTaskID),
		ID:         id,
		OrgID:      orgID,
	})
}

// marshalInstructions encodes the template instructions as the [{text, url}]
// JSONB stored in schedules.instructions. A nil slice is stored as an empty JSON
// array so the column never holds SQL NULL.
func marshalInstructions(instructions []model.ScheduleInstruction) (json.RawMessage, error) {
	if instructions == nil {
		instructions = []model.ScheduleInstruction{}
	}
	data, err := json.Marshal(instructions)
	if err != nil {
		return nil, fmt.Errorf("marshal schedule instructions: %w", err)
	}
	return data, nil
}

func toModelSchedule(row sqlc.Schedule) (*model.Schedule, error) {
	var instructions []model.ScheduleInstruction
	if err := json.Unmarshal(row.Instructions, &instructions); err != nil {
		return nil, fmt.Errorf("unmarshal schedule instructions: %w", err)
	}
	return &model.Schedule{
		ID:           row.ID,
		OrgID:        row.OrgID,
		CreatedBy:    row.CreatedBy,
		Name:         row.Name,
		Workspace:    row.Workspace,
		Runner:       row.Runner,
		Namespace:    row.Namespace,
		Instructions: instructions,
		AutoArchive:  time.Duration(row.AutoArchive) * time.Microsecond,
		CronExpr:     row.CronExpr,
		Timezone:     row.Timezone,
		Enabled:      row.Enabled,
		NextRunAt:    timePtr(row.NextRunAt),
		LastRunAt:    timePtr(row.LastRunAt),
		LastTaskID:   int64Ptr(row.LastTaskID),
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}, nil
}

func toModelSchedules(rows []sqlc.Schedule) ([]*model.Schedule, error) {
	schedules := make([]*model.Schedule, len(rows))
	for i, row := range rows {
		sched, err := toModelSchedule(row)
		if err != nil {
			return nil, err
		}
		schedules[i] = sched
	}
	return schedules, nil
}

// nullTime maps an optional UTC instant to sql.NullTime.
func nullTime(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *t, Valid: true}
}

// timePtr maps a nullable timestamp column back to an optional instant.
func timePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

// nullInt64 maps an optional id to sql.NullInt64.
func nullInt64(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}

// int64Ptr maps a nullable bigint column back to an optional id.
func int64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}
