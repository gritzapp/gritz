package scenario_test

import (
	"strings"
	"testing"

	"github.com/gritzapp/gritz/internal/e2e/harness"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// lifecycleOf returns the messages of the task's lifecycle entries of kind.
func lifecycleOf(task *harness.Task, kind gritzv1.LifecycleKind) []string {
	var msgs []string
	for _, lc := range testx.ProtoVariantsOf[*gritzv1.LifecyclePayload](task.Events()) {
		if lc.GetKind() == kind {
			msgs = append(msgs, lc.GetMessage())
		}
	}
	return msgs
}

func TestAutoReset(t *testing.T) {
	t.Parallel()
	h := harness.New(t, &harness.Options{Runner: &harness.RunnerOptions{
		AutoReset: true,
		// Writes an id unique to the sandbox it provisions.
		Commands: []string{"cat /proc/sys/kernel/random/uuid > sandbox"},
	}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	h.Agent.WaitRun(task.ID).Complete()
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	old := h.Backend.ReadFile(task.ID, "sandbox")
	h.Backend.WaitSandbox(task.ID, backend.StatusSuspended)
	h.Backend.DestroySandbox(task.ID)

	task.Restart()

	// The run completed in a fresh sandbox, and the timeline says why.
	h.Agent.WaitRun(task.ID).Complete()
	got := task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, got.Version, int64(2))
	assert.Assert(t, h.Backend.ReadFile(task.ID, "sandbox") != old)
	resets := lifecycleOf(task, gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_RESET)
	assert.Assert(t, cmp.Len(resets, 1))
	assert.Assert(t, strings.HasPrefix(resets[0], "previous sandbox gone: "), resets[0])
	assert.Assert(t, cmp.Len(lifecycleOf(task, gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_FAILED), 0))
}

func TestAutoReset_Off(t *testing.T) {
	t.Parallel()
	h := harness.New(t, &harness.Options{Runner: &harness.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	h.Agent.WaitRun(task.ID).Complete()
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	h.Backend.WaitSandbox(task.ID, backend.StatusSuspended)
	h.Backend.DestroySandbox(task.ID)

	task.Restart()

	// The run failed with the gone error, without a sandbox reset.
	got := task.WaitStatus(gritzv1.TaskStatus_FAILED)
	assert.Equal(t, got.Version, int64(2))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 1))
	assert.DeepEqual(t, lifecycleOf(task, gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_FAILED), []string{backend.ErrGone.Error()})
	assert.Assert(t, cmp.Len(lifecycleOf(task, gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_RESET), 0))
}

func TestAutoReset_RunnerRestart(t *testing.T) {
	t.Parallel()
	h := harness.New(t, &harness.Options{Runner: &harness.RunnerOptions{
		AutoReset: true,
		// Writes an id unique to the sandbox it provisions.
		Commands: []string{"cat /proc/sys/kernel/random/uuid > sandbox"},
	}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	h.Agent.WaitRun(task.ID).Complete()
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	old := h.Backend.ReadFile(task.ID, "sandbox")
	h.Backend.WaitSandbox(task.ID, backend.StatusSuspended)
	// The sandbox vanishes while the task is idle and no runner is watching.
	h.Runner.Stop()
	h.Backend.DestroySandbox(task.ID)
	h.Runner.Start()

	task.Restart()

	// The new runner kept the gone sandbox, so the run's boot found it gone
	// and replaced it, as it does without a runner restart.
	h.Agent.WaitRun(task.ID).Complete()
	got := task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, got.Version, int64(2))
	assert.Assert(t, h.Backend.ReadFile(task.ID, "sandbox") != old)
	resets := lifecycleOf(task, gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_RESET)
	assert.Assert(t, cmp.Len(resets, 1))
	assert.Assert(t, strings.HasPrefix(resets[0], "previous sandbox gone: "), resets[0])
}
