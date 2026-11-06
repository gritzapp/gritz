package oauthflow_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/auth/oauthflow"
	"github.com/gritzapp/gritz/internal/server"
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"golang.org/x/oauth2"
	"gotest.tools/v3/assert"
)

// authorize runs the authorization code flow for the org's owner and returns
// the oauth2 client config and the issued token.
func authorize(t *testing.T, st *store.Store, org *teststore.Org) (*oauth2.Config, *oauth2.Token) {
	t.Helper()
	key, err := apiauth.CreateAppPrivateKey()
	assert.NilError(t, err)
	auth, err := oauthflow.New(oauthflow.Options{
		AppKey:      key,
		OrgResolver: server.NewStoreUserResolver(st),
		BaseURL:     "http://localhost:8080",
	})
	assert.NilError(t, err)

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/register", auth.HandleRegister)
	mux.HandleFunc("/oauth/authorize", auth.HandleAuthorize)
	mux.HandleFunc("/oauth/token", auth.HandleToken)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	// Register a client
	regBody := `{"client_name":"test-client","redirect_uris":["http://localhost/callback"]}`
	resp, err := http.Post(ts.URL+"/oauth/register", "application/json", strings.NewReader(regBody))
	assert.NilError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, resp.StatusCode, http.StatusCreated)
	var regResp map[string]any
	assert.NilError(t, json.NewDecoder(resp.Body).Decode(&regResp))
	clientID := regResp["client_id"].(string)

	// Configure oauth2 client
	redirectURI := "http://localhost/callback"
	oauthCfg := &oauth2.Config{
		ClientID: clientID,
		Endpoint: oauth2.Endpoint{
			AuthURL:  ts.URL + "/oauth/authorize",
			TokenURL: ts.URL + "/oauth/token",
		},
		RedirectURL: redirectURI,
	}

	// Generate PKCE verifier
	verifier := oauth2.GenerateVerifier()

	// Create app JWT for authorization
	user := &apiauth.UserInfo{
		ID:    org.UserID,
		Email: "test@example.com",
		Name:  "Test User",
		OrgID: org.OrgID,
		Type:  apiauth.AuthTypeApp,
	}
	claims := apiauth.NewAppClaims(user)
	appToken, err := apiauth.SignAppToken(key, claims)
	assert.NilError(t, err)

	// Build authorize URL with PKCE challenge
	authURL := oauthCfg.AuthCodeURL("test-state", oauth2.S256ChallengeOption(verifier))

	// POST to authorize endpoint with app JWT (non-standard: our authorize is a POST with a token field)
	authParsed, err := url.Parse(authURL)
	assert.NilError(t, err)
	authForm := authParsed.Query()
	authForm.Set("token", appToken)
	resp, err = http.PostForm(ts.URL+"/oauth/authorize", authForm)
	assert.NilError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, resp.StatusCode, http.StatusOK)
	var authResp map[string]string
	assert.NilError(t, json.NewDecoder(resp.Body).Decode(&authResp))
	redirectURL, err := url.Parse(authResp["redirect_uri"])
	assert.NilError(t, err)
	code := redirectURL.Query().Get("code")
	assert.Assert(t, code != "")
	assert.Equal(t, redirectURL.Query().Get("state"), "test-state")

	// Exchange authorization code for tokens using oauth2 client
	token, err := oauthCfg.Exchange(t.Context(), code, oauth2.VerifierOption(verifier))
	assert.NilError(t, err)
	assert.Assert(t, token.AccessToken != "")
	assert.Assert(t, token.RefreshToken != "")
	return oauthCfg, token
}

// refresh exchanges the refresh token for a new token.
func refresh(t *testing.T, oauthCfg *oauth2.Config, token *oauth2.Token) (*oauth2.Token, error) {
	t.Helper()
	expiredToken := &oauth2.Token{RefreshToken: token.RefreshToken}
	return oauthCfg.TokenSource(t.Context(), expiredToken).Token()
}

func TestAuthorizationCodeFlow(t *testing.T) {
	t.Parallel()
	// Arrange
	st := teststore.New(t)
	org := teststore.CreateOrg(t, st, nil)
	oauthCfg, token := authorize(t, st, org)

	// Act
	newToken, err := refresh(t, oauthCfg, token)

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, newToken.AccessToken != "")
}

func TestRefreshToken_ArchivedOrg(t *testing.T) {
	t.Parallel()
	// Arrange
	st := teststore.New(t)
	org := teststore.CreateOrg(t, st, nil)
	oauthCfg, token := authorize(t, st, org)
	assert.NilError(t, st.ArchiveOrg(t.Context(), nil, org.OrgID))

	// Act
	_, err := refresh(t, oauthCfg, token)

	// Assert
	assert.ErrorContains(t, err, "invalid refresh_token")
}

func TestRefreshToken_RemovedMember(t *testing.T) {
	t.Parallel()
	// Arrange
	st := teststore.New(t)
	org := teststore.CreateOrg(t, st, nil)
	oauthCfg, token := authorize(t, st, org)
	assert.NilError(t, st.RemoveOrgMember(t.Context(), nil, org.OrgID, org.UserID))

	// Act
	_, err := refresh(t, oauthCfg, token)

	// Assert
	assert.ErrorContains(t, err, "invalid refresh_token")
}
