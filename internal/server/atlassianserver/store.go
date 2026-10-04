//go:generate go tool moq -out store_moq_test.go . Store

package atlassianserver

import (
	"context"
	"database/sql"

	"github.com/gritzapp/gritz/internal/model"
)

// Store is the subset of store.Store used by the Atlassian webhook handler.
type Store interface {
	GetOrgAtlassianWebhookSecret(ctx context.Context, tx *sql.Tx, orgID int64) (string, error)
	GetUserByAtlassianAccountID(ctx context.Context, tx *sql.Tx, atlassianAccountID string) (*model.User, error)
}
