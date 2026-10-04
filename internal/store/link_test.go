package store_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
)

func TestCreateAndListLinksRoutingKey(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task := teststore.CreateTask(t, s, org, nil)
	link := &model.Link{
		TaskID:     task.ID,
		Relevance:  "trigger",
		URL:        "https://github.com/o/r/pull/5#issuecomment-9",
		RoutingKey: "https://github.com/o/r/pull/5",
		Title:      "PR",
		Subscribe:  true,
		CreatedAt:  time.Now(),
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

func TestFindSubscribedLinksForOrgsMatchesRoutingKey(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task := teststore.CreateTask(t, s, org, nil)
	link := &model.Link{
		TaskID:     task.ID,
		Relevance:  "trigger",
		URL:        "https://github.com/o/r/pull/5#issuecomment-9",
		RoutingKey: "https://github.com/o/r/pull/5",
		Subscribe:  true,
		CreatedAt:  time.Now(),
	}
	assert.NilError(t, s.CreateLink(t.Context(), nil, link))

	// Act - matching on the routing key finds the link
	byRouting, err := s.FindSubscribedLinksForOrgs(t.Context(), nil, "https://github.com/o/r/pull/5", []int64{org.OrgID})
	assert.NilError(t, err)
	// matching on the expressive URL does not
	byURL, err := s.FindSubscribedLinksForOrgs(t.Context(), nil, "https://github.com/o/r/pull/5#issuecomment-9", []int64{org.OrgID})
	assert.NilError(t, err)

	// Assert
	assert.Equal(t, len(byRouting[org.OrgID]), 1)
	assert.Equal(t, byRouting[org.OrgID][0].ID, link.ID)
	assert.Equal(t, byRouting[org.OrgID][0].RoutingKey, "https://github.com/o/r/pull/5")
	assert.Equal(t, len(byURL[org.OrgID]), 0)
}

func TestFindSubscribedLinksForOrgsCarriesNamespace(t *testing.T) {
	t.Parallel()
	// Arrange - two subscribers to the same routing key: one in the "reviewbot"
	// namespace, one in the default namespace.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	nsTask := teststore.CreateTask(t, s, org, &teststore.TaskOptions{Namespace: "reviewbot"})
	defTask := teststore.CreateTask(t, s, org, nil)
	routingKey := "https://github.com/o/r/pull/7"
	nsLink := &model.Link{
		TaskID:     nsTask.ID,
		URL:        routingKey,
		RoutingKey: routingKey,
		Subscribe:  true,
		CreatedAt:  time.Now(),
	}
	assert.NilError(t, s.CreateLink(t.Context(), nil, nsLink))
	defLink := &model.Link{
		TaskID:     defTask.ID,
		URL:        routingKey,
		RoutingKey: routingKey,
		Subscribe:  true,
		CreatedAt:  time.Now(),
	}
	assert.NilError(t, s.CreateLink(t.Context(), nil, defLink))

	// Act
	byRouting, err := s.FindSubscribedLinksForOrgs(t.Context(), nil, routingKey, []int64{org.OrgID})
	assert.NilError(t, err)

	// Assert - each returned link carries its task's namespace.
	byID := map[int64]*model.Link{}
	for _, l := range byRouting[org.OrgID] {
		byID[l.ID] = l
	}
	assert.Equal(t, len(byID), 2)
	assert.Equal(t, byID[nsLink.ID].Namespace, "reviewbot")
	assert.Equal(t, byID[defLink.ID].Namespace, "")
}
