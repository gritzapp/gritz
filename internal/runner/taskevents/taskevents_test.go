package taskevents_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/poll"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/taskevents"
	"github.com/gritzapp/gritz/internal/runner/testrunner"
	"github.com/gritzapp/gritz/internal/x/testx"
)

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
	srv := testrunner.NewServer(testrunner.ServerOptions{PageSize: 2})
	srv.Add(
		&gritzv1.Event{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}},
		&gritzv1.Event{Id: 2, TaskId: 20, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "b"}}},
		&gritzv1.Event{Id: 3, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "c"}}},
		&gritzv1.Event{Id: 4, TaskId: 20, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "d"}}},
		&gritzv1.Event{Id: 5, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "e"}}},
	)
	client := srv.Client()
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
	srv := testrunner.NewServer(testrunner.ServerOptions{PageSize: 10})
	srv.Add(
		&gritzv1.Event{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}},
		&gritzv1.Event{Id: 2, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "b"}}},
	)
	first, err := runnerstore.Open(path)
	assert.NilError(t, err)
	l := taskevents.New(taskevents.Options{
		Store:    first,
		Client:   srv.Client(),
		RunnerID: "runner-1",
	})
	stop := run(t, l)
	_, err = l.Stream(10).Read(t.Context(), 1)
	assert.NilError(t, err)
	assert.ErrorIs(t, stop(), context.Canceled)
	assert.NilError(t, first.Close())
	srv.Add(&gritzv1.Event{Id: 3, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "c"}}})

	// Act - a second Log opens the same database.
	client := srv.Client()
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
	srv := testrunner.NewServer(testrunner.ServerOptions{PageSize: 10})
	l := taskevents.New(taskevents.Options{
		Store:    store,
		Client:   srv.Client(),
		RunnerID: "runner-1",
	})

	// Act
	run(t, l)
	reports := []runnerstore.TaskChange{<-l.Tasks(), <-l.Tasks()}

	// Assert - the unarchived tasks are reported in id order, archived 20 is
	// skipped; their watches hold the same state, and 20's has none.
	assert.DeepEqual(t,
		reports,
		[]runnerstore.TaskChange{
			{ID: 10, Command: gritzv1.TaskCommand_START, Version: 1, CommandEventID: 1},
			{ID: 30, Command: gritzv1.TaskCommand_START, Version: 2, CommandEventID: 3},
		},
	)
	for _, want := range reports {
		got, ok := l.Task(want.ID).Latest()
		assert.Assert(t, ok)
		assert.DeepEqual(t, got, want)
	}
	_, ok := l.Task(20).Latest()
	assert.Assert(t, !ok)
}

func TestLog_Tasks(t *testing.T) {
	t.Parallel()
	// Arrange - one event per page.
	srv := testrunner.NewServer(testrunner.ServerOptions{PageSize: 1})
	srv.Add(
		&gritzv1.Event{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START, Version: 1}}},
		&gritzv1.Event{Id: 2, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}},
		&gritzv1.Event{Id: 3, TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED}}},
		&gritzv1.Event{Id: 4, TaskId: 10, Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED}}},
		&gritzv1.Event{Id: 5, TaskId: 10, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_STOP, Version: 2}}},
	)
	l := taskevents.New(taskevents.Options{
		Store:    openStore(t, filepath.Join(t.TempDir(), "runner.db")),
		Client:   srv.Client(),
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
			{ID: 10, Command: gritzv1.TaskCommand_START, Version: 1, CommandEventID: 1},
			{ID: 10, Command: gritzv1.TaskCommand_START, Version: 1, CommandEventID: 1, Archived: true},
			{ID: 10, Command: gritzv1.TaskCommand_START, Version: 1, CommandEventID: 1},
			{ID: 10, Command: gritzv1.TaskCommand_STOP, Version: 2, CommandEventID: 5},
		},
	)
}

func TestTaskWatch_Latest(t *testing.T) {
	t.Parallel()
	// Arrange - a page with task 10's command and an instruction.
	srv := testrunner.NewServer(testrunner.ServerOptions{PageSize: 10})
	srv.Add(
		&gritzv1.Event{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START, Version: 1}}},
		&gritzv1.Event{Id: 2, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}},
	)
	l := taskevents.New(taskevents.Options{
		Store:    openStore(t, filepath.Join(t.TempDir(), "runner.db")),
		Client:   srv.Client(),
		RunnerID: "runner-1",
	})
	w := l.Task(10)
	_, before := w.Latest()

	// Act - run until the page is reported.
	run(t, l)
	<-l.Tasks()
	after, ok := w.Latest()

	// Assert - nothing before the page, its command after it.
	assert.Assert(t, !before)
	assert.Assert(t, ok)
	assert.DeepEqual(t, after, runnerstore.TaskChange{ID: 10, Command: gritzv1.TaskCommand_START, Version: 1, CommandEventID: 1})
}

func TestTaskWatch_Changed(t *testing.T) {
	t.Parallel()
	// Arrange - tasks 10 and 20 are replicated.
	srv := testrunner.NewServer(testrunner.ServerOptions{PageSize: 10})
	srv.Add(
		&gritzv1.Event{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START, Version: 1}}},
		&gritzv1.Event{Id: 2, TaskId: 20, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START, Version: 1}}},
	)
	l := taskevents.New(taskevents.Options{
		Store:    openStore(t, filepath.Join(t.TempDir(), "runner.db")),
		Client:   srv.Client(),
		RunnerID: "runner-1",
	})
	run(t, l)
	<-l.Tasks()
	<-l.Tasks()
	changed10 := l.Task(10).Changed()
	changed20 := l.Task(20).Changed()

	// Act - a page with a command for task 10 only.
	srv.Add(&gritzv1.Event{Id: 3, TaskId: 10, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_STOP, Version: 2}}})
	l.Wake()
	<-l.Tasks()

	// Assert - task 10's channel is closed, task 20's is not.
	select {
	case <-changed10:
	default:
		t.Fatal("task 10's Changed is not closed")
	}
	select {
	case <-changed20:
		t.Fatal("task 20's Changed is closed")
	default:
	}
	got, ok := l.Task(10).Latest()
	assert.Assert(t, ok)
	assert.DeepEqual(t, got, runnerstore.TaskChange{ID: 10, Command: gritzv1.TaskCommand_STOP, Version: 2, CommandEventID: 3})
}

func TestStreamRead_Blocks(t *testing.T) {
	t.Parallel()
	// Arrange - event 1 is replicated.
	srv := testrunner.NewServer(testrunner.ServerOptions{PageSize: 10})
	srv.Add(&gritzv1.Event{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}})
	l := taskevents.New(taskevents.Options{
		Store:    openStore(t, filepath.Join(t.TempDir(), "runner.db")),
		Client:   srv.Client(),
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
	srv.Add(
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
	srv := testrunner.NewServer(testrunner.ServerOptions{PageSize: 10})
	client := srv.Client()
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
	srv.Add(&gritzv1.Event{Id: 1, TaskId: 10, Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "a"}}})
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
