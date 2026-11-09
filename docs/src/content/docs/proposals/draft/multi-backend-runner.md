---
title: Multiple backends in one runner
description: A runner uses exactly one sandbox backend, so serving workspaces on Docker and on Lambda MicroVMs takes one runner per backend.
---

Issue: https://github.com/gritzapp/gritz/issues/2159

## Problem

`gritz runner --backend docker|lambda-microvm|local` picks one
[`backend.Backend`](https://github.com/gritzapp/gritz/blob/master/internal/runner/backend/sandbox.go)
for the whole process
([`internal/command/runner.go`](https://github.com/gritzapp/gritz/blob/master/internal/command/runner.go)),
and every workspace the runner registers runs on it. A workspace's YAML can
already carry both a `container:` and a `lambda_microvm:` section, but only so
that one `workspaces.yaml` can be shared by runners on different backends; which
section is used is decided by the runner, not by the workspace. Running some
workspaces in Docker and others in Lambda MicroVMs means deploying, operating
and sizing one runner per backend, each with its own runner ID, state DB and
registered workspace list.

Goal: one runner serves workspaces on more than one backend.

## Design

Three changes, each small:

1. **The runner enables a list of backends.** `--backend` becomes repeatable.
   Each enabled backend is constructed once, from the flags it already has.
2. **A workspace names its backend.** A new optional `backend:` field; empty
   means the runner's first enabled backend.
3. **The runner dispatches.** A `backend.Set` holds the enabled backends and is
   itself a `backend.Backend`: it routes `Create` by the spec's workspace and
   `Open` by the handle's type. The runner, `TaskManager` and `TaskRunner` keep
   talking to one `backend.Backend` and barely change.

There is no proto, server or database change: the server never sees backends,
and the runner store already persists each sandbox's `Handle.Type`
([`runnerstore/sandboxes.go`](https://github.com/gritzapp/gritz/blob/master/internal/runner/runnerstore/sandboxes.go)).

### Configuring backends: `--backend`

`--backend` changes from a `StringFlag` to a `StringSliceFlag`:

```bash
gritz runner --backend docker --backend lambda-microvm
GRITZ_BACKEND=docker,lambda-microvm gritz runner
```

- The default stays `docker`, so a runner started without the flag behaves as
  today.
- Order matters only for the first entry: it is the **default backend**, used
  by workspaces with no `backend:`.
- An unknown name or a name given twice is a startup error.
- Backend-specific flags are unchanged and configure their backend when it is
  enabled: `--lambda-microvm-region` for `lambda-microvm`, `--local-root` for
  `local` (still required when `local` is enabled). Each backend is
  constructed only if it is listed, so a Docker-less host can run
  `--backend lambda-microvm` as today.

One instance per backend type. A runner serves one AWS region and one Docker
daemon, as it does now; see [Trade-offs](#trade-offs).

### Choosing a backend: `workspaces.yaml`

`workspace.Workspace` gets a `Backend` field:

```yaml
workspaces:
  web:
    # no backend: runs on the runner's default (first) backend
    container:
      image: ghcr.io/gritzapp/workspace-debian:latest
    agent:
      type: claude
  batch:
    backend: lambda-microvm
    secrets:
      CLAUDE_CODE_OAUTH_TOKEN: ${env:CLAUDE_CODE_OAUTH_TOKEN}
    lambda_microvm:
      image_identifier: arn:aws:lambda:us-east-1:123456789012:microvm-image/gritz-driver
      execution_role: arn:aws:iam::123456789012:role/gritz-microvm
      egress_connector: arn:aws:lambda:us-east-1:aws:network-connector:aws-network-connector:INTERNET_EGRESS
    agent:
      type: claude
```

- `Workspace.Validate` rejects a `backend` that is not one of `docker`,
  `lambda-microvm` or `local`, so `gritz workspaces` catches a typo.
- At startup, after `LoadConfig`, the runner calls `Config.Select` with its
  enabled backends. It fills an empty `backend` with the default backend, and
  **drops** every workspace whose backend this runner does not enable, logging
  each one. A dropped workspace is not registered with the server, so users
  cannot create tasks in it against this runner. This keeps one
  `workspaces.yaml` shareable between runners: a Docker-only runner skips the
  `lambda-microvm` workspaces instead of registering workspaces it can't run.
  If nothing is left, startup fails, as an empty config does today.
- The runtime sections keep their meaning and their validation time: the
  selected backend's `ValidateWorkspace` checks its own section when a run
  starts, as today. A workspace may still carry both sections; only the
  selected backend's is used.

Explicit, not inferred: the backend is not guessed from which section a
workspace has. Both sections may be present, and an explicit field is one line
of YAML that reads unambiguously.

### Dispatch: `backend.Set`

A `Set` maps backend names to backends. Each name is the `HandleType` its
backend stamps on the handles it produces, which is what makes `Open`
dispatchable. To keep the two from drifting, the names become constants in
`workspace` (which every backend already imports) and each backend's
`HandleType` is defined as its constant.

| Method | Routes by |
| --- | --- |
| `ValidateWorkspace(ws)` | `ws.Backend` |
| `Create(ctx, spec)` | `spec.Workspace.Backend` |
| `Open(h)` | `h.Type` |
| `BinaryPath(ws)` | `ws.Backend` |
| `RunEnv(ws)` | `ws.Backend`; nil for a backend that is not a `RunEnver` |
| `Close()` | closes every backend, joining errors |

A workspace whose `Backend` the set doesn't hold can't reach the set:
`Config.Select` dropped it at startup. The set panics on one, naming the broken
invariant. `Open` of a handle whose type the set doesn't hold returns an error,
since that one is reachable (below).

`Handle.Type` stops being "informational only": its doc comment says it
selects the backend that opens the handle. Every backend already sets it, and
every backend's `Open` already rejects a foreign type.

#### `BinaryPath` takes the workspace

`Backend.BinaryPath()` has no workspace to route by, and the runner needs the
binary path before the sandbox exists (it goes into the agent config's MCP
server command, in
[`buildSpecs`](https://github.com/gritzapp/gritz/blob/master/internal/runner/spec.go)).
It becomes `BinaryPath(ws *workspace.Workspace) string`, matching
`RunEnver.RunEnv(ws)`. Docker and Lambda ignore `ws` and return
`backend.BinaryPath`; `local` returns its binary.

### Runner changes

- `internal/command/runner.go`: parse the `--backend` list, construct each
  enabled backend in the existing `switch` (moved into a loop), build a
  `backend.Set`, call `workspaces.Select`, and pass the set as
  `ServeOptions.Backend`. The startup log line lists the backends.
- `internal/runner`: `TaskManager.specs` calls `m.backend.BinaryPath(ws)`
  instead of `BinaryPath()`. Nothing else changes: `TaskManager` and
  `TaskRunner` see one `backend.Backend`, and tests and the e2e harness keep
  passing a single backend.
- Concurrency stays one pool: `--concurrency` caps the runner's sandboxes
  across all backends.

### Existing sandboxes

A task keeps its sandbox until it is archived, and the sandbox keeps the
backend that created it, because `Open` routes by the record's `Handle.Type`,
not by the workspace:

- **Upgrading a single-backend runner** changes nothing: its records already
  carry the type of the backend it keeps enabling by default.
- **Changing a workspace's `backend:`** applies to sandboxes created from then
  on, like a change to `container:`. A task with a live sandbox keeps it on the
  old backend. Its runs are built from the current workspace, so they get the
  new backend's run environment (`lambda_microvm.environment`, or none); the
  binary path is the same for Docker and Lambda.
- **Removing a backend from `--backend`** while it still has sandboxes makes
  their records unopenable. `Load` drops an unopenable record and logs it
  (today's behaviour for a runner restarted with a different `--backend`), and
  the sandbox is left for the operator to clean up. Archive those tasks before
  removing the backend.

### Docs

The operator pages describe one backend per runner and need editing in the
same PR as the runner wiring:

- `operators/backends`: the opening ("one backend, chosen with `--backend`")
  becomes "one or more backends"; the table's `--backend` row; the paragraph
  on a workspace having both sections becomes how `backend:` picks one, with
  the two-backend example above; the Lambda workspace example gains
  `backend: lambda-microvm`.
- `operators/runner` and `reference/cli`: `--backend` is repeatable, and the
  first is the default.
- `operators/workspaces-yaml`: a `backend` row in the field table, `backend`
  in "Changing a workspace", and "Checking a config" mentions that the
  backend name is checked.

### API

```go
package workspace // github.com/gritzapp/gritz/internal/runner/workspace

// new: backend names, the values of Workspace.Backend and --backend.
const (
	// BackendDocker is the Docker backend's name.
	BackendDocker = "docker"
	// BackendLambdaMicroVM is the Lambda MicroVMs backend's name.
	BackendLambdaMicroVM = "lambda-microvm"
	// BackendLocal is the local backend's name.
	BackendLocal = "local"
)

type Workspace struct {
	// Backend names the backend the workspace's sandboxes run on. Empty
	// means the runner's default backend until Config.Select fills it in.
	Backend string `yaml:"backend,omitempty"` // new
	// ... unchanged
}

// Select fills in an empty Backend with enabled[0] and removes every
// workspace whose Backend is not in enabled, returning the removed names.
func (c *Config) Select(enabled []string) (removed []string) // new
```

```go
package backend // github.com/gritzapp/gritz/internal/runner/backend

type Handle struct {
	// Type is the backend that produced this handle ("docker", ...). A Set
	// opens the handle with the backend of this name.
	Type string // doc changed: no longer informational only
	// ... unchanged
}

type Backend interface {
	// BinaryPath returns the path of the gritz binary inside the sandboxes
	// the backend creates for ws.
	BinaryPath(ws *workspace.Workspace) string // changed: takes ws
	// ... unchanged
}

// Set is a Backend that routes each call to one of several backends, by
// workspace for sandboxes it creates and by handle type for ones it opens.
type Set struct{ ... } // new

// NewSet returns a Set of backends, keyed by the name each stamps on its
// handles. It panics if backends is empty.
func NewSet(backends map[string]Backend) *Set // new

// ValidateWorkspace validates ws with its backend.
func (s *Set) ValidateWorkspace(ws *workspace.Workspace) error

// Create creates the sandbox with the backend of spec.Workspace.
func (s *Set) Create(ctx context.Context, spec *Spec) (Sandbox, error)

// Open opens h with the backend named by h.Type.
func (s *Set) Open(h Handle) (Sandbox, error)

// BinaryPath returns the binary path of ws's backend.
func (s *Set) BinaryPath(ws *workspace.Workspace) string

// RunEnv returns the run environment of ws's backend, if it has one.
func (s *Set) RunEnv(ws *workspace.Workspace) map[string]string

// Close closes every backend.
func (s *Set) Close() error
```

```go
package docker // also lambdamicrovm (BackendLambdaMicroVM) and local (BackendLocal)

// HandleType is the backend.Handle.Type this backend stamps on its handles.
const HandleType = workspace.BackendDocker // changed: defined from workspace

// BinaryPath returns backend.BinaryPath.
func (b *Backend) BinaryPath(*workspace.Workspace) string // changed: takes ws
```

**How it fits:** `gritz runner` constructs the enabled backends, wraps them in
`backend.NewSet`, calls `Config.Select` on the loaded workspaces, and hands the
set to `runner.Serve` as its `Backend`. `TaskManager.specs` calls
`ValidateWorkspace`, `RunEnv` and `BinaryPath` with the task's workspace;
`TaskRunner` calls `Create` with a spec carrying that workspace, and `Restore`
calls `Open` with a record's handle. The set forwards each call to the right
backend.

## Implementation Plan

1. **`BinaryPath` takes the workspace** — Delivers: `Backend.BinaryPath(ws)` in
   the interface, the three backends, the regenerated moq and the e2e harness;
   `TaskManager.specs` passes the workspace. A pure refactor. Depends on:
   nothing. Verifiable by: the existing unit and e2e tests passing unchanged.
2. **Workspace `backend:` field** — Delivers: the `workspace.Backend*`
   constants, each backend's `HandleType` defined from them,
   `Workspace.Backend` with validation of the name, and `Config.Select`.
   Nothing calls `Select` yet, so the field is accepted but has no effect.
   Depends on: nothing. Verifiable by: `LoadConfig` tests for an unknown name,
   and `Select` tests for defaulting and dropping.
3. **`backend.Set`** — Delivers: `NewSet` and its routing, and the `Handle.Type`
   doc change. Unused by the runner yet. Depends on: (1), (2). Verifiable by:
   unit tests with two `BackendMock`s stamping different handle types: `Create`
   and `ValidateWorkspace` reach the workspace's backend, `Open` reaches the
   handle's, an unknown handle type is an error, `RunEnv` is nil for a
   non-`RunEnver`, and `Close` closes both.
4. **Runner wiring and docs** — Delivers: repeatable `--backend`, construction
   of each enabled backend, the set and `Select` in `gritz runner`, and the
   operator and CLI docs edits listed above. Depends on: (3). Verifiable by:
   a runner started without `--backend` behaving as today (e2e suite); a
   runner with `--backend docker --backend lambda-microvm` registering both a
   Docker and a Lambda workspace and running a task in each; a Docker-only
   runner on the same `workspaces.yaml` skipping the Lambda workspace;
   `cd docs && pnpm build`.

## Trade-offs

- **A `backends:` section in `workspaces.yaml` with named instances** (say,
  `lambda-east` and `lambda-west`, or two Docker hosts) would allow several
  instances of one backend type per runner. Rejected for now: nothing asks for
  it, it needs a backend name distinct from the handle type (and so a new
  persisted field to route `Open`), and it moves backend config out of flags
  that already work. The design leaves room: `Workspace.Backend` would name an
  instance instead of a type.
- **Inferring the backend from the workspace's sections** needs no new field,
  but is ambiguous when a workspace has both, which is allowed and in use for
  shared files. An explicit field is clearer.
- **A `Set` that is not a `Backend`**, with the runner looking up a backend per
  task, would avoid changing `BinaryPath`'s signature, but every
  runner-package caller and test would have to learn about the set. Making the
  set a `Backend` keeps the change in the command and one signature.
- **Failing startup on a workspace for a backend the runner doesn't enable**,
  instead of dropping it, would be stricter, but would break the shared
  `workspaces.yaml` that the two-section layout exists for.
- **Per-backend concurrency limits** are left out: one `--concurrency` pool is
  what a runner has today. They can be added as a separate flag if a fast,
  elastic backend starves a host-bound one in practice.

## Open Questions

- Should a task whose live sandbox is on a different backend than its
  workspace now names fail its next run (or recreate the sandbox), instead of
  running on the old one with the new workspace's run environment? Keeping it
  matches how other workspace changes apply; the mismatch only arises when an
  operator changes `backend:` under live tasks.
- Now that each workspace's backend is known at startup, should the runner
  validate the runtime sections (`ValidateWorkspace`) at startup, failing fast,
  rather than when a run starts? It would also let `gritz workspaces` check
  them. Left as today to keep this change small.
