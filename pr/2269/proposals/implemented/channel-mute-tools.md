# `channel_mute` / `channel_unmute` / `channel_muted`: per-task mute for the channel bridge

Issue: https://github.com/icholy/gritz/issues/1156

## Problem

An agent running `gritz mcp --channel` receives a Claude Code channel
notification (`<channel source="gritz">…</channel>`) for **every**
channel-worthy lifecycle transition in its org — task queued / completed /
cancelled / archived / restart-requested / woken-by-event. That firehose
includes tasks the agent created and is no longer waiting on, and tasks created
by *other* users in the same org. It is noisy and pulls the agent off its
current thread.

The agent wants to **selectively silence** notifications for specific tasks it
no longer cares about, without changing anything else. This is an **opt-out
mute** (a blocklist), not an opt-in allowlist: by default the agent stays
subscribed to everything, exactly as today.

### How channel notifications reach an agent today

The push half lives entirely in the local stdio bridge, `gritz mcp --channel`
(`internal/command/mcp.go`):

1. The bridge opens an SSE subscription to the server's **per-org** stream
   (`GET /events`) via `gritzclient.NewNotificationClient`.
2. For every `model.Notification` that arrives, the handler forwards it to the
   host Claude Code session as a `notifications/claude/channel` event **iff**
   `n.ChannelMessage != ""` (`internal/command/mcp.go:93`):

   ```go
   Handler: func(n model.Notification) {
       if n.ChannelMessage == "" {
           return
       }
       if err := transport.SendChannel(ctx, mcpchannel.Params{Content: n.ChannelMessage}); err != nil {
           slog.Warn("gritz channel: failed to send", "error", err)
       }
   },
   ```

The only two filters that exist today are both client-side, in the bridge:

- **`ChannelMessage != ""`** — the summary gate from
  `proposals/implemented/summary-gated-channel-notifications.md`. It suppresses
  log spam and lifecycle churn, leaving only the human-readable
  terminal-status / queued / woken lines set at the publish sites.
- **own-`ClientID` suppression** — `NotificationClient` drops notifications
  stamped with the bridge's own client id so the agent doesn't echo its own
  `create_task` / `update_task` mutations
  (`internal/gritzclient/notificationclient.go:140`).

Neither filter is scoped to *which tasks the agent still cares about*. Every
channel-worthy transition for **any** task in the org is forwarded.

This proposal is scoped to that bridge. In-container agents
(`internal/agentmcp`) do not consume channels yet; when they do, they reuse this
same `gritz mcp --channel` code path and inherit this mechanism unchanged.

### Relationship to #793

Issue #793 and its draft, `proposals/draft/explicit-task-channel-subscriptions.md`,
attack the same firehose but with the **opposite default**: mute-by-default plus
an opt-in allowlist (`watch_task` / `unwatch_task`), where an agent receives
*zero* channel events until it explicitly watches a task. That is a deliberate
behaviour change — every existing `--channel` consumer goes silent until it
opts in.

This proposal implements the settled decision to **keep the current default
intact**: subscribed to everything, opt *out* per task. The two designs are
mutually exclusive on the default axis (see [Trade-offs](#blocklist-opt-out-vs-allowlist-opt-in--793))
but structurally near-identical — both are a per-process, in-memory task-id set
consulted by the bridge's forwarding gate, plus a pair of MCP tools. Whichever
default is chosen, the plumbing below is the same.

## Design

Add a per-process **mute set** to the bridge — a set of task ids whose channel
notifications are suppressed — and gate forwarding on non-membership. The set
starts empty, so the default is "forward everything", byte-for-byte what the
bridge does today. Two MCP tools mutate the set; an optional third introspects
it.

- **Subscribe-all by default.** An empty mute set means every channel-worthy
  notification is forwarded, unchanged. Nothing an agent doesn't touch behaves
  differently.
- **Opt-out per task.** `channel_mute(task_ids)` adds ids to the mute set;
  matching notifications are dropped. `channel_unmute(task_ids)` removes them,
  restoring delivery.
- **Never an allowlist.** There is no "mute everything" mode. Muting is always
  an explicit, finite set of ids; a task not in the set is always delivered.

### Where the mute set lives: client-side, in `internal/mcpbridge`

The mute set is **in-memory, per-bridge-process state**, not server state, and
lives in the bridge, not the CLI entrypoint. This is the same placement argument
made by the #793 draft, and it applies identically here — the two existing
channel filters (the `ChannelMessage` gate and own-`ClientID` suppression) are
already applied client-side in the bridge; per-task muting is the same kind of
concern, one axis over.

Concretely, introduce a small package `internal/mcpbridge` that owns the
local-bridge-specific half of the channel feature. The two existing candidate
homes don't fit:

- **`internal/x/mcpchannel`** is, by its own package doc, *"gritz-agnostic: it
  knows only the Claude Code channel protocol and the MCP SDK."* The mute set
  speaks `model.Notification`, task ids, and the task-resource shape — the
  opposite. `mcpbridge` instead *depends on* `mcpchannel` for the transport /
  `Params` primitives.
- **`internal/server/mcpserver`** is server-side: it holds the HTTP `Handler`,
  imports `apiauth`, and every tool it registers proxies to the Connect service.
  These tools mutate local state and call no RPC. They don't belong there.

The package exposes a `Channel` type tying together the mute set, a channel
sender, and the forwarding gate:

```go
// Package mcpbridge implements the local stdio MCP bridge: it re-exposes
// the user-facing gritz tools and forwards task-change notifications to
// the host Claude Code session as channel events, subject to a per-process
// per-task mute set (subscribe-all by default).
package mcpbridge

// ChannelSender is the subset of *mcpchannel.Transport the bridge needs to
// push a channel notification. Defined here so the gate can be tested
// without a live stdio transport.
type ChannelSender interface {
    SendChannel(ctx context.Context, p mcpchannel.Params) error
}

// Channel owns the per-process mute set and the forwarding gate. One
// Channel is created per `gritz mcp --channel` process.
type Channel struct {
    sender ChannelSender

    mu    sync.Mutex
    muted map[int64]struct{} // muted task ids; empty == forward everything
}

func NewChannel(sender ChannelSender) *Channel {
    return &Channel{sender: sender, muted: map[int64]struct{}{}}
}

func (c *Channel) mute(id int64)   { c.mu.Lock(); defer c.mu.Unlock(); c.muted[id] = struct{}{} }
func (c *Channel) unmute(id int64) { c.mu.Lock(); defer c.mu.Unlock(); delete(c.muted, id) }

func (c *Channel) isMuted(id int64) bool {
    c.mu.Lock()
    defer c.mu.Unlock()
    _, ok := c.muted[id]
    return ok
}

func (c *Channel) mutedIDs() []int64 {
    c.mu.Lock()
    defer c.mu.Unlock()
    ids := make([]int64, 0, len(c.muted))
    for id := range c.muted {
        ids = append(ids, id)
    }
    slices.Sort(ids)
    return ids
}
```

### The forwarding gate

`Forward` is a method on `Channel` suitable as the `NotificationClient` handler.
It adds exactly **one** condition to today's inline bridge handler: drop the
notification if it names a muted task. Task ids ride along in `n.Resources` —
every channel-worthy publish site stamps a `NotificationResource{Type: "task",
ID: …}` (e.g. `internal/server/apiserver/runner.go:30`), which the current
handler ignores.

```go
// primaryTaskID returns the id of the first task resource in the
// notification, if any.
func primaryTaskID(n model.Notification) (int64, bool) {
    for _, r := range n.Resources {
        if r.Type == "task" {
            return r.ID, true
        }
    }
    return 0, false
}

// Forward applies the summary gate and the mute set, then pushes the
// channel notification. When the mute set is empty this is identical to
// the bridge's current inline handler.
func (c *Channel) Forward(ctx context.Context, n model.Notification) {
    if n.ChannelMessage == "" {
        return // summary gate: not channel-worthy
    }
    if id, ok := primaryTaskID(n); ok && c.isMuted(id) {
        return // this task has been muted by the agent
    }
    if err := c.sender.SendChannel(ctx, mcpchannel.Params{Content: n.ChannelMessage}); err != nil {
        slog.Warn("gritz channel: failed to send", "error", err)
    }
}
```

**The default path is byte-for-byte unchanged.** When the tools are never
called, `c.muted` is empty, `c.isMuted(id)` is always `false`, and the branch is
never taken. The surviving code is line-for-line today's handler: the same
`ChannelMessage == ""` gate and the same `SendChannel(ctx, mcpchannel.Params{Content: n.ChannelMessage})`
call — same `Content`, no `Meta`, no reordering. A message with **no** task
resource has no id to match against, so it is *always* forwarded (a blocklist
can only drop ids it holds). This is the deliberately conservative default and
is what preserves current behaviour for the non-task-scoped case.

### MCP tool surface

Registered on the bridge's MCP server **only when `--channel` is enabled**
(without the channel there is no notification stream to mute, so the tools would
be inert; not registering them keeps the tool list honest and matches the
capability, which is likewise only advertised under `--channel`). They are
bridge tools, not server-proxy tools: they mutate the local mute set and issue
no RPC, so they are registered by `Channel.AddTools` in `internal/mcpbridge`,
not by `mcpserver.AddTools`.

| Tool | Input | Behaviour |
| --- | --- | --- |
| `channel_mute` | `task_ids []int64` | Add each id to the mute set. Idempotent. No further channel notifications for those tasks. Returns the updated muted set. |
| `channel_unmute` | `task_ids []int64`, `all bool` (optional) | Remove each id from the mute set (restoring delivery). `all: true` clears the whole set, resetting to the subscribe-all default. Idempotent. Returns the updated muted set. |
| `channel_muted` | — | Return the sorted mute set and a note that all other tasks are delivered. Pure introspection. |

A list (`task_ids`) rather than a single id lets the agent mute or un-mute a
batch in one call (e.g. silencing the dozen tasks it just finished
supervising); a one-element list covers the single-id case. Both mutations
return the resulting muted set so the model always sees current state without a
follow-up `channel_muted`.

Sketch (`Channel.AddTools` in `internal/mcpbridge`):

```go
// AddTools registers channel_mute / channel_unmute / channel_muted on
// server. Called by the bridge only when --channel is enabled.
func (c *Channel) AddTools(server *mcp.Server) {
    mcp.AddTool(server, &mcp.Tool{
        Name: "channel_mute",
        Description: "Stop receiving gritz channel notifications (queued, " +
            "woken, completed, failed, cancelled, archived) for the given " +
            "tasks. You are subscribed to every task by default; call this to " +
            "mute tasks you no longer care about. Re-enable with " +
            "channel_unmute. Distinct from create_link(subscribe=true), which " +
            "routes external events INTO a task.",
    }, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
        TaskIDs []int64 `json:"task_ids" jsonschema:"Task IDs to mute"`
    }) (*mcp.CallToolResult, any, error) {
        for _, id := range in.TaskIDs {
            c.mute(id)
        }
        return jsonResult(map[string]any{"muted": c.mutedIDs()}), nil, nil
    })
    // channel_unmute (with optional all) and channel_muted follow the same shape.
}
```

`channel_mute` is a pure local set insertion — it deliberately does **not**
round-trip to the server to validate the task exists. Muting a non-existent or
not-yet-created id is harmless (no notification will ever match it), and keeping
the tool offline avoids both an RPC per call and a failure mode where a
transient server error blocks the agent from silencing itself.

### `command/mcp.go` stays thin

With the mute set, tools, and gate in `internal/mcpbridge`, the CLI entrypoint
only constructs and wires the pieces:

```go
transport := mcpchannel.NewTransport(&mcp.StdioTransport{})
server := mcp.NewServer(&mcp.Implementation{Name: "gritz", Version: "1.0.0"},
    &mcp.ServerOptions{Instructions: mcpserver.Instructions, Capabilities: &capabilities})
mcpserver.AddTools(server, client, toolOpts...) // user-facing proxy tools (unchanged)

var ch *mcpbridge.Channel
if cmd.Bool("channel") {
    ch = mcpbridge.NewChannel(transport) // *mcpchannel.Transport satisfies ChannelSender
    ch.AddTools(server)                  // channel_mute / channel_unmute / channel_muted
}

session, err := server.Connect(ctx, transport, nil)
if err != nil {
    return err
}
if ch != nil {
    go func() {
        nc := gritzclient.NewNotificationClient(gritzclient.NotificationClientOptions{
            BaseURL:  cmd.String("server"),
            Token:    cmd.String("token"),
            ClientID: clientID,
            Handler:  func(n model.Notification) { ch.Forward(ctx, n) },
        })
        if err := nc.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
            slog.Warn("gritz channel: stream ended", "error", err)
        }
    }()
}
return session.Wait()
```

The one-line `Handler` closure only adapts the `func(model.Notification)`
callback shape to `Channel.Forward`'s `(ctx, n)` signature. Everything
channel-specific — the mute set, the tools, the gate — becomes unit-testable in
`internal/mcpbridge` against a fake `ChannelSender`, with no stdio transport or
live CLI.

### Lifecycle and persistence

- **Ephemeral by design.** The mute set lives only for the bridge process /
  Claude Code session. It survives SSE reconnects (the `NotificationClient`
  reconnects with backoff inside the same process; the set is untouched), but a
  full restart starts with an **empty** set — subscribed to everything. This is
  the correct default: a stale mute from a previous session must never silently
  suppress a task the user now cares about. Re-muting after a restart is a
  couple of tool calls, and only for the tasks still worth silencing.
- **No schema, no proto, no migration.** Nothing is stored server-side. The
  server, the runner consumer, the web UI consumer, and `model.Notification` are
  all untouched. The org-wide SSE stream still carries every notification to the
  bridge; the bridge filters client-side, exactly as it already does for
  `ChannelMessage` and own-`ClientID`.

### Edge cases

- **A muted task woken by an external event.** The woken-by-event line is
  task-scoped — `internal/eventrouter/eventrouter.go:236` formats `"Task N woken
  by event …"` on a notification carrying the task resource — so it is muted
  along with the rest. Muting only suppresses the *channel ping to this
  observing agent*; the wake itself is unaffected. The task's own driver still
  receives the event through the event system (`get_my_task`), and the change is
  still visible in the web UI and via `get_task`. If the agent wants to hear
  about wakes again, it calls `channel_unmute`.
- **Unknown / invalid / not-yet-created task ids.** A local set insert with no
  server validation. A typo or a never-existing id simply never matches a
  notification and is inert; it costs one map entry until the session ends.
- **Non-task-scoped channel messages.** Today every `ChannelMessage` is set at a
  publish site that has a task in scope, so in practice there are none. Should a
  future message be emitted with no task resource, `primaryTaskID` returns
  `false` and the message is **always forwarded** — a blocklist can only drop
  ids it holds, and forwarding is the safe, behaviour-preserving default.
- **Re-muting an already-muted task, or un-muting one that isn't muted.** Both
  are idempotent set operations (insert / `delete`). The returned muted set
  reflects the final state.

## Trade-offs

### Client-side (bridge) filtering vs. server-side subscription state

We filter in the bridge. The alternative is to push the mute set to the server
(a `Mute`/`Unmute` RPC plus per-client mute state and a server-side filter on
the SSE fan-out).

- **Client-side (chosen):** zero server changes, no proto / migration, no new
  failure modes, and it sits next to the two filters already implemented
  client-side. The cost is that the bridge still receives the full org firehose
  over the wire and discards the muted slice — but that traffic already flows
  today (the web UI consumes the same stream) and is trivial.
- **Server-side:** would save the wire traffic and could enforce scoping
  centrally, but introduces persistent per-client state on the server, a new RPC
  surface, and reconnect/resync complexity (the mute set would have to survive
  or be re-sent across the SSE reconnects the client already does with backoff).
  Not justified for a low-volume notification stream.

If bandwidth or cross-tenant secrecy ever became a concern, server-side
filtering could be added later without changing the agent-facing tool contract.

### Blocklist (opt-out) vs. allowlist (opt-in) — #793

This is the crux, and it is a settled product decision, recorded here for the
reviewer:

- **Blocklist / subscribe-all (this proposal):** the default is unchanged —
  every existing `--channel` consumer keeps getting every channel-worthy event,
  and an agent silences only the specific tasks it names. The default path is
  byte-for-byte identical when the tools are never called. The cost is that a
  spawn-many orchestrator that wants quiet must mute each task it's done with
  (or never opt into muting and tolerate the noise).
- **Allowlist / mute-by-default (#793 draft):** the default flips to silent, and
  the agent `watch_task`s the few tasks it cares about. Great for the
  spawn-many orchestrator, but it is a **behaviour change for every current
  consumer** — completions stop arriving until the agent opts in — and it
  requires the agent to know it must opt in at all.

The two cannot coexist as written: they define opposite defaults for the same
stream. This proposal is the "don't change the default, add an escape hatch"
variant. If both are desirable, they would have to be unified behind a single
configured default (see Open Questions), not shipped as two independent gates.

### List param vs. single task id

A list lets the agent mute/un-mute a batch atomically and reads naturally for
"I'm done supervising these five." A single-id tool would be marginally simpler
but forces N calls for N tasks. The one-element list is the single-id case, so
nothing is lost.

### `channel_unmute(all=true)` vs. no reset

Clearing the whole mute set is a common, safe operation ("I want to hear
everything again"), and expressing it as `all: true` avoids making the agent
first list then un-mute each id. It stays a blocklist operation — it empties the
blocklist, it does not create an allowlist. There is deliberately **no**
`channel_mute(all=true)`: "mute everything" is an allowlist in disguise (block
all current *and future* tasks), which this design explicitly rejects.

## Open Questions

1. **Auto-expire mutes on terminal / archived status.** A muted task that
   reaches a terminal state (or is archived) will, by definition, never emit
   another channel-worthy notification — so its entry lingers in the mute set as
   dead weight until the session ends. Should `Forward` prune a task's id from
   the mute set after observing its terminal notification? It's a slow,
   bounded leak (bounded by tasks-touched-per-session), so probably not worth
   the extra status-awareness in the otherwise dumb gate — flagged for a
   decision.

2. **Cap on mute-set size.** Should the bridge bound the number of muted tasks?
   Given subscribe-all-by-default, a huge mute set is only reachable by an agent
   deliberately muting thousands of ids, so likely unnecessary — flagged.

3. **Coexistence / unification with #793.** If the project ever wants *both*
   defaults available (subscribe-all for most consumers, mute-by-default for
   orchestrators), the clean shape is a single configured default (e.g. a
   `--channel-default=all|none` flag, or the server instructions selecting one)
   with **one** id set whose meaning flips — mute set when the default is `all`,
   watch set when the default is `none`. That is out of scope here; this
   proposal only implements the `all` default. Naming would need reconciling
   (`channel_mute`/`channel_unmute` vs. `watch_task`/`unwatch_task`).

4. **Surfacing the model to the agent.** Unlike #793, no discovery is strictly
   required — the default is the status quo, so an agent that never learns about
   the tools simply keeps its current behaviour. Still, the `gritz mcp` server
   `Instructions` (and/or the orchestrator skill) should mention that
   `channel_mute(task_ids)` exists for silencing finished tasks, so agents
   actually use it. Where to put that is an implementation detail.

5. **Validate-on-mute.** Should `channel_mute` round-trip to confirm the ids
   exist / belong to the org? Recommendation: no — muting a bad id is inert, and
   an RPC per call adds a failure mode for a local, best-effort operation.