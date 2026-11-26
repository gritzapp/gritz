package customserver

import (
	"testing"

	"github.com/gritzapp/gritz/internal/server/eventrouter"
	"gotest.tools/v3/assert"
)

func TestLinkTypeCanonical(t *testing.T) {
	reg := eventrouter.NewSchemaRegistry()
	RegisterSchemas(reg)
	lt, found := reg.LinkType(LinkTypeURL)
	assert.Assert(t, found)

	tests := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{"https", "https://example.com/foo/bar", "https://example.com/foo/bar", true},
		{"http", "http://example.com/foo", "http://example.com/foo", true},
		{
			"query and fragment kept",
			"https://dynatrace.example.com/problems?id=1#top",
			"https://dynatrace.example.com/problems?id=1#top",
			true,
		},
		{
			"github not reduced",
			"https://github.com/o/r/issues/5#issuecomment-9",
			"https://github.com/o/r/issues/5#issuecomment-9",
			true,
		},
		{"other scheme", "ftp://example.com/foo", "", false},
		{"relative", "/foo/bar", "", false},
		{"empty", "", "", false},
		{"plain text", "not a url", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lt.Canonical(tt.raw)

			assert.Equal(t, got, tt.want)
			assert.Equal(t, ok, tt.ok)
		})
	}
}
