---
title: Secrets
description: Declare credentials in a workspace's secrets map so gritz masks them in the task log it ships to the server, and what is masked where.
sidebar:
  order: 6
---

A task's driver log is shipped to the server, where anyone in the organisation
can read it with `gritz logs` and in the Web UI, long after the sandbox is gone.
Credentials that reach that log would be stored with it. Declare them in the
workspace's `secrets:` map and gritz keeps them out of the shipped copy:

```yaml
workspaces:
  pets-workshop:
    secrets:
      GH_TOKEN: ${sh:gh auth token}
    commands:
      - git clone https://x-access-token:${GH_TOKEN}@github.com/private/repo.git
```

## How secrets work

- Each entry becomes an environment variable in the sandbox, exactly like a
  `container.environment` entry, for the setup commands and the agent. Values go
  through the same [`${env:}`/`${sh:}` expansion](/operators/workspaces-yaml/#variable-expansion)
  on the runner. To protect a credential that is in `environment:` today, move it
  to `secrets:`; nothing else about it changes.
- Secrets work the same on every backend. They travel to the driver with each
  run, not in the sandbox's own config, so a sandbox reused for a later run never
  holds stale values.
- In the log shipped to the server, every occurrence of a declared value is
  replaced by `[gritz:masked NAME]`, such as `[gritz:masked GH_TOKEN]`. That
  includes values echoed by a setup command's output, `set -x` traces or an
  `env` dump.
- The task's own token, `GRITZ_TOKEN`, is masked the same way, as
  `[gritz:masked token]`.

## Reference secrets by variable

Reference secrets from `commands:` by variable, as `${GH_TOKEN}`. The runner only
expands `${namespace:value}`, so it leaves `${GH_TOKEN}` for the sandbox's shell
to expand when the command runs. The command line the driver logs then carries
the name rather than the value.

Writing `${env:GH_TOKEN}` in a command would put the value into the command line
itself. The mask still catches it, but the name is the clearer record.

## What is masked where

| Where | Masked |
| --- | --- |
| The log shipped to the server: `gritz logs` and the Web UI | Yes |
| `/gritz/log` inside the sandbox | No |
| The sandbox's stderr (`docker logs`) | No |

The raw copies stay inside the boundary the secrets already live in: anyone who
can read them can read the sandbox's environment too. Full fidelity there is what
makes post-mortems with [`gritz shell`](/operators/debugging/#a-shell-in-the-sandbox)
useful.

## Limits

gritz doesn't detect credentials: it masks exactly the values you declare.

- An undeclared credential ships unmasked.
- A secret that another tool shortens before it reaches the log, such as a long
  value cut off in a tool-call summary, arrives as a fragment the mask can't
  match.
- A value that is encoded on its way to the log, such as base64 in an HTTP
  header dump, no longer matches the declared value.

Masking matches the longest declared value at each position, so a secret that is
a prefix of another can't leave part of the longer one exposed. A value split
across two writes to the log is still masked.
