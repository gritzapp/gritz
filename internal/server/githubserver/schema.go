package githubserver

import (
	"github.com/icholy/gritz/internal/server/eventrouter"
)

// init registers githubserver's schemas on the process-wide default registry.
func init() {
	RegisterSchemas(eventrouter.DefaultSchemaRegistry)
}

// RegisterSchemas records the eventrouter schema for every event type
// githubserver emits (see toInputEvent) on reg. Each schema declares the
// complete valid attr set for its type (the derived body/url plus its emitted
// dimensions). Every AttrDef is declared inline per event type so its display
// copy (label/help/placeholder) speaks to that specific event: a "Body" on an
// issue comment is the comment text, on an opened PR it is the description,
// rather than sharing a single generic definition. It is exported so tests
// (e.g. eventrouter's Route tests) can populate an isolated registry with the
// real producer schemas.
func RegisterSchemas(reg *eventrouter.SchemaRegistry) {
	reg.MustRegister(eventrouter.EventTypeDef{
		Source: "github",
		Type:   EventTypeIssueComment,
		Label:  "GitHub: Issue/PR Comment",
		Attrs: []eventrouter.AttrDef{
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
		},
	})
	reg.MustRegister(eventrouter.EventTypeDef{
		Source: "github",
		Type:   EventTypePullRequestReviewComment,
		Label:  "GitHub: PR Review Comment",
		Attrs: []eventrouter.AttrDef{
			{
				Key:         "body",
				Label:       "Review Comment Body",
				Placeholder: "gritz:",
				Help:        "Matched against the inline review comment text.",
			},
			{
				Key:         "url",
				Label:       "PR URL",
				Placeholder: "https://github.com/owner/repo/pull/123",
				Help:        "Matched against the reviewed PR URL, e.g. to scope a rule to a single repo.",
			},
			{
				Key:         "mention",
				Label:       "Mention",
				Placeholder: "octocat",
				Help:        "GitHub username @-mentioned in the review comment (no leading @).",
			},
			{
				Key:         "user",
				Label:       "User",
				Placeholder: "octocat",
				Help:        "The GitHub username of the user who left the review comment (no leading @).",
			},
		},
	})
	reg.MustRegister(eventrouter.EventTypeDef{
		Source: "github",
		Type:   EventTypePullRequestReview,
		Label:  "GitHub: PR Review",
		Attrs: []eventrouter.AttrDef{
			{
				Key:         "body",
				Label:       "Review Body",
				Placeholder: "gritz:",
				Help:        "Matched against the review summary text.",
			},
			{
				Key:         "url",
				Label:       "PR URL",
				Placeholder: "https://github.com/owner/repo/pull/123",
				Help:        "Matched against the reviewed PR URL, e.g. to scope a rule to a single repo.",
			},
			{
				Key:         "mention",
				Label:       "Mention",
				Placeholder: "octocat",
				Help:        "GitHub username @-mentioned in the review (no leading @).",
			},
			{
				Key:         "user",
				Label:       "User",
				Placeholder: "octocat",
				Help:        "The GitHub username of the user who submitted the review (no leading @).",
			},
		},
	})
	reg.MustRegister(eventrouter.EventTypeDef{
		Source: "github",
		Type:   EventTypeIssueAssigned,
		Label:  "GitHub: Issue Assigned",
		Attrs: []eventrouter.AttrDef{
			{
				Key:         "body",
				Label:       "Issue Body",
				Placeholder: "gritz:",
				Help:        "Matched against the issue description text.",
			},
			{
				Key:         "url",
				Label:       "Issue URL",
				Placeholder: "https://github.com/owner/repo/issues/123",
				Help:        "Matched against the assigned issue URL, e.g. to scope a rule to a single repo.",
			},
			{
				Key:         "assignee",
				Label:       "Assignee",
				Placeholder: "icholy-bot",
				Help:        "The GitHub username newly assigned to the issue (no leading @).",
			},
			{
				Key:         "user",
				Label:       "User",
				Placeholder: "octocat",
				Help:        "The GitHub username of the user who assigned the issue (no leading @).",
			},
		},
	})
	reg.MustRegister(eventrouter.EventTypeDef{
		Source: "github",
		Type:   EventTypePullRequestAssigned,
		Label:  "GitHub: PR Assigned",
		Attrs: []eventrouter.AttrDef{
			{
				Key:         "body",
				Label:       "PR Body",
				Placeholder: "gritz:",
				Help:        "Matched against the pull request description text.",
			},
			{
				Key:         "url",
				Label:       "PR URL",
				Placeholder: "https://github.com/owner/repo/pull/123",
				Help:        "Matched against the assigned PR URL, e.g. to scope a rule to a single repo.",
			},
			{
				Key:         "assignee",
				Label:       "Assignee",
				Placeholder: "icholy-bot",
				Help:        "The GitHub username newly assigned to the pull request (no leading @).",
			},
			{
				Key:         "user",
				Label:       "User",
				Placeholder: "octocat",
				Help:        "The GitHub username of the user who assigned the pull request (no leading @).",
			},
		},
	})
	reg.MustRegister(eventrouter.EventTypeDef{
		Source: "github",
		Type:   EventTypePullRequestOpened,
		Label:  "GitHub: PR Opened",
		Attrs: []eventrouter.AttrDef{
			{
				Key:         "body",
				Label:       "PR Description",
				Placeholder: "gritz:",
				Help:        "Matched against the description of the newly opened pull request.",
			},
			{
				Key:         "url",
				Label:       "PR URL",
				Placeholder: "https://github.com/owner/repo/pull/123",
				Help:        "Matched against the opened PR URL, e.g. to scope a rule to a single repo.",
			},
			{
				Key:         "user",
				Label:       "User",
				Placeholder: "octocat",
				Help:        "The GitHub username of the user who opened the pull request (no leading @).",
			},
		},
	})
	reg.MustRegister(eventrouter.EventTypeDef{
		Source: "github",
		Type:   EventTypePullRequestClosed,
		Label:  "GitHub: PR Closed",
		Attrs: []eventrouter.AttrDef{
			{
				Key:         "body",
				Label:       "PR Description",
				Placeholder: "gritz:",
				Help:        "Matched against the description of the closed pull request.",
			},
			{
				Key:         "url",
				Label:       "PR URL",
				Placeholder: "https://github.com/owner/repo/pull/123",
				Help:        "Matched against the closed PR URL, e.g. to scope a rule to a single repo.",
			},
			{
				Key:         "state",
				Label:       "State",
				Placeholder: "merged",
				Help:        `Whether the PR was "merged" or just "closed".`,
			},
			{
				Key:         "user",
				Label:       "User",
				Placeholder: "octocat",
				Help:        "The GitHub username of the user who closed the pull request (no leading @).",
			},
		},
	})
	reg.MustRegister(eventrouter.EventTypeDef{
		Source: "github",
		Type:   EventTypeLabelAdded,
		Label:  "GitHub: Label Added",
		Attrs: []eventrouter.AttrDef{
			{
				Key:         "body",
				Label:       "Issue/PR Body",
				Placeholder: "gritz:",
				Help:        "Matched against the description of the labeled issue or PR.",
			},
			{
				Key:         "url",
				Label:       "Issue/PR URL",
				Placeholder: "https://github.com/owner/repo/",
				Help:        "Matched against the labeled issue or PR URL, e.g. to scope a rule to a single repo.",
			},
			{
				Key:         "label",
				Label:       "Label",
				Placeholder: "gritz",
				Help:        "The label added to the issue or PR.",
			},
			{
				Key:         "user",
				Label:       "User",
				Placeholder: "octocat",
				Help:        "The GitHub username of the user who added the label (no leading @).",
			},
		},
	})
}
