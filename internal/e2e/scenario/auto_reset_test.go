package scenario_test

import (
	"testing"

	"github.com/gritzapp/gritz/internal/e2e/harness"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

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
	resets := testx.ProtoVariantsOf[*gritzv1.LifecyclePayload](task.FilterEvents(harness.EventFilter{
		LifecycleKinds: []gritzv1.LifecycleKind{gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_RESET},
	}))
	assert.DeepEqual(t, testx.ExtractField(resets, "Message"), []string{"previous sandbox gone: " + backend.ErrGone.Error()})
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

	// The run failed with the gone error.
	got := task.WaitStatus(gritzv1.TaskStatus_FAILED)
	assert.Equal(t, got.Version, int64(2))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 1))
	failed := testx.ProtoVariantsOf[*gritzv1.LifecyclePayload](task.FilterEvents(harness.EventFilter{
		LifecycleKinds: []gritzv1.LifecycleKind{gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_FAILED},
	}))
	assert.DeepEqual(t, testx.ExtractField(failed, "Message"), []string{backend.ErrGone.Error()})
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
	resets := testx.ProtoVariantsOf[*gritzv1.LifecyclePayload](task.FilterEvents(harness.EventFilter{
		LifecycleKinds: []gritzv1.LifecycleKind{gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_RESET},
	}))
	assert.DeepEqual(t, testx.ExtractField(resets, "Message"), []string{"previous sandbox gone: " + backend.ErrGone.Error()})
}
