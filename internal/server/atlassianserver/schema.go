package atlassianserver

import (
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/server/eventrouter"
	"github.com/gritzapp/gritz/internal/x/atlassian"
)

// LinkTypeIssue is the link type of a Jira issue, the resource every
// atlassianserver event is about.
const LinkTypeIssue = "jira.issue"

// init registers atlassianserver's schemas on the process-wide default registry.
func init() {
	RegisterSchemas(eventrouter.DefaultSchemaRegistry)
}

// RegisterSchemas records the eventrouter schema for every event type
// atlassianserver emits (see toInputEvents) on reg, after the link type they
// name. Each schema declares the complete valid attr set for its type (the
// derived body/url plus its emitted dimensions). Every AttrDef is declared
// inline per event type so its display copy (label/help/placeholder) speaks to
// that specific event rather than sharing a single generic definition. It is exported so tests (e.g.
// eventrouter's Route tests) can populate an isolated registry with the real
// producer schemas.
func RegisterSchemas(reg *eventrouter.SchemaRegistry) {
	reg.MustRegisterLinkType(model.LinkTypeDef{
		Name:      LinkTypeIssue,
		Source:    "atlassian",
		Label:     "Jira: Issue",
		Canonical: atlassian.CanonicalIssue,
	})
	reg.MustRegister(model.EventTypeDef{
		Source:    "atlassian",
		Type:      EventTypeCommentCreated,
		Label:     "Jira: Issue Comment",
		LinkTypes: []string{LinkTypeIssue},
		Attrs: []model.AttrDef{
			{
				Key:         "body",
				Label:       "Comment Body",
				Placeholder: "gritz:",
				Help:        "Matched against the comment text.",
			},
			{
				Key:         "url",
				Label:       "Comment URL",
				Placeholder: "https://your-domain.atlassian.net/browse/PROJ-",
				Help:        "Matched against the comment's own URL (the issue URL followed by ?focusedCommentId=…), e.g. to scope a rule to a single project.",
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
	reg.MustRegister(model.EventTypeDef{
		Source:    "atlassian",
		Type:      EventTypeIssueCreated,
		Label:     "Jira: Issue Created",
		LinkTypes: []string{LinkTypeIssue},
		Attrs: []model.AttrDef{
			{
				Key:         "body",
				Label:       "Issue Description",
				Placeholder: "gritz:",
				Help:        "Matched against the description of the new issue.",
			},
			{
				Key:         "url",
				Label:       "Issue URL",
				Placeholder: "https://your-domain.atlassian.net/browse/PROJ-",
				Help:        "Matched against the new issue URL, e.g. to scope a rule to a single project.",
			},
			{
				Key:         "label",
				Label:       "Label",
				Placeholder: "gritz",
				Help:        "A label the issue was created with.",
			},
			{
				Key:         "assignee",
				Label:       "Assignee",
				Placeholder: "5b10ac8d82e05b22cc7d4ef5",
				Help:        "Atlassian account id of the user the issue was created assigned to. Enter the bare id (no [~accountid:…] wrapper).",
			},
			{
				Key:         "user",
				Label:       "User",
				Placeholder: "5b10ac8d82e05b22cc7d4ef5",
				Help:        "Atlassian account id of the user who created the issue. Enter the bare id (no [~accountid:…] wrapper).",
			},
		},
	})
	reg.MustRegister(model.EventTypeDef{
		Source:    "atlassian",
		Type:      EventTypeLabelAdded,
		Label:     "Jira: Label Added",
		LinkTypes: []string{LinkTypeIssue},
		Attrs: []model.AttrDef{
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
	reg.MustRegister(model.EventTypeDef{
		Source:    "atlassian",
		Type:      EventTypeIssueAssigned,
		Label:     "Jira: Issue Assigned",
		LinkTypes: []string{LinkTypeIssue},
		Attrs: []model.AttrDef{
			{
				Key:         "body",
				Label:       "Issue Description",
				Placeholder: "gritz:",
				Help:        "Matched against the description of the assigned issue.",
			},
			{
				Key:         "url",
				Label:       "Issue URL",
				Placeholder: "https://your-domain.atlassian.net/browse/PROJ-",
				Help:        "Matched against the assigned issue URL, e.g. to scope a rule to a single project.",
			},
			{
				Key:         "assignee",
				Label:       "Assignee",
				Placeholder: "5b10ac8d82e05b22cc7d4ef5",
				Help:        "Atlassian account id of the user newly assigned to the issue. Enter the bare id (no [~accountid:…] wrapper).",
			},
			{
				Key:         "user",
				Label:       "User",
				Placeholder: "5b10ac8d82e05b22cc7d4ef5",
				Help:        "Atlassian account id of the user who assigned the issue. Enter the bare id (no [~accountid:…] wrapper).",
			},
		},
	})
	reg.MustRegister(model.EventTypeDef{
		Source:    "atlassian",
		Type:      EventTypeIssueStatusChanged,
		Label:     "Jira: Issue Status Changed",
		LinkTypes: []string{LinkTypeIssue},
		Attrs: []model.AttrDef{
			{
				Key:         "body",
				Label:       "Issue Description",
				Placeholder: "gritz:",
				Help:        "Matched against the description of the issue whose status changed.",
			},
			{
				Key:         "url",
				Label:       "Issue URL",
				Placeholder: "https://your-domain.atlassian.net/browse/PROJ-",
				Help:        "Matched against the issue URL, e.g. to scope a rule to a single project.",
			},
			{
				Key:         "status",
				Label:       "Status",
				Placeholder: "In Progress",
				Help:        "Name of the status the issue moved to.",
			},
			{
				Key:         "from",
				Label:       "Previous Status",
				Placeholder: "To Do",
				Help:        "Name of the status the issue moved from.",
			},
			{
				Key:         "user",
				Label:       "User",
				Placeholder: "5b10ac8d82e05b22cc7d4ef5",
				Help:        "Atlassian account id of the user who changed the status. Enter the bare id (no [~accountid:…] wrapper).",
			},
		},
	})
}
