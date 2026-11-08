---
title: "Remove `GetTaskDetails`; consumers compose from the primitives"
description: "GetTaskDetails bundles three reads already exposed as their own RPCs, so its largest consumer, the web UI, fetches events it discards while the agent needs a different slice."
---

Issue: https://github.com/icholy/gritz/issues/1426

## Problem

`GetTaskDetails` (`internal/server/apiserver/task.go:188`) is a thin server-side
aggregator that bundles three store reads already exposed as their own RPCs:

```go
task   := store.GetTask(id)                                     // the fat Task header
events := store.ListEventsByTask(id, [instruction, external])   // the to-agent slice
links  := store.ListLinksByTask(id)
return &GetTaskDetailsResponse{Task: task, Events: events, Links: links}
```

Every piece it composes is already on the service: `GetTask`,
`ListEventsByTask` (paginated, type-filterable), `ListLinks`. The aggregator
adds nothing but a fixed bundling of the three, and it serves audiences that
want different slices — so it does wasted work for its largest consumer.

**The smell — the webui fetches events it discards.**
`webui/src/routes/tasks.$id.tsx:82` polls `GetTaskDetails` every 60s but reads
only `data.task` and `data.links` (`tasks.$id.tsx:169-170`); it **never touches
`data.events`**. The activity timeline is a *separate* bidirectional paginated
query (`useTaskTimeline` → `ListEventsByTask`, `use-task-timeline.ts:51`). So
every poll loads and serializes the instruction/external slice and the webui
throws it away — the careful pagination the timeline does is undercut by the
aggregator dumping the slice anyway.

This supersedes the now-closed #1406 (event-native reshape): removing the RPC
removes the `instructions`-projecting adapter outright, and every consumer reads
instruction-arm events straight from `ListEventsByTask`. No shared-renderer
abstraction — that was rejected on the prior effort.

### Settled constraints (carried in, not re-litigated)

- **`Task` stays fat.** The webui detail page and the runner need the full
  message; do not slim it.
- **No backward compatibility.** Early dev, no external users. Reshape in place
  and migrate every consumer in the same effort — no dual-shape window.
- **No shared cross-package renderer.** `get_my_task` emits JSON; the driver
  renders markdown via `agentprompt`; `mcpserver.getTask` emits its own struct.
  They compose the same primitives but keep their own presentation.

## Design

Delete the `GetTaskDetails` rpc, its handler, its request/response messages, and
the generated code; migrate each consumer to call `GetTask` / `ListEventsByTask`
/ `ListLinks` directly. `GetTaskDetailsResponse` is the *only* message that
carries the `events`+`links` bundle, so removing it breaks nothing else — every
field it held is reachable through a primitive that survives.

### Consumer-by-consumer migration

Verified against the current tree. Each row states what the consumer needs today
and the exact primitive calls that replace its `GetTaskDetails` call.

#### 1. `get_my_task` — `internal/agentmcp/xmcp.go`

**Today** (`xmcp.go:108-115`, `taskDetailsToMap` at `:154-191`): one
`GetTaskDetails` call, then renders `{id, name, status, workspace, namespace,
url, instructions, links, events}` — where `instructions` is the instruction
arm filtered back out of `events`, and `events` is the raw instruction+external
slice.

**After:** three calls, composed inline in the handler (this is the in-container
`agentmcp` process — a distinct client from the driver):

```go
task,   _ := s.client.GetTask(ctx, &gritzv1.GetTaskRequest{Id: s.task.ID})
events, _ := s.client.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{
    TaskId: s.task.ID,
    Types:  []string{model.EventTypeInstruction, model.EventTypeExternal}, // the brief
})
links,  _ := s.client.ListLinks(ctx, &gritzv1.ListLinksRequest{TaskId: s.task.ID})
```

The output goes **fully event-native** — this is the #1406 motivation this
effort subsumes. `taskDetailsToMap` **drops the synthesized `instructions`
key**: the output becomes `{id, name, status, workspace, namespace, url, links,
events}`, where `events` is the raw instruction+external slice and the agent
reads instruction-arm events straight from it. The `Types` filter reproduces the
brief the handler used to compute server-side (`task.go:205-206`).
`ListEventsByTask` with no page fields takes the legacy unpaged path (all
matching events, ascending — `event.go:100-108`), which is what the brief wants.
No synthesized `instructions` array — re-bucketing the instruction arm back out
of the stream is exactly what #1406 set out to remove, so the projection is
deleted, not relocated. (This is **not** byte-identical to today's output; the
`instructions` key is gone.)

#### 2. driver / first-run brief — `internal/agent/driver.go` + `agentprompt`

**Today** (`driver.go:189-195`, `:216-233`): on the *first run only*, the driver
calls `GetTaskDetails` and uses `details.GetEvents()` for `promptEvents` and
`details.GetLinks()` for `promptLinks`. A wake run leaves `details` nil and
renders the drained events instead.

**The key finding: the driver already holds everything except links.**

- The **task header** is already fetched at the top of every run
  (`driver.go:70`, `d.Client.GetTask`) and threaded into `runAgent` as `task`
  (`:133`, `:148`, used at `:230`). `GetTaskDetails` re-fetched the same `Task`
  redundantly.
- The **brief events** are already drained. `drainEvents` (`:299-317`) pages
  `ListEventsByTask` forward from `cfg.NextEventToken`, filtered server-side to
  `[instruction, external]` (`:306`). On the **first run `cfg.NextEventToken`
  is empty** (`:169`), so the walk starts at the head of the stream and returns
  *every* instruction+external event — byte-identical to what
  `GetTaskDetails.Events` returned. The code currently ignores this drained
  slice on the first run and re-fetches it inside `GetTaskDetails`.

So the driver does **not** need three calls, and an in-package assembly helper
is **not warranted**. Drop the `GetTaskDetails` call entirely; on the first run,
use the already-drained `events` for `promptEvents` (as the wake path already
does) and make a **single new `ListLinks` call** for `promptLinks`:

```go
// first run only
var promptLinks []*gritzv1.TaskLink
if !cfg.Started {
    resp, err := d.Client.ListLinks(ctx, &gritzv1.ListLinksRequest{TaskId: d.TaskID})
    if err != nil {
        return fmt.Errorf("failed to fetch task links: %w", err)
    }
    promptLinks = resp.GetLinks()
}
promptEvents := events // drained above; already the brief on the first run
```

`agentprompt.Render` is unchanged — it still takes `Task`, `Events`, `Links`
(`agentprompt.go`). No shared renderer with `get_my_task`; the driver renders
markdown, `get_my_task` renders JSON, and each now composes its own inputs.

#### 3. `mcpserver.getTask` — `internal/server/mcpserver/mcpserver.go`

**Today** (`mcpserver.go:184-262`): it makes **two** reads already —
`GetTaskDetails` (for the header, the instruction arm → `instructions`, and
`links`) *plus* a separate `ListEventsByTask` with no type filter (`:240`) to
pull the full stream and project `report`/`lifecycle` arms into `logs`.

**After:** go **fully event-native**, matching the webui timeline shape and
finishing the #1406 reshape. The handler already fetches the full stream once for
`logs`; now that single stream *is* the output. Fetch header + full stream +
links:

```go
task,   _ := h.service.GetTask(ctx, &gritzv1.GetTaskRequest{Id: input.ID})
events, _ := h.service.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{TaskId: input.ID}) // all arms
links,  _ := h.service.ListLinks(ctx, &gritzv1.ListLinksRequest{TaskId: input.ID})
```

The output struct **drops both synthesized fields** — the `Instructions`
projection *and* the `Logs` (report/lifecycle) projection — and presents the
**raw `events` stream** (all arms, stream order) alongside the header and links.
A consumer wanting the old `instructions` filters `events` for the instruction
arm; one wanting the old activity `logs` filters for `report`/`lifecycle` — the
same filters the handler did internally, now done by the caller over one uniform
list. This is the same event-native shape the webui timeline already renders from
`ListEventsByTask`, so `getTask` and the timeline agree. Net: `taskDetails`
becomes `{id, name, workspace, runner, status, url, links, events}` (no
`instructions`, no `logs`), a single event fetch, and no `GetTaskDetails`.

#### 4. CLI `task list` — `internal/command/task_list.go`

**Today** (`task_list.go:49-85`): `ListTasks`, then an **N+1** — one
`GetTaskDetails` per task — to attach `instructions`/`links`/`events` to each
row.

**After (already decided in the issue):** header-only from `ListTasks`, no
per-task fetch. `ListTasks` already returns the fat `Task` per row, so the
command renders `{id, name, status, …}` straight from `resp.Tasks` and drops the
per-task `instructions`/`links`/`events` columns and the N+1 loop entirely. This
is the largest simplification of the set.

#### 5. webui task detail — `webui/src/routes/tasks.$id.tsx` + `use-org-sse.ts`

**Today** (`tasks.$id.tsx:82-86`): one `useQuery(getTaskDetails, {id}, {60s})`,
reading only `data.task` and `data.links` (`:169-170`). The timeline is already
the separate `useTaskTimeline` → `listEventsByTask` infinite query
(`use-task-timeline.ts`), untouched by this change.

**After:** two finite queries replacing the one bundle —

```tsx
const { data: taskData, ... } = useQuery(getTask,   { id: taskId }, { refetchInterval: 60000 })
const { data: linkData }      = useQuery(listLinks, { taskId },     { refetchInterval: 60000 })
const task  = taskData?.task
const links = linkData?.links ?? []
```

`getTask` returns `{task}` (the same fat `Task`); `listLinks` returns `{links}`
(the same `TaskLink` list). `task-links.tsx` takes `links` as a prop already
(`:10`), so only its doc comment ("binds to `getTaskDetails.links`") updates —
no logic change. The timeline keeps rendering from `listEventsByTask`; the
webui simply stops fetching the instruction/external slice it discarded.

**SSE cache-invalidation** (`use-org-sse.ts`) — the query keys move from the
bundle to the two primitives, preserving today's routing:

- `case 'task'` (`:25-36`): invalidate **`getTask`** (was `getTaskDetails`) plus
  the existing `listTasks`.
- `case 'task_links'` (`:45-53`): invalidate **`listLinks`** (was
  `getTaskDetails`).
- `case 'task_logs'` (`:37-44`): unchanged — still drives the timeline
  tail-follow via `timelineFollowers.notify`.
- `RECONNECT_SCHEMAS` (`:93-101`): swap `getTaskDetails` for `getTask` **and**
  `listLinks` so a reconnect resyncs both families.

The invalidation stays correct because it already keyed on the underlying
resources — a `task` change touched the header (now `getTask`), a `task_links`
change touched the links (now `listLinks`). The timeline and its follow-on-SSE
behavior are entirely unaffected.

#### 6. n8n node — `n8n-node/nodes/Gritz/GritzExecutor.ts`

Not in the issue's table, but a real consumer that breaks at compile time when
the generated client loses `getTaskDetails`. It calls `getTaskDetails` in three
operations — `create` (`:228`), `getDetails` (`:274`), `update` (`:296`) — each
emitting `{...GetTaskDetailsResponse, logs}`, where `logs` already comes from a
*separate* `listEventsByTask` (`activityLogs`, `:201-212`).

**After:** a small private helper composes the same output shape from the
primitives (mirroring the driver/mcpserver pattern), reusing the existing
`activityLogs`:

```ts
private async taskDetails(taskId: bigint): Promise<IDataObject> {
    const { task }  = await this.client.getTask({ id: taskId });
    const { events } = await this.client.listEventsByTask({ taskId });
    const { links } = await this.client.listLinks({ taskId });
    const logs = /* project report/lifecycle from events (as activityLogs does) */;
    return { task, events, links, logs };
}
```

The three call sites collapse to `return { json: await this.taskDetails(taskId), ... }`.
`GetTaskDetailsResponseSchema` (`:12`) and its `toJson` wrapping are dropped.
This is versioned node code, so the field-shape change ships as a node bump —
consistent with "no server-side compat."

### Proto / RPC removal

In `proto/gritz/v1/gritz.proto`, delete:

- the rpc `GetTaskDetails(GetTaskDetailsRequest) returns (GetTaskDetailsResponse);` (`:17`)
- `message GetTaskDetailsRequest { int64 id = 1; }` (`:192-194`)
- `message GetTaskDetailsResponse { Task task; repeated Event events; repeated TaskLink links; }` (`:196-201`)

then `mise run generate` (which runs `go generate ./...` — `go tool buf generate`
via `generate.go` **and** the `moq` client mock via
`internal/gritzclient/client.go:1` — plus `webui` codegen), and
`buf generate` in `n8n-node/`. That drops:

- the Go server/client/handler stubs in `internal/proto/.../gritz.connect.go`
  and the `GetTaskDetailsRequest`/`Response` types in `gritz.pb.go`;
- `GetTaskDetailsFunc` and friends from the generated mock
  `internal/gritzclient/client_moq.go`;
- `getTaskDetails` from the webui and n8n generated clients.

Delete the handler `func (s *Server) GetTaskDetails` (`task.go:188-214`).
`GetTaskDetailsResponse` is the only message carrying the `events`/`links`
bundle, so nothing else references these types once the consumers above are
migrated.

## Implementation Plan

A strangler layer cake: migrate each consumer *off* `GetTaskDetails` first (the
RPC still exists, so every such slice is independently mergeable and shippable),
then delete the RPC last once nothing calls it. Slices 1–6 are order-independent
and each ships on its own; slice 7 depends on all of them (the regenerated
clients must have no remaining callers, or the Go/webui/n8n builds break).

1. **`get_my_task` off the aggregator, event-native** — Delivers: `agentmcp`
   composes `GetTask` + `ListEventsByTask(instruction,external)` + `ListLinks`;
   `taskDetailsToMap` takes the three responses and **drops the `instructions`
   key** (output = header + `links` + raw `events`). Depends on: nothing (RPC
   still exists). Verifiable by: `internal/agentmcp/xmcp_test.go` — output has
   **no `instructions` key**, and the instruction-arm events are present in the
   `events` stream.

2. **Driver first-run brief off the aggregator** — Delivers: drop the
   `GetTaskDetails` call; use the already-drained `events` for `promptEvents`
   and add a single `ListLinks` call for `promptLinks`. Depends on: nothing.
   Verifiable by: `internal/agent/driver_test.go` + `agentprompt_test.go` — the
   rendered first-run prompt is unchanged; `runner_test.go` updates its mock
   expectations (it currently asserts two `GetTaskDetails` calls,
   `runner_test.go:162-164`).

3. **`mcpserver.getTask` off the aggregator, event-native** — Delivers: `GetTask`
   + one full-stream `ListEventsByTask` + `ListLinks`; the output **drops the
   synthesized `instructions` and `logs` fields** and presents the raw `events`
   stream (all arms) beside header + links. Depends on: nothing. Verifiable by:
   the `mcpserver` tests — output has **no `instructions`/`logs`** fields and the
   raw stream is present; a single event fetch.

4. **CLI `task list` header-only** — Delivers: render rows from `ListTasks`
   alone; delete the per-task `GetTaskDetails` N+1 and the
   instructions/links/events columns. Depends on: nothing. Verifiable by:
   running `gritz task list`; the command makes exactly one RPC.

5. **webui to `GetTask` + `ListLinks`** — Delivers: split the detail query into
   `getTask` + `listLinks`; update `use-org-sse.ts` invalidation +
   `RECONNECT_SCHEMAS`; update `task-links.tsx` doc comment. Depends on:
   nothing. Verifiable by: `pnpm lint`; loading a task renders header + links +
   timeline; an SSE `task`/`task_links` notification refreshes the right pane;
   the timeline still tail-follows.

6. **n8n node composes the details shape** — Delivers: a private `taskDetails`
   helper (`getTask` + `listEventsByTask` + `listLinks` + existing
   `activityLogs`) replacing the three `getTaskDetails` call sites; drop
   `GetTaskDetailsResponseSchema`. Depends on: nothing. Verifiable by: the node
   builds and `create`/`getDetails`/`update` return `{task, events, links,
   logs}`.

7. **Remove the RPC + messages + handler, regenerate** — Delivers: delete the
   rpc, `GetTaskDetailsRequest`/`Response`, and the handler; `mise run generate`
   + n8n `buf generate`; delete the handler's tests
   (`apiserver/task_test.go`, `taskscope_test.go` cases for `GetTaskDetails`).
   Depends on: (1)–(6) — no caller may remain in any language. Verifiable by:
   `go build ./...`, `webui` build, and `n8n-node` build all pass with zero
   `GetTaskDetails` references (`grep` is clean); full `mise run test`.

## Trade-offs

- **Strangler (consumers first, delete last) vs. one big-bang PR.** A single PR
  could delete the RPC and migrate everything atomically. Rejected: the six
  consumers span three languages and five packages, and a big-bang PR is hard to
  review and impossible to bisect. The strangler makes each migration a small,
  independently-verifiable diff while the RPC still works, and reduces slice 7 to
  a pure deletion. Cost: `GetTaskDetails` lives a few PRs longer with no callers
  before its final removal — harmless.

- **Driver: reuse the drained events vs. a fresh brief fetch.** The driver could
  keep making an explicit brief fetch for symmetry with `get_my_task`. Rejected:
  it already drains exactly the instruction+external slice from the head of the
  stream on the first run, so a second fetch is pure waste. Reusing the drained
  slice and adding only `ListLinks` is the minimal honest change — and it means
  **no assembly helper is warranted** for the driver (it already holds the header
  and events; it needs one more primitive, not a bundler).

- **`get_my_task`/`mcpserver`/n8n: three inline calls vs. a shared assembler.**
  A cross-package helper that returns `{task, events, links}` would centralize
  the composition. Rejected per the carried-in constraint (no shared renderer):
  the three consumers want *different* event slices (brief vs. full stream),
  render to *different* shapes (JSON map vs. struct vs. n8n `IDataObject`), and
  live behind *different* clients (in-container agent client, server-internal
  service, external n8n client). Three short call sequences are clearer than one
  helper threading a filter and three output shapes.

- **Event-native output vs. preserving the synthesized buckets.** `get_my_task`
  and `mcpserver.getTask` could have kept their `instructions` (and, for
  `mcpserver`, `logs`) projections byte-for-byte, changing only the plumbing
  underneath. Rejected: re-bucketing the stream back into `instructions`/`logs`
  is precisely what #1406 set out to remove, and this effort subsumes #1406. Both
  consumers now present the raw `events` stream (the brief slice for the agent,
  the full stream for `getTask`), and a consumer that wants "just instructions"
  or "just the activity log" filters the arm it cares about — the same filter the
  handler used to bake in. Cost: a breaking output-shape change for these two
  surfaces (the `instructions`/`logs` keys vanish); accepted under the no-compat
  constraint, and the LLM/timeline consumers read the flat stream more directly
  than a nested re-bucketing.

- **`mcpserver`: one full-stream fetch vs. keeping the brief+full split.** Today
  it fetches the brief (via `GetTaskDetails`) *and* the full stream. Going
  event-native means the single full-stream `ListEventsByTask` *is* the output —
  it already fetched that stream for `logs`, so this removes a round trip and the
  two projections in one move; the full stream is a strict superset of the brief,
  so nothing is lost.

## Open Questions

- **`ListLinks` poll cost in the webui.** Splitting into two finite queries adds
  a second 60s poll (`listLinks`). It is a small indexed read and the SSE
  invalidation already refreshes links on change, so the poll is a fallback. If
  the extra poll is unwanted, `listLinks` could rely on SSE alone (no
  `refetchInterval`) and refetch only on `task_links` notifications — a webui-only
  tuning decision, deferred.

- **n8n node output field stability.** The composed `{task, events, links,
  logs}` keeps `logs` and the top-level `task`/`links`/`events` keys, so saved
  workflows referencing those survive. Worth confirming no workflow keys off a
  `GetTaskDetailsResponse`-specific wrapper before the node bump.
