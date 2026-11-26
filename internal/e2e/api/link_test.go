package api_test

import (
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/e2e/harness"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestCreateLink(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	agent := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "implementer",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Links:     []*gritzv1.AgentLink{{Type: "github.pr", Kind: "owned-pr", Claim: "pr-owner"}},
	}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task with Link", AgentId: agent.Id, Draft: true})

	// Act - a comment's URL, which names the PR.
	resp, err := h.Org.Client.CreateLink(t.Context(), &gritzv1.CreateLinkRequest{
		TaskId:    task.ID,
		Relevance: "Related PR",
		Url:       "https://github.com/example/repo/pull/123#issuecomment-9",
		Title:     "Fix bug",
		Kind:      "owned-pr",
	})

	// Assert - the link stores the PR's canonical URL, and the kind's claim.
	assert.NilError(t, err)
	assert.DeepEqual(t,
		resp.Link,
		&gritzv1.TaskLink{
			TaskId:    task.ID,
			Relevance: "Related PR",
			Url:       "https://github.com/example/repo/pull/123",
			Title:     "Fix bug",
			Kind:      "owned-pr",
			Claim:     "pr-owner",
		},
		protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.TaskLink{}, "id", "created_at"),
	)
}

func TestCreateLink_Reference(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task with Link", Draft: true})

	// Act - no kind: a plain reference link.
	resp, err := h.Org.Client.CreateLink(t.Context(), &gritzv1.CreateLinkRequest{
		TaskId: task.ID,
		Url:    "https://github.com/example/repo/pull/123#issuecomment-9",
	})

	// Assert - stored as given.
	assert.NilError(t, err)
	assert.DeepEqual(t,
		resp.Link,
		&gritzv1.TaskLink{
			TaskId: task.ID,
			Url:    "https://github.com/example/repo/pull/123#issuecomment-9",
		},
		protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.TaskLink{}, "id", "created_at"),
	)
}

func TestCreateLink_Invalid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		url  string
		kind string
		err  string
	}{
		{
			name: "unknown kind",
			url:  "https://github.com/example/repo/pull/123",
			kind: "github.issue",
			err:  `unknown link kind "github.issue": agent "implementer" has link kinds "github.pr", "owned-pr"`,
		},
		{
			name: "url not of the kind's link type",
			url:  "https://github.com/example/repo/issues/123",
			kind: "owned-pr",
			err:  `url "https://github.com/example/repo/issues/123" is not a GitHub: Pull Request, the link type of kind "owned-pr"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			h := harness.New(t, nil)
			agent := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
				Name:      "implementer",
				Workspace: harness.DefaultWorkspace,
				Runner:    harness.DefaultRunner,
				Links: []*gritzv1.AgentLink{
					{Type: "github.pr"},
					{Type: "github.pr", Kind: "owned-pr"},
				},
			}})
			task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{AgentId: agent.Id, Draft: true})

			// Act
			_, err := h.Org.Client.CreateLink(t.Context(), &gritzv1.CreateLinkRequest{
				TaskId: task.ID,
				Url:    tt.url,
				Kind:   tt.kind,
			})

			// Assert
			assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
			assert.ErrorContains(t, err, tt.err)
			assert.Assert(t, cmp.Len(h.Org.ListLinks(&gritzv1.ListLinksRequest{TaskId: task.ID}), 0))
		})
	}
}

func TestCreateLink_ArchivedTask(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind string
	}{
		{name: "plain link"},
		{name: "link with a kind", kind: "github.pr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			h := harness.New(t, nil)
			agent := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
				Name:      "implementer",
				Workspace: harness.DefaultWorkspace,
				Runner:    harness.DefaultRunner,
				Links:     []*gritzv1.AgentLink{{Type: "github.pr"}},
			}})
			task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{AgentId: agent.Id, Draft: true})
			h.Org.ArchiveTask(&gritzv1.ArchiveTaskRequest{Id: task.ID})

			// Act
			_, err := h.Org.Client.CreateLink(t.Context(), &gritzv1.CreateLinkRequest{
				TaskId: task.ID,
				Url:    "https://github.com/example/repo/pull/123",
				Kind:   tt.kind,
			})

			// Assert
			assert.Equal(t, connect.CodeOf(err), connect.CodeFailedPrecondition)
			assert.ErrorContains(t, err, fmt.Sprintf("task %d is archived", task.ID))
			assert.Assert(t, cmp.Len(h.Org.ListLinks(&gritzv1.ListLinksRequest{TaskId: task.ID}), 0))
		})
	}
}

func TestCreateLink_AppendsLinkEvent(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	agent := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "implementer",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Links:     []*gritzv1.AgentLink{{Type: "github.pr", Claim: "pr-owner"}},
	}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Name: "Task with Link", AgentId: agent.Id, Draft: true})

	// Act
	resp, err := h.Org.Client.CreateLink(t.Context(), &gritzv1.CreateLinkRequest{
		TaskId:    task.ID,
		Relevance: "Related PR",
		Url:       "https://github.com/example/repo/pull/123",
		Title:     "Fix bug",
		Kind:      "github.pr",
	})

	// Assert - a link event mirroring the task_links row is appended to the
	// task's stream (the timeline source of truth; task_links is the projection).
	assert.NilError(t, err)
	events := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
		Types:  []string{model.EventTypeLink},
	})
	// The link event is about-task, not to-agent, so it does not wake the task.
	assert.DeepEqual(t,
		events,
		[]*gritzv1.Event{{
			TaskId: task.ID,
			Payload: &gritzv1.Event_Link{Link: &gritzv1.LinkPayload{
				LinkId:    resp.Link.Id,
				Relevance: "Related PR",
				Url:       "https://github.com/example/repo/pull/123",
				Title:     "Fix bug",
				Kind:      "github.pr",
				Claim:     "pr-owner",
			}},
		}},
		protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.Event{}, "id", "created_at"),
	)
}

func TestCreateLink_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
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
	h := harness.New(t, nil)
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
	h := harness.New(t, nil)
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
