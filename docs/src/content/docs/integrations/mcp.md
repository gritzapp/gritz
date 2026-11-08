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

A request to `/mcp` without a valid bearer token gets a `401` with a
`WWW-Authenticate` challenge whose `resource_metadata` points at the server's
protected resource metadata:

```http
HTTP/1.1 401 Unauthorized
WWW-Authenticate: Bearer resource_metadata="https://gritz.dev/.well-known/oauth-protected-resource"
```

The client reads the authorization server from there, and starts the sign-in.
An expired or invalid token gets the same challenge with
`error="invalid_token"`. `/mcp` doesn't accept the Web UI's browser session.

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
| `create_task` | Creates a task and starts it. |
| `list_tasks` | Lists the organisation's 50 newest unarchived tasks: ID, name, workspace, status and URL. |
| `get_task` | Gets a task with its links and its full timeline: instructions, events, reports and status changes. |
| `update_task` | Adds an instruction to a task, and optionally starts it. |
| `archive_task` | Archives a finished task. |

You don't call these yourself: ask your agent, for example "Create a gritz task
in the pets-workshop workspace to add a `--json` flag, then tell me when it has
opened a pull request." It calls `list_workspaces` to find the runner, then
`create_task`.

### `create_task`

| Argument | |
| --- | --- |
| `workspace` | The [workspace](/concepts/workspaces/). Required. |
| `runner` | The [runner](/concepts/runners/) that offers it. Required. |
| `instruction` | The task's instruction. Required. |
| `name` | A name for the task. The task's agent names it if this is empty. |
| `auto_archive` | The auto-archive delay, such as `30m` or `24h`. `0` is never; a negative value, such as `-1s`, archives the task as soon as it finishes. |

It returns the task's ID, name, workspace, status and URL.

### `update_task`

| Argument | |
| --- | --- |
| `id` | The task ID. Required. |
| `instruction` | The instruction to add. Required. |
| `url` | A URL the instruction is about, such as an issue. |
| `start` | Start the task. A running task isn't interrupted: it runs again when its current run ends. |

An instruction added without `start` waits until something else starts the task.

### `get_task`, `list_tasks`, `archive_task`

`get_task` and `archive_task` take the task's `id`. `archive_task` only archives
a task that has completed, failed or been cancelled; archiving destroys its
sandbox. Cancelling and restarting tasks are done in the
[Web UI](/using/web-ui/#the-task-page).

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
