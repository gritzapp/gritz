package store_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"github.com/gritzapp/gritz/internal/x/pagination"
	"github.com/gritzapp/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
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
