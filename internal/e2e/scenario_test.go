package e2e_test

import (
	"testing"

	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestStart(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	task := h.Org.CreateTask(nil)
	task.AddInstruction("do the thing")

	task.Start()
	run := h.Agent.WaitRun(task.ID)
	h.Backend.WaitSandbox(task.ID, backend.StatusUp)
	assert.Assert(t, cmp.Contains(run.Prompt, "do the thing"))
	run.Complete()

	got := task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, got.Version, int64(1))
	var kinds []gritzv1.LifecycleKind
	for _, e := range task.Events() {
		if l := e.GetLifecycle(); l != nil {
			kinds = append(kinds, l.Kind)
		}
	}
	assert.Assert(t, cmp.Contains(kinds, gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_STARTED))
	assert.Assert(t, cmp.Contains(kinds, gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_EXITED))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 1))
}
