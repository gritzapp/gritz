package apiauth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/zitadel/oidc/v3/pkg/client/rp"
	"github.com/zitadel/oidc/v3/pkg/crypto"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	openid "github.com/zitadel/zitadel-go/v3/pkg/authentication/oidc"
)

// sessionCookieName is zitadel-go's default, pinned explicitly in New so that
// readSession and writeSession stay in sync with the library's own codec.
const sessionCookieName = "zitadel.session"

// sessionLifetime is how long the session cookie survives in the browser. It is
// deliberately decoupled from the ID token's exp: tying the two together (as
// zitadel-go does) means a user who closes the tab for longer than the ID token's
// lifetime comes back to a cookie the browser has already discarded, so the
// refresh token never gets a chance to be spent and "stay signed in" cannot work.
//
// Each renewal rewrites the cookie, rolling this window forward, so an active user
// stays signed in indefinitely. The ceiling is ZITADEL's, not ours: this must not
// exceed the instance's refresh token *idle* expiration, or the cookie outlives the
// only credential that can renew it. The absolute refresh token expiration is the
// hard stop at which re-authentication is unavoidable.
const sessionLifetime = 30 * 24 * time.Hour

// renewBefore is how long before the ID token's exp a session is renewed.
// Renewal is driven by /auth/token, which the web UI calls at least every
// AppTokenTTL while a tab is open, so an active session renews once per window
// with an hour of slack. The slack is the point: a renewal that fails has room
// to be retried on the next call instead of being terminal.
const renewBefore = time.Hour

// readSession decrypts the session cookie into an auth context. It deliberately
// does not check IsAuthenticated: an expired session is still renewable, and
// refusing to decode it here would make expiry unrecoverable. Authority rests on
// the refresh token, which ZITADEL validates.
func (a *Auth) readSession(r *http.Request) (*openid.DefaultContext, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil, err
	}
	raw, err := crypto.DecryptAES(cookie.Value, a.encryptionKey)
	if err != nil {
		return nil, err
	}
	var authCtx openid.DefaultContext
	if err := json.Unmarshal([]byte(raw), &authCtx); err != nil {
		return nil, err
	}
	return &authCtx, nil
}

// writeSession encrypts the auth context into the session cookie. The attributes
// mirror zitadel-go's setSessionCookie, apart from MaxAge, so a renewed cookie is
// otherwise indistinguishable from a freshly minted one.
func (a *Auth) writeSession(w http.ResponseWriter, authCtx *openid.DefaultContext) error {
	data, err := json.Marshal(authCtx)
	if err != nil {
		return err
	}
	value, err := crypto.EncryptAES(string(data), a.encryptionKey)
	if err != nil {
		return err
	}
	// A session that can be renewed should outlive its ID token; one that cannot
	// has nothing to gain from it, so it keeps zitadel-go's exp-derived MaxAge.
	maxAge := int(sessionLifetime.Seconds())
	if authCtx.Tokens == nil || authCtx.Tokens.RefreshToken == "" {
		maxAge = 0
		if exp := authCtx.GetExpiration(); !exp.IsZero() {
			// Avoid rounding down to 0, which would turn it into a browser-session cookie
			maxAge = max(int(time.Until(exp).Seconds()), 1)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// renewSession keeps a cookie session alive by exchanging its refresh token for a
// fresh token set, which doubles as a liveness check against the IdP: ZITADEL only
// honours the grant while the user still exists, is active, and holds the grant.
//
// It is best-effort by design. A session that is still within its ID token's
// validity is served as-is when renewal fails, because the alternative — treating
// every token endpoint hiccup as "this user is gone" — logs people out for reasons
// that have nothing to do with their account. Only an unrenewable *expired*
// session is fatal, and that is reported by returning errSessionExpired.
//
// Renewal is skipped entirely when the session is nowhere near expiry, has no
// refresh token (no offline_access, or a session minted before this landed), or
// when there is no IdP to talk to (dev-user mode).
func (a *Auth) renewSession(w http.ResponseWriter, r *http.Request) (*openid.DefaultContext, error) {
	if a.relyingParty == nil {
		return nil, nil
	}
	authCtx, err := a.readSession(r)
	if err != nil {
		// No cookie, or one we cannot decode. Not our problem to report: the
		// caller's normal unauthenticated path handles it.
		return nil, nil
	}
	expired := !authCtx.IsAuthenticated()
	if !expired && time.Until(authCtx.GetExpiration()) > renewBefore {
		return nil, nil
	}
	refreshToken := ""
	if authCtx.Tokens != nil {
		refreshToken = authCtx.Tokens.RefreshToken
	}
	if refreshToken == "" {
		if expired {
			return nil, errSessionExpired
		}
		return nil, nil
	}

	// Rotation means only one caller can spend a given refresh token. Collapsing
	// concurrent renewals on the token itself leaves no loser to recover: every
	// waiter gets the winner's result and writes the same cookie. This holds only
	// while the server is a single process, which it is today (one Dokku app on
	// one droplet); scaling out would need the sessions in Postgres instead.
	tokens, err := a.renew(r, refreshToken)
	if err != nil {
		slog.Warn("session renewal failed", "err", err, "expired", expired)
		if expired {
			return nil, errSessionExpired
		}
		// Still valid — serve the request and leave the cookie untouched. It must
		// not be rewritten here: a concurrent renewal may already have installed a
		// newer one in the browser, and overwriting it with our stale copy would
		// turn a harmless race into a broken session.
		return nil, nil
	}

	authCtx.SetTokens(tokens)
	// v3.30.0 derives both the cookie MaxAge and IsAuthenticated from
	// Tokens.IDTokenClaims, so a renewal that does not carry new claims would be
	// evicted at the original exp regardless of the new tokens' validity.
	if tokens.IDTokenClaims == nil {
		slog.Warn("renewed session has no id_token claims; session will expire at its original exp")
	}
	if err := a.writeSession(w, authCtx); err != nil {
		return nil, err
	}
	return authCtx, nil
}

// clearSession deletes the session cookie, mirroring zitadel-go's
// deleteSessionCookie so an unrenewable session does not linger in the browser.
func (a *Auth) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Path:     "/",
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// renew performs the refresh token grant, collapsing concurrent calls for the
// same token into one round trip.
func (a *Auth) renew(r *http.Request, refreshToken string) (*oidc.Tokens[*oidc.IDTokenClaims], error) {
	// Keyed on the refresh token because that is exactly what rotation
	// invalidates: two requests sharing one are the two that would collide.
	// Callers are cookie-authenticated, so the key is not attacker-chosen.
	result, err, _ := a.renewals.Do(refreshToken, func() (any, error) {
		return rp.RefreshTokens[*oidc.IDTokenClaims](r.Context(), a.relyingParty, refreshToken, "", "")
	})
	if err != nil {
		return nil, err
	}
	tokens, ok := result.(*oidc.Tokens[*oidc.IDTokenClaims])
	if !ok || tokens == nil {
		return nil, errors.New("refresh returned no tokens")
	}
	return tokens, nil
}

// errSessionExpired means the session is past its ID token's exp and could not be
// renewed, so the user must authenticate again.
var errSessionExpired = errors.New("session expired")

// renewSessionQuietly renews a cookie session on the request-serving path, where
// there is no way to report an error to the user that is better than the normal
// unauthenticated redirect. It returns the renewed context, or nil if the session
// did not need renewing, could not be renewed, or does not exist.
func (a *Auth) renewSessionQuietly(w http.ResponseWriter, r *http.Request) *openid.DefaultContext {
	renewed, err := a.renewSession(w, r)
	if err != nil {
		// errSessionExpired included: the caller falls through to the library
		// middleware, which rejects the session and redirects to login.
		return nil
	}
	return renewed
}
