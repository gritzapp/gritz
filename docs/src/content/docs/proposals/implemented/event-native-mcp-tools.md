---
title: "Event-native MCP tool surfaces"
description: "MCP tools still rebuild legacy instructions/logs/links buckets on top of the unified event stream, duplicating data and requiring extra round trips and client re-projection."
---

Issue: https://github.com/icholy/gritz/issues/971

## Problem

The unified task event stream (#947,
`proposals/draft/unified-task-event-stream.md`) made a task one ordered stream
of typed events — `instruction`, `external`, `report`, `lifecycle`, `link` — in
a single `events` table, and dropped the `logs` table. The MCP tool *writes* are
already event-based: the agent's `report` tool appends a `report` event,
`create_link` appends a `link` event, and adding an instruction appends an
`instruction` event.

The gap is the *read* shape. The tools still reconstruct the old separate
buckets on output, so the storage model is a stream but every MCP consumer sees
the legacy instructions / logs / links shape rebuilt on top of it:

- **Agent-side `get_my_task`** (`internal/agentmcp/xmcp.go`, `taskDetailsToMap`)
  reads the task's events but returns `{id, name, status, …, instructions,
  links, events}` — it walks the event stream, filters the `instruction` arms
  back out into a separate `instructions` array, *and also* emits the full
  `events` list. Instructions appear twice, once re-bucketed and once in the raw
  stream.
- **User-side `get_task`** (`internal/server/mcpserver/mcpserver.go`, `getTask`)
  returns `{instructions, logs, links}`, where `logs` is reconstructed from
  `report` events plus `lifecycle` events (rendered through
  `model.LifecyclePayload.Summary()`) to mimic the dropped `logs` table. It
  makes *two* round trips — `GetTaskDetails` for instructions/links and
  `ListEventsByTask` for the report/lifecycle projection — to rebuild a shape
  the stream already expresses.

The downstream consumers inherit this. The n8n node
(`n8n-node/nodes/Gritz/GritzExecutor.ts`) carries an `activityLogs` helper
that re-projects the report/lifecycle arms into flat `{type, content}` rows
precisely "because the logs table is gone" — reconstructing the legacy shape a
second time on the client.

The event stream is the source of truth, but no MCP surface presents it as one.
This proposal designs the event-native read shape for both the agent-side tools
(`get_my_task`, `report`, `create_link`, `update_my_task`) and the user-facing
tools (`get_task`, `create_task`, `update_task`, `list_tasks`): a task's history
as one ordered, typed event stream, not legacy buckets. It is the read-shape
companion to the write-surface reshape in #947 (`UpdateTask` takes `repeated
Event`) and the agent-surface split in #939 (`agent-rpc-surface`) — it does not
restate those; it consumes their result.

## Design

### Principle: the stream is the shape

Both surfaces return the same primitive — an ordered list of typed events —
differing only in *which slice* of the stream they expose and *whose vantage*
they speak from:

- `get_my_task` returns the **brief**: the to-agent slice
  (`instruction` + `external`), the events a run is handed.
- `get_task` returns the **full timeline**: every arm, in stream order, the
  whole history of what happened to the task.

Neither re-buckets. The re-bucketing code (`taskDetailsToMap`'s `instructions`
array, `getTask`'s `logs` reconstruction and its second `ListEventsByTask`
round trip) is deleted, not relocated.

### Tool output: a projection of `Event`, not raw `Event`

The wire `gritzv1.Event` is a typed `oneof` (`internal/model/event.go`,
`Event.payload`). Two options for what the tools emit:

1. **Raw protojson of `Event`** — `{id, task_id, wake, created_at, payload:
   {instruction: {…}}}`, the oneof rendered as a nested single-key object.
2. **A tool-friendly projection** — flatten the arm to a `type` discriminator
   plus the payload fields, so each event reads as one self-describing object.

This proposal chooses **(2), a flat projection**, for the agent-and-LLM-facing
tools. protojson's oneof rendering (`{"payload": {"instruction": {…}}}`) forces
every reader — the agent's model, the user's Claude, the n8n template author —
to know the arm is nested one level under `payload` and key off the *presence*
of a field rather than a value. A flat `type`-tagged object is what an LLM reads
most reliably and what a JSON consumer filters most simply:

```jsonc
// one event, tool projection
{ "id": 1043, "type": "instruction", "wake": true,
  "created_at": "2026-06-14T18:22:10Z",
  "text": "rebase onto main", "url": "https://github.com/…/pull/481" }

{ "id": 1044, "type": "report", "wake": false,
  "created_at": "2026-06-14T18:25:51Z",
  "content": "Opened PR #952 with the migration." }

{ "id": 1051, "type": "lifecycle", "wake": false,
  "created_at": "2026-06-14T18:40:02Z",
  "kind": "SANDBOX_EXITED", "actor": {"kind": "runner"},
  "from_status": "RUNNING", "to_status": "COMPLETED",
  "summary": "Sandbox exited (Running -> Completed)" }
```

The discriminator is the existing `Payload.Type()` value (`instruction`,
`external`, `report`, `lifecycle`, `link` — the constants in
`internal/model/event.go`). The projection is a thin Go helper alongside the
tool handlers (a function from `*gritzv1.Event` to `map[string]any`, switching
on the arm), mirroring how `taskDetailsToMap` switches today — but emitting one
uniform list instead of three buckets.

For `lifecycle` events the projection includes a pre-rendered `summary` field
(from `LifecyclePayload.Summary()`) *in addition to* the structured `kind` /
`actor` / `from_status` / `to_status`. The structured fields let a programmatic
consumer (n8n) branch; the `summary` gives an LLM the same human line the web UI
timeline shows, so it needn't re-derive "sandbox exited" from an enum. This is
the one place a rendered string survives — as a convenience field beside the
structure, not as the only representation (the legacy `logs` reconstruction was
*only* the string).

### Agent surface: `get_my_task`

`get_my_task` returns the brief as a flat event list, plus the task identity
fields the agent needs and the links it has created. Concretely the output
becomes:

```jsonc
{
  "id": 869,
  "name": "Proposal: event-native MCP tools",
  "status": "RUNNING",
  "workspace": "gritz",
  "url": "https://gritz.dev/ui/tasks/869",
  "events": [ /* brief: instruction + external arms, stream order, projected */ ],
  "links":  [ /* the task's links — see below */ ]
}
```

What changes from today (`taskDetailsToMap`):

- The separate **`instructions` array is removed.** Instructions are
  `type: "instruction"` events inside `events`. The brief is already filtered to
  `instruction` + `external` (the `to_agent` arms, per #947's "agent's brief"
  query), so `events` *is* the instruction-and-trigger list — no second copy.
- **`events` is the brief, projected**, not raw protojson of every arm. Today
  `taskDetailsToMap` marshals every event raw *and* re-buckets instructions; the
  new shape has one projected list.
- **`links` stays**, because links are about-task records the agent benefits
  from seeing (what it has already attached) and the subscription/list
  projection (`task_links`) is the cheap read for them (#947, "Links: event is
  truth, `task_links` is the index"). Links are not in the brief slice
  (`link` is an `about_task` arm, excluded from `instruction`+`external`), so
  surfacing them needs an explicit field. This is a deliberate exception to
  "only the brief": the agent's own links are operationally useful context, and
  the projection read is cheap.

The tool *description* updates from "Get the current task instructions, links,
and events" to reflect the unified shape, e.g. "Get the current task: its
to-agent event stream (instructions and external triggers) and the links it has
created."

### User surface: `get_task`

`get_task` returns the **full timeline** — every arm in stream order — as the
flat projected list, replacing the `{instructions, logs, links}` triple:

```jsonc
{
  "id": 869,
  "name": "…",
  "workspace": "gritz",
  "runner": "…",
  "status": "RUNNING",
  "url": "…",
  "events": [ /* full timeline: all arms, stream order, projected */ ],
  "links":  [ /* link projection, for filtering/subscription visibility */ ]
}
```

What changes from today (`getTask`):

- The **`logs` reconstruction is deleted**, along with the second
  `ListEventsByTask` round trip. `report` and `lifecycle` events are simply two
  of the arms in `events`; a consumer wanting "the activity log" filters
  `events` for `type in (report, lifecycle)` — the same filter `activityLogs`
  does today, but now done by the consumer over a uniform list rather than
  baked into the tool.
- The **`instructions` bucket is deleted** — instruction events are in
  `events`.
- `links` is retained as a top-level field for the same reason as on the agent
  surface: the subscription projection is the cheap, filter-friendly read, and
  surfacing it saves the consumer from filtering `link` arms out of the timeline
  just to answer "what's subscribed."

Mechanically this *simplifies* the handler: one read of the full stream
(`ListEventsByTask`, or `ListEvents` scoped by `task_id` once #947's read RPC
lands) plus the links projection, versus today's `GetTaskDetails` +
`ListEventsByTask` + three-way bucketing.

`get_task`'s difference from `get_my_task` is exactly **slice and vantage**: the
full timeline (all arms) from the operator's view, versus the brief (to-agent
arms) from the agent's view. Same projection function, different filter.

### Write tools stay thin typed verbs

The taxonomy question — do `report` / `create_link` survive as separate tools or
collapse into one "append event" verb — resolves to **keep them as thin typed
verbs**, consistent with #947 ("the MCP tools become thin verbs over the
stream") and #939 (each agent method "fixes its event type").

- `report` appends a `report` event; `create_link` appends a `link` event;
  adding an instruction (user surface) appends an `instruction` event. Each tool
  names one event type and fills its payload — the LLM picks the verb by intent,
  not by hand-constructing a discriminated union.
- A single `append_event(type, payload)` tool is **rejected** for the
  agent/user surfaces: it pushes the oneof discrimination into the model's lap
  (it must remember the arm names and which fields each takes), invites invalid
  combinations a typed verb makes unrepresentable, and weakens authorization —
  a typed verb *is* the type-allowlist (the agent's `report` tool can only ever
  produce a `report` event; it cannot forge a `lifecycle`). The forge-proofness
  #947 and #939 rely on is structural precisely *because* the verbs are
  per-type.

So the surface is asymmetric by design and stays that way: **reads unify**
(one stream, one projection) while **writes stay typed** (one verb per
appendable arm). Unifying writes would trade a clean LLM affordance and a
structural authz boundary for a cosmetic symmetry with reads.

This composes directly with the two sibling reshapes rather than duplicating
them:

- **#947's `UpdateTask` takes `repeated Event`.** On the user surface,
  `update_task` adds an instruction; under #947 it is a thin verb that sends an
  `UpdateTask` carrying one `instruction` event (with `wake`/`start` set), and
  reads back the resulting task. This proposal leaves the *write* exactly as
  #947 defines it and only restates the *read-back* in event-native terms
  (return the task summary, or optionally the refreshed timeline — see Open
  Questions). The handler's instruction-only type-allowlist (#947, AuthZ) is
  what keeps `update_task` from appending `lifecycle`/`external`.
- **#939's `AgentService`.** The agent-side tools (`get_my_task`, `report`,
  `create_link`, `update_my_task`) map 1:1 onto identity-scoped `AgentService`
  methods where "my task" comes from the token, not a request field. This
  proposal specifies *what those methods return/emit in event-native shape*;
  #939 specifies *the service/auth boundary they live behind*. `get_my_task`
  reads the brief on that surface; `report`/`create_link` are the per-type
  append verbs (#939's `Report` / `CreateMyLink`). Whichever of the three
  proposals merges last rebases onto the others; none contradicts.

### `create_task` and `list_tasks`

These two need only light touches, because neither returns a history bucket
today:

- **`create_task`** returns a task summary (`taskSummaryOf`) and is unchanged in
  shape. Its *input* — a single `instruction` string — is already the seed of
  the stream (#947: `CreateTask` seeds the stream with one `instruction` event).
  No output change; noted here for completeness so the surface is covered
  end-to-end.
- **`list_tasks`** returns summaries (`{id, name, workspace, status, url}`) and
  stays summaries. Listing must not replay streams (#947, "Status stays a
  projection"); the materialized `status` column powers the list. No timeline in
  a list view. Unchanged.

So of the eight tools, the substantive output changes are `get_my_task` and
`get_task`; `report` / `create_link` / `update_my_task` / `update_task` change
only insofar as #939/#947 already reshape their write path; `create_task` /
`list_tasks` are unchanged.

### Backward compatibility and migration

Changing tool output is a breaking change for three consumer classes, each with
a different migration story.

**1. Agent prompts (the in-container model).** The agent reads `get_my_task`
output per run; there is no stored schema, the model re-reads the JSON each
time. Dropping the duplicate `instructions` array and unifying on a projected
`events` list is a prompt-legibility *improvement* — a flat `type`-tagged list
is easier for the model than a nested oneof or a split bucket. Risk is low and
self-correcting (the next run reads the new shape). Mitigation: update the
tool's `Description` string so the schema the model is told about matches what
it receives, and keep the top-level identity fields (`id`, `name`, `status`,
`url`) stable so existing prompt scaffolding that references them is unaffected.

**2. The user's Claude (user-facing MCP over HTTP/stdio).** Same story as the
agent: an interactive LLM consumer that re-reads tool output each call, no
pinned schema. The flat projection is easier for it, and the tool descriptions
(`get_task`: "Get full details of a task including instructions, logs, and
links") update to describe the event stream. Low risk.

**3. The n8n node (a programmatic consumer).** This is the real
compatibility surface: n8n workflows have field references (`$json.logs`,
`$json.instructions`) baked into saved workflow definitions, and they break if
those keys vanish. Options:

- **(a) Coordinated bump.** The node ships from this repo (`n8n-node/`) against
  the generated client; cut a new node major version whose `getDetails` /
  `create` / `update` return `{…, events}` (the projected stream) and drop the
  `activityLogs` helper. Document the field migration (`logs` → filter `events`
  for `report`/`lifecycle`; `instructions` → filter `events` for `instruction`)
  in the node changelog. Existing workflows pin the old node version until
  updated. **Recommended** — it is the honest cut, and the node is versioned for
  exactly this.
- **(b) Transitional compatibility fields.** Have the node keep deriving `logs`
  and `instructions` *client-side* from `events` for one release (the
  `activityLogs` projection it already has, plus an `instructions` filter),
  emitting both the new `events` and the legacy buckets, with the buckets marked
  deprecated. Removed in the following major. Softer landing, more code to carry.

Because the breaking edge is the node — not the server — the server-side tools
can cut cleanly to the event-native shape, and the *node* owns whichever
compatibility window (a) or (b) is chosen. The MCP tools themselves do not need
dual-shape output; an LLM consumer adapts on re-read.

A note on sequencing: this is a read-shape change layered on top of #947's
storage cutover (already merged) and is independent of #939's service split. It
can land before or after #939 — before, it reshapes the current
`agentmcp`/`mcpserver` handlers; after, it reshapes the `AgentService` method
bodies. The projection helper is the same either way.

## Trade-offs

- **Flat projection vs. raw `Event` protojson.** Raw protojson is
  zero-code (just `protojson.Marshal` the events, as `taskDetailsToMap` partly
  does) and keeps the wire and tool output identical, which is tidy for a
  programmatic consumer that already has the generated types. It was rejected
  for the *primary* output because the nested-oneof shape
  (`{"payload":{"instruction":{…}}}`) is awkward for the LLM consumers that
  dominate this surface, and "key off field presence" is a worse filter than
  "match a `type` value." The projection costs a small switch function and a
  slight divergence between wire and tool shape — accepted, because the tool
  surface exists to be *consumed by models*, and legibility there outweighs
  wire-parity. (A consumer that wants the raw `Event` can still read it via the
  RPC directly; the MCP tool is the ergonomic layer.)

- **Keeping `links` as a top-level field vs. pure timeline.** A purist
  event-native shape would return *only* `events` and let the consumer filter
  `link` arms for "current links." Rejected: the subscription projection
  (`task_links`) is the cheap, indexed read for links, surfacing "what's
  subscribed" is a common operator/agent question, and forcing every consumer to
  reduce the timeline to answer it is hostile. The cost is one field that is
  technically derivable from the stream — accepted, mirroring how #947 itself
  keeps `task_links` as a materialized projection rather than replaying.

- **Reads unify, writes stay typed (asymmetry).** Collapsing writes into one
  `append_event` verb would make the surface symmetric and smaller. Rejected:
  the typed verbs are the authorization boundary (a `report` tool cannot forge a
  `lifecycle` event) and the better LLM affordance (intent → verb, no
  hand-built union). The asymmetry is principled, not incidental.

- **`lifecycle.summary` convenience field.** Carrying a rendered string beside
  the structured lifecycle fields duplicates information (the consumer could
  render it from `kind`/`from_status`/`to_status`). Kept because the LLM
  consumers benefit from the same human line the UI shows without re-deriving it,
  and `LifecyclePayload.Summary()` already exists as the canonical Go renderer.
  The structure is still present for programmatic branching, so this is additive,
  not lossy — unlike the legacy `logs` reconstruction it replaces.

## Open Questions

- **Should `update_task` / `update_my_task` return the refreshed timeline?**
  Today they return a task summary after the write (`update_task` re-`GetTask`s;
  `update_my_task` returns a status string). An event-native option is to return
  the appended event(s) and/or the refreshed brief, so the caller sees the
  result of its append without a follow-up `get_*`. This trades a larger write
  response for fewer round trips. Leaning toward keeping the lean summary
  (matches #947's `UpdateTask` returning task fields) and letting callers
  `get_*` when they want the stream, but worth confirming against n8n's
  update-then-read pattern (`GritzExecutor.update` already does a follow-up
  `getTaskDetails`).

- **Does `get_my_task`'s brief include `external` events the agent hasn't seen,
  or only instructions?** #947 defines the brief as `instruction` + `external`.
  This proposal follows that. But the agent's *first* read vs. a re-read after a
  wake see different external events; whether `get_my_task` should mark which
  events are new since the last run is the incremental-delivery concern (#946),
  explicitly out of scope here — the brief is returned in full each call, as
  today. Flagged so the boundary is clear.

- **Flat projection field naming for collisions.** The flat shape hoists payload
  fields to the top level alongside `id`/`type`/`wake`/`created_at`. No current
  payload field collides with those reserved keys (`InstructionPayload.Text/URL`,
  `ReportPayload.Content`, `LinkPayload.*`, `LifecyclePayload.Kind/Actor/…`), but
  a future arm could. Nesting payload fields under a `payload` key would avoid
  the risk at the cost of the flatness that makes the shape LLM-friendly. Resolve
  by reserving the four envelope keys and validating new arms against them, or by
  nesting — decision deferred until an arm actually collides.

- **n8n compatibility window: (a) coordinated bump vs. (b) transitional
  fields.** Recommended (a), but the call depends on how many live workflows
  reference `$json.logs`/`$json.instructions` and how much warning their authors
  get. Owner's call.
