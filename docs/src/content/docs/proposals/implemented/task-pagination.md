---
title: "Task Pagination"
description: "ListTasks and the task list page are unpaginated and fetch every non-archived task for the org at once, which degrades as task counts grow."
---

Issue: https://github.com/icholy/gritz/issues/1012

## Problem

The `ListTasks` RPC and the task list page (`webui/src/routes/tasks.index.tsx`) are unpaginated. The RPC request takes no parameters and the store query fetches every non-archived task for the org in one shot:

```sql
-- name: ListTasks :many
SELECT id, name, runner, workspace, status, command, version, org_id, archived, created_at, updated_at, auto_archive
FROM tasks
WHERE archived = FALSE AND org_id = $1
ORDER BY created_at DESC;
```

The web UI fetches the full list, refetches it every 60 seconds, and filters by name **client-side**. As an org accumulates tasks this becomes increasingly expensive: a growing payload on every poll, all rows rendered into a single table, and a query with no bound. The task pages and the `ListTasks` RPC should be paginated.

## Design

### Overview

Add **keyset (cursor) pagination** to `ListTasks`. The request gains a page size and an opaque page token; the response returns a `next_page_token`. The web UI switches to `useInfiniteQuery` with a "Load more" control.

The name search box currently on the task page is **removed**. Its client-side filtering only matches already-loaded pages once the list is paginated, and the server-side alternative is an un-indexed `ILIKE` scan over the org's whole task history in the worst case — see [Trade-offs](#trade-offs).

Pagination mechanics are owned entirely by the **store**. `Store.ListTasksPage` accepts the request's `page_size`/`page_token` verbatim and returns a page of tasks plus the next token; the cursor type, token encoding, page-size bounds, and the "fetch one extra, trim" step are all behind the store boundary. The RPC handler passes fields through and maps errors — it does not know the pagination is keyset-based at all.

Keyset pagination (rather than `LIMIT`/`OFFSET`) is chosen because tasks are ordered `created_at DESC` and new tasks are continually inserted at the top while the UI polls. Offset pagination would skip or duplicate rows whenever a task is created between page loads. A cursor anchored to a specific row is stable against inserts. See [Trade-offs](#trade-offs).

### 1. Proto Definitions

`proto/gritz/v1/gritz.proto` — replace the empty request and extend the response:

```protobuf
message ListTasksRequest {
  int32 page_size = 1;     // Max tasks to return (default: 50, max: 100)
  string page_token = 2;   // Opaque cursor from a previous next_page_token; empty for the first page
}

message ListTasksResponse {
  repeated Task tasks = 1;
  string next_page_token = 2;  // Token for the next page; empty when there are no more results
}
```

The `page_token` is opaque to clients. The server encodes the keyset of the last returned row — its `created_at` and `id` — as a base64 string. Clients must treat it as a blob and pass it back verbatim, matching the convention used by Google AIP-158-style pagination.

### 2. Pagination Package

The mechanical parts — page-size validation, token encode/decode, and the "fetch one extra, trim, build next token" dance — live in a small generic package, **`internal/pagination`**, consumed by the store. Its exported surface is deliberately tiny: a `Config`, a `Page[T]` result, one sentinel error, a `Source[T, C]` interface, and a single `List` entry point that runs a whole page end-to-end against a `Source`. Per-store-method code only supplies the things that are genuinely query-specific: the **cursor type** and a small `Source` implementation providing the query and the row-to-cursor mapping.

The package is storage- and proto-agnostic — it deals in plain ints, strings, and generic type parameters, so it has no dependency on `sqlc`, `connect`, or the proto package.

```go
// internal/pagination/pagination.go
package pagination

import (
    "cmp"
    "context"
    "encoding/base64"
    "encoding/json"
    "errors"
    "fmt"
)

// ErrInvalidRequest reports a bad page size or an undecodable page token.
// RPC handlers map it to connect.CodeInvalidArgument.
var ErrInvalidRequest = errors.New("invalid page request")

// Config bounds the page size for a paginated list.
type Config struct {
    Default int // size used when the request omits page_size (0)
    Max     int // largest size a caller may request
}

// Page is one page of results plus the opaque token for the next page.
type Page[T any] struct {
    Items     []T
    NextToken string // empty when there are no more results
}

// Source supplies the query-specific parts of a keyset-paginated list:
// how to fetch a bounded slice of rows after a cursor, and how to derive
// a cursor from a returned row.
type Source[T, C any] interface {
    // Query fetches up to limit rows that sort after cursor.
    // A nil cursor means the first page.
    Query(ctx context.Context, cursor *C, limit int32) ([]T, error)
    // Cursor returns the keyset of row; it is what page tokens encode.
    Cursor(row T) C
}

// List runs one page of a keyset-paginated query against src. It validates
// pageSize against cfg, decodes pageToken into a cursor of type C (nil on
// the first page), and calls src.Query with the cursor and a limit of
// size+1. If the extra row came back there is a next page: the row is
// trimmed and NextToken is encoded from src.Cursor(last returned row);
// otherwise NextToken is empty.
func List[T, C any](ctx context.Context, cfg Config, pageSize int32, pageToken string, src Source[T, C]) (*Page[T], error) {
    size := cmp.Or(int(pageSize), cfg.Default)
    if size < 1 || size > cfg.Max {
        return nil, fmt.Errorf("%w: page_size must be between 1 and %d", ErrInvalidRequest, cfg.Max)
    }
    var cursor *C
    if pageToken != "" {
        c, err := decode[C](pageToken)
        if err != nil {
            return nil, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
        }
        cursor = &c
    }
    items, err := src.Query(ctx, cursor, int32(size+1))
    if err != nil {
        return nil, err
    }
    page := &Page[T]{Items: items}
    if len(items) > size {
        page.Items = items[:size]
        token, err := encode(src.Cursor(page.Items[size-1]))
        if err != nil {
            return nil, err
        }
        page.NextToken = token
    }
    return page, nil
}

func encode[C any](c C) (string, error) {
    b, err := json.Marshal(c)
    if err != nil {
        return "", err
    }
    return base64.URLEncoding.EncodeToString(b), nil
}

func decode[C any](token string) (C, error) {
    var c C
    b, err := base64.URLEncoding.DecodeString(token)
    if err != nil {
        return c, err
    }
    err = json.Unmarshal(b, &c)
    return c, err
}
```

The same `List` call backs any future paginated store method (`ListEventsPage`, `ListLogsPage`, …) — each one adds only its cursor struct and `Source` implementation.

### 3. SQL Query

`internal/store/sql/queries/task.sql` — add a tiebreaker to the ordering, a keyset predicate, and a limit. The existing `ListTasks` query is retained (callers that want every task continue to use the unpaged path; see [Other callers](#5-other-callers)):

```sql
-- name: ListTasksPage :many
SELECT id, name, runner, workspace, status, command, version, org_id, archived, created_at, updated_at, auto_archive
FROM tasks
WHERE archived = FALSE
  AND org_id = sqlc.arg(org_id)
  AND (
    NOT sqlc.arg(use_cursor)::bool
    OR (created_at, id) < (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::bigint)
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit);
```

Notes:

- `ORDER BY created_at DESC, id DESC` makes the sort deterministic. The current query orders by `created_at DESC` only; the `id` tiebreaker is required for a correct keyset.
- The `use_cursor` boolean lets the first page (no token) skip the keyset predicate cleanly within a single query.
- The store fetches `page_size + 1` rows. If `page_size + 1` rows come back, there is a next page: the extra row is dropped from the result and the `next_page_token` is built from the keyset of the **last returned** row. Otherwise `next_page_token` is empty. This happens inside `pagination.List`; neither the SQL nor the handler deals with it.
- A partial index supports the keyset scan:

```sql
CREATE INDEX tasks_org_created_id_idx
  ON tasks (org_id, created_at DESC, id DESC)
  WHERE archived = FALSE;
```

### 4. Store Layer

`internal/store/task.go` — the paged variant lives alongside the existing `ListTasks` and owns everything pagination-related: the cursor type, the page-size bounds, and the token format. Its params mirror the proto's pagination fields as plain ints and strings, so the RPC handler can pass them through untouched.

The keyset is `(created_at, id)` because `created_at` alone is not unique — two tasks can share a timestamp, so `id` is the tiebreaker. The store implements `pagination.Source` with an unexported `taskSource` type that carries the `Store`, the transaction, and the request params as fields. Both `taskCursor` and `taskSource` are unexported: no one outside the store ever sees a cursor, only opaque tokens.

```go
// taskCursor is the keyset a task page token encodes. created_at is not
// unique, so id is the tiebreaker.
type taskCursor struct {
    CreatedAt time.Time `json:"c"`
    ID        int64     `json:"i"`
}

var listTasksPaging = pagination.Config{Default: 50, Max: 100}

type ListTasksPageParams struct {
    OrgID     int64
    PageSize  int32  // 0 means the default (50); max 100
    PageToken string // opaque token from a previous page; empty for the first page
}

// taskSource implements pagination.Source for the tasks table.
type taskSource struct {
    store  *Store
    tx     *sql.Tx
    params ListTasksPageParams
}

func (src taskSource) Query(ctx context.Context, cursor *taskCursor, limit int32) ([]*model.Task, error) {
    args := sqlc.ListTasksPageParams{
        OrgID:     src.params.OrgID,
        UseCursor: cursor != nil,
        PageLimit: limit,
    }
    if cursor != nil {
        args.CursorCreatedAt = cursor.CreatedAt
        args.CursorID = cursor.ID
    }
    rows, err := src.store.q(src.tx).ListTasksPage(ctx, args)
    if err != nil {
        return nil, err
    }
    return toModelTasks(rows)
}

func (src taskSource) Cursor(t *model.Task) taskCursor {
    return taskCursor{CreatedAt: t.CreatedAt, ID: t.ID}
}

func (s *Store) ListTasksPage(ctx context.Context, tx *sql.Tx, p ListTasksPageParams) (*pagination.Page[*model.Task], error) {
    return pagination.List(ctx, listTasksPaging, p.PageSize, p.PageToken, taskSource{store: s, tx: tx, params: p})
}
```

A bad `PageSize` or an undecodable `PageToken` surfaces as a wrapped `pagination.ErrInvalidRequest`; query failures surface as-is. This is the store's only pagination-specific error contract with callers.

The existing `ListTasks(ctx, tx, orgID)` method and its SQL query are retained for internal callers that legitimately need every task (see below).

### 5. Other callers

`ListTasks` (store) is also used outside the web-facing RPC. These callers must keep fetching the full set and are **not** migrated to the paged query:

- `ListTasksForRunner` / runner reconciliation — needs all tasks for a runner, not a page.
- Any background reconciliation or cleanup loops.

Audit of `store.ListTasks` callers is part of implementation; the rule is: only the `GritzService.ListTasks` RPC handler switches to `ListTasksPage`.

### 6. Server Handler

With the store owning pagination, the handler is pure plumbing: check the scope, pass the request fields through, map errors, convert to proto. It never sees a cursor, a limit, or a token's contents.

```go
func (s *Server) ListTasks(ctx context.Context, req *gritzv1.ListTasksRequest) (*gritzv1.ListTasksResponse, error) {
    caller := apiauth.MustCaller(ctx)
    if !caller.Scopes.Allow(authscope.OpTaskRead) {
        return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot list tasks"))
    }

    page, err := s.store.ListTasksPage(ctx, nil, store.ListTasksPageParams{
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

    resp := &gritzv1.ListTasksResponse{
        Tasks:         make([]*gritzv1.Task, len(page.Items)),
        NextPageToken: page.NextToken,
    }
    for i, t := range page.Items {
        resp.Tasks[i] = t.Proto(s.baseURL)
    }
    return resp, nil
}
```

The `errors.Is` check against `pagination.ErrInvalidRequest` is the one place the handler acknowledges pagination exists: bad page sizes and undecodable tokens become `CodeInvalidArgument`, everything else `CodeInternal`.

### 7. Web UI

`webui/src/routes/tasks.index.tsx` switches from `useQuery` to `useInfiniteQuery` (connect-query supports it via `useInfiniteQuery` with `pageParamKey: 'pageToken'` and `getNextPageParam`).

```tsx
const {
  data,
  isLoading,
  error,
  fetchNextPage,
  hasNextPage,
  isFetchingNextPage,
  refetch,
} = useInfiniteQuery(
  listTasks,
  { pageSize: 50 },
  {
    pageParamKey: 'pageToken',
    getNextPageParam: (lastPage) => lastPage.nextPageToken || undefined,
    refetchInterval: 60000,
  },
)

const tasks = data?.pages.flatMap((p) => p.tasks) ?? []
```

Changes:

- **Search box removed.** The search input and the client-side `allTasks.filter(...)` behind it (`tasks.index.tsx`) are deleted. Once the list is paginated, client-side filtering silently matches only the loaded pages, which is worse than no search at all; the server-side alternative was rejected (see [Trade-offs](#trade-offs)).
- **"Load more" control.** A button below the table calls `fetchNextPage()` and is shown while `hasNextPage` is true, disabled while `isFetchingNextPage`. (Infinite scroll via an intersection observer is a possible follow-up; a button keeps the first cut simple.)
- **Polling.** `refetchInterval: 60000` with `useInfiniteQuery` refetches all currently loaded pages, preserving the live-update behavior. Because pagination is keyset-based, refetched pages remain stable even as new tasks are inserted at the top — newly created tasks appear when the first page is refetched.

No other RPC clients consume `ListTasksResponse` in a way that breaks: adding `next_page_token` is backward compatible, and existing callers that send an empty request get `page_size = 0 → default 50`.

### 8. Implementation Order

1. `internal/pagination` package (`Config`, `Page`, `Source`, `ErrInvalidRequest`, `List`) + unit tests — independent of everything else, can land first.
2. Proto changes + regenerate (`mise run generate` for Go, `pnpm` buf generate for webui).
3. SQL migration: add `tasks_org_created_id_idx`; add the `ListTasksPage` query; `sqlc` generate.
4. Store layer: `taskCursor`, `taskSource`, `ListTasksPageParams`, `ListTasksPage`.
5. Server handler: pass-through wiring + `errors.Is` mapping.
6. Web UI: `useInfiniteQuery`, remove the search box, "Load more".
7. Tests (package round-trip + bounds; store-level keyset correctness and token round-trip driven entirely through `ListTasksPage`; handler validation).

## Trade-offs

### Keyset (cursor) vs offset/limit

**Chosen: keyset.** Tasks are ordered newest-first and inserted continuously while the UI polls every 60s. With `OFFSET`, inserting a task at the top shifts every subsequent row down by one, so page 2 would repeat the last item of page 1 (or skip one). Keyset anchors each page to a concrete `(created_at, id)` and is immune to inserts above the cursor. The cost is that random page access ("jump to page 7") is not supported — acceptable for a task list where users scan recent work and "Load more" is the primary navigation mode.

### Dropping search vs moving it server-side

**Chosen: drop the search box.** The task page currently has a name search input backed by client-side filtering of the fully-fetched list. Pagination breaks it: the filter would only see already-loaded pages, so a search for an older task silently shows nothing even when matches exist — worse than no search. The obvious fix, a server-side `filter` field implemented as `name ILIKE '%...%'`, was considered and rejected: a leading-wildcard match cannot use a B-tree index, so a term matching few or no rows degenerates into scanning the org's entire task history on every (debounced) keystroke. The only real index support is a `pg_trgm` trigram GIN index, which drags in an extension and write-side cost to serve an occasional query.

Rather than ship either a misleading search or an unbounded scan, this proposal removes the search input alongside pagination. Search can return later as a backward-compatible proto change (a new `filter` request field) backed by `pg_trgm` so it never scans; an earlier revision of this document contains the full server-side design if that's picked up.

### Simple `limit` (the `ListExternalEvents` convention) vs full pagination

`ListExternalEvents` uses a bare `limit` with no cursor — it only ever exposes the latest N events and has no "next page". That is insufficient here: the issue asks for the task **pages** to be paginated, i.e. the ability to page through the entire history, not just cap the first slice. Hence the richer `page_token`/`next_page_token` contract. The validation shape (default + max + `CodeInvalidArgument`) still mirrors the existing convention.

### Store-owned pagination vs handler-owned

**Chosen: the store owns pagination end-to-end.** An earlier revision had the handler drive the mechanics — `pagination.Parse` before the store call, `pagination.Result` after — with the store exposing raw `Cursor`/`Limit` params. That leaks storage detail: the page token encodes a keyset, and the keyset is the store's ordering (`created_at DESC, id DESC`) — a fact of the SQL, not of the RPC. With the store owning it, `ListTasksPage` takes exactly the proto's pagination fields (as plain ints/strings, so the store still imports no proto or connect) and returns items plus the next token. If the keyset, sort order, or token format ever changes, only the store changes; the handler and any future callers (e.g. the MCP server's task listing) are untouched, and it is impossible for a caller to construct an inconsistent cursor/limit combination.

The costs are minor: the store must distinguish caller mistakes from internal failures, hence the `pagination.ErrInvalidRequest` sentinel; and the store's params adopt proto conventions (`0` → default page size). Both are contained in one params struct and one `errors.Is` check.

### Reusable package vs open-coding in the store

**Chosen: an `internal/pagination` package.** Token encode/decode, page-size bounds, and the over-fetch-and-trim step are mechanical and identical for every keyset-paginated store method. Open-coding them in `ListTasksPage` invites copy-paste drift when the next method (`ListEventsPage`, `ListLogsPage`) is paginated. The generic `List` helper keeps each store method down to a `Source` implementation and a one-line `List` call; only the cursor struct and the two `Source` methods are written per method. The trade-off is a generics-heavy helper for what is, today, a single caller — acceptable since the package is ~80 lines and the issue's framing ("the task **pages**") plus the un-paginated `ListExternalEvents` both point at more paginated lists soon.

Placement: `internal/pagination` as proposed. Now that the store is its only consumer, `internal/store/pagination` is also defensible, as is `internal/x/pagination` to match the utility convention — reviewer's call.

### `Source` interface vs closure parameters

**Chosen: a `Source[T, C]` interface.** An earlier revision passed the query and the row-to-cursor mapping to `List` as two positional function parameters. The interface names that contract instead: the implementation is a small struct (`taskSource`) whose dependencies — the `Store`, the transaction, the request params — are explicit fields rather than variables captured by closures, `ctx` flows through `Query` explicitly instead of being closed over, and the two methods carry their documentation with them. The `List` call site shrinks to a single argument, and a `Source` can be exercised on its own (e.g. testing `Query`'s keyset behavior against a real database) without going through `List`. The cost is a few lines of struct boilerplate per paginated list.

### "Load more" vs numbered pages vs infinite scroll

**Chosen: "Load more" button.** Numbered pages require total counts and offset access, which keyset pagination doesn't provide cheaply. A "Load more" button is the simplest control that fits cursor semantics; infinite-scroll-on-view can layer on later without an API change.

## Open Questions

1. **Default page size.** Proposed 50 (max 100). Is 50 the right first-screen size for the table, or should it match a typical viewport more tightly (e.g. 25)?
2. **Archived tasks.** The list excludes archived tasks today. Out of scope here, but if a future "show archived" toggle is added, it would become another request field that changes which rows the keyset scan visits — worth keeping the cursor format flexible (the JSON blob already is).
3. **Total count.** Should the response include an approximate total (e.g. for a "showing X of ~Y" label)? An exact `COUNT(*)` defeats some of the pagination win; an estimate via `pg_class.reltuples` is possible but org-filtered counts complicate it. Left out unless the UI needs it.
4. **Search and filtering follow-up.** Removing the search box is a small UX regression for users who relied on it. If it's missed, the follow-up is a server-side `filter` request field backed by a `pg_trgm` trigram index (and possibly status filters alongside it) — a backward-compatible proto change. Does that need to be scheduled together with this work, or wait for demand?
