package oauthx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/oauth2"
	"gotest.tools/v3/assert"
)

// tokenServer is a token endpoint that rotates the refresh token on every
// request, issuing access tokens that expire after expiresIn seconds.
type tokenServer struct {
	URL       string
	expiresIn int
	refreshed []string // the refresh token of each request
}

func newTokenServer(t *testing.T, expiresIn int) *tokenServer {
	t.Helper()
	s := &tokenServer{expiresIn: expiresIn}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.refreshed = append(s.refreshed, r.FormValue("refresh_token"))
		w.Header().Set("Content-Type", "application/json")
		assert.NilError(t, json.NewEncoder(w).Encode(map[string]any{
			"access_token":  fmt.Sprintf("at-%d", len(s.refreshed)),
			"token_type":    "Bearer",
			"expires_in":    s.expiresIn,
			"refresh_token": fmt.Sprintf("rt-%d", len(s.refreshed)),
		}))
	}))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

func TestRefreshSource(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := newTokenServer(t, 300)
	var reported []string
	ts := NewRefreshSource(t.Context(), RefreshSourceOptions{
		Config:            &oauth2.Config{Endpoint: oauth2.Endpoint{TokenURL: srv.URL}},
		RefreshToken:      "rt-0",
		OnNewRefreshToken: func(token string) { reported = append(reported, token) },
	})

	// Act
	first, err := ts.Token()
	assert.NilError(t, err)
	second, err := ts.Token()
	assert.NilError(t, err)

	// Assert - one refresh until the access token expires
	assert.DeepEqual(t, srv.refreshed, []string{"rt-0"})
	assert.Equal(t, first.AccessToken, "at-1")
	assert.Equal(t, second.AccessToken, "at-1")
	assert.DeepEqual(t, reported, []string{"rt-1"})
}

func TestRefreshSource_Expired(t *testing.T) {
	t.Parallel()
	// Arrange - access tokens that are already within oauth2's early expiry
	srv := newTokenServer(t, 1)
	var reported []string
	ts := NewRefreshSource(t.Context(), RefreshSourceOptions{
		Config:            &oauth2.Config{Endpoint: oauth2.Endpoint{TokenURL: srv.URL}},
		RefreshToken:      "rt-0",
		OnNewRefreshToken: func(token string) { reported = append(reported, token) },
	})

	// Act
	_, err := ts.Token()
	assert.NilError(t, err)
	second, err := ts.Token()
	assert.NilError(t, err)

	// Assert - the second refresh uses the new refresh token
	assert.DeepEqual(t, srv.refreshed, []string{"rt-0", "rt-1"})
	assert.Equal(t, second.AccessToken, "at-2")
	assert.DeepEqual(t, reported, []string{"rt-1", "rt-2"})
}
