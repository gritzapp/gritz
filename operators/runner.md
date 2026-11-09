# Runner

`gritz runner` runs tasks on infrastructure you control. It connects to the
server with an API key, offers the workspaces in its `workspaces.yaml`, and runs
every task created for it in a sandbox of its own: a Docker container by default.

## Quick start

These steps run a runner against gritz.dev on a machine with Docker.

1. **Install `gritz`:**

   ```bash
   curl -fsSL https://docs.gritz.dev/install.sh | sh
   ```

   This installs the latest release into `~/.local/bin`. See
   [Install](/using/cli/#install) for the other ways to install it, with mise
   or from a release archive, and for how to update.

2. **Create an API key** on the **Keys** page
   (`https://gritz.dev/ui/keys/new`) and copy the `xat_…` token: it is shown only
   once. Log in with it:

   ```bash
   gritz login --key xat_...
   ```

   This saves the key in `~/.config/gritz/.gritz.yml`; see
   [Logging in with an API key](/using/cli/#logging-in-with-an-api-key). On a server, pass it with
   `--key` or `GRITZ_API_KEY` instead.

3. **Write a `workspaces.yaml`** in `~/.config/gritz/`. This one clones a sample
   repository and runs Claude Code on it with your Claude Code OAuth token:

   ```yaml
   workspaces:
     pets-workshop:
       description: Example Node.js pets workshop
       secrets:
         CLAUDE_CODE_OAUTH_TOKEN: ${env:CLAUDE_CODE_OAUTH_TOKEN}
       container:
         image: ghcr.io/gritzapp/workspace-debian:latest
         working_dir: /root
       commands:
         - git clone https://github.com/github-samples/pets-workshop
       agent:
         type: claude
         cwd: /root/pets-workshop
   ```

   See [`workspaces.yaml`](/operators/workspaces-yaml/) for every field.

4. **Start the runner:**

   ```bash
   gritz runner --state-db ~/.local/state/gritz/runner.db
   ```

5. **Create a task** in the Web UI at `https://gritz.dev/`, choosing your runner
   and the `pets-workshop` workspace.

## Flags

| Flag | Environment variable | Default | |
| --- | --- | --- | --- |
| `--server`, `-s` | `GRITZ_SERVER` | `https://gritz.dev` | The server URL to use with `--key`, which it requires; without `--key`, the organisation's `server` is used. Sandboxes connect to the same URL, so it must be reachable from inside them too. |
| `--key`, `-k` | `GRITZ_API_KEY` | | The API key. Set, the runner reads no `.gritz.yml` files, and `--org` is an error. |
| `--org` | `GRITZ_ORG` | the `org` in the `.gritz.yml` files | The organisation to run tasks for, by its name in the `.gritz.yml` files. |
| `--workspaces`, `-w` | | `~/.config/gritz/workspaces.yaml` | The [workspace config](/operators/workspaces-yaml/). |
| `--id` | `GRITZ_RUNNER_ID` | the hostname | The runner's ID, which users pick when they create a task. Letters and digits, no spaces. |
| `--backend` | `GRITZ_BACKEND` | `docker` | The [sandbox backend](/operators/backends/): `docker` or `lambda-microvm`. |
| `--concurrency` | | `5` | The most task runs at once; further runs wait for a slot. `0` means no limit. |
| `--state-db` | `GRITZ_STATE_DB` | `/var/lib/gritz/runner.db` | The runner's SQLite state. |
| `--poll` | | `30s` | How often to poll the server when the notification stream is down. |
| `--lambda-microvm-region` | `GRITZ_LAMBDA_MICROVM_REGION` | the AWS SDK's region | The AWS region for the `lambda-microvm` backend. |
| `--debug` | | off | Debug logging. |

### The config directory

`.gritz.yml`, the default `workspaces.yaml` and downloaded driver binaries
live in the gritz config directory: `GRITZ_CONFIG_DIR` if set, otherwise `gritz`
under the user config directory (`~/.config/gritz` on Linux,
`~/Library/Application Support/gritz` on macOS).

### The state database

`--state-db` records which sandbox belongs to which task, the task changes the
runner has received, and the reports it still has to send to the server. Keep it
on persistent storage and give each runner its own: a runner that loses it no
longer knows its sandboxes and leaves them behind (see
[Debugging](/operators/debugging/#containers)). The runner creates the file and
its directory; the default path usually needs root, so point it somewhere the
runner's user can write on a workstation.

### Runner IDs and workspaces

On start, the runner registers its workspace names and descriptions with the
server under its ID. Users see them when they create a task, and a task stays
with the runner and workspace it was created for. Give every runner a unique
ID; change `workspaces.yaml` and restart the runner to change what it offers.

## Driver binaries

The Docker backend copies a `gritz` binary into every container to run as the
driver, picking the one that matches the image's architecture:

- When the runner itself is a static Linux binary for that architecture, which
  the binary in the release archives is, it copies itself.
- Otherwise it reads `gritz-linux-amd64` or `gritz-linux-arm64` from the
  `prebuilt` directory: `GRITZ_PREBUILT_DIR` if set, otherwise `prebuilt` in the
  config directory.

`gritz download` fetches both binaries from the latest GitHub release into that
directory. You need it when the runner runs on macOS, or on a different
architecture from your images. The driver has to match the runner's release, so
download again after upgrading. The `ghcr.io/gritzapp/runner` image already
contains both.

The Lambda MicroVMs backend doesn't copy anything: the driver is built into the
MicroVM image (see [Backends](/operators/backends/#lambda-microvms)).