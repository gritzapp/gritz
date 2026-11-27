---
title: MCP
description: Connect Claude Code, Cursor or any MCP client to gritz, over HTTP with OAuth or locally with gritz mcp, to create and manage tasks and administer the organisation; the tools it offers, and task notifications through Claude Code channels.
sidebar:
  order: 4
---

gritz is an MCP server, so the agent you work with locally, such as Claude Code
or Cursor, can hand work off to gritz: list the workspaces, create tasks, follow
them up with instructions, read what they did, and archive them. It can also
manage the organisation's agents and their schedules, events and custom event
types, and read its members, API keys and settings.

There are two ways to connect:

- **Over HTTP, to `/mcp` on the server.** Nothing to install; you sign in with
  your browser. Use this unless you need channels.
- **Locally, with `gritz mcp`.** Your client runs the `gritz` binary, which
  talks to the server with the CLI's login. It is also the only way to get
  [channels](#channels): task updates pushed into a Claude Code session.

Both offer the same [tools](#tools). Over HTTP the client acts in one
organisation; `gritz mcp` can [switch](#organisations) between the
organisations you've logged in to.

This page is about the tools *you* use to drive gritz. The tools gritz gives the
agents running inside tasks are in [Agent tools](/using/agent-tools/).

## Connect over HTTP

The server's MCP URL is on **Settings → Organisation**, in the **MCP Server**
card: `https://gritz.dev/mcp` for gritz.dev. It uses the Streamable HTTP
transport.

### Claude Code

```bash
claude mcp add --transport http gritz https://gritz.dev/mcp
```

Then run `/mcp` in Claude Code, pick `gritz` and authenticate.

### Cursor

Add the server to `~/.cursor/mcp.json`, or to `.cursor/mcp.json` in a project:

```json
{
  "mcpServers": {
    "gritz": {
      "url": "https://gritz.dev/mcp"
    }
  }
}
```

### Other clients

Give the client the URL. gritz supports the MCP authorization flow: OAuth 2.1
with PKCE and dynamic client registration, so a client that supports it
registers itself and needs no client ID.

### Signing in

When the client first connects, it opens gritz in your browser. Sign in if you
aren't already, check the organisation in the selector at the top, and click
**Approve** on the **Authorize Application** page. The page shows the account
and organisation the client will act as.

The client then acts as you, in that organisation: it sees and changes what you
can there. To use another organisation, switch to it in the Web UI, and sign the
client in again.

The client keeps its access by refreshing its token. It loses it if it isn't
used for 30 days, if you leave the organisation, or if the organisation is
deleted; sign in again then.

### With an API key

`/mcp` also accepts an [API key](/concepts/orgs-members-keys/#api-keys) as a
bearer token, for clients that can't do OAuth or that run unattended. Most
clients take a header in their config, for example:

```bash
claude mcp add --transport http gritz https://gritz.dev/mcp \
  --header "Authorization: Bearer ${GRITZ_API_KEY}"
```

The client then acts with the key: in the key's organisation, and recorded
under the key's name.

## Connect with `gritz mcp`

`gritz mcp` is a local MCP server over stdio. Your client starts it, and it
forwards each tool call to the server's API with the CLI's login. Install the
[CLI](/using/cli/) and [log in](/using/cli/#configuration), then add it to your
client.

For Claude Code:

```bash
claude mcp add gritz -- gritz mcp
```

Or in a project's `.mcp.json`:

```json
{
  "mcpServers": {
    "gritz": {
      "command": "gritz",
      "args": ["mcp", "--channel"]
    }
  }
}
```

`gritz mcp` picks its organisation, login and server as every CLI command does,
from the `.gritz.yml` files of the directory the client starts it in: a
repository whose `.gritz.yml` sets `org` gets that organisation with no
credential in the MCP config.

| Flag | |
| --- | --- |
| `--org` | The organisation, by its name in the `.gritz.yml` files. Defaults to `GRITZ_ORG`, otherwise their `org`. |
| `--key`, `-k` | An API key to use instead of the `.gritz.yml` files. Defaults to `GRITZ_API_KEY`. The session stays in the key's organisation. |
| `--server`, `-s` | The server URL to use with `--key`, which it requires. Defaults to `GRITZ_SERVER`, otherwise `https://gritz.dev`. |
| `--channel` | Push task updates into the Claude Code session. See [Channels](#channels). |

### Organisations

A session started from the `.gritz.yml` files starts in the organisation
`--org` picks, and can switch to any other entry in those files, within their
`allowed` list. A session started with `--key` has one entry, with an empty
name, for the key's organisation. A session acts in one organisation at a time, the current one, and
every other tool acts in it. Two tools manage it:

- `list_orgs` lists the entries: each one's `name`, organisation `id`,
  `server`, and whether it is `current`. It doesn't contact the server.
- `switch_org` takes an entry's `name` and makes it current. It returns the
  entry's `name`, `id` and `server`. It doesn't contact the server either: if
  the entry's login doesn't work, the first tool call after the switch fails.

The entries are those in the files when `gritz mcp` started; restart it to pick
up a `gritz login` made since.

## Tools

| Tool | |
| --- | --- |
| `list_orgs` | Lists the organisations a `gritz mcp` session can switch to, and marks the current one. See [Organisations](#organisations). |
| `switch_org` | Switches a `gritz mcp` session to another organisation. |
| `list_workspaces` | Lists the workspaces runners offer: each one's name, description, runner ID and features. |
| `list_agents` | Lists the organisation's agents. |
| `get_agent` | Gets an agent, with its triggers, link kinds and schedules. |
| `put_agent` | Creates an agent, or replaces one. |
| `delete_agent` | Deletes an agent and its schedules. |
| `create_task` | Creates a task of an agent and starts it, or creates a draft. |
| `list_tasks` | Lists the organisation's tasks, newest first, a page at a time. |
| `get_task` | Gets a task with its links, the actions it allows, and its timeline: instructions, events, reports and status changes. |
| `update_task` | Adds an instruction to a task, renames it, changes its auto-archive delay, or starts it. |
| `cancel_task` | Cancels a task. |
| `restart_task` | Restarts a task. |
| `reset_sandbox` | Deletes a finished task's sandbox, so its next run starts in a fresh one. |
| `archive_task` | Archives a finished task. |
| `unarchive_task` | Unarchives a task. |
| `get_task_log` | Reads the end of a task's agent transcript. |
| `create_link` | Links a resource, such as a pull request, to a task. |
| `list_events` | Lists the organisation's newest external events. |
| `get_event` | Gets an event in full. |
| `delete_event` | Deletes an event. |
| `list_event_types` | Lists the event types agent triggers can match, with their attributes and link types. |
| `put_custom_event_type` | Adds a custom event type, or replaces the one with the same name. |
| `delete_custom_event_type` | Deletes a custom event type. |
| `test_event` | Tests the organisation's agents with an event, or routes it for real. |
| `list_members` | Lists the organisation's members. |
| `list_keys` | Lists the organisation's API keys, without their tokens. |
| `get_org_settings` | Gets the organisation's integration URLs and its GitHub App installation. |

You don't call these yourself: ask your agent, for example "Create a gritz task
of the pets-workshop agent to add a `--json` flag, then tell me when it has
opened a pull request." It calls `list_agents` to find the agent, then
`create_task`.

gritz also points your agent at these docs, through
[`llms.txt`](https://docs.gritz.dev/llms.txt), so it can look up how gritz
works.

Each tool that changes a task returns the task's ID, name, agent ID, workspace,
runner, status, whether it is archived, its URL, and when it was created and
last updated. `list_tasks` returns the same for each task.

### `create_task`

| Argument | |
| --- | --- |
| `agent_id` | The ID of the [agent](#agents) the task belongs to. The task runs in the agent's workspace and runner, with its auto-archive delay, variables and features. Required. |
| `instruction` | The task's instruction. Required unless `draft` is set. |
| `name` | A name for the task. The task's agent names it if this is empty. |
| `draft` | Create the task as a draft, which doesn't run until `update_task` starts it. |

### `update_task`

| Argument | |
| --- | --- |
| `id` | The task ID. Required. |
| `instruction` | An instruction to add. |
| `url` | A URL the instruction is about, such as an issue. Only with `instruction`. |
| `name` | A new name for the task. |
| `auto_archive` | A new [auto-archive](/concepts/tasks/#auto-archive) delay, such as `30m` or `24h`. `0` is never; a negative value, such as `-1s`, archives the task as soon as it finishes. |
| `start` | Start the task. A running task isn't interrupted: it runs again when its current run ends. |

A call sets at least one of `instruction`, `name`, `auto_archive` and `start`.
An instruction added without `start` waits until something else starts the
task.

### `list_tasks`

| Argument | |
| --- | --- |
| `page_size` | How many tasks to return: 50 by default, at most 100. |
| `page_token` | The `next_page_token` of the previous page; empty for the first page. |
| `archived` | Include archived tasks. |

It returns `tasks` and `next_page_token`, which is empty on the last page.

### `get_task`

| Argument | |
| --- | --- |
| `id` | The task ID. Required. |
| `event_types` | Only return timeline events of these types: `instruction`, `external`, `report`, `lifecycle` or `link`. Empty returns all of them. |

Besides the fields above, it returns the task's auto-archive delay, its links,
its timeline, and `actions`: whether the task can be archived, cancelled,
restarted, started, unarchived or have its sandbox reset now.

### `cancel_task`, `restart_task`, `reset_sandbox`, `archive_task`, `unarchive_task`

Each takes the task's `id`. `archive_task` only archives a task that has
completed, failed or been cancelled; archiving destroys its sandbox.
`reset_sandbox` [resets the sandbox](/concepts/tasks/#resetting-the-sandbox) of
a finished task: it deletes the sandbox, so the task's next run starts in a
fresh one, and keeps the task's status and history. It isn't available while
the task is running or has a command pending. `get_task`'s `actions` say which
of these a task allows.

### `get_task_log`

| Argument | |
| --- | --- |
| `id` | The task ID. Required. |
| `max_bytes` | About how many bytes to return: 64 KiB by default, at most 1 MiB. |
| `before` | The `before` of a previous call, to read the bytes preceding it. Empty reads the end of the log. |

It returns `log`, the raw transcript the task's agent wrote, the same as the
task page's **Logs** tab, with [secrets](/operators/secrets/) masked; and
`before`, which is empty once `log` reaches the start of the transcript.

### `create_link`

| Argument | |
| --- | --- |
| `task_id` | The task ID. Required. |
| `url` | The resource's URL. Required. |
| `relevance` | Why the resource matters to the task. |
| `title` | A title for the link. |
| `kind` | A [link kind](/concepts/agents/#link-kinds) of the task's agent. The link is then subscribed: the resource's events that the kind lists, such as comments on a pull request, are delivered to the task. The URL must be a resource of the kind's link type. Leave it out for a plain reference link, which receives no events. |

Use it to subscribe a task to something you made yourself, such as a pull
request you opened by hand.

### Agents

An [agent](/concepts/agents/) is the organisation's template for a
kind of task, together with the triggers that create its tasks from events, the
schedules that create them on a cron schedule, and the kinds of subscribed link
its tasks may create. `list_agents` returns every
agent; `get_agent` takes an agent's `id` and returns it in full:

| Field | |
| --- | --- |
| `id` | The agent's ID. |
| `name` | The agent's name. |
| `workspace`, `runner` | The [workspace](/concepts/workspaces/) its tasks run in, and the [runner](/concepts/runners/) that offers it. `list_workspaces` lists both. |
| `prompt` | The first instruction of the tasks its triggers and schedules create. |
| `auto_archive` | The [auto-archive](/concepts/tasks/#auto-archive) delay of its tasks, in seconds, such as `"86400s"`. `0` never archives. |
| `variables` | Values for the workspace's [variables](/operators/workspaces-yaml/#variables), an object of name to string value. Each must be declared by the workspace, and every required one set. |
| `features` | Names of the workspace's [features](/operators/workspaces-yaml/#features) its tasks get, in order: where two set the same thing, the later one wins. Each must be declared by the workspace. |
| `triggers` | Each creates a task of the agent when an event matches it. The first trigger that matches wins. |
| `links` | The kinds of subscribed link its tasks may create, and the events each delivers. |
| `schedules` | Each creates a task of the agent whenever its cron expression matches. |

Each trigger has:

| Field | |
| --- | --- |
| `source`, `type` | The [event type](/reference/event-types/) it matches, as `list_event_types` lists it. |
| `conditions` | Conditions that must all hold, each an `attr`, an `op` (`equals`, `prefix`, `contains` or `glob`) and a `value`. |
| `public` | Also match events from people who aren't members of the organisation. A trigger on a custom event type must be public: custom events have no actor. |
| `yield_to` | Claim names: the trigger creates no task while an unarchived task holds a link with one of these claims on the event's resource. |
| `link` | A link kind of the agent whose type the event type declares. The task it creates gets a subscribed link of that kind to the event's resource. |

Each link kind has:

| Field | |
| --- | --- |
| `type` | A link type that `list_event_types` lists, such as `github.pr`. |
| `kind` | The kind's name, unique in the agent. Defaults to `type`. |
| `claim` | A claim stamped on every link of this kind, which triggers can `yield_to`. |
| `events` | The event types a link of this kind delivers to its task, each `{type, conditions}`: an event type that declares the link type, and conditions its events must all meet, in a trigger's shape. With none, it delivers nothing. |
| `public` | Also deliver events from people who aren't members of the organisation. |

Each [schedule](/concepts/schedules/) has:

| Field | |
| --- | --- |
| `id` | The schedule's ID. |
| `name` | The name of every task it creates. |
| `cron_expr` | When to create a task: a five-field [cron expression](/concepts/schedules/#cron-expressions) or a shorthand such as `@daily`. |
| `timezone` | The IANA timezone the cron expression is read in, such as `Europe/Paris`. Empty is `UTC`. |
| `enabled` | Whether the schedule runs. |
| `next_run_at`, `last_run_at`, `last_task_id` | When it next runs (unset while disabled), when it last ran, and the task it last created. Set by the server. |

`put_agent` takes `agent`, in the shape `get_agent` returns. With an `id`, it
replaces that agent; without one, it creates a new agent. Your agent reads an
agent, edits it and writes it back. Replacing an agent doesn't change the tasks
it already created. A schedule that keeps its `id` keeps its last run, and its
next run only moves when its cron expression, timezone or `enabled` changed; a
schedule without an `id` is added, and one left out is deleted. An agent with an
unknown workspace, event type or link type, a schedule `id` that isn't one of
its schedules, a cron expression or timezone gritz can't parse, variables the
workspace doesn't accept, or a feature it doesn't declare, fails the call. It returns
the stored agent.

`delete_agent` takes the agent's `id`, and deletes the agent and its schedules.
It fails while an unarchived task belongs to the agent: archive its tasks
first.

### `list_events`, `get_event`, `delete_event`

`list_events` takes `limit`, at most and by default 100, and returns the newest
[events](/concepts/events-and-links/) of the organisation: each one's ID, the
task it reached, its source, type, description, URL and when it arrived. It
leaves out the event's body; `get_event` takes the event's `id` and returns all
of it. `delete_event` takes the `id` of the event to delete.

### `list_event_types`

Returns every [event type](/reference/event-types/), built-in and custom, with
the attributes a trigger can have conditions on and the link types each
declares, and the link types an agent's link kinds can name. The attributes are
those `test_event` takes.

### `put_custom_event_type`

| Argument | |
| --- | --- |
| `type` | The type's name, which senders put in the event's `type`. Required. |
| `label` | The type's name in the trigger editor and on task timelines. Required. |
| `attrs` | The attributes triggers can match on, each with a `key` and an optional `label`, `help` and `placeholder`. It must include `body` and `url`. |
| `link_types` | The [link types](/reference/event-types/#link-types) the event's URL can name: `["custom.url"]`, so its events can be delivered to subscribed tasks, or none. |

It adds the type, or replaces the custom type of the same name, and returns
every custom type. The rules for names are in
[Custom events](/integrations/custom-events/#1-define-a-custom-event-type).

### `delete_custom_event_type`

Takes the `type` to delete. It fails while an agent uses the type.

### `test_event`

| Argument | |
| --- | --- |
| `source` | The event type's source, such as `github`, `jira` or `custom`. Required. |
| `type` | The event type, such as `issue_comment`. Required. |
| `url` | The absolute `http` or `https` URL of the resource the event is about. Required. |
| `body` | The event's text. |
| `attrs` | The values of the type's other attributes, each a list of strings. |
| `description` | A description shown on the timeline of a task the event reaches. It defaults to the type's label. |
| `details` | Extra key/value context shown with the event. Triggers don't match on it. |
| `fire` | Act on it: wake or create tasks. Without it, nothing changes. |

The event is checked against its type as the
[custom events webhook](/integrations/custom-events/) checks an envelope. It
returns, for each agent the event concerns, the index of the trigger that
matched and whether it creates a task or yields, with the tasks holding the
claim it yields to, and the tasks the event is delivered to, each with the link
kind that matched. With `fire`, it also returns the tasks and events it
created. An event of a GitHub or Jira type has you as its actor. A custom event
has none, so only public triggers and link kinds admit it, as when it comes from
the webhook: firing a custom event is how to send one. See
[Testing with a test event](/concepts/agents/#testing-with-a-test-event).

### `get_org_settings`

Returns the organisation's MCP URL, custom event webhook URL, Atlassian webhook
URL, GitHub App URL and GitHub App installation ID. It never returns the
Atlassian webhook secret; read it in the Web UI, as in
[Jira](/integrations/jira/#1-generate-the-webhook-secret).

## Channels

[Channels](https://code.claude.com/docs/en/channels) let an MCP server push
messages into a running Claude Code session. With `--channel`, `gritz mcp`
pushes the task updates of every organisation the session has been in, so your Claude hears about a task
finishing without polling:

```
<channel source="gritz" org="acme">Task 1234 created on my-runner/pets-workshop.</channel>
<channel source="gritz" org="acme">Task 1234 woken by event 567: octocat commented on PR #42 (https://github.com/…)</channel>
<channel source="gritz" org="acme">Task 1234 completed.</channel>
```

The `org` attribute is the name of the `.gritz.yml` entry of the task's
organisation; a session started with `--key` sends `org=""`.

It sends a message when a task is created (by anyone, by an agent's trigger or
by a schedule), when one is queued with new instructions, woken from a finished
state by an event, completed, failed, cancelled, restarted or archived, and when a shell is opened in one. It leaves out the changes made through the same `gritz mcp` process, so
your Claude isn't told about the task it just created.

It also tells your Claude when an organisation's notification stream goes down
and comes back, since it misses updates in between:

```
<channel source="gritz" org="acme">Lost the connection: unexpected status: 502, retrying.</channel>
<channel source="gritz" org="acme">Reconnected (notifications sent while it was down were missed).</channel>
```

A stream that can't connect in the first place is reported as lost too.
`gritz mcp` keeps retrying until it connects, and sends nothing more until the
stream comes back.

Channels are a Claude Code research preview, and only work over stdio, so only
with `gritz mcp`. gritz isn't on the preview's allowlist, so start Claude Code
with:

```bash
claude --dangerously-load-development-channels server:gritz
```

### Muting tasks

With `--channel`, three more tools let your Claude choose which tasks it hears
about. Every task is on by default.

| Tool | |
| --- | --- |
| `channel_mute` | Stops messages for the tasks in `task_ids`, or for every task with `all=true`. |
| `channel_unmute` | Resumes messages for the tasks in `task_ids`, or for every task with `all=true`. After `channel_mute` with `all=true`, unmuting some tasks keeps just those on. |
| `channel_muted` | Shows which tasks are muted. |

Each tool takes `org`, the `org` attribute of the messages to mute, and names
it in its result; in a session started with `--key` it is empty and can be
left out. Muting lasts until `gritz mcp` exits. It is separate from
[subscribed links](/concepts/events-and-links/#subscriptions), which route
external events into tasks.
