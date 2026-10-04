package notifyserver

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/x/sse"
)

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	caller := apiauth.Caller(r.Context())
	if caller == nil {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	orgID := caller.OrgID

	// Cookie auth doesn't have an org id, so we get it from the query parameter.
	if caller.Type == apiauth.AuthTypeCookie {
		var err error
		orgID, err = strconv.ParseInt(r.URL.Query().Get("org_id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid org_id", http.StatusBadRequest)
			return
		}
		orgID, err = s.orgResolver.ResolveOrg(r.Context(), caller.ID, orgID)
		if err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	// When set, only forward notifications carrying pending work for this
	// runner. Used by runners to wake on actionable task changes.
	runner := r.URL.Query().Get("runner")

	ch, cancel, err := s.subscriber.Subscribe(r.Context(), orgID)
	if err != nil {
		http.Error(w, "subscribe failed", http.StatusInternalServerError)
		return
	}
	defer cancel()

	sw, err := sse.NewServerWriter(w)
	if err != nil {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	var seq int64

	// Send ready event so clients know the subscription is live.
	data, err := json.Marshal(model.Notification{Type: "ready", OrgID: orgID})
	if err != nil {
		return
	}
	if err := sw.Write(sse.Event{
		ID:    strconv.FormatInt(seq, 10),
		Event: "ready",
		Data:  data,
	}); err != nil {
		return
	}

	// Keep the stream alive so reverse proxies with an idle read timeout
	// (nginx defaults to 60s, Cloudflare 524s at 100s) don't tear it down.
	// It carries no data and no id: subscribers skip it by event name and
	// seq keeps counting only real notifications.
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()

	ctx := r.Context()
	for {
		select {
		case <-keepAlive.C:
			if err := sw.Write(sse.Event{Event: "keepalive"}); err != nil {
				return
			}
		case n := <-ch:
			if runner != "" && n.Runner != runner {
				continue
			}
			seq++
			data, err := json.Marshal(n)
			if err != nil {
				s.log.Warn("failed to marshal notification", "err", err)
				continue
			}
			if err := sw.Write(sse.Event{
				ID:    strconv.FormatInt(seq, 10),
				Event: n.Type,
				Data:  data,
			}); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}
