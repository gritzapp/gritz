package apiserver

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/icholy/gritz/internal/model"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"github.com/icholy/gritz/internal/pubsub"
	"github.com/icholy/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestCreateTask_Publishes(t *testing.T) {
	t.Parallel()

	pub := &pubsub.PublisherMock{
		PublishFunc: func(_ context.Context, _ model.Notification) error { return nil },
	}
	st := teststore.New(t)
	srv := New(Options{Store: st, Publisher: pub})
	org := teststore.CreateOrg(t, st, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "r", Name: "w"}}})
	ctx := createCtx(t, org)

	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name: "test", Runner: "r", Workspace: "w",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)

	assert.DeepEqual(t, pub.PublishedNotifications(), []model.Notification{{
		Type: "change",
		Resources: []model.NotificationResource{
			{Action: "created", Type: "task", ID: resp.Task.Id},
			{Action: "appended", Type: "task_events", ID: resp.Task.Id},
		},
		OrgID:  org.OrgID,
		Runner: "r",
		UserID: org.UserID,
	}}, cmpopts.IgnoreFields(model.Notification{}, "Time", "ChannelMessage"))
}

// An empty task has no command, so PendingRunner() is empty and the
// notification carries no runner: the UI sees the new task, no runner is woken
// for it. See proposals/draft/create-empty-task.md.
func TestCreateTask_NoInstructions_PublishesWithoutRunner(t *testing.T) {
	t.Parallel()

	pub := &pubsub.PublisherMock{
		PublishFunc: func(_ context.Context, _ model.Notification) error { return nil },
	}
	st := teststore.New(t)
	srv := New(Options{Store: st, Publisher: pub})
	org := teststore.CreateOrg(t, st, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "r", Name: "w"}}})
	ctx := createCtx(t, org)

	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name: "test", Runner: "r", Workspace: "w",
	})
	assert.NilError(t, err)

	assert.DeepEqual(t, pub.PublishedNotifications(), []model.Notification{{
		Type: "change",
		Resources: []model.NotificationResource{
			{Action: "created", Type: "task", ID: resp.Task.Id},
			{Action: "appended", Type: "task_events", ID: resp.Task.Id},
		},
		OrgID:  org.OrgID,
		UserID: org.UserID,
	}}, cmpopts.IgnoreFields(model.Notification{}, "Time", "ChannelMessage"))
}

func TestUpdateTask_Publishes(t *testing.T) {
	t.Parallel()

	pub := &pubsub.PublisherMock{
		PublishFunc: func(_ context.Context, _ model.Notification) error { return nil },
	}
	st := teststore.New(t)
	srv := New(Options{Store: st, Publisher: pub})
	org := teststore.CreateOrg(t, st, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "r", Name: "w"}}})
	ctx := createCtx(t, org)

	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name: "test", Runner: "r", Workspace: "w",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)
	pub.ResetCalls()

	_, err = srv.UpdateTask(ctx, &gritzv1.UpdateTaskRequest{
		Id: resp.Task.Id, Name: "updated",
	})
	assert.NilError(t, err)

	assert.DeepEqual(t, pub.PublishedNotifications(), []model.Notification{{
		Type: "change",
		Resources: []model.NotificationResource{
			{Action: "updated", Type: "task", ID: resp.Task.Id},
			{Action: "appended", Type: "task_events", ID: resp.Task.Id},
		},
		OrgID:  org.OrgID,
		Runner: "r",
		UserID: org.UserID,
	}}, cmpopts.IgnoreFields(model.Notification{}, "Time", "ChannelMessage"))
}

func TestCancelTask_Publishes(t *testing.T) {
	t.Parallel()

	pub := &pubsub.PublisherMock{
		PublishFunc: func(_ context.Context, _ model.Notification) error { return nil },
	}
	st := teststore.New(t)
	srv := New(Options{Store: st, Publisher: pub})
	org := teststore.CreateOrg(t, st, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "r", Name: "w"}}})
	ctx := createCtx(t, org)

	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name: "test", Runner: "r", Workspace: "w",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)
	pub.ResetCalls()

	_, err = srv.CancelTask(ctx, &gritzv1.CancelTaskRequest{Id: resp.Task.Id})
	assert.NilError(t, err)

	assert.DeepEqual(t, pub.PublishedNotifications(), []model.Notification{{
		Type: "change",
		Resources: []model.NotificationResource{
			{Action: "cancelled", Type: "task", ID: resp.Task.Id},
			{Action: "appended", Type: "task_events", ID: resp.Task.Id},
		},
		OrgID:  org.OrgID,
		UserID: org.UserID,
	}}, cmpopts.IgnoreFields(model.Notification{}, "Time", "ChannelMessage"))
}

func TestArchiveTask_Publishes(t *testing.T) {
	t.Parallel()

	pub := &pubsub.PublisherMock{
		PublishFunc: func(_ context.Context, _ model.Notification) error { return nil },
	}
	st := teststore.New(t)
	srv := New(Options{Store: st, Publisher: pub})
	org := teststore.CreateOrg(t, st, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "r", Name: "w"}}})
	ctx := createCtx(t, org)

	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name: "test", Runner: "r", Workspace: "w",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)
	// Archive requires a terminal status; mark the task COMPLETED first.
	dbTask, err := st.GetTask(ctx, nil, resp.Task.Id, org.OrgID)
	assert.NilError(t, err)
	dbTask.Status = 5 // COMPLETED
	dbTask.Command = 0
	assert.NilError(t, st.UpdateTask(ctx, nil, dbTask))
	pub.ResetCalls()

	_, err = srv.ArchiveTask(ctx, &gritzv1.ArchiveTaskRequest{Id: resp.Task.Id})
	assert.NilError(t, err)

	notifications := pub.PublishedNotifications()
	assert.DeepEqual(t, notifications, []model.Notification{{
		Type: "change",
		Resources: []model.NotificationResource{
			{Action: "archived", Type: "task", ID: resp.Task.Id},
			{Action: "appended", Type: "task_events", ID: resp.Task.Id},
		},
		OrgID:  org.OrgID,
		UserID: org.UserID,
	}}, cmpopts.IgnoreFields(model.Notification{}, "Time", "ChannelMessage"))
	msg := notifications[0].ChannelMessage
	assert.Assert(t, strings.Contains(msg, "archived"), "expected archived in message, got %q", msg)
}

func TestUploadLogs_Publishes(t *testing.T) {
	t.Parallel()

	pub := &pubsub.PublisherMock{
		PublishFunc: func(_ context.Context, _ model.Notification) error { return nil },
	}
	st := teststore.New(t)
	srv := New(Options{Store: st, Publisher: pub})
	org := teststore.CreateOrg(t, st, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "r", Name: "w"}}})
	ctx := createCtx(t, org)

	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name: "test", Runner: "r", Workspace: "w",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)
	pub.ResetCalls()

	_, err = srv.UploadLogs(ctx, &gritzv1.UploadLogsRequest{
		TaskId: resp.Task.Id,
		Entries: []*gritzv1.LogEntry{
			{Type: "llm", Content: "hello"},
		},
	})
	assert.NilError(t, err)

	assert.DeepEqual(t, pub.PublishedNotifications(), []model.Notification{{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "appended", Type: "task_events", ID: resp.Task.Id}},
		OrgID:     org.OrgID,
		UserID:    org.UserID,
	}}, cmpopts.IgnoreFields(model.Notification{}, "Time", "ChannelMessage"))
}

func TestCreateLink_Publishes(t *testing.T) {
	t.Parallel()

	pub := &pubsub.PublisherMock{
		PublishFunc: func(_ context.Context, _ model.Notification) error { return nil },
	}
	st := teststore.New(t)
	srv := New(Options{Store: st, Publisher: pub})
	org := teststore.CreateOrg(t, st, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "r", Name: "w"}}})
	ctx := createCtx(t, org)

	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name: "test", Runner: "r", Workspace: "w",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)
	pub.ResetCalls()

	linkResp, err := srv.CreateLink(ctx, &gritzv1.CreateLinkRequest{
		TaskId: resp.Task.Id,
		Url:    "https://example.com",
		Title:  "test",
	})
	assert.NilError(t, err)

	assert.DeepEqual(t, pub.PublishedNotifications(), []model.Notification{{
		Type: "change",
		Resources: []model.NotificationResource{
			{Action: "created", Type: "task_links", ID: resp.Task.Id},
			{Action: "created", Type: "link", ID: linkResp.Link.Id},
		},
		OrgID:  org.OrgID,
		UserID: org.UserID,
	}}, cmpopts.IgnoreFields(model.Notification{}, "Time", "ChannelMessage"))
}

func TestUpdateTask_ChannelMessage_QueuedOnStart(t *testing.T) {
	t.Parallel()

	pub := &pubsub.PublisherMock{
		PublishFunc: func(_ context.Context, _ model.Notification) error { return nil },
	}
	st := teststore.New(t)
	srv := New(Options{Store: st, Publisher: pub})
	org := teststore.CreateOrg(t, st, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "r", Name: "w"}}})
	ctx := createCtx(t, org)

	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name: "test", Runner: "r", Workspace: "w",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)
	// Move past the queued/start state so the next UpdateTask works from
	// a non-queued task; then re-queue via Start: true.
	_, err = srv.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: resp.Task.Id, Event: string(model.RunnerEventStarted), Version: 1},
			{TaskId: resp.Task.Id, Event: string(model.RunnerEventStopped), Version: 1},
		},
	})
	assert.NilError(t, err)
	pub.ResetCalls()

	_, err = srv.UpdateTask(ctx, &gritzv1.UpdateTaskRequest{
		Id: resp.Task.Id, Start: true,
	})
	assert.NilError(t, err)

	notifications := pub.PublishedNotifications()
	assert.Assert(t, cmp.Len(notifications, 1))
	msg := notifications[0].ChannelMessage
	assert.Assert(t, strings.Contains(msg, "queued"), "expected queued in message, got %q", msg)
}

func TestUpdateTask_NoChannelMessage_NameOnly(t *testing.T) {
	t.Parallel()

	pub := &pubsub.PublisherMock{
		PublishFunc: func(_ context.Context, _ model.Notification) error { return nil },
	}
	st := teststore.New(t)
	srv := New(Options{Store: st, Publisher: pub})
	org := teststore.CreateOrg(t, st, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "r", Name: "w"}}})
	ctx := createCtx(t, org)

	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name: "test", Runner: "r", Workspace: "w",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)
	// Transition to Running so PendingRunner == "" and the helper stays silent
	// on a name-only update.
	_, err = srv.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: resp.Task.Id, Event: string(model.RunnerEventStarted), Version: 1},
		},
	})
	assert.NilError(t, err)
	pub.ResetCalls()

	_, err = srv.UpdateTask(ctx, &gritzv1.UpdateTaskRequest{
		Id: resp.Task.Id, Name: "renamed",
	})
	assert.NilError(t, err)

	notifications := pub.PublishedNotifications()
	assert.Assert(t, cmp.Len(notifications, 1))
	assert.Equal(t, notifications[0].ChannelMessage, "")
}

func TestSubmitRunnerEvents_Publishes(t *testing.T) {
	t.Parallel()

	pub := &pubsub.PublisherMock{
		PublishFunc: func(_ context.Context, _ model.Notification) error { return nil },
	}
	st := teststore.New(t)
	srv := New(Options{Store: st, Publisher: pub})
	org := teststore.CreateOrg(t, st, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "r", Name: "w"}}})
	ctx := createCtx(t, org)

	taskResp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name: "test", Runner: "r", Workspace: "w",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)
	pub.ResetCalls()

	_, err = srv.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{
				TaskId:  taskResp.Task.Id,
				Event:   string(model.RunnerEventStarted),
				Version: 1,
			},
		},
	})
	assert.NilError(t, err)

	assert.DeepEqual(t, pub.PublishedNotifications(), []model.Notification{{
		Type: "change",
		Resources: []model.NotificationResource{
			{Action: "updated", Type: "task", ID: taskResp.Task.Id},
			{Action: "appended", Type: "task_events", ID: taskResp.Task.Id},
		},
		OrgID:  org.OrgID,
		UserID: org.UserID,
	}}, cmpopts.IgnoreFields(model.Notification{}, "Time", "ChannelMessage"))
}

func TestSubmitRunnerEvents_NotApplied_DoesNotPublish(t *testing.T) {
	t.Parallel()

	pub := &pubsub.PublisherMock{
		PublishFunc: func(_ context.Context, _ model.Notification) error { return nil },
	}
	st := teststore.New(t)
	srv := New(Options{Store: st, Publisher: pub})
	org := teststore.CreateOrg(t, st, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "r", Name: "w"}}})
	ctx := createCtx(t, org)

	taskResp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name: "test", Runner: "r", Workspace: "w",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)
	pub.ResetCalls()

	// Stale version: ApplyRunnerEvent returns false and the notification's
	// Ignore field is set inside the tx, so publish is a no-op.
	_, err = srv.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: taskResp.Task.Id, Event: string(model.RunnerEventStarted), Version: 999},
		},
	})
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(pub.PublishedNotifications(), 0))
}

func TestServerPublish_IgnoreSuppressesDelivery(t *testing.T) {
	t.Parallel()

	pub := &pubsub.PublisherMock{
		PublishFunc: func(_ context.Context, _ model.Notification) error { return nil },
	}
	srv := New(Options{Publisher: pub})

	srv.publish(model.Notification{Type: "change", OrgID: 1, Ignore: true})
	assert.Assert(t, cmp.Len(pub.PublishedNotifications(), 0))

	srv.publish(model.Notification{Type: "change", OrgID: 1})
	assert.Assert(t, cmp.Len(pub.PublishedNotifications(), 1))
}
