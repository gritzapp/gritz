# Events and links

Tasks don't end when their agent stops. An agent that opens a pull request links
it to its task, and a review comment on that pull request later wakes the task up
to address it.

![A sequence diagram: the agent creates a pull request on GitHub and a subscribed link to it, and its task completes. A reviewer comments "gritz: fix the failing test" on the pull request, GitHub's webhook restarts the agent, and the agent pushes a fix.](../../../assets/events.svg)

## Links

A link connects a task to an external resource: a pull request, an issue, a Jira
ticket, a dashboard. It has:

- a **URL**, ideally one a person can open;
- a **relevance**, saying how the resource relates to the task, such as "PR
  implementing the fix";
- an optional **title** to display;
- **subscribe**, whether events about the resource should reach the task.

Agents create links with the [`create_link`](/using/agent-tools/#create_link)
tool, and gritz tells every agent to create a subscribed link for each resource
it creates, such as a pull request, an issue or a comment. A task created by a
[routing rule](/concepts/routing-rules/) also gets a subscribed link to the URL of
the event that created it.

Links are listed in the task page's sidebar and appear on its timeline. They
can't be removed: a task stays subscribed until it is archived.

## Subscriptions

A link created with `subscribe=true` is a subscription. When an event arrives
about the same URL, gritz delivers it to every task subscribed to that URL in the
organisation, rather than starting a new task.

The URL has to match:

- **GitHub and Jira URLs** are matched by the issue, pull request or Jira issue
  they point at. A comment's URL, a pull request's `/files` page and the API URL
  of the same pull request all match a link to the pull request.
- **Any other URL** must match exactly, character for character. A different
  query string or a trailing slash is a different resource. See
  [The URL is the subscription key](/integrations/custom-events/#the-url-is-the-subscription-key).

Archived tasks aren't subscribed to anything. An event about a URL whose only
subscriber is archived is treated as if nobody were subscribed.

## How an event reaches a task

Events come from the [GitHub](/integrations/github/) and
[Jira](/integrations/jira/) integrations and from your own systems
through [custom events](/integrations/custom-events/). For each event, gritz:

1. **Finds a matching routing rule.** Each organisation's
   [routing rules](/concepts/routing-rules/) decide what kinds of events it acts
   on. The first rule that matches wins; an event that matches no rule does
   nothing, even for subscribed tasks.
2. **Looks for subscribed tasks** for the event's URL, in the matched rule's
   [namespace](/concepts/namespaces/).
3. **Delivers or creates.** If there are subscribed tasks, the event is added to
   each of them, and the rule decides whether it also wakes them. If there are
   none and the rule creates tasks, it creates one, subscribed to the event's
   URL, so the next event about the same resource goes to it.

A woken task that has finished starts a new run. A running task isn't
interrupted: it runs again as soon as its current run ends. Either way the agent
gets the event at the start of its next run. An event delivered without a wake
waits on the task's timeline until something else starts the task; the agent
also sees it if it calls [`get_my_task`](/using/agent-tools/#get_my_task).

## Where events show up

- **The task's timeline** shows each event delivered to the task, with its
  source, description, URL and text, next to the instructions and the agent's
  reports.
- **Events → Recent Events** in the [Web UI](/using/web-ui/#events) lists the
  organisation's latest delivered events, each linked to its task. Events that
  matched no rule, or reached no task, aren't stored, so they don't appear.
- **The test event form** on **Events → Routing Rules** shows what an event
  would do before you rely on it. See
  [Testing rules](/concepts/routing-rules/#testing-rules).