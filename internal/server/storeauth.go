package server

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store"
)

// StoreKeyValidator implements apiauth.KeyValidator using the store.
type StoreKeyValidator struct {
	store *store.Store
}

// NewStoreKeyValidator returns a KeyValidator backed by the given store.
func NewStoreKeyValidator(s *store.Store) *StoreKeyValidator {
	return &StoreKeyValidator{store: s}
}

func (v *StoreKeyValidator) ValidateKey(ctx context.Context, keyHash string) (*apiauth.UserInfo, error) {
	key, err := v.store.GetKeyByHash(ctx, nil, keyHash)
	if err != nil {
		return nil, err
	}
	if key.IsExpired() {
		return nil, fmt.Errorf("key expired")
	}
	// Scopes are populated from the keys.scopes column, which the migration
	// defaults to the admin wildcard for every existing and future row, so no
	// runtime default is needed here.
	return &apiauth.UserInfo{
		OrgID:  key.OrgID,
		Name:   key.Name,
		Type:   apiauth.AuthTypeKey,
		Scopes: key.Scopes,
	}, nil
}

// StoreUserResolver implements apiauth.UserResolver using the store.
type StoreUserResolver struct {
	store *store.Store
}

// NewStoreUserResolver returns a UserResolver backed by the given store.
func NewStoreUserResolver(s *store.Store) *StoreUserResolver {
	return &StoreUserResolver{store: s}
}

func (r *StoreUserResolver) Provision(ctx context.Context, user *apiauth.UserInfo) (*model.User, error) {
	u := &model.User{
		ID:    user.ID,
		Email: user.Email,
		Name:  user.Name,
	}
	err := r.store.WithTx(ctx, nil, func(tx *sql.Tx) error {
		if err := r.store.UpsertUser(ctx, tx, u); err != nil {
			return err
		}
		// If the user has no default org, create one
		if u.DefaultOrgID == 0 {
			org := &model.Org{
				Name:  user.Name + "'s Org",
				Owner: user.ID,
			}
			if err := r.store.CreateOrg(ctx, tx, org); err != nil {
				return err
			}
			if err := r.store.AddOrgMember(ctx, tx, &model.OrgMember{
				OrgID:  org.ID,
				UserID: user.ID,
				Role:   "owner",
			}); err != nil {
				return err
			}
			if err := r.store.UpdateDefaultOrgID(ctx, tx, user.ID, org.ID); err != nil {
				return err
			}
			// Reflect the freshly created org on the returned user so it is
			// internally consistent and callers don't have to re-resolve it.
			u.DefaultOrgID = org.ID
		}
		return tx.Commit()
	})
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (r *StoreUserResolver) ResolveOrg(ctx context.Context, userID string, orgID int64) (int64, error) {
	// Fall back to the user's default org if none requested
	if orgID == 0 {
		u, err := r.store.GetUser(ctx, nil, userID)
		if err != nil {
			return 0, err
		}
		if u.DefaultOrgID == 0 {
			return 0, fmt.Errorf("user %s has no default org", userID)
		}
		orgID = u.DefaultOrgID
	}
	// Validate membership
	ok, err := r.store.IsOrgMember(ctx, nil, orgID, userID)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("user %s is not a member of org %d", userID, orgID)
	}
	return orgID, nil
}
