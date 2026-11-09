# Claude Code Channels for a local gritz MCP bridge

Issue: https://github.com/icholy/gritz/issues/466

## Problem

A common way to drive gritz is from a local Claude Code session: a developer runs `claude` on their workstation, and that session creates and supervises gritz tasks through gritz's user-facing MCP server. Today, after creating a task, the local Claude has no way to know when something changes — it must poll `get_task` or `list_tasks` to discover new logs, new instructions, status transitions, or completion. Polling wastes turns, delays reactions, and bloats the model's context with repeated reads.

Claude Code Channels (research preview, v2.1.80+) provide exactly the primitive that's missing: an MCP server can push `notifications/claude/channel` events into a running session as `<channel>` tags in Claude's context, so the model reacts on the next turn without polling. The server already publishes structured change notifications for every task mutation; the gap is the transport that delivers them to the local Claude.

## Background: How Channels Work

Sources: [code.claude.com/docs/en/channels](https://code.claude.com/docs/en/channels), [code.claude.com/docs/en/channels-reference](https://code.claude.com/docs/en/channels-reference).

- **Status**: research preview. Requires Claude Code **v2.1.80+** (one-way + tools); permission relay needs **v2.1.81+**.
- **Capability declaration**: an MCP server registers as a channel by setting `capabilities.experimental["claude/channel"] = {}`. The value is always an empty object — its presence is the signal. Two-way channels additionally declare `tools: {}`; permission relay adds `capabilities.experimental["claude/channel/permission"] = {}`.
- **Notification format**: method `notifications/claude/channel`, params `{ content: string, meta: Record<string, string> }`. `content` becomes the body of a `<channel>` tag; each `meta` entry becomes a tag attribute. The `source` attribute is auto-populated from the server's configured name.
- **`meta` keys must be identifiers**: letters, digits, and underscores only. Keys containing hyphens or other characters are **silently dropped**. Use `task_id`, not `task-id`.
- **Transport is stdio-only**: a channel server must be a subprocess spawned by Claude Code. Streamable HTTP MCP servers cannot register as channels.
- **Delivery is fire-and-forget**: the notification call resolves when the JSON-RPC frame is written to the transport, not when Claude processes it. If the session didn't load the server with `--channels`, or org policy blocks channels, events are dropped silently with no error returned. Guaranteed delivery requires a reply tool that the model can call back through.
- **Queuing**: events are delivered in order, and multiple events arriving while Claude is mid-turn are batched onto the next turn.
- **Allowlist constraint**: during the preview, `--channels` only accepts plugins on an Anthropic-curated allowlist. A custom server like `gritz` is not on it, so the session must launch with `--dangerously-load-development-channels server:gritz` (which prompts for confirmation per entry), or the org must add the server to the `allowedChannelPlugins` managed setting. Being listed in `.mcp.json` is necessary but not sufficient — the server must also be named in `--channels`.
- **Auth/platform constraints**: channels require Anthropic auth via claude.ai or a Console API key. They are not available on Amazon Bedrock, Google Vertex AI, or Microsoft Foundry. Team/Enterprise orgs must enable `channelsEnabled` in managed settings.
- **Channels are a notification layer, not a data layer**. The event says "task 42 updated" with small `meta` attributes; Claude then calls `get_task` for the full payload.

An event arrives in Claude's context as:

```
<channel source="gritz" action="updated" resource="task" id="42">
Task 42 was updated.
</channel>
```

## Design

### Two MCP servers already exist

The proposal hinges on distinguishing the two gritz MCP servers in the tree today:

1. **User-facing MCP server** (`internal/server/mcpserver/mcpserver.go`, backed by package `mcpserver`). Served as MCP **Streamable HTTP** via `mcp.NewStreamableHTTPHandler` with `Stateless: true`, mounted on the server HTTP API at `/mcp`. Exposes `list_workspaces`, `create_task`, `get_task`, `list_tasks`, `update_task`. This is what the developer's local Claude Code talks to today.

2. **In-container agent MCP server** (`internal/command/mcp.go` — `McpCommand` — backed by `internal/agentmcp`). stdio transport. Spawned by the runner inside each task's container. Exposes `get_my_task`, `update_my_task`, `report`, `create_link`, and the child-task tools. (A separate task is moving this command out of the top-level `mcp` slot to `gritz tool agent-mcp`, freeing `gritz mcp` for the new bridge described below.)

This proposal only affects path (1): how the local Claude that drives the user-facing server receives push notifications. Pushing into in-container agents is explicitly out of scope (see "Future work").

### The hard constraint

The user-facing server is stateless Streamable HTTP. **Channels require stdio.** We cannot simply add `claude/channel` to the experimental capabilities in `mcpserver.go` and have it push notifications into a session — the session does not have a long-lived bidirectional connection to that handler, and `--channels` does not accept HTTP MCP servers. Any push delivery has to happen over a stdio subprocess that Claude Code spawns.

### The bridge

Introduce a new top-level subcommand:

```
gritz mcp [--server URL] [--token TOKEN]
```

A local stdio MCP server that the developer's `.mcp.json` launches. It does two things:

1. **Re-exposes the user-facing tools** (`list_workspaces`, `create_task`, `get_task`, `list_tasks`, `update_task`) over stdio, proxying each call to the server via `gritzclient.New(...)` (the existing Connect RPC client). For a CLI-driven setup this replaces the remote HTTP MCP entry, so the developer only needs **one** MCP entry instead of an HTTP endpoint plus a separate channel process.

2. **Declares the `claude/channel` capability** and pushes `notifications/claude/channel` events for task changes by translating an SSE subscription to the existing notification stream.

The user-facing HTTP MCP endpoint at `/mcp` stays in place for hosted/web-driven Claude clients that cannot spawn local subprocesses.

#### Updated architecture

```
Local Claude Code session
    ↕ stdio (MCP tools + notifications/claude/channel)
gritz mcp  (NEW local bridge — proxies tools, translates SSE → channel)
    ↕ HTTP: Connect RPC (tools)  +  SSE subscription (notifyserver)
gritz server  (already publishes task notifications on every change)
```

### Reusing the existing notification pipeline

This is a translator, not a new event system. The pieces are already in place:

- `internal/server/apiserver/apiserver.go` calls `s.publish(model.Notification{...})` on every mutating RPC (`task.go`, `event.go`, `log.go`, `link.go`, `workspace.go`, `key.go`, `org.go`, `runner.go`). Every task create / update / status change / log append / link append already produces a notification.
- `internal/server/notifyserver/sse.go` fans these notifications out per-org over an SSE endpoint mounted at `/events`. The web UI is already a consumer of this stream for live updates.
- `internal/model/notification.go` defines the payload:

  ```go
  type Notification struct {
      Type      string                 `json:"type"`       // "ready" | "change"
      Resources []NotificationResource `json:"resources,omitempty"`
      Time      time.Time              `json:"timestamp"`
      OrgID     int64                  `json:"org_id"`
      UserID    string                 `json:"user_id,omitempty"`
      ClientID  string                 `json:"client_id,omitempty"`
      Runner    string                 `json:"for_runner,omitempty"`
  }

  type NotificationResource struct {
      Action string `json:"action"` // created | updated | appended
      Type   string `json:"type"`   // task | event | log | link | task_logs
      ID     int64  `json:"id"`
  }
  ```

  Every field that ends up in a channel `meta` attribute (`action`, `type`, `id`) is already identifier-safe — letters, digits, underscores only — so they pass the channel `meta` key/value rules without transformation.
- `internal/x/sse` is an existing SSE Reader/Writer the bridge can consume.

The bridge:

1. Connects to the server SSE endpoint (`GET /events`, `Accept: text/event-stream`) using the same auth token configured for the RPC client.
2. Reads `model.Notification` JSON payloads via `internal/x/sse.Reader`.
3. Filters down to task-relevant resources (`type` in `{task, log, link, task_logs, event}`).
4. For each surviving `NotificationResource`, emits one `notifications/claude/channel`:

   ```jsonc
   {
     "method": "notifications/claude/channel",
     "params": {
       "content": "Task 42 was updated.",
       "meta": {
         "action":   "updated",
         "resource": "task",
         "id":       "42"
       }
     }
   }
   ```

   Channel `meta` requires identifier keys, so we rename `Type` → `resource` (since `type` is also reserved in some contexts) and stringify `ID`. `OrgID`, `UserID`, `ClientID` are not forwarded to the model.

5. Reconnects the SSE stream on transport errors with backoff, mirroring the web UI's behavior.

The bridge does **not** open new RPCs or read full task payloads. Claude does that itself by calling `get_task` through the same bridge after the channel event arrives.

### `mcpserver.AddTools` refactor

To avoid duplicating the tool schemas between the HTTP handler and the new stdio bridge, extract the tool registrations currently inline in `mcpserver.Server.Handler()` (the five `mcp.AddTool(server, ...)` calls plus the input/output types) into a reusable function on the `mcpserver` package, roughly:

```go
// AddTools registers the user-facing gritz tools on the given MCP server.
// Both the HTTP handler and the local stdio bridge call this so they share
// schemas, descriptions, and behavior.
func AddTools(server *mcp.Server, service gritzv1connect.GritzServiceHandler, baseURL string) {
    s := &Server{service: service, baseURL: cmp.Or(baseURL, gritzclient.DefaultURL)}
    mcp.AddTool(server, &mcp.Tool{Name: "list_workspaces", /* ... */}, s.listWorkspaces)
    mcp.AddTool(server, &mcp.Tool{Name: "create_task",     /* ... */}, s.createTask)
    mcp.AddTool(server, &mcp.Tool{Name: "get_task",        /* ... */}, s.getTask)
    mcp.AddTool(server, &mcp.Tool{Name: "list_tasks",      /* ... */}, s.listTasks)
    mcp.AddTool(server, &mcp.Tool{Name: "update_task",     /* ... */}, s.updateTask)
}
```

`mcpserver.Handler()` calls `AddTools(server, s.service, s.baseURL)` after constructing the server; the bridge calls the same function with a Connect-client-backed `service` (the existing `gritzclient.Client` type already satisfies the same `GritzServiceHandler` interface used by `apiserver.Server`, since tool calls just forward to RPCs).

The handler keeps its `Stateless: true` Streamable HTTP wrapper; the bridge wraps the same server with `mcp.StdioTransport` and additionally sets `Capabilities.Experimental["claude/channel"] = map[string]any{}` plus channel-specific `Instructions`.

### `gritz mcp` skeleton

```go
var McpCommand = &cli.Command{
    Name:  "mcp",
    Usage: "Local stdio MCP bridge: re-exposes gritz tools and pushes task change notifications as Claude Code channel events",
    Flags: []cli.Flag{
        &cli.StringFlag{Name: "server", Value: gritzclient.DefaultURL, Usage: "server URL"},
        &cli.StringFlag{Name: "token",  Required: true,                 Usage: "API token"},
    },
    Action: func(ctx context.Context, cmd *cli.Command) error {
        client := gritzclient.New(gritzclient.Options{
            BaseURL: cmd.String("server"),
            Token:   cmd.String("token"),
        })

        server := mcp.NewServer(&mcp.Implementation{
            Name:    "gritz",
            Version: version.String(),
        }, &mcp.ServerOptions{
            Capabilities: &mcp.ServerCapabilities{
                Experimental: map[string]any{
                    "claude/channel": map[string]any{},
                },
            },
            Instructions: "Events from the gritz channel arrive as " +
                "<channel source=\"gritz\" action=... resource=... id=...>. " +
                "They notify you that an gritz task, log, link, or event " +
                "changed. Call get_task with the id for details before acting.",
        })

        mcpserver.AddTools(server, client, cmd.String("server"))

        // channelTransport wraps StdioTransport and exposes a public
        // Notify(method, params) for the SSE→channel goroutine. See
        // "Sending notifications/claude/channel" below.
        transport := newChannelTransport(&mcp.StdioTransport{})
        session, err := server.Connect(ctx, transport, nil)
        if err != nil {
            return err
        }
        go pushTaskChannels(ctx, transport, client, cmd.String("server"), cmd.String("token"))
        return session.Wait()
    },
}
```

`server.Run` is replaced by the lower-level `server.Connect` + `session.Wait` pair so the bridge can hold a reference both to the `ServerSession` (for shutdown) and to the transport wrapper (for sending `notifications/claude/channel` from the background goroutine).

### The Go MCP SDK: capability vs. notification

The repo currently vendors `github.com/modelcontextprotocol/go-sdk` **v1.4.1**; latest is **v1.6.1**. The relevant API surface is identical across those versions. Splitting the prior "SDK gap" into the two things it actually was:

**Advertising `claude/channel` — already public API.** `ServerOptions.Capabilities.Experimental` is a public `map[string]any` (`protocol.go` ~ L1547 in v1.4.1) and is plumbed into the InitializeResult the server returns to Claude Code. Setting `Experimental: map[string]any{"claude/channel": map[string]any{}}` on stock SDK is sufficient to register the listener; **no patch, fork, or wrapper is needed** for the capability declaration shown in the `gritz mcp` skeleton above.

**Sending `notifications/claude/channel` — chosen path is a transport wrapper.** The SDK's public notification methods (`NotifyProgress`, `Log`, `ResourceUpdated`) only cover predefined MCP types, and there is no exported general-purpose `Server.Notify(method, params)`. However, the transport-level types are fully exported and sufficient:

- `mcp.Transport` and `mcp.Connection` are public interfaces (`transport.go` L37–67). `Connection` exposes `Write(context.Context, jsonrpc.Message) error`; its contract documents that "Write may be called concurrently."
- `jsonrpc.Request`, `jsonrpc.Message`, and `jsonrpc.EncodeMessage` are public re-exports from `github.com/modelcontextprotocol/go-sdk/jsonrpc`. A `*jsonrpc.Request{Method: "notifications/claude/channel", Params: raw}` **with no ID** is, by JSON-RPC 2.0 definition, a notification (see `Request.IsCall()` — `messages.go:110`).

The bridge therefore ships a ~30-line wrapper that:

1. Wraps `mcp.StdioTransport` (`type channelTransport struct { inner mcp.Transport; conn *channelConn }`).
2. On `Connect`, calls `inner.Connect(ctx)`, retains the returned `Connection`, and returns its own wrapper that delegates `Read`/`Close`/`SessionID` straight through.
3. Exposes a public `Notify(ctx, method string, params any) error` that JSON-marshals `params` to `json.RawMessage`, constructs `&jsonrpc.Request{Method: method, Params: raw}` (no ID), and calls the wrapped `Connection.Write`.
4. Holds a `sync.Mutex` around `Write` so injected notification frames cannot interleave with the SDK's own writes. (The `Connection` contract already promises concurrent-safe `Write`, but the lock keeps the framing easy to reason about and matches the reviewer's recommendation.)

The bridge constructs `channelTransport{inner: &mcp.StdioTransport{}}`, passes it to `server.Connect`, and keeps a handle to the wrapper so the background SSE→channel goroutine can call `wrapper.Notify(ctx, "notifications/claude/channel", params)` directly. 100% public API; no fork; no internal-package access.

**Why not upstream `ServerSession.Notify`?** This was the prior draft's preferred option. It is the wrong bet for now: it has been proposed in [`go-sdk` PR #898](https://github.com/modelcontextprotocol/go-sdk/pull/898) (which explicitly cites `notifications/claude/channel` as motivation) and rejected by maintainer @jba — "A send-only solution isn't sufficient. There must be a story on the receive side… let's not write more code until we understand the solution." The unified send/receive design is tracked in [`go-sdk` #745](https://github.com/modelcontextprotocol/go-sdk/issues/745), with competing PRs [#844](https://github.com/modelcontextprotocol/go-sdk/pull/844) and [#956](https://github.com/modelcontextprotocol/go-sdk/pull/956) still in flight. The net is: ship the transport wrapper now, add a `TODO` referencing #745, and delete the wrapper once upstream lands a combined design. A temporary fork is now unnecessary and is dropped from consideration.

The receive-side concern @jba raised matters only if we add **permission relay** (Claude Code → bridge → user → response). That is explicitly out of scope for v1; the send-only one-way "task updated" push is fully covered by the wrapper.

### What the original draft proposed and why we're dropping it

The earlier draft of this proposal proposed:

- A new `task_events` table for an incremental, channel-shaped event log.
- A new `PollEventsRequest` / `PollEventsResponse` Connect RPC the agent process would poll every few seconds with a cursor.
- A `channel/channel` capability bolted onto the in-container `gritz mcp` process so it could push events into the *in-container* Claude Code agent.

All three are superseded:

- The server **already publishes a `model.Notification` on every relevant mutation** and **already fans them out over SSE** at `/events`. A new table and a new polling RPC would duplicate that pipeline.
- The use case has moved to the local-developer Claude that drives the user-facing MCP server, not to in-container agents. The original framing of "replace the in-container agent's `get_my_task` polling with channels" is preserved as future work but is no longer the primary motivation.

The "Capability Declaration", "Server.Run Refactoring", and "Runner Integration" sections from the prior draft are replaced by the bridge command and `AddTools` refactor described above; "Database Changes" and "Event Polling" are removed entirely.

## Trade-offs

### Go bridge vs. TypeScript/Bun bridge — resolved toward Go

The prior draft rejected a TypeScript channel server because it would have dragged a Node runtime *into container images*. The bridge runs locally — Bun is already a Claude Code Channels prerequisite — so the runtime cost of TS is no longer an objection. That reopened the choice.

The transport-wrapper path described above closes it again, this time on its own merits:

- **Go bridge** (`gritz mcp` subcommand): reuses `gritzclient`, `internal/x/sse`, the `mcpserver` tool definitions, and `model.Notification` directly. No code duplication, single static binary, ships through the existing release pipeline. The "no public arbitrary-notify API" cost that previously offset these gains is paid by ~30 lines of transport-wrapper code with 100% public-API surface.
- **TS/Bun bridge**: the official `@modelcontextprotocol/sdk` server has first-class `notification()` support, so the channel-side problem is trivial — but the bridge would re-implement Connect-RPC tool proxying, auth token handling, and SSE parsing in TypeScript and introduce a second release artifact in a new language for the project to maintain.

Once `Notify` is no longer a real engineering cost on the Go side, the TS bridge's only remaining argument is "native channel support," which the wrapper provides for free. **Go wins.** This trade-off is resolved here rather than left as an open question.

### Push into the bridge vs. point Claude at the existing HTTP endpoint

We could leave `/mcp` as the only entry point and ship a separate, minimal stdio "channel-only" subprocess whose sole job is to translate SSE → notifications. Pros: smaller surface; Claude can keep talking to the proven HTTP endpoint for tools. Cons: developers configure two MCP entries; the channel server still needs auth, an SSE client, and Bun-or-Go runtime — most of the bridge's complexity — without the simplicity win of "one stdio entry replaces everything for local use." We propose the bundled bridge as the default, but the split layout remains a valid alternative.

### Reusing the existing SSE stream vs. building a per-task subscription

The notify SSE stream is per-org: a bridge subscribes once and sees every notification the user's org generates. We could instead build a per-task channel-shaped endpoint and have the bridge open one subscription per task it has touched. Cons: more state on the bridge, more reconnect/lifecycle handling, more endpoints on the server. Pro of going per-task: trivially scoped — no risk of leaking another user's activity into the local Claude. We propose reusing the existing org-scoped stream and filtering in the bridge, but the filtering policy is itself an open question (next section).

## Open Questions

1. **Scope of forwarded notifications.** Should the bridge push every task notification on the org's SSE stream, or filter to tasks created by the same user (`Notification.UserID`) or even the same session (`Notification.ClientID`)? The `model.Notification` envelope carries both, so a filter is cheap; the policy choice (and the UX of "I created task 42 from this terminal — only tell me about it" vs. "tell me about everything in my org") is the question.
2. **Bridge-as-everything vs. channel-only bridge.** Should `gritz mcp` re-expose the user-facing tools alongside the channel (one MCP entry for the local user, as proposed), or stay channel-only and let the user keep pointing Claude at the HTTP `/mcp` endpoint for tools (two entries, sharper layering)?
3. **How rich should `content` be?** Channel `content` is the `<channel>` tag body. We could send a minimal `"Task 42 updated."` and rely on Claude calling `get_task`, or we could embed a short human-readable summary (status transition, instruction author) to save a round-trip. Richer `content` means the bridge fetches details before emitting, which costs an RPC per change.
4. **Permission relay.** Two-way channels and the `claude/channel/permission` capability would let gritz prompt the local Claude for approvals (e.g. before running a destructive task action). Out of scope for v1; flagged because permission relay would need the receive-side story that [`go-sdk` #745](https://github.com/modelcontextprotocol/go-sdk/issues/745) is blocking on, so picking it up later is bounded by that upstream design.

## Future work: pushing into in-container agents

The original framing — replacing the in-container `gritz mcp` process's reliance on the agent polling `get_my_task` with pushed channel events — is still achievable on top of this work. The transport wrapper used by the local bridge is reusable as-is inside the agent server, since the agent already runs over stdio. The design question is which agent-side state changes (child completions, parent instructions, routed external events, child logs) deserve a push, and whether the agent should subscribe to its own per-task slice of `model.Notification`s or get a curated stream. That work is deferred to a follow-up proposal so this one can ship the local-developer use case first.