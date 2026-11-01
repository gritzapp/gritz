package store_test

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"github.com/gritzapp/gritz/internal/x/pagination"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
	"gotest.tools/v3/poll"
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

	// Act - open at the tail (empty token), then follow NextToken toward older
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
		if page.NextToken == "" {
			break
		}
		token = page.NextToken
	}

	// Assert - the forward walk covers the whole stream ascending, no gaps/dups.
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

	// Act/Assert - walk NextToken toward older history. More stays true while an
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
		// More agrees with NextToken on this forward walk: both mark older history.
		assert.Equal(t, page.More, page.NextToken != "")
		wantMore = append(wantMore, page.More)
		if page.NextToken == "" {
			break
		}
		token = page.NextToken
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

	// The newest page carries a PrevToken (the live-follow cursor at the tail).
	page, err := s.ListEventsByTaskPage(t.Context(), nil, store.ListEventsByTaskPageParams{
		TaskID:   task.ID,
		OrgID:    org.OrgID,
		PageSize: 50,
	})
	assert.NilError(t, err)
	assert.Assert(t, page.PrevToken != "")
	follow := page.PrevToken

	// Following the tail cursor now yields nothing new (echoes the cursor).
	empty, err := s.ListEventsByTaskPage(t.Context(), nil, store.ListEventsByTaskPageParams{
		TaskID:    task.ID,
		OrgID:     org.OrgID,
		PageSize:  50,
		PageToken: follow,
	})
	assert.NilError(t, err)
	assert.Equal(t, len(empty.Items), 0)
	assert.Assert(t, empty.PrevToken != "") // still resumable
	assert.Assert(t, !empty.More)           // nothing newer: the tail is reached

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

// listRunnerEventsN pages the runner's events after cursor until n have been
// returned. The xid watermark is cluster-wide, so a transaction held open by a
// concurrent test can briefly hold back rows that have already committed.
func listRunnerEventsN(t *testing.T, s *store.Store, org *teststore.Org, runner string, after store.EventCursor, n int) ([]*model.Event, store.EventCursor) {
	t.Helper()
	var got []*model.Event
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		events, next, err := s.ListRunnerEvents(t.Context(), nil, org.OrgID, runner, after, 100)
		if err != nil {
			return poll.Error(err)
		}
		got = append(got, events...)
		after = next
		if len(got) < n {
			return poll.Continue("got %d of %d events", len(got), n)
		}
		return poll.Success()
	}, poll.WithTimeout(10*time.Second), poll.WithDelay(20*time.Millisecond))
	return got, after
}

func TestListRunnerEvents_CommitOutOfIDOrder(t *testing.T) {
	t.Parallel()
	// Arrange - two tasks on the runner, each written by its own transaction.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	taskA := teststore.CreateTask(t, s, org, &teststore.TaskOptions{Runner: "runner-1"})
	taskB := teststore.CreateTask(t, s, org, &teststore.TaskOptions{Runner: "runner-1"})
	eventA := &model.Event{TaskID: taskA.ID, OrgID: org.OrgID, Payload: &model.ReportPayload{Content: "a"}}
	eventB := &model.Event{TaskID: taskB.ID, OrgID: org.OrgID, Payload: &model.ReportPayload{Content: "b"}}
	var heldBack []*model.Event
	var heldBackCursor store.EventCursor

	// Act - Tx A inserts first (lower id), Tx B inserts second and commits
	// first; read while A is still open, then again once A commits.
	err := s.WithTx(t.Context(), nil, func(txA *sql.Tx) error {
		assert.NilError(t, s.CreateEvent(t.Context(), txA, eventA))
		err := s.WithTx(t.Context(), nil, func(txB *sql.Tx) error {
			assert.NilError(t, s.CreateEvent(t.Context(), txB, eventB))
			return txB.Commit()
		})
		assert.NilError(t, err)
		heldBack, heldBackCursor, err = s.ListRunnerEvents(t.Context(), nil, org.OrgID, "runner-1", store.EventCursor{}, 100)
		assert.NilError(t, err)
		return txA.Commit()
	})
	assert.NilError(t, err)
	got, cursor := listRunnerEventsN(t, s, org, "runner-1", store.EventCursor{}, 2)

	// Assert - B's committed row is held back behind A's open transaction,
	// then both come back in xid order and the cursor names the last one.
	assert.Assert(t, eventA.ID < eventB.ID)
	assert.Assert(t, eventA.XID < eventB.XID)
	assert.Assert(t, cmp.Len(heldBack, 0))
	assert.Equal(t, heldBackCursor, store.EventCursor{})
	assert.DeepEqual(t, testx.ExtractField(got, "ID"), []int64{eventA.ID, eventB.ID})
	assert.Equal(t, cursor, store.EventCursor{XID: eventB.XID, ID: eventB.ID})
}

func TestListRunnerEvents_EmptyPage(t *testing.T) {
	t.Parallel()
	// Arrange - read the runner's only event, leaving the cursor at its end.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task := teststore.CreateTask(t, s, org, &teststore.TaskOptions{Runner: "runner-1"})
	event := &model.Event{TaskID: task.ID, OrgID: org.OrgID, Payload: &model.ReportPayload{Content: "a"}}
	assert.NilError(t, s.CreateEvent(t.Context(), nil, event))
	_, after := listRunnerEventsN(t, s, org, "runner-1", store.EventCursor{}, 1)

	// Act
	events, next, err := s.ListRunnerEvents(t.Context(), nil, org.OrgID, "runner-1", after, 100)

	// Assert - an empty page hands back the request's cursor.
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(events, 0))
	assert.Equal(t, after, store.EventCursor{XID: event.XID, ID: event.ID})
	assert.Equal(t, next, after)
}

func TestListRunnerEvents_Pages(t *testing.T) {
	t.Parallel()
	// Arrange - five events on one task, committed in separate transactions.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task := teststore.CreateTask(t, s, org, &teststore.TaskOptions{Runner: "runner-1"})
	var want []int64
	for i := range 5 {
		event := &model.Event{TaskID: task.ID, OrgID: org.OrgID, Payload: &model.ReportPayload{Content: fmt.Sprint(i)}}
		assert.NilError(t, s.CreateEvent(t.Context(), nil, event))
		want = append(want, event.ID)
	}
	listRunnerEventsN(t, s, org, "runner-1", store.EventCursor{}, 5)

	// Act - follow the cursor two at a time until a page comes back empty.
	var got []int64
	var after store.EventCursor
	pages := 0
	for {
		events, next, err := s.ListRunnerEvents(t.Context(), nil, org.OrgID, "runner-1", after, 2)
		assert.NilError(t, err)
		if len(events) == 0 {
			break
		}
		got = append(got, testx.ExtractField(events, "ID").([]int64)...)
		after = next
		pages++
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
		event := &model.Event{TaskID: task.ID, OrgID: task.OrgID, Payload: &model.ReportPayload{Content: "x"}}
		assert.NilError(t, s.CreateEvent(t.Context(), nil, event))
		if task == mine {
			want = event.ID
		}
	}
	// Wait for the last event to pass the watermark, so the others have too.
	listRunnerEventsN(t, s, otherOrg, "runner-1", store.EventCursor{}, 1)

	// Act
	events, _, err := s.ListRunnerEvents(t.Context(), nil, org.OrgID, "runner-1", store.EventCursor{}, 100)

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, testx.ExtractField(events, "ID"), []int64{want})
}
