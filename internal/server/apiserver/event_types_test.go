package apiserver

import (
	"testing"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"

	// Blank-imported so their init registers the eventrouter schemas the
	// handler serves. atlassianserver is already imported transitively by the
	// apiserver package; githubserver is only referenced via an interface, so it
	// must be pulled in explicitly here for its github/* types to be registered.
	_ "github.com/gritzapp/gritz/internal/server/githubserver"
)

func TestGetEventTypes(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)

	// Act
	resp, err := srv.GetEventTypes(ctx, &gritzv1.GetEventTypesRequest{})

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

	// The atlassian producer registers too (apiserver imports it), confirming the
	// handler exposes the whole global registry, not just one source.
	_, ok = byKey["atlassian:comment_created"]
	assert.Assert(t, ok, "expected atlassian/comment_created to be registered")
}
