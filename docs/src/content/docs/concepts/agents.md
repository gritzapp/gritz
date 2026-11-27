---
title: Agents
description: How an agent defines a kind of task, how its triggers create tasks from events, how its link kinds decide which events reach its tasks, how claims stop two agents working on the same resource, and testing agents with a test event.
sidebar:
  order: 5
---

An agent is one kind of task in an organisation, such as "implement a Jira
issue" or "answer `@gritz` on pull requests". Every task belongs to exactly one
agent, and the agent decides four things about its tasks:

- **The template** every task of the agent starts from: where it runs and with
  what.
- **Triggers**, which create a new task of the agent when a matching event
  arrives: a [GitHub](/integrations/github/) comment, a
  [Jira](/integrations/jira/) label, a
  [custom event](/integrations/custom-events/).
- **[Schedules](/concepts/schedules/)**, which create a new task of the agent on
  a cron schedule.
- **Link kinds**, the only links its tasks may subscribe to, and the events each
  one delivers to the task.

The coding agent that runs in a task's sandbox, such as Claude Code or Codex, is
set by the task's [workspace](/concepts/workspaces/), or by one of the
[features](#the-template) the agent picks, not here.

Agents are on the **Agents** page of the [Web UI](/using/web-ui/#agents).
**+ Agent** creates one, clicking an agent's name opens it for editing, and its
**Run** button opens a new task of the agent with its prompt filled in.
**Clone**, in the **⋯** menu of an agent's page, opens a new agent's form filled
in with a copy of the agent, named `<name> (copy)`; nothing is created until
you save it. MCP clients manage agents with
[`put_agent`](/integrations/mcp/#agents) and the other agent tools.

An organisation needs an agent before it can create any task, by hand, from a
[schedule](/concepts/schedules/) or from an event. An agent with no triggers and
no link kinds is just a template for tasks you create yourself.

## The template

- **Name**: unique in the organisation.
- **Runner** and **Workspace**: where its tasks run. Pick a runner first; the
  workspace list shows only that runner's workspaces.
- **Variables**: one field per [variable](/operators/workspaces-yaml/#variables)
  the workspace declares. They are fixed values: every task gets the same ones,
  and nothing from an event is copied into them. Leave an optional variable
  empty to use the workspace's default when each task starts.
- **Features**: a switch for each
  [feature](/operators/workspaces-yaml/#features) the workspace declares, with
  its description. Each feature that is on adds its environment, secrets, MCP
  servers and setup commands to the workspace's, and can replace its coding
  agent. Their order matters: where two set the same thing, the later one wins,
  and their setup commands run in that order. The order is the one you turn them
  on in, and each feature that is on shows its position. To move a feature to
  the end, turn it off and on again. Changing the runner or workspace clears
  the features, as it clears the variables.
- **Prompt**: the first instruction of the tasks its triggers and
  [schedules](/concepts/schedules/) create. A trigger or schedule can add
  [its own prompt](#the-triggers-prompt) as a second instruction. Left blank,
  the tasks start with only the trigger's or schedule's prompt, or with no
  instruction when that is blank too. A triggered task's first event is the one
  that created it, so the agent gets the event's text either way. Tasks you
  create by hand get their own instructions instead.
- **Auto-archive**: the [auto-archive](/concepts/tasks/#auto-archive) delay of
  its tasks.

The workspace, variables and features are checked when you save the agent, and
it isn't saved if the workspace isn't registered on the runner, doesn't accept
the variables, or doesn't declare one of the features. A saved feature the
workspace no longer declares is flagged in the editor: turn it off to save the
agent.

A task copies its runner, workspace, auto-archive delay, variables and features
from the agent when it is created. Editing the agent changes the tasks it creates from
then on; tasks it already created keep theirs.

The runner checks a task's variables again when it starts the task. If the
workspace's `variables:` changed after you saved the agent, and no longer
accepts its values (a new required variable, or one the agent sets that the
workspace no longer declares), its triggers still create tasks, and each task
fails with the reason on its timeline. The same goes for a feature the
workspace no longer declares, and creating a task by hand fails until the agent
is fixed. Edit the agent to fix it.

## Triggers

A trigger creates a task of its agent when an event matches it. It never wakes
an existing task: that is what [link kinds](#link-kinds) do.

### Matching

A trigger matches an event by:

- **Event type**: the event's source and type, such as a GitHub pull request
  review comment, a Jira issue comment, or one of the organisation's custom
  types.
- **Conditions**, each an attribute, an operator and a value, optionally
  negated. The attributes on offer depend on the event type. Every type has `body`, the event's text, and
  `url`, the event's own URL (a comment's URL rather than its issue's); built-in
  types add others, such as the users mentioned in a comment, an issue's new
  label or its new assignee. See [Event types](/reference/event-types/) for every
  built-in type and its attributes.

| Operator | Holds when the attribute's value |
| --- | --- |
| `equals` | is exactly the value |
| `prefix` | starts with the value |
| `contains` | contains the value anywhere |
| `glob` | matches the value as a pattern, such as `internal/store/**` or `release/*` |

Each operator also has a negated form, offered in the operator list as **does
not equal**, **does not start with**, **does not contain** and **does not match
glob**. A negated condition holds exactly when the same condition without
negation doesn't, so `label` does not equal `wontfix` holds for an issue none of
whose labels is `wontfix`.

A `glob` pattern must match the whole value:

| Pattern | Matches |
| --- | --- |
| `*` | any run of characters except `/` |
| `**` | as a whole path segment, any number of segments, including none |
| `?` | any one character except `/` |
| `[abc]`, `[a-z]`, `[^a]` | one character from the class |
| `{a,b}` | either alternative |
| `\x` | a literal `x` |

So `**/*.sql` matches every `.sql` file at any depth and `{go.mod,go.sum}`
matches either file. Because `*` stops at `/`, use `prefix` or `contains` for
URLs and text. An agent with a malformed pattern can't be saved.

Comparisons are exact and case-sensitive. An attribute can have several values,
such as the labels on an issue, and a condition holds when any of them
satisfies it; a negated condition holds when none of them does. A condition on
an attribute the event doesn't have never holds, and a negated one always
does.

A trigger matches when every condition holds. A trigger with no conditions
matches every event of its type.

### One task per agent

An agent's triggers are checked from the top of its list down, and the first
one that matches is the only one that acts: an event creates at most one task
per agent. Put narrow triggers above broad ones. The arrows on each trigger
change its position.

Agents don't affect each other. Every agent whose trigger matches creates its
own task, so a reviewer agent and an implementer agent can both act on the same
event. To have one agent stay out of the way of another's tasks, use
[claims](#claims).

### The trigger's link

A trigger can name one of its agent's link kinds as its **Link**. The task it
creates then gets a subscribed link of that kind to the event's resource, so
the next events about the same pull request or issue reach that task, if the
kind lists them. Only kinds whose link type the event type can name are offered.
An event whose URL isn't a resource of the kind's type, such as a check run
outside GitHub Actions for a workflow run kind, still creates the task, without
the link.

Without a link, the created task isn't subscribed to anything unless its agent
creates links itself.

### The trigger's prompt

A trigger can have its own **Prompt**, an instruction for the tasks it creates
only. The task gets it after the agent's prompt, as a second instruction, or as
its only one when the agent has none. Use it when an agent's triggers need
different instructions, such as "Review the pull request" on one and "Answer
the comment" on another, while the agent's prompt holds what they share.

## Link kinds

A link kind is a kind of subscribed link the agent's tasks may create. An agent
creates links with the [`create_link`](/using/agent-tools/#create_link) tool,
and every link needs one of its agent's kinds; gritz tells it the kinds its
agent has, with their descriptions. An agent with no link kinds can't create
links. See [Events and links](/concepts/events-and-links/).

Each link kind has:

- **Link type**: the kind of resource its links name, such as a GitHub pull
  request or a Jira issue. See [Link types](/reference/event-types/#link-types).
- **Kind**: its name, which agents pass to `create_link`. Defaults to the link
  type's name, such as `github.pr`; give one only when the agent has two kinds
  of the same type, such as `owned-pr` for the pull requests its tasks open and
  `github.pr` for the ones they only watch.
- **Claim**: an optional name the links of this kind hold. See [Claims](#claims).
- **Description**: optional free text saying what the kind is for and when to
  create a link of it, such as "Pull requests the task opens". gritz shows it to
  the agent's tasks beside the kind, so they pick the right one.
- **Events**: the event types a link of this kind delivers, each with optional
  conditions on the event's attributes, as a [trigger's](#triggers). Only event
  types that can name the link type are offered. A kind with no events checked
  delivers nothing; its links still hold its claim. Each event type also has:
  - **Wake**: delivering one of its events wakes the task. On for a new event
    type.
  - **Public**: also deliver its events from people who aren't members of the
    organisation. See [Members and public](#members-and-public).

An event on a subscribed link's resource is delivered to the task when the
link's kind lists the event's type and the event meets every condition on that
type. When that event type has **Wake**, delivery wakes the task: a finished
task starts a new run, and a running one runs again when its current run ends.
Without it, the event is only added to the task, which is left as it is: the
agent sees it the next time something else wakes the task, such as an
instruction or an event that wakes. Use that for events worth knowing about but
not worth a run, such as successful checks.
A task with several links to the same resource gets each event once. Delivery
doesn't depend on any trigger: an event can be delivered to one task and create
another through a trigger, of the same agent or another one.

Which events a link receives is read from its kind on the agent each time an
event arrives, so editing a kind's events or their conditions applies to
existing links too.
Removing a kind stops delivery to the links created with it.

## Claims

A claim is a name, such as `pr-owner`, that a link kind gives its links. A task
holds the claim on its link's resource while the task is unarchived. A trigger's
**Yield to claims** lists claims: the trigger creates no task while any
unarchived task in the organisation holds one of them on the event's resource.
The event isn't passed to the holder by the trigger; the holder gets it only if
its own link kind lists the event type.

Claims are free-form names shared across the organisation: a trigger can yield
to a claim held by another agent's tasks. Nothing checks that a claim you yield
to is given by some link kind, so a misspelled name yields to nothing. A test
event shows which triggers yield.

A link keeps the claim it was created with: changing a kind's claim only
applies to links created after the change. A task holds its claims until it is
archived.

For example, an implementer agent's tasks link the pull requests they open with
an `owned-pr` kind that has the claim `pr-owner`. A responder agent answers
`@gritz` mentions on pull requests, with a trigger for
**GitHub: PR Comment** that has the condition `mention equals gritz`, yields
to `pr-owner`, and links the pull request. A mention on a pull request an
implementer task opened creates no responder task, and reaches the implementer
task if its `owned-pr` kind lists comments. A mention anywhere else creates a
responder task. For the next mention on that pull request to wake the responder
task rather than create another, give the responder's link kind a claim too,
such as `responding`, and add it to the trigger's yield list.

## Members and public

By default an agent only acts on events from members of the organisation: a
GitHub or Jira event counts as a member's when its author has linked that
account to a gritz user in the organisation.

A **Public** trigger also creates tasks for events from anyone else, and a link
kind delivers them for each event type it marks **Public**, as long as the
event belongs to the organisation:

- a GitHub event from a repository where the organisation installed the gritz
  GitHub App;
- a Jira event sent to the organisation's Atlassian webhook;
- a custom event sent with one of the organisation's API keys.

Make a trigger or a link kind's event type public to let people outside the
organisation, such as outside contributors or a team without gritz accounts,
reach it. A link kind can deliver failed checks from anyone, say, while
keeping comments to members. Custom events have no author, so triggers for
custom types must be public: gritz refuses to save an agent with one that
isn't, and a link kind only delivers custom events of a type it marks public. See
[Why custom event triggers must be public](/integrations/custom-events/#why-custom-event-triggers-must-be-public).

## Testing with a test event

**Test event** on the **Agents** page opens a form to compose an event: its
type, an optional description, its attributes such as `body` and `url`, and
details.

- **Dry run** shows, for each agent, which trigger would match the event and
  whether it would create a task or yield, and to which tasks, and which tasks
  the event would be delivered to, through which link kind, and whether it
  would wake each one. It changes nothing.
- **Fire** routes the event for real, as if it had arrived from its source: it
  delivers the event to tasks, waking them where it would, and creates tasks.
  It doesn't post anything back to GitHub or Jira.

Both run the event only for the organisation you are in, and both reject an
event that doesn't fit its type: the URL must be an absolute `http` or `https`
URL, and every attribute must be one the type declares. An event without a
description gets its type's label. A GitHub or Jira event runs as yours, so
member-only triggers and link kinds admit it. A
[custom event](/integrations/custom-events/) is routed like one posted to the
webhook: it has no actor, so only public triggers and the public event types
of link kinds admit it.

## Deleting an agent

An agent can't be deleted while it has an unarchived task: archive its tasks
first. Deleting it also deletes its [schedules](/concepts/schedules/). Its
archived tasks keep their history.
