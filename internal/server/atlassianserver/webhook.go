package atlassianserver

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gritzapp/gritz/internal/server/eventrouter"
	"github.com/gritzapp/gritz/internal/x/atlassian"
)

// WebhookHandler handles incoming Atlassian (Jira) webhook events.
type WebhookHandler struct {
	Router Router
	Store  Store
}

func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract org ID from query parameter
	orgIDStr := r.URL.Query().Get("org")
	if orgIDStr == "" {
		http.Error(w, "missing org query parameter", http.StatusBadRequest)
		return
	}
	orgID, err := strconv.ParseInt(orgIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid org query parameter", http.StatusBadRequest)
		return
	}

	// Look up the org's webhook secret
	secret, err := h.Store.GetOrgAtlassianWebhookSecret(r.Context(), nil, orgID)
	if err != nil {
		slog.Error("failed to get Atlassian webhook secret", "org_id", orgID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if secret == "" {
		slog.Warn("no Atlassian webhook secret configured", "org_id", orgID)
		http.Error(w, "webhook not configured", http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Error("failed to read webhook body", "err", err)
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Verify HMAC-SHA256 signature
	if err := atlassian.VerifyWebhook(body, r.Header.Get("X-Hub-Signature"), secret); err != nil {
		slog.Error("failed to verify Atlassian webhook signature", "err", err)
		http.Error(w, "invalid signature", http.StatusForbidden)
		return
	}

	// Parse and extract the event. A nil input means the webhook should be
	// ignored.
	input, err := toInputEvent(body)
	if err != nil {
		slog.Error("failed to parse Atlassian webhook payload", "err", err)
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if input == nil {
		slog.Debug("ignoring Atlassian webhook event")
		fmt.Fprintf(w, "ignored")
		return
	}
	// toInputEvent always sets Meta to an AtlassianMeta, so this assertion is
	// safe. It panics loudly if that invariant is ever broken.
	meta := input.Meta.(AtlassianMeta)

	// The event belongs to the org addressed by the ?org= param, independent of
	// the actor's membership. It gates non-member routing: a Public rule on this
	// org can fire even when the actor is unlinked.
	input.Orgs = []int64{orgID}

	// Look up the gritz user by Atlassian account ID. A linked actor routes to
	// their member orgs as before; an unlinked actor keeps an empty UserID and
	// routes only via Public rules on input.Orgs.
	user, err := h.Store.GetUserByAtlassianAccountID(r.Context(), nil, meta.AuthorAccountID)
	switch {
	case err == nil:
		input.UserID = user.ID
	case errors.Is(err, sql.ErrNoRows):
		slog.Info("no linked Atlassian account; routing via org", "atlassian_account_id", meta.AuthorAccountID, "org_id", orgID)
	default:
		slog.Error("failed to look up Atlassian account", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Route the event to subscribed tasks
	routed, err := h.Router.Route(r.Context(), *input)
	if err != nil {
		slog.Error("failed to route event", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	slog.Info("Atlassian webhook processed", "url", input.URL, "type", input.Type, "tasks_routed", routed)

	fmt.Fprintf(w, "processed")
}

// AtlassianMeta is attached to an eventrouter.InputEvent's Meta field, carrying
// Atlassian-native identity that the router does not interpret.
type AtlassianMeta struct {
	AuthorAccountID   string
	AuthorDisplayName string
}

// Event-type strings set on eventrouter.InputEvent.Type by toInputEvent. They
// form a contract between the extractor (producer) and any consumer that
// dispatches on InputEvent.Type.
const (
	EventTypeCommentCreated = "comment_created"
	EventTypeLabelAdded     = "label_added"
)

// toInputEvent extracts a single routable event from a Jira webhook payload. It
// returns nil for events that should be ignored.
func toInputEvent(body []byte) (*eventrouter.InputEvent, error) {
	payload, err := atlassian.ParseWebhook(body)
	if err != nil {
		return nil, err
	}

	switch payload.WebhookEvent {
	case atlassian.WebhookEventCommentCreated:
		if payload.Comment == nil || payload.Issue == nil {
			return nil, nil
		}
		commentBody := strings.TrimSpace(payload.Comment.Body)

		accountID := payload.Comment.Author.AccountID
		displayName := payload.Comment.Author.DisplayName
		if accountID == "" {
			return nil, nil
		}

		// The expressive trigger URL is the comment itself, focused via the
		// browse URL's query param when the comment id is available. The router
		// derives the parent issue routing key from it via model.RoutingKey.
		url := payload.Issue.CommentBrowseURL(payload.Comment.ID)
		if url == "" {
			return nil, nil
		}

		description := fmt.Sprintf("%s commented on %s", displayName, payload.Issue.Key)

		return &eventrouter.InputEvent{
			Source:      "atlassian",
			Type:        EventTypeCommentCreated,
			Description: description,
			Data:        commentBody,
			URL:         url,
			Attrs:       eventrouter.Attrs{"mention": atlassian.Mentions(commentBody), "user": {accountID}},
			Meta:        AtlassianMeta{AuthorAccountID: accountID, AuthorDisplayName: displayName},
		}, nil

	case atlassian.WebhookEventIssueUpdated:
		added := payload.AddedLabels()
		if len(added) == 0 || payload.Issue == nil || payload.User == nil {
			return nil, nil
		}

		accountID := payload.User.AccountID
		displayName := payload.User.DisplayName
		if accountID == "" {
			return nil, nil
		}

		url := payload.Issue.BrowseURL()
		if url == "" {
			return nil, nil
		}

		// Emit a single event carrying all added labels in the "label" attr so
		// routing rules can match a specific label via a label condition.
		return &eventrouter.InputEvent{
			Source:      "atlassian",
			Type:        EventTypeLabelAdded,
			Description: fmt.Sprintf("%s added label(s) %s to %s", displayName, strings.Join(quote(added), ", "), payload.Issue.Key),
			Attrs:       eventrouter.Attrs{"label": added, "user": {accountID}},
			URL:         url,
			Meta:        AtlassianMeta{AuthorAccountID: accountID, AuthorDisplayName: displayName},
		}, nil
	}

	return nil, nil
}

// quote returns a copy of labels with each element wrapped in double quotes,
// for inclusion in an event description.
func quote(labels []string) []string {
	out := make([]string, len(labels))
	for i, l := range labels {
		out[i] = strconv.Quote(l)
	}
	return out
}
