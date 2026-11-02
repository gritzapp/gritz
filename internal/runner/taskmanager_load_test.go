package runner_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/testrunner"
	"gotest.tools/v3/assert"
)

// TestTaskManagerRun_Fatal covers a durable write failure in a RunLoop: Run
// returns the FatalError cause it terminated the runner with.
func TestTaskManagerRun_Fatal(t *testing.T) {
	t.Parallel()
	// Arrange - the outbox's store is closed, so the first event the runner
	// persists fails and terminates it.
	ctx, cancelCause := context.WithCancelCause(t.Context())
	defer cancelCause(nil)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Fatal:      cancelCause,
		QueueStore: closedStore(t),
	})
	done := make(chan error, 1)
	go func() { done <- m.TaskManager.Run(ctx) }()

	// Act - a stop command for a task with no sandbox, whose "stopped" cannot
	// be persisted.
	m.Add(testrunner.CommandEvent(1, 7, gritzv1.TaskCommand_STOP, 3))

	// Assert - Run returns the FatalError cause.
	select {
	case err := <-done:
		var fatal runner.FatalError
		assert.Assert(t, errors.As(err, &fatal), "got %v", err)
		assert.ErrorContains(t, err, "database is closed")
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}
}

// TestTaskManagerDie_StopWithoutSandbox is the sharpest unrecoverable case from #1209:
// a stop command with no running sandbox has no reconciliation path on restart
// (Load has nothing to probe and cannot re-derive "stopped"), so a dropped persist
// would leave the task stuck in cancelling forever. When the durable outbox write
// fails there, the runner must crash — die cancels the root ctx with a
// FatalError cause — instead of logging and continuing.
func TestTaskManagerDie_StopWithoutSandbox(t *testing.T) {
	t.Parallel()
	// No record, so there is no run to stop → the TaskManager emits "stopped".
	// Back the outbox with a closed store, so its durable write fails.
	ctx, cancelCause := context.WithCancelCause(t.Context())
	defer cancelCause(nil)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Fatal:      cancelCause,
		QueueStore: closedStore(t),
	})
	m.RunContext(t, ctx)

	// Act - the "stopped" enqueue fails durably, so die fires.
	m.Add(testrunner.CommandEvent(1, 7, gritzv1.TaskCommand_STOP, 3))

	// Assert - the root ctx is cancelled with a FatalError cause, which the
	// command layer maps to a non-zero exit.
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("runner was not terminated")
	}
	var fatal runner.FatalError
	assert.Assert(t, errors.As(context.Cause(ctx), &fatal))
}

// closedStore returns a runner store that is already closed, so every write
// to it fails.
func closedStore(t *testing.T) *runnerstore.Store {
	t.Helper()
	s, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	assert.NilError(t, s.Close())
	return s
}
