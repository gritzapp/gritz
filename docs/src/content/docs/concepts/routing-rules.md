---
title: Routing rules
description: How an organisation's routing rules match events by type and conditions, why the first match wins, waking versus creating tasks, public rules, and testing rules with a test event.
sidebar:
  order: 5
---

Routing rules decide what an organisation does with incoming events:
[GitHub](/integrations/github/) comments, [Jira](/integrations/jira/) updates
and [custom events](/integrations/custom-events/). Each
rule matches a kind of event and says what to do with it: wake the tasks
subscribed to the event's resource, create a new task, or both.

An organisation with no rules acts on no events. Even a task with a
[subscribed link](/concepts/events-and-links/#subscriptions) is only woken by an
event that some rule matches.

Rules are on **Events → Routing Rules** in the [Web UI](/using/web-ui/#events).
**+ Rule** adds one, and the arrows on each row change its position.

## Matching

A rule matches an event by:

- **Event type**: the event's source and type, such as a GitHub pull request
  review comment, a Jira issue comment, or one of the organisation's custom
  types.
- **Conditions**, each an attribute, an operator and a value. The attributes on
  offer depend on the event type. Every type has `body`, the event's text, and
  `url`, the resource it is about; built-in types add others, such as the users
  mentioned in a comment, an issue's new label or its new assignee. See
  [Event types](/reference/event-types/) for every built-in type and its
  attributes.

| Operator | Holds when the attribute's value |
| --- | --- |
| `equals` | is exactly the value |
| `prefix` | starts with the value |
| `contains` | contains the value anywhere |

Comparisons are exact and case-sensitive. An attribute can have several values,
such as the labels on an issue, and a condition holds when any of them
satisfies it. A condition on an attribute the event doesn't have never holds.

A rule matches when every condition holds. A rule with no conditions matches
every event of its type.

### The first match wins

Rules are checked from the top of the list down, and the first one that matches
is the only one that acts. Put narrow rules above broad ones: a rule for
`body contains "gritz:"` placed under a rule with no conditions for the same
type never matches.

Each organisation's rules are checked on their own. If an event concerns two
organisations, as when its author is a member of both, each organisation's first
matching rule acts.

## What a rule does

Once a rule matches, gritz looks for tasks subscribed to the event's URL, in the
rule's [namespace](/concepts/namespaces/):

- **Subscribed tasks found**: the event is added to each of them. With **Wake up
  linked tasks** on, they also start again to handle it. With it off, the event
  waits on their timelines until they next run.
- **No subscribed task, and Create a task on**: gritz creates a task on the
  rule's runner and workspace, subscribed to the event's URL. The next event
  about the same resource wakes that task instead of creating another.
- **No subscribed task, and Create a task off**: nothing happens.

So a rule with both on starts one task per resource and keeps it up to date, and
a rule with only **Wake up linked tasks** on acts only on resources a task has
already linked.

### Creating tasks

A rule that creates tasks sets their:

- **Runner** and **Workspace**.
- **Prompt**: the created task's instruction. Left blank, the instruction is a
  fixed one that only says a routing rule created the task in response to the
  event's source and type. The task's first event
  is the one that created it, so the agent gets the event's text either way.
- **Auto-archive**: the created task's [auto-archive](/concepts/tasks/#auto-archive)
  delay.
- **Namespace**: the rule's namespace.

## Public rules

By default a rule only acts on events from members of the organisation: a GitHub
or Jira event counts as a member's when its author has linked that account to a
gritz user in the organisation.

A **Public** rule also acts on events from anyone else, as long as the event
belongs to the organisation:

- a GitHub event from a repository where the organisation installed the gritz
  GitHub App;
- a Jira event sent to the organisation's Atlassian webhook;
- a custom event sent with one of the organisation's API keys.

Make a rule public to let people outside the organisation, such as outside
contributors or a team without gritz accounts, trigger it. Custom events have no
author, so rules for custom types must be public: gritz refuses to save one that
isn't. See [Why custom event rules must be public](/integrations/custom-events/#why-custom-event-rules-must-be-public).

## Testing rules

**Test** on the Routing Rules tab opens a form to compose an event: its type,
an optional description, its attributes such as `body` and `url`, and details.

- **Dry run** shows which rule would match the event and whether that rule would
  wake tasks or create one. It changes nothing. It reports the rule's settings
  only: it doesn't check whether a task is subscribed to the URL.
- **Fire** routes the event for real, as if it had arrived from its source: it
  wakes or creates tasks and stores the event on them. It doesn't post anything
  back to GitHub or Jira.

Both run the event as yours, so your organisation's member-only rules can match
it, and only for the organisation you are in.
