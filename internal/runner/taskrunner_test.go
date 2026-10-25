package runner_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"

	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/backend/fake"
	"github.com/gritzapp/gritz/internal/runner/taskstate"
	"github.com/gritzapp/gritz/internal/runner/testrunner"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// restoredRunner returns the TaskRunner of rec's task, managed by a test
// manager on be and restored from rec as Load does, along with the manager.
// rec's sandbox must be in be; restoring an up one claims the TaskRunner for
// its Run. tune, when set, is ManagerOptions.TaskRunner.
func restoredRunner(t *testing.T, be *fake.Backend, rec taskstate.Record, tune func(*runner.TaskRunnerOptions)) (*runner.TaskRunner, *testrunner.Manager) {
	t.Helper()
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be, Store: testrunner.NewStore(t, rec), TaskRunner: tune})
	tr := m.Runner(rec.TaskID)
	_, err := tr.Restore(t.Context(), rec)
	assert.NilError(t, err)
	return tr, m
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
	be := testrunner.NewBackend(t, nil)
	store := testrunner.NewStore(t)
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
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3, Agent: &agent.Config{Type: agent.TypeDummy}})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	be.Add("c1", fake.Up)
	tr, m := restoredRunner(t, be, taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"}, nil)

	// Act
	err := tr.Run(t.Context(), nil)

	// Assert - the driver reported, so no event is owed; the sandbox is parked
	// after Finished.
	assert.NilError(t, err)
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Parked)
	assert.Equal(t, sbx.Parks, 1)
	assert.Equal(t, m.Queued(t), 0)
}

func TestTaskRunnerRun_Reattach(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going until it is stopped.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3, Agent: testrunner.SleepForever})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	be.Add("c1", fake.Up)
	tr, m := restoredRunner(t, be, taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"}, nil)
	done := runAsync(t.Context(), tr, nil)
	testrunner.WaitRuns(t, be, "c1", 1)

	// Act - cut the attached stream, wait for Run to reattach, then stop the
	// run.
	be.Drop("c1")
	testrunner.WaitRuns(t, be, "c1", 2)
	testrunner.StopRun(t, client, 3)

	// Assert - the reattached stream saw Finished: the driver reported, the
	// sandbox is parked, and no event is owed.
	assert.NilError(t, waitDone(t, done))
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Parked)
	assert.Equal(t, m.Queued(t), 0)
}

func TestTaskRunnerRun_DriverUnreachable(t *testing.T) {
	t.Parallel()
	// Arrange - a sandbox that stays up while its driver never answers.
	be := testrunner.NewBackend(t, nil)
	be.Add("c1", fake.Up)
	tr, m := restoredRunner(t, be, taskstate.Record{TaskID: 2, Version: 4, Type: "fake", ID: "c1"}, func(opts *runner.TaskRunnerOptions) {
		opts.LostTimeout = 100 * time.Millisecond
	})

	// Act
	err := tr.Run(t.Context(), nil)

	// Assert - Run gave up: the sandbox is parked, and the runner's fallback
	// "failed" is scoped to the run's version.
	assert.NilError(t, err)
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Parked)
	m.WaitQueued(t, 1)
	assert.DeepEqual(t, m.Submitted(t), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 2, Version: 4, Reason: "driver unreachable"},
	}, protocmp.Transform())
}

func TestTaskRunnerRun_NoSuchRun(t *testing.T) {
	t.Parallel()
	// Arrange - a driver that has never run, so attaching to version 3 is
	// NotFound.
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3})})
	be.Add("c1", fake.Up)
	tr, m := restoredRunner(t, be, taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"}, nil)

	// Act
	err := tr.Run(t.Context(), nil)

	// Assert - nothing runs on the sandbox, so it is parked and the run is
	// reported failed.
	assert.NilError(t, err)
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Parked)
	m.WaitQueued(t, 1)
	assert.DeepEqual(t, m.Submitted(t), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 1, Version: 3, Reason: "driver has no such run"},
	}, protocmp.Transform())
}

// TestTaskRunnerRun_NotReady covers a run started on a sandbox whose driver
// never comes up: the run is reported failed and the sandbox parked.
func TestTaskRunnerRun_NotReady(t *testing.T) {
	t.Parallel()
	// Arrange
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewDriver(t, nil), DriverNotReady: true})
	be.Add("c1", fake.Up)
	tr, m := restoredRunner(t, be, taskstate.Record{TaskID: 1, Version: 1, Type: "fake", ID: "c1"}, func(opts *runner.TaskRunnerOptions) {
		opts.ReadyTimeout = 100 * time.Millisecond
	})

	// Act
	err := tr.Run(t.Context(), &driverv1.RunSpec{TaskId: 1})

	// Assert
	assert.NilError(t, err)
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Parked)
	m.WaitQueued(t, 1)
	events := m.Submitted(t)
	assert.Assert(t, cmp.Len(events, 1))
	assert.Equal(t, events[0].Event, "failed")
	assert.Equal(t, events[0].Version, int64(1))
	assert.Assert(t, cmp.Contains(events[0].Reason, "driver server did not become ready"))
}

// finishingDriver is a driver that accepts every Run and finishes it at
// once, reported.
type finishingDriver struct {
	driverv1connect.UnimplementedDriverServiceHandler
}

func (finishingDriver) Status(context.Context, *driverv1.StatusRequest) (*driverv1.StatusResponse, error) {
	return &driverv1.StatusResponse{}, nil
}

func (finishingDriver) Run(_ context.Context, req *driverv1.RunRequest, stream *connect.ServerStream[driverv1.RunResponse]) error {
	if err := stream.Send(&driverv1.RunResponse{Event: &driverv1.RunResponse_Accepted{Accepted: &driverv1.Accepted{}}}); err != nil {
		return err
	}
	return stream.Send(&driverv1.RunResponse{Event: &driverv1.RunResponse_Finished{Finished: &driverv1.Finished{Version: req.GetVersion(), Reported: true}}})
}

// TestTaskRunnerRun_Started covers a run started with a spec: the stream that
// starts it is the one Run follows to its Finished.
func TestTaskRunnerRun_Started(t *testing.T) {
	t.Parallel()
	// Arrange
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewStubDriver(t, finishingDriver{})})
	be.Add("c1", fake.Up)
	tr, m := restoredRunner(t, be, taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"}, nil)

	// Act
	err := tr.Run(t.Context(), &driverv1.RunSpec{TaskId: 1})

	// Assert - one Run stream, with the spec; the driver reported, and the
	// sandbox is parked after Finished.
	assert.NilError(t, err)
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.Runs, 1)
	assert.Assert(t, cmp.Len(sbx.RunSpecs, 1))
	assert.Equal(t, sbx.State, fake.Parked)
	assert.Equal(t, m.Queued(t), 0)
}

// TestTaskRunnerRun_StartSandboxDown covers a run started on a sandbox that is
// not up: the start fails at once rather than after ReadyTimeout.
func TestTaskRunnerRun_StartSandboxDown(t *testing.T) {
	t.Parallel()
	// Arrange - a parked sandbox, with a ReadyTimeout well past waitDone's.
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewStubDriver(t, finishingDriver{})})
	be.Add("c1", fake.Parked)
	tr, m := restoredRunner(t, be, taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"}, func(opts *runner.TaskRunnerOptions) {
		opts.ReadyTimeout = time.Hour
	})

	// Act
	err := waitDone(t, runAsync(t.Context(), tr, &driverv1.RunSpec{TaskId: 1}))

	// Assert - the run is reported failed, and nothing was started.
	assert.NilError(t, err)
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.Runs, 0)
	m.WaitQueued(t, 1)
	events := m.Submitted(t)
	assert.Assert(t, cmp.Len(events, 1))
	assert.Equal(t, events[0].Event, "failed")
	assert.Equal(t, events[0].Version, int64(3))
	assert.Assert(t, cmp.Contains(events[0].Reason, "sandbox is parked"))
}

func TestTaskRunnerRun_Shutdown(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3, Agent: testrunner.SleepForever})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	be.Add("c1", fake.Up)
	tr, m := restoredRunner(t, be, taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"}, nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := runAsync(ctx, tr, nil)
	testrunner.WaitRuns(t, be, "c1", 1)

	// Act - the runner shuts down.
	cancel()

	// Assert - Run returns ctx's error, so the manager keeps the slot, and the
	// sandbox stays up for next-boot rehydration: no park, no event.
	assert.ErrorIs(t, waitDone(t, done), context.Canceled)
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Up)
	assert.Equal(t, sbx.Parks, 0)
	assert.Equal(t, m.Queued(t), 0)
}

func TestTaskRunnerRun_Destroyed(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going, on a recorded sandbox.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3, Agent: testrunner.SleepForever})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	be.Add("c1", fake.Up)
	tr, m := restoredRunner(t, be, taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"}, nil)
	done := runAsync(t.Context(), tr, nil)
	testrunner.WaitRuns(t, be, "c1", 1)

	// Act - the sandbox is destroyed under the run.
	assert.NilError(t, tr.Destroy(t.Context()))

	// Assert - Run is not shutting down, so it returns no error; the run is
	// lost, and the task's record and sandbox are gone.
	assert.NilError(t, waitDone(t, done))
	assert.DeepEqual(t, be.Snapshot().Destroyed, []string{"c1"})
	m.WaitQueued(t, 1)
	assert.DeepEqual(t, m.Submitted(t), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 1, Version: 3, Reason: "sandbox removed before the run finished"},
	}, protocmp.Transform())
	m.AssertTracked(t, 1, nil)
}

// TestTaskRunnerRun_DestroyedBeforeRun covers a TaskRunner destroyed after
// Start claimed it but before its Run began: the run is lost without being
// started.
func TestTaskRunnerRun_DestroyedBeforeRun(t *testing.T) {
	t.Parallel()
	// Arrange - a parked sandbox, started and then destroyed.
	be := testrunner.NewBackend(t, nil)
	be.Add("c1", fake.Parked)
	tr, m := restoredRunner(t, be, taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"}, nil)
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
	m.WaitQueued(t, 1)
	assert.DeepEqual(t, m.Submitted(t), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 1, Reason: "sandbox removed before the run finished"},
	}, protocmp.Transform())
	m.AssertTracked(t, 1, nil)
}

// TestTaskRunnerStart covers starting runs on a TaskRunner: Start stamps the
// record with the run's version, and refuses a second run while the first is
// claimed.
func TestTaskRunnerStart(t *testing.T) {
	t.Parallel()
	// Arrange
	be := testrunner.NewBackend(t, nil)
	be.Add("c1", fake.Parked)
	tr, m := restoredRunner(t, be, taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"}, nil)

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
	m.AssertRecord(t, 1, &taskstate.Record{TaskID: 1, Version: 4, Type: "fake", ID: "c1"})
}

// TestTaskRunnerStart_Create covers starting a TaskRunner with no sandbox:
// Start creates one from the spec, records its handle, and starts it.
func TestTaskRunnerStart_Create(t *testing.T) {
	t.Parallel()
	// Arrange
	be := testrunner.NewBackend(t, nil)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})
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
	m.AssertRecord(t, 1, &taskstate.Record{TaskID: 1, Version: 4, Type: "fake", ID: "c1"})
}

// TestTaskRunnerStart_StartFails covers a sandbox Start creates but cannot
// start: the error is returned, and the TaskRunner keeps the sandbox and its
// record, unclaimed.
func TestTaskRunnerStart_StartFails(t *testing.T) {
	t.Parallel()
	// Arrange - the sandbox Start creates refuses to start.
	boom := errors.New("boom")
	be := testrunner.NewBackend(t, nil)
	be.FailNext(fake.OpStart, "", boom)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})

	// Act
	err := m.Runner(1).Start(t.Context(), backend.Spec{TaskID: 1, Version: 4})

	// Assert - the record surviving the failed start shows it was written
	// before the sandbox was started.
	assert.ErrorIs(t, err, boom)
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Created)
	assert.Equal(t, sbx.Starts, 0)
	m.AssertTracked(t, 1, &taskstate.Record{TaskID: 1, Version: 4, Type: "fake", ID: "c1"})
}

// TestTaskRunnerStart_Concurrent covers two Starts racing on a task with no
// sandbox: one sandbox is created and started, and the other Start finds it
// claimed.
func TestTaskRunnerStart_Concurrent(t *testing.T) {
	t.Parallel()
	// Arrange
	be := testrunner.NewBackend(t, nil)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})

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
	m.AssertRecord(t, 1, &taskstate.Record{TaskID: 1, Version: 4, Type: "fake", ID: "c1"})
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
			be := testrunner.NewBackend(t, nil)
			if tt.state != 0 {
				be.Add("c1", tt.state)
			}
			if tt.openErr != nil {
				be.FailNext(fake.OpOpen, "c1", tt.openErr)
			}
			if tt.statusErr != nil {
				be.FailNext(fake.OpStatus, "c1", tt.statusErr)
			}
			store := testrunner.NewStore(t, rec)
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
				testrunner.AssertRecord(t, store, rec.TaskID, &rec)
			} else {
				testrunner.AssertRecord(t, store, rec.TaskID, nil)
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

// TestTaskRunnerSend_Shutdown covers a lost run with nothing handling events:
// Run's send returns once ctx is done, dropping the event.
func TestTaskRunnerSend_Shutdown(t *testing.T) {
	t.Parallel()
	// Arrange - a run followed on an up sandbox by a TaskRunner whose events
	// nobody reads.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3, Agent: testrunner.SleepForever})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	be.Add("c1", fake.Up)
	rec := taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"}
	tr := runner.NewTaskRunner(runner.TaskRunnerOptions{
		TaskID:  1,
		Backend: be,
		Store:   testrunner.NewStore(t, rec),
		Events:  make(chan runner.TaskEvent),
	})
	_, err := tr.Restore(t.Context(), rec)
	assert.NilError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := runAsync(ctx, tr, nil)
	testrunner.WaitRuns(t, be, "c1", 1)
	// The sandbox stops under the run. Run parks it, which is the last thing
	// it does before sending the lost run.
	be.Set("c1", fake.Parked)
	_, err = be.Wait(t.Context(), func(s fake.Snapshot) bool {
		sbx, _ := s.Sandbox("c1")
		return sbx.Parks == 1
	})
	assert.NilError(t, err)

	// Act - the runner shuts down.
	cancel()

	// Assert - Run returned without an error: the run was lost, not shut
	// down, and its event was dropped.
	assert.NilError(t, waitDone(t, done))
}
