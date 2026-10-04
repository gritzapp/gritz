package store_test

import (
	"slices"
	"testing"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
)

func TestGetOrgRoutingRulesDefault(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)

	// Act
	rules, err := s.GetOrgRoutingRules(t.Context(), nil, org.OrgID)

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, len(rules), 0)
}

func TestSetAndGetOrgRoutingRules(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	rules := []model.RoutingRule{
		{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "bot:"}}},
		{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "mention", Op: "equals", Value: "mybot"}}},
	}
	err := s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, rules)
	assert.NilError(t, err)

	// Act
	got, err := s.GetOrgRoutingRules(t.Context(), nil, org.OrgID)

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, got, rules)
}

func TestGetRoutingRulesByOrgs(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	orgA := teststore.CreateOrg(t, s, nil)
	orgB := teststore.CreateOrg(t, s, nil)
	rulesA := []model.RoutingRule{{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "a:"}}}}
	err := s.SetOrgRoutingRules(t.Context(), nil, orgA.OrgID, rulesA)
	assert.NilError(t, err)

	// Act
	result, err := s.GetRoutingRulesByOrgs(t.Context(), nil, []int64{orgA.OrgID, orgB.OrgID})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, len(result), 2)
	assert.DeepEqual(t, result[orgA.OrgID], rulesA)
	assert.Equal(t, len(result[orgB.OrgID]), 0)
}

func TestSetOrgRoutingRulesOverwrite(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	rules1 := []model.RoutingRule{{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "old:"}}}}
	err := s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, rules1)
	assert.NilError(t, err)
	rules2 := []model.RoutingRule{{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "mention", Op: "equals", Value: "newbot"}}}}
	err = s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, rules2)
	assert.NilError(t, err)

	// Act
	got, err := s.GetOrgRoutingRules(t.Context(), nil, org.OrgID)

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, got, rules2)
}

func TestListRoutingRulesForEvent(t *testing.T) {
	t.Parallel()
	// Arrange: a member org (the actor belongs to it) and a distinct org the
	// actor is NOT a member of, each with its own rules.
	s := teststore.New(t)
	memberOrg := teststore.CreateOrg(t, s, nil)
	memberRules := []model.RoutingRule{{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "m:"}}}}
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, memberOrg.OrgID, memberRules))

	nonMemberOrg := teststore.CreateOrg(t, s, nil)
	nonMemberRules := []model.RoutingRule{{Source: "github", Type: "issue_comment", Public: true}}
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, nonMemberOrg.OrgID, nonMemberRules))

	// Act
	result, err := s.ListRoutingRulesForEvent(t.Context(), nil, memberOrg.UserID, []int64{nonMemberOrg.OrgID})

	// Assert: the member org is tagged IsMember=true with all its rules; the
	// passed org is tagged IsMember=false with its rules (the store returns a
	// faithful view — the public-flag filter is the router's job).
	assert.NilError(t, err)
	member := testx.FindFunc(t, result, func(o store.OrgRoutingRules, _ int) bool { return o.OrgID == memberOrg.OrgID })
	assert.Equal(t, member.IsMember, true)
	assert.DeepEqual(t, member.Rules, memberRules)

	nonMember := testx.FindFunc(t, result, func(o store.OrgRoutingRules, _ int) bool { return o.OrgID == nonMemberOrg.OrgID })
	assert.Equal(t, nonMember.IsMember, false)
	assert.DeepEqual(t, nonMember.Rules, nonMemberRules)
}

func TestListRoutingRulesForEventMembershipWins(t *testing.T) {
	t.Parallel()
	// Arrange: an org the actor is a member of, also named in the passed orgs.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	rules := []model.RoutingRule{{Source: "github", Type: "issue_comment", Public: true}}
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, rules))

	// Act: pass the member org's own id — the overlap should resolve in favor of
	// membership (returned once, from the member branch).
	result, err := s.ListRoutingRulesForEvent(t.Context(), nil, org.UserID, []int64{org.OrgID})

	// Assert: the overlapping org is returned exactly once, from the member branch.
	assert.NilError(t, err)
	assert.Equal(t, len(result), 1)
	got := testx.FindFunc(t, result, func(o store.OrgRoutingRules, _ int) bool { return o.OrgID == org.OrgID })
	assert.Equal(t, got.IsMember, true)
	assert.DeepEqual(t, got.Rules, rules)
}

func TestListRoutingRulesForEventExcludesArchived(t *testing.T) {
	t.Parallel()
	// Arrange: an archived member org and an archived non-member org.
	s := teststore.New(t)
	memberOrg := teststore.CreateOrg(t, s, nil)
	assert.NilError(t, s.ArchiveOrg(t.Context(), nil, memberOrg.OrgID))
	nonMemberOrg := teststore.CreateOrg(t, s, nil)
	assert.NilError(t, s.ArchiveOrg(t.Context(), nil, nonMemberOrg.OrgID))

	// Act
	result, err := s.ListRoutingRulesForEvent(t.Context(), nil, memberOrg.UserID, []int64{nonMemberOrg.OrgID})

	// Assert: archived orgs are excluded from both branches.
	assert.NilError(t, err)
	for _, o := range result {
		assert.Assert(t, o.OrgID != memberOrg.OrgID, "archived member org should be excluded")
		assert.Assert(t, o.OrgID != nonMemberOrg.OrgID, "archived non-member org should be excluded")
	}
}

func TestListRoutingRulesForEventEmptyOrgs(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	memberOrg := teststore.CreateOrg(t, s, nil)
	rules := []model.RoutingRule{{Source: "github", Type: "issue_comment"}}
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, memberOrg.OrgID, rules))

	// Act: an empty orgs slice reduces to the member-only set.
	result, err := s.ListRoutingRulesForEvent(t.Context(), nil, memberOrg.UserID, nil)

	// Assert
	assert.NilError(t, err)
	member := testx.FindFunc(t, result, func(o store.OrgRoutingRules, _ int) bool { return o.OrgID == memberOrg.OrgID })
	assert.Equal(t, member.IsMember, true)
	assert.DeepEqual(t, member.Rules, rules)
}

func TestListRoutingRulesForEventEmptyUser(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	rules := []model.RoutingRule{{Source: "github", Type: "issue_comment", Public: true}}
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, rules))

	// Act: an empty user id yields just the non-member branch.
	result, err := s.ListRoutingRulesForEvent(t.Context(), nil, "", []int64{org.OrgID})

	// Assert
	assert.NilError(t, err)
	got := testx.FindFunc(t, result, func(o store.OrgRoutingRules, _ int) bool { return o.OrgID == org.OrgID })
	assert.Equal(t, got.IsMember, false)
	assert.DeepEqual(t, got.Rules, rules)
}

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

func TestSetOrgRoutingRulesClearToEmpty(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	rules := []model.RoutingRule{{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "test:"}}}}
	err := s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, rules)
	assert.NilError(t, err)
	err = s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, nil)
	assert.NilError(t, err)

	// Act
	got, err := s.GetOrgRoutingRules(t.Context(), nil, org.OrgID)

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, len(got), 0)
}
