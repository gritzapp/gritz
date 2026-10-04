//go:generate go tool moq -out store_moq_test.go . Store

package githubserver

import (
	"context"
	"database/sql"

	"github.com/gritzapp/gritz/internal/model"
)

// Store is the subset of store.Store used by the GitHub webhook handler.
type Store interface {
	GetUserByGitHubUserID(ctx context.Context, tx *sql.Tx, githubUserID int64) (*model.User, error)
	UpdateGitHubUsername(ctx context.Context, tx *sql.Tx, githubUserID int64, username string) error
	ClearGitHubInstallation(ctx context.Context, tx *sql.Tx, installationID int64) error
	ListOrgIDsByGitHubInstallation(ctx context.Context, tx *sql.Tx, installationID int64) ([]int64, error)
}
