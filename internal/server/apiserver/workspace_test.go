package apiserver_test

import (
	"testing"

	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/testx"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestRegisterWorkspaces(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	_, err := h.Org.Client.RegisterWorkspaces(t.Context(), &gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{
			{Name: "workspace-a", Description: "First workspace"},
			{Name: "workspace-b"},
		},
	})

	// Assert - runner-1's workspaces beside the harness's own.
	assert.NilError(t, err)
	assert.DeepEqual(t, h.Org.ListWorkspaces(&gritzv1.ListWorkspacesRequest{}), []*gritzv1.RegisteredWorkspace{
		{Name: e2e.DefaultWorkspace, RunnerId: e2e.DefaultRunner},
		{Name: "workspace-a", RunnerId: "runner-1", Description: "First workspace"},
		{Name: "workspace-b", RunnerId: "runner-1"},
	}, protocmp.Transform(), protocmp.IgnoreFields(&gritzv1.RegisteredWorkspace{}, "updated_at"))
}

func TestRegisterWorkspaces_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	h.Org.RegisterWorkspaces(&gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{
			{Name: "workspace-a"},
		},
	})
	other.RegisterWorkspaces(&gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{
			{Name: "workspace-b"},
		},
	})

	// Act
	respA, errA := h.Org.Client.ListWorkspaces(t.Context(), &gritzv1.ListWorkspacesRequest{})
	respB, errB := other.Client.ListWorkspaces(t.Context(), &gritzv1.ListWorkspacesRequest{})

	// Assert - each org sees only its own workspaces.
	assert.NilError(t, errA)
	assert.NilError(t, errB)
	assert.DeepEqual(t, testx.ExtractField(respA.Workspaces, "Name"), []string{e2e.DefaultWorkspace, "workspace-a"})
	assert.DeepEqual(t, testx.ExtractField(respB.Workspaces, "Name"), []string{e2e.DefaultWorkspace, "workspace-b"})
}

func TestRegisterWorkspaces_SameRunnerDifferentUsers(t *testing.T) {
	t.Parallel()
	// Arrange - both orgs register workspaces for the same runner ID.
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	h.Org.RegisterWorkspaces(&gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{
			{Name: "workspace-a"},
		},
	})
	other.RegisterWorkspaces(&gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{
			{Name: "workspace-b"},
		},
	})

	// Act - re-registering replaces only the caller's org's workspaces.
	_, err := h.Org.Client.RegisterWorkspaces(t.Context(), &gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{
			{Name: "workspace-c"},
		},
	})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t,
		testx.ExtractField(h.Org.ListWorkspaces(&gritzv1.ListWorkspacesRequest{}), "Name"),
		[]string{e2e.DefaultWorkspace, "workspace-c"},
	)
	assert.DeepEqual(t,
		testx.ExtractField(other.ListWorkspaces(&gritzv1.ListWorkspacesRequest{}), "Name"),
		[]string{e2e.DefaultWorkspace, "workspace-b"},
	)
}

func TestClearWorkspaces(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	h.Org.RegisterWorkspaces(&gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{
			{Name: "workspace-a"},
			{Name: "workspace-b"},
		},
	})

	// Act
	_, err := h.Org.Client.ClearWorkspaces(t.Context(), &gritzv1.ClearWorkspacesRequest{})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(h.Org.ListWorkspaces(&gritzv1.ListWorkspacesRequest{}), 0))
}

func TestClearWorkspaces_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	h.Org.RegisterWorkspaces(&gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{
			{Name: "workspace-a"},
		},
	})
	other.RegisterWorkspaces(&gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{
			{Name: "workspace-b"},
		},
	})

	// Act - clears only the caller's org's workspaces.
	_, err := h.Org.Client.ClearWorkspaces(t.Context(), &gritzv1.ClearWorkspacesRequest{})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(h.Org.ListWorkspaces(&gritzv1.ListWorkspacesRequest{}), 0))
	assert.DeepEqual(t,
		testx.ExtractField(other.ListWorkspaces(&gritzv1.ListWorkspacesRequest{}), "Name"),
		[]string{e2e.DefaultWorkspace, "workspace-b"},
	)
}
