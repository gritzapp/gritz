// Package customserver accepts events of an org's custom event types and
// routes them like the built-in GitHub and Atlassian events.
package customserver

import (
	"log/slog"
	"net/http"

	"github.com/gritzapp/gritz/internal/server/eventrouter"
	"github.com/gritzapp/gritz/internal/server/pubsub"
	"github.com/gritzapp/gritz/internal/store"
)

// Server handles the custom event webhook route.
type Server struct {
	log       *slog.Logger
	store     *store.Store
	publisher pubsub.Publisher
}

// Options configures a Server.
type Options struct {
	Log       *slog.Logger
	Store     *store.Store
	Publisher pubsub.Publisher
}

// New returns a new Server.
func New(opts Options) *Server {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		log:       log,
		store:     opts.Store,
		publisher: opts.Publisher,
	}
}

// WebhookHandler returns the HTTP handler for custom events. A custom source
// has no channel back to the sender, so its router has no OnRouteOutcome: the
// response's routed count is the sender's feedback.
func (s *Server) WebhookHandler() http.Handler {
	return &WebhookHandler{
		Router: &eventrouter.Router{Log: s.log, Store: s.store, Publisher: s.publisher},
		Store:  s.store,
	}
}
