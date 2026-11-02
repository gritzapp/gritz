package runner_test

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/backend/fake"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/taskevents"
	"github.com/gritzapp/gritz/internal/runner/testrunner"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
)

func TestTaskManagerRunLoop_StopWithoutSandbox(t *testing.T) {
	t.Parallel()
	// Arrange - task being cancelled with no tracked sandbox.
	be := testrunner.NewBackend(t, nil)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})
	m.Run(t)

	// Act
	m.Add(testrunner.CommandEvent(1, 7, gritzv1.TaskCommand_STOP, 3))

	// Assert - no Run is open, so there is no driver to stop and the
	// TaskManager emits "stopped" itself.
	m.WaitQueued(t, 1)
	assert.DeepEqual(t, m.Submitted(t), []*gritzv1.RunnerEvent{
		{Event: "stopped", TaskId: 7, Version: 3},
	}, protocmp.Transform())
}

// TestTaskManagerRunLoop_RestartCancelled covers a restart cancelled before
// the driver started it: the driver stops run 3 and never starts run 4, so
// the runner reports run 4 stopped, as no run will.
func TestTaskManagerRunLoop_RestartCancelled(t *testing.T) {
	t.Parallel()
	// Arrange - run 3 keeps going on c1, and Load reattaches to it.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{
		Version: 3,
		Agent:   testrunner.SleepForever,
	})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	be.Add("c1", fake.Up)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Backend:     be,
		RunnerStore: testrunner.NewRunnerStore(t, runnerstore.SandboxRecord{TaskID: 1, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c1"}}),
	})
	m.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 3))
	assert.NilError(t, m.Load(t.Context()))
	testrunner.WaitRuns(t, be, "c1", 1)
	m.Run(t)

	// Act - the task is restarted and cancelled in one page: the stop
	// carries the restart's version.
	m.Add(
		testrunner.CommandEvent(2, 1, gritzv1.TaskCommand_RESTART, 4),
		testrunner.CommandEvent(3, 1, gritzv1.TaskCommand_STOP, 4),
	)

	// Assert - run 4 never ran, and the runner reported it stopped.
	testrunner.WaitSuspended(t, be, "c1")
	m.WaitQueued(t, 1)
	assert.DeepEqual(t, client.Reported(), []string{"started", "stopped"})
	assert.DeepEqual(t, m.Submitted(t), []*gritzv1.RunnerEvent{
		{Event: "stopped", TaskId: 1, Version: 4},
	}, protocmp.Transform())
	m.AssertRecord(t, 1, &runnerstore.SandboxRecord{TaskID: 1, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c1"}})
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
	store, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
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
	m, err := runner.NewTaskManager(runner.TaskManagerOptions{
		Client:      client,
		Backend:     &backend.BackendMock{},
		Workspaces:  &workspace.Config{},
		Concurrency: 1,
		RunnerID:    "r",
		Queue:       runner.NewRunnerEventOutbox(runner.RunnerEventOutboxOptions{Store: closed, Client: client, Backoff: backoff.NewConstantBackOff(0), Log: slog.Default()}),
		TaskLog:     taskevents.New(taskevents.Options{Store: store, Client: client, RunnerID: "r"}),
		RunnerStore: store,
		Fatal:       cancel,
	})
	assert.NilError(t, err)

	// Act
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()

	// Assert - Run returns the FatalError the runner was terminated with.
	select {
	case err := <-done:
		var fatal runner.FatalError
		assert.Assert(t, errors.As(err, &fatal), "got %v", err)
		assert.ErrorContains(t, err, "database is closed")
		assert.Equal(t, err, context.Cause(ctx))
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}
}
