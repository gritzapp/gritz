---
title: "Task Event Pagination"
description: "ListEventsByTask and GetTaskDetails return a task's full event stream with no limit, and the web UI refetches the whole timeline repeatedly, which scales badly for long-running tasks."
---

Issue: https://github.com/icholy/gritz/issues/1325

## Problem

A task's event stream is returned unbounded. Two RPCs hand events back with no limit:

- **`ListEventsByTask`** returns the full task timeline — every arm of the event union
  (instruction, external, report, lifecycle, link) in chronological order. Its store query
  is `ORDER BY id` with no `LIMIT`:

  ```sql
  -- name: ListEventsByTask :many
  SELECT id, org_id, created_at, task_id, type, wake, payload
  FROM events
  WHERE task_id = $1 AND org_id = $2
    AND (cardinality(sqlc.arg(types)::text[]) = 0 OR type = ANY(sqlc.arg(types)::text[]))
  ORDER BY id;
  ```

  The web UI timeline (`webui/src/routes/tasks.$id.tsx`) fetches the whole list and refetches
  it — on a 60s interval and on every SSE `task_logs` signal — replacing the entire timeline
  each time.

- **`GetTaskDetails`** returns the agent **brief**: the same query filtered to
  `[instruction, external]` (`internal/server/apiserver/task.go`). It is consumed **only** by
  the agent's `get_my_task` tool (`internal/agentmcp/xmcp.go`), which projects the instruction
  events into an `instructions` list and dumps all brief events into an `events` list. The web
  UI calls `GetTaskDetails` for the task and links but **ignores its `events`** — the timeline
  comes from `ListEventsByTask`.

A long-lived task accumulates events continuously: every report, lifecycle transition, tool
call, and inbound PR/Jira event is a row. As the timeline grows, the `ListEventsByTask` payload
and the work to render it grow without bound.

This proposal targets that timeline. The **agent brief** (`GetTaskDetails`) is explicitly **out
of scope** — it stays unbounded and unchanged, per maintainer direction: it is already narrowed
to the two low-volume arms the agent needs (instruction + external), and it is the agent's
one-shot task picture rather than a scrollable surface (see
[The agent brief](#4b-the-agent-brief-gettaskdetails--unchanged-out-of-scope)).

Task **list** pagination already landed — the generic `internal/pagination` keyset package and
`Store.ListTasksPage` / `taskSource` (see `proposals/draft/task-pagination.md`). This proposal
reuses those conventions for the per-task event stream, adapting them for an append-only,
live-followed **timeline**.

## Design

### Overview

Paginate the **timeline** RPC, `ListEventsByTask`, with a **bidirectional keyset cursor** that
starts at the tail. The request keeps a single opaque `page_token`; the response returns **two**
tokens, `prev_page_token` (older) and `next_page_token` (newer).

The shape is dictated by how a timeline is used: open at the newest events, scroll **up** for
history, and watch new events arrive at the **bottom**. So:

- **Open = the tail.** An empty request token returns the **newest** page (one request, O(1)),
  not the oldest — the user sees current activity immediately with no history walk.
- **Scroll up = older.** `prev_page_token` (derived from the page's **first**/oldest row) fetches
  the previous, older page. It is **empty once history is exhausted** (the oldest event is
  loaded).
- **Live-follow = newer.** `next_page_token` (derived from the page's **last**/newest row) fetches
  newer events. It is **always populated** in the paged path: even at the tail with nothing newer
  yet, it returns the resume cursor so a client keeps polling for appends. Polling this cursor *is*
  the live-update mechanism — there is no separate live channel for the timeline.

**Every page is ascending (display order)**, in both directions and in the legacy path. The
cursor is a single boundary `id`; the older/newest-page read scans `id < X ORDER BY id DESC`
(reversed to ascending before returning), and the newer/live-follow read scans
`id > X ORDER BY id ASC`. The direction is encoded *inside* the opaque token, so the request
surface stays a single `page_token`.

Because the event stream is **append-only** (rows are only ever inserted, with monotonically
increasing `id`), every fully-loaded page is an **immutable window** over a fixed id range.
That removes all of the machinery an in-place-mutating list needs: **no page reversal in the
client, no `refetchInterval`, no cache invalidation of loaded pages, and no head-aware
bookkeeping.** The web UI opens at the tail, prepends older pages on scroll-up, and appends the
live delta on each SSE `task_logs` signal.

The **agent brief** (`GetTaskDetails`) is deliberately **left unbounded** — it keeps fetching
all instruction + external events exactly as it does today. It is out of scope for this
proposal. See [The agent brief](#4b-the-agent-brief-gettaskdetails--unchanged-out-of-scope).

Pagination mechanics stay owned by the **store** and the `internal/pagination` package: the
store's `ListEventsByTaskPage` takes the request's `page_size`/`page_token` as plain values and
returns items plus the two tokens; the cursor type, direction encoding, token format, page-size
bounds, and over-fetch all live behind that boundary. The handler passes fields through and maps
errors.

Keyset (not `LIMIT`/`OFFSET`) is the right fit and is even simpler here than for tasks:
`events.id` is a monotonic, unique `bigserial`, so the cursor is a **single column** (`id`), with
no `(created_at, id)` tiebreaker. The existing `idx_events_task_id_id (task_id, id)` index serves
both the forward and backward range scans, so **no new migration is required**.

### 1. Proto Definitions

`proto/gritz/v1/gritz.proto` — extend the existing request/response. `task_id` stays field 1;
the pagination fields are additive, so existing callers are unaffected (see
[Backward compatibility](#7-backward-compatibility)):

```protobuf
message ListEventsByTaskRequest {
  int64 task_id = 1;
  int32 page_size = 2;    // Max events per page (default 50, max 200). 0 with an empty
                          // page_token selects the legacy unpaged path (all events, ascending).
  string page_token = 3;  // Opaque bidirectional cursor (encodes a boundary id + direction).
                          // Empty returns the newest page.
}

message ListEventsByTaskResponse {
  repeated Event events = 1;   // Always oldest-first (ascending id), every page and path.
  string prev_page_token = 2;  // Older page (scroll back); empty when history is exhausted.
  string next_page_token = 3;  // Newer page (scroll forward / live-follow); ALWAYS populated in
                               // the paged path so a client can keep polling the tail for appends.
                               // A page shorter than page_size means the tail is currently reached.
}
```

Both tokens are opaque — the server encodes a boundary `id` and a direction as base64. Clients
treat them as blobs and pass one back as the next request's `page_token`.

### 2. Pagination Package — one `Source`, one `List`, one `Page`

`List`, `Source`, and `Page` become bidirectional in place — there is no separate `ListBi`/`BiPage`.
A page token carries a **`Backward bool`** (not a direction enum) that defaults to `false`
(**forward**), so the zero value is the intuitive default. `List` passes that bit straight into
`Source`'s single `Query(…, backward, …)`; a source that only walks one way returns
`ErrUnsupportedDirection` for the other, which surfaces only if a client resubmits a token for it.

**Forward is the primary walk, backward is the reverse.** Forward reads the descending side (keys
below the cursor; a **nil cursor starts at the newest page**) and continues toward older rows —
this is exactly the task list's existing behavior *and* a timeline's open + scroll-back. Backward
reads the ascending side (keys above the cursor) toward newer rows — a timeline's live-follow. So
the task list is **forward-only** (unchanged), and only a followed timeline also walks
backward. The append-only shape gives each direction a fixed rule: the **forward** (older) token
empties when history runs out; the **backward** (newer) token, when supported, stays populated so
a client keeps polling the tail.

```go
// ErrUnsupportedDirection is returned by a Source's Query asked to walk a direction it
// does not implement (e.g. the task list with backward=true). List never probes for
// support, so it surfaces only if a client submits a page token for a walk the Source
// lacks — which a forward-only source's caller never hands out.
var ErrUnsupportedDirection = errors.New("unsupported page direction")

// Source walks a keyset in either direction and maps a row to a cursor. Query's backward
// argument — the same bit the page token carries — selects the walk: false (default) is
// the primary/forward walk, true its reverse.
//
// Two invariants every Source must satisfy — List relies on these and nothing more:
//
//   - Nearest-first, exclusive. Query returns up to limit rows adjacent to the cursor,
//     ordered from the row nearest the cursor outward, and never re-emits the cursor row
//     itself (the cursor is the previous page's boundary row). A nil cursor starts from
//     that walk's far end (the forward walk's first page).
//   - Mutually opposite. Whatever key order the forward walk returns, the backward walk
//     returns the reverse. List needs only that the two are opposite; it never assumes
//     which one is ascending — that is the Source's choice. (By the convention used here,
//     forward walks toward lower keys / older rows and backward toward higher keys /
//     newer rows, but the package does not depend on it.)
//
// A one-directional Source returns ErrUnsupportedDirection for the walk it does not
// implement. List never calls the opposite walk to build a page (both tokens come from
// the page it already fetched), so the error only surfaces if a client resubmits a token
// for it.
type Source[T, C any] interface {
    Query(ctx context.Context, cursor *C, backward bool, limit int) ([]T, error)
    Cursor(row T) C
}

// Config bounds page size and sets the display order of Page.Items via Reverse,
// defined relative to the forward walk's order:
//
//   - Reverse == false (default): forward pages are returned as-is and backward pages
//     reversed, so Items always read in the forward walk's order (newest-first for the
//     task list, unchanged).
//   - Reverse == true: forward pages are reversed and backward pages returned as-is —
//     the opposite order (oldest-first, for a timeline rendered top-down).
//
// Reverse affects only Items ordering, never token derivation. Because the two Query
// methods are mutually opposite, this single flag lands every page — from either
// direction — in one consistent order.
type Config struct {
    Default int
    Max     int
    Reverse bool
}

// keysetToken is what an opaque page token encodes: a cursor plus the direction to
// resume — the bit List passes to Source.Query. Backward defaults to false (the forward,
// primary walk).
type keysetToken[C any] struct {
    Cursor   C    `json:"c"`
    Backward bool `json:"b,omitempty"`
}

// Page is one page plus the two continuation tokens, both derived from the page's own
// boundary rows — no extra query. ForwardToken continues the primary walk (older) and
// empties when that direction is exhausted; BackwardToken reverses it (newer) and, on a
// non-empty page, stays populated past the tail so an append-only stream can be followed.
// A forward-only caller (the task list) simply ignores BackwardToken.
type Page[T any] struct {
    Items         []T
    ForwardToken  string
    BackwardToken string
}
```

`List` runs one page:

1. Decode the token → `(cursor, backward)`; an empty token is `(nil, false)` — the newest page.
2. Call `Query` with the token's `backward` and `limit = size+1`; over-fetch tells
   whether more rows lie further in that direction. `ErrUnsupportedDirection` from the requested
   walk surfaces as a bad token (the client shouldn't have had it).
3. Derive both continuation tokens from the page's own boundary rows *before* reordering — one
   query per page, no probe of the opposite walk:
   - **ForwardToken** (older): from the boundary furthest along the walk (the lowest-key row under
     the forward = descending convention), populated only when the over-fetch shows more rows remain
     that way.
   - **BackwardToken** (newer): from the boundary nearest the cursor (the highest-key row), populated
     on any non-empty page so an append-only stream can be followed; an empty follow-poll echoes the
     request token so the caller keeps its place.
   `List` never calls the opposite walk to decide whether to emit a token — both come from the page
   it already fetched, so a bidirectional page still costs a **single query**. A forward-only source's
   caller doesn't expose its `BackwardToken`, and `ErrUnsupportedDirection` is reached only if a
   client resubmits a token for a walk the source lacks (which that caller never hands out).
4. Orient `Items` per `cfg.Reverse`: reverse a forward page iff `Reverse`, a backward page iff
   `!Reverse` (see `Config`). Because the two walks are mutually opposite, this lands every page in
   one consistent order regardless of which direction produced it. This is the *only* per-list
   behavioral difference, and it is a `Config` field, not a code fork.

The task list keeps `Reverse: false` (the default — newest-first, unchanged), implements
`Query` for the forward walk (its existing `ListTasksPage` query), and returns
`ErrUnsupportedDirection` when asked for `backward`. Its `ForwardToken` is the same next-page token
it emits today; `List` also fills a `BackwardToken` from the page boundary, but the task list
neither exposes nor resubmits it (the `ListTasks` response has no newer-token field), so `Query` is
never called with `backward=true`. The timeline sets `Reverse: true` and serves both walks.

The one case `List` can't synthesize a follow token for is a *truly empty* stream: the newest page
returns zero rows, so there is no newer boundary to derive `BackwardToken` from and both tokens
come back empty. A task effectively always has its opening instruction event; even if not, the web
UI just re-requests the newest page (empty token) on the next signal until the first event lands —
no synthetic "zero cursor" is needed.

The storage-agnostic package deals in plain `int` for counts (`limit`, `Config.Default/Max`, the
internal `size`). `int32` appears at exactly two type boundaries: the `page_size` request value
(the proto field type, passed verbatim into `List` and store params) and the `LIMIT` argument at
the sqlc call (`int32(limit)` inside each store `Query*` method).

### 3. Store Layer

#### SQL queries

`internal/store/sql/queries/event.sql` — add two paged variants alongside the retained
`ListEventsByTask`, named by SQL order (`Desc`/`Asc`) to stay decoupled from the pagination
package's forward/backward. Both keep the optional `types` filter (parity with the existing query,
so future arm-filtered paging is a param, not a new query) and both are covered by
`idx_events_task_id_id`:

```sql
-- name: ListEventsByTaskDesc :many
-- Newest-first slice: the newest page (no cursor) and scroll-back (id < cursor).
-- Backs the pagination-forward (primary) walk; List reverses to ascending for display.
SELECT id, org_id, created_at, task_id, type, wake, payload
FROM events
WHERE task_id = sqlc.arg(task_id)
  AND org_id = sqlc.arg(org_id)
  AND (cardinality(sqlc.arg(types)::text[]) = 0 OR type = ANY(sqlc.arg(types)::text[]))
  AND (NOT sqlc.arg(use_cursor)::bool OR id < sqlc.arg(cursor_id)::bigint)
ORDER BY id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListEventsByTaskAsc :many
-- Live-follow slice (id > cursor), ascending. Backs the pagination-backward walk.
SELECT id, org_id, created_at, task_id, type, wake, payload
FROM events
WHERE task_id = sqlc.arg(task_id)
  AND org_id = sqlc.arg(org_id)
  AND (cardinality(sqlc.arg(types)::text[]) = 0 OR type = ANY(sqlc.arg(types)::text[]))
  AND id > sqlc.arg(cursor_id)::bigint
ORDER BY id ASC
LIMIT sqlc.arg(page_limit);
```

Notes:

- **Single-column keyset.** `id` is a unique monotonic `bigserial` (the `events_id_seq` PK), so
  `id <=> cursor_id` is a total order — no `created_at` tiebreaker, unlike tasks. `id` order *is*
  insertion (stream) order.
- **No new index.** `idx_events_task_id_id ON events (task_id, id)` already exists and a B-tree
  serves both `id > ? ORDER BY id ASC` and `id < ? ORDER BY id DESC`.
- The `Desc` query serves both the newest page (`use_cursor = false`) and scroll-back
  (`use_cursor = true`); `List` reverses its rows to ascending for display (`Config.Reverse`).
- The existing `ListEventsByTask` query is **retained** for the legacy unpaged path and internal
  callers (see [Backward compatibility](#7-backward-compatibility)).

#### Store method

`internal/store/event.go` — `eventCursor` and `eventSource` are unexported; callers only ever see
opaque tokens. `eventSource`'s `Query` serves both walks (unlike `taskSource`'s, which errors on
`backward=true`). `Reverse: true` makes `List` return each page oldest-first:

```go
// eventCursor is the keyset an event page token encodes. events.id is a unique
// monotonic bigserial, so it is a total order on its own — no tiebreaker.
type eventCursor struct {
    ID int64 `json:"i"`
}

// Timelines are dense (a report/lifecycle/tool row per step), so the default and
// max pages are larger than the task list's; Reverse renders oldest-at-top.
var listEventsPaging = pagination.Config{Default: 50, Max: 200, Reverse: true}

type ListEventsByTaskPageParams struct {
    TaskID    int64
    OrgID     int64
    Types     []string // nil/empty → all arms; a future arm-filtered page passes e.g. [external]
    PageSize  int32    // 0 → default (50); max 200
    PageToken string   // opaque cursor; empty for the newest page
}

// eventSource implements pagination.Source for a task's events, serving both walks from
// one Query: backward=false (primary, descending) → the Desc SQL, backward=true
// (ascending) → the Asc SQL.
type eventSource struct {
    store  *Store
    tx     *sql.Tx
    params ListEventsByTaskPageParams
}

func (src eventSource) types() []string {
    if src.params.Types == nil {
        return []string{} // nil encodes as SQL NULL; empty array matches the cardinality(...) = 0 guard
    }
    return src.params.Types
}

// Query serves both walks: backward=true is the ascending live-follow (rows newer than
// the cursor); backward=false the primary descending walk (a nil cursor = newest page).
func (src eventSource) Query(ctx context.Context, cursor *eventCursor, backward bool, limit int) ([]*model.Event, error) {
    if backward {
        rows, err := src.store.q(src.tx).ListEventsByTaskAsc(ctx, sqlc.ListEventsByTaskAscParams{
            TaskID: src.params.TaskID, OrgID: src.params.OrgID, Types: src.types(),
            CursorID: cursor.ID, PageLimit: int32(limit), // int32 only at the sqlc boundary
        })
        if err != nil {
            return nil, err
        }
        return toModelEvents(rows)
    }
    args := sqlc.ListEventsByTaskDescParams{
        TaskID: src.params.TaskID, OrgID: src.params.OrgID, Types: src.types(),
        UseCursor: cursor != nil, PageLimit: int32(limit), // int32 only at the sqlc boundary
    }
    if cursor != nil {
        args.CursorID = cursor.ID
    }
    rows, err := src.store.q(src.tx).ListEventsByTaskDesc(ctx, args)
    if err != nil {
        return nil, err
    }
    return toModelEvents(rows)
}

func (src eventSource) Cursor(e *model.Event) eventCursor {
    return eventCursor{ID: e.ID}
}

func (s *Store) ListEventsByTaskPage(ctx context.Context, tx *sql.Tx, p ListEventsByTaskPageParams) (*pagination.Page[*model.Event], error) {
    return pagination.List(ctx, listEventsPaging, p.PageSize, p.PageToken, eventSource{store: s, tx: tx, params: p})
}
```

A bad `PageSize` or undecodable `PageToken` surfaces as a wrapped `pagination.ErrInvalidRequest`;
query failures surface as-is. Same contract as `ListTasksPage`.

### 4. Server Handler (`ListEventsByTask`)

`internal/server/apiserver/event.go` — keep the scope checks, then branch on whether the caller
opted into pagination. `page_size == 0 && page_token == ""` preserves today's behavior (all
events, ascending, no tokens); otherwise serve a bidirectional page.

```go
func (s *Server) ListEventsByTask(ctx context.Context, req *gritzv1.ListEventsByTaskRequest) (*gritzv1.ListEventsByTaskResponse, error) {
    caller := apiauth.MustCaller(ctx)
    // ... unchanged scope / instance checks ...

    // Legacy unpaged path: no pagination fields → all events, ascending.
    if req.PageSize == 0 && req.PageToken == "" {
        events, err := s.store.ListEventsByTask(ctx, nil, req.TaskId, caller.OrgID, nil)
        if err != nil {
            return nil, connect.NewError(connect.CodeInternal, err)
        }
        return &gritzv1.ListEventsByTaskResponse{Events: model.ProtoMap(events)}, nil
    }

    // Paged path: bidirectional keyset page (empty token → newest page).
    page, err := s.store.ListEventsByTaskPage(ctx, nil, store.ListEventsByTaskPageParams{
        TaskID:    req.TaskId,
        OrgID:     caller.OrgID,
        PageSize:  req.PageSize,
        PageToken: req.PageToken,
    })
    if err != nil {
        code := connect.CodeInternal
        if errors.Is(err, pagination.ErrInvalidRequest) {
            code = connect.CodeInvalidArgument
        }
        return nil, connect.NewError(code, err)
    }
    // The primary (forward) walk goes toward older rows, so it is the timeline's
    // "previous" page; the reverse (backward) walk is the newer/live-follow "next".
    return &gritzv1.ListEventsByTaskResponse{
        Events:        model.ProtoMap(page.Items),
        PrevPageToken: page.ForwardToken,
        NextPageToken: page.BackwardToken,
    }, nil
}
```

Every path returns events ascending, so the client never reorders. The `errors.Is` mapping is the
one place the handler acknowledges pagination, matching `ListTasks`.

### 4b. The agent brief (`GetTaskDetails`) — unchanged, out of scope

`GetTaskDetails` and the agent's `get_my_task` tool are **left exactly as they are**: they keep
fetching the full brief (all instruction + external events, ascending) with no bound, no tail,
and no paging.

This is a deliberate scope decision. The brief is the agent's one-shot picture of its task, read
once per wake, and it is already narrowed to the two low-volume, semantically-required arms
(instruction + external) — it excludes the high-volume arms (report, lifecycle, link) that make
the *timeline* grow. Paging it would force the agent to make follow-up calls just to reconstruct
its own instructions, and any tail risks dropping context the agent needs mid-task. The
unbounded growth this proposal targets is the full **timeline** (`ListEventsByTask`), not the
brief.

Concretely: `internal/server/apiserver/task.go`'s `GetTaskDetails` and
`internal/agentmcp/xmcp.go`'s `getMyTask` / `taskDetailsToMap` are untouched. If the brief ever
does become a problem for a pathological task, bounding it is a separate follow-up (a dedicated
paged agent tool, or a head-preserving tail) — not part of this change.

### 5. Web UI

`webui/src/routes/tasks.$id.tsx` switches the timeline from `useQuery(listEventsByTask)` to a
**bidirectional** `useInfiniteQuery`: open at the tail, prepend older pages on scroll-up, append
the live delta on SSE. Every page is already ascending, so pages flatten directly — **no
reversal**:

```tsx
const PAGE_SIZE = 50

const {
  data,
  fetchPreviousPage,     // older history (scroll up)
  hasPreviousPage,
  isFetchingPreviousPage,
  fetchNextPage,         // newer events (live-follow)
} = useInfiniteQuery(
  listEventsByTask,
  { taskId, pageSize: PAGE_SIZE },
  {
    // Empty initial pageParam → the newest (tail) page: one request on open.
    pageParamKey: 'pageToken',
    getPreviousPageParam: (firstPage) => firstPage.prevPageToken || undefined,
    // next_page_token is always set (it doubles as the live-follow cursor), so it
    // can't signal "stop"; we only fetch it on an SSE signal, never as "load more".
    getNextPageParam: (lastPage) => lastPage.nextPageToken || undefined,
  },
)

// TanStack keeps pages in navigation order and prepends fetchPreviousPage results,
// so flattening yields one ascending stream across all loaded pages.
const events = data?.pages.flatMap((p) => p.events) ?? []
const timeline = eventsToTimeline(events)
```

- **Open (O(1)).** The initial request carries an empty token and gets the newest page — the user
  sees current activity immediately, no history walk.
- **Scroll up.** A "Load older" control (or a scroll/intersection trigger) calls
  `fetchPreviousPage()` while `hasPreviousPage`; TanStack prepends the older page and
  `getPreviousPageParam` reads `prev_page_token`, which goes empty at the oldest event. The
  connect-query wrapper passes `getPreviousPageParam` through (verified against the installed
  types).
- **Live-follow.** On each SSE `task_logs` signal for this task, call `fetchNextPage()` once: it
  requests `id > <newest seen>` and appends any newly-inserted events at the bottom. Loaded full
  pages are immutable windows over append-only rows, so nothing already rendered changes — **no
  invalidation, no `refetchInterval`, no reversal, no head-aware bookkeeping.**
  `use-org-sse.ts`'s `task_logs` case changes from `invalidateQueries(listEventsByTask)` to
  "`fetchNextPage()` for this task's timeline."
- **Empty tail polls.** When nothing new has been appended, `fetchNextPage()` returns an empty
  page whose token echoes the same cursor; left alone these accumulate. Trim trailing empty pages
  after each follow fetch (the preceding page's `next_page_token` already points at the same
  resume cursor, so the live-follow is unaffected):

  ```tsx
  queryClient.setQueryData(key, (prev) =>
    !prev ? prev : { ...prev, pages: dropTrailingEmpty(prev.pages), pageParams: /* sliced to match */ },
  )
  ```

`GetTaskDetails` (task + links) keeps its existing `useQuery` + SSE invalidation unchanged; only
the timeline query moves to the bidirectional infinite model.

### 6. Other callers

`store.ListEventsByTask` (unpaged) is kept and still used by:

- `GetTaskDetails` — for the complete agent brief, unchanged (see
  [The agent brief](#4b-the-agent-brief-gettaskdetails--unchanged-out-of-scope)).
- Tests and any internal consumers that legitimately want the full stream.

Only the web UI timeline moves to the paged path. The `ListEventsByTask` **RPC** serves both:
legacy callers (empty page fields) get the full list; the web UI opts into bidirectional paging.

### 7. Backward compatibility

- **`ListEventsByTaskRequest`** gains two fields (`page_size`, `page_token`); the response gains
  `prev_page_token` and `next_page_token`. All additive — old clients that send neither request
  field hit the legacy branch and get **exactly today's behavior**: every event, ascending, empty
  tokens. Existing server tests (`event_test.go`, `log_test.go`, `link_test.go`,
  `lifecycle_test.go`, `taskscope_test.go`) that call `ListEventsByTask` with only `TaskId` keep
  passing unchanged.
- **Ordering is unchanged.** Legacy and every paged page return events ascending (`id`), so a
  caller that opts into paging sees the same order it saw before, just in bounded pages.
- **Always-populated `next_page_token`.** In the paged path it is never empty — it doubles as the
  live-follow resume cursor. Clients detect "caught up" from a page shorter than `page_size`, not
  from an empty token. `prev_page_token` follows the usual rule (empty = no older rows).
  Documented on the proto fields.
- **`GetTaskDetails` / `get_my_task`** are untouched — no proto change, no behavior change. The
  agent brief keeps returning every instruction + external event as it does today (see
  [The agent brief](#4b-the-agent-brief-gettaskdetails--unchanged-out-of-scope)).

## Implementation Plan

1. **Proto fields** — Delivers: `page_size`/`page_token` on `ListEventsByTaskRequest`,
   `prev_page_token`/`next_page_token` on the response, with the tail / always-populated / empty
   semantics documented on the fields; regenerate Go + webui (`mise run generate`, buf for webui).
   Depends on: nothing. Verifiable by: generated code compiles; no behavior change yet.
2. **Pagination bidirectional `List`** — Delivers: `ErrUnsupportedDirection`, the `Backward`-bool
   token, `Config.Reverse`, and both continuation tokens on `Page` — folded into the existing
   `List`/`Source`/`Page` (no `ListBi`/`BiPage`); give `Source` a single `Query(…, backward bool, …)`
   (documenting the nearest-first + mutually-opposite invariants) and narrow `limit` to `int`.
   `taskSource`'s `Query` keeps its descending query and errors on `backward=true` —
   behavior-preserving. Depends on: nothing. Verifiable by: package unit tests (mocked `Source`) —
   newest page (empty token), forward scroll to exhaustion (`ForwardToken` empties), backward follow
   (`BackwardToken` always set), empty-follow-poll echo, a bidirectional page costs one query (no
   probe), submitting a backward token to a forward-only source → `ErrUnsupportedDirection`,
   `Reverse` on/off ordering, page-size bounds, undecodable token → `ErrInvalidRequest`; task list
   still green.
3. **Store paged queries + method** — Delivers: `ListEventsByTaskDesc` / `ListEventsByTaskAsc`
   SQL (sqlc-generated), `eventCursor`, `eventSource` (both walks), `Store.ListEventsByTaskPage`.
   Depends on: (2) (reuses `idx_events_task_id_id`; no migration). Verifiable by: store unit tests
   against a real DB — walking forward/backward covers the whole stream ascending without gaps/dups;
   a tail backward token picks up a subsequently-inserted event; `types` filter honored; bad
   size/token → `ErrInvalidRequest`.
4. **`ListEventsByTask` handler paging** — Delivers: the legacy/paged branch + `errors.Is` mapping,
   returning both tokens. Depends on: (1), (3). Verifiable by: handler tests — empty page fields →
   full ascending list (unchanged); empty token + `page_size` → newest page with both tokens;
   walking `prev`/`next` reaches history/tail; invalid page size → `CodeInvalidArgument`.
5. **Web UI bidirectional timeline** — Delivers: `useInfiniteQuery` opening at the tail,
   `fetchPreviousPage` for history (with a "Load older" trigger), `fetchNextPage` on SSE `task_logs`
   for live-follow, trailing-empty-page trimming; drops the timeline's `refetchInterval` and the old
   `invalidateQueries(listEventsByTask)`. Depends on: (4). Verifiable by: opening a task with >
   `page_size` events shows the newest page in one request; scroll-up prepends older pages and stops
   at the oldest; appending an event + firing `task_logs` appends it at the bottom with no reflow;
   idle polls don't grow the page cache; run `pnpm lint`.

## Trade-offs

### Bidirectional tail-start cursor vs one-directional walks

**Chosen: a bidirectional cursor that opens at the tail.** A timeline is opened at "now," read
upward for history, and watched downward for new events — three motions the bidirectional cursor
serves directly: empty token → newest page (O(1) open), `prev_page_token` → older history on
demand, `next_page_token` → the live delta. The rejected alternatives each fail one motion: a
newest-first "load older" cursor needs a *separate* mechanism to discover and append new events at
the head (and reversal to render top-down); an oldest-first forward-only cursor makes live-follow
trivial but forces walking the **entire history on open** (O(N) requests) before the user reaches
current activity. The bidirectional cursor gets O(1) open *and* one-cursor live-follow.

The cost is that `List`/`Source`/`Page` grow bidirectionality and there are two query shapes. But
this stays inside the package (the store and handler stay thin), the task list keeps its exact
behavior, and it is reusable by the next append-only timeline (e.g. an org or run event feed).

### Folding bidirectionality into `List`/`Source`/`Page` vs a separate `ListBi`/`BiPage`

**Chosen: one `List`, one `Source`, one `Page` — bidirectionality folded in, not a parallel
`ListBi`/`BiPage`.** The token carries a `Backward bool` (default `false` = forward), so the zero
value is the intuitive default; there is no exported `Direction` type. `Source` exposes a **single**
`Query(ctx, cursor, backward bool, limit)` — `backward` is that same token bit passed straight
through, not a new concept — where `false` is the primary descending/newest-first walk (the task
list's existing behavior) and `true` its ascending, newer/live-follow reverse. A one-directional
source returns `ErrUnsupportedDirection` for the walk it doesn't implement (an early return in the
one method, not a dead stub); `List` never probes it (both tokens come from the already-fetched page,
so a bidirectional page costs one query), and the guard trips only if a client resubmits a token for
the missing walk — which the forward-only task list never exposes. Alternatives weighed and rejected:
**two methods** `QueryForward`/`QueryBackward` (a dead erroring stub on every one-directional source,
plus an extra interface method); an exported `dir Direction` enum (a type for a binary choice the
token already carries as a bool); two **parallel interfaces** or `BiSource` embedding `Source` (a
second interface); and a separate `ListBi`/`BiPage` (duplicate entry points and page types). The one
real cost of the fold is that `List` must serve two display orders — the task list's newest-first and
the timeline's oldest-first — so `Config` gains a `Reverse` flag (the sole per-list behavioral knob,
defaulting to the task list's current order). `Reverse` is well-defined *because* the `Source`
contract pins the two walks to be mutually opposite and nearest-first: the flag reverses each page
relative to the forward walk's order, and being opposite guarantees every page — from either
direction — lands in one consistent order. Terminology is generic **forward/backward** (by key
order), not domain newer/older; `backward` (which walk) is deliberately distinct from `Config.Reverse`
(which display order), since the two are independent per page; and the guard is a **runtime**
`ErrUnsupportedDirection` rather than a compile-time impossibility. The residual cost is that a
bidirectional `Query` branches on `backward` over its two SQL queries — accepted for the smaller
interface.

### Always-populated newer token vs empty-token-means-done

**Chosen: the newer (backward) token always resumes; the older (forward) token empties at
history's end.** An append-only followed stream has no real "done" at the newest end — the tail is
only *currently* the end, and new rows will arrive — so the newer token must survive the tail to
act as the live-follow cursor ("caught up" is signalled by a short page). The older end *is*
finite, so the forward/older token empties normally. `List` applies this by direction: the forward
(older) token is over-fetch-gated (empties), the backward (newer) token is filled from the page
boundary on any non-empty page. The task list, forward-only, ignores its newer token (its RPC
exposes no field for one), so it behaves exactly as before (its forward token empties at the end) —
bounded and followed pagination coexist in one `List` without either compromising the other.

### Single-column `id` keyset vs `(created_at, id)`

**Chosen: `id` alone.** `events.id` is a unique monotonic `bigserial` and *is* the stream order,
so it is a total order with no tiebreaker — unlike `tasks`, where `created_at` is not unique and
needs `id` appended. This also means the pre-existing `idx_events_task_id_id` covers both scans and
**no migration is needed**. Using `created_at` would be both unnecessary and weaker (ties
possible).

### Legacy path vs migrating every caller to paging

**Chosen: keep the legacy unpaged path.** `ListEventsByTask` has several internal/test callers
that want the whole stream, and `GetTaskDetails` still uses the unpaged store method for the
complete brief. A backward-compatible branch (empty page fields → legacy behavior) lets the RPC
serve both without a flag-day migration of every caller, and since both paths return ascending
there is no ordering seam.

### Leaving the agent brief unbounded vs bounding it now

**Chosen: leave the brief unbounded (out of scope).** The brief is the agent's one-shot task
picture, read once per wake, and it is already narrowed to the two low-volume, load-bearing arms
(instruction + external) — it excludes the high-volume report/lifecycle/link arms that actually
drive timeline growth. Paging it would make the agent issue follow-up calls just to reassemble
its own instructions, and any tail risks dropping context it needs mid-task. The concrete
unbounded-growth problem is the full timeline, which this proposal paginates; the brief keeps its
current behavior. If the brief ever becomes a problem for a pathological task, bounding it is a
separate follow-up (a dedicated paged agent tool, or a head-preserving tail).

### Reusing `internal/pagination` vs an events-specific helper

**Chosen: extend the package.** Making `List` bidirectional keeps token encode/decode, page-size
bounds, over-fetch, and the `ErrInvalidRequest` contract shared, and the store still adds only an
`eventCursor` and an `eventSource`. An events-specific open-coding was the fallback if the generic
extension got awkward; it did not — the bidirectional logic is regular enough to live generically,
and a followed timeline is a shape the codebase will hit again.

## Open Questions

1. **Default / max page size.** Proposed 50 / 200 (vs the task list's 50 / 100) because timelines
   are denser than task lists. Is 50 the right page size, and is 200 a safe ceiling for a single
   response?
2. **Scroll-up trigger.** History via `fetchPreviousPage` can be a "Load older" button or an
   intersection-observer at the top of the list. Which for the first cut?
3. **Type-filtered scans.** Any future arm-filtered paging scans `(task_id, id)` and filters `type`
   in place. On a task that is overwhelmingly reports, finding N events of a given type may read
   many rows. Acceptable now; if it bites, add `(task_id, type, id)`. Worth pre-empting, or defer
   until measured?
4. **`ListExternalEvents` (org feed).** The org-level external feed (`ListEvents`, bare `limit`, no
   cursor) has the same unbounded shape and could adopt the bidirectional `List` with an org-scoped `Source`. In
   scope as a follow-up, or a separate proposal?
