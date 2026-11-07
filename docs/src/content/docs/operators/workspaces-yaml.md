---
title: workspaces.yaml
description: Reference for the runner's workspace config, the container, setup commands, agent, MCP servers, capabilities and secrets of each workspace, and ${env:} and ${sh:} expansion.
sidebar:
  order: 5
---

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
| `type` | `claude` (the default), `codex`, `cursor` or `copilot`. `dummy` is a stand-in that runs no model, for testing a runner. |
| `cwd` | The directory the agent starts in. `$VAR` references are expanded from the sandbox's environment. |
| `prompt` | Text added to the agent's prompt on a task's first run, after gritz's own instructions: conventions for this repository, say. |
| `verbose` | Log every raw line of the agent CLI's output instead of parsing it. Supported by `claude`, `codex` and `cursor`. See [Debugging](/operators/debugging/#raw-agent-output). |
| `mcp_servers` | MCP servers for the agent, by name. |
| `claude`, `codex`, `cursor`, `copilot` | Options for that agent: `model`, and `bin`, the CLI to run if not the default (`claude`, `codex` and so on). |

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

## `capabilities`

Capabilities let a task's token do more than read and update its own task. One
exists today:

- `github_token`: the task can get GitHub App installation tokens for its
  organisation, through the server's GitHub App. The agent gets a
  `get_github_token` tool, and two helpers in the `gritz` binary use the token:
  `gritz tool git-credential`, a git credential helper, and
  `gritz tool github-mcp`, which fronts GitHub's MCP server with fresh tokens.

```yaml
workspaces:
  pets-workshop:
    capabilities:
      - github_token
    commands:
      - git config --global credential.https://github.com.helper "!gritz tool git-credential"
      - git clone https://github.com/example/pets-workshop.git
    agent:
      mcp_servers:
        github:
          type: stdio
          command: gritz
          args: ["tool", "github-mcp"]
```

This needs the [GitHub App](/operators/server/#github-app) configured on the
server and installed on the organisation's repositories.

## Variable expansion

When it loads the file, the runner expands `${namespace:value}` in every string
value:

- `${env:NAME}` is the runner's environment variable `NAME`, or empty if unset.
- `${sh:command}` is the output of `sh -c command` on the runner's host, with
  surrounding whitespace trimmed. A failing command fails the load.

Any other namespace is an error. References without a namespace, such as
`${GH_TOKEN}` or `$HOME`, are left alone, for the sandbox's shell to expand.

## Environment

The setup commands and the agent see, in order of precedence:

1. The workspace's `secrets`.
2. The variables gritz sets: `GRITZ_TASK_ID`, `GRITZ_SERVER` and `GRITZ_TOKEN`,
   the task's own token. On Lambda MicroVMs, also `lambda_microvm.environment`.
3. The sandbox's environment: the image's, plus `container.environment` on
   Docker.

## Changing a workspace

A task's sandbox is set up from its workspace once, when it is first created
and run, and keeps that setup until the task is archived. After you change a
workspace and restart the runner:

- `container`, `lambda_microvm` (except `environment`), `commands`,
  `capabilities` and `agent`, including `prompt` and `mcp_servers`, apply to
  tasks created from then on. Existing tasks keep what they started with.
- `secrets` and `lambda_microvm.environment` are sent with each run, so existing
  tasks get the new values from their next run.

## Checking a config

`gritz workspaces` loads a file the way the runner does and prints the result:

```bash
gritz workspaces -c ~/.config/gritz/workspaces.yaml
```

It checks the YAML, the agent's MCP servers and the capabilities, but not the
`container:` or `lambda_microvm:` section, which the backend checks when a run
starts. The output is the config after expansion, so it contains your secrets'
values.
