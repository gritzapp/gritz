// Package archiver runs a periodic background loop that archives terminal
// tasks whose auto_archive deadline has elapsed.
//
// The worker is a single goroutine; each tick it asks the store for a small
// batch of due tasks and archives them through the same transactional path
// the manual ArchiveTask handler uses, so the same "change" notification fires
// and the runner's existing Prune() loop reaps the container on its next tick.
package archiver

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/server/pubsub"
	"github.com/gritzapp/gritz/internal/store"
)

// DefaultInterval is how often the archiver tick fires when no override is set.
const DefaultInterval = time.Minute

// DefaultBatchSize is the maximum number of tasks archived per tick.
const DefaultBatchSize = 100

// Archiver periodically archives terminal tasks past their auto_archive deadline.
type Archiver struct {
	store     *store.Store
	publisher pubsub.Publisher
	interval  time.Duration
	batchSize int
	log       *slog.Logger
}

// Options configures the Archiver.
type Options struct {
	Store     *store.Store
	Publisher pubsub.Publisher
	Interval  time.Duration
	BatchSize int
	Log       *slog.Logger
}

// New returns a new Archiver. Store is required; Publisher is optional (no
// SSE notifications are emitted if nil). Interval and BatchSize fall back to
// DefaultInterval / DefaultBatchSize when unset.
func New(opts Options) *Archiver {
	return &Archiver{
		store:     opts.Store,
		publisher: opts.Publisher,
		interval:  cmp.Or(opts.Interval, DefaultInterval),
		batchSize: cmp.Or(opts.BatchSize, DefaultBatchSize),
		log:       cmp.Or(opts.Log, slog.Default()),
	}
}

// Run blocks until ctx is cancelled, calling Tick at the configured interval.
func (a *Archiver) Run(ctx context.Context) error {
	t := time.NewTicker(a.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := a.Tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
				a.log.Error("archiver tick failed", "err", err)
			}
		}
	}
}

// Tick performs one batch of archive work. Exported so tests (and ad-hoc
// callers) can drive the loop deterministically.
func (a *Archiver) Tick(ctx context.Context) error {
	due, err := a.store.ListTasksDueForArchive(ctx, nil, a.batchSize)
	if err != nil {
		return fmt.Errorf("list tasks due for archive: %w", err)
	}
	if len(due) == 0 {
		return nil
	}
	var archived int
	for _, d := range due {
		if err := ctx.Err(); err != nil {
			return err
		}
		ok, err := a.archive(ctx, d)
		if err != nil {
			a.log.Warn("archiver failed to archive task", "id", d.ID, "err", err)
			continue
		}
		if ok {
			archived++
		}
	}
	if archived > 0 {
		a.log.Info("archiver archived tasks", "count", archived)
	}
	return nil
}

// archive runs the same transactional archive flow as ArchiveTask. The version
// is read from the candidate list; if it no longer matches (because the task
// was restarted or already archived between the list and the update), the
// archive is skipped silently.
func (a *Archiver) archive(ctx context.Context, due store.TaskDueForArchive) (bool, error) {
	err := a.store.WithTx(ctx, nil, func(tx *sql.Tx) error {
		t, err := a.store.GetTaskForUpdate(ctx, tx, due.ID, due.OrgID)
		if err != nil {
			return err
		}
		if t.Archived || t.Version != due.Version {
			return errSkip
		}
		from := t.Status
		if !t.Archive() {
			return errSkip
		}
		if err := a.store.UpdateTask(ctx, tx, t); err != nil {
			return err
		}
		// The auto-archive is a runner-side (server worker) action, not a user
		// one, so the lifecycle event's actor is the runner.
		if err := a.store.CreateEvent(ctx, tx, &model.Event{
			TaskID: t.ID,
			OrgID:  t.OrgID,
			Payload: &model.LifecyclePayload{
				Kind:       model.LifecycleKindAutoArchived,
				Actor:      model.RunnerActor,
				FromStatus: from.Label(),
				ToStatus:   t.Status.Label(),
			},
		}); err != nil {
			return err
		}
		return tx.Commit()
	})
	if errors.Is(err, errSkip) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if a.publisher != nil {
		if err := a.publisher.Publish(ctx, model.Notification{
			Type: "change",
			Resources: []model.NotificationResource{
				{Action: "archived", Type: "task", ID: due.ID},
				{Action: "appended", Type: "task_events", ID: due.ID},
			},
			OrgID: due.OrgID,
			Time:  time.Now(),
		}); err != nil {
			a.log.Warn("failed to publish archive notification", "id", due.ID, "err", err)
		}
	}
	return true, nil
}

var errSkip = errors.New("archiver: skip task")
