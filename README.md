# GRITZ

Runs coding agents (Claude Code, Codex, Cursor, GitHub Copilot) inside remote sandboxes.

## Features

- **Self-hosted runners** - Run agents on your own infrastructure inside Docker containers
- **Third-party integrations** - Interact with agents via GitHub and Jira events
- **MCP server** - Create and manage tasks from Claude Code, Cursor, or any MCP client

## Web UI

![Web UI](images/webui.png)

## Quick Start

Install `gritz` cli:

```bash
mise run install
```

Download the pre-built binaries (if needed):

```bash
gritz download
```

Create an API key in the Web UI (https://gritz.dev/ui/keys/new) and copy the returned `xat_…` value into `~/.config/gritz/config.yaml`:

```yaml
token: xat_...
```

Create a `workspaces.yaml` file (see examples below):

```bash
vim ~/.config/gritz/workspaces.yaml
```

Start the local runner:

```bash
gritz runner
```

Create and monitor tasks via the Web UI.

Open: https://gritz.dev/

## Workspace Examples

See [gritzapp/workspaces](https://github.com/gritzapp/workspaces) for workspace
configuration examples (Claude Code, Codex, Cursor, Copilot, MCP servers,
private repositories) and the source of the `gritz-workspace-*` images.

## Secrets

A task's driver log is shipped to the server and readable by anyone in the org
via `gritz logs` and the Web UI, so credentials that reach it are persisted well
beyond the sandbox's lifetime. Declare them in the workspace's `secrets:` map
and gritz keeps them out of the shipped copy:

```yaml
workspaces:
  pets-workshop:
    secrets:
      GH_TOKEN: ${sh:gh auth token}
    commands:
      - git clone https://x-access-token:${GH_TOKEN}@github.com/private/repo.git
```

- Each entry becomes an environment variable in the sandbox, exactly like a
  `container.environment` entry — values go through the same `${env:}`/`${sh:}`
  expansion. Move credentials from `environment:` to `secrets:`; nothing else
  about them changes.
- Every occurrence of a declared value is replaced by `[gritz:masked NAME]`
  (e.g. `[gritz:masked GH_TOKEN]`) in the log shipped to the server — including
  values echoed back by setup-command output, `set -x` traces or an `env` dump.
  The task's own API token is masked the same way, as `[gritz:masked token]`.
- Reference secrets from `commands:` by variable (`${GH_TOKEN}`). The config
  loader leaves those alone — it only expands `${namespace:value}` — so the
  sandbox shell expands them at run time and the command string the driver logs
  carries the name rather than the value.
- `/gritz/log` inside the sandbox and the container's stderr stay **raw**. They
  sit inside the boundary the secrets already live in, and full fidelity is what
  makes `gritz shell` post-mortems useful.

There is no detection: gritz masks exactly the values you declare. An undeclared
credential ships unmasked, and a secret that some other tool truncates before it
reaches the log (a long value cut short in a tool-call summary, say) arrives as a
fragment the mask cannot match.

## Docker Compose Runner

See [gritzapp/workspaces](https://github.com/gritzapp/workspaces/tree/main/runner) for running the runner as a Docker Compose service with a pull-through registry cache.

## Debugging

View a task's driver log — the agent CLI's output plus the driver's own
logging, streamed to the server as the task runs, so it stays readable after the
sandbox is gone and needs no Docker access:

```bash
gritz logs <taskid>     # print the whole transcript
gritz logs -f <taskid>  # follow it as the task runs
```

Set `verbose: true` on a workspace's agent to bypass the CLI output parser and
log every raw line. Useful when the parser is hiding details (errors,
intermediate output, tool-use payloads) you want to inspect via `gritz logs`.

```yaml
agent:
  type: claude
  verbose: true
```

Get a shell to a task container:

```bash
gritz shell <taskid>
```

List task containers:

```bash
gritz containers
```

## Local Development

```bash
# Start server and postgres locally
docker compose up -d

# Run the FE
cd webui
pnpm install
pnpm run dev
```

The local server runs with `--no-auth`, but the runner still requires an API key.
Create one in the local Web UI at http://localhost:5173/ui/keys/new, then start the runner:

```bash
gritz runner --server http://localhost:6464 -key <api-key>
```


### Build

```bash
mise run build      # Build main + prebuilt binaries (linux amd64/arm64)
mise run generate   # Generate protobuf code
go build            # Build main binary only
```

## Events

Agents can attach **links** to their tasks for external resources they create (PRs, Jira issues, etc.). Links created with `subscribe=true` act as subscriptions. When a new event occurs on the resource, the agent is automatically restarted to respond.

![Events](images/events.svg)

## Architecture

![Architecture](images/architecture.svg)


## Schema

![Schema](images/schema.svg)
