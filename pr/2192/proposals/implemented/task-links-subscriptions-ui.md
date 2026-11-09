# Task links tab (read-only)

Issue: https://github.com/icholy/gritz/issues/1158

> **Scope note (2026-07):** an earlier draft of this proposal also designed
> *management* — add / remove / toggle-subscribe — backed by new `DeleteLink` and
> `UpdateLink` RPCs. Per maintainer feedback on #1159 ("I just want this to be a
> read-only view for now"), this revision is **read-only**: a Links tab that
> lists a task's links and shows which are subscribed, with **no new API** and no
> mutations. The management design is preserved in [Deferred: management](#deferred-management)
> as a follow-up.
>
> Placement also changed: the task detail page has since grown an in-page tab bar
> (Timeline | Shell), added by "make task shell an in-page tab" (#1184, on top of
> the in-browser shell #1154). The links view lands as a **Links tab** beside
> Timeline and Shell rather than a standalone card.

## Problem

A task accumulates **links** — PRs, issues, Jira tickets, and other external
resources — and some of those links are **subscriptions**. A link with
`subscribe=true` tells the event router to route external events on that URL
back to the task: `eventrouter.Router.Route` normalizes the incoming event URL
to a routing key and calls `store.FindSubscribedLinksForOrgs`, which matches
`task_links` rows where `routing_key = $1 AND subscribe = TRUE`
(`internal/eventrouter/eventrouter.go:109-110`,
`internal/store/link.go:43-67`). So `subscribe` is not cosmetic — it is the
exact switch that decides whether a comment on a PR wakes the task.

Today the only place a task's links appear in the Web UI is the activity
timeline on the task detail page (`webui/src/routes/tasks.$id.tsx`). Each link
is rendered as a `Link created` row (`LinkRow` in
`webui/src/components/task-timeline.tsx`) interleaved with instructions,
reports, and lifecycle events. This makes links **hard to find at a glance**:
the PR/issue links are buried among every other timeline entry, ordered by time,
and there is no single place that answers "what is this task attached to, and
what is it subscribed to?"

This proposal adds a dedicated, **read-only Links tab** to the task detail page
that lists a task's links at a glance and marks which are subscribed. Managing
links (add / remove / toggle-subscribe) is intentionally out of scope for this
iteration — see [Deferred: management](#deferred-management).

## Terminology

The UI should be consistent about two closely-related words:

- **Link** — any external resource associated with a task (`task_links` row).
  Has a `url`, optional `title`, a `relevance` note, and a `subscribe` flag.
- **Subscription** — a link with `subscribe=true`. Not a separate entity; it is
  a link whose flag is on. Events matching its `routing_key` are routed to the
  task.

The tab therefore shows **links**, and a per-link **Subscribed** badge marks the
ones that are subscriptions. We deliberately avoid a separate "Subscriptions"
list — it would split one row across two places and invite the misconception
that a subscription can exist without a link.

## Current state

### Data model

`task_links` (`internal/store/sql/schema.sql`):

```sql
CREATE TABLE public.task_links (
    id          bigint NOT NULL,
    task_id     bigint NOT NULL,
    relevance   text   NOT NULL,
    url         text   NOT NULL,
    title       text   DEFAULT ''::text  NOT NULL,
    subscribe   boolean DEFAULT false    NOT NULL,
    created_at  timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    routing_key text   DEFAULT ''::text  NOT NULL
);
```

`routing_key` is derived from `url` by `model.RoutingKey`
(`internal/model/url.go:32-63`), which canonicalizes recognized GitHub/Jira
URLs (strips `#fragment`, `?query`, normalizes API vs web forms) so a comment
URL, web URL, and API URL for the same resource all match. It is **server-owned
and read-only** to clients (`TaskLink.routing_key` is documented as read-only in
the proto). The read-only tab never surfaces it.

### API surface that exists today — enough for a read-only view

Proto (`proto/gritz/v1/gritz.proto`):

| RPC | Purpose | Notes |
| --- | --- | --- |
| `CreateLink(CreateLinkRequest) → CreateLinkResponse` | add a link | fields: `task_id, relevance, url, title, subscribe` |
| `ListLinks(ListLinksRequest) → ListLinksResponse` | list a task's links | `task_id` in, `repeated TaskLink` out |
| `GetTaskDetails(...) → GetTaskDetailsResponse` | task + events + links | returns `repeated TaskLink links = 4` |

```proto
message TaskLink {
  int64 id = 1;
  int64 task_id = 2;
  string relevance = 3;
  string url = 4;
  string title = 5;
  bool subscribe = 6;
  google.protobuf.Timestamp created_at = 7;
  string routing_key = 8; // read-only, server-derived
}
```

`GetTaskDetails` already returns everything a read-only tab needs — `id`, `url`,
`title`, `relevance`, `subscribe`, `created_at` per link. **No proto or handler
changes are required for this iteration.** (Read handlers: `ListLinks` in
`internal/server/apiserver/link.go` gates on `OpTaskRead`; `GetTaskDetails`
returns the same links.)

### How the Web UI fetches and stays live

- The task detail page calls `useQuery(getTaskDetails, { id }, { refetchInterval: 60000 })`
  and `useQuery(listEventsByTask, { taskId }, ...)`
  (`webui/src/routes/tasks.$id.tsx`). `getTaskDetails` already returns `links`,
  but the page currently ignores that field and renders links only from the
  events timeline.
- The page already has an **in-page tab bar** inside the details card. `#1184`
  introduced local `const [tab, setTab] = useState<TabKey>('timeline')` with
  `type TabKey = 'timeline' | 'shell'`, a reusable `TabButton` component (an
  underline-style tab that takes an `icon`, `label`, and either a `count` badge
  or an "active" `dot`), and a tab bar that renders `<TabButton>` for Timeline
  (with a `count` of timeline items) and Shell (with a `dot` when a shell session
  is active). Each tab's body is a `{tab === '…' && (…)}` block. Adding a Links
  tab means extending `TabKey`, adding one `<TabButton>`, and adding one
  conditional body — no new layout machinery.
- Live updates: `useOrgSSE` (`webui/src/hooks/use-org-sse.ts`) listens to the
  server's SSE notification stream and invalidates TanStack Query keys per
  resource. The `task_links` case already invalidates `getTaskDetails`
  (`use-org-sse.ts`, `case 'task_links'`). So when the agent creates a link via
  the `create_link` MCP tool (which publishes a `task_links` `change`), the tab
  refreshes on every connected client for free.
- Source-icon helpers already exist: `sourceFromUrl` / `ExternalSource`
  (`github` / `jira` / `other`) in `webui/src/lib/timeline.ts`, used by the
  timeline's `LinkRow`; the tab reuses them.
- shadcn/ui components already in `webui/src/components/ui/`: `Card`, `Badge`,
  `Tooltip` — enough to build a read-only list with no new primitives.

## Design

### 1. UI placement — a Links tab in the existing tab bar

Add a **Links** tab to the in-page tab bar in `tasks.$id.tsx`, beside Timeline
and Shell, rather than a separate route or a standalone card.

Concretely:

- Extend `type TabKey = 'timeline' | 'shell'` to `'timeline' | 'shell' | 'links'`.
- Add a third `<TabButton>` with a links icon (e.g. `Link2` from `lucide-react`,
  already used by `LinkRow`), the label **Links**, and a `count` of the task's
  links — so the tab shows "Links 3" at a glance, mirroring Timeline's count.
- Add a `{tab === 'links' && (<TaskLinksTab links={data.links} />)}` body that
  renders the read-only list described below, reading from `getTaskDetails.links`.

Rationale:

- The tab bar already exists and is the page's established pattern for "switch
  between views of this task without leaving the page". A links view belongs
  there for the same reason the shell does — it is one more facet of the task,
  not a separate destination.
- Links are inherently *of a task*; a dedicated `/tasks/$id/links` route would
  cost a navigation away from the page and a separate fetch, whereas the data
  (`getTaskDetails.links`) is already loaded here.
- The tab's `count` badge answers "what is this task attached to, and how much?"
  from the bar itself, so links are discoverable even before the tab is opened.

### 2. Layout — a read-only list

The **Links** tab body renders one row per link, newest first (or grouped
subscribed-first — see open questions). Every row is display-only: no buttons,
no toggles, no inputs.

```
│ Timeline  ·  Shell  ·  Links 2                                      │  ← in-page tab bar
├────────────────────────────────────────────────────────────────────┤
│  ○ fix: close operator shell leg                     [ subscribed ] │  ← github icon, title links to url
│    github.com/icholy/gritz/pull/1149                               │
│    Opened to resolve the exit hang                                  │  ← relevance, muted
│                                                                     │
│  ○ PROJ-42  Investigate flaky test                                  │  ← jira icon, no badge (not subscribed)
│    acme.atlassian.net/browse/PROJ-42                                │
```

Per link, show:

- **Source icon** derived from the URL, reusing `sourceFromUrl` / `ExternalSource`
  from the timeline (`github` / `jira` / `other`).
- **Title** (falls back to the URL when empty) as an external `<a>` opening in a
  new tab — same treatment as `LinkRow` today.
- **URL**, muted, below the title.
- **Relevance** note, muted, when present.
- **Subscribed** badge — shown only when `subscribe` is true, reusing the exact
  read-only "subscribed" `Badge` the timeline's `LinkRow` already renders
  (`item.subscribed && <Badge …>subscribed</Badge>`). A tooltip explains what it
  means ("Events on this URL wake the task"). It is a **status indicator, not a
  control** in this iteration.

Empty state: "No links yet. The agent adds links (PRs, issues, tickets) with the
`create_link` tool as it works." — read-only, so it explains where links come
from rather than offering an add button.

The existing timeline `LinkRow` **stays** — it is the historical "a link was
created at time T" record and reads naturally in the narrative. The new tab is
the current-state view of the same underlying rows. (Divergence is not a concern:
the tab reflects `getTaskDetails.links`, the timeline reflects the append-only
`link` events; both already share one source via `CreateLink`'s transaction.)

### 3. Data source and live updates

The tab binds directly to the **`getTaskDetails.links`** array the page already
fetches — no second `listLinks` query, one fewer round-trip and cache key. When
the agent creates a link, `CreateLink` publishes a `task_links` `change`
notification, `use-org-sse.ts` invalidates `getTaskDetails`, and the tab (and its
`count` badge) refresh with no tab-specific wiring. The 60 s `refetchInterval`
remains the fallback. Because the view is read-only, there are no optimistic
updates or rollback paths to reason about.

## Scope

**In scope:**

- A read-only **Links tab** on the task detail page: list links from
  `getTaskDetails.links`, source icon, title/url/relevance, and a read-only
  **Subscribed** badge; a `count` on the tab button; empty state.
- Reuse of existing helpers (`sourceFromUrl`) and components (`Badge`,
  `TabButton`). **No proto, handler, or store changes.**

**Out of scope (this iteration):**

- **All mutation:** adding, removing, or toggling subscription on a link, and the
  `DeleteLink` / `UpdateLink` RPCs that would back them. Deferred below.
- Editing a link's `url`, `title`, or `relevance`.
- **Showing which events matched a subscription.** That information already lives
  in the timeline as `external` events; correlating an event back to the specific
  link that routed it is a bigger feature (the router attaches by routing key, not
  by link id). A future "N events" affordance per link is noted below.
- Redesigning event matching / routing keys.

## Deferred: management

The read-only tab is a deliberate first step. When management is wanted, it slots
into the same tab with a well-understood API shape (this is the earlier draft's
design, kept for reference):

- **Add link** — a dialog calling the existing `CreateLink` RPC (URL required;
  optional title/relevance; a `Subscribed` default-on switch). No new API needed.
- **Remove link** — a new `DeleteLink(id, task_id)` RPC. Notably `store.DeleteLink`
  (`internal/store/link.go:39`, `DELETE FROM task_links WHERE id = $1`) already
  exists and is unwired; the handler just calls it inside `WithTx` and publishes
  the `task_links` `change`. A confirm dialog is warranted because removing a
  subscription silently stops event routing.
- **Toggle subscribe** — a new `UpdateLink(id, task_id, optional subscribe, …)`
  RPC with `optional` scalar fields as a minimal field-mask (so a toggle sends
  only `subscribe`). This must be an in-place update, **not** delete+recreate:
  delete+recreate would lose `created_at`, double-log the timeline, and briefly
  drop the subscription, whereas the routing key is unchanged by a toggle so only
  the boolean flips. `url` stays immutable (changing it is a different resource).

Both new RPCs gate on `OpTaskWrite` exactly like `CreateLink`, and publish the
same `task_links` `change` so the tab stays live. Because the read-only tab
already binds to `getTaskDetails.links` and reuses the SSE invalidation, adding
management later is additive: the row grows controls, the list gains an add
button, and no data-flow is rearchitected.

## Trade-offs

- **A tab vs. an always-visible card vs. a dedicated route.** A route isolates
  the feature but costs a navigation and a separate fetch. An always-visible card
  keeps links on screen at all times but is now the odd-one-out on a page that
  organizes Timeline and Shell as tabs, and it would compete with the timeline for
  vertical space. A tab matches the page's existing pattern, reuses already-fetched
  data, and keeps links discoverable via the tab's `count` badge. Chosen: tab.
- **Read-only now vs. full management now.** Shipping read-only first delivers the
  "find links at a glance" value with zero API risk and no new mutation paths to
  test, and lets the tab's layout settle before controls are added. The cost is a
  second iteration for management — accepted, per maintainer preference.
- **Reading `getTaskDetails.links` vs. a dedicated `listLinks` query.** Reusing the
  page's existing query avoids a second fetch and a second invalidation path; the
  trade-off is coupling the tab to `getTaskDetails`, acceptable since they share a
  page and a notification resource.
- **Keeping the timeline `LinkRow` vs. removing it.** Removing it would avoid two
  representations of a link, but the timeline row is a genuine historical event
  ("added at T") while the tab is current state. They answer different questions;
  both stay.

## Open questions

1. **Ordering / grouping.** List newest-first, or group subscribed links first?
   Subscribed-first surfaces the routing-relevant links, but newest-first matches
   the timeline's mental model. Leaning: newest-first for v1, revisit if noisy.
2. **Many-links UX.** A tab scrolls independently, so a long list is less of a
   problem than an always-visible card. Still, at some count (10? 20?) the tab may
   want search/sort. Not built up front.
3. **Per-link event count.** Surfacing "N events routed here" per subscription is
   appealing but requires correlating `external` events to a link. Worth a
   follow-up once the router can attribute an event to the link id that matched.
4. **When to add management.** This proposal defers add/remove/toggle. Should the
   follow-up be a single PR (both RPCs + full controls) or split (add first, then
   remove/toggle)? See [Deferred: management](#deferred-management).