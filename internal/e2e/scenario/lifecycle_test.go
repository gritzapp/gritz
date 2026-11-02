package scenario_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestCancel_PendingStart(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{Concurrency: 1}})
	a := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do a"}},
	})
	runA := h.Agent.WaitRun(a.ID)
	b := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do b"}},
	})
	b.WaitStatus(gritzv1.TaskStatus_PENDING)

	b.Cancel()

	b.WaitStatus(gritzv1.TaskStatus_CANCELLED)

	// Free the slot, then run a third task. C's start is later in the runner's
	// event log than B's cancel, so once C runs the runner has seen both.
	a.Cancel()
	runA.WaitCancelled()
	a.WaitStatus(gritzv1.TaskStatus_CANCELLED)
	c := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do c"}},
	})
	h.Agent.WaitRun(c.ID).Complete()
	c.WaitStatus(gritzv1.TaskStatus_COMPLETED)

	// B never ran, even once the slot was free.
	assert.Assert(t, cmp.Len(h.Agent.Runs(b.ID), 0))
	lifecycle := testx.ProtoVariantsOf[*gritzv1.LifecyclePayload](b.Events())
	for _, l := range lifecycle {
		assert.Assert(t, l.Kind != gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_STARTED)
	}
	h.Backend.WaitSandbox(b.ID, backend.StatusGone)
}

func TestConcurrent(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{Concurrency: 2}})
	instructions := []string{"do one", "do two", "do three"}
	// Slots aren't handed out in start order: the first two tasks take both
	// before the third starts, so the third is the one that waits.
	var tasks []*e2e.Task
	var runs []*e2e.Run
	for _, text := range instructions[:2] {
		task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
			Instructions: []*gritzv1.Instruction{{Text: text}},
		})
		tasks = append(tasks, task)
		runs = append(runs, h.Agent.WaitRun(task.ID))
	}
	tasks = append(tasks, h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: instructions[2]}},
	}))
	tasks[2].WaitStatus(gritzv1.TaskStatus_PENDING)
	assert.Assert(t, cmp.Len(h.Agent.Runs(tasks[2].ID), 0))

	runs[0].Complete()
	runs = append(runs, h.Agent.WaitRun(tasks[2].ID))
	runs[1].Complete()
	runs[2].Complete()

	// Each run has its own task's instruction, and none another's.
	for i, run := range runs {
		for j, text := range instructions {
			if i == j {
				assert.Assert(t, cmp.Contains(run.Prompt, text))
			} else {
				assert.Assert(t, !strings.Contains(run.Prompt, text), "run %d has %q", i+1, text)
			}
		}
	}
	for _, task := range tasks {
		task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
		assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 1))
	}
}

func TestArchive(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{
		Commands: []string{"echo setup >> setup"},
	}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	h.Agent.WaitRun(task.ID).Complete()
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)

	task.Archive()

	h.Backend.WaitSandbox(task.ID, backend.StatusGone)

	// Unarchived and woken, the task gets a fresh sandbox, provisioned again.
	task.Unarchive()
	task.Update(&gritzv1.UpdateTaskRequest{
		Start:           true,
		AddInstructions: []*gritzv1.Instruction{{Text: "more"}},
	})
	run2 := h.Agent.WaitRun(task.ID)
	assert.Assert(t, cmp.Contains(run2.Prompt, "more"))
	run2.Complete()
	got := task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, got.Version, int64(2))
	assert.Equal(t, h.Backend.ReadFile(task.ID, "setup"), "setup\n")
}

func TestArchive_DestroyFails(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	h.Agent.WaitRun(task.ID).Complete()
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	// The first destroy fails, and removes the mock so the next one is real.
	m := &backend.SandboxMock{
		DestroyFunc: func(context.Context) error {
			h.Backend.MockSandbox(task.ID, nil)
			return errors.New("e2e: destroy failed")
		},
	}
	h.Backend.MockSandbox(task.ID, m)

	task.Archive()

	// The runner retried the destroy.
	h.Backend.WaitSandbox(task.ID, backend.StatusGone)
	assert.Assert(t, cmp.Len(m.DestroyCalls(), 1))
}
