package api_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/gritzapp/gritz/internal/e2e/harness"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/cmpx"
	"github.com/gritzapp/gritz/internal/x/testx"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestResetSandbox(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task", Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: task.ID, Event: string(model.RunnerEventStarted), Version: 1},
			{TaskId: task.ID, Event: string(model.RunnerEventFailed), Version: 1},
		},
	})

	// Act
	_, err := h.Org.Client.ResetSandbox(t.Context(), &gritzv1.ResetSandboxRequest{TaskId: task.ID})

	// Assert - the reset command is set at the next version, and the status
	// is not touched.
	assert.NilError(t, err)
	assert.DeepEqual(t,
		h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID}),
		&gritzv1.Task{
			Id:      task.ID,
			Status:  gritzv1.TaskStatus_FAILED,
			Command: gritzv1.TaskCommand_RESET,
			Version: 2,
		},
		protocmp.Transform(),
		cmpx.OnlyProtoFields(&gritzv1.Task{}, "id", "status", "command", "version"),
	)
	lifecycle := testx.ProtoVariantsOf[*gritzv1.LifecyclePayload](h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
		Types:  []string{model.EventTypeLifecycle},
	}))
	assert.DeepEqual(t,
		lifecycle[len(lifecycle)-1],
		&gritzv1.LifecyclePayload{
			Kind:       gritzv1.LifecycleKind_LIFECYCLE_KIND_RESET_REQUESTED,
			Actor:      &gritzv1.Actor{Kind: "user", Name: h.Org.Owner.UserID},
			FromStatus: "Failed",
			ToStatus:   "Failed",
		},
		protocmp.Transform(),
	)
	commands := testx.ProtoVariantsOf[*gritzv1.CommandPayload](h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
		Types:  []string{model.EventTypeCommand},
	}))
	assert.DeepEqual(t,
		commands[len(commands)-1],
		&gritzv1.CommandPayload{
			Command:   gritzv1.TaskCommand_RESET,
			Version:   2,
			Runner:    harness.DefaultRunner,
			Workspace: harness.DefaultWorkspace,
		},
		protocmp.Transform(),
	)
}

func TestResetSandbox_Publishes(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task", Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: task.ID, Event: string(model.RunnerEventStarted), Version: 1},
			{TaskId: task.ID, Event: string(model.RunnerEventFailed), Version: 1},
		},
	})
	notifications := h.Org.Subscribe()

	// Act
	_, err := h.Org.Client.ResetSandbox(t.Context(), &gritzv1.ResetSandboxRequest{TaskId: task.ID})
	assert.NilError(t, err)

	// Assert - the runner is woken, and the channel is told nothing: the
	// task's outcome did not change.
	assert.DeepEqual(t, notifications.Next(), model.Notification{
		Type: "change",
		Resources: []model.NotificationResource{
			{Action: "reset", Type: "task", ID: task.ID},
			{Action: "appended", Type: "task_events", ID: task.ID},
		},
		OrgID:  h.Org.ID,
		Runner: harness.DefaultRunner,
		UserID: h.Org.Owner.UserID,
	}, cmpopts.IgnoreFields(model.Notification{}, "Time"))
}

func TestResetSandbox_FailedPrecondition(t *testing.T) {
	t.Parallel()
	h := harness.New(t, nil)
	tests := []struct {
		name    string
		arrange func() *harness.Task
	}{
		{"running", func() *harness.Task {
			task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task", Draft: true})
			h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
			h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
				Events: []*gritzv1.RunnerEvent{{TaskId: task.ID, Event: string(model.RunnerEventStarted), Version: 1}},
			})
			return task
		}},
		{"command in flight", func() *harness.Task {
			task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task", Draft: true})
			h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
			h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
				Events: []*gritzv1.RunnerEvent{
					{TaskId: task.ID, Event: string(model.RunnerEventStarted), Version: 1},
					{TaskId: task.ID, Event: string(model.RunnerEventFailed), Version: 1},
				},
			})
			task.ResetSandbox()
			return task
		}},
		{"archived", func() *harness.Task {
			task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task", Draft: true})
			h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
			h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
				Events: []*gritzv1.RunnerEvent{
					{TaskId: task.ID, Event: string(model.RunnerEventStarted), Version: 1},
					{TaskId: task.ID, Event: string(model.RunnerEventFailed), Version: 1},
				},
			})
			task.Archive()
			return task
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			task := tt.arrange()
			before := task.Get()

			// Act
			_, err := h.Org.Client.ResetSandbox(t.Context(), &gritzv1.ResetSandboxRequest{TaskId: task.ID})

			// Assert - refused, and the task is unchanged.
			assert.Equal(t, connect.CodeOf(err), connect.CodeFailedPrecondition)
			assert.DeepEqual(t, task.Get(), before, protocmp.Transform())
		})
	}
}

func TestResetSandbox_CrossOrgDenied(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	other := h.Server.CreateOrg()
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task", Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: task.ID, Event: string(model.RunnerEventStarted), Version: 1},
			{TaskId: task.ID, Event: string(model.RunnerEventFailed), Version: 1},
		},
	})

	// Act
	_, err := other.Client.ResetSandbox(t.Context(), &gritzv1.ResetSandboxRequest{TaskId: task.ID})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeNotFound)
	assert.Equal(t, task.Get().Command, gritzv1.TaskCommand_NONE)
}

func TestResetSandbox_RunnerReset(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task", Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: task.ID, Event: string(model.RunnerEventStarted), Version: 1},
			{TaskId: task.ID, Event: string(model.RunnerEventFailed), Version: 1},
		},
	})
	task.ResetSandbox()
	notifications := h.Org.Subscribe()

	// Act
	_, err := h.Org.Client.SubmitRunnerEvents(t.Context(), &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: task.ID, Event: string(model.RunnerEventReset), Version: 2}},
	})

	// Assert - the command is cleared and the task is still failed, at the
	// reset's version. Its outcome is not announced again.
	assert.NilError(t, err)
	assert.DeepEqual(t,
		task.Get(),
		&gritzv1.Task{
			Id:      task.ID,
			Status:  gritzv1.TaskStatus_FAILED,
			Command: gritzv1.TaskCommand_NONE,
			Version: 2,
		},
		protocmp.Transform(),
		cmpx.OnlyProtoFields(&gritzv1.Task{}, "id", "status", "command", "version"),
	)
	lifecycle := testx.ProtoVariantsOf[*gritzv1.LifecyclePayload](h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
		Types:  []string{model.EventTypeLifecycle},
	}))
	assert.DeepEqual(t,
		lifecycle[len(lifecycle)-1],
		&gritzv1.LifecyclePayload{
			Kind:       gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_RESET,
			Actor:      &gritzv1.Actor{Kind: "runner"},
			FromStatus: "Failed",
			ToStatus:   "Failed",
		},
		protocmp.Transform(),
	)
	assert.DeepEqual(t, notifications.Next(), model.Notification{
		Type: "change",
		Resources: []model.NotificationResource{
			{Action: "updated", Type: "task", ID: task.ID},
			{Action: "appended", Type: "task_events", ID: task.ID},
		},
		OrgID:  h.Org.ID,
		UserID: h.Org.Owner.UserID,
	}, cmpopts.IgnoreFields(model.Notification{}, "Time"))
}

func TestResetSandbox_DuplicateRunnerResetIgnored(t *testing.T) {
	t.Parallel()
	// Arrange - the runner reported the reset, which cleared the command.
	h := harness.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task", Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: task.ID, Event: string(model.RunnerEventStarted), Version: 1},
			{TaskId: task.ID, Event: string(model.RunnerEventFailed), Version: 1},
		},
	})
	task.ResetSandbox()
	reset := &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: task.ID, Event: string(model.RunnerEventReset), Version: 2}},
	}
	h.Org.SubmitRunnerEvents(reset)
	before := task.Events()

	// Act - the runner reports it again.
	_, err := h.Org.Client.SubmitRunnerEvents(t.Context(), reset)

	// Assert - no reset is in flight, so the report changes nothing.
	assert.NilError(t, err)
	assert.Equal(t, task.Get().Command, gritzv1.TaskCommand_NONE)
	assert.Assert(t, cmp.Len(task.Events(), len(before)))
}
