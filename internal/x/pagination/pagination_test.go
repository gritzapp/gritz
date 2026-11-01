package pagination_test

import (
	"encoding/base64"
	"errors"
	"testing"

	"github.com/gritzapp/gritz/internal/x/pagination"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// Keys used across these tests are ascending ints, each row its own cursor.
// Sources are stateless, so a test models appended rows by resuming a token
// against a fresh source holding more rows.

// TestList walks a list to exhaustion: every row once, in walk order, with
// NextToken emptying on the last page.
func TestList(t *testing.T) {
	t.Parallel()
	src := pagination.NewMockCursorSource([]int{1, 2, 3, 4, 5, 6, 7})

	var got [][]int
	var token string
	for {
		page, err := pagination.List(t.Context(), src, pagination.Options{
			DefaultPageSize: 3, MaxPageSize: 100, PageToken: token,
		})
		assert.NilError(t, err)
		got = append(got, page.Items)
		if page.NextToken == "" {
			break
		}
		token = page.NextToken
	}

	assert.DeepEqual(t, got, [][]int{{1, 2, 3}, {4, 5, 6}, {7}})
	assert.DeepEqual(t, src.QueryCalls()[0].After, (*int)(nil))
	assert.Equal(t, src.QueryCalls()[0].Limit, 4) // over-fetch is size+1
}

// TestList_ExactPageBoundary covers a final page that exactly fills page_size:
// the over-fetch finds no extra row, so NextToken empties without a probe.
func TestList_ExactPageBoundary(t *testing.T) {
	t.Parallel()
	src := pagination.NewMockCursorSource([]int{1, 2, 3, 4, 5, 6})
	first, err := pagination.List(t.Context(), src, pagination.Options{
		DefaultPageSize: 3, MaxPageSize: 100,
	})
	assert.NilError(t, err)

	second, err := pagination.List(t.Context(), src, pagination.Options{
		DefaultPageSize: 3, MaxPageSize: 100, PageToken: first.NextToken,
	})

	assert.NilError(t, err)
	assert.DeepEqual(t, second, &pagination.ListPage[int]{Items: []int{4, 5, 6}})
	assert.Assert(t, cmp.Len(src.QueryCalls(), 2))
}

// TestList_Empty covers a list with no rows: no items and no token.
func TestList_Empty(t *testing.T) {
	t.Parallel()
	src := pagination.NewMockCursorSource(nil)

	page, err := pagination.List(t.Context(), src, pagination.Options{
		DefaultPageSize: 3, MaxPageSize: 100,
	})

	assert.NilError(t, err)
	assert.DeepEqual(t, page, &pagination.ListPage[int]{})
}

// TestHistory_NewestPage covers the open: an empty token reads the newest page
// in one Older query and returns it ascending, with both tokens set.
func TestHistory_NewestPage(t *testing.T) {
	t.Parallel()
	src := pagination.NewMockHistorySource([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})

	page, err := pagination.History(t.Context(), src, pagination.Options{
		DefaultPageSize: 3, MaxPageSize: 100,
	})

	assert.NilError(t, err)
	assert.DeepEqual(t, page.Items, []int{8, 9, 10})
	assert.Assert(t, page.OlderToken != "")
	assert.Assert(t, page.FollowToken != "")
	assert.Assert(t, page.More)
	calls := src.OlderCalls()
	assert.Assert(t, cmp.Len(calls, 1))
	assert.DeepEqual(t, calls[0].Before, (*int)(nil))
	assert.Equal(t, calls[0].Limit, 4)
	assert.Assert(t, cmp.Len(src.NewerCalls(), 0))
}

// TestHistory_ScrollBack walks OlderToken to the start of history: every page
// ascending, and OlderToken and More both falling off on the oldest page.
func TestHistory_ScrollBack(t *testing.T) {
	t.Parallel()
	src := pagination.NewMockHistorySource([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})

	var got [][]int
	var token string
	for {
		page, err := pagination.History(t.Context(), src, pagination.Options{
			DefaultPageSize: 3, MaxPageSize: 100, PageToken: token,
		})
		assert.NilError(t, err)
		got = append(got, page.Items)
		assert.Equal(t, page.More, page.OlderToken != "")
		if page.OlderToken == "" {
			break
		}
		token = page.OlderToken
	}

	assert.DeepEqual(t, got, [][]int{{8, 9, 10}, {5, 6, 7}, {2, 3, 4}, {1}})
}

// TestHistory_ExactPageBoundary covers an oldest page that exactly fills
// page_size: OlderToken empties on it, without an extra empty page.
func TestHistory_ExactPageBoundary(t *testing.T) {
	t.Parallel()
	src := pagination.NewMockHistorySource([]int{1, 2, 3, 4, 5, 6})
	newest, err := pagination.History(t.Context(), src, pagination.Options{
		DefaultPageSize: 3, MaxPageSize: 100,
	})
	assert.NilError(t, err)

	oldest, err := pagination.History(t.Context(), src, pagination.Options{
		DefaultPageSize: 3, MaxPageSize: 100, PageToken: newest.OlderToken,
	})

	assert.NilError(t, err)
	assert.DeepEqual(t, oldest.Items, []int{1, 2, 3})
	assert.Equal(t, oldest.OlderToken, "")
	assert.Assert(t, !oldest.More)
}

// TestHistory_Follow follows the rows appended after the newest page: pages
// walk Newer, More marks the tail, and a follow page's OlderToken leads back
// to the rows before it.
func TestHistory_Follow(t *testing.T) {
	t.Parallel()
	newest, err := pagination.History(t.Context(), pagination.NewMockHistorySource([]int{1, 2, 3, 4, 5, 6}), pagination.Options{
		DefaultPageSize: 3, MaxPageSize: 100,
	})
	assert.NilError(t, err)
	src := pagination.NewMockHistorySource([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})

	first, err := pagination.History(t.Context(), src, pagination.Options{
		DefaultPageSize: 3, MaxPageSize: 100, PageToken: newest.FollowToken,
	})
	assert.NilError(t, err)
	second, err := pagination.History(t.Context(), src, pagination.Options{
		DefaultPageSize: 3, MaxPageSize: 100, PageToken: first.FollowToken,
	})
	assert.NilError(t, err)
	back, err := pagination.History(t.Context(), src, pagination.Options{
		DefaultPageSize: 3, MaxPageSize: 100, PageToken: first.OlderToken,
	})
	assert.NilError(t, err)

	assert.DeepEqual(t, first.Items, []int{7, 8, 9})
	assert.Assert(t, first.More)
	assert.DeepEqual(t, second.Items, []int{10})
	assert.Assert(t, !second.More)
	assert.Assert(t, second.FollowToken != "")
	assert.DeepEqual(t, back.Items, []int{4, 5, 6})
}

// TestHistory_FollowCaughtUp covers a follow poll with nothing new: an empty
// page that echoes the request token, so the caller keeps its place.
func TestHistory_FollowCaughtUp(t *testing.T) {
	t.Parallel()
	src := pagination.NewMockHistorySource([]int{1, 2, 3, 4, 5, 6})
	newest, err := pagination.History(t.Context(), src, pagination.Options{
		DefaultPageSize: 3, MaxPageSize: 100,
	})
	assert.NilError(t, err)

	page, err := pagination.History(t.Context(), src, pagination.Options{
		DefaultPageSize: 3, MaxPageSize: 100, PageToken: newest.FollowToken,
	})

	assert.NilError(t, err)
	assert.DeepEqual(t, page, &pagination.HistoryPage[int]{FollowToken: newest.FollowToken})
}

// TestHistory_EmptyFollowsFromStart covers opening a history with no rows yet:
// the empty page still carries a FollowToken, which follows from the start of
// history once rows arrive.
func TestHistory_EmptyFollowsFromStart(t *testing.T) {
	t.Parallel()
	empty, err := pagination.History(t.Context(), pagination.NewMockHistorySource(nil), pagination.Options{
		DefaultPageSize: 3, MaxPageSize: 100,
	})
	assert.NilError(t, err)
	src := pagination.NewMockHistorySource([]int{1, 2})

	page, err := pagination.History(t.Context(), src, pagination.Options{
		DefaultPageSize: 3, MaxPageSize: 100, PageToken: empty.FollowToken,
	})

	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(empty.Items, 0))
	assert.Equal(t, empty.OlderToken, "")
	assert.Assert(t, empty.FollowToken != "")
	assert.DeepEqual(t, page.Items, []int{1, 2})
	assert.Equal(t, page.OlderToken, "", "a follow from the start has no older rows")
	calls := src.NewerCalls()
	assert.Assert(t, cmp.Len(calls, 1))
	assert.DeepEqual(t, calls[0].After, (*int)(nil))
}

// TestStream reads a stream from its start to the tail, where More falls off,
// then picks up the rows appended later from the last NextToken.
func TestStream(t *testing.T) {
	t.Parallel()
	src := pagination.NewMockCursorSource([]int{1, 2, 3, 4, 5, 6, 7})

	var got [][]int
	var token string
	for {
		page, err := pagination.Stream(t.Context(), src, pagination.Options{
			DefaultPageSize: 3, MaxPageSize: 100, PageToken: token,
		})
		assert.NilError(t, err)
		got = append(got, page.Items)
		token = page.NextToken
		if !page.More {
			break
		}
	}
	appended, err := pagination.Stream(t.Context(), pagination.NewMockCursorSource([]int{1, 2, 3, 4, 5, 6, 7, 8}), pagination.Options{
		DefaultPageSize: 3, MaxPageSize: 100, PageToken: token,
	})
	assert.NilError(t, err)

	assert.DeepEqual(t, got, [][]int{{1, 2, 3}, {4, 5, 6}, {7}})
	assert.DeepEqual(t, src.QueryCalls()[0].After, (*int)(nil))
	assert.DeepEqual(t, appended.Items, []int{8})
}

// TestStream_ExactPageBoundary covers a tail page that exactly fills page_size:
// More is false on it even though NextToken stays set.
func TestStream_ExactPageBoundary(t *testing.T) {
	t.Parallel()
	src := pagination.NewMockCursorSource([]int{1, 2, 3, 4, 5, 6})
	first, err := pagination.Stream(t.Context(), src, pagination.Options{
		DefaultPageSize: 3, MaxPageSize: 100,
	})
	assert.NilError(t, err)

	second, err := pagination.Stream(t.Context(), src, pagination.Options{
		DefaultPageSize: 3, MaxPageSize: 100, PageToken: first.NextToken,
	})

	assert.NilError(t, err)
	assert.DeepEqual(t, second.Items, []int{4, 5, 6})
	assert.Assert(t, !second.More)
	assert.Assert(t, second.NextToken != "")
}

// TestStream_CaughtUp covers a poll at the tail: an empty page whose NextToken
// echoes the request token.
func TestStream_CaughtUp(t *testing.T) {
	t.Parallel()
	src := pagination.NewMockCursorSource([]int{1, 2})
	first, err := pagination.Stream(t.Context(), src, pagination.Options{
		DefaultPageSize: 3, MaxPageSize: 100,
	})
	assert.NilError(t, err)

	page, err := pagination.Stream(t.Context(), src, pagination.Options{
		DefaultPageSize: 3, MaxPageSize: 100, PageToken: first.NextToken,
	})

	assert.NilError(t, err)
	assert.DeepEqual(t, page, &pagination.StreamPage[int]{NextToken: first.NextToken})
}

// TestStream_EmptyStart covers a stream with no rows yet: the empty first page
// still carries a NextToken, which reads from the start once rows arrive.
func TestStream_EmptyStart(t *testing.T) {
	t.Parallel()
	empty, err := pagination.Stream(t.Context(), pagination.NewMockCursorSource(nil), pagination.Options{
		DefaultPageSize: 3, MaxPageSize: 100,
	})
	assert.NilError(t, err)

	page, err := pagination.Stream(t.Context(), pagination.NewMockCursorSource([]int{1, 2}), pagination.Options{
		DefaultPageSize: 3, MaxPageSize: 100, PageToken: empty.NextToken,
	})

	assert.NilError(t, err)
	assert.Assert(t, empty.NextToken != "")
	assert.DeepEqual(t, page.Items, []int{1, 2})
}

// TestInvalidRequest covers the shared ErrInvalidRequest contract: out-of-range
// page sizes and undecodable tokens (bad base64, or base64 that isn't JSON).
func TestInvalidRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		pageSize int
		token    string
	}{
		{"negative size", -1, ""},
		{"above max size", 101, ""},
		{"token not base64", 10, "!!!not base64!!!"},
		{"token not json", 10, base64.URLEncoding.EncodeToString([]byte("not json"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opt := pagination.Options{DefaultPageSize: 50, MaxPageSize: 100, PageSize: tt.pageSize, PageToken: tt.token}

			_, listErr := pagination.List(t.Context(), pagination.NewMockCursorSource(nil), opt)
			_, historyErr := pagination.History(t.Context(), pagination.NewMockHistorySource(nil), opt)
			_, streamErr := pagination.Stream(t.Context(), pagination.NewMockCursorSource(nil), opt)

			assert.Assert(t, errors.Is(listErr, pagination.ErrInvalidRequest))
			assert.Assert(t, errors.Is(historyErr, pagination.ErrInvalidRequest))
			assert.Assert(t, errors.Is(streamErr, pagination.ErrInvalidRequest))
		})
	}
}
