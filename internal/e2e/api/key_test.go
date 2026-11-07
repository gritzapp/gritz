package api_test

import (
	"slices"
	"testing"

	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
)

func TestCreateKey(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	resp, err := h.Org.Client.CreateKey(t.Context(), &gritzv1.CreateKeyRequest{
		Name: "test-key",
	})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t,
		resp.Key,
		&gritzv1.Key{Name: "test-key"},
		protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.Key{}, "id", "created_at"),
	)
	assert.Assert(t, resp.Key.Id != "")
	assert.Assert(t, resp.RawToken != "")
	assert.Assert(t, apiauth.IsKey(resp.RawToken))
}

func TestCreateKeyStoresAdminScopes(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	resp, err := h.Org.Client.CreateKey(t.Context(), &gritzv1.CreateKeyRequest{
		Name: "admin-key",
	})

	// Assert - new keys carry the admin wildcard (*.*); there is no way to
	// choose narrower scopes yet. No RPC exposes a key's scopes, so read them
	// from the store.
	assert.NilError(t, err)
	key, err := h.Server.Store.GetKey(t.Context(), nil, resp.Key.Id, h.Org.ID)
	assert.NilError(t, err)
	assert.DeepEqual(t, key.Scopes, authscope.Admin())
}

func TestCreateAndListKeys(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	h.Org.CreateKey(&gritzv1.CreateKeyRequest{Name: "key-1"})
	h.Org.CreateKey(&gritzv1.CreateKeyRequest{Name: "key-2"})

	// Act
	resp, err := h.Org.Client.ListKeys(t.Context(), &gritzv1.ListKeysRequest{})

	// Assert - the org also holds the harness's runner key, so look for the
	// created keys by name.
	assert.NilError(t, err)
	var names []string
	for _, k := range resp.Keys {
		names = append(names, k.Name)
	}
	assert.Assert(t, slices.Contains(names, "key-1"), "keys: %v", names)
	assert.Assert(t, slices.Contains(names, "key-2"), "keys: %v", names)
}

func TestDeleteKey(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	key := h.Org.CreateKey(&gritzv1.CreateKeyRequest{Name: "to-delete"})

	// Act
	_, err := h.Org.Client.DeleteKey(t.Context(), &gritzv1.DeleteKeyRequest{
		Id: key.ID,
	})

	// Assert
	assert.NilError(t, err)
	for _, k := range h.Org.ListKeys(&gritzv1.ListKeysRequest{}) {
		assert.Assert(t, k.Id != key.ID, "deleted key %s is still listed", key.ID)
	}
}

func TestListKeys_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	orgA := h.Org
	orgB := h.Server.CreateOrg()
	orgA.CreateKey(&gritzv1.CreateKeyRequest{Name: "user-a-key"})
	orgB.CreateKey(&gritzv1.CreateKeyRequest{Name: "user-b-key"})

	// Act
	respA, err := orgA.Client.ListKeys(t.Context(), &gritzv1.ListKeysRequest{})
	assert.NilError(t, err)
	respB, err := orgB.Client.ListKeys(t.Context(), &gritzv1.ListKeysRequest{})
	assert.NilError(t, err)

	// Assert - each org only sees its own keys
	var namesA, namesB []string
	for _, k := range respA.Keys {
		namesA = append(namesA, k.Name)
	}
	for _, k := range respB.Keys {
		namesB = append(namesB, k.Name)
	}
	assert.Assert(t, slices.Contains(namesA, "user-a-key"), "org A keys: %v", namesA)
	assert.Assert(t, !slices.Contains(namesA, "user-b-key"), "org A keys: %v", namesA)
	assert.Assert(t, slices.Contains(namesB, "user-b-key"), "org B keys: %v", namesB)
	assert.Assert(t, !slices.Contains(namesB, "user-a-key"), "org B keys: %v", namesB)
}

func TestDeleteKey_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	orgB := h.Server.CreateOrg()
	key := h.Org.CreateKey(&gritzv1.CreateKeyRequest{Name: "user-a-key"})

	// Act - org B tries to delete org A's key
	_, err := orgB.Client.DeleteKey(t.Context(), &gritzv1.DeleteKeyRequest{
		Id: key.ID,
	})

	// Assert - delete doesn't error (SQL DELETE with no rows is not an error)
	// but the key should still exist for org A
	assert.NilError(t, err)
	var ids []string
	for _, k := range h.Org.ListKeys(&gritzv1.ListKeysRequest{}) {
		ids = append(ids, k.Id)
	}
	assert.Assert(t, slices.Contains(ids, key.ID), "org A keys: %v", ids)
}
