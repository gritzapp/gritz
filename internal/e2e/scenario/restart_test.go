package scenario_test

import (
	"testing"

	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestRestart_MidRun(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	run1 := h.Agent.WaitRun(task.ID)

	task.Restart()

	run1.WaitCancelled()
	// Run 1 never completed its instructions, so run 2 is given them again.
	run2 := h.Agent.WaitRun(task.ID)
	assert.Equal(t, run2.Prompt, run1.Prompt)
	assert.Equal(t, task.Get().Version, int64(2))

	task.Cancel()
	run2.WaitCancelled()
	task.WaitStatus(gritzv1.TaskStatus_CANCELLED)
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 2))
}

func TestRestart_Completed(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{
		Commands: []string{"echo setup >> setup"},
	}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	h.Agent.WaitRun(task.ID).Complete()
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)

	task.Restart()

	run2 := h.Agent.WaitRun(task.ID)
	run2.Complete()
	got := task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, got.Version, int64(2))
	// The restart ran on the same sandbox, provisioned once.
	assert.Equal(t, task.ReadFile("setup"), "setup\n")
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 2))
}
