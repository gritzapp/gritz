// Package githubmcp adapts GitHub's MCP server for use with rotating
// GitHub App installation tokens. A Server holds a single upstream
// session to the GitHub MCP endpoint via internal/x/mcpswap and refreshes
// the underlying installation token before each expiry so a long-running
// agent never sees the 1h TTL break its session.
package githubmcp

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/icholy/gritz/internal/gritzclient"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"github.com/icholy/gritz/internal/x/mcpswap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DefaultURL is the default upstream GitHub MCP endpoint.
const DefaultURL = "https://api.githubcopilot.com/mcp/"

// DefaultMinTTL is the default minimum remaining lifetime on the active
// installation token; the upstream session is rotated when the token's
// remaining time drops below this.
const DefaultMinTTL = 5 * time.Minute

// retryBackoff is how long to wait before retrying a failed token swap.
// The previous session keeps serving requests while we retry.
const retryBackoff = 30 * time.Second

// Config configures a Server.
type Config struct {
	// Client is the gritz API client used to fetch GitHub App
	// installation tokens via CreateGitHubToken.
	Client gritzclient.Client
	// URL is the upstream GitHub MCP endpoint. Defaults to DefaultURL.
	URL string
	// MinTTL is the minimum remaining lifetime on the active installation
	// token; the upstream session is rotated when the token has less than
	// this much time left. Defaults to DefaultMinTTL.
	MinTTL time.Duration
	// Logger is used for lifecycle events. Defaults to slog.Default().
	// It must not write to stdout — stdout is the MCP stdio channel.
	Logger *slog.Logger
}

// Server fronts the GitHub MCP server over stdio, hot-swapping its
// upstream session as the GitHub App installation token rotates.
type Server struct {
	client   gritzclient.Client
	url      string
	minTTL   time.Duration
	logger   *slog.Logger
	upstream mcpswap.Upstream
}

// New returns a Server configured from cfg.
func New(cfg Config) *Server {
	s := &Server{
		client: cfg.Client,
		url:    cmp.Or(cfg.URL, DefaultURL),
		minTTL: cmp.Or(cfg.MinTTL, DefaultMinTTL),
		logger: cmp.Or(cfg.Logger, slog.Default()),
	}
	s.upstream.SetLogger(s.logger)
	return s
}

// Run fetches an initial installation token, opens an upstream session
// with it, and serves the proxied MCP server over stdio. A background
// goroutine refreshes the token before each expiry; swap failures are
// logged and retried while the previous session keeps serving. Run
// blocks until ctx is done or the MCP server returns.
//
// The initial swap must succeed — without a valid token the agent has
// nothing to forward to.
func (s *Server) Run(ctx context.Context) error {
	defer s.upstream.Close()

	expiresAt, err := s.swap(ctx)
	if err != nil {
		return err
	}
	go s.rotate(ctx, expiresAt)

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "gritz-github-mcp",
		Version: "1.0.0",
	}, &mcp.ServerOptions{
		HasTools:     true,
		HasPrompts:   true,
		HasResources: true,
	})
	srv.AddReceivingMiddleware(s.upstream.Dispatch)
	return srv.Run(ctx, &mcp.StdioTransport{})
}

// swap mints a fresh installation token and replaces the active
// upstream session with one connected using that token. Returns the new
// token's expiry.
func (s *Server) swap(ctx context.Context) (time.Time, error) {
	resp, err := s.client.CreateGitHubToken(ctx, &gritzv1.CreateGitHubTokenRequest{})
	if err != nil {
		return time.Time{}, fmt.Errorf("create github token: %w", err)
	}
	transport := &mcp.StreamableClientTransport{
		Endpoint: s.url,
		HTTPClient: &http.Client{
			Transport: &bearerTransport{token: resp.Token, base: http.DefaultTransport},
		},
	}
	if err := s.upstream.Swap(ctx, transport); err != nil {
		return time.Time{}, fmt.Errorf("swap upstream: %w", err)
	}
	return resp.GetExpiresAt().AsTime(), nil
}

// rotate refreshes the upstream session before each token's expiry. On
// failure it retries with a short backoff while the previous session
// keeps serving requests (Swap leaves the active session intact on
// error). Returns when ctx is done.
func (s *Server) rotate(ctx context.Context, expiresAt time.Time) {
	for {
		wait := time.Until(expiresAt.Add(-s.minTTL))
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		next, err := s.swap(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.logger.Error("github-mcp: token rotation failed; retrying", "err", err, "backoff", retryBackoff)
			expiresAt = time.Now().Add(retryBackoff + s.minTTL)
			continue
		}
		expiresAt = next
	}
}

// bearerTransport injects an Authorization: Bearer header on every
// request. A fresh transport is built per swap so a session always sees
// the token it was opened with.
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
}
