package model

import (
	"fmt"

	"github.com/gritzapp/gritz/internal/x/atlassian"
	"github.com/gritzapp/gritz/internal/x/githubx"
)

// TaskURL returns the user-facing UI URL for a task, including the org query
// parameter so deep links resolve to the correct org for users with
// multiple memberships.
func TaskURL(baseURL string, taskID, orgID int64) string {
	if baseURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/ui/tasks/%d?org=%d", baseURL, taskID, orgID)
}

// TaskBadgeURL returns the shields.io image URL rendering the task's badge,
// which shields.io fills from the server's public /badge/{id}.json endpoint.
func TaskBadgeURL(baseURL string, taskID int64) string {
	if baseURL == "" {
		return ""
	}
	return fmt.Sprintf("https://img.shields.io/endpoint?url=%s/badge/%d.json", baseURL, taskID)
}

// RoutingKey reduces a recognized resource URL to a stable routing key, so two
// URLs that point at the same logical resource — a comment vs. its issue, or an
// API URL vs. its web URL, or two jobs of one Actions workflow run — produce
// the same key. The key is the canonical URL of the first built-in link type
// that accepts raw (GitHub issues, PRs and workflow runs, Jira issues; see
// githubx.CanonicalIssue and friends); anything else is returned unchanged.
func RoutingKey(raw string) string {
	if key, ok := githubx.CanonicalIssue(raw); ok {
		return key
	}
	if key, ok := githubx.CanonicalPullRequest(raw); ok {
		return key
	}
	if key, ok := githubx.CanonicalWorkflowRun(raw); ok {
		return key
	}
	if key, ok := atlassian.CanonicalIssue(raw); ok {
		return key
	}
	return raw
}
