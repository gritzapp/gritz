package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store/sqlc"
)

func (s *Store) CreateSchedule(ctx context.Context, tx *sql.Tx, sched *model.Schedule) error {
	id, err := s.q(tx).CreateSchedule(ctx, sqlc.CreateScheduleParams{
		OrgID:      sched.OrgID,
		Name:       sched.Name,
		AgentID:    sched.AgentID,
		CronExpr:   sched.CronExpr,
		Timezone:   sched.Timezone,
		Enabled:    sched.Enabled,
		NextRunAt:  nullTime(sched.NextRunAt),
		LastRunAt:  nullTime(sched.LastRunAt),
		LastTaskID: nullInt64(sched.LastTaskID),
	})
	if err != nil {
		return err
	}
	sched.ID = id
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
	return toModelSchedule(row), nil
}

func (s *Store) GetScheduleForUpdate(ctx context.Context, tx *sql.Tx, id int64, orgID int64) (*model.Schedule, error) {
	row, err := s.q(tx).GetScheduleForUpdate(ctx, sqlc.GetScheduleForUpdateParams{
		ID:    id,
		OrgID: orgID,
	})
	if err != nil {
		return nil, err
	}
	return toModelSchedule(row), nil
}

func (s *Store) ListSchedules(ctx context.Context, tx *sql.Tx, orgID int64) ([]*model.Schedule, error) {
	rows, err := s.q(tx).ListSchedules(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return toModelSchedules(rows), nil
}

// UpdateSchedule writes sched; moved to another agent, it goes last in it.
func (s *Store) UpdateSchedule(ctx context.Context, tx *sql.Tx, sched *model.Schedule) error {
	return s.q(tx).UpdateSchedule(ctx, sqlc.UpdateScheduleParams{
		Name:       sched.Name,
		AgentID:    sched.AgentID,
		CronExpr:   sched.CronExpr,
		Timezone:   sched.Timezone,
		Enabled:    sched.Enabled,
		NextRunAt:  nullTime(sched.NextRunAt),
		LastRunAt:  nullTime(sched.LastRunAt),
		LastTaskID: nullInt64(sched.LastTaskID),
		ID:         sched.ID,
		OrgID:      sched.OrgID,
	})
}

func (s *Store) DeleteSchedule(ctx context.Context, tx *sql.Tx, id int64, orgID int64) error {
	return s.q(tx).DeleteSchedule(ctx, sqlc.DeleteScheduleParams{
		ID:    id,
		OrgID: orgID,
	})
}

// DisableSchedules disables all of the org's schedules.
func (s *Store) DisableSchedules(ctx context.Context, tx *sql.Tx, orgID int64) error {
	return s.q(tx).DisableSchedules(ctx, orgID)
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
	return toModelSchedules(rows), nil
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
// last_run_at/last_task_id to the run just created. Called in
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

func toModelSchedule(row sqlc.Schedule) *model.Schedule {
	return &model.Schedule{
		ID:         row.ID,
		OrgID:      row.OrgID,
		Name:       row.Name,
		AgentID:    row.AgentID,
		CronExpr:   row.CronExpr,
		Timezone:   row.Timezone,
		Enabled:    row.Enabled,
		NextRunAt:  timePtr(row.NextRunAt),
		LastRunAt:  timePtr(row.LastRunAt),
		LastTaskID: int64Ptr(row.LastTaskID),
	}
}

func toModelSchedules(rows []sqlc.Schedule) []*model.Schedule {
	schedules := make([]*model.Schedule, len(rows))
	for i, row := range rows {
		schedules[i] = toModelSchedule(row)
	}
	return schedules
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
