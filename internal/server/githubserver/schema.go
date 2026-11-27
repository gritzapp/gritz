package githubserver

import (
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/server/eventrouter"
	"github.com/gritzapp/gritz/internal/x/githubx"
)

// Link types githubserver registers, one per resource its events' URLs name.
const (
	LinkTypeIssue       = "github.issue"
	LinkTypePullRequest = "github.pr"
	LinkTypeWorkflowRun = "github.workflow_run"
)

// init registers githubserver's schemas on the process-wide default registry.
func init() {
	RegisterSchemas(eventrouter.DefaultSchemaRegistry)
}

// RegisterSchemas records the eventrouter schema for every event type
// githubserver emits (see toInputEvents) on reg, after the link types they
// name. Each schema declares the complete valid attr set for its type (the
// derived body/url plus its emitted dimensions). Every AttrDef is declared
// inline per event type so its display copy (label/help/placeholder) speaks to
// that specific event: a "Body" on an issue comment is the comment text, on an
// opened PR it is the description, rather than sharing a single generic
// definition. It is exported so tests
// (e.g. eventrouter's Route tests) can populate an isolated registry with the
// real producer schemas.
func RegisterSchemas(reg *eventrouter.SchemaRegistry) {
	reg.MustRegisterLinkType(model.LinkTypeDef{
		Name:      LinkTypeIssue,
		Source:    "github",
		Label:     "GitHub: Issue",
		Canonical: githubx.CanonicalIssue,
	})
	reg.MustRegisterLinkType(model.LinkTypeDef{
		Name:      LinkTypePullRequest,
		Source:    "github",
		Label:     "GitHub: Pull Request",
		Canonical: githubx.CanonicalPullRequest,
	})
	reg.MustRegisterLinkType(model.LinkTypeDef{
		Name:      LinkTypeWorkflowRun,
		Source:    "github",
		Label:     "GitHub: Workflow Run",
		Canonical: githubx.CanonicalWorkflowRun,
	})
	reg.MustRegister(model.EventTypeDef{
		Source:    "github",
		Type:      EventTypeIssueComment,
		Label:     "GitHub: Issue Comment",
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
				Placeholder: "https://github.com/owner/repo/issues/",
				Help:        "Matched against the comment's own URL (the issue URL followed by #issuecomment-…), e.g. to scope a rule to a single repo.",
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
				Help:        "The GitHub username of the user who commented on the issue (no leading @).",
			},
		},
	})
	reg.MustRegister(model.EventTypeDef{
		Source:    "github",
		Type:      EventTypePullRequestComment,
		Label:     "GitHub: PR Comment",
		LinkTypes: []string{LinkTypePullRequest},
		Attrs: []model.AttrDef{
			{
				Key:         "body",
				Label:       "Comment Body",
				Placeholder: "gritz:",
				Help:        "Matched against the text of the comment on the pull request's conversation.",
			},
			{
				Key:         "url",
				Label:       "Comment URL",
				Placeholder: "https://github.com/owner/repo/pull/",
				Help:        "Matched against the comment's own URL (the PR URL followed by #issuecomment-…), e.g. to scope a rule to a single repo.",
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
				Help:        "The GitHub username of the user who commented on the pull request (no leading @).",
			},
		},
	})
	reg.MustRegister(model.EventTypeDef{
		Source:    "github",
		Type:      EventTypePullRequestReviewComment,
		Label:     "GitHub: PR Review Comment",
		LinkTypes: []string{LinkTypePullRequest},
		Attrs: []model.AttrDef{
			{
				Key:         "body",
				Label:       "Review Comment Body",
				Placeholder: "gritz:",
				Help:        "Matched against the inline review comment text.",
			},
			{
				Key:         "url",
				Label:       "Review Comment URL",
				Placeholder: "https://github.com/owner/repo/",
				Help:        "Matched against the review comment's own URL (the PR URL followed by #discussion_r…), e.g. to scope a rule to a single repo.",
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
	reg.MustRegister(model.EventTypeDef{
		Source:    "github",
		Type:      EventTypePullRequestReview,
		Label:     "GitHub: PR Review",
		LinkTypes: []string{LinkTypePullRequest},
		Attrs: []model.AttrDef{
			{
				Key:         "body",
				Label:       "Review Body",
				Placeholder: "gritz:",
				Help:        "Matched against the review summary text.",
			},
			{
				Key:         "url",
				Label:       "Review URL",
				Placeholder: "https://github.com/owner/repo/",
				Help:        "Matched against the review's own URL (the PR URL followed by #pullrequestreview-…), e.g. to scope a rule to a single repo.",
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
	reg.MustRegister(model.EventTypeDef{
		Source:    "github",
		Type:      EventTypeIssueAssigned,
		Label:     "GitHub: Issue Assigned",
		LinkTypes: []string{LinkTypeIssue},
		Attrs: []model.AttrDef{
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
	reg.MustRegister(model.EventTypeDef{
		Source:    "github",
		Type:      EventTypePullRequestAssigned,
		Label:     "GitHub: PR Assigned",
		LinkTypes: []string{LinkTypePullRequest},
		Attrs: []model.AttrDef{
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
	reg.MustRegister(model.EventTypeDef{
		Source:    "github",
		Type:      EventTypePullRequestOpened,
		Label:     "GitHub: PR Opened",
		LinkTypes: []string{LinkTypePullRequest},
		Attrs: []model.AttrDef{
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
	reg.MustRegister(model.EventTypeDef{
		Source:    "github",
		Type:      EventTypePullRequestClosed,
		Label:     "GitHub: PR Closed",
		LinkTypes: []string{LinkTypePullRequest},
		Attrs: []model.AttrDef{
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
				Label:       "Merged or Closed",
				Placeholder: "merged",
				Help:        `"merged" if the PR was merged, "closed" if it was closed without merging.`,
			},
			{
				Key:         "user",
				Label:       "User",
				Placeholder: "octocat",
				Help:        "The GitHub username of the user who closed the pull request (no leading @).",
			},
		},
	})
	reg.MustRegister(model.EventTypeDef{
		Source:    "github",
		Type:      EventTypeIssueLabelAdded,
		Label:     "GitHub: Issue Label Added",
		LinkTypes: []string{LinkTypeIssue},
		Attrs: []model.AttrDef{
			{
				Key:         "body",
				Label:       "Issue Body",
				Placeholder: "gritz:",
				Help:        "Matched against the description of the labeled issue.",
			},
			{
				Key:         "url",
				Label:       "Issue URL",
				Placeholder: "https://github.com/owner/repo/issues/123",
				Help:        "Matched against the labeled issue URL, e.g. to scope a rule to a single repo.",
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
				Placeholder: "octocat",
				Help:        "The GitHub username of the user who added the label to the issue (no leading @).",
			},
		},
	})
	reg.MustRegister(model.EventTypeDef{
		Source:    "github",
		Type:      EventTypePullRequestLabelAdded,
		Label:     "GitHub: PR Label Added",
		LinkTypes: []string{LinkTypePullRequest},
		Attrs: []model.AttrDef{
			{
				Key:         "body",
				Label:       "PR Body",
				Placeholder: "gritz:",
				Help:        "Matched against the description of the labeled pull request.",
			},
			{
				Key:         "url",
				Label:       "PR URL",
				Placeholder: "https://github.com/owner/repo/pull/123",
				Help:        "Matched against the labeled PR URL, e.g. to scope a rule to a single repo.",
			},
			{
				Key:         "label",
				Label:       "Label",
				Placeholder: "gritz",
				Help:        "The label added to the pull request.",
			},
			{
				Key:         "user",
				Label:       "User",
				Placeholder: "octocat",
				Help:        "The GitHub username of the user who added the label to the pull request (no leading @).",
			},
		},
	})
	reg.MustRegister(model.EventTypeDef{
		Source: "github",
		Type:   EventTypePush,
		Label:  "GitHub: Push",
		Attrs: []model.AttrDef{
			{
				Key:         "body",
				Label:       "Head Commit Message",
				Placeholder: "gritz:",
				Help:        "Matched against the message of the push's newest commit.",
			},
			{
				Key:         "url",
				Label:       "Head Commit URL",
				Placeholder: "https://github.com/owner/repo/commit/",
				Help:        "Matched against the URL of the push's newest commit, e.g. to scope a rule to a single repo.",
			},
			{
				Key:         "repo",
				Label:       "Repository",
				Placeholder: "owner/repo",
				Help:        "The repository pushed to, as owner/name.",
			},
			{
				Key:         "branch",
				Label:       "Branch",
				Placeholder: "master",
				Help:        "The branch pushed to, without refs/heads/.",
			},
			{
				Key:         "path",
				Label:       "Changed Path",
				Placeholder: "internal/store/**",
				Help:        "A file added, modified or removed by the push. A condition holds when any changed file satisfies it.",
			},
			{
				Key:         "user",
				Label:       "User",
				Placeholder: "octocat",
				Help:        "The GitHub username of the user who pushed (no leading @).",
			},
		},
	})
	reg.MustRegister(model.EventTypeDef{
		Source:    "github",
		Type:      EventTypeCheckRunCompleted,
		Label:     "GitHub: Check Run Completed",
		LinkTypes: []string{LinkTypeWorkflowRun},
		Attrs: []model.AttrDef{
			{
				Key:         "body",
				Label:       "Check Output",
				Placeholder: "gritz:",
				Help:        "Matched against the check run's output title and summary, which are often empty for GitHub Actions.",
			},
			{
				Key:         "url",
				Label:       "Check Run URL",
				Placeholder: "https://github.com/owner/repo/actions/runs/",
				Help:        "Matched against the check run's URL (for GitHub Actions, the job), e.g. to scope a rule to a single repo.",
			},
			{
				Key:         "repo",
				Label:       "Repository",
				Placeholder: "owner/repo",
				Help:        "The repository the check ran in, as owner/name.",
			},
			{
				Key:         "branch",
				Label:       "Branch",
				Placeholder: "master",
				Help:        "The branch the check ran on, without refs/heads/. Absent for commits pushed to a fork, so a branch condition never matches those.",
			},
			{
				Key:         "conclusion",
				Label:       "Conclusion",
				Placeholder: "failure",
				Help:        "How the check finished: success, failure, cancelled, timed_out, action_required, neutral, skipped, stale or startup_failure. Match several with glob, e.g. {failure,timed_out,startup_failure}.",
			},
			{
				Key:         "check",
				Label:       "Check Name",
				Placeholder: "build*",
				Help:        "The check's name; for GitHub Actions, the job name. Matrix jobs carry their axes, e.g. build (linux, 1.26), so prefix or glob is usually the right op.",
			},
			{
				Key:         "app",
				Label:       "App",
				Placeholder: "github-actions",
				Help:        "The slug of the GitHub App that reported the check, e.g. github-actions.",
			},
			{
				Key:         "user",
				Label:       "User",
				Placeholder: "octocat",
				Help:        "The GitHub username GitHub attributes the run to (no leading @): usually the pusher, but a bot for bot pushes, so a create rule usually needs to be public.",
			},
		},
	})
	reg.MustRegister(model.EventTypeDef{
		Source:    "github",
		Type:      EventTypePullRequestCheckRunCompleted,
		Label:     "GitHub: PR Check Run Completed",
		LinkTypes: []string{LinkTypePullRequest},
		Attrs: []model.AttrDef{
			{
				Key:         "body",
				Label:       "Check Output",
				Placeholder: "gritz:",
				Help:        "Matched against the check run's output title and summary, which are often empty for GitHub Actions.",
			},
			{
				Key:         "url",
				Label:       "PR URL",
				Placeholder: "https://github.com/owner/repo/pull/",
				Help:        "Matched against the URL of the pull request the check ran for, e.g. to scope a rule to a single repo.",
			},
			{
				Key:         "repo",
				Label:       "Repository",
				Placeholder: "owner/repo",
				Help:        "The repository the check ran in, as owner/name.",
			},
			{
				Key:         "branch",
				Label:       "Branch",
				Placeholder: "master",
				Help:        "The PR's head branch the check ran on, without refs/heads/.",
			},
			{
				Key:         "conclusion",
				Label:       "Conclusion",
				Placeholder: "failure",
				Help:        "How the check finished: success, failure, cancelled, timed_out, action_required, neutral, skipped, stale or startup_failure. Match several with glob, e.g. {failure,timed_out,startup_failure}.",
			},
			{
				Key:         "check",
				Label:       "Check Name",
				Placeholder: "build*",
				Help:        "The check's name; for GitHub Actions, the job name. Matrix jobs carry their axes, e.g. build (linux, 1.26), so prefix or glob is usually the right op.",
			},
			{
				Key:         "app",
				Label:       "App",
				Placeholder: "github-actions",
				Help:        "The slug of the GitHub App that reported the check, e.g. github-actions.",
			},
			{
				Key:         "user",
				Label:       "User",
				Placeholder: "octocat",
				Help:        "The GitHub username GitHub attributes the run to (no leading @): usually the pusher, but a bot for bot pushes, so a rule usually needs to be public. The event only reaches tasks subscribed to the PR.",
			},
		},
	})
}
