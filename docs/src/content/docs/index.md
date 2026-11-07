---
title: What is gritz?
description: gritz runs coding agents such as Claude Code and Codex in sandboxes, driven by tasks and events.
---

gritz is an orchestrator for coding agents. It runs agents such as Claude Code,
Codex, Cursor and Copilot in parallel, each in its own sandbox, and keeps track of
their work in one place.

- **Tasks** are the unit of work. A task gives an agent its instructions and runs
  it in a sandbox built from a workspace: the image, repositories, agent and MCP
  servers it needs.
- **Runners** start the sandboxes, on Docker or Lambda MicroVMs, and stream each
  agent's log back to the gritz server, where the Web UI shows it live.
- **Events** keep a task going after its first run. When an agent opens a pull
  request or comments on an issue, it links the resource to its task, and a review
  comment on that pull request wakes the task up with the new event.

Agents and users drive gritz the same way: from the Web UI, the `gritz` CLI, or
any MCP client.
