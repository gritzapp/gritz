package scenario_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gritzapp/gritz/internal/e2e/harness"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
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
	// failed on the same run, with no command left.
	h.Backend.WaitSandbox(task.ID, backend.StatusGone)
	h.Org.WaitEvent(task.ID, func(e *gritzv1.Event) bool {
		return e.GetLifecycle().GetKind() == gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_RESET
	})
	got := task.Get()
	assert.Equal(t, got.Status, gritzv1.TaskStatus_FAILED)
	assert.Equal(t, got.Command, gritzv1.TaskCommand_NONE)
	assert.Equal(t, got.Version, int64(1))
	assert.Assert(t, cmp.Len(m.BootCalls(), 1))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 0))

	// Restarted, the task runs in a sandbox created and provisioned for it.
	h.Backend.MockSandbox(task.ID, nil)
	task.Restart()
	h.Agent.WaitRun(task.ID).Complete()
	got = task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, got.Version, int64(2))
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

	// The second reset of the same run is carried out and reported too, or
	// its command would never be cleared.
	h.Org.WaitEvent(task.ID, func(e *gritzv1.Event) bool {
		return e.Id > first.Id && e.GetLifecycle().GetKind() == gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_RESET
	})
	got := task.Get()
	assert.Equal(t, got.Status, gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, got.Command, gritzv1.TaskCommand_NONE)
	assert.Equal(t, got.Version, int64(1))
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
	// The first destroy hangs until released, then fails and removes the
	// mock, so the next one is real.
	release := make(chan struct{})
	m := &backend.SandboxMock{
		DestroyFunc: func(ctx context.Context) error {
			select {
			case <-release:
			case <-ctx.Done():
			}
			h.Backend.MockSandbox(task.ID, nil)
			return errors.New("e2e: destroy failed")
		},
	}
	h.Backend.MockSandbox(task.ID, m)

	task.ResetSandbox()

	// The runner reports the reset before it destroys the sandbox, so the
	// command is cleared while the destroy hangs.
	h.Org.WaitEvent(task.ID, func(e *gritzv1.Event) bool {
		return e.GetLifecycle().GetKind() == gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_RESET
	})
	assert.Equal(t, task.Get().Command, gritzv1.TaskCommand_NONE)

	// The restart waits for the destroy to be retried rather than booting
	// the old sandbox, and runs in a fresh one.
	task.Restart()
	close(release)
	h.Agent.WaitRun(task.ID).Complete()
	got := task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, got.Version, int64(2))
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
