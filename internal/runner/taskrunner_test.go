package runner

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
	"gotest.tools/v3/poll"

	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/taskevents"
)

// testTaskRunner is a TaskRunner for task 1 over be, with an outbox nothing
// delivers, so the runner events it reports stay in store.
type testTaskRunner struct {
	*TaskRunner
	be    *backend.BackendMock
	store *runnerstore.Store
	slots *Slots
}

func newTestTaskRunner(t *testing.T, be *backend.BackendMock) *testTaskRunner {
	t.Helper()
	store, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, store.Close()) })
	client := &gritzclient.ClientMock{}
	slots := NewSlots(1)
	tr := NewTaskRunner(TaskRunnerOptions{
		TaskID:  1,
		Backend: be,
		Store:   store,
		Slots:   slots,
		Queue:   NewRunnerEventOutbox(RunnerEventOutboxOptions{Store: store, Client: client}),
		Stream:  taskevents.New(taskevents.Options{Store: store, Client: client}).Stream(1),
		Spec: func(_ context.Context, c runnerstore.TaskChange) (*backend.Spec, *driverv1.RunSpec, error) {
			return &backend.Spec{TaskID: c.ID, Version: c.Version}, &driverv1.RunSpec{}, nil
		},
	})
	return &testTaskRunner{TaskRunner: tr, be: be, store: store, slots: slots}
}

// restore gives the task the suspended sandbox sbx, recorded at version, as
// Load would.
func (tr *testTaskRunner) restore(t *testing.T, sbx *backend.SandboxMock, version int64) {
	t.Helper()
	rec := runnerstore.SandboxRecord{TaskID: 1, Version: version, Handle: backend.Handle{ID: "old"}}
	assert.NilError(t, tr.store.WriteSandbox(t.Context(), rec))
	sbx.HandleFunc = func() backend.Handle { return rec.Handle }
	sbx.StatusFunc = func(context.Context) (backend.Status, error) { return backend.StatusSuspended, nil }
	tr.be.OpenFunc = func(backend.Handle) (backend.Sandbox, error) { return sbx, nil }
	_, err := tr.Restore(t.Context(), rec)
	assert.NilError(t, err)
}

// run runs RunLoop until the test ends, and checks it returned on shutdown.
func (tr *testTaskRunner) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- tr.RunLoop(ctx) }()
	t.Cleanup(func() {
		cancel()
		assert.Equal(t, <-done, context.Canceled)
	})
}

// waitEvents waits for n runner events in the outbox, and removes and
// returns them.
func (tr *testTaskRunner) waitEvents(t *testing.T, n int) []model.RunnerEvent {
	t.Helper()
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		got, err := tr.store.CountRunnerEvents(t.Context())
		if err != nil {
			return poll.Error(err)
		}
		if got < n {
			return poll.Continue("%d of %d runner events", got, n)
		}
		return poll.Success()
	})
	var events []model.RunnerEvent
	for range n {
		seq, ev, ok, err := tr.store.OldestRunnerEvent(t.Context())
		assert.NilError(t, err)
		assert.Assert(t, ok)
		assert.NilError(t, tr.store.DeleteRunnerEvent(t.Context(), seq))
		events = append(events, ev)
	}
	return events
}

func TestTaskRunnerReset(t *testing.T) {
	t.Parallel()
	// Arrange - task 1 failed its run 3, and its sandbox is suspended.
	sbx := &backend.SandboxMock{DestroyFunc: func(context.Context) error { return nil }}
	tr := newTestTaskRunner(t, &backend.BackendMock{})
	tr.restore(t, sbx, 3)
	tr.Change(runnerstore.TaskChange{ID: 1, Command: gritzv1.TaskCommand_RESET, Version: 3})

	// Act
	tr.run(t)

	// Assert - the sandbox and its record are gone, the reset is reported at
	// the run's version, and nothing was booted or held a slot.
	events := tr.waitEvents(t, 1)
	assert.DeepEqual(t, events, []model.RunnerEvent{{TaskID: 1, Event: model.RunnerEventReset, Version: 3}})
	assert.Assert(t, cmp.Len(sbx.DestroyCalls(), 1))
	assert.Assert(t, cmp.Len(sbx.BootCalls(), 0))
	_, ok, err := tr.store.Sandbox(t.Context(), 1)
	assert.NilError(t, err)
	assert.Assert(t, !ok)
	tr.slots.mu.Lock()
	defer tr.slots.mu.Unlock()
	assert.Equal(t, tr.slots.count, int64(0))
}

func TestTaskRunnerReset_NoSandbox(t *testing.T) {
	t.Parallel()
	// Arrange - task 1 has no sandbox on this runner.
	be := &backend.BackendMock{}
	tr := newTestTaskRunner(t, be)
	tr.Change(runnerstore.TaskChange{ID: 1, Command: gritzv1.TaskCommand_RESET, Version: 2})

	// Act
	tr.run(t)

	// Assert - the reset is reported all the same, and the backend untouched.
	events := tr.waitEvents(t, 1)
	assert.DeepEqual(t, events, []model.RunnerEvent{{TaskID: 1, Event: model.RunnerEventReset, Version: 2}})
	assert.Assert(t, cmp.Len(be.CreateCalls(), 0))
}

func TestTaskRunnerReset_DestroyFails(t *testing.T) {
	t.Parallel()
	// Arrange - the first destroy fails; the record is checked at the second.
	tr := newTestTaskRunner(t, &backend.BackendMock{})
	var kept atomic.Bool
	var sbx *backend.SandboxMock
	sbx = &backend.SandboxMock{DestroyFunc: func(context.Context) error {
		if len(sbx.DestroyCalls()) == 1 {
			return errors.New("destroy failed")
		}
		_, ok, _ := tr.store.Sandbox(t.Context(), 1)
		kept.Store(ok)
		return nil
	}}
	tr.restore(t, sbx, 3)
	tr.Change(runnerstore.TaskChange{ID: 1, Command: gritzv1.TaskCommand_RESET, Version: 3})

	// Act
	tr.run(t)

	// Assert - the failed destroy kept the record, and was retried; the
	// reset is reported once, after the destroy that succeeded.
	events := tr.waitEvents(t, 1)
	assert.DeepEqual(t, events, []model.RunnerEvent{{TaskID: 1, Event: model.RunnerEventReset, Version: 3}})
	assert.Assert(t, cmp.Len(sbx.DestroyCalls(), 2))
	assert.Assert(t, kept.Load())
	_, ok, err := tr.store.Sandbox(t.Context(), 1)
	assert.NilError(t, err)
	assert.Assert(t, !ok)
}

func TestTaskRunnerReset_ThenStart(t *testing.T) {
	t.Parallel()
	// Arrange - task 1's sandbox has been reset. The fresh sandbox the next
	// start creates fails to boot, which is all this test needs of it.
	old := &backend.SandboxMock{DestroyFunc: func(context.Context) error { return nil }}
	fresh := &backend.SandboxMock{
		HandleFunc: func() backend.Handle { return backend.Handle{ID: "fresh"} },
		BootFunc:   func(context.Context) error { return errors.New("boot failed") },
	}
	be := &backend.BackendMock{
		CreateFunc: func(context.Context, *backend.Spec) (backend.Sandbox, error) { return fresh, nil },
	}
	tr := newTestTaskRunner(t, be)
	tr.restore(t, old, 3)
	tr.Change(runnerstore.TaskChange{ID: 1, Command: gritzv1.TaskCommand_RESET, Version: 3})
	tr.run(t)
	tr.waitEvents(t, 1)

	// Act
	tr.Change(runnerstore.TaskChange{ID: 1, Command: gritzv1.TaskCommand_RESTART, Version: 4})

	// Assert - the restart booted a sandbox created for it, recorded at its
	// version, not the old one.
	events := tr.waitEvents(t, 1)
	assert.DeepEqual(t, events, []model.RunnerEvent{{TaskID: 1, Event: model.RunnerEventFailed, Version: 4, Reason: "boot failed"}})
	assert.Assert(t, cmp.Len(be.CreateCalls(), 1))
	assert.Assert(t, cmp.Len(fresh.BootCalls(), 1))
	assert.Assert(t, cmp.Len(old.BootCalls(), 0))
	rec, ok, err := tr.store.Sandbox(t.Context(), 1)
	assert.NilError(t, err)
	assert.Assert(t, ok)
	assert.DeepEqual(t, rec, runnerstore.SandboxRecord{TaskID: 1, Version: 4, Handle: backend.Handle{ID: "fresh"}})
}

func TestTaskRunnerReset_Repeated(t *testing.T) {
	t.Parallel()
	// Arrange - task 1's sandbox has been reset, and the user resets it again
	// at the same version: the runner's sync collapsed the clear and the
	// second reset into one report that repeats the first.
	tr := newTestTaskRunner(t, &backend.BackendMock{})
	tr.restore(t, &backend.SandboxMock{DestroyFunc: func(context.Context) error { return nil }}, 3)
	reset := runnerstore.TaskChange{ID: 1, Command: gritzv1.TaskCommand_RESET, Version: 3}
	tr.Change(reset)
	tr.run(t)
	tr.waitEvents(t, 1)

	// Act
	tr.Change(reset)

	// Assert - the second reset is reported too, or its command would never
	// be cleared.
	events := tr.waitEvents(t, 1)
	assert.DeepEqual(t, events, []model.RunnerEvent{{TaskID: 1, Event: model.RunnerEventReset, Version: 3}})
}
