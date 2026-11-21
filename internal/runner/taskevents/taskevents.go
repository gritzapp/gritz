// Package taskevents replicates the runner's event log from the server into
// its runnerstore and exposes each task's events as a stream. See
// docs/src/content/docs/proposals/implemented/runner-event-replica.md.
package taskevents

import (
	"cmp"
	"context"
	"log/slog"
	"time"

	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/x/wakeup"
)

// DefaultPollInterval is how long Run waits for a Wake before it checks the
// server anyway.
const DefaultPollInterval = 30 * time.Second

// Options configures a Log.
type Options struct {
	// Store holds the replica and its cursor.
	Store *runnerstore.Store
	// Client fetches the runner's events with ListRunnerEvents.
	Client gritzclient.Client
	// RunnerID is the runner whose tasks' events are replicated.
	RunnerID string
	// PollInterval is how long Run waits for a Wake before it checks the
	// server anyway; 0 means DefaultPollInterval.
	PollInterval time.Duration
	// Log receives replication diagnostics.
	Log *slog.Logger
}

// Log is the runner's replicated copy of its tasks' event logs.
type Log struct {
	store    *runnerstore.Store
	client   gritzclient.Client
	runnerID string
	interval time.Duration
	log      *slog.Logger

	wake  wakeup.Chan
	tasks chan runnerstore.TaskChange
	// synced is closed once Run's first sync has completed.
	synced chan struct{}

	// stored wakes every blocked Read when a page is stored.
	stored wakeup.Broadcast
}

// New returns a Log that replicates the runner's tasks from the server into
// opts.Store once Run is called.
func New(opts Options) *Log {
	return &Log{
		store:    opts.Store,
		client:   opts.Client,
		runnerID: opts.RunnerID,
		interval: cmp.Or(opts.PollInterval, DefaultPollInterval),
		log:      cmp.Or(opts.Log, slog.Default()),
		wake:     wakeup.New(),
		tasks:    make(chan runnerstore.TaskChange),
		synced:   make(chan struct{}),
	}
}

// Run replicates until ctx ends: it syncs with the server, then waits for a
// Wake or the poll interval, and repeats. A failed fetch is logged and
// retried at the next sync; a store error ends Run. Run closes Synced once
// a sync has fetched every page the server had.
func (l *Log) Run(ctx context.Context) error {
	synced := false
	for {
		done, err := l.sync(ctx)
		if err != nil {
			return err
		}
		if done && !synced {
			close(l.synced)
			synced = true
		}
		// A page with more == false means nothing is readable right now, not
		// that the replica is caught up: the server's xid watermark can hold
		// committed events back and later release them without anything
		// calling Wake. The poll interval is what picks those up.
		select {
		case <-l.wake:
		case <-time.After(l.interval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// sync stores every page the server has after the stored cursor, and
// reports whether it got to the last one. It returns only store and ctx
// errors; a failed fetch is logged and ends the sync.
func (l *Log) sync(ctx context.Context) (done bool, err error) {
	cursor, err := l.store.EventReplicationCursor(ctx)
	if err != nil {
		return false, err
	}
	req := &gritzv1.ListRunnerEventsRequest{Runner: l.runnerID, PageToken: cursor}
	for resp, err := range gritzclient.ListRunnerEvents(ctx, l.client, req) {
		if err != nil {
			if ctx.Err() == nil {
				l.log.Warn("taskevents: fetch runner events", "err", err)
			}
			return false, nil
		}
		if len(resp.GetEvents()) == 0 {
			continue
		}
		changes, err := l.store.AppendEvents(ctx, resp.GetEvents(), resp.GetNextPageToken())
		if err != nil {
			return false, err
		}
		l.stored.Wake()
		for _, c := range changes {
			if err := l.report(ctx, c); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

// report sends c on Tasks(), blocking until it is received or ctx ends.
func (l *Log) report(ctx context.Context, c runnerstore.TaskChange) error {
	select {
	case l.tasks <- c:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wake makes Run check the server for new events now, or as soon as its
// current sync ends.
func (l *Log) Wake() {
	l.wake.Wake()
}

// Tasks reports the full state of each task with a command or archive event
// in a page Run stores, after that page. Tasks already stored when Run starts
// are not reported. A report may repeat a task's current state, so the
// consumer must apply them idempotently. Run blocks until each report is
// received.
func (l *Log) Tasks() <-chan runnerstore.TaskChange {
	return l.tasks
}

// Synced is closed once Run's first sync has completed: every page the
// server had when it was fetched is stored, and every report from it
// received from Tasks(). A failed fetch doesn't complete a sync, so while the
// server is unreachable Synced stays open.
func (l *Log) Synced() <-chan struct{} {
	return l.synced
}

// Stream returns the task's event stream.
func (l *Log) Stream(taskID int64) *Stream {
	return &Stream{log: l, taskID: taskID}
}

// Stream is one task's events, in log order, with no gaps. Read is stateless:
// any number of readers can follow a stream from their own positions.
type Stream struct {
	log    *Log
	taskID int64
}

// Stored returns the events after the event with id after (0 for the start)
// that are stored now, which may be none, without blocking.
func (s *Stream) Stored(ctx context.Context, after int64) ([]*gritzv1.Event, error) {
	return s.log.store.TaskEventsAfter(ctx, s.taskID, after)
}

// Read returns the task's events after the event with id after (0 for the
// start), blocking until there is at least one or ctx ends.
func (s *Stream) Read(ctx context.Context, after int64) ([]*gritzv1.Event, error) {
	for {
		// Take the signal before reading, so a page stored between the read
		// and the wait still wakes this Read. Any stored page wakes every
		// blocked Read, not only those of tasks with an event in it; each
		// re-runs its query and blocks again if it has nothing new.
		stored := s.log.stored.Wait()
		events, err := s.log.store.TaskEventsAfter(ctx, s.taskID, after)
		if err != nil {
			return nil, err
		}
		if len(events) > 0 {
			return events, nil
		}
		select {
		case <-stored:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
