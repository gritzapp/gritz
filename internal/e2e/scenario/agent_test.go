package scenario_test

import (
	"testing"

	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestAgentError(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})

	h.Agent.WaitRun(task.ID).Fail("boom")

	task.WaitStatus(gritzv1.TaskStatus_FAILED)
	lifecycle := testx.ProtoVariantsOf[*gritzv1.LifecyclePayload](task.Events())
	failed := testx.FindFunc(t, lifecycle, func(l *gritzv1.LifecyclePayload, _ int) bool {
		return l.Kind == gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_FAILED
	})
	assert.Assert(t, cmp.Contains(failed.Message, "boom"))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 1))
}
