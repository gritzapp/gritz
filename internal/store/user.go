package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"

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

// ErrAmbiguousEmail is returned by GetUserByEmail when the email matches
// several users that differ only in case, none of them exactly.
var ErrAmbiguousEmail = errors.New("email matches several users that differ only in case")

// GetUserByEmail returns the user with the given email, ignoring case. When
// several users match, the one whose email matches exactly wins; if none does,
// it returns ErrAmbiguousEmail. It returns sql.ErrNoRows when none matches.
func (s *Store) GetUserByEmail(ctx context.Context, tx *sql.Tx, email string) (*model.User, error) {
	rows, err := s.q(tx).GetUsersByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	var row sqlc.User
	switch len(rows) {
	case 0:
		return nil, sql.ErrNoRows
	case 1:
		row = rows[0]
	default:
		i := slices.IndexFunc(rows, func(r sqlc.User) bool { return r.Email == email })
		if i < 0 {
			return nil, ErrAmbiguousEmail
		}
		row = rows[i]
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
	row, err := s.q(tx).GetUserByAtlassianAccountID(ctx, sql.NullString{
		String: atlassianAccountID,
		Valid:  true,
	})
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
