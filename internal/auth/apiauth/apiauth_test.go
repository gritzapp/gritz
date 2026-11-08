package apiauth

import (
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

func TestRequireBearerAuth(t *testing.T) {
	t.Parallel()
	// Arrange
	key, err := CreateAppPrivateKey()
	assert.NilError(t, err)
	// The dev user is never used: RequireBearerAuth has no fallback.
	a, err := New(t.Context(), Config{AppKey: key, DevUser: &UserInfo{ID: "dev"}})
	assert.NilError(t, err)
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
	a.RequireBearerAuth()(next).ServeHTTP(rec, req)

	// Assert
	assert.Equal(t, rec.Code, http.StatusOK)
	assert.Equal(t, caller.ID, "user-1")
	assert.Equal(t, caller.OrgID, int64(7))
	assert.Equal(t, caller.Type, AuthTypeApp)
}

func TestRequireBearerAuth_NoToken(t *testing.T) {
	t.Parallel()
	// Arrange
	key, err := CreateAppPrivateKey()
	assert.NilError(t, err)
	// The dev user is never used: RequireBearerAuth has no fallback.
	a, err := New(t.Context(), Config{AppKey: key, DevUser: &UserInfo{ID: "dev"}})
	assert.NilError(t, err)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next called without a token")
	})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()

	// Act
	a.RequireBearerAuth()(next).ServeHTTP(rec, req)

	// Assert
	assert.Equal(t, rec.Code, http.StatusUnauthorized)
}

func TestRequireBearerAuth_InvalidToken(t *testing.T) {
	t.Parallel()
	// Arrange: a token signed with another key.
	key, err := CreateAppPrivateKey()
	assert.NilError(t, err)
	other, err := CreateAppPrivateKey()
	assert.NilError(t, err)
	// The dev user is never used: RequireBearerAuth has no fallback.
	a, err := New(t.Context(), Config{AppKey: key, DevUser: &UserInfo{ID: "dev"}})
	assert.NilError(t, err)
	token, err := SignAppToken(other, NewAppClaims(&UserInfo{ID: "user-1", OrgID: 7}))
	assert.NilError(t, err)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next called with an invalid token")
	})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	// Act
	a.RequireBearerAuth()(next).ServeHTTP(rec, req)

	// Assert
	assert.Equal(t, rec.Code, http.StatusUnauthorized)
}

func TestRequireResourceAuth(t *testing.T) {
	t.Parallel()
	const metadata = "https://gritz.example.com/.well-known/oauth-protected-resource"
	key, err := CreateAppPrivateKey()
	assert.NilError(t, err)
	other, err := CreateAppPrivateKey()
	assert.NilError(t, err)
	valid, err := SignAppToken(key, NewAppClaims(&UserInfo{ID: "user-1", OrgID: 7}))
	assert.NilError(t, err)
	invalid, err := SignAppToken(other, NewAppClaims(&UserInfo{ID: "user-1", OrgID: 7}))
	assert.NilError(t, err)
	tests := []struct {
		name          string
		authorization string
		wantCode      int
		wantChallenge string
	}{
		{
			name:          "no token",
			wantCode:      http.StatusUnauthorized,
			wantChallenge: `Bearer resource_metadata="` + metadata + `"`,
		},
		{
			name:          "invalid token",
			authorization: "Bearer " + invalid,
			wantCode:      http.StatusUnauthorized,
			wantChallenge: `Bearer resource_metadata="` + metadata + `", error="invalid_token"`,
		},
		{
			name:          "valid token",
			authorization: "Bearer " + valid,
			wantCode:      http.StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange: the dev user is never used, RequireResourceAuth has no fallback.
			a, err := New(t.Context(), Config{AppKey: key, DevUser: &UserInfo{ID: "dev"}})
			assert.NilError(t, err)
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, MustCaller(r.Context()).ID, "user-1")
			})
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tt.authorization != "" {
				req.Header.Set("Authorization", tt.authorization)
			}
			rec := httptest.NewRecorder()

			// Act
			a.RequireResourceAuth(metadata)(next).ServeHTTP(rec, req)

			// Assert
			assert.Equal(t, rec.Code, tt.wantCode)
			assert.Equal(t, rec.Header().Get("WWW-Authenticate"), tt.wantChallenge)
		})
	}
}
