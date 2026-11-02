package runner_test

import (
	"errors"
	"testing"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/backend/fake"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/testrunner"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
)

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
