package model

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestAgentLink(t *testing.T) {
	agent := &Agent{Links: []AgentLink{
		{Type: "jira.issue"},
		{Type: "github.pr", Kind: "owned-pr", Claim: "pr-owner"},
		{Type: "github.pr"},
	}}
	tests := []struct {
		kind string
		want AgentLink
		ok   bool
	}{
		{"jira.issue", AgentLink{Type: "jira.issue"}, true},
		{"owned-pr", AgentLink{Type: "github.pr", Kind: "owned-pr", Claim: "pr-owner"}, true},
		{"github.pr", AgentLink{Type: "github.pr"}, true},
		{"missing", AgentLink{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			got, ok := agent.Link(tt.kind)
			assert.Equal(t, ok, tt.ok)
			assert.DeepEqual(t, got, tt.want)
		})
	}
}
