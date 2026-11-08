---
title: MCP
description: Connect Claude Code, Cursor or any MCP client to gritz, over HTTP with OAuth or locally with gritz mcp, to create and manage tasks; the tools it offers, and task notifications through Claude Code channels.
sidebar:
  order: 4
---

gritz is an MCP server, so the agent you work with locally, such as Claude Code
or Cursor, can hand work off to gritz: list the workspaces, create tasks, follow
them up with instructions, read what they did, and archive them.

There are two ways to connect:

- **Over HTTP, to `/mcp` on the server.** Nothing to install; you sign in with
  your browser. Use this unless you need channels.
- **Locally, with `gritz mcp`.** Your client runs the `gritz` binary, which
  talks to the server with an API key. It is also the only way to get
  [channels](#channels): task updates pushed into a Claude Code session.

Both offer the same [tools](#tools), and act in one organisation.

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
forwards each tool call to the server's API with an API key. Install the
[CLI](/using/cli/), create an API key on the **Keys** page, and add it to your
client.

For Claude Code:

```bash
claude mcp add gritz --env GRITZ_TOKEN=xat_... -- gritz mcp
```

Or in a project's `.mcp.json`:

```json
{
  "mcpServers": {
    "gritz": {
      "command": "gritz",
      "args": ["mcp", "--channel"],
      "env": { "GRITZ_TOKEN": "${GRITZ_TOKEN}" }
    }
  }
}
```

`gritz mcp` takes the key from `--token` or `GRITZ_TOKEN`, not from the CLI's
`config.json`, and talks to `https://gritz.dev` unless `--server` says
otherwise:

| Flag | |
| --- | --- |
| `--token` | The API key. Required; defaults to `GRITZ_TOKEN`. |
| `--server`, `-s` | The server URL. Defaults to `https://gritz.dev`. `GRITZ_SERVER` isn't read. |
| `--auto-archive` | The [auto-archive](/concepts/tasks/#auto-archive) delay for tasks created without an `auto_archive` argument, such as `1h`. |
| `--channel` | Push task updates into the Claude Code session. See [Channels](#channels). |

## Tools

| Tool | |
| --- | --- |
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

You don't call these yourself: ask your agent, for example "Create a gritz task
in the pets-workshop workspace to add a `--json` flag, then tell me when it has
opened a pull request." It calls `list_workspaces` to find the runner, then
`create_task`.

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

## Channels

[Channels](https://code.claude.com/docs/en/channels) let an MCP server push
messages into a running Claude Code session. With `--channel`, `gritz mcp`
pushes the organisation's task updates, so your Claude hears about a task
finishing without polling:

```
Task 1234 created on my-runner/pets-workshop.
Task 1234 woken by event 567: octocat commented on PR #42 (https://github.com/…)
Task 1234 completed.
```

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

Muting lasts until `gritz mcp` exits. It is separate from
[subscribed links](/concepts/events-and-links/#subscriptions), which route
external events into tasks.
