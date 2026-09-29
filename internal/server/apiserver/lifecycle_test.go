package apiserver

import (
	"context"
	"slices"
	"testing"

	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"github.com/icholy/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
)

// lifecycleEvents returns the task's lifecycle events most-recent-first.
// ListEventsByTask returns chronological (oldest-first) order, so the collected
// events are reversed to put the most recent lifecycle event at index 0.
func lifecycleEvents(t *testing.T, srv *Server, ctx context.Context, taskID int64) []*gritzv1.LifecyclePayload {
	t.Helper()
	resp, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{TaskId: taskID})
	assert.NilError(t, err)
	var out []*gritzv1.LifecyclePayload
	for _, e := range resp.Events {
		if l := e.GetLifecycle(); l != nil {
			out = append(out, l)
		}
	}
	slices.Reverse(out)
	return out
}

func lifecycleTestServer(t *testing.T) (*Server, context.Context) {
	t.Helper()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}}})
	return srv, createCtx(t, org)
}

func TestLifecycle_CreateAppendsCreatedEvent(t *testing.T) {
	t.Parallel()
	srv, ctx := lifecycleTestServer(t)

	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:         "Task",
		Runner:       "test-runner",
		Workspace:    "test-workspace",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)

	events := lifecycleEvents(t, srv, ctx, resp.Task.Id)
	assert.Equal(t, len(events), 1)
	assert.Equal(t, events[0].Kind, gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED)
	assert.Equal(t, events[0].Actor.Kind, "user")
	// A freshly created task has no prior status; it lands in PENDING.
	assert.Equal(t, events[0].FromStatus, "")
	assert.Equal(t, events[0].ToStatus, "Pending")
}

func TestLifecycle_CancelAppendsCancelledEventBesideProjection(t *testing.T) {
	t.Parallel()
	srv, ctx := lifecycleTestServer(t)

	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:         "Task",
		Runner:       "test-runner",
		Workspace:    "test-workspace",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)
	taskID := resp.Task.Id

	_, err = srv.CancelTask(ctx, &gritzv1.CancelTaskRequest{Id: taskID})
	assert.NilError(t, err)

	// The lifecycle event records the transition...
	events := lifecycleEvents(t, srv, ctx, taskID)
	assert.Equal(t, events[0].Kind, gritzv1.LifecycleKind_LIFECYCLE_KIND_CANCELLED)
	assert.Equal(t, events[0].FromStatus, "Pending")
	assert.Equal(t, events[0].ToStatus, "Cancelled")
	// ...and the materialized status projection is updated in the same tx.
	getResp, err := srv.GetTask(ctx, &gritzv1.GetTaskRequest{Id: taskID})
	assert.NilError(t, err)
	assert.Equal(t, getResp.Task.Status, gritzv1.TaskStatus_CANCELLED)
}

func TestLifecycle_TaskMutationsAppendEvents(t *testing.T) {
	t.Parallel()
	srv, ctx := lifecycleTestServer(t)

	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:         "Task",
		Runner:       "test-runner",
		Workspace:    "test-workspace",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)
	taskID := resp.Task.Id

	// A pending task can't be restarted, so start its sandbox first, then restart
	// the running task. Runner events use version 0 to bypass the version check.
	_, err = srv.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: taskID, Event: "started", Version: 0}},
	})
	assert.NilError(t, err)
	_, err = srv.RestartTask(ctx, &gritzv1.RestartTaskRequest{Id: taskID})
	assert.NilError(t, err)

	// Drive it back to running, then to a terminal status so it can be archived.
	_, err = srv.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: taskID, Event: "started", Version: 0}},
	})
	assert.NilError(t, err)
	_, err = srv.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: taskID, Event: "stopped", Version: 0}},
	})
	assert.NilError(t, err)

	_, err = srv.ArchiveTask(ctx, &gritzv1.ArchiveTaskRequest{Id: taskID})
	assert.NilError(t, err)
	_, err = srv.UnarchiveTask(ctx, &gritzv1.UnarchiveTaskRequest{Id: taskID})
	assert.NilError(t, err)

	// Collect the set of lifecycle kinds present on the stream.
	kinds := map[gritzv1.LifecycleKind]bool{}
	for _, e := range lifecycleEvents(t, srv, ctx, taskID) {
		kinds[e.Kind] = true
	}
	assert.Assert(t, kinds[gritzv1.LifecycleKind_LIFECYCLE_KIND_CREATED])
	assert.Assert(t, kinds[gritzv1.LifecycleKind_LIFECYCLE_KIND_RESTARTED])
	assert.Assert(t, kinds[gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_STARTED])
	assert.Assert(t, kinds[gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_EXITED])
	assert.Assert(t, kinds[gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED])
	assert.Assert(t, kinds[gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED])
}

func TestLifecycle_RunnerEventsAppendSandboxEvents(t *testing.T) {
	t.Parallel()
	srv, ctx := lifecycleTestServer(t)

	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:         "Task",
		Runner:       "test-runner",
		Workspace:    "test-workspace",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)
	taskID := resp.Task.Id

	// started: PENDING -> RUNNING, actor is the runner.
	_, err = srv.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: taskID, Event: "started", Version: 1}},
	})
	assert.NilError(t, err)

	events := lifecycleEvents(t, srv, ctx, taskID)
	assert.Equal(t, events[0].Kind, gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_STARTED)
	assert.Equal(t, events[0].Actor.Kind, "runner")
	assert.Equal(t, events[0].FromStatus, "Pending")
	assert.Equal(t, events[0].ToStatus, "Running")

	// failed: RUNNING -> FAILED, with the producer's reason threaded into message.
	_, err = srv.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{
			TaskId:  taskID,
			Event:   "failed",
			Version: 0,
			Reason:  "setup command 0 failed: exit status 1",
		}},
	})
	assert.NilError(t, err)

	events = lifecycleEvents(t, srv, ctx, taskID)
	assert.Equal(t, events[0].Kind, gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_FAILED)
	assert.Equal(t, events[0].Message, "setup command 0 failed: exit status 1")
	assert.Equal(t, events[0].ToStatus, "Failed")
}

// TestLifecycle_RunnerFailedFallsBackWhenNoReason verifies that a "failed" event
// with an empty reason (an old runner/driver) keeps the legacy message.
func TestLifecycle_RunnerFailedFallsBackWhenNoReason(t *testing.T) {
	t.Parallel()
	srv, ctx := lifecycleTestServer(t)

	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:         "Task",
		Runner:       "test-runner",
		Workspace:    "test-workspace",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)
	taskID := resp.Task.Id

	_, err = srv.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: taskID, Event: "started", Version: 1}},
	})
	assert.NilError(t, err)

	_, err = srv.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: taskID, Event: "failed", Version: 0}},
	})
	assert.NilError(t, err)

	events := lifecycleEvents(t, srv, ctx, taskID)
	assert.Equal(t, events[0].Kind, gritzv1.LifecycleKind_LIFECYCLE_KIND_SANDBOX_FAILED)
	assert.Equal(t, events[0].Message, "container failed")
}
