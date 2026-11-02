package apiserver_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"gotest.tools/v3/assert"
)

func TestOpenShell(t *testing.T) {
	t.Parallel()
	// Arrange: drive a task to a terminal (completed) status so it can be
	// shelled. Runner events use version 0 to bypass the version check.
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task"})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: task.ID, Event: "started", Version: 0}},
	})
	h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: task.ID, Event: "stopped", Version: 0}},
	})

	// Act
	resp, err := h.Org.Client.OpenShell(t.Context(), &gritzv1.OpenShellRequest{TaskId: task.ID})

	// Assert: a session id is returned, recorded on the task's shell_session,
	// and a start is issued so the runner brings the sandbox up against the
	// preserved disk.
	assert.NilError(t, err)
	assert.Assert(t, resp.SessionId != "")
	got := h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID})
	assert.Equal(t, got.ShellSession, resp.SessionId)
	assert.Equal(t, got.Command, gritzv1.TaskCommand_START)
	assert.Equal(t, got.Status, gritzv1.TaskStatus_PENDING)
}

func TestOpenShell_RejectsPending(t *testing.T) {
	t.Parallel()
	// Arrange: a started task is PENDING (non-terminal).
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task"})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})

	// Act
	_, err := h.Org.Client.OpenShell(t.Context(), &gritzv1.OpenShellRequest{TaskId: task.ID})

	// Assert: rejected, no shell session recorded.
	assert.Equal(t, connect.CodeOf(err), connect.CodeFailedPrecondition)
	got := h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID})
	assert.Equal(t, got.ShellSession, "")
}

func TestOpenShell_RejectsRunning(t *testing.T) {
	t.Parallel()
	// Arrange: drive the task to RUNNING; opening a shell must not displace a
	// live run.
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task"})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: task.ID, Event: "started", Version: 0}},
	})

	// Act
	_, err := h.Org.Client.OpenShell(t.Context(), &gritzv1.OpenShellRequest{TaskId: task.ID})

	// Assert: rejected, no shell session recorded.
	assert.Equal(t, connect.CodeOf(err), connect.CodeFailedPrecondition)
	got := h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID})
	assert.Equal(t, got.ShellSession, "")
}

func TestOpenShell_CrossOrgDenied(t *testing.T) {
	t.Parallel()
	// Arrange: a completed task owned by the default org.
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task"})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: task.ID, Event: "started", Version: 0}},
	})
	h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{{TaskId: task.ID, Event: "stopped", Version: 0}},
	})

	// Act: a caller in another org opens a shell for the task.
	_, err := other.Client.OpenShell(t.Context(), &gritzv1.OpenShellRequest{TaskId: task.ID})

	// Assert: the task is invisible to the other org, and no shell session is
	// recorded.
	assert.Equal(t, connect.CodeOf(err), connect.CodeNotFound)
	got := h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID})
	assert.Equal(t, got.ShellSession, "")
}
