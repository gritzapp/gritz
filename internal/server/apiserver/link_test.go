package apiserver_test

import (
	"testing"

	"github.com/gritzapp/gritz/internal/e2e"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestCreateLink(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task with Link", Draft: true})

	// Act
	resp, err := h.Org.Client.CreateLink(t.Context(), &gritzv1.CreateLinkRequest{
		TaskId:    task.ID,
		Relevance: "Related PR",
		Url:       "https://github.com/example/repo/pull/123",
		Title:     "Fix bug",
		Subscribe: true,
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, resp.Link.TaskId, task.ID)
	assert.Equal(t, resp.Link.Url, "https://github.com/example/repo/pull/123")
	assert.Equal(t, resp.Link.Subscribe, true)
}

func TestCreateLink_AppendsLinkEvent(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task with Link", Draft: true})

	// Act
	resp, err := h.Org.Client.CreateLink(t.Context(), &gritzv1.CreateLinkRequest{
		TaskId:    task.ID,
		Relevance: "Related PR",
		Url:       "https://github.com/example/repo/pull/123",
		Title:     "Fix bug",
		Subscribe: true,
	})

	// Assert - a link event mirroring the task_links row is appended to the
	// task's stream (the timeline source of truth; task_links is the projection).
	assert.NilError(t, err)
	events := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
		Types:  []string{model.EventTypeLink},
	})
	assert.Assert(t, cmp.Len(events, 1))
	link := events[0].GetLink()
	assert.Equal(t, link.LinkId, resp.Link.Id)
	assert.Equal(t, link.Relevance, "Related PR")
	assert.Equal(t, link.Url, "https://github.com/example/repo/pull/123")
	assert.Equal(t, link.Title, "Fix bug")
	assert.Equal(t, link.Subscribe, true)
	// The link event is about-task, not to-agent, so it does not wake the task.
	assert.Equal(t, events[0].Wake, false)
}

func TestCreateLink_DerivesRoutingKey(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task with Link", Draft: true})

	// Act - no routing_key supplied, so the server derives it from url
	resp, err := h.Org.Client.CreateLink(t.Context(), &gritzv1.CreateLinkRequest{
		TaskId: task.ID,
		Url:    "https://github.com/example/repo/pull/123#issuecomment-9",
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, resp.Link.Url, "https://github.com/example/repo/pull/123#issuecomment-9")
	assert.Equal(t, resp.Link.RoutingKey, "https://github.com/example/repo/pull/123")
}

func TestCreateLink_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "User A's Task", Draft: true})

	// Act
	_, err := other.Client.CreateLink(t.Context(), &gritzv1.CreateLinkRequest{
		TaskId:    task.ID,
		Relevance: "Sneaky link",
		Url:       "https://github.com/example/repo/pull/123",
	})

	// Assert
	assert.ErrorContains(t, err, "not found")
}

func TestListLinks(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task with Links", Draft: true})
	h.Org.CreateLink(&gritzv1.CreateLinkRequest{
		TaskId:    task.ID,
		Relevance: "Link 1",
		Url:       "https://github.com/example/repo/pull/1",
	})
	h.Org.CreateLink(&gritzv1.CreateLinkRequest{
		TaskId:    task.ID,
		Relevance: "Link 2",
		Url:       "https://github.com/example/repo/pull/2",
	})

	// Act
	resp, err := h.Org.Client.ListLinks(t.Context(), &gritzv1.ListLinksRequest{
		TaskId: task.ID,
	})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(resp.Links, 2))
}

func TestListLinks_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "User A's Task", Draft: true})
	h.Org.CreateLink(&gritzv1.CreateLinkRequest{
		TaskId:    task.ID,
		Relevance: "User A's Link",
		Url:       "https://github.com/example/repo/pull/1",
	})

	// Act
	resp, err := other.Client.ListLinks(t.Context(), &gritzv1.ListLinksRequest{
		TaskId: task.ID,
	})

	// Assert - the other org gets an empty list, not an error (blanket read
	// skips the row load; the list query is org-scoped).
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(resp.Links, 0))
}
