---
title: "Runner SSE Subscription Instead of Polling"
description: "The runner polls for pending task commands every 5 seconds, adding up to 5 seconds of pickup latency and needless database load when nothing has changed."
---

Issue: https://github.com/icholy/gritz/issues/527

## Problem

The runner uses a fixed-interval polling loop (default 5 seconds) to discover pending task commands. Every cycle it calls `ListRunnerTasks`, which queries the database for tasks with `command != 0`. This introduces up to 5 seconds of latency before a new or restarted task is picked up, and generates unnecessary database load when nothing has changed.

The server already has a pub/sub system (`internal/pubsub/`) and an SSE endpoint (`/events`) that pushes real-time change notifications to the web UI. The runner should subscribe to this same notification stream so it reacts to task changes immediately.

## Design

### High-level approach

SSE is a **wake-up signal**, not a data channel. The runner still calls `ListRunnerTasks` to get the authoritative list of pending commands — an SSE notification only tells it *when* to poll. This keeps the command-dispatch logic in one place and means the runner always acts on fresh state.

```
Before:  loop { Poll(); sleep(5s) }
After:   loop { Poll(); waitForSSEOrTimeout(fallback) }
```

The runner polls immediately when it receives a relevant SSE notification, and falls back to a periodic poll as a safety net for missed notifications.

### Reuse `/events` with a `runner` filter (no new endpoint)

The original draft proposed a dedicated `/events/runner` endpoint, justified by the claim that `/events` filters out self-notifications server-side. **That claim was wrong**: `handleSSE` forwards every notification for the subscribed org. The web UI's self-notification suppression is entirely client-side (`webui/src/lib/notification-sse.ts` compares `client_id`). So no separate endpoint is needed — `/events` already:

- Authenticates via the same `CheckAuth` middleware, which accepts the runner's API-key bearer token (the old `X-Auth-Type` header dispatch has since been removed).
- Delivers all org notifications, including ones the runner itself triggered.

Instead, `/events` gained an optional `runner` query parameter. When set, the handler forwards only notifications whose `Runner` field matches:

```go
// internal/server/notifyserver/sse.go
runner := r.URL.Query().Get("runner")
...
case n := <-ch:
    if runner != "" && n.Runner != runner {
        continue
    }
    // forward
```

The `ready` event is sent before the loop, so it always reaches the client. With no `runner` param the behavior is unchanged, so the web UI is unaffected.

### `Notification.Runner`: routing target *and* actionability signal

A `Runner` field was added to `model.Notification`:

```go
type Notification struct {
    Type      string
    Resources []NotificationResource
    Time      time.Time
    OrgID     int64
    UserID    string
    ClientID  string
    // Runner is only set if there's pending work to do.
    Runner string `json:"for_runner,omitempty"`
}
```

It is set **only when a change leaves pending work for a runner** — the same condition `ListRunnerTasks` filters on (`command != 0 AND archived = FALSE`). That invariant lives in one place on the model:

```go
// internal/model/task.go
func (t *Task) PendingRunner() string {
    if t.Command == TaskCommandNone || t.Archived {
        return ""
    }
    return t.Runner
}
```

The invariant: **`Notification.Runner` is set ⟺ the task would appear in that runner's next `ListRunnerTasks` query.** This single field doubles as the routing key (which runner) and the actionability signal (is there anything to do), which collapses several open questions from the original draft:

- **Per-runner scoping**: pub/sub fans out by org, so in a multi-runner org each runner would otherwise see every runner's task notifications. `?runner=<id>` filters to its own.
- **Command precision**: changes that don't leave a pending command (name-only edits, unarchive, a `started` event that transitions to `running`) yield an empty `Runner` and never reach the runner.
- **Log firehose**: `AppendLogs` publishes only a `task_logs` resource and never sets `Runner`, so the high-frequency log stream is excluded automatically.

### Publish sites

Every task publish computes the field from the post-transition task via `PendingRunner()`:

- `apiserver`: `CreateTask`, `UpdateTask`, `ArchiveTask`, `UnarchiveTask`, `CancelTask`, `RestartTask`, and the `applied` branch of `SubmitRunnerEvents`.
- `eventrouter`: `attach` (a webhook event calling `task.Start()`).

Handlers that mutate the task inside a transaction pre-declare the `notification`, then fill in `Resources` and `Runner` from the loaded task inside the closure, so both come from the model value rather than the request.

### Why no client-id self-filtering

An earlier idea was to stamp the runner's `ClientID` on its RPCs and have it skip its own notifications (as the web UI does). This was rejected: unlike the web UI — whose self-notifications merely confirm an optimistic update — the runner's own `SubmitRunnerEvents` can cause the server to set a **new** command. For example, a reported `stopped` while a `Start` is pending sends the task back to `Pending` with `Command=Start` (`applyRunnerEventStopped`), which is new work for the runner. Because `PendingRunner()` is computed from the resulting state, that notification correctly carries `Runner` and wakes the runner — so suppressing self-notifications would risk dropping real work. Self-wakes that *don't* carry pending work simply have an empty `Runner` and are filtered out anyway.

### Runner-side client and loop (follow-up, not yet implemented)

The server half above is implemented. The remaining work is the runner side:

- A small SSE client that connects to `/events?runner=<id>` with the runner's bearer token, reads the event stream, and signals a buffered (size-1) channel on each `change` event so bursts coalesce into a single poll. It reconnects with backoff and signals once on reconnect to catch anything missed while disconnected.
- The runner loop selects on that channel or a fallback timer:

```go
for {
    if err := r.Poll(ctx); err != nil {
        log.Error("failed to poll tasks", "error", err)
    }
    select {
    case <-sseCh:        // task change for this runner → poll now
    case <-time.After(fallback):  // safety net for missed notifications
    case <-ctx.Done():
        return nil
    }
}
```

- The `--poll` flag is repurposed as the fallback interval. For Unix-socket server connections, SSE is skipped and the existing polling behavior is preserved.

## Trade-offs

**SSE wake-up vs. server-push with full task data**: SSE is only a trigger; `ListRunnerTasks` stays authoritative. Pushing the full command payload over SSE would duplicate dispatch logic and turn a lightweight change-signal system into a data channel.

**Reuse `/events` vs. a dedicated endpoint**: reusing `/events` with a `runner` filter avoids a second handler and route, and a second pub/sub subscription path. The only thing a dedicated endpoint would have added is server-side filtering, which the `runner` param provides.

**`Runner` set on every task change vs. only when actionable**: setting it only when `PendingRunner()` is non-empty means the `?runner=` stream is already scoped to actionable work, so the runner does not need to re-derive "does this change imply a command" — and there is no need to encode that mapping anywhere but the model.

**Fallback poll vs. none**: the periodic fallback bounds worst-case latency if a notification is missed (e.g. a brief disconnect) without reintroducing tight polling.

## Open Questions

1. **Fallback interval**: what value best balances overhead against worst-case latency when SSE is degraded? (Original draft suggested ~30s, 6× less frequent than today's 5s.)
2. **Debounce**: the buffered size-1 channel already coalesces bursts; an explicit debounce window is probably unnecessary but could further reduce redundant polls during bulk operations.
