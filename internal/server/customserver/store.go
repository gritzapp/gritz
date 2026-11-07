//go:generate go tool moq -out store_moq_test.go . Store

package customserver

import (
	"context"
	"database/sql"

	"github.com/gritzapp/gritz/internal/model"
)

// Store is the subset of store.Store used by the custom event webhook handler.
type Store interface {
	GetOrgCustomEventTypes(ctx context.Context, tx *sql.Tx, orgID int64) ([]model.EventTypeDef, error)
}
