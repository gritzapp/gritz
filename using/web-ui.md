# Web UI

The Web UI is served by the gritz server under `/ui/`, for example
`https://gritz.dev/ui/`. It updates live: task statuses, timelines and logs
change as they happen, without reloading. The dot next to the organisation
selector shows whether the live connection is up.

The bar at the top links to every page, and holds the organisation selector, the
light and dark theme toggle, the **Settings** gear and **Logout**. Press Ctrl+K
(⌘K on a Mac) anywhere to jump to one of the recent tasks by name.

## Tasks

**Tasks** lists the organisation's tasks, newest first: each one's
[agent](/concepts/agents/) with the task's runner and workspace beneath it, its
name and ID, its [status](/concepts/tasks/#statuses) and when it was created.
On a narrow screen the agent shows only as its monogram. Click the agent to
open its page. A `command:` badge next to the status is an action the runner
hasn't carried out yet. Check one or more agents in the agent filter to list
only their tasks, or pick **All agents** for every task. Turn on **Show
archived** to include archived tasks, and archive or unarchive a task from its
row. The filters are remembered per organisation.

**+ Task** opens the page of a new task. Pick its [agent](/concepts/agents/);
the name is optional. The task runs with the agent's runner, workspace,
auto-archive delay, variables and features. Sending the first instruction creates the
task and starts it. An agent's **Run** button, on the **Agents** page or the
agent's page, opens it with that agent picked and its prompt in the instruction
box.

## The task page

The sidebar on the left holds the task's status, its agent, name, ID, runner and
workspace, its three views, its created and updated times, its variables, the
links it **Follows** with their [kind and claim](/concepts/agents/#link-kinds),
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
new line. An archived task has no box. A finished task starts again with it; a running task picks it up when
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

**Agents** lists the organisation's [agents](/concepts/agents/), each with its
runner and workspace, what it **Starts on** (its triggers and
[schedules](/concepts/schedules/), or **Manual only**), what it **Follows** (its
link kinds) and how **Recent** its newest task is. **Recent** links to the
[task list](#tasks) filtered to that agent. **List**, **Cards** and **Routing**
switch between the table, a card per agent with everything written out, and a
diagram of which events reach which agents and link types.

**+ Agent** creates an agent. Each row has a **Run** button, which opens a
[new task](#tasks) of the agent with its prompt as the first instruction, and a
**⋯** menu to **Edit** or **Delete** it; clicking the name also opens it.

An agent's page is its form, with **Run**, **Test event** and **⋯** at the top.
**⋯** holds **Delete** and **Clone**, which opens a new agent's form filled in
with a copy of this one, named `<name> (copy)`; the copy is created only when
you save it. The form has three sections:

- **Template**: name, runner, workspace, variables,
  [features](/concepts/agents/#the-template), prompt and auto-archive delay.
- **Follows**: the [link kinds](/concepts/agents/#link-kinds), each with a link
  type, a kind name, a claim, a description, and a switch per event type it
  delivers. A switched-on event type has its conditions, **Public** and
  **Wake**.
- **Starts on**: **Add trigger** and **Add schedule**.
  - Each [trigger](/concepts/agents/#triggers) has an event type, conditions, a
    link kind, claims to yield to, a prompt and **Public**. Triggers are checked
    from the top; the arrows reorder them.
  - Each schedule has a name, a cron expression, a timezone and a prompt, and
    an on/off switch on its header. A saved schedule shows its next run and its
    last run, with a link to the task it created.

The bar at the bottom saves the agent, or discards the changes.

**Test event**, on the **Agents** page, the **Events** page or an agent's page,
composes an event and shows what each agent would do with it, or fires it. See
[Testing with a test event](/concepts/agents/#testing-with-a-test-event).

## Events

**Events** has two tabs:

- **Recent**: the latest events delivered to the organisation's tasks. Open
  one to see its description, URL, body, details and the task it was delivered
  to, and whether it woke that task.
- **Custom types**: the organisation's custom event types. **+ Type** adds one.
  See [Custom events](/integrations/custom-events/).

**Test event** opens the [test event](/concepts/agents/#testing-with-a-test-event)
form.

## Workspaces

**Workspaces** lists the [workspaces](/concepts/workspaces/) the organisation's
[runners](/concepts/runners/) have registered, with their runner and
description, the [features](/operators/workspaces-yaml/#features) and
[variables](/operators/workspaces-yaml/#variables) they declare, the agents that
use them and when they were last registered. Filter by runner, and **Clear** to
remove the listed runner's workspaces, or every runner's. A workspace's page
lists its features and variables with their descriptions, and the agents that
use it and each of its features.

## Members and Keys

**Members** lists the organisation's members and their role. The owner adds
members by email here, and removes them from a member's **⋯** menu. **Keys**
lists the organisation's API keys. **+ Key** creates one and shows its token
once, and a key's **⋯** menu deletes it. See
[Organisations, members and API keys](/concepts/orgs-members-keys/).

## Settings

**Settings** has two tabs:

- **Account**: **My organisations**, the organisations you own, where you
  create new ones and delete them, and **Connected accounts**, your linked
  [GitHub](/integrations/github/#link-your-github-account) and
  [Atlassian](/integrations/jira/#link-your-atlassian-account) accounts. Linking them lets
  [agents](/concepts/agents/#members-and-public) recognise your comments
  as a member's.
- **Organisation**: the URLs and settings that connect the organisation to
  other systems: the [MCP server URL](/integrations/mcp/) for MCP clients, the
  [custom events](/integrations/custom-events/) webhook URL, the
  [GitHub App installation](/integrations/github/#install-the-github-app), and
  the [Atlassian webhook](/integrations/jira/#connect-jira) URL and secret.

The server's version is at the bottom of the page.