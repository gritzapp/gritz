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
	})

	// Other types also register.
	_, ok = reg.EventTypeFor("github", EventTypeIssueLabelAdded)
	assert.Assert(t, ok, "EventTypeFor(github, %q) = _, false; want hit", EventTypeIssueLabelAdded)
}

func TestSchemaPushRule(t *testing.T) {
	reg := eventrouter.NewSchemaRegistry()
	RegisterSchemas(reg)

	def, ok := reg.EventTypeFor("github", EventTypePush)
	assert.Assert(t, ok, "EventTypeFor(github, %q) = _, false; want hit", EventTypePush)
	assert.Equal(t, def.Label, "GitHub: Push")

	err := reg.ValidateAgent(&model.Agent{Triggers: []model.AgentTrigger{{
		Source: "github",
		Type:   EventTypePush,
		Conditions: []model.Condition{
			{Attr: "branch", Op: "equals", Value: "master"},
			{Attr: "path", Op: "glob", Value: "internal/store/sql/migrations/**"},
		},
	}}})
	assert.NilError(t, err)
}

func TestLinkTypeCanonical(t *testing.T) {
	reg := eventrouter.NewSchemaRegistry()
	RegisterSchemas(reg)

	tests := []struct {
		name     string
		linkType string
		raw      string
		want     string
		ok       bool
	}{
		// github.issue
		{"issue", LinkTypeIssue, "https://github.com/o/r/issues/5", "https://github.com/o/r/issues/5", true},
		{
			"issue comment fragment",
			LinkTypeIssue,
			"https://github.com/o/r/issues/5#issuecomment-9",
			"https://github.com/o/r/issues/5",
			true,
		},
		{
			"issue trailing slash",
			LinkTypeIssue,
			"https://github.com/o/r/issues/5/",
			"https://github.com/o/r/issues/5",
			true,
		},
		{
			"issue api",
			LinkTypeIssue,
			"https://api.github.com/repos/o/r/issues/5",
			"https://github.com/o/r/issues/5",
			true,
		},
		{"issue api comment", LinkTypeIssue, "https://api.github.com/repos/o/r/issues/comments/12345", "", false},
		{"issue non-numeric", LinkTypeIssue, "https://github.com/o/r/issues/abc", "", false},
		{"issue list", LinkTypeIssue, "https://github.com/o/r/issues", "", false},
		{"issue rejects pull", LinkTypeIssue, "https://github.com/o/r/pull/5", "", false},

		// github.pr
		{"pull", LinkTypePullRequest, "https://github.com/o/r/pull/5", "https://github.com/o/r/pull/5", true},
		{
			"pull files",
			LinkTypePullRequest,
			"https://github.com/o/r/pull/5/files",
			"https://github.com/o/r/pull/5",
			true,
		},
		{
			"pull commits",
			LinkTypePullRequest,
			"https://github.com/o/r/pull/5/commits/abc123",
			"https://github.com/o/r/pull/5",
			true,
		},
		{
			"pull review fragment",
			LinkTypePullRequest,
			"https://github.com/o/r/pull/5#pullrequestreview-77",
			"https://github.com/o/r/pull/5",
			true,
		},
		{
			"pull discussion fragment",
			LinkTypePullRequest,
			"https://github.com/o/r/pull/5/files#discussion_r123",
			"https://github.com/o/r/pull/5",
			true,
		},
		{
			"pull api",
			LinkTypePullRequest,
			"https://api.github.com/repos/o/r/pulls/5",
			"https://github.com/o/r/pull/5",
			true,
		},
		{"pull rejects issue", LinkTypePullRequest, "https://github.com/o/r/issues/5", "", false},

		// github.workflow_run
		{
			"run",
			LinkTypeWorkflowRun,
			"https://github.com/o/r/actions/runs/7",
			"https://github.com/o/r/actions/runs/7",
			true,
		},
		{
			"run job",
			LinkTypeWorkflowRun,
			"https://github.com/o/r/actions/runs/7/job/8",
			"https://github.com/o/r/actions/runs/7",
			true,
		},
		{
			"run attempt",
			LinkTypeWorkflowRun,
			"https://github.com/o/r/actions/runs/7/attempts/2",
			"https://github.com/o/r/actions/runs/7",
			true,
		},
		{"run rejects check run", LinkTypeWorkflowRun, "https://github.com/o/r/runs/9", "", false},

		// Not GitHub resources
		{"issue other path", LinkTypeIssue, "https://github.com/o/r/blob/main/README.md", "", false},
		{"pull unrelated host", LinkTypePullRequest, "https://example.com/o/r/pull/5", "", false},
		{"run empty", LinkTypeWorkflowRun, "", "", false},
		{"issue plain text", LinkTypeIssue, "not a url", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lt, found := reg.LinkType(tt.linkType)
			assert.Assert(t, found)

			got, ok := lt.Canonical(tt.raw)

			assert.Equal(t, got, tt.want)
			assert.Equal(t, ok, tt.ok)
		})
	}
}

func TestSchemaLinkTypes(t *testing.T) {
	reg := eventrouter.NewSchemaRegistry()
	RegisterSchemas(reg)

	got := map[string][]string{}
	for _, def := range reg.EventTypes() {
		got[def.Type] = def.LinkTypes
	}

	assert.DeepEqual(t, got, map[string][]string{
		EventTypeIssueComment:                 {LinkTypeIssue},
		EventTypePullRequestComment:           {LinkTypePullRequest},
		EventTypePullRequestReviewComment:     {LinkTypePullRequest},
		EventTypePullRequestReview:            {LinkTypePullRequest},
		EventTypeIssueOpened:                  {LinkTypeIssue},
		EventTypeIssueClosed:                  {LinkTypeIssue},
		EventTypeIssueAssigned:                {LinkTypeIssue},
		EventTypePullRequestAssigned:          {LinkTypePullRequest},
		EventTypePullRequestReviewRequested:   {LinkTypePullRequest},
		EventTypePullRequestOpened:            {LinkTypePullRequest},
		EventTypePullRequestClosed:            {LinkTypePullRequest},
		EventTypeIssueLabelAdded:              {LinkTypeIssue},
		EventTypePullRequestLabelAdded:        {LinkTypePullRequest},
		EventTypePush:                         nil,
		EventTypeCheckRunCompleted:            {LinkTypeWorkflowRun},
		EventTypePullRequestCheckRunCompleted: {LinkTypePullRequest},
	})
}
