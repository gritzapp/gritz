package apiauth

import (
	"testing"

	"github.com/gritzapp/gritz/internal/auth/authscope"
	"gotest.tools/v3/assert"
)

func TestAppClaimsScopesRoundTrip(t *testing.T) {
	t.Parallel()
	// Arrange: mint admin claims for a user.
	key, err := CreateAppPrivateKey()
	assert.NilError(t, err)
	claims := NewAppClaims(&UserInfo{ID: "u1", OrgID: 7})
	assert.DeepEqual(t, claims.Scopes, authscope.Admin())

	// Act: sign then verify, mirroring the mint → verify path.
	token, err := SignAppToken(key, claims)
	assert.NilError(t, err)
	verified, err := VerifyAppToken(key, token)
	assert.NilError(t, err)

	// Assert: the scopes claim survives JWT round-trip and allows any operation,
	// exactly as authenticate populates UserInfo.Scopes.
	assert.DeepEqual(t, verified.Scopes, authscope.Admin())
	user := &UserInfo{Scopes: verified.Scopes}
	assert.Assert(t, user.Scopes.Allow(authscope.OpTaskWrite, authscope.StringAttr("id", "1")))
}
