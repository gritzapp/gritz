---
title: "Channel-Message-Gated Notifications"
description: "The notification bus forwards every task change to the agent channel unfiltered, so host Claude Code sessions receive noisy events instead of meaningful summaries."
---

## Problem

`model.Notification` (`internal/model/notification.go`) is a shared change bus published by `apiserver.Server.publish` (`internal/server/apiserver/apiserver.go:54`) and a few other emitters, fanned out to three very different consumers:

1. **Runners.** They watch `Notification.Runner` (`for_runner`) to learn which runner has pending work to claim. They don't care about the resources at all.
2. **Web UI** (TanStack Query, `webui/src/lib/notification-sse.ts`). It inspects `Resources: [{action, type, id}]` to know which queries to invalidate and refetch. The thin envelope is exactly what it wants — the FE re-fetches the canonical resource from the API, it doesn't try to render the notification itself.
3. **Agent channel events.** The local `gritz mcp` bridge (`internal/command/mcp.go:86`) subscribes to the SSE stream and pushes task changes into the host Claude Code session as `notifications/claude/channel` events via `mcpserver.ForwardNotification` (`internal/server/mcpserver/channel.go:21`).

Consumer (3) is the broken one. `ForwardNotification` today:

```go
func ForwardNotification(ctx context.Context, sender ChannelSender, n model.Notification) error {
    if n.Type != "change" {
        return nil
    }
    var errs []error
    for _, r := range n.Resources {
        if r.Type != "task" {
            continue
        }
        errs = append(errs, sender.SendChannel(ctx, mcpchannel.Params{
            Content: fmt.Sprintf("task %d was %s.", r.ID, r.Action),
            Meta: map[string]string{
                "resource": "task",
                "id":       strconv.FormatInt(r.ID, 10),
            },
        }))
    }
    return errors.Join(errs...)
}
```

Three problems with that:

- **Lossy verbs.** Every task-mutating site emits `Action: "updated"` (or `"created"` / `"archived"` / …), but a runner event that transitions a task to `RUNNING`, a runner event that transitions it to `COMPLETED`, and an instruction being appended all collapse into the same string: `task N was updated.`
- **Dropped resources.** `r.Type != "task"` discards every event-, link-, and log-typed resource. The interesting wake case — `eventrouter.attach` (`internal/eventrouter/eventrouter.go:116-154`), where a GitHub PR comment or Jira webhook routes through the router and starts the linked task — is conveyed in part by `[{type:"event",action:"updated"}]` plus a task resource. The channel session sees `task N was updated.` with no hint *why*.
- **Context is gone by the time the bridge sees it.** The semantic context that would make the notification useful (which status the task moved to, which event woke it, what changed in `UpdateTask`, which runner event arrived) exists only at the publish call site. By the time the bridge receives the SSE frame, all it has is `(action, type, id)`. Reconstructing the *cause* from the post-state by fetching the task is impossible: a task observed in `RUNNING` could have just started, or could have been running for an hour while a log line was appended.

We need to enrich the channel-facing output without disturbing consumers (1) and (2), who are perfectly happy with the current envelope.

## Design

### A single optional field

Add one field to `model.Notification`:

```go
type Notification struct {
    Type      string                 `json:"type"`
    Resources []NotificationResource `json:"resources,omitempty"`
    Time      time.Time              `json:"timestamp"`
    OrgID     int64                  `json:"org_id"`
    UserID    string                 `json:"user_id,omitempty"`
    ClientID  string                 `json:"client_id,omitempty"`
    Runner    string                 `json:"for_runner,omitempty"`
    ChannelMessage string            `json:"channel_message,omitempty"` // NEW
}
```

`ChannelMessage` is free-text, human-readable, and authored by the publishing call site from the context already local to it. It is intentionally *not* structured: the channel content is rendered into a `<channel>` tag in Claude's context, and a sentence is what the model can act on. The name is deliberately specific to its sole consumer — this field exists for, and is only consumed by, the agent channel forwarder. Naming it `Summary` would invite the FE or runner to grow opinions about it; they should not.

### Channel message as the gate

`ForwardNotification` becomes a pure relay:

```go
func ForwardNotification(ctx context.Context, sender ChannelSender, n model.Notification) error {
    if n.Type != "change" || n.ChannelMessage == "" {
        return nil
    }
    meta := map[string]string{}
    if id, ok := primaryTaskID(n.Resources); ok {
        meta["resource"] = "task"
        meta["id"] = strconv.FormatInt(id, 10)
    }
    return sender.SendChannel(ctx, mcpchannel.Params{
        Content: n.ChannelMessage,
        Meta:    meta,
    })
}
```

Key consequences:

- **`ChannelMessage == ""` ⇒ no channel event.** The bridge stays silent. This is how we drop lifecycle churn (key created, workspace registered, log line appended, link created by the agent itself) that would otherwise spam the Claude session.
- **One frame per notification, not one per resource.** The fan-out-over-resources loop disappears. A single `Resources: [{task, updated}, {event, updated}]` produces one well-formed sentence.
- **The `type != "task"` filter is gone.** Whether resources contain a task, an event, both, or neither, the gate is `ChannelMessage != ""`. Resource info is still carried to populate `meta` attributes (so `get_task` from the model can find an `id` to query), but resource presence is no longer the trigger.
- **No fallback string.** Absence of `ChannelMessage` is interpreted as *intentional* silence, not "render the default."

### Backward compatibility

- **Runner consumer:** unaffected. It reads `Runner` and ignores everything else.
- **Web UI consumer:** unaffected. It reads `Resources` and ignores everything else. JSON parsing tolerates the new optional field; existing FE never deserializes `channel_message` so the existing TypeScript types don't need to change.
- **SSE wire format:** purely additive (`channel_message` is `omitempty`). Old SSE clients keep working.
- **No migration:** the field is in-memory and on the SSE wire; nothing in PostgreSQL changes.

The FE *could* later choose to surface the message (toast notifications, activity feed); that's out of scope here. The point is the field is freely available to it if it ever wants it.

### Setting `ChannelMessage`: the terminal-or-queued rule

Rather than authoring a bespoke message at every `s.publish(...)` site, apply a single rule. At every publish site that has a task in scope, set `ChannelMessage` if and only if one of these conditions holds:

- **Terminal status reached.** `task.Status ∈ {TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled}` — exactly the set already grouped at `task.go:279`. The message is:
  - `"Task N completed."`
  - `"Task N failed."`
  - `"Task N cancelled."`

- **Task is now queued for a runner.** `task.PendingRunner() != ""` (`task.go:412`). This is the same value already assigned to `notification.Runner` at every queueing site. The message is the site-specific *cause* of the queueing (wake, instruction, restart, creation).

- **Neither condition holds.** Leave `ChannelMessage = ""` → silent.

Everything else (`Pending` not just queued, `Running` mid-task, `Restarting`, `Cancelling`, non-transitions) stays silent. The cases are mutually exclusive: a task reaching a terminal status has `Command == None` and so `PendingRunner() == ""`, so the terminal branch fires; a task with a pending runner command is, by definition, not in a terminal status, so the queued branch fires.

#### Why this rule is the right one

It cleanly handles the cases the prior draft of this proposal got wrong:

- **`runner.go` `SubmitRunnerEvents`** publishes whenever `task.ApplyRunnerEvent` returns `applied == true`. But `applied == true` does **not** imply a status transition. `applyRunnerEventStarted` (`task.go:204-228`) returns `true` for a `Running → Running` no-op transition when `Command == Start`/`Restart` (lines 213-218), and a `started` event on an archived task yields `Cancelling` (lines 206-210). A bespoke "switch on resulting `task.Status`" would re-emit `"Task N is now running."` on every started event of an already-running task and reintroduce the noise the gate is meant to remove. Under the terminal-or-queued rule:
  - `Running → Running` (no-op): non-terminal, `Command == None` after the no-op match → `PendingRunner() == ""` → silent. ✓
  - Archived task started → `Cancelling`: non-terminal, but `Archived == true` makes `PendingRunner()` return `""` regardless of `Command` → silent. ✓
  - `stopped` with `Command == Start` (container exited, restart pending) → `Pending`, `Command == Start` → `PendingRunner()` returns the runner → queued message fires (`"Task N restarting."`). ✓
  - `stopped` with `Command == None` → `Completed` → terminal message fires. ✓
  - `failed` from `Running` → `Failed` → terminal message fires. ✓

- **`CancelTask`** has two branches: cancelling a `Pending` task transitions to `Cancelled` directly (terminal fires); cancelling a `Running` task transitions to `Cancelling` with `Command == Stop` (queued fires with `"Task N cancellation requested."`). Both surface; no bespoke handling needed.

- **`ArchiveTask`** sets `Archived = true`; `PendingRunner()` returns `""` for archived tasks regardless of `Command`, and the task's terminal status was reached and announced earlier. Silent.

- **`UpdateTask`** is queued only when `req.Start` is true (it then calls `task.Start()`, which sets `Command = Start` and increments version). If `Start` is false and the caller only added instructions or changed the name, `Command` stays `None` → silent. That's correct: a name change is not agent-actionable.

The implementation is a small helper on `model.Task` that returns the message string (or `""`), called at each publishing site that has a task in scope:

```go
// ChannelMessage returns the agent-channel notification line for the task's
// current state, or "" if the state isn't channel-worthy. The cause string is
// only used for the queued case; terminal status produces its own message.
func (t *Task) ChannelMessage(cause string) string {
    switch t.Status {
    case TaskStatusCompleted:
        return fmt.Sprintf("Task %d completed.", t.ID)
    case TaskStatusFailed:
        return fmt.Sprintf("Task %d failed.", t.ID)
    case TaskStatusCancelled:
        return fmt.Sprintf("Task %d cancelled.", t.ID)
    }
    if t.PendingRunner() != "" {
        return cause
    }
    return ""
}
```

The shape gives every publishing site exactly one decision: write the cause sentence for "if this queued work, here's why," and let the helper apply the gate.

#### Publishing-site survey

A repo-wide grep (`rg "\.[Pp]ublish\(" internal/`) finds publishers in three packages: `internal/server/apiserver/`, `internal/eventrouter/`, and `internal/server/archiver/`. The full set:

| File:line | Site | Cause passed to helper | Result |
| --- | --- | --- | --- |
| `apiserver/task.go:120` | `CreateTask` | `fmt.Sprintf("Task %d created on %s/%s.", task.ID, task.Runner, task.Workspace)` | always queued → cause fires |
| `apiserver/task.go:230` | `UpdateTask` | `fmt.Sprintf("Task %d queued: %s.", task.ID, strings.Join(changed, ", "))` | queued iff `req.Start` (otherwise silent) |
| `apiserver/task.go:275` | `ArchiveTask` | `""` | `PendingRunner == ""` (archived) → silent |
| `apiserver/task.go:320` | `UnarchiveTask` | `""` | `PendingRunner == ""` (no command set) → silent |
| `apiserver/task.go:365` | `CancelTask` | `fmt.Sprintf("Task %d cancellation requested.", task.ID)` | terminal in Pending branch, queued in Running branch |
| `apiserver/task.go:410` | `RestartTask` | `fmt.Sprintf("Task %d restart requested.", task.ID)` | always queued |
| `apiserver/runner.go:56` | `SubmitRunnerEvents` | `fmt.Sprintf("Task %d restarting.", task.ID)` (only meaningful in the re-queue case) | terminal/queued/silent per rule above |
| `apiserver/event.go:45` | `CreateEvent` | (no task in scope) | silent |
| `apiserver/event.go:78` | `DeleteEvent` | (no task in scope) | silent |
| `apiserver/event.go:111` | `AddEventTask` (RPC) | (no task fetched; not the wake path) | silent — see next section |
| `apiserver/event.go:147` | `RemoveEventTask` | (no task fetched) | silent |
| `apiserver/link.go:36` | `CreateLink` | (no task fetched; agent-created link is self-echo) | silent |
| `apiserver/log.go:31` | `UploadLogs` | (high-frequency log spam) | silent |
| `apiserver/org.go:108` | `AddOrgMember` | (admin housekeeping) | silent |
| `apiserver/org.go:143` | `RemoveOrgMember` | (admin housekeeping) | silent |
| `apiserver/key.go:36` | `CreateKey` | (API key management) | silent |
| `apiserver/key.go:67` | `DeleteKey` | (API key management) | silent |
| `apiserver/workspace.go:31` | `RegisterWorkspaces` | (runner startup) | silent |
| `apiserver/workspace.go:64` | `ClearWorkspaces` | (runner reset) | silent |
| `eventrouter/eventrouter.go:152` | `attach` | wake message — see next section | always queued |
| `archiver/archiver.go:142` | `archive` | `""` | silent — see below |

`archiver.archive` is left silent intentionally. Auto-archive runs after a terminal task's `archive_after` deadline (`archiver.go:79-105`); the terminal message already fired earlier when the task hit `Completed`/`Failed`/`Cancelled`. The archive transition itself is admin cleanup, not agent-actionable, and doesn't change the task's effective state from the agent's point of view.

### The motivating case: wake belongs in `eventrouter.attach`

`eventrouter.attach` (`internal/eventrouter/eventrouter.go:116-154`) is the real external-event wake. It is invoked from `Router.Route` (line 73) when an inbound `InputEvent` matches a `subscribe=true` link on a task. It calls `r.Store.AddEventTask` directly inside its own transaction (line 123), calls `task.Start()` (line 130), and publishes its own `model.Notification` (line 152). The apiserver `AddEventTask` handler (`event.go:111`) is a separate, manual-RPC association path — not the path the GitHub/Jira pollers use, and not where the wake message should live.

`attach` already has the full `*model.Event` in scope (the second parameter). It can build the cause sentence directly from `event.Description` and `event.URL` with **no extra `GetEvent` round-trip**:

```go
cause := fmt.Sprintf("Task %d woken by event %d: %s", task.ID, event.ID, event.Description)
if event.URL != "" {
    cause += " (" + event.URL + ")"
}
notification.ChannelMessage = task.ChannelMessage(cause)
```

`attach` always calls `task.Start()`, so `PendingRunner()` is non-empty by the time the message is composed → the queued branch of the helper fires.

Rendered example (PR comment routed to a task):

```
<channel source="gritz" resource="task" id="42">
Task 42 woken by event 17: PR comment from alice on icholy/gritz#481: please rebase (https://github.com/icholy/gritz/pull/481#issuecomment-...)
</channel>
```

That sentence, plus the embedded URL, is enough for the local Claude session to decide whether to immediately fetch the PR, call `get_task`, or wait.

### What stays out

- **Channel `meta`.** The current `meta` map (`resource`, `id`) stays for task notifications so the model can still find a `task_id` to call `get_task` with. No `channel_message` meta — the message is the body.
- **Resource fan-out.** The forwarder no longer emits one event per resource. One notification = one channel event.
- **Notification batching.** Out of scope. If channel-event volume becomes a problem, batching belongs to the bridge, not to `model.Notification`.

## Trade-offs

### Free-text `ChannelMessage` vs. structured `Kind` + typed fields

We could replace `ChannelMessage string` with something like:

```go
type Notification struct {
    // ...
    Kind  string         `json:"kind,omitempty"`   // "task_started", "task_woken_by_event", ...
    Cause map[string]any `json:"cause,omitempty"`  // typed payload per Kind
}
```

The bridge would then look up a template per `Kind` and render the sentence client-side.

Rejected because:

- **The renderer would live in the bridge,** not in the server. Adding a new event kind would mean updating two places (publish site + renderer table). Today's `ForwardNotification` is a pure relay; the kind table re-introduces the coupling we're trying to eliminate.
- **Channel content is for humans** (well, an LLM that reads English). Structuring the payload only to turn it back into English at the edge is busywork. The publishing site already has the raw values formatted in its log lines; it's the natural place to write the sentence.
- **A free-text field doesn't preclude structure later.** If a future consumer needs `Kind`-style routing, it can be added additively; the message becomes the human-readable fallback.

### Enriching at the channel boundary (fetch on receipt)

The bridge could, on every received notification, fetch the affected task/event/link and render a sentence from the fresh state. Considered and rejected because:

- **Race-y.** By the time the bridge fetches, the task may have transitioned again. The fetched state isn't necessarily the state that triggered the notification.
- **Loses the cause.** A task in `RUNNING` could have just started, or could have been running while a log line appended. The boundary cannot distinguish them.
- **Costs an RPC per notification.** Channel events fire frequently; an extra round-trip per event scales poorly.
- **Re-reads what we already had.** The publish site computed `changed []string`, decided the new status, and knows which event id was just attached. Throwing that away and re-reading it from disk is wasteful.

The publishing site is the only place where the cause is unambiguously known; the message belongs there.

### Channel gate vs. always-emit-with-fallback

We could have `ForwardNotification` fall back to the existing `task N was updated.` template when `ChannelMessage == ""`. Rejected:

- **It defeats the silencing.** The whole reason `UploadLogs`, `RegisterWorkspaces`, and `ArchiveTask` set `""` is that we *don't* want a channel event for them. A fallback would re-create today's noise.
- **It hides bugs.** If a call site forgets to set `ChannelMessage`, we want the agent to notice the missing event (and us to fix it) rather than papering over with a useless string.

Absence of `ChannelMessage` is part of the contract: it means "this is silent on the channel."

## Test plan

The change is small enough to cover with a handful of focused tests.

**`internal/server/mcpserver/channel_test.go`** — replace the existing `TestForwardNotification` (which asserts the lossy template) with:

- `TestForwardNotification_GatesOnEmptyChannelMessage` — feed a `Notification{Type: "change", Resources: [...], ChannelMessage: ""}`, assert `sender.calls == 0`.
- `TestForwardNotification_RelaysChannelMessage` — feed `ChannelMessage: "Task 1 completed."`, assert exactly one `SendChannel` call with that content and `meta` populated from the first task resource.
- `TestForwardNotification_NoTaskResource` — feed a notification whose `Resources` contain no task entries but `ChannelMessage` is set, assert one call with no `id` in `meta`. Confirms the gate is the message, not the resource type.
- `TestForwardNotification_IgnoresNonChange` — feed `Type: "ready"`, assert zero calls.

**`internal/model/task_test.go`** — exercise the gate helper directly so the rule is documented next to the code that implements it:

- `TestTask_ChannelMessage_Terminal` — table-driven over `{Completed, Failed, Cancelled}`, asserts the helper returns the expected sentence regardless of the `cause` argument.
- `TestTask_ChannelMessage_Queued` — task in `Pending` with `Command = Start` on a runner, asserts the helper returns the passed `cause` verbatim.
- `TestTask_ChannelMessage_Silent` — covers the regression cases the prior draft got wrong:
  - `Running → Running` no-op (Status=`Running`, Command=`None` after the no-op match).
  - Started event on an archived task (Status=`Cancelling`, Archived=`true`).
  - `Restarting` mid-flight.
  - `Pending` with no command.

  All assert the helper returns `""`.

**`internal/eventrouter/eventrouter_test.go`** — `TestRouter_AttachSetsWakeMessage`: build a router with an in-memory pubsub publisher, attach an event with a known `Description` and `URL` to a pending task, capture the published notification, assert `ChannelMessage` contains the task id, event id, description, and URL.

**`internal/server/apiserver/publish_test.go`** — one representative emission-site assertion in addition to the helper tests:

- `TestUpdateTask_ChannelMessage_QueuedOnStart` — call `UpdateTask` with `Start: true`, assert the published notification has a non-empty `ChannelMessage` that contains `"queued"` (or whatever wording the helper produces for the queued case at this site).
- `TestUpdateTask_NoChannelMessage_NameOnly` — call `UpdateTask` with only `Name` set (no `Start`), assert `ChannelMessage == ""`.
- `TestUploadLogs_NoChannelMessage` — call `UploadLogs`, assert `ChannelMessage == ""` (regression guard for the log-spam silencing decision).

No new test infrastructure is needed; the in-memory `pubsub` publisher (`internal/pubsub/local.go`) already captures published notifications for inspection.

## Open Questions

- **Locale / wording.** The messages are written in English. The codebase has no i18n elsewhere; flagging for completeness, not as a blocker.
- **Truncation.** Should `ForwardNotification` cap message length (e.g. 500 chars) to defend against a runaway `event.Description`? Probably yes — a one-line guard in the forwarder is cheap insurance. Default cap to be decided in implementation.
- **FE consumption.** Whether the FE should also surface `ChannelMessage` (e.g. as a toast title) is out of scope; flagged so the implementer doesn't accidentally remove the field from any FE-side TypeScript types it later acquires.
