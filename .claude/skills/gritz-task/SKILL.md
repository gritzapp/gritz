---
name: gritz-task
description: Create gritz tasks using the MCP tools. Use when the user wants to create a task for the gritz system.
---

# Creating gritz Tasks

Use the `mcp__gritz__create_task` and `mcp__gritz__list_agents` MCP tools to create tasks.

A task belongs to an agent. The agent decides where the task runs (its workspace and runner) and what it gets (variables, features), so `create_task` takes an `agent_id` and no workspace or runner.

## Workflow

1. If the user hasn't said which agent to use, call `mcp__gritz__list_agents` and pick the one whose workspace and purpose fit the work. If none clearly fits, ask the user instead of guessing.
2. Create the task with `mcp__gritz__create_task`, passing `agent_id` and the `instruction`.
3. Show the user the task URL from the response.

## Guidelines

- **Agent**: an agent's `prompt` is only the first instruction of tasks its triggers and schedules create. A task you create starts with the instruction you give it, so any agent on the right workspace works.
- **Link kinds matter for follow-up**: a task is only woken by PR or issue comments if its agent has a link kind for that resource (see `mcp__gritz__get_agent`). Pick an agent with pull request and issue link kinds when the work will be reviewed on a PR.
- **Name**: Keep task names short and descriptive (under 60 chars).
- **Instruction**: Write clear, actionable instructions. Include context you **already have** from the current conversation (code you've read, file paths discussed, architecture context). Do **not** go researching or exploring the codebase to gather more context before creating the task — the agent can do that itself. The whole point of delegating to gritz is to offload work, not to front-load it.

## Task Types

- **Investigation tasks**: When the task is investigative (research, analysis, auditing), the deliverable should be a GitHub issue.
- **Implementation tasks**: When the task involves writing code, the deliverable is a PR.

## Container Environment

The agent runs inside a Docker container with a limited toolset. The available tools are defined in `mise.toml` at the repo root. Do **not** assume any CLI tools are available beyond what mise installs and standard Linux utilities.
