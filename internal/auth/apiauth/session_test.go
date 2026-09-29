package apiauth

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zitadel/oidc/v3/pkg/client/rp"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/zitadel-go/v3/pkg/authentication"
	openid "github.com/zitadel/zitadel-go/v3/pkg/authentication/oidc"
	"golang.org/x/oauth2"
	"gotest.tools/v3/assert"
)

// testEncryptionKey is 32 bytes, as AES requires.
const testEncryptionKey = "0123456789abcdef0123456789abcdef"

// fakeIDP is a minimal OIDC provider: just enough discovery for
// rp.NewRelyingPartyOIDC and a token endpoint that counts refresh grants.
type fakeIDP struct {
	server *httptest.Server
	// calls counts refresh_token grants received.
	calls atomic.Int64
	// delay is applied inside the token endpoint, to widen the window in which
	// concurrent renewals can overlap.
	delay time.Duration
	// fail makes the token endpoint reject the grant, as ZITADEL does for a
	// revoked, rotated, or expired refresh token.
	fail bool
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	idp := &fakeIDP{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                idp.server.URL,
			"authorization_endpoint":                idp.server.URL + "/oauth/v2/authorize",
			"token_endpoint":                        idp.server.URL + "/oauth/v2/token",
			"userinfo_endpoint":                     idp.server.URL + "/oidc/v1/userinfo",
			"jwks_uri":                              idp.server.URL + "/oauth/v2/keys",
			"end_session_endpoint":                  idp.server.URL + "/oidc/v1/end_session",
			"response_types_supported":              []string{"code"},
			"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/oauth/v2/token", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("grant_type") == string(oidc.GrantTypeRefreshToken) {
			idp.calls.Add(1)
		}
		time.Sleep(idp.delay)
		if idp.fail {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// No id_token: ZITADEL is not obliged to return one on refresh, and the
		// OIDC spec says so explicitly. This is the branch worth pinning, because
		// it is the one where the renewed session carries no new claims.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access-token",
			"refresh_token": "new-refresh-token",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	})
	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

// newTestAuth builds an Auth wired to idp, bypassing New (which needs a real
// ZITADEL instance to discover).
func newTestAuth(t *testing.T, idp *fakeIDP) *Auth {
	t.Helper()
	party, err := rp.NewRelyingPartyOIDC(t.Context(), idp.server.URL, "client-id", "client-secret", "http://app/auth/callback", []string{oidc.ScopeOpenID, oidc.ScopeOfflineAccess})
	assert.NilError(t, err)
	return &Auth{
		relyingParty:  party,
		encryptionKey: testEncryptionKey,
		appKey:        mustAppKey(t),
	}
}

func mustAppKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	key, err := CreateAppPrivateKey()
	assert.NilError(t, err)
	return key
}

// newSession builds an auth context whose ID token expires at exp.
func newSession(exp time.Time, refreshToken string) *openid.DefaultContext {
	return &openid.DefaultContext{
		UserInfo: &oidc.UserInfo{
			Subject: "user-1",
			UserInfoEmail: oidc.UserInfoEmail{
				Email: "user@example.com",
			},
		},
		Tokens: &oidc.Tokens[*oidc.IDTokenClaims]{
			Token: &oauth2.Token{
				AccessToken:  "access-token",
				RefreshToken: refreshToken,
				TokenType:    "Bearer",
			},
			IDTokenClaims: &oidc.IDTokenClaims{
				TokenClaims: oidc.TokenClaims{Expiration: oidc.FromTime(exp)},
			},
		},
	}
}

// requestWithSession returns a request carrying session in its cookie.
func requestWithSession(t *testing.T, a *Auth, session *openid.DefaultContext) *http.Request {
	t.Helper()
	rec := httptest.NewRecorder()
	assert.NilError(t, a.writeSession(rec, session))
	req := httptest.NewRequest(http.MethodGet, "/auth/token", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	return req
}

// sessionCookie returns the session cookie set on rec, or nil if none was.
func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	return nil
}

func TestSessionCodec_RoundTrip(t *testing.T) {
	t.Parallel()
	// Arrange: a session an hour from expiry.
	a := &Auth{encryptionKey: testEncryptionKey}
	exp := time.Now().Add(time.Hour)
	rec := httptest.NewRecorder()

	// Act: write it to a cookie and read it back.
	assert.NilError(t, a.writeSession(rec, newSession(exp, "refresh-token")))
	cookie := sessionCookie(rec)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	got, err := a.readSession(req)

	// Assert: the refresh token and expiry survive the round trip, and the
	// cookie's MaxAge tracks the ID token rather than the browser session.
	assert.NilError(t, err)
	assert.Equal(t, got.Tokens.RefreshToken, "refresh-token")
	assert.Equal(t, got.UserInfo.Subject, "user-1")
	assert.Equal(t, got.GetExpiration().Unix(), exp.Unix())
	assert.Assert(t, cookie.HttpOnly)
	assert.Assert(t, cookie.Secure)
}

func TestReadSession_NoCookie(t *testing.T) {
	t.Parallel()
	// Arrange: a request with no session cookie.
	a := &Auth{encryptionKey: testEncryptionKey}

	// Act & Assert: reading reports the miss rather than panicking.
	_, err := a.readSession(httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Assert(t, err != nil)
}

func TestRenewSession_SkipsWhenFarFromExpiry(t *testing.T) {
	t.Parallel()
	// Arrange: a session well outside the renewal window.
	idp := newFakeIDP(t)
	a := newTestAuth(t, idp)
	req := requestWithSession(t, a, newSession(time.Now().Add(12*time.Hour), "refresh-token"))
	rec := httptest.NewRecorder()

	// Act
	renewed, err := a.renewSession(rec, req)

	// Assert: the IdP is left alone and the cookie is untouched.
	assert.NilError(t, err)
	assert.Assert(t, renewed == nil)
	assert.Equal(t, idp.calls.Load(), int64(0))
	assert.Assert(t, sessionCookie(rec) == nil)
}

func TestRenewSession_RenewsNearExpiry(t *testing.T) {
	t.Parallel()
	// Arrange: a session inside the renewal window but not yet expired.
	idp := newFakeIDP(t)
	a := newTestAuth(t, idp)
	req := requestWithSession(t, a, newSession(time.Now().Add(10*time.Minute), "old-refresh-token"))
	rec := httptest.NewRecorder()

	// Act
	renewed, err := a.renewSession(rec, req)

	// Assert: one refresh grant, and the rotated token is persisted.
	assert.NilError(t, err)
	assert.Assert(t, renewed != nil)
	assert.Equal(t, idp.calls.Load(), int64(1))
	assert.Equal(t, renewed.Tokens.RefreshToken, "new-refresh-token")

	cookie := sessionCookie(rec)
	assert.Assert(t, cookie != nil)
	stored := httptest.NewRequest(http.MethodGet, "/", nil)
	stored.AddCookie(cookie)
	got, err := a.readSession(stored)
	assert.NilError(t, err)
	assert.Equal(t, got.Tokens.RefreshToken, "new-refresh-token")
	assert.Equal(t, got.UserInfo.Subject, "user-1")
}

func TestRenewSession_RevivesExpiredSession(t *testing.T) {
	t.Parallel()
	// Arrange: a session already past its ID token's exp. zitadel-go would evict
	// it, so the middleware never populates a context for it.
	idp := newFakeIDP(t)
	a := newTestAuth(t, idp)
	expired := newSession(time.Now().Add(-time.Minute), "old-refresh-token")
	assert.Assert(t, !expired.IsAuthenticated(), "fixture should be expired")
	req := requestWithSession(t, a, expired)
	rec := httptest.NewRecorder()

	// Act
	renewed, err := a.renewSession(rec, req)

	// Assert: expiry is recoverable as long as the refresh token still works.
	assert.NilError(t, err)
	assert.Assert(t, renewed != nil)
	assert.Equal(t, renewed.Tokens.RefreshToken, "new-refresh-token")
}

func TestRenewSession_FailureIsNonFatalWhileStillValid(t *testing.T) {
	t.Parallel()
	// Arrange: a still-valid session, and an IdP that rejects the grant — the
	// shape a rotation race takes for whichever caller loses it.
	idp := newFakeIDP(t)
	idp.fail = true
	a := newTestAuth(t, idp)
	req := requestWithSession(t, a, newSession(time.Now().Add(10*time.Minute), "old-refresh-token"))
	rec := httptest.NewRecorder()

	// Act
	renewed, err := a.renewSession(rec, req)

	// Assert: the request proceeds, and crucially the cookie is not rewritten —
	// the winner of the race may already have installed a newer one.
	assert.NilError(t, err)
	assert.Assert(t, renewed == nil)
	assert.Assert(t, sessionCookie(rec) == nil, "a failed renewal must not write a stale cookie")
}

func TestRenewSession_ExpiredAndUnrenewable(t *testing.T) {
	t.Parallel()
	// Arrange: an expired session whose refresh token no longer works.
	idp := newFakeIDP(t)
	idp.fail = true
	a := newTestAuth(t, idp)
	req := requestWithSession(t, a, newSession(time.Now().Add(-time.Minute), "old-refresh-token"))

	// Act
	_, err := a.renewSession(httptest.NewRecorder(), req)

	// Assert: this is the one case that must end the session.
	assert.ErrorIs(t, err, errSessionExpired)
}

func TestRenewSession_ExpiredWithoutRefreshToken(t *testing.T) {
	t.Parallel()
	// Arrange: a session minted before offline_access was requested.
	idp := newFakeIDP(t)
	a := newTestAuth(t, idp)
	req := requestWithSession(t, a, newSession(time.Now().Add(-time.Minute), ""))

	// Act
	_, err := a.renewSession(httptest.NewRecorder(), req)

	// Assert: nothing to renew with, and no pointless call to the IdP.
	assert.ErrorIs(t, err, errSessionExpired)
	assert.Equal(t, idp.calls.Load(), int64(0))
}

func TestRenewSession_ConcurrentRenewalsCollapse(t *testing.T) {
	t.Parallel()
	// Arrange: several tabs refreshing the same session at once. Rotation means
	// only one of them could spend the refresh token.
	idp := newFakeIDP(t)
	idp.delay = 50 * time.Millisecond
	a := newTestAuth(t, idp)
	session := newSession(time.Now().Add(10*time.Minute), "old-refresh-token")

	const tabs = 5
	var wg sync.WaitGroup
	results := make([]*openid.DefaultContext, tabs)
	errs := make([]error, tabs)
	for i := range tabs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = a.renewSession(httptest.NewRecorder(), requestWithSession(t, a, session))
		}()
	}
	wg.Wait()

	// Assert: one grant spent, and every caller got the winner's tokens, so none
	// of them is left holding an invalidated token.
	assert.Equal(t, idp.calls.Load(), int64(1))
	for i := range tabs {
		assert.NilError(t, errs[i], fmt.Sprintf("tab %d", i))
		assert.Assert(t, results[i] != nil, "tab %d got no session", i)
		assert.Equal(t, results[i].Tokens.RefreshToken, "new-refresh-token")
	}
}

func TestRenewSession_NoRelyingParty(t *testing.T) {
	t.Parallel()
	// Arrange: dev-user mode, where there is no IdP to renew against.
	a := &Auth{encryptionKey: testEncryptionKey}
	req := requestWithSession(t, a, newSession(time.Now().Add(-time.Minute), "refresh-token"))

	// Act & Assert: renewal is inert rather than an error.
	renewed, err := a.renewSession(httptest.NewRecorder(), req)
	assert.NilError(t, err)
	assert.Assert(t, renewed == nil)
}

func TestHandleToken_RenewedExpiredSessionIssuesToken(t *testing.T) {
	t.Parallel()
	// Arrange: an expired session the middleware left out of the request context,
	// exactly as zitadel-go v3.30.0 does.
	idp := newFakeIDP(t)
	a := newTestAuth(t, idp)
	req := requestWithSession(t, a, newSession(time.Now().Add(-time.Minute), "old-refresh-token"))
	rec := httptest.NewRecorder()

	// Act
	a.HandleToken()(rec, req)

	// Assert: renewal revives it and a fresh app JWT is issued.
	assert.Equal(t, rec.Code, http.StatusOK)
	var body struct {
		Token string `json:"token"`
	}
	assert.NilError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	claims, err := VerifyAppToken(a.appKey, body.Token)
	assert.NilError(t, err)
	assert.Equal(t, claims.Subject, "user-1")
}

func TestHandleToken_UnrenewableSessionClearsCookie(t *testing.T) {
	t.Parallel()
	// Arrange: an expired session whose refresh token is dead — a user who was
	// deactivated in ZITADEL looks like this.
	idp := newFakeIDP(t)
	idp.fail = true
	a := newTestAuth(t, idp)
	req := requestWithSession(t, a, newSession(time.Now().Add(-time.Minute), "old-refresh-token"))
	rec := httptest.NewRecorder()

	// Act
	a.HandleToken()(rec, req)

	// Assert: 401 (which the web UI already turns into a login redirect) and the
	// dead cookie is evicted rather than left to fail on every request.
	assert.Equal(t, rec.Code, http.StatusUnauthorized)
	cookie := sessionCookie(rec)
	assert.Assert(t, cookie != nil)
	assert.Equal(t, cookie.MaxAge, -1)
}

func TestWriteSession_RenewableCookieOutlivesIDToken(t *testing.T) {
	t.Parallel()
	// Arrange: a session with a refresh token and a short-lived ID token.
	a := &Auth{encryptionKey: testEncryptionKey}
	rec := httptest.NewRecorder()

	// Act
	assert.NilError(t, a.writeSession(rec, newSession(time.Now().Add(12*time.Hour), "refresh-token")))

	// Assert: the cookie lasts the full session lifetime. If it expired with the
	// ID token, a user away for longer than 12h would return to no cookie at all
	// and the refresh token could never be spent.
	assert.Equal(t, sessionCookie(rec).MaxAge, int(sessionLifetime.Seconds()))
}

func TestWriteSession_UnrenewableCookieTracksIDToken(t *testing.T) {
	t.Parallel()
	// Arrange: a session with no refresh token, e.g. one minted before
	// offline_access was requested.
	a := &Auth{encryptionKey: testEncryptionKey}
	rec := httptest.NewRecorder()

	// Act
	assert.NilError(t, a.writeSession(rec, newSession(time.Now().Add(time.Hour), "")))

	// Assert: nothing to renew with, so it keeps zitadel-go's exp-derived MaxAge
	// rather than lingering past the point it can be used.
	maxAge := sessionCookie(rec).MaxAge
	assert.Assert(t, maxAge > 0 && maxAge <= 3600, "MaxAge was %d", maxAge)
}

// stubChecker stands in for zitadel-go's authenticator so the middleware paths can
// be exercised without an IdP handshake.
type stubChecker struct {
	authCtx   *openid.DefaultContext
	redirects atomic.Int64
}

func (s *stubChecker) IsAuthenticated(*http.Request) (*openid.DefaultContext, error) {
	if s.authCtx == nil {
		return nil, errors.New("no session")
	}
	return s.authCtx, nil
}

func (s *stubChecker) Authenticate(w http.ResponseWriter, r *http.Request, requestedURI string) {
	s.redirects.Add(1)
	http.Redirect(w, r, "https://idp.example/oauth/v2/authorize", http.StatusFound)
}

func TestRequireAuth_RenewsExpiredSessionInsteadOfRedirecting(t *testing.T) {
	t.Parallel()
	// Arrange: an expired session that zitadel-go rejects (as v3.30.0 does), but
	// whose refresh token still works. /ui/ sits behind this middleware, so this is
	// the path a user returning after an absence actually takes.
	idp := newFakeIDP(t)
	a := newTestAuth(t, idp)
	stub := &stubChecker{}
	a.cookie = authentication.Middleware[*openid.DefaultContext](stub)

	var caller *UserInfo
	handler := a.RequireAuth()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller = Caller(r.Context())
	}))
	req := requestWithSession(t, a, newSession(time.Now().Add(-time.Minute), "old-refresh-token"))
	rec := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(rec, req)

	// Assert: served, not bounced to the IdP login page.
	assert.Equal(t, rec.Code, http.StatusOK)
	assert.Equal(t, stub.redirects.Load(), int64(0))
	assert.Assert(t, caller != nil)
	assert.Equal(t, caller.ID, "user-1")
	assert.Equal(t, caller.Type, AuthTypeCookie)
	assert.Equal(t, idp.calls.Load(), int64(1))
}

func TestRequireAuth_UnrenewableSessionRedirects(t *testing.T) {
	t.Parallel()
	// Arrange: the same request, but the refresh token is dead.
	idp := newFakeIDP(t)
	idp.fail = true
	a := newTestAuth(t, idp)
	stub := &stubChecker{}
	a.cookie = authentication.Middleware[*openid.DefaultContext](stub)

	handler := a.RequireAuth()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not run for an unrenewable session")
	}))
	req := requestWithSession(t, a, newSession(time.Now().Add(-time.Minute), "old-refresh-token"))
	rec := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(rec, req)

	// Assert: falls through to the normal login redirect.
	assert.Equal(t, rec.Code, http.StatusFound)
	assert.Equal(t, stub.redirects.Load(), int64(1))
}

func TestRequireAuth_ValidSessionSkipsRenewal(t *testing.T) {
	t.Parallel()
	// Arrange: a healthy session nowhere near expiry.
	idp := newFakeIDP(t)
	a := newTestAuth(t, idp)
	session := newSession(time.Now().Add(12*time.Hour), "refresh-token")
	a.cookie = authentication.Middleware[*openid.DefaultContext](&stubChecker{authCtx: session})

	var caller *UserInfo
	handler := a.RequireAuth()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller = Caller(r.Context())
	}))
	rec := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(rec, requestWithSession(t, a, session))

	// Assert: served through the library middleware, with no token endpoint traffic
	// on the hot path.
	assert.Equal(t, rec.Code, http.StatusOK)
	assert.Assert(t, caller != nil)
	assert.Equal(t, idp.calls.Load(), int64(0))
}
