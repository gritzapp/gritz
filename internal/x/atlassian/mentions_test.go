package atlassian

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestMentions(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{name: "none", body: "just a plain comment", want: nil},
		{
			name: "single",
			body: "[~accountid:557058:abc] please review",
			want: []string{"557058:abc"},
		},
		{
			name: "multiple",
			body: "[~accountid:557058:abc] and [~accountid:5b10ac] please review",
			want: []string{"557058:abc", "5b10ac"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.DeepEqual(t, Mentions(tt.body), tt.want)
		})
	}
}
