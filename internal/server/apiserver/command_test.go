package apiserver

import (
	"testing"

	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestCreateTask_CommandEvent(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)

	events, err := srv.store.ListEventsByTask(ctx, nil, resp.Task.Id, org.OrgID, []string{model.EventTypeCommand})
	assert.NilError(t, err)
	assert.DeepEqual(t,
		model.FilterPayloads(events, model.EventTypeCommand),
		[]model.EventPayload{
			&model.CommandPayload{Command: model.TaskCommandStart, Version: 1, Runner: "test-runner"},
		},
	)
}

func TestCreateTask_DraftNoCommandEvent(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Runner:    "test-runner",
		Workspace: "test-workspace",
		Draft:     true,
	})
	assert.NilError(t, err)

	events, err := srv.store.ListEventsByTask(ctx, nil, resp.Task.Id, org.OrgID, []string{model.EventTypeCommand})
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(events, 0))
}

func TestUpdateTask_StartCommandEvent(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)
	task := teststore.CreateTask(t, srv.store, org, &teststore.TaskOptions{
		Status:    model.TaskStatusCompleted,
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})

	_, err := srv.UpdateTask(ctx, &gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	assert.NilError(t, err)

	events, err := srv.store.ListEventsByTask(ctx, nil, task.ID, org.OrgID, []string{model.EventTypeCommand})
	assert.NilError(t, err)
	assert.DeepEqual(t,
		model.FilterPayloads(events, model.EventTypeCommand),
		[]model.EventPayload{
			&model.CommandPayload{Command: model.TaskCommandStart, Version: 1, Runner: "test-runner"},
		},
	)
}

func TestUpdateTask_UnchangedCommandNoEvent(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)
	task := teststore.CreateTask(t, srv.store, org, &teststore.TaskOptions{
		Status:    model.TaskStatusCompleted,
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})

	_, err := srv.UpdateTask(ctx, &gritzv1.UpdateTaskRequest{Id: task.ID, Name: "renamed"})
	assert.NilError(t, err)

	events, err := srv.store.ListEventsByTask(ctx, nil, task.ID, org.OrgID, []string{model.EventTypeCommand})
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(events, 0))
}

func TestRestartTask_CommandEvent(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)
	task := teststore.CreateTask(t, srv.store, org, &teststore.TaskOptions{
		Status:    model.TaskStatusFailed,
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})

	_, err := srv.RestartTask(ctx, &gritzv1.RestartTaskRequest{Id: task.ID})
	assert.NilError(t, err)

	events, err := srv.store.ListEventsByTask(ctx, nil, task.ID, org.OrgID, []string{model.EventTypeCommand})
	assert.NilError(t, err)
	assert.DeepEqual(t,
		model.FilterPayloads(events, model.EventTypeCommand),
		[]model.EventPayload{
			&model.CommandPayload{Command: model.TaskCommandRestart, Version: 1, Runner: "test-runner"},
		},
	)
}

func TestCancelTask_PendingCommandEvent(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)
	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)

	_, err = srv.CancelTask(ctx, &gritzv1.CancelTaskRequest{Id: resp.Task.Id})
	assert.NilError(t, err)

	// The withdrawal is a none command for the start's version.
	events, err := srv.store.ListEventsByTask(ctx, nil, resp.Task.Id, org.OrgID, []string{model.EventTypeCommand})
	assert.NilError(t, err)
	assert.DeepEqual(t,
		model.FilterPayloads(events, model.EventTypeCommand),
		[]model.EventPayload{
			&model.CommandPayload{Command: model.TaskCommandStart, Version: 1, Runner: "test-runner"},
			&model.CommandPayload{Command: model.TaskCommandNone, Version: 1, Runner: "test-runner"},
		},
	)
}

func TestSubmitRunnerEvents_ClearCommandEvent(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)
	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)

	_, err = srv.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: resp.Task.Id, Event: "started", Version: 1}},
	})
	assert.NilError(t, err)

	events, err := srv.store.ListEventsByTask(ctx, nil, resp.Task.Id, org.OrgID, []string{model.EventTypeCommand})
	assert.NilError(t, err)
	assert.DeepEqual(t,
		model.FilterPayloads(events, model.EventTypeCommand),
		[]model.EventPayload{
			&model.CommandPayload{Command: model.TaskCommandStart, Version: 1, Runner: "test-runner"},
			&model.CommandPayload{Command: model.TaskCommandNone, Version: 1, Runner: "test-runner"},
		},
	)
}

func TestSubmitRunnerEvents_ServerIssuedStopCommandEvent(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)
	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)
	_, err = srv.CancelTask(ctx, &gritzv1.CancelTaskRequest{Id: resp.Task.Id})
	assert.NilError(t, err)

	// The runner had already acted on the withdrawn start: the server stops it.
	_, err = srv.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: resp.Task.Id, Event: "started", Version: 1}},
	})
	assert.NilError(t, err)

	events, err := srv.store.ListEventsByTask(ctx, nil, resp.Task.Id, org.OrgID, []string{model.EventTypeCommand})
	assert.NilError(t, err)
	assert.DeepEqual(t,
		model.FilterPayloads(events, model.EventTypeCommand),
		[]model.EventPayload{
			&model.CommandPayload{Command: model.TaskCommandStart, Version: 1, Runner: "test-runner"},
			&model.CommandPayload{Command: model.TaskCommandNone, Version: 1, Runner: "test-runner"},
			&model.CommandPayload{Command: model.TaskCommandStop, Version: 1, Runner: "test-runner"},
		},
	)
}
