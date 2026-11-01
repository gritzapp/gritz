package apiserver

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
	"gotest.tools/v3/poll"
)

func TestSubmitRunnerEvents(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	// Create a task (starts as pending with start command)
	createResp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Test Task",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)
	taskID := createResp.Task.Id

	// Verify initial state
	getResp, err := srv.GetTask(ctx, &gritzv1.GetTaskRequest{Id: taskID})
	assert.NilError(t, err)
	assert.Equal(t, getResp.Task.Status, gritzv1.TaskStatus_PENDING)
	assert.Equal(t, getResp.Task.Command, gritzv1.TaskCommand_START)
	assert.Equal(t, getResp.Task.Version, int64(1))

	// Send started event (simulating container start)
	_, err = srv.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{
				TaskId:  taskID,
				Event:   "started",
				Version: 1,
			},
		},
	})
	assert.NilError(t, err)

	// Verify task is running and command is cleared
	getResp, err = srv.GetTask(ctx, &gritzv1.GetTaskRequest{Id: taskID})
	assert.NilError(t, err)
	assert.Equal(t, getResp.Task.Status, gritzv1.TaskStatus_RUNNING)
	assert.Equal(t, getResp.Task.Command, gritzv1.TaskCommand_NONE)

	// Send stopped event (simulating container exit with code 0)
	// Use version 0 to bypass version check (spontaneous event)
	_, err = srv.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{
				TaskId:  taskID,
				Event:   "stopped",
				Version: 0,
			},
		},
	})
	assert.NilError(t, err)

	// Verify task status was updated to completed
	getResp, err = srv.GetTask(ctx, &gritzv1.GetTaskRequest{Id: taskID})
	assert.NilError(t, err)
	assert.Equal(t, getResp.Task.Status, gritzv1.TaskStatus_COMPLETED)
}

func TestSubmitRunnerEvents_Permissions(t *testing.T) {
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
	_, err = srv.SubmitRunnerEvents(ctxB, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: taskResp.Task.Id, Event: "started", Version: 1},
		},
	})

	// Assert
	assert.ErrorContains(t, err, "not found")
}

func TestListRunnerTasks(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{
		Workspaces: []teststore.WorkspaceOptions{
			{RunnerID: "runner-1", Name: "test-workspace"},
			{RunnerID: "runner-2", Name: "test-workspace"},
		},
	})
	ctx := createCtx(t, org)
	_, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Task for runner-1",
		Workspace: "test-workspace",
		Runner:    "runner-1",
	})
	assert.NilError(t, err)
	_, err = srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Task for runner-2",
		Workspace: "test-workspace",
		Runner:    "runner-2",
	})
	assert.NilError(t, err)

	// Act
	resp, err := srv.ListRunnerTasks(ctx, &gritzv1.ListRunnerTasksRequest{
		Runner: "runner-1",
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, len(resp.Tasks), 1)
	assert.Equal(t, resp.Tasks[0].Name, "Task for runner-1")
}

func TestListRunnerTasks_OnlyWithCommand(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{
		Workspaces: []teststore.WorkspaceOptions{
			{RunnerID: "runner-1", Name: "test-workspace"},
		},
	})
	ctx := createCtx(t, org)
	taskResp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Task with command",
		Workspace: "test-workspace",
		Runner:    "runner-1",
	})
	assert.NilError(t, err)
	_, err = srv.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: taskResp.Task.Id, Event: "started", Version: 1},
		},
	})
	assert.NilError(t, err)

	// Act
	resp, err := srv.ListRunnerTasks(ctx, &gritzv1.ListRunnerTasksRequest{
		Runner: "runner-1",
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, len(resp.Tasks), 0)
}

func TestListRunnerTasks_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	runnerWorkspaces := &teststore.OrgOptions{
		Workspaces: []teststore.WorkspaceOptions{
			{RunnerID: "runner-1", Name: "test-workspace"},
		},
	}
	orgA := teststore.CreateOrg(t, srv.store, runnerWorkspaces)
	ctxA := createCtx(t, orgA)
	orgB := teststore.CreateOrg(t, srv.store, runnerWorkspaces)
	ctxB := createCtx(t, orgB)
	_, err := srv.CreateTask(ctxA, &gritzv1.CreateTaskRequest{
		Name:      "User A's Task",
		Workspace: "test-workspace",
		Runner:    "runner-1",
	})
	assert.NilError(t, err)
	_, err = srv.CreateTask(ctxB, &gritzv1.CreateTaskRequest{
		Name:      "User B's Task",
		Workspace: "test-workspace",
		Runner:    "runner-1",
	})
	assert.NilError(t, err)

	// Act
	respA, err := srv.ListRunnerTasks(ctxA, &gritzv1.ListRunnerTasksRequest{
		Runner: "runner-1",
	})
	assert.NilError(t, err)
	respB, err := srv.ListRunnerTasks(ctxB, &gritzv1.ListRunnerTasksRequest{
		Runner: "runner-1",
	})
	assert.NilError(t, err)

	// Assert
	assert.Equal(t, len(respA.Tasks), 1)
	assert.Equal(t, respA.Tasks[0].Name, "User A's Task")
	assert.Equal(t, len(respB.Tasks), 1)
	assert.Equal(t, respB.Tasks[0].Name, "User B's Task")
}

// listRunnerEventsAll follows ListRunnerEvents from cursor until at least n
// events have come back. The xid watermark is cluster-wide, so a transaction
// held open by a concurrent test can briefly hold back committed rows.
func listRunnerEventsAll(t *testing.T, srv *Server, ctx context.Context, runner string, token string, n int) ([]*gritzv1.Event, string) {
	t.Helper()
	var got []*gritzv1.Event
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		resp, err := srv.ListRunnerEvents(ctx, &gritzv1.ListRunnerEventsRequest{Runner: runner, PageToken: token})
		if err != nil {
			return poll.Error(err)
		}
		got = append(got, resp.Events...)
		token = resp.NextPageToken
		if len(got) < n {
			return poll.Continue("got %d of %d events", len(got), n)
		}
		return poll.Success()
	}, poll.WithTimeout(10*time.Second), poll.WithDelay(20*time.Millisecond))
	return got, token
}

func TestListRunnerEvents(t *testing.T) {
	t.Parallel()
	// Arrange - a task on each runner; creating one writes its command event.
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{
		Workspaces: []teststore.WorkspaceOptions{
			{RunnerID: "runner-1", Name: "test-workspace"},
			{RunnerID: "runner-2", Name: "test-workspace"},
		},
	})
	ctx := createCtx(t, org)
	mine, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{Name: "mine", Workspace: "test-workspace", Runner: "runner-1"})
	assert.NilError(t, err)
	_, err = srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{Name: "other", Workspace: "test-workspace", Runner: "runner-2"})
	assert.NilError(t, err)
	// The later task's events passing the watermark means the earlier one's have.
	listRunnerEventsAll(t, srv, ctx, "runner-2", "", 1)
	want, err := srv.store.ListEventsByTask(ctx, nil, mine.Task.Id, org.OrgID, nil)
	assert.NilError(t, err)

	// Act - read runner-1's log from the start.
	events, token := listRunnerEventsAll(t, srv, ctx, "runner-1", "", 1)

	// Assert - every event of runner-1's task, and nothing of runner-2's.
	assert.DeepEqual(t, testx.ExtractField(events, "Id"), testx.ExtractField(want, "ID"))
	assert.Assert(t, token != "")
}

func TestListRunnerEvents_EmptyPage(t *testing.T) {
	t.Parallel()
	// Arrange - read the runner's whole log.
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{
		Workspaces: []teststore.WorkspaceOptions{{RunnerID: "runner-1", Name: "test-workspace"}},
	})
	ctx := createCtx(t, org)
	_, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{Name: "task", Workspace: "test-workspace", Runner: "runner-1"})
	assert.NilError(t, err)
	_, token := listRunnerEventsAll(t, srv, ctx, "runner-1", "", 1)

	// Act
	resp, err := srv.ListRunnerEvents(ctx, &gritzv1.ListRunnerEventsRequest{Runner: "runner-1", PageToken: token})

	// Assert - nothing new, and the request's token comes back unchanged.
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(resp.Events, 0))
	assert.Equal(t, resp.NextPageToken, token)
}

func TestListRunnerEvents_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange - a caller scoped to runner-1's tasks.
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := scopedCtx(t, org, authscope.Scopes{
		authscope.New(authscope.OpTaskRead, authscope.WithTaskRunner("runner-1")),
	})

	// Act
	_, errOwn := srv.ListRunnerEvents(ctx, &gritzv1.ListRunnerEventsRequest{Runner: "runner-1"})
	_, errOther := srv.ListRunnerEvents(ctx, &gritzv1.ListRunnerEventsRequest{Runner: "runner-2"})

	// Assert
	assert.NilError(t, errOwn)
	assert.Equal(t, connect.CodeOf(errOther), connect.CodePermissionDenied)
}

func TestListRunnerEvents_BadRequest(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)
	tests := []struct {
		name string
		req  *gritzv1.ListRunnerEventsRequest
	}{
		{"no runner", &gritzv1.ListRunnerEventsRequest{}},
		{"bad token", &gritzv1.ListRunnerEventsRequest{Runner: "runner-1", PageToken: "!"}},
		{"negative page size", &gritzv1.ListRunnerEventsRequest{Runner: "runner-1", PageSize: -1}},
		{"page size too large", &gritzv1.ListRunnerEventsRequest{Runner: "runner-1", PageSize: 1001}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := srv.ListRunnerEvents(ctx, tt.req)
			assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
		})
	}
}
