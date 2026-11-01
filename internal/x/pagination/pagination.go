// Package pagination provides generic keyset (cursor) pagination plumbing for
// store list methods. It is storage- and proto-agnostic: it deals only in
// plain ints, strings, and generic type parameters, so it depends on nothing
// but the standard library.
//
// It serves three shapes, each with its own entry point:
//
//   - List pages one way through a keyset and stops at its end (the task list).
//   - History opens at the newest page, scrolls back into older rows until the
//     start of history, and follows newer rows forever (logs, the timeline).
//   - Stream reads from the start of a keyset and follows it forever (runner
//     events).
//
// All three over-fetch a single row to learn whether another page exists, so a
// page costs exactly one query, and derive their continuation tokens from the
// page's own boundary rows. Page-size validation, the opaque token encoding and
// ErrInvalidRequest are shared.
package pagination

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// ErrInvalidRequest reports a bad page size or an undecodable page token.
// RPC handlers map it to connect.CodeInvalidArgument.
var ErrInvalidRequest = errors.New("invalid page request")

// Options configures a single page request: the page-size bounds and the
// request's size and token.
type Options struct {
	DefaultPageSize int    // size used when the request omits page_size (0)
	MaxPageSize     int    // largest size a caller may request
	PageSize        int    // requested size (0 -> DefaultPageSize)
	PageToken       string // opaque token from a previous page; empty -> first page
}

// CursorSource walks a keyset in one direction and maps a row to a cursor. It
// backs both List and Stream, which differ only in when a page stops handing
// out a continuation token.
//
//go:generate go tool moq -out source_moq_test.go . CursorSource HistorySource
type CursorSource[T, C any] interface {
	// Query returns up to limit rows following after in the walk's order,
	// excluding after itself. A nil after starts at the beginning of the walk.
	Query(ctx context.Context, after *C, limit int) ([]T, error)
	// Cursor returns the keyset of row; it is what page tokens encode.
	Cursor(row T) C
}

// HistorySource walks a keyset both ways from a cursor and maps a row to a
// cursor. Both walks exclude the cursor row itself.
type HistorySource[T, C any] interface {
	// Older returns up to limit rows preceding before, newest first. A nil before
	// starts at the newest row.
	Older(ctx context.Context, before *C, limit int) ([]T, error)
	// Newer returns up to limit rows following after, oldest first. A nil after
	// starts at the oldest row.
	Newer(ctx context.Context, after *C, limit int) ([]T, error)
	// Cursor returns the keyset of row; it is what page tokens encode.
	Cursor(row T) C
}

// ListPage is one page of a List walk.
type ListPage[T any] struct {
	Items     []T
	NextToken string // resumes after the last item; empty when exhausted
}

// HistoryPage is one page of a History walk.
type HistoryPage[T any] struct {
	// Items is always ascending (oldest first), whichever way the page walked.
	Items []T
	// OlderToken scrolls back from the page's oldest item. It is empty at the
	// start of history, and on an empty follow page, which has no oldest item.
	OlderToken string
	// FollowToken resumes after the page's newest item, or, on an empty page,
	// after the request's cursor — "from the start" when it had none. It is
	// always set, so a caller can always follow the rows appended after a page.
	FollowToken string
	// More reports whether another page lies beyond Items in the direction this
	// page walked: older rows for an opening or OlderToken page, newer rows for
	// a FollowToken page. It is exact even at a page_size boundary, and is the
	// only tail signal for a follow, whose FollowToken never empties.
	More bool
}

// StreamPage is one page of a Stream walk.
type StreamPage[T any] struct {
	Items []T
	// NextToken resumes after the last item, or echoes the request's position
	// on an empty page. It is always set.
	NextToken string
	// More reports whether another page lies beyond Items right now. It is the
	// only tail signal, since NextToken never empties.
	More bool
}

// token is the JSON payload behind every base64 page token: a boundary cursor,
// nil at the start of a walk, and for History the walk to resume.
type token[C any] struct {
	Cursor *C   `json:"c,omitempty"`
	Follow bool `json:"f,omitempty"` // History only: walk Newer instead of Older
}

// List runs one page of a one-direction walk over src. An empty token is the
// first page; NextToken resumes after the last item and empties once the
// over-fetch shows nothing remains.
func List[T, C any](ctx context.Context, src CursorSource[T, C], opt Options) (*ListPage[T], error) {
	size, tok, err := open[C](opt)
	if err != nil {
		return nil, err
	}
	items, err := src.Query(ctx, tok.Cursor, size+1)
	if err != nil {
		return nil, err
	}
	items, more := trim(items, size)
	page := &ListPage[T]{Items: items}
	if more {
		last := src.Cursor(items[len(items)-1])
		page.NextToken = encode(token[C]{Cursor: &last})
	}
	return page, nil
}

// History runs one page of a history walk over src. An empty token opens at the
// newest page; OlderToken pages walk back toward the start of history and
// FollowToken pages walk toward newer rows. Items is always ascending.
func History[T, C any](ctx context.Context, src HistorySource[T, C], opt Options) (*HistoryPage[T], error) {
	size, tok, err := open[C](opt)
	if err != nil {
		return nil, err
	}
	var items []T
	if tok.Follow {
		items, err = src.Newer(ctx, tok.Cursor, size+1)
	} else {
		items, err = src.Older(ctx, tok.Cursor, size+1)
	}
	if err != nil {
		return nil, err
	}
	items, more := trim(items, size)
	if !tok.Follow {
		slices.Reverse(items)
	}
	page := &HistoryPage[T]{Items: items, More: more}
	// Older rows lie before the oldest item when the older walk's over-fetch
	// says so, or always on a follow page that started after a cursor — the
	// cursor row itself is older. A follow page from the start of history has
	// nothing before its first item.
	if len(items) > 0 && (tok.Follow && tok.Cursor != nil || !tok.Follow && more) {
		oldest := src.Cursor(items[0])
		page.OlderToken = encode(token[C]{Cursor: &oldest})
	}
	follow := tok.Cursor
	if len(items) > 0 {
		newest := src.Cursor(items[len(items)-1])
		follow = &newest
	}
	page.FollowToken = encode(token[C]{Cursor: follow, Follow: true})
	return page, nil
}

// Stream runs one page of a follow-forever walk over src. An empty token is the
// start of the walk; NextToken resumes after the last item, or keeps the
// request's position on an empty page.
func Stream[T, C any](ctx context.Context, src CursorSource[T, C], opt Options) (*StreamPage[T], error) {
	size, tok, err := open[C](opt)
	if err != nil {
		return nil, err
	}
	items, err := src.Query(ctx, tok.Cursor, size+1)
	if err != nil {
		return nil, err
	}
	items, more := trim(items, size)
	next := tok.Cursor
	if len(items) > 0 {
		last := src.Cursor(items[len(items)-1])
		next = &last
	}
	return &StreamPage[T]{Items: items, NextToken: encode(token[C]{Cursor: next}), More: more}, nil
}

// open validates opt's page size and decodes its token. An empty token is the
// zero token: a nil cursor.
func open[C any](opt Options) (int, token[C], error) {
	var tok token[C]
	size := cmp.Or(opt.PageSize, opt.DefaultPageSize)
	if size < 1 || size > opt.MaxPageSize {
		return 0, tok, fmt.Errorf("%w: page_size must be between 1 and %d", ErrInvalidRequest, opt.MaxPageSize)
	}
	if opt.PageToken == "" {
		return size, tok, nil
	}
	b, err := base64.URLEncoding.DecodeString(opt.PageToken)
	if err == nil {
		err = json.Unmarshal(b, &tok)
	}
	if err != nil {
		return 0, tok, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	return size, tok, nil
}

// trim drops the over-fetched row, reporting whether there was one.
func trim[T any](items []T, size int) ([]T, bool) {
	if len(items) > size {
		return items[:size], true
	}
	return items, false
}

// encode serializes a token into its opaque base64 page-token string.
func encode[C any](t token[C]) string {
	b, err := json.Marshal(t)
	if err != nil {
		panic(fmt.Sprintf("pagination: a cursor must always marshal: %v", err))
	}
	return base64.URLEncoding.EncodeToString(b)
}
