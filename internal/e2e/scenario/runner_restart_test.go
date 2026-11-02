package scenario_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
	"gotest.tools/v3/poll"
)

func TestRunnerRestart_MidRun(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	run1 := h.Agent.WaitRun(task.ID)

	h.Runner.Restart()
	task.Cancel()

	// The new runner reattached to the driver and carried the cancel to it,
	// without starting the run again.
	run1.WaitCancelled()
	task.WaitStatus(gritzv1.TaskStatus_CANCELLED)
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 1))
	lifecycle := testx.ProtoVariantsOf[*gritzv1.LifecyclePayload](task.Events())
	kinds := testx.ExtractField(lifecycle, "Kind").([]gritzv1.LifecycleKind)
	started := slices.DeleteFunc(kinds, func(k gritzv1.LifecycleKind) bool {
		return k != gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_STARTED
	})
	assert.Assert(t, cmp.Len(started, 1))
}

func TestRunnerRestart_PendingWake(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	run1 := h.Agent.WaitRun(task.ID)
	task.Update(&gritzv1.UpdateTaskRequest{
		Start:           true,
		AddInstructions: []*gritzv1.Instruction{{Text: "more"}},
	})
	// The wake landed on the open run.
	got := task.Get()
	assert.Equal(t, got.Status, gritzv1.TaskStatus_RUNNING)
	assert.Equal(t, got.Version, int64(1))
	assert.Equal(t, got.Command, gritzv1.TaskCommand_START)

	h.Runner.Restart()
	run1.Complete()

	// The pending start survived the dropped stream.
	run2 := h.Agent.WaitRun(task.ID)
	assert.Assert(t, cmp.Contains(run2.Prompt, "more"))
	run2.Complete()
	got = task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, got.Version, int64(2))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 2))
}

func TestRunnerRestart_SandboxGone(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	run1 := h.Agent.WaitRun(task.ID)

	// The sandbox vanishes while no runner is watching, so no driver is left
	// to report its run.
	h.Runner.Stop()
	h.Backend.DestroySandbox(task.ID)
	run1.WaitCancelled()
	h.Runner.Start()

	// The new runner found the sandbox gone and the task still running, and
	// reported the run failed, without starting it again.
	got := task.WaitStatus(gritzv1.TaskStatus_FAILED)
	assert.Equal(t, got.Version, int64(1))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 1))
}

func TestRunnerRestart_DriverKilled(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	run1 := h.Agent.WaitRun(task.ID)

	// The driver dies while no runner is watching, leaving its sandbox
	// suspended and its run unreported.
	h.Runner.Stop()
	h.Backend.KillDriver(task.ID)
	run1.WaitCancelled()
	h.Runner.Start()

	// The new runner found the sandbox suspended and the task still running,
	// and reported the run failed before booting the sandbox again.
	got := task.WaitStatus(gritzv1.TaskStatus_FAILED)
	assert.Equal(t, got.Version, int64(1))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 1))
	lifecycle := testx.ProtoVariantsOf[*gritzv1.LifecyclePayload](task.Events())
	failed := slices.DeleteFunc(lifecycle, func(lc *gritzv1.LifecyclePayload) bool {
		return lc.GetKind() != gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_FAILED
	})
	assert.DeepEqual(t, testx.ExtractField(failed, "Message"), []string{"sandbox exited"})
}

func TestRunnerRestart_Archived(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	h.Agent.WaitRun(task.ID).Complete()
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	// The runner can't destroy the sandbox, so the archive is still pending
	// when it stops.
	m := &backend.SandboxMock{
		DestroyFunc: func(context.Context) error { return errors.New("e2e: destroy failed") },
	}
	h.Backend.MockSandbox(task.ID, m)
	task.Archive()
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		if len(m.DestroyCalls()) == 0 {
			return poll.Continue("no destroy yet")
		}
		return poll.Success()
	})
	h.Runner.Stop()
	h.Backend.MockSandbox(task.ID, nil)

	h.Runner.Start()

	// The new runner loaded the archived task and destroyed its sandbox.
	h.Backend.WaitSandbox(task.ID, backend.StatusGone)
}

func TestRunnerRestart_Completed(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{
		Commands: []string{"echo setup >> setup"},
	}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	h.Agent.WaitRun(task.ID).Complete()
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	h.Backend.WaitSandbox(task.ID, backend.StatusSuspended)

	h.Runner.Restart()

	// The new runner kept the suspended sandbox, so a wake boots it again
	// rather than creating another.
	task.Update(&gritzv1.UpdateTaskRequest{
		Start:           true,
		AddInstructions: []*gritzv1.Instruction{{Text: "more"}},
	})
	run2 := h.Agent.WaitRun(task.ID)
	assert.Assert(t, cmp.Contains(run2.Prompt, "more"))
	run2.Complete()
	got := task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, got.Version, int64(2))
	assert.Equal(t, h.Backend.ReadFile(task.ID, "setup"), "setup\n")
}
