---
title: CLI
description: Every gritz subcommand and flag, with its environment variable and default.
sidebar:
  order: 3
---

The `gritz` binary holds the CLI, the server, the runner and the tools that run
inside sandboxes. This page lists every subcommand and flag;
[Using the CLI](/using/cli/) shows how to use the everyday ones.

```
gritz [--help] [--version] <command> [flags] [args]
```

`--help` (`-h`) works on every command, and `--version` (`-v`) prints the
version, as `gritz version` does. Where a flag has an environment variable, the
flag wins when both are set.

## Authentication

`gritz task`, `gritz logs`, `gritz shell`, `gritz mcp`, `gritz notify` and
`gritz runner` take these flags, which pick the organisation, API key and server:

| Flag | Environment | |
| --- | --- | --- |
| `--org` | `GRITZ_ORG` | The organisation's name in the `.gritz.yml` files. Defaults to their `org`. |
| `--key`, `-k` | `GRITZ_API_KEY` | An API key to use instead of the `.gritz.yml` files and `--org`. |
| `--server`, `-s` | `GRITZ_SERVER` | The server URL. Defaults to the organisation's `server`, otherwise `https://gritz.dev`. |

Without `--key`, a command merges `.gritz.yml` in the config directory with every
`.gritz.yml` from the working directory up to `/`, the nearest winning, and uses
the organisation it selects. See [Configuration](/using/cli/#configuration).

The config directory is `GRITZ_CONFIG_DIR` if set, otherwise `gritz` under the
user config directory (`~/.config/gritz` on Linux).

### `gritz login`

Asks the server which organisation an API key belongs to, and saves the key
under a name in `.gritz.yml` in the config directory. Logging in again with a new
key for the same organisation and server replaces the key. The server is always
saved with the organisation. If the file has no `org`, the name becomes it.

| Flag | Environment | Default | |
| --- | --- | --- | --- |
| `--key`, `-k` | `GRITZ_API_KEY` | | The API key. Required. |
| `--name` | | the organisation's name, in lowercase with dashes | The name to save it under. |
| `--server`, `-s` | `GRITZ_SERVER` | the top-level `server` of the nearest `.gritz.yml` that sets one, otherwise `https://gritz.dev` | The server URL. |

## Tasks

### `gritz task create`

Creates a task and starts it, then prints it as JSON. It also takes the [authentication](#authentication) flags.

| Flag | Environment | Default | |
| --- | --- | --- | --- |
| `--runner`, `-r` | | | The runner to run on. Required. |
| `--workspace`, `-w` | | `default` | The workspace. |
| `--instruction`, `-i` | | | An instruction. Repeat it for several. |
| `--name`, `-n` | | | The task's name. |
| `--auto-archive` | | `0` | The [auto-archive](/concepts/tasks/#auto-archive) delay, such as `24h`. `0` is never; a negative value archives the task as soon as it finishes. |

### `gritz task list`

Prints the organisation's 50 newest unarchived tasks as JSON. It takes only the
[authentication](#authentication) flags.

### `gritz task update <task-id>`

Changes a task. It needs at least one of `--add-instruction`, `--start`,
`--name` or `--auto-archive`. It also takes the [authentication](#authentication) flags.

| Flag | Environment | Default | |
| --- | --- | --- | --- |
| `--add-instruction`, `-i` | | | Add an instruction. Repeat it for several. |
| `--start`, `-r` | | | Start the task. A running task isn't interrupted: it runs again when its current run ends. |
| `--name`, `-n` | | | Rename the task. |
| `--auto-archive` | | | Set the auto-archive delay. `0` is never; a negative value archives the task as soon as it finishes. Left out, the task keeps the delay it has. |

### `gritz logs <task-id>`

Prints a task's log. It also takes the [authentication](#authentication) flags.

| Flag | Environment | Default | |
| --- | --- | --- | --- |
| `--follow`, `-f` | | | Keep printing new output. |
| `--interval` | | `2s` | How often `--follow` checks for new output. |

### `gritz shell <task-id>`

Opens an interactive shell in a task's sandbox, relayed through the server. See
[A shell in the sandbox](/operators/debugging/#a-shell-in-the-sandbox). It takes
only the [authentication](#authentication) flags.

## Clients

### `gritz mcp`

Runs a local MCP server over stdio, for Claude Code, Cursor and other MCP
clients. See [MCP](/integrations/mcp/#connect-with-gritz-mcp). It also takes the [authentication](#authentication) flags.

| Flag | Environment | Default | |
| --- | --- | --- | --- |
| `--auto-archive` | | | The auto-archive delay for tasks created without an `auto_archive` argument. |
| `--channel` | | off | Push task updates into the Claude Code session as [channel](/integrations/mcp/#channels) messages, and add the `channel_mute`, `channel_unmute` and `channel_muted` tools. |

### `gritz notify`

Shows a desktop notification each time one of the organisation's tasks
completes, fails or is cancelled, until stopped. It also takes the [authentication](#authentication) flags.

| Flag | Environment | Default | |
| --- | --- | --- | --- |
| `--title` | | `gritz` | The notifications' title. |

## Server and runner

### `gritz server`

Runs the server. Its flags are described in [Server](/operators/server/).

| Flag | Environment | Default |
| --- | --- | --- |
| `--addr`, `-a` | | `:6464` |
| `--db`, `-d` | `GRITZ_DATABASE_URL` | |
| `--base-url` | `GRITZ_BASE_URL` | |
| `--cors` | `GRITZ_CORS` | off |
| `--auth-domain` | `GRITZ_AUTH_DOMAIN` | |
| `--auth-client-id` | `GRITZ_AUTH_CLIENT_ID` | |
| `--auth-client-secret` | `GRITZ_AUTH_CLIENT_SECRET` | |
| `--auth-scopes` | `GRITZ_AUTH_SCOPES` | `openid,profile,email,offline_access` |
| `--auth-skip-nonce-verify` | `GRITZ_AUTH_SKIP_NONCE_VERIFY` | off |
| `--auth-encryption-key` | `GRITZ_AUTH_ENCRYPTION_KEY` | required unless `--no-auth` |
| `--auth-app-key` | `GRITZ_AUTH_APP_KEY` | generated at each start |
| `--no-auth` | | off |
| `--github-app-id` | `GRITZ_GITHUB_APP_ID` | |
| `--github-app-slug` | `GRITZ_GITHUB_APP_SLUG` | |
| `--github-client-id` | `GRITZ_GITHUB_CLIENT_ID` | |
| `--github-client-secret` | `GRITZ_GITHUB_CLIENT_SECRET` | |
| `--github-webhook-secret` | `GRITZ_GITHUB_WEBHOOK_SECRET` | |
| `--github-private-key` | `GRITZ_GITHUB_APP_PRIVATE_KEY` | |
| `--atlassian-client-id` | `GRITZ_ATLASSIAN_CLIENT_ID` | |
| `--atlassian-client-secret` | `GRITZ_ATLASSIAN_CLIENT_SECRET` | |
| `--archive-poll` | `GRITZ_ARCHIVE_POLL` | `0` (off) |
| `--archive-batch` | `GRITZ_ARCHIVE_BATCH` | `100` |
| `--schedule-poll` | `GRITZ_SCHEDULE_POLL` | `10s` |
| `--schedule-batch` | `GRITZ_SCHEDULE_BATCH` | `100` |

### `gritz runner`

Runs a runner. Its flags are described in [Runner](/operators/runner/#flags).
It also takes the [authentication](#authentication) flags.

| Flag | Environment | Default |
| --- | --- | --- |
| `--workspaces`, `-w` | | `workspaces.yaml` in the config directory |
| `--id` | `GRITZ_RUNNER_ID` | the hostname |
| `--backend` | `GRITZ_BACKEND` | `docker` |
| `--concurrency` | | `5` |
| `--state-db` | `GRITZ_STATE_DB` | `/var/lib/gritz/runner.db` |
| `--poll` | | `30s` |
| `--lambda-microvm-region` | `GRITZ_LAMBDA_MICROVM_REGION` | the AWS SDK's region |
| `--local-root` | `GRITZ_LOCAL_ROOT` | |
| `--debug` | | off |

`--backend` also accepts `local`, which runs agents directly on the runner's
host, without isolation, in sandboxes under `--local-root`. It is for tests and
development.

### `gritz download`

Downloads the prebuilt driver binaries a runner needs from the latest GitHub
release. See [Driver binaries](/operators/runner/#driver-binaries).

| Flag | Default | |
| --- | --- | --- |
| `--github-repo` | `gritzapp/gritz` | The repository to download from, as `owner/repo`. |

### `gritz workspaces`

Loads a workspace config, with its `${env:}` and `${sh:}` expansions, and prints
the result, to check it before restarting a runner.

| Flag | Default | |
| --- | --- | --- |
| `--config`, `-c` | `workspaces.yaml` | The file to load, relative to the current directory. |

### `gritz containers`

Lists the gritz containers on this host's Docker, running or not.

| Flag | |
| --- | --- |
| `--prune` | Remove every stopped gritz container instead. |

### `gritz version`

Prints the version.

## Inside the sandbox

These commands run inside a task's sandbox. The runner and the workspace config
start them; you don't run them yourself.

### `gritz driver`

The driver server, which the runner's backend starts in each sandbox. It runs
the setup commands and the agent, and ships the task's log.

| Flag | Default | |
| --- | --- | --- |
| `--addr` | `:8080` | The TCP address or `unix:///path` to serve on. |
| `--prefix` | `/` | A directory prepended to every path the driver uses in the sandbox. |
| `--aws-lambda-hooks` | off | Also answer the Lambda MicroVM lifecycle hooks. |
| `--aws-lambda-validate` | | An executable to run on the Lambda MicroVM image's validate hook, to warm the snapshot. Needs `--aws-lambda-hooks`. |

### `gritz tool agent-mcp`

The MCP server behind the [agent tools](/using/agent-tools/). The runner adds it
to every agent's MCP config.

| Flag | |
| --- | --- |
| `--task`, `-t` | The task ID. Required. |
| `--runner`, `-r` | The runner ID. Required. |
| `--workspace`, `-w` | The workspace. Required. |
| `--token` | The task's token. Required. |
| `--server`, `-s` | The server URL. Defaults to `https://gritz.dev`. |
| `--capability` | A [capability](/operators/workspaces-yaml/#capabilities) of the task. Repeat it for several. |

### `gritz tool github-mcp`

An MCP server over stdio that forwards to GitHub's MCP server with GitHub App
installation tokens, rotating them before they expire. For workspaces with the
`github_token` capability; see
[GitHub credentials for agents](/integrations/github/#github-credentials-for-agents).

| Flag | Environment | Default | |
| --- | --- | --- | --- |
| `--server` | `GRITZ_SERVER` | `https://gritz.dev` | The server URL. |
| `--token` | `GRITZ_TOKEN` | | The task's token. |
| `--url` | | `https://api.githubcopilot.com/mcp/` | GitHub's MCP endpoint. |
| `--min-ttl` | | `5m` | Rotate to a new token when the current one has less than this left. |

### Hidden commands

`gritz --help` doesn't list these two:

- `gritz tool git-credential`: a git credential helper that answers with GitHub
  App installation tokens, for workspaces with the `github_token` capability. It
  takes `--server` (`GRITZ_SERVER`) and `--token` (`GRITZ_TOKEN`). Configure it
  with
  `git config --global credential.https://github.com.helper "!gritz tool git-credential"`.
- `gritz tool proxy --addr <addr>`: connects its stdin and stdout to a TCP
  address or `unix:///path`. The Docker backend uses it to reach the driver's
  socket.
