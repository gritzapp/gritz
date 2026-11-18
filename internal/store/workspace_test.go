package store_test

import (
	"testing"

	"github.com/gritzapp/gritz/internal/store/teststore"
	"github.com/gritzapp/gritz/internal/taskvar"
	"gotest.tools/v3/assert"
)

func TestCreateWorkspace_Variables(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	decls := []taskvar.Decl{
		{Name: "BRANCH", Description: "Branch to check out", Default: "main"},
		{Name: "JIRA_ISSUE", Required: true},
	}

	// Act
	assert.NilError(t, s.CreateWorkspace(t.Context(), nil, "r", "a", "", decls, org.OrgID))
	assert.NilError(t, s.CreateWorkspace(t.Context(), nil, "r", "b", "", nil, org.OrgID))

	// Assert - a workspace without declarations reads back nil.
	got, err := s.ListWorkspaces(t.Context(), nil, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(got), 2)
	assert.DeepEqual(t, got[0].Variables, decls)
	assert.Assert(t, got[1].Variables == nil)
}
