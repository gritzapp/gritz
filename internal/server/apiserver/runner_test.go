package apiserver_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestSubmitRunnerEvents(t *testing.T) {
	t.Parallel()
	// Arrange - a pending task with a start command.
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Test Task", Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	got := h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID})
	assert.Equal(t, got.Status, gritzv1.TaskStatus_PENDING)
	assert.Equal(t, got.Command, gritzv1.TaskCommand_START)
	assert.Equal(t, got.Version, int64(1))

	// Act - the run starts.
	_, err := h.Org.Client.SubmitRunnerEvents(t.Context(), &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: task.ID, Event: "started", Version: 1},
		},
	})

	// Assert - the task is running and its command is cleared.
	assert.NilError(t, err)
	got = h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID})
	assert.Equal(t, got.Status, gritzv1.TaskStatus_RUNNING)
	assert.Equal(t, got.Command, gritzv1.TaskCommand_NONE)

	// Act - the run exits on its own; version 0 skips the version check.
	_, err = h.Org.Client.SubmitRunnerEvents(t.Context(), &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: task.ID, Event: "stopped", Version: 0},
		},
	})

	// Assert
	assert.NilError(t, err)
	got = h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID})
	assert.Equal(t, got.Status, gritzv1.TaskStatus_COMPLETED)
}

func TestSubmitRunnerEvents_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "User A's Task", Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})

	// Act
	_, err := other.Client.SubmitRunnerEvents(t.Context(), &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: task.ID, Event: "started", Version: 1},
		},
	})

	// Assert
	assert.ErrorContains(t, err, "not found")
}

func TestListRunnerEvents(t *testing.T) {
	t.Parallel()
	// Arrange - a started task on each runner; starting one writes its command
	// event.
	h := e2e.New(t, nil)
	h.Org.RegisterWorkspaces(&gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{
			{Name: "test-workspace"},
		},
	})
	h.Org.RegisterWorkspaces(&gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-2",
		Workspaces: []*gritzv1.RegisteredWorkspace{
			{Name: "test-workspace"},
		},
	})
	// runner-2's task is started first, so once runner-1's events pass the
	// watermark, runner-2's have too.
	other := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Draft:     true,
		Name:      "other",
		Runner:    "runner-2",
		Workspace: "test-workspace",
	})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: other.ID, Start: true})
	mine := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Draft:     true,
		Name:      "mine",
		Runner:    "runner-1",
		Workspace: "test-workspace",
	})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: mine.ID, Start: true})
	want := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{TaskId: mine.ID})
	h.Org.WaitRunnerEvents("runner-1", len(want))

	// Act - read runner-1's log from the start.
	resp, err := h.Org.Client.ListRunnerEvents(t.Context(), &gritzv1.ListRunnerEventsRequest{
		Runner: "runner-1",
	})

	// Assert - every event of runner-1's task, and nothing of runner-2's.
	assert.NilError(t, err)
	assert.DeepEqual(t, testx.ExtractField(resp.Events, "Id"), testx.ExtractField(want, "Id"))
	assert.Assert(t, resp.NextPageToken != "")
}

func TestListRunnerEvents_EmptyPage(t *testing.T) {
	t.Parallel()
	// Arrange - read the runner's whole log.
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "task", Draft: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	want := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{TaskId: task.ID})
	token := h.Org.WaitRunnerEvents(e2e.DefaultRunner, len(want))

	// Act
	resp, err := h.Org.Client.ListRunnerEvents(t.Context(), &gritzv1.ListRunnerEventsRequest{
		Runner:    e2e.DefaultRunner,
		PageToken: token,
	})

	// Assert - nothing new, and the request's token comes back unchanged.
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(resp.Events, 0))
	assert.Equal(t, resp.NextPageToken, token)
}

func TestListRunnerEvents_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange - a caller scoped to runner-1's tasks.
	h := e2e.New(t, nil)
	key := h.Org.CreateScopedKey(authscope.Scopes{
		authscope.New(
			authscope.OpTaskRead,
			authscope.WithTaskRunner("runner-1"),
		),
	})

	// Act
	_, errOwn := key.Client.ListRunnerEvents(t.Context(), &gritzv1.ListRunnerEventsRequest{Runner: "runner-1"})
	_, errOther := key.Client.ListRunnerEvents(t.Context(), &gritzv1.ListRunnerEventsRequest{Runner: "runner-2"})

	// Assert
	assert.NilError(t, errOwn)
	assert.Equal(t, connect.CodeOf(errOther), connect.CodePermissionDenied)
}

func TestListRunnerEvents_BadRequest(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, nil)
	tests := []struct {
		name string
		req  *gritzv1.ListRunnerEventsRequest
	}{
		{"no runner", &gritzv1.ListRunnerEventsRequest{}},
		{"bad token", &gritzv1.ListRunnerEventsRequest{Runner: "runner-1", PageToken: "!"}},
		{"negative page size", &gritzv1.ListRunnerEventsRequest{Runner: "runner-1", PageSize: -1}},
		{
			"page size too large",
			&gritzv1.ListRunnerEventsRequest{Runner: "runner-1", PageSize: 1001},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := h.Org.Client.ListRunnerEvents(t.Context(), tt.req)
			assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
		})
	}
}
