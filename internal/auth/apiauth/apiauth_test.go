package apiauth

import (
	"testing"

	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/zitadel/oidc/v3/pkg/oidc"
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

func TestProfileName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		profile oidc.UserInfoProfile
		want    string
	}{
		{"name", oidc.UserInfoProfile{Name: "Ada L", GivenName: "Ada", FamilyName: "Lovelace"}, "Ada L"},
		{"given and family", oidc.UserInfoProfile{GivenName: "Ada", FamilyName: "Lovelace"}, "Ada Lovelace"},
		{"given only", oidc.UserInfoProfile{GivenName: "Ada"}, "Ada"},
		{"family only", oidc.UserInfoProfile{FamilyName: "Lovelace"}, "Lovelace"},
		{"none", oidc.UserInfoProfile{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Act: derive the display name from the profile claims.
			got := profileName(&oidc.UserInfo{UserInfoProfile: tt.profile})

			// Assert
			assert.Equal(t, got, tt.want)
		})
	}
}
