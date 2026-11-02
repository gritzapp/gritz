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
	"gotest.tools/v3/assert"
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

	// Assert - the reattached run holds the only slot.
	assert.Assert(t, !m.slots.sem.TryAcquire(1))
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
