package eventrouter

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestInputEventAttr(t *testing.T) {
	e := InputEvent{
		Data: "the body",
		URL:  "https://example.com/x",
		Attrs: Attrs{
			"mention": {"alice", "bob"},
			"label":   {"urgent"},
		},
	}
	// body and url are derived views over Data and URL.
	assert.DeepEqual(t, e.Attr("body"), []string{"the body"})
	assert.DeepEqual(t, e.Attr("url"), []string{"https://example.com/x"})
	// Other keys read from Attrs.
	assert.DeepEqual(t, e.Attr("mention"), []string{"alice", "bob"})
	assert.DeepEqual(t, e.Attr("label"), []string{"urgent"})
	// An absent key yields nil.
	assert.Assert(t, e.Attr("assignee") == nil)
	// body/url are derived even when Attrs is nil.
	empty := InputEvent{Data: "d", URL: "u"}
	assert.DeepEqual(t, empty.Attr("body"), []string{"d"})
	assert.Assert(t, empty.Attr("mention") == nil)
}
