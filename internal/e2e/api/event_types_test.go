package api_test

import (
	"testing"

	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
)

func TestGetEventTypes(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)

	// Act
	resp, err := h.Org.Client.GetEventTypes(t.Context(), &gritzv1.GetEventTypesRequest{})

	// Assert
	assert.NilError(t, err)
	byKey := map[string]*gritzv1.EventTypeDef{}
	for _, et := range resp.EventTypes {
		byKey[et.Source+":"+et.Type] = et
	}

	// The handler exposes the registry: the type comes through with its label and
	// its attr keys in order. The exact per-source display copy is owned by the
	// producer schema tests (githubserver/atlassianserver); here we only prove the
	// RPC surfaces it.
	issueComment, ok := byKey["github:issue_comment"]
	assert.Assert(t, ok, "expected github/issue_comment to be registered")
	assert.Equal(t, issueComment.Label, "GitHub: Issue/PR Comment")
	var keys []string
	for _, a := range issueComment.Attrs {
		keys = append(keys, a.Key)
	}
	assert.DeepEqual(t, keys, []string{"body", "url", "mention", "user"})

	// One type asserted whole to prove the AttrDef -> proto conversion carries
	// every display field (label/placeholder/help), not just the key.
	labelAdded, ok := byKey["github:label_added"]
	assert.Assert(t, ok, "expected github/label_added to be registered")
	assert.DeepEqual(t, labelAdded.Attrs, []*gritzv1.AttrDef{
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
	}, protocmp.Transform())

	// The atlassian producer registers too, confirming the handler exposes the
	// whole global registry, not just one source.
	_, ok = byKey["atlassian:comment_created"]
	assert.Assert(t, ok, "expected atlassian/comment_created to be registered")
}

func TestGetEventTypes_IncludesOrgCustomTypes(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	problem := &gritzv1.EventTypeDef{
		Source: "custom",
		Type:   "problem",
		Label:  "Monitoring: Problem",
		Attrs: []*gritzv1.AttrDef{
			{Key: "body", Label: "Description"},
			{Key: "url", Label: "Problem URL"},
			{Key: "severity", Label: "Severity", Help: "e.g. critical", Placeholder: "critical"},
		},
	}
	_, err := h.Org.Client.SetCustomEventTypes(t.Context(), &gritzv1.SetCustomEventTypesRequest{
		EventTypes: []*gritzv1.EventTypeDef{problem},
	})
	assert.NilError(t, err)

	// Act
	resp, err := h.Org.Client.GetEventTypes(t.Context(), &gritzv1.GetEventTypesRequest{})

	// Assert: the custom type follows the built-in ones.
	assert.NilError(t, err)
	assert.Assert(t, len(resp.EventTypes) > 1)
	assert.DeepEqual(t, resp.EventTypes[len(resp.EventTypes)-1], problem, protocmp.Transform())

	// Act
	otherResp, err := other.Client.GetEventTypes(t.Context(), &gritzv1.GetEventTypesRequest{})

	// Assert: another org doesn't see it.
	assert.NilError(t, err)
	assert.Equal(t, len(otherResp.EventTypes), len(resp.EventTypes)-1)
	for _, et := range otherResp.EventTypes {
		assert.Assert(t, et.Source != "custom", "unexpected custom type %s", et.Type)
	}
}
