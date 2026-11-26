package model

import "fmt"

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
