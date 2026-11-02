package api_test

import (
	"testing"

	"github.com/gritzapp/gritz/internal/e2e"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestLifecycle_CreateAppendsCreatedEvent(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	resp, err := h.Org.Client.CreateTask(t.Context(), &gritzv1.CreateTaskRequest{
		Name:      "Task",
		Runner:    e2e.DefaultRunner,
		Workspace: e2e.DefaultWorkspace,
	})

	// Assert
	assert.NilError(t, err)
	events := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: resp.Task.Id,
		Types:  []string{model.EventTypeLifecycle},
	})
	assert.Assert(t, cmp.Len(events, 1))
	created := events[0].GetLifecycle()
	assert.Equal(t, created.Kind, gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED)
	assert.Equal(t, created.Actor.Kind, "user")
	// A freshly created task has no prior status; it lands in PENDING.
	assert.Equal(t, created.FromStatus, "")
	assert.Equal(t, created.ToStatus, "Pending")
}

func TestLifecycle_CancelAppendsCancelledEventBesideProjection(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task", Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})

	// Act
	_, err := h.Org.Client.CancelTask(t.Context(), &gritzv1.CancelTaskRequest{Id: task.ID})

	// Assert - the lifecycle event records the transition...
	assert.NilError(t, err)
	events := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
		Types:  []string{model.EventTypeLifecycle},
	})
	last := events[len(events)-1].GetLifecycle()
	assert.Equal(t, last.Kind, gritzv1.LifecycleKind_LIFECYCLE_KIND_CANCELLED)
	assert.Equal(t, last.FromStatus, "Pending")
	assert.Equal(t, last.ToStatus, "Cancelled")
	// ...and the materialized status projection is updated in the same tx.
	got := h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID})
	assert.Equal(t, got.Status, gritzv1.TaskStatus_CANCELLED)
}

func TestLifecycle_TaskMutationsAppendEvents(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	runner := h.Org.CreateKey(&gritzv1.CreateKeyRequest{Name: "runner"})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task", Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})

	// Act - a pending task can't be restarted, so start its sandbox first, then
	// restart the running task. Runner events use version 0 to bypass the
	// version check.
	_, err := runner.Client.SubmitRunnerEvents(t.Context(), &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: task.ID, Event: "started", Version: 0}},
	})
	assert.NilError(t, err)
	_, err = h.Org.Client.RestartTask(t.Context(), &gritzv1.RestartTaskRequest{Id: task.ID})
	assert.NilError(t, err)
	// Drive it back to running, then to a terminal status so it can be archived.
	_, err = runner.Client.SubmitRunnerEvents(t.Context(), &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: task.ID, Event: "started", Version: 0}},
	})
	assert.NilError(t, err)
	_, err = runner.Client.SubmitRunnerEvents(t.Context(), &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: task.ID, Event: "stopped", Version: 0}},
	})
	assert.NilError(t, err)
	_, err = h.Org.Client.ArchiveTask(t.Context(), &gritzv1.ArchiveTaskRequest{Id: task.ID})
	assert.NilError(t, err)
	_, err = h.Org.Client.UnarchiveTask(t.Context(), &gritzv1.UnarchiveTaskRequest{Id: task.ID})
	assert.NilError(t, err)

	// Assert - collect the set of lifecycle kinds present on the stream.
	kinds := map[gritzv1.LifecycleKind]bool{}
	for _, e := range h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
		Types:  []string{model.EventTypeLifecycle},
	}) {
		kinds[e.GetLifecycle().Kind] = true
	}
	assert.Assert(t, kinds[gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED])
	assert.Assert(t, kinds[gritzv1.LifecycleKind_LIFECYCLE_KIND_RESTARTED])
	assert.Assert(t, kinds[gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_STARTED])
	assert.Assert(t, kinds[gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_EXITED])
	assert.Assert(t, kinds[gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED])
	assert.Assert(t, kinds[gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED])
}

func TestLifecycle_RunnerEventsAppendSandboxEvents(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	runner := h.Org.CreateKey(&gritzv1.CreateKeyRequest{Name: "runner"})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task", Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})

	// Act - started: PENDING -> RUNNING, actor is the runner.
	_, err := runner.Client.SubmitRunnerEvents(t.Context(), &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: task.ID, Event: "started", Version: 1}},
	})

	// Assert
	assert.NilError(t, err)
	events := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
		Types:  []string{model.EventTypeLifecycle},
	})
	last := events[len(events)-1].GetLifecycle()
	assert.Equal(t, last.Kind, gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_STARTED)
	assert.Equal(t, last.Actor.Kind, "runner")
	assert.Equal(t, last.FromStatus, "Pending")
	assert.Equal(t, last.ToStatus, "Running")

	// Act - failed: RUNNING -> FAILED, with the producer's reason threaded into
	// message.
	_, err = runner.Client.SubmitRunnerEvents(t.Context(), &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{
			TaskId:  task.ID,
			Event:   "failed",
			Version: 0,
			Reason:  "setup command 0 failed: exit status 1",
		}},
	})

	// Assert
	assert.NilError(t, err)
	events = h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
		Types:  []string{model.EventTypeLifecycle},
	})
	last = events[len(events)-1].GetLifecycle()
	assert.Equal(t, last.Kind, gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_FAILED)
	assert.Equal(t, last.Message, "setup command 0 failed: exit status 1")
	assert.Equal(t, last.ToStatus, "Failed")
}

// TestLifecycle_RunnerFailedFallsBackWhenNoReason verifies that a "failed" event
// with an empty reason (an old runner/driver) keeps the legacy message.
func TestLifecycle_RunnerFailedFallsBackWhenNoReason(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	runner := h.Org.CreateKey(&gritzv1.CreateKeyRequest{Name: "runner"})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task", Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: task.ID, Event: "started", Version: 1}},
	})

	// Act
	_, err := runner.Client.SubmitRunnerEvents(t.Context(), &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: task.ID, Event: "failed", Version: 0}},
	})

	// Assert
	assert.NilError(t, err)
	events := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
		Types:  []string{model.EventTypeLifecycle},
	})
	last := events[len(events)-1].GetLifecycle()
	assert.Equal(t, last.Kind, gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_FAILED)
	assert.Equal(t, last.Message, "container failed")
}
