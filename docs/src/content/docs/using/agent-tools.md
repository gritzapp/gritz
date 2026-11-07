---
title: Agent tools
description: The gritz tools every agent gets (get_my_task, update_my_task, create_link, report and get_github_token), what gritz tells agents to do with them, and how to write instructions that use them.
sidebar:
  order: 3
---

gritz adds an MCP server named `gritz` to every agent it runs, alongside the MCP
servers in the task's [workspace](/concepts/workspaces/). Its tools let the
agent read and update its own task, and nothing else: they work with a token
that only grants access to that task (see
[Task tokens](/concepts/orgs-members-keys/#task-tokens)).

## What agents are told

On a task's first run, before your instructions, gritz tells the agent to:

- name the task with `update_my_task` if it has no name;
- create a link with `subscribe=true` for each resource it creates, such as a
  pull request, an issue or a comment, so replies reach the task even after it
  finishes, and `subscribe=false` for reference links it didn't create;
- prefer URLs a person can open over API URLs;
- use `report` for anything you should see, since its text replies aren't shown;
- answer questions or problems on the platform the latest instruction or event
  came from, such as the pull request, and say which task it is from;
- call `get_my_task` if it needs to check for updates mid-run.

Then it gives the agent the task: its instructions, events and links, so it
doesn't need a tool call to start. On later runs the agent gets only what is new.
See [Instructions and runs](/concepts/tasks/#instructions-and-runs).

## Tools

### `get_my_task`

Returns the task: its ID, name, status, workspace, [namespace](/concepts/namespaces/)
and URL, its links, and its instructions and events in order. Reports and status
changes aren't included.

### `update_my_task`

Renames the task. It takes `name`, the new name. Agents can't change anything
else about their task, including its auto-archive delay.

### `create_link`

Links the task to an external resource, and appears on the task's timeline and
in its sidebar.

| Parameter | |
| --- | --- |
| `url` | The resource's URL. |
| `relevance` | How the resource relates to the task, such as "PR implementing the fix". |
| `title` | Optional. A title to show for the link. |
| `subscribe` | Optional. `true` to have events about the resource delivered to the task. |

A subscribed link is how a task hears about review comments on its pull request,
or replies on its Jira issue. Which events wake it is up to the organisation's
[routing rules](/concepts/routing-rules/). See
[Events and links](/concepts/events-and-links/).

### `report`

Posts `message` to the task's timeline. This is how an agent tells you what it
did, what it found or what is blocking it: the agent's own replies only appear
in the task's log.

### `get_github_token`

Meant to return a short-lived GitHub token for the organisation's GitHub App
installation. Only tasks whose workspace has the `github_token`
[capability](/operators/workspaces-yaml/#capabilities) get this tool, and the
server doesn't issue these tokens yet, so the tool always fails.

Agents get GitHub access today from their workspace instead: a token under
`secrets:`, such as `GH_TOKEN`, which `gh`, git and the GitHub MCP server can
use. See [`secrets`](/operators/secrets/).

## Writing instructions that use them

The agent already uses these tools as above without being asked. Mention them
when you want something specific:

- **To get a summary where you'll see it**: "When you're done, report a summary
  of the changes and anything you couldn't do."
- **To keep a task listening**: "Open a draft pull request and link it with
  subscribe=true." The task finishes, and a review comment on the pull request
  wakes it if a routing rule matches.
- **To link context you gave it**: "Link the incident page as a reference, not
  subscribed."
- **To name it predictably**: "Name this task `deps: bump <package>`."

The agent's other tools, such as for GitHub or Jira, come from the MCP servers
and credentials in its workspace. Ask your operator what a workspace provides,
or see [`workspaces.yaml`](/operators/workspaces-yaml/).
