package api_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/auth/agentauth"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"gotest.tools/v3/assert"
)

func TestGetRunSpecInputs(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	h.Org.RegisterWorkspaces(&gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{{
			Name: "story",
			Variables: []*gritzv1.VariableDecl{
				{Name: "BRANCH", Default: "main"},
				{Name: "JIRA_ISSUE", Required: true},
			},
		}},
	})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Runner:    "runner-1",
		Workspace: "story",
		Draft:     true,
		Variables: map[string]string{"JIRA_ISSUE": "APP-142"},
	})

	// Act
	resp, err := h.Org.Client.GetRunSpecInputs(t.Context(), &gritzv1.GetRunSpecInputsRequest{
		TaskId:       task.ID,
		Capabilities: []string{agentauth.CapabilityGitHubToken},
	})

	// Assert - the task's stored variables, and a token the task can call the
	// API with.
	assert.NilError(t, err)
	assert.DeepEqual(t, resp.Variables, map[string]string{"JIRA_ISSUE": "APP-142", "BRANCH": "main"})
	_, err = h.Server.Client(resp.Token).GetTask(t.Context(), &gritzv1.GetTaskRequest{Id: task.ID})
	assert.NilError(t, err)
}

func TestGetRunSpecInputs_InvalidCapability(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})

	// Act
	_, err := h.Org.Client.GetRunSpecInputs(t.Context(), &gritzv1.GetRunSpecInputsRequest{
		TaskId:       task.ID,
		Capabilities: []string{"bogus"},
	})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
}

func TestGetRunSpecInputs_Denied(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	// A caller that can read the task but not mint its token.
	key := h.Org.CreateScopedKey(authscope.Scopes{authscope.New(authscope.OpTaskRead)})

	// Act
	_, err := key.Client.GetRunSpecInputs(t.Context(), &gritzv1.GetRunSpecInputsRequest{TaskId: task.ID})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
}

func TestGetRunSpecInputs_CrossOrg(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})

	// Act
	_, err := other.Client.GetRunSpecInputs(t.Context(), &gritzv1.GetRunSpecInputsRequest{TaskId: task.ID})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeNotFound)
}
