package model

import (
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

func TestRoutingRuleProtoRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		rule RoutingRule
	}{
		{
			name: "full rule with create and conditions",
			rule: RoutingRule{
				Source: "atlassian",
				Type:   "label_added",
				Conditions: []Condition{
					{Attr: "body", Op: "prefix", Value: "gritz:"},
					{Attr: "mention", Op: "equals", Value: "abc123"},
					{Attr: "label", Op: "equals", Value: "gritz"},
				},
				Create: &CreateTaskAction{
					Workspace:   "default",
					Runner:      "runner-1",
					Prompt:      "do the thing",
					AutoArchive: time.Hour,
					Variables:   map[string]string{"REPO": "gritz"},
				},
				Wakeup: true,
				Public: true,
			},
		},
		{
			name: "public opt-in",
			rule: RoutingRule{
				Source: "github",
				Type:   "issue_comment",
				Public: true,
			},
		},
		{
			name: "wakeup disabled, no conditions or create",
			rule: RoutingRule{Source: "github", Wakeup: false},
		},
		{
			name: "conditions only, no create",
			rule: RoutingRule{
				Source:     "atlassian",
				Type:       "label_added",
				Conditions: []Condition{{Attr: "label", Op: "equals", Value: "urgent"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RoutingRuleFromProto(tt.rule.Proto())
			assert.DeepEqual(t, got, tt.rule)
		})
	}
}
