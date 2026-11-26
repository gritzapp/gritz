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

// CountOrgMembershipsByUser returns the number of non-archived orgs the user
// is a member of: the same set ListOrgsByMember returns.
func (s *Store) CountOrgMembershipsByUser(ctx context.Context, tx *sql.Tx, userID string) (int64, error) {
	return s.q(tx).CountOrgMembershipsByUser(ctx, userID)
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

// LockOrg takes a row lock on the org until tx ends. It serializes changes to
// the org's custom event types.
func (s *Store) LockOrg(ctx context.Context, tx *sql.Tx, orgID int64) error {
	if tx == nil {
		panic("store: LockOrg requires a transaction")
	}
	return s.q(tx).LockOrg(ctx, orgID)
}

func (s *Store) GetOrgCustomEventTypes(ctx context.Context, tx *sql.Tx, orgID int64) ([]model.EventTypeDef, error) {
	data, err := s.q(tx).GetOrgCustomEventTypes(ctx, orgID)
	if err != nil {
		return nil, err
	}
	var defs []model.EventTypeDef
	if err := json.Unmarshal(data, &defs); err != nil {
		return nil, err
	}
	return defs, nil
}

func (s *Store) SetOrgCustomEventTypes(ctx context.Context, tx *sql.Tx, orgID int64, defs []model.EventTypeDef) error {
	data, err := json.Marshal(defs)
	if err != nil {
		return err
	}
	return s.q(tx).SetOrgCustomEventTypes(ctx, sqlc.SetOrgCustomEventTypesParams{
		ID:               orgID,
		CustomEventTypes: data,
	})
}

// EventOrg is an org an event routes in, and whether the routing actor is a
// member of it. A non-member org (IsMember == false) admits only public
// triggers and link kinds.
type EventOrg struct {
	OrgID    int64
	IsMember bool
}

// ListEventOrgs returns the orgs an event of the actor userID routes in,
// ordered by ID. It unions two sources: every org the user is a member of
// (IsMember == true), and the passed-in orgs the user is NOT a member of
// (IsMember == false). Membership wins on overlap — an org the user belongs to
// is returned once, from the member branch. An empty orgs slice reduces this
// to the member orgs.
func (s *Store) ListEventOrgs(ctx context.Context, tx *sql.Tx, userID string, orgs []int64) ([]EventOrg, error) {
	rows, err := s.q(tx).ListEventOrgs(ctx, sqlc.ListEventOrgsParams{
		UserID: userID,
		OrgIds: orgs,
	})
	if err != nil {
		return nil, err
	}
	var result []EventOrg
	for _, row := range rows {
		result = append(result, EventOrg{OrgID: row.ID, IsMember: row.IsMember})
	}
	return result, nil
}
