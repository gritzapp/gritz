package scenario_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestSetupCommands(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{
		Commands: []string{"echo setup >> setup", "echo setup output"},
	}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	h.Agent.WaitRun(task.ID).Complete()
	task.WaitStatus(gritzv1.TaskStatus_COMPLETED)

	task.Update(&gritzv1.UpdateTaskRequest{
		Start:           true,
		AddInstructions: []*gritzv1.Instruction{{Text: "more"}},
	})
	h.Agent.WaitRun(task.ID).Complete()

	got := task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, got.Version, int64(2))
	// The commands ran on the first run only.
	assert.Equal(t, h.Backend.ReadFile(task.ID, "setup"), "setup\n")
	log := task.WaitLog(func(log string) bool { return strings.Contains(log, "==== run version=2 ") })
	// A line of its own: the driver also logs the command it runs.
	lines := strings.Split(log, "\n")
	assert.Equal(t, len(slices.DeleteFunc(lines, func(l string) bool { return l != "setup output" })), 1, log)
}

func TestCancel_DuringSetup(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, &e2e.Options{Runner: &e2e.RunnerOptions{Commands: []string{"sleep 60"}}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	task.WaitStatus(gritzv1.TaskStatus_RUNNING)
	start := time.Now()

	task.Cancel()

	task.WaitStatus(gritzv1.TaskStatus_CANCELLED)
	assert.Assert(t, time.Since(start) < 10*time.Second, "cancelled after %s", time.Since(start))
	assert.Assert(t, cmp.Len(h.Agent.Runs(task.ID), 0))
}
