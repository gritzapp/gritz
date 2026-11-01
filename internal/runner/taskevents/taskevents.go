// Package taskevents replicates the runner's event log from the server into
// its runnerstore and exposes each task's events as a stream and its latest
// state as a watch. See
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

	// stored wakes every blocked Read when a page is stored.
	stored wakeup.Broadcast

	// mu guards watches, each task's latest state; Task adds a task's watch
	// on first use.
	mu      sync.Mutex
	watches map[int64]*TaskWatch
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
		watches:  map[int64]*TaskWatch{},
	}
}

// Run fills the watches of every stored unarchived task and reports each on
// Tasks(), then replicates until
// ctx ends: it syncs with the server, then waits for a Wake or the poll
// interval, and repeats. A failed fetch is logged and retried at the next
// sync; a store error ends Run.
func (l *Log) Run(ctx context.Context) error {
	tasks, err := l.store.UnarchivedTasks(ctx)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		l.Task(t.ID).set(t)
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

// sync stores every page the server has after the stored cursor, and updates
// the watches of the tasks each page changes. It returns
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
		l.stored.Wake()
		for _, c := range changes {
			l.Task(c.ID).set(c)
		}
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

// Wake makes Run check the server for new events now, or as soon as its
// current sync ends.
func (l *Log) Wake() {
	l.wake.Wake()
}

// Tasks reports the full state of tasks: every unarchived stored task when Run
// starts, then each task with a command or archive event in a stored page,
// after that page. A report may repeat a task's current state, so the
// consumer must apply them idempotently. Run blocks until each report is
// received.
func (l *Log) Tasks() <-chan runnerstore.TaskChange {
	return l.tasks
}

// Task returns the watch for one task's latest state.
func (l *Log) Task(id int64) *TaskWatch {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.watches[id]
	if w == nil {
		w = &TaskWatch{}
		l.watches[id] = w
	}
	return w
}

// TaskWatch is one task's latest state as the replica knows it.
type TaskWatch struct {
	// mu guards latest, the task's latest change, and ok, whether there has
	// been one.
	mu     sync.Mutex
	latest runnerstore.TaskChange
	ok     bool
	// changed wakes the task's readers when set stores a change.
	changed wakeup.Broadcast
}

// Latest returns the task's latest TaskChange; ok is false before its first
// report.
func (w *TaskWatch) Latest() (c runnerstore.TaskChange, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.latest, w.ok
}

// Changed returns a channel closed by the task's next change. Take it before
// calling Latest, so a change in between is not missed.
func (w *TaskWatch) Changed() <-chan struct{} {
	return w.changed.Wait()
}

// set stores c as the task's latest change and wakes its readers.
func (w *TaskWatch) set(c runnerstore.TaskChange) {
	w.mu.Lock()
	w.latest, w.ok = c, true
	w.mu.Unlock()
	w.changed.Wake()
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
