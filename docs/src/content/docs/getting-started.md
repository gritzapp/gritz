---
title: Getting started
description: Sign in to gritz, pick an organisation, create a task in the Web UI, watch it run, send a follow-up instruction and archive it.
---

This page walks through a first task on a gritz deployment such as
[gritz.dev](https://gritz.dev). It assumes a runner is already connected to your
organisation. If none is, ask whoever runs gritz for you, or set one up yourself
with [Runner](/operators/runner/).

## 1. Sign in

Open the deployment, for example `https://gritz.dev`, and sign in. The first time
you sign in, gritz creates your user and a personal organisation named after you,
such as "Ada Lovelace's Org".

## 2. Pick an organisation

Everything in gritz (tasks, runners, routing rules, keys) belongs to an
[organisation](/concepts/orgs-members-keys/). The selector at the top right of
the Web UI switches between the organisations you are a member of, and the
current one is kept in the page's `?org=` parameter, so links you share open in
the right one.

To work in a team's organisation, ask its owner to add you on the **Members**
page. They add you by email, so sign in once first.

## 3. Check for a runner

Open **Workspaces**. It lists every [workspace](/concepts/workspaces/) the
organisation's [runners](/concepts/runners/) offer, with the runner that offers
it. If the list is empty, no runner has connected to this organisation yet.

## 4. Create a task

Open **Tasks** and click **+ Task**:

- **Name** is optional. The agent names the task itself if you leave it blank.
- **Runner** and **Workspace** decide where the task runs and what it gets: the
  image, repositories, agent and tools. Pick a runner first; the workspace list
  shows only that runner's workspaces.
- **Namespace** is optional. Leave it empty unless your routing rules use
  [namespaces](/concepts/namespaces/).
- **Auto-archive** archives the task some time after it finishes. Leave it on
  **Never** for now.

**Create Task** opens the task's page. The task is a draft: nothing runs yet.

## 5. Send the first instruction

Type what you want the agent to do in the box at the bottom of the page and
press Enter (Shift+Enter adds a new line):

> Add a `--json` flag to the `list` command and open a pull request.

The task moves to **pending** while the runner prepares its sandbox, then to
**running** when the agent starts.

## 6. Watch it run

The task page has three views, in the sidebar on the left:

- **Timeline**: your instructions, the agent's reports, the links it creates
  (such as the pull request it opened), events from GitHub, Jira and other
  systems, and status changes, in order.
- **Logs**: the sandbox's log as it runs: setup commands, the agent's messages
  and tool calls.
- **Shell**: a terminal in the task's sandbox, once the task has finished.

When the agent is done, the task moves to **completed**, or to **failed** if a
setup command, the agent or the sandbox failed. The [task lifecycle](/concepts/tasks/#statuses) lists
every status.

## 7. Follow up

Send another instruction from the same box: "Also update the README". A finished
task starts again, in the same sandbox, and the agent continues the same
session, so it remembers what it did. If the task is still running, the
instruction waits and the task runs again as soon as the current run ends.

Tasks also come back by themselves. When the agent opens a pull request, it
links it to the task with a subscription, and a review comment on that pull
request wakes the task up. See [Events and links](/concepts/events-and-links/).

## 8. Archive it

When you are done with the task, click **Archive** in the sidebar. The runner
destroys the task's sandbox and the task leaves the task list; turn on **Show
archived** to see it again. A task can only be archived once it has finished.

## Next steps

- [Tasks](/concepts/tasks/) covers statuses, restarts, cancelling and
  auto-archive.
- [Routing rules](/concepts/routing-rules/) create and wake tasks from GitHub,
  Jira and [custom events](/integrations/custom-events/).
- [Schedules](/concepts/schedules/) create tasks on a cron schedule.
- [The CLI](/using/cli/) creates and follows tasks from a terminal.
