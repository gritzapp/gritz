// Package notifyserver serves the SSE endpoint that fans out
// pubsub notifications to connected clients.
//
//go:generate go tool moq -out org_resolver_moq_test.go . OrgResolver
package notifyserver

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/icholy/gritz/internal/server/pubsub"
)

// OrgResolver validates that a user can subscribe to the requested org and
// returns the resolved org id (e.g. the user's default if 0 was passed).
type OrgResolver interface {
	ResolveOrg(ctx context.Context, userID string, orgID int64) (int64, error)
}

// Server handles SSE subscriptions backed by a pubsub.Subscriber.
type Server struct {
	log         *slog.Logger
	subscriber  pubsub.Subscriber
	orgResolver OrgResolver
}

// Options configures a Server.
type Options struct {
	Log         *slog.Logger
	Subscriber  pubsub.Subscriber
	OrgResolver OrgResolver
}

// New returns a new Server.
func New(opts Options) *Server {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		log:         log,
		subscriber:  opts.Subscriber,
		orgResolver: opts.OrgResolver,
	}
}

// Handler returns the Server-Sent Events HTTP handler. The caller is
// responsible for wrapping it with authentication middleware that populates
// apiauth.UserInfo in the request context.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.handleSSE)
}
