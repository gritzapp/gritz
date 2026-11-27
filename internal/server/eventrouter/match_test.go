package eventrouter

import (
	"testing"

	"github.com/gritzapp/gritz/internal/model"
	"gotest.tools/v3/assert"
)

func TestMatch(t *testing.T) {
	tests := []struct {
		name    string
		trigger model.AgentTrigger
		event   InputEvent
		want    bool
	}{
		// --- selector: source/type ---
		{
			name:    "empty trigger matches everything",
			trigger: model.AgentTrigger{},
			event:   InputEvent{Source: "github", Type: "issue_comment", Data: "hello"},
			want:    true,
		},
		{
			name:    "source match",
			trigger: model.AgentTrigger{Source: "github"},
			event:   InputEvent{Source: "github"},
			want:    true,
		},
		{
			name:    "source mismatch",
			trigger: model.AgentTrigger{Source: "github"},
			event:   InputEvent{Source: "atlassian"},
			want:    false,
		},
		{
			name:    "type match",
			trigger: model.AgentTrigger{Type: "issue_comment"},
			event:   InputEvent{Source: "github", Type: "issue_comment"},
			want:    true,
		},
		{
			name:    "type mismatch",
			trigger: model.AgentTrigger{Type: "issue_comment"},
			event:   InputEvent{Source: "github", Type: "pull_request_review"},
			want:    false,
		},
		{
			name:    "empty source is a wildcard",
			trigger: model.AgentTrigger{Type: "issue_comment"},
			event:   InputEvent{Source: "atlassian", Type: "issue_comment"},
			want:    true,
		},
		{
			name:    "empty type is a wildcard",
			trigger: model.AgentTrigger{Source: "github"},
			event:   InputEvent{Source: "github", Type: "anything"},
			want:    true,
		},

		// --- Prefix -> {body, prefix} ---
		{
			name: "prefix match",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "body", Op: "prefix", Value: "gritz:"},
				},
			},
			event: InputEvent{Data: "gritz: fix the tests"},
			want:  true,
		},
		{
			name: "prefix mismatch",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "body", Op: "prefix", Value: "gritz:"},
				},
			},
			event: InputEvent{Data: "just a comment"},
			want:  false,
		},

		// --- Mention -> {mention, equals} ---
		// The event now supplies extracted mentions in Attrs["mention"]
		// rather than the router parsing @-syntax out of Data.
		{
			name: "github mention match",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "mention", Op: "equals", Value: "botuser"},
				},
			},
			event: InputEvent{Source: "github", Attrs: Attrs{"mention": {"botuser"}}},
			want:  true,
		},
		{
			// DELTA: legacy Mention matching was case-insensitive; conditions
			// are literal, so BotUser no longer matches an extracted "botuser".
			name: "github mention is now case sensitive",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "mention", Op: "equals", Value: "BotUser"},
				},
			},
			event: InputEvent{Source: "github", Attrs: Attrs{"mention": {"botuser"}}},
			want:  false,
		},
		{
			name: "github mention no substring match",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "mention", Op: "equals", Value: "bot"},
				},
			},
			event: InputEvent{Source: "github", Attrs: Attrs{"mention": {"botuser"}}},
			want:  false,
		},
		{
			name: "github mention mismatch",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "mention", Op: "equals", Value: "botuser"},
				},
			},
			event: InputEvent{Source: "github", Attrs: Attrs{"mention": {"someoneelse"}}},
			want:  false,
		},
		{
			name: "atlassian mention match",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "mention", Op: "equals", Value: "abc123"},
				},
			},
			event: InputEvent{Source: "atlassian", Attrs: Attrs{"mention": {"abc123"}}},
			want:  true,
		},
		{
			name: "atlassian mention mismatch",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "mention", Op: "equals", Value: "abc123"},
				},
			},
			event: InputEvent{Source: "atlassian", Attrs: Attrs{"mention": {"xyz"}}},
			want:  false,
		},
		{
			// A mention condition against an event that carries no mention
			// attr fails, regardless of what's in Data.
			name: "mention condition on event without mention attr fails",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "mention", Op: "equals", Value: "user"},
				},
			},
			event: InputEvent{Source: "unknown", Data: "@user hello"},
			want:  false,
		},

		// --- Assignee -> {assignee, equals} ---
		{
			name: "github assignee match",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "assignee", Op: "equals", Value: "icholy-bot"},
				},
			},
			event: InputEvent{
				Source: "github",
				Type:   "pull_request_assigned",
				Attrs:  Attrs{"assignee": {"icholy-bot"}},
			},
			want: true,
		},
		{
			// DELTA: legacy Assignee matching was case-insensitive; conditions
			// are literal, so Icholy-Bot no longer matches "icholy-bot".
			name: "github assignee is now case sensitive",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "assignee", Op: "equals", Value: "icholy-bot"},
				},
			},
			event: InputEvent{
				Source: "github",
				Type:   "pull_request_assigned",
				Attrs:  Attrs{"assignee": {"Icholy-Bot"}},
			},
			want: false,
		},
		{
			name: "github assignee mismatch",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "assignee", Op: "equals", Value: "icholy-bot"},
				},
			},
			event: InputEvent{
				Source: "github",
				Type:   "pull_request_assigned",
				Attrs:  Attrs{"assignee": {"octocat"}},
			},
			want: false,
		},
		{
			// Replaces the legacy "assignee empty on non-assignment event"
			// case: an event that carries no assignee attr fails the condition.
			name: "assignee condition on event without assignee attr fails",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "assignee", Op: "equals", Value: "icholy-bot"},
				},
			},
			event: InputEvent{Source: "github", Type: "issue_comment", Data: "hi"},
			want:  false,
		},

		// --- URLPrefix -> {url, prefix} ---
		{
			name:    "empty url prefix (no condition) matches any url",
			trigger: model.AgentTrigger{},
			event:   InputEvent{URL: "https://github.com/icholy/gritz/pull/1"},
			want:    true,
		},
		{
			name: "url prefix match",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "url", Op: "prefix", Value: "https://github.com/icholy/gritz/"},
				},
			},
			event: InputEvent{URL: "https://github.com/icholy/gritz/pull/1"},
			want:  true,
		},
		{
			name: "url prefix mismatch",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "url", Op: "prefix", Value: "https://github.com/icholy/gritz/"},
				},
			},
			event: InputEvent{URL: "https://github.com/other/repo/pull/1"},
			want:  false,
		},
		{
			name: "url prefix mismatch on empty url",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "url", Op: "prefix", Value: "https://github.com/icholy/gritz/"},
				},
			},
			event: InputEvent{URL: ""},
			want:  false,
		},
		{
			name: "url prefix is independent of body",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "url", Op: "prefix", Value: "https://github.com/icholy/gritz/"},
				},
			},
			event: InputEvent{
				URL:  "https://github.com/icholy/gritz/issues/1",
				Data: "no gritz: prefix here",
			},
			want: true,
		},
		{
			name: "body prefix is independent of url",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "body", Op: "prefix", Value: "gritz:"},
				},
			},
			event: InputEvent{
				URL:  "https://example.com/whatever",
				Data: "gritz: do the thing",
			},
			want: true,
		},

		// --- Value -> {label, equals} ---
		{
			name: "label match",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "label", Op: "equals", Value: "gritz"},
				},
			},
			event: InputEvent{Attrs: Attrs{"label": {"gritz", "urgent"}}},
			want:  true,
		},
		{
			name: "label no match",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "label", Op: "equals", Value: "gritz"},
				},
			},
			event: InputEvent{Attrs: Attrs{"label": {"bug", "urgent"}}},
			want:  false,
		},
		{
			name:    "no label condition is a wildcard",
			trigger: model.AgentTrigger{},
			event:   InputEvent{Attrs: Attrs{"label": {"gritz"}}},
			want:    true,
		},
		{
			name: "label condition with empty event labels no match",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "label", Op: "equals", Value: "gritz"},
				},
			},
			event: InputEvent{Attrs: Attrs{"label": nil}},
			want:  false,
		},
		{
			name: "label equals requires exact membership not substring",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "label", Op: "equals", Value: "agent"},
				},
			},
			event: InputEvent{Attrs: Attrs{"label": {"gritz"}}},
			want:  false,
		},

		// --- section 2 semantics: AND, missing attr, multi-value, contains ---
		{
			name: "multiple conditions AND together (all match)",
			trigger: model.AgentTrigger{Conditions: []model.Condition{
				{Attr: "body", Op: "prefix", Value: "gritz:"},
				{Attr: "label", Op: "equals", Value: "urgent"},
			}},
			event: InputEvent{Data: "gritz: do it", Attrs: Attrs{"label": {"urgent"}}},
			want:  true,
		},
		{
			name: "multiple conditions AND together (one fails)",
			trigger: model.AgentTrigger{Conditions: []model.Condition{
				{Attr: "body", Op: "prefix", Value: "gritz:"},
				{Attr: "label", Op: "equals", Value: "urgent"},
			}},
			event: InputEvent{Data: "gritz: do it", Attrs: Attrs{"label": {"backlog"}}},
			want:  false,
		},
		{
			name: "condition on a missing attr fails even when others match",
			trigger: model.AgentTrigger{Conditions: []model.Condition{
				{Attr: "body", Op: "prefix", Value: "gritz:"},
				{Attr: "assignee", Op: "equals", Value: "icholy-bot"},
			}},
			event: InputEvent{Data: "gritz: do it"},
			want:  false,
		},
		{
			name: "multi-value attr membership: any value matches",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "label", Op: "equals", Value: "urgent"},
				},
			},
			event: InputEvent{Attrs: Attrs{"label": {"backlog", "urgent", "triage"}}},
			want:  true,
		},
		{
			name: "contains op matches a substring of a value",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "body", Op: "contains", Value: "please"},
				},
			},
			event: InputEvent{Data: "hey, please fix this"},
			want:  true,
		},
		{
			name: "contains op mismatch",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "body", Op: "contains", Value: "please"},
				},
			},
			event: InputEvent{Data: "just a comment"},
			want:  false,
		},

		// --- combined selector + conditions ---
		{
			name: "combined source type label all match",
			trigger: model.AgentTrigger{
				Source:     "atlassian",
				Type:       "label_added",
				Conditions: []model.Condition{{Attr: "label", Op: "equals", Value: "gritz"}},
			},
			event: InputEvent{
				Source: "atlassian",
				Type:   "label_added",
				Attrs:  Attrs{"label": {"gritz"}},
			},
			want: true,
		},
		{
			name: "combined label mismatch",
			trigger: model.AgentTrigger{
				Source:     "atlassian",
				Type:       "label_added",
				Conditions: []model.Condition{{Attr: "label", Op: "equals", Value: "gritz"}},
			},
			event: InputEvent{
				Source: "atlassian",
				Type:   "label_added",
				Attrs:  Attrs{"label": {"bug"}},
			},
			want: false,
		},
		{
			name: "combined gate type mismatch short-circuits conditions",
			trigger: model.AgentTrigger{
				Source: "github",
				Type:   "pull_request_assigned",
				Conditions: []model.Condition{
					{Attr: "assignee", Op: "equals", Value: "icholy-bot"},
				},
			},
			event: InputEvent{
				Source: "github",
				Type:   "issue_assigned",
				Attrs:  Attrs{"assignee": {"icholy-bot"}},
			},
			want: false,
		},
		{
			name: "combined gate assignee condition mismatch",
			trigger: model.AgentTrigger{
				Source: "github",
				Type:   "pull_request_assigned",
				Conditions: []model.Condition{
					{Attr: "assignee", Op: "equals", Value: "icholy-bot"},
				},
			},
			event: InputEvent{
				Source: "github",
				Type:   "pull_request_assigned",
				Attrs:  Attrs{"assignee": {"octocat"}},
			},
			want: false,
		},

		// --- glob ---
		{
			name: "glob holds when any of many values matches",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "path", Op: "glob", Value: "docs/**/*.md"},
				},
			},
			event: InputEvent{Attrs: Attrs{"path": {"go.mod", "docs/src/index.md", "main.go"}}},
			want:  true,
		},
		{
			name: "glob fails when no value matches",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "path", Op: "glob", Value: "docs/**/*.md"},
				},
			},
			event: InputEvent{Attrs: Attrs{"path": {"go.mod", "docs/diagram.svg"}}},
			want:  false,
		},

		// --- negated ops hold exactly when the positive op does not ---
		{
			name: "not_equals holds when no value equals",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "mention", Op: "not_equals", Value: "bot"},
				},
			},
			event: InputEvent{Attrs: Attrs{"mention": {"alice", "bob"}}},
			want:  true,
		},
		{
			name: "not_equals fails when any value equals",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "mention", Op: "not_equals", Value: "bob"},
				},
			},
			event: InputEvent{Attrs: Attrs{"mention": {"alice", "bob"}}},
			want:  false,
		},
		{
			name: "not_prefix holds when no value has the prefix",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "label", Op: "not_prefix", Value: "team-"},
				},
			},
			event: InputEvent{Attrs: Attrs{"label": {"bug", "urgent"}}},
			want:  true,
		},
		{
			name: "not_prefix fails when a value has the prefix",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "label", Op: "not_prefix", Value: "team-"},
				},
			},
			event: InputEvent{Attrs: Attrs{"label": {"bug", "team-a"}}},
			want:  false,
		},
		{
			name: "not_contains holds when no value contains",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "label", Op: "not_contains", Value: "skip"},
				},
			},
			event: InputEvent{Attrs: Attrs{"label": {"bug"}}},
			want:  true,
		},
		{
			name: "not_contains fails when a value contains",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "label", Op: "not_contains", Value: "skip"},
				},
			},
			event: InputEvent{Attrs: Attrs{"label": {"ci-skip"}}},
			want:  false,
		},
		{
			name: "not_glob holds when no value matches",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "path", Op: "not_glob", Value: "docs/**/*.md"},
				},
			},
			event: InputEvent{Attrs: Attrs{"path": {"go.mod", "docs/diagram.svg"}}},
			want:  true,
		},
		{
			name: "not_glob fails when any of many values matches",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "path", Op: "not_glob", Value: "docs/**/*.md"},
				},
			},
			event: InputEvent{Attrs: Attrs{"path": {"go.mod", "docs/src/index.md"}}},
			want:  false,
		},
		{
			name: "not_equals holds on an absent attr",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "label", Op: "not_equals", Value: "x"},
				},
			},
			event: InputEvent{Attrs: Attrs{"mention": {"x"}}},
			want:  true,
		},
		{
			name: "not_prefix holds on an absent attr",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "label", Op: "not_prefix", Value: "x"},
				},
			},
			event: InputEvent{Attrs: Attrs{"mention": {"x"}}},
			want:  true,
		},
		{
			name: "not_contains holds on an absent attr",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "label", Op: "not_contains", Value: "x"},
				},
			},
			event: InputEvent{Attrs: Attrs{"mention": {"x"}}},
			want:  true,
		},
		{
			name: "not_glob holds on an absent attr",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "label", Op: "not_glob", Value: "x"},
				},
			},
			event: InputEvent{Attrs: Attrs{"mention": {"x"}}},
			want:  true,
		},
		{
			name: "not_prefix fails on a derived attr that has the prefix",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "body", Op: "not_prefix", Value: "gritz:"},
				},
			},
			event: InputEvent{Data: "gritz: fix it"},
			want:  false,
		},

		// --- unknown op never matches ---
		{
			name: "unknown op never matches",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "body", Op: "regex", Value: "gritz:"},
				},
			},
			event: InputEvent{Data: "gritz: do it"},
			want:  false,
		},
		{
			name: "negation of an unknown op never matches",
			trigger: model.AgentTrigger{
				Conditions: []model.Condition{
					{Attr: "body", Op: "not_regex", Value: "gritz:"},
				},
			},
			event: InputEvent{Data: "hello"},
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.DeepEqual(t, Match(tt.trigger, tt.event), tt.want)
		})
	}
}
