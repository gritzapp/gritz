package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gritzapp/gritz/internal/store"
)

// healthTimeout bounds the database ping, so a hung connection fails the
// check instead of outlasting the caller's own timeout.
const healthTimeout = 2 * time.Second

// HealthHandler reports whether the server can serve traffic: 200 when the
// database answers a ping, 503 otherwise. It is unauthenticated, so the
// response carries no error detail; the error is logged instead.
func HealthHandler(log *slog.Logger, st *store.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
		defer cancel()
		w.Header().Set("Cache-Control", "no-store")
		if err := st.Ping(ctx); err != nil {
			log.Warn("health check: database ping failed", "err", err)
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
}
