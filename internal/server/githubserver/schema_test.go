package githubserver

import (
	"testing"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/server/eventrouter"
	"gotest.tools/v3/assert"
)

func TestSchemaRegistration(t *testing.T) {
	reg := eventrouter.NewSchemaRegistry()
	RegisterSchemas(reg)

	def, ok := reg.EventTypeFor("github", EventTypeIssueComment)
	assert.Assert(t, ok, "EventTypeFor(github, %q) = _, false; want hit", EventTypeIssueComment)

	// AttrDefs carry GitHub-flavoured display copy declared inline per event
	// type, so the copy speaks to this specific event (a comment's body/URL).
	assert.DeepEqual(t, def.Attrs, []model.AttrDef{
		{
			Key:         "body",
			Label:       "Comment Body",
			Placeholder: "gritz:",
			Help:        "Matched against the comment text.",
		},
		{
			Key:         "url",
			Label:       "Comment URL",
			Placeholder: "https://github.com/owner/repo/",
			Help:        "Matched against the comment's own URL (the issue or PR URL followed by #issuecomment-…), e.g. to scope a rule to a single repo.",
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

func TestSchemaPushRule(t *testing.T) {
	reg := eventrouter.NewSchemaRegistry()
	RegisterSchemas(reg)

	def, ok := reg.EventTypeFor("github", EventTypePush)
	assert.Assert(t, ok, "EventTypeFor(github, %q) = _, false; want hit", EventTypePush)
	assert.Equal(t, def.Label, "GitHub: Push")

	err := reg.Validate(model.RoutingRule{
		Source: "github",
		Type:   EventTypePush,
		Conditions: []model.Condition{
			{Attr: "branch", Op: "equals", Value: "master"},
			{Attr: "path", Op: "glob", Value: "internal/store/sql/migrations/**"},
		},
		Create: &model.CreateTaskAction{Workspace: "default", Runner: "r"},
	})
	assert.NilError(t, err)
}
