package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store/sqlc"
)

func (s *Store) CreateOrg(ctx context.Context, tx *sql.Tx, org *model.Org) error {
	now := time.Now().UTC()
	id, err := s.q(tx).CreateOrg(ctx, sqlc.CreateOrgParams{
		Name:      org.Name,
		Owner:     org.Owner,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		return err
	}
	org.ID = id
	org.CreatedAt = now
	org.UpdatedAt = now
	return nil
}

func (s *Store) GetOrg(ctx context.Context, tx *sql.Tx, id int64) (*model.Org, error) {
	row, err := s.q(tx).GetOrg(ctx, id)
	if err != nil {
		return nil, err
	}
	return &model.Org{
		ID:                     row.ID,
		Name:                   row.Name,
		Owner:                  row.Owner,
		Archived:               row.Archived,
		CreatedAt:              row.CreatedAt,
		UpdatedAt:              row.UpdatedAt,
		GitHubInstallationID:   row.GithubInstallationID.Int64,
		AtlassianWebhookSecret: row.AtlassianWebhookSecret,
	}, nil
}

func (s *Store) ListOrgsByMember(ctx context.Context, tx *sql.Tx, userID string) ([]*model.Org, error) {
	rows, err := s.q(tx).ListOrgsByMember(ctx, userID)
	if err != nil {
		return nil, err
	}
	orgs := make([]*model.Org, len(rows))
	for i, row := range rows {
		orgs[i] = &model.Org{
			ID:                   row.ID,
			Name:                 row.Name,
			Owner:                row.Owner,
			Archived:             row.Archived,
			CreatedAt:            row.CreatedAt,
			UpdatedAt:            row.UpdatedAt,
			GitHubInstallationID: row.GithubInstallationID.Int64,
		}
	}
	return orgs, nil
}

func (s *Store) UpdateOrg(ctx context.Context, tx *sql.Tx, org *model.Org) error {
	org.UpdatedAt = time.Now().UTC()
	return s.q(tx).UpdateOrg(ctx, sqlc.UpdateOrgParams{
		ID:        org.ID,
		Name:      org.Name,
		UpdatedAt: org.UpdatedAt,
	})
}

func (s *Store) ArchiveOrg(ctx context.Context, tx *sql.Tx, id int64) error {
	return s.q(tx).ArchiveOrg(ctx, id)
}

func (s *Store) DestroyOrg(ctx context.Context, tx *sql.Tx, id int64) error {
	return s.q(tx).DestroyOrg(ctx, id)
}

func (s *Store) AddOrgMember(ctx context.Context, tx *sql.Tx, member *model.OrgMember) error {
	now := time.Now().UTC()
	err := s.q(tx).AddOrgMember(ctx, sqlc.AddOrgMemberParams{
		OrgID:     member.OrgID,
		UserID:    member.UserID,
		Role:      member.Role,
		CreatedAt: now,
	})
	if err != nil {
		return err
	}
	member.CreatedAt = now
	return nil
}

func (s *Store) RemoveOrgMember(ctx context.Context, tx *sql.Tx, orgID int64, userID string) error {
	return s.q(tx).RemoveOrgMember(ctx, sqlc.RemoveOrgMemberParams{
		OrgID:  orgID,
		UserID: userID,
	})
}

func (s *Store) ListOrgMembers(ctx context.Context, tx *sql.Tx, orgID int64) ([]*model.OrgMember, error) {
	rows, err := s.q(tx).ListOrgMembers(ctx, orgID)
	if err != nil {
		return nil, err
	}
	members := make([]*model.OrgMember, len(rows))
	for i, row := range rows {
		members[i] = toModelOrgMember(row)
	}
	return members, nil
}

func (s *Store) ListOrgMembersWithUsers(ctx context.Context, tx *sql.Tx, orgID int64) ([]*model.OrgMemberWithUser, error) {
	rows, err := s.q(tx).ListOrgMembersWithUsers(ctx, orgID)
	if err != nil {
		return nil, err
	}
	members := make([]*model.OrgMemberWithUser, len(rows))
	for i, row := range rows {
		members[i] = &model.OrgMemberWithUser{
			OrgMember: model.OrgMember{
				OrgID:     row.OrgID,
				UserID:    row.UserID,
				Role:      row.Role,
				CreatedAt: row.CreatedAt,
			},
			Email: row.Email,
			Name:  row.Name,
		}
	}
	return members, nil
}

func (s *Store) IsOrgMember(ctx context.Context, tx *sql.Tx, orgID int64, userID string) (bool, error) {
	return s.q(tx).IsOrgMember(ctx, sqlc.IsOrgMemberParams{
		OrgID:  orgID,
		UserID: userID,
	})
}

// ResolveOrgOwner verifies that the user is a member of the given org and
// returns the org ID. Returns an error if the user is not a member.
func (s *Store) ResolveOrgOwner(ctx context.Context, tx *sql.Tx, orgID int64, userID string) (int64, error) {
	ok, err := s.IsOrgMember(ctx, tx, orgID, userID)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("user %s is not a member of org %d", userID, orgID)
	}
	return orgID, nil
}

func (s *Store) GetOrgAtlassianWebhookSecret(ctx context.Context, tx *sql.Tx, orgID int64) (string, error) {
	return s.q(tx).GetOrgAtlassianWebhookSecret(ctx, orgID)
}

func (s *Store) SetOrgAtlassianWebhookSecret(ctx context.Context, tx *sql.Tx, orgID int64, secret string) error {
	return s.q(tx).SetOrgAtlassianWebhookSecret(ctx, sqlc.SetOrgAtlassianWebhookSecretParams{
		ID:                     orgID,
		AtlassianWebhookSecret: secret,
	})
}

func (s *Store) SetOrgGitHubInstallation(ctx context.Context, tx *sql.Tx, orgID, installationID int64) error {
	return s.q(tx).SetOrgGitHubInstallation(ctx, sqlc.SetOrgGitHubInstallationParams{
		ID:                   orgID,
		GithubInstallationID: installationID,
	})
}

func (s *Store) ClearGitHubInstallation(ctx context.Context, tx *sql.Tx, installationID int64) error {
	return s.q(tx).ClearGitHubInstallation(ctx, installationID)
}

// ListOrgIDsByGitHubInstallation returns the ids of the non-archived orgs that
// share the given GitHub App installation. It resolves a webhook's installation
// to the orgs an event belongs to, independent of the actor's membership.
func (s *Store) ListOrgIDsByGitHubInstallation(ctx context.Context, tx *sql.Tx, installationID int64) ([]int64, error) {
	return s.q(tx).ListOrgIDsByGitHubInstallation(ctx, installationID)
}

func toModelOrgMember(row sqlc.OrgMember) *model.OrgMember {
	return &model.OrgMember{
		OrgID:     row.OrgID,
		UserID:    row.UserID,
		Role:      row.Role,
		CreatedAt: row.CreatedAt,
	}
}

// DecodeRoutingRules decodes stored routing_rules JSONB into conditions-native
// model.RoutingRule values. Storage is conditions-native, so this is a plain
// unmarshal; empty/absent data decodes to a nil slice.
func DecodeRoutingRules(data []byte) ([]model.RoutingRule, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var rules []model.RoutingRule
	if err := json.Unmarshal(data, &rules); err != nil {
		return nil, err
	}
	return rules, nil
}

func (s *Store) GetOrgRoutingRules(ctx context.Context, tx *sql.Tx, orgID int64) ([]model.RoutingRule, error) {
	data, err := s.q(tx).GetOrgRoutingRules(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return DecodeRoutingRules(data)
}

func (s *Store) SetOrgRoutingRules(ctx context.Context, tx *sql.Tx, orgID int64, rules []model.RoutingRule) error {
	data, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	return s.q(tx).SetOrgRoutingRules(ctx, sqlc.SetOrgRoutingRulesParams{
		ID:           orgID,
		RoutingRules: data,
	})
}

func (s *Store) GetRoutingRulesByOrgs(ctx context.Context, tx *sql.Tx, orgIDs []int64) (map[int64][]model.RoutingRule, error) {
	rows, err := s.q(tx).GetRoutingRulesByOrgs(ctx, orgIDs)
	if err != nil {
		return nil, err
	}
	result := make(map[int64][]model.RoutingRule, len(rows))
	for _, row := range rows {
		rules, err := DecodeRoutingRules(row.RoutingRules)
		if err != nil {
			return nil, fmt.Errorf("org %d: %w", row.ID, err)
		}
		result[row.ID] = rules
	}
	return result, nil
}

// OrgRoutingRules pairs an org's routing rules with whether the routing actor
// is a member of it. Non-member orgs (IsMember == false) contribute only their
// public rules; the caller decides eligibility from the rule flag.
type OrgRoutingRules struct {
	OrgID    int64
	IsMember bool
	Rules    []model.RoutingRule
}

// ListRoutingRulesForEvent returns routing rules for the event's orgs, tagging
// each with whether the actor is a member. It unions two sources: every org the
// user is a member of (IsMember == true, all rules), and the passed-in orgs the
// user is NOT a member of (IsMember == false). Membership wins on overlap — an
// org the user belongs to is returned once, from the member branch. Member orgs
// without configured rules are included with a nil/empty slice so callers can
// apply a fallback. An empty orgs slice reduces this to the member-only set.
func (s *Store) ListRoutingRulesForEvent(ctx context.Context, tx *sql.Tx, userID string, orgs []int64) ([]OrgRoutingRules, error) {
	rows, err := s.q(tx).ListRoutingRulesForEvent(ctx, sqlc.ListRoutingRulesForEventParams{
		UserID: userID,
		OrgIds: orgs,
	})
	if err != nil {
		return nil, err
	}
	result := make([]OrgRoutingRules, 0, len(rows))
	for _, row := range rows {
		rules, err := DecodeRoutingRules(row.RoutingRules)
		if err != nil {
			return nil, fmt.Errorf("org %d: %w", row.ID, err)
		}
		result = append(result, OrgRoutingRules{
			OrgID:    row.ID,
			IsMember: row.IsMember,
			Rules:    rules,
		})
	}
	return result, nil
}
