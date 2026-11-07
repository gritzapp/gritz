package scenario_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/gritzapp/gritz/internal/e2e"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/x/testx"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/durationpb"
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
	assert.DeepEqual(t,
		task.Get(),
		&gritzv1.Task{
			Id:          task.ID,
			Runner:      e2e.DefaultRunner,
			Workspace:   e2e.DefaultWorkspace,
			Status:      gritzv1.TaskStatus_RUNNING,
			Command:     gritzv1.TaskCommand_START,
			Version:     1,
			Actions:     &gritzv1.TaskActions{Start: true, Restart: true, Cancel: true},
			AutoArchive: durationpb.New(0),
		},
		protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.Task{}, "created_at", "updated_at"),
	)

	h.Runner.Restart()
	run1.Complete()

	// The pending start survived the dropped stream.
	run2 := h.Agent.WaitRun(task.ID)
	assert.Assert(t, cmp.Contains(run2.Prompt, "more"))
	run2.Complete()
	got := task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
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

func TestRunnerRestart_CancelledSandboxGone(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	run1 := h.Agent.WaitRun(task.ID)

	// While no runner is running, the sandbox vanishes and the task is
	// cancelled.
	h.Runner.Stop()
	h.Backend.DestroySandbox(task.ID)
	run1.WaitCancelled()
	task.Cancel()
	h.Runner.Start()

	// No driver is left to stop the run, so the runner reported it stopped.
	got := task.WaitStatus(gritzv1.TaskStatus_CANCELLED)
	assert.Equal(t, got.Version, int64(1))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 1))
}

func TestRunnerRestart_RestartCancelled(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	run1 := h.Agent.WaitRun(task.ID)

	// While no runner is running, the task is restarted and cancelled, so
	// the new runner gets the restart and the stop together.
	h.Runner.Stop()
	task.Restart()
	task.Cancel()
	h.Runner.Start()

	// The driver stopped run 1 and never started run 2, so the runner
	// reported run 2 stopped.
	run1.WaitCancelled()
	got := task.WaitStatus(gritzv1.TaskStatus_CANCELLED)
	assert.Equal(t, got.Version, int64(2))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 1))
}

func TestRunnerRestart_RestartCancelledAfterRun(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	run1 := h.Agent.WaitRun(task.ID)

	// While no runner is running, the task is restarted and cancelled, and
	// run 1 finishes, so the driver is idle when the new runner attaches.
	h.Runner.Stop()
	task.Restart()
	task.Cancel()
	run1.Complete()
	h.Runner.Start()

	// Run 2 never started, so the runner reported it stopped.
	got := task.WaitStatus(gritzv1.TaskStatus_CANCELLED)
	assert.Equal(t, got.Version, int64(2))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 1))
}

func TestRunnerRestart_DriverNeverRan(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
		Draft:        true,
	})
	// The runner boots the sandbox but can't reach its driver, and stops
	// before it does: the driver is up and has never run.
	m := &backend.SandboxMock{
		DialFunc: func(context.Context, string) (driverv1connect.DriverServiceClient, error) {
			return nil, errors.New("e2e: dial failed")
		},
	}
	h.Backend.MockSandbox(task.ID, m)
	task.Update(&gritzv1.UpdateTaskRequest{Start: true})
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		if len(m.DialCalls()) == 0 {
			return poll.Continue("no dial yet")
		}
		return poll.Success()
	})
	h.Runner.Stop()
	h.Backend.MockSandbox(task.ID, nil)

	h.Runner.Start()

	// The new runner attached to the idle driver without failing a run it
	// never had, and started run 1 on it.
	h.Agent.WaitRun(task.ID).Complete()
	got := task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, got.Version, int64(1))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 1))
}
