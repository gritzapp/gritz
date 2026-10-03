package atlassianserver

import (
	"testing"

	"github.com/icholy/gritz/internal/server/eventrouter"
	"gotest.tools/v3/assert"
)

func TestSchemaRegistration(t *testing.T) {
	reg := eventrouter.NewSchemaRegistry()
	RegisterSchemas(reg)

	def, ok := reg.EventTypeFor("atlassian", EventTypeCommentCreated)
	assert.Assert(t, ok, "EventTypeFor(atlassian, %q) = _, false; want hit", EventTypeCommentCreated)

	// AttrDefs carry Jira-flavoured display copy declared inline per event type;
	// the mention placeholder is an Atlassian account id, not a GitHub username.
	assert.DeepEqual(t, def.Attrs, []eventrouter.AttrDef{
		{
			Key:         "body",
			Label:       "Comment Body",
			Placeholder: "gritz:",
			Help:        "Matched against the comment text.",
		},
		{
			Key:         "url",
			Label:       "Issue URL",
			Placeholder: "https://your-domain.atlassian.net/browse/PROJ-",
			Help:        "Matched against the commented issue URL, e.g. to scope a rule to a single project.",
		},
		{
			Key:         "mention",
			Label:       "Mention",
			Placeholder: "5b10ac8d82e05b22cc7d4ef5",
			Help:        "Atlassian account id @-mentioned in the comment. Enter the bare id (no [~accountid:…] wrapper).",
		},
		{
			Key:         "user",
			Label:       "User",
			Placeholder: "5b10ac8d82e05b22cc7d4ef5",
			Help:        "Atlassian account id of the user who commented. Enter the bare id (no [~accountid:…] wrapper).",
		},
	})

	// label_added also registers.
	_, ok = reg.EventTypeFor("atlassian", EventTypeLabelAdded)
	assert.Assert(t, ok, "EventTypeFor(atlassian, %q) = _, false; want hit", EventTypeLabelAdded)
}
