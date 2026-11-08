---
title: "Org-level default auto-archive for the HTTP MCP"
description: "The server-hosted HTTP MCP has no default auto-archive timeout, so create_task calls that omit auto_archive create tasks that never archive, unlike the stdio bridge flag."
---

Issue: https://github.com/icholy/gritz/issues/948

## Problem

The local stdio MCP bridge (`gritz mcp`) accepts an `--auto-archive` flag that
sets the default auto-archive timeout for tasks it creates via `create_task`
when the call omits the `auto_archive` param (`internal/command/mcp.go`,
wired through `mcpserver.WithDefaultAutoArchive`). MCP clients that don't pass a
per-task value get a sensible org/operator-chosen default instead of "never".

The server-hosted HTTP MCP (`/mcp`, `internal/server/mcpserver/mcpserver.go`
`Handler`) has no equivalent. It builds the tool server with `NewServer(service)`
and no options, so `h.defaultAutoArchive` is always `nil` and every
`create_task` call that omits `auto_archive` results in a task that never
auto-archives. A flag is not an option here: the HTTP MCP is a single
server-hosted endpoint shared by every org, so the default must be configured
per-org rather than per-process. This should live in the org settings.

## Design

Add a per-org `mcp_auto_archive` setting. When an HTTP MCP `create_task` call
omits `auto_archive`, the server applies the calling org's configured default.
This is the exact analogue of the stdio `--auto-archive` flag, scoped to an org
instead of a process.

### Value semantics (unchanged, tri-state)

The setting reuses the existing `auto_archive` duration semantics (see
`model.Task.AutoArchive`): `0` = never, negative = archive immediately on
terminal status, positive = delay. To distinguish "no org default configured"
from an explicit "never (0)" — exactly as the stdio side uses
`cmd.IsSet("auto-archive")` to decide whether to send a default at all — the
setting is **nullable**:

- `NULL` / unset → no org default; the HTTP MCP behaves as today (tasks never
  auto-archive unless the call specifies `auto_archive`).
- set (including `0`) → applied as the `create_task` default when the call omits
  `auto_archive`.

The per-call `auto_archive` param always takes precedence over the org default,
matching the stdio behavior.

### Database schema

New nullable column on `orgs`, in the same microsecond units as
`tasks.auto_archive` (`internal/store/sqlc/models.go` stores it as `int64`
microseconds; `store/task.go` converts via `Duration.Microseconds()`):

```sql
-- internal/store/sql/migrations/202606XXXXXXXX_org_mcp_auto_archive.sql
-- migrate:up
ALTER TABLE orgs ADD COLUMN mcp_auto_archive bigint;

-- migrate:down
ALTER TABLE orgs DROP COLUMN mcp_auto_archive;
```

`bigint` NULL (not `NOT NULL DEFAULT 0`) so the column is genuinely tri-state.
`schema.sql` is regenerated to include the column.

### Store + model

`model.Org` gains a nullable field:

```go
// internal/model/org.go
type Org struct {
    // ...existing fields...
    // MCPAutoArchive is the default auto-archive applied to tasks created via
    // the HTTP MCP create_task tool when the call omits auto_archive. nil = no
    // org default configured. See Task.AutoArchive for the value semantics.
    MCPAutoArchive *time.Duration
}
```

New sqlc queries + store methods alongside the existing focused org accessors
(`GetOrgAtlassianWebhookSecret` / `SetOrgAtlassianWebhookSecret` in
`internal/store/org.go`):

```go
func (s *Store) GetOrgMCPAutoArchive(ctx context.Context, tx *sql.Tx, orgID int64) (*time.Duration, error)
func (s *Store) SetOrgMCPAutoArchive(ctx context.Context, tx *sql.Tx, orgID int64, d *time.Duration) error
```

`Get` maps SQL `NULL` → `nil`; a non-null value is `time.Duration(micros) * time.Microsecond`.
`GetOrg` is also extended to populate `Org.MCPAutoArchive`.

### Proto / API

Surface the setting on the existing org-settings RPCs
(`proto/gritz/v1/gritz.proto`):

```proto
message GetOrgSettingsResponse {
  string atlassian_webhook_secret = 1;
  string atlassian_webhook_url = 2;
  string github_app_url = 3;
  string mcp_url = 4;
  int64 github_installation_id = 5;
  // Default auto-archive applied to tasks created via the HTTP MCP create_task
  // tool when the call omits auto_archive. Unset = no org default. See
  // Task.auto_archive for the value semantics.
  optional google.protobuf.Duration mcp_auto_archive = 6;
}

// Set the org's HTTP MCP default auto-archive. Omit mcp_auto_archive to clear
// it (no org default).
message SetMCPAutoArchiveRequest {
  optional google.protobuf.Duration mcp_auto_archive = 1;
}
message SetMCPAutoArchiveResponse {
  optional google.protobuf.Duration mcp_auto_archive = 1;
}

service GritzService {
  // ...
  rpc SetMCPAutoArchive(SetMCPAutoArchiveRequest) returns (SetMCPAutoArchiveResponse);
}
```

`proto3` `optional google.protobuf.Duration` keeps the field a true tri-state on
the wire (a message is already nilable; `optional` documents intent and keeps the
generated getter explicit). The `GetOrgSettings` handler in
`internal/server/apiserver/org.go` populates `mcp_auto_archive` from
`org.MCPAutoArchive`; the new `SetMCPAutoArchive` handler mirrors
`SetRoutingRules` — `OpOrgWrite` scope check, owner gate consistent with the
other settings mutations, write-through to the store, publish a `change`
notification so other sessions refresh.

### Applying the default in the HTTP MCP

`mcpserver` already has the static `WithDefaultAutoArchive(d)` option used by
stdio. For the HTTP path the default is per-org and must be resolved per
request. Generalize the existing option with a resolver:

```go
// internal/server/mcpserver/mcpserver.go

// WithDefaultAutoArchiveFunc resolves the create_task default per call from the
// request context (e.g. the caller's org). Returns nil when no default applies.
func WithDefaultAutoArchiveFunc(fn func(ctx context.Context) *durationpb.Duration) Option {
    return func(h *handlers) { h.resolveDefaultAutoArchive = fn }
}
```

`handlers.createTask` resolves the default only when the call omits
`auto_archive` — the static `WithDefaultAutoArchive` value (stdio) is folded into
a constant resolver so both paths share one code path:

```go
func (h *handlers) createTask(ctx context.Context, _ *mcp.CallToolRequest, input createTaskInput) (...) {
    req := &gritzv1.CreateTaskRequest{ /* ... */ }
    if input.AutoArchive != "" {
        d, err := time.ParseDuration(input.AutoArchive)
        if err != nil { return errorResult(...), nil, nil }
        req.AutoArchive = durationpb.New(d)
    } else if h.resolveDefaultAutoArchive != nil {
        req.AutoArchive = h.resolveDefaultAutoArchive(ctx) // may be nil
    }
    // ...
}
```

`mcpserver.Handler` wires an org-aware resolver. The factory closure already runs
in request context (the caller/org is on `r.Context()` via `apiauth.Caller`), so
the resolver reads the org default through the service the handler already holds:

```go
func Handler(service gritzv1connect.GritzServiceHandler) http.Handler {
    server := NewServer(service, WithDefaultAutoArchiveFunc(func(ctx context.Context) *durationpb.Duration {
        resp, err := service.GetOrgSettings(ctx, &gritzv1.GetOrgSettingsRequest{})
        if err != nil {
            return nil // fail open: behave as no org default
        }
        return resp.McpAutoArchive
    }))
    return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
        if apiauth.Caller(r.Context()) == nil {
            return nil
        }
        return server
    }, &mcp.StreamableHTTPOptions{Stateless: true})
}
```

The shared `*mcp.Server` is retained (no per-request reflection); only a context
lookup happens per `create_task`. `GetOrgSettings` is already org-scoped and
auth-checked, so the resolver inherits the caller's org without new plumbing. No
change is needed in `apiserver.CreateTask`: it already applies a non-nil
`req.AutoArchive` and ignores `nil`.

### Web UI

Add a control to the existing **MCP Server** card on the org Settings page
(`webui/src/routes/settings.tsx`, the `OrgSettings` component), next to the MCP
URL. It reads `mcpAutoArchive` from `getOrgSettings` and saves via the new
`setMCPAutoArchive` mutation, reusing the auto-archive helpers in
`webui/src/lib/duration.ts` (`durationFromHours` / `hoursFromDuration`) and the
same `Select` pattern as the Create Task screen and routing-rule editor:

- "No default" → clears the setting (`mcp_auto_archive` omitted).
- Hour presets (e.g. 1h / 24h / 72h) → positive duration.
- Optionally "Archive immediately" → a small negative duration, matching the
  stdio flag's negative case.

Run `pnpm lint` in `webui/` before finishing (CI requirement).

## Trade-offs

- **MCP layer vs. `apiserver.CreateTask`.** Applying the default in
  `CreateTask` whenever `AutoArchive` is `nil` would cover every client, but it
  would also silently change the Connect API / web UI Create Task flow (which
  has its own explicit control and defaults to "never") and would not match the
  issue's framing of "MCP initiated tasks". Resolving the default in the MCP
  handler keeps the behavior scoped to MCP `create_task`, exactly mirroring the
  stdio flag, and leaves `CreateTask` untouched.

- **Resolver vs. per-request server build.** An alternative is to build a fresh
  `*mcp.Server` per request with `WithDefaultAutoArchive(orgDefault)`. That
  pays `mcp.AddTool` reflection on every request for no benefit; the resolver
  keeps the shared stateless server and adds only a context-time lookup.

- **Reusing `GetOrgSettings` vs. a dedicated store lookup.** The resolver calls
  the existing `GetOrgSettings` RPC, which also computes webhook/app URLs — a
  little extra work per `create_task`. The upside is zero new plumbing
  (`mcpserver` keeps depending only on the service interface, not the store) and
  it inherits the existing org-scoping and auth checks. If the overhead ever
  matters, a focused `GetOrgMCPAutoArchive` store accessor can be threaded in
  without changing the resolver shape.

- **Nullable column vs. `NOT NULL DEFAULT 0`.** A non-null `0` default would
  conflate "no org default" with "never", removing the operator's ability to
  leave the HTTP MCP at its current behavior while still meaning "never" for a
  specific task. The nullable column preserves the same tri-state the stdio
  flag has via `IsSet`.

- **Dedicated `SetMCPAutoArchive` RPC vs. a generic `UpdateOrgSettings`.** A
  focused RPC matches the existing granular settings mutations
  (`SetRoutingRules`, `GenerateAtlassianWebhookSecret`). A general
  `UpdateOrgSettings` would be more future-proof but is larger than this change
  needs; it can be introduced later if settings proliferate.

## Open Questions

- Should the web UI expose the negative ("archive immediately") case, or only
  "No default" + positive hour presets (leaving immediate to API callers)?
- Preferred hour presets for the UI Select (1h / 24h / 72h?), and should it
  allow a free-form duration like the stdio flag (e.g. `30m`) rather than only
  whole hours?
- Should the same org default also apply to tasks created by the in-container
  agent MCP (`create_child_task`, `internal/agentmcp`), or is this strictly the
  user-facing HTTP MCP `create_task`? (This proposal scopes it to the latter.)
