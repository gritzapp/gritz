# Agent Transcript as Task Events

Issue: https://github.com/icholy/xagent/issues/1297

## Problem

Every harness the driver runs already streams a full transcript — assistant
messages, tool calls with their inputs, tool results — on the structured
`stream-json` / `--json` stdout the driver parses line by line
(`internal/agent/{claude,cursor,codex}.go`). Today that data is **parsed and
then thrown away**: each harness logs a one-line `slog` summary (bulky fields
`old_string`/`new_string`/`content` actively stripped by
`internal/agent/toollog`) and drops the rest. Tool *result* content is
discarded entirely. The only agent→server content channel is the deliberate
`report` tool, routed through the `UploadLogs` shim
(`internal/server/apiserver/log.go`). There is no per-run transcript anywhere:
the summaries live only in `docker logs xagent-{task-id}` and die with the
container.

Issue #1297 investigated capturing this transcript and **rejected OTel as the
vehicle** (uneven harness coverage, a 4 MiB OTLP ceiling and 2 KiB attribute
truncation that fight full transcripts, an experimental spec that has moved
content storage three times) and **rejected a bespoke raw-stream blob store**
(the superseded `proposal-driver-logs-to-server` branch). Its recommendation:
capture the transcript keyed by `(task, run version)` using infrastructure
xagent already has.

This proposal takes the simplest form of that recommendation: **send the
transcript through the existing task event stream as a new `Event.payload`
arm.** The driver already parses the stream and, since the version-stamped
driver work (`proposals/draft/version-stamped-runner-events.md`, PR #1298),
fetches `task.Version` once at the top of `Driver.Run` — so it can emit
normalized transcript events over its existing authenticated client, each
stamped with the run it belongs to.

**Out of scope** (per #1297): OTel export of any kind, and raw-stream blob
storage. The transcript is stored as a normalized, capped event projection in
Postgres — not as the harness's raw bytes.

## Design

### 1. Normalized transcript schema

A sixth `Event.payload` arm, `TranscriptPayload`, following the existing
`LifecyclePayload` shape (a `kind` discriminator plus flat fields) rather than
a nested oneof. One transcript **entry** = one `events` row, so entries slot
into the same task-scoped stream, ordering, and Web UI timeline as every other
arm. The stream is naturally interleaved and append-only, matching how the
harnesses emit.

```proto
// proto/xagent/v1/xagent.proto — new arm on the Event oneof (field 10)
message Event {
  // ...existing 1..9...
  oneof payload {
    InstructionPayload instruction = 5;
    ExternalPayload    external    = 6;
    ReportPayload      report      = 7;
    LifecyclePayload   lifecycle   = 8;
    LinkPayload        link        = 9;
    TranscriptPayload  transcript  = 10;
  }
}

enum TranscriptKind {
  TRANSCRIPT_KIND_UNSPECIFIED = 0;
  TRANSCRIPT_KIND_MESSAGE     = 1; // assistant/user/system text
  TRANSCRIPT_KIND_TOOL_CALL   = 2; // a tool invocation + its input
  TRANSCRIPT_KIND_TOOL_RESULT = 3; // the result of a tool call
  TRANSCRIPT_KIND_USAGE       = 4; // per-turn / final token+cost accounting
}

// A single normalized transcript entry produced by the driver from one
// harness stream line. `version` is the run this entry belongs to (task.Version
// at driver start); the timeline groups on it. Fields not relevant to `kind`
// are empty.
message TranscriptPayload {
  TranscriptKind kind         = 1;
  int64          version      = 2;  // run version (stamped by the driver)
  string         role         = 3;  // MESSAGE: "assistant" | "user" | "system"
  string         text         = 4;  // MESSAGE: message text
  string         tool_name    = 5;  // TOOL_CALL: normalized tool name
  string         tool_call_id = 6;  // TOOL_CALL/TOOL_RESULT: correlation id
  string         input        = 7;  // TOOL_CALL: JSON-encoded input (capped)
  string         content      = 8;  // TOOL_RESULT: result body (capped)
  bool           is_error     = 9;  // TOOL_RESULT: harness is_error flag
  TranscriptUsage usage       = 10; // USAGE
  bool           truncated    = 11; // input/content was capped (see §3)
}

message TranscriptUsage {
  string model         = 1;
  int64  input_tokens  = 2;
  int64  output_tokens = 3;
  int64  cache_read_tokens  = 4;
  int64  cache_write_tokens = 5;
  double cost_usd      = 6; // when the harness reports it (claude final result)
}
```

The Go model mirrors this as a new `EventPayload` implementation in
`internal/model/event.go` (a `TranscriptPayload` struct with
`Type() string { return EventTypeTranscript }`, an `isEventPayload()` marker,
and `SetPayloadProto`), plus the `EventTypeTranscript = "transcript"`
discriminator. The `events.type` column is text, so **no SQL migration is
required** — the new arm reuses the existing `payload jsonb` column and the
`(task_id, id)` index. The run version lives inside the payload jsonb, not on a
new `events.version` column; see Trade-offs for why the column was deferred.

**Per-harness mapping.** The driver already extracts most of these fields;
today it only logs them. The normalization keeps the union of what the four
streams expose:

| Field | claude (`stream-json`) | cursor (`stream-json`) | codex (`exec --json`) | copilot (`--output-format=json`, §7) |
|---|---|---|---|---|
| role / text | `assistant`/`user` message content blocks | `assistant` text blocks | `item.type=message` text | message events |
| tool name | `tool_use.name` | mapped from `read/write/edit/bash` variant | `function_call.name` | tool events |
| tool input | `tool_use.input` (decoded) | unwrapped `{args}` (decoded) | `function_call.arguments` (JSON string) | tool input |
| tool_call_id | `tool_use.id` | `call_id` | `function_call` id | tool id |
| tool result | `tool_result.content` + `is_error` | `result` + `is_error` | `function_call_output.output` + `status` | tool result |
| usage | final `result` (tokens, cost, model) | — | — | — (if present) |

**Fidelity deliberately given up vs the raw streams.** The normalized arm is a
projection, not a faithful replay:

- **Reasoning/thinking blocks and provider-internal metadata are dropped.**
  claude `thinking` blocks, cursor/codex internal bookkeeping, message ids,
  and stop reasons are not modeled. (Thinking capture is an open question.)
- **Tool inputs/results are size-capped** (§3), so entries that carried a whole
  file body keep a head + a truncation marker, not the full bytes.
- **Usage is best-effort.** Only claude emits a rich final `result`; the others
  contribute little or nothing, so cross-harness usage is sparse by design.
- **The exact wire framing is not preserved** — this is a normalized event
  view, not the harness's bytes. The raw stream is explicitly *not* stored
  (that was #1297's rejected blob path). If a raw replay is ever needed, the
  harnesses each persist a full local session file as a fallback source; that
  is out of scope here.

### 2. Wire path: a new batched `AppendTranscript` RPC

The `report` tool's `UploadLogs` shim is the prior art for a driver→server
append that becomes an event — but it is single-typed (`type="llm"` only, all
else dropped) and unbatched. Rather than overload it, add a dedicated,
**batched** RPC on `XAgentService`:

```proto
rpc AppendTranscript(AppendTranscriptRequest) returns (AppendTranscriptResponse);

message AppendTranscriptRequest {
  int64 task_id = 1;
  int64 version = 2;                       // run version, stamped on every entry
  repeated TranscriptPayload entries = 3;  // one flush's worth
}
message AppendTranscriptResponse {}
```

**Authorization** reuses the existing surface with no new scope: the driver
already holds a task-scoped JWT and `AppendTranscript` authorizes `task_id`
against the loaded task row via `OpTaskWrite`, exactly like `SubmitRunnerEvents`
and `UploadLogs` (`internal/server/apiserver/`).

**Server handler** (`internal/server/apiserver/transcript.go`): load+authorize
the task once, then in a single transaction `CreateEvent` one row per entry,
stamping `version` into each payload and applying the §3 caps. Reports never
wake the task; **transcript events never wake either** (`Wake: false`) — they
are from-agent, like reports. After commit, publish exactly **one** coalesced
notification for the whole batch (§5).

**Driver batching.** Each harness parse loop currently logs and discards. Add a
small transcript sink the harnesses write normalized `transcript.Entry` values
to (a callback/channel threaded through the existing `Agent` construction in
`internal/agent/interface.go`; the harnesses keep their `slog` summary line and
*additionally* emit an entry). The driver owns the sink and runs a buffered
shipper that flushes on whichever comes first:

- **N entries** buffered (e.g. 50), or
- **T milliseconds** since the last flush (e.g. 1s),

and a **final flush** on run teardown, on the same SIGTERM-surviving context
the driver already uses for terminal runner events (`driver-owned-events.md`).
Batching bounds RPC count and, combined with §5, bounds notification fanout.
Shipping is **best-effort**: a failed `AppendTranscript` is logged and dropped,
never blocking or failing the run — the transcript is observability, not the
run's source of truth.

### 3. Size policy: cap, don't redact; retain and sweep

Full tool results can carry whole file bodies, and a tool-heavy run can produce
hundreds of entries — so size is the load-bearing constraint. Two levers:

**Cap, do not redact.** `internal/agent/toollog` is today's redaction machinery
and is *far too aggressive for this purpose*: it replaces `old_string`,
`new_string`, `content`, `fileText`, `contents` wholesale with `<truncated>`
and clamps summaries to ~200 runes. That is right for a one-line log and wrong
for a transcript — it destroys exactly the content we want to keep. The
transcript path therefore **does not** reuse `toollog.Redact`; `toollog` stays
in place for the `slog` summary lines. Instead the driver applies a **byte
cap** per large field:

- `input` and `content` are truncated to a configurable limit (default **32
  KiB** each) at a UTF-8 boundary; when truncated, `truncated=true` is set and a
  trailing `… [truncated N bytes]` marker is appended.
- A per-run ceiling (default **~5 MiB** of transcript, or a max entry count)
  stops capturing further entries after the limit, emitting a single terminal
  `MESSAGE` entry noting the cap. This bounds a pathological run's DB footprint.

Capping (keep the head, mark the tail) is chosen over field-redaction (drop the
field) because the head of a tool result is usually the useful part, and over
hashing/eliding because a human reading the timeline wants readable content.

**Secret handling.** Tool inputs/results can contain env values and tokens.
This proposal keeps content verbatim within the cap and does **not** add
secret-scrubbing; the transcript is only ever readable by a caller with
`task.read` on the task (same trust boundary as `report`/instruction content
already in the stream). Adding opt-in scrubbing is an open question, not a
blocker.

**Retention / archival.** Transcript events are the bulk of stream growth, so
they get a retention policy distinct from the low-volume arms
(instruction/report/lifecycle/link, which are kept indefinitely as the task's
record). A periodic sweep deletes `type='transcript'` events older than a
configurable window (default **30 days**) and unconditionally when the owning
task is archived. Because transcript rows carry `type='transcript'`, the sweep
is a single indexed `DELETE ... WHERE type='transcript' AND created_at < $1`.
We accept bounded Postgres growth (caps × retention) rather than standing up
object storage — moving transcripts out of Postgres would reintroduce the
blob-store the issue rejected, and can be revisited if row volume demands it.

### 4. Excluding transcript from the agent-facing brief

`GetTaskDetails` (`internal/server/apiserver/task.go`) builds the agent's brief
by listing only the to-agent arms:

```go
events, _ := s.store.ListEventsByTask(ctx, nil, req.Id, caller.OrgID,
    []string{model.EventTypeInstruction, model.EventTypeExternal})
```

`report`, `lifecycle`, and `link` are already excluded for exactly the reason
transcript must be: an agent should not be fed the task's about-task / from-agent
records. **Transcript is deliberately *not* added to that include list** — an
agent must never receive its own (or a prior run's) transcript in `get_my_task`,
or it would drown in its own output and recurse. This needs no code change
beyond keeping the include list as-is; the design point is a regression test in
`internal/agentmcp` / the `GetTaskDetails` handler asserting `transcript` events
never appear in the brief, mirroring the existing report/lifecycle exclusion
tests.

### 5. Notification fanout: coalesce for subscribers, live-tail for the UI

Event creation publishes a `model.Notification` fanned out per-org over SSE
(`internal/server/notifyserver`, `internal/pubsub`). The notification carries
only resource descriptors (`{type, action, id}`) — not payloads — and the Web
UI reacts by invalidating the `listEventsByTask` query (`use-org-sse.ts`).
Hundreds of transcript entries per run must not turn into hundreds of Slack
pings, runner wakeups, or full-timeline refetches — yet a **live-tailing UI
timeline is a desirable outcome**. Reconciled by three rules:

1. **One notification per batch, not per entry.** The §2 shipper flushes in
   batches, and the handler publishes a single notification per flush — so
   fanout is bounded by flush cadence (≤ ~1/s per run), not entry count.
2. **Transcript notifications drive UI refresh only — nothing else.** The
   notification for a transcript batch sets a dedicated resource
   `{Type: "transcript", Action: "appended", ID: task_id}` and leaves
   `Notification.Runner`, `TaskStatus`, and `ChannelMessage` **empty**. Those
   fields are what trigger runner wakeups and external channel messages
   (`SubmitRunnerEvents` populates them for terminal transitions); leaving them
   empty means transcript growth never spams Slack or the runner. `LocalPubSub`
   already drops notifications for slow subscribers with a full buffer, so a
   burst degrades gracefully.
3. **The UI tails incrementally.** A new `transcript` resource case in
   `use-org-sse.ts` refetches the task's events **since the last seen event id**
   (a `ListEventsByTask` variant with an `after_id` cursor) and appends, rather
   than invalidating and refetching the whole stream on every batch. This gives
   a live timeline without O(entries²) refetch cost. Optionally the server-side
   publish can debounce per task (coalesce multiple flushes within a short
   window into one notification) if flush cadence proves too chatty.

### 6. Web UI: rendering the transcript arm, grouped by run version

The timeline already switches on the payload arm in two places
(`webui/src/lib/timeline.ts` `eventsToTimeline` on `e.payload.case`, and
`webui/src/components/task-timeline.tsx` `TimelineRow` on `item.kind`). Add:

- **`eventsToTimeline`**: a `case 'transcript'` mapping each entry to a
  `TimelineItem` carrying `version` and a sub-kind (`message` / `tool-call` /
  `tool-result` / `usage`), with `text` / `toolName` / `input` / `content` /
  `isError` / `truncated`.
- **`TimelineRow`**: a `TranscriptRow` renderer — messages as prose, tool calls
  as a labeled, collapsible input block, tool results as a collapsible
  (error-styled when `is_error`) block, usage as a compact token/cost chip.
  Large capped content renders collapsed with the truncation marker visible.
- **Run-version grouping.** The UI currently shows a flat, ungrouped stream and
  does not surface `Task.version` at all. This proposal introduces grouping the
  timeline into **per-run sections** keyed by the transcript entries' `version`
  (with a "Run N" header), so a task with several runs reads as distinct
  transcripts rather than one blur. Non-transcript events (instruction, etc.)
  interleave by id as today; the run headers are derived from the transcript
  arm's `version` field.
- **Live-tail** via the §5 `transcript` SSE resource case, appending new entries
  as they arrive.

`pnpm lint` in `webui/` must pass (per CLAUDE.md).

### 7. copilot: switch off `--silent` to its JSON output mode

copilot is the one harness that is not capturable today: it runs
`copilot --silent ... --prompt` in **plain-text** mode
(`internal/agent/copilot.go`), so there is no structured stream to parse — the
driver only sees raw lines. Switch copilot to its structured JSON output
(`--output-format=json`, the JSONL mode noted in #1297) and add a
`handleStreamEvent`-style parser mirroring the other three harnesses, emitting
the same normalized `transcript.Entry` values (message text, tool name+input,
tool result). Until this lands, copilot runs simply produce no transcript
events (graceful degradation) — the other three are unaffected. The exact flag
name and JSON shape must be confirmed against the installed copilot version
(Open Question).

## Implementation Plan

An ordered layer cake; each layer is independently reviewable and safe to merge
before the ones above it. Foundation first (inert until a producer exists), then
the wire, then producers, then UI, then retention.

1. **Proto + model arm** — Delivers: `TranscriptPayload` / `TranscriptUsage` /
   `TranscriptKind` in the proto (Event field 10), regenerated code, the
   `model.TranscriptPayload` `EventPayload` implementation, and
   `EventTypeTranscript`. Depends on: nothing. No SQL migration (reuses the
   `payload jsonb` column). Verifiable by: model round-trip tests
   (payload ↔ proto ↔ jsonb) alongside the existing arm tests.
2. **`AppendTranscript` RPC + handler** — Delivers: the RPC, the batched handler
   that writes one capped `events` row per entry in a transaction, stamps
   `version`, sets `Wake:false`, and publishes one non-runner/non-channel
   notification. Depends on: (1). Verifiable by: handler tests — a batch becomes
   N transcript events, caps applied, no wake, no runner/channel fields set.
3. **Brief-exclusion test** — Delivers: a regression test asserting `transcript`
   events never enter `GetTaskDetails`' brief (no code change; guards §4).
   Depends on: (1). Verifiable by: the test fails if `transcript` is added to
   the include list.
4. **Driver transcript sink + shipper** — Delivers: the `transcript.Entry` type,
   the sink threaded through `internal/agent/interface.go`, the driver's
   buffered N-or-T shipper (with byte caps + per-run ceiling) calling
   `AppendTranscript`, stamped with the version already fetched at
   `Driver.Run` start (PR #1298). Depends on: (2). Verifiable by: driver tests
   against a fake client — parsed entries batch, cap, and ship with the run
   version; ship failure never fails the run.
5. **claude / cursor / codex emitters** — Delivers: each harness's
   `handleStreamEvent` emits normalized entries (keeping tool-result content,
   capped not redacted) in addition to its existing `slog` summary. Depends on:
   (4). Verifiable by: parser unit tests over captured `stream-json` fixtures →
   expected normalized entries.
6. **copilot JSON mode + emitter** — Delivers: copilot switched off `--silent`
   to `--output-format=json` with a matching parser/emitter. Depends on: (4).
   Verifiable by: parsing a copilot JSON fixture into normalized entries.
7. **Retention sweep** — Delivers: a periodic `DELETE` of `type='transcript'`
   events past the window and on task archive. Depends on: (2). Verifiable by:
   a sweep test — old transcript rows removed, non-transcript arms untouched.
8. **Web UI** — Delivers: the `transcript` timeline arm, `TranscriptRow`
   renderer, per-run grouping, and the incremental SSE live-tail (`after_id`
   cursor + `transcript` resource case). Depends on: (2) for the events, (5)/(6)
   for real content. Verifiable by: rendering a multi-run task's timeline with
   transcript events grouped by run version; `pnpm lint` passes.

## Trade-offs

- **New `AppendTranscript` RPC vs extending `UploadLogs`.** `UploadLogs` is a
  deliberately narrow shim (`type="llm"` → one `report` event, everything else
  dropped) and unbatched. A dedicated typed, batched RPC keeps the transcript
  path first-class and avoids widening the legacy shim. Rejected: overloading
  `UploadLogs` with a second type — it would resurrect the multi-type log
  surface the events refactor (`65ff0b67`) intentionally collapsed.
- **One `events` row per entry vs one blob per run.** Per-entry rows reuse the
  task-scoped stream, ordering, notification, and timeline machinery wholesale —
  a transcript entry renders exactly like any other event — and enable live
  tailing. The cost is row volume, bounded by §3's caps and retention. A
  single per-run blob would be fewer rows but is the raw-stream store #1297
  rejected, and it can't interleave into the timeline or live-tail without
  re-parsing.
- **Cap-and-keep vs reuse `toollog` redaction.** `toollog` exists and is
  battle-tested, but it is tuned for one-line summaries and drops the exact
  content a transcript needs. Byte-capping keeps the useful head and marks the
  tail; `toollog` stays for the `slog` summaries. Rejected: reusing
  `toollog.Redact` — it would make the transcript as lossy as today's log.
- **Version in the payload jsonb vs a new `events.version` column.** A dedicated
  column would be queryable and could later carry run identity for *all* arms —
  but `task-run-versions.md` explicitly lists "stamping `events` rows with a run
  version" as a deferred non-goal, and only the transcript arm needs run
  grouping today. Embedding `version` in the transcript payload needs no
  migration and touches nothing else; the column remains a clean future
  generalization (a `type='transcript'`-only backfill from the jsonb path).
- **Best-effort shipping vs guaranteed delivery.** The transcript is
  observability, not the run's source of truth (that is the lifecycle/runner
  events). Dropping a batch on RPC failure keeps the agent run unaffected and
  avoids back-pressuring the harness. Rejected: blocking the run on transcript
  acks — it would let an observability path fail a real run.
- **Bounded Postgres growth vs object storage.** Keeping capped, retained
  transcripts in Postgres avoids new infra and keeps everything queryable in one
  place. If volume ever outgrows this, moving the `content`/`input` bodies to
  object storage with a reference in the row is a contained follow-up — but it
  reintroduces exactly the blob dependency #1297 rejected, so it is deferred
  until measured need.

## Open Questions

- **Thinking / reasoning blocks.** claude emits `thinking` blocks in its stream.
  Capture them as a `MESSAGE` sub-role, a distinct kind, or drop them? They are
  high-signal for debugging but add volume and may carry sensitive chain-of-
  thought. Proposed: drop initially, revisit.
- **Cap sizes and per-run ceiling.** Are 32 KiB/field and ~5 MiB/run the right
  defaults, and should they be per-workspace configurable?
- **Retention window and archival.** Is 30 days right, and should archived-task
  transcripts be exported anywhere before deletion or simply dropped?
- **Secret scrubbing.** Should tool inputs/results be scanned for obvious
  secrets (tokens, env values) before storage, given the transcript is readable
  by any `task.read` caller? Proposed: no scrubbing initially (same trust
  boundary as existing report/instruction content).
- **copilot JSON shape.** The exact `--output-format` flag and JSONL schema must
  be confirmed against the installed copilot version before the emitter (layer
  6) can be finalized.
- **Notification debounce.** Is one notification per batch sufficient, or does a
  busy run still need server-side per-task debouncing of transcript
  notifications to protect SSE subscribers?
