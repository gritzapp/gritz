package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store/sqlc"
)

func (s *Store) CreateKey(ctx context.Context, tx *sql.Tx, key *model.Key) error {
	now := time.Now().UTC()
	var expiresAt sql.NullTime
	if key.ExpiresAt != nil {
		expiresAt = sql.NullTime{Time: *key.ExpiresAt, Valid: true}
	}
	err := s.q(tx).CreateKey(ctx, sqlc.CreateKeyParams{
		ID:        key.ID,
		Name:      key.Name,
		TokenHash: key.TokenHash,
		OrgID:     key.OrgID,
		ExpiresAt: expiresAt,
		CreatedAt: now,
		Scopes:    scopeStrings(key.Scopes),
	})
	if err != nil {
		return err
	}
	key.CreatedAt = now
	return nil
}

func (s *Store) GetKey(ctx context.Context, tx *sql.Tx, id string, orgID int64) (*model.Key, error) {
	row, err := s.q(tx).GetKey(ctx, sqlc.GetKeyParams{
		ID:    id,
		OrgID: orgID,
	})
	if err != nil {
		return nil, err
	}
	return toModelKey(row), nil
}

func (s *Store) GetKeyByHash(ctx context.Context, tx *sql.Tx, hash string) (*model.Key, error) {
	row, err := s.q(tx).GetKeyByHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	return toModelKey(row), nil
}

func (s *Store) ListKeys(ctx context.Context, tx *sql.Tx, orgID int64) ([]*model.Key, error) {
	rows, err := s.q(tx).ListKeys(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return toModelKeys(rows), nil
}

func (s *Store) DeleteKey(ctx context.Context, tx *sql.Tx, id string, orgID int64) error {
	return s.q(tx).DeleteKey(ctx, sqlc.DeleteKeyParams{
		ID:    id,
		OrgID: orgID,
	})
}

func toModelKey(row sqlc.Key) *model.Key {
	key := &model.Key{
		ID:        row.ID,
		Name:      row.Name,
		TokenHash: row.TokenHash,
		OrgID:     row.OrgID,
		CreatedAt: row.CreatedAt,
		Scopes:    parseScopes(row.Scopes),
	}
	if row.ExpiresAt.Valid {
		key.ExpiresAt = &row.ExpiresAt.Time
	}
	return key
}

// scopeStrings flattens a scope set into the wire-grammar strings stored in the
// scopes text[] column. A nil/empty set becomes a nil slice, stored as SQL NULL
// so the column round-trips back to a nil Scopes.
func scopeStrings(scopes authscope.Scopes) []string {
	if len(scopes) == 0 {
		return nil
	}
	strs := make([]string, len(scopes))
	for i, s := range scopes {
		strs[i] = s.String()
	}
	return strs
}

// parseScopes parses the scopes column back into a scope set. A NULL/empty column
// yields a nil Scopes; callers apply their own default (see StoreKeyValidator). A
// malformed entry is dropped to nil rather than failing the read.
func parseScopes(strs []string) authscope.Scopes {
	if len(strs) == 0 {
		return nil
	}
	scopes, err := authscope.ParseScopes(strs)
	if err != nil {
		return nil
	}
	return scopes
}

func toModelKeys(rows []sqlc.Key) []*model.Key {
	keys := make([]*model.Key, len(rows))
	for i, row := range rows {
		keys[i] = toModelKey(row)
	}
	return keys
}
