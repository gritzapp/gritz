# Link Routing URL

Issue: https://github.com/icholy/gritz/issues/810

## Problem

Event routing matches events to tasks by **exact string equality** on a URL. `FindSubscribedLinksForOrgs` (`internal/store/sql/queries/link.sql`) runs `WHERE l.url = $1 ...`, and `eventrouter.Router.Route` feeds it `input.URL` verbatim. So the URL an agent stores on a link must be byte-identical to the URL a future webhook produces.

To make that hold, the webhook handlers deliberately collapse every event to its *parent* resource URL (`internal/server/githubserver/webhook.go`):

| Event | URL stored today |
|---|---|
| `issue_comment` | `event.Issue.HTMLURL` (the issue/PR, **not** the comment) |
| `pull_request_review_comment` | `event.PullRequest.HTMLURL` (the PR, **not** the comment) |
| `pull_request_review` | `event.PullRequest.HTMLURL` (the PR, **not** the review) |

An event triggered by a specific comment therefore links to the whole issue, and the agent has to be steered toward the one URL form that will match. In practice agents also store **API URLs** (e.g. `api.github.com/repos/o/r/issues/5`) because that is what the JSON responses from GitHub/Jira MCP servers return, and those never match the web URLs the webhooks produce.

The issue proposes giving links (and events) **two URLs**: the original expressive `url`, and a `routing_url` derived from it that is used solely for routing.

## Design

### Core idea

Add a `routing_url` column to `task_links`, derived from `url` by `model.RoutingURL`. Routing matches on the link's `routing_url` instead of its `url`. `url` stays expressive and user-facing; `routing_url` is an internal key.

The `events` table does **not** get a `routing_url` column. Routing matches on the *link* side, and nothing queries events by URL (the only such query, `FindEventsByURL`, is removed — see Cleanup). The router still derives a routing key to look up links, but it doesn't need to persist that key on the event; the event keeps its expressive `url` for display.

`RoutingURL` only normalizes URLs it recognizes (GitHub issue/PR and Jira issue, in both their **web and API** forms). Everything else is returned unchanged — there is no general-purpose normalization.

### 1. Routing URL derivation

New code in `internal/model/url.go` (which already houses `TaskURL`):

```go
// RoutingURL reduces a recognized resource URL to a stable routing key, so two
// URLs that point at the same logical resource — a comment vs. its issue, or an
// API URL vs. its web URL — produce the same key. Only recognized URLs are
// normalized; anything else is returned unchanged.
//
//   github.com/o/r/issues/5#issuecomment-9            -> github.com/o/r/issues/5
//   github.com/o/r/pull/5/files                       -> github.com/o/r/pull/5
//   api.github.com/repos/o/r/issues/5                 -> github.com/o/r/issues/5
//   api.github.com/repos/o/r/pulls/5                  -> github.com/o/r/pull/5
//   site.atlassian.net/browse/X-1?focusedCommentId=2  -> site.atlassian.net/browse/X-1
//   site.atlassian.net/rest/api/2/issue/X-1           -> site.atlassian.net/browse/X-1
func RoutingURL(raw string) string
```

GitHub, web host `github.com`: path `/{owner}/{repo}/{issues|pull}/{n}[/...]` → `https://github.com/{owner}/{repo}/{kind}/{n}`. Collapses `#issuecomment-…`, `#discussion_r…`, `#pullrequestreview-…`, `/files`, `/commits/…`. Issues and pull requests stay distinct (`/issues/5` ≠ `/pull/5`).

GitHub, API host `api.github.com`: path `/repos/{owner}/{repo}/{issues|pulls}/{n}` → the same web key (`issues`→`/issues/`, `pulls`→`/pull/`). API comment URLs that don't embed the parent number (`/repos/o/r/issues/comments/{id}`) can't be reduced and are returned unchanged — noted as a limitation.

Jira, web `/browse/{KEY}[?…]` → `https://{host}/browse/{KEY}`; API `/rest/api/{v}/issue/{KEY}` → `https://{host}/browse/{KEY}` (mirrors the existing `Issue.BrowseURL` logic in `internal/x/atlassian/webhook.go`). API URLs that use a numeric issue id rather than the key can't be mapped without a lookup and are returned unchanged.

Anything else: returned unchanged (matches by exact equality, as today).

`RoutingURL` gets a thorough unit test table covering each web/API/comment/fragment form plus non-matching hosts and malformed input.

### 2. Where `routing_url` is set

`routing_url` is **derived in the application/RPC layer, not in the store** — deriving it is domain logic, and the store should only persist what it is handed. So `store.CreateLink` / `store.CreateEvent` simply write the `RoutingURL` field as given.

- **`gritz:create_link` (apiserver).** `Server.CreateLink` (`internal/server/apiserver/link.go`) sets the field when it builds the model:

  ```go
  link := &model.Link{
      TaskID:     req.TaskId,
      Relevance:  req.Relevance,
      URL:        req.Url,
      RoutingURL: model.RoutingURL(req.Url),
      Title:      req.Title,
      Subscribe:  req.Subscribe,
      CreatedAt:  time.Now(),
  }
  ```

- **Event routing (`eventrouter`).** The router already computes the routing key (see §3) and sets it on the auto-created subscribed link before calling the store.

`CreateLinkRequest` itself is unchanged — clients still send only `url`; the server derives `routing_url`.

### 3. `InputEvent.RoutingURL` and the Router

Add a `RoutingURL string` field to `eventrouter.InputEvent`. The webhook handlers populate it directly — they usually **already have** the parent issue/PR URL on hand, so no parsing is needed:

```go
type InputEvent struct {
    // ... existing fields ...
    URL        string // expressive: the comment/review that triggered the event
    RoutingURL string // routing key; webhooks set this from the parent they already have
}
```

`Router.Route` uses `RoutingURL` when the producer supplied it, and only falls back to deriving from `URL` otherwise:

```go
key := input.RoutingURL
if key == "" {
    key = model.RoutingURL(input.URL)
}
linksByOrg, err := r.links(ctx, key, orgIDs)   // FindSubscribedLinksForOrgs(key, ...)
...
event := &model.Event{
    Description: input.Description,
    Data:        input.Data,
    URL:         input.URL,   // the comment/review that triggered it
    OrgID:       orgID,
}
```

`key` is only used to look up links; it is not stored on the event (the `events` table has no `routing_url` column). The auto-created subscribed link in `Router.create`, however, is given the same `key` as its `RoutingURL`.

### 4. Webhook handlers — link to the real trigger

The handlers set `URL` to the actual comment/review and `RoutingURL` to the parent they already extract (`internal/server/githubserver/webhook.go`):

| Event | `InputEvent.URL` | `InputEvent.RoutingURL` |
|---|---|---|
| `issue_comment` | `event.Comment.HTMLURL` | `event.Issue.HTMLURL` |
| `pull_request_review_comment` | `event.Comment.HTMLURL` | `event.PullRequest.HTMLURL` |
| `pull_request_review` | `event.Review.HTMLURL` | `event.PullRequest.HTMLURL` |
| `issues`/`pull_request` assigned | parent URL (no comment) | same parent URL |

These webhook URLs are already canonical, so the handlers can set `RoutingURL` without calling `model.RoutingURL` at all — matching the reviewer's point that webhooks usually already have the routing URL.

Atlassian (`internal/server/atlassianserver/webhook.go`): set `URL` to the comment-focused browse URL when available, and `RoutingURL` to the plain issue browse URL it already builds via `Issue.BrowseURL()`.

### 5. Schema, model, proto

Migration `internal/store/sql/migrations/<ts>_link_routing_url.sql`:

```sql
-- migrate:up
ALTER TABLE task_links ADD COLUMN routing_url TEXT NOT NULL DEFAULT '';

-- Existing rows already stored the canonical parent URL (the old contract),
-- so seeding routing_url = url preserves all current matches.
UPDATE task_links SET routing_url = url WHERE routing_url = '';

CREATE INDEX idx_task_links_routing_url ON task_links (routing_url);

-- migrate:down
DROP INDEX IF EXISTS idx_task_links_routing_url;
ALTER TABLE task_links DROP COLUMN IF EXISTS routing_url;
```

Only `task_links` gets the column. The existing `idx_task_links_url` / `idx_events_url` indexes are dropped, because their only users (`FindLinksByURL` / `FindEventsByURL`) are removed — see Cleanup below.

### 6. Cleanup: remove unused `FindLinksByURL` / `FindEventsByURL`

Both lookups are dead code today and routing no longer needs them, so they are removed rather than carried forward:

- **`FindEventsByURL`** (`store.FindEventsByURL` + the `event.sql` query) has no callers at all — no RPC, no CLI, no test.
- **`FindLinksByURL`** has a store method, an `event`/`link.sql` query, an RPC (`FindLinksByURLRequest`/`Response` + the `apiserver` handler), and generated stubs — but nothing consumes the RPC (not the webui, the CLI, the n8n node, nor the `gritzclient` wrapper). Removing it also drops the `FindLinksByURL` RPC from `proto/gritz/v1/gritz.proto`.

This leaves `FindSubscribedLinksForOrgs` (now matching on `routing_url`) as the only URL-based lookup, and removes the `idx_task_links_url` / `idx_events_url` indexes that backed the deleted queries.

Queries: `CreateLink` inserts `routing_url`; the link `SELECT`s return it; `FindSubscribedLinksForOrgs` matches `WHERE l.routing_url = sqlc.arg(routing_url)`. The `events` queries are unchanged (no `routing_url`). `model.Link` gains a `RoutingURL string` field (with `Proto` / `FromProto` wiring); `model.Event` is unchanged. Proto adds a read-only `string routing_url` to `TaskLink` (field 8) only.

## Trade-offs

- **Derive in the RPC/router layer vs. in the store.** Deriving `routing_url` is domain logic, so it lives with the callers that own the URL (the `CreateLink` RPC handler and the router) rather than in the persistence layer. The store stays a dumb writer. The cost is that each create path must set the field; there are only two.
- **Stored column vs. derive-at-query-time.** Storing `routing_url` keeps matching a single indexed equality lookup; the host-aware Go logic can't be expressed as a Postgres index expression. The cost is a derived column that must be re-derived if the function changes (see Open Questions).
- **Webhook-supplied routing URL vs. always deriving.** The webhooks already carry the parent URL, so they set `RoutingURL` directly and the router skips derivation for events. `model.RoutingURL` is still the fallback (and the path for agent-created links, which is where API URLs show up).
- **Backward compatibility.** Existing rows are backfilled with `routing_url = url`. Because the old contract already required canonical parent URLs, those rows keep matching new events without a Go-side backfill.

## Open Questions

1. **Re-derivation on logic change.** If `RoutingURL` evolves, stored keys can drift. A one-shot backfill command can re-derive existing rows; acceptable to defer.
2. **Unmappable API URLs.** GitHub API comment URLs without the parent number, and Jira API URLs that use a numeric issue id, can't be reduced to the web key without a network lookup. Proposed: leave them unchanged (they simply won't cross-match a web URL). Worth confirming this is acceptable or whether a lookup is warranted.