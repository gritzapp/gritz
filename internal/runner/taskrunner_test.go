package runner_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/backend/fake"
	"github.com/gritzapp/gritz/internal/runner/taskstate"
	"github.com/gritzapp/gritz/internal/x/outbox"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// testTaskRunner returns the TaskRunner of rec's task, managed by a test
// manager and restored from rec as Load does, along with the manager, its
// store, client and outbox. rec's sandbox must be in be; restoring an up one
// claims the TaskRunner for its Run.
func testTaskRunner(t *testing.T, be *fake.Backend, rec taskstate.Record, tune ...func(*runner.TaskRunnerOptions)) (*runner.TaskRunner, *runner.TaskManager, *taskstate.Store, *gritzclient.ClientMock, *outbox.Outbox[model.RunnerEvent]) {
	t.Helper()
	store := testStore(t, rec)
	m, mock, queue := testManager(t, be, store, tune...)
	tr := m.Runner(rec.TaskID)
	_, err := tr.Restore(t.Context(), rec)
	assert.NilError(t, err)
	return tr, m, store, mock, queue
}

// runAsync calls tr's Run on its own goroutine, and returns the channel its
// error is sent on.
func runAsync(ctx context.Context, tr *runner.TaskRunner, spec *driverv1.RunSpec) <-chan error {
	done := make(chan error, 1)
	go func() { done <- tr.Run(ctx, spec) }()
	return done
}

// waitDone waits for the error of a Run started with runAsync.
func waitDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

func TestNewTaskRunner(t *testing.T) {
	t.Parallel()
	be := fake.New(fake.Options{})
	store := testStore(t)
	events := make(chan runner.TaskEvent)
	tests := []struct {
		name string
		opts runner.TaskRunnerOptions
	}{
		{name: "no backend", opts: runner.TaskRunnerOptions{TaskID: 1, Store: store, Events: events}},
		{name: "no store", opts: runner.TaskRunnerOptions{TaskID: 1, Backend: be, Events: events}},
		{name: "no events", opts: runner.TaskRunnerOptions{TaskID: 1, Backend: be, Store: store}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Act & Assert
			assert.Assert(t, cmp.Panics(func() { runner.NewTaskRunner(tt.opts) }))
		})
	}
}

func TestTaskRunnerRun_Finished(t *testing.T) {
	t.Parallel()
	// Arrange - a run that finishes on its own, on an up sandbox.
	client := serveDriver(t, 1, 3, &agent.Config{Type: agent.TypeDummy})
	be := newFake(t, client)
	be.Add("c1", fake.Up)
	tr, _, _, _, queue := testTaskRunner(t, be, taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"})

	// Act
	err := tr.Run(t.Context(), nil)

	// Assert - the driver reported, so no event is owed; the sandbox is parked
	// after Finished.
	assert.NilError(t, err)
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Parked)
	assert.Equal(t, sbx.Parks, 1)
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
}

func TestTaskRunnerRun_Reattach(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going until it is stopped.
	client := serveDriver(t, 1, 3, sleepForever)
	be := newFake(t, client)
	be.Add("c1", fake.Up)
	tr, _, _, _, queue := testTaskRunner(t, be, taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"})
	done := runAsync(t.Context(), tr, nil)
	waitDialed(t, be, "c1", 1)

	// Act - drop every connection to the driver, the attached stream with
	// them, until Run has dialed again; then stop the run.
	dialed := func() int {
		sbx, _ := be.Snapshot().Sandbox("c1")
		return sbx.Dials
	}
	dials := dialed()
	deadline := time.Now().Add(10 * time.Second)
	for dialed() == dials {
		if time.Now().After(deadline) {
			t.Fatal("Run did not reattach")
		}
		be.Drop("c1")
		time.Sleep(5 * time.Millisecond)
	}
	stopRun(t, client, 3)

	// Assert - the reattached stream saw Finished: the driver reported, the
	// sandbox is parked, and no event is owed.
	assert.NilError(t, waitDone(t, done))
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Parked)
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
}

func TestTaskRunnerRun_DriverUnreachable(t *testing.T) {
	t.Parallel()
	// Arrange - a sandbox that stays up while its driver never answers.
	be := fake.New(fake.Options{})
	be.Add("c1", fake.Up)
	tr, _, _, mock, queue := testTaskRunner(t, be, taskstate.Record{TaskID: 2, Version: 4, Type: "fake", ID: "c1"}, func(opts *runner.TaskRunnerOptions) {
		opts.LostTimeout = 100 * time.Millisecond
	})

	// Act
	err := tr.Run(t.Context(), nil)

	// Assert - Run gave up: the sandbox is parked, and the runner's fallback
	// "failed" is scoped to the run's version.
	assert.NilError(t, err)
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Parked)
	waitQueued(t, queue, 1)
	assert.DeepEqual(t, submitted(t, mock, queue), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 2, Version: 4, Reason: "driver unreachable"},
	}, protocmp.Transform())
}

func TestTaskRunnerRun_NoSuchRun(t *testing.T) {
	t.Parallel()
	// Arrange - a driver that has never run, so attaching to version 3 is
	// NotFound.
	be := newFake(t, driverClient(t, 3, nil))
	be.Add("c1", fake.Up)
	tr, _, _, mock, queue := testTaskRunner(t, be, taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"})

	// Act
	err := tr.Run(t.Context(), nil)

	// Assert - nothing runs on the sandbox, so it is parked and the run is
	// reported failed.
	assert.NilError(t, err)
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Parked)
	waitQueued(t, queue, 1)
	assert.DeepEqual(t, submitted(t, mock, queue), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 1, Version: 3, Reason: "driver has no such run"},
	}, protocmp.Transform())
}

// TestTaskRunnerRun_NotReady covers a run started on a sandbox whose driver
// never comes up: the run is reported failed and the sandbox parked.
func TestTaskRunnerRun_NotReady(t *testing.T) {
	t.Parallel()
	// Arrange
	be := newFake(t, driverClient(t, 1, make(chan struct{})))
	be.Add("c1", fake.Up)
	tr, _, _, mock, queue := testTaskRunner(t, be, taskstate.Record{TaskID: 1, Version: 1, Type: "fake", ID: "c1"}, func(opts *runner.TaskRunnerOptions) {
		opts.ReadyTimeout = 100 * time.Millisecond
	})

	// Act
	err := tr.Run(t.Context(), &driverv1.RunSpec{TaskId: 1})

	// Assert
	assert.NilError(t, err)
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Parked)
	waitQueued(t, queue, 1)
	events := submitted(t, mock, queue)
	assert.Assert(t, cmp.Len(events, 1))
	assert.Equal(t, events[0].Event, "failed")
	assert.Equal(t, events[0].Version, int64(1))
	assert.Assert(t, cmp.Contains(events[0].Reason, "driver server did not become ready"))
}

func TestTaskRunnerRun_Shutdown(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going.
	client := serveDriver(t, 1, 3, sleepForever)
	be := newFake(t, client)
	be.Add("c1", fake.Up)
	tr, _, _, _, queue := testTaskRunner(t, be, taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"})
	ctx, cancel := context.WithCancel(t.Context())
	done := runAsync(ctx, tr, nil)
	waitDialed(t, be, "c1", 1)

	// Act - the runner shuts down.
	cancel()

	// Assert - Run returns ctx's error, so the manager keeps the slot, and the
	// sandbox stays up for next-boot rehydration: no park, no event.
	assert.ErrorIs(t, waitDone(t, done), context.Canceled)
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Up)
	assert.Equal(t, sbx.Parks, 0)
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
}

func TestTaskRunnerRun_Destroyed(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going, on a recorded sandbox.
	client := serveDriver(t, 1, 3, sleepForever)
	be := newFake(t, client)
	be.Add("c1", fake.Up)
	tr, m, store, mock, queue := testTaskRunner(t, be, taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"})
	done := runAsync(t.Context(), tr, nil)
	waitDialed(t, be, "c1", 1)

	// Act - the sandbox is destroyed under the run.
	assert.NilError(t, tr.Destroy(t.Context()))

	// Assert - Run is not shutting down, so it returns no error; the run is
	// lost, and the task's record and sandbox are gone.
	assert.NilError(t, waitDone(t, done))
	assert.DeepEqual(t, be.Snapshot().Destroyed, []string{"c1"})
	waitQueued(t, queue, 1)
	assert.DeepEqual(t, submitted(t, mock, queue), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 1, Version: 3, Reason: "sandbox removed before the run finished"},
	}, protocmp.Transform())
	assertTracked(t, m, be, store, 1, nil)
}

// TestTaskRunnerRun_DestroyedBeforeRun covers a TaskRunner destroyed after
// Start claimed it but before its Run began: the run is lost without being
// started.
func TestTaskRunnerRun_DestroyedBeforeRun(t *testing.T) {
	t.Parallel()
	// Arrange - a parked sandbox, started and then destroyed.
	be := fake.New(fake.Options{})
	be.Add("c1", fake.Parked)
	tr, m, store, mock, queue := testTaskRunner(t, be, taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"})
	assert.NilError(t, tr.Start(t.Context(), backend.Spec{TaskID: 1, Version: 4}))
	assert.NilError(t, tr.Destroy(t.Context()))

	// Act
	err := tr.Run(t.Context(), &driverv1.RunSpec{TaskId: 1})

	// Assert - Run returned without reaching for the driver: its context was
	// cancelled from the start. Destroy deleted the record, so the lost run
	// has no version.
	assert.NilError(t, err)
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.Dials, 0)
	waitQueued(t, queue, 1)
	assert.DeepEqual(t, submitted(t, mock, queue), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 1, Reason: "sandbox removed before the run finished"},
	}, protocmp.Transform())
	assertTracked(t, m, be, store, 1, nil)
}

// TestTaskRunnerStart covers starting runs on a TaskRunner: Start stamps the
// record with the run's version, and refuses a second run while the first is
// claimed.
func TestTaskRunnerStart(t *testing.T) {
	t.Parallel()
	// Arrange
	be := fake.New(fake.Options{})
	be.Add("c1", fake.Parked)
	tr, _, store, _, _ := testTaskRunner(t, be, taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"})

	// Act
	err := tr.Start(t.Context(), backend.Spec{TaskID: 1, Version: 4})
	again := tr.Start(t.Context(), backend.Spec{TaskID: 1, Version: 5})

	// Assert - the first Start reused c1 and started it; the second, refused,
	// left the record at the first's version. The refusal shows the TaskRunner
	// is claimed, which also rules out probing it.
	assert.NilError(t, err)
	assert.ErrorIs(t, again, runner.ErrRunning)
	snap := be.Snapshot()
	assert.Assert(t, cmp.Len(snap.Specs, 0))
	sbx, _ := snap.Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Up)
	assert.Equal(t, sbx.Starts, 1)
	assertRecord(t, store, 1, &taskstate.Record{TaskID: 1, Version: 4, Type: "fake", ID: "c1"})
}

// TestTaskRunnerStart_Create covers starting a TaskRunner with no sandbox:
// Start creates one from the spec, records its handle, and starts it.
func TestTaskRunnerStart_Create(t *testing.T) {
	t.Parallel()
	// Arrange
	be := fake.New(fake.Options{})
	store := testStore(t)
	m, _, _ := testManager(t, be, store)
	tr := m.Runner(1)

	// Act
	err := tr.Start(t.Context(), backend.Spec{TaskID: 1, Version: 4})

	// Assert - the TaskRunner is claimed, so the store and the fake tell what
	// it did.
	assert.NilError(t, err)
	snap := be.Snapshot()
	assert.DeepEqual(t, snap.Specs, []backend.Spec{{TaskID: 1, Version: 4}})
	sbx, _ := snap.Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Up)
	assert.Equal(t, sbx.Starts, 1)
	assertRecord(t, store, 1, &taskstate.Record{TaskID: 1, Version: 4, Type: "fake", ID: "c1"})
}

// TestTaskRunnerStart_StartFails covers a sandbox Start creates but cannot
// start: the error is returned, and the TaskRunner keeps the sandbox and its
// record, unclaimed.
func TestTaskRunnerStart_StartFails(t *testing.T) {
	t.Parallel()
	// Arrange - the sandbox Start creates refuses to start.
	boom := errors.New("boom")
	be := fake.New(fake.Options{})
	be.FailNext(fake.OpStart, "", boom)
	store := testStore(t)
	m, _, _ := testManager(t, be, store)

	// Act
	err := m.Runner(1).Start(t.Context(), backend.Spec{TaskID: 1, Version: 4})

	// Assert - the record surviving the failed start shows it was written
	// before the sandbox was started.
	assert.ErrorIs(t, err, boom)
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Created)
	assert.Equal(t, sbx.Starts, 0)
	assertTracked(t, m, be, store, 1, &taskstate.Record{TaskID: 1, Version: 4, Type: "fake", ID: "c1"})
}

// TestTaskRunnerStart_Concurrent covers two Starts racing on a task with no
// sandbox: one sandbox is created and started, and the other Start finds it
// claimed.
func TestTaskRunnerStart_Concurrent(t *testing.T) {
	t.Parallel()
	// Arrange
	be := fake.New(fake.Options{})
	store := testStore(t)
	m, _, _ := testManager(t, be, store)

	// Act
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- m.Runner(1).Start(t.Context(), backend.Spec{TaskID: 1, Version: 4}) }()
	}
	first, second := <-errs, <-errs

	// Assert - one Start created and started the sandbox, the other started
	// nothing.
	assert.Assert(t, (first == nil) != (second == nil), "first: %v, second: %v", first, second)
	assert.ErrorIs(t, errors.Join(first, second), runner.ErrRunning)
	snap := be.Snapshot()
	assert.Assert(t, cmp.Len(snap.Specs, 1))
	sbx, _ := snap.Sandbox("c1")
	assert.Equal(t, sbx.Starts, 1)
	assertRecord(t, store, 1, &taskstate.Record{TaskID: 1, Version: 4, Type: "fake", ID: "c1"})
}

// TestTaskRunnerRestore covers adopting a record at Load: an up sandbox is
// claimed for its Run, a parked one is kept unclaimed, a gone or unopenable one
// leaves no sandbox and no record, and a failed status check keeps both.
func TestTaskRunnerRestore(t *testing.T) {
	t.Parallel()
	rec := taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"}
	tests := []struct {
		name string
		// state is the sandbox's state in the fake; 0 means the fake never
		// had it.
		state       fake.State
		openErr     error
		statusErr   error
		wantStatus  backend.Status
		wantErr     bool
		wantSandbox bool // the sandbox and record are kept
		wantClaimed bool // which also shows the sandbox was adopted
	}{
		{name: "up", state: fake.Up, wantStatus: backend.StatusUp, wantSandbox: true, wantClaimed: true},
		{name: "parked", state: fake.Parked, wantStatus: backend.StatusParked, wantSandbox: true},
		{name: "gone", state: fake.Gone, wantStatus: backend.StatusGone},
		{name: "never existed", wantStatus: backend.StatusGone},
		{name: "status error", state: fake.Up, statusErr: errors.New("boom"), wantErr: true, wantSandbox: true},
		{name: "open fails", state: fake.Up, openErr: errors.New("malformed handle"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - a TaskRunner with no sandbox, and a previous process's
			// record.
			be := fake.New(fake.Options{})
			if tt.state != 0 {
				be.Add("c1", tt.state)
			}
			if tt.openErr != nil {
				be.FailNext(fake.OpOpen, "c1", tt.openErr)
			}
			if tt.statusErr != nil {
				be.FailNext(fake.OpStatus, "c1", tt.statusErr)
			}
			store := testStore(t, rec)
			tr := runner.NewTaskRunner(runner.TaskRunnerOptions{
				TaskID:  rec.TaskID,
				Backend: be,
				Store:   store,
				Events:  make(chan runner.TaskEvent, 1),
			})

			// Act
			st, err := tr.Restore(t.Context(), rec)

			// Assert
			if tt.wantErr {
				assert.Assert(t, err != nil)
			} else {
				assert.NilError(t, err)
				assert.Equal(t, st, tt.wantStatus)
			}
			if tt.wantSandbox {
				assertRecord(t, store, rec.TaskID, &rec)
			} else {
				assertRecord(t, store, rec.TaskID, nil)
			}
			// Restore never destroys, even a gone sandbox.
			assert.Assert(t, cmp.Len(be.Snapshot().Destroyed, 0))

			// Probe, last - a Start shows what the TaskRunner holds: claimed
			// refuses, c1 is started, and no sandbox means a Create.
			before := be.Snapshot()
			err = tr.Start(t.Context(), backend.Spec{TaskID: 1, Version: 4})
			after := be.Snapshot()
			was, _ := before.Sandbox("c1")
			now, _ := after.Sandbox("c1")
			switch {
			case tt.wantClaimed:
				assert.ErrorIs(t, err, runner.ErrRunning)
			case tt.wantSandbox:
				assert.NilError(t, err)
				assert.Equal(t, now.Starts, was.Starts+1, "TaskRunner does not hold c1")
				assert.Equal(t, len(after.Specs), 0, "TaskRunner held no sandbox")
			default:
				assert.NilError(t, err)
				assert.Equal(t, len(after.Specs), 1, "TaskRunner kept a sandbox")
			}
		})
	}
}
