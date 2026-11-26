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

func TestTaskBadgeURL(t *testing.T) {
	assert.Equal(t,
		TaskBadgeURL("https://gritz.dev", 2474),
		"https://img.shields.io/endpoint?url=https://gritz.dev/badge/2474.json")
	assert.Equal(t, TaskBadgeURL("", 2474), "")
}
