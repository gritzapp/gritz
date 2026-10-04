package oauthflow

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gritzapp/gritz/internal/auth/apiauth"
)

// HandleMetadata serves the OAuth 2.1 authorization server metadata.
// GET /.well-known/oauth-authorization-server
func (a *Auth) HandleMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                           a.baseURL.String(),
		"authorization_endpoint":           a.baseURL.JoinPath("/ui/oauth/authorize").String(),
		"token_endpoint":                   a.baseURL.JoinPath("/oauth/token").String(),
		"registration_endpoint":            a.baseURL.JoinPath("/oauth/register").String(),
		"response_types_supported":         []string{"code"},
		"grant_types_supported":            []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported": []string{"S256"},
	})
}

// HandleResourceMetadata serves the OAuth 2.1 protected resource metadata.
// GET /.well-known/oauth-protected-resource
func (a *Auth) HandleResourceMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"resource":              a.baseURL.String(),
		"authorization_servers": []string{a.baseURL.String()},
	})
}

// HandleRegister is a stub DCR endpoint per RFC 7591.
// POST /oauth/register
func (a *Auth) HandleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"client_id":     uuid.New().String(),
		"client_name":   req.ClientName,
		"redirect_uris": req.RedirectURIs,
	})
}

// HandleAuthorize handles the authorization endpoint.
// POST /oauth/authorize
func (a *Auth) HandleAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}
	token := r.FormValue("token")
	clientID := r.FormValue("client_id")
	redirectURI := r.FormValue("redirect_uri")
	state := r.FormValue("state")
	codeChallenge := r.FormValue("code_challenge")
	codeChallengeMethod := r.FormValue("code_challenge_method")
	responseType := r.FormValue("response_type")

	if token == "" || clientID == "" || redirectURI == "" || codeChallenge == "" {
		http.Error(w, "missing required parameters", http.StatusBadRequest)
		return
	}
	if responseType != "code" {
		http.Error(w, "unsupported response_type", http.StatusBadRequest)
		return
	}
	if codeChallengeMethod != "S256" {
		http.Error(w, "unsupported code_challenge_method", http.StatusBadRequest)
		return
	}

	// Verify the app JWT
	appClaims, err := apiauth.VerifyAppToken(a.appKey, token)
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	// Sign an auth code JWT
	now := time.Now()
	codeClaims := &authCodeClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   appClaims.Subject,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(a.authCodeTTL)),
		},
		TokenType:     "auth_code",
		Email:         appClaims.Email,
		Name:          appClaims.Name,
		OrgID:         appClaims.OrgID,
		ClientID:      clientID,
		RedirectURI:   redirectURI,
		CodeChallenge: codeChallenge,
	}
	codeToken := jwt.NewWithClaims(jwt.SigningMethodEdDSA, codeClaims)
	code, err := codeToken.SignedString(a.appKey)
	if err != nil {
		http.Error(w, "failed to sign auth code", http.StatusInternalServerError)
		return
	}

	// Build the redirect URL with the auth code
	redirectURL, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	q := redirectURL.Query()
	q.Set("code", code)
	if state != "" {
		q.Set("state", state)
	}
	redirectURL.RawQuery = q.Encode()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"redirect_uri": redirectURL.String(),
	})
}

// HandleToken handles the token endpoint.
// POST /oauth/token
func (a *Auth) HandleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	grantType := r.FormValue("grant_type")
	switch grantType {
	case "authorization_code":
		a.handleAuthCodeGrant(w, r)
	case "refresh_token":
		a.handleRefreshTokenGrant(w, r)
	default:
		http.Error(w, "unsupported grant_type", http.StatusBadRequest)
	}
}

func (a *Auth) handleAuthCodeGrant(w http.ResponseWriter, r *http.Request) {
	code := r.FormValue("code")
	clientID := r.FormValue("client_id")
	redirectURI := r.FormValue("redirect_uri")
	codeVerifier := r.FormValue("code_verifier")

	if code == "" || clientID == "" || redirectURI == "" || codeVerifier == "" {
		http.Error(w, "missing required parameters", http.StatusBadRequest)
		return
	}

	// Verify the auth code JWT
	codeClaims, err := a.verifyAuthCode(code)
	if err != nil {
		http.Error(w, "invalid authorization code", http.StatusBadRequest)
		return
	}

	// Verify client_id and redirect_uri match
	if codeClaims.ClientID != clientID {
		http.Error(w, "client_id mismatch", http.StatusBadRequest)
		return
	}
	if codeClaims.RedirectURI != redirectURI {
		http.Error(w, "redirect_uri mismatch", http.StatusBadRequest)
		return
	}

	// Verify PKCE: SHA256(code_verifier) must equal code_challenge
	h := sha256.Sum256([]byte(codeVerifier))
	computedChallenge := base64.RawURLEncoding.EncodeToString(h[:])
	if computedChallenge != codeClaims.CodeChallenge {
		http.Error(w, "invalid code_verifier", http.StatusBadRequest)
		return
	}

	// Issue tokens
	a.issueTokens(w, codeClaims.Subject, codeClaims.Email, codeClaims.Name, codeClaims.OrgID)
}

func (a *Auth) handleRefreshTokenGrant(w http.ResponseWriter, r *http.Request) {
	refreshToken := r.FormValue("refresh_token")
	if refreshToken == "" {
		http.Error(w, "missing refresh_token", http.StatusBadRequest)
		return
	}

	// Verify the refresh token JWT
	claims, err := a.verifyRefreshToken(refreshToken)
	if err != nil {
		http.Error(w, "invalid refresh_token", http.StatusBadRequest)
		return
	}

	// Issue new tokens (rotation)
	a.issueTokens(w, claims.Subject, claims.Email, claims.Name, claims.OrgID)
}

func (a *Auth) issueTokens(w http.ResponseWriter, subject, email, name string, orgID int64) {
	user := &apiauth.UserInfo{
		ID:    subject,
		Email: email,
		Name:  name,
		OrgID: orgID,
		Type:  apiauth.AuthTypeApp,
	}

	// Sign access token (app JWT, 5min TTL)
	appClaims := apiauth.NewAppClaims(user)
	accessToken, err := apiauth.SignAppToken(a.appKey, appClaims)
	if err != nil {
		http.Error(w, "failed to sign access token", http.StatusInternalServerError)
		return
	}

	// Sign refresh token (30 day TTL)
	now := time.Now()
	rtClaims := &refreshTokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(a.refreshTokenTTL)),
		},
		TokenType: "refresh_token",
		Email:     email,
		Name:      name,
		OrgID:     orgID,
	}
	refreshToken, err := a.signRefreshToken(rtClaims)
	if err != nil {
		http.Error(w, "failed to sign refresh token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token":  accessToken,
		"token_type":    "Bearer",
		"expires_in":    int(apiauth.AppTokenTTL.Seconds()),
		"refresh_token": refreshToken,
	})
}
