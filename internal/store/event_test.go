package store_test

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"github.com/gritzapp/gritz/internal/x/pagination"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestListEventsByTaskPage(t *testing.T) {
	t.Parallel()
	// Arrange - seed 10 report events, recording their ids in insertion order.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task := teststore.CreateTask(t, s, org, nil)
	var want []int64
	for i := range 10 {
		event := &model.Event{
			TaskID:  task.ID,
			OrgID:   org.OrgID,
			Payload: &model.ReportPayload{Content: fmt.Sprintf("report %d", i+1)},
		}
		assert.NilError(t, s.CreateEvent(t.Context(), nil, event))
		want = append(want, event.ID)
	}

	// Act - open at the tail (empty token), then follow OlderToken toward older
	// history, prepending each older page so the whole stream reassembles
	// oldest-first.
	var got []int64
	token := ""
	pages := 0
	for {
		page, err := s.ListEventsByTaskPage(t.Context(), nil, store.ListEventsByTaskPageParams{
			TaskID:    task.ID,
			OrgID:     org.OrgID,
			PageSize:  3,
			PageToken: token,
		})
		assert.NilError(t, err)
		// Every page is ascending; older pages are prepended.
		got = append(testx.ExtractField(page.Items, "ID").([]int64), got...)
		pages++
		if page.OlderToken == "" {
			break
		}
		token = page.OlderToken
	}

	// Assert - the scroll-back covers the whole stream ascending, no gaps/dups.
	assert.Equal(t, pages, 4) // 3+3+3+1
	assert.DeepEqual(t, got, want)
}

func TestListEventsByTaskPage_More(t *testing.T) {
	t.Parallel()
	// Arrange - seed exactly 9 events so the final page (page size 3) is full,
	// exercising the exact-page_size boundary the length heuristic gets wrong.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task := teststore.CreateTask(t, s, org, nil)
	for i := range 9 {
		assert.NilError(t, s.CreateEvent(t.Context(), nil, &model.Event{
			TaskID:  task.ID,
			OrgID:   org.OrgID,
			Payload: &model.ReportPayload{Content: fmt.Sprintf("report %d", i+1)},
		}))
	}

	// Act/Assert - walk OlderToken toward older history. More stays true while an
	// older page remains and flips false on the last (full) page, even though the
	// walk lands on an exact page_size boundary.
	var wantMore []bool
	token := ""
	for {
		page, err := s.ListEventsByTaskPage(t.Context(), nil, store.ListEventsByTaskPageParams{
			TaskID:    task.ID,
			OrgID:     org.OrgID,
			PageSize:  3,
			PageToken: token,
		})
		assert.NilError(t, err)
		// More agrees with OlderToken on this scroll-back: both mark older history.
		assert.Equal(t, page.More, page.OlderToken != "")
		wantMore = append(wantMore, page.More)
		if page.OlderToken == "" {
			break
		}
		token = page.OlderToken
	}
	// Three full pages (3+3+3); the tail page reports no further page.
	assert.DeepEqual(t, wantMore, []bool{true, true, false})
}

func TestListEventsByTaskPage_LiveFollow(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task := teststore.CreateTask(t, s, org, nil)
	for i := range 3 {
		assert.NilError(t, s.CreateEvent(t.Context(), nil, &model.Event{
			TaskID:  task.ID,
			OrgID:   org.OrgID,
			Payload: &model.ReportPayload{Content: fmt.Sprintf("report %d", i+1)},
		}))
	}

	// The newest page carries a FollowToken (the live-follow cursor at the tail).
	page, err := s.ListEventsByTaskPage(t.Context(), nil, store.ListEventsByTaskPageParams{
		TaskID:   task.ID,
		OrgID:    org.OrgID,
		PageSize: 50,
	})
	assert.NilError(t, err)
	assert.Assert(t, page.FollowToken != "")
	follow := page.FollowToken

	// Following the tail cursor now yields nothing new (echoes the cursor).
	empty, err := s.ListEventsByTaskPage(t.Context(), nil, store.ListEventsByTaskPageParams{
		TaskID:    task.ID,
		OrgID:     org.OrgID,
		PageSize:  50,
		PageToken: follow,
	})
	assert.NilError(t, err)
	assert.Equal(t, len(empty.Items), 0)
	assert.Assert(t, empty.FollowToken != "") // still resumable
	assert.Assert(t, !empty.More)             // nothing newer: the tail is reached

	// Act - a subsequently-inserted event is picked up by the same tail token.
	newEvent := &model.Event{
		TaskID:  task.ID,
		OrgID:   org.OrgID,
		Payload: &model.ReportPayload{Content: "report 4"},
	}
	assert.NilError(t, s.CreateEvent(t.Context(), nil, newEvent))
	page, err = s.ListEventsByTaskPage(t.Context(), nil, store.ListEventsByTaskPageParams{
		TaskID:    task.ID,
		OrgID:     org.OrgID,
		PageSize:  50,
		PageToken: follow,
	})
	assert.NilError(t, err)

	// Assert - the one new event, and no further page beyond it.
	assert.DeepEqual(t, testx.ExtractField(page.Items, "ID"), []int64{newEvent.ID})
	assert.Assert(t, !page.More)

	// Insert two more events so more than a (small) page is newer than the tail
	// cursor, then follow with page size 2: More is true while newer rows remain.
	for i := range 2 {
		assert.NilError(t, s.CreateEvent(t.Context(), nil, &model.Event{
			TaskID:  task.ID,
			OrgID:   org.OrgID,
			Payload: &model.ReportPayload{Content: fmt.Sprintf("report %d", i+5)},
		}))
	}
	backlog, err := s.ListEventsByTaskPage(t.Context(), nil, store.ListEventsByTaskPageParams{
		TaskID:    task.ID,
		OrgID:     org.OrgID,
		PageSize:  2,
		PageToken: follow,
	})
	assert.NilError(t, err)
	assert.Equal(t, len(backlog.Items), 2)
	assert.Assert(t, backlog.More) // a third newer event remains beyond this page
}

func TestListEventsByTaskPage_FollowFromEmpty(t *testing.T) {
	t.Parallel()
	// Arrange - open the timeline of a task with no events yet.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task := teststore.CreateTask(t, s, org, nil)
	empty, err := s.ListEventsByTaskPage(t.Context(), nil, store.ListEventsByTaskPageParams{
		TaskID:   task.ID,
		OrgID:    org.OrgID,
		PageSize: 50,
	})
	assert.NilError(t, err)
	event := &model.Event{
		TaskID:  task.ID,
		OrgID:   org.OrgID,
		Payload: &model.ReportPayload{Content: "report 1"},
	}
	assert.NilError(t, s.CreateEvent(t.Context(), nil, event))

	// Act - follow the empty page's token once an event has landed.
	page, err := s.ListEventsByTaskPage(t.Context(), nil, store.ListEventsByTaskPageParams{
		TaskID:    task.ID,
		OrgID:     org.OrgID,
		PageSize:  50,
		PageToken: empty.FollowToken,
	})

	// Assert - the empty page still follows, from the task's first event.
	assert.NilError(t, err)
	assert.Equal(t, len(empty.Items), 0)
	assert.Assert(t, empty.FollowToken != "")
	assert.DeepEqual(t, testx.ExtractField(page.Items, "ID"), []int64{event.ID})
}

func TestListEventsByTaskPage_TypesFilter(t *testing.T) {
	t.Parallel()
	// Arrange - a mix of report and external events on the same task.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task := teststore.CreateTask(t, s, org, nil)
	assert.NilError(t, s.CreateEvent(t.Context(), nil, &model.Event{
		TaskID:  task.ID,
		OrgID:   org.OrgID,
		Payload: &model.ReportPayload{Content: "report"},
	}))
	external := &model.Event{
		TaskID:  task.ID,
		OrgID:   org.OrgID,
		Payload: &model.ExternalPayload{Description: "external"},
	}
	assert.NilError(t, s.CreateEvent(t.Context(), nil, external))

	// Act
	page, err := s.ListEventsByTaskPage(t.Context(), nil, store.ListEventsByTaskPageParams{
		TaskID:   task.ID,
		OrgID:    org.OrgID,
		Types:    []string{model.EventTypeExternal},
		PageSize: 50,
	})

	// Assert - only the external event comes back.
	assert.NilError(t, err)
	assert.DeepEqual(t, testx.ExtractField(page.Items, "ID"), []int64{external.ID})
}

func TestListEventsByTaskPage_BadPageSize(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task := teststore.CreateTask(t, s, org, nil)

	// Act - a page size past the max is rejected.
	_, err := s.ListEventsByTaskPage(t.Context(), nil, store.ListEventsByTaskPageParams{
		TaskID:   task.ID,
		OrgID:    org.OrgID,
		PageSize: 201,
	})

	// Assert
	assert.Assert(t, errors.Is(err, pagination.ErrInvalidRequest))
}

func TestListEventsByTaskPage_BadToken(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task := teststore.CreateTask(t, s, org, nil)

	// Act - an undecodable token is rejected.
	_, err := s.ListEventsByTaskPage(t.Context(), nil, store.ListEventsByTaskPageParams{
		TaskID:    task.ID,
		OrgID:     org.OrgID,
		PageSize:  50,
		PageToken: "!!!not-base64!!!",
	})

	// Assert
	assert.Assert(t, errors.Is(err, pagination.ErrInvalidRequest))
}

func TestListRunnerEvents_CommitOutOfIDOrder(t *testing.T) {
	t.Parallel()
	// Arrange - two tasks on the runner, each written by its own transaction.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	taskA := teststore.CreateTask(t, s, org, &teststore.TaskOptions{Runner: "runner-1"})
	taskB := teststore.CreateTask(t, s, org, &teststore.TaskOptions{Runner: "runner-1"})
	eventA := &model.Event{
		TaskID:  taskA.ID,
		OrgID:   org.OrgID,
		Payload: &model.ReportPayload{Content: "a"},
	}
	eventB := &model.Event{
		TaskID:  taskB.ID,
		OrgID:   org.OrgID,
		Payload: &model.ReportPayload{Content: "b"},
	}
	var heldBack *pagination.StreamPage[*model.Event]

	// Act - Tx A inserts first (lower id), Tx B inserts second and commits
	// first; read while A is still open, then again once A commits.
	err := s.WithTx(t.Context(), nil, func(txA *sql.Tx) error {
		assert.NilError(t, s.CreateEvent(t.Context(), txA, eventA))
		err := s.WithTx(t.Context(), nil, func(txB *sql.Tx) error {
			assert.NilError(t, s.CreateEvent(t.Context(), txB, eventB))
			return txB.Commit()
		})
		assert.NilError(t, err)
		heldBack, err = s.ListRunnerEvents(t.Context(), nil, store.ListRunnerEventsParams{
			OrgID:  org.OrgID,
			Runner: "runner-1",
		})
		assert.NilError(t, err)
		return txA.Commit()
	})
	assert.NilError(t, err)
	got, _ := teststore.WaitRunnerEvents(t, s, org, teststore.WaitRunnerEventsOptions{
		Runner: "runner-1",
		Count:  2,
	})

	// Assert - B's committed row is held back behind A's open transaction,
	// then both come back in xid order.
	assert.Assert(t, eventA.ID < eventB.ID)
	assert.Assert(t, eventA.XID < eventB.XID)
	assert.Assert(t, cmp.Len(heldBack.Items, 0))
	assert.Assert(t, !heldBack.More)
	assert.DeepEqual(t, testx.ExtractField(got, "ID"), []int64{eventA.ID, eventB.ID})
}

func TestListRunnerEvents_EmptyPage(t *testing.T) {
	t.Parallel()
	// Arrange - read the runner's only event, leaving the token at its end.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task := teststore.CreateTask(t, s, org, &teststore.TaskOptions{Runner: "runner-1"})
	event := &model.Event{
		TaskID:  task.ID,
		OrgID:   org.OrgID,
		Payload: &model.ReportPayload{Content: "a"},
	}
	assert.NilError(t, s.CreateEvent(t.Context(), nil, event))
	_, token := teststore.WaitRunnerEvents(t, s, org, teststore.WaitRunnerEventsOptions{
		Runner: "runner-1",
		Count:  1,
	})

	// Act
	page, err := s.ListRunnerEvents(t.Context(), nil, store.ListRunnerEventsParams{
		OrgID:     org.OrgID,
		Runner:    "runner-1",
		PageToken: token,
	})

	// Assert - an empty page hands back the request's token.
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(page.Items, 0))
	assert.Equal(t, page.NextToken, token)
}

func TestListRunnerEvents_Pages(t *testing.T) {
	t.Parallel()
	// Arrange - five events on one task, committed in separate transactions.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task := teststore.CreateTask(t, s, org, &teststore.TaskOptions{Runner: "runner-1"})
	var want []int64
	for i := range 5 {
		event := &model.Event{
			TaskID:  task.ID,
			OrgID:   org.OrgID,
			Payload: &model.ReportPayload{Content: fmt.Sprint(i)},
		}
		assert.NilError(t, s.CreateEvent(t.Context(), nil, event))
		want = append(want, event.ID)
	}
	teststore.WaitRunnerEvents(t, s, org, teststore.WaitRunnerEventsOptions{
		Runner: "runner-1",
		Count:  5,
	})

	// Act - follow the token two at a time until More reports the tail.
	var got []int64
	token := ""
	pages := 0
	for {
		page, err := s.ListRunnerEvents(t.Context(), nil, store.ListRunnerEventsParams{
			OrgID:     org.OrgID,
			Runner:    "runner-1",
			PageSize:  2,
			PageToken: token,
		})
		assert.NilError(t, err)
		got = append(got, testx.ExtractField(page.Items, "ID").([]int64)...)
		token = page.NextToken
		pages++
		if !page.More {
			break
		}
	}

	// Assert
	assert.Equal(t, pages, 3) // 2+2+1
	assert.DeepEqual(t, got, want)
}

func TestListRunnerEvents_ScopedToRunner(t *testing.T) {
	t.Parallel()
	// Arrange - an event on runner-1's task, on runner-2's task, and on a
	// runner-1 task in another org.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	otherOrg := teststore.CreateOrg(t, s, nil)
	mine := teststore.CreateTask(t, s, org, &teststore.TaskOptions{Runner: "runner-1"})
	otherRunner := teststore.CreateTask(t, s, org, &teststore.TaskOptions{Runner: "runner-2"})
	otherOrgs := teststore.CreateTask(t, s, otherOrg, &teststore.TaskOptions{Runner: "runner-1"})
	var want int64
	for _, task := range []*model.Task{mine, otherRunner, otherOrgs} {
		event := &model.Event{
			TaskID:  task.ID,
			OrgID:   task.OrgID,
			Payload: &model.ReportPayload{Content: "x"},
		}
		assert.NilError(t, s.CreateEvent(t.Context(), nil, event))
		if task == mine {
			want = event.ID
		}
	}
	// Wait for the last event to pass the watermark, so the others have too.
	teststore.WaitRunnerEvents(t, s, otherOrg, teststore.WaitRunnerEventsOptions{
		Runner: "runner-1",
		Count:  1,
	})

	// Act
	page, err := s.ListRunnerEvents(t.Context(), nil, store.ListRunnerEventsParams{
		OrgID:  org.OrgID,
		Runner: "runner-1",
	})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, testx.ExtractField(page.Items, "ID"), []int64{want})
}
