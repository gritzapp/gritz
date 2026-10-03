// Package scheduler runs a periodic background loop that fires recurring
// schedules: each tick it claims the schedules whose next_run_at has passed and,
// for each, creates a normal task through the exact same store path CreateTask
// uses, then advances next_run_at to the following occurrence.
//
// The worker is structurally the archiver (internal/server/archiver): a single
// Run(ctx) goroutine ticking on a time.Ticker, each tick processing a bounded
// batch. The one difference that matters is the claim: schedules are claimed with
// FOR UPDATE SKIP LOCKED and both the fire and the advance commit in the same
// transaction, so a schedule fires exactly once even with several schedulers
// ticking at the same instant (a duplicated fire would be a duplicated real task
// run, not an idempotent no-op). See proposals/accepted/scheduled-tasks.md.
package scheduler

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/icholy/gritz/internal/model"
	"github.com/icholy/gritz/internal/server/pubsub"
	"github.com/icholy/gritz/internal/store"
)

//go:generate go tool moq -out store_moq_test.go . Store

// Store is the subset of *store.Store the scheduler uses. The signatures match
// the real methods exactly, so *store.Store satisfies it unchanged; tests drive a
// generated StoreMock instead of a live database.
type Store interface {
	WithTx(ctx context.Context, tx *sql.Tx, f func(tx *sql.Tx) error) error
	ClaimDueSchedules(ctx context.Context, tx *sql.Tx, limit int) ([]*model.Schedule, error)
	CreateTask(ctx context.Context, tx *sql.Tx, task *model.Task) error
	CreateEvent(ctx context.Context, tx *sql.Tx, event *model.Event) error
	AdvanceSchedule(ctx context.Context, tx *sql.Tx, id int64, orgID int64, adv store.ScheduleAdvance) error
	UpdateSchedule(ctx context.Context, tx *sql.Tx, sched *model.Schedule) error
}

// DefaultInterval is how often the scheduler tick fires when no override is set.
// A 10s tick bounds firing latency to well under the minute-resolution cron grid
// while keeping DB load negligible (the partial index makes each scan
// index-only).
const DefaultInterval = 10 * time.Second

// DefaultBatchSize is the maximum number of distinct schedules fired per tick.
const DefaultBatchSize = 100

// Scheduler periodically fires due schedules into real task runs.
type Scheduler struct {
	store     Store
	publisher pubsub.Publisher
	interval  time.Duration
	batchSize int
	log       *slog.Logger
}

// Options configures the Scheduler.
type Options struct {
	Store     Store
	Publisher pubsub.Publisher
	Interval  time.Duration
	BatchSize int
	Log       *slog.Logger
}

// New returns a new Scheduler. Store is required; Publisher is optional (no SSE
// notifications are emitted if nil). Interval and BatchSize fall back to
// DefaultInterval / DefaultBatchSize when unset.
func New(opts Options) *Scheduler {
	return &Scheduler{
		store:     opts.Store,
		publisher: opts.Publisher,
		interval:  cmp.Or(opts.Interval, DefaultInterval),
		batchSize: cmp.Or(opts.BatchSize, DefaultBatchSize),
		log:       cmp.Or(opts.Log, slog.Default()),
	}
}

// Run blocks until ctx is cancelled, calling Tick at the configured interval.
func (s *Scheduler) Run(ctx context.Context) error {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := s.Tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
				s.log.Error("scheduler tick failed", "err", err)
			}
		}
	}
}

// Tick fires one bounded batch of due schedules. Claiming and firing happen in a
// single transaction: ClaimDueSchedules holds a FOR UPDATE SKIP LOCKED lock on
// each claimed row for the transaction's lifetime, and each fire advances the
// row's next_run_at past now before commit, so on commit the locks release and
// the rows are no longer due. A crash (or error) before commit rolls the whole
// tick back and the rows stay due for the next tick — at-least-once claim, made
// exactly-once by pairing the advance with the fire. Exported so tests (and
// ad-hoc callers) can drive the loop deterministically.
func (s *Scheduler) Tick(ctx context.Context) error {
	var notifications []model.Notification
	err := s.store.WithTx(ctx, nil, func(tx *sql.Tx) error {
		due, err := s.store.ClaimDueSchedules(ctx, tx, s.batchSize)
		if err != nil {
			return fmt.Errorf("claim due schedules: %w", err)
		}
		for _, sched := range due {
			n, err := s.fire(ctx, tx, sched)
			if err != nil {
				return fmt.Errorf("fire schedule %d: %w", sched.ID, err)
			}
			notifications = append(notifications, n)
		}
		// Commit the batch: every fire's task creation and schedule advance land
		// together. tx is nil only under a mocked store in tests (there is no real
		// transaction to commit); the real WithTx always supplies one.
		if tx == nil {
			return nil
		}
		return tx.Commit()
	})
	if err != nil {
		return err
	}
	// Publish only after the fire is durable, so a subscriber that reacts (the Web
	// UI, the runner wake channel) never observes a task the tick later rolled back.
	for i := range notifications {
		s.publish(ctx, notifications[i])
	}
	if len(notifications) > 0 {
		s.log.Info("scheduler fired schedules", "count", len(notifications))
	}
	return nil
}

// Fire materializes one schedule occurrence inside tx: it creates the template
// task and seeds its events exactly as CreateTask does (Pending/Start, a created
// event attributed to model.ScheduleActor, one instruction event per template
// instruction), and returns the created task plus the change notification to
// publish once tx commits. It writes nothing to the schedule row — the caller
// decides what, if anything, happens to the cadence: the worker's Scheduler.fire
// advances next_run_at via AdvanceSchedule, while a one-off manual run
// (RunSchedule) records nothing on the schedule at all. Sharing this function is
// what makes a manual run and a scheduled run produce a byte-for-byte identical
// task.
func Fire(ctx context.Context, tx *sql.Tx, st Store, sched *model.Schedule) (*model.Task, model.Notification, error) {
	// The schedule owns the template -> task/events mapping. Insert the task, then
	// seed its events (Events needs the assigned task.ID), so a scheduled task is
	// indistinguishable from a hand-created one downstream.
	task := sched.Task()
	if err := st.CreateTask(ctx, tx, task); err != nil {
		return nil, model.Notification{}, err
	}
	for _, ev := range sched.Events(task) {
		if err := st.CreateEvent(ctx, tx, ev); err != nil {
			return nil, model.Notification{}, err
		}
	}
	// The same "change" notification the manual create path emits: task created +
	// task_events appended, with the runner set so its wake channel picks the new
	// task up immediately. There is no user, so UserID / ClientID are left empty.
	return task, model.Notification{
		Type: "change",
		Resources: []model.NotificationResource{
			{Action: "created", Type: "task", ID: task.ID},
			{Action: "appended", Type: "task_events", ID: task.ID},
		},
		OrgID:          task.OrgID,
		Runner:         task.PendingRunner(),
		Time:           time.Now(),
		ChannelMessage: fmt.Sprintf("Task %d created on %s/%s.", task.ID, task.Runner, task.Workspace),
	}, nil
}

// fire materializes one schedule occurrence via Fire, then advances the schedule
// to its next occurrence. The next fire is computed as the first occurrence
// strictly after now, never after the stored next_run_at, so any occurrences
// missed while the scheduler was down collapse into this single fire (skip-only,
// never backfill). It returns the change notification to publish once the tick
// commits.
func (s *Scheduler) fire(ctx context.Context, tx *sql.Tx, sched *model.Schedule) (model.Notification, error) {
	now := time.Now()
	task, note, err := Fire(ctx, tx, s.store, sched)
	if err != nil {
		return model.Notification{}, err
	}

	// The task is fired either way; only the schedule write differs.
	if next, err := sched.Next(now); err != nil {
		// The spec was valid when written, so this only happens if the tz database
		// dropped the schedule's timezone under us. Rather than let the row wedge as
		// permanently due, disable it and clear next_run_at so the claim query skips
		// it. The due occurrence still fired above — creating its task never needed
		// Next(); only advancing did.
		s.log.Error("scheduler disabling schedule with unresolvable next occurrence", "id", sched.ID, "err", err)
		sched.Enabled = false
		sched.NextRunAt = nil
		sched.LastRunAt = new(now.UTC())
		sched.LastTaskID = &task.ID
		if err := s.store.UpdateSchedule(ctx, tx, sched); err != nil {
			return model.Notification{}, err
		}
	} else {
		if err := s.store.AdvanceSchedule(ctx, tx, sched.ID, sched.OrgID, store.ScheduleAdvance{
			NextRunAt:  &next,
			LastRunAt:  new(now.UTC()),
			LastTaskID: &task.ID,
		}); err != nil {
			return model.Notification{}, err
		}
	}

	return note, nil
}

func (s *Scheduler) publish(ctx context.Context, n model.Notification) {
	if s.publisher == nil {
		return
	}
	if err := s.publisher.Publish(ctx, n); err != nil {
		s.log.Warn("failed to publish schedule fire notification", "err", err)
	}
}
