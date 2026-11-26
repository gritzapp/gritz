package store_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
)

func TestCreateAndListLinks(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task := teststore.CreateTask(t, s, org, nil)
	link := &model.Link{
		TaskID:    task.ID,
		Relevance: "trigger",
		URL:       "https://github.com/o/r/pull/5",
		Title:     "PR",
		Kind:      "owned-pr",
		Claim:     "pr-owner",
		CreatedAt: time.Now(),
	}
	err := s.CreateLink(t.Context(), nil, link)
	assert.NilError(t, err)

	// Act
	links, err := s.ListLinksByTask(t.Context(), nil, task.ID, org.OrgID)

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, len(links), 1)
	assert.DeepEqual(t, links[0], link, cmpopts.IgnoreFields(model.Link{}, "CreatedAt"))
}

func TestFindSubscribedLinks(t *testing.T) {
	t.Parallel()
	// Arrange - on one URL: a subscribed link, a plain reference link, a
	// subscribed link of an archived task and one in another org.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	other := teststore.CreateOrg(t, s, nil)
	const url = "https://github.com/o/r/pull/5"
	agent := teststore.CreateAgent(t, s, org, nil)
	task := teststore.CreateTask(t, s, org, &teststore.TaskOptions{
		AgentID: agent.ID,
		Links: []teststore.LinkOptions{
			{URL: url, Kind: "owned-pr", Claim: "pr-owner"},
			{URL: url},
			{URL: "https://github.com/o/r/pull/6", Kind: "owned-pr"},
		},
	})
	archived := teststore.CreateTask(t, s, org, &teststore.TaskOptions{
		Links: []teststore.LinkOptions{{URL: url, Kind: "owned-pr"}},
	})
	archived.Archived = true
	assert.NilError(t, s.UpdateTask(t.Context(), nil, archived))
	teststore.CreateTask(t, s, other, &teststore.TaskOptions{
		Links: []teststore.LinkOptions{{URL: url, Kind: "owned-pr"}},
	})

	// Act
	links, err := s.FindSubscribedLinks(t.Context(), nil, url, []int64{org.OrgID})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, links, []store.SubscribedLink{{
		Link: &model.Link{
			TaskID: task.ID,
			URL:    url,
			Title:  "test link",
			Kind:   "owned-pr",
			Claim:  "pr-owner",
		},
		OrgID:   org.OrgID,
		AgentID: agent.ID,
	}}, cmpopts.IgnoreFields(model.Link{}, "ID", "CreatedAt"))
}

func TestFindClaimHolders(t *testing.T) {
	t.Parallel()
	// Arrange - holders of different claims on one URL, the same claim on
	// another URL, an archived holder, and a holder in another org.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	other := teststore.CreateOrg(t, s, nil)
	const url = "https://github.com/o/r/pull/5"
	owner := teststore.CreateTask(t, s, org, &teststore.TaskOptions{
		Links: []teststore.LinkOptions{
			{URL: url, Kind: "owned-pr", Claim: "pr-owner"},
			{URL: url, Kind: "reviewed-pr", Claim: "pr-reviewer"},
		},
	})
	reviewer := teststore.CreateTask(t, s, org, &teststore.TaskOptions{
		Links: []teststore.LinkOptions{{URL: url, Kind: "reviewed-pr", Claim: "pr-reviewer"}},
	})
	teststore.CreateTask(t, s, org, &teststore.TaskOptions{
		Links: []teststore.LinkOptions{{URL: url, Kind: "github.pr"}},
	})
	teststore.CreateTask(t, s, org, &teststore.TaskOptions{
		Links: []teststore.LinkOptions{{URL: "https://github.com/o/r/pull/6", Kind: "owned-pr", Claim: "pr-owner"}},
	})
	archived := teststore.CreateTask(t, s, org, &teststore.TaskOptions{
		Links: []teststore.LinkOptions{{URL: url, Kind: "owned-pr", Claim: "pr-owner"}},
	})
	archived.Archived = true
	assert.NilError(t, s.UpdateTask(t.Context(), nil, archived))
	teststore.CreateTask(t, s, other, &teststore.TaskOptions{
		Links: []teststore.LinkOptions{{URL: url, Kind: "owned-pr", Claim: "pr-owner"}},
	})

	// Act
	holders, err := s.FindClaimHolders(t.Context(), nil, org.OrgID, url, []string{"pr-owner", "pr-reviewer"})

	// Assert - each holder once, however many of the claims it holds.
	assert.NilError(t, err)
	assert.DeepEqual(t, holders, []int64{owner.ID, reviewer.ID})
}
