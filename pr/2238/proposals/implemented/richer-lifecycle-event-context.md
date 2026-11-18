# Richer Lifecycle Event Context

Issue: https://github.com/icholy/gritz/issues/1165

## Problem

The sandbox lifecycle events in a task's timeline are just a status transition with
no detail. `Sandbox started`, `Sandbox exited (Running -> Completed)`, and
`Sandbox failed` tell you *that* something happened but never *why*. When a
sandbox fails, the operator is left with a bare "Sandbox failed" and has to go
dig through container logs to find the actual error — which for a dispatch
failure (bad image, failed setup command) may not even be in the agent logs.

The reason exists at the source and is thrown away:

- The **driver** (`internal/agent/driver.go`) has the concrete error from the
  agent/setup run in scope at the moment it decides to emit `failed`:

  ```go
  err := d.run(ctx)
  // ...
  event := model.RunnerEventStopped
  if err != nil {
      d.Log.Error("task failed", "error", err) // <-- err is right here
      event = model.RunnerEventFailed
  }
  if serr := d.submit(eventCtx, event); serr != nil { // <-- but submit only takes the type
      return errors.Join(err, serr)
  }
  ```

- The **runner** (`internal/runner/runner.go`) emits `failed` on dispatch
  failures (`r.Start` returns an image-pull / boot error) and on "sandbox exited
  without reporting" (`supervise`, `failIfTaskRunning`) — with the underlying
  error or non-zero exit code in scope — and drops all of it.

- The **server** (`internal/model/task.go`, `RunnerEvent.LifecycleEvent`)
  therefore hardcodes the message to the constant string `"container failed"`
  for every `SANDBOX_FAILED` event:

  ```go
  case RunnerEventFailed:
      return lifecycle(LifecycleKindSandboxFailed, "container failed"), true
  ```

The important discovery is that **the persistence and render paths already
support a message end to end** — the only missing link is transport. This
proposal closes that gap and lays out how to keep the door open for richer
structured context later without a migration.

## Current path, end to end

```
driver.submit(RunnerEventFailed)                 internal/agent/driver.go
  → SubmitRunnerEvents RPC (RunnerEvent proto)   proto/gritz/v1/gritz.proto:407
    → ApplyRunnerEvent (status fold)             internal/model/task.go:185
    → RunnerEvent.LifecycleEvent(task, from)     internal/model/task.go:282
        → LifecyclePayload{Kind: SANDBOX_FAILED, Message: "container failed"}
    → store.CreateEvent (payload jsonb)          internal/store/event.go:14
  → ListEventsByTask RPC
    → eventsToTimeline / lifecycleSummary        webui/src/lib/timeline.ts:90
        → "Sandbox failed: container failed"
```

What already exists and needs **no change**:

- `LifecyclePayload` has a `message` field in both the proto
  (`proto/gritz/v1/gritz.proto:348`, `string message = 5`) and the Go model
  (`internal/model/event.go:180`).
- The events table stores the payload as `jsonb`
  (`internal/store/sql/migrations/20260614000001_event_typed_payload.sql`), with
  a separate `type text` discriminator column. Adding fields to a payload is a
  pure JSON change — **no migration**.
- Both renderers already print the message: Go `LifecyclePayload.Summary()`
  (`internal/model/event.go:237`) and TS `lifecycleSummary()`
  (`webui/src/lib/timeline.ts:122`) emit `Sandbox failed: <message>`.

What is missing: the `RunnerEvent` message
(`proto/gritz/v1/gritz.proto:407`) has only `task_id`, `event`, `version`,
`reconcile` — no field to carry the reason from the driver/runner to the server.
So `LifecycleEvent` has nothing to thread through and falls back to the constant.

## Design

### 1. Carry the reason on `RunnerEvent`

Add a single free-form field to the runner event transport.

```protobuf
message RunnerEvent {
  int64 task_id = 1;
  string event = 2;     // "started", "stopped", "failed"
  int64 version = 3;    // Current version, or 0 for bypass
  bool reconcile = 4;   // True if from reconciliation, not real-time
  string reason = 5;    // Optional human-readable detail (failure reason, etc.)
}
```

Mirror it on the Go model (`internal/model/task.go`):

```go
type RunnerEvent struct {
    TaskID    int64
    Event     RunnerEventType
    Version   int64
    Reconcile bool
    Reason    string // optional, currently populated only for "failed"
}
```

`reason` is optional and defaults to empty, so it is invisible to `started` /
`stopped` and to any producer that doesn't set it. It is threaded through
`Proto()` / `RunnerEventFromProto()` alongside the existing fields.

### 2. Producers populate the reason

**Driver** (`internal/agent/driver.go`) — thread the error into the event.
Rather than growing `submit`'s positional argument list as `RunnerEvent` gains
fields, change its signature to take a `model.RunnerEvent` value and let the
caller build it. `submit` today constructs the event internally from a bare
`model.RunnerEventType`:

```go
func (d *Driver) submit(ctx context.Context, event model.RunnerEventType) error {
    re := model.RunnerEvent{TaskID: d.TaskID, Event: event}
    _, err := d.Client.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
        Events: []*gritzv1.RunnerEvent{re.Proto()},
    })
    // ...
}
```

Move the `RunnerEvent` construction out to the callers so the reason (and any
future field) rides on the value:

```go
func (d *Driver) submit(ctx context.Context, re model.RunnerEvent) error {
    _, err := d.Client.SubmitRunnerEvents(ctx, &gritzv1.SubmitRunnerEventsRequest{
        Events: []*gritzv1.RunnerEvent{re.Proto()},
    })
    // ...
}
```

The `started` emit becomes `d.submit(eventCtx, model.RunnerEvent{TaskID: d.TaskID,
Event: model.RunnerEventStarted})`, and the terminal emit fills in the reason on
failure:

```go
re := model.RunnerEvent{TaskID: d.TaskID, Event: model.RunnerEventStopped}
if err != nil {
    d.Log.Error("task failed", "error", err)
    re.Event, re.Reason = model.RunnerEventFailed, err.Error()
}
if serr := d.submit(eventCtx, re); serr != nil { ... }
```

The driver's error already distinguishes setup failures from agent failures
(they surface as different wrapped errors from `d.setup` vs the agent run), so
the reason string naturally carries that context, e.g.
`setup command "npm ci" failed: exit status 1`.

**Runner** (`internal/runner/runner.go`) — set a reason at each `failed` emit:

| Emit site | Reason |
|---|---|
| `Poll` start/restart dispatch failure (`r.Start` err) | `err.Error()` (e.g. image pull failure) |
| `supervise` non-zero exit without a driver report | `fmt.Sprintf("sandbox exited without reporting (exit code %d)", code)` |
| `failIfTaskRunning` (reconcile: sandbox gone, task still running) | `"sandbox exited without reporting"` |

These are the cases where **no driver exists** to describe the failure, so the
runner is the only source of a reason — exactly the cases that today are the most
opaque ("Sandbox failed" with nothing in the agent logs because the agent never
ran).

### 3. Server threads the reason instead of hardcoding

`RunnerEvent.LifecycleEvent` (`internal/model/task.go:282`) uses the event's
reason, falling back to the existing constant when a producer left it empty (old
runners, belt-and-suspenders):

```go
case RunnerEventFailed:
    msg := e.Reason
    if msg == "" {
        msg = "container failed"
    }
    return lifecycle(LifecycleKindSandboxFailed, msg), true
```

`SubmitRunnerEvents` (`internal/server/apiserver/runner.go`) needs no structural
change — it already calls `event.LifecycleEvent(task, from)` and persists the
result. `RunnerEventFromProto` now carries `Reason`, so it flows in for free.

Optionally, the terminal `ChannelMessage` in the same handler could include the
reason (`Task 42 failed: <reason>`) so `gritz notify` subscribers see it too.
Recommended as a follow-up, not load-bearing for this proposal.

### 4. Sanitize and bound the reason

Error strings are attacker-adjacent free text (they can be long, contain
newlines, or echo secrets from a failed command). Before it becomes a
`LifecyclePayload.Message`, the server (in `LifecycleEvent` or the handler)
should:

- **Truncate** to a fixed cap (e.g. 1 KiB) with an ellipsis. The timeline is a
  summary surface, not a log viewer.
- **Collapse** to a single line (or keep the first line) for the summary
  rendering — the full string is still stored in the payload.

Truncation belongs on the server so the cap is enforced regardless of which
producer (driver, runner, future callers) set the reason.

### 5. Store / migration impact

**None.** The `events.payload` column is `jsonb` and `LifecyclePayload.Message`
already exists. New `SANDBOX_FAILED` events simply store a non-empty `message`
where they previously stored `"container failed"`. No schema change, no data
backfill.

The one new column-ish thing is the `reason` field on the `RunnerEvent`
*protobuf* — a wire/RPC message, not a table — so there is no DB migration for it
either. `RunnerEvent`s are transient (submitted, folded, discarded); they are
never stored.

### 6. Backward compatibility

- **Old stored events**: `SANDBOX_FAILED` rows written before this change keep
  their `"container failed"` message and render exactly as they do today.
  Reading is unchanged.
- **Old driver / new server**: an old driver submits a `RunnerEvent` with no
  `reason` field. Proto field addition is wire-compatible; the field decodes to
  `""`, and the server's fallback restores `"container failed"`. No breakage.
- **New driver / old server**: the extra `reason` field is ignored by an old
  server (unknown-field skipping), which continues to hardcode the message. The
  reason is silently dropped until the server is upgraded — no error.
- **UI**: `lifecycleSummary` already renders whatever `message` is present. A
  richer message needs zero UI change to appear. (See UI section for optional
  presentation polish.)

### 7. UI display

The reason surfaces automatically: `lifecycleSummary` (`webui/src/lib/timeline.ts:122`)
already appends `: <message>` for `SANDBOX_FAILED`, and the Go `Summary()` does
the same for the MCP `get_my_task` output. No change is *required*.

Optional polish, given failure reasons are now real (and potentially multi-line
even after single-line collapsing for the summary):

- Keep the one-line summary as-is, but render the failed lifecycle item with a
  `failed` tone (already handled — `lifecycleCategory` returns `'failed'` for
  `SANDBOX_FAILED`) and show the full stored `message` in a hover/expand, the way
  the timeline treats other detail-bearing items.
- Since the truncated/collapsed summary and the full payload can differ, the UI
  should read `message` directly for the expanded view rather than re-deriving
  from the summary string.

This is presentation-only and can ship separately from the backend change.

## Trade-offs

The core decision is **how structured** the carried context should be. Three
options, from lightest to heaviest:

### Option A — Free-form `reason` string (recommended)

Add `string reason` to `RunnerEvent`, thread it into the existing
`LifecyclePayload.message`. This is the design above.

- **Pros**: Minimal surface. Reuses the `message` field, the JSONB store (no
  migration), and both existing renderers. Ships the exact driving example — the
  real failure reason in the timeline — in a handful of lines. Forward- and
  backward-compatible by construction.
- **Cons**: Untyped. Consumers can't filter/branch on *why* it failed (setup vs
  agent vs dispatch) without string-matching. Mixes machine detail (exit codes)
  and human prose in one field.

### Option B — Structured/typed detail payload

Give `LifecyclePayload` a typed sub-message for failure context, e.g.

```protobuf
message SandboxFailure {
  string message = 1;       // human-readable
  int32 exit_code = 2;      // 0 if N/A
  FailurePhase phase = 3;   // SETUP, AGENT, DISPATCH, LOST_REPORT
}
```

carried either as a new `oneof detail` on `LifecyclePayload` or a dedicated
optional field, and a matching structured field on `RunnerEvent`.

- **Pros**: Machine-actionable. The UI could badge "setup failure" vs "agent
  error"; future automation (auto-retry only on dispatch failures, say) becomes
  possible. Cleanly extensible per-kind (each lifecycle kind could grow its own
  detail arm).
- **Cons**: Requires designing the taxonomy up front (what are the phases? which
  exit codes matter?) and getting it right, or churning the enum later. More
  proto surface, more mapping code, more UI work — for value no current consumer
  needs. The driver's error already encodes the phase in its prose, so the
  human-facing win over Option A is marginal today. Still no migration (JSONB),
  but meaningfully more code.

### Option C — Free-form key/value map (`google.protobuf.Struct` or `map`)

Attach an arbitrary `map<string,string>` / `Struct` of context to the lifecycle
payload.

- **Pros**: Maximally flexible; new keys need no schema change at all.
- **Cons**: Untyped *and* unindexed *and* undiscoverable — the worst of both
  worlds for a small, well-understood set of fields. No schema means every
  consumer invents its own key conventions, and the timeline renderer can't know
  what to show. Over-engineered for "put the error message in the event."

### Recommendation

**Option A now, with Option B as the documented extension path.**

The concrete need is a human-readable failure reason in the timeline, and the
render + store plumbing for exactly that (`message` + `jsonb`) already exists.
Option A delivers it with the least code and zero migration, and is
forward-compatible: because payloads are JSONB, a later `SandboxFailure` /
`oneof detail` (Option B) can be *added alongside* `message` without a migration
and without invalidating any stored event — `message` stays the human string,
the structured arm carries machine fields when a second consumer (badging,
auto-retry) actually appears. Building the taxonomy speculatively now would be
guessing at requirements we don't have.

In short: thread the string today, keep `message` as the stable human-facing
field, and grow a typed detail arm on `LifecyclePayload` when — and only when —
something needs to branch on the structure.

## Open Questions

- **Truncation cap**: is 1 KiB the right bound, and should the summary keep the
  first line or the last line of a multi-line error? (Last line is often the
  actual cause; first line is often the wrapper.)
- **Redaction**: should the server scrub obvious secret shapes from the reason
  before storing, or is a failing command's output considered already inside the
  trust boundary (the operator can see the container logs anyway)?
- **`ChannelMessage` inclusion**: should terminal `failed` notifications
  (`gritz notify`) include the reason, or keep them terse (`Task N failed.`) and
  make the reason timeline-only?
- **Non-failure context**: `started` / `stopped` gain a `reason` field they never
  populate. Fine as an unused optional, or should the transport field be named
  more neutrally (e.g. `detail`) to invite future non-failure use?