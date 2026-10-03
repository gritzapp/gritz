package githubserver

import (
	"testing"

	"github.com/icholy/gritz/internal/server/eventrouter"
	"gotest.tools/v3/assert"
)

func TestSchemaRegistration(t *testing.T) {
	reg := eventrouter.NewSchemaRegistry()
	RegisterSchemas(reg)

	def, ok := reg.EventTypeFor("github", EventTypeIssueComment)
	assert.Assert(t, ok, "EventTypeFor(github, %q) = _, false; want hit", EventTypeIssueComment)

	// AttrDefs carry GitHub-flavoured display copy declared inline per event
	// type, so the copy speaks to this specific event (a comment's body/URL).
	assert.DeepEqual(t, def.Attrs, []eventrouter.AttrDef{
		{
			Key:         "body",
			Label:       "Comment Body",
			Placeholder: "gritz:",
			Help:        "Matched against the comment text.",
		},
		{
			Key:         "url",
			Label:       "Issue/PR URL",
			Placeholder: "https://github.com/owner/repo/",
			Help:        "Matched against the commented issue or PR URL, e.g. to scope a rule to a single repo.",
		},
		{
			Key:         "mention",
			Label:       "Mention",
			Placeholder: "octocat",
			Help:        "GitHub username @-mentioned in the comment (no leading @).",
		},
		{
			Key:         "user",
			Label:       "User",
			Placeholder: "octocat",
			Help:        "The GitHub username of the user who commented (no leading @).",
		},
	})

	// Another type also registers.
	_, ok = reg.EventTypeFor("github", EventTypeLabelAdded)
	assert.Assert(t, ok, "EventTypeFor(github, %q) = _, false; want hit", EventTypeLabelAdded)
}
