package runner_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"connectrpc.com/connect"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/backend/fake"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/testrunner"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// TestTaskManagerSpec_PreCreatesLogDir asserts the sandbox spec ships a directory
// entry for the driver's /gritz/log parent, created 0777 so a non-root driver
// can write the append-only log file there (the driver's MkdirAll is only a
// fallback).
func TestTaskManagerSpec_PreCreatesLogDir(t *testing.T) {
	t.Parallel()
	// Arrange
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewDriver(t, nil)})
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Backend:   be,
		ServerURL: "http://localhost",
		Workspaces: &workspace.Config{
			Workspaces: map[string]workspace.Workspace{
				"test": {
					Container: workspace.Container{Image: "alpine:latest"},
					Agent:     workspace.Agent{Type: "dummy"},
				},
			},
		},
	})
	m.Run(t)

	// Act
	m.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 1))
	testrunner.WaitSuspended(t, be, "c1")

	// Assert - the spec the sandbox was created from has the log dir.
	specs := be.Snapshot().Specs
	assert.Assert(t, cmp.Len(specs, 1))
	logDir := filepath.Dir(agent.DefaultLogPath)
	var found bool
	for _, f := range specs[0].Files {
		if f.Path == logDir {
			found = true
			assert.Assert(t, f.Dir)
			assert.Equal(t, f.Mode, int64(0o777))
		}
	}
	assert.Assert(t, found, "spec should pre-create the log dir %q", logDir)
}

// TestTaskManagerSpec_Secrets asserts the TaskManager hands the backend the server URL,
// the task token and the declared workspace secrets in their own fields, and
// gives the run the GRITZ_* environment the agent's tools read. The secrets stay
// out of Env: the driver adds them to the run's environment and masks them in
// the log it ships.
func TestTaskManagerSpec_Secrets(t *testing.T) {
	t.Parallel()
	// Arrange
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewDriver(t, nil)})
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Backend:   be,
		ServerURL: "http://localhost",
		Workspaces: &workspace.Config{
			Workspaces: map[string]workspace.Workspace{
				"test": {
					Container: workspace.Container{Image: "alpine:latest"},
					Agent:     workspace.Agent{Type: "dummy"},
					Secrets: map[string]string{
						"NPM_TOKEN": "npm_secretvalue",
						"GH_TOKEN":  "gho_secretvalue",
					},
				},
			},
		},
	})
	m.Run(t)

	// Act
	m.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 1))
	testrunner.WaitSuspended(t, be, "c1")

	// Assert
	specs := be.Snapshot().Specs
	assert.Assert(t, cmp.Len(specs, 1))
	spec := specs[0]
	assert.Equal(t, spec.ServerURL, "http://localhost")
	assert.Equal(t, spec.Token, "t")
	assert.DeepEqual(t, spec.Env, map[string]string{
		"GRITZ_TASK_ID": "1",
		"GRITZ_TOKEN":   "t",
		"GRITZ_SERVER":  "http://localhost",
	})
	assert.DeepEqual(t, spec.Secrets, map[string]string{
		"GH_TOKEN":  "gho_secretvalue",
		"NPM_TOKEN": "npm_secretvalue",
	})
}

// TestTaskManagerRunSpec_RunEnv asserts a backend's RunEnv reaches the run's
// environment under the TaskManager's own.
func TestTaskManagerRunSpec_RunEnv(t *testing.T) {
	t.Parallel()
	// Arrange - a backend whose runs get an environment of their own.
	drv := &specDriver{}
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{
		Driver: testrunner.NewStubDriver(t, drv),
		RunEnv: func(*workspace.Workspace) map[string]string {
			return map[string]string{"FOO": "bar", "GRITZ_TASK_ID": "shadowed"}
		},
	})
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})
	m.Run(t)

	// Act
	m.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 1))
	testrunner.WaitSuspended(t, be, "c1")

	// Assert - the environment of the run the driver was asked to start is
	// RunEnv's, overridden by the TaskManager's.
	specs := drv.started()
	assert.Assert(t, cmp.Len(specs, 1))
	assert.DeepEqual(t, specs[0].GetEnv(), map[string]string{
		"FOO":           "bar",
		"GRITZ_TASK_ID": "1",
		"GRITZ_TOKEN":   "t",
		"GRITZ_SERVER":  "",
	})
}

// specDriver is a driver that records the spec of every stream it is opened
// with, and goes idle at once, as after a run of version 1 it reported.
type specDriver struct {
	driverv1connect.UnimplementedDriverServiceHandler

	mu    sync.Mutex
	specs []*driverv1.RunSpec
}

func (d *specDriver) Run(_ context.Context, stream *connect.BidiStream[driverv1.RunRequest, driverv1.RunResponse]) error {
	req, err := stream.Receive()
	if err != nil {
		return err
	}
	if spec := req.GetSpec(); spec != nil {
		d.mu.Lock()
		d.specs = append(d.specs, spec)
		d.mu.Unlock()
	}
	if err := stream.Send(&driverv1.RunResponse{Msg: &driverv1.RunResponse_Hello{Hello: &driverv1.Hello{}}}); err != nil {
		return err
	}
	return stream.Send(&driverv1.RunResponse{Msg: &driverv1.RunResponse_Idle{
		Idle: &driverv1.Idle{Version: 1, Reported: true},
	}})
}

// started returns the spec of every stream d was opened with.
func (d *specDriver) started() []*driverv1.RunSpec {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.specs)
}

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

// TestTaskManagerRunLoop_BootFails covers a sandbox that cannot be booted
// after it is created: the run is reported failed with the record kept, and
// archiving the task destroys the sandbox.
func TestTaskManagerRunLoop_BootFails(t *testing.T) {
	t.Parallel()
	// Arrange - the sandbox the start creates refuses to boot.
	be := testrunner.NewBackend(t, nil)
	be.FailNext(fake.OpBoot, "", errors.New("boom"))
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})
	m.Run(t)

	// Act
	m.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 1))

	// Assert - the run failed with the record and sandbox kept and nothing
	// run. The record surviving the failed boot also shows it was written
	// before the sandbox was booted.
	m.WaitQueued(t, 1)
	assert.DeepEqual(t, m.Submitted(t), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 1, Version: 1, Reason: "boom"},
	}, protocmp.Transform())
	m.AssertRecord(t, 1, &runnerstore.SandboxRecord{TaskID: 1, Version: 1, Handle: backend.Handle{Type: "fake", ID: "c1"}})
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Created)
	assert.Equal(t, sbx.Dials, 0)

	// Act - the task is archived.
	m.Add(testrunner.ArchiveEvent(2, 1))

	// Assert - the TaskRunner still held c1: it is destroyed, and the record
	// is gone.
	testrunner.WaitSandbox(t, be, "c1", func(sbx fake.Info) bool { return sbx.Destroys == 1 })
	m.AssertRecord(t, 1, nil)
}

// TestTaskManagerRunLoop_Gone covers a task whose recorded sandbox has
// vanished: the boot fails with the run reported failed and the record
// dropped, and the next start creates a fresh sandbox.
func TestTaskManagerRunLoop_Gone(t *testing.T) {
	t.Parallel()
	// Arrange - task 8's recorded sandbox, restored suspended, is then removed
	// behind the runner's back.
	rec := runnerstore.SandboxRecord{TaskID: 8, Version: 1, Handle: backend.Handle{Type: "fake", ID: "c1"}}
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{
		Driver: testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3}),
	})
	be.Add("c1", fake.Suspended)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Backend:     be,
		RunnerStore: testrunner.NewRunnerStore(t, rec),
	})
	assert.NilError(t, m.Load(t.Context()))
	be.Set("c1", fake.Gone)
	m.Run(t)

	// Act
	m.Add(testrunner.CommandEvent(1, 8, gritzv1.TaskCommand_START, 2))

	// Assert - the run failed, no fresh sandbox was created, and the
	// dangling record was removed.
	m.WaitQueued(t, 1)
	assert.DeepEqual(t, m.Submitted(t), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 8, Version: 2, Reason: backend.ErrGone.Error()},
	}, protocmp.Transform())
	assert.Assert(t, cmp.Len(be.Snapshot().Specs, 0))
	m.AssertRecord(t, 8, nil)

	// Act - the task is started again.
	m.Add(testrunner.CommandEvent(2, 8, gritzv1.TaskCommand_START, 3))

	// Assert - the task got one fresh sandbox, which ran the workspace's
	// agent; the driver reported, and the sandbox is suspended. The runner
	// never destroyed the vanished sandbox; it only dropped it.
	testrunner.WaitSuspended(t, be, "c2")
	snap := be.Snapshot()
	assert.Assert(t, cmp.Len(snap.Specs, 1))
	assert.Equal(t, snap.Specs[0].Version, int64(3))
	assert.Assert(t, cmp.Len(snap.Destroyed, 0))
	assert.Equal(t, m.Queued(t), 0)
	m.AssertRecord(t, 8, &runnerstore.SandboxRecord{TaskID: 8, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c2"}})
}
