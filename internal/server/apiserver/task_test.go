package apiserver

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/durationpb"
	"gotest.tools/v3/assert"
)

func TestGetTask(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	// Create a task using the API
	createResp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Test Task",
		Runner:    "test-runner",
		Workspace: "test-workspace",
		Instructions: []*gritzv1.Instruction{
			{
				Text: "Do something important",
				Url:  "https://example.com/issue/1",
			},
			{
				Text: "Do something else",
				Url:  "https://example.com/issue/2",
			},
		},
	})
	assert.NilError(t, err)

	// Get the task via the API
	getResp, err := srv.GetTask(ctx, &gritzv1.GetTaskRequest{
		Id: createResp.Task.Id,
	})
	assert.NilError(t, err)

	// Verify the task matches what we created. Instructions are no longer a task
	// field — they are instruction events in the stream, asserted below.
	expected := &gritzv1.Task{
		Id:          createResp.Task.Id,
		Name:        "Test Task",
		Runner:      "test-runner",
		Workspace:   "test-workspace",
		Status:      gritzv1.TaskStatus_PENDING,
		Command:     gritzv1.TaskCommand_START,
		Actions:     &gritzv1.TaskActions{Cancel: true},
		Version:     1,
		CreatedAt:   getResp.Task.CreatedAt, // Copy timestamps since we can't predict them
		UpdatedAt:   getResp.Task.UpdatedAt,
		AutoArchive: durationpb.New(0),
	}

	assert.DeepEqual(t, getResp.Task, expected, protocmp.Transform())

	// The initial instructions are seeded into the stream as instruction events.
	eventsResp, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{TaskId: createResp.Task.Id})
	assert.NilError(t, err)
	assert.DeepEqual(t, instructionPayloads(eventsResp.Events), []*gritzv1.InstructionPayload{
		{Text: "Do something important", Url: "https://example.com/issue/1"},
		{Text: "Do something else", Url: "https://example.com/issue/2"},
	}, protocmp.Transform())
}

// instructionPayloads extracts the instruction-arm payloads from a stream, in
// order — the brief's instruction projection.
func instructionPayloads(events []*gritzv1.Event) []*gritzv1.InstructionPayload {
	var out []*gritzv1.InstructionPayload
	for _, e := range events {
		if inst := e.GetInstruction(); inst != nil {
			out = append(out, inst)
		}
	}
	return out
}

func TestGetTask_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	orgA := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxA := createCtx(t, orgA)
	orgB := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxB := createCtx(t, orgB)
	createResp, err := srv.CreateTask(ctxA, &gritzv1.CreateTaskRequest{
		Name:      "User A's Task",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)

	// Act
	_, errA := srv.GetTask(ctxA, &gritzv1.GetTaskRequest{Id: createResp.Task.Id})
	_, errB := srv.GetTask(ctxB, &gritzv1.GetTaskRequest{Id: createResp.Task.Id})

	// Assert
	assert.NilError(t, errA)
	assert.ErrorContains(t, errB, "not found")
}

func TestCreateTask(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	// Act
	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "New Task",
		Runner:    "test-runner",
		Workspace: "test-workspace",
		Instructions: []*gritzv1.Instruction{
			{
				Text: "Do something",
				Url:  "https://example.com/issue/1",
			},
		},
	})

	// Assert
	assert.NilError(t, err)
	expected := &gritzv1.Task{
		Id:          resp.Task.Id,
		Name:        "New Task",
		Runner:      "test-runner",
		Workspace:   "test-workspace",
		Status:      gritzv1.TaskStatus_PENDING,
		Command:     gritzv1.TaskCommand_START,
		Actions:     &gritzv1.TaskActions{Cancel: true},
		Version:     1,
		CreatedAt:   resp.Task.CreatedAt,
		UpdatedAt:   resp.Task.UpdatedAt,
		AutoArchive: durationpb.New(0),
	}
	assert.DeepEqual(t, resp.Task, expected, protocmp.Transform())

	// The initial instruction is seeded into the stream as an instruction event.
	eventsResp, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{TaskId: resp.Task.Id})
	assert.NilError(t, err)
	assert.DeepEqual(t, instructionPayloads(eventsResp.Events), []*gritzv1.InstructionPayload{
		{Text: "Do something", Url: "https://example.com/issue/1"},
	}, protocmp.Transform())
}

// A draft task carries no command, so no runner picks it up, and the first
// instruction from the task page's composer starts it.
func TestCreateTask_Draft(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	// Act
	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Draft Task",
		Runner:    "test-runner",
		Workspace: "test-workspace",
		Draft:     true,
	})

	// Assert: draft, no command, and startable + archivable. Not cancellable and
	// not restartable — there is nothing to cancel, and nothing to restart.
	assert.NilError(t, err)
	expected := &gritzv1.Task{
		Id:          resp.Task.Id,
		Name:        "Draft Task",
		Runner:      "test-runner",
		Workspace:   "test-workspace",
		Status:      gritzv1.TaskStatus_DRAFT,
		Command:     gritzv1.TaskCommand_NONE,
		Actions:     &gritzv1.TaskActions{Start: true, Archive: true},
		Version:     0,
		CreatedAt:   resp.Task.CreatedAt,
		UpdatedAt:   resp.Task.UpdatedAt,
		AutoArchive: durationpb.New(0),
	}
	assert.DeepEqual(t, resp.Task, expected, protocmp.Transform())

	// No instruction events, but the Created lifecycle event is still there so
	// the timeline is not empty.
	eventsResp, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{TaskId: resp.Task.Id})
	assert.NilError(t, err)
	assert.Equal(t, len(instructionPayloads(eventsResp.Events)), 0)
	assert.Equal(t, len(eventsResp.Events), 1)
	assert.Equal(t, eventsResp.Events[0].GetLifecycle().Kind, gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED)

	// No command, so no runner ever sees it: nothing is launched.
	runnerResp, err := srv.ListRunnerTasks(ctx, &gritzv1.ListRunnerTasksRequest{Runner: "test-runner"})
	assert.NilError(t, err)
	assert.Equal(t, len(runnerResp.Tasks), 0)
}

// The first instruction from the composer starts a draft task: it lands on
// PENDING/START — the row an ordinary create presents — and the instruction
// event wakes.
func TestCreateTask_Draft_FirstInstructionStarts(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)
	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Draft Task",
		Runner:    "test-runner",
		Workspace: "test-workspace",
		Draft:     true,
	})
	assert.NilError(t, err)

	// Act: exactly what the task page composer sends.
	_, err = srv.UpdateTask(ctx, &gritzv1.UpdateTaskRequest{
		Id:              resp.Task.Id,
		Start:           true,
		AddInstructions: []*gritzv1.Instruction{{Text: "Do something"}},
	})

	// Assert
	assert.NilError(t, err)
	getResp, err := srv.GetTask(ctx, &gritzv1.GetTaskRequest{Id: resp.Task.Id})
	assert.NilError(t, err)
	assert.Equal(t, getResp.Task.Status, gritzv1.TaskStatus_PENDING)
	assert.Equal(t, getResp.Task.Command, gritzv1.TaskCommand_START)
	// The draft was never provisioned (version 0), so its first run is run 1 —
	// the same run number an ordinary create starts at.
	assert.Equal(t, getResp.Task.Version, int64(1))

	// The runner now has work for it.
	runnerResp, err := srv.ListRunnerTasks(ctx, &gritzv1.ListRunnerTasksRequest{Runner: "test-runner"})
	assert.NilError(t, err)
	assert.Equal(t, len(runnerResp.Tasks), 1)
	assert.Equal(t, runnerResp.Tasks[0].Id, resp.Task.Id)

	// The instruction is in the stream and woke the task.
	eventsResp, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{TaskId: resp.Task.Id})
	assert.NilError(t, err)
	assert.DeepEqual(t, instructionPayloads(eventsResp.Events), []*gritzv1.InstructionPayload{
		{Text: "Do something"},
	}, protocmp.Transform())
	for _, e := range eventsResp.Events {
		if e.GetInstruction() != nil {
			assert.Assert(t, e.Wake, "the first instruction must wake the task")
		}
	}
}

func TestListTasks(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)
	_, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Task 1",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)
	_, err = srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Task 2",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)

	// Act
	resp, err := srv.ListTasks(ctx, &gritzv1.ListTasksRequest{})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, len(resp.Tasks), 2)
}

func TestListTasks_IncludeArchived(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	// One active task and one archived task in the same org.
	_, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{Name: "active", Runner: "test-runner", Workspace: "test-workspace"})
	assert.NilError(t, err)
	assert.NilError(t, srv.store.CreateTask(ctx, nil, &model.Task{
		Runner: "test-runner", Workspace: "test-workspace", Status: model.TaskStatusCompleted, OrgID: org.OrgID, Archived: true,
	}))

	// The default request (archived unset) returns active tasks only, unchanged.
	def, err := srv.ListTasks(ctx, &gritzv1.ListTasksRequest{})
	assert.NilError(t, err)
	assert.Equal(t, len(def.Tasks), 1)
	assert.Assert(t, !def.Tasks[0].Archived)

	// Archived: false is likewise active-only.
	off, err := srv.ListTasks(ctx, &gritzv1.ListTasksRequest{Archived: false})
	assert.NilError(t, err)
	assert.Equal(t, len(off.Tasks), 1)

	// Archived: true returns active and archived rows together.
	on, err := srv.ListTasks(ctx, &gritzv1.ListTasksRequest{Archived: true})
	assert.NilError(t, err)
	assert.Equal(t, len(on.Tasks), 2)
}

func TestListTasks_ArchivedTokenMismatch(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	// Two active and two archived rows so each filter mints a next-page token.
	_, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{Name: "active-1", Runner: "test-runner", Workspace: "test-workspace"})
	assert.NilError(t, err)
	_, err = srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{Name: "active-2", Runner: "test-runner", Workspace: "test-workspace"})
	assert.NilError(t, err)
	assert.NilError(t, srv.store.CreateTask(ctx, nil, &model.Task{
		Runner: "test-runner", Workspace: "test-workspace", Status: model.TaskStatusCompleted, OrgID: org.OrgID, Archived: true,
	}))
	assert.NilError(t, srv.store.CreateTask(ctx, nil, &model.Task{
		Runner: "test-runner", Workspace: "test-workspace", Status: model.TaskStatusCompleted, OrgID: org.OrgID, Archived: true,
	}))

	// A token minted under archived=true, replayed under archived=false → invalid.
	archivedPage, err := srv.ListTasks(ctx, &gritzv1.ListTasksRequest{PageSize: 1, Archived: true})
	assert.NilError(t, err)
	assert.Assert(t, archivedPage.NextPageToken != "")
	_, err = srv.ListTasks(ctx, &gritzv1.ListTasksRequest{PageSize: 1, PageToken: archivedPage.NextPageToken, Archived: false})
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)

	// And the reverse: an active-minted token replayed under archived=true.
	activePage, err := srv.ListTasks(ctx, &gritzv1.ListTasksRequest{PageSize: 1, Archived: false})
	assert.NilError(t, err)
	assert.Assert(t, activePage.NextPageToken != "")
	_, err = srv.ListTasks(ctx, &gritzv1.ListTasksRequest{PageSize: 1, PageToken: activePage.NextPageToken, Archived: true})
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
}

func TestListTasks_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	orgA := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxA := createCtx(t, orgA)
	orgB := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxB := createCtx(t, orgB)
	_, err := srv.CreateTask(ctxA, &gritzv1.CreateTaskRequest{
		Name:      "User A's Task 1",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)
	_, err = srv.CreateTask(ctxA, &gritzv1.CreateTaskRequest{
		Name:      "User A's Task 2",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)
	_, err = srv.CreateTask(ctxB, &gritzv1.CreateTaskRequest{
		Name:      "User B's Task",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)

	// Act
	respA, err := srv.ListTasks(ctxA, &gritzv1.ListTasksRequest{})
	assert.NilError(t, err)
	respB, err := srv.ListTasks(ctxB, &gritzv1.ListTasksRequest{})
	assert.NilError(t, err)

	// Assert
	assert.Equal(t, len(respA.Tasks), 2)
	assert.Equal(t, len(respB.Tasks), 1)
}

func TestListTasks_Pagination(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	first, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{Name: "task-1", Runner: "test-runner", Workspace: "test-workspace"})
	assert.NilError(t, err)
	second, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{Name: "task-2", Runner: "test-runner", Workspace: "test-workspace"})
	assert.NilError(t, err)

	// The first page holds the newest task and a token for the next page.
	page1, err := srv.ListTasks(ctx, &gritzv1.ListTasksRequest{PageSize: 1})
	assert.NilError(t, err)
	assert.Equal(t, len(page1.Tasks), 1)
	assert.Equal(t, page1.Tasks[0].Id, second.Task.Id)
	assert.Assert(t, page1.NextPageToken != "")

	// Passing that token back returns the next (older) task and no further pages.
	page2, err := srv.ListTasks(ctx, &gritzv1.ListTasksRequest{PageSize: 1, PageToken: page1.NextPageToken})
	assert.NilError(t, err)
	assert.Equal(t, len(page2.Tasks), 1)
	assert.Equal(t, page2.Tasks[0].Id, first.Task.Id)
	assert.Equal(t, page2.NextPageToken, "")
}

func TestCreateTask_Namespace(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	// Act
	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Namespaced Task",
		Runner:    "test-runner",
		Workspace: "test-workspace",
		Namespace: "reviewbot",
	})

	// Assert: the response carries the namespace...
	assert.NilError(t, err)
	assert.Equal(t, resp.Task.Namespace, "reviewbot")

	// ...and it is persisted, readable back on the task.
	getResp, err := srv.GetTask(ctx, &gritzv1.GetTaskRequest{Id: resp.Task.Id})
	assert.NilError(t, err)
	assert.Equal(t, getResp.Task.Namespace, "reviewbot")
}

func TestCreateTask_DefaultNamespace(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	// Act: no namespace set — the default (empty) namespace.
	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Default Task",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})

	// Assert: empty namespace behaves as today.
	assert.NilError(t, err)
	assert.Equal(t, resp.Task.Namespace, "")
	getResp, err := srv.GetTask(ctx, &gritzv1.GetTaskRequest{Id: resp.Task.Id})
	assert.NilError(t, err)
	assert.Equal(t, getResp.Task.Namespace, "")
}

func TestCreateTask_BadRunner(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	_, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Bad Task",
		Runner:    "nonexistent-runner",
		Workspace: "test-workspace",
	})
	assert.ErrorContains(t, err, "not found")
}

func TestCreateTask_BadWorkspace(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	_, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Bad Task",
		Runner:    "test-runner",
		Workspace: "fake-workspace",
	})
	assert.ErrorContains(t, err, "not found")
}

func TestUpdateTask(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)
	createResp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Original Name",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)

	// Act
	_, err = srv.UpdateTask(ctx, &gritzv1.UpdateTaskRequest{
		Id:   createResp.Task.Id,
		Name: "Updated Name",
	})

	// Assert
	assert.NilError(t, err)
	getResp, err := srv.GetTask(ctx, &gritzv1.GetTaskRequest{Id: createResp.Task.Id})
	assert.NilError(t, err)
	assert.Equal(t, getResp.Task.Name, "Updated Name")
}

func TestUpdateTask_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	orgA := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxA := createCtx(t, orgA)
	orgB := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxB := createCtx(t, orgB)
	createResp, err := srv.CreateTask(ctxA, &gritzv1.CreateTaskRequest{
		Name:      "User A's Task",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)

	// Act
	_, err = srv.UpdateTask(ctxB, &gritzv1.UpdateTaskRequest{
		Id:   createResp.Task.Id,
		Name: "Hijacked Name",
	})

	// Assert
	assert.ErrorContains(t, err, "not found")
}

func TestArchiveTask_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	orgA := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxA := createCtx(t, orgA)
	orgB := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxB := createCtx(t, orgB)
	createResp, err := srv.CreateTask(ctxA, &gritzv1.CreateTaskRequest{
		Name:      "User A's Task",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)

	// Act
	_, err = srv.ArchiveTask(ctxB, &gritzv1.ArchiveTaskRequest{
		Id: createResp.Task.Id,
	})

	// Assert
	assert.ErrorContains(t, err, "not found")
}

func TestCancelTask_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	orgA := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxA := createCtx(t, orgA)
	orgB := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxB := createCtx(t, orgB)
	createResp, err := srv.CreateTask(ctxA, &gritzv1.CreateTaskRequest{
		Name:      "User A's Task",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)

	// Act
	_, err = srv.CancelTask(ctxB, &gritzv1.CancelTaskRequest{
		Id: createResp.Task.Id,
	})

	// Assert
	assert.ErrorContains(t, err, "not found")
}

func TestRestartTask_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	orgA := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxA := createCtx(t, orgA)
	orgB := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxB := createCtx(t, orgB)
	createResp, err := srv.CreateTask(ctxA, &gritzv1.CreateTaskRequest{
		Name:      "User A's Task",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)

	// Act
	_, err = srv.RestartTask(ctxB, &gritzv1.RestartTaskRequest{
		Id: createResp.Task.Id,
	})

	// Assert
	assert.ErrorContains(t, err, "not found")
}

func TestCreateTask_AutoArchive(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	want := 90 * time.Minute
	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:        "With archive",
		Runner:      "test-runner",
		Workspace:   "test-workspace",
		AutoArchive: durationpb.New(want),
	})
	assert.NilError(t, err)
	assert.Assert(t, resp.Task.AutoArchive != nil, "AutoArchive should be set on response")
	assert.Equal(t, resp.Task.AutoArchive.AsDuration(), want)

	getResp, err := srv.GetTask(ctx, &gritzv1.GetTaskRequest{Id: resp.Task.Id})
	assert.NilError(t, err)
	assert.Assert(t, getResp.Task.AutoArchive != nil)
	assert.Equal(t, getResp.Task.AutoArchive.AsDuration(), want)
}

func TestUpdateTask_AutoArchive(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	createResp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "No archive",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)
	assert.Equal(t, createResp.Task.AutoArchive.AsDuration(), time.Duration(0), "unset means never auto-archive")

	// Set a value via Update
	want := 24 * time.Hour
	_, err = srv.UpdateTask(ctx, &gritzv1.UpdateTaskRequest{
		Id:          createResp.Task.Id,
		AutoArchive: durationpb.New(want),
	})
	assert.NilError(t, err)
	getResp, err := srv.GetTask(ctx, &gritzv1.GetTaskRequest{Id: createResp.Task.Id})
	assert.NilError(t, err)
	assert.Assert(t, getResp.Task.AutoArchive != nil)
	assert.Equal(t, getResp.Task.AutoArchive.AsDuration(), want)

	// Omitting AutoArchive on Update leaves the existing value untouched.
	_, err = srv.UpdateTask(ctx, &gritzv1.UpdateTaskRequest{
		Id:   createResp.Task.Id,
		Name: "just a rename",
	})
	assert.NilError(t, err)
	getResp, err = srv.GetTask(ctx, &gritzv1.GetTaskRequest{Id: createResp.Task.Id})
	assert.NilError(t, err)
	assert.Equal(t, getResp.Task.AutoArchive.AsDuration(), want, "omitted AutoArchive preserves existing value")

	// Setting to zero reverts to "never auto-archive" (omitted on the wire).
	_, err = srv.UpdateTask(ctx, &gritzv1.UpdateTaskRequest{
		Id:          createResp.Task.Id,
		AutoArchive: durationpb.New(0),
	})
	assert.NilError(t, err)
	getResp, err = srv.GetTask(ctx, &gritzv1.GetTaskRequest{Id: createResp.Task.Id})
	assert.NilError(t, err)
	assert.Equal(t, getResp.Task.AutoArchive.AsDuration(), time.Duration(0), "zero duration means never auto-archive")
}

func TestUnarchiveTask_ClearsAutoArchive(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	createResp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:        "Auto-archive task",
		Runner:      "test-runner",
		Workspace:   "test-workspace",
		AutoArchive: durationpb.New(time.Hour),
	})
	assert.NilError(t, err)

	// Put the task in a terminal state, then archive it manually.
	dbTask, err := srv.store.GetTask(ctx, nil, createResp.Task.Id, org.OrgID)
	assert.NilError(t, err)
	dbTask.Status = 5 // COMPLETED
	dbTask.Command = 0
	assert.NilError(t, srv.store.UpdateTask(ctx, nil, dbTask))

	_, err = srv.ArchiveTask(ctx, &gritzv1.ArchiveTaskRequest{Id: createResp.Task.Id})
	assert.NilError(t, err)

	// Unarchive should clear auto_archive so the archiver doesn't immediately re-archive.
	_, err = srv.UnarchiveTask(ctx, &gritzv1.UnarchiveTaskRequest{Id: createResp.Task.Id})
	assert.NilError(t, err)

	getResp, err := srv.GetTask(ctx, &gritzv1.GetTaskRequest{Id: createResp.Task.Id})
	assert.NilError(t, err)
	assert.Assert(t, !getResp.Task.Archived, "task should be unarchived")
	assert.Equal(t, getResp.Task.AutoArchive.AsDuration(), time.Duration(0), "AutoArchive should reset to 0 (never) on unarchive")
}
