package store_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
)

func TestCreateKeyScopesRoundTrip(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	readOwn := authscope.New(authscope.OpTaskRead, authscope.WithTaskID(42))
	key := &model.Key{
		ID:        uuid.NewString(),
		Name:      "scoped-key",
		TokenHash: uuid.NewString(),
		OrgID:     org.OrgID,
		Scopes:    authscope.Scopes{readOwn},
	}
	err := s.CreateKey(t.Context(), nil, key)
	assert.NilError(t, err)

	// Act
	got, err := s.GetKeyByHash(t.Context(), nil, key.TokenHash)

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, got.Scopes, authscope.Scopes{readOwn})
}

func TestCreateKeyNilScopesIsNull(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	key := &model.Key{
		ID:        uuid.NewString(),
		Name:      "no-scopes",
		TokenHash: uuid.NewString(),
		OrgID:     org.OrgID,
		// Scopes left nil ⇒ stored as SQL NULL ⇒ read back as nil.
	}
	err := s.CreateKey(t.Context(), nil, key)
	assert.NilError(t, err)

	// Act
	got, err := s.GetKeyByHash(t.Context(), nil, key.TokenHash)

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, got.Scopes == nil)
}
