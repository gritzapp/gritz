package atlassianserver

import (
	"github.com/icholy/gritz/internal/server/eventrouter"
)

// init registers atlassianserver's schemas on the process-wide default registry.
func init() {
	RegisterSchemas(eventrouter.DefaultSchemaRegistry)
}

// RegisterSchemas records the eventrouter schema for every event type
// atlassianserver emits (see toInputEvent) on reg. Each schema declares the
// complete valid attr set for its type (the derived body/url plus its emitted
// dimensions). Every AttrDef is declared inline per event type so its display
// copy (label/help/placeholder) speaks to that specific event rather than
// sharing a single generic definition. It is exported so tests (e.g.
// eventrouter's Route tests) can populate an isolated registry with the real
// producer schemas.
func RegisterSchemas(reg *eventrouter.SchemaRegistry) {
	reg.MustRegister(eventrouter.EventTypeDef{
		Source: "atlassian",
		Type:   EventTypeCommentCreated,
		Label:  "Jira: Issue Comment",
		Attrs: []eventrouter.AttrDef{
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
		},
	})
	reg.MustRegister(eventrouter.EventTypeDef{
		Source: "atlassian",
		Type:   EventTypeLabelAdded,
		Label:  "Jira: Label Added",
		Attrs: []eventrouter.AttrDef{
			{
				Key:         "body",
				Label:       "Issue Description",
				Placeholder: "gritz:",
				Help:        "Matched against the description of the labeled issue.",
			},
			{
				Key:         "url",
				Label:       "Issue URL",
				Placeholder: "https://your-domain.atlassian.net/browse/PROJ-",
				Help:        "Matched against the labeled issue URL, e.g. to scope a rule to a single project.",
			},
			{
				Key:         "label",
				Label:       "Label",
				Placeholder: "gritz",
				Help:        "The label added to the issue.",
			},
			{
				Key:         "user",
				Label:       "User",
				Placeholder: "5b10ac8d82e05b22cc7d4ef5",
				Help:        "Atlassian account id of the user who added the label(s). Enter the bare id (no [~accountid:…] wrapper).",
			},
		},
	})
}
