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
