package customserver

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/server/eventrouter"
)

// maxEnvelopeBytes caps the size of a submitted envelope.
const maxEnvelopeBytes = 1 << 20

// Envelope is the JSON body of a custom event submission.
type Envelope struct {
	Type        string              `json:"type"`
	Description string              `json:"description"`
	Body        string              `json:"body"`
	URL         string              `json:"url"`
	Attrs       map[string][]string `json:"attrs"`
	Details     map[string]string   `json:"details"`
}

// NewWebhookRequest returns a request submitting env to the custom event
// webhook at url, authenticated with token (an API key) as a bearer token.
func NewWebhookRequest(ctx context.Context, url, token string, env Envelope) (*http.Request, error) {
	data, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	return req, nil
}

// WebhookHandler accepts custom events from Bearer-authenticated callers. It
// must be mounted behind apiauth's RequireBearerAuth.
type WebhookHandler struct {
	Router Router
	Store  Store
}

// ServeHTTP authorizes, validates and routes one custom event. The event
// has no actor: it belongs to the caller's org and routes with an empty
// UserID, so only the org's public rules can fire.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	caller := apiauth.MustCaller(r.Context())
	if !caller.Scopes.Allow(authscope.OpEventCreate) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}

	var env Envelope
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEnvelopeBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		http.Error(w, fmt.Sprintf("invalid envelope: %v", err), http.StatusBadRequest)
		return
	}

	types, err := h.Store.GetOrgCustomEventTypes(r.Context(), nil, caller.OrgID)
	if err != nil {
		slog.Error("failed to get custom event types", "org_id", caller.OrgID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	reg := eventrouter.DefaultSchemaRegistry.With(types...)
	// The source is pinned to custom, so a type named like a built-in one
	// can't resolve to the built-in schema. The URL is used as sent: it is the
	// subscription key, so every event about the same resource must send the
	// same one.
	input := eventrouter.InputEvent{
		Source:  eventrouter.SourceCustom,
		Type:    env.Type,
		Data:    env.Body,
		URL:     env.URL,
		Orgs:    []int64{caller.OrgID},
		Attrs:   env.Attrs,
		Details: env.Details,
	}
	def, err := reg.ValidateEvent(input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	input.Description = cmp.Or(env.Description, def.Label)

	routed, err := h.Router.Route(r.Context(), input)
	if err != nil {
		slog.Error("failed to route event", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	slog.Info("custom event processed", "org_id", caller.OrgID, "url", input.URL, "type", input.Type, "tasks_routed", routed)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]int{"routed": routed}); err != nil {
		slog.Error("failed to write response", "err", err)
	}
}
