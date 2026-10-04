package apiserver

import (
	"testing"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
)

func TestCreateLink(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)
	taskResp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Task with Link",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)

	// Act
	resp, err := srv.CreateLink(ctx, &gritzv1.CreateLinkRequest{
		TaskId:    taskResp.Task.Id,
		Relevance: "Related PR",
		Url:       "https://github.com/example/repo/pull/123",
		Title:     "Fix bug",
		Subscribe: true,
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, resp.Link.TaskId, taskResp.Task.Id)
	assert.Equal(t, resp.Link.Url, "https://github.com/example/repo/pull/123")
	assert.Equal(t, resp.Link.Subscribe, true)
}

func TestCreateLink_AppendsLinkEvent(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)
	taskResp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Task with Link",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)

	// Act
	linkResp, err := srv.CreateLink(ctx, &gritzv1.CreateLinkRequest{
		TaskId:    taskResp.Task.Id,
		Relevance: "Related PR",
		Url:       "https://github.com/example/repo/pull/123",
		Title:     "Fix bug",
		Subscribe: true,
	})
	assert.NilError(t, err)

	// Assert - a link event mirroring the task_links row is appended to the
	// task's stream (the timeline source of truth; task_links is the projection).
	eventsResp, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{
		TaskId: taskResp.Task.Id,
	})
	assert.NilError(t, err)
	// The stream also carries the lifecycle CREATED event from task creation, so
	// pick out the link event rather than asserting it is the only one.
	var linkEvent *gritzv1.Event
	for _, e := range eventsResp.Events {
		if e.GetLink() != nil {
			linkEvent = e
		}
	}
	assert.Assert(t, linkEvent != nil)
	link := linkEvent.GetLink()
	assert.Equal(t, link.LinkId, linkResp.Link.Id)
	assert.Equal(t, link.Relevance, "Related PR")
	assert.Equal(t, link.Url, "https://github.com/example/repo/pull/123")
	assert.Equal(t, link.Title, "Fix bug")
	assert.Equal(t, link.Subscribe, true)
	// The link event is about-task, not to-agent, so it does not wake the task.
	assert.Equal(t, linkEvent.Wake, false)
}

func TestCreateLink_DerivesRoutingKey(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)
	taskResp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Task with Link",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)

	// Act - no routing_key supplied, so the server derives it from url
	resp, err := srv.CreateLink(ctx, &gritzv1.CreateLinkRequest{
		TaskId: taskResp.Task.Id,
		Url:    "https://github.com/example/repo/pull/123#issuecomment-9",
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, resp.Link.Url, "https://github.com/example/repo/pull/123#issuecomment-9")
	assert.Equal(t, resp.Link.RoutingKey, "https://github.com/example/repo/pull/123")
}

func TestCreateLink_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	orgA := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxA := createCtx(t, orgA)
	orgB := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxB := createCtx(t, orgB)
	taskResp, err := srv.CreateTask(ctxA, &gritzv1.CreateTaskRequest{
		Name:      "User A's Task",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)

	// Act
	_, err = srv.CreateLink(ctxB, &gritzv1.CreateLinkRequest{
		TaskId:    taskResp.Task.Id,
		Relevance: "Sneaky link",
		Url:       "https://github.com/example/repo/pull/123",
	})

	// Assert
	assert.ErrorContains(t, err, "not found")
}

func TestListLinks(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)
	taskResp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Task with Links",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)
	_, err = srv.CreateLink(ctx, &gritzv1.CreateLinkRequest{
		TaskId:    taskResp.Task.Id,
		Relevance: "Link 1",
		Url:       "https://github.com/example/repo/pull/1",
	})
	assert.NilError(t, err)
	_, err = srv.CreateLink(ctx, &gritzv1.CreateLinkRequest{
		TaskId:    taskResp.Task.Id,
		Relevance: "Link 2",
		Url:       "https://github.com/example/repo/pull/2",
	})
	assert.NilError(t, err)

	// Act
	resp, err := srv.ListLinks(ctx, &gritzv1.ListLinksRequest{
		TaskId: taskResp.Task.Id,
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, len(resp.Links), 2)
}

func TestListLinks_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	orgA := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxA := createCtx(t, orgA)
	orgB := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxB := createCtx(t, orgB)
	taskResp, err := srv.CreateTask(ctxA, &gritzv1.CreateTaskRequest{
		Name:      "User A's Task",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)
	_, err = srv.CreateLink(ctxA, &gritzv1.CreateLinkRequest{
		TaskId:    taskResp.Task.Id,
		Relevance: "User A's Link",
		Url:       "https://github.com/example/repo/pull/1",
	})
	assert.NilError(t, err)

	// Act
	resp, err := srv.ListLinks(ctxB, &gritzv1.ListLinksRequest{
		TaskId: taskResp.Task.Id,
	})

	// Assert - User B gets empty list, not an error (blanket read skips the row
	// load; the list query is org-scoped).
	assert.NilError(t, err)
	assert.Equal(t, len(resp.Links), 0)
}
