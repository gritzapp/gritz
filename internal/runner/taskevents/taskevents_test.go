package taskevents_test

import (
	"context"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/poll"

	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/taskevents"
	"github.com/gritzapp/gritz/internal/x/testx"
)

// server is a fake ListRunnerEvents: it serves its events in pages of
// pageSize, with the offset of the next page as the page token.
type server struct {
	pageSize int

	mu     sync.Mutex
	events []*gritzv1.Event
}

func (s *server) add(events ...*gritzv1.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, events...)
}

func (s *server) ListRunnerEvents(_ context.Context, req *gritzv1.ListRunnerEventsRequest) (*gritzv1.ListRunnerEventsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	offset := 0
	if req.GetPageToken() != "" {
		var err error
		offset, err = strconv.Atoi(req.GetPageToken())
		if err != nil {
			return nil, err
		}
	}
	end := min(offset+s.pageSize, len(s.events))
	return &gritzv1.ListRunnerEventsResponse{
		Events:        s.events[offset:end],
		NextPageToken: strconv.Itoa(end),
		More:          end < len(s.events),
	}, nil
}

// run runs l until the returned stop is called, which returns Run's error.
func run(t *testing.T, l *taskevents.Log) (stop func() error) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	var once sync.Once
	var err error
	stop = func() error {
		once.Do(func() {
			cancel()
			err = <-done
		})
		return err
	}
	t.Cleanup(func() { _ = stop() })
	return stop
}

func openStore(t *testing.T, path string) *runnerstore.Store {
	store, err := runnerstore.Open(path)
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })
	return store
}

func TestLog(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := &server{pageSize: 2}
	srv.add(
		&gritzv1.Event{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}},
		&gritzv1.Event{Id: 2, TaskId: 20, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "b"}}},
		&gritzv1.Event{Id: 3, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "c"}}},
		&gritzv1.Event{Id: 4, TaskId: 20, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "d"}}},
		&gritzv1.Event{Id: 5, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "e"}}},
	)
	client := &gritzclient.ClientMock{ListRunnerEventsFunc: srv.ListRunnerEvents}
	l := taskevents.New(taskevents.Options{
		Store:    openStore(t, filepath.Join(t.TempDir(), "runner.db")),
		Client:   client,
		RunnerID: "runner-1",
	})

	// Act - run until the last event, 5, is stored.
	run(t, l)
	last, err := l.Stream(10).Read(t.Context(), 3)
	assert.NilError(t, err)

	// Assert - the sync walked every page from the start, and each task's
	// stream holds its events from the server, in order.
	assert.DeepEqual(t,
		last,
		[]*gritzv1.Event{
			{Id: 5, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "e"}}},
		},
		protocmp.Transform(),
	)
	assert.DeepEqual(t,
		testx.ExtractField(client.ListRunnerEventsCalls(), "ListRunnerEventsRequest"),
		[]*gritzv1.ListRunnerEventsRequest{
			{Runner: "runner-1"},
			{Runner: "runner-1", PageToken: "2"},
			{Runner: "runner-1", PageToken: "4"},
		},
		protocmp.Transform(),
	)
	events, err := l.Stream(10).Read(t.Context(), 0)
	assert.NilError(t, err)
	assert.DeepEqual(t,
		events,
		[]*gritzv1.Event{
			{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}},
			{Id: 3, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "c"}}},
			{Id: 5, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "e"}}},
		},
		protocmp.Transform(),
	)
	events, err = l.Stream(20).Read(t.Context(), 0)
	assert.NilError(t, err)
	assert.DeepEqual(t,
		events,
		[]*gritzv1.Event{
			{Id: 2, TaskId: 20, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "b"}}},
			{Id: 4, TaskId: 20, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "d"}}},
		},
		protocmp.Transform(),
	)
}

func TestLog_Restart(t *testing.T) {
	t.Parallel()
	// Arrange - a first Log replicates events 1 and 2, then stops.
	path := filepath.Join(t.TempDir(), "runner.db")
	srv := &server{pageSize: 10}
	srv.add(
		&gritzv1.Event{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}},
		&gritzv1.Event{Id: 2, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "b"}}},
	)
	first, err := runnerstore.Open(path)
	assert.NilError(t, err)
	l := taskevents.New(taskevents.Options{
		Store:    first,
		Client:   &gritzclient.ClientMock{ListRunnerEventsFunc: srv.ListRunnerEvents},
		RunnerID: "runner-1",
	})
	stop := run(t, l)
	_, err = l.Stream(10).Read(t.Context(), 1)
	assert.NilError(t, err)
	assert.ErrorIs(t, stop(), context.Canceled)
	assert.NilError(t, first.Close())
	srv.add(&gritzv1.Event{Id: 3, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "c"}}})

	// Act - a second Log opens the same database.
	client := &gritzclient.ClientMock{ListRunnerEventsFunc: srv.ListRunnerEvents}
	l = taskevents.New(taskevents.Options{
		Store:    openStore(t, path),
		Client:   client,
		RunnerID: "runner-1",
	})
	run(t, l)
	events, err := l.Stream(10).Read(t.Context(), 2)
	assert.NilError(t, err)

	// Assert - it fetched from the stored cursor, so only event 3 is new.
	assert.DeepEqual(t,
		events,
		[]*gritzv1.Event{
			{Id: 3, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "c"}}},
		},
		protocmp.Transform(),
	)
	assert.DeepEqual(t,
		testx.ExtractField(client.ListRunnerEventsCalls(), "ListRunnerEventsRequest"),
		[]*gritzv1.ListRunnerEventsRequest{
			{Runner: "runner-1", PageToken: "2"},
		},
		protocmp.Transform(),
	)
}

func TestLog_Boot(t *testing.T) {
	t.Parallel()
	// Arrange - tasks 10, 20 and 30 are stored, and 20 is archived; 30 was
	// archived and unarchived.
	store := openStore(t, filepath.Join(t.TempDir(), "runner.db"))
	_, err := store.AppendEvents(t.Context(),
		[]*gritzv1.Event{
			{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START, Version: 1}}},
			{Id: 2, TaskId: 20, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START, Version: 1}}},
			{Id: 3, TaskId: 30, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START, Version: 2}}},
			{Id: 4, TaskId: 20, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED}}},
			{Id: 5, TaskId: 30, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED}}},
			{Id: 6, TaskId: 30, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED}}},
		},
		"0", // the fake server below has no events, so it serves empty pages from here
	)
	assert.NilError(t, err)
	srv := &server{pageSize: 10}
	l := taskevents.New(taskevents.Options{
		Store:    store,
		Client:   &gritzclient.ClientMock{ListRunnerEventsFunc: srv.ListRunnerEvents},
		RunnerID: "runner-1",
	})

	// Act
	run(t, l)
	reports := []runnerstore.TaskChange{<-l.Tasks(), <-l.Tasks()}

	// Assert - the unarchived tasks are reported in id order, archived 20 is
	// skipped.
	assert.DeepEqual(t,
		reports,
		[]runnerstore.TaskChange{
			{ID: 10, Command: gritzv1.TaskCommand_START, Version: 1},
			{ID: 30, Command: gritzv1.TaskCommand_START, Version: 2},
		},
	)
}

func TestLog_Tasks(t *testing.T) {
	t.Parallel()
	// Arrange - one event per page.
	srv := &server{pageSize: 1}
	srv.add(
		&gritzv1.Event{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START, Version: 1}}},
		&gritzv1.Event{Id: 2, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}},
		&gritzv1.Event{Id: 3, TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED}}},
		&gritzv1.Event{Id: 4, TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED}}},
		&gritzv1.Event{Id: 5, TaskId: 10, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_STOP, Version: 2}}},
	)
	l := taskevents.New(taskevents.Options{
		Store:    openStore(t, filepath.Join(t.TempDir(), "runner.db")),
		Client:   &gritzclient.ClientMock{ListRunnerEventsFunc: srv.ListRunnerEvents},
		RunnerID: "runner-1",
	})

	// Act
	run(t, l)
	reports := []runnerstore.TaskChange{<-l.Tasks(), <-l.Tasks(), <-l.Tasks(), <-l.Tasks()}

	// Assert - each command, archive and unarchive page reports the task's
	// state after it; the instruction page reports nothing.
	assert.DeepEqual(t,
		reports,
		[]runnerstore.TaskChange{
			{ID: 10, Command: gritzv1.TaskCommand_START, Version: 1},
			{ID: 10, Command: gritzv1.TaskCommand_START, Version: 1, Archived: true},
			{ID: 10, Command: gritzv1.TaskCommand_START, Version: 1},
			{ID: 10, Command: gritzv1.TaskCommand_STOP, Version: 2},
		},
	)
}

func TestStreamRead_Blocks(t *testing.T) {
	t.Parallel()
	// Arrange - event 1 is replicated.
	srv := &server{pageSize: 10}
	srv.add(&gritzv1.Event{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}})
	l := taskevents.New(taskevents.Options{
		Store:    openStore(t, filepath.Join(t.TempDir(), "runner.db")),
		Client:   &gritzclient.ClientMock{ListRunnerEventsFunc: srv.ListRunnerEvents},
		RunnerID: "runner-1",
	})
	run(t, l)
	_, err := l.Stream(10).Read(t.Context(), 0)
	assert.NilError(t, err)

	// Act - a Read after event 1 waits; then events for tasks 20 and 10 are
	// stored on a Wake.
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, blockedErr := l.Stream(10).Read(ctx, 1)
	type result struct {
		events []*gritzv1.Event
		err    error
	}
	done := make(chan result, 1)
	go func() {
		events, err := l.Stream(10).Read(t.Context(), 1)
		done <- result{events, err}
	}()
	srv.add(
		&gritzv1.Event{Id: 2, TaskId: 20, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "b"}}},
		&gritzv1.Event{Id: 3, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "c"}}},
	)
	l.Wake()
	got := <-done

	// Assert - the first Read gave up with ctx, the second returned event 3.
	assert.ErrorIs(t, blockedErr, context.DeadlineExceeded)
	assert.NilError(t, got.err)
	assert.DeepEqual(t,
		got.events,
		[]*gritzv1.Event{
			{Id: 3, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "c"}}},
		},
		protocmp.Transform(),
	)
}

func TestLog_PollInterval(t *testing.T) {
	t.Parallel()
	// Arrange - the first sync finds no events.
	srv := &server{pageSize: 10}
	client := &gritzclient.ClientMock{ListRunnerEventsFunc: srv.ListRunnerEvents}
	l := taskevents.New(taskevents.Options{
		Store:        openStore(t, filepath.Join(t.TempDir(), "runner.db")),
		Client:       client,
		RunnerID:     "runner-1",
		PollInterval: 10 * time.Millisecond,
	})
	run(t, l)
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		if len(client.ListRunnerEventsCalls()) == 0 {
			return poll.Continue("no sync yet")
		}
		return poll.Success()
	})

	// Act - an event becomes readable with no Wake.
	srv.add(&gritzv1.Event{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}})
	events, err := l.Stream(10).Read(t.Context(), 0)

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t,
		events,
		[]*gritzv1.Event{
			{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}},
		},
		protocmp.Transform(),
	)
}
