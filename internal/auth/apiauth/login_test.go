package apiauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/gritzapp/gritz/internal/model"
	httphelper "github.com/zitadel/oidc/v3/pkg/http"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// newTestIdP starts an OIDC provider that completes every code exchange for
// clientID with an ID token for subject, carrying nonce when it isn't empty.
// It swaps httphelper.DefaultHTTPClient for one that trusts the provider, so
// tests using it can't run in parallel.
func newTestIdP(t *testing.T, clientID, subject, nonce string) *httptest.Server {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	assert.NilError(t, err)
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "test"}},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	assert.NilError(t, err)
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                                srv.URL,
			"authorization_endpoint":                srv.URL + "/authorize",
			"token_endpoint":                        srv.URL + "/token",
			"userinfo_endpoint":                     srv.URL + "/userinfo",
			"jwks_uri":                              srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"},
		}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		claims := map[string]any{
			"iss": srv.URL,
			"sub": subject,
			"aud": clientID,
			"iat": now.Unix(),
			"exp": now.Add(time.Hour).Unix(),
		}
		if nonce != "" {
			claims["nonce"] = nonce
		}
		payload, err := json.Marshal(claims)
		assert.Check(t, err)
		sig, err := signer.Sign(payload)
		assert.Check(t, err)
		idToken, err := sig.CompactSerialize()
		assert.Check(t, err)
		writeJSON(w, map[string]any{
			"access_token": "access",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     idToken,
		})
	})
	mux.HandleFunc("GET /userinfo", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"sub":   subject,
			"email": subject + "@example.com",
			"name":  "Test User",
		})
	})
	prev := httphelper.DefaultHTTPClient
	httphelper.DefaultHTTPClient = srv.Client()
	t.Cleanup(func() { httphelper.DefaultHTTPClient = prev })
	return srv
}

func TestLogin_UnrequestedNonce(t *testing.T) {
	// Arrange: an IdP that, like Cognito federating to another provider, puts
	// a nonce in the ID token although the authorization request sent none.
	idp := newTestIdP(t, "client", "user-1", "unrequested")
	resolver := &UserResolverMock{
		ProvisionFunc: func(ctx context.Context, user *UserInfo) (*model.User, error) {
			return &model.User{}, nil
		},
	}
	auth, err := New(t.Context(), Config{
		Domain:        strings.TrimPrefix(idp.URL, "https://"),
		ClientID:      "client",
		ClientSecret:  "secret",
		RedirectURI:   "https://gritz.example.com/auth/callback",
		PostLogoutURI: "https://gritz.example.com/",
		EncryptionKey: []byte("0123456789abcdef0123456789abcdef"),
		Scopes:        []string{"openid", "email", "profile"},
		UserResolver:  resolver,
	})
	assert.NilError(t, err)
	login := httptest.NewRecorder()
	auth.Handler().ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	assert.Equal(t, login.Code, http.StatusFound)
	authorize, err := url.Parse(login.Header().Get("Location"))
	assert.NilError(t, err)
	assert.Equal(t, authorize.Query().Get("nonce"), "")
	callback := httptest.NewRequest(http.MethodGet, "/auth/callback?"+url.Values{
		"code":  {"code"},
		"state": {authorize.Query().Get("state")},
	}.Encode(), nil)
	for _, c := range login.Result().Cookies() {
		callback.AddCookie(c)
	}

	// Act
	rec := httptest.NewRecorder()
	auth.Handler().ServeHTTP(rec, callback)

	// Assert: the login completes and provisions the user.
	assert.Equal(t, rec.Code, http.StatusFound, rec.Body.String())
	calls := resolver.ProvisionCalls()
	assert.Assert(t, cmp.Len(calls, 1))
	assert.DeepEqual(t, calls[0].User, &UserInfo{
		ID:    "user-1",
		Email: "user-1@example.com",
		Name:  "Test User",
	})
}
