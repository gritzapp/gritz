package customserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"

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

// ToInputEvent validates env against the custom types in reg and builds the
// event to route, with no actor. The description defaults to the type's
// label. The
// URL is used as sent: it is the subscription key, so every event about the
// same resource must send the same one.
func ToInputEvent(env Envelope, reg *eventrouter.SchemaRegistry, orgID int64) (eventrouter.InputEvent, error) {
	// The source is pinned to custom, so a type named like a built-in one
	// can't resolve to the built-in schema.
	def, ok := reg.EventTypeFor(eventrouter.SourceCustom, env.Type)
	if !ok {
		return eventrouter.InputEvent{}, fmt.Errorf("unknown event type %q", env.Type)
	}
	if env.URL == "" {
		return eventrouter.InputEvent{}, errors.New("url is required")
	}
	u, err := url.Parse(env.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return eventrouter.InputEvent{}, fmt.Errorf("url %q must be an absolute http or https URL", env.URL)
	}
	for _, key := range slices.Sorted(maps.Keys(env.Attrs)) {
		// InputEvent.Attr derives these from Data and URL, so a value sent
		// here would be silently ignored.
		if key == "body" || key == "url" {
			return eventrouter.InputEvent{}, fmt.Errorf("attr %q must be sent as the envelope's %q field", key, key)
		}
		if !def.HasAttr(key) {
			return eventrouter.InputEvent{}, fmt.Errorf("attr %q not declared by event type %q", key, def.Type)
		}
	}
	description := env.Description
	if description == "" {
		description = def.Label
	}
	return eventrouter.InputEvent{
		Source:      eventrouter.SourceCustom,
		Type:        def.Type,
		Description: description,
		Data:        env.Body,
		URL:         env.URL,
		Orgs:        []int64{orgID},
		Attrs:       env.Attrs,
		Details:     env.Details,
	}, nil
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
	input, err := ToInputEvent(env, reg, caller.OrgID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

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
