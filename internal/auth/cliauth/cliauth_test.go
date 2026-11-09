package cliauth

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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

// oauthServer serves oauthflow's authorize and token endpoints, and counts the
// token requests.
type oauthServer struct {
	URL      string
	key      ed25519.PrivateKey
	requests atomic.Int32
}

func newOAuthServer(t *testing.T) *oauthServer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	assert.NilError(t, err)
	auth, err := oauthflow.New(oauthflow.Options{AppKey: key, OrgResolver: memberResolver{}, BaseURL: "http://localhost"})
	assert.NilError(t, err)
	s := &oauthServer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/authorize", auth.HandleAuthorize)
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		auth.HandleToken(w, r)
	})
	srv := httptest.NewServer(mux)
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
	var reported []string
	ts := TokenSource(t.Context(), TokenSourceOptions{
		Server:         srv.URL,
		RefreshToken:   rt,
		OnRefreshToken: func(token string) { reported = append(reported, token) },
	})

	// Act
	first, err := ts.Token()
	assert.NilError(t, err)
	second, err := ts.Token()
	assert.NilError(t, err)

	// Assert - one refresh until the access token expires, and nothing reported
	assert.Equal(t, srv.requests.Load(), int32(1))
	assert.Equal(t, second.AccessToken, first.AccessToken)
	assert.Assert(t, first.RefreshToken != rt)
	assert.Assert(t, reported == nil)
}

func TestTokenSource_OldRefreshToken(t *testing.T) {
	t.Parallel()
	// Arrange - a refresh token issued more than a day ago
	srv := newOAuthServer(t)
	rt := srv.refreshToken(t, time.Now().Add(-25*time.Hour), time.Now().Add(time.Hour))
	var reported []string
	ts := TokenSource(t.Context(), TokenSourceOptions{
		Server:         srv.URL,
		RefreshToken:   rt,
		OnRefreshToken: func(token string) { reported = append(reported, token) },
	})

	// Act
	tok, err := ts.Token()

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, reported, []string{tok.RefreshToken})
}

func TestTokenSource_LoginExpired(t *testing.T) {
	t.Parallel()
	srv := newOAuthServer(t)
	rt := srv.refreshToken(t, time.Now().Add(-31*24*time.Hour), time.Now().Add(-time.Hour))

	_, err := TokenSource(t.Context(), TokenSourceOptions{Server: srv.URL, RefreshToken: rt}).Token()

	assert.ErrorIs(t, err, ErrLoginExpired)
	assert.Error(t, err, "login expired or revoked: run `gritz login --server "+srv.URL+"`")
}
