package model

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestTaskURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		taskID  int64
		orgID   int64
		want    string
	}{
		{
			"basic",
			"https://gritz.example.com",
			42,
			7,
			"https://gritz.example.com/ui/tasks/42?org=7",
		},
		{
			"multi-digit org",
			"https://gritz.dev",
			804,
			123,
			"https://gritz.dev/ui/tasks/804?org=123",
		},
		{"empty base url", "", 42, 7, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, TaskURL(tt.baseURL, tt.taskID, tt.orgID), tt.want)
		})
	}
}

func TestRoutingKey(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		// GitHub web — issues
		{"github issue", "https://github.com/o/r/issues/5", "https://github.com/o/r/issues/5"},
		{
			"github issue comment fragment",
			"https://github.com/o/r/issues/5#issuecomment-9",
			"https://github.com/o/r/issues/5",
		},
		{
			"github issue trailing slash",
			"https://github.com/o/r/issues/5/",
			"https://github.com/o/r/issues/5",
		},

		// GitHub web — pull requests
		{"github pull", "https://github.com/o/r/pull/5", "https://github.com/o/r/pull/5"},
		{
			"github pull files",
			"https://github.com/o/r/pull/5/files",
			"https://github.com/o/r/pull/5",
		},
		{
			"github pull commits",
			"https://github.com/o/r/pull/5/commits/abc123",
			"https://github.com/o/r/pull/5",
		},
		{
			"github pull review fragment",
			"https://github.com/o/r/pull/5#pullrequestreview-77",
			"https://github.com/o/r/pull/5",
		},
		{
			"github pull discussion fragment",
			"https://github.com/o/r/pull/5/files#discussion_r123",
			"https://github.com/o/r/pull/5",
		},

		// issues vs pull stay distinct
		{
			"github issues and pull distinct",
			"https://github.com/o/r/issues/5",
			"https://github.com/o/r/issues/5",
		},

		// GitHub API
		{
			"github api issue",
			"https://api.github.com/repos/o/r/issues/5",
			"https://github.com/o/r/issues/5",
		},
		{
			"github api pull",
			"https://api.github.com/repos/o/r/pulls/5",
			"https://github.com/o/r/pull/5",
		},

		// GitHub API comment URL without parent number — unchanged
		{
			"github api issue comment",
			"https://api.github.com/repos/o/r/issues/comments/12345",
			"https://api.github.com/repos/o/r/issues/comments/12345",
		},

		// Jira web
		{
			"jira browse",
			"https://site.atlassian.net/browse/X-1",
			"https://site.atlassian.net/browse/X-1",
		},
		{
			"jira browse focused comment",
			"https://site.atlassian.net/browse/X-1?focusedCommentId=2",
			"https://site.atlassian.net/browse/X-1",
		},

		// Jira API
		{
			"jira api issue",
			"https://site.atlassian.net/rest/api/2/issue/X-1",
			"https://site.atlassian.net/browse/X-1",
		},
		{
			"jira api issue v3",
			"https://site.atlassian.net/rest/api/3/issue/X-1",
			"https://site.atlassian.net/browse/X-1",
		},
		{
			"jira api issue comment",
			"https://site.atlassian.net/rest/api/2/issue/X-1/comment/9",
			"https://site.atlassian.net/browse/X-1",
		},

		// Jira API numeric id — can't map to a key, unchanged
		{
			"jira api numeric id",
			"https://site.atlassian.net/rest/api/2/issue/12345",
			"https://site.atlassian.net/rest/api/2/issue/12345",
		},

		// Non-matching / unchanged
		{
			"github non-numeric",
			"https://github.com/o/r/issues/abc",
			"https://github.com/o/r/issues/abc",
		},
		{
			"github other path",
			"https://github.com/o/r/blob/main/README.md",
			"https://github.com/o/r/blob/main/README.md",
		},
		{"github issues list", "https://github.com/o/r/issues", "https://github.com/o/r/issues"},
		{"unrelated host", "https://example.com/foo/bar", "https://example.com/foo/bar"},
		{"empty", "", ""},
		{"plain text", "not a url", "not a url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, RoutingKey(tt.raw), tt.want)
		})
	}
}
