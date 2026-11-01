package taskevents_test

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"

	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/taskevents"
)

// fakeServer serves ListRunnerEvents from an in-memory log, two events per
// page. A page token is the number of events before the page.
type fakeServer struct {
	mu     sync.Mutex
	events []*gritzv1.Event
}

func (s *fakeServer) add(events ...*gritzv1.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, events...)
}

func (s *fakeServer) client() *gritzclient.ClientMock {
	return &gritzclient.ClientMock{
		ListRunnerEventsFunc: func(ctx context.Context, req *gritzv1.ListRunnerEventsRequest) (*gritzv1.ListRunnerEventsResponse, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			start := 0
			if req.GetPageToken() != "" {
				var err error
				if start, err = strconv.Atoi(req.GetPageToken()); err != nil {
					return nil, err
				}
			}
			end := min(start+2, len(s.events))
			if start == end {
				return &gritzv1.ListRunnerEventsResponse{NextPageToken: req.GetPageToken()}, nil
			}
			return &gritzv1.ListRunnerEventsResponse{
				Events:        s.events[start:end],
				NextPageToken: strconv.Itoa(end),
				More:          end < len(s.events),
			}, nil
		},
	}
}

func openStore(t *testing.T, path string) *runnerstore.Store {
	t.Helper()
	store, err := runnerstore.Open(path)
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })
	return store
}

// runLog runs l until the test ends, or until the returned stop is called.
func runLog(t *testing.T, l *taskevents.Log) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	stop = sync.OnceFunc(func() {
		cancel()
		assert.Assert(t, errors.Is(<-done, context.Canceled))
	})
	t.Cleanup(stop)
	return stop
}

// readUntil reads the task's stream until it holds the event with id last.
func readUntil(t *testing.T, l *taskevents.Log, taskID, last int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var after int64
	for after != last {
		events, err := l.Stream(taskID).Read(ctx, after)
		assert.NilError(t, err)
		after = events[len(events)-1].GetId()
	}
}

func receive(t *testing.T, l *taskevents.Log) taskevents.TaskState {
	t.Helper()
	select {
	case s := <-l.Tasks():
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a task state")
		return taskevents.TaskState{}
	}
}

func lifecycle(id, taskID int64, kind gritzv1.LifecycleKind) *gritzv1.Event {
	return &gritzv1.Event{
		Id:      id,
		TaskId:  taskID,
		Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: kind}},
	}
}

func instruction(id, taskID int64, text string) *gritzv1.Event {
	return &gritzv1.Event{
		Id:      id,
		TaskId:  taskID,
		Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: text}},
	}
}

func TestLogRun(t *testing.T) {
	t.Parallel()
	// Arrange - two tasks' events interleaved over three pages.
	server := &fakeServer{}
	server.add(
		lifecycle(1, 10, gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED),
		lifecycle(2, 20, gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED),
		instruction(3, 10, "first"),
		instruction(4, 20, "second"),
		instruction(5, 10, "third"),
	)
	store := openStore(t, filepath.Join(t.TempDir(), "runner.db"))
	l := taskevents.New(taskevents.Options{
		Store:    store,
		Client:   server.client(),
		RunnerID: "r1",
	})

	// Act
	runLog(t, l)

	// Assert - each new task is reported once, and the store holds every
	// task's events in log order.
	assert.DeepEqual(t, receive(t, l), taskevents.TaskState{ID: 10})
	assert.DeepEqual(t, receive(t, l), taskevents.TaskState{ID: 20})
	readUntil(t, l, 10, 5)
	readUntil(t, l, 20, 4)
	ids, err := store.UnarchivedTaskIDs(t.Context())
	assert.NilError(t, err)
	assert.DeepEqual(t, ids, []int64{10, 20})
	events, err := store.TaskEventsAfter(t.Context(), 10, 0)
	assert.NilError(t, err)
	assert.DeepEqual(t,
		events,
		[]*gritzv1.Event{
			lifecycle(1, 10, gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED),
			instruction(3, 10, "first"),
			instruction(5, 10, "third"),
		},
		protocmp.Transform(),
	)
	events, err = store.TaskEventsAfter(t.Context(), 20, 0)
	assert.NilError(t, err)
	assert.DeepEqual(t,
		events,
		[]*gritzv1.Event{
			lifecycle(2, 20, gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED),
			instruction(4, 20, "second"),
		},
		protocmp.Transform(),
	)
	cursor, err := store.Cursor(t.Context())
	assert.NilError(t, err)
	assert.Equal(t, cursor, "5")
}

func TestLogRun_ResumesFromCursor(t *testing.T) {
	t.Parallel()
	// Arrange - a first Log replicates the first two events, then stops.
	path := filepath.Join(t.TempDir(), "runner.db")
	server := &fakeServer{}
	server.add(
		lifecycle(1, 10, gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED),
		instruction(2, 10, "first"),
	)
	first, err := runnerstore.Open(path)
	assert.NilError(t, err)
	l := taskevents.New(taskevents.Options{
		Store:    first,
		Client:   server.client(),
		RunnerID: "r1",
	})
	stop := runLog(t, l)
	assert.DeepEqual(t, receive(t, l), taskevents.TaskState{ID: 10})
	readUntil(t, l, 10, 2)
	stop()
	assert.NilError(t, first.Close())
	server.add(instruction(3, 10, "second"))
	client := server.client()

	// Act - a new Log over the reopened database.
	store := openStore(t, path)
	l = taskevents.New(taskevents.Options{
		Store:    store,
		Client:   client,
		RunnerID: "r1",
	})
	runLog(t, l)

	// Assert - boot reports the stored task, and replication resumes from
	// the stored cursor rather than the start of the log.
	assert.DeepEqual(t, receive(t, l), taskevents.TaskState{ID: 10})
	readUntil(t, l, 10, 3)
	calls := client.ListRunnerEventsCalls()
	assert.Assert(t, len(calls) > 0)
	assert.DeepEqual(t,
		calls[0].ListRunnerEventsRequest,
		&gritzv1.ListRunnerEventsRequest{Runner: "r1", PageToken: "2"},
		protocmp.Transform(),
	)
	events, err := store.TaskEventsAfter(t.Context(), 10, 0)
	assert.NilError(t, err)
	assert.DeepEqual(t,
		events,
		[]*gritzv1.Event{
			lifecycle(1, 10, gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED),
			instruction(2, 10, "first"),
			instruction(3, 10, "second"),
		},
		protocmp.Transform(),
	)
}

func TestLogTasks_NewTaskOnce(t *testing.T) {
	t.Parallel()
	// Arrange - task 10 is reported when its first event is stored.
	server := &fakeServer{}
	server.add(lifecycle(1, 10, gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED))
	l := taskevents.New(taskevents.Options{
		Store:    openStore(t, filepath.Join(t.TempDir(), "runner.db")),
		Client:   server.client(),
		RunnerID: "r1",
	})
	runLog(t, l)
	assert.DeepEqual(t, receive(t, l), taskevents.TaskState{ID: 10})

	// Act - more events for task 10 arrive, then task 20's first.
	server.add(instruction(2, 10, "first"))
	l.Wake()
	readUntil(t, l, 10, 2)
	server.add(lifecycle(3, 20, gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED))
	l.Wake()

	// Assert - the next report is task 20: task 10 is not reported again.
	assert.DeepEqual(t, receive(t, l), taskevents.TaskState{ID: 20})
}

func TestLogTasks_ArchiveAndUnarchive(t *testing.T) {
	t.Parallel()
	// Arrange
	server := &fakeServer{}
	server.add(lifecycle(1, 10, gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED))
	l := taskevents.New(taskevents.Options{
		Store:    openStore(t, filepath.Join(t.TempDir(), "runner.db")),
		Client:   server.client(),
		RunnerID: "r1",
	})
	runLog(t, l)
	assert.DeepEqual(t, receive(t, l), taskevents.TaskState{ID: 10})

	// Act - archive, auto-archive after an unarchive, then unarchive again.
	server.add(lifecycle(2, 10, gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED))
	l.Wake()
	archived := receive(t, l)
	server.add(lifecycle(3, 10, gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED))
	l.Wake()
	unarchived := receive(t, l)
	server.add(lifecycle(4, 10, gritzv1.LifecycleKind_LIFECYCLE_KIND_AUTO_ARCHIVED))
	l.Wake()
	autoArchived := receive(t, l)

	// Assert
	assert.DeepEqual(t,
		[]taskevents.TaskState{archived, unarchived, autoArchived},
		[]taskevents.TaskState{
			{ID: 10, Archived: true},
			{ID: 10},
			{ID: 10, Archived: true},
		},
	)
}

func TestLogTasks_SkipsArchivedAtBoot(t *testing.T) {
	t.Parallel()
	// Arrange - a database with task 10 archived, task 20 active, and task 30
	// archived then unarchived, all already replicated.
	events := []*gritzv1.Event{
		lifecycle(1, 10, gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED),
		lifecycle(2, 20, gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED),
		lifecycle(3, 30, gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED),
		lifecycle(4, 10, gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED),
		lifecycle(5, 30, gritzv1.LifecycleKind_LIFECYCLE_KIND_AUTO_ARCHIVED),
		lifecycle(6, 30, gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED),
	}
	server := &fakeServer{}
	server.add(events...)
	store := openStore(t, filepath.Join(t.TempDir(), "runner.db"))
	_, err := store.AppendEvents(t.Context(), events, "6")
	assert.NilError(t, err)
	l := taskevents.New(taskevents.Options{
		Store:    store,
		Client:   server.client(),
		RunnerID: "r1",
	})

	// Act
	runLog(t, l)
	booted := []taskevents.TaskState{receive(t, l), receive(t, l)}
	server.add(lifecycle(7, 40, gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED))
	l.Wake()
	next := receive(t, l)

	// Assert - boot reports the unarchived tasks in id order and nothing for
	// task 10, so the next report is the new task 40.
	assert.DeepEqual(t, booted, []taskevents.TaskState{{ID: 20}, {ID: 30}})
	assert.DeepEqual(t, next, taskevents.TaskState{ID: 40})
}

func TestStreamRead_BlocksUntilStored(t *testing.T) {
	t.Parallel()
	// Arrange - task 10's first event is replicated, and a Read waits for
	// the next one.
	server := &fakeServer{}
	server.add(lifecycle(1, 10, gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED))
	l := taskevents.New(taskevents.Options{
		Store:    openStore(t, filepath.Join(t.TempDir(), "runner.db")),
		Client:   server.client(),
		RunnerID: "r1",
	})
	runLog(t, l)
	assert.DeepEqual(t, receive(t, l), taskevents.TaskState{ID: 10})
	readUntil(t, l, 10, 1)
	type result struct {
		events []*gritzv1.Event
		err    error
	}
	done := make(chan result, 1)
	go func() {
		events, err := l.Stream(10).Read(t.Context(), 1)
		done <- result{events, err}
	}()

	// Act - an event for another task is stored, then one for task 10.
	server.add(lifecycle(2, 20, gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED))
	l.Wake()
	assert.DeepEqual(t, receive(t, l), taskevents.TaskState{ID: 20})
	select {
	case r := <-done:
		t.Fatalf("Read returned before an event for its task was stored: %v", r)
	case <-time.After(50 * time.Millisecond):
	}
	server.add(instruction(3, 10, "first"))
	l.Wake()

	// Assert - Read returns only the events after its position.
	select {
	case r := <-done:
		assert.NilError(t, r.err)
		assert.DeepEqual(t,
			r.events,
			[]*gritzv1.Event{instruction(3, 10, "first")},
			protocmp.Transform(),
		)
	case <-time.After(5 * time.Second):
		t.Fatal("Read did not return after an event for its task was stored")
	}
}

func TestLogRun_PollsWithoutWake(t *testing.T) {
	t.Parallel()
	// Arrange - a short poll interval, and no Wake after the server's log
	// grows, as when a held-back row is released by a transaction that sends
	// no notification.
	server := &fakeServer{}
	server.add(lifecycle(1, 10, gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED))
	l := taskevents.New(taskevents.Options{
		Store:        openStore(t, filepath.Join(t.TempDir(), "runner.db")),
		Client:       server.client(),
		RunnerID:     "r1",
		PollInterval: 10 * time.Millisecond,
	})
	runLog(t, l)
	assert.DeepEqual(t, receive(t, l), taskevents.TaskState{ID: 10})
	readUntil(t, l, 10, 1)

	// Act
	server.add(instruction(2, 10, "first"))

	// Assert - the poll interval picks it up.
	readUntil(t, l, 10, 2)
}
