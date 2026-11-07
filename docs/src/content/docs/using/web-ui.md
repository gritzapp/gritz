---
title: Web UI
description: A tour of the gritz Web UI, the task list and task page, schedules, events and routing rules, workspaces, members, keys and settings.
sidebar:
  order: 1
---

The Web UI is served by the gritz server under `/ui/`, for example
`https://gritz.dev/ui/`. It updates live: task statuses, timelines and logs
change as they happen, without reloading. The dot next to the organisation
selector shows whether the live connection is up.

The bar at the top links to every page, and holds the organisation selector, the
light and dark theme toggle, **Settings** and **Logout**. Press Ctrl+K (⌘K on a
Mac) anywhere to jump to a task by name.

## Tasks

**Tasks** lists the organisation's tasks, newest first, with their runner,
workspace and [status](/concepts/tasks/#statuses). A `command:` badge next to the
status is an action the runner hasn't carried out yet. Turn on **Show
archived** to include archived tasks, and archive or unarchive a task from its
row.

**+ Task** creates a task. Pick a runner, then one of its workspaces; the name,
[namespace](/concepts/namespaces/) and
[auto-archive](/concepts/tasks/#auto-archive) delay are optional. The form
remembers the last runner and workspace you used. The task is created as a
draft, and starts when you send its first instruction.

## The task page

The sidebar on the left holds the task's status and name, its three views, its
details (runner, workspace, namespace, created and updated times), its links, and
its actions:

- **Archive** or **Unarchive**. When an auto-archive delay is counting down, the
  time left shows next to it.
- **Cancel**, while the task is pending or running.
- **Restart**, once it is running or finished.
- **Auto-archive**, to change the delay: never, immediately, 1 hour, 6 hours,
  24 hours or 7 days.

The arrow at the top collapses the sidebar to icons.

### Timeline

The timeline is everything that happened to the task, in order: your
instructions, events from GitHub, Jira and custom sources, the agent's reports,
links it created, and status changes with who caused them. It opens at the
latest entry and loads older ones as you scroll up.

The box at the bottom sends an instruction: Enter sends it, Shift+Enter adds a
new line. A finished task starts again with it; a running task picks it up when
its current run ends. See [Instructions and runs](/concepts/tasks/#instructions-and-runs).

### Logs

The task's log, as the sandbox writes it: setup commands, the agent's messages
and tool calls, and gritz's own lines. It follows new output while the task
runs. Declared [secrets](/operators/secrets/) are masked.

### Shell

A terminal in the task's sandbox, for looking at what the agent left behind. It
can be opened once the task has finished, and works like
[`gritz shell`](/using/cli/#gritz-shell): the runner starts the sandbox again
with a shell in place of the agent.

## Schedules

**Schedules** lists the organisation's [schedules](/concepts/schedules/) with
their cron expression, timezone, next run and workspace, and the task each
created last. From each row you can turn a schedule on or off, run it now,
edit it or delete it. **+ Schedule** creates one.

## Events

**Events** has three tabs:

- **Routing Rules**: the organisation's [routing rules](/concepts/routing-rules/),
  in the order they are checked, with their event type, conditions and action.
  Reorder them with the arrows, edit or remove them, add one with **+ Rule**, or
  try an event against them with **Test**.
- **Recent Events**: the latest events delivered to the organisation's tasks.
  Open one to see its description, URL, data and the task it reached.
- **Custom Schemas**: the organisation's custom event types. See
  [Custom events](/integrations/custom-events/).

## Workspaces

**Workspaces** lists the [workspaces](/concepts/workspaces/) the organisation's
[runners](/concepts/runners/) have registered, with their description, runner and
when they were last registered. Filter by runner, and **Clear** to remove the
listed runner's workspaces, or every runner's.

## Members and Keys

**Members** lists the organisation's members and their role. The owner adds
members by email here and removes them. **Keys** lists the organisation's API
keys and creates and deletes them. See
[Organisations, members and API keys](/concepts/orgs-members-keys/).

## Settings

**Settings** has two tabs:

- **Account**: the organisations you own, where you create new ones and delete
  them, and your linked [Atlassian](/integrations/jira/#link-your-atlassian-account)
  and [GitHub](/integrations/github/#link-your-github-account) accounts. Linking them lets
  [routing rules](/concepts/routing-rules/#public-rules) recognise your comments
  as a member's.
- **Organisation**: the URLs and settings that connect the organisation to
  other systems: the [MCP server URL](/integrations/mcp/) for MCP clients, the
  [custom events](/integrations/custom-events/) webhook URL, the
  [GitHub App installation](/integrations/github/#install-the-github-app), and
  the [Atlassian webhook](/integrations/jira/#connect-jira) URL and secret.

The server's version is at the bottom of the page.
