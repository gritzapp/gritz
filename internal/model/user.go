package model

import (
	"time"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// User represents a registered user, provisioned on first login.
type User struct {
	ID                 string    `json:"id"`
	Email              string    `json:"email"`
	Name               string    `json:"name"`
	GitHubUserID       int64     `json:"github_user_id"`
	GitHubUsername     string    `json:"github_username"`
	AtlassianAccountID string    `json:"atlassian_account_id"`
	AtlassianUsername  string    `json:"atlassian_username"`
	DefaultOrgID       int64     `json:"default_org_id"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// HasGitHub returns true if the user has a linked GitHub account.
func (u *User) HasGitHub() bool {
	return u.GitHubUserID != 0
}

// GitHubAccountProto converts the user's GitHub info to the protobuf representation.
func (u *User) GitHubAccountProto() *gritzv1.GitHubAccount {
	if !u.HasGitHub() {
		return nil
	}
	return &gritzv1.GitHubAccount{
		GithubUserId:   u.GitHubUserID,
		GithubUsername: u.GitHubUsername,
		CreatedAt:      timestamppb.New(u.CreatedAt),
	}
}

// HasAtlassian returns true if the user has a linked Atlassian account.
func (u *User) HasAtlassian() bool {
	return u.AtlassianAccountID != ""
}

// AtlassianAccountProto converts the user's Atlassian info to the protobuf representation.
func (u *User) AtlassianAccountProto() *gritzv1.AtlassianAccount {
	if !u.HasAtlassian() {
		return nil
	}
	return &gritzv1.AtlassianAccount{
		AtlassianAccountId: u.AtlassianAccountID,
		AtlassianUsername:  u.AtlassianUsername,
		CreatedAt:          timestamppb.New(u.CreatedAt),
	}
}
