# MCP admin tools and fuller task management

Issue: https://github.com/gritzapp/gritz/issues/2095

## Problem

The user-facing MCP, served as `gritz mcp` over stdio and as `/mcp` on the
server, registers six tools in
[`internal/server/mcpserver`](https://github.com/gritzapp/gritz/blob/master/internal/server/mcpserver/mcpserver.go):
`list_workspaces`, `create_task`, `get_task`, `list_tasks`, `update_task` and
`archive_task`. Everything else the `GritzService` API offers is unreachable
from an MCP client, so an agent that a user works with locally can't:

- **Administer events.** List the org's external events, inspect or delete one,
  see which event types exist, define custom event types, dry-run an event
  against the routing rules, or send a custom event.
- **Manage schedules.** Create, edit, pause, delete or run one now.
- **Read org administration data or edit routing rules.** Who the members are,
  what API keys exist and when they expire, the org's webhook and MCP URLs; the
  routing rules can't be read or changed either.
- **Manage tasks fully.** Even the task tools lag the API and Web UI: there is
  no cancel, restart or unarchive, no rename, no paging past the 50 newest
  tasks, no archived tasks, no drafts or namespaces, and no access to the
  agent's transcript.

The in-sandbox agent MCP (`gritz tool agent-mcp`,
[`internal/runner/agentmcp`](https://github.com/gritzapp/gritz/tree/master/internal/runner/agentmcp))
is out of scope. It serves a task's own agent with a task token, and nothing
here changes it.

## Design

Add tools to `mcpserver.AddTools`, every one a thin proxy over a
`GritzService` RPC. Every capability below already has an RPC, because the Web
UI uses it, so no RPCs, store changes or migrations are needed.

### Principles

- **One tool, one intent.** A tool maps onto one RPC, or onto a short fixed
  sequence where the RPC's shape doesn't fit an agent (read-modify-write for the
  replace-whole-set RPCs, a follow-up `GetTask` to return the changed task, as
  `update_task` and `archive_task` already do).
- **The API is the authority.** The tools check no permissions themselves. Each
  RPC already checks `caller.Scopes` (`authscope.OpEventWrite`,
  `OpOrgRead`, `OpKeyRead`, …) and owner-only rules, and the MCP caller is an
  ordinary API caller (an OAuth app JWT or an API key, both `authscope.Admin()`
  today). A denied call surfaces as an `mcpx.ErrorResult`, as now.
- **Org data is read-only, except routing rules.** No tool calls
  `AddOrgMember`, `RemoveOrgMember`, `CreateKey`, `DeleteKey`, `CreateOrg`,
  `DeleteOrg`, `GenerateAtlassianWebhookSecret`, `LinkGitHubInstallation`, or the
  account unlink RPCs. Those stay in the Web UI and CLI. Routing rules are the
  one org setting an agent writes, through `set_routing_rules`, because
  configuring routing is the admin work an agent is most often asked to do.
- **No secrets in results.** `GetOrgSettingsResponse.atlassian_webhook_secret`
  is dropped from `get_org_settings`. `ListKeys` returns only key metadata, never
  a token, so `list_keys` is safe as is.
- **Annotations tell the client what a tool does.** Reads set `ReadOnlyHint`;
  `delete_event`, `delete_schedule`, `delete_custom_event_type` and
  `set_routing_rules` set `DestructiveHint`, so clients that confirm destructive
  calls do.
- **Same tools on both transports.** `gritz mcp` and `/mcp` share `AddTools`;
  every tool goes through the `gritzv1connect.GritzServiceHandler` interface, so
  none needs anything the Connect client can't do.

### Task tools: audit and gaps

What the API and Web UI do with tasks, against the MCP today:

| Capability | RPC | Web UI | MCP today | Proposal |
| --- | --- | --- | --- | --- |
| List, paged, with archived | `ListTasks` (`page_size`, `page_token`, `archived`) | yes | first page of 50, unarchived only | `list_tasks` takes `page_size`, `page_token`, `archived`; returns `next_page_token` |
| Task fields in lists | `Task` | status, archived badge, auto-archive | id, name, workspace, status, url | summary adds `runner`, `namespace`, `archived`, `created_at`, `updated_at` |
| Read task + timeline | `GetTask`, `ListEventsByTask`, `ListLinks` | yes | yes, every event | `get_task` adds `auto_archive`, `archived`, `namespace`, `actions`, timestamps; takes `event_types` to filter the timeline |
| Create as draft, in a namespace | `CreateTask` (`draft`, `namespace`) | yes (always draft) | no | `create_task` takes `draft`, `namespace` |
| Rename | `UpdateTask.name` | no | no | `update_task` takes `name` |
| Change auto-archive | `UpdateTask.auto_archive` | yes | no | `update_task` takes `auto_archive` |
| Add instruction, start | `UpdateTask` | yes | yes | `instruction` becomes optional, so a call can only rename or start |
| Cancel | `CancelTask` | yes | no | `cancel_task` |
| Restart | `RestartTask` | yes | no | `restart_task` |
| Archive | `ArchiveTask` | yes | yes | unchanged |
| Unarchive | `UnarchiveTask` | yes | no | `unarchive_task` |
| Agent transcript | `ListLogChunksByTask` | yes (Logs tab) | no | `get_task_log` |
| Link a resource | `CreateLink` | no | no | `create_link` |
| Shell | `OpenShell` | yes | no | out of scope: an interactive terminal, not a tool call |

`get_task` returning `actions` (`TaskActions`: archive, cancel, restart, start,
unarchive) lets the agent see what the task allows before it tries, the same
flags the Web UI uses to enable its buttons.

### Tool set

Existing tools are marked *(changed)* or *(unchanged)*; the rest are new.

#### Tasks

| Tool | RPC | Arguments |
| --- | --- | --- |
| `list_workspaces` *(unchanged)* | `ListWorkspaces` | — |
| `list_tasks` *(changed)* | `ListTasks` | `page_size`, `page_token`, `archived` |
| `get_task` *(changed)* | `GetTask`, `ListEventsByTask`, `ListLinks` | `id`, `event_types` |
| `create_task` *(changed)* | `CreateTask` | `workspace`, `runner`, `instruction`, `name`, `auto_archive`, `namespace`, `draft` |
| `update_task` *(changed)* | `UpdateTask`, `GetTask` | `id`, `instruction`, `url`, `name`, `auto_archive`, `start` |
| `cancel_task` | `CancelTask`, `GetTask` | `id` |
| `restart_task` | `RestartTask`, `GetTask` | `id` |
| `archive_task` *(unchanged)* | `ArchiveTask`, `GetTask` | `id` |
| `unarchive_task` | `UnarchiveTask`, `GetTask` | `id` |
| `get_task_log` | `ListLogChunksByTask` | `id`, `before`, `max_bytes` |
| `create_link` | `CreateLink` | `task_id`, `url`, `title`, `relevance`, `subscribe` |

Details:

- **`list_tasks`** returns `{tasks, next_page_token}` instead of a bare array.
  `page_size` defaults to the server's 50.
- **`get_task`**'s `event_types` is passed to `gritzclient.ListAllEventsByTask`,
  which already sends it as `ListEventsByTaskRequest.types` (the tool passes
  `nil` today), so an agent that only wants
  the instructions or the external events doesn't page in the whole timeline.
  Empty means every type, as now; command events stay skipped.
- **`update_task`** rejects a call that sets none of `instruction`, `name`,
  `auto_archive` or `start` with an error result. `auto_archive` takes the Go
  duration strings `create_task` already parses; an unset argument leaves
  `UpdateTaskRequest.auto_archive` nil (leave alone).
- **`create_task`** with `draft: true` creates the task in `DRAFT`; the agent
  starts it later with `update_task` and `start: true`, the same flow as the Web
  UI's composer. `instruction` stays required for a non-draft create.
- **`get_task_log`** returns the tail of the task's transcript as text: it walks
  `ListLogChunksByTask` backwards from the newest page (empty `page_token`,
  then `prev_page_token`) until it has `max_bytes` (default 64 KiB, capped at
  1 MiB) or history runs out, and returns `{log, before}`, where `before` is the
  `prev_page_token` to pass back for the preceding bytes, empty at the start of
  the log. Chunks are the shipped log, so declared secrets are already masked.
  The tool doesn't parse the driver's log format.
- **`create_link`** lets the user subscribe a task to a resource they made by
  hand, such as a PR opened outside gritz, so the task receives its events. It
  maps 1:1 onto `CreateLink`; `relevance` is free text describing why the link
  matters, as in the agent's own `create_link`.

#### Events

| Tool | RPC | Arguments |
| --- | --- | --- |
| `list_events` | `ListExternalEvents` | `limit` |
| `get_event` | `GetEvent` | `id` |
| `delete_event` | `DeleteEvent` | `id` |
| `test_event` | `TestEvent` | `source`, `type`, `body`, `url`, `attrs`, `description`, `details`, `fire` |

- **`list_events`** returns a summary per event: `id`, `task_id`, `source`,
  `type`, `description`, `url`, `created_at` from `ExternalPayload`, not the
  `data` blob. `get_event` returns the full event as protojson, like `get_task`'s
  timeline.
- **`test_event`** is for testing routing. It takes any registered source and
  type, defaults to a dry run (`fire: false`), and returns
  `TestEventResponse.matches`: which rule matched and whether it would wake or
  create. `fire: true` routes it for real; the RPC already requires
  `OpOrgWrite` for that. The RPC validates the event against its type and
  routes a custom event with no actor, exactly as `/webhook/custom` does, so
  firing a custom event is how an agent sends one; an invalid event comes back
  as an error result carrying the validation message.

#### Event types

| Tool | RPC | Arguments |
| --- | --- | --- |
| `list_event_types` | `GetEventTypes` | — |
| `put_custom_event_type` | `GetCustomEventTypes`, `SetCustomEventTypes` | `type`, `label`, `attrs` |
| `delete_custom_event_type` | `GetCustomEventTypes`, `SetCustomEventTypes` | `type` |

- **`list_event_types`** lists the built-in and custom types with their
  attributes, which is what an agent needs to fill in `test_event`'s attrs,
  or to write a routing rule's conditions.
- `SetCustomEventTypes` replaces the whole set. The two write tools read the
  current set, add, replace or remove the one entry by `type`, and write the set
  back, exactly as the Web UI's custom event type pages do. The RPC validates
  the result: a malformed schema is `InvalidArgument`, and removing a type a
  routing rule still uses is `FailedPrecondition`, which the tool returns as an
  error result naming the problem. `source` is always `"custom"` and is not an
  argument.

#### Schedules

| Tool | RPC | Arguments |
| --- | --- | --- |
| `list_schedules` | `ListSchedules` | — |
| `get_schedule` | `GetSchedule` | `id` |
| `create_schedule` | `CreateSchedule` | `name`, `workspace`, `runner`, `namespace`, `instruction`, `cron_expr`, `timezone`, `auto_archive`, `enabled` |
| `update_schedule` | `GetSchedule`, `UpdateSchedule` | `id`, and any of the create arguments except `enabled` |
| `set_schedule_enabled` | `SetScheduleEnabled` | `id`, `enabled` |
| `run_schedule` | `RunSchedule` | `id` |
| `delete_schedule` | `DeleteSchedule` | `id` |

- Schedules take one `instruction`, like `create_task`, mapped to a
  one-element `instructions`. A schedule with several stored instructions is
  shown with all of them by `get_schedule`; `update_schedule` without
  `instruction` keeps them.
- `UpdateScheduleRequest` replaces every template field. `update_schedule`
  takes each as optional (pointer fields in the input struct), loads the
  schedule with `GetSchedule`, overlays the arguments given and sends the full
  request, so "change the cron to 9am" doesn't blank the instructions.
- `create_schedule` defaults `enabled` to `false`, as the RPC does; the tool
  description says so, so an agent turns it on deliberately.
- `run_schedule` returns the created task's summary.

#### Org

| Tool | RPC | Arguments |
| --- | --- | --- |
| `list_members` | `ListOrgMembers` | — |
| `list_keys` | `ListKeys` | — |
| `get_org_settings` | `GetOrgSettings` | — |
| `get_routing_rules` | `GetRoutingRules` | — |
| `set_routing_rules` | `SetRoutingRules` | `rules` |

- `list_members` returns `user_id`, `email`, `name`, `role`, `created_at`.
- `list_keys` returns `id`, `name`, `expires_at`, `created_at`.
- `get_org_settings` returns `mcp_url`, `custom_webhook_url`,
  `atlassian_webhook_url`, `github_app_url`, `github_installation_id`, and
  never `atlassian_webhook_secret`.
- `get_routing_rules` returns the rules as protojson.
- `set_routing_rules` proxies `SetRoutingRules` as is: `rules` is the full list
  as protojson `RoutingRule`s, in the shape `get_routing_rules` returns. The
  agent reads the list, edits it, and writes the whole list back. The tool
  description says that it replaces every rule in the org, so a list missing a
  rule deletes it. The RPC validates every rule against the org's event types
  and returns `InvalidArgument` naming the bad rule, which the tool returns as
  an error result. It returns the stored rules.

### Instructions

`mcpserver.Instructions` grows a sentence saying the server also administers
the org's events, custom event types, schedules and routing rules, and reads its
members and keys, so clients route those requests here.

### Code layout

`mcpserver.go` holds the server, `AddTools` and the shared helpers
(`taskSummary`, duration parsing). The handlers move into one file per domain,
each with a `register` function `AddTools` calls in order:

```
internal/server/mcpserver/
  mcpserver.go   // NewServer, AddTools, Handler, Instructions, shared helpers
  task.go        // addTaskTools: task and workspace tools
  event.go       // addEventTools: events, event types, test and custom events
  schedule.go    // addScheduleTools
  org.go         // addOrgTools: members, keys, settings, routing rules
```

Tests stay on the existing pattern in `mcpserver_test.go`: a
`gritzclient.ClientMock` behind an in-memory MCP session, asserting the RPC
request each tool sends and the result it renders, plus `TestListTools` for the
registered names. The test file splits along the same lines.

### API

The package's exported API doesn't change: `AddTools` registers more tools.

```go
package mcpserver // github.com/gritzapp/gritz/internal/server/mcpserver

// Instructions is the prompt shown to MCP clients describing what the gritz server offers.
const Instructions = "..." // changed: mentions events, schedules, routing rules and org data

// AddTools registers the user-facing gritz tools on the given MCP server.
func AddTools(server *mcp.Server, service gritzv1connect.GritzServiceHandler, opts ...Option) // changed: registers the new tools
```

**How it fits:** unchanged. The server's `/mcp` handler and `gritz mcp` both
call `AddTools`; the server passes the in-process `apiserver.Server`, `gritz mcp`
the Connect client. Every handler calls `GritzServiceHandler` methods only.

### Considered and left out

- **Per-type custom event type RPCs** (`PutCustomEventType`,
  `DeleteCustomEventType`). They would remove the lost-update window of
  read-modify-write, but the Web UI has the same window and edits are rare.
- **Per-rule routing-rule RPCs.** Rules have no IDs, so per-rule add and remove
  would need stable rule IDs in `RoutingRule`. Proxying `SetRoutingRules` is
  what the Web UI's rule editor does already.
- **`ListTasks` filters** (status, workspace, namespace). Useful to an agent
  hunting for "my failed tasks", but the Web UI doesn't filter either; paging
  covers it for now.
- **`ListExternalEvents` paging.** It returns at most the 100 newest events.
  That's enough for an agent looking at recent activity, and the Web UI has the
  same limit.

## Implementation Plan

Each slice updates the **Tools** section of `integrations/mcp` for the tools it
adds or changes, in the same PR.

1. **Split the package** — Delivers: `mcpserver.go` split into `task.go` with
   the existing handlers and the per-domain `register` structure; no behaviour
   change. Depends on: nothing. Verifiable by: the existing `mcpserver` tests
   pass unchanged.
2. **Task tools** — Delivers: `list_tasks` paging and `archived`, the wider
   task summary, `get_task`'s extra fields and `event_types` filter;
   `create_task` `draft` and `namespace`; `update_task` `name`, `auto_archive`,
   optional `instruction` and the nothing-to-do error; `cancel_task`,
   `restart_task`, `unarchive_task`; `get_task_log`; `create_link`. Depends on:
   (1). Verifiable by: mock tests on the requests each tool sends, including
   `next_page_token` in the `list_tasks` result, the `types` passed to
   `ListEventsByTask`, and `get_task_log` walking several `ListLogChunksByTask`
   pages backwards, stopping at `max_bytes` and returning the right `before`; a
   manual cancel and restart through `gritz mcp` against a local server.
3. **Events and event types** — Delivers: `list_events`, `get_event`,
   `delete_event`, `list_event_types`, `put_custom_event_type`,
   `delete_custom_event_type`, `test_event`. Updates
   `integrations/custom-events` to mention firing a custom event with
   `test_event`. Depends on: (1). Verifiable by: mock tests, including that
   `list_events` omits `data`, that the written custom type set adds, replaces
   or removes exactly one entry, that `test_event` sends `body`, `url` and
   multi-valued `attrs`, and that a `FailedPrecondition` or `InvalidArgument`
   comes back as an error result.
4. **Schedules** — Delivers: `list_schedules`, `get_schedule`,
   `create_schedule`, `update_schedule`, `set_schedule_enabled`,
   `run_schedule`, `delete_schedule`. Depends on: (1). Verifiable by: mock
   tests, including that `update_schedule` with only `cron_expr` sends the
   loaded instructions back unchanged.
5. **Org** — Delivers: `list_members`, `list_keys`, `get_org_settings`,
   `get_routing_rules`, `set_routing_rules`. Depends on: (1). Verifiable by:
   mock tests, including that `get_org_settings` drops the webhook secret, that
   `set_routing_rules` sends the list unchanged and carries `DestructiveHint`,
   and that an `InvalidArgument` comes back as an error result.
6. **Instructions** — Delivers: the updated `mcpserver.Instructions` and the
   intro of `integrations/mcp` describing the wider tool set. Depends on:
   (2)–(5). Verifiable by: reading the page; `TestListTools` lists every tool.

Slices 2–5 depend only on (1) and can land in any order.

## Trade-offs

- **Proxy tools over existing RPCs, not new MCP-specific endpoints.** Every
  capability already exists for the Web UI, with its authorization and
  validation. Proxying keeps `gritz mcp` and `/mcp` identical and keeps the MCP
  out of the permission model. The cost is a few read-modify-write tools where
  the RPC replaces a whole set.
- **Routing rules are written whole.** `set_routing_rules` proxies
  `SetRoutingRules`, which replaces the org's whole rule list; one bad call
  from an agent that drops rules can silently stop events from waking tasks.
  Per-rule add and remove would be safer, but rules have no IDs to address
  them by. `DestructiveHint` and a description that says every rule is
  replaced make the client confirm, the RPC still validates every rule, and
  `get_routing_rules` plus `test_event` let the agent check its work.
- **A flat tool set, not toolsets.** The MCP goes from 6 tools to 30, which costs
  client context. Grouping tools behind a flag (`gritz mcp --tools task,event`)
  or MCP-level toolsets would trim it, but clients already let users disable
  individual tools, and the tool descriptions stay short. Revisit if it hurts.
- **`get_task_log` returns a byte tail, not parsed turns.** The transcript is
  the driver's raw log in whatever format the agent CLI writes; parsing it per
  agent would couple the MCP to every agent integration. A byte budget keeps
  results bounded.
- **One instruction per create.** `CreateTask` and `CreateSchedule` accept
  several instructions, but nothing in the Web UI makes more than one, and one
  string is easier for an agent to fill.