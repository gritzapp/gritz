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

Log in with your browser:

```bash
gritz login
```

```
Opening https://gritz.dev/ui/oauth/authorize?... in your browser.
Logged in to Acme Labs (org 12) as "acme-labs"
```

`gritz login` opens the gritz Web UI in your browser, signed in as you, and
waits for you to approve. It logs in to the organisation selected in the Web UI
at that moment; to log in to another, switch to it in the Web UI before you
approve. If the browser doesn't open, open the printed URL yourself, on the same
machine: the browser hands the login back to `gritz login` there. It gives up
after 10 minutes. The CLI then acts as you, with what a member can do in that
organisation.

`gritz login` saves the login under a name in `.gritz.yml` in the gritz config
directory. The name is the organisation's name in lowercase with dashes, unless
`--name` gives another, and the first organisation you log in to becomes the
selected `org`. Log in once for each organisation you work in:

```yaml
org: acme-labs
orgs:
  acme-labs:
    id: 12
    refresh_token: eyJhbGciOiJFZERTQSIs...
    server: https://gritz.dev
  personal:
    id: 3
    refresh_token: eyJhbGciOiJFZERTQSIs...
    server: https://gritz.dev
```

A login lasts as long as you keep using it: it expires after 30 days in which
the CLI didn't use it, and stops working once you are removed from the
organisation or it is deleted. The CLI renews the saved `refresh_token` in the file it came from
about once a day while you use it. When a login has expired, commands fail with:

```
login expired or revoked: run `gritz login`
```

Run `gritz login` again to replace it. A login isn't tied to the machine, so an
entry copied into another machine's `.gritz.yml` works there too.

### Logging in with an API key

Where no browser can reach `gritz login`, such as a remote machine over SSH, or
for a program rather than a person, log in with an
[API key](/concepts/orgs-members-keys/#api-keys) instead. Create one on the
**Keys** page and pass it with `--key` (`-k`):

```bash
gritz login --key xat_...
```

`gritz login` asks the server which organisation the key belongs to and saves
the key in place of a `refresh_token`:

```yaml
orgs:
  acme-ci:
    id: 12
    key: xat_...
    server: https://gritz.dev
```

A key doesn't expire. Logging in again to the same organisation and server
replaces the entry's credential, key or browser login.

The config directory is `GRITZ_CONFIG_DIR` if set, otherwise `gritz` under your
user config directory: `~/.config/gritz` on Linux,
`~/Library/Application Support/gritz` on macOS.

### Choosing an organisation

Every command acts in one organisation: the one `--org` (or `GRITZ_ORG`) names,
otherwise the `org` of the `.gritz.yml` files.

```bash
gritz task list --org personal
```

A `.gritz.yml` file can also live in any directory, such as a repository or a
folder of checkouts. A command reads every `.gritz.yml` from the directory it runs
in up to `/`, on top of the one in the config directory, and the nearest file
wins. Organisations of the same name replace each other whole. A repository can
pick its organisation without holding a login:

```yaml
org: acme-labs
allowed: [acme-labs]
```

`allowed` lists the organisations that can be used below the file; a command run
there with any other `--org` fails, and so does every command if the `org`
isn't allowed. Every field is optional, and an unknown field is an error.

`gritz login --list` shows the organisations you can pick from the current
directory, with `*` marking the one selected by default:

```
   NAME      ID  SERVER             LOGIN
   acme-ci   12  https://gritz.dev  key
*  personal  3   https://gritz.dev  browser
```

### Server

Every command talks to the organisation's `server`, or to `https://gritz.dev` if
it has none. `gritz login` saves the server you give it with `--server` (`-s`)
or the `GRITZ_SERVER` environment variable:

```bash
gritz login --server https://gritz.example.com
```

Without `--server`, it uses the top-level `server` of the nearest `.gritz.yml`
that sets one, otherwise `https://gritz.dev`, so a repository can name the
server its organisation lives on:

```yaml
server: https://gritz.example.com
```

The top-level `server` is read only by `gritz login`.

### Key without a config file

`--key` (`-k`) or the `GRITZ_API_KEY` environment variable gives a command its key
directly, for CI and other places with no config file. The command then reads no
`.gritz.yml` files and ignores `--org`, and talks to `--server`, `GRITZ_SERVER` or
`https://gritz.dev`. Other than in `gritz login`, `--server` and `GRITZ_SERVER`
need a key this way, since an organisation in a `.gritz.yml` carries its own
server:

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
