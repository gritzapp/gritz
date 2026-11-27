---
title: Web UI
description: A tour of the gritz Web UI, the task list and task page, agents and their schedules, events, workspaces, members, keys and settings.
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

**Tasks** lists the organisation's tasks, newest first, with their
[agent](/concepts/agents/) and [status](/concepts/tasks/#statuses). Click the
agent to open its page. A `command:` badge next to the
status is an action the runner hasn't carried out yet. Pick an agent in the
agent filter to list only its tasks, or **All agents** for every task. Turn on
**Show archived** to include archived tasks, and archive or unarchive a task
from its row. The filters are remembered per organisation.

**+ Task** opens the page of a new task. Pick its [agent](/concepts/agents/);
the name is optional. The task runs with the agent's runner, workspace,
auto-archive delay, variables and features. Sending the first instruction creates the
task and starts it. An agent's run button on the **Agents** page opens it with
that agent picked and its prompt in the instruction box.

## The task page

The sidebar on the left holds the task's status and name, its three views, its
details (agent, runner, workspace, created and updated times), its
variables, its links with their [kind and claim](/concepts/agents/#link-kinds),
and its actions:

- **Archive** or **Unarchive**. When an auto-archive delay is counting down, the
  time left shows next to it.
- **Cancel**, while the task is pending or running.
- **Restart**, once it is running or finished.
- **Reset sandbox**, once it is finished, to delete its sandbox so the next run
  starts in a new one. See [Resetting the sandbox](/concepts/tasks/#resetting-the-sandbox).
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

## Agents

**Agents** lists the organisation's [agents](/concepts/agents/) with their
runner and workspace, their triggers, the next run of their
[schedules](/concepts/schedules/) and their link kinds. **+ Agent** creates
one, and each row has buttons to run, edit or delete it. Run opens a
[new task](#tasks) of the agent, with its prompt as the first instruction. The
agent form has four sections:

- **Template**: name, runner, workspace, variables,
  [features](/concepts/agents/#the-template), prompt and auto-archive delay.
- **Triggers**: each with an event type, conditions, a link kind, claims to
  yield to, and **Public**. The arrows reorder them.
- **Schedules**: each with a name, a cron expression, a timezone and an on/off
  switch. A saved schedule shows its next run and its last run, with a link to
  the task it created.
- **Link kinds**: each with a link type, a kind name, a claim, the events it
  delivers with conditions on each, and **Public**.

**Test event** composes an event and shows what each agent would do with it, or
fires it. See [Testing with a test event](/concepts/agents/#testing-with-a-test-event).

## Events

**Events** has two tabs:

- **Recent Events**: the latest events delivered to the organisation's tasks.
  Open one to see its description, URL, data and the task it reached. **Test**
  opens the [test event](/concepts/agents/#testing-with-a-test-event) form.
- **Custom Schemas**: the organisation's custom event types. See
  [Custom events](/integrations/custom-events/).

## Workspaces

**Workspaces** lists the [workspaces](/concepts/workspaces/) the organisation's
[runners](/concepts/runners/) have registered, with their description, how many
[features](/operators/workspaces-yaml/#features) they declare, their runner and
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
  [agents](/concepts/agents/#members-and-public) recognise your comments
  as a member's.
- **Organisation**: the URLs and settings that connect the organisation to
  other systems: the [MCP server URL](/integrations/mcp/) for MCP clients, the
  [custom events](/integrations/custom-events/) webhook URL, the
  [GitHub App installation](/integrations/github/#install-the-github-app), and
  the [Atlassian webhook](/integrations/jira/#connect-jira) URL and secret.

The server's version is at the bottom of the page.
