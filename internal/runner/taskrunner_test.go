package runner_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	"connectrpc.com/connect"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/backend/fake"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/testrunner"
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

// idleDriver is a driver whose streams each go idle at once, after the run
// whose version is next in versions, which it reported. It records the spec
// of each stream.
type idleDriver struct {
	driverv1connect.UnimplementedDriverServiceHandler

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
		Idle: &driverv1.Idle{Version: version, Reported: true},
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
