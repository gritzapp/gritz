package atlassian

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestVerifyWebhook(t *testing.T) {
	secret := "test-secret"
	body := []byte(`{"test": "payload"}`)
	validSig := SignWebhook(body, secret)

	tests := []struct {
		name      string
		signature string
		errMsg    string
	}{
		{
			name:      "ValidSignature",
			signature: validSig,
			errMsg:    "",
		},
		{
			name:      "InvalidSignature",
			signature: "sha256=deadbeef",
			errMsg:    "signature mismatch",
		},
		{
			name:      "MissingSignature",
			signature: "",
			errMsg:    "missing X-Hub-Signature header",
		},
		{
			name:      "UnsupportedFormat",
			signature: "sha1=abc",
			errMsg:    "unsupported signature format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyWebhook(body, tt.signature, secret)
			if tt.errMsg == "" {
				assert.NilError(t, err)
			} else {
				assert.ErrorContains(t, err, tt.errMsg)
			}
		})
	}
}

func TestAddedLabels(t *testing.T) {
	tests := []struct {
		name     string
		payload  WebhookPayload
		expected []string
	}{
		{
			name: "SingleLabelAdded",
			payload: WebhookPayload{
				Changelog: &Changelog{
					Items: []ChangelogItem{
						{Field: "labels", FromString: "", ToString: "gritz"},
					},
				},
			},
			expected: []string{"gritz"},
		},
		{
			name: "LabelAddedToExisting",
			payload: WebhookPayload{
				Changelog: &Changelog{
					Items: []ChangelogItem{
						{Field: "labels", FromString: "bug urgent", ToString: "bug urgent gritz"},
					},
				},
			},
			expected: []string{"gritz"},
		},
		{
			name: "MultipleLabelsAdded",
			payload: WebhookPayload{
				Changelog: &Changelog{
					Items: []ChangelogItem{
						{Field: "labels", FromString: "bug", ToString: "bug gritz urgent"},
					},
				},
			},
			expected: []string{"gritz", "urgent"},
		},
		{
			name: "LabelRemovedOnly",
			payload: WebhookPayload{
				Changelog: &Changelog{
					Items: []ChangelogItem{
						{Field: "labels", FromString: "bug gritz", ToString: "bug"},
					},
				},
			},
			expected: nil,
		},
		{
			name: "OtherFieldChange",
			payload: WebhookPayload{
				Changelog: &Changelog{
					Items: []ChangelogItem{
						{Field: "status", FromString: "To Do", ToString: "In Progress"},
					},
				},
			},
			expected: nil,
		},
		{
			name:     "NoChangelog",
			payload:  WebhookPayload{},
			expected: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.payload.AddedLabels()
			assert.DeepEqual(t, got, tt.expected)
		})
	}
}

func TestNewAssignee(t *testing.T) {
	tests := []struct {
		name     string
		payload  WebhookPayload
		expected *User
	}{
		{
			name: "Assigned",
			payload: WebhookPayload{
				Changelog: &Changelog{
					Items: []ChangelogItem{
						{Field: "assignee", To: "bot456", ToString: "Gritz Bot"},
					},
				},
			},
			expected: &User{AccountID: "bot456", DisplayName: "Gritz Bot"},
		},
		{
			name: "Reassigned",
			payload: WebhookPayload{
				Changelog: &Changelog{
					Items: []ChangelogItem{
						{Field: "labels", FromString: "", ToString: "gritz"},
						{Field: "assignee", From: "old123", FromString: "Old User", To: "bot456", ToString: "Gritz Bot"},
					},
				},
			},
			expected: &User{AccountID: "bot456", DisplayName: "Gritz Bot"},
		},
		{
			name: "Unassigned",
			payload: WebhookPayload{
				Changelog: &Changelog{
					Items: []ChangelogItem{
						{Field: "assignee", From: "old123", FromString: "Old User"},
					},
				},
			},
			expected: nil,
		},
		{
			name: "OtherFieldChange",
			payload: WebhookPayload{
				Changelog: &Changelog{
					Items: []ChangelogItem{
						{Field: "status", FromString: "To Do", ToString: "In Progress"},
					},
				},
			},
			expected: nil,
		},
		{
			name:     "NoChangelog",
			payload:  WebhookPayload{},
			expected: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.payload.NewAssignee()
			assert.DeepEqual(t, got, tt.expected)
		})
	}
}

func TestParseWebhookAssigneeChange(t *testing.T) {
	// Jira sends account ids in from/to and null for an empty side.
	body := []byte(`{"webhookEvent":"jira:issue_updated","changelog":{"items":[{"field":"assignee","fieldtype":"jira","fieldId":"assignee","from":null,"fromString":null,"to":"bot456","toString":"Gritz Bot"}]}}`)
	payload, err := ParseWebhook(body)
	assert.NilError(t, err)
	assert.DeepEqual(t, payload.NewAssignee(), &User{AccountID: "bot456", DisplayName: "Gritz Bot"})
}

func TestIssueBrowseURL(t *testing.T) {
	tests := []struct {
		name     string
		issue    Issue
		expected string
	}{
		{
			name: "ValidSelfLink",
			issue: Issue{
				Key:  "PROJ-123",
				Self: "https://mycompany.atlassian.net/rest/api/2/issue/12345",
			},
			expected: "https://mycompany.atlassian.net/browse/PROJ-123",
		},
		{
			name: "InvalidSelfLink",
			issue: Issue{
				Key:  "PROJ-123",
				Self: "invalid-url",
			},
			expected: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.issue.BrowseURL()
			assert.Equal(t, got, tt.expected)
		})
	}
}

func TestIssueCommentBrowseURL(t *testing.T) {
	tests := []struct {
		name      string
		issue     Issue
		commentID string
		expected  string
	}{
		{
			name: "WithCommentID",
			issue: Issue{
				Key:  "PROJ-123",
				Self: "https://mycompany.atlassian.net/rest/api/2/issue/12345",
			},
			commentID: "10001",
			expected:  "https://mycompany.atlassian.net/browse/PROJ-123?focusedCommentId=10001",
		},
		{
			name: "EmptyCommentID",
			issue: Issue{
				Key:  "PROJ-123",
				Self: "https://mycompany.atlassian.net/rest/api/2/issue/12345",
			},
			commentID: "",
			expected:  "https://mycompany.atlassian.net/browse/PROJ-123",
		},
		{
			name: "UnparseableSelf",
			issue: Issue{
				Key:  "PROJ-123",
				Self: "invalid-url",
			},
			commentID: "10001",
			expected:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.issue.CommentBrowseURL(tt.commentID)
			assert.Equal(t, got, tt.expected)
		})
	}
}
