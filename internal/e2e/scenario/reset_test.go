package scenario_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/gritzapp/gritz/internal/e2e/harness"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
	"gotest.tools/v3/poll"
)

func TestReset_Failed(t *testing.T) {
	t.Parallel()
	h := harness.New(t, &harness.Options{Runner: &harness.RunnerOptions{
		Commands: []string{"echo setup >> setup"},
	}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
		Draft:        true,
	})
	// The task's sandbox is wedged: every boot fails.
	m := &backend.SandboxMock{
		BootFunc: func(context.Context) error { return errors.New("e2e: boot failed") },
	}
	h.Backend.MockSandbox(task.ID, m)
	task.Update(&gritzv1.UpdateTaskRequest{Start: true})
	task.WaitStatus(gritzv1.TaskStatus_FAILED)

	task.ResetSandbox()

	// The sandbox is gone and nothing was booted, and the task is still
	// failed, at the reset's version, with no command left.
	h.Backend.WaitSandbox(task.ID, backend.StatusGone)
	h.Org.WaitEvent(task.ID, func(e *gritzv1.Event) bool {
		return e.GetLifecycle().GetKind() == gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_RESET
	})
	got := task.Get()
	assert.Equal(t, got.Status, gritzv1.TaskStatus_FAILED)
	assert.Equal(t, got.Command, gritzv1.TaskCommand_NONE)
	assert.Equal(t, got.Version, int64(2))
	assert.Assert(t, cmp.Len(m.BootCalls(), 1))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 0))

	// Restarted, the task runs in a sandbox created and provisioned for it.
	h.Backend.MockSandbox(task.ID, nil)
	task.Restart()
	h.Agent.WaitRun(task.ID).Complete()
	got = task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, got.Version, int64(3))
	assert.Equal(t, h.Backend.ReadFile(task.ID, "setup"), "setup\n")
}

func TestReset_NoSandbox(t *testing.T) {
	t.Parallel()
	h := harness.New(t, &harness.Options{Runner: &harness.RunnerOptions{}})
	h.Backend.Mock(&backend.BackendMock{
		CreateFunc: func(context.Context, *backend.Spec) (backend.Sandbox, error) {
			return nil, errors.New("e2e: create failed")
		},
	})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	task.WaitStatus(gritzv1.TaskStatus_FAILED)
	h.Backend.Mock(nil)

	task.ResetSandbox()

	// There was no sandbox to delete, and the reset completes all the same.
	h.Org.WaitEvent(task.ID, func(e *gritzv1.Event) bool {
		return e.GetLifecycle().GetKind() == gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_RESET
	})
	got := task.Get()
	assert.Equal(t, got.Status, gritzv1.TaskStatus_FAILED)
	assert.Equal(t, got.Command, gritzv1.TaskCommand_NONE)
}

func TestReset_Repeated(t *testing.T) {
	t.Parallel()
	h := harness.New(t, &harness.Options{Runner: &harness.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	h.Agent.WaitRun(task.ID).Complete()
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	task.ResetSandbox()
	first := h.Org.WaitEvent(task.ID, func(e *gritzv1.Event) bool {
		return e.GetLifecycle().GetKind() == gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_RESET
	})

	task.ResetSandbox()

	// The second reset, at a newer version, is carried out and reported too,
	// or its command would never be cleared.
	h.Org.WaitEvent(task.ID, func(e *gritzv1.Event) bool {
		return e.Id > first.Id && e.GetLifecycle().GetKind() == gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_RESET
	})
	got := task.Get()
	assert.Equal(t, got.Status, gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, got.Command, gritzv1.TaskCommand_NONE)
	assert.Equal(t, got.Version, int64(3))
}

func TestReset_DestroyFails(t *testing.T) {
	t.Parallel()
	h := harness.New(t, &harness.Options{Runner: &harness.RunnerOptions{
		// Writes an id unique to the sandbox it provisions.
		Commands: []string{"cat /proc/sys/kernel/random/uuid > sandbox"},
	}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	h.Agent.WaitRun(task.ID).Complete()
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	old := h.Backend.ReadFile(task.ID, "sandbox")
	// The first destroy hangs until released, then destroys the sandbox but
	// fails all the same, and removes the mock, so the retry is real and
	// finds the sandbox already gone.
	release := make(chan struct{})
	m := &backend.SandboxMock{
		DestroyFunc: func(ctx context.Context) error {
			select {
			case <-release:
			case <-ctx.Done():
			}
			h.Backend.MockSandbox(task.ID, nil)
			h.Backend.DestroySandbox(task.ID)
			return errors.New("e2e: destroy failed")
		},
	}
	h.Backend.MockSandbox(task.ID, m)

	task.ResetSandbox()

	// The reset is not reported while the destroy is in flight.
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		if len(m.DestroyCalls()) == 0 {
			return poll.Continue("no destroy yet")
		}
		return poll.Success()
	})
	assert.Equal(t, task.Get().Command, gritzv1.TaskCommand_RESET)

	// Once the retried destroy succeeds, it is.
	close(release)
	h.Org.WaitEvent(task.ID, func(e *gritzv1.Event) bool {
		return e.GetLifecycle().GetKind() == gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_RESET
	})
	assert.Equal(t, task.Get().Command, gritzv1.TaskCommand_NONE)

	// Restarted, the task runs in a fresh sandbox.
	task.Restart()
	h.Agent.WaitRun(task.ID).Complete()
	got := task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, got.Version, int64(3))
	assert.Assert(t, cmp.Len(m.DestroyCalls(), 1))
	assert.Assert(t, h.Backend.ReadFile(task.ID, "sandbox") != old)
}

func TestReset_RunnerRestart(t *testing.T) {
	t.Parallel()
	h := harness.New(t, &harness.Options{Runner: &harness.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	h.Agent.WaitRun(task.ID).Complete()
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	h.Backend.WaitSandbox(task.ID, backend.StatusSuspended)
	h.Runner.Stop()
	task.ResetSandbox()

	h.Runner.Start()

	// The new runner loaded the task and carried out the reset it missed.
	h.Backend.WaitSandbox(task.ID, backend.StatusGone)
	h.Org.WaitEvent(task.ID, func(e *gritzv1.Event) bool {
		return e.GetLifecycle().GetKind() == gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_RESET
	})
	assert.Equal(t, task.Get().Command, gritzv1.TaskCommand_NONE)
}

func TestReset_RunnerRestart_DestroyFails(t *testing.T) {
	t.Parallel()
	h := harness.New(t, &harness.Options{Runner: &harness.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	h.Agent.WaitRun(task.ID).Complete()
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	// The runner can't destroy the sandbox, so the reset is still pending
	// when it stops.
	m := &backend.SandboxMock{
		DestroyFunc: func(context.Context) error { return errors.New("e2e: destroy failed") },
	}
	h.Backend.MockSandbox(task.ID, m)
	task.ResetSandbox()
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		if len(m.DestroyCalls()) == 0 {
			return poll.Continue("no destroy yet")
		}
		return poll.Success()
	})
	h.Runner.Stop()
	h.Backend.MockSandbox(task.ID, nil)
	assert.Equal(t, task.Get().Command, gritzv1.TaskCommand_RESET)

	h.Runner.Start()

	// The new runner loaded the task, destroyed its sandbox and reported the
	// reset once.
	h.Backend.WaitSandbox(task.ID, backend.StatusGone)
	h.Org.WaitTask(task.ID, func(got *gritzv1.Task) bool { return got.Command == gritzv1.TaskCommand_NONE })
	resets := slices.DeleteFunc(task.Events(), func(e *gritzv1.Event) bool {
		return e.GetLifecycle().GetKind() != gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_RESET
	})
	assert.Assert(t, cmp.Len(resets, 1))
}
