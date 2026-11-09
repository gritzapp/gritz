package cliauth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"gotest.tools/v3/assert"
)

func TestLogin(t *testing.T) {
	t.Parallel()
	// Arrange - a browser that approves for org 12 as the approval page does,
	// then follows the redirect
	srv := newOAuthServer(t)
	appToken, err := apiauth.SignAppToken(srv.key, apiauth.NewAppClaims(&apiauth.UserInfo{ID: "user-1", OrgID: 12}))
	assert.NilError(t, err)
	openBrowser := func(authURL string) error {
		u, err := url.Parse(authURL)
		assert.NilError(t, err)
		assert.Equal(t, u.Path, "/ui/oauth/authorize")
		form := u.Query()
		form.Set("token", appToken)
		resp, err := http.PostForm(srv.URL+"/oauth/authorize", form)
		assert.NilError(t, err)
		defer resp.Body.Close()
		var authz struct {
			RedirectURI string `json:"redirect_uri"`
		}
		assert.NilError(t, json.NewDecoder(resp.Body).Decode(&authz))
		callback, err := http.Get(authz.RedirectURI)
		assert.NilError(t, err)
		defer callback.Body.Close()
		assert.Equal(t, callback.StatusCode, http.StatusOK)
		return nil
	}

	// Act
	tok, err := Login(t.Context(), LoginOptions{
		Server:      srv.URL,
		OpenBrowser: openBrowser,
		Prompt:      io.Discard,
		Timeout:     time.Minute,
	})

	// Assert - the refresh token works
	assert.NilError(t, err)
	_, err = TokenSource(t.Context(), TokenSourceOptions{Server: srv.URL, RefreshToken: tok.RefreshToken}).Token()
	assert.NilError(t, err)
}
