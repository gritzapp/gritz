package store_test

import (
	"slices"
	"testing"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
)

func TestListOrgIDsByGitHubInstallation(t *testing.T) {
	t.Parallel()
	// Arrange: two non-archived orgs share an installation, a third archived org
	// shares it too, and a fourth org has a different installation.
	s := teststore.New(t)
	orgA := teststore.CreateOrg(t, s, nil)
	orgB := teststore.CreateOrg(t, s, nil)
	archived := teststore.CreateOrg(t, s, nil)
	other := teststore.CreateOrg(t, s, nil)

	// Derive the installation ids from a unique org id so parallel tests sharing
	// the orgs table don't collide on installation values.
	installationID := orgA.OrgID
	otherInstallationID := orgA.OrgID + 1_000_000

	assert.NilError(t, s.SetOrgGitHubInstallation(t.Context(), nil, orgA.OrgID, installationID))
	assert.NilError(t, s.SetOrgGitHubInstallation(t.Context(), nil, orgB.OrgID, installationID))
	assert.NilError(t, s.SetOrgGitHubInstallation(t.Context(), nil, archived.OrgID, installationID))
	assert.NilError(t, s.ArchiveOrg(t.Context(), nil, archived.OrgID))
	assert.NilError(t, s.SetOrgGitHubInstallation(t.Context(), nil, other.OrgID, otherInstallationID))

	// Act
	ids, err := s.ListOrgIDsByGitHubInstallation(t.Context(), nil, installationID)

	// Assert: only the non-archived orgs sharing the installation are returned —
	// the archived org and the differently-installed org are excluded.
	assert.NilError(t, err)
	slices.Sort(ids)
	want := []int64{orgA.OrgID, orgB.OrgID}
	slices.Sort(want)
	assert.DeepEqual(t, ids, want)
}

func TestGetOrgCustomEventTypesDefault(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)

	// Act
	defs, err := s.GetOrgCustomEventTypes(t.Context(), nil, org.OrgID)

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, len(defs), 0)
}

func TestSetAndGetOrgCustomEventTypes(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	defs := []model.EventTypeDef{
		{
			Source: "custom",
			Type:   "problem",
			Label:  "Problem",
			Attrs: []model.AttrDef{
				{Key: "body"},
				{Key: "url"},
				{Key: "severity", Label: "Severity", Help: "How bad it is", Placeholder: "critical"},
			},
		},
		{
			Source: "custom",
			Type:   "deploy",
			Attrs:  []model.AttrDef{{Key: "body"}, {Key: "url"}},
		},
	}
	err := s.SetOrgCustomEventTypes(t.Context(), nil, org.OrgID, defs)
	assert.NilError(t, err)

	// Act
	got, err := s.GetOrgCustomEventTypes(t.Context(), nil, org.OrgID)

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, got, defs)
}
