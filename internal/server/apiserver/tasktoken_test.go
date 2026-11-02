package apiserver_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gritzapp/gritz/internal/auth/agentauth"
	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"gotest.tools/v3/assert"
)

func TestCreateTaskToken(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(nil)

	// Act
	resp, err := h.Org.Client.CreateTaskToken(t.Context(), &gritzv1.CreateTaskTokenRequest{
		TaskId:       task.ID,
		Capabilities: []string{agentauth.CapabilityGitHubToken},
	})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, resp.Token != "")
	// The minted token is an ordinary app JWT: it verifies on the normal path...
	_, err = h.Server.Client(resp.Token).GetTask(t.Context(), &gritzv1.GetTaskRequest{Id: task.ID})
	assert.NilError(t, err)
	// ...and carries the task's org plus the narrow scopes derived from the row
	// (not the admin wildcard the caller holds). The server verified the
	// signature above, so the claims are read without verifying it again.
	var claims apiauth.AppClaims
	_, _, err = jwt.NewParser().ParseUnverified(resp.Token, &claims)
	assert.NilError(t, err)
	assert.Equal(t, claims.OrgID, h.Org.ID)
	want := agentauth.Scopes(agentauth.ScopeOptions{
		TaskID:       task.ID,
		Capabilities: []string{agentauth.CapabilityGitHubToken},
	})
	assert.DeepEqual(t, claims.Scopes, want)
}

func TestCreateTaskToken_InvalidCapability(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(nil)

	// Act
	_, err := h.Org.Client.CreateTaskToken(t.Context(), &gritzv1.CreateTaskTokenRequest{
		TaskId:       task.ID,
		Capabilities: []string{"bogus"},
	})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
}

func TestCreateTaskToken_Denied(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(nil)
	// A present-but-scopeless caller lacks the task_token.create capability.
	key := h.Org.CreateScopedKey(authscope.Scopes{})

	// Act
	_, err := key.Client.CreateTaskToken(t.Context(), &gritzv1.CreateTaskTokenRequest{TaskId: task.ID})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
}

func TestCreateTaskToken_CrossOrg(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	task := h.Org.CreateTask(nil)

	// Act - the other org's caller cannot mint a token for this org's task; the
	// org-scoped read hides it.
	_, err := other.Client.CreateTaskToken(t.Context(), &gritzv1.CreateTaskTokenRequest{TaskId: task.ID})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeNotFound)
}
