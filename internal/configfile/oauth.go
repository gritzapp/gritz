package configfile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gritzapp/gritz/internal/x/oauthx"
	"golang.org/x/oauth2"
)

// ClientID is the OAuth client id the CLI uses.
const ClientID = "gritz-cli"

// OAuthConfig returns the OAuth client config for server, with redirectURL as
// the redirect URI (empty for refresh only).
func OAuthConfig(server, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID: ClientID,
		Endpoint: oauth2.Endpoint{
			AuthURL:  server + "/ui/oauth/authorize",
			TokenURL: server + "/oauth/token",
			// The server reads client_id from the form; this also keeps a
			// rejected request from being retried with the other style.
			AuthStyle: oauth2.AuthStyleInParams,
		},
		RedirectURL: redirectURL,
	}
}

// rotateAge is how old the stored refresh token must be before a new one
// is written back.
const rotateAge = 24 * time.Hour

// TokenSource returns the entry's credentials: the key as a static token, or
// access tokens refreshed from RefreshToken, writing new refresh tokens back
// to Path once the stored one is more than a day old.
func (e *Entry) TokenSource(ctx context.Context) oauth2.TokenSource {
	if e.Key != "" {
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: e.Key})
	}
	stored := e.RefreshToken
	return &entrySource{entry: e, src: oauthx.NewRefreshSource(ctx, oauthx.RefreshSourceOptions{
		Config:       OAuthConfig(e.Server, ""),
		RefreshToken: e.RefreshToken,
		OnNewRefreshToken: func(token string) {
			if !issuedBefore(stored, time.Now().Add(-rotateAge)) {
				return
			}
			if err := UpdateEntry(e.Path, e.Name, rotate(stored, token)); err != nil {
				slog.Warn("failed to write new refresh token", "path", e.Path, "org", e.Name, "err", err)
				return
			}
			stored = token
		},
	})}
}

// entrySource names the entry in refresh errors, and tells the user to log
// in again when the server rejects the refresh token.
type entrySource struct {
	entry *Entry
	src   oauth2.TokenSource
}

func (s *entrySource) Token() (*oauth2.Token, error) {
	tok, err := s.src.Token()
	if err == nil {
		return tok, nil
	}
	var re *oauth2.RetrieveError
	if errors.As(err, &re) && re.Response.StatusCode == http.StatusBadRequest {
		login := "gritz login --name " + s.entry.Name
		if s.entry.Server != DefaultServer {
			login += " --server " + s.entry.Server
		}
		return nil, fmt.Errorf("org entry %q: login expired or revoked: run `%s`", s.entry.Name, login)
	}
	return nil, fmt.Errorf("org entry %q: refresh: %w", s.entry.Name, err)
}

// issuedBefore reports whether the JWT token was issued before t, read
// without verification. A token without a readable iat counts as issued
// before t, so it is replaced.
func issuedBefore(token string, t time.Time) bool {
	claims := jwt.RegisteredClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, &claims); err != nil || claims.IssuedAt == nil {
		return true
	}
	return claims.IssuedAt.Before(t)
}
