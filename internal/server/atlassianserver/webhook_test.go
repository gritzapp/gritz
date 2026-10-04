package atlassianserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/server/eventrouter"
	"github.com/gritzapp/gritz/internal/x/atlassian"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestToAtlassianInputEvents(t *testing.T) {
	tests := []struct {
		name     string
		payload  atlassian.WebhookPayload
		expected *eventrouter.InputEvent
	}{
		{
			name: "CommentCreated",
			payload: atlassian.WebhookPayload{
				WebhookEvent: "comment_created",
				Comment: &atlassian.Comment{
					ID:     "10001",
					Body:   "gritz: do something",
					Author: atlassian.User{AccountID: "abc123", DisplayName: "Test User"},
				},
				Issue: &atlassian.Issue{
					Key:  "PROJ-123",
					Self: "https://mycompany.atlassian.net/rest/api/2/issue/12345",
				},
			},
			expected: &eventrouter.InputEvent{
				Source:      "atlassian",
				Type:        "comment_created",
				Description: "Test User commented on PROJ-123",
				Data:        "gritz: do something",
				URL:         "https://mycompany.atlassian.net/browse/PROJ-123?focusedCommentId=10001",
				Attrs:       eventrouter.Attrs{"mention": nil, "user": {"abc123"}},
				Meta:        AtlassianMeta{AuthorAccountID: "abc123", AuthorDisplayName: "Test User"},
			},
		},
		{
			name: "CommentWithMentions",
			payload: atlassian.WebhookPayload{
				WebhookEvent: "comment_created",
				Comment: &atlassian.Comment{
					ID:     "10009",
					Body:   "[~accountid:557058:abc] and [~accountid:5b10ac] please review",
					Author: atlassian.User{AccountID: "abc123", DisplayName: "Test User"},
				},
				Issue: &atlassian.Issue{
					Key:  "PROJ-123",
					Self: "https://mycompany.atlassian.net/rest/api/2/issue/12345",
				},
			},
			expected: &eventrouter.InputEvent{
				Source:      "atlassian",
				Type:        "comment_created",
				Description: "Test User commented on PROJ-123",
				Data:        "[~accountid:557058:abc] and [~accountid:5b10ac] please review",
				URL:         "https://mycompany.atlassian.net/browse/PROJ-123?focusedCommentId=10009",
				Attrs:       eventrouter.Attrs{"mention": {"557058:abc", "5b10ac"}, "user": {"abc123"}},
				Meta:        AtlassianMeta{AuthorAccountID: "abc123", AuthorDisplayName: "Test User"},
			},
		},
		{
			name: "NoGritzPrefix",
			payload: atlassian.WebhookPayload{
				WebhookEvent: "comment_created",
				Comment: &atlassian.Comment{
					Body:   "just a regular comment",
					Author: atlassian.User{AccountID: "abc123", DisplayName: "Test User"},
				},
				Issue: &atlassian.Issue{
					Key:  "PROJ-123",
					Self: "https://mycompany.atlassian.net/rest/api/2/issue/12345",
				},
			},
			expected: &eventrouter.InputEvent{
				Source:      "atlassian",
				Type:        "comment_created",
				Description: "Test User commented on PROJ-123",
				Data:        "just a regular comment",
				URL:         "https://mycompany.atlassian.net/browse/PROJ-123",
				Attrs:       eventrouter.Attrs{"mention": nil, "user": {"abc123"}},
				Meta:        AtlassianMeta{AuthorAccountID: "abc123", AuthorDisplayName: "Test User"},
			},
		},
		{
			name: "NilComment",
			payload: atlassian.WebhookPayload{
				WebhookEvent: "comment_created",
				Comment:      nil,
				Issue: &atlassian.Issue{
					Key:  "PROJ-123",
					Self: "https://mycompany.atlassian.net/rest/api/2/issue/12345",
				},
			},
			expected: nil,
		},
		{
			name: "NilIssue",
			payload: atlassian.WebhookPayload{
				WebhookEvent: "comment_created",
				Comment: &atlassian.Comment{
					Body:   "gritz: test",
					Author: atlassian.User{AccountID: "abc123", DisplayName: "Test User"},
				},
				Issue: nil,
			},
			expected: nil,
		},
		{
			name: "UnknownEventType",
			payload: atlassian.WebhookPayload{
				WebhookEvent: "issue_updated",
			},
			expected: nil,
		},
		{
			name: "CommentUpdatedIgnored",
			payload: atlassian.WebhookPayload{
				WebhookEvent: "comment_updated",
				Comment: &atlassian.Comment{
					Body:   "gritz: test",
					Author: atlassian.User{AccountID: "abc123", DisplayName: "Test User"},
				},
				Issue: &atlassian.Issue{
					Key:  "PROJ-123",
					Self: "https://mycompany.atlassian.net/rest/api/2/issue/12345",
				},
			},
			expected: nil,
		},
		{
			name: "WhitespacePrefix",
			payload: atlassian.WebhookPayload{
				WebhookEvent: "comment_created",
				Comment: &atlassian.Comment{
					ID:     "20002",
					Body:   "  gritz: trimmed",
					Author: atlassian.User{AccountID: "abc123", DisplayName: "Test User"},
				},
				Issue: &atlassian.Issue{
					Key:  "PROJ-1",
					Self: "https://mycompany.atlassian.net/rest/api/2/issue/1",
				},
			},
			expected: &eventrouter.InputEvent{
				Source:      "atlassian",
				Type:        "comment_created",
				Description: "Test User commented on PROJ-1",
				Data:        "gritz: trimmed",
				URL:         "https://mycompany.atlassian.net/browse/PROJ-1?focusedCommentId=20002",
				Attrs:       eventrouter.Attrs{"mention": nil, "user": {"abc123"}},
				Meta:        AtlassianMeta{AuthorAccountID: "abc123", AuthorDisplayName: "Test User"},
			},
		},
		{
			name: "LabelAdded",
			payload: atlassian.WebhookPayload{
				WebhookEvent: "jira:issue_updated",
				User:         &atlassian.User{AccountID: "abc123", DisplayName: "Test User"},
				Issue: &atlassian.Issue{
					Key:  "PROJ-7",
					Self: "https://mycompany.atlassian.net/rest/api/2/issue/7",
				},
				Changelog: &atlassian.Changelog{
					Items: []atlassian.ChangelogItem{
						{Field: "labels", FromString: "bug", ToString: "bug gritz"},
					},
				},
			},
			expected: &eventrouter.InputEvent{
				Source:      "atlassian",
				Type:        "label_added",
				Description: `Test User added label(s) "gritz" to PROJ-7`,
				Attrs:       eventrouter.Attrs{"label": {"gritz"}, "user": {"abc123"}},
				URL:         "https://mycompany.atlassian.net/browse/PROJ-7",
				Meta:        AtlassianMeta{AuthorAccountID: "abc123", AuthorDisplayName: "Test User"},
			},
		},
		{
			name: "MultipleLabelsAdded",
			payload: atlassian.WebhookPayload{
				WebhookEvent: "jira:issue_updated",
				User:         &atlassian.User{AccountID: "abc123", DisplayName: "Test User"},
				Issue: &atlassian.Issue{
					Key:  "PROJ-8",
					Self: "https://mycompany.atlassian.net/rest/api/2/issue/8",
				},
				Changelog: &atlassian.Changelog{
					Items: []atlassian.ChangelogItem{
						{Field: "labels", FromString: "", ToString: "gritz urgent"},
					},
				},
			},
			expected: &eventrouter.InputEvent{
				Source:      "atlassian",
				Type:        "label_added",
				Description: `Test User added label(s) "gritz", "urgent" to PROJ-8`,
				Attrs:       eventrouter.Attrs{"label": {"gritz", "urgent"}, "user": {"abc123"}},
				URL:         "https://mycompany.atlassian.net/browse/PROJ-8",
				Meta:        AtlassianMeta{AuthorAccountID: "abc123", AuthorDisplayName: "Test User"},
			},
		},
		{
			name: "IssueUpdatedNoLabelChange",
			payload: atlassian.WebhookPayload{
				WebhookEvent: "jira:issue_updated",
				User:         &atlassian.User{AccountID: "abc123", DisplayName: "Test User"},
				Issue: &atlassian.Issue{
					Key:  "PROJ-9",
					Self: "https://mycompany.atlassian.net/rest/api/2/issue/9",
				},
				Changelog: &atlassian.Changelog{
					Items: []atlassian.ChangelogItem{
						{Field: "status", FromString: "To Do", ToString: "In Progress"},
					},
				},
			},
			expected: nil,
		},
		{
			name: "LabelRemovedIgnored",
			payload: atlassian.WebhookPayload{
				WebhookEvent: "jira:issue_updated",
				User:         &atlassian.User{AccountID: "abc123", DisplayName: "Test User"},
				Issue: &atlassian.Issue{
					Key:  "PROJ-9",
					Self: "https://mycompany.atlassian.net/rest/api/2/issue/9",
				},
				Changelog: &atlassian.Changelog{
					Items: []atlassian.ChangelogItem{
						{Field: "labels", FromString: "bug gritz", ToString: "bug"},
					},
				},
			},
			expected: nil,
		},
		{
			name: "LabelAddedNilUser",
			payload: atlassian.WebhookPayload{
				WebhookEvent: "jira:issue_updated",
				Issue: &atlassian.Issue{
					Key:  "PROJ-9",
					Self: "https://mycompany.atlassian.net/rest/api/2/issue/9",
				},
				Changelog: &atlassian.Changelog{
					Items: []atlassian.ChangelogItem{
						{Field: "labels", FromString: "", ToString: "gritz"},
					},
				},
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.payload)
			assert.NilError(t, err)
			got, err := toInputEvent(body)
			assert.NilError(t, err)
			assert.DeepEqual(t, got, tt.expected)
		})
	}
}

func makeAtlassianWebhookRequest(t *testing.T, orgID int64, payload any, secret string) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	assert.NilError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/webhook/atlassian?org="+strconv.FormatInt(orgID, 10), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature", atlassian.SignWebhook(body, secret))
	return req
}

func TestHandleAtlassianWebhookRoutesToTask(t *testing.T) {
	secret := "test-webhook-secret"
	accountID := "atlassian-abc123"
	router := &RouterMock{
		RouteFunc: func(ctx context.Context, input eventrouter.InputEvent) (int, error) {
			return 1, nil
		},
	}
	h := &WebhookHandler{
		Router: router,
		Store: &StoreMock{
			GetOrgAtlassianWebhookSecretFunc: func(ctx context.Context, tx *sql.Tx, orgID int64) (string, error) {
				return secret, nil
			},
			GetUserByAtlassianAccountIDFunc: func(ctx context.Context, tx *sql.Tx, id string) (*model.User, error) {
				if id == accountID {
					return &model.User{ID: "user-1"}, nil
				}
				return nil, sql.ErrNoRows
			},
		},
	}

	payload := atlassian.WebhookPayload{
		WebhookEvent: "comment_created",
		Comment: &atlassian.Comment{
			ID:     "30003",
			Body:   "gritz: please fix the tests",
			Author: atlassian.User{AccountID: accountID, DisplayName: "Test User"},
		},
		Issue: &atlassian.Issue{
			Key:  "PROJ-10",
			Self: "https://mycompany.atlassian.net/rest/api/2/issue/10",
		},
	}

	req := makeAtlassianWebhookRequest(t, 1, payload, secret)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, rec.Code, http.StatusOK)
	assert.Equal(t, rec.Body.String(), "processed")

	assert.DeepEqual(t, router.RoutedInputs(), []eventrouter.InputEvent{{
		Source:      "atlassian",
		Type:        "comment_created",
		Description: "Test User commented on PROJ-10",
		Data:        "gritz: please fix the tests",
		URL:         "https://mycompany.atlassian.net/browse/PROJ-10?focusedCommentId=30003",
		Attrs:       eventrouter.Attrs{"mention": nil, "user": {"atlassian-abc123"}},
		UserID:      "user-1",
		Orgs:        []int64{1},
		Meta:        AtlassianMeta{AuthorAccountID: accountID, AuthorDisplayName: "Test User"},
	}})
}

func TestHandleAtlassianWebhookRoutesLabelAdded(t *testing.T) {
	secret := "test-webhook-secret"
	accountID := "atlassian-abc123"
	router := &RouterMock{
		RouteFunc: func(ctx context.Context, input eventrouter.InputEvent) (int, error) {
			return 1, nil
		},
	}
	h := &WebhookHandler{
		Router: router,
		Store: &StoreMock{
			GetOrgAtlassianWebhookSecretFunc: func(ctx context.Context, tx *sql.Tx, orgID int64) (string, error) {
				return secret, nil
			},
			GetUserByAtlassianAccountIDFunc: func(ctx context.Context, tx *sql.Tx, id string) (*model.User, error) {
				if id == accountID {
					return &model.User{ID: "user-1"}, nil
				}
				return nil, sql.ErrNoRows
			},
		},
	}

	payload := atlassian.WebhookPayload{
		WebhookEvent: "jira:issue_updated",
		User:         &atlassian.User{AccountID: accountID, DisplayName: "Test User"},
		Issue: &atlassian.Issue{
			Key:  "PROJ-10",
			Self: "https://mycompany.atlassian.net/rest/api/2/issue/10",
		},
		Changelog: &atlassian.Changelog{
			Items: []atlassian.ChangelogItem{
				{Field: "labels", FromString: "bug", ToString: "bug gritz urgent"},
			},
		},
	}

	req := makeAtlassianWebhookRequest(t, 1, payload, secret)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, rec.Code, http.StatusOK)
	assert.Equal(t, rec.Body.String(), "processed")

	assert.DeepEqual(t, router.RoutedInputs(), []eventrouter.InputEvent{{
		Source:      "atlassian",
		Type:        "label_added",
		Description: `Test User added label(s) "gritz", "urgent" to PROJ-10`,
		Attrs:       eventrouter.Attrs{"label": {"gritz", "urgent"}, "user": {"atlassian-abc123"}},
		URL:         "https://mycompany.atlassian.net/browse/PROJ-10",
		UserID:      "user-1",
		Orgs:        []int64{1},
		Meta:        AtlassianMeta{AuthorAccountID: accountID, AuthorDisplayName: "Test User"},
	}})
}

func TestHandleAtlassianWebhookIgnoredEventType(t *testing.T) {
	secret := "test-webhook-secret"
	h := &WebhookHandler{
		Store: &StoreMock{
			GetOrgAtlassianWebhookSecretFunc: func(ctx context.Context, tx *sql.Tx, orgID int64) (string, error) {
				return secret, nil
			},
		},
	}

	payload := atlassian.WebhookPayload{
		WebhookEvent: "issue_updated",
	}
	req := makeAtlassianWebhookRequest(t, 1, payload, secret)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, rec.Code, http.StatusOK)
	assert.Equal(t, rec.Body.String(), "ignored")
}

// An unlinked actor is no longer dropped: the handler leaves UserID empty and
// routes via the org named in the ?org= param, where a Public rule can match.
func TestHandleAtlassianWebhookUnlinkedActorRoutesViaOrg(t *testing.T) {
	secret := "test-webhook-secret"
	router := &RouterMock{
		RouteFunc: func(ctx context.Context, input eventrouter.InputEvent) (int, error) {
			return 1, nil
		},
	}
	h := &WebhookHandler{
		Router: router,
		Store: &StoreMock{
			GetOrgAtlassianWebhookSecretFunc: func(ctx context.Context, tx *sql.Tx, orgID int64) (string, error) {
				return secret, nil
			},
			GetUserByAtlassianAccountIDFunc: func(ctx context.Context, tx *sql.Tx, id string) (*model.User, error) {
				return nil, sql.ErrNoRows
			},
		},
	}

	payload := atlassian.WebhookPayload{
		WebhookEvent: "comment_created",
		Comment: &atlassian.Comment{
			Body:   "gritz: test",
			Author: atlassian.User{AccountID: "unknown-account", DisplayName: "Unknown"},
		},
		Issue: &atlassian.Issue{
			Key:  "PROJ-1",
			Self: "https://mycompany.atlassian.net/rest/api/2/issue/1",
		},
	}
	req := makeAtlassianWebhookRequest(t, 1, payload, secret)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, rec.Code, http.StatusOK)
	assert.Equal(t, rec.Body.String(), "processed")

	// The event routes with an empty UserID and the ?org= org, so a Public rule
	// on that org can fire.
	inputs := router.RoutedInputs()
	assert.Assert(t, cmp.Len(inputs, 1))
	assert.Equal(t, inputs[0].UserID, "")
	assert.DeepEqual(t, inputs[0].Orgs, []int64{1})
}

func TestHandleAtlassianWebhookInvalidSignature(t *testing.T) {
	secret := "test-webhook-secret"
	h := &WebhookHandler{
		Store: &StoreMock{
			GetOrgAtlassianWebhookSecretFunc: func(ctx context.Context, tx *sql.Tx, orgID int64) (string, error) {
				return secret, nil
			},
		},
	}

	payload := atlassian.WebhookPayload{WebhookEvent: "comment_created"}
	body, err := json.Marshal(payload)
	assert.NilError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/webhook/atlassian?org=1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature", "sha256=deadbeef")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, rec.Code, http.StatusForbidden)
}

func TestHandleAtlassianWebhookMissingOrg(t *testing.T) {
	h := &WebhookHandler{}

	req := httptest.NewRequest(http.MethodPost, "/webhook/atlassian", bytes.NewReader([]byte("{}")))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, rec.Code, http.StatusBadRequest)
}
