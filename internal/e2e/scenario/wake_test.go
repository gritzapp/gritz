package scenario_test

import (
	"testing"

	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/cmpx"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestWake_MidRun(t *testing.T) {
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

	// The wake landed on the open run: v1 is still running, with the start of
	// v2 pending.
	assert.DeepEqual(t,
		task.Get(),
		&gritzv1.Task{
			Id:      task.ID,
			Status:  gritzv1.TaskStatus_RUNNING,
			Command: gritzv1.TaskCommand_START,
			Version: 1,
		},
		protocmp.Transform(),
		cmpx.OnlyProtoFields(&gritzv1.Task{}, "id", "status", "command", "version"),
	)

	run1.Complete()
	run2 := h.Agent.WaitRun(task.ID)
	assert.Assert(t, cmp.Contains(run2.Prompt, "more"))
	run2.Complete()
	got := task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, got.Version, int64(2))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 2))
}

func TestWake_CancelledAtBoundary(t *testing.T) {
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
	assert.DeepEqual(t,
		task.Get(),
		&gritzv1.Task{
			Id:      task.ID,
			Status:  gritzv1.TaskStatus_RUNNING,
			Command: gritzv1.TaskCommand_START,
			Version: 1,
		},
		protocmp.Transform(),
		cmpx.OnlyProtoFields(&gritzv1.Task{}, "id", "status", "command", "version"),
	)

	task.Cancel()

	// The cancel superseded the pending start of v2, which never ran.
	run1.WaitCancelled()
	task.WaitStatus(gritzv1.TaskStatus_CANCELLED)
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 1))
}
