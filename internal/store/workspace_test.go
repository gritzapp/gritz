package store_test

import (
	"database/sql"
	"testing"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/gritzapp/gritz/internal/model"
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
	assert.NilError(t, s.CreateWorkspace(t.Context(), nil, &model.Workspace{RunnerID: "r", Name: "a", OrgID: org.OrgID, Variables: decls}))
	assert.NilError(t, s.CreateWorkspace(t.Context(), nil, &model.Workspace{RunnerID: "r", Name: "b", OrgID: org.OrgID}))

	// Assert - a workspace without declarations reads back nil.
	got, err := s.ListWorkspaces(t.Context(), nil, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(got), 2)
	assert.DeepEqual(t, got[0].Variables, decls)
	assert.Assert(t, got[1].Variables == nil)
}

func TestCreateWorkspace_Features(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	decls := []model.FeatureDecl{
		{Name: "codex", Description: "Run on Codex"},
		{Name: "jira"},
	}

	// Act
	assert.NilError(t, s.CreateWorkspace(t.Context(), nil, &model.Workspace{RunnerID: "r", Name: "a", OrgID: org.OrgID, Features: decls}))
	assert.NilError(t, s.CreateWorkspace(t.Context(), nil, &model.Workspace{RunnerID: "r", Name: "b", OrgID: org.OrgID}))

	// Assert - a workspace without declarations reads back nil.
	got, err := s.ListWorkspaces(t.Context(), nil, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(got), 2)
	assert.DeepEqual(t, got[0].Features, decls)
	assert.Assert(t, got[1].Features == nil)
}

func TestGetWorkspace(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	ws := &model.Workspace{
		RunnerID:    "r",
		Name:        "a",
		Description: "Stories",
		OrgID:       org.OrgID,
		Variables:   []taskvar.Decl{{Name: "JIRA_ISSUE", Required: true}},
		Features:    []model.FeatureDecl{{Name: "jira", Description: "Jira MCP server"}},
	}
	assert.NilError(t, s.CreateWorkspace(t.Context(), nil, ws))

	// Act
	got, err := s.GetWorkspace(t.Context(), nil, "r", "a", org.OrgID)

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, got, ws, cmpopts.IgnoreFields(model.Workspace{}, "ID", "UpdatedAt"))
}

func TestGetWorkspace_NotFound(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	assert.NilError(t, s.CreateWorkspace(t.Context(), nil, &model.Workspace{RunnerID: "r", Name: "a", OrgID: org.OrgID}))

	// Act - registered on another runner only.
	_, err := s.GetWorkspace(t.Context(), nil, "other", "a", org.OrgID)

	// Assert
	assert.ErrorIs(t, err, sql.ErrNoRows)
}
