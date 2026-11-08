# CLI

The `gritz` binary is the CLI as well as the runner and server. Release binaries
for Linux are attached to each
[GitHub release](https://github.com/gritzapp/gritz/releases); to build it from a
checkout, run `mise run install`.

## Configuration

The CLI authenticates with an [API key](/concepts/orgs-members-keys/#api-keys),
and acts in the organisation the key belongs to. Create one on the **Keys** page
and save it in `config.json` in the gritz config directory:

```json
{
  "token": "xat_..."
}
```

The config directory is `GRITZ_CONFIG_DIR` if set, otherwise `gritz` under your
user config directory: `~/.config/gritz` on Linux,
`~/Library/Application Support/gritz` on macOS. A [runner](/operators/runner/)
on the same machine reads the same file.

Every command talks to `https://gritz.dev` unless `--server` (`-s`) or the
`GRITZ_SERVER` environment variable names another server:

```bash
export GRITZ_SERVER=https://gritz.example.com
```

`gritz notify` and `gritz mcp` are different: they take the key from `--token` or
`GRITZ_TOKEN` rather than `config.json`, and the server only from `--server`.

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
GRITZ_TOKEN=xat_... gritz notify
```

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