package api_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/gritzapp/gritz/internal/e2e"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"gotest.tools/v3/assert"
)

// These tests read the notifications a change publishes from the org's /events
// stream, as the web UI and runners receive them.

func TestCreateTask_Publishes(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	notifications := h.Org.Subscribe()

	// Act
	resp, err := h.Org.Client.CreateTask(t.Context(), &gritzv1.CreateTaskRequest{
		Name:      "test",
		Runner:    e2e.DefaultRunner,
		Workspace: e2e.DefaultWorkspace,
	})
	assert.NilError(t, err)

	// Assert
	assert.DeepEqual(t, notifications.Next(), model.Notification{
		Type: "change",
		Resources: []model.NotificationResource{
			{Action: "created", Type: "task", ID: resp.Task.Id},
			{Action: "appended", Type: "task_events", ID: resp.Task.Id},
		},
		OrgID:  h.Org.ID,
		Runner: e2e.DefaultRunner,
		UserID: h.Org.Owner.UserID,
	}, cmpopts.IgnoreFields(model.Notification{}, "Time", "ChannelMessage"))
}

// A draft has no command, so PendingRunner() is empty and the notification
// carries no runner: the UI sees the new task, no runner is woken for it.
func TestCreateTask_Draft_PublishesWithoutRunner(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	notifications := h.Org.Subscribe()

	// Act
	resp, err := h.Org.Client.CreateTask(t.Context(), &gritzv1.CreateTaskRequest{
		Name:      "test",
		Runner:    e2e.DefaultRunner,
		Workspace: e2e.DefaultWorkspace,
		Draft:     true,
	})
	assert.NilError(t, err)

	// Assert
	assert.DeepEqual(t, notifications.Next(), model.Notification{
		Type: "change",
		Resources: []model.NotificationResource{
			{Action: "created", Type: "task", ID: resp.Task.Id},
			{Action: "appended", Type: "task_events", ID: resp.Task.Id},
		},
		OrgID:  h.Org.ID,
		UserID: h.Org.Owner.UserID,
	}, cmpopts.IgnoreFields(model.Notification{}, "Time", "ChannelMessage"))
}

func TestUpdateTask_Publishes(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "test", Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	notifications := h.Org.Subscribe()

	// Act
	_, err := h.Org.Client.UpdateTask(t.Context(), &gritzv1.UpdateTaskRequest{
		Id:   task.ID,
		Name: "updated",
	})
	assert.NilError(t, err)

	// Assert
	assert.DeepEqual(t, notifications.Next(), model.Notification{
		Type: "change",
		Resources: []model.NotificationResource{
			{Action: "updated", Type: "task", ID: task.ID},
			{Action: "appended", Type: "task_events", ID: task.ID},
		},
		OrgID:  h.Org.ID,
		Runner: e2e.DefaultRunner,
		UserID: h.Org.Owner.UserID,
	}, cmpopts.IgnoreFields(model.Notification{}, "Time", "ChannelMessage"))
}

func TestCancelTask_Publishes(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "test", Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	notifications := h.Org.Subscribe()

	// Act
	_, err := h.Org.Client.CancelTask(t.Context(), &gritzv1.CancelTaskRequest{Id: task.ID})
	assert.NilError(t, err)

	// Assert
	assert.DeepEqual(t, notifications.Next(), model.Notification{
		Type: "change",
		Resources: []model.NotificationResource{
			{Action: "cancelled", Type: "task", ID: task.ID},
			{Action: "appended", Type: "task_events", ID: task.ID},
		},
		OrgID:  h.Org.ID,
		UserID: h.Org.Owner.UserID,
	}, cmpopts.IgnoreFields(model.Notification{}, "Time", "ChannelMessage"))
}

func TestArchiveTask_Publishes(t *testing.T) {
	t.Parallel()
	// Arrange - a draft can be archived directly.
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "test", Draft: true})
	notifications := h.Org.Subscribe()

	// Act
	_, err := h.Org.Client.ArchiveTask(t.Context(), &gritzv1.ArchiveTaskRequest{Id: task.ID})
	assert.NilError(t, err)

	// Assert
	n := notifications.Next()
	assert.DeepEqual(t, n, model.Notification{
		Type: "change",
		Resources: []model.NotificationResource{
			{Action: "archived", Type: "task", ID: task.ID},
			{Action: "appended", Type: "task_events", ID: task.ID},
		},
		OrgID:  h.Org.ID,
		UserID: h.Org.Owner.UserID,
	}, cmpopts.IgnoreFields(model.Notification{}, "Time", "ChannelMessage"))
	assert.Assert(t, strings.Contains(n.ChannelMessage, "archived"), "expected archived in message, got %q", n.ChannelMessage)
}

func TestUploadLogs_Publishes(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "test", Draft: true})
	notifications := h.Org.Subscribe()

	// Act
	_, err := h.Org.Client.UploadLogs(t.Context(), &gritzv1.UploadLogsRequest{
		TaskId: task.ID,
		Entries: []*gritzv1.LogEntry{
			{Type: "llm", Content: "hello"},
		},
	})
	assert.NilError(t, err)

	// Assert
	assert.DeepEqual(t, notifications.Next(), model.Notification{
		Type: "change",
		Resources: []model.NotificationResource{
			{Action: "appended", Type: "task_events", ID: task.ID},
		},
		OrgID:  h.Org.ID,
		UserID: h.Org.Owner.UserID,
	}, cmpopts.IgnoreFields(model.Notification{}, "Time", "ChannelMessage"))
}

func TestCreateLink_Publishes(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "test", Draft: true})
	notifications := h.Org.Subscribe()

	// Act
	resp, err := h.Org.Client.CreateLink(t.Context(), &gritzv1.CreateLinkRequest{
		TaskId: task.ID,
		Url:    "https://example.com",
		Title:  "test",
	})
	assert.NilError(t, err)

	// Assert
	assert.DeepEqual(t, notifications.Next(), model.Notification{
		Type: "change",
		Resources: []model.NotificationResource{
			{Action: "created", Type: "task_links", ID: task.ID},
			{Action: "created", Type: "link", ID: resp.Link.Id},
		},
		OrgID:  h.Org.ID,
		UserID: h.Org.Owner.UserID,
	}, cmpopts.IgnoreFields(model.Notification{}, "Time", "ChannelMessage"))
}

func TestUpdateTask_ChannelMessage_QueuedOnStart(t *testing.T) {
	t.Parallel()
	// Arrange - run the task to a stop, so the start under test re-queues a
	// task that isn't already queued.
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "test", Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	version := h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID}).Version
	h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: task.ID, Event: string(model.RunnerEventStarted), Version: version},
			{TaskId: task.ID, Event: string(model.RunnerEventStopped), Version: version},
		},
	})
	notifications := h.Org.Subscribe()

	// Act
	_, err := h.Org.Client.UpdateTask(t.Context(), &gritzv1.UpdateTaskRequest{
		Id:    task.ID,
		Start: true,
	})
	assert.NilError(t, err)

	// Assert
	msg := notifications.Next().ChannelMessage
	assert.Assert(t, strings.Contains(msg, "queued"), "expected queued in message, got %q", msg)
}

func TestUpdateTask_NoChannelMessage_NameOnly(t *testing.T) {
	t.Parallel()
	// Arrange - a running task has no pending runner, so a name-only update
	// stays silent on the channel.
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "test", Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	version := h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID}).Version
	h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: task.ID, Event: string(model.RunnerEventStarted), Version: version},
		},
	})
	notifications := h.Org.Subscribe()

	// Act
	_, err := h.Org.Client.UpdateTask(t.Context(), &gritzv1.UpdateTaskRequest{
		Id:   task.ID,
		Name: "renamed",
	})
	assert.NilError(t, err)

	// Assert
	assert.Equal(t, notifications.Next().ChannelMessage, "")
}

func TestSubmitRunnerEvents_Publishes(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "test", Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	version := h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID}).Version
	notifications := h.Org.Subscribe()

	// Act
	_, err := h.Org.Client.SubmitRunnerEvents(t.Context(), &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: task.ID, Event: string(model.RunnerEventStarted), Version: version},
		},
	})
	assert.NilError(t, err)

	// Assert
	assert.DeepEqual(t, notifications.Next(), model.Notification{
		Type: "change",
		Resources: []model.NotificationResource{
			{Action: "updated", Type: "task", ID: task.ID},
			{Action: "appended", Type: "task_events", ID: task.ID},
		},
		OrgID:  h.Org.ID,
		UserID: h.Org.Owner.UserID,
	}, cmpopts.IgnoreFields(model.Notification{}, "Time", "ChannelMessage"))
}

func TestSubmitRunnerEvents_NotApplied_DoesNotPublish(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "test", Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	notifications := h.Org.Subscribe()

	// Act - a stale version isn't applied, so the notification is ignored.
	_, err := h.Org.Client.SubmitRunnerEvents(t.Context(), &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: task.ID, Event: string(model.RunnerEventStarted), Version: 999},
		},
	})
	assert.NilError(t, err)

	// Assert - the stream is ordered, so a later rename's notification arriving
	// first shows the stale event published nothing.
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Name: "renamed"})
	n := notifications.Next()
	assert.DeepEqual(t, n.Resources, []model.NotificationResource{
		{Action: "updated", Type: "task", ID: task.ID},
		{Action: "appended", Type: "task_events", ID: task.ID},
	})
}
