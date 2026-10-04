package gritzclient_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/pagination"
	"gotest.tools/v3/assert"
)

// testPageSize is the page size these tests open a TaskLog with. It matches the
// fake's default so the page boundaries in the fixtures are the real ones.
const testPageSize = 50

// logChunkSlice is a pagination.Source over an in-memory chunk slice held in
// ascending id order — the store's keyset, minus the database. The forward walk
// runs toward older rows (descending) and the backward walk toward newer ones
// (ascending), both exclusive of the cursor, exactly as the sqlc queries do.
type logChunkSlice struct {
	chunks []*gritzv1.LogChunk
}

func (s logChunkSlice) Cursor(chunk *gritzv1.LogChunk) int64 { return chunk.GetId() }

func (s logChunkSlice) Query(ctx context.Context, token pagination.Token[int64], limit int) ([]*gritzv1.LogChunk, error) {
	var out []*gritzv1.LogChunk
	if token.Backward {
		for _, chunk := range s.chunks {
			if token.Cursor != nil && chunk.GetId() <= *token.Cursor {
				continue
			}
			if out = append(out, chunk); len(out) == limit {
				break
			}
		}
		return out, nil
	}
	for i := len(s.chunks) - 1; i >= 0; i-- {
		chunk := s.chunks[i]
		if token.Cursor != nil && chunk.GetId() >= *token.Cursor {
			continue
		}
		if out = append(out, chunk); len(out) == limit {
			break
		}
	}
	return out, nil
}

// fakeLogs answers ListLogChunksByTask out of an in-memory log. It runs the real
// pagination.List with the apiserver handler's options and token mapping, so the
// iterator is exercised against production cursor semantics rather than a
// hand-rolled imitation of them — the direction of each token is the one thing
// this helper has to get right.
type fakeLogs struct {
	chunks []*gritzv1.LogChunk
}

// append adds a chunk to the end of the log, as a shipped AppendLogChunk would.
func (f *fakeLogs) append(data string) {
	f.chunks = append(f.chunks, &gritzv1.LogChunk{Id: int64(len(f.chunks) + 1), TaskId: 1, Data: []byte(data)})
}

func (f *fakeLogs) list(ctx context.Context, req *gritzv1.ListLogChunksByTaskRequest) (*gritzv1.ListLogChunksByTaskResponse, error) {
	page, err := pagination.List(ctx, pagination.Options[*gritzv1.LogChunk, int64]{
		DefaultPageSize: 50,
		MaxPageSize:     200,
		Reverse:         true,
		PageSize:        int(req.GetPageSize()),
		PageToken:       req.GetPageToken(),
		Source:          logChunkSlice{chunks: f.chunks},
	})
	if err != nil {
		return nil, err
	}
	// The store's forward walk goes toward older rows, so its NextToken is the
	// transcript's scroll-back token and its PrevToken the live-follow one.
	return &gritzv1.ListLogChunksByTaskResponse{
		Chunks:        page.Items,
		PrevPageToken: page.NextToken,
		NextPageToken: page.PrevToken,
		More:          page.More,
	}, nil
}

// newFakeLogs returns a client mock serving a log of n chunks, each holding its
// own 1-based index, along with the transcript those chunks concatenate into.
func newFakeLogs(t *testing.T, n int) (*fakeLogs, *gritzclient.ClientMock, string) {
	t.Helper()
	var (
		fake fakeLogs
		want strings.Builder
	)
	for i := 1; i <= n; i++ {
		data := fmt.Sprintf("line %d\n", i)
		fake.append(data)
		want.WriteString(data)
	}
	return &fake, &gritzclient.ClientMock{ListLogChunksByTaskFunc: fake.list}, want.String()
}

// drain writes a task log's transcript to w — history, then appends when follow
// is non-zero — the way the logs CLI composes the two iterators.
func drain(ctx context.Context, w io.Writer, log *gritzclient.TaskLog, follow time.Duration) error {
	write := func(chunks iter.Seq2[*gritzv1.LogChunk, error]) error {
		for chunk, err := range chunks {
			if err != nil {
				return err
			}
			if _, err := w.Write(chunk.GetData()); err != nil {
				return err
			}
		}
		return nil
	}
	if err := write(log.History(ctx)); err != nil {
		return err
	}
	if follow == 0 {
		return nil
	}
	return write(log.Follow(ctx, follow))
}

// pageTokens decodes the cursor of every request the mock received, reporting
// for each whether it walked backward (toward newer rows) or forward (toward
// older ones). The first request opens at the tail and has no token, so it is
// reported as the zero Token.
func pageTokens(t *testing.T, client *gritzclient.ClientMock) []pagination.Token[int64] {
	t.Helper()
	var tokens []pagination.Token[int64]
	for _, call := range client.ListLogChunksByTaskCalls() {
		raw := call.ListLogChunksByTaskRequest.GetPageToken()
		if raw == "" {
			tokens = append(tokens, pagination.Token[int64]{})
			continue
		}
		token, err := pagination.Decode[int64](raw)
		assert.NilError(t, err)
		tokens = append(tokens, token)
	}
	return tokens
}

func TestTaskLogHistory(t *testing.T) {
	// Arrange - two and a half pages of history, so the walk has to page back.
	_, client, want := newFakeLogs(t, 2*testPageSize+testPageSize/2)

	// Act
	var buf bytes.Buffer
	err := drain(t.Context(), &buf, gritzclient.OpenTaskLog(client, 1, testPageSize), 0)

	// Assert - the whole transcript, oldest-first, in one piece.
	assert.NilError(t, err)
	assert.Equal(t, buf.String(), want)
	// Three pages: a backward pass that opens at the tail and steps to the oldest
	// page, then a forward replay of the two pages newer than it. Nothing but the
	// page cursors is held between the passes.
	tokens := pageTokens(t, client)
	assert.Equal(t, len(tokens), 5)
	assert.Equal(t, tokens[0].Cursor, (*int64)(nil), "the walk opens at the tail")
	for _, token := range tokens[1:3] {
		assert.Equal(t, token.Backward, false, "the history pass must walk toward older rows")
	}
	for _, token := range tokens[3:] {
		assert.Equal(t, token.Backward, true, "the replay must walk toward newer rows")
	}
}

func TestTaskLogHistory_ExactPageBoundary(t *testing.T) {
	// Arrange - history that is an exact multiple of the page size, where a
	// shorter-than-page_size heuristic would stop a page early (or walk one too
	// far). Only `more` gets this right.
	_, client, want := newFakeLogs(t, 2*testPageSize)

	// Act
	var buf bytes.Buffer
	err := drain(t.Context(), &buf, gritzclient.OpenTaskLog(client, 1, testPageSize), 0)

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, buf.String(), want)
	assert.Equal(t, len(client.ListLogChunksByTaskCalls()), 3, "two pages back, one replayed forward")
}

func TestTaskLogHistory_Empty(t *testing.T) {
	// Arrange - a task that never shipped a chunk (or whose logs were deleted).
	_, client, _ := newFakeLogs(t, 0)

	// Act
	var buf bytes.Buffer
	err := drain(t.Context(), &buf, gritzclient.OpenTaskLog(client, 1, testPageSize), 0)

	// Assert - the tail page is the whole transcript, so nothing is replayed.
	assert.NilError(t, err)
	assert.Equal(t, buf.String(), "")
	assert.Equal(t, len(client.ListLogChunksByTaskCalls()), 1)
}

func TestTaskLogHistory_AppendDuringWalk(t *testing.T) {
	// Arrange - a chunk shipped while the backward pass is still running. The
	// pages the replay re-requests are pinned by their cursors, so the append can
	// only land in the tail page the forward walk drains to: it must be printed
	// once, at the end, with nothing skipped ahead of it.
	fake, client, want := newFakeLogs(t, 2*testPageSize+testPageSize/2)
	list := client.ListLogChunksByTaskFunc
	var calls int
	client.ListLogChunksByTaskFunc = func(ctx context.Context, req *gritzv1.ListLogChunksByTaskRequest) (*gritzv1.ListLogChunksByTaskResponse, error) {
		if calls++; calls == 2 {
			fake.append("appended\n")
		}
		return list(ctx, req)
	}

	// Act
	var buf bytes.Buffer
	err := drain(t.Context(), &buf, gritzclient.OpenTaskLog(client, 1, testPageSize), 0)

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, buf.String(), want+"appended\n")
}

func TestTaskLogHistory_Error(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		ListLogChunksByTaskFunc: func(ctx context.Context, req *gritzv1.ListLogChunksByTaskRequest) (*gritzv1.ListLogChunksByTaskResponse, error) {
			return nil, fmt.Errorf("task 1 not found")
		},
	}

	// Act
	var buf bytes.Buffer
	err := drain(t.Context(), &buf, gritzclient.OpenTaskLog(client, 1, testPageSize), 0)

	// Assert
	assert.ErrorContains(t, err, "task 1 not found")
}

func TestTaskLogFollow(t *testing.T) {
	// Arrange - history plus a chunk that lands after the first poll. The follow
	// cursor must walk toward newer rows: a token pointed the other way would
	// replay history instead of picking the appended chunk up.
	fake, client, want := newFakeLogs(t, 2)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	list := client.ListLogChunksByTaskFunc
	var calls int
	client.ListLogChunksByTaskFunc = func(ctx context.Context, req *gritzv1.ListLogChunksByTaskRequest) (*gritzv1.ListLogChunksByTaskResponse, error) {
		calls++
		switch calls {
		case 2:
			fake.append("line 3\n")
		case 4:
			// Two empty polls after the append: stop following.
			cancel()
		}
		return list(ctx, req)
	}

	// Act
	var buf bytes.Buffer
	err := drain(ctx, &buf, gritzclient.OpenTaskLog(client, 1, testPageSize), time.Millisecond)

	// Assert - the append is printed exactly once, after the history.
	assert.NilError(t, err)
	assert.Equal(t, buf.String(), want+"line 3\n")
	// The history fits in one page, so every call after the first is a poll, and
	// every poll rides the live-follow (newer) direction.
	for _, token := range pageTokens(t, client)[1:] {
		assert.Equal(t, token.Backward, true, "follow must poll toward newer rows")
	}
}

// cancelAfter cancels ctx as soon as the output it has accumulated contains
// marker. It stops a follow loop the moment its expected output has arrived,
// which keeps the poll count deterministic — a follow loop stopped on a call
// count instead would hide an extra poll.
type cancelAfter struct {
	buf    bytes.Buffer
	marker string
	cancel context.CancelFunc
}

func (c *cancelAfter) Write(p []byte) (int, error) {
	n, err := c.buf.Write(p)
	if strings.Contains(c.buf.String(), c.marker) {
		c.cancel()
	}
	return n, err
}

func TestTaskLogFollow_DrainsFullPages(t *testing.T) {
	// Arrange - a poll that finds exactly two full pages of appends. The drain
	// has to walk both and stop on !more: a final page that exactly fills
	// page_size is indistinguishable by length from a page with more behind it.
	fake, client, want := newFakeLogs(t, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	list := client.ListLogChunksByTaskFunc
	var calls int
	client.ListLogChunksByTaskFunc = func(ctx context.Context, req *gritzv1.ListLogChunksByTaskRequest) (*gritzv1.ListLogChunksByTaskResponse, error) {
		calls++
		if calls == 2 {
			for i := 1; i <= 2*testPageSize; i++ {
				data := fmt.Sprintf("append %d\n", i)
				fake.append(data)
				want += data
			}
		}
		return list(ctx, req)
	}
	w := &cancelAfter{marker: fmt.Sprintf("append %d\n", 2*testPageSize), cancel: cancel}

	// Act
	err := drain(ctx, w, gritzclient.OpenTaskLog(client, 1, testPageSize), time.Millisecond)

	// Assert - everything appended, in order, in one drain of two pages.
	assert.NilError(t, err)
	assert.Equal(t, w.buf.String(), want)
	assert.Equal(t, len(client.ListLogChunksByTaskCalls()), 3, "the drain must stop on !more, without an extra probe")
}

func TestTaskLogFollow_FromEmpty(t *testing.T) {
	// Arrange - following a task before it has shipped anything: the tail page
	// yields no follow cursor, so the tail must be re-opened until it does.
	fake, client, _ := newFakeLogs(t, 0)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	list := client.ListLogChunksByTaskFunc
	var calls int
	client.ListLogChunksByTaskFunc = func(ctx context.Context, req *gritzv1.ListLogChunksByTaskRequest) (*gritzv1.ListLogChunksByTaskResponse, error) {
		calls++
		switch calls {
		case 3:
			fake.append("first line\n")
		case 5:
			cancel()
		}
		return list(ctx, req)
	}

	// Act
	var buf bytes.Buffer
	err := drain(ctx, &buf, gritzclient.OpenTaskLog(client, 1, testPageSize), time.Millisecond)

	// Assert - the first chunk is printed once, and not replayed by the polls
	// that follow it.
	assert.NilError(t, err)
	assert.Equal(t, buf.String(), "first line\n")
}

func TestListLogChunksByTask(t *testing.T) {
	// Arrange - a page and a half of appends landing past a task's tail, which is
	// the shape a live-follow poll walks.
	fake, client, _ := newFakeLogs(t, 1)
	tail, err := fake.list(t.Context(), &gritzv1.ListLogChunksByTaskRequest{TaskId: 1, PageSize: testPageSize})
	assert.NilError(t, err)
	var want strings.Builder
	for i := 2; i <= testPageSize+2; i++ {
		data := fmt.Sprintf("line %d\n", i)
		fake.append(data)
		want.WriteString(data)
	}

	// Act - walk from the tail cursor toward newer rows.
	var (
		buf   bytes.Buffer
		pages int
		token = tail.GetNextPageToken()
	)
	req := &gritzv1.ListLogChunksByTaskRequest{TaskId: 1, PageSize: testPageSize, PageToken: token}
	for resp, err := range gritzclient.ListLogChunksByTask(t.Context(), client, req) {
		assert.NilError(t, err)
		pages++
		for _, chunk := range resp.GetChunks() {
			buf.Write(chunk.GetData())
		}
	}

	// Assert - both pages, in order, and the caller's request is left untouched.
	assert.Equal(t, buf.String(), want.String())
	assert.Equal(t, pages, 2)
	assert.Equal(t, req.GetPageToken(), token, "the caller's request must not be mutated")
}

func TestListLogChunksByTask_Error(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		ListLogChunksByTaskFunc: func(ctx context.Context, req *gritzv1.ListLogChunksByTaskRequest) (*gritzv1.ListLogChunksByTaskResponse, error) {
			return nil, fmt.Errorf("task 1 not found")
		},
	}

	// Act
	var pages int
	var last error
	for _, err := range gritzclient.ListLogChunksByTask(t.Context(), client, &gritzv1.ListLogChunksByTaskRequest{TaskId: 1}) {
		pages++
		last = err
	}

	// Assert - the error is yielded once and the walk stops.
	assert.Equal(t, pages, 1)
	assert.ErrorContains(t, last, "task 1 not found")
}
