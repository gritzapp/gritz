package apiauth

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
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

// newBearerAuth returns an Auth signing app tokens with key. It has a dev
// user, so the RequireBearer tests also show it is never used.
func newBearerAuth(t *testing.T, key ed25519.PrivateKey) *Auth {
	t.Helper()
	a, err := New(t.Context(), Config{AppKey: key, DevUser: &UserInfo{ID: "dev"}})
	assert.NilError(t, err)
	return a
}

func TestRequireBearer(t *testing.T) {
	t.Parallel()
	// Arrange
	key, err := CreateAppPrivateKey()
	assert.NilError(t, err)
	a := newBearerAuth(t, key)
	token, err := SignAppToken(key, NewAppClaims(&UserInfo{ID: "user-1", OrgID: 7, Scopes: authscope.Admin()}))
	assert.NilError(t, err)
	var caller *UserInfo
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller = MustCaller(r.Context())
	})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	// Act
	a.RequireBearer()(next).ServeHTTP(rec, req)

	// Assert
	assert.Equal(t, rec.Code, http.StatusOK)
	assert.Equal(t, caller.ID, "user-1")
	assert.Equal(t, caller.OrgID, int64(7))
	assert.Equal(t, caller.Type, AuthTypeApp)
}

func TestRequireBearer_NoToken(t *testing.T) {
	t.Parallel()
	// Arrange
	key, err := CreateAppPrivateKey()
	assert.NilError(t, err)
	a := newBearerAuth(t, key)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next called without a token")
	})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()

	// Act
	a.RequireBearer()(next).ServeHTTP(rec, req)

	// Assert
	assert.Equal(t, rec.Code, http.StatusUnauthorized)
}

func TestRequireBearer_InvalidToken(t *testing.T) {
	t.Parallel()
	// Arrange: a token signed with another key.
	key, err := CreateAppPrivateKey()
	assert.NilError(t, err)
	other, err := CreateAppPrivateKey()
	assert.NilError(t, err)
	a := newBearerAuth(t, key)
	token, err := SignAppToken(other, NewAppClaims(&UserInfo{ID: "user-1", OrgID: 7}))
	assert.NilError(t, err)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next called with an invalid token")
	})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	// Act
	a.RequireBearer()(next).ServeHTTP(rec, req)

	// Assert
	assert.Equal(t, rec.Code, http.StatusUnauthorized)
}
