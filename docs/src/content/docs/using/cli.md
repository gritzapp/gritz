---
title: CLI
description: Use the gritz CLI to create, list and update tasks, read a task's log, open a shell in its sandbox and get desktop notifications, and log in to one or more organisations.
sidebar:
  order: 2
---

The `gritz` binary is the CLI as well as the runner and server. Release binaries
for Linux are attached to each
[GitHub release](https://github.com/gritzapp/gritz/releases); to build it from a
checkout, run `mise run install`.

## Configuration

The CLI authenticates with an [API key](/concepts/orgs-members-keys/#api-keys),
and acts in the organisation the key belongs to. Create one on the **Keys** page
and log in with it:

```bash
gritz login --key xat_...
```

```
Logged in to Acme Labs (org 12) as "acme-labs"
```

`gritz login` asks the server which organisation the key belongs to and saves the
key under a name in `.gritz.yml` in the gritz config directory. The name is the
organisation's name in lowercase with dashes, unless `--name` gives another, and
the first organisation you log in to becomes the default. Log in once for each
organisation you work in:

```yaml
default: acme-labs
orgs:
  acme-labs:
    id: 12
    key: xat_...
  personal:
    id: 3
    key: xat_...
```

The config directory is `GRITZ_CONFIG_DIR` if set, otherwise `gritz` under your
user config directory: `~/.config/gritz` on Linux,
`~/Library/Application Support/gritz` on macOS.

### Choosing an organisation

Every command acts in one organisation: the one `--org` (or `GRITZ_ORG`) names,
otherwise the `default`.

```bash
gritz task list --org personal
```

A `.gritz.yml` file can also live in any directory, such as a repository or a
folder of checkouts. A command reads every `.gritz.yml` from the directory it runs
in up to `/`, on top of the one in the config directory, and the nearest file
wins. Organisations of the same name replace each other whole. A repository can
pick its organisation without holding a key:

```yaml
default: acme-labs
allowed: [acme-labs]
```

`allowed` lists the organisations that can be used below the file; a command run
there with any other `--org` fails, and so does every command if the `default`
isn't allowed. Every field is optional, and an unknown field is an error.

### Server

Every command talks to `https://gritz.dev` unless the organisation has a
`server`, or `--server` (`-s`) or the `GRITZ_SERVER` environment variable names
another server:

```bash
gritz login --key xat_... --server https://gritz.example.com
```

`gritz login` with `--server` saves the server with the organisation.

### Key without a config file

`--key` (`-k`) or the `GRITZ_API_KEY` environment variable gives a command its key
directly, for CI and other places with no config file. The command then reads no
`.gritz.yml` files and ignores `--org`, and talks to `--server`, `GRITZ_SERVER` or
`https://gritz.dev`:

```bash
GRITZ_API_KEY=xat_... gritz task list
```

## Tasks

### `gritz task create`

Creates a task and starts it straight away, then prints it as JSON.

```bash
gritz task create -r my-runner -w pets-workshop \
  -i "Add a --json flag to the list command and open a pull request."
```

| Flag | |
| --- | --- |
| `--runner`, `-r` | The [runner](/concepts/runners/) to run on. Required. |
| `--workspace`, `-w` | The [workspace](/concepts/workspaces/). Defaults to `default`. |
| `--instruction`, `-i` | An instruction. Repeat it for several. |
| `--name`, `-n` | The task's name. The agent names it if you don't. |
| `--auto-archive` | The [auto-archive](/concepts/tasks/#auto-archive) delay, such as `1h` or `24h`. `0`, the default, is never; a negative value archives the task as soon as it finishes. |

The **Workspaces** page in the Web UI lists runner IDs and their workspaces.

### `gritz task list`

Prints the organisation's 50 newest unarchived tasks as JSON, with their status,
runner, workspace and URL.

### `gritz task update`

Changes a task.

```bash
gritz task update 1234 --start -i "Also update the README."
```

| Flag | |
| --- | --- |
| `--add-instruction`, `-i` | Add an instruction. Repeat it for several. |
| `--start`, `-r` | Start the task, as the Web UI does when you send an instruction. A running task isn't interrupted: it runs again when its current run ends. |
| `--name`, `-n` | Rename the task. |
| `--auto-archive` | Set the auto-archive delay. Left out, the task keeps the delay it has. |

An instruction added without `--start` waits on the task until something else
starts it, such as an event or a later `--start`.

Cancelling, restarting and archiving tasks are done in the
[Web UI](/using/web-ui/#the-task-page).

## `gritz logs`

Prints a task's log: the setup commands, the agent's output and gritz's own
lines, as the Web UI's **Logs** view shows them. It works for any task in the
organisation, including tasks whose sandbox is gone.

```bash
gritz logs 1234     # print the log so far
gritz logs -f 1234  # and keep following it
```

`--interval` sets how often `-f` checks for new output, `2s` by default.

## `gritz shell`

Opens an interactive shell in a finished task's sandbox, to look at what the
agent left behind:

```bash
gritz shell 1234
```

The runner starts the sandbox again with a shell in place of the agent. It works
whatever the runner's backend and wherever it runs: the server relays the
session, so you need no access to the runner. See
[A shell in the sandbox](/operators/debugging/#a-shell-in-the-sandbox).

## `gritz notify`

Stays running and shows a desktop notification each time one of the
organisation's tasks completes, fails or is cancelled.

```bash
gritz notify
```

Run one `gritz notify --org <name>` for each organisation to watch several.

`--title` sets the notifications' title, `gritz` by default. It uses
`notify-send` on Linux and the system notifications on macOS and Windows.

## Other commands

- `gritz mcp` runs a local MCP server that lets Claude Code, Cursor and other
  MCP clients create and manage tasks; see [MCP](/integrations/mcp/).
- `gritz runner`, `gritz download`, `gritz workspaces` and `gritz containers`
  are for running a runner; see [Runner](/operators/runner/) and
  [Debugging](/operators/debugging/).
- `gritz version` prints the CLI's version.

[CLI reference](/reference/cli/) lists every command and flag.
