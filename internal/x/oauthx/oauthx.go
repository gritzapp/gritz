// Package oauthx provides OAuth client helpers on top of golang.org/x/oauth2.
package oauthx

import (
	"context"

	"golang.org/x/oauth2"
)

// RefreshSourceOptions configures NewRefreshSource.
type RefreshSourceOptions struct {
	// Config is the OAuth client config whose token endpoint refreshes.
	Config *oauth2.Config
	// RefreshToken is the refresh token to start from.
	RefreshToken string
	// OnNewRefreshToken is called whenever a refresh returns a refresh token
	// different from the one in use. Later refreshes use the new token.
	// Calls are serialised. It is optional.
	OnNewRefreshToken func(token string)
}

// NewRefreshSource returns a TokenSource that exchanges the refresh token for
// access tokens, caching each until it expires.
func NewRefreshSource(ctx context.Context, opts RefreshSourceOptions) oauth2.TokenSource {
	return oauth2.ReuseTokenSource(nil, &refreshSource{ctx: ctx, opts: opts, token: opts.RefreshToken})
}

// refreshSource refreshes on every call; oauth2.ReuseTokenSource caches its
// tokens and serialises the calls.
type refreshSource struct {
	ctx   context.Context
	opts  RefreshSourceOptions
	token string // the refresh token in use
}

func (s *refreshSource) Token() (*oauth2.Token, error) {
	tok, err := s.opts.Config.TokenSource(s.ctx, &oauth2.Token{RefreshToken: s.token}).Token()
	if err != nil {
		return nil, err
	}
	if tok.RefreshToken != s.token {
		s.token = tok.RefreshToken
		if s.opts.OnNewRefreshToken != nil {
			s.opts.OnNewRefreshToken(s.token)
		}
	}
	return tok, nil
}
