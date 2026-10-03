package store_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/icholy/gritz/internal/model"
	"github.com/icholy/gritz/internal/store"
	"github.com/icholy/gritz/internal/store/teststore"
	"github.com/icholy/gritz/internal/x/pagination"
	"github.com/icholy/gritz/internal/x/testx"
	"gotest.tools/v3/assert"
)

// createLogChunks ships chunks one at a time — the way the driver's single
// in-flight sender does — and returns them with their assigned ids.
func createLogChunks(t *testing.T, s *store.Store, org *teststore.Org, task *model.Task, datas ...string) []*model.LogChunk {
	t.Helper()
	chunks := make([]*model.LogChunk, len(datas))
	for i, data := range datas {
		chunks[i] = &model.LogChunk{
			OrgID:   org.OrgID,
			TaskID:  task.ID,
			Version: task.Version,
			Data:    []byte(data),
		}
		assert.NilError(t, s.CreateLogChunk(t.Context(), nil, chunks[i]))
	}
	return chunks
}

// readTranscript walks the whole log oldest-first (open at the tail, prepend
// older pages) and returns the concatenated bytes.
func readTranscript(t *testing.T, s *store.Store, org *teststore.Org, task *model.Task, pageSize int32) string {
	t.Helper()
	var out []byte
	token := ""
	for {
		page, err := s.ListLogChunksByTaskPage(t.Context(), nil, store.ListLogChunksByTaskPageParams{
			TaskID:    task.ID,
			OrgID:     org.OrgID,
			PageSize:  pageSize,
			PageToken: token,
		})
		assert.NilError(t, err)
		var older []byte
		for _, chunk := range page.Items {
			older = append(older, chunk.Data...)
		}
		out = append(older, out...)
		if page.NextToken == "" {
			return string(out)
		}
		token = page.NextToken
	}
}

func TestCreateLogChunk(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task := teststore.CreateTask(t, s, org, nil)

	// Act - three chunks, inserted in write order.
	chunks := createLogChunks(t, s, org, task, "alpha", "beta", "gamma")

	// Assert - ids are assigned in write order, so reading by id rebuilds the
	// byte stream exactly as it was written.
	assert.Assert(t, chunks[0].ID < chunks[1].ID)
	assert.Assert(t, chunks[1].ID < chunks[2].ID)
	assert.Equal(t, readTranscript(t, s, org, task, 50), "alphabetagamma")
}

func TestCreateLogChunk_Order(t *testing.T) {
	t.Parallel()
	// Arrange - enough chunks that a read which lost insert order would show up.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task := teststore.CreateTask(t, s, org, nil)
	var want bytes.Buffer
	for i := range 50 {
		data := fmt.Sprintf("[%d]", i)
		want.WriteString(data)
		assert.NilError(t, s.CreateLogChunk(t.Context(), nil, &model.LogChunk{
			OrgID:  org.OrgID,
			TaskID: task.ID,
			Data:   []byte(data),
		}))
	}

	// Act - 50 chunks over a page size of 7, so page boundaries fall between
	// inserts rather than lining up with them.
	got := readTranscript(t, s, org, task, 7)

	// Assert - the transcript is byte-identical to the write order.
	assert.Equal(t, got, want.String())
}

func TestCreateLogChunk_Version(t *testing.T) {
	t.Parallel()
	// Arrange - a pre-run preamble (version 0) followed by a run's bytes.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task := teststore.CreateTask(t, s, org, nil)
	assert.NilError(t, s.CreateLogChunk(t.Context(), nil, &model.LogChunk{
		OrgID: org.OrgID, TaskID: task.ID, Version: 0, Data: []byte("preamble"),
	}))
	assert.NilError(t, s.CreateLogChunk(t.Context(), nil, &model.LogChunk{
		OrgID: org.OrgID, TaskID: task.ID, Version: 3, Data: []byte("run"),
	}))

	// Act
	page, err := s.ListLogChunksByTaskPage(t.Context(), nil, store.ListLogChunksByTaskPageParams{
		TaskID:   task.ID,
		OrgID:    org.OrgID,
		PageSize: 50,
	})

	// Assert - the version each chunk was shipped under round-trips.
	assert.NilError(t, err)
	assert.DeepEqual(t, testx.ExtractField(page.Items, "Version"), []int64{0, 3})
}

func TestListLogChunksByTaskPage_OrgScoped(t *testing.T) {
	t.Parallel()
	// Arrange - two orgs, each with a task carrying log chunks.
	s := teststore.New(t)
	orgA := teststore.CreateOrg(t, s, nil)
	taskA := teststore.CreateTask(t, s, orgA, nil)
	createLogChunks(t, s, orgA, taskA, "org A bytes")
	orgB := teststore.CreateOrg(t, s, nil)
	taskB := teststore.CreateTask(t, s, orgB, nil)
	createLogChunks(t, s, orgB, taskB, "org B bytes")

	// Act - org B reads org A's task id.
	page, err := s.ListLogChunksByTaskPage(t.Context(), nil, store.ListLogChunksByTaskPageParams{
		TaskID:   taskA.ID,
		OrgID:    orgB.OrgID,
		PageSize: 50,
	})

	// Assert - nothing crosses the org boundary, in either direction.
	assert.NilError(t, err)
	assert.Equal(t, len(page.Items), 0)
	assert.Equal(t, readTranscript(t, s, orgA, taskA, 50), "org A bytes")
	assert.Equal(t, readTranscript(t, s, orgB, taskB, 50), "org B bytes")
}

func TestListLogChunksByTaskPage_TaskScoped(t *testing.T) {
	t.Parallel()
	// Arrange - two tasks in the same org.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task1 := teststore.CreateTask(t, s, org, nil)
	task2 := teststore.CreateTask(t, s, org, nil)
	createLogChunks(t, s, org, task1, "one")
	createLogChunks(t, s, org, task2, "two")

	// Act/Assert - each task sees only its own bytes.
	assert.Equal(t, readTranscript(t, s, org, task1, 50), "one")
	assert.Equal(t, readTranscript(t, s, org, task2, 50), "two")
}

func TestListLogChunksByTaskPage_CascadeDelete(t *testing.T) {
	t.Parallel()
	// Arrange - two tasks with chunks; only one is deleted.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	doomed := teststore.CreateTask(t, s, org, nil)
	kept := teststore.CreateTask(t, s, org, nil)
	createLogChunks(t, s, org, doomed, "doomed")
	createLogChunks(t, s, org, kept, "kept")
	assert.Equal(t, readTranscript(t, s, org, doomed, 50), "doomed")

	// Act - deleting the task takes its chunks with it (ON DELETE CASCADE);
	// there is no other retention in v1.
	assert.NilError(t, s.DeleteTask(t.Context(), nil, doomed.ID, org.OrgID))

	// Assert
	assert.Equal(t, readTranscript(t, s, org, doomed, 50), "")
	assert.Equal(t, readTranscript(t, s, org, kept, 50), "kept")
}

func TestListLogChunksByTaskPage(t *testing.T) {
	t.Parallel()
	// Arrange - 10 chunks, recording their ids in write order.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task := teststore.CreateTask(t, s, org, nil)
	var datas []string
	for i := range 10 {
		datas = append(datas, fmt.Sprintf("chunk %d", i+1))
	}
	chunks := createLogChunks(t, s, org, task, datas...)
	want := testx.ExtractField(chunks, "ID").([]int64)

	// Act - open at the tail (empty token), then follow NextToken toward older
	// history, prepending each older page so the whole log reassembles
	// oldest-first.
	var got []int64
	token := ""
	pages := 0
	for {
		page, err := s.ListLogChunksByTaskPage(t.Context(), nil, store.ListLogChunksByTaskPageParams{
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

	// Assert - the backward walk covers the whole log ascending, no gaps/dups.
	assert.Equal(t, pages, 4) // 3+3+3+1
	assert.DeepEqual(t, got, want)
}

func TestListLogChunksByTaskPage_LiveFollow(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task := teststore.CreateTask(t, s, org, nil)
	createLogChunks(t, s, org, task, "a", "b", "c")

	// The newest page carries a PrevToken (the live-follow cursor at the tail).
	page, err := s.ListLogChunksByTaskPage(t.Context(), nil, store.ListLogChunksByTaskPageParams{
		TaskID:   task.ID,
		OrgID:    org.OrgID,
		PageSize: 50,
	})
	assert.NilError(t, err)
	assert.Assert(t, page.PrevToken != "")
	follow := page.PrevToken

	// Following the tail cursor now yields nothing new (echoes the cursor).
	empty, err := s.ListLogChunksByTaskPage(t.Context(), nil, store.ListLogChunksByTaskPageParams{
		TaskID:    task.ID,
		OrgID:     org.OrgID,
		PageSize:  50,
		PageToken: follow,
	})
	assert.NilError(t, err)
	assert.Equal(t, len(empty.Items), 0)
	assert.Assert(t, empty.PrevToken != "") // still resumable
	assert.Assert(t, !empty.More)           // nothing newer: the tail is reached

	// Act - a subsequently-shipped chunk is picked up by the same tail token.
	shipped := createLogChunks(t, s, org, task, "d")
	page, err = s.ListLogChunksByTaskPage(t.Context(), nil, store.ListLogChunksByTaskPageParams{
		TaskID:    task.ID,
		OrgID:     org.OrgID,
		PageSize:  50,
		PageToken: follow,
	})
	assert.NilError(t, err)

	// Assert - the one new chunk, and no further page beyond it.
	assert.DeepEqual(t, testx.ExtractField(page.Items, "ID"), []int64{shipped[0].ID})
	assert.Equal(t, string(page.Items[0].Data), "d")
	assert.Assert(t, !page.More)

	// Ship two more so more than a (small) page is newer than the tail cursor,
	// then follow with page size 2: More is true while newer chunks remain.
	createLogChunks(t, s, org, task, "e", "f")
	backlog, err := s.ListLogChunksByTaskPage(t.Context(), nil, store.ListLogChunksByTaskPageParams{
		TaskID:    task.ID,
		OrgID:     org.OrgID,
		PageSize:  2,
		PageToken: follow,
	})
	assert.NilError(t, err)
	assert.Equal(t, len(backlog.Items), 2)
	assert.Assert(t, backlog.More) // a third newer chunk remains beyond this page
}

func TestListLogChunksByTaskPage_BadPageSize(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task := teststore.CreateTask(t, s, org, nil)

	// Act - a page size past the max is rejected.
	_, err := s.ListLogChunksByTaskPage(t.Context(), nil, store.ListLogChunksByTaskPageParams{
		TaskID:   task.ID,
		OrgID:    org.OrgID,
		PageSize: 201,
	})

	// Assert
	assert.Assert(t, errors.Is(err, pagination.ErrInvalidRequest))
}

func TestListLogChunksByTaskPage_BadToken(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	task := teststore.CreateTask(t, s, org, nil)

	// Act - an undecodable token is rejected.
	_, err := s.ListLogChunksByTaskPage(t.Context(), nil, store.ListLogChunksByTaskPageParams{
		TaskID:    task.ID,
		OrgID:     org.OrgID,
		PageSize:  50,
		PageToken: "!!!not-base64!!!",
	})

	// Assert
	assert.Assert(t, errors.Is(err, pagination.ErrInvalidRequest))
}
