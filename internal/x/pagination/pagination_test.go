package pagination_test

import (
	"encoding/base64"
	"errors"
	"testing"

	"github.com/gritzapp/gritz/internal/x/pagination"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// Keys used across these tests run 1..10, oldest (1) to newest (10). The
// forward walk is newest-first (descending); the backward walk is the
// ascending live-follow reverse.

// TestList_NewestPage covers the open motion: an empty token returns the newest
// page (forward walk from the tail) in one query, with a next-older token and an
// always-populated live-follow token.
func TestList_NewestPage(t *testing.T) {
	t.Parallel()
	src := pagination.NewMockSource([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, false)

	page, err := pagination.List(t.Context(), pagination.Options[int, int]{
		DefaultPageSize: 3, MaxPageSize: 100, PageSize: 3, Source: src,
	})
	assert.NilError(t, err)

	// Reverse is off, so items read newest-first, exactly like the task list.
	assert.DeepEqual(t, page.Items, []int{10, 9, 8})
	assert.Assert(t, page.NextToken != "", "older history remains")
	assert.Assert(t, page.PrevToken != "", "live-follow token is always set")
	// One query, no probe of the opposite walk.
	assert.Assert(t, cmp.Len(src.QueryCalls(), 1))
	assert.Equal(t, src.QueryCalls()[0].Token.Backward, false)
	assert.Equal(t, src.QueryCalls()[0].Limit, 4) // over-fetch is size+1
}

// TestList_ForwardScrollToExhaustion walks the NextToken (older) direction to
// the oldest row and asserts the concatenated pages reproduce every row once,
// newest-first, and that NextToken empties at history's end.
func TestList_ForwardScrollToExhaustion(t *testing.T) {
	t.Parallel()
	src := pagination.NewMockSource([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, false)

	var got []int
	token := ""
	pages := 0
	for {
		page, err := pagination.List(t.Context(), pagination.Options[int, int]{
			DefaultPageSize: 3, MaxPageSize: 100, PageSize: 3, PageToken: token, Source: src,
		})
		assert.NilError(t, err)
		got = append(got, page.Items...)
		// Every non-empty page keeps a live-follow token.
		assert.Assert(t, page.PrevToken != "")
		pages++
		if page.NextToken == "" {
			break
		}
		token = page.NextToken
	}

	assert.DeepEqual(t, got, []int{10, 9, 8, 7, 6, 5, 4, 3, 2, 1})
	assert.Equal(t, pages, 4) // 3 full pages of 3, then a final page of 1
}

// TestList_BackwardFollow covers live-follow: resuming a PrevToken walks
// toward newer rows, and the PrevToken is always populated so polling can
// continue past the tail.
func TestList_BackwardFollow(t *testing.T) {
	t.Parallel()
	src := pagination.NewMockSource([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, false)
	opt := pagination.Options[int, int]{DefaultPageSize: 3, MaxPageSize: 100, PageSize: 3, Source: src}

	// Grab the newest page, then follow forward-in-time from an older position:
	// take the oldest page's live-follow token to walk newer.
	oldest, err := pagination.List(t.Context(), opt)
	assert.NilError(t, err)
	// Walk to the oldest page to obtain a follow token with rows above it.
	page := oldest
	for page.NextToken != "" {
		opt.PageToken = page.NextToken
		page, err = pagination.List(t.Context(), opt)
		assert.NilError(t, err)
	}
	// page is now [1] (oldest). Follow newer.
	opt.PageToken = page.PrevToken
	page, err = pagination.List(t.Context(), opt)
	assert.NilError(t, err)
	// Backward page, Reverse off → newest-first: rows above 1 nearest-first
	// ([2,3,4]) reversed for display.
	assert.DeepEqual(t, page.Items, []int{4, 3, 2})
	assert.Assert(t, page.PrevToken != "", "follow token stays populated")
	assert.Assert(t, page.NextToken != "", "backward page exposes a way back")
}

// TestList_EmptyFollowPollEcho covers the tail: following newer when nothing has
// arrived returns an empty page whose PrevToken echoes the submitted token,
// so the caller keeps its place.
func TestList_EmptyFollowPollEcho(t *testing.T) {
	t.Parallel()
	src := pagination.NewMockSource([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, false)
	opt := pagination.Options[int, int]{DefaultPageSize: 3, MaxPageSize: 100, PageSize: 3, Source: src}

	// Newest page's PrevToken points at the tail (id 10); nothing is newer.
	newest, err := pagination.List(t.Context(), opt)
	assert.NilError(t, err)
	opt.PageToken = newest.PrevToken
	follow, err := pagination.List(t.Context(), opt)
	assert.NilError(t, err)

	assert.Assert(t, cmp.Len(follow.Items, 0))
	assert.Equal(t, follow.PrevToken, newest.PrevToken, "empty poll echoes the token")
	assert.Equal(t, follow.NextToken, "", "no older boundary on an empty poll")
}

// TestList_OneQueryPerPage asserts a bidirectional page derives both tokens from
// the page it fetched — a single query, no probe of the opposite walk.
func TestList_OneQueryPerPage(t *testing.T) {
	t.Parallel()
	src := pagination.NewMockSource([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, false)

	page, err := pagination.List(t.Context(), pagination.Options[int, int]{
		DefaultPageSize: 3, MaxPageSize: 100, PageSize: 3, Source: src,
	})
	assert.NilError(t, err)
	assert.Assert(t, page.NextToken != "")
	assert.Assert(t, page.PrevToken != "")
	assert.Assert(t, cmp.Len(src.QueryCalls(), 1))
}

// TestList_ForwardOnlyPrevToken covers a forward-only source (the task list):
// resubmitting a backward token surfaces ErrUnsupportedDirection, wrapped as
// ErrInvalidRequest so the handler maps it to CodeInvalidArgument.
func TestList_ForwardOnlyPrevToken(t *testing.T) {
	t.Parallel()
	src := pagination.NewMockSource([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, true) // forward-only
	opt := pagination.Options[int, int]{DefaultPageSize: 3, MaxPageSize: 100, PageSize: 3, Source: src}

	// A forward-only source's newest page still yields a PrevToken; the
	// caller normally never exposes it, but a client that resubmits it trips
	// the guard.
	newest, err := pagination.List(t.Context(), opt)
	assert.NilError(t, err)
	opt.PageToken = newest.PrevToken
	_, err = pagination.List(t.Context(), opt)
	assert.Assert(t, errors.Is(err, pagination.ErrUnsupportedDirection))
	assert.Assert(t, errors.Is(err, pagination.ErrInvalidRequest))
}

// TestList_Reverse covers the Options.Reverse display order: off keeps the
// forward walk's order (newest-first), on flips it (oldest-first) for a
// top-down timeline. Reverse never changes token derivation.
func TestList_Reverse(t *testing.T) {
	t.Parallel()

	off, err := pagination.List(t.Context(), pagination.Options[int, int]{
		DefaultPageSize: 3, MaxPageSize: 100, Reverse: false, PageSize: 3,
		Source: pagination.NewMockSource([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, false),
	})
	assert.NilError(t, err)
	assert.DeepEqual(t, off.Items, []int{10, 9, 8})

	on, err := pagination.List(t.Context(), pagination.Options[int, int]{
		DefaultPageSize: 3, MaxPageSize: 100, Reverse: true, PageSize: 3,
		Source: pagination.NewMockSource([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, false),
	})
	assert.NilError(t, err)
	assert.DeepEqual(t, on.Items, []int{8, 9, 10})

	// Same window, opposite order; both advertise the same continuations.
	assert.Assert(t, off.NextToken != "" && on.NextToken != "")
	assert.Assert(t, off.PrevToken != "" && on.PrevToken != "")
}

// TestList_ReverseBackwardPage covers Reverse's effect on a backward page: with
// Reverse on, a backward (ascending, nearest-first) page is returned as-is so it
// still reads oldest-first, consistent with a forward page.
func TestList_ReverseBackwardPage(t *testing.T) {
	t.Parallel()
	src := pagination.NewMockSource([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, false)
	opt := pagination.Options[int, int]{DefaultPageSize: 3, MaxPageSize: 100, Reverse: true, PageSize: 3, Source: src}

	// Newest page (Reverse on → oldest-first) then follow newer (there is
	// nothing newer than 10, so seed a follow from an older cursor instead).
	first, err := pagination.List(t.Context(), opt)
	assert.NilError(t, err)
	assert.DeepEqual(t, first.Items, []int{8, 9, 10})

	// Walk to the oldest page, then follow newer.
	page := first
	for page.NextToken != "" {
		opt.PageToken = page.NextToken
		page, err = pagination.List(t.Context(), opt)
		assert.NilError(t, err)
	}
	opt.PageToken = page.PrevToken
	page, err = pagination.List(t.Context(), opt)
	assert.NilError(t, err)
	// Backward page with Reverse on stays as-is: oldest-first.
	assert.DeepEqual(t, page.Items, []int{2, 3, 4})
}

// TestList_SinglePage covers page shaping and next/prev advertisement on a
// newest (forward) page: empty, partial, exactly-full, over-full, and a zero
// page size falling back to the default.
func TestList_SinglePage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		rows          []int
		pageSize      int
		defaultSize   int
		wantItems     []int
		wantNextToken bool
	}{
		{"empty", nil, 3, 50, nil, false},
		{"partial", []int{9, 10}, 3, 50, []int{10, 9}, false},
		{"exactly full", []int{8, 9, 10}, 3, 50, []int{10, 9, 8}, false},
		{"over full", []int{7, 8, 9, 10}, 3, 50, []int{10, 9, 8}, true},
		{"zero uses default", []int{8, 9, 10}, 0, 2, []int{10, 9}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src := pagination.NewMockSource(tt.rows, false)
			page, err := pagination.List(t.Context(), pagination.Options[int, int]{
				DefaultPageSize: tt.defaultSize, MaxPageSize: 100, PageSize: tt.pageSize, Source: src,
			})
			assert.NilError(t, err)
			assert.DeepEqual(t, page.Items, tt.wantItems)
			assert.Equal(t, page.NextToken != "", tt.wantNextToken)
			// PrevToken is set on any non-empty page; empty otherwise.
			assert.Equal(t, page.PrevToken != "", len(tt.wantItems) > 0)
			// On the forward walk, More tracks the over-fetch exactly like NextToken:
			// true only when a further (older) page remains. A full final page — the
			// exact-page_size boundary — carries no extra row, so More is false.
			assert.Equal(t, page.More, tt.wantNextToken)
		})
	}
}

// TestList_MoreLiveFollow covers More on the backward (live-follow) walk, where
// PrevToken is always populated so More is the only tail signal: it stays true
// while newer rows remain and flips false on the last page — including when that
// page is exactly page_size (the boundary the length heuristic gets wrong).
func TestList_MoreLiveFollow(t *testing.T) {
	t.Parallel()
	src := pagination.NewMockSource([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, false)
	opt := pagination.Options[int, int]{DefaultPageSize: 3, MaxPageSize: 100, PageSize: 3, Source: src}

	// Walk to the oldest page to get a follow token with rows above it (2..10).
	page, err := pagination.List(t.Context(), opt)
	assert.NilError(t, err)
	for page.NextToken != "" {
		opt.PageToken = page.NextToken
		page, err = pagination.List(t.Context(), opt)
		assert.NilError(t, err)
	}

	// Follow newer from the oldest row (1): rows 2..10 remain, so each full page
	// reports More until the tail.
	opt.PageToken = page.PrevToken // above 1: [2,3,4], more (5..10 remain)
	page, err = pagination.List(t.Context(), opt)
	assert.NilError(t, err)
	assert.DeepEqual(t, page.Items, []int{4, 3, 2})
	assert.Assert(t, page.More, "rows 5..10 remain above the page")

	opt.PageToken = page.PrevToken // above 4: [5,6,7], more (8..10 remain)
	page, err = pagination.List(t.Context(), opt)
	assert.NilError(t, err)
	assert.Assert(t, page.More, "rows 8..10 remain above the page")

	// Above 7: [8,9,10] is exactly page_size and the last of the stream — the
	// boundary case. More must be false even though PrevToken stays populated.
	opt.PageToken = page.PrevToken
	page, err = pagination.List(t.Context(), opt)
	assert.NilError(t, err)
	assert.DeepEqual(t, page.Items, []int{10, 9, 8})
	assert.Assert(t, !page.More, "exact page_size tail carries no further page")
	assert.Assert(t, page.PrevToken != "", "follow token stays populated at the tail")

	// An empty follow-poll past the tail is not "more" either.
	opt.PageToken = page.PrevToken
	page, err = pagination.List(t.Context(), opt)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(page.Items, 0))
	assert.Assert(t, !page.More, "empty follow-poll has no further page")
}

// TestList_Errors covers the ErrInvalidRequest contract: out-of-range page sizes
// and undecodable tokens (bad base64, or base64 that isn't JSON).
func TestList_Errors(t *testing.T) {
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
			src := pagination.NewMockSource([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, false)
			_, err := pagination.List(t.Context(), pagination.Options[int, int]{
				DefaultPageSize: 50, MaxPageSize: 100, PageSize: tt.pageSize, PageToken: tt.token, Source: src,
			})
			assert.Assert(t, errors.Is(err, pagination.ErrInvalidRequest))
		})
	}
}
