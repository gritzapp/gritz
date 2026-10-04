package apiserver

import (
	"testing"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
)

func TestRegisterWorkspaces(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)

	// Register workspaces
	_, err := srv.RegisterWorkspaces(ctx, &gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{
			{Name: "workspace-a", Description: "First workspace"},
			{Name: "workspace-b"},
		},
	})
	assert.NilError(t, err)

	// List workspaces
	listResp, err := srv.ListWorkspaces(ctx, &gritzv1.ListWorkspacesRequest{})
	assert.NilError(t, err)
	assert.DeepEqual(t, listResp.Workspaces, []*gritzv1.RegisteredWorkspace{
		{Name: "workspace-a", RunnerId: "runner-1", Description: "First workspace"},
		{Name: "workspace-b", RunnerId: "runner-1"},
	}, protocmp.Transform(), protocmp.IgnoreFields(&gritzv1.RegisteredWorkspace{}, "updated_at"))
}

func TestRegisterWorkspaces_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	orgA := teststore.CreateOrg(t, srv.store, nil)
	ctxA := createCtx(t, orgA)
	orgB := teststore.CreateOrg(t, srv.store, nil)
	ctxB := createCtx(t, orgB)

	// User A registers workspaces
	_, err := srv.RegisterWorkspaces(ctxA, &gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{
			{Name: "workspace-a"},
		},
	})
	assert.NilError(t, err)

	// User B registers different workspaces
	_, err = srv.RegisterWorkspaces(ctxB, &gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{
			{Name: "workspace-b"},
		},
	})
	assert.NilError(t, err)

	// Act
	listRespA, errA := srv.ListWorkspaces(ctxA, &gritzv1.ListWorkspacesRequest{})
	listRespB, errB := srv.ListWorkspaces(ctxB, &gritzv1.ListWorkspacesRequest{})

	// Assert - each user should only see their own workspaces
	assert.NilError(t, errA)
	assert.NilError(t, errB)
	assert.Equal(t, len(listRespA.Workspaces), 1)
	assert.Equal(t, listRespA.Workspaces[0].Name, "workspace-a")
	assert.Equal(t, len(listRespB.Workspaces), 1)
	assert.Equal(t, listRespB.Workspaces[0].Name, "workspace-b")
}

func TestRegisterWorkspaces_SameRunnerDifferentUsers(t *testing.T) {
	t.Parallel()
	// Arrange - both users register workspaces for the same runner ID
	srv := New(Options{Store: teststore.New(t)})
	orgA := teststore.CreateOrg(t, srv.store, nil)
	ctxA := createCtx(t, orgA)
	orgB := teststore.CreateOrg(t, srv.store, nil)
	ctxB := createCtx(t, orgB)

	// User A registers workspaces for runner-1
	_, err := srv.RegisterWorkspaces(ctxA, &gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{
			{Name: "workspace-a"},
		},
	})
	assert.NilError(t, err)

	// User B registers workspaces for the same runner-1
	_, err = srv.RegisterWorkspaces(ctxB, &gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{
			{Name: "workspace-b"},
		},
	})
	assert.NilError(t, err)

	// Act - User A re-registers (should only delete their own workspaces)
	_, err = srv.RegisterWorkspaces(ctxA, &gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{
			{Name: "workspace-c"},
		},
	})
	assert.NilError(t, err)

	// Assert - User A should see workspace-c, User B should still see workspace-b
	listRespA, err := srv.ListWorkspaces(ctxA, &gritzv1.ListWorkspacesRequest{})
	assert.NilError(t, err)
	assert.Equal(t, len(listRespA.Workspaces), 1)
	assert.Equal(t, listRespA.Workspaces[0].Name, "workspace-c")

	listRespB, err := srv.ListWorkspaces(ctxB, &gritzv1.ListWorkspacesRequest{})
	assert.NilError(t, err)
	assert.Equal(t, len(listRespB.Workspaces), 1)
	assert.Equal(t, listRespB.Workspaces[0].Name, "workspace-b")
}

func TestClearWorkspaces(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)

	// Register workspaces
	_, err := srv.RegisterWorkspaces(ctx, &gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{
			{Name: "workspace-a"},
			{Name: "workspace-b"},
		},
	})
	assert.NilError(t, err)

	// Verify workspaces exist
	listResp, err := srv.ListWorkspaces(ctx, &gritzv1.ListWorkspacesRequest{})
	assert.NilError(t, err)
	assert.Equal(t, len(listResp.Workspaces), 2)

	// Clear workspaces
	_, err = srv.ClearWorkspaces(ctx, &gritzv1.ClearWorkspacesRequest{})
	assert.NilError(t, err)

	// Verify workspaces are cleared
	listResp, err = srv.ListWorkspaces(ctx, &gritzv1.ListWorkspacesRequest{})
	assert.NilError(t, err)
	assert.Equal(t, len(listResp.Workspaces), 0)
}

func TestClearWorkspaces_Permissions(t *testing.T) {
	t.Parallel()
	srv := New(Options{Store: teststore.New(t)})
	orgA := teststore.CreateOrg(t, srv.store, nil)
	ctxA := createCtx(t, orgA)
	orgB := teststore.CreateOrg(t, srv.store, nil)
	ctxB := createCtx(t, orgB)

	// User A registers workspaces
	_, err := srv.RegisterWorkspaces(ctxA, &gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{
			{Name: "workspace-a"},
		},
	})
	assert.NilError(t, err)

	// User B registers workspaces
	_, err = srv.RegisterWorkspaces(ctxB, &gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{
			{Name: "workspace-b"},
		},
	})
	assert.NilError(t, err)

	// User A clears workspaces - should only clear their own
	_, err = srv.ClearWorkspaces(ctxA, &gritzv1.ClearWorkspacesRequest{})
	assert.NilError(t, err)

	// Verify User A has no workspaces
	listRespA, err := srv.ListWorkspaces(ctxA, &gritzv1.ListWorkspacesRequest{})
	assert.NilError(t, err)
	assert.Equal(t, len(listRespA.Workspaces), 0)

	// Verify User B still has their workspaces
	listRespB, err := srv.ListWorkspaces(ctxB, &gritzv1.ListWorkspacesRequest{})
	assert.NilError(t, err)
	assert.Equal(t, len(listRespB.Workspaces), 1)
	assert.Equal(t, listRespB.Workspaces[0].Name, "workspace-b")
}
