package server

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/auth/oauthflow"
	"github.com/gritzapp/gritz/internal/proto/gritz/v1/gritzv1connect"
	"github.com/gritzapp/gritz/internal/server/apiserver"
	"github.com/gritzapp/gritz/internal/server/atlassianserver"
	"github.com/gritzapp/gritz/internal/server/eventrouter"
	"github.com/gritzapp/gritz/internal/server/githubserver"
	"github.com/gritzapp/gritz/internal/server/mcpserver"
	"github.com/gritzapp/gritz/internal/server/notifyserver"
	"github.com/gritzapp/gritz/internal/server/pubsub"
	"github.com/gritzapp/gritz/internal/server/shellserver"
	"github.com/gritzapp/gritz/internal/shell"
	"github.com/gritzapp/gritz/internal/shell/shellrelay"
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/x/otelx"
	"github.com/justinas/alice"
)

type Server struct {
	log       *slog.Logger
	api       *apiserver.Server
	auth      *apiauth.Auth
	github    *githubserver.Server
	atlassian *atlassianserver.Server
	baseURL   string
	oauth     *oauthflow.Auth
	cors      bool
	notify    *notifyserver.Server
	shell     *shellserver.Registry
}

type Options struct {
	Log           *slog.Logger
	Store         *store.Store
	Auth          *apiauth.Auth
	GitHub        *githubserver.Server
	Atlassian     *atlassianserver.Server
	BaseURL       string
	EncryptionKey []byte
	OAuth         *oauthflow.Auth
	CORS          bool
	Publisher     pubsub.Publisher
	Notify        *notifyserver.Server
	AppKey        ed25519.PrivateKey
	// OrgResolver authorizes cookie-authenticated (browser) operators on the
	// shell attach leg, the same way it does for the SSE stream.
	OrgResolver shellserver.OrgResolver
}

func New(opts Options) *Server {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	// When a debug-shell rendezvous tears down, clear the task's shell_session so
	// a later restart of that task boots the agent rather than another shell. Best
	// effort: log a warning on error, never fail teardown on it.
	onShellClose := func(session string, orgID int64) {
		if err := opts.Store.ClearShellSession(context.Background(), nil, session, orgID); err != nil {
			log.Warn("failed to clear shell_session", "session", session, "org", orgID, "err", err)
		}
	}
	shell := shellserver.New(shellserver.Options{
		Log:              log,
		EstablishTimeout: shellrelay.DefaultEstablishTimeout,
		IdleTimeout:      shellrelay.DefaultIdleTimeout,
		OnClose:          onShellClose,
		OrgResolver:      opts.OrgResolver,
	})
	apiOpts := apiserver.Options{
		Log:       log,
		Store:     opts.Store,
		BaseURL:   opts.BaseURL,
		Publisher: opts.Publisher,
		Atlassian: opts.Atlassian,
		AppKey:    opts.AppKey,
		Shells:    shell,
		// Router powers TestEvent's dry-run matcher. It only reads (Router.Plan),
		// so OnRouteOutcome is intentionally left unset.
		Router: &eventrouter.Router{
			Log:       log,
			Store:     opts.Store,
			Publisher: opts.Publisher,
		},
	}
	// Only populate GitHub when configured: assigning a typed-nil
	// *githubserver.Server into the interface field would make
	// apiserver's s.github != nil ("configured") check spuriously true.
	if opts.GitHub != nil {
		apiOpts.GitHub = opts.GitHub
	}
	api := apiserver.New(apiOpts)
	return &Server{
		log:       log,
		api:       api,
		auth:      opts.Auth,
		github:    opts.GitHub,
		atlassian: opts.Atlassian,
		baseURL:   opts.BaseURL,
		oauth:     opts.OAuth,
		cors:      opts.CORS,
		notify:    opts.Notify,
		shell:     shell,
	}
}

func (s *Server) Handler() http.Handler {
	mux := otelx.NewMux("gritz")
	// App JWT token endpoint (cookie-authenticated)
	mux.Handle("/auth/token", alice.New(s.auth.CheckAuth()).Then(s.auth.HandleToken()))
	// Auth routes (login, callback, logout)
	mux.Handle("/auth/", s.auth.Handler())
	// Connect RPC API (protected)
	// HTTP middleware checks auth and attaches UserInfo to context
	// Connect interceptor enforces auth with proper RPC error responses
	otelInterceptor, err := otelconnect.NewInterceptor()
	if err != nil {
		s.log.Error("failed to create otelconnect interceptor", "err", err)
	}
	path, handler := gritzv1connect.NewGritzServiceHandler(s.api,
		connect.WithInterceptors(otelInterceptor, apiauth.RequireUserInterceptor(), apiauth.ObservabilityInterceptor()),
	)
	mux.Handle(path, alice.New(s.auth.CheckAuth()).Then(handler))
	// SSE endpoint (protected)
	if s.notify != nil {
		mux.Handle("/events", alice.New(s.auth.CheckAuth()).Then(s.notify.Handler()))
	}
	// GitHub App routes (conditionally registered)
	if s.github != nil {
		link := s.github.OAuthLink()
		mux.Handle("/github/login", alice.New(s.auth.RequireAuth()).ThenFunc(link.HandleLogin))
		mux.Handle("/github/callback", alice.New(s.auth.RequireAuth()).ThenFunc(link.HandleCallback))
		mux.Handle("/webhook/github", s.github.WebhookHandler())
	}
	// Atlassian OAuth routes (conditionally registered)
	if s.atlassian != nil {
		link := s.atlassian.OAuthLink()
		mux.Handle("/atlassian/login", alice.New(s.auth.RequireAuth()).ThenFunc(link.HandleLogin))
		mux.Handle("/atlassian/callback", alice.New(s.auth.RequireAuth()).ThenFunc(link.HandleCallback))
		mux.Handle("/webhook/atlassian", s.atlassian.WebhookHandler())
	}
	// OAuth 2.1 endpoints (public, conditionally registered)
	if s.oauth != nil {
		mux.HandleFunc("/.well-known/oauth-authorization-server", s.oauth.HandleMetadata)
		mux.HandleFunc("/.well-known/oauth-protected-resource", s.oauth.HandleResourceMetadata)
		mux.HandleFunc("/oauth/register", s.oauth.HandleRegister)
		mux.HandleFunc("/oauth/authorize", s.oauth.HandleAuthorize)
		mux.HandleFunc("/oauth/token", s.oauth.HandleToken)
	}
	// Shell rendezvous relay (step 2 of the driver reverse shell, #1113).
	// The driver leg rides Bearer auth with its task token. The operator (attach)
	// leg serves two clients: the CLI with a Bearer token whose org claim must
	// match the session's owning org, and the browser with a same-origin
	// WebSocket that carries only the cookie session (no Authorization header).
	// So the attach route mirrors the /events SSE wiring — CheckAuth() populates
	// the caller but does not redirect, so an unauthenticated WebSocket gets a
	// clean pre-upgrade 401 rather than a 302 to the login page.
	mux.Handle(shell.DriverRoute, alice.New(s.auth.RequireAuth()).Then(s.shell.DriverHandler()))
	mux.Handle(shell.AttachRoute, alice.New(s.auth.CheckAuth()).Then(s.shell.AttachHandler()))
	// MCP endpoint (protected by auth middleware)
	mux.Handle("/mcp", alice.New(s.auth.RequireAuth()).Then(mcpserver.Handler(s.api)))
	// React UI (SPA with client-side routing, protected by cookie auth)
	mux.Handle("/ui/", http.StripPrefix("/ui", s.auth.RequireAuth()(WebUI())))
	mux.Handle("/", http.RedirectHandler("/ui/", http.StatusFound))
	return mux.Handler(s.handleCORS)
}

// handleCORS adds permissive CORS headers to all responses when CORS is enabled.
func (s *Server) handleCORS(next http.Handler) http.Handler {
	if !s.cors {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, MCP-Protocol-Version")
		w.Header().Set("Access-Control-Expose-Headers", "Traceresponse")
		if r.Method == http.MethodOptions {
			return
		}
		next.ServeHTTP(w, r)
	})
}
