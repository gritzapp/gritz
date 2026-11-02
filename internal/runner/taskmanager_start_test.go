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
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/taskevents"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"gotest.tools/v3/assert"
)

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
