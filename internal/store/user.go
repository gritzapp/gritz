package store

import (
	"context"
	"database/sql"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store/sqlc"
)

func (s *Store) UpsertUser(ctx context.Context, tx *sql.Tx, user *model.User) error {
	row, err := s.q(tx).UpsertUser(ctx, sqlc.UpsertUserParams{
		ID:    user.ID,
		Email: user.Email,
		Name:  user.Name,
	})
	if err != nil {
		return err
	}
	user.CreatedAt = row.CreatedAt
	user.UpdatedAt = row.UpdatedAt
	user.DefaultOrgID = row.DefaultOrgID.Int64
	return nil
}

func (s *Store) CreateUser(ctx context.Context, tx *sql.Tx, user *model.User) error {
	row, err := s.q(tx).CreateUser(ctx, sqlc.CreateUserParams{
		ID:    user.ID,
		Email: user.Email,
		Name:  user.Name,
		GithubUserID: sql.NullInt64{
			Int64: user.GitHubUserID,
			Valid: user.GitHubUserID != 0,
		},
		GithubUsername: sql.NullString{
			String: user.GitHubUsername,
			Valid:  user.GitHubUsername != "",
		},
		DefaultOrgID: sql.NullInt64{
			Int64: user.DefaultOrgID,
			Valid: user.DefaultOrgID != 0,
		},
	})
	if err != nil {
		return err
	}
	user.CreatedAt = row.CreatedAt
	user.UpdatedAt = row.UpdatedAt
	user.DefaultOrgID = row.DefaultOrgID.Int64
	return nil
}

func (s *Store) GetUser(ctx context.Context, tx *sql.Tx, id string) (*model.User, error) {
	row, err := s.q(tx).GetUser(ctx, id)
	if err != nil {
		return nil, err
	}
	return &model.User{
		ID:                 row.ID,
		Email:              row.Email,
		Name:               row.Name,
		GitHubUserID:       row.GithubUserID.Int64,
		GitHubUsername:     row.GithubUsername.String,
		AtlassianAccountID: row.AtlassianAccountID.String,
		AtlassianUsername:  row.AtlassianUsername,
		DefaultOrgID:       row.DefaultOrgID.Int64,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}, nil
}

func (s *Store) GetUserByEmail(ctx context.Context, tx *sql.Tx, email string) (*model.User, error) {
	row, err := s.q(tx).GetUserByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	return &model.User{
		ID:                 row.ID,
		Email:              row.Email,
		Name:               row.Name,
		GitHubUserID:       row.GithubUserID.Int64,
		GitHubUsername:     row.GithubUsername.String,
		AtlassianAccountID: row.AtlassianAccountID.String,
		AtlassianUsername:  row.AtlassianUsername,
		DefaultOrgID:       row.DefaultOrgID.Int64,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}, nil
}

func (s *Store) GetUserByGitHubUserID(ctx context.Context, tx *sql.Tx, githubUserID int64) (*model.User, error) {
	row, err := s.q(tx).GetUserByGitHubUserID(ctx, sql.NullInt64{Int64: githubUserID, Valid: true})
	if err != nil {
		return nil, err
	}
	return &model.User{
		ID:                 row.ID,
		Email:              row.Email,
		Name:               row.Name,
		GitHubUserID:       row.GithubUserID.Int64,
		GitHubUsername:     row.GithubUsername.String,
		AtlassianAccountID: row.AtlassianAccountID.String,
		AtlassianUsername:  row.AtlassianUsername,
		DefaultOrgID:       row.DefaultOrgID.Int64,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}, nil
}

func (s *Store) GetUserByAtlassianAccountID(ctx context.Context, tx *sql.Tx, atlassianAccountID string) (*model.User, error) {
	row, err := s.q(tx).GetUserByAtlassianAccountID(ctx, sql.NullString{String: atlassianAccountID, Valid: true})
	if err != nil {
		return nil, err
	}
	return &model.User{
		ID:                 row.ID,
		Email:              row.Email,
		Name:               row.Name,
		GitHubUserID:       row.GithubUserID.Int64,
		GitHubUsername:     row.GithubUsername.String,
		AtlassianAccountID: row.AtlassianAccountID.String,
		AtlassianUsername:  row.AtlassianUsername,
		DefaultOrgID:       row.DefaultOrgID.Int64,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}, nil
}

func (s *Store) LinkGitHubAccount(ctx context.Context, tx *sql.Tx, userID string, githubUserID int64, githubUsername string) error {
	return s.q(tx).LinkGitHubAccount(ctx, sqlc.LinkGitHubAccountParams{
		ID:             userID,
		GithubUserID:   sql.NullInt64{Int64: githubUserID, Valid: true},
		GithubUsername: sql.NullString{String: githubUsername, Valid: true},
	})
}

func (s *Store) UnlinkGitHubAccount(ctx context.Context, tx *sql.Tx, userID string) error {
	return s.q(tx).UnlinkGitHubAccount(ctx, userID)
}

func (s *Store) UpdateGitHubUsername(ctx context.Context, tx *sql.Tx, githubUserID int64, username string) error {
	return s.q(tx).UpdateGitHubUsername(ctx, sqlc.UpdateGitHubUsernameParams{
		GithubUsername: sql.NullString{String: username, Valid: true},
		GithubUserID:   sql.NullInt64{Int64: githubUserID, Valid: true},
	})
}

func (s *Store) UpdateDefaultOrgID(ctx context.Context, tx *sql.Tx, userID string, orgID int64) error {
	return s.q(tx).UpdateDefaultOrgID(ctx, sqlc.UpdateDefaultOrgIDParams{
		ID:           userID,
		DefaultOrgID: sql.NullInt64{Int64: orgID, Valid: orgID != 0},
	})
}

func (s *Store) LinkAtlassianAccount(ctx context.Context, tx *sql.Tx, userID string, atlassianAccountID, atlassianUsername string) error {
	return s.q(tx).LinkAtlassianAccount(ctx, sqlc.LinkAtlassianAccountParams{
		ID:                 userID,
		AtlassianAccountID: sql.NullString{String: atlassianAccountID, Valid: true},
		AtlassianUsername:  atlassianUsername,
	})
}

func (s *Store) UnlinkAtlassianAccount(ctx context.Context, tx *sql.Tx, userID string) error {
	return s.q(tx).UnlinkAtlassianAccount(ctx, userID)
}
