package apiserver

import (
	"testing"

	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
)

func TestCreateOrg(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)

	// Act
	resp, err := srv.CreateOrg(ctx, &gritzv1.CreateOrgRequest{
		Name: "my-org",
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, resp.Org.Name, "my-org")
	assert.Assert(t, resp.Org.Id != 0)
}

func TestListOrgs(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)
	_, err := srv.CreateOrg(ctx, &gritzv1.CreateOrgRequest{Name: "org-1"})
	assert.NilError(t, err)
	_, err = srv.CreateOrg(ctx, &gritzv1.CreateOrgRequest{Name: "org-2"})
	assert.NilError(t, err)

	// Act
	resp, err := srv.ListOrgs(ctx, &gritzv1.ListOrgsRequest{})

	// Assert
	assert.NilError(t, err)
	// 2 created + 1 from CreateOrg
	assert.Equal(t, len(resp.Orgs), 3)
}

func TestDeleteOrg(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)
	createResp, err := srv.CreateOrg(ctx, &gritzv1.CreateOrgRequest{Name: "to-delete"})
	assert.NilError(t, err)

	// Act
	_, err = srv.DeleteOrg(ctx, &gritzv1.DeleteOrgRequest{Id: createResp.Org.Id})

	// Assert
	assert.NilError(t, err)
	resp, err := srv.ListOrgs(ctx, &gritzv1.ListOrgsRequest{})
	assert.NilError(t, err)
	assert.Equal(t, len(resp.Orgs), 1)
}

func TestDeleteOrg_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	orgA := teststore.CreateOrg(t, srv.store, nil)
	ctxA := createCtx(t, orgA)
	orgB := teststore.CreateOrg(t, srv.store, nil)
	ctxB := createCtx(t, orgB)
	createResp, err := srv.CreateOrg(ctxA, &gritzv1.CreateOrgRequest{Name: "user-a-org"})
	assert.NilError(t, err)

	// Act
	_, err = srv.DeleteOrg(ctxB, &gritzv1.DeleteOrgRequest{Id: createResp.Org.Id})

	// Assert
	assert.ErrorContains(t, err, "only the org owner can delete it")
}

func TestDeleteOrg_DefaultOrg(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)

	// Act
	_, err := srv.DeleteOrg(ctx, &gritzv1.DeleteOrgRequest{Id: org.OrgID})

	// Assert
	assert.ErrorContains(t, err, "cannot delete your default org")
}

func TestAddAndListOrgMembers(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	ownerOrg := teststore.CreateOrg(t, srv.store, nil)
	owner := createCtx(t, ownerOrg)
	memberOrg := teststore.CreateOrg(t, srv.store, nil)
	member := createCtx(t, memberOrg)
	memberEmail := apiauth.Caller(member).ID + "@test.com"

	// Act
	resp, err := srv.AddOrgMember(owner, &gritzv1.AddOrgMemberRequest{
		Email: memberEmail,
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, resp.Member.Role, "member")
	listResp, err := srv.ListOrgMembers(owner, &gritzv1.ListOrgMembersRequest{})
	assert.NilError(t, err)
	assert.Equal(t, len(listResp.Members), 2)
}

func TestDeleteOrg_WithTasks(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)
	createResp, err := srv.CreateOrg(ctx, &gritzv1.CreateOrgRequest{Name: "has-tasks"})
	assert.NilError(t, err)
	orgCtx := apiauth.WithUser(ctx, &apiauth.UserInfo{
		ID:     apiauth.Caller(ctx).ID,
		OrgID:  createResp.Org.Id,
		Scopes: authscope.Admin(),
	})
	_, err = srv.RegisterWorkspaces(orgCtx, &gritzv1.RegisterWorkspacesRequest{
		RunnerId: "test-runner",
		Workspaces: []*gritzv1.RegisteredWorkspace{
			{Name: "default"},
		},
	})
	assert.NilError(t, err)
	_, err = srv.CreateTask(orgCtx, &gritzv1.CreateTaskRequest{
		Runner:    "test-runner",
		Workspace: "default",
	})
	assert.NilError(t, err)

	// Act
	_, err = srv.DeleteOrg(ctx, &gritzv1.DeleteOrgRequest{Id: createResp.Org.Id})

	// Assert
	assert.NilError(t, err)
	listResp, err := srv.ListOrgs(ctx, &gritzv1.ListOrgsRequest{})
	assert.NilError(t, err)
	for _, o := range listResp.Orgs {
		assert.Assert(t, o.Id != createResp.Org.Id)
	}
}

func TestGetOrgSettings_GitHubInstallationID(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)
	err := srv.store.SetOrgGitHubInstallation(ctx, nil, org.OrgID, 12345)
	assert.NilError(t, err)

	// Act
	resp, err := srv.GetOrgSettings(ctx, &gritzv1.GetOrgSettingsRequest{})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, resp.GithubInstallationId, int64(12345))
}

func TestGetOrgSettings_NoGitHubInstallation(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)

	// Act
	resp, err := srv.GetOrgSettings(ctx, &gritzv1.GetOrgSettingsRequest{})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, resp.GithubInstallationId, int64(0))
}

func TestRemoveOrgMember(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	ownerOrg := teststore.CreateOrg(t, srv.store, nil)
	owner := createCtx(t, ownerOrg)
	memberOrg := teststore.CreateOrg(t, srv.store, nil)
	memberUser := apiauth.Caller(createCtx(t, memberOrg))
	memberEmail := memberUser.ID + "@test.com"
	_, err := srv.AddOrgMember(owner, &gritzv1.AddOrgMemberRequest{
		Email: memberEmail,
	})
	assert.NilError(t, err)

	// Act
	_, err = srv.RemoveOrgMember(owner, &gritzv1.RemoveOrgMemberRequest{
		UserId: memberUser.ID,
	})

	// Assert
	assert.NilError(t, err)
	listResp, err := srv.ListOrgMembers(owner, &gritzv1.ListOrgMembersRequest{})
	assert.NilError(t, err)
	assert.Equal(t, len(listResp.Members), 1)
}
