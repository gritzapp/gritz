# Debugging

## The task log

Each task's driver log holds the setup commands and their output, the agent
CLI's output and the driver's own logging. The driver streams it to the server as
the task runs, so it stays readable after the sandbox is gone and needs no access
to the runner or Docker:

```bash
gritz logs <task-id>     # print the whole log
gritz logs -f <task-id>  # follow it as the task runs
```

The Web UI shows the same log on the task's page. Declared
[secrets](/operators/secrets/) are masked in it.

`gritz logs` uses the API key in `config.json` (see
[Runner](/operators/runner/#quick-start)) and talks to `https://gritz.dev` unless
`--server` or `GRITZ_SERVER` says otherwise. `--interval` sets how often `-f`
polls, `2s` by default.

A setup command that fails ends the run with `setup command N failed`, where `N`
counts from 0. Its output is just above that line.

## Raw agent output

By default the driver parses the agent CLI's streamed output and logs a summary
of each message and tool call. Set `verbose: true` on a workspace's agent to log
every raw line instead, when the summary hides what you need: errors,
intermediate output, the full payload of a tool call.

```yaml
agent:
  type: claude
  verbose: true
```

`verbose` works for `claude`, `codex` and `cursor`. Restart the runner to pick
up the change. Like the rest of the agent config, it applies to tasks created
after that; see [changing a workspace](/operators/workspaces-yaml/#changing-a-workspace).

## A shell in the sandbox

```bash
gritz shell <task-id>
```

opens an interactive shell in a task's sandbox, to inspect what a finished task
left behind. The task must have finished: gritz won't interrupt a running agent.
The runner brings the sandbox back up with a shell run in place of the agent, on
the same filesystem, and the task's timeline records it as a restart.

It works for every backend and for runners on other machines: the driver dials
the server, and the server relays your terminal to it, so you need neither
Docker access nor a route to the runner.

Inside, `/gritz/log` is the driver's raw log, with secrets unmasked, and
`/tmp/gritz/<task-id>.json` is the agent config the runner wrote, including the
MCP servers.

## Containers

On the Docker backend each task's container is named `gritz-<task-id>` and
labelled `gritz=true`, `gritz.task=<task-id>` and `gritz.runner=<runner-id>`.
`gritz containers` lists them on the current Docker host, running and stopped:

```bash
gritz containers
```

A task's container is stopped between runs and removed when the task is
archived. Containers can be left behind when a runner loses its
[state database](/operators/runner/#the-state-database), or when a container is
created for a runner ID that no longer runs. To remove them:

- `gritz containers --prune` removes every stopped gritz container on the host,
  including those of tasks that are only idle between runs, which then lose
  their sandbox's files. Use it only when no runner on the host has tasks you
  want to keep.
- `docker rm` a container by name, such as `docker rm gritz-1234`, to remove one.

The runner logs each sandbox it creates, boots, suspends and destroys; run it
with `--debug` for more detail.