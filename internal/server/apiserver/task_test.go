package apiserver_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/e2e"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/durationpb"
	"gotest.tools/v3/assert"
)

func TestGetTask(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Name: "Test Task",
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
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})

	// Act
	resp, err := h.Org.Client.GetTask(t.Context(), &gritzv1.GetTaskRequest{Id: task.ID})

	// Assert: instructions are not a task field; they are instruction events in
	// the stream, asserted below.
	assert.NilError(t, err)
	assert.DeepEqual(t, resp.Task, &gritzv1.Task{
		Id:          task.ID,
		Name:        "Test Task",
		Runner:      e2e.DefaultRunner,
		Workspace:   e2e.DefaultWorkspace,
		Status:      gritzv1.TaskStatus_PENDING,
		Command:     gritzv1.TaskCommand_START,
		Actions:     &gritzv1.TaskActions{Cancel: true},
		Version:     1,
		CreatedAt:   resp.Task.CreatedAt, // Copy timestamps since we can't predict them
		UpdatedAt:   resp.Task.UpdatedAt,
		AutoArchive: durationpb.New(0),
	}, protocmp.Transform())
	// The initial instructions are seeded into the stream as instruction events.
	events := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
		Types:  []string{model.EventTypeInstruction},
	})
	assert.Equal(t, len(events), 2)
	assert.DeepEqual(t, events[0].GetInstruction(), &gritzv1.InstructionPayload{
		Text: "Do something important",
		Url:  "https://example.com/issue/1",
	}, protocmp.Transform())
	assert.DeepEqual(t, events[1].GetInstruction(), &gritzv1.InstructionPayload{
		Text: "Do something else",
		Url:  "https://example.com/issue/2",
	}, protocmp.Transform())
}

func TestGetTask_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "User A's Task"})

	// Act & Assert - the task's own org reads it.
	_, err := h.Org.Client.GetTask(t.Context(), &gritzv1.GetTaskRequest{Id: task.ID})
	assert.NilError(t, err)

	// Act & Assert - another org can't see it.
	_, err = other.Client.GetTask(t.Context(), &gritzv1.GetTaskRequest{Id: task.ID})
	assert.ErrorContains(t, err, "not found")
}

func TestCreateTask(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	resp, err := h.Org.Client.CreateTask(t.Context(), &gritzv1.CreateTaskRequest{
		Name:      "New Task",
		Runner:    e2e.DefaultRunner,
		Workspace: e2e.DefaultWorkspace,
		Instructions: []*gritzv1.Instruction{
			{
				Text: "Do something",
				Url:  "https://example.com/issue/1",
			},
		},
	})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, resp.Task, &gritzv1.Task{
		Id:          resp.Task.Id,
		Name:        "New Task",
		Runner:      e2e.DefaultRunner,
		Workspace:   e2e.DefaultWorkspace,
		Status:      gritzv1.TaskStatus_PENDING,
		Command:     gritzv1.TaskCommand_START,
		Actions:     &gritzv1.TaskActions{Cancel: true},
		Version:     1,
		CreatedAt:   resp.Task.CreatedAt,
		UpdatedAt:   resp.Task.UpdatedAt,
		AutoArchive: durationpb.New(0),
	}, protocmp.Transform())
	// The initial instruction is seeded into the stream as an instruction event.
	events := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: resp.Task.Id,
		Types:  []string{model.EventTypeInstruction},
	})
	assert.Equal(t, len(events), 1)
	assert.DeepEqual(t, events[0].GetInstruction(), &gritzv1.InstructionPayload{
		Text: "Do something",
		Url:  "https://example.com/issue/1",
	}, protocmp.Transform())
}

// A draft task carries no command, so no runner picks it up, and the first
// instruction from the task page's composer starts it.
func TestCreateTask_Draft(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	resp, err := h.Org.Client.CreateTask(t.Context(), &gritzv1.CreateTaskRequest{
		Name:      "Draft Task",
		Runner:    e2e.DefaultRunner,
		Workspace: e2e.DefaultWorkspace,
		Draft:     true,
	})

	// Assert: draft, no command, and startable + archivable. Not cancellable and
	// not restartable — there is nothing to cancel, and nothing to restart.
	assert.NilError(t, err)
	assert.DeepEqual(t, resp.Task, &gritzv1.Task{
		Id:          resp.Task.Id,
		Name:        "Draft Task",
		Runner:      e2e.DefaultRunner,
		Workspace:   e2e.DefaultWorkspace,
		Status:      gritzv1.TaskStatus_DRAFT,
		Command:     gritzv1.TaskCommand_NONE,
		Actions:     &gritzv1.TaskActions{Start: true, Archive: true},
		Version:     0,
		CreatedAt:   resp.Task.CreatedAt,
		UpdatedAt:   resp.Task.UpdatedAt,
		AutoArchive: durationpb.New(0),
	}, protocmp.Transform())
	// The Created lifecycle event is the only event, so the timeline is not
	// empty, and there is no instruction and no command event: no runner ever
	// acts on it, so nothing is launched.
	events := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{TaskId: resp.Task.Id})
	assert.Equal(t, len(events), 1)
	assert.Equal(t, events[0].GetLifecycle().Kind, gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED)
}

// The first instruction from the composer starts a draft task: it lands on
// PENDING/START — the row an ordinary create presents — and the instruction
// event wakes.
func TestCreateTask_Draft_FirstInstructionStarts(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Draft Task"})

	// Act: exactly what the task page composer sends.
	_, err := h.Org.Client.UpdateTask(t.Context(), &gritzv1.UpdateTaskRequest{
		Id:              task.ID,
		Start:           true,
		AddInstructions: []*gritzv1.Instruction{{Text: "Do something"}},
	})

	// Assert
	assert.NilError(t, err)
	got := h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID})
	assert.Equal(t, got.Status, gritzv1.TaskStatus_PENDING)
	assert.Equal(t, got.Command, gritzv1.TaskCommand_START)
	// The draft was never provisioned (version 0), so its first run is run 1 —
	// the same run number an ordinary create starts at.
	assert.Equal(t, got.Version, int64(1))
	// The runner now has work for it: a start for run 1.
	commands := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
		Types:  []string{model.EventTypeCommand},
	})
	assert.Equal(t, len(commands), 1)
	assert.DeepEqual(t, commands[0].GetCommand(), &gritzv1.CommandPayload{
		Command:   gritzv1.TaskCommand_START,
		Version:   1,
		Runner:    e2e.DefaultRunner,
		Workspace: e2e.DefaultWorkspace,
	}, protocmp.Transform())
	// The instruction is in the stream and woke the task.
	instructions := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
		Types:  []string{model.EventTypeInstruction},
	})
	assert.Equal(t, len(instructions), 1)
	assert.DeepEqual(t, instructions[0].GetInstruction(), &gritzv1.InstructionPayload{
		Text: "Do something",
	}, protocmp.Transform())
	assert.Assert(t, instructions[0].Wake, "the first instruction must wake the task")
}

func TestListTasks(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task 1"})
	h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task 2"})

	// Act
	resp, err := h.Org.Client.ListTasks(t.Context(), &gritzv1.ListTasksRequest{})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, len(resp.Tasks), 2)
}

func TestListTasks_IncludeArchived(t *testing.T) {
	t.Parallel()
	// Arrange: one active task and one archived task in the same org.
	h := e2e.New(t, nil)
	h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "active"})
	archived := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "archived"})
	h.Org.ArchiveTask(&gritzv1.ArchiveTaskRequest{Id: archived.ID})

	// Act & Assert - the default request (archived unset) returns active tasks
	// only.
	def, err := h.Org.Client.ListTasks(t.Context(), &gritzv1.ListTasksRequest{})
	assert.NilError(t, err)
	assert.Equal(t, len(def.Tasks), 1)
	assert.Assert(t, !def.Tasks[0].Archived)

	// Act & Assert - Archived: false is likewise active-only.
	off, err := h.Org.Client.ListTasks(t.Context(), &gritzv1.ListTasksRequest{Archived: false})
	assert.NilError(t, err)
	assert.Equal(t, len(off.Tasks), 1)

	// Act & Assert - Archived: true returns active and archived rows together.
	on, err := h.Org.Client.ListTasks(t.Context(), &gritzv1.ListTasksRequest{Archived: true})
	assert.NilError(t, err)
	assert.Equal(t, len(on.Tasks), 2)
}

func TestListTasks_ArchivedTokenMismatch(t *testing.T) {
	t.Parallel()
	// Arrange: two active and two archived rows so each filter mints a
	// next-page token.
	h := e2e.New(t, nil)
	h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "active-1"})
	h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "active-2"})
	archived1 := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "archived-1"})
	h.Org.ArchiveTask(&gritzv1.ArchiveTaskRequest{Id: archived1.ID})
	archived2 := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "archived-2"})
	h.Org.ArchiveTask(&gritzv1.ArchiveTaskRequest{Id: archived2.ID})

	// Act & Assert - a token minted under archived=true, replayed under
	// archived=false, is invalid.
	archivedPage, err := h.Org.Client.ListTasks(t.Context(), &gritzv1.ListTasksRequest{PageSize: 1, Archived: true})
	assert.NilError(t, err)
	assert.Assert(t, archivedPage.NextPageToken != "")
	_, err = h.Org.Client.ListTasks(t.Context(), &gritzv1.ListTasksRequest{
		PageSize:  1,
		PageToken: archivedPage.NextPageToken,
		Archived:  false,
	})
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)

	// Act & Assert - and the reverse: an active-minted token replayed under
	// archived=true.
	activePage, err := h.Org.Client.ListTasks(t.Context(), &gritzv1.ListTasksRequest{PageSize: 1, Archived: false})
	assert.NilError(t, err)
	assert.Assert(t, activePage.NextPageToken != "")
	_, err = h.Org.Client.ListTasks(t.Context(), &gritzv1.ListTasksRequest{
		PageSize:  1,
		PageToken: activePage.NextPageToken,
		Archived:  true,
	})
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
}

func TestListTasks_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "User A's Task 1"})
	h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "User A's Task 2"})
	other.CreateTask(&gritzv1.CreateTaskRequest{Name: "User B's Task"})

	// Act & Assert - each org lists only its own tasks.
	respA, err := h.Org.Client.ListTasks(t.Context(), &gritzv1.ListTasksRequest{})
	assert.NilError(t, err)
	assert.Equal(t, len(respA.Tasks), 2)

	respB, err := other.Client.ListTasks(t.Context(), &gritzv1.ListTasksRequest{})
	assert.NilError(t, err)
	assert.Equal(t, len(respB.Tasks), 1)
}

func TestListTasks_Pagination(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	first := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "task-1"})
	second := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "task-2"})

	// Act & Assert - the first page holds the newest task and a token for the
	// next page.
	page1, err := h.Org.Client.ListTasks(t.Context(), &gritzv1.ListTasksRequest{PageSize: 1})
	assert.NilError(t, err)
	assert.Equal(t, len(page1.Tasks), 1)
	assert.Equal(t, page1.Tasks[0].Id, second.ID)
	assert.Assert(t, page1.NextPageToken != "")

	// Act & Assert - passing that token back returns the next (older) task and
	// no further pages.
	page2, err := h.Org.Client.ListTasks(t.Context(), &gritzv1.ListTasksRequest{
		PageSize:  1,
		PageToken: page1.NextPageToken,
	})
	assert.NilError(t, err)
	assert.Equal(t, len(page2.Tasks), 1)
	assert.Equal(t, page2.Tasks[0].Id, first.ID)
	assert.Equal(t, page2.NextPageToken, "")
}

func TestCreateTask_Namespace(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	resp, err := h.Org.Client.CreateTask(t.Context(), &gritzv1.CreateTaskRequest{
		Name:      "Namespaced Task",
		Runner:    e2e.DefaultRunner,
		Workspace: e2e.DefaultWorkspace,
		Namespace: "reviewbot",
	})

	// Assert: the response carries the namespace...
	assert.NilError(t, err)
	assert.Equal(t, resp.Task.Namespace, "reviewbot")
	// ...and it is persisted, readable back on the task.
	got := h.Org.GetTask(&gritzv1.GetTaskRequest{Id: resp.Task.Id})
	assert.Equal(t, got.Namespace, "reviewbot")
}

func TestCreateTask_DefaultNamespace(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act: no namespace set — the default (empty) namespace.
	resp, err := h.Org.Client.CreateTask(t.Context(), &gritzv1.CreateTaskRequest{
		Name:      "Default Task",
		Runner:    e2e.DefaultRunner,
		Workspace: e2e.DefaultWorkspace,
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, resp.Task.Namespace, "")
	got := h.Org.GetTask(&gritzv1.GetTaskRequest{Id: resp.Task.Id})
	assert.Equal(t, got.Namespace, "")
}

func TestCreateTask_BadRunner(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	_, err := h.Org.Client.CreateTask(t.Context(), &gritzv1.CreateTaskRequest{
		Name:      "Bad Task",
		Runner:    "nonexistent-runner",
		Workspace: e2e.DefaultWorkspace,
	})

	// Assert
	assert.ErrorContains(t, err, "not found")
}

func TestCreateTask_BadWorkspace(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	_, err := h.Org.Client.CreateTask(t.Context(), &gritzv1.CreateTaskRequest{
		Name:      "Bad Task",
		Runner:    e2e.DefaultRunner,
		Workspace: "fake-workspace",
	})

	// Assert
	assert.ErrorContains(t, err, "not found")
}

func TestUpdateTask(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Original Name"})

	// Act
	_, err := h.Org.Client.UpdateTask(t.Context(), &gritzv1.UpdateTaskRequest{
		Id:   task.ID,
		Name: "Updated Name",
	})

	// Assert
	assert.NilError(t, err)
	got := h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID})
	assert.Equal(t, got.Name, "Updated Name")
}

func TestUpdateTask_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "User A's Task"})

	// Act
	_, err := other.Client.UpdateTask(t.Context(), &gritzv1.UpdateTaskRequest{
		Id:   task.ID,
		Name: "Hijacked Name",
	})

	// Assert
	assert.ErrorContains(t, err, "not found")
}

func TestArchiveTask_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "User A's Task"})

	// Act
	_, err := other.Client.ArchiveTask(t.Context(), &gritzv1.ArchiveTaskRequest{Id: task.ID})

	// Assert
	assert.ErrorContains(t, err, "not found")
}

func TestCancelTask_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "User A's Task"})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})

	// Act
	_, err := other.Client.CancelTask(t.Context(), &gritzv1.CancelTaskRequest{Id: task.ID})

	// Assert
	assert.ErrorContains(t, err, "not found")
}

func TestRestartTask_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "User A's Task"})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})

	// Act
	_, err := other.Client.RestartTask(t.Context(), &gritzv1.RestartTaskRequest{Id: task.ID})

	// Assert
	assert.ErrorContains(t, err, "not found")
}

func TestCreateTask_AutoArchive(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	want := 90 * time.Minute

	// Act
	resp, err := h.Org.Client.CreateTask(t.Context(), &gritzv1.CreateTaskRequest{
		Name:        "With archive",
		Runner:      e2e.DefaultRunner,
		Workspace:   e2e.DefaultWorkspace,
		AutoArchive: durationpb.New(want),
	})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, resp.Task.AutoArchive != nil, "AutoArchive should be set on response")
	assert.Equal(t, resp.Task.AutoArchive.AsDuration(), want)
	got := h.Org.GetTask(&gritzv1.GetTaskRequest{Id: resp.Task.Id})
	assert.Assert(t, got.AutoArchive != nil)
	assert.Equal(t, got.AutoArchive.AsDuration(), want)
}

func TestUpdateTask_AutoArchive(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "No archive"})
	assert.Equal(t, task.Resp.Task.AutoArchive.AsDuration(), time.Duration(0), "unset means never auto-archive")
	want := 24 * time.Hour

	// Act & Assert - setting a value.
	_, err := h.Org.Client.UpdateTask(t.Context(), &gritzv1.UpdateTaskRequest{
		Id:          task.ID,
		AutoArchive: durationpb.New(want),
	})
	assert.NilError(t, err)
	got := h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID})
	assert.Assert(t, got.AutoArchive != nil)
	assert.Equal(t, got.AutoArchive.AsDuration(), want)

	// Act & Assert - omitting AutoArchive leaves the existing value untouched.
	_, err = h.Org.Client.UpdateTask(t.Context(), &gritzv1.UpdateTaskRequest{
		Id:   task.ID,
		Name: "just a rename",
	})
	assert.NilError(t, err)
	got = h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID})
	assert.Equal(t, got.AutoArchive.AsDuration(), want, "omitted AutoArchive preserves existing value")

	// Act & Assert - setting zero reverts to "never auto-archive" (omitted on
	// the wire).
	_, err = h.Org.Client.UpdateTask(t.Context(), &gritzv1.UpdateTaskRequest{
		Id:          task.ID,
		AutoArchive: durationpb.New(0),
	})
	assert.NilError(t, err)
	got = h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID})
	assert.Equal(t, got.AutoArchive.AsDuration(), time.Duration(0), "zero duration means never auto-archive")
}

func TestUnarchiveTask_ClearsAutoArchive(t *testing.T) {
	t.Parallel()
	// Arrange: a draft can be archived directly.
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Name:        "Auto-archive task",
		AutoArchive: durationpb.New(time.Hour),
	})
	h.Org.ArchiveTask(&gritzv1.ArchiveTaskRequest{Id: task.ID})

	// Act
	_, err := h.Org.Client.UnarchiveTask(t.Context(), &gritzv1.UnarchiveTaskRequest{Id: task.ID})

	// Assert: unarchive clears auto_archive so the archiver doesn't immediately
	// re-archive.
	assert.NilError(t, err)
	got := h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID})
	assert.Assert(t, !got.Archived, "task should be unarchived")
	assert.Equal(t, got.AutoArchive.AsDuration(), time.Duration(0), "AutoArchive should reset to 0 (never) on unarchive")
}
