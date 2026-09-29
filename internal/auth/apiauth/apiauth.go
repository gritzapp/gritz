package apiauth

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/icholy/gritz/internal/auth/authscope"
	"github.com/icholy/gritz/internal/model"
	"github.com/icholy/gritz/internal/x/logctx"
	httphelper "github.com/zitadel/oidc/v3/pkg/http"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/zitadel-go/v3/pkg/authentication"
	openid "github.com/zitadel/zitadel-go/v3/pkg/authentication/oidc"
	"github.com/zitadel/zitadel-go/v3/pkg/zitadel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Auth type labels for UserInfo.Type.
const (
	AuthTypeKey    = "key"
	AuthTypeApp    = "app"
	AuthTypeCookie = "cookie"
)

// UserInfo contains authenticated user information.
type UserInfo struct {
	ID       string
	Email    string
	Name     string
	OrgID    int64
	Type     string           // Auth type: one of AuthType* constants
	ClientID string           // Per-tab client identifier from X-Client-ID header
	Scopes   authscope.Scopes // Capabilities held by the caller within OrgID
}

// DisplayName returns the best available display name for the user.
func (u *UserInfo) DisplayName() string {
	if u.Name != "" {
		return u.Name
	}
	if u.Email != "" {
		return u.Email
	}
	return u.ID
}

// AuditName returns the display name annotated with the auth type for audit logs.
func (u *UserInfo) AuditName() string {
	name := u.DisplayName()
	if u.Type == AuthTypeKey {
		return name + " (API key)"
	}
	return name
}

type userInfoKey struct{}

// Caller returns the authenticated user from context.
func Caller(ctx context.Context) *UserInfo {
	u, _ := ctx.Value(userInfoKey{}).(*UserInfo)
	return u
}

// MustCaller returns the authenticated user from context, panicking if not present.
func MustCaller(ctx context.Context) *UserInfo {
	u := Caller(ctx)
	if u == nil {
		panic("no UserInfo in request context")
	}
	return u
}

// WithUser returns a context with the user set.
func WithUser(ctx context.Context, user *UserInfo) context.Context {
	return context.WithValue(ctx, userInfoKey{}, user)
}

// KeyValidator validates API keys and returns the associated user.
type KeyValidator interface {
	ValidateKey(ctx context.Context, keyHash string) (*UserInfo, error)
}

// UserResolver provisions users on login and resolves orgs for token issuance.
type UserResolver interface {
	// Provision creates the user and their default org on first login.
	// Called from the OIDC callback via WithOnAuthenticated. It returns the
	// provisioned user with DefaultOrgID populated (the pre-existing org, or
	// the one just created).
	Provision(ctx context.Context, user *UserInfo) (*model.User, error)
	// ResolveOrg resolves the org for token issuance.
	// orgID is the requested org from the query param, or 0 to use the user's default.
	// Returns the resolved org ID or an error if the user is not a member.
	ResolveOrg(ctx context.Context, userID string, orgID int64) (int64, error)
}

// Config holds the configuration for ZITADEL authentication.
type Config struct {
	Domain        string
	ClientID      string
	ClientSecret  string
	RedirectURI   string
	PostLogoutURI string
	EncryptionKey []byte
	Scopes        []string
	// KeyValidator validates xat_ API keys.
	KeyValidator KeyValidator
	// UserResolver provisions users on login and resolves orgs for token issuance.
	UserResolver UserResolver
	// AppKey is the Ed25519 private key for signing/verifying app JWTs.
	// If nil, a new key is generated on startup.
	AppKey ed25519.PrivateKey
	// DevUser, when set, bypasses SSO authentication.
	// All cookie-based requests are treated as this user.
	// API key and app JWT authentication are unaffected.
	DevUser *UserInfo
}

// Auth provides hybrid authentication supporting both cookie-based sessions
// (for web UI) and Bearer tokens (for API calls).
type Auth struct {
	// devUser, when set, bypasses SSO for cookie-based requests
	devUser *UserInfo
	// cookie middleware
	cookie *authentication.Interceptor[*openid.DefaultContext]
	// validator validates xat_ API keys
	validator KeyValidator
	// resolver provisions users and resolves orgs on token issuance
	resolver UserResolver
	// handler handles /auth/* routes (login, callback, logout)
	handler http.Handler
	// appKey is the Ed25519 private key for signing/verifying app JWTs
	appKey ed25519.PrivateKey
}

// New creates a new Auth instance with both cookie and bearer token support.
// If cfg.DevUser is set, SSO is bypassed and cookie-based requests use that user.
// API key and app JWT authentication are always available.
func New(ctx context.Context, cfg Config) (*Auth, error) {
	appKey := cfg.AppKey
	if appKey == nil {
		var err error
		appKey, err = CreateAppPrivateKey()
		if err != nil {
			return nil, err
		}
	}
	if cfg.DevUser != nil {
		return &Auth{
			devUser:   cfg.DevUser,
			validator: cfg.KeyValidator,
			resolver:  cfg.UserResolver,
			handler:   http.NotFoundHandler(),
			appKey:    appKey,
		}, nil
	}
	if len(cfg.Scopes) == 0 {
		// offline_access makes ZITADEL issue a refresh token, which WithRefresh
		// uses to renew expired sessions.
		cfg.Scopes = []string{oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail, oidc.ScopeOfflineAccess}
	}
	authN, err := authentication.New(ctx,
		// my zitadel instance domain
		zitadel.New(cfg.Domain),
		// not sure?
		string(cfg.EncryptionKey),
		// Initialize cookie-based authentication (for web UI)
		openid.WithCodeFlow[*openid.DefaultContext](
			openid.ClientIDSecretAuthentication(
				// credentials to access our zitadel instance
				cfg.ClientID,
				cfg.ClientSecret,
				// where to redirect the browswer after login
				cfg.RedirectURI,
				// what jwt claims we want
				cfg.Scopes,
				// used to store the encrypted token & refresh token & state in cookies
				httphelper.NewCookieHandler(cfg.EncryptionKey, cfg.EncryptionKey),
			),
			// renew expired sessions with the refresh token
			openid.WithRefresh(),
		),
		// tell zitadel where to redirect to after logout
		authentication.WithPostLogoutRedirectURI[*openid.DefaultContext](cfg.PostLogoutURI),
		// store session in cookie instead of in-memory (survives server restarts)
		authentication.WithCookieSession[*openid.DefaultContext](),
		// provision user and default org on first login
		authentication.WithOnAuthenticated(func(ctx context.Context, authCtx *openid.DefaultContext) error {
			_, err := cfg.UserResolver.Provision(ctx, &UserInfo{
				ID:    authCtx.UserInfo.Subject,
				Email: authCtx.UserInfo.Email,
				Name:  authCtx.UserInfo.Name,
			})
			return err
		}),
	)
	if err != nil {
		return nil, err
	}
	return &Auth{
		cookie:    authentication.Middleware(authN),
		validator: cfg.KeyValidator,
		resolver:  cfg.UserResolver,
		handler:   authN,
		appKey:    appKey,
	}, nil
}

// authenticate dispatches Bearer token validation by token shape.
// Returns (nil, nil) when no Authorization header is present, signalling
// callers to fall through to cookie middleware.
func (a *Auth) authenticate(r *http.Request) (*UserInfo, error) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return nil, nil
	}
	if IsKey(raw) {
		user, err := a.validator.ValidateKey(r.Context(), HashKey(raw))
		if err != nil {
			return nil, err
		}
		user.ClientID = r.Header.Get("X-Client-ID")
		return user, nil
	}
	claims, err := VerifyAppToken(a.appKey, raw)
	if err != nil {
		return nil, err
	}
	return &UserInfo{
		ID:       claims.Subject,
		Email:    claims.Email,
		Name:     claims.Name,
		OrgID:    claims.OrgID,
		Type:     AuthTypeApp,
		ClientID: r.Header.Get("X-Client-ID"),
		Scopes:   claims.Scopes,
	}, nil
}

// useDevUser serves the request as the dev user if one is configured.
// Returns true if the request was handled.
func (a *Auth) useDevUser(w http.ResponseWriter, r *http.Request, next http.Handler) bool {
	if a.devUser == nil {
		return false
	}
	r = r.WithContext(WithUser(r.Context(), a.devUser))
	next.ServeHTTP(w, r)
	return true
}

// RequireAuth returns middleware that requires either a valid Bearer token
// or an authenticated cookie session.
func (a *Auth) RequireAuth() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, err := a.authenticate(r)
			if err != nil {
				// A Bearer token was supplied but invalid/expired. Return 401
				// rather than falling through to cookie auth (which would
				// redirect). OAuth clients like n8n only refresh on 401.
				http.Error(w, "invalid or expired token", http.StatusUnauthorized)
				return
			}
			if user != nil {
				next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), user)))
				return
			}
			if !a.useDevUser(w, r, next) {
				a.cookie.RequireAuthentication()(a.attachUserInfo(next)).ServeHTTP(w, r)
			}
		})
	}
}

// CheckAuth returns middleware that checks for valid authentication and
// populates context, but does not reject unauthenticated requests.
func (a *Auth) CheckAuth() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, err := a.authenticate(r)
			if err != nil {
				// Bearer present but invalid — let the request through with
				// no user attached.
				next.ServeHTTP(w, r)
				return
			}
			if user != nil {
				next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), user)))
				return
			}
			if !a.useDevUser(w, r, next) {
				a.cookie.CheckAuthentication()(a.attachUserInfo(next)).ServeHTTP(w, r)
			}
		})
	}
}

// RequireUserInterceptor returns a Connect interceptor that fails if UserInfo is not in the context.
func RequireUserInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if Caller(ctx) == nil {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required"))
			}
			return next(ctx, req)
		}
	}
}

// ObservabilityInterceptor returns a Connect interceptor that attaches the
// caller's org id and the request's task id (when present) to the active span
// and request context, so they show up on traces and logs. It should run after
// authentication has populated the caller.
func ObservabilityInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			span := trace.SpanFromContext(ctx)
			if u := Caller(ctx); u != nil {
				ctx = logctx.WithOrgID(ctx, u.OrgID)
				span.SetAttributes(attribute.Int64("org.id", u.OrgID))
			}
			if m, ok := req.Any().(interface{ GetTaskId() int64 }); ok {
				if id := m.GetTaskId(); id != 0 {
					ctx = logctx.WithTaskID(ctx, id)
					span.SetAttributes(attribute.Int64("task.id", id))
				}
			}
			return next(ctx, req)
		}
	}
}

// attachUserInfo wraps a handler to extract user info from the zitadel auth
// context and attach it to the request context before calling the inner handler.
func (a *Auth) attachUserInfo(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user := a.User(r); user != nil {
			r = r.WithContext(WithUser(r.Context(), user))
		}
		next.ServeHTTP(w, r)
	})
}

// HandleToken returns an HTTP handler for GET /auth/token that issues app JWTs.
// The endpoint is authenticated via cookie session or bearer token.
func (a *Auth) HandleToken() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		user := a.User(r)
		if user == nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		// Parse org_id from query parameter
		var orgID int64
		if raw := r.URL.Query().Get("org_id"); raw != "" {
			var err error
			orgID, err = strconv.ParseInt(raw, 10, 64)
			if err != nil {
				http.Error(w, "invalid org_id", http.StatusBadRequest)
				return
			}
		}
		// Resolve org membership
		if a.resolver != nil {
			resolved, err := a.resolver.ResolveOrg(r.Context(), user.ID, orgID)
			if err != nil {
				slog.Error("failed to resolve org", "err", err, "user_id", user.ID)
				http.Error(w, "failed to resolve org", http.StatusForbidden)
				return
			}
			user.OrgID = resolved
		}
		claims := NewAppClaims(user)
		token, err := SignAppToken(a.appKey, claims)
		if err != nil {
			http.Error(w, "failed to sign token", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      token,
			"org_id":     user.OrgID,
			"expires_at": claims.ExpiresAt.Unix(),
		})
	}
}

// Handler returns the HTTP handler for auth routes (login, callback, logout).
func (a *Auth) Handler() http.Handler {
	return a.handler
}

// User returns the authenticated user's information.
// Works with API keys, app JWTs, and cookie authentication.
func (a *Auth) User(r *http.Request) *UserInfo {
	// If UserInfo already set (e.g., by API key or app token in CheckAuth), return it
	if user := Caller(r.Context()); user != nil {
		return user
	}
	if ctx := a.cookie.Context(r.Context()); ctx != nil {
		return &UserInfo{
			ID:       ctx.UserInfo.Subject,
			Email:    ctx.UserInfo.Email,
			Name:     ctx.UserInfo.Name,
			Type:     AuthTypeCookie,
			ClientID: r.Header.Get("X-Client-ID"),
			// TODO: revisit permissions for cookie auth. Cookie sessions are
			// omnipotent within their org today, so grant the admin wildcard for
			// now to keep behavior unchanged once enforcement lands.
			Scopes: authscope.Admin(),
		}
	}
	return nil
}
