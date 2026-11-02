package api_test

import (
	"testing"

	"github.com/gritzapp/gritz/internal/e2e"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/testx"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestCreateTask_CommandEvent(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	resp, err := h.Org.Client.CreateTask(t.Context(), &gritzv1.CreateTaskRequest{
		Runner:    e2e.DefaultRunner,
		Workspace: e2e.DefaultWorkspace,
	})

	// Assert
	assert.NilError(t, err)
	events := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: resp.Task.Id,
		Types:  []string{model.EventTypeCommand},
	})
	got := testx.ProtoVariantsOf[*gritzv1.CommandPayload](events)
	assert.DeepEqual(t, got, []*gritzv1.CommandPayload{
		{
			Command:   gritzv1.TaskCommand_START,
			Version:   1,
			Runner:    e2e.DefaultRunner,
			Workspace: e2e.DefaultWorkspace,
		},
	}, protocmp.Transform())
}

func TestCreateTask_DraftNoCommandEvent(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	resp, err := h.Org.Client.CreateTask(t.Context(), &gritzv1.CreateTaskRequest{
		Runner:    e2e.DefaultRunner,
		Workspace: e2e.DefaultWorkspace,
		Draft:     true,
	})

	// Assert
	assert.NilError(t, err)
	events := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: resp.Task.Id,
		Types:  []string{model.EventTypeCommand},
	})
	assert.Assert(t, cmp.Len(events, 0))
}

func TestUpdateTask_StartCommandEvent(t *testing.T) {
	t.Parallel()
	// Arrange - run 1 starts and completes.
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: task.ID, Event: "started", Version: 1},
			{TaskId: task.ID, Event: "stopped", Version: 1},
		},
	})

	// Act - start the completed task again.
	_, err := h.Org.Client.UpdateTask(t.Context(), &gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})

	// Assert - run 1's start and its clear, then the start for run 2.
	assert.NilError(t, err)
	events := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
		Types:  []string{model.EventTypeCommand},
	})
	got := testx.ProtoVariantsOf[*gritzv1.CommandPayload](events)
	assert.DeepEqual(t, got, []*gritzv1.CommandPayload{
		{
			Command:   gritzv1.TaskCommand_START,
			Version:   1,
			Runner:    e2e.DefaultRunner,
			Workspace: e2e.DefaultWorkspace,
		},
		{
			Command:   gritzv1.TaskCommand_NONE,
			Version:   1,
			Runner:    e2e.DefaultRunner,
			Workspace: e2e.DefaultWorkspace,
		},
		{
			Command:   gritzv1.TaskCommand_START,
			Version:   2,
			Runner:    e2e.DefaultRunner,
			Workspace: e2e.DefaultWorkspace,
		},
	}, protocmp.Transform())
}

func TestUpdateTask_UnchangedCommandNoEvent(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})

	// Act
	_, err := h.Org.Client.UpdateTask(t.Context(), &gritzv1.UpdateTaskRequest{Id: task.ID, Name: "renamed"})

	// Assert
	assert.NilError(t, err)
	events := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
		Types:  []string{model.EventTypeCommand},
	})
	assert.Assert(t, cmp.Len(events, 0))
}

func TestRestartTask_CommandEvent(t *testing.T) {
	t.Parallel()
	// Arrange - run 1 starts and fails.
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: task.ID, Event: "started", Version: 1}},
	})
	h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: task.ID, Event: "failed", Version: 1}},
	})

	// Act
	_, err := h.Org.Client.RestartTask(t.Context(), &gritzv1.RestartTaskRequest{Id: task.ID})

	// Assert - run 1's start and its clear, then the restart for run 2.
	assert.NilError(t, err)
	events := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
		Types:  []string{model.EventTypeCommand},
	})
	got := testx.ProtoVariantsOf[*gritzv1.CommandPayload](events)
	assert.DeepEqual(t, got, []*gritzv1.CommandPayload{
		{
			Command:   gritzv1.TaskCommand_START,
			Version:   1,
			Runner:    e2e.DefaultRunner,
			Workspace: e2e.DefaultWorkspace,
		},
		{
			Command:   gritzv1.TaskCommand_NONE,
			Version:   1,
			Runner:    e2e.DefaultRunner,
			Workspace: e2e.DefaultWorkspace,
		},
		{
			Command:   gritzv1.TaskCommand_RESTART,
			Version:   2,
			Runner:    e2e.DefaultRunner,
			Workspace: e2e.DefaultWorkspace,
		},
	}, protocmp.Transform())
}

func TestCancelTask_PendingCommandEvent(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})

	// Act
	_, err := h.Org.Client.CancelTask(t.Context(), &gritzv1.CancelTaskRequest{Id: task.ID})

	// Assert - the withdrawal is a none command for the start's version.
	assert.NilError(t, err)
	events := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
		Types:  []string{model.EventTypeCommand},
	})
	got := testx.ProtoVariantsOf[*gritzv1.CommandPayload](events)
	assert.DeepEqual(t, got, []*gritzv1.CommandPayload{
		{
			Command:   gritzv1.TaskCommand_START,
			Version:   1,
			Runner:    e2e.DefaultRunner,
			Workspace: e2e.DefaultWorkspace,
		},
		{
			Command:   gritzv1.TaskCommand_NONE,
			Version:   1,
			Runner:    e2e.DefaultRunner,
			Workspace: e2e.DefaultWorkspace,
		},
	}, protocmp.Transform())
}

func TestSubmitRunnerEvents_ClearCommandEvent(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	runner := h.Org.CreateKey(&gritzv1.CreateKeyRequest{Name: "runner"})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})

	// Act
	_, err := runner.Client.SubmitRunnerEvents(t.Context(), &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: task.ID, Event: "started", Version: 1}},
	})

	// Assert
	assert.NilError(t, err)
	events := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
		Types:  []string{model.EventTypeCommand},
	})
	got := testx.ProtoVariantsOf[*gritzv1.CommandPayload](events)
	assert.DeepEqual(t, got, []*gritzv1.CommandPayload{
		{
			Command:   gritzv1.TaskCommand_START,
			Version:   1,
			Runner:    e2e.DefaultRunner,
			Workspace: e2e.DefaultWorkspace,
		},
		{
			Command:   gritzv1.TaskCommand_NONE,
			Version:   1,
			Runner:    e2e.DefaultRunner,
			Workspace: e2e.DefaultWorkspace,
		},
	}, protocmp.Transform())
}

// TestSubmitRunnerEvents_RunBoundaryCommandEvent covers a start pending on a
// running task: the run's exit keeps the start but moves it to the next run's
// version, and that move is a command event too.
func TestSubmitRunnerEvents_RunBoundaryCommandEvent(t *testing.T) {
	t.Parallel()
	// Arrange - run 1 is running, and a start is queued behind it.
	h := e2e.New(t, nil)
	runner := h.Org.CreateKey(&gritzv1.CreateKeyRequest{Name: "runner"})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: task.ID, Event: "started", Version: 1}},
	})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})

	// Act - run 1 exits with the start still pending.
	_, err := runner.Client.SubmitRunnerEvents(t.Context(), &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: task.ID, Event: "stopped", Version: 1}},
	})

	// Assert - run 1's start and its clear, the queued start, then the same
	// start for run 2.
	assert.NilError(t, err)
	events := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
		Types:  []string{model.EventTypeCommand},
	})
	got := testx.ProtoVariantsOf[*gritzv1.CommandPayload](events)
	assert.DeepEqual(t, got, []*gritzv1.CommandPayload{
		{
			Command:   gritzv1.TaskCommand_START,
			Version:   1,
			Runner:    e2e.DefaultRunner,
			Workspace: e2e.DefaultWorkspace,
		},
		{
			Command:   gritzv1.TaskCommand_NONE,
			Version:   1,
			Runner:    e2e.DefaultRunner,
			Workspace: e2e.DefaultWorkspace,
		},
		{
			Command:   gritzv1.TaskCommand_START,
			Version:   1,
			Runner:    e2e.DefaultRunner,
			Workspace: e2e.DefaultWorkspace,
		},
		{
			Command:   gritzv1.TaskCommand_START,
			Version:   2,
			Runner:    e2e.DefaultRunner,
			Workspace: e2e.DefaultWorkspace,
		},
	}, protocmp.Transform())
}

func TestSubmitRunnerEvents_ServerIssuedStopCommandEvent(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	runner := h.Org.CreateKey(&gritzv1.CreateKeyRequest{Name: "runner"})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	h.Org.CancelTask(&gritzv1.CancelTaskRequest{Id: task.ID})

	// Act - the runner had already acted on the withdrawn start: the server
	// stops it.
	_, err := runner.Client.SubmitRunnerEvents(t.Context(), &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: task.ID, Event: "started", Version: 1}},
	})

	// Assert
	assert.NilError(t, err)
	events := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
		Types:  []string{model.EventTypeCommand},
	})
	got := testx.ProtoVariantsOf[*gritzv1.CommandPayload](events)
	assert.DeepEqual(t, got, []*gritzv1.CommandPayload{
		{
			Command:   gritzv1.TaskCommand_START,
			Version:   1,
			Runner:    e2e.DefaultRunner,
			Workspace: e2e.DefaultWorkspace,
		},
		{
			Command:   gritzv1.TaskCommand_NONE,
			Version:   1,
			Runner:    e2e.DefaultRunner,
			Workspace: e2e.DefaultWorkspace,
		},
		{
			Command:   gritzv1.TaskCommand_STOP,
			Version:   1,
			Runner:    e2e.DefaultRunner,
			Workspace: e2e.DefaultWorkspace,
		},
	}, protocmp.Transform())
}
