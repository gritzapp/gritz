# Multiple backends in one runner

Issue: https://github.com/gritzapp/gritz/issues/2159

## Problem

`gritz runner --backend docker|lambda-microvm|local` picks one
[`backend.Backend`](https://github.com/gritzapp/gritz/blob/master/internal/runner/backend/sandbox.go)
for the whole process
([`internal/command/runner.go`](https://github.com/gritzapp/gritz/blob/master/internal/command/runner.go)),
and every workspace the runner registers runs on it. Its settings are runner
flags too: `--lambda-microvm-region` and `--local-root`. Running some
workspaces in Docker and others in Lambda MicroVMs means deploying, operating
and sizing one runner per backend, each with its own runner ID, state DB and
registered workspace list.

Goal: one runner serves workspaces on more than one backend.

## Design

A runner can run every backend; each workspace says which one it uses.

1. **No backend flags.** `--backend`, `--lambda-microvm-region` and
   `--local-root` are removed.
2. **The workspace's section is its backend.** `container:` runs on Docker,
   `lambda_microvm:` on Lambda MicroVMs, and a new `local:` on the local
   backend. A workspace has exactly one of them.
3. **Backend settings live in the section.** The Lambda region comes from the
   section's ARNs; the local root is `local.root`. Docker keeps using the
   standard Docker environment (`DOCKER_HOST`, ...).
4. **Per-sandbox settings travel in the handle.** The region and the root are
   written into the sandbox's `Handle.Data`, so one backend instance per type
   serves every region or root, and `Open` reconnects after a restart without
   the workspace.
5. **The runner builds only the backends its workspaces use** and dispatches
   through a `backend.Set`, which is itself a `backend.Backend`: `Create`
   routes by the workspace's section, `Open` by the handle's type.

There is no proto, server or database change: the server never sees backends,
and the runner store already persists each sandbox's `Handle.Type` and
`Handle.Data`
([`runnerstore/sandboxes.go`](https://github.com/gritzapp/gritz/blob/master/internal/runner/runnerstore/sandboxes.go)).

### `workspaces.yaml`

```yaml
workspaces:
  web:
    container:
      image: ghcr.io/gritzapp/workspace-debian:latest
    agent:
      type: claude
  batch:
    secrets:
      CLAUDE_CODE_OAUTH_TOKEN: ${env:CLAUDE_CODE_OAUTH_TOKEN}
    lambda_microvm:
      image_identifier: arn:aws:lambda:us-east-1:123456789012:microvm-image/gritz-driver
      execution_role: arn:aws:iam::123456789012:role/gritz-microvm
      egress_connector: arn:aws:lambda:us-east-1:aws:network-connector:aws-network-connector:INTERNET_EGRESS
    agent:
      type: claude
  dev:
    local:
      root: /tmp/gritz
    agent:
      type: claude
```

`workspace.Workspace` changes:

- `Container` becomes `*Container`, so a missing section is detectable, like
  `LambdaMicroVM` already is.
- A new `Local *Local` section, with one field, `root`: the directory holding
  the local backend's sandboxes. It is required and made absolute at load.
- `Workspace.Backend()` returns the backend name of the section that is set.
- `Workspace.Validate` requires **exactly one** section; none or more than one
  is a load error naming the workspace.

#### Validation moves to load time

Each backend validated its own section when a run started, through
`Backend.ValidateWorkspace`, only because one file could describe workspaces
for backends the runner didn't use. That reason is gone, and every check is
pure config, so the checks move into the sections' `Validate` methods, called
from `Workspace.Validate`, and `Backend.ValidateWorkspace` is removed:

- `Container.Validate`: `image` is required (unchanged).
- `LambdaMicroVM.Validate`: today's required fields and duration bound, plus
  the region rule below.
- `Local.Validate`: `root` is set, and short enough for the drivers' socket
  paths (the check `local.New` makes today).

A bad section now fails the runner's start, and `gritz workspaces` reports it.

#### The Lambda region comes from the ARNs

The `lambda_microvm.region` field is removed. `image_identifier` must be an ARN
(it is documented as one), and its region field is the workspace's region:
`LambdaMicroVM.Region()` returns it. `Validate` rejects an `image_identifier`
that doesn't parse as an ARN, and an `egress_connector` or `ingress_connector`
whose ARN names another region. The default ingress connector is built from
this region, as it is from the runner's today.

A runner can therefore serve workspaces in several regions.

### Backends

#### Settings carried per sandbox

A sandbox's region or root has to be known in two places: at `Create`, from the
workspace, and at `Open`, from a record written by an earlier process, when the
workspace may have changed. Two ways to get it to `Open`:

- **One backend instance per distinct value**, keyed by type and region or
  root. `Open` still needs to know which instance owns a handle, so the value
  would have to be in the handle anyway, plus a second level of dispatch.
- **The value in the handle's `Data`**, which is backend-defined, persisted,
  and never read by the runner. One instance per type; it reads the value from
  the workspace at `Create` and from `Data` at `Open`.

The second is simpler and is the design. Records written before this change
have no region or root in `Data`; per the no-backwards-compatibility rule,
`Open` rejects them and `Load` drops them like any unopenable record (see
[Existing sandboxes](#existing-sandboxes)).

#### Docker

Unchanged but for the interface changes below. Handles stay `{ID: container id}`.

#### Lambda MicroVMs

- `sandboxData` gains `Region`, next to `Endpoint`. `Create` writes the
  workspace's region; `Open` requires it.
- `Options.Cloud Cloud` becomes `Options.Cloud func(region string) Cloud`, and
  the backend keeps one client per region it has used, made on first use. The
  command passes a function returning
  `lambdamicrovms.NewFromConfig(awsCfg, withRegion(region))`; tests return
  their fake. `Options.Region` is removed.
- Credentials still resolve through the standard AWS SDK chain on the runner,
  loaded once, only when some workspace has `lambda_microvm:`.

#### Local

- `sandboxData{Root}` is new: `Create` writes the workspace's `local.root`
  and creates it if missing; `Open` requires it. The handle ID stays the task
  ID, and the sandbox directory is `<root>/<task-id>`.
- `Options.Root` is removed; `local.New` keeps `Binary`, `Environ` and `Log`.
  Its per-sandbox locks are keyed by directory instead of handle ID, since two
  roots can hold the same task ID's directory.

#### `BinaryPath` takes the workspace

The runner needs the binary path before the sandbox exists (it goes into the
agent config's MCP server command, in
[`buildSpecs`](https://github.com/gritzapp/gritz/blob/master/internal/runner/spec.go)),
so a dispatching backend needs the workspace to route it.
`Backend.BinaryPath()` becomes `BinaryPath(ws *workspace.Workspace) string`,
matching `RunEnver.RunEnv(ws)`. Docker and Lambda return `backend.BinaryPath`;
local returns its binary.

### Dispatch: `backend.Set`

A `Set` maps backend names to backends. A name is both the `HandleType` its
backend stamps on its handles and the value `Workspace.Backend()` returns. To
keep the two from drifting, the names are constants in `workspace` (which
every backend already imports), and each backend's `HandleType` is defined as
its constant.

| Method | Routes by |
| --- | --- |
| `Create(ctx, spec)` | `spec.Workspace.Backend()` |
| `Open(h)` | `h.Type` |
| `BinaryPath(ws)` | `ws.Backend()` |
| `RunEnv(ws)` | `ws.Backend()`; nil for a backend that is not a `RunEnver` |
| `Close()` | closes every backend, joining errors |

The set is built from the same workspaces the runner serves, so a workspace
whose backend isn't in it is unreachable: the set panics, naming the broken
invariant. `Open` of a handle whose type the set doesn't hold returns an error,
since that one is reachable (below).

`Handle.Type` stops being "informational only": its doc comment says it
selects the backend that opens the handle. Every backend already sets it, and
every backend's `Open` already rejects a foreign type.

### Runner changes

- `internal/command/runner.go`: drops the three flags; after `LoadConfig`, it
  builds one backend for each name in `Config.Backends()`: the Docker client,
  the AWS config and Lambda backend, or the local backend with
  `os.Executable()`. It wraps them in `backend.NewSet` and passes the set as
  `ServeOptions.Backend`. A Docker-less host works as long as no workspace has
  `container:`. The startup log line lists the backends.
- `internal/runner`: `TaskManager.specs` drops its `ValidateWorkspace` call and
  calls `m.backend.BinaryPath(ws)`. Nothing else changes: `TaskManager` and
  `TaskRunner` see one `backend.Backend`, and unit tests keep passing a single
  mock.
- Concurrency stays one pool: `--concurrency` caps the runner's sandboxes
  across all backends.

### Existing sandboxes

A task keeps its sandbox until it is archived, and the sandbox keeps the
backend, region and root it was created with, because `Open` routes by the
record's `Handle.Type` and reads the rest from `Handle.Data`, not from the
workspace.

- **Moving a workspace to another section** applies to sandboxes created from
  then on, like any change to `container:`. A task with a live sandbox keeps it
  on the old backend; its runs are built from the current workspace, so they
  get the new backend's run environment.
- **A record whose backend the runner no longer builds** (no workspace uses it
  any more), and a Lambda or local record from before this change, can't be
  opened. `Load` drops it and logs it, as it does for a runner restarted with
  a different `--backend` today, and the sandbox is left for the operator.
  Archive those tasks before the upgrade or the config change.

### e2e harness

[`internal/e2e`](https://github.com/gritzapp/gritz/blob/master/internal/e2e/backend.go)
wraps a `local.Backend` and passes it to `runner.Serve` directly, which still
works: it doesn't need a `Set`. Its changes:

- `newBackend` no longer passes `Root` to `local.New`; the harness keeps its
  temporary root, and `newRunner`'s `DefaultWorkspace` gets
  `Local: &workspace.Local{Root: root}`, so sandboxes land where `destroy` and
  `Kill` look for them.
- `Backend` implements `BinaryPath(ws)` and drops `ValidateWorkspace`.

### Docs

Edited in the same PR as the runner wiring:

- `operators/backends`: the opening ("one backend, chosen with `--backend`")
  becomes "each workspace's section picks its backend; a runner runs every
  backend its workspaces use". The table's `--backend` row becomes the
  section row; the paragraph on a workspace having both sections goes. The
  Lambda part loses `--backend lambda-microvm` and `--lambda-microvm-region`,
  says the region is the image ARN's, and drops `region` from the field table.
- `operators/runner` and `reference/cli`: the three flag rows go. The CLI
  page's note on `--backend local` becomes a pointer to the `local:` section.
- `operators/workspaces-yaml`: the field table says a workspace has exactly one
  of `container`, `lambda_microvm` and `local`; a short `local` section
  (`root`, unisolated, for tests and development); "Checking a config" says the
  section is checked too.
- `internal/runner/backend/lambdamicrovm/README.md`: the region paragraph.

### API

```go
package workspace // github.com/gritzapp/gritz/internal/runner/workspace

// new: backend names, the values of Workspace.Backend and Handle.Type.
const (
	// BackendDocker is the Docker backend's name.
	BackendDocker = "docker"
	// BackendLambdaMicroVM is the Lambda MicroVMs backend's name.
	BackendLambdaMicroVM = "lambda-microvm"
	// BackendLocal is the local backend's name.
	BackendLocal = "local"
)

type Workspace struct {
	Container     *Container     `yaml:"container,omitempty"` // changed: pointer
	LambdaMicroVM *LambdaMicroVM `yaml:"lambda_microvm,omitempty"`
	Local         *Local         `yaml:"local,omitempty"` // new
	// ... unchanged
}

// Backend returns the name of the backend whose section the workspace sets.
func (w *Workspace) Backend() string // new

// Backends returns the distinct backends the config's workspaces use.
func (c *Config) Backends() []string // new

type LambdaMicroVM struct {
	// Region string // removed
	// ... unchanged
}

// Region returns the AWS region of the section's image_identifier ARN.
func (l *LambdaMicroVM) Region() string // new

// Validate reports whether the section can launch a MicroVM.
func (l *LambdaMicroVM) Validate() error // new: was lambdamicrovm.Backend.ValidateWorkspace

// Local holds the local backend's config for a workspace.
type Local struct {
	// Root holds one directory per sandbox, named after its task ID.
	Root string `yaml:"root"`
} // new

// Validate reports whether Root is set and short enough for socket paths.
func (l *Local) Validate() error // new
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
	// ValidateWorkspace(ws *workspace.Workspace) error // removed

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
package lambdamicrovm // github.com/gritzapp/gritz/internal/runner/backend/lambdamicrovm

// HandleType is the backend.Handle.Type this backend stamps on its handles.
const HandleType = workspace.BackendLambdaMicroVM // changed: defined from workspace

type Options struct {
	// Cloud returns the control-plane client for a region; the backend calls
	// it once per region it uses.
	Cloud func(region string) Cloud // changed: was Cloud
	// Region string // removed
	// ... unchanged
}
```

```go
package local // github.com/gritzapp/gritz/internal/runner/backend/local

// HandleType is the backend.Handle.Type of local sandboxes.
const HandleType = workspace.BackendLocal // changed: defined from workspace

type Options struct {
	// Root string // removed: comes from the workspace's local.root
	// ... unchanged
}
```

`docker.HandleType` is likewise defined as `workspace.BackendDocker`, and all
three backends take `BinaryPath(ws)` and drop `ValidateWorkspace`.

**How it fits:** `gritz runner` loads the workspaces, builds a backend for each
name in `Config.Backends()`, wraps them in `backend.NewSet`, and hands the set
to `runner.Serve` as its `Backend`. `TaskManager.specs` calls `RunEnv` and
`BinaryPath` with the task's workspace; `TaskRunner` calls `Create` with a spec
carrying that workspace, and `Restore` calls `Open` with a record's handle. The
set forwards each call to the right backend, which reads the region or root
from the workspace at `Create` and from the handle at `Open`.

## Implementation Plan

1. **`BinaryPath` takes the workspace** — Delivers: `Backend.BinaryPath(ws)` in
   the interface, the three backends, the regenerated moq and the e2e harness;
   `TaskManager.specs` passes the workspace. A pure refactor. Depends on:
   nothing. Verifiable by: the existing unit and e2e tests passing unchanged.
2. **Section validation at load time** — Delivers: `Container` as a pointer,
   the `workspace.Backend*` constants with each `HandleType` defined from them,
   `Workspace.Backend()`, `Config.Backends()`, the exactly-one-section rule,
   and `LambdaMicroVM.Validate` (with the ARN region rule and `Region()`, the
   `region` field removed). `Backend.ValidateWorkspace` and its call in
   `TaskManager.specs` are removed; the Lambda backend's check that the
   workspace is in the runner's region moves into its `Create` until (3).
   Depends on: nothing. Verifiable by: `LoadConfig` tests for none, both, and
   an invalid section; `gritz workspaces` on a bad file.
3. **Per-sandbox Lambda region** — Delivers: `Region` in the Lambda handle's
   `Data`, `Options.Cloud` as a per-region function with cached clients, and
   `Options.Region` and `--lambda-microvm-region` removed, with their docs.
   `gritz runner` passes the function. Depends on: (2). Verifiable by: backend tests with a fake per region: `Create` in
   two regions reaches two fakes, `Open` of each handle reaches its region's,
   a handle without a region is an error.
4. **Local section and per-sandbox root** — Delivers: `workspace.Local` and its
   validation, `Root` in the local handle's `Data`, `Options.Root` removed,
   locks keyed by directory, and the e2e harness's `DefaultWorkspace` carrying
   `local.root`. `--local-root` is removed, with its docs; `--backend local`
   stays until (5). Depends on: (2). Verifiable by: `local`
   tests across two roots; the e2e suite.
5. **`backend.Set`, runner wiring and docs** — Delivers: `NewSet` and its
   routing, the `Handle.Type` doc change, `gritz runner` building backends from
   `Config.Backends()`, `--backend` removed, and the rest of the docs edits
   listed above. Depends on: (1), (3), (4). Verifiable by: `Set` unit tests with two
   `BackendMock`s stamping different handle types (`Create` and `BinaryPath`
   reach the workspace's backend, `Open` the handle's, an unknown handle type
   is an error, `Close` closes both); a runner with one Docker and one Lambda
   workspace registering both and running a task in each; a runner with only
   Lambda workspaces starting on a host without Docker;
   `cd docs && pnpm build`.

## Trade-offs

- **A `backend:` field with a runner-level list of enabled backends** keeps
  sections and backend choice separate, so one workspace could carry both
  sections. Sharing a workspace across backends isn't a use case, and the field
  brings a default backend, enable flags and dropping workspaces a runner
  can't serve. The section alone is enough.
- **One backend instance per region or root** was set aside for carrying the
  value in the handle: `Open` needs the value either way, and with it in the
  handle, no second dispatch level is needed.
- **A `backends:` section in `workspaces.yaml`** for backend settings (a
  Docker host per backend, AWS profiles) would allow several instances of one
  type. Nothing asks for it; Docker's environment and the AWS SDK chain cover
  today's settings.
- **A `Set` that is not a `Backend`**, with the runner looking up a backend per
  task, would avoid changing `BinaryPath`'s signature, but every
  runner-package caller and test would have to learn about the set. Making the
  set a `Backend` keeps the change in the command and one signature.
- **Per-backend concurrency limits** are left out: one `--concurrency` pool is
  what a runner has today. They can be added as a separate flag if a fast,
  elastic backend starves a host-bound one in practice.

## Open Questions

- Should a task whose live sandbox is on a different backend than its
  workspace now uses fail its next run (or recreate the sandbox), instead of
  running on the old one with the new workspace's run environment? Keeping it
  matches how other workspace changes apply, and the case only arises when an
  operator moves a workspace between sections under live tasks.