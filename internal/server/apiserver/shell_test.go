package apiserver

import (
	"testing"

	"connectrpc.com/connect"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"github.com/icholy/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestOpenShell(t *testing.T) {
	t.Parallel()
	shell := &ShellRegistryMock{SeedFunc: func(id string, orgID, taskID int64) error { return nil }}
	srv := New(Options{Store: teststore.New(t), Shells: shell})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	// Create a task and drive it to a terminal (completed) status so it can be
	// shelled. Runner events use version 0 to bypass the version check.
	createResp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:         "Task",
		Runner:       "test-runner",
		Workspace:    "test-workspace",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)
	taskID := createResp.Task.Id
	_, err = srv.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: taskID, Event: "started", Version: 0}},
	})
	assert.NilError(t, err)
	_, err = srv.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: taskID, Event: "stopped", Version: 0}},
	})
	assert.NilError(t, err)

	// Act
	resp, err := srv.OpenShell(ctx, &gritzv1.OpenShellRequest{TaskId: taskID})

	// Assert: a session id is returned...
	assert.NilError(t, err)
	assert.Assert(t, resp.SessionId != "")

	// ...it is recorded on the task's shell_session and a start is issued so the
	// runner brings the sandbox up against the preserved disk...
	getResp, err := srv.GetTask(ctx, &gritzv1.GetTaskRequest{Id: taskID})
	assert.NilError(t, err)
	assert.Equal(t, getResp.Task.ShellSession, resp.SessionId)
	assert.Equal(t, getResp.Task.Command, gritzv1.TaskCommand_START)
	assert.Equal(t, getResp.Task.Status, gritzv1.TaskStatus_PENDING)

	// ...and the rendezvous is seeded with the caller's org and the task id, so
	// the driver leg can be bound to the task that owns the session.
	calls := shell.SeedCalls()
	assert.Assert(t, cmp.Len(calls, 1))
	assert.Equal(t, calls[0].ID, resp.SessionId)
	assert.Equal(t, calls[0].OrgID, org.OrgID)
	assert.Equal(t, calls[0].TaskID, taskID)
}

func TestOpenShell_RejectsPending(t *testing.T) {
	t.Parallel()
	shell := &ShellRegistryMock{SeedFunc: func(id string, orgID, taskID int64) error { return nil }}
	srv := New(Options{Store: teststore.New(t), Shells: shell})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	// A task created with an instruction is PENDING (non-terminal).
	createResp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:         "Task",
		Runner:       "test-runner",
		Workspace:    "test-workspace",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)

	// Act
	_, err = srv.OpenShell(ctx, &gritzv1.OpenShellRequest{TaskId: createResp.Task.Id})

	// Assert: rejected, nothing seeded, no shell session recorded.
	assert.Equal(t, connect.CodeOf(err), connect.CodeFailedPrecondition)
	assert.Assert(t, cmp.Len(shell.SeedCalls(), 0))
	getResp, err := srv.GetTask(ctx, &gritzv1.GetTaskRequest{Id: createResp.Task.Id})
	assert.NilError(t, err)
	assert.Equal(t, getResp.Task.ShellSession, "")
}

func TestOpenShell_RejectsRunning(t *testing.T) {
	t.Parallel()
	shell := &ShellRegistryMock{SeedFunc: func(id string, orgID, taskID int64) error { return nil }}
	srv := New(Options{Store: teststore.New(t), Shells: shell})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctx := createCtx(t, org)

	// Drive the task to RUNNING: opening a shell must not displace a live run.
	createResp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:         "Task",
		Runner:       "test-runner",
		Workspace:    "test-workspace",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)
	taskID := createResp.Task.Id
	_, err = srv.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: taskID, Event: "started", Version: 0}},
	})
	assert.NilError(t, err)

	// Act
	_, err = srv.OpenShell(ctx, &gritzv1.OpenShellRequest{TaskId: taskID})

	// Assert: rejected, nothing seeded, no shell session recorded.
	assert.Equal(t, connect.CodeOf(err), connect.CodeFailedPrecondition)
	assert.Assert(t, cmp.Len(shell.SeedCalls(), 0))
	getResp, err := srv.GetTask(ctx, &gritzv1.GetTaskRequest{Id: taskID})
	assert.NilError(t, err)
	assert.Equal(t, getResp.Task.ShellSession, "")
}

func TestOpenShell_CrossOrgDenied(t *testing.T) {
	t.Parallel()
	shell := &ShellRegistryMock{SeedFunc: func(id string, orgID, taskID int64) error { return nil }}
	srv := New(Options{Store: teststore.New(t), Shells: shell})
	orgA := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	orgB := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	ctxA := createCtx(t, orgA)
	ctxB := createCtx(t, orgB)

	// Completed task owned by orgA.
	createResp, err := srv.CreateTask(ctxA, &gritzv1.CreateTaskRequest{
		Name:         "Task",
		Runner:       "test-runner",
		Workspace:    "test-workspace",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)
	taskID := createResp.Task.Id
	_, err = srv.SubmitRunnerEvents(ctxA, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: taskID, Event: "started", Version: 0}},
	})
	assert.NilError(t, err)
	_, err = srv.SubmitRunnerEvents(ctxA, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: taskID, Event: "stopped", Version: 0}},
	})
	assert.NilError(t, err)

	// A caller in another org cannot open a shell for orgA's task.
	_, err = srv.OpenShell(ctxB, &gritzv1.OpenShellRequest{TaskId: taskID})
	assert.Assert(t, err != nil)
	assert.Assert(t, cmp.Len(shell.SeedCalls(), 0))
}
