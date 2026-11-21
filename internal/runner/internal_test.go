package runner

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/taskevents"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
	"gotest.tools/v3/poll"
)

// TestDoubling checks the backoff matches the schedule the hand-rolled loops
// used: start at initial, double without jitter, cap at maxInterval, and go
// back to initial on Reset.
func TestDoubling(t *testing.T) {
	bo := doubling(250*time.Millisecond, 3*time.Second)
	var got []time.Duration
	for range 6 {
		got = append(got, bo.NextBackOff())
	}
	assert.DeepEqual(t, got, []time.Duration{
		250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 3 * time.Second, 3 * time.Second,
	})
	bo.Reset()
	assert.Equal(t, bo.NextBackOff(), 250*time.Millisecond)
}

// TestTaskManagerLoad_Slot checks that Load counts a sandbox it finds up
// against the concurrency limit: the run it reattaches to holds a slot.
func TestTaskManagerLoad_Slot(t *testing.T) {
	t.Parallel()
	// Arrange - task 1's sandbox is up, and its driver unreachable.
	store, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })
	assert.NilError(t, store.WriteSandbox(t.Context(), runnerstore.SandboxRecord{TaskID: 1, Version: 1}))
	sbx := &backend.SandboxMock{
		StatusFunc: func(context.Context) (backend.Status, error) { return backend.StatusUp, nil },
		DialFunc: func(context.Context, string) (driverv1connect.DriverServiceClient, error) {
			return nil, errors.New("unreachable")
		},
	}
	be := &backend.BackendMock{
		OpenFunc: func(backend.Handle) (backend.Sandbox, error) { return sbx, nil },
	}
	client := &gritzclient.ClientMock{}
	m, err := NewTaskManager(TaskManagerOptions{
		Client:      client,
		Backend:     be,
		Workspaces:  &workspace.Config{},
		Concurrency: 1,
		RunnerID:    "r",
		Queue:       NewRunnerEventOutbox(RunnerEventOutboxOptions{Store: store, Client: client, Backoff: backoff.NewConstantBackOff(0), Log: slog.Default()}),
		TaskLog:     taskevents.New(taskevents.Options{Store: store, Client: client}),
		RunnerStore: store,
	})
	assert.NilError(t, err)

	// Act
	assert.NilError(t, m.Load(t.Context()))

	// Assert - the reattached run holds the only slot: an Acquire with a
	// canceled ctx finds none free.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assert.Equal(t, m.slots.Acquire(ctx, false), context.Canceled)
}

// TestTaskManagerRun_FirstSync checks that no task gets its state before the
// event log's first sync, and that each stored task, with a sandbox record or
// not, then gets its state from the synced replica, once. A start the
// replica had when the runner stopped, but which already ran, doesn't boot the
// task's sandbox again.
func TestTaskManagerRun_FirstSync(t *testing.T) {
	t.Parallel()
	// Arrange - tasks 1, 2 and 3 have a stored start. Task 1's sandbox is up,
	// task 2's suspended and task 3 has none. The server has task 2's start
	// cleared, as its run started before the runner stopped, and a newer
	// start for task 3.
	store, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })
	_, err = store.AppendEvents(t.Context(), []*gritzv1.Event{
		{Id: 1, TaskId: 1, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START, Version: 1}}},
		{Id: 2, TaskId: 2, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START, Version: 1}}},
		{Id: 3, TaskId: 3, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START, Version: 1}}},
	}, "3")
	assert.NilError(t, err)
	assert.NilError(t, store.WriteSandbox(t.Context(), runnerstore.SandboxRecord{TaskID: 1, Version: 1, Handle: backend.Handle{ID: "1"}}))
	assert.NilError(t, store.WriteSandbox(t.Context(), runnerstore.SandboxRecord{TaskID: 2, Version: 1, Handle: backend.Handle{ID: "2"}}))
	up := &backend.SandboxMock{
		StatusFunc: func(context.Context) (backend.Status, error) { return backend.StatusUp, nil },
		DialFunc: func(context.Context, string) (driverv1connect.DriverServiceClient, error) {
			return nil, errors.New("unreachable")
		},
	}
	husk := &backend.SandboxMock{
		StatusFunc: func(context.Context) (backend.Status, error) { return backend.StatusSuspended, nil },
	}
	be := &backend.BackendMock{
		OpenFunc: func(h backend.Handle) (backend.Sandbox, error) {
			if h.ID == "1" {
				return up, nil
			}
			return husk, nil
		},
	}
	// The server is unreachable until the test lets it answer.
	reachable := make(chan struct{})
	client := &gritzclient.ClientMock{
		GetTaskFunc: func(context.Context, *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
			return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Status: gritzv1.TaskStatus_COMPLETED}}, nil
		},
		ListRunnerEventsFunc: func(context.Context, *gritzv1.ListRunnerEventsRequest) (*gritzv1.ListRunnerEventsResponse, error) {
			select {
			case <-reachable:
			default:
				return nil, errors.New("unreachable")
			}
			return &gritzv1.ListRunnerEventsResponse{
				Events: []*gritzv1.Event{
					{Id: 4, TaskId: 2, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_NONE, Version: 1}}},
					{Id: 5, TaskId: 3, Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{Command: gritzv1.TaskCommand_START, Version: 2}}},
				},
				NextPageToken: "5",
			}, nil
		},
	}
	runners := make(chan *TaskRunner, 3)
	tasklog := taskevents.New(taskevents.Options{Store: store, Client: client, RunnerID: "r", PollInterval: 10 * time.Millisecond})
	m, err := NewTaskManager(TaskManagerOptions{
		Client:      client,
		Backend:     be,
		Workspaces:  &workspace.Config{},
		Concurrency: 2,
		RunnerID:    "r",
		Queue:       NewRunnerEventOutbox(RunnerEventOutboxOptions{Store: store, Client: client, Backoff: backoff.NewConstantBackOff(0), Log: slog.Default()}),
		TaskLog:     tasklog,
		RunnerStore: store,
		NewTaskRunner: func(opts TaskRunnerOptions) *TaskRunner {
			tr := NewTaskRunner(opts)
			runners <- tr
			return tr
		},
	})
	assert.NilError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// Act - run while the server is unreachable, then let it answer.
	assert.NilError(t, m.Load(ctx))
	go func() { done <- m.Run(ctx) }()
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		if len(client.ListRunnerEventsCalls()) < 2 {
			return poll.Continue("no retried sync yet")
		}
		return poll.Success()
	})
	var trs []*TaskRunner
	for range 2 {
		trs = append(trs, <-runners)
	}
	for _, tr := range trs {
		_, seq := tr.latest()
		assert.Equal(t, seq, uint64(0), "task %d reported before the first sync", tr.opts.TaskID)
	}
	close(reachable)
	<-tasklog.Synced()
	trs = append(trs, <-runners)

	// Assert - each TaskRunner got the synced state, once.
	want := map[int64]runnerstore.TaskChange{
		1: {ID: 1, Command: gritzv1.TaskCommand_START, Version: 1},
		2: {ID: 2, Command: gritzv1.TaskCommand_NONE, Version: 1},
		3: {ID: 3, Command: gritzv1.TaskCommand_START, Version: 2},
	}
	for _, tr := range trs {
		poll.WaitOn(t, func(poll.LogT) poll.Result {
			if _, seq := tr.latest(); seq == 0 {
				return poll.Continue("task %d not reported yet", tr.opts.TaskID)
			}
			return poll.Success()
		})
		c, seq := tr.latest()
		assert.DeepEqual(t, c, want[tr.opts.TaskID])
		assert.Equal(t, seq, uint64(1))
	}
	// Task 2's husk was never booted for the start it already ran.
	assert.Assert(t, cmp.Len(husk.BootCalls(), 0))
}

// TestTaskManagerRegisterWorkspaces checks that the runner registers each
// workspace with the task variables it declares.
func TestTaskManagerRegisterWorkspaces(t *testing.T) {
	t.Parallel()
	// Arrange
	store, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })
	client := &gritzclient.ClientMock{
		RegisterWorkspacesFunc: func(context.Context, *gritzv1.RegisterWorkspacesRequest) (*gritzv1.RegisterWorkspacesResponse, error) {
			return &gritzv1.RegisterWorkspacesResponse{}, nil
		},
	}
	m, err := NewTaskManager(TaskManagerOptions{
		Client:  client,
		Backend: &backend.BackendMock{},
		Workspaces: &workspace.Config{Workspaces: map[string]workspace.Workspace{
			"story-agent": {
				Description: "Stories",
				Variables: map[string]workspace.VariableConfig{
					"JIRA_ISSUE": {Description: "The Jira story", Required: true},
					"BRANCH":     {Default: "main"},
				},
			},
		}},
		Concurrency: 1,
		RunnerID:    "r",
		Queue:       NewRunnerEventOutbox(RunnerEventOutboxOptions{Store: store, Client: client, Backoff: backoff.NewConstantBackOff(0), Log: slog.Default()}),
		TaskLog:     taskevents.New(taskevents.Options{Store: store, Client: client}),
		RunnerStore: store,
	})
	assert.NilError(t, err)

	// Act
	assert.NilError(t, m.RegisterWorkspaces(t.Context()))

	// Assert - the declarations, sorted by name.
	calls := client.RegisterWorkspacesCalls()
	assert.Assert(t, cmp.Len(calls, 1))
	assert.DeepEqual(t, calls[0].RegisterWorkspacesRequest, &gritzv1.RegisterWorkspacesRequest{
		RunnerId: "r",
		Workspaces: []*gritzv1.RegisteredWorkspace{{
			Name:        "story-agent",
			Description: "Stories",
			Variables: []*gritzv1.VariableDecl{
				{Name: "BRANCH", Default: "main"},
				{Name: "JIRA_ISSUE", Description: "The Jira story", Required: true},
			},
		}},
	}, protocmp.Transform())
}

// TestTaskManagerRun_Fatal covers a durable write failure in a RunLoop. A
// stop for a task with no sandbox has no reconciliation path on restart (Load
// has nothing to probe and cannot re-derive "stopped"), so a "stopped" that
// cannot be persisted would leave the task cancelling forever. The runner
// terminates instead: die cancels the root context with a FatalError cause,
// which Run returns.
func TestTaskManagerRun_Fatal(t *testing.T) {
	t.Parallel()
	// Arrange - the event log holds a stop for task 7, which has no sandbox,
	// and the outbox's store is closed, so persisting its "stopped" fails.
	store, err := runnerstore.Open(filepath.Join(t.TempDir(), "db"))
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })
	_, err = store.AppendEvents(t.Context(), []*gritzv1.Event{{
		Id:     1,
		TaskId: 7,
		Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{
			Command: gritzv1.TaskCommand_STOP,
			Version: 3,
		}},
	}}, "1")
	assert.NilError(t, err)
	closed, err := runnerstore.Open(filepath.Join(t.TempDir(), "outbox.db"))
	assert.NilError(t, err)
	assert.NilError(t, closed.Close())
	client := &gritzclient.ClientMock{
		ListRunnerEventsFunc: func(context.Context, *gritzv1.ListRunnerEventsRequest) (*gritzv1.ListRunnerEventsResponse, error) {
			return &gritzv1.ListRunnerEventsResponse{}, nil
		},
	}
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	m, err := NewTaskManager(TaskManagerOptions{
		Client:      client,
		Backend:     &backend.BackendMock{},
		Workspaces:  &workspace.Config{},
		Concurrency: 1,
		RunnerID:    "r",
		Queue:       NewRunnerEventOutbox(RunnerEventOutboxOptions{Store: closed, Client: client, Backoff: backoff.NewConstantBackOff(0), Log: slog.Default()}),
		TaskLog:     taskevents.New(taskevents.Options{Store: store, Client: client, RunnerID: "r"}),
		RunnerStore: store,
		Fatal:       cancel,
	})
	assert.NilError(t, err)

	// Act
	assert.NilError(t, m.Load(ctx))
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()

	// Assert - Run returns the FatalError the runner was terminated with.
	select {
	case err := <-done:
		var fatal FatalError
		assert.Assert(t, errors.As(err, &fatal), "got %v", err)
		assert.ErrorContains(t, err, "database is closed")
		assert.Equal(t, err, context.Cause(ctx))
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}
}
