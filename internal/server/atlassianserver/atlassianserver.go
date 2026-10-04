// Package atlassianserver handles Atlassian OAuth account linking
// and Jira webhook processing.
package atlassianserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/auth/oauthlink"
	"github.com/gritzapp/gritz/internal/server/eventrouter"
	"github.com/gritzapp/gritz/internal/server/pubsub"
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/x/atlassian"
	"golang.org/x/oauth2"
)

// Server handles Atlassian OAuth and webhook routes.
type Server struct {
	log          *slog.Logger
	store        *store.Store
	baseURL      string
	clientID     string
	clientSecret string
	publisher    pubsub.Publisher
}

// Options configures a Server.
type Options struct {
	Log          *slog.Logger
	Store        *store.Store
	BaseURL      string
	ClientID     string
	ClientSecret string
	Publisher    pubsub.Publisher
}

// New returns a new Server.
func New(opts Options) *Server {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		log:          log,
		store:        opts.Store,
		baseURL:      opts.BaseURL,
		clientID:     opts.ClientID,
		clientSecret: opts.ClientSecret,
		publisher:    opts.Publisher,
	}
}

// OAuthLink returns the OAuth link handler for Atlassian account linking.
func (s *Server) OAuthLink() *oauthlink.Handler {
	return oauthlink.New(oauthlink.Config{
		Provider:     "atlassian",
		ClientID:     s.clientID,
		ClientSecret: s.clientSecret,
		RedirectURL:  s.baseURL + "/atlassian/callback",
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://auth.atlassian.com/authorize",
			TokenURL: "https://auth.atlassian.com/oauth/token",
		},
		Scopes: []string{"read:me"},
		AuthParams: []oauth2.AuthCodeOption{
			oauth2.SetAuthURLParam("audience", "api.atlassian.com"),
			oauth2.SetAuthURLParam("prompt", "consent"),
		},
		Log: s.log,
		OnSuccess: func(w http.ResponseWriter, r *http.Request, token *oauth2.Token) {
			caller := apiauth.Caller(r.Context())
			if caller == nil {
				http.Error(w, "not authenticated", http.StatusUnauthorized)
				return
			}
			if caller.ID == "" {
				http.Error(w, "this operation requires a user identity", http.StatusForbidden)
				return
			}
			me, err := atlassian.FetchMe(r.Context(), token.AccessToken)
			if err != nil {
				s.log.Error("failed to fetch Atlassian user", "err", err)
				http.Error(w, "failed to fetch Atlassian user", http.StatusInternalServerError)
				return
			}
			if err := s.store.LinkAtlassianAccount(r.Context(), nil, caller.ID, me.AccountID, me.Name); err != nil {
				http.Error(w, "failed to link Atlassian account", http.StatusInternalServerError)
				return
			}
			http.Redirect(w, r, "/ui/settings", http.StatusFound)
		},
	})
}

// WebhookHandler returns the HTTP handler for Atlassian/Jira webhook events.
func (s *Server) WebhookHandler() http.Handler {
	return &WebhookHandler{
		Router: &eventrouter.Router{Log: s.log, Store: s.store, Publisher: s.publisher},
		Store:  s.store,
	}
}

// WebhookURL returns the webhook URL for the given org.
func (s *Server) WebhookURL(orgID int64) string {
	return fmt.Sprintf("%s/webhook/atlassian?org=%d", s.baseURL, orgID)
}

// UnlinkAccount removes the Atlassian account link for the given user.
func (s *Server) UnlinkAccount(ctx context.Context, userID string) error {
	return s.store.UnlinkAtlassianAccount(ctx, nil, userID)
}

// GenerateWebhookSecret generates and stores a new webhook secret for the given org.
func (s *Server) GenerateWebhookSecret(ctx context.Context, orgID int64) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	secret := hex.EncodeToString(b)
	if err := s.store.SetOrgAtlassianWebhookSecret(ctx, nil, orgID, secret); err != nil {
		return "", err
	}
	s.log.Info("atlassian webhook secret generated", "org_id", orgID)
	return secret, nil
}

// GetWebhookSecret returns the webhook secret for the given org.
func (s *Server) GetWebhookSecret(ctx context.Context, orgID int64) (string, error) {
	return s.store.GetOrgAtlassianWebhookSecret(ctx, nil, orgID)
}
