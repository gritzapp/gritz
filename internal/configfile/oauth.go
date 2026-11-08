package configfile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

// ClientID is the OAuth client id the CLI uses.
const ClientID = "gritz-cli"

// rotateAge is how old the stored refresh token must be before a refresh
// writes the rotated one back.
const rotateAge = 24 * time.Hour

// OAuthConfig returns the OAuth client config for server, with redirectURL as
// the redirect URI (empty for refresh only).
func OAuthConfig(server, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID: ClientID,
		Endpoint: oauth2.Endpoint{
			AuthURL:   server + "/ui/oauth/authorize",
			TokenURL:  server + "/oauth/token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
		RedirectURL: redirectURL,
	}
}

// TokenSource returns the entry's credentials: the key as a static token, or
// access tokens refreshed from RefreshToken, writing rotated refresh tokens
// back to Path.
func (e *Entry) TokenSource(ctx context.Context) oauth2.TokenSource {
	if e.Key != "" {
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: e.Key})
	}
	return oauth2.ReuseTokenSource(nil, &refreshSource{
		ctx:     ctx,
		config:  OAuthConfig(e.Server, ""),
		entry:   e,
		current: e.RefreshToken,
		stored:  e.RefreshToken,
	})
}

// refreshSource exchanges the entry's refresh token for an access token on
// every call. oauth2.ReuseTokenSource serialises the calls.
type refreshSource struct {
	ctx    context.Context
	config *oauth2.Config
	entry  *Entry
	// current is the refresh token used for the next refresh.
	current string
	// stored is the refresh token last written to entry.Path.
	stored string
}

func (s *refreshSource) Token() (*oauth2.Token, error) {
	tok, err := s.config.TokenSource(s.ctx, &oauth2.Token{RefreshToken: s.current}).Token()
	if err != nil {
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
	s.current = tok.RefreshToken
	if s.current != s.stored && issuedBefore(s.stored, time.Now().Add(-rotateAge)) {
		if err := Rotate(s.entry.Path, s.entry.Name, s.stored, s.current); err != nil {
			slog.Warn("failed to write rotated refresh token", "path", s.entry.Path, "org", s.entry.Name, "err", err)
		} else {
			s.stored = s.current
		}
	}
	return tok, nil
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
