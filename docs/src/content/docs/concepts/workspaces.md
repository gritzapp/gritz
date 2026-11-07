---
title: Workspaces
description: What a workspace gives a task (the image, repositories, agent and tools), how to pick one, and who defines them.
sidebar:
  order: 2
---

A workspace is the recipe for a task's sandbox. When you create a task you pick
one, and it decides everything the agent starts with:

- **The image** the sandbox runs, with the agent's CLI and the tools the work
  needs.
- **Setup commands**, such as cloning the repositories the task works on. They
  run once, before the task's first run.
- **The agent**: Claude Code, Codex, Cursor or Copilot, its model, the directory
  it starts in, and any standing instructions for that repository.
- **MCP servers** the agent can use, in addition to the
  [gritz tools](/using/agent-tools/) every agent gets.
- **Credentials**, such as a GitHub or model provider token, passed in as
  secrets.

## Picking a workspace

Workspaces belong to [runners](/concepts/runners/): each runner offers its own
set, by name and with a short description. **Workspaces** in the
[Web UI](/using/web-ui/#workspaces) lists them all, and the new task form shows
the workspaces of the runner you pick. Two runners can offer a workspace with
the same name and a different setup.

Pick the workspace for the repository or system the task is about. A task keeps
its workspace for its whole life; to run the same work in another workspace,
create another task.

## Who defines them

Workspaces aren't created in the Web UI. The operator of each runner defines its
workspaces in a `workspaces.yaml` file, and the runner registers their names and
descriptions with the server when it starts. To add a workspace or change one,
ask whoever runs the runner, or see [`workspaces.yaml`](/operators/workspaces-yaml/)
if that is you.

A change to a workspace applies to tasks created after the runner restarts with
it; existing tasks keep the setup they started with. See
[Changing a workspace](/operators/workspaces-yaml/#changing-a-workspace).
