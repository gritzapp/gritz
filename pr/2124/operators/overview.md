# Operators overview

A gritz deployment has one server and any number of runners. Each runner starts
a sandbox per task, and a driver inside each sandbox runs the agent.

![gritz architecture: the Web UI, MCP clients and GitHub and Jira webhooks talk to the gritz server, which stores everything in PostgreSQL. The runner fetches tasks from the server and manages sandboxes through its backend. In each sandbox the driver runs the agent and its gritz MCP server, and both connect to the server directly with the task token.](../../../assets/architecture.svg)

## Components

**Server** (`gritz server`) is the control plane. It serves the API, the Web UI,
the user-facing MCP endpoint and the webhooks for GitHub, Jira and custom events,
and it stores tasks, events and logs in PostgreSQL. See [Server](/operators/server/).

**Runner** (`gritz runner`) runs tasks. It authenticates to the server with an API
key, registers the workspaces in its [`workspaces.yaml`](/operators/workspaces-yaml/),
and picks up the tasks that users create for it. For each task it creates a
sandbox through its [backend](/operators/backends/), starts the task's runs, and
destroys the sandbox when the task is archived. See [Runner](/operators/runner/).

**Sandbox** is where a task's agent runs: a Docker container named
`gritz-<task-id>`, or an AWS Lambda MicroVM. A task keeps its sandbox from its
first run until it is archived, so files the agent leaves behind are still there
when an event wakes the task up.

**Driver** (`gritz driver`) is the sandbox's main process. The runner copies the
gritz binary into the sandbox and starts each run over the driver protocol. The
driver runs the workspace's setup commands, then the agent CLI (Claude Code,
Codex, Cursor or Copilot), and ships the run's log to the server as it goes.

**Agent MCP server** (`gritz tool agent-mcp`) is added to every agent's MCP
servers. It gives the agent the tools to read and update its own task, link
resources to it and report progress.

## Who connects to whom

| From | To | How |
| --- | --- | --- |
| Browsers, the `gritz` CLI, MCP clients | Server | HTTPS: an SSO session, an API key or an OAuth token |
| GitHub, Jira, other systems | Server | Webhooks |
| Server | PostgreSQL | `--db` connection string |
| Runner | Server | HTTPS with an API key: it holds a notification stream open for new work, and polls as a fallback |
| Runner | Driver | Through the backend: a `docker exec` tunnel to `/gritz/driver.sock` for Docker, AWS's managed proxy for Lambda MicroVMs |
| Driver and agent MCP server | Server | HTTPS with the task's own token |

Two consequences for networking:

- The server never connects to a runner, so a runner can sit behind NAT or a
  firewall as long as it can reach the server.
- Sandboxes connect to the server themselves, at the runner's server URL: its
  organisation's `server`, or `--server` with `--key`. That URL has to resolve and be reachable from inside the
  sandbox, not just from the runner's host.

## Versions

The server, runner and driver ship together, as one `gritz` binary and one
release. The runner puts its own binary into each sandbox, so the driver always
matches the runner. Upgrade the server and the runners to the same release; see
[Deployment](/operators/deployment/#upgrades).

Animated walkthroughs of how these parts interact, such as creating a task, are
on the [diagrams site](/diagrams/).

## Data model

The server keeps its state in PostgreSQL. Everything belongs to an **org**
(`orgs`, with `users` joined through `org_members`). **Tasks** (`tasks`) carry the
workspace and runner they run on, and their status. **Links** (`task_links`)
attach external resources to a task, and a subscribed link routes **events**
(`events`) to it. **Workspaces** (`workspaces`) are what runners register from
their `workspaces.yaml`, and **API keys** (`keys`) authenticate runners and
clients.

![gritz database schema: orgs, users and org_members; tasks with their task_links and events; workspaces and keys, each belonging to an org; and pending_integrations.](../../../assets/schema.svg)