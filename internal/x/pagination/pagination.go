// Package pagination provides generic keyset (cursor) pagination plumbing for
// store list methods. It is storage- and proto-agnostic: it deals only in
// plain ints, strings, and generic type parameters, so it depends on nothing
// but the standard library.
//
// Pagination is bidirectional. A page token carries a boundary cursor plus a
// Backward bool (default false = the primary/forward walk). List passes that
// bit straight into Source.Query, and returns two continuation tokens on every
// page: NextToken continues the primary (forward) walk (toward older rows) and
// empties when that direction is exhausted; PrevToken reverses it (the backward
// walk, toward newer rows) and, on a non-empty page, stays populated so an
// append-only stream can be followed past its tail. A forward-only caller (the
// task list) simply ignores PrevToken and behaves exactly as it did before
// bidirectionality.
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

// ErrUnsupportedDirection is returned by a Source's Query asked to walk a
// direction it does not implement (e.g. the task list with backward=true).
// List never probes for support, so it surfaces only if a client submits a
// page token for a walk the Source lacks — which a forward-only source's caller
// never hands out. List wraps it as ErrInvalidRequest (a bad token).
var ErrUnsupportedDirection = errors.New("unsupported page direction")

// Options configures a single List call: the page-size bounds, the request's
// size and cursor, the Source to walk, and Reverse, which sets the display
// order of Page.Items relative to the forward walk's order:
//
//   - Reverse == false (default): forward pages are returned as-is and backward
//     pages reversed, so Items always read in the forward walk's order
//     (newest-first for the task list, unchanged).
//   - Reverse == true: forward pages are reversed and backward pages returned
//     as-is — the opposite order (oldest-first, for a timeline rendered
//     top-down).
//
// Reverse affects only Items ordering, never token derivation. Because the two
// Query walks are mutually opposite, this single flag lands every page — from
// either direction — in one consistent order.
type Options[T any, C any] struct {
	DefaultPageSize int    // size used when the request omits page_size (0)
	MaxPageSize     int    // largest size a caller may request
	Reverse         bool   // display Items in the reverse of the forward walk's order
	PageSize        int    // requested size (0 -> DefaultPageSize)
	PageToken       string // opaque cursor; empty -> newest page
	Source          Source[T, C]
}

// Page is one page plus the two continuation tokens, both derived from the
// page's own boundary rows — no extra query. NextToken continues the primary
// (forward) walk and empties when that direction is exhausted; PrevToken
// reverses it (the backward walk) and, on a non-empty page, stays populated past
// the tail so an append-only stream can be followed. A forward-only caller (the
// task list) simply ignores PrevToken.
type Page[T any] struct {
	Items     []T
	NextToken string
	PrevToken string
	// More reports whether another page of rows exists beyond Items in the walked
	// direction. It is the size+1 over-fetch result, independent of token
	// emptiness, so a live-follow caller (whose PrevToken is always populated)
	// can detect the tail without the page-length heuristic: More is false
	// exactly when Items is the last page along the walk, including at an
	// exact-page_size boundary (a full final page carries no extra row).
	More bool
}

// Token is what an opaque page token encodes and the value List hands to
// Source.Query: a boundary cursor plus the direction to resume the walk. A nil
// Cursor is the newest page (the forward walk's start); Backward defaults to
// false (the forward, primary walk). It is the JSON payload behind every base64
// page token.
type Token[C any] struct {
	Cursor   *C   `json:"c,omitempty"`
	Backward bool `json:"b,omitempty"`
}

// Source walks a keyset in either direction and maps a row to a cursor. The
// Token it receives carries both the boundary cursor and the direction: a nil
// Token.Cursor starts the walk at its far end, and Token.Backward — the same bit
// the page token carries — selects the walk (false, the default, is the
// primary/forward walk; true its reverse).
//
// Two invariants every Source must satisfy — List relies on these and nothing
// more:
//
//   - Nearest-first, exclusive. Query returns up to limit rows adjacent to the
//     cursor, ordered from the row nearest the cursor outward, and never
//     re-emits the cursor row itself (the cursor is the previous page's
//     boundary row). A nil cursor starts from that walk's far end (the forward
//     walk's first page).
//   - Mutually opposite. Whatever key order the forward walk returns, the
//     backward walk returns the reverse. List needs only that the two are
//     opposite; it never assumes which one is ascending — that is the Source's
//     choice. (By the convention used here, forward walks toward lower keys /
//     older rows and backward toward higher keys / newer rows, but the package
//     does not depend on it.)
//
// A one-directional Source returns ErrUnsupportedDirection for the walk it does
// not implement. List never calls the opposite walk to build a page (both
// tokens come from the page it already fetched), so the error only surfaces if
// a client resubmits a token for it.
//
//go:generate go tool moq -out source_moq_test.go . Source
type Source[T, C any] interface {
	Query(ctx context.Context, token Token[C], limit int) ([]T, error)
	// Cursor returns the keyset of row; it is what page tokens encode.
	Cursor(row T) C
}

// List runs one page of a keyset-paginated query against opt.Source.
//
// It validates opt.PageSize against opt's bounds, decodes opt.PageToken into a
// cursor and a direction (an empty token is the newest page: nil cursor,
// forward), and calls Source.Query once with a limit of size+1 — the over-fetch
// reveals whether more rows lie further along that walk. Both continuation
// tokens are derived from the returned page's own boundary rows, so a
// bidirectional page costs a single query with no probe of the opposite walk:
//
//   - NextToken (the forward walk, older) comes from the lowest-key boundary and
//     is populated only when the over-fetch shows more rows remain that way — it
//     empties at history's end.
//   - PrevToken (the backward walk, newer) comes from the highest-key boundary
//     and is populated on any non-empty page so an append-only stream can be
//     followed; an empty follow-poll echoes the request token so the caller
//     keeps its place.
//   - More is the over-fetch result itself: true iff a further page exists along
//     the walked direction. Unlike NextToken it stays meaningful for the
//     live-follow walk, whose PrevToken is always populated, so a caller detects
//     the tail with !More instead of a page-length heuristic.
//
// Finally Items is oriented per opt.Reverse. ErrUnsupportedDirection from the
// requested walk surfaces as ErrInvalidRequest (a bad token the client should
// not have had); other query failures surface as-is.
func List[T, C any](ctx context.Context, opt Options[T, C]) (*Page[T], error) {
	size := cmp.Or(opt.PageSize, opt.DefaultPageSize)
	if size < 1 || size > opt.MaxPageSize {
		return nil, fmt.Errorf("%w: page_size must be between 1 and %d", ErrInvalidRequest, opt.MaxPageSize)
	}
	var token Token[C]
	if opt.PageToken != "" {
		var err error
		if token, err = Decode[C](opt.PageToken); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
		}
	}
	backward := token.Backward
	items, err := opt.Source.Query(ctx, token, size+1)
	if err != nil {
		if errors.Is(err, ErrUnsupportedDirection) {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		return nil, err
	}
	// Over-fetch: an extra row means more rows lie further along this walk.
	more := len(items) > size
	if more {
		items = items[:size]
	}
	page := &Page[T]{Items: items, More: more}
	switch {
	case len(items) > 0:
		// Query returns rows nearest-first, so items[0] is the boundary nearest
		// the cursor and the last item is the boundary furthest into the walk.
		// Same-direction continuation resumes from the furthest row; the
		// opposite direction resumes from the nearest row (back toward where the
		// cursor came from).
		nearest := opt.Source.Cursor(items[0])
		furthest := opt.Source.Cursor(items[len(items)-1])
		if backward {
			// Newer/live-follow page: follow onward from the furthest (highest)
			// key — always resumable so the tail can be polled — and expose the
			// nearest (lowest) key as the way back into older history.
			if page.PrevToken, err = Encode(Token[C]{Cursor: &furthest, Backward: true}); err != nil {
				return nil, err
			}
			if page.NextToken, err = Encode(Token[C]{Cursor: &nearest}); err != nil {
				return nil, err
			}
		} else {
			// Older/primary page: the nearest (highest) key resumes the
			// newer/live-follow direction, always populated on a non-empty page;
			// the furthest (lowest) key continues toward older history, but only
			// when the over-fetch shows more remain.
			if page.PrevToken, err = Encode(Token[C]{Cursor: &nearest, Backward: true}); err != nil {
				return nil, err
			}
			if more {
				if page.NextToken, err = Encode(Token[C]{Cursor: &furthest}); err != nil {
					return nil, err
				}
			}
		}
	case backward:
		// Empty follow-poll: no boundary row to derive from, so echo the request
		// token — the same resume cursor — to keep the caller's place at the tail.
		page.PrevToken = opt.PageToken
	}
	// Orient Items relative to the forward walk's order: reverse a forward page
	// iff Reverse, a backward page iff !Reverse. Because the two walks are
	// mutually opposite, this lands every page in one consistent order.
	if backward != opt.Reverse {
		slices.Reverse(page.Items)
	}
	return page, nil
}

// Encode serializes a Token into its opaque base64 page-token string.
func Encode[C any](t Token[C]) (string, error) {
	b, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// Decode parses an opaque base64 page-token string back into a Token.
func Decode[C any](token string) (Token[C], error) {
	var t Token[C]
	b, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return t, err
	}
	err = json.Unmarshal(b, &t)
	return t, err
}
