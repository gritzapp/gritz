package apiserver

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/auth/agentauth"
	"github.com/gritzapp/gritz/internal/auth/apiauth"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
)

func TestCreateTaskToken(t *testing.T) {
	t.Parallel()
	appKey, err := apiauth.CreateAppPrivateKey()
	assert.NilError(t, err)
	srv := New(Options{Store: teststore.New(t), AppKey: appKey})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{
		Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}},
	})
	task := teststore.CreateTask(t, srv.store, org, &teststore.TaskOptions{
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	ctx := createCtx(t, org)

	resp, err := srv.CreateTaskToken(ctx, &gritzv1.CreateTaskTokenRequest{
		TaskId:       task.ID,
		Capabilities: []string{agentauth.CapabilityGitHubToken},
	})
	assert.NilError(t, err)
	assert.Assert(t, resp.Token != "")

	// The minted token is an ordinary app JWT: it verifies on the normal path and
	// carries the task's org plus the narrow scopes derived from the row (not the
	// admin wildcard the caller holds).
	claims, err := apiauth.VerifyAppToken(appKey, resp.Token)
	assert.NilError(t, err)
	assert.Equal(t, claims.OrgID, org.OrgID)
	want := agentauth.Scopes(agentauth.ScopeOptions{
		TaskID:       task.ID,
		Capabilities: []string{agentauth.CapabilityGitHubToken},
	})
	assert.DeepEqual(t, claims.Scopes, want)
}

func TestCreateTaskToken_InvalidCapability(t *testing.T) {
	t.Parallel()
	appKey, err := apiauth.CreateAppPrivateKey()
	assert.NilError(t, err)
	srv := New(Options{Store: teststore.New(t), AppKey: appKey})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{
		Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}},
	})
	task := teststore.CreateTask(t, srv.store, org, &teststore.TaskOptions{
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})

	_, err = srv.CreateTaskToken(createCtx(t, org), &gritzv1.CreateTaskTokenRequest{
		TaskId:       task.ID,
		Capabilities: []string{"bogus"},
	})

	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
}

func TestCreateTaskToken_Denied(t *testing.T) {
	t.Parallel()
	appKey, err := apiauth.CreateAppPrivateKey()
	assert.NilError(t, err)
	srv := New(Options{Store: teststore.New(t), AppKey: appKey})
	org := teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{
		Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}},
	})
	task := teststore.CreateTask(t, srv.store, org, &teststore.TaskOptions{
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	// A present-but-scopeless caller lacks the task_token.create capability.
	ctx := apiauth.WithUser(t.Context(), &apiauth.UserInfo{ID: org.UserID, OrgID: org.OrgID})

	_, err = srv.CreateTaskToken(ctx, &gritzv1.CreateTaskTokenRequest{TaskId: task.ID})

	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
}

func TestCreateTaskToken_CrossOrg(t *testing.T) {
	t.Parallel()
	appKey, err := apiauth.CreateAppPrivateKey()
	assert.NilError(t, err)
	srv := New(Options{Store: teststore.New(t), AppKey: appKey})
	workspaces := &teststore.OrgOptions{
		Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}},
	}
	orgA := teststore.CreateOrg(t, srv.store, workspaces)
	orgB := teststore.CreateOrg(t, srv.store, workspaces)
	task := teststore.CreateTask(t, srv.store, orgA, &teststore.TaskOptions{
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})

	// orgB's caller cannot mint a token for orgA's task; the org-scoped read hides it.
	_, err = srv.CreateTaskToken(createCtx(t, orgB), &gritzv1.CreateTaskTokenRequest{TaskId: task.ID})

	assert.Equal(t, connect.CodeOf(err), connect.CodeNotFound)
}
