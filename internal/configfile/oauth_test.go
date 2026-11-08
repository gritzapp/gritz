package configfile

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gritzapp/gritz/internal/auth/oauthflow"
	"gotest.tools/v3/assert"
)

type memberResolver struct{}

func (memberResolver) ResolveOrg(_ context.Context, _ string, orgID int64) (int64, error) {
	return orgID, nil
}

// oauthServer serves oauthflow's token endpoint and records the refresh
// tokens it is sent.
type oauthServer struct {
	URL string
	key ed25519.PrivateKey
	// expiresIn, when set, replaces the expires_in of every response.
	expiresIn int

	mu        sync.Mutex
	refreshed []string
}

func newOAuthServer(t *testing.T) *oauthServer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	assert.NilError(t, err)
	auth, err := oauthflow.New(oauthflow.Options{AppKey: key, OrgResolver: memberResolver{}, BaseURL: "http://localhost"})
	assert.NilError(t, err)
	s := &oauthServer{key: key}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, r.URL.Path, "/oauth/token")
		s.mu.Lock()
		s.refreshed = append(s.refreshed, r.FormValue("refresh_token"))
		s.mu.Unlock()
		if s.expiresIn == 0 {
			auth.HandleToken(w, r)
			return
		}
		rec := httptest.NewRecorder()
		auth.HandleToken(rec, r)
		var body map[string]any
		assert.NilError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		body["expires_in"] = s.expiresIn
		w.Header().Set("Content-Type", "application/json")
		assert.NilError(t, json.NewEncoder(w).Encode(body))
	}))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

// refreshToken signs a refresh token for org 12 the way oauthflow does.
func (s *oauthServer) refreshToken(t *testing.T, iat, exp time.Time) string {
	t.Helper()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{
		"sub":        "user-1",
		"iat":        iat.Unix(),
		"exp":        exp.Unix(),
		"token_type": "refresh_token",
		"org_id":     12,
	}).SignedString(s.key)
	assert.NilError(t, err)
	return tok
}

func TestTokenSource(t *testing.T) {
	t.Parallel()
	// Arrange - a refresh token issued an hour ago
	srv := newOAuthServer(t)
	rt := srv.refreshToken(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	path := filepath.Join(t.TempDir(), Name)
	assert.NilError(t, Login(path, "acme", Entry{ID: 12, RefreshToken: rt, Server: srv.URL}))
	entry := &Entry{ID: 12, RefreshToken: rt, Server: srv.URL, Name: "acme", Path: path}
	ts := entry.TokenSource(t.Context())

	// Act
	first, err := ts.Token()
	assert.NilError(t, err)
	second, err := ts.Token()
	assert.NilError(t, err)

	// Assert - one refresh for both, and the stored token is kept
	assert.DeepEqual(t, srv.refreshed, []string{rt})
	assert.Equal(t, second.AccessToken, first.AccessToken)
	f, err := Read(path)
	assert.NilError(t, err)
	assert.Equal(t, f.Orgs["acme"].RefreshToken, rt)
}

func TestTokenSource_AccessTokenExpired(t *testing.T) {
	t.Parallel()
	// Arrange - access tokens that are already within oauth2's early expiry
	srv := newOAuthServer(t)
	srv.expiresIn = 1
	rt := srv.refreshToken(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	entry := &Entry{ID: 12, RefreshToken: rt, Server: srv.URL, Name: "acme", Path: filepath.Join(t.TempDir(), Name)}
	ts := entry.TokenSource(t.Context())

	// Act
	first, err := ts.Token()
	assert.NilError(t, err)
	_, err = ts.Token()
	assert.NilError(t, err)

	// Assert - the second refresh uses the rotated token
	assert.DeepEqual(t, srv.refreshed, []string{rt, first.RefreshToken})
}

func TestTokenSource_WritesBack(t *testing.T) {
	t.Parallel()
	// Arrange - a refresh token issued more than a day ago
	srv := newOAuthServer(t)
	rt := srv.refreshToken(t, time.Now().Add(-25*time.Hour), time.Now().Add(time.Hour))
	path := filepath.Join(t.TempDir(), Name)
	assert.NilError(t, Login(path, "acme", Entry{ID: 12, RefreshToken: rt, Server: srv.URL}))
	entry := &Entry{ID: 12, RefreshToken: rt, Server: srv.URL, Name: "acme", Path: path}

	// Act
	tok, err := entry.TokenSource(t.Context()).Token()

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, tok.RefreshToken != rt)
	f, err := Read(path)
	assert.NilError(t, err)
	assert.Equal(t, f.Orgs["acme"].RefreshToken, tok.RefreshToken)
}

func TestTokenSource_RefreshTokenExpired(t *testing.T) {
	t.Parallel()
	srv := newOAuthServer(t)
	rt := srv.refreshToken(t, time.Now().Add(-31*24*time.Hour), time.Now().Add(-time.Hour))
	entry := &Entry{ID: 12, RefreshToken: rt, Server: srv.URL, Name: "acme", Path: filepath.Join(t.TempDir(), Name)}

	_, err := entry.TokenSource(t.Context()).Token()

	assert.Error(t, err, "org entry \"acme\": login expired or revoked: run `gritz login --name acme --server "+srv.URL+"`")
}
