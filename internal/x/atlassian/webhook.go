package atlassian

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Webhook-event strings sent by Jira Cloud, matched on
// WebhookPayload.WebhookEvent.
const (
	WebhookEventCommentCreated = "comment_created"
	WebhookEventIssueUpdated   = "jira:issue_updated"
)

// WebhookPayload represents the relevant fields of a Jira Cloud webhook payload.
type WebhookPayload struct {
	WebhookEvent string     `json:"webhookEvent"`
	User         *User      `json:"user"`
	Comment      *Comment   `json:"comment"`
	Issue        *Issue     `json:"issue"`
	Changelog    *Changelog `json:"changelog"`
}

// Changelog represents the set of field changes included with a
// jira:issue_updated webhook event.
type Changelog struct {
	Items []ChangelogItem `json:"items"`
}

// ChangelogItem represents a single field change within a Changelog. For label
// changes, Field is "labels" and FromString/ToString hold the space-separated
// label lists before and after the change. For assignee changes, Field is
// "assignee", From/To hold the old and new assignee's account ids and
// FromString/ToString their display names.
type ChangelogItem struct {
	Field      string `json:"field"`
	From       string `json:"from"`
	FromString string `json:"fromString"`
	To         string `json:"to"`
	ToString   string `json:"toString"`
}

// AddedLabels returns the labels that were added by this webhook event, i.e.
// labels present in the new value of a "labels" changelog item but not the old
// value. It returns nil when the event carries no label change.
func (p *WebhookPayload) AddedLabels() []string {
	if p.Changelog == nil {
		return nil
	}
	for _, item := range p.Changelog.Items {
		if item.Field != "labels" {
			continue
		}
		before := make(map[string]struct{})
		for _, label := range strings.Fields(item.FromString) {
			before[label] = struct{}{}
		}
		var added []string
		for _, label := range strings.Fields(item.ToString) {
			if _, ok := before[label]; !ok {
				added = append(added, label)
			}
		}
		return added
	}
	return nil
}

// NewAssignee returns the user this webhook event assigned the issue to. It
// returns nil when the event carries no assignee change or unassigns the issue.
func (p *WebhookPayload) NewAssignee() *User {
	if p.Changelog == nil {
		return nil
	}
	for _, item := range p.Changelog.Items {
		if item.Field != "assignee" {
			continue
		}
		if item.To == "" {
			return nil
		}
		return &User{AccountID: item.To, DisplayName: item.ToString}
	}
	return nil
}

// Comment represents a Jira issue comment from a webhook payload.
type Comment struct {
	ID     string `json:"id"`
	Body   string `json:"body"`
	Author User   `json:"author"`
}

// User represents a Jira user reference in a webhook payload.
type User struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
}

// Issue represents a Jira issue from a webhook payload.
type Issue struct {
	Key    string      `json:"key"`
	Fields IssueFields `json:"fields"`
	Self   string      `json:"self"`
}

// IssueFields represents the fields of a Jira issue.
type IssueFields struct {
	Summary string `json:"summary"`
}

// ParseWebhook parses a Jira webhook payload from JSON.
func ParseWebhook(body []byte) (*WebhookPayload, error) {
	var payload WebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

// BrowseURL constructs the browse URL for a Jira issue from the API self link and issue key.
// The self link is like "https://mycompany.atlassian.net/rest/api/2/issue/12345".
func (i *Issue) BrowseURL() string {
	idx := strings.Index(i.Self, "/rest/")
	if idx == -1 {
		return ""
	}
	return i.Self[:idx] + "/browse/" + i.Key
}

// CommentBrowseURL returns the browse URL focused on a specific comment, e.g.
// https://site.atlassian.net/browse/X-1?focusedCommentId=10001. Returns the
// plain browse URL when commentID is empty, and "" when the browse URL can't
// be derived.
func (i *Issue) CommentBrowseURL(commentID string) string {
	base := i.BrowseURL()
	if base == "" || commentID == "" {
		return base
	}
	return base + "?focusedCommentId=" + commentID
}

// SignWebhook computes the HMAC-SHA256 signature for a webhook payload.
// Returns the signature in "sha256=<hex>" format.
func SignWebhook(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifyWebhook verifies the HMAC-SHA256 signature from Jira Cloud webhooks.
// The signature header value should be in the format "sha256=<hex>".
func VerifyWebhook(body []byte, signature, secret string) error {
	if signature == "" {
		return fmt.Errorf("missing X-Hub-Signature header")
	}
	algo, sigHex, ok := strings.Cut(signature, "=")
	if !ok || algo != "sha256" {
		return fmt.Errorf("unsupported signature format: %s", signature)
	}
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil {
		return fmt.Errorf("invalid signature hex: %w", err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)
	if !hmac.Equal(sigBytes, expected) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}
