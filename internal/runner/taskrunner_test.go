package runner_test

import (
	"context"
	"errors"
	"slices"
	"sync"
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
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/testrunner"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// c1Record is task 1's record of sandbox c1, at version 3.
var c1Record = runnerstore.SandboxRecord{TaskID: 1, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c1"}}

// loaded returns a test manager on be that has loaded rec, a record of an
// up sandbox, with its task's stored events: Load starts the task's RunLoop,
// which drives the sandbox first. ctx is Load's. tune, when set, is
// ManagerOptions.TaskRunner. The events are stored as Replicated does, so
// the manager is not to be Run.
func loaded(t *testing.T, ctx context.Context, be *fake.Backend, rec runnerstore.SandboxRecord, tune func(*runner.TaskRunnerOptions), events ...*gritzv1.Event) *testrunner.Manager {
	t.Helper()
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Backend:     be,
		RunnerStore: testrunner.NewRunnerStore(t, rec),
		TaskRunner:  tune,
	})
	if len(events) > 0 {
		m.Replicated(t, events...)
	}
	assert.NilError(t, m.Load(ctx))
	return m
}

func TestNewTaskRunner(t *testing.T) {
	t.Parallel()
	be := testrunner.NewBackend(t, nil)
	store := testrunner.NewRunnerStore(t)
	tests := []struct {
		name string
		opts runner.TaskRunnerOptions
	}{
		{name: "no backend", opts: runner.TaskRunnerOptions{TaskID: 1, Store: store}},
		{name: "no store", opts: runner.TaskRunnerOptions{TaskID: 1, Backend: be}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Act & Assert
			assert.Assert(t, cmp.Panics(func() { runner.NewTaskRunner(tt.opts) }))
		})
	}
}

func TestTaskRunnerRunLoop_Finished(t *testing.T) {
	t.Parallel()
	// Arrange - a run that finishes on its own, on an up sandbox.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{
		Version: 3,
		Agent:   &agent.Config{Type: agent.TypeDummy},
	})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	be.Add("c1", fake.Up)

	// Act
	m := loaded(t, t.Context(), be, c1Record, nil)

	// Assert - the driver reported, so no event is owed; the sandbox is
	// suspended once the driver is idle.
	sbx := testrunner.WaitSuspended(t, be, "c1")
	assert.Equal(t, sbx.Suspends, 1)
	assert.Equal(t, m.Queued(t), 0)
}

// positionDriver is a driver whose Hello says it has stored the events up
// to helloID. It records the events the runner forwards, and ends the run,
// reported, once it has the one with lastID.
type positionDriver struct {
	driverv1connect.UnimplementedDriverServiceHandler
	helloID, lastID int64

	mu       sync.Mutex
	received []int64
}

func (d *positionDriver) Run(_ context.Context, stream *connect.BidiStream[driverv1.RunRequest, driverv1.RunResponse]) error {
	if _, err := stream.Receive(); err != nil {
		return err
	}
	if err := stream.Send(&driverv1.RunResponse{Msg: &driverv1.RunResponse_Hello{
		Hello: &driverv1.Hello{LastEventId: d.helloID},
	}}); err != nil {
		return err
	}
	for {
		req, err := stream.Receive()
		if err != nil {
			return err
		}
		last := int64(0)
		d.mu.Lock()
		for _, ev := range req.GetEvents().GetEvents() {
			d.received = append(d.received, ev.GetId())
			last = ev.GetId()
		}
		d.mu.Unlock()
		if last == d.lastID {
			return stream.Send(&driverv1.RunResponse{Msg: &driverv1.RunResponse_Idle{
				Idle: &driverv1.Idle{Version: 3, Reported: true},
			}})
		}
	}
}

// forwarded returns the ids of the events d received.
func (d *positionDriver) forwarded() []int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.received)
}

func TestTaskRunnerRunLoop_ForwardsEvents(t *testing.T) {
	t.Parallel()
	// Arrange - a driver that has stored the task's events up to id 2, and
	// a replica with two more, and one of another task's.
	drv := &positionDriver{helloID: 2, lastID: 4}
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewStubDriver(t, drv)})
	be.Add("c1", fake.Up)

	// Act
	m := loaded(t, t.Context(), be, c1Record, nil,
		testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 3),
		testrunner.LifecycleEvent(2, 1, gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_STARTED),
		testrunner.CommandEvent(10, 2, gritzv1.TaskCommand_START, 1),
		testrunner.LifecycleEvent(3, 1, gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_EXITED),
		testrunner.CommandEvent(4, 1, gritzv1.TaskCommand_NONE, 3),
	)

	// Assert - the runner sent the task's events after the driver's position,
	// and nothing else; the busy period ended on the driver's Idle.
	testrunner.WaitSuspended(t, be, "c1")
	assert.DeepEqual(t, drv.forwarded(), []int64{3, 4})
	assert.Equal(t, m.Queued(t), 0)
}

// TestTaskRunnerRunLoop_Reattach covers a dropped stream: drive reopens it,
// and the busy period ends on the reopened stream's Idle.
func TestTaskRunnerRunLoop_Reattach(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going until it is stopped.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{
		Version: 3,
		Agent:   testrunner.SleepForever,
	})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	be.Add("c1", fake.Up)
	server := testrunner.NewServer(testrunner.ServerOptions{})
	server.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 3))
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Backend:     be,
		Server:      server,
		RunnerStore: testrunner.NewRunnerStore(t, c1Record),
	})
	assert.NilError(t, m.Load(t.Context()))
	m.Run(t)
	testrunner.WaitRuns(t, be, "c1", 1)

	// Act - cut the attached stream, wait for drive to reattach, then stop
	// the run.
	be.Drop("c1")
	testrunner.WaitRuns(t, be, "c1", 2)
	m.Add(testrunner.CommandEvent(2, 1, gritzv1.TaskCommand_STOP, 3))

	// Assert - the reattached stream saw Idle: the driver reported, the
	// sandbox is suspended, and no event is owed.
	testrunner.WaitSuspended(t, be, "c1")
	assert.DeepEqual(t, client.Reported(), []string{"started", "stopped"})
	assert.Equal(t, m.Queued(t), 0)
}

func TestTaskRunnerRunLoop_DriverUnreachable(t *testing.T) {
	t.Parallel()
	// Arrange - a sandbox that stays up while its driver never answers.
	be := testrunner.NewBackend(t, nil)
	be.Add("c1", fake.Up)

	// Act
	m := loaded(t, t.Context(), be, runnerstore.SandboxRecord{
		TaskID:  2,
		Version: 4,
		Handle:  backend.Handle{Type: "fake", ID: "c1"},
	}, func(opts *runner.TaskRunnerOptions) {
		opts.LostTimeout = 100 * time.Millisecond
	})

	// Assert - drive gave up: the sandbox is suspended, and the runner's
	// fallback "failed" is scoped to the run's version.
	testrunner.WaitSuspended(t, be, "c1")
	m.WaitQueued(t, 1)
	assert.DeepEqual(t, m.Submitted(t), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 2, Version: 4, Reason: "driver unreachable"},
	}, protocmp.Transform())
}

func TestTaskRunnerRunLoop_NothingToRun(t *testing.T) {
	t.Parallel()
	// Arrange - a driver that has never run, and has nothing to run.
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{
		Driver: testrunner.NewDriver(t, nil),
	})
	be.Add("c1", fake.Up)

	// Act
	m := loaded(t, t.Context(), be, c1Record, nil)

	// Assert - the driver went idle at once: the sandbox is suspended, and
	// no event is owed.
	testrunner.WaitSuspended(t, be, "c1")
	assert.Equal(t, m.Queued(t), 0)
}

// idleDriver is a driver whose streams each go idle at once, after the run
// whose version is next in versions, which it reported unless unreported is
// set. It records the spec of each stream.
type idleDriver struct {
	driverv1connect.UnimplementedDriverServiceHandler
	unreported bool

	mu       sync.Mutex
	versions []int64
	specs    []*driverv1.RunSpec
}

func (d *idleDriver) Run(_ context.Context, stream *connect.BidiStream[driverv1.RunRequest, driverv1.RunResponse]) error {
	req, err := stream.Receive()
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.specs = append(d.specs, req.GetSpec())
	version := d.versions[0]
	d.versions = d.versions[1:]
	d.mu.Unlock()
	if err := stream.Send(&driverv1.RunResponse{Msg: &driverv1.RunResponse_Hello{Hello: &driverv1.Hello{}}}); err != nil {
		return err
	}
	return stream.Send(&driverv1.RunResponse{Msg: &driverv1.RunResponse_Idle{
		Idle: &driverv1.Idle{Version: version, Reported: !d.unreported},
	}})
}

// opened returns the spec of every stream d was opened with.
func (d *idleDriver) opened() []*driverv1.RunSpec {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.specs)
}

func TestTaskRunnerRunLoop_Reopen(t *testing.T) {
	t.Parallel()
	// Arrange - a driver that goes idle after run 3 before the task's start
	// of run 4 reaches it, and after run 4 on the next stream.
	drv := &idleDriver{versions: []int64{3, 4}}
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewStubDriver(t, drv)})
	be.Add("c1", fake.Up)

	// Act
	m := loaded(t, t.Context(), be, c1Record, nil, testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 4))

	// Assert - drive reopened the stream with run 4's spec instead of
	// returning, with the record moved to run 4; then the sandbox was
	// suspended once.
	sbx := testrunner.WaitSuspended(t, be, "c1")
	assert.Equal(t, sbx.Suspends, 1)
	specs := drv.opened()
	assert.Assert(t, cmp.Len(specs, 2))
	assert.Assert(t, specs[0] == nil)
	assert.Equal(t, specs[1].GetTaskId(), int64(1))
	assert.Equal(t, m.Queued(t), 0)
	m.AssertRecord(t, 1, &runnerstore.SandboxRecord{TaskID: 1, Version: 4, Handle: backend.Handle{Type: "fake", ID: "c1"}})
}

func TestTaskRunnerRunLoop_StopNeverRun(t *testing.T) {
	t.Parallel()
	// Arrange - a driver that goes idle after run 3, and a stop of run 4,
	// a restart cancelled before the driver started it.
	drv := &idleDriver{versions: []int64{3}}
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewStubDriver(t, drv)})
	be.Add("c1", fake.Up)

	// Act
	m := loaded(t, t.Context(), be, c1Record, nil, testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_STOP, 4))

	// Assert - no run will report run 4, so the runner reported it stopped.
	m.WaitQueued(t, 1)
	assert.DeepEqual(t, m.Submitted(t), []*gritzv1.RunnerEvent{
		{Event: "stopped", TaskId: 1, Version: 4},
	}, protocmp.Transform())
}

func TestTaskRunnerRunLoop_NotReported(t *testing.T) {
	t.Parallel()
	// Arrange - a driver whose run 3 failed to report its outcome.
	drv := &idleDriver{versions: []int64{3}, unreported: true}
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewStubDriver(t, drv)})
	be.Add("c1", fake.Up)

	// Act
	m := loaded(t, t.Context(), be, c1Record, nil)

	// Assert - the runner reported run 3 failed, and suspended the sandbox.
	testrunner.WaitSuspended(t, be, "c1")
	m.WaitQueued(t, 1)
	assert.DeepEqual(t, m.Submitted(t), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 1, Version: 3, Reason: "driver did not report the run's outcome"},
	}, protocmp.Transform())
}

func TestTaskRunnerRunLoop_Shutdown(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{
		Version: 3,
		Agent:   testrunner.SleepForever,
	})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	be.Add("c1", fake.Up)
	ctx, cancel := context.WithCancel(t.Context())
	m := loaded(t, ctx, be, c1Record, nil)
	done := make(chan error, 1)
	go func() { done <- m.TaskManager.Run(ctx) }()
	testrunner.WaitRuns(t, be, "c1", 1)

	// Act - the runner shuts down; Run returns once every RunLoop has.
	cancel()
	assert.ErrorIs(t, <-done, context.Canceled)

	// Assert - the sandbox stays up for next-boot rehydration: no suspend,
	// no event.
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Up)
	assert.Equal(t, sbx.Suspends, 0)
	assert.Equal(t, m.Queued(t), 0)
}

// TestTaskRunnerRestore covers adopting a record at Load: an up or suspended
// sandbox is kept, a gone or unopenable one leaves no record, and a failed
// status check keeps it.
func TestTaskRunnerRestore(t *testing.T) {
	t.Parallel()
	rec := c1Record
	tests := []struct {
		name string
		// state is the sandbox's state in the fake; 0 means the fake never
		// had it.
		state      fake.State
		openErr    error
		statusErr  error
		wantStatus backend.Status
		wantErr    bool
		wantRecord bool
	}{
		{name: "up", state: fake.Up, wantStatus: backend.StatusUp, wantRecord: true},
		{name: "suspended", state: fake.Suspended, wantStatus: backend.StatusSuspended, wantRecord: true},
		{name: "gone", state: fake.Gone, wantStatus: backend.StatusGone},
		{name: "never existed", wantStatus: backend.StatusGone},
		{
			name:       "status error",
			state:      fake.Up,
			statusErr:  errors.New("boom"),
			wantErr:    true,
			wantRecord: true,
		},
		{
			name:    "open fails",
			state:   fake.Up,
			openErr: errors.New("malformed handle"),
			wantErr: true,
		},
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
			store := testrunner.NewRunnerStore(t, rec)
			tr := runner.NewTaskRunner(runner.TaskRunnerOptions{
				TaskID:  rec.TaskID,
				Backend: be,
				Store:   store,
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
			if tt.wantRecord {
				testrunner.AssertRecord(t, store, rec.TaskID, &rec)
			} else {
				testrunner.AssertRecord(t, store, rec.TaskID, nil)
			}
			// Restore never destroys, even a gone sandbox.
			assert.Assert(t, cmp.Len(be.Snapshot().Destroyed, 0))
		})
	}
}
