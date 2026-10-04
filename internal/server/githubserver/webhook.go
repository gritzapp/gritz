package githubserver

import (
	"cmp"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/go-github/v88/github"
	"github.com/gritzapp/gritz/internal/server/eventrouter"
	"github.com/gritzapp/gritz/internal/x/githubx"
)

// WebhookHandler handles incoming GitHub App webhook events.
type WebhookHandler struct {
	Router        Router
	Store         Store
	WebhookSecret string
}

func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Error("failed to read webhook body", "err", err)
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	webhookEvent, err := githubx.ParseWebHook(body, r.Header, []byte(h.WebhookSecret))
	if err != nil {
		slog.Error("failed to parse GitHub webhook", "err", err)
		http.Error(w, "invalid webhook", http.StatusBadRequest)
		return
	}
	if event, ok := webhookEvent.(*github.InstallationEvent); ok {
		h.handleInstallationEvent(w, r, event)
		return
	}
	input := toInputEvent(webhookEvent)
	if input == nil {
		eventType := r.Header.Get("X-GitHub-Event")
		slog.Debug("ignoring GitHub webhook event", "event_type", eventType)
		fmt.Fprintf(w, "ignored")
		return
	}
	// toInputEvent always sets Meta to a GitHubMeta, so this
	// assertion is safe. It panics loudly if that invariant is ever broken.
	meta := input.Meta.(GitHubMeta)

	// Resolve the App installation to the orgs the event belongs to,
	// independent of the actor's membership. These gate non-member routing: a
	// Public rule on one of these orgs can fire even when the actor is unlinked.
	input.Orgs, err = h.Store.ListOrgIDsByGitHubInstallation(r.Context(), nil, meta.InstallationID)
	if err != nil {
		slog.Error("failed to list orgs for GitHub installation", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Look up the gritz user by GitHub user ID. A linked actor routes to their
	// member orgs as before; an unlinked actor keeps an empty UserID and routes
	// only via Public rules on the installation's orgs (input.Orgs).
	user, err := h.Store.GetUserByGitHubUserID(r.Context(), nil, meta.AuthorID)
	switch {
	case err == nil:
		input.UserID = user.ID
		// Update cached username if it changed.
		if meta.AuthorLogin != "" && meta.AuthorLogin != user.GitHubUsername {
			if err := h.Store.UpdateGitHubUsername(r.Context(), nil, user.GitHubUserID, meta.AuthorLogin); err != nil {
				slog.Warn("failed to update GitHub username", "err", err)
			}
		}
	case errors.Is(err, sql.ErrNoRows):
		slog.Info("no linked GitHub account; routing via installation orgs", "github_user_id", meta.AuthorID)
	default:
		slog.Error("failed to look up GitHub account", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Route event to subscribed tasks.
	totalRouted, err := h.Router.Route(r.Context(), *input)
	if err != nil {
		slog.Error("failed to route event", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	slog.Info("GitHub webhook processed", "url", input.URL, "tasks_routed", totalRouted)
	fmt.Fprintf(w, "processed")
}

// handleInstallationEvent reacts only to App uninstalls. Linking is now
// verified on demand against live GitHub membership (see
// Server.VerifyInstallationAccess), so the App no longer records a pending row
// on "created"; routing is author-keyed and needs no installation bookkeeping
// either. The only remaining bookkeeping is clearing the stored installation id
// across every org sharing it when the App is uninstalled, so Settings stops
// showing "Installed" and reactions stop minting tokens against a dead install.
func (h *WebhookHandler) handleInstallationEvent(w http.ResponseWriter, r *http.Request, event *github.InstallationEvent) {
	installationID := event.GetInstallation().GetID()
	action := event.GetAction()
	if action != "deleted" {
		slog.Info("github installation event", "action", action, "installation_id", installationID)
		fmt.Fprintf(w, "ignored")
		return
	}
	if err := h.Store.ClearGitHubInstallation(r.Context(), nil, installationID); err != nil {
		slog.Error("failed to clear github installation", "err", err, "installation_id", installationID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	slog.Info("github installation cleared", "installation_id", installationID)
	fmt.Fprintf(w, "cleared")
}

// GitHubMeta is attached to an eventrouter.InputEvent's Meta field, carrying
// GitHub-native identity that the router does not interpret.
type GitHubMeta struct {
	AuthorID    int64
	AuthorLogin string

	// InstallationID is the GitHub App installation the event was delivered
	// through. The handler resolves it to the orgs the event belongs to
	// (eventrouter.InputEvent.Orgs) so non-member actors can route via Public
	// rules on those orgs.
	InstallationID int64

	// NodeID is the GraphQL global node ID of this event's reactable target: the
	// comment for issue_comment and pull_request_review_comment events, the
	// review summary for pull_request_review events, and the issue/PR for
	// issue_assigned and pull_request_assigned events. All reactions go through
	// the GraphQL addReaction mutation, which accepts any reactable node
	// uniformly, so this single ID is all react needs regardless of event type.
	// It is empty when the event has no reactable target.
	NodeID string
}

// Event-type strings set on eventrouter.InputEvent.Type by toInputEvent. They
// form a contract between the extractor (producer) and any consumer that
// dispatches on InputEvent.Type.
const (
	EventTypeIssueComment             = "issue_comment"
	EventTypePullRequestReviewComment = "pull_request_review_comment"
	EventTypePullRequestReview        = "pull_request_review"
	EventTypeIssueAssigned            = "issue_assigned"
	EventTypePullRequestAssigned      = "pull_request_assigned"
	EventTypePullRequestOpened        = "pull_request_opened"
	EventTypePullRequestClosed        = "pull_request_closed"
	EventTypeLabelAdded               = "label_added"
)

func toInputEvent(webhookEvent any) *eventrouter.InputEvent {
	switch event := webhookEvent.(type) {
	case *github.IssueCommentEvent:
		if action := event.GetAction(); action != "created" && action != "edited" {
			return nil
		}
		if event.Comment == nil || event.Issue == nil ||
			event.Comment.Body == nil || event.Comment.HTMLURL == nil ||
			event.Comment.User == nil || event.Comment.User.ID == nil {
			return nil
		}
		body := strings.TrimSpace(*event.Comment.Body)
		login := event.Comment.User.GetLogin()
		number := event.Issue.GetNumber()
		description := fmt.Sprintf("%s commented on issue #%d", login, number)
		if event.Issue.IsPullRequest() {
			description = fmt.Sprintf("%s commented on PR #%d", login, number)
		}
		return &eventrouter.InputEvent{
			Source:      "github",
			Type:        EventTypeIssueComment,
			Description: description,
			Data:        body,
			// The expressive trigger URL is the comment itself; the router
			// derives the parent routing key from it via model.RoutingKey.
			URL:   *event.Comment.HTMLURL,
			Attrs: eventrouter.Attrs{"mention": githubx.Mentions(body), "user": {login}},
			Meta: GitHubMeta{
				AuthorID:       *event.Comment.User.ID,
				AuthorLogin:    login,
				InstallationID: event.GetInstallation().GetID(),
				NodeID:         event.GetComment().GetNodeID(),
			},
		}

	case *github.PullRequestReviewCommentEvent:
		if action := event.GetAction(); action != "created" && action != "edited" {
			return nil
		}
		if event.Comment == nil || event.PullRequest == nil ||
			event.Comment.Body == nil || event.Comment.HTMLURL == nil ||
			event.Comment.User == nil || event.Comment.User.ID == nil {
			return nil
		}
		body := strings.TrimSpace(*event.Comment.Body)
		login := event.Comment.User.GetLogin()
		number := event.PullRequest.GetNumber()

		// Carry the comment's code location into the persisted payload so the
		// agent and UI see the anchored file/line and diff without a GitHub API
		// round trip. line/start_line are capture-time hints; diff_hunk is the
		// durable anchor (see proposals/draft/pr-review-comment-code-location.md).
		c := event.Comment
		// GitHub sends a null line for comments on an outdated diff; fall back to
		// original_line, which still points at the reviewed commit. This records
		// no freshness claim about the number (see the proposal).
		line := cmp.Or(c.GetLine(), c.GetOriginalLine())
		details := map[string]string{
			"path": c.GetPath(),
			"line": strconv.Itoa(line),
		}
		if s := c.GetStartLine(); s != 0 {
			details["start_line"] = strconv.Itoa(s)
		}
		if s := c.GetSide(); s != "" {
			details["side"] = s
		}
		if h := c.GetDiffHunk(); h != "" {
			details["diff_hunk"] = h
		}

		// Fold path:line into the description so even the timeline row and the
		// data channel read better before any UI change lands.
		description := fmt.Sprintf("%s reviewed PR #%d", login, number)
		if path := c.GetPath(); path != "" {
			description = fmt.Sprintf("%s reviewed PR #%d (%s:%d)", login, number, path, line)
		}
		return &eventrouter.InputEvent{
			Source:      "github",
			Type:        EventTypePullRequestReviewComment,
			Description: description,
			Data:        body,
			Details:     details,
			// The expressive trigger URL is the review comment itself; the
			// router derives the parent PR routing key via model.RoutingKey.
			URL:   *event.Comment.HTMLURL,
			Attrs: eventrouter.Attrs{"mention": githubx.Mentions(body), "user": {login}},
			Meta: GitHubMeta{
				AuthorID:       *event.Comment.User.ID,
				AuthorLogin:    login,
				InstallationID: event.GetInstallation().GetID(),
				NodeID:         event.GetComment().GetNodeID(),
			},
		}

	case *github.PullRequestReviewEvent:
		if event.Action == nil || *event.Action != "submitted" ||
			event.Review == nil || event.PullRequest == nil ||
			event.Review.Body == nil || event.Review.HTMLURL == nil ||
			event.Review.User == nil || event.Review.User.ID == nil {
			return nil
		}
		body := strings.TrimSpace(*event.Review.Body)
		login := event.Review.User.GetLogin()
		number := event.PullRequest.GetNumber()
		return &eventrouter.InputEvent{
			Source:      "github",
			Type:        EventTypePullRequestReview,
			Description: fmt.Sprintf("%s reviewed PR #%d", login, number),
			Data:        body,
			// The expressive trigger URL is the review itself; the router
			// derives the parent PR routing key via model.RoutingKey.
			URL:   *event.Review.HTMLURL,
			Attrs: eventrouter.Attrs{"mention": githubx.Mentions(body), "user": {login}},
			Meta: GitHubMeta{
				AuthorID:       *event.Review.User.ID,
				AuthorLogin:    login,
				InstallationID: event.GetInstallation().GetID(),
				NodeID:         event.GetReview().GetNodeID(),
			},
		}

	case *github.IssuesEvent:
		switch event.GetAction() {
		case "assigned":
			if event.Issue == nil || event.Issue.HTMLURL == nil ||
				event.Assignee == nil || event.Assignee.Login == nil ||
				event.Sender == nil || event.Sender.ID == nil {
				return nil
			}
			senderLogin := event.Sender.GetLogin()
			assigneeLogin := event.Assignee.GetLogin()
			number := event.Issue.GetNumber()
			return &eventrouter.InputEvent{
				Source:      "github",
				Type:        EventTypeIssueAssigned,
				Description: fmt.Sprintf("%s assigned issue #%d to @%s", senderLogin, number, assigneeLogin),
				URL:         *event.Issue.HTMLURL,
				Attrs:       eventrouter.Attrs{"assignee": {assigneeLogin}, "user": {senderLogin}},
				Meta: GitHubMeta{
					AuthorID:       *event.Sender.ID,
					AuthorLogin:    senderLogin,
					InstallationID: event.GetInstallation().GetID(),
					NodeID:         event.GetIssue().GetNodeID(),
				},
			}
		case "labeled":
			if event.Issue == nil || event.Issue.HTMLURL == nil ||
				event.Label == nil || event.Label.Name == nil ||
				event.Sender == nil || event.Sender.ID == nil {
				return nil
			}
			senderLogin := event.Sender.GetLogin()
			label := event.Label.GetName()
			number := event.Issue.GetNumber()
			// GitHub fires a separate "labeled" delivery per label, so the "label"
			// attr carries the single added label for a label condition to match.
			return &eventrouter.InputEvent{
				Source:      "github",
				Type:        EventTypeLabelAdded,
				Description: fmt.Sprintf("%s labeled issue #%d %q", senderLogin, number, label),
				Attrs:       eventrouter.Attrs{"label": {label}, "user": {senderLogin}},
				URL:         *event.Issue.HTMLURL,
				Meta: GitHubMeta{
					AuthorID:       *event.Sender.ID,
					AuthorLogin:    senderLogin,
					InstallationID: event.GetInstallation().GetID(),
					NodeID:         event.GetIssue().GetNodeID(),
				},
			}
		}
		return nil

	case *github.PullRequestEvent:
		switch event.GetAction() {
		case "opened":
			if event.PullRequest == nil || event.PullRequest.HTMLURL == nil ||
				event.Sender == nil || event.Sender.ID == nil {
				return nil
			}
			senderLogin := event.Sender.GetLogin()
			number := event.PullRequest.GetNumber()
			return &eventrouter.InputEvent{
				Source:      "github",
				Type:        EventTypePullRequestOpened,
				Description: fmt.Sprintf("%s opened PR #%d", senderLogin, number),
				Attrs:       eventrouter.Attrs{"user": {senderLogin}},
				// model.RoutingKey reduces this PR URL to the canonical /pull/N,
				// matching the link the agent created when it opened the PR.
				URL: *event.PullRequest.HTMLURL,
				Meta: GitHubMeta{
					AuthorID:       *event.Sender.ID,
					AuthorLogin:    senderLogin,
					InstallationID: event.GetInstallation().GetID(),
					NodeID:         event.GetPullRequest().GetNodeID(),
				},
			}
		case "assigned":
			if event.PullRequest == nil || event.PullRequest.HTMLURL == nil ||
				event.Assignee == nil || event.Assignee.Login == nil ||
				event.Sender == nil || event.Sender.ID == nil {
				return nil
			}
			senderLogin := event.Sender.GetLogin()
			assigneeLogin := event.Assignee.GetLogin()
			number := event.PullRequest.GetNumber()
			return &eventrouter.InputEvent{
				Source:      "github",
				Type:        EventTypePullRequestAssigned,
				Description: fmt.Sprintf("%s assigned PR #%d to @%s", senderLogin, number, assigneeLogin),
				URL:         *event.PullRequest.HTMLURL,
				Attrs:       eventrouter.Attrs{"assignee": {assigneeLogin}, "user": {senderLogin}},
				Meta: GitHubMeta{
					AuthorID:       *event.Sender.ID,
					AuthorLogin:    senderLogin,
					InstallationID: event.GetInstallation().GetID(),
					NodeID:         event.GetPullRequest().GetNodeID(),
				},
			}
		case "labeled":
			if event.PullRequest == nil || event.PullRequest.HTMLURL == nil ||
				event.Label == nil || event.Label.Name == nil ||
				event.Sender == nil || event.Sender.ID == nil {
				return nil
			}
			senderLogin := event.Sender.GetLogin()
			label := event.Label.GetName()
			number := event.PullRequest.GetNumber()
			// GitHub fires a separate "labeled" delivery per label, so the "label"
			// attr carries the single added label for a label condition to match.
			return &eventrouter.InputEvent{
				Source:      "github",
				Type:        EventTypeLabelAdded,
				Description: fmt.Sprintf("%s labeled PR #%d %q", senderLogin, number, label),
				Attrs:       eventrouter.Attrs{"label": {label}, "user": {senderLogin}},
				URL:         *event.PullRequest.HTMLURL,
				Meta: GitHubMeta{
					AuthorID:       *event.Sender.ID,
					AuthorLogin:    senderLogin,
					InstallationID: event.GetInstallation().GetID(),
					NodeID:         event.GetPullRequest().GetNodeID(),
				},
			}
		case "closed":
			if event.PullRequest == nil || event.PullRequest.HTMLURL == nil ||
				event.Sender == nil || event.Sender.ID == nil {
				return nil
			}
			senderLogin := event.Sender.GetLogin()
			number := event.PullRequest.GetNumber()
			// GitHub fires "closed" for both merges and plain closes; the merge
			// state goes in Data so a routing rule can target merges via
			// Prefix=merged if desired.
			data := "closed"
			verb := "closed"
			if event.PullRequest.GetMerged() {
				data = "merged"
				verb = "merged"
			}
			return &eventrouter.InputEvent{
				Source:      "github",
				Type:        EventTypePullRequestClosed,
				Description: fmt.Sprintf("%s %s PR #%d", senderLogin, verb, number),
				Data:        data,
				// state mirrors the "merged"/"closed" string already in Data.
				Attrs: eventrouter.Attrs{"state": {data}, "user": {senderLogin}},
				// model.RoutingKey reduces this PR URL to the canonical /pull/N,
				// matching the link the agent created when it opened the PR.
				URL: *event.PullRequest.HTMLURL,
				Meta: GitHubMeta{
					AuthorID:       *event.Sender.ID,
					AuthorLogin:    senderLogin,
					InstallationID: event.GetInstallation().GetID(),
					NodeID:         event.GetPullRequest().GetNodeID(),
				},
			}
		}
		return nil
	}

	return nil
}
