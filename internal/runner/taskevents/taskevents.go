// Package taskevents replicates the runner's tasks' event logs from the server
// into the runner's local store, and exposes each task's events as a stream.
// Readers keep their own position as an event id and never see the
// replication cursor. See proposals/draft/runner-event-replica.md.
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

// defaultPollInterval is Run's poll interval when Options leaves it unset; it
// matches the runner's TaskManager.
const defaultPollInterval = 30 * time.Second

// Options configures a Log.
type Options struct {
	Store    *runnerstore.Store
	Client   gritzclient.Client
	RunnerID string
	// PollInterval is how long Run waits for a Wake before it checks the
	// server again. Defaults to 30s.
	PollInterval time.Duration
	Log          *slog.Logger
}

// Log is the runner's replicated copy of its tasks' event logs.
type Log struct {
	store        *runnerstore.Store
	client       gritzclient.Client
	runnerID     string
	pollInterval time.Duration
	log          *slog.Logger
	wake         wakeup.Chan
	tasks        chan TaskState

	// archived is whether each stored task is archived. Only Run touches it.
	archived map[int64]bool

	mu sync.Mutex
	// stored holds, per task with a blocked Read, a channel closed when the
	// next events for the task are stored.
	stored map[int64]chan struct{}
}

// TaskState is a change in whether the runner should have a TaskRunner for a
// task.
type TaskState struct {
	ID       int64
	Archived bool
}

// New returns a Log that replicates the runner's tasks from the server into
// the store.
func New(opts Options) *Log {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Log{
		store:        opts.Store,
		client:       opts.Client,
		runnerID:     opts.RunnerID,
		pollInterval: cmp.Or(opts.PollInterval, defaultPollInterval),
		log:          log,
		wake:         wakeup.New(),
		tasks:        make(chan TaskState),
		archived:     map[int64]bool{},
		stored:       map[int64]chan struct{}{},
	}
}

// Wake makes Run check the server for new events now. Wake-ups coalesce.
func (l *Log) Wake() { l.wake.Wake() }

// Tasks reports when a task becomes active or archived: every unarchived
// stored task at boot, then each new, archived or unarchived task. Run blocks
// until each report is received.
func (l *Log) Tasks() <-chan TaskState { return l.tasks }

// Run replicates until ctx ends, when it returns ctx's cause. It returns
// early only when the store fails; a failed fetch is logged and retried.
func (l *Log) Run(ctx context.Context) error {
	if err := l.boot(ctx); err != nil {
		return err
	}
	for {
		if err := l.sync(ctx); err != nil {
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			return err
		}
		// The interval is not only a fallback for a lost wake. more == false
		// means nothing is readable right now, not that the log is caught up:
		// committed rows can be held back by the server's xid watermark, and
		// the transaction whose end releases them may send no wake. Polling
		// again after the interval is what picks them up.
		select {
		case <-l.wake:
		case <-time.After(l.pollInterval):
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}

// boot loads each stored task's archive state and reports the unarchived
// ones as active.
func (l *Log) boot(ctx context.Context) error {
	ids, err := l.store.TaskIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		events, err := l.store.TaskEventsAfter(ctx, id, 0)
		if err != nil {
			return err
		}
		var archived bool
		for _, e := range events {
			archived = foldArchived(archived, e)
		}
		l.archived[id] = archived
		if !archived {
			if err := l.report(ctx, TaskState{ID: id}); err != nil {
				return err
			}
		}
	}
	return nil
}

// sync pages the server's events after the stored cursor into the store until
// no more are available. It returns an error only when the store fails or ctx
// ends.
func (l *Log) sync(ctx context.Context) error {
	cursor, err := l.store.Cursor(ctx)
	if err != nil {
		return err
	}
	req := &gritzv1.ListRunnerEventsRequest{Runner: l.runnerID, PageToken: cursor}
	for resp, err := range gritzclient.ListRunnerEvents(ctx, l.client, req) {
		if err != nil {
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			l.log.Error("failed to list runner events", "err", err)
			return nil
		}
		// An empty page echoes the request's token, so there is nothing to
		// store.
		if len(resp.GetEvents()) == 0 {
			continue
		}
		if err := l.store.AppendEvents(ctx, resp.GetEvents(), resp.GetNextPageToken()); err != nil {
			return err
		}
		if err := l.apply(ctx, resp.GetEvents()); err != nil {
			return err
		}
	}
	return nil
}

// apply wakes the Reads of the tasks in a stored page, and reports each task
// the page made new, archived or unarchived. A task that is new and archived
// within one page was never active, so it is not reported.
func (l *Log) apply(ctx context.Context, events []*gritzv1.Event) error {
	type before struct{ known, archived bool }
	var ids []int64
	page := map[int64]before{}
	for _, e := range events {
		id := e.GetTaskId()
		if _, ok := page[id]; !ok {
			archived, known := l.archived[id]
			page[id] = before{known: known, archived: archived}
			ids = append(ids, id)
		}
		l.archived[id] = foldArchived(l.archived[id], e)
	}

	l.mu.Lock()
	for _, id := range ids {
		if ch, ok := l.stored[id]; ok {
			close(ch)
			delete(l.stored, id)
		}
	}
	l.mu.Unlock()

	for _, id := range ids {
		was, now := page[id], l.archived[id]
		if was.known && was.archived == now || !was.known && now {
			continue
		}
		if err := l.report(ctx, TaskState{ID: id, Archived: now}); err != nil {
			return err
		}
	}
	return nil
}

func (l *Log) report(ctx context.Context, s TaskState) error {
	select {
	case l.tasks <- s:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// foldArchived returns whether a task is archived after e, given whether it
// was before.
func foldArchived(archived bool, e *gritzv1.Event) bool {
	switch e.GetLifecycle().GetKind() {
	case gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED, gritzv1.LifecycleKind_LIFECYCLE_KIND_AUTO_ARCHIVED:
		return true
	case gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED:
		return false
	default:
		return archived
	}
}

// storedSignal returns a channel closed when the task's next events are
// stored.
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

// Stream returns the task's event stream.
func (l *Log) Stream(taskID int64) *Stream {
	return &Stream{log: l, taskID: taskID}
}

// Stream is one task's events, in order, with no gaps. It holds no position,
// so several readers can follow it from different positions.
type Stream struct {
	log    *Log
	taskID int64
}

// Read returns the events after the event with id after (0 for the start),
// blocking until at least one exists or ctx ends.
func (s *Stream) Read(ctx context.Context, after int64) ([]*gritzv1.Event, error) {
	for {
		// Take the signal before reading, so events stored between the read
		// and the wait still wake it.
		stored := s.log.storedSignal(s.taskID)
		events, err := s.log.store.TaskEventsAfter(ctx, s.taskID, after)
		if err != nil || len(events) > 0 {
			return events, err
		}
		select {
		case <-stored:
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
}
