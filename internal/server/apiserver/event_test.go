package apiserver

import (
	"context"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"github.com/gritzapp/gritz/internal/x/testx"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
)

// orgWithWorkspace creates an org that has a runner/workspace pair, which is a
// prerequisite for creating the tasks that events are now scoped to.
func orgWithWorkspace(t *testing.T, srv *Server) *teststore.Org {
	t.Helper()
	return teststore.CreateOrg(t, srv.store, &teststore.OrgOptions{
		Workspaces: []teststore.WorkspaceOptions{{RunnerID: "test-runner", Name: "test-workspace"}},
	})
}

// createTestTask creates a task to own the events under test.
func createTestTask(t *testing.T, srv *Server, ctx context.Context) int64 {
	t.Helper()
	resp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:      "Test Task",
		Runner:    "test-runner",
		Workspace: "test-workspace",
	})
	assert.NilError(t, err)
	return resp.Task.Id
}

func TestGetEvent(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := orgWithWorkspace(t, srv)
	ctx := createCtx(t, org)
	taskID := createTestTask(t, srv, ctx)
	// The CreateEvent RPC is gone, so seed the fixture via the store's append.
	event := &model.Event{
		TaskID: taskID,
		OrgID:  org.OrgID,
		Payload: &model.ExternalPayload{
			Description: "Issue updated",
			URL:         "https://github.com/example/repo/issues/42",
			Data:        `{"status": "closed"}`,
		},
	}
	assert.NilError(t, srv.store.CreateEvent(ctx, nil, event))

	// Act
	getResp, err := srv.GetEvent(ctx, &gritzv1.GetEventRequest{
		Id: event.ID,
	})

	// Assert
	assert.NilError(t, err)
	expected := &gritzv1.Event{
		Id:     event.ID,
		TaskId: taskID,
		Payload: &gritzv1.Event_External{External: &gritzv1.ExternalPayload{
			Description: "Issue updated",
			Url:         "https://github.com/example/repo/issues/42",
			Data:        `{"status": "closed"}`,
		}},
		CreatedAt: getResp.Event.CreatedAt,
	}
	assert.DeepEqual(t, getResp.Event, expected, protocmp.Transform())
}

func TestGetEvent_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	orgA := orgWithWorkspace(t, srv)
	ctxA := createCtx(t, orgA)
	orgB := orgWithWorkspace(t, srv)
	ctxB := createCtx(t, orgB)
	taskA := createTestTask(t, srv, ctxA)
	event := &model.Event{
		TaskID:  taskA,
		OrgID:   orgA.OrgID,
		Payload: &model.ExternalPayload{Description: "User A's Event"},
	}
	assert.NilError(t, srv.store.CreateEvent(ctxA, nil, event))

	// Act
	_, err := srv.GetEvent(ctxB, &gritzv1.GetEventRequest{
		Id: event.ID,
	})

	// Assert
	assert.ErrorContains(t, err, "not found")
}

func TestListEvents(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := orgWithWorkspace(t, srv)
	ctx := createCtx(t, org)
	taskID := createTestTask(t, srv, ctx)
	assert.NilError(t, srv.store.CreateEvent(ctx, nil, &model.Event{
		TaskID:  taskID,
		OrgID:   org.OrgID,
		Payload: &model.ExternalPayload{Description: "Event 1", Data: `{"test": "data1"}`},
	}))
	assert.NilError(t, srv.store.CreateEvent(ctx, nil, &model.Event{
		TaskID:  taskID,
		OrgID:   org.OrgID,
		Payload: &model.ExternalPayload{Description: "Event 2", Data: `{"test": "data2"}`},
	}))

	// Act - uses default limit (100)
	resp, err := srv.ListExternalEvents(ctx, &gritzv1.ListExternalEventsRequest{})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, len(resp.Events), 2)
	// Events are ordered by created_at DESC (newest first)
	assert.Equal(t, resp.Events[0].GetExternal().Description, "Event 2")
	assert.Equal(t, resp.Events[1].GetExternal().Description, "Event 1")
}

func TestListEvents_ExternalOnly(t *testing.T) {
	t.Parallel()
	// Arrange - a task seeded with an instruction event, plus a link event from
	// CreateLink; both carry an org_id but are not org-feed rows.
	srv := New(Options{Store: teststore.New(t)})
	org := orgWithWorkspace(t, srv)
	ctx := createCtx(t, org)
	taskResp, err := srv.CreateTask(ctx, &gritzv1.CreateTaskRequest{
		Name:         "Test Task",
		Runner:       "test-runner",
		Workspace:    "test-workspace",
		Instructions: []*gritzv1.Instruction{{Text: "do the thing"}},
	})
	assert.NilError(t, err)
	_, err = srv.CreateLink(ctx, &gritzv1.CreateLinkRequest{
		TaskId: taskResp.Task.Id,
		Url:    "https://github.com/example/repo/pull/1",
	})
	assert.NilError(t, err)
	assert.NilError(t, srv.store.CreateEvent(ctx, nil, &model.Event{
		TaskID:  taskResp.Task.Id,
		OrgID:   org.OrgID,
		Payload: &model.ExternalPayload{Description: "PR comment"},
	}))

	// Act
	resp, err := srv.ListExternalEvents(ctx, &gritzv1.ListExternalEventsRequest{})

	// Assert - the org feed returns only the external event; instruction and link
	// events do not leak into it.
	assert.NilError(t, err)
	assert.Equal(t, len(resp.Events), 1)
	assert.Equal(t, resp.Events[0].GetExternal().Description, "PR comment")
}

func TestListEventsWithLimit(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := orgWithWorkspace(t, srv)
	ctx := createCtx(t, org)
	taskID := createTestTask(t, srv, ctx)

	// Create 5 events
	for i := range 5 {
		assert.NilError(t, srv.store.CreateEvent(ctx, nil, &model.Event{
			TaskID:  taskID,
			OrgID:   org.OrgID,
			Payload: &model.ExternalPayload{Description: fmt.Sprintf("Event %d", i+1)},
		}))
	}

	// Act - Get only 2 most recent events
	resp, err := srv.ListExternalEvents(ctx, &gritzv1.ListExternalEventsRequest{
		Limit: 2,
	})
	assert.NilError(t, err)

	// Assert
	assert.Equal(t, len(resp.Events), 2)
	// Events are ordered by created_at DESC (newest first)
	assert.Equal(t, resp.Events[0].GetExternal().Description, "Event 5")
	assert.Equal(t, resp.Events[1].GetExternal().Description, "Event 4")
}

func TestListEvents_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	orgA := orgWithWorkspace(t, srv)
	ctxA := createCtx(t, orgA)
	orgB := orgWithWorkspace(t, srv)
	ctxB := createCtx(t, orgB)
	taskA := createTestTask(t, srv, ctxA)
	taskB := createTestTask(t, srv, ctxB)
	assert.NilError(t, srv.store.CreateEvent(ctxA, nil, &model.Event{
		TaskID:  taskA,
		OrgID:   orgA.OrgID,
		Payload: &model.ExternalPayload{Description: "User A's Event 1"},
	}))
	assert.NilError(t, srv.store.CreateEvent(ctxA, nil, &model.Event{
		TaskID:  taskA,
		OrgID:   orgA.OrgID,
		Payload: &model.ExternalPayload{Description: "User A's Event 2"},
	}))
	assert.NilError(t, srv.store.CreateEvent(ctxB, nil, &model.Event{
		TaskID:  taskB,
		OrgID:   orgB.OrgID,
		Payload: &model.ExternalPayload{Description: "User B's Event"},
	}))

	// Act
	respA, err := srv.ListExternalEvents(ctxA, &gritzv1.ListExternalEventsRequest{})
	assert.NilError(t, err)
	respB, err := srv.ListExternalEvents(ctxB, &gritzv1.ListExternalEventsRequest{})
	assert.NilError(t, err)

	// Assert
	assert.Equal(t, len(respA.Events), 2)
	assert.Equal(t, len(respB.Events), 1)
}

func TestDeleteEvent(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := orgWithWorkspace(t, srv)
	ctx := createCtx(t, org)
	taskID := createTestTask(t, srv, ctx)
	event := &model.Event{
		TaskID:  taskID,
		OrgID:   org.OrgID,
		Payload: &model.ExternalPayload{Description: "Event to Delete"},
	}
	assert.NilError(t, srv.store.CreateEvent(ctx, nil, event))

	// Act
	_, err := srv.DeleteEvent(ctx, &gritzv1.DeleteEventRequest{
		Id: event.ID,
	})

	// Assert
	assert.NilError(t, err)
	_, getErr := srv.GetEvent(ctx, &gritzv1.GetEventRequest{Id: event.ID})
	assert.ErrorContains(t, getErr, "")
}

func TestDeleteEvent_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	orgA := orgWithWorkspace(t, srv)
	ctxA := createCtx(t, orgA)
	orgB := orgWithWorkspace(t, srv)
	ctxB := createCtx(t, orgB)
	taskA := createTestTask(t, srv, ctxA)
	event := &model.Event{
		TaskID:  taskA,
		OrgID:   orgA.OrgID,
		Payload: &model.ExternalPayload{Description: "User A's Event"},
	}
	assert.NilError(t, srv.store.CreateEvent(ctxA, nil, event))

	// Act
	_, err := srv.DeleteEvent(ctxB, &gritzv1.DeleteEventRequest{
		Id: event.ID,
	})

	// Assert - delete should silently fail (no error, but event still exists)
	assert.NilError(t, err)
	// Verify event still exists for user A
	_, err = srv.GetEvent(ctxA, &gritzv1.GetEventRequest{Id: event.ID})
	assert.NilError(t, err)
}

func TestListEventsByTask(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := orgWithWorkspace(t, srv)
	ctx := createCtx(t, org)
	taskID := createTestTask(t, srv, ctx)

	assert.NilError(t, srv.store.CreateEvent(ctx, nil, &model.Event{
		TaskID:  taskID,
		OrgID:   org.OrgID,
		Payload: &model.ExternalPayload{Description: "Event 1"},
	}))
	assert.NilError(t, srv.store.CreateEvent(ctx, nil, &model.Event{
		TaskID:  taskID,
		OrgID:   org.OrgID,
		Payload: &model.ExternalPayload{Description: "Event 2"},
	}))

	// Act
	resp, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{
		TaskId: taskID,
	})

	// Assert - the stream also carries the lifecycle CREATED event from task
	// creation, so filter to the external events. They are in chronological
	// (oldest-first) stream order.
	assert.NilError(t, err)
	var external []*gritzv1.ExternalPayload
	for _, e := range resp.Events {
		if x := e.GetExternal(); x != nil {
			external = append(external, x)
		}
	}
	assert.DeepEqual(t, external, []*gritzv1.ExternalPayload{
		{Description: "Event 1"},
		{Description: "Event 2"},
	}, protocmp.Transform())
}

func TestListEventsByTask_LegacyNoTokens(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := orgWithWorkspace(t, srv)
	ctx := createCtx(t, org)
	taskID := createTestTask(t, srv, ctx)
	assert.NilError(t, srv.store.CreateEvent(ctx, nil, &model.Event{
		TaskID:  taskID,
		OrgID:   org.OrgID,
		Payload: &model.ExternalPayload{Description: "Event 1"},
	}))

	// Act - no pagination fields → the legacy unpaged path.
	resp, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{TaskId: taskID})

	// Assert - full ascending list, no tokens (exactly today's behavior).
	assert.NilError(t, err)
	assert.Assert(t, len(resp.Events) >= 2) // lifecycle CREATED + the external event
	assert.Equal(t, resp.PrevPageToken, "")
	assert.Equal(t, resp.NextPageToken, "")
}

func TestListEventsByTask_Paged(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := orgWithWorkspace(t, srv)
	ctx := createCtx(t, org)
	taskID := createTestTask(t, srv, ctx)
	for i := range 5 {
		assert.NilError(t, srv.store.CreateEvent(ctx, nil, &model.Event{
			TaskID:  taskID,
			OrgID:   org.OrgID,
			Payload: &model.ExternalPayload{Description: fmt.Sprintf("Event %d", i+1)},
		}))
	}

	// The legacy unpaged list is the ground-truth ascending order.
	full, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{TaskId: taskID})
	assert.NilError(t, err)
	fullIDs := testx.ExtractField(full.Events, "Id").([]int64)
	total := len(fullIDs)
	assert.Assert(t, total > 2) // multiple pages at page size 2

	// Newest page: empty token + a page size returns the last page-size events
	// ascending, with both tokens populated — older history exists, and the
	// live-follow token is always set on the paged path.
	const pageSize = 2
	newest, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{
		TaskId: taskID, PageSize: pageSize,
	})
	assert.NilError(t, err)
	assert.Equal(t, len(newest.Events), pageSize)
	assert.DeepEqual(t, testx.ExtractField(newest.Events, "Id"), fullIDs[total-pageSize:])
	assert.Assert(t, newest.PrevPageToken != "")
	assert.Assert(t, newest.NextPageToken != "")

	// Walk prev (scroll back) to the oldest event. Each page is ascending, so
	// prepending older pages reconstructs the full ascending stream; prev empties
	// once history is exhausted.
	got := testx.ExtractField(newest.Events, "Id").([]int64)
	for token := newest.PrevPageToken; token != ""; {
		page, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{
			TaskId: taskID, PageSize: pageSize, PageToken: token,
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
	// Arrange - createTestTask seeds one lifecycle CREATED event; add 5 externals
	// for 6 total, an exact multiple of page size 2 so the oldest page is full
	// (the exact-page_size boundary).
	srv := New(Options{Store: teststore.New(t)})
	org := orgWithWorkspace(t, srv)
	ctx := createCtx(t, org)
	taskID := createTestTask(t, srv, ctx)
	for i := range 5 {
		assert.NilError(t, srv.store.CreateEvent(ctx, nil, &model.Event{
			TaskID:  taskID,
			OrgID:   org.OrgID,
			Payload: &model.ExternalPayload{Description: fmt.Sprintf("Event %d", i+1)},
		}))
	}

	// Newest page (empty token) walks toward older history: More is true while an
	// older page remains. Walk prev_page_token to the oldest page, collecting More.
	const pageSize = 2
	var gotMore []bool
	resp, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{
		TaskId: taskID, PageSize: pageSize,
	})
	assert.NilError(t, err)
	for {
		// More agrees with prev_page_token on this scroll-back walk.
		assert.Equal(t, resp.More, resp.PrevPageToken != "")
		gotMore = append(gotMore, resp.More)
		if resp.PrevPageToken == "" {
			break
		}
		resp, err = srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{
			TaskId: taskID, PageSize: pageSize, PageToken: resp.PrevPageToken,
		})
		assert.NilError(t, err)
	}
	// 6 events / 2 = 3 pages; only the last (oldest, full) page has no further page.
	assert.DeepEqual(t, gotMore, []bool{true, true, false})
}

func TestListEventsByTask_LiveFollow(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := orgWithWorkspace(t, srv)
	ctx := createCtx(t, org)
	taskID := createTestTask(t, srv, ctx)
	assert.NilError(t, srv.store.CreateEvent(ctx, nil, &model.Event{
		TaskID:  taskID,
		OrgID:   org.OrgID,
		Payload: &model.ExternalPayload{Description: "Event 1"},
	}))

	// A large page fits the whole stream: no older history (prev empty), but the
	// live-follow token is still populated so the tail can be polled.
	newest, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{
		TaskId: taskID, PageSize: 100,
	})
	assert.NilError(t, err)
	assert.Equal(t, newest.PrevPageToken, "")
	assert.Assert(t, newest.NextPageToken != "")

	// Follow the tail with nothing newer yet → an empty page whose token echoes the
	// resume cursor so polling continues.
	poll, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{
		TaskId: taskID, PageSize: 100, PageToken: newest.NextPageToken,
	})
	assert.NilError(t, err)
	assert.Equal(t, len(poll.Events), 0)
	assert.Assert(t, poll.NextPageToken != "")
	assert.Assert(t, !poll.More) // tail reached: no further page despite the live token

	// Append a new event, then follow again → it appears at the tail.
	assert.NilError(t, srv.store.CreateEvent(ctx, nil, &model.Event{
		TaskID:  taskID,
		OrgID:   org.OrgID,
		Payload: &model.ExternalPayload{Description: "Event 2"},
	}))
	poll2, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{
		TaskId: taskID, PageSize: 100, PageToken: poll.NextPageToken,
	})
	assert.NilError(t, err)
	assert.Equal(t, len(poll2.Events), 1)
	assert.Equal(t, poll2.Events[0].GetExternal().Description, "Event 2")
}

func TestListEventsByTask_TypesFilter(t *testing.T) {
	t.Parallel()
	// Arrange - a task whose stream carries mixed event types: the lifecycle
	// CREATED event from task creation, plus a seeded instruction and external.
	srv := New(Options{Store: teststore.New(t)})
	org := orgWithWorkspace(t, srv)
	ctx := createCtx(t, org)
	taskID := createTestTask(t, srv, ctx)
	assert.NilError(t, srv.store.CreateEvent(ctx, nil, &model.Event{
		TaskID:  taskID,
		OrgID:   org.OrgID,
		Payload: &model.InstructionPayload{Text: "do the thing"},
	}))
	assert.NilError(t, srv.store.CreateEvent(ctx, nil, &model.Event{
		TaskID:  taskID,
		OrgID:   org.OrgID,
		Payload: &model.ExternalPayload{Description: "PR comment"},
	}))

	// Empty types on the paged path → all types (instruction + external + the
	// lifecycle CREATED), so more than the two seeded events come back.
	all, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{
		TaskId: taskID, PageSize: 100,
	})
	assert.NilError(t, err)
	assert.Assert(t, len(all.Events) >= 3)

	// A types filter narrows to just the requested types.
	resp, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{
		TaskId: taskID, PageSize: 100,
		Types: []string{model.EventTypeExternal},
	})
	assert.NilError(t, err)
	assert.Equal(t, len(resp.Events), 1)
	assert.Equal(t, resp.Events[0].GetExternal().Description, "PR comment")

	// Multiple types widen the filter but still exclude lifecycle.
	resp, err = srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{
		TaskId: taskID, PageSize: 100,
		Types: []string{model.EventTypeInstruction, model.EventTypeExternal},
	})
	assert.NilError(t, err)
	assert.Equal(t, len(resp.Events), 2)
	assert.Equal(t, resp.Events[0].GetInstruction().Text, "do the thing")
	assert.Equal(t, resp.Events[1].GetExternal().Description, "PR comment")
}

func TestListEventsByTask_InvalidArgs(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	org := orgWithWorkspace(t, srv)
	ctx := createCtx(t, org)
	taskID := createTestTask(t, srv, ctx)

	// A page size past the max → CodeInvalidArgument.
	_, err := srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{
		TaskId: taskID, PageSize: 5000,
	})
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)

	// An undecodable page token → CodeInvalidArgument.
	_, err = srv.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{
		TaskId: taskID, PageToken: "@@not-a-valid-token@@",
	})
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
}

func TestListEventsByTask_Permissions(t *testing.T) {
	t.Parallel()
	// Arrange
	srv := New(Options{Store: teststore.New(t)})
	orgA := orgWithWorkspace(t, srv)
	ctxA := createCtx(t, orgA)
	orgB := orgWithWorkspace(t, srv)
	ctxB := createCtx(t, orgB)
	taskA := createTestTask(t, srv, ctxA)
	assert.NilError(t, srv.store.CreateEvent(ctxA, nil, &model.Event{
		TaskID:  taskA,
		OrgID:   orgA.OrgID,
		Payload: &model.ExternalPayload{Description: "User A's Event"},
	}))

	// Act
	respA, err := srv.ListEventsByTask(ctxA, &gritzv1.ListEventsByTaskRequest{
		TaskId: taskA,
	})
	assert.NilError(t, err)
	respB, err := srv.ListEventsByTask(ctxB, &gritzv1.ListEventsByTaskRequest{
		TaskId: taskA,
	})
	assert.NilError(t, err)

	// Assert - org A sees its external event (alongside the lifecycle CREATED
	// event from task creation); org B gets an empty list (blanket read skips the
	// row load; the list query is org-scoped).
	var externalA int
	for _, e := range respA.Events {
		if e.GetExternal() != nil {
			externalA++
		}
	}
	assert.Equal(t, externalA, 1)
	assert.Equal(t, len(respB.Events), 0)
}
