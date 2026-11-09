---
title: "Multi-org gritz mcp"
description: "gritz mcp resolves one org at startup and acts in it for the whole session, so a user with several .gritz.yml org entries has to run one gritz mcp per org or restart it to switch."
---

Issue: https://github.com/gritzapp/gritz/issues/2122

## Problem

`gritz mcp` calls `configfile.Resolve` once at startup
([`internal/command/mcp.go`](https://github.com/gritzapp/gritz/blob/master/internal/command/mcp.go)),
builds one `gritzclient` for the selected entry, and hands it to
`mcpserver.AddTools`. With `--channel` it also opens one `NotificationClient`
for that entry. Everything the session does, every tool call and every channel
message, is in that one org.

A user with several entries in their merged `.gritz.yml` files (see
[Multi-org CLI auth](/proposals/implemented/multi-org-cli-auth/)) has to
register one `gritz mcp` per org in their MCP client, which multiplies every
tool by the number of orgs, or restart the server with another `--org` to
switch.

Decided in the issue: the session gets tools that list the available orgs and
switch the current one, and the existing tools act in the current org. They do
not take an org argument. The server's `/mcp` endpoint is out of scope: its
caller (an OAuth token or an API key) is already bound to one org.

## Design

### Overview

- Two new tools on `gritz mcp`, `list_orgs` and `switch_org`. They exist only
  when the session was started from `.gritz.yml` files, not with `--key`.
- The session has one **current org**. Every other tool, the existing task
  tools and the [MCP admin tools](/proposals/implemented/mcp-admin-tools/) alike, acts
  in it. `switch_org` replaces the current org's client with a new one.
- With `--channel`, the session follows **every org it has used**: the startup
  org, plus each org it switches to. Each channel message carries the org's
  entry name as a tag attribute, and mute sets are kept per org.

### `list_orgs`

Lists the entries the session can switch to: `merged.Entries()`, every entry
in the merged `orgs` that `allowed` permits, sorted by name, each mapped to
`{name, id, server, current}`. No arguments, `ReadOnlyHint`.

```json
{
  "orgs": [
    {"name": "acme", "id": 12, "server": "https://gritz.dev", "current": true},
    {"name": "local", "id": 1, "server": "http://localhost:8080", "current": false}
  ]
}
```

- `server` is the entry's own `server`, else `https://gritz.dev`; never
  empty.
- Keys are never returned.
- The list is the merged files as loaded at startup. An entry added with
  `gritz login` while the session runs shows up after a restart.
- It makes no server call, so it never fails on a stale key; `switch_org` is
  where a bad key surfaces.

This is how the current org is shown: the entry with `current: true` here, and
the result of `switch_org`. No other tool result names the org; task results
already carry a Web UI `url` with `?org=<id>`.

### `switch_org`

Makes the named entry the current org.

| Argument | |
| --- | --- |
| `name` | An entry name from `list_orgs`. Required. |

1. Look the entry up with `merged.Lookup(name)` on the `*configfile.Merged`
   loaded at startup: the same rules as `--org` at startup, so a name outside `allowed` or with no entry fails
   with the same error message.
2. Build a `gritzclient` for it from `entry.TokenSource(ctx)` (see
   [Credentials](#credentials)) with the session's client id, and call
   `GetCaller`. A failure (bad or expired key, unreachable server) is an error
   result, and the current org is unchanged.
3. If `GetCallerResponse.org.id` differs from the entry's `id`, fail without
   switching: `entry "acme" says org 12, but its key belongs to org 31`. This
   keeps a mis-pasted key from sending writes to an org the user didn't name.
4. Make the client current. With `--channel`, start following the org (see
   [Channel notifications](#channel-notifications)).
5. Return the new current org:

```json
{"name": "local", "id": 1, "server": "http://localhost:8080", "org_name": "Local dev"}
```

`org_name` is the server-side org name from `GetCaller`, so the agent can tell
the user which org it is now in. Switching to the current org repeats the check
and is otherwise a no-op.

### How the tools pick up the current org

`mcpserver.AddTools` takes a function returning the service instead of the
service itself:

```go
type ServiceFunc func() gritzv1connect.GritzServiceHandler
```

Each handler calls it **once**, at its start, and uses that value for every RPC
it makes. Handlers that make several calls (`update_task` and its follow-up
`GetTask`; the admin tools' read-modify-write `put_custom_event_type`,
`update_schedule`, `set_routing_rules`) therefore stay in one org even if a
`switch_org` runs concurrently. The server's `/mcp` handler passes
`mcpserver.Static(apiServer)`, and `gritz mcp --key` passes
`mcpserver.Static(client)`, so neither changes behaviour.

Clients are created **lazily, on switch**, and only the current one is kept.
`gritzclient.New` builds an `http.Client` and a Connect client and makes no
connection, so there's nothing worth caching or creating up front; switching
back to an org builds a fresh one. One client id (`uuid.NewString()` at
startup, as now) is shared by every client the session builds, so the
notification streams filter out the session's own changes in every org.

### Credentials

Every client and `NotificationClient` the session builds gets its credentials
from the entry's token source, `entry.TokenSource(ctx)`, as every command does
since [CLI OAuth login](/proposals/implemented/cli-oauth-login/), never from
`entry.Key`.
A `key` entry gives a static token, and a `refresh_token` entry exchanges and
rotates its refresh token, so both kinds work for the startup org, for
`switch_org` and for `Channel.Follow`. `--key` is a static token too.

`ctx` is the process context, not the tool call's: the token source outlives
the `switch_org` call that built it, and refreshes the access token on later
calls and on each SSE reconnect.

### Channel notifications

**Follow every org the session has used.** The startup org is followed from
the start; `switch_org` starts a `NotificationClient` for the new org if it
isn't followed yet. Streams are never stopped before the process exits.

Following only the current org would be simpler, but an agent that creates a
task in `acme`, switches to `local` and does work there would never hear that
the `acme` task finished, which is what channels are for. Following every
entry at startup would open a stream per entry, including entries the user
never touches in this session and stale keys that only fail and log.
Following the orgs the session has used covers exactly the tasks the agent can
have acted on.

Followed orgs are identified by entry name, `""` in `--key` mode. Two entries
for the same org (`acme` and `acme-ci`) get separate streams and mute sets, so
a session that switches between them hears each notification once per entry.
That takes two entries for one org and a switch between them, which is rare
enough not to need deduplicating.

**Telling tasks apart.** Task ids are a `BIGSERIAL` per server, so two orgs on
one server never share an id, but the same id can exist on two servers
(`acme` on gritz.dev and `local` on localhost). Every channel message from a
`.gritz.yml` session carries the entry name in `mcpchannel.Params.Meta`, which
Claude Code renders as a tag attribute:

```
<channel source="gritz" org="acme">Task 1234 completed.</channel>
```

The message text is
unchanged, and `--key` sessions send no `org` attribute (there's only one org).
The attribute is always set, not only once a second org is followed, so the
format doesn't change mid-session.

**Mute sets per org.** `Channel` keeps one `TaskFilter` per entry name.
`Forward` checks the filter of the entry whose stream delivered the
notification. `channel_mute`, `channel_unmute` and `channel_muted` take an
`org` argument, an entry name as shown in the `org="..."` channel attribute,
and act on that org's set, including `all=true`. `org` is required unless the
session uses `--key`, whose stream has no name; an `org` that isn't followed
is an error result (`not following org "x"`). Their results gain an `org` field naming it:

```json
{"org": "acme", "all": false, "muted": [1234], "note": "..."}
```

Muting doesn't depend on the current org, so `switch_org` doesn't change which
tasks are muted. Mute sets reset when the process exits.

### Flags

The startup selection is unchanged: `--key`, `--org` and their env vars
resolve as in every other command. Every entry, at startup and on
`switch_org`, uses its own `server`, else `https://gritz.dev`.

- **`--org` / `GRITZ_ORG`** picks the startup current org; otherwise it's the
  merged `org`, and with neither the command fails as now. `switch_org`
  can then move to any entry `list_orgs` shows.
- **`allowed`** limits `list_orgs` and `switch_org` exactly as it limits
  `--org`. A repo whose `.gritz.yml` pins `allowed: [acme]` gets a session
  that can't leave `acme`.
- **`--key` / `GRITZ_API_KEY`** (with `--server`, which only applies to
  `--key`) is a single raw key with no entries: `list_orgs` and `switch_org` aren't registered, as the channel tools aren't
  without `--channel`. The session behaves as today, apart from the per-org
  mute-set refactor being invisible with one org.

### Instructions

When the org tools are registered, `gritz mcp` appends to
`mcpserver.Instructions`:

> This session acts in one gritz organisation at a time. list_orgs lists the
> organisations and marks the current one; switch_org changes it. Every other
> tool acts in the current organisation. Check it with list_orgs before
> changing anything if you're not sure which organisation you're in.

`/mcp` and `--key` sessions keep `mcpserver.Instructions` as is.

### Composing with the MCP admin tools

The [MCP admin tools](/proposals/implemented/mcp-admin-tools/) are registered by
`mcpserver.AddTools`, so they get the `ServiceFunc` and the once-per-call rule
with no extra work: `list_members`, `get_org_settings`, `set_routing_rules`,
schedules and events all act in the current org. Two consequences:

- The name `list_orgs` doesn't clash with any admin tool, and `switch_org`
  stays out of `mcpserver`: it only exists in the stdio bridge, because `/mcp`
  callers can't switch.
- The admin tools never create or delete orgs. An org created in the Web UI
  becomes switchable after `gritz login` and a session restart.

### API

```go
package configfile // github.com/gritzapp/gritz/internal/configfile

// Entries returns the entries Lookup accepts (those Allowed permits), sorted
// by name, each as Lookup returns it: Name and Path set, Server defaulted.
func (m *Merged) Entries() []Entry // new

// Lookup returns the entry named org, or m.Org when org is empty, enforcing
// m.Allowed, with Name and Path set, and Server set to DefaultServer when the
// entry has none. Call Validate first.
func (m *Merged) Lookup(org string) (*Entry, error) // changed: fills in DefaultServer

// Resolve applies --key, --server and --org.
func Resolve(cmd *cli.Command) (*Entry, error) // changed: no longer defaults Server; Lookup does
```

`gritz mcp` loads the files as every command does,
`configfile.Load(configfile.LoadOptions{})` (an empty `Dir` is the working
directory) followed by `merged.Validate()`, and keeps the `*Merged` for the
session. `--key` sessions skip loading entirely, as now.

```go
package mcpserver // github.com/gritzapp/gritz/internal/server/mcpserver

// ServiceFunc returns the service a tool call goes to; each handler calls it
// once per call.
type ServiceFunc func() gritzv1connect.GritzServiceHandler // new

// Static returns a ServiceFunc that always returns s.
func Static(s gritzv1connect.GritzServiceHandler) ServiceFunc // new

// AddTools registers the user-facing gritz tools on the given MCP server.
func AddTools(server *mcp.Server, service ServiceFunc, opts ...Option) // changed: takes a ServiceFunc

// NewServer builds an MCP server with the user-facing gritz tools registered.
func NewServer(service gritzv1connect.GritzServiceHandler, opts ...Option) *mcp.Server // unchanged; wraps with Static
```

```go
package mcpbridge // github.com/gritzapp/gritz/internal/mcpbridge

// OrgsInstructions is appended to the server instructions when the org tools
// are registered.
const OrgsInstructions = "..." // new

// OrgSwitcher holds the session's current org and its client.
type OrgSwitcher struct{ ... } // new

// NewOrgSwitcher starts the session in entry, calling follow for it and for every
// org switched to later. switch_org looks entries up in merged. Clients are
// built from entry.TokenSource(ctx).
func NewOrgSwitcher(ctx context.Context, merged *configfile.Merged, entry *configfile.Entry, clientID string, follow func(entry *configfile.Entry)) *OrgSwitcher // new

// Service returns the current org's client; it is a mcpserver.ServiceFunc.
func (o *OrgSwitcher) Service() gritzv1connect.GritzServiceHandler // new

// AddTools registers list_orgs and switch_org.
func (o *OrgSwitcher) AddTools(server *mcp.Server) // new

// NewChannel returns a Channel that forwards through sender and follows no
// org yet.
func NewChannel(sender ChannelSender, clientID string) *Channel // changed: takes clientID

// Follow starts forwarding entry's notifications, labelled with entry.Name
// (none when empty, as for --key), unless it is already followed. The
// NotificationClient is built from entry.TokenSource(ctx). The stream runs
// until ctx ends.
func (c *Channel) Follow(ctx context.Context, entry *configfile.Entry) // new

// Forward applies the summary gate and the mute set of the entry named name,
// then pushes the channel notification, with name as the org attribute.
func (c *Channel) Forward(ctx context.Context, name string, n model.Notification) // changed: takes the entry name
```

**How it fits:** with `--key`, `gritz mcp` calls `Resolve`, builds one client
from the entry's token source, passes `mcpserver.Static(client)` to `AddTools`,
and with `--channel` calls `ch.Follow(ctx, entry)` (the entry has no `Name`).
Otherwise it loads and validates the `*Merged`, calls `merged.Lookup(--org)`,
builds `mcpbridge.NewOrgSwitcher` with `ch.Follow` as `follow` (a no-op without
`--channel`), passes `switcher.Service` to `AddTools`, calls `switcher.AddTools`, and
appends `OrgsInstructions`. The `NotificationClient` setup moves from `mcp.go`
into `Channel.Follow`. `list_orgs` maps `merged.Entries()` to its result, with no second lookup;
`switch_org` calls `merged.Lookup`, `gritzclient.New` with
`Options{TokenSource: entry.TokenSource(ctx)}`, `GetCaller` and `follow`;
`Channel`'s tools pick the mute set by their `org` argument.

## Implementation Plan

1. **`Merged.Entries` and the `Lookup` server default** — Delivers:
   `(*Merged).Entries`; `Lookup` sets `DefaultServer` on an entry without a
   server, and `Resolve` stops doing it. Depends on: nothing. Verifiable by:
   the existing `configfile` tests pass; tests that `Entries` filters by
   `allowed`, sorts by name and returns each entry as `Lookup` would (`Name`
   set, `Server` defaulted), and that `Lookup` keeps an entry's own `server` and
   fills in `DefaultServer` when it has none.
2. **`mcpserver.ServiceFunc`** — Delivers: `ServiceFunc`, `Static`,
   `AddTools` taking a `ServiceFunc`, every handler snapshotting the service
   once; `/mcp` and `gritz mcp` pass `Static`. No behaviour change. Depends on:
   nothing. Verifiable by: the existing `mcpserver` tests pass with
   `Static(mock)`; one test where the `ServiceFunc` returns a different mock on
   each call and `update_task` sends both its RPCs to the first. Admin-tool
   slices that land after this one follow the same rule.
3. **Per-org channel** — Delivers: `Channel.Follow` owning the
   `NotificationClient`, per-org mute sets, the `org` meta attribute on `.gritz.yml`
   sessions, and the `org` field in the channel tools' results; `gritz mcp`
   follows only its startup org. Updates the Channels section of
   `integrations/mcp`. Depends on: (1). Verifiable by: `mcpbridge` tests that
   `Forward` uses the delivering org's mute set, that the meta carries the name
   and is absent with an empty name, that a second `Follow` for the same
   entry starts no stream, and that the mute tools edit
   the set `org` names.
4. **`list_orgs` and `switch_org`** — Delivers: `mcpbridge.OrgSwitcher`, the two
   tools, `OrgsInstructions`, the `gritz mcp` wiring, and following each
   switched-to org. Updates `integrations/mcp` (a section on orgs, the tool
   table, the `--key` note). Depends on: (1), (2), (3). Verifiable by:
   `mcpbridge` tests with `httptest` servers for `switch_org` (success, bad
   key leaves the current org, id mismatch fails, `allowed` enforced, a
   `refresh_token` entry authenticates); an e2e
   test that logs in to two orgs, starts `gritz mcp --channel` in-process,
   creates a task in org A, switches to org B and creates a task there; then
   `list_tasks` shows only org B's task, and cancelling the org A task from
   another client delivers its channel message with `org="a"`.

Slices 1 and 2 are independent.

## Trade-offs

- **Switch tools vs. an org argument on every tool.** Decided in the issue.
  An argument on 30-odd tools costs context on every call and has to be
  threaded through every handler; a current org costs one tool call per switch
  and the risk of acting in the wrong org, which `list_orgs`, the switch
  result, the instructions and the `?org=` in task URLs keep visible.
- **Follow used orgs vs. only the current one vs. all.** See
  [Channel notifications](#channel-notifications): current-only loses
  completions of tasks the agent started; all-entries opens streams nobody
  asked for. Used orgs costs one stream per org switched to, never torn down.
- **`org` as a tag attribute vs. a prefix in the text.** `Meta` is what the
  channel protocol offers for structured context, and it leaves the message
  text and its tests unchanged.
- **Fresh client per switch vs. a client cache.** Clients hold no connection
  state worth keeping; a cache would be a map with no benefit.
- **`GetCaller` on switch vs. trusting the entry.** One round trip per switch
  catches a dead key before the first real call fails, and an id mismatch
  before a write lands in the wrong org.
- **Snapshot of `.gritz.yml` at startup vs. reloading on each call.**
  Reloading would pick up `gritz login` without a restart, but would let the
  current entry's key or `allowed` change under a running session. Logins are
  rare; a restart is fine.
- **Startup error vs. starting with no current org.** A session with no
  merged `org` and no `--org` fails at startup, as today, rather than starting
  with only `list_orgs` and `switch_org` working until the first switch.
- **No `org` field on state-changing results.** Results stay unchanged;
  `list_orgs`, the `switch_org` result and the `?org=` in task URLs show the
  current org.
