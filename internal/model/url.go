package model

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
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
// API URL vs. its web URL — produce the same key. Only recognized URLs (GitHub
// issues/PRs and Jira issues, in both their web and API forms) are normalized;
// anything else is returned unchanged.
//
//	github.com/o/r/issues/5#issuecomment-9            -> github.com/o/r/issues/5
//	github.com/o/r/pull/5/files                       -> github.com/o/r/pull/5
//	api.github.com/repos/o/r/issues/5                 -> github.com/o/r/issues/5
//	api.github.com/repos/o/r/pulls/5                  -> github.com/o/r/pull/5
//	site.atlassian.net/browse/X-1?focusedCommentId=2  -> site.atlassian.net/browse/X-1
//	site.atlassian.net/rest/api/2/issue/X-1           -> site.atlassian.net/browse/X-1
func RoutingKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	// url.Parse strips the fragment (#issuecomment-…) and query
	// (?focusedCommentId=…), so the matching only has to reason about u.Path.
	switch {
	case u.Host == "github.com":
		// /{owner}/{repo}/{issues|pull}/{n}[/...] — trailing segments
		// (/files, /commits/…) and fragments are already dropped.
		if m := githubWebRe.FindStringSubmatch(u.Path); m != nil {
			return fmt.Sprintf("https://github.com/%s/%s/%s/%s", m[1], m[2], m[3], m[4])
		}
	case u.Host == "api.github.com":
		// /repos/{owner}/{repo}/{issues|pulls}/{n} maps to the web key.
		// Comment URLs (/issues/comments/{id}) lack the parent number, so
		// \d+ won't match and they fall through unchanged.
		if m := githubAPIRe.FindStringSubmatch(u.Path); m != nil {
			kind := m[3]
			if kind == "pulls" {
				kind = "pull"
			}
			return fmt.Sprintf("https://github.com/%s/%s/%s/%s", m[1], m[2], kind, m[4])
		}
	case strings.HasSuffix(u.Host, ".atlassian.net"):
		if key, ok := jiraKey(u.Path); ok {
			return fmt.Sprintf("https://%s/browse/%s", u.Host, key)
		}
	}
	return raw
}

var (
	githubWebRe = regexp.MustCompile(`^/([^/]+)/([^/]+)/(issues|pull)/(\d+)`)
	githubAPIRe = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/(issues|pulls)/(\d+)`)
	jiraAPIRe   = regexp.MustCompile(`^/rest/api/\d+/issue/([^/]+)`)
	digitsRe    = regexp.MustCompile(`^\d+$`)
)

// jiraKey extracts the issue key from a Jira browse path (/browse/{KEY}) or a
// REST API issue path (/rest/api/{v}/issue/{KEY}). API URLs that use a numeric
// issue id can't be mapped to a key without a lookup, so they're rejected.
func jiraKey(path string) (string, bool) {
	if rest := strings.TrimPrefix(path, "/browse/"); rest != path {
		if key, _, _ := strings.Cut(rest, "/"); key != "" {
			return key, true
		}
	}
	if m := jiraAPIRe.FindStringSubmatch(path); m != nil && !digitsRe.MatchString(m[1]) {
		return m[1], true
	}
	return "", false
}
