package apiauth

import (
	"testing"

	"github.com/gritzapp/gritz/internal/auth/authscope"
	"gotest.tools/v3/assert"
)

func TestUserInfoAllow_Admin(t *testing.T) {
	t.Parallel()
	// Arrange: a caller granted the admin wildcard.
	user := &UserInfo{Scopes: authscope.Admin()}

	// Act & Assert: admin allows any 2-segment operation.
	assert.Assert(t, user.Scopes.Allow(authscope.OpTaskRead, authscope.StringAttr("id", "1")))
	assert.Assert(t, user.Scopes.Allow(authscope.OpGitHubTokenCreate))
}

func TestUserInfoAllow_NoScopes(t *testing.T) {
	t.Parallel()
	// Arrange: a caller carrying no scopes.
	user := &UserInfo{}

	// Act & Assert: an empty set allows nothing.
	assert.Assert(t, !user.Scopes.Allow(authscope.OpTaskRead))
}
