# workspaces.yaml

A runner's `workspaces.yaml` defines the workspaces it offers. A workspace is
everything a task's sandbox needs: the image, the setup commands that prepare
it, the agent that runs, and the MCP servers and credentials that agent gets.

The runner reads the file once, at start, from `--workspaces`
(`~/.config/gritz/workspaces.yaml` by default). Restart the runner after
changing it, and see [Changing a workspace](#changing-a-workspace) for which
tasks see the change.

```yaml
workspaces:
  pets-workshop:
    description: Example Node.js pets workshop
    secrets:
      CLAUDE_CODE_OAUTH_TOKEN: ${env:CLAUDE_CODE_OAUTH_TOKEN}
      GH_TOKEN: ${sh:gh auth token}
    container:
      image: ghcr.io/gritzapp/workspace-debian:latest
      working_dir: /root
    commands:
      - git clone https://x-access-token:${GH_TOKEN}@github.com/example/pets-workshop.git
    agent:
      type: claude
      cwd: /root/pets-workshop
      prompt: |
        Run the tests with `npm test` before you open a pull request.
```

[gritzapp/workspaces](https://github.com/gritzapp/workspaces) has more
examples, for each agent, MCP servers and private repositories, and builds the
`ghcr.io/gritzapp/workspace-*` images they use. The images don't contain gritz:
the runner adds its driver to each sandbox.

## Workspace fields

`workspaces` maps each workspace's name to its config. The name is what users
pick when they create a task.

| Field | |
| --- | --- |
| `description` | Shown next to the name in the Web UI. |
| `container` | The sandbox for the [Docker backend](#container). |
| `lambda_microvm` | The sandbox for the [Lambda MicroVMs backend](/operators/backends/#workspace-config). |
| `commands` | [Setup commands](#commands), run before the agent. |
| `agent` | The [agent](#agent) and its MCP servers. |
| `secrets` | Environment variables whose values are masked in the shipped log. See [Secrets](/operators/secrets/). |
| `capabilities` | Extra [capabilities](#capabilities) for the task's token. |
| `variables` | Per-task [variables](#variables) a task can set when it is created. |
| `features` | Optional [features](#features) an agent can add to its tasks. |
| `auto_reset` | Start a run whose sandbox is gone in a new sandbox instead of failing it. Off by default. See [A gone sandbox](/concepts/tasks/#a-gone-sandbox). |

## `container`

| Field | |
| --- | --- |
| `image` | The image to run. Required by the Docker backend. Pulled if missing. |
| `working_dir` | The container's working directory, where setup commands run. |
| `user` | The user the driver, setup commands and agent run as. Defaults to the image's. |
| `environment` | Environment variables, as a map. Put credentials under `secrets:` instead. |
| `volumes` | Bind mounts and volumes, in Docker's `source:target[:options]` form. |
| `networks` | Docker networks to attach the container to. |
| `network_mode` | Docker's network mode, such as `host`. |
| `group_add` | Extra groups for the container's user. |
| `runtime` | The container runtime, such as `sysbox-runc` for Docker inside the sandbox. |
| `privileged` | Run the container privileged. |

## `commands`

Setup commands prepare the sandbox: clone repositories, install dependencies,
configure tools. Each is run with `sh -c`, in order, in the sandbox's working
directory, with the same environment as the agent.

They run once per task, on its first run. A command that fails fails the run;
the next run of the task starts again at the command that failed, so the ones
before it aren't repeated.

Their output goes to the task's log. Reference credentials by variable, as
`${GH_TOKEN}` rather than `${env:GH_TOKEN}`: the sandbox's shell expands it, so
the command line the driver logs carries the name, not the value.

## `agent`

| Field | |
| --- | --- |
| `type` | `claude` (the default), `codex`, `cursor`, `copilot`, `process` or `gritzrpc`. `dummy` is a stand-in that runs no model, for testing a runner. |
| `cwd` | The directory the agent starts in. `$VAR` references are expanded from the sandbox's environment. |
| `prompt` | Text added to the agent's prompt on a task's first run, after gritz's own instructions: conventions for this repository, say. Not allowed with `gritzrpc`. |
| `verbose` | Log every raw line of the agent CLI's output instead of parsing it. Supported by `claude`, `codex` and `cursor`; not allowed with `gritzrpc`. See [Debugging](/operators/debugging/#raw-agent-output). |
| `mcp_servers` | MCP servers for the agent, by name. |
| `claude`, `codex`, `cursor`, `copilot` | Options for that agent: `model`, and `bin`, the CLI to run if not the default (`claude`, `codex` and so on). |
| `process` | For `type: process`: `command`, the program to run and its arguments, as a list. See [Custom agent process](#custom-agent-process). |
| `gritzrpc` | For `type: gritzrpc`: `command`, the program to run (required), and `args`, its arguments. See [gritzrpc agent](#gritzrpc-agent). |

The agent CLI has to be installed in the image. Claude Code runs with
`--model opus` unless `claude.model` is set; the other agents use their CLI's
default model.

```yaml
agent:
  type: claude
  cwd: /root/pets-workshop
  claude:
    model: sonnet
```

### Custom agent process

`type: process` runs your own program as the agent, for each prompt:

- The prompt arrives on its standard input.
- `GRITZ_RESUME` is `true` when the prompt continues an earlier session of the
  task, such as a wake for a new event.
- `mcp_servers` don't apply: the program gets no MCP configuration.
- Its standard output and standard error go to the task's log.
- A non-zero exit status fails the run.
- Without a `command`, the run ends as soon as it starts.

```yaml
agent:
  type: process
  process:
    command: [/usr/local/bin/my-agent, --fast]
```

### gritzrpc agent

`type: gritzrpc` runs your own program as the agent and talks to it over
[gritzrpc](/reference/gritzrpc/), a JSON-RPC protocol on its standard input and
output. Use it for a program rather than a chat-style CLI, such as a workflow
that suspends until a pull request is reviewed:

- On each run the program receives the task, the typed events that started the
  run, and the configs of its MCP servers, the `gritz` server included.
- It reports its outcome in its response: a successful response completes the
  run, an error fails it with the error's message.
- Its standard error and the log lines it sends go to the task's log.
- A new process starts for every run, so state the program needs on a later run
  has to be saved, in the workspace or elsewhere.

```yaml
workspaces:
  mastra:
    container:
      image: ghcr.io/acme/mastra-agent:latest
    secrets:
      OPENAI_API_KEY: ${env:OPENAI_API_KEY}
    commands:
      - cd /workspace && pnpm install --frozen-lockfile && pnpm build
    agent:
      type: gritzrpc
      cwd: /workspace
      gritzrpc:
        command: node
        args: [dist/agent.js]
```

The program reads `OPENAI_API_KEY` from its environment.

### `mcp_servers`

Each entry is a stdio server, which the agent starts inside the sandbox, or a
remote one it connects to over HTTP:

| Field | |
| --- | --- |
| `type` | `stdio`, `http` or `sse`. |
| `command`, `args`, `env` | For `stdio`: the command to run, its arguments and extra environment. |
| `url`, `headers` | For `http` and `sse`: the server's URL and request headers. |

```yaml
workspaces:
  pets-workshop:
    secrets:
      METAMCP_API_KEY: ${env:METAMCP_API_KEY}
    agent:
      mcp_servers:
        meta:
          type: http
          url: http://metamcp:12008/metamcp/Default/mcp
          headers:
            Authorization: Bearer ${env:METAMCP_API_KEY}
```

gritz passes header values to the agent as written, so a credential in a header
comes from `${env:}` or `${sh:}`. Declare the same value under `secrets:`, as
above, so it is masked if it shows up in the log.

The runner adds an MCP server named `gritz` to every agent, with the tools an
agent uses to work with its task (see the
[overview](/operators/overview/#components)). Don't define one with that name.

## `variables`

Variables are values a task sets when it is created, such as a branch or an
issue key, declared by name. Names are upper case letters, digits and
underscores, starting with a letter. Each run sees a variable `X` as the
environment variable `GRITZ_VAR_X`; one the task doesn't set and that has no
default is left unset.

| Field | |
| --- | --- |
| `description` | What the value is for. |
| `required` | A task must set it. |
| `default` | The value when the task doesn't set it. Not allowed on a required variable. |

```yaml
workspaces:
  story:
    variables:
      JIRA_ISSUE:
        description: The story to implement
        required: true
      BRANCH:
        default: main
    commands:
      - git clone --branch "$GRITZ_VAR_BRANCH" https://github.com/acme/app.git /work
```

Quote the reference, as `"$GRITZ_VAR_BRANCH"`, so the shell passes the value as
one word. Values are readable by anyone who can read the task and appear
unmasked in its log: put credentials under `secrets:`.

Each time it starts a task, the runner checks the task's variables against its
own `variables:` and fills in defaults. A task this config no longer accepts,
because it sets a variable that isn't declared or lacks a required one, fails
with the reason on its timeline. So a change to `variables:` can fail the next
start of a task created earlier, and of every task created later from an
[agent](/concepts/agents/#the-template) saved before it.

## `features`

Features are additions to a workspace that an
[agent](/concepts/agents/#the-template) can pick for its tasks, such as a tool
and the credential it needs, or a different agent CLI. `features` maps each
feature's name to its config. An agent on the workspace can pick any of them;
a task whose agent picks none gets the workspace as written.

| Field | |
| --- | --- |
| `description` | Shown next to the feature's name in the agent editor. |
| `environment` | Environment variables, as a map, on every backend. Put credentials under `secrets` instead. |
| `secrets` | Like the workspace's [`secrets`](/operators/secrets/): environment variables whose values are masked in the shipped log. |
| `mcp_servers` | [MCP servers](#mcp_servers) added to the task's agent. |
| `commands` | [Setup commands](#commands), run after the workspace's. |
| `agent` | An [agent](#agent) that [replaces](#replacing-the-agent) the workspace's. |

```yaml
workspaces:
  app:
    secrets:
      CLAUDE_CODE_OAUTH_TOKEN: ${env:CLAUDE_CODE_OAUTH_TOKEN}
    container:
      image: ghcr.io/gritzapp/workspace-debian:latest
    commands:
      - git clone https://github.com/acme/app.git /root/app
    agent:
      type: claude
      cwd: /root/app
    features:
      node:
        description: Node.js 22 and pnpm
        commands:
          - mise use -g node@22 pnpm@10
      jira:
        description: Jira MCP server
        environment:
          JIRA_SITE: acme.atlassian.net
        secrets:
          JIRA_API_TOKEN: ${env:JIRA_API_TOKEN}
        mcp_servers:
          jira:
            type: stdio
            command: jira-mcp
            args: [--site, acme.atlassian.net]
      codex:
        description: Run on Codex instead of Claude Code
        secrets:
          OPENAI_API_KEY: ${env:OPENAI_API_KEY}
        commands:
          - mise use -g node@22 npm:@openai/codex
        agent:
          type: codex
          cwd: /root/app
```

Features go through the same [expansion](#variable-expansion) as the rest of
the file, all of them, whether or not an agent picks them.

### Merge order

An agent lists its features in order, and each of its tasks gets the workspace
with those features applied on top, in that order. Where two of them, or a
feature and the workspace, set the same name, the later one wins. Nothing checks
for such conflicts.

- **Environment and secrets**: a feature's `environment` or `secrets` entry
  replaces a variable of the same name from the workspace's `secrets`,
  `container.environment` or `lambda_microvm.environment`, or from an earlier
  feature. The later one also decides whether it is a secret: a name a feature
  sets in `environment` is no longer masked.
- **MCP servers**: each feature's `mcp_servers` are added to the agent's, and
  replace a server of the same name. The `gritz` server is still added last.
- **Commands**: the workspace's `commands` run first, then each feature's, in
  the agent's order. List a feature that needs another's tools after it, such as
  a feature that installs a package with pnpm after `node` above.

### Replacing the agent

A feature's `agent` takes the same fields as the workspace's
[`agent`](#agent), and replaces it as a whole. Nothing of the workspace's
`agent` carries over: not its `type`, `cwd`, `prompt` or `mcp_servers`. Repeat
whatever the feature's agent should keep, such as an MCP server every agent
uses.

When several of a task's features have an `agent`, the last one in the agent's
list is used. The others still add their environment, secrets, MCP servers and
commands, and every feature's `mcp_servers`, that one's included, are added to
the agent it ends up with. The workspace's `commands` and `capabilities` aren't
part of `agent` and stay as they are.

## `capabilities`

Capabilities let a task's token do more than read and update its own task. One
exists:

- `github_token`: meant to let the task get GitHub App installation tokens for
  its organisation. The runner accepts it and the agent gets a
  [`get_github_token`](/using/agent-tools/#get_github_token) tool, but the
  server doesn't issue these tokens yet: every request fails, and so do the
  `gritz tool git-credential` and `gritz tool github-mcp` helpers that use them.

Give agents GitHub access with a token under `secrets:` instead, and reference
it by variable:

```yaml
workspaces:
  pets-workshop:
    secrets:
      GH_TOKEN: ${env:GH_TOKEN}
    commands:
      - git config --global credential.https://github.com.helper '!f() { echo username=x-access-token; echo "password=${GH_TOKEN}"; }; f'
      - git clone https://github.com/example/pets-workshop.git
    agent:
      mcp_servers:
        github:
          type: http
          url: https://api.githubcopilot.com/mcp/
          headers:
            Authorization: Bearer ${env:GH_TOKEN}
```

The credential helper reads `GH_TOKEN` when git runs it, so the logged command
carries only the name. The MCP header uses `${env:}` because header values reach
the agent [as written](#mcp_servers).

## Variable expansion

When it loads the file, the runner expands `${namespace:value}` in every string
value:

- `${env:NAME}` is the runner's environment variable `NAME`, or empty if unset.
- `${sh:command}` is the output of `sh -c command` on the runner's host, with
  surrounding whitespace trimmed. A failing command fails the load.

Any other namespace is an error. References without a namespace, such as
`${GH_TOKEN}` or `$HOME`, are left alone, for the sandbox's shell to expand.

## Environment

The setup commands, the agent and a
[`gritz shell`](/operators/debugging/#a-shell-in-the-sandbox) session see, in
order of precedence:

1. The workspace's `secrets` and its features' `secrets`.
2. The variables gritz sets: `GRITZ_TASK_ID`, `GRITZ_SERVER` and `GRITZ_TOKEN`,
   the task's own token, and the task's [variables](#variables) as
   `GRITZ_VAR_<NAME>`. On Lambda MicroVMs, also `lambda_microvm.environment`
   and the features' `environment`.
3. The sandbox's environment: the image's, plus `container.environment` and the
   features' `environment` on Docker.

A name that the workspace and the task's features set more than once is first
resolved by [merge order](#merge-order), so only its last value reaches this
list.

## Changing a workspace

A task's sandbox is set up from its workspace once, when it is first created
and run, and keeps that setup until the task is archived or its sandbox is
[reset](/concepts/tasks/#resetting-the-sandbox). After you change a
workspace and restart the runner:

- `container`, `lambda_microvm` (except `environment`), `commands`,
  `capabilities` and `agent`, including `prompt` and `mcp_servers`, apply to
  tasks created from then on. Existing tasks keep what they started with until
  their sandbox is reset.
- `secrets` and `lambda_microvm.environment` are sent with each run, so existing
  tasks get the new values from their next run.
- A feature's fields follow the same rules: its `secrets`, and its
  `environment` on Lambda MicroVMs, reach existing tasks from their next run;
  the rest applies from their next sandbox. A task keeps the features its agent
  had when the task was created. Removing or renaming a feature fails the next
  start of every task that has it, with the reason on its timeline.
- `auto_reset` applies to the next run of every task.

## Checking a config

`gritz workspaces` loads a file the way the runner does and prints the result:

```bash
gritz workspaces -c ~/.config/gritz/workspaces.yaml
```

It checks the YAML, the agents and MCP servers, features' included, and the
capabilities, but not the `container:` or `lambda_microvm:` section, which the
backend checks when a run starts. The output is the config after expansion, so
it contains your workspace secrets' values; features' secrets are printed as
`[masked]`.