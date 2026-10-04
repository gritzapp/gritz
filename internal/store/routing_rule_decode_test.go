package store_test

import (
	"testing"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store"
	"gotest.tools/v3/assert"
)

func TestDecodeRoutingRules(t *testing.T) {
	tests := []struct {
		name string
		data string
		want []model.RoutingRule
	}{
		{
			// A conditions-native stored rule is decoded directly.
			name: "conditions rule",
			data: `[{"source":"github","type":"issue_comment","conditions":[{"attr":"body","op":"prefix","value":"x:"}],"wakeup":true}]`,
			want: []model.RoutingRule{{
				Source:     "github",
				Type:       "issue_comment",
				Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "x:"}},
				Wakeup:     true,
			}},
		},
		{
			// A bare source/type rule with no conditions decodes to nil conditions.
			name: "bare rule",
			data: `[{"source":"github","type":"issue_comment","wakeup":true}]`,
			want: []model.RoutingRule{{
				Source: "github",
				Type:   "issue_comment",
				Wakeup: true,
			}},
		},
		{
			// An empty stored array (the column default) decodes to an empty slice;
			// like nil it is len 0, so callers apply the ruleless-org fallback.
			name: "empty json array",
			data: `[]`,
			want: []model.RoutingRule{},
		},
		{
			name: "no data",
			data: ``,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules, err := store.DecodeRoutingRules([]byte(tt.data))
			assert.NilError(t, err)
			assert.DeepEqual(t, rules, tt.want)
		})
	}
}
