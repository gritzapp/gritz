package model

import (
	"testing"
	"time"

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

// TestAgentTaskAndEvents pins the agent -> task/events mapping a schedule's
// fire materializes: a pending/start task carrying the agent's template, then a
// created event by the actor, the start command and one wake instruction with
// the agent's prompt, or the generated line when it has none.
func TestAgentTaskAndEvents(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   string
	}{
		{"prompt", "Bump outdated dependencies.", "Bump outdated dependencies."},
		{"no prompt", "", "generated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := &Agent{
				ID:          3,
				OrgID:       7,
				Name:        "deps",
				Runner:      "r",
				Workspace:   "w",
				Prompt:      tt.prompt,
				AutoArchive: time.Hour,
				Variables:   map[string]string{"JIRA_ISSUE": "APP-142"},
			}

			task := agent.Task("nightly")
			assert.DeepEqual(t, task, &Task{
				Name:        "nightly",
				Runner:      "r",
				Workspace:   "w",
				AgentID:     3,
				Status:      TaskStatusPending,
				Command:     TaskCommandStart,
				Version:     1,
				OrgID:       7,
				AutoArchive: time.Hour,
				Variables:   map[string]string{"JIRA_ISSUE": "APP-142"},
			})

			// Events reference the inserted task; simulate the id the store would assign.
			task.ID = 42
			assert.DeepEqual(t, agent.Events(task, ScheduleActor, "generated"), []*Event{
				{
					TaskID: 42,
					OrgID:  7,
					Payload: &LifecyclePayload{
						Kind:     LifecycleKindCreated,
						Actor:    ScheduleActor,
						ToStatus: TaskStatusPending.Label(),
					},
				},
				{
					TaskID: 42,
					OrgID:  7,
					Payload: &CommandPayload{
						Command:   TaskCommandStart,
						Version:   1,
						Runner:    "r",
						Workspace: "w",
					},
				},
				{
					TaskID:  42,
					OrgID:   7,
					Wake:    true,
					Payload: &InstructionPayload{Text: tt.want},
				},
			})
		})
	}
}
