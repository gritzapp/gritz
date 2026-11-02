package apiserver_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/e2e"
	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"gotest.tools/v3/assert"
)

// This file exercises the per-task scope enforcement on the apiserver handlers
// (proposals/implemented/eliminate-runner-socket-proxy.md §3/§5, with the
// two-tier AllowOp+Allow override): a task token scoped to its own task may act
// on that task but not on any other task in the org. For every converted handler
// there is a wrong-instance test: a caller scoped to task A must be denied on
// task B. That is the safety net for the pre-gate design — the empty-scopes
// completeness test (scope_test.go) can't catch a forgotten post-load Allow
// because the pre-gate already denies empty scopes, but a wrong-instance caller
// passes the pre-gate and must be stopped by the post-load Allow.

// --- Behavioral spec: own task allowed, any other task denied ---

func TestTaskScope_GetTask_OwnAllowed_OtherDenied(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	a := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "A"})
	b := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "B"})
	own := h.Server.Client(h.Org.CreateTaskToken(&gritzv1.CreateTaskTokenRequest{TaskId: a.ID}))

	// Act & Assert - own task is always readable.
	_, err := own.GetTask(t.Context(), &gritzv1.GetTaskRequest{Id: a.ID})
	assert.NilError(t, err)

	// Act & Assert - any other task is denied.
	_, err = own.GetTask(t.Context(), &gritzv1.GetTaskRequest{Id: b.ID})
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
}

func TestTaskScope_UpdateTask_OwnAllowed_OtherDenied(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	a := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "A"})
	b := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "B"})
	own := h.Server.Client(h.Org.CreateTaskToken(&gritzv1.CreateTaskTokenRequest{TaskId: a.ID}))

	// Act & Assert
	_, err := own.UpdateTask(t.Context(), &gritzv1.UpdateTaskRequest{Id: a.ID, Name: "renamed"})
	assert.NilError(t, err)
	_, err = own.UpdateTask(t.Context(), &gritzv1.UpdateTaskRequest{Id: b.ID, Name: "hijack"})
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
}

// A task token holds no create scope at all, so an agent cannot create tasks.
func TestTaskScope_CreateTask_Denied(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	a := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "A"})
	own := h.Server.Client(h.Org.CreateTaskToken(&gritzv1.CreateTaskTokenRequest{TaskId: a.ID}))

	// Act
	_, err := own.CreateTask(t.Context(), &gritzv1.CreateTaskRequest{
		Runner: a.Resp.Task.Runner, Workspace: a.Resp.Task.Workspace,
	})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
}

// --- Behavioral spec: archived gating ---
//
// These tests build the archived-constrained scope directly to prove the
// handlers pass the task's real archived state and the predicate denies an
// archived task uniformly for reads and writes.

func TestTaskScope_ArchivedGating(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	active := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "A"})
	archived := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "B"})
	// Run 1 starts and stops with no command pending, which completes the task.
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: archived.ID, Start: true})
	h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: archived.ID, Event: "started", Version: 1},
			{TaskId: archived.ID, Event: "stopped", Version: 1},
		},
	})
	h.Org.ArchiveTask(&gritzv1.ArchiveTaskRequest{Id: archived.ID})
	activeReader := h.Org.CreateScopedKey(authscope.Scopes{authscope.New(authscope.OpTaskRead,
		authscope.WithTaskID(active.ID), authscope.WithTaskArchived(false))})
	archivedReader := h.Org.CreateScopedKey(authscope.Scopes{authscope.New(authscope.OpTaskRead,
		authscope.WithTaskID(archived.ID), authscope.WithTaskArchived(false))})
	archivedWriter := h.Org.CreateScopedKey(authscope.Scopes{authscope.New(authscope.OpTaskWrite,
		authscope.WithTaskID(archived.ID), authscope.WithTaskArchived(false))})

	// Act & Assert - an active task carries task.archived:"false" → matches →
	// allowed.
	_, err := activeReader.Client.GetTask(t.Context(), &gritzv1.GetTaskRequest{Id: active.ID})
	assert.NilError(t, err)

	// Act & Assert - an archived task carries task.archived:"true" → fails the
	// "false" predicate → denied, for reads...
	_, err = archivedReader.Client.GetTask(t.Context(), &gritzv1.GetTaskRequest{Id: archived.ID})
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)

	// ...and writes (this is the unarchive-resurrect hole, closed for free).
	_, err = archivedWriter.Client.UnarchiveTask(t.Context(), &gritzv1.UnarchiveTaskRequest{Id: archived.ID})
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
}

// --- Wrong-instance: caller scoped to task A is denied on task B ---

func TestTaskScope_WrongInstanceDenied(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, nil)
	a := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "A"})
	b := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "B"})
	// Scoped to task A only (read+write on its id).
	own := h.Server.Client(h.Org.CreateTaskToken(&gritzv1.CreateTaskTokenRequest{TaskId: a.ID}))

	// The row-loading task handlers whose post-load Allow must reject a caller
	// scoped to a different task. Each row calls the handler on task id.
	tests := []struct {
		name string
		call func(ctx context.Context, c gritzclient.Client, id int64) error
	}{
		{"GetTask", func(ctx context.Context, c gritzclient.Client, id int64) error {
			_, err := c.GetTask(ctx, &gritzv1.GetTaskRequest{Id: id})
			return err
		}},
		{"UpdateTask", func(ctx context.Context, c gritzclient.Client, id int64) error {
			_, err := c.UpdateTask(ctx, &gritzv1.UpdateTaskRequest{Id: id, Name: "x"})
			return err
		}},
		{"ArchiveTask", func(ctx context.Context, c gritzclient.Client, id int64) error {
			_, err := c.ArchiveTask(ctx, &gritzv1.ArchiveTaskRequest{Id: id})
			return err
		}},
		{"UnarchiveTask", func(ctx context.Context, c gritzclient.Client, id int64) error {
			_, err := c.UnarchiveTask(ctx, &gritzv1.UnarchiveTaskRequest{Id: id})
			return err
		}},
		{"CancelTask", func(ctx context.Context, c gritzclient.Client, id int64) error {
			_, err := c.CancelTask(ctx, &gritzv1.CancelTaskRequest{Id: id})
			return err
		}},
		{"RestartTask", func(ctx context.Context, c gritzclient.Client, id int64) error {
			_, err := c.RestartTask(ctx, &gritzv1.RestartTaskRequest{Id: id})
			return err
		}},
		{"CreateLink", func(ctx context.Context, c gritzclient.Client, id int64) error {
			_, err := c.CreateLink(ctx, &gritzv1.CreateLinkRequest{
				TaskId: id,
				Url:    "https://example.com/x",
			})
			return err
		}},
		{"ListLinks", func(ctx context.Context, c gritzclient.Client, id int64) error {
			_, err := c.ListLinks(ctx, &gritzv1.ListLinksRequest{TaskId: id})
			return err
		}},
		{"UploadLogs", func(ctx context.Context, c gritzclient.Client, id int64) error {
			_, err := c.UploadLogs(ctx, &gritzv1.UploadLogsRequest{
				TaskId: id, Entries: []*gritzv1.LogEntry{{Type: "llm", Content: "x"}},
			})
			return err
		}},
		{"AppendLogChunk", func(ctx context.Context, c gritzclient.Client, id int64) error {
			_, err := c.AppendLogChunk(ctx, &gritzv1.AppendLogChunkRequest{
				TaskId: id, Version: 1, Data: []byte("x"),
			})
			return err
		}},
		{"ListLogChunksByTask", func(ctx context.Context, c gritzclient.Client, id int64) error {
			_, err := c.ListLogChunksByTask(ctx, &gritzv1.ListLogChunksByTaskRequest{TaskId: id})
			return err
		}},
		{"ListEventsByTask", func(ctx context.Context, c gritzclient.Client, id int64) error {
			_, err := c.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{TaskId: id})
			return err
		}},
		{"SubmitRunnerEvents", func(ctx context.Context, c gritzclient.Client, id int64) error {
			_, err := c.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
				Events: []*gritzv1.RunnerEvent{{TaskId: id, Event: "started", Version: 1}},
			})
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			err := tt.call(t.Context(), own, b.ID)

			// Assert
			assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied,
				"%s scoped to task A must deny task B", tt.name)
		})
	}
}

// SubmitRunnerEvents authorizes per-event; a partial-batch failure is accepted
// (the own-task event is applied before the foreign event is rejected).
func TestTaskScope_SubmitRunnerEvents_PartialBatch(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	a := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "A"})
	b := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "B"})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: a.ID, Start: true})
	own := h.Server.Client(h.Org.CreateTaskToken(&gritzv1.CreateTaskTokenRequest{TaskId: a.ID}))

	// Act
	_, err := own.SubmitRunnerEvents(t.Context(), &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: a.ID, Event: "started", Version: 1},
			{TaskId: b.ID, Event: "started", Version: 1},
		},
	})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
	// The own-task event was applied before the foreign one was rejected.
	assert.Equal(t, h.Org.GetTask(&gritzv1.GetTaskRequest{Id: a.ID}).Status, gritzv1.TaskStatus_RUNNING)
}
