# Inject New Events Into the Wake Prompt via a Saved Cursor

Issue: https://github.com/icholy/gritz/issues/946

## Problem

When a task is woken (restarted after a new instruction or external event), the
driver resumes the agent's session with a fixed nudge and nothing else. The
wake branch of the bootstrap template is literally:

```
The task was updated. Check gritz:get_my_task and continue.
```

(`internal/agent/PROMPT.md`, rendered by `Config.prompt` in
`internal/agent/driver.go:244`.)

So every wake costs the agent a `get_my_task` round-trip just to discover *why*
it was woken — even though the server already knew the new events at dispatch
time (they are the reason the task was restarted). Two problems follow:

1. **It's a convention, not a contract.** The wake only works if the prompt
   nags the model into calling the tool and the model complies. We have seen the
   harness render the `get_my_task` `<invoke>` block as literal text and exit
   without ever making the call — a silent no-op that leaves the agent unaware
   of the event that woke it. The one thing a wake exists to deliver depends on
   a tool call that can fail invisibly.

2. **It costs a turn.** The model burns a turn fetching context that could have
   been handed to it in the prompt it already received.

This proposal pushes the new events into the wake prompt instead of making the
agent pull them. The driver keeps a per-task **event token** — the opaque
pagination cursor the event stream already hands back — fetches the events newer
than the token on each wake, and injects their **raw JSON** (the same shape
`get_my_task` returns) directly into the prompt it hands the harness. The wake no
longer depends on any tool call.

This is the narrow, wake-path slice of #946. The issue's broader "render the
whole brief server-side and mark events delivered" is discussed under
[Trade-offs](#trade-offs); this design deliberately keeps the token
**driver-local** and reuses the existing event-stream pagination rather than
adding a server-side delivery-tracking column.

## Design

### Where the token lives

The driver already persists per-task state to a JSON config file in the
sandbox: `ConfigStore` writes `/tmp/gritz/{taskID}.json` via atomic
write, and the `Config` struct already carries agent-managed state
(`SetupCommandsCompleted`, `Started`) alongside the runner-provided fields
(`internal/agent/config.go`). The token is one more agent-managed field:

```go
type Config struct {
    // ... runner-provided fields ...

    // Agent-managed state
    SetupCommandsCompleted int    `json:"setup_commands_completed,omitempty"`
    Started                bool   `json:"started,omitempty"`
    NextEventToken         string `json:"next_event_token,omitempty"` // pagination cursor for events already delivered to the agent
}
```

`NextEventToken` is the opaque `next_page_token` the event stream returned last time
— the same base64 keyset cursor `ListEventsByTask` hands back
(`internal/store/event.go`, `internal/pagination/pagination.go`). Its zero value
(`""`) is the natural first-run state: "no page consumed yet." The config file
survives a restart because the driver-owned-events design restarts the **same**
container (the filesystem persists — see
`proposals/implemented/driver-owned-events.md`), so the token written by one run is
read by the next. If the container is *recreated* rather than restarted, the
config file is gone and the token resets to `""` — but so do `Started` and
`SetupCommandsCompleted`, so a recreate is already a fresh first run and the
reset is consistent.

Storing the token in the sandbox (rather than server-side) keeps this change
entirely within the driver plus the read RPC that already exists: no schema
migration, no new task column, no delivery bookkeeping on the write path.

### Fetching new events: reuse `ListEventsByTask` paging

The bidirectional keyset pagination that just landed already provides exactly
the "keep polling the tail" primitive we need. `ListEventsByTaskResponse` always
returns a `next_page_token` — an opaque bidirectional cursor the proto documents
as "so a client can keep polling the tail for appends", backed by the ascending
live-follow walk `ListEventsByTaskAsc` (`WHERE id > cursor_id ORDER BY id ASC`,
`internal/store/sql/queries/event.sql`). The driver's saved cursor **is** that
token.

An earlier draft proposed a dedicated `ListEventsSince(after_id)` RPC out of a
worry that clients should not fabricate opaque tokens. That objection dissolves:
the driver never *fabricates* a token — it stores the one the server handed back
and replays it. Replaying a server-issued `next_page_token` is precisely the
live-follow contract the token was built for, so the existing paged RPC is the
right primitive and no new endpoint is needed.

Flow:

- **First run** — `NextEventToken` is empty. `ListEventsByTask(task_id, page_size=N)`
  with an empty token returns the newest page (oldest-first within the page) plus
  a fresh `next_page_token` pointing at the current tail. The driver saves that
  token and injects nothing (first run bootstraps via `get_my_task`).

- **Wake** — `ListEventsByTask(task_id, page_token=NextEventToken)`. The live-follow
  walk returns the events newer than the token, oldest-first. A page shorter than
  `page_size` means the tail is reached; a full page means more may remain, so the
  driver follows the returned `next_page_token` until it gets a short page,
  accumulating events across pages. It injects the accumulated events and saves
  the final `next_page_token`.

**Type filtering.** The paged request
(`ListEventsByTaskRequest{task_id, page_size, page_token}`) has no event-type
filter, unlike the non-paged `ListEventsByTask` store method the brief uses,
which narrows to instruction + external (`GetTaskDetails` in
`internal/server/apiserver/task.go`). So the paged walk returns the full stream
— including the agent's own `report`/`link` events and internal `lifecycle`
transitions. The driver filters the accumulated events to
`model.EventTypeInstruction` + `model.EventTypeExternal` before injecting, while
advancing the token over the **full** stream (so the saved cursor tracks the real
stream position). Alternatively we add an optional `types` filter to
`ListEventsByTaskRequest` and push the filter server-side — the store's
`ListEventsByTaskPageParams` already carries a `Types []string`, so it is a small
addition. Client-side filtering is the default here because it leaves the RPC
untouched; the server-side filter is noted as an [open question](#open-questions).

### The wake path

`runAgent` (`internal/agent/driver.go:149`) gains an event-fetch step between
loading the config and building the prompt:

1. **Fetch.** Call `ListEventsByTask(task_id, page_token=cfg.NextEventToken)`,
   draining pages to the tail as above. Marshal each retained event with the same
   `protojson` options `taskDetailsToMap` uses (`Indent: "  "`), so the injected
   payload is byte-for-byte the shape the `events` array already has in
   `get_my_task` output — the agent parses one format, not two.

2. **Inject (wake only).** When `cfg.Started` is true and the fetch retained
   events, the wake branch of `PROMPT.md` renders the raw JSON instead of the
   `get_my_task` nudge:

   ```
   The task received new events:

   [ { "id": "...", "createdAt": "...", "external": { ... } }, ... ]

   Continue working on the task.
   ```

   When `cfg.Started` is true but the fetch retained nothing (a spurious wake, or
   only filtered-out event types), the branch falls back to a bare
   `The task was updated. Continue.` — no tool-call instruction, because the
   design's whole point is that the wake no longer depends on one.

3. **Advance.** After `a.Prompt` returns, set `cfg.NextEventToken` to the final
   `next_page_token` and `Config.Save`, next to the existing `cfg.Started = true`
   save (`internal/agent/driver.go:197`).

The template's data struct grows one field:

```go
err := promptTemplate.Execute(&b, struct {
    Started bool
    Prompt  string
    Events  string // raw JSON array of events since the token; empty when none
}{ ... })
```

### First-run initialization

The **first** run (`cfg.Started == false`) keeps today's bootstrap: the prompt
tells the agent to call `get_my_task`, which establishes full context (task
name, links, status, and all instructions) — more than the event stream alone
carries. But the driver still runs the fetch on the first run to **seed the
token**: an empty-token `ListEventsByTask` call returns the current tail's
`next_page_token`, which the driver saves (ignoring the page's events for
injection — the bootstrap prompt already covers instructions).

Seeding to the tail is race-safe in the *safe* direction: any event that arrives
after the seed call has a higher id, so it is past the saved token and gets
delivered on the next wake. The only cost is that an event the agent already saw
through its own `get_my_task` call (which runs slightly later than the driver's
seed fetch) may be re-injected once on the next wake — a harmless duplicate,
never a miss.

### Delivery timing: at-least-once

The token advances **after** `a.Prompt` returns, not before. If the run crashes
or fails mid-prompt, the token is not advanced and the next run re-fetches from
the same cursor and re-injects the same events. This is at-least-once delivery:
duplicates are possible, misses are not — the same bias the driver-owned-events
design takes with duplicate runner events ("duplicates are safe"). An injected
event the agent has already handled is a cheap re-read; a dropped event is a task
that never learns why it was woken.

### `get_my_task` stays

`get_my_task` is unchanged and still registered
(`internal/agentmcp/xmcp.go`). It remains useful for:

- **First-run bootstrap** (unchanged), which needs task name/links/status, not
  just events.
- **Mid-run refresh** — a long-running agent asking "did anything arrive while I
  was working?" Start-time context is now pushed; mid-run context is still
  pulled.

What changes is that the **wake path no longer depends on it**. Removing that
dependency is the reliability win: the events that justify the wake are in the
prompt text the model already reads, so a harness that fails to execute a tool
call can no longer silently drop them.

## Implementation Plan

1. **`NextEventToken` config field** — Delivers: the `next_event_token` string field on
   `agent.Config`. Depends on: nothing. Verifiable by: a load/save round-trip
   test in `internal/agent`.

2. **Driver seeds the token** — Delivers: the first-run empty-token
   `ListEventsByTask` call that saves the tail `next_page_token` (no injection).
   Depends on: (1). Verifiable by: a driver test asserting `NextEventToken` is
   non-empty after a non-wake run.

3. **Driver injects on wake** — Delivers: the `PROMPT.md` wake-branch change, the
   `Events` template field, the fetch/drain/filter/inject step in `runAgent`, and
   the post-run token advance. Depends on: (2). Verifiable by: updating
   `internal/agent/driver_test.go` — the two existing assertions that the wake
   prompt equals `"The task was updated. Check gritz:get_my_task and
   continue."` become assertions that it contains the injected event JSON when
   events are pending, and the bare `"The task was updated. Continue."` fallback
   when none are.

4. **(Optional) server-side type filter** — Delivers: an optional `types` field
   on `ListEventsByTaskRequest`, wired to the store's existing
   `ListEventsByTaskPageParams.Types`, so the paged walk can return only
   instruction + external and the driver drops its client-side filter. Depends
   on: nothing. Verifiable by: a handler test. Only needed if we prefer
   server-side filtering.

5. **Docs/prompt cleanup** — Delivers: PROMPT.md wording that documents
   `get_my_task` as an on-demand mid-run refresh rather than a required wake
   step. Depends on: (3). Verifiable by: template rendering tests.

Layer 1 is the independent foundation; 2 and 3 build the behavior on top; 4 is an
optional refinement; 5 is cosmetic.

## Trade-offs

**Driver-local token vs. server-side delivery tracking.** #946 frames the cursor
as "delivery cursors on events": mark an event delivered server-side once it
enters a brief. This proposal instead keeps the cursor in the sandbox config
file. The driver-local approach needs no migration, no new column, and no
write-path bookkeeping, and it composes cleanly with the same-container-restart
model where the file already persists other resume state. Its cost is that the
cursor is per-sandbox: a container *recreate* resets it (mitigated — a recreate
already resets `Started`/setup state, so it is a fresh run), and the server has
no record of what a given agent has seen (only the agent's sandbox does). If we
later want a server-authoritative "what has this task's agent seen" for the Web
UI or for multi-consumer delivery, a server-side cursor is the better home; for
the single-driver wake path, sandbox-local is simpler.

**Reusing the paged endpoint vs. a dedicated RPC.** We store and replay the
opaque `next_page_token` the event pagination already returns rather than adding
a `ListEventsSince(after_id)` read. This reuses the exact primitive the token was
designed for ("keep polling the tail"), needs no new API surface, and keeps the
driver's cursor a single opaque string it never has to interpret. The cost is
that the paged request carries no type filter today, so the driver filters the
returned page client-side (or we add the optional filter in layer 4).

**Raw JSON vs. a rendered brief.** We inject the same `protojson` event shape
`get_my_task` already returns, not a human-readable summary. This means one
format for the agent to parse (it already handles this shape), zero new
rendering code, and no second format to keep in sync. The cost is that a raw
JSON array is less scannable than prose — acceptable, because the consumer is a
model that already reads this exact structure, and the issue explicitly calls
for the raw shape rather than a formatted brief.

**At-least-once duplicates.** Advancing the token after the run means a retried
run re-injects. Chosen deliberately over at-most-once (advance before the run),
which would drop the wake's payload on any mid-run failure — the worse outcome
for a mechanism whose entire job is to deliver that payload.

## Open Questions

- **Server-side vs. client-side type filtering.** The paged RPC returns the full
  stream; the driver filters to instruction + external. Is it worth adding a
  `types` filter to `ListEventsByTaskRequest` (layer 4) to push that server-side,
  or is the client-side filter fine given the driver drains the pages anyway?

- **Event type filter contents.** Should the wake injection ever include
  `lifecycle` events (e.g. a status change made by a human while the agent was
  asleep)? The current answer is no — lifecycle is internal timeline, not agent
  input — but external triggers that carry `details` may already cover the useful
  cases.

- **Token advance point under partial delivery.** If `a.Prompt` injects events
  but the agent errors partway, we re-inject on retry (at-least-once). Is there
  any event whose *re-delivery* is harmful (e.g. one that instructs an
  irreversible external action)? If so, the token would need to advance the
  moment the prompt is accepted rather than after the run — trading a miss risk
  for a double-action risk. Current stance: re-delivery is safe because the agent
  re-reads context, not re-executes a command queue.

- **Should first-run also drop `get_my_task`?** This design keeps the first-run
  bootstrap on `get_my_task` because it needs task metadata beyond events. Fully
  retiring the tool from the driver prompt (pushing name/links/status too) is the
  larger #946 brief and is left as future work.

- **Interaction with #945 (C2-hosted agent MCP).** If the agent MCP moves
  server-side, the token and the injection could move with it. Does the
  driver-local token become redundant, or does the driver still own wake-prompt
  assembly? Worth settling before both land.