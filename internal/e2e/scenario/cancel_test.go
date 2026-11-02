package scenario_test

import (
	"slices"
	"testing"

	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestCancel_MidRun(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	run1 := h.Agent.WaitRun(task.ID)

	task.Cancel()

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
