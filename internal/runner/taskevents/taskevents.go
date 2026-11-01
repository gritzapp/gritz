// Package taskevents replicates the runner's event log from the server into
// its runnerstore and exposes each task's events as a stream. See
// proposals/draft/runner-event-replica.md.
package taskevents

import (
	"cmp"
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
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

	wake  chan struct{}
	tasks chan runnerstore.TaskChange

	mu sync.Mutex
	// stored holds, per task with a blocked Read, a channel closed when the
	// next page with an event for that task is stored.
	stored map[int64]chan struct{}
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
		wake:     make(chan struct{}, 1),
		tasks:    make(chan runnerstore.TaskChange),
		stored:   map[int64]chan struct{}{},
	}
}

// Run reports every stored unarchived task on Tasks(), then replicates until
// ctx ends: it syncs with the server, then waits for a Wake or the poll
// interval, and repeats. A failed fetch is logged and retried at the next
// sync; a store error ends Run.
func (l *Log) Run(ctx context.Context) error {
	tasks, err := l.store.UnarchivedTasks(ctx)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if err := l.report(ctx, t); err != nil {
			return err
		}
	}
	for {
		if err := l.sync(ctx); err != nil {
			return err
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

// sync stores every page the server has after the stored cursor. It returns
// only store and ctx errors; a failed fetch is logged and ends the sync.
func (l *Log) sync(ctx context.Context) error {
	cursor, err := l.store.EventReplicationCursor(ctx)
	if err != nil {
		return err
	}
	req := &gritzv1.ListRunnerEventsRequest{Runner: l.runnerID, PageToken: cursor}
	for resp, err := range gritzclient.ListRunnerEvents(ctx, l.client, req) {
		if err != nil {
			if ctx.Err() == nil {
				l.log.Warn("taskevents: fetch runner events", "err", err)
			}
			return nil
		}
		if len(resp.GetEvents()) == 0 {
			continue
		}
		changes, err := l.store.AppendEvents(ctx, resp.GetEvents(), resp.GetNextPageToken())
		if err != nil {
			return err
		}
		l.notify(resp.GetEvents())
		for _, c := range changes {
			if err := l.report(ctx, c); err != nil {
				return err
			}
		}
	}
	return nil
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

// notify wakes the blocked Reads of the tasks with an event in events.
func (l *Log) notify(events []*gritzv1.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range events {
		if ch, ok := l.stored[e.GetTaskId()]; ok {
			close(ch)
			delete(l.stored, e.GetTaskId())
		}
	}
}

// storedSignal returns a channel closed when the next page with an event for
// the task is stored.
func (l *Log) storedSignal(taskID int64) <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	ch, ok := l.stored[taskID]
	if !ok {
		ch = make(chan struct{})
		l.stored[taskID] = ch
	}
	return ch
}

// Wake makes Run check the server for new events now, or as soon as its
// current sync ends.
func (l *Log) Wake() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// Tasks reports the full state of tasks: every unarchived stored task when Run
// starts, then each task with a command or archive event in a stored page,
// after that page. A report may repeat a task's current state, so the
// consumer must apply them idempotently. Run blocks until each report is
// received.
func (l *Log) Tasks() <-chan runnerstore.TaskChange {
	return l.tasks
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

// Read returns the task's events after the event with id after (0 for the
// start), blocking until there is at least one or ctx ends.
func (s *Stream) Read(ctx context.Context, after int64) ([]*gritzv1.Event, error) {
	for {
		// Take the signal before reading, so a page stored between the read
		// and the wait still wakes this Read.
		stored := s.log.storedSignal(s.taskID)
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
