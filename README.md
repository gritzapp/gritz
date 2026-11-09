# GRITZ

Runs coding agents (Claude Code, Codex, Cursor, GitHub Copilot) inside remote sandboxes.

Documentation: https://docs.gritz.dev

## Features

- **Self-hosted runners** - Run agents on your own infrastructure inside Docker containers
- **Third-party integrations** - Interact with agents via GitHub and Jira events
- **MCP server** - Create and manage tasks from Claude Code, Cursor, or any MCP client

## Web UI

![Web UI](images/webui.png)

## Setup

```bash
gritz login
```

Opens your browser to log you in, and saves the organisation to `.gritz.yml`.
See https://docs.gritz.dev/using/cli/ for more.

## Local Development

```bash
mise run dev
```

Builds the Web UI and a Linux `gritz` binary, then starts PostgreSQL, a
`--no-auth` server and a runner with the `dummy` workspace from
`dev/workspaces.yml` in Docker (`docker-compose.dev.yml`, which reads a `.env`
file at the repo root). The Web UI is at http://localhost:6464/ui.

### Build

```bash
mise run build      # Build main + prebuilt binaries (linux amd64/arm64)
mise run generate   # Generate protobuf code
go build            # Build main binary only
```
