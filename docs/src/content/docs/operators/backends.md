---
title: Backends
description: The sandbox backends a runner can use, Docker and AWS Lambda MicroVMs, how each runs a task, and when to use which.
sidebar:
  order: 4
---

A runner starts every task's sandbox through one backend, chosen with
`--backend`. The backend creates the sandbox, starts and suspends it around each
run, connects the runner to the driver inside it, and destroys it when the task
is archived.

| | Docker | Lambda MicroVMs |
| --- | --- | --- |
| `--backend` | `docker` (the default) | `lambda-microvm` |
| Sandbox | A container on the runner's Docker daemon | An AWS Lambda MicroVM |
| Workspace section | `container:` | `lambda_microvm:` |
| Image | Any Linux image; the runner copies the driver in | A MicroVM image with the driver as its entrypoint, ARM64 only |
| Between runs | The container is stopped | The VM is suspended |
| Runner reaches the driver | `docker exec` tunnel to `/gritz/driver.sock` | AWS's managed proxy |
| Capacity | The runner host's | AWS's, up to your account limits |

**Use Docker** to get started, on a workstation or a dedicated host, or
when tasks need Docker-specific setup: volumes, networks, other runtimes such as
sysbox, or privileged containers.

**Use Lambda MicroVMs** to run many tasks without sizing a host for them, with
each task isolated in its own VM and nothing left running between runs.

A workspace can have both a `container:` and a `lambda_microvm:` section, so one
`workspaces.yaml` can serve runners on either backend. Each backend checks only
its own section, when a task's run starts, so a missing section fails that run
rather than the runner's start.

## Docker

The runner talks to the Docker daemon from its environment (`DOCKER_HOST`, or
the local socket), so it needs access to it: run it as a user in the `docker`
group, or mount `/var/run/docker.sock` into the runner's container.

For each task the runner:

1. Pulls the workspace's `container.image` if it isn't present, with the
   credentials from the runner user's `~/.docker/config.json` for private
   registries.
2. Creates a container named `gritz-<task-id>` with the labels `gritz=true`,
   `gritz.task=<task-id>` and `gritz.runner=<runner-id>`, and copies the
   matching `gritz` binary into it as `/usr/local/bin/gritz` (see
   [Driver binaries](/operators/runner/#driver-binaries)). Its main process is
   `gritz driver`, listening on `/gritz/driver.sock`.
3. Starts the container for each run and stops it afterwards. The container's
   filesystem survives between runs, so a woken task finds its checkout where it
   left it.
4. Removes the container when the task is archived.

The `container:` section maps onto the container's settings: `image`, `user`,
`working_dir`, `environment`, `volumes`, `networks`, `network_mode`,
`group_add`, `runtime` and `privileged`. See
[`workspaces.yaml`](/operators/workspaces-yaml/#container).

The driver inside the container connects to the server at the runner's
`--server` URL. When that is `localhost`, as in local development, the container
can't reach it on the default bridge network: use `network_mode: host` on Linux,
or a URL such as `http://host.docker.internal:6464`.

## Lambda MicroVMs

```bash
AWS_REGION=us-east-1 gritz runner --backend lambda-microvm
```

The runner resolves AWS credentials and the region through the standard AWS SDK
chain; `--lambda-microvm-region` overrides the region. The runner's credentials
make the MicroVM control-plane calls: `RunMicrovm`, `GetMicrovm`,
`SuspendMicrovm`, `ResumeMicrovm`, `TerminateMicrovm` and
`CreateMicrovmAuthToken`. Sandboxes never hold them.

For each task the runner:

1. Launches a MicroVM from the workspace's image (`run-microvm`) on the first
   run, and waits until it is running.
2. Starts each run over the managed proxy, on port 8080, with a proxy auth
   token scoped to that port.
3. Suspends the VM after every run (`suspend-microvm`) and resumes it for the
   next (`resume-microvm`).
4. Terminates the VM when the task is archived (`terminate-microvm`).

A VM runs for at most `max_duration_seconds`, 8 hours by default and at most, so
keep tasks that wake up over days on Docker.

### Workspace config

```yaml
workspaces:
  example:
    secrets:
      CLAUDE_CODE_OAUTH_TOKEN: ${env:CLAUDE_CODE_OAUTH_TOKEN}
    lambda_microvm:
      image_identifier: arn:aws:lambda:us-east-1:123456789012:microvm-image/gritz-driver
      execution_role: arn:aws:iam::123456789012:role/gritz-microvm
      egress_connector: arn:aws:lambda:us-east-1:aws:network-connector:aws-network-connector:INTERNET_EGRESS
    agent:
      type: claude
      cwd: /root
```

| Field | Required | |
| --- | --- | --- |
| `image_identifier` | yes | The MicroVM image's ARN. |
| `execution_role` | yes | The IAM role the VM runs as. It needs whatever the agent's work needs, and no MicroVM permissions. |
| `egress_connector` | yes | The network connector for outbound traffic, so the driver can reach the server: `INTERNET_EGRESS` or a VPC connector. |
| `ingress_connector` | no | The network connector the runner's proxy traffic comes in through. Defaults to the managed `ALL_INGRESS`; a connector scoped to port 8080 is tighter. |
| `region` | no | Must match the runner's region if set. One runner serves one region. |
| `max_duration_seconds` | no | The VM's maximum lifetime, up to `28800` (8 hours), the default. |
| `environment` | no | Environment variables for every run. A VM has no environment of its own, so these are passed with each run. |

### Building the image

A MicroVM image is built by Lambda from a zip holding a `Dockerfile` and a
`linux/arm64` `gritz` binary, with `gritz driver --aws-lambda-hooks` as its
entrypoint. Install the agent CLIs and tools your workspaces need in the same
`Dockerfile`. The
[backend's README](https://github.com/gritzapp/gritz/blob/master/internal/runner/backend/lambdamicrovm/README.md)
has the full recipe: the example `microvm.Dockerfile`, the
`create-microvm-image` call with its hooks, the build role, and warming the
image with `--aws-lambda-validate` so cold VMs start their tools quickly.

Unlike Docker, the driver is part of the image, so rebuild the image with the
new binary whenever you upgrade the runner.
