# MCP

gritz is an MCP server, so the agent you work with locally, such as Claude Code
or Cursor, can hand work off to gritz: list the workspaces, create tasks, follow
them up with instructions, read what they did, and archive them. It can also
manage the organisation's events, custom event types, schedules and routing
rules, and read its members, API keys and settings.

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
| `--key`, `-k` | An API key to use instead of the `.gritz.yml` files. Defaults to `GRITZ_API_KEY`. The session stays in the key's organisation, without `list_orgs` and `switch_org`. |
| `--server`, `-s` | The server URL to use with `--key`, which it requires. Defaults to `GRITZ_SERVER`, otherwise `https://gritz.dev`. |
| `--auto-archive` | The [auto-archive](/concepts/tasks/#auto-archive) delay for tasks created without an `auto_archive` argument, such as `1h`. |
| `--channel` | Push task updates into the Claude Code session. See [Channels](#channels). |

### Organisations

A session started from the `.gritz.yml` files starts in the organisation
`--org` picks, and can switch to any other entry in those files, within their
`allowed` list. It acts in one organisation at a time, the current one, and
every other tool acts in it. Two tools manage it:

- `list_orgs` lists the entries: each one's `name`, organisation `id`,
  `server`, and whether it is `current`. It doesn't contact the server.
- `switch_org` takes an entry's `name` and makes it current. It checks the
  entry's login with the server first, and fails, staying in the current
  organisation, if the login doesn't work or belongs to another organisation
  than the entry's `id`. It returns the entry's `name`, `id` and `server`, and
  the organisation's name as `org_name`.

The entries are those in the files when `gritz mcp` started; restart it to pick
up a `gritz login` made since.

## Tools

| Tool | |
| --- | --- |
| `list_orgs` | Lists the organisations a `gritz mcp` session can switch to, and marks the current one. See [Organisations](#organisations). |
| `switch_org` | Switches a `gritz mcp` session to another organisation. |
| `list_workspaces` | Lists the workspaces runners offer: each one's name, description and runner ID. |
| `create_task` | Creates a task and starts it, or creates a draft. |
| `list_tasks` | Lists the organisation's tasks, newest first, a page at a time. |
| `get_task` | Gets a task with its links, the actions it allows, and its timeline: instructions, events, reports and status changes. |
| `update_task` | Adds an instruction to a task, renames it, changes its auto-archive delay, or starts it. |
| `cancel_task` | Cancels a task. |
| `restart_task` | Restarts a task. |
| `archive_task` | Archives a finished task. |
| `unarchive_task` | Unarchives a task. |
| `get_task_log` | Reads the end of a task's agent transcript. |
| `create_link` | Links a resource, such as a pull request, to a task. |
| `list_events` | Lists the organisation's newest external events. |
| `get_event` | Gets an event in full. |
| `delete_event` | Deletes an event. |
| `list_event_types` | Lists the event types routing rules can match, with their attributes. |
| `put_custom_event_type` | Adds a custom event type, or replaces the one with the same name. |
| `delete_custom_event_type` | Deletes a custom event type. |
| `test_event` | Tests the routing rules with an event, or routes it for real. |
| `list_schedules` | Lists the organisation's schedules. |
| `get_schedule` | Gets a schedule, with its instructions and its next and last run. |
| `create_schedule` | Creates a schedule. |
| `update_schedule` | Changes a schedule's template, cron expression or timezone. |
| `set_schedule_enabled` | Enables or disables a schedule. |
| `run_schedule` | Creates a task from a schedule now. |
| `delete_schedule` | Deletes a schedule. |
| `list_members` | Lists the organisation's members. |
| `list_keys` | Lists the organisation's API keys, without their tokens. |
| `get_org_settings` | Gets the organisation's integration URLs and its GitHub App installation. |
| `get_routing_rules` | Gets the organisation's routing rules. |
| `set_routing_rules` | Replaces every routing rule of the organisation. |

You don't call these yourself: ask your agent, for example "Create a gritz task
in the pets-workshop workspace to add a `--json` flag, then tell me when it has
opened a pull request." It calls `list_workspaces` to find the runner, then
`create_task`.

gritz also points your agent at these docs, through
[`llms.txt`](https://docs.gritz.dev/llms.txt), so it can look up how gritz
works.

Each tool that changes a task returns the task's ID, name, workspace, runner,
namespace, status, whether it is archived, its URL, and when it was created and
last updated. `list_tasks` returns the same for each task.

### `create_task`

| Argument | |
| --- | --- |
| `workspace` | The [workspace](/concepts/workspaces/). Required. |
| `runner` | The [runner](/concepts/runners/) that offers it. Required. |
| `instruction` | The task's instruction. Required unless `draft` is set. |
| `name` | A name for the task. The task's agent names it if this is empty. |
| `auto_archive` | The auto-archive delay, such as `30m` or `24h`. `0` is never; a negative value, such as `-1s`, archives the task as soon as it finishes. |
| `namespace` | The task's [namespace](/concepts/namespaces/). Empty is the default namespace. It can't be changed later. |
| `draft` | Create the task as a draft, which doesn't run until `update_task` starts it. |

### `update_task`

| Argument | |
| --- | --- |
| `id` | The task ID. Required. |
| `instruction` | An instruction to add. |
| `url` | A URL the instruction is about, such as an issue. Only with `instruction`. |
| `name` | A new name for the task. |
| `auto_archive` | A new auto-archive delay, as in `create_task`. |
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
restarted, started or unarchived now.

### `cancel_task`, `restart_task`, `archive_task`, `unarchive_task`

Each takes the task's `id`. `archive_task` only archives a task that has
completed, failed or been cancelled; archiving destroys its sandbox. `get_task`'s
`actions` say which of these a task allows.

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
| `subscribe` | Route the resource's events, such as comments on a pull request, to the task. See [subscriptions](/concepts/events-and-links/#subscriptions). |

Use it to subscribe a task to something you made yourself, such as a pull
request you opened by hand.

### `list_events`, `get_event`, `delete_event`

`list_events` takes `limit`, at most and by default 100, and returns the newest
[events](/concepts/events-and-links/) of the organisation: each one's ID, the
task it reached, its source, type, description, URL and when it arrived. It
leaves out the event's body; `get_event` takes the event's `id` and returns all
of it. `delete_event` takes the `id` of the event to delete.

### `list_event_types`

Returns every [event type](/reference/event-types/), built-in and custom, with
the attributes a routing rule can have conditions on. Those are the attributes
`test_event` takes.

### `put_custom_event_type`

| Argument | |
| --- | --- |
| `type` | The type's name, which senders put in the event's `type`. Required. |
| `label` | The type's name in the routing rule editor and on task timelines. Required. |
| `attrs` | The attributes rules can match on, each with a `key` and an optional `label`, `help` and `placeholder`. It must include `body` and `url`. |

It adds the type, or replaces the custom type of the same name, and returns
every custom type. The rules for names are in
[Custom events](/integrations/custom-events/#1-define-a-custom-event-type).

### `delete_custom_event_type`

Takes the `type` to delete. It fails while a routing rule uses the type.

### `test_event`

| Argument | |
| --- | --- |
| `source` | The event type's source, such as `github`, `jira` or `custom`. Required. |
| `type` | The event type, such as `issue_comment`. Required. |
| `url` | The absolute `http` or `https` URL of the resource the event is about. Required. |
| `body` | The event's text. |
| `attrs` | The values of the type's other attributes, each a list of strings. |
| `description` | A description shown on the timeline of a task the event reaches. It defaults to the type's label. |
| `details` | Extra key/value context shown with the event. Rules don't match on it. |
| `fire` | Act on the match: wake or create tasks. Without it, nothing changes. |

The event is checked against its type as the
[custom events webhook](/integrations/custom-events/) checks an envelope. It
returns the rule that matched, if any, and whether it wakes linked tasks or
creates one. An event of a GitHub or Jira type has you as its actor. A custom
event has none, so only public rules match it, as when it comes from the
webhook: firing a custom event is how to send one.

### `create_schedule`

| Argument | |
| --- | --- |
| `workspace` | The workspace of the tasks it creates. Required. |
| `runner` | The runner that offers it. Required. |
| `instruction` | The instruction every task it creates starts with. Required. |
| `cron_expr` | When to create a task: a five-field [cron expression](/concepts/schedules/#cron-expressions) or a shorthand such as `@daily`. Required. |
| `timezone` | The IANA timezone the cron expression is read in, such as `Europe/Paris`. Defaults to `UTC`. |
| `name` | The name of every task it creates. |
| `namespace` | Its tasks' namespace. |
| `auto_archive` | Its tasks' auto-archive delay, as in `create_task`. |
| `enabled` | Whether the schedule runs. Without it, the schedule is created disabled. |

It returns the [schedule](/concepts/schedules/). `list_schedules`,
`get_schedule`, `update_schedule` and `set_schedule_enabled` return schedules
in the same shape.

### `update_schedule`

Takes the schedule's `id` and any of `create_schedule`'s arguments except
`enabled`. The arguments left out keep their current values. `instruction`
replaces every instruction of the schedule.

### `set_schedule_enabled`, `run_schedule`, `delete_schedule`

Each takes the schedule's `id`; `set_schedule_enabled` also takes `enabled`.
`run_schedule` creates a task from the schedule now, even if it is disabled,
without changing its next or last run, and returns the task. Deleting a
schedule keeps the tasks it created.

### `get_org_settings`

Returns the organisation's MCP URL, custom event webhook URL, Atlassian webhook
URL, GitHub App URL and GitHub App installation ID. It never returns the
Atlassian webhook secret; read it in the Web UI, as in
[Jira](/integrations/jira/#1-generate-the-webhook-secret).

### `get_routing_rules`, `set_routing_rules`

`get_routing_rules` returns the organisation's
[routing rules](/concepts/routing-rules/) in order. `set_routing_rules` takes
`rules`, the whole list in the same shape, and replaces every rule with it: a
rule missing from the list is deleted. Your agent reads the rules, edits the
list and writes it all back. A rule that names an unknown event type or
attribute fails the call with the bad rule named, and no rule is changed. It
returns the stored rules.

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
organisation; a session started with `--key` sends none.

It sends a message when a task is created (by anyone, by a routing rule or by a
schedule), when one is queued with new instructions, woken from a finished state
by an event, given an event without being woken, completed, failed, cancelled,
restarted or archived, and when a shell is opened in one. It leaves out the changes made through the same `gritz mcp` process, so
your Claude isn't told about the task it just created.

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
it in its result; leave it out only in a session started with `--key`. Muting lasts until `gritz mcp` exits. It is separate from
[subscribed links](/concepts/events-and-links/#subscriptions), which route
external events into tasks.