package apiserver

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/icholy/gritz/internal/auth/agentauth"
	"github.com/icholy/gritz/internal/auth/apiauth"
	"github.com/icholy/gritz/internal/auth/authscope"
	"github.com/icholy/gritz/internal/model"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"github.com/icholy/gritz/internal/store/teststore"
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

// scopedCtx returns a context carrying a caller in org with exactly the given
// scopes — the narrow-token analogue of createCtx (which grants admin).
func scopedCtx(t *testing.T, org *teststore.Org, scopes authscope.Scopes) context.Context {
	t.Helper()
	return apiauth.WithUser(t.Context(), &apiauth.UserInfo{
		ID:     org.UserID,
		OrgID:  org.OrgID,
		Scopes: scopes,
	})
}

// taskScopes mints the scopes a runner would grant a task token for taskID,
// using the production minter so the tests track real grants.
func taskScopes(taskID int64, caps ...string) authscope.Scopes {
	return agentauth.Scopes(agentauth.ScopeOptions{
		TaskID:       taskID,
		Capabilities: caps,
	})
}

// makeArchived puts the task in a terminal state and archives it via the admin
// context, mirroring how a finished task is archived in production.
func makeArchived(t *testing.T, srv *Server, adminCtx context.Context, org *teststore.Org, id int64) {
	t.Helper()
	task, err := srv.store.GetTask(adminCtx, nil, id, org.OrgID)
	assert.NilError(t, err)
	task.Status = model.TaskStatusCompleted
	task.Command = model.TaskCommandNone
	assert.NilError(t, srv.store.UpdateTask(adminCtx, nil, task))
	_, err = srv.ArchiveTask(adminCtx, &gritzv1.ArchiveTaskRequest{Id: id})
	assert.NilError(t, err)
}

// newOrgWithTasks builds an org with the standard workspace and returns the
// admin context plus two unrelated tasks in that org.
func newOrgWithTasks(t *testing.T, srv *Server) (context.Context, *teststore.Org, *gritzv1.Task, *gritzv1.Task) {
	t.Helper()
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{
		Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}},
	})
	adminCtx := createCtx(t, org)
	// The instruction is what starts the task — these fixtures are submitted
	// runner events against, so they have to be pending with a start command
	// rather than the completed row an instruction-less create produces.
	taskA, err := srv.CreateTask(adminCtx, &gritzv1.CreateTaskRequest{
		Name: "A", Runner: "test-runner", Workspace: "test-workspace",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)
	taskB, err := srv.CreateTask(adminCtx, &gritzv1.CreateTaskRequest{
		Name: "B", Runner: "test-runner", Workspace: "test-workspace",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)
	return adminCtx, org, taskA.Task, taskB.Task
}

// --- Behavioral spec: own task allowed, any other task denied ---

func TestTaskScope_GetTask_OwnAllowed_OtherDenied(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	_, org, taskA, taskB := newOrgWithTasks(t, srv)

	own := scopedCtx(t, org, taskScopes(taskA.Id))

	// Own task is always readable.
	_, err := srv.GetTask(own, &gritzv1.GetTaskRequest{Id: taskA.Id})
	assert.NilError(t, err)

	// Any other task is denied.
	_, err = srv.GetTask(own, &gritzv1.GetTaskRequest{Id: taskB.Id})
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
}

func TestTaskScope_UpdateTask_OwnAllowed_OtherDenied(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	_, org, taskA, taskB := newOrgWithTasks(t, srv)

	own := scopedCtx(t, org, taskScopes(taskA.Id))

	_, err := srv.UpdateTask(own, &gritzv1.UpdateTaskRequest{Id: taskA.Id, Name: "renamed"})
	assert.NilError(t, err)
	_, err = srv.UpdateTask(own, &gritzv1.UpdateTaskRequest{Id: taskB.Id, Name: "hijack"})
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
}

// A task token holds no create scope at all, so an agent cannot create tasks.
func TestTaskScope_CreateTask_Denied(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	_, org, taskA, _ := newOrgWithTasks(t, srv)

	own := scopedCtx(t, org, taskScopes(taskA.Id))
	_, err := srv.CreateTask(own, &gritzv1.CreateTaskRequest{
		Runner: "test-runner", Workspace: "test-workspace",
	})
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
}

// --- Behavioral spec: archived gating ---
//
// These tests build the archived-constrained scope directly to prove the
// handlers pass the task's real archived state and the predicate denies an
// archived task uniformly for reads and writes.

func TestTaskScope_ArchivedGating(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	adminCtx, org, active, archived := newOrgWithTasks(t, srv)
	makeArchived(t, srv, adminCtx, org, archived.Id)

	readScope := func(id int64) authscope.Scopes {
		return authscope.Scopes{authscope.New(authscope.OpTaskRead,
			authscope.WithTaskID(id), authscope.WithTaskArchived(false))}
	}
	writeScope := func(id int64) authscope.Scopes {
		return authscope.Scopes{authscope.New(authscope.OpTaskWrite,
			authscope.WithTaskID(id), authscope.WithTaskArchived(false))}
	}

	// An active task carries task.archived:"false" → matches → allowed.
	_, err := srv.GetTask(scopedCtx(t, org, readScope(active.Id)), &gritzv1.GetTaskRequest{Id: active.Id})
	assert.NilError(t, err)

	// An archived task carries task.archived:"true" → fails the "false"
	// predicate → denied, for reads...
	_, err = srv.GetTask(scopedCtx(t, org, readScope(archived.Id)), &gritzv1.GetTaskRequest{Id: archived.Id})
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)

	// ...and writes (this is the unarchive-resurrect hole, closed for free).
	_, err = srv.UnarchiveTask(scopedCtx(t, org, writeScope(archived.Id)), &gritzv1.UnarchiveTaskRequest{Id: archived.Id})
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
}

// --- Wrong-instance: caller scoped to task A is denied on task B ---

// taskInstanceHandlers are the row-loading task handlers whose post-load Allow
// must reject a caller scoped to a different task. Each closure invokes the
// handler against the given task id with the caller in ctx.
func taskInstanceHandlers() []struct {
	name string
	call func(ctx context.Context, srv *Server, id int64) error
} {
	return []struct {
		name string
		call func(ctx context.Context, srv *Server, id int64) error
	}{
		{"GetTask", func(ctx context.Context, srv *Server, id int64) error {
			_, err := srv.GetTask(ctx, &gritzv1.GetTaskRequest{Id: id})
			return err
		}},
		{"UpdateTask", func(ctx context.Context, srv *Server, id int64) error {
			_, err := srv.UpdateTask(ctx, &gritzv1.UpdateTaskRequest{Id: id, Name: "x"})
			return err
		}},
		{"ArchiveTask", func(ctx context.Context, srv *Server, id int64) error {
			_, err := srv.ArchiveTask(ctx, &gritzv1.ArchiveTaskRequest{Id: id})
			return err
		}},
		{"UnarchiveTask", func(ctx context.Context, srv *Server, id int64) error {
			_, err := srv.UnarchiveTask(ctx, &gritzv1.UnarchiveTaskRequest{Id: id})
			return err
		}},
		{"CancelTask", func(ctx context.Context, srv *Server, id int64) error {
			_, err := srv.CancelTask(ctx, &gritzv1.CancelTaskRequest{Id: id})
			return err
		}},
		{"RestartTask", func(ctx context.Context, srv *Server, id int64) error {
			_, err := srv.RestartTask(ctx, &gritzv1.RestartTaskRequest{Id: id})
			return err
		}},
		{"CreateLink", func(ctx context.Context, srv *Server, id int64) error {
			_, err := srv.CreateLink(ctx, &gritzv1.CreateLinkRequest{TaskId: id, Url: "https://example.com/x"})
			return err
		}},
		{"ListLinks", func(ctx context.Context, srv *Server, id int64) error {
			_, err := srv.ListLinks(ctx, &gritzv1.ListLinksRequest{TaskId: id})
			return err
		}},
		{"UploadLogs", func(ctx context.Context, srv *Server, id int64) error {
			_, err := srv.UploadLogs(ctx, &gritzv1.UploadLogsRequest{
				TaskId: id, Entries: []*gritzv1.LogEntry{{Type: "llm", Content: "x"}},
			})
			return err
		}},
		{"AppendLogChunk", func(ctx context.Context, srv *Server, id int64) error {
			_, err := srv.AppendLogChunk(ctx, &gritzv1.AppendLogChunkRequest{
				TaskId: id, Version: 1, Data: []byte("x"),
			})
			return err
		}},
		{"ListLogChunksByTask", func(ctx context.Context, srv *Server, id int64) error {
			_, err := srv.ListLogChunksByTask(ctx, &gritzv1.ListLogChunksByTaskRequest{TaskId: id})
			return err
		}},
		{"ListEventsByTask", func(ctx context.Context, srv *Server, id int64) error {
			_, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{TaskId: id})
			return err
		}},
		{"SubmitRunnerEvents", func(ctx context.Context, srv *Server, id int64) error {
			_, err := srv.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
				Events: []*gritzv1.RunnerEvent{{TaskId: id, Event: "started", Version: 1}},
			})
			return err
		}},
	}
}

func TestTaskScope_WrongInstanceDenied(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	_, org, taskA, taskB := newOrgWithTasks(t, srv)

	// Scoped to taskA only (read+write on its id).
	ctxA := scopedCtx(t, org, taskScopes(taskA.Id))

	for _, h := range taskInstanceHandlers() {
		t.Run(h.name, func(t *testing.T) {
			err := h.call(ctxA, srv, taskB.Id)
			assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied,
				"%s scoped to task A must deny task B", h.name)
		})
	}
}

// SubmitRunnerEvents authorizes per-event; a partial-batch failure is accepted
// (the own-task event is applied before the foreign event is rejected).
func TestTaskScope_SubmitRunnerEvents_PartialBatch(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	adminCtx, org, taskA, taskB := newOrgWithTasks(t, srv)

	ctxA := scopedCtx(t, org, taskScopes(taskA.Id))
	_, err := srv.SubmitRunnerEvents(ctxA, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: taskA.Id, Event: "started", Version: 1},
			{TaskId: taskB.Id, Event: "started", Version: 1},
		},
	})
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)

	// The own-task event was applied before the foreign one was rejected.
	got, err := srv.GetTask(adminCtx, &gritzv1.GetTaskRequest{Id: taskA.Id})
	assert.NilError(t, err)
	assert.Equal(t, got.Task.Status, gritzv1.TaskStatus_RUNNING)
}
