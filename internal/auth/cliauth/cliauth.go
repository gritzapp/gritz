// Package cliauth is the gritz CLI's OAuth client for the gritz server.
package cliauth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

// ClientID is the OAuth client id the gritz CLI uses.
const ClientID = "gritz-cli"

// reportAge is how old the last reported refresh token must be before a
// rotated one is reported.
const reportAge = 24 * time.Hour

// ErrLoginExpired is returned when the server rejects the refresh token.
var ErrLoginExpired = errors.New("login expired or revoked")

// Config returns the OAuth client config for the gritz server at server, with
// redirectURL as the redirect URI (empty for refresh only).
func Config(server, redirectURL string) *oauth2.Config {
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

// TokenSourceOptions configures TokenSource.
type TokenSourceOptions struct {
	// Server is the gritz server URL.
	Server string
	// RefreshToken is the refresh token to start from.
	RefreshToken string
	// OnRefreshToken, if set, is called with a rotated refresh token once the
	// one last reported (initially RefreshToken) is more than a day old, read
	// from its iat. Calls are serialised.
	OnRefreshToken func(token string)
}

// TokenSource returns a TokenSource that refreshes access tokens from
// opts.RefreshToken, caching each until it expires.
func TokenSource(ctx context.Context, opts TokenSourceOptions) oauth2.TokenSource {
	return oauth2.ReuseTokenSource(nil, &refreshSource{
		ctx:      ctx,
		config:   Config(opts.Server, ""),
		opts:     opts,
		current:  opts.RefreshToken,
		reported: opts.RefreshToken,
	})
}

// refreshSource refreshes on every call; oauth2.ReuseTokenSource caches its
// tokens and serialises the calls.
type refreshSource struct {
	ctx      context.Context
	config   *oauth2.Config
	opts     TokenSourceOptions
	current  string // the refresh token used for the next refresh
	reported string // the refresh token last reported
}

func (s *refreshSource) Token() (*oauth2.Token, error) {
	tok, err := s.config.TokenSource(s.ctx, &oauth2.Token{RefreshToken: s.current}).Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.Response.StatusCode == http.StatusBadRequest {
			return nil, ErrLoginExpired
		}
		return nil, err
	}
	s.current = tok.RefreshToken
	if s.opts.OnRefreshToken != nil && s.current != s.reported && issuedBefore(s.reported, time.Now().Add(-reportAge)) {
		s.opts.OnRefreshToken(s.current)
		s.reported = s.current
	}
	return tok, nil
}

// issuedBefore reports whether the JWT token was issued before t, read
// without verification. A token without a readable iat counts as issued
// before t.
func issuedBefore(token string, t time.Time) bool {
	claims := jwt.RegisteredClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, &claims); err != nil || claims.IssuedAt == nil {
		return true
	}
	return claims.IssuedAt.Before(t)
}
