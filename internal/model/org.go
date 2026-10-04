package model

import (
	"time"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Org represents an organisation that owns resources.
type Org struct {
	ID                      int64     `json:"id"`
	Name                    string    `json:"name"`
	Owner                   string    `json:"owner"`
	Archived                bool      `json:"archived"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
	GitHubInstallationID    int64     `json:"github_installation_id"`
	AtlassianWebhookSecret  string    `json:"atlassian_webhook_secret"`
}

// Proto converts an Org to its protobuf representation.
func (o *Org) Proto() *gritzv1.Org {
	return &gritzv1.Org{
		Id:        o.ID,
		Name:      o.Name,
		Owner:     o.Owner,
		CreatedAt: timestamppb.New(o.CreatedAt),
		UpdatedAt: timestamppb.New(o.UpdatedAt),
	}
}

// OrgMember represents a user's membership in an organisation.
type OrgMember struct {
	OrgID     int64     `json:"org_id"`
	UserID    string    `json:"user_id"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// OrgMemberWithUser is an OrgMember with user profile info for display.
type OrgMemberWithUser struct {
	OrgMember
	Email string `json:"email"`
	Name  string `json:"name"`
}

// Proto converts an OrgMemberWithUser to its protobuf representation.
func (m *OrgMemberWithUser) Proto() *gritzv1.OrgMember {
	return &gritzv1.OrgMember{
		OrgId:     m.OrgID,
		UserId:    m.UserID,
		Email:     m.Email,
		Name:      m.Name,
		Role:      m.Role,
		CreatedAt: timestamppb.New(m.CreatedAt),
	}
}
