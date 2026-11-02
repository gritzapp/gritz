package scenario_test

import (
	"testing"

	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestStart(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})

	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	run := h.Agent.WaitRun(task.ID)
	h.Backend.WaitSandbox(task.ID, backend.StatusUp)
	assert.Assert(t, cmp.Contains(run.Prompt, "do the thing"))
	run.Complete()

	got := task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, got.Version, int64(1))
	lifecycle := testx.ProtoVariantsOf[*gritzv1.LifecyclePayload](task.Events())
	kinds := testx.ExtractField(lifecycle, "Kind").([]gritzv1.LifecycleKind)
	assert.Assert(t, cmp.Contains(kinds, gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_STARTED))
	assert.Assert(t, cmp.Contains(kinds, gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_EXITED))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 1))
}
