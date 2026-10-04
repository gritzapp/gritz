package apiserver

import (
	"testing"

	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
)

func TestCreateKey(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)

	// Act
	resp, err := srv.CreateKey(ctx, &gritzv1.CreateKeyRequest{
		Name: "test-key",
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, resp.Key.Name, "test-key")
	assert.Assert(t, resp.Key.Id != "")
	assert.Assert(t, resp.RawToken != "")
	assert.Assert(t, apiauth.IsKey(resp.RawToken))
}

func TestCreateKeyStoresAdminScopes(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)

	// Act
	resp, err := srv.CreateKey(ctx, &gritzv1.CreateKeyRequest{
		Name: "admin-key",
	})
	assert.NilError(t, err)

	// Assert - new keys carry the admin wildcard (*.*); there is no way to
	// choose narrower scopes yet.
	key, err := srv.store.GetKey(ctx, nil, resp.Key.Id, org.OrgID)
	assert.NilError(t, err)
	assert.DeepEqual(t, key.Scopes, authscope.Admin())
}

func TestCreateAndListKeys(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)
	_, err := srv.CreateKey(ctx, &gritzv1.CreateKeyRequest{
		Name: "key-1",
	})
	assert.NilError(t, err)
	_, err = srv.CreateKey(ctx, &gritzv1.CreateKeyRequest{
		Name: "key-2",
	})
	assert.NilError(t, err)

	// Act
	resp, err := srv.ListKeys(ctx, &gritzv1.ListKeysRequest{})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, len(resp.Keys), 2)
}

func TestDeleteKey(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)
	createResp, err := srv.CreateKey(ctx, &gritzv1.CreateKeyRequest{
		Name: "to-delete",
	})
	assert.NilError(t, err)

	// Act
	_, err = srv.DeleteKey(ctx, &gritzv1.DeleteKeyRequest{
		Id: createResp.Key.Id,
	})
	assert.NilError(t, err)

	// Assert
	listResp, err := srv.ListKeys(ctx, &gritzv1.ListKeysRequest{})
	assert.NilError(t, err)
	assert.Equal(t, len(listResp.Keys), 0)
}

func TestListKeys_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	orgA := teststore.CreateOrg(t, srv.store, nil)
	ctxA := createCtx(t, orgA)
	orgB := teststore.CreateOrg(t, srv.store, nil)
	ctxB := createCtx(t, orgB)
	_, err := srv.CreateKey(ctxA, &gritzv1.CreateKeyRequest{
		Name: "user-a-key",
	})
	assert.NilError(t, err)
	_, err = srv.CreateKey(ctxB, &gritzv1.CreateKeyRequest{
		Name: "user-b-key",
	})
	assert.NilError(t, err)

	// Act
	respA, err := srv.ListKeys(ctxA, &gritzv1.ListKeysRequest{})
	assert.NilError(t, err)
	respB, err := srv.ListKeys(ctxB, &gritzv1.ListKeysRequest{})
	assert.NilError(t, err)

	// Assert - each user only sees their own keys
	assert.Equal(t, len(respA.Keys), 1)
	assert.Equal(t, respA.Keys[0].Name, "user-a-key")
	assert.Equal(t, len(respB.Keys), 1)
	assert.Equal(t, respB.Keys[0].Name, "user-b-key")
}

func TestDeleteKey_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	orgA := teststore.CreateOrg(t, srv.store, nil)
	ctxA := createCtx(t, orgA)
	orgB := teststore.CreateOrg(t, srv.store, nil)
	ctxB := createCtx(t, orgB)
	createResp, err := srv.CreateKey(ctxA, &gritzv1.CreateKeyRequest{
		Name: "user-a-key",
	})
	assert.NilError(t, err)

	// Act - User B tries to delete User A's key
	_, err = srv.DeleteKey(ctxB, &gritzv1.DeleteKeyRequest{
		Id: createResp.Key.Id,
	})

	// Assert - delete doesn't error (SQL DELETE with no rows is not an error)
	// but the key should still exist for User A
	assert.NilError(t, err)
	listResp, err := srv.ListKeys(ctxA, &gritzv1.ListKeysRequest{})
	assert.NilError(t, err)
	assert.Equal(t, len(listResp.Keys), 1)
}
