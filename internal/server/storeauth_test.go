package server_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/server"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
)

func TestValidateKeyReturnsScopes(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	readOwn := authscope.New(authscope.OpTaskRead, authscope.WithTaskID(7))
	hash := uuid.NewString()
	err := s.CreateKey(t.Context(), nil, &model.Key{
		ID:        uuid.NewString(),
		Name:      "scoped",
		TokenHash: hash,
		OrgID:     org.OrgID,
		Scopes:    authscope.Scopes{readOwn},
	})
	assert.NilError(t, err)

	// Act
	info, err := server.NewStoreKeyValidator(s).ValidateKey(t.Context(), hash)

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, info.Scopes, authscope.Scopes{readOwn})
}

func TestProvisionReturnsUserWithDefaultOrg(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	userID := uuid.NewString()

	// Act
	user, err := server.NewStoreUserResolver(s).Provision(t.Context(), &apiauth.UserInfo{
		ID:    userID,
		Email: userID + "@test.com",
		Name:  "Test User",
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, user.ID, userID)
	// The returned user carries the freshly created default org, so callers
	// don't have to re-resolve it.
	assert.Assert(t, user.DefaultOrgID != 0)
	t.Cleanup(func() {
		if err := s.DestroyOrg(context.Background(), nil, user.DefaultOrgID); err != nil {
			t.Logf("DestroyOrg cleanup: %v", err)
		}
	})

	// The org was persisted and the user is its owner-member.
	stored, err := s.GetUser(t.Context(), nil, userID)
	assert.NilError(t, err)
	assert.Equal(t, stored.DefaultOrgID, user.DefaultOrgID)
	member, err := s.IsOrgMember(t.Context(), nil, user.DefaultOrgID, userID)
	assert.NilError(t, err)
	assert.Assert(t, member)
}
