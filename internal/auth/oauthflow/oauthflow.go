package oauthflow

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"fmt"
	"net/url"
	"time"
)

// OrgResolver validates that a user can act in the requested org and returns
// the resolved org id. It rejects archived orgs and users who are no longer
// members.
type OrgResolver interface {
	ResolveOrg(ctx context.Context, userID string, orgID int64) (int64, error)
}

// Options configures the OAuth 2.1 authorization flow.
type Options struct {
	AppKey          ed25519.PrivateKey
	OrgResolver     OrgResolver
	BaseURL         string
	AuthCodeTTL     time.Duration
	RefreshTokenTTL time.Duration
}

// Auth implements the OAuth 2.1 authorization code flow with PKCE.
type Auth struct {
	appKey          ed25519.PrivateKey
	orgResolver     OrgResolver
	baseURL         *url.URL
	authCodeTTL     time.Duration
	refreshTokenTTL time.Duration
}

// New creates a new OAuth flow handler.
func New(opts Options) (*Auth, error) {
	u, err := url.Parse(opts.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	return &Auth{
		appKey:          opts.AppKey,
		orgResolver:     opts.OrgResolver,
		baseURL:         u,
		authCodeTTL:     cmp.Or(opts.AuthCodeTTL, DefaultAuthCodeTTL),
		refreshTokenTTL: cmp.Or(opts.RefreshTokenTTL, DefaultRefreshTokenTTL),
	}, nil
}
