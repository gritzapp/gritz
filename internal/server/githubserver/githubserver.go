// Package githubserver handles GitHub App OAuth account linking and
// webhook event routing.
package githubserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v88/github"
	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/auth/oauthlink"
	"github.com/gritzapp/gritz/internal/server/eventrouter"
	"github.com/gritzapp/gritz/internal/server/pubsub"
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/x/githubx"
	"golang.org/x/oauth2"
	oauth2github "golang.org/x/oauth2/github"
)

// Config holds GitHub App configuration.
type Config struct {
	AppID         string
	AppSlug       string
	ClientID      string
	ClientSecret  string
	WebhookSecret string
	PrivateKey    []byte
}

// Server handles GitHub OAuth and webhook routes.
type Server struct {
	log       *slog.Logger
	config    *Config
	store     *store.Store
	baseURL   string
	publisher pubsub.Publisher
	app       *ghinstallation.AppsTransport
	tokens    *githubx.AppTokenCache
}

// Options configures a Server.
type Options struct {
	Log       *slog.Logger
	Config    *Config
	Store     *store.Store
	BaseURL   string
	Publisher pubsub.Publisher
}

// New returns a new Server. The GitHub App ID and private key are parsed
// up-front so configuration errors surface at startup rather than the first
// token request.
func New(opts Options) (*Server, error) {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	if opts.Config == nil {
		return nil, fmt.Errorf("config is required")
	}
	appID, err := strconv.ParseInt(opts.Config.AppID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid GitHub App ID: %w", err)
	}
	key, err := githubx.ParsePrivateKey(opts.Config.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse GitHub App private key: %w", err)
	}
	tokens, err := githubx.NewAppTokenCache(githubx.AppTokenCacheOptions{
		AppID:     appID,
		Key:       key,
		Transport: http.DefaultTransport,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create GitHub App token cache: %w", err)
	}
	return &Server{
		log:       log,
		config:    opts.Config,
		store:     opts.Store,
		baseURL:   opts.BaseURL,
		publisher: opts.Publisher,
		app:       ghinstallation.NewAppsTransportFromPrivateKey(http.DefaultTransport, appID, key),
		tokens:    tokens,
	}, nil
}

// InstallationToken is a short-lived GitHub App installation access token.
type InstallationToken struct {
	Token     string
	ExpiresAt time.Time
}

// CreateInstallationToken creates a GitHub App installation access token.
func (s *Server) CreateInstallationToken(ctx context.Context, installationID int64) (*InstallationToken, error) {
	transport := ghinstallation.NewFromAppsTransport(s.app, installationID)
	token, err := transport.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create GitHub installation token: %w", err)
	}
	expiresAt, _, err := transport.Expiry()
	if err != nil {
		return nil, fmt.Errorf("failed to get token expiry: %w", err)
	}
	return &InstallationToken{Token: token, ExpiresAt: expiresAt}, nil
}

// AppInstallURL returns the GitHub App installation URL, or empty string
// if no app slug is configured.
func (s *Server) AppInstallURL() string {
	if s.config.AppSlug == "" {
		return ""
	}
	return fmt.Sprintf("https://github.com/apps/%s/installations/new", s.config.AppSlug)
}

// OAuthLink returns the OAuth link handler for GitHub account linking.
func (s *Server) OAuthLink() *oauthlink.Handler {
	return oauthlink.New(oauthlink.Config{
		Provider:     "github",
		ClientID:     s.config.ClientID,
		ClientSecret: s.config.ClientSecret,
		RedirectURL:  s.baseURL + "/github/callback",
		Endpoint:     oauth2github.Endpoint,
		Scopes:       []string{"read:user"},
		Log:          s.log,
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
			ghClient, err := github.NewClient(github.WithAuthToken(token.AccessToken))
			if err != nil {
				s.log.Error("failed to create GitHub client", "err", err)
				http.Error(w, "failed to create GitHub client", http.StatusInternalServerError)
				return
			}
			ghUser, _, err := ghClient.Users.Get(r.Context(), "")
			if err != nil {
				s.log.Error("failed to fetch GitHub user", "err", err)
				http.Error(w, "failed to fetch GitHub user", http.StatusInternalServerError)
				return
			}
			if err := s.store.LinkGitHubAccount(r.Context(), nil, caller.ID, ghUser.GetID(), ghUser.GetLogin()); err != nil {
				http.Error(w, "failed to link GitHub account", http.StatusInternalServerError)
				return
			}
			http.Redirect(w, r, "/ui/settings", http.StatusFound)
		},
	})
}

// WebhookHandler returns the HTTP handler for GitHub App webhook events.
func (s *Server) WebhookHandler() http.Handler {
	return &WebhookHandler{
		Router: &eventrouter.Router{
			Log:       s.log,
			Store:     s.store,
			Publisher: s.publisher,
			OnRouteOutcome: func(ctx context.Context, o eventrouter.RouteOutcome) {
				// react is synchronous; the goroutine + detached context +
				// timeout keep a slow GitHub round-trip off the webhook path.
				go func() {
					ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
					defer cancel()
					if err := s.react(ctx, o); err != nil {
						s.log.Warn("github reaction failed", "org_id", o.OrgID, "url", o.Input.URL, "err", err)
					}
				}()
			},
		},
		Store:         s.store,
		WebhookSecret: s.config.WebhookSecret,
	}
}
