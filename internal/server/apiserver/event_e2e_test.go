package apiserver_test

import (
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/e2e"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/testx"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
)

// External events arrive only through routing, so these tests arrange them as
// a webhook would: the task subscribes to an issue URL, a notify-only rule
// (no wakeup) matches every issue comment, and TestEvent fires each event at
// that URL. The rule leaves the task untouched, so each fire adds exactly one
// external event to the task's stream.

func TestGetEvent(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	url := "https://github.com/example/repo/issues/42"
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	h.Org.CreateLink(&gritzv1.CreateLinkRequest{TaskId: task.ID, Url: url, Subscribe: true})
	h.Org.SetRoutingRules(&gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{{Source: "github", Type: "issue_comment"}},
	})
	fired := h.Org.TestEvent(&gritzv1.TestEventRequest{
		Source:      "github",
		Type:        "issue_comment",
		Description: "Issue updated",
		Attrs:       map[string]string{"url": url, "body": `{"status": "closed"}`},
		Fire:        true,
	})
	eventID := fired.Matches[0].EventIds[0]

	// Act
	resp, err := h.Org.Client.GetEvent(t.Context(), &gritzv1.GetEventRequest{Id: eventID})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, resp.Event, &gritzv1.Event{
		Id:     eventID,
		TaskId: task.ID,
		Payload: &gritzv1.Event_External{External: &gritzv1.ExternalPayload{
			Description: "Issue updated",
			Url:         url,
			Data:        `{"status": "closed"}`,
			Source:      "github",
			Type:        "issue_comment",
		}},
		CreatedAt: resp.Event.CreatedAt,
	}, protocmp.Transform())
}

func TestGetEvent_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	url := "https://github.com/example/repo/issues/42"
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	h.Org.CreateLink(&gritzv1.CreateLinkRequest{TaskId: task.ID, Url: url, Subscribe: true})
	h.Org.SetRoutingRules(&gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{{Source: "github", Type: "issue_comment"}},
	})
	fired := h.Org.TestEvent(&gritzv1.TestEventRequest{
		Source:      "github",
		Type:        "issue_comment",
		Description: "Org A's event",
		Attrs:       map[string]string{"url": url},
		Fire:        true,
	})

	// Act
	_, err := other.Client.GetEvent(t.Context(), &gritzv1.GetEventRequest{Id: fired.Matches[0].EventIds[0]})

	// Assert
	assert.ErrorContains(t, err, "not found")
}

func TestListEvents(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	url := "https://github.com/example/repo/issues/42"
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	h.Org.CreateLink(&gritzv1.CreateLinkRequest{TaskId: task.ID, Url: url, Subscribe: true})
	h.Org.SetRoutingRules(&gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{{Source: "github", Type: "issue_comment"}},
	})
	for _, description := range []string{"Event 1", "Event 2"} {
		h.Org.TestEvent(&gritzv1.TestEventRequest{
			Source:      "github",
			Type:        "issue_comment",
			Description: description,
			Attrs:       map[string]string{"url": url},
			Fire:        true,
		})
	}

	// Act - uses default limit (100)
	resp, err := h.Org.Client.ListExternalEvents(t.Context(), &gritzv1.ListExternalEventsRequest{})

	// Assert - events are ordered newest first.
	assert.NilError(t, err)
	assert.Equal(t, len(resp.Events), 2)
	assert.Equal(t, resp.Events[0].GetExternal().Description, "Event 2")
	assert.Equal(t, resp.Events[1].GetExternal().Description, "Event 1")
}

func TestListEvents_ExternalOnly(t *testing.T) {
	t.Parallel()
	// Arrange - a task created with an instruction event, plus a link event from
	// CreateLink; both belong to the org but are not org-feed rows.
	h := e2e.New(t, nil)
	url := "https://github.com/example/repo/pull/1"
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Draft:        true,
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	h.Org.CreateLink(&gritzv1.CreateLinkRequest{TaskId: task.ID, Url: url, Subscribe: true})
	h.Org.SetRoutingRules(&gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{{Source: "github", Type: "issue_comment"}},
	})
	h.Org.TestEvent(&gritzv1.TestEventRequest{
		Source:      "github",
		Type:        "issue_comment",
		Description: "PR comment",
		Attrs:       map[string]string{"url": url},
		Fire:        true,
	})

	// Act
	resp, err := h.Org.Client.ListExternalEvents(t.Context(), &gritzv1.ListExternalEventsRequest{})

	// Assert - the org feed returns only the external event; instruction and link
	// events do not leak into it.
	assert.NilError(t, err)
	assert.Equal(t, len(resp.Events), 1)
	assert.Equal(t, resp.Events[0].GetExternal().Description, "PR comment")
}

func TestListEventsWithLimit(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	url := "https://github.com/example/repo/issues/42"
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	h.Org.CreateLink(&gritzv1.CreateLinkRequest{TaskId: task.ID, Url: url, Subscribe: true})
	h.Org.SetRoutingRules(&gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{{Source: "github", Type: "issue_comment"}},
	})
	for i := range 5 {
		h.Org.TestEvent(&gritzv1.TestEventRequest{
			Source:      "github",
			Type:        "issue_comment",
			Description: fmt.Sprintf("Event %d", i+1),
			Attrs:       map[string]string{"url": url},
			Fire:        true,
		})
	}

	// Act - get only the 2 most recent events.
	resp, err := h.Org.Client.ListExternalEvents(t.Context(), &gritzv1.ListExternalEventsRequest{
		Limit: 2,
	})

	// Assert - events are ordered newest first.
	assert.NilError(t, err)
	assert.Equal(t, len(resp.Events), 2)
	assert.Equal(t, resp.Events[0].GetExternal().Description, "Event 5")
	assert.Equal(t, resp.Events[1].GetExternal().Description, "Event 4")
}

func TestListEvents_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange - org A has two events, org B one.
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	url := "https://github.com/example/repo/issues/42"
	for org, descriptions := range map[*e2e.Org][]string{
		h.Org: {"Org A's event 1", "Org A's event 2"},
		other: {"Org B's event"},
	} {
		task := org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
		org.CreateLink(&gritzv1.CreateLinkRequest{TaskId: task.ID, Url: url, Subscribe: true})
		org.SetRoutingRules(&gritzv1.SetRoutingRulesRequest{
			Rules: []*gritzv1.RoutingRule{{Source: "github", Type: "issue_comment"}},
		})
		for _, description := range descriptions {
			org.TestEvent(&gritzv1.TestEventRequest{
				Source:      "github",
				Type:        "issue_comment",
				Description: description,
				Attrs:       map[string]string{"url": url},
				Fire:        true,
			})
		}
	}

	// Act
	respA, err := h.Org.Client.ListExternalEvents(t.Context(), &gritzv1.ListExternalEventsRequest{})
	assert.NilError(t, err)
	respB, err := other.Client.ListExternalEvents(t.Context(), &gritzv1.ListExternalEventsRequest{})
	assert.NilError(t, err)

	// Assert
	assert.Equal(t, len(respA.Events), 2)
	assert.Equal(t, len(respB.Events), 1)
}

func TestDeleteEvent(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	url := "https://github.com/example/repo/issues/42"
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	h.Org.CreateLink(&gritzv1.CreateLinkRequest{TaskId: task.ID, Url: url, Subscribe: true})
	h.Org.SetRoutingRules(&gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{{Source: "github", Type: "issue_comment"}},
	})
	fired := h.Org.TestEvent(&gritzv1.TestEventRequest{
		Source:      "github",
		Type:        "issue_comment",
		Description: "Event to delete",
		Attrs:       map[string]string{"url": url},
		Fire:        true,
	})
	eventID := fired.Matches[0].EventIds[0]

	// Act
	_, err := h.Org.Client.DeleteEvent(t.Context(), &gritzv1.DeleteEventRequest{Id: eventID})

	// Assert
	assert.NilError(t, err)
	_, err = h.Org.Client.GetEvent(t.Context(), &gritzv1.GetEventRequest{Id: eventID})
	assert.Equal(t, connect.CodeOf(err), connect.CodeNotFound)
}

func TestDeleteEvent_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	url := "https://github.com/example/repo/issues/42"
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	h.Org.CreateLink(&gritzv1.CreateLinkRequest{TaskId: task.ID, Url: url, Subscribe: true})
	h.Org.SetRoutingRules(&gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{{Source: "github", Type: "issue_comment"}},
	})
	fired := h.Org.TestEvent(&gritzv1.TestEventRequest{
		Source:      "github",
		Type:        "issue_comment",
		Description: "Org A's event",
		Attrs:       map[string]string{"url": url},
		Fire:        true,
	})
	eventID := fired.Matches[0].EventIds[0]

	// Act
	_, err := other.Client.DeleteEvent(t.Context(), &gritzv1.DeleteEventRequest{Id: eventID})

	// Assert - the delete silently does nothing: no error, and the event still
	// exists for org A.
	assert.NilError(t, err)
	h.Org.GetEvent(&gritzv1.GetEventRequest{Id: eventID})
}

func TestListEventsByTask(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	url := "https://github.com/example/repo/issues/42"
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	h.Org.CreateLink(&gritzv1.CreateLinkRequest{TaskId: task.ID, Url: url, Subscribe: true})
	h.Org.SetRoutingRules(&gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{{Source: "github", Type: "issue_comment"}},
	})
	for _, description := range []string{"Event 1", "Event 2"} {
		h.Org.TestEvent(&gritzv1.TestEventRequest{
			Source:      "github",
			Type:        "issue_comment",
			Description: description,
			Attrs:       map[string]string{"url": url},
			Fire:        true,
		})
	}

	// Act
	resp, err := h.Org.Client.ListEventsByTask(t.Context(), &gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
	})

	// Assert - the stream also carries the lifecycle CREATED and link events, so
	// filter to the external events. They are in chronological (oldest-first)
	// stream order.
	assert.NilError(t, err)
	var descriptions []string
	for _, e := range resp.Events {
		if x := e.GetExternal(); x != nil {
			descriptions = append(descriptions, x.Description)
		}
	}
	assert.DeepEqual(t, descriptions, []string{"Event 1", "Event 2"})
}

func TestListEventsByTask_Paged(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	url := "https://github.com/example/repo/issues/42"
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	h.Org.CreateLink(&gritzv1.CreateLinkRequest{TaskId: task.ID, Url: url, Subscribe: true})
	h.Org.SetRoutingRules(&gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{{Source: "github", Type: "issue_comment"}},
	})
	for i := range 5 {
		h.Org.TestEvent(&gritzv1.TestEventRequest{
			Source:      "github",
			Type:        "issue_comment",
			Description: fmt.Sprintf("Event %d", i+1),
			Attrs:       map[string]string{"url": url},
			Fire:        true,
		})
	}
	// One page that fits the whole stream is the ground-truth ascending order.
	full := h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{TaskId: task.ID, PageSize: 100})
	fullIDs := testx.ExtractField(full, "Id").([]int64)
	total := len(fullIDs)
	assert.Assert(t, total > 2) // multiple pages at page size 2

	// Act - newest page: empty token + a page size returns the last page-size
	// events ascending.
	const pageSize = 2
	newest, err := h.Org.Client.ListEventsByTask(t.Context(), &gritzv1.ListEventsByTaskRequest{
		TaskId:   task.ID,
		PageSize: pageSize,
	})

	// Assert - both tokens are populated: older history exists, and the
	// live-follow token is always set on the paged path.
	assert.NilError(t, err)
	assert.Equal(t, len(newest.Events), pageSize)
	assert.DeepEqual(t, testx.ExtractField(newest.Events, "Id"), fullIDs[total-pageSize:])
	assert.Assert(t, newest.PrevPageToken != "")
	assert.Assert(t, newest.NextPageToken != "")

	// Act & Assert - walk prev (scroll back) to the oldest event. Each page is
	// ascending, so prepending older pages reconstructs the full ascending
	// stream; prev empties once history is exhausted.
	got := testx.ExtractField(newest.Events, "Id").([]int64)
	for token := newest.PrevPageToken; token != ""; {
		page, err := h.Org.Client.ListEventsByTask(t.Context(), &gritzv1.ListEventsByTaskRequest{
			TaskId:    task.ID,
			PageSize:  pageSize,
			PageToken: token,
		})
		assert.NilError(t, err)
		assert.Assert(t, len(page.Events) > 0)
		got = append(testx.ExtractField(page.Events, "Id").([]int64), got...)
		token = page.PrevPageToken
	}
	assert.DeepEqual(t, got, fullIDs)
}

func TestListEventsByTask_More(t *testing.T) {
	t.Parallel()
	// Arrange - the task's stream starts with its lifecycle CREATED and link
	// events; add 4 externals for 6 total, an exact multiple of page size 2 so
	// the oldest page is full (the exact-page_size boundary).
	h := e2e.New(t, nil)
	url := "https://github.com/example/repo/issues/42"
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	h.Org.CreateLink(&gritzv1.CreateLinkRequest{TaskId: task.ID, Url: url, Subscribe: true})
	h.Org.SetRoutingRules(&gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{{Source: "github", Type: "issue_comment"}},
	})
	for i := range 4 {
		h.Org.TestEvent(&gritzv1.TestEventRequest{
			Source:      "github",
			Type:        "issue_comment",
			Description: fmt.Sprintf("Event %d", i+1),
			Attrs:       map[string]string{"url": url},
			Fire:        true,
		})
	}
	assert.Equal(t, len(h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{TaskId: task.ID, PageSize: 100})), 6)

	// Act & Assert - the newest page (empty token) walks toward older history:
	// More is true while an older page remains. Walk prev_page_token to the
	// oldest page, collecting More.
	const pageSize = 2
	var gotMore []bool
	resp, err := h.Org.Client.ListEventsByTask(t.Context(), &gritzv1.ListEventsByTaskRequest{
		TaskId:   task.ID,
		PageSize: pageSize,
	})
	assert.NilError(t, err)
	for {
		// More agrees with prev_page_token on this scroll-back walk.
		assert.Equal(t, resp.More, resp.PrevPageToken != "")
		gotMore = append(gotMore, resp.More)
		if resp.PrevPageToken == "" {
			break
		}
		resp, err = h.Org.Client.ListEventsByTask(t.Context(), &gritzv1.ListEventsByTaskRequest{
			TaskId:    task.ID,
			PageSize:  pageSize,
			PageToken: resp.PrevPageToken,
		})
		assert.NilError(t, err)
	}
	// 6 events / 2 = 3 pages; only the last (oldest, full) page has no further page.
	assert.DeepEqual(t, gotMore, []bool{true, true, false})
}

func TestListEventsByTask_LiveFollow(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	url := "https://github.com/example/repo/issues/42"
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	h.Org.CreateLink(&gritzv1.CreateLinkRequest{TaskId: task.ID, Url: url, Subscribe: true})
	h.Org.SetRoutingRules(&gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{{Source: "github", Type: "issue_comment"}},
	})
	fire := &gritzv1.TestEventRequest{
		Source:      "github",
		Type:        "issue_comment",
		Description: "Event 1",
		Attrs:       map[string]string{"url": url},
		Fire:        true,
	}
	h.Org.TestEvent(fire)

	// Act & Assert - a large page fits the whole stream: no older history (prev
	// empty), but the live-follow token is still populated so the tail can be
	// polled.
	newest, err := h.Org.Client.ListEventsByTask(t.Context(), &gritzv1.ListEventsByTaskRequest{
		TaskId:   task.ID,
		PageSize: 100,
	})
	assert.NilError(t, err)
	assert.Equal(t, newest.PrevPageToken, "")
	assert.Assert(t, newest.NextPageToken != "")

	// Act & Assert - follow the tail with nothing newer yet → an empty page
	// whose token echoes the resume cursor so polling continues.
	poll, err := h.Org.Client.ListEventsByTask(t.Context(), &gritzv1.ListEventsByTaskRequest{
		TaskId:    task.ID,
		PageSize:  100,
		PageToken: newest.NextPageToken,
	})
	assert.NilError(t, err)
	assert.Equal(t, len(poll.Events), 0)
	assert.Assert(t, poll.NextPageToken != "")
	assert.Assert(t, !poll.More) // tail reached: no further page despite the live token

	// Act & Assert - append a new event, then follow again → it appears at the
	// tail.
	fire.Description = "Event 2"
	h.Org.TestEvent(fire)
	poll2, err := h.Org.Client.ListEventsByTask(t.Context(), &gritzv1.ListEventsByTaskRequest{
		TaskId:    task.ID,
		PageSize:  100,
		PageToken: poll.NextPageToken,
	})
	assert.NilError(t, err)
	assert.Equal(t, len(poll2.Events), 1)
	assert.Equal(t, poll2.Events[0].GetExternal().Description, "Event 2")
}

func TestListEventsByTask_TypesFilter(t *testing.T) {
	t.Parallel()
	// Arrange - a task whose stream carries mixed event types: an instruction
	// from task creation, the lifecycle CREATED and link events, and an
	// external.
	h := e2e.New(t, nil)
	url := "https://github.com/example/repo/pull/1"
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Draft:        true,
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	h.Org.CreateLink(&gritzv1.CreateLinkRequest{TaskId: task.ID, Url: url, Subscribe: true})
	h.Org.SetRoutingRules(&gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{{Source: "github", Type: "issue_comment"}},
	})
	h.Org.TestEvent(&gritzv1.TestEventRequest{
		Source:      "github",
		Type:        "issue_comment",
		Description: "PR comment",
		Attrs:       map[string]string{"url": url},
		Fire:        true,
	})

	// Act & Assert - empty types on the paged path → all types, so more than
	// the instruction and external come back.
	all, err := h.Org.Client.ListEventsByTask(t.Context(), &gritzv1.ListEventsByTaskRequest{
		TaskId:   task.ID,
		PageSize: 100,
	})
	assert.NilError(t, err)
	assert.Assert(t, len(all.Events) >= 3)

	// Act & Assert - a types filter narrows to just the requested types.
	resp, err := h.Org.Client.ListEventsByTask(t.Context(), &gritzv1.ListEventsByTaskRequest{
		TaskId:   task.ID,
		PageSize: 100,
		Types:    []string{model.EventTypeExternal},
	})
	assert.NilError(t, err)
	assert.Equal(t, len(resp.Events), 1)
	assert.Equal(t, resp.Events[0].GetExternal().Description, "PR comment")

	// Act & Assert - multiple types widen the filter but still exclude the
	// lifecycle and link events.
	resp, err = h.Org.Client.ListEventsByTask(t.Context(), &gritzv1.ListEventsByTaskRequest{
		TaskId:   task.ID,
		PageSize: 100,
		Types:    []string{model.EventTypeInstruction, model.EventTypeExternal},
	})
	assert.NilError(t, err)
	assert.Equal(t, len(resp.Events), 2)
	assert.Equal(t, resp.Events[0].GetInstruction().Text, "do the thing")
	assert.Equal(t, resp.Events[1].GetExternal().Description, "PR comment")
}

func TestListEventsByTask_InvalidArgs(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})

	// Act & Assert - a page size past the max → CodeInvalidArgument.
	_, err := h.Org.Client.ListEventsByTask(t.Context(), &gritzv1.ListEventsByTaskRequest{
		TaskId:   task.ID,
		PageSize: 5000,
	})
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)

	// Act & Assert - an undecodable page token → CodeInvalidArgument.
	_, err = h.Org.Client.ListEventsByTask(t.Context(), &gritzv1.ListEventsByTaskRequest{
		TaskId:    task.ID,
		PageToken: "@@not-a-valid-token@@",
	})
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
}

func TestListEventsByTask_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	other := h.Server.CreateOrg()
	url := "https://github.com/example/repo/issues/42"
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	h.Org.CreateLink(&gritzv1.CreateLinkRequest{TaskId: task.ID, Url: url, Subscribe: true})
	h.Org.SetRoutingRules(&gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{{Source: "github", Type: "issue_comment"}},
	})
	h.Org.TestEvent(&gritzv1.TestEventRequest{
		Source:      "github",
		Type:        "issue_comment",
		Description: "Org A's event",
		Attrs:       map[string]string{"url": url},
		Fire:        true,
	})

	// Act
	respA, err := h.Org.Client.ListEventsByTask(t.Context(), &gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
	})
	assert.NilError(t, err)
	respB, err := other.Client.ListEventsByTask(t.Context(), &gritzv1.ListEventsByTaskRequest{
		TaskId: task.ID,
	})
	assert.NilError(t, err)

	// Assert - org A sees its external event (alongside its lifecycle and link
	// events); org B gets an empty list (blanket read skips the row load; the
	// list query is org-scoped).
	var externalA int
	for _, e := range respA.Events {
		if e.GetExternal() != nil {
			externalA++
		}
	}
	assert.Equal(t, externalA, 1)
	assert.Equal(t, len(respB.Events), 0)
}
