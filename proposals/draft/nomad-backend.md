# Nomad Backend for the Runner

Issue: https://github.com/icholy/gritz/issues/1146

## Problem

The runner's sandbox runtime is abstracted behind `backend.Backend`
(proposals/implemented/runner-backend-interface.md), but the only backend that runs
work on infrastructure an operator already owns is Docker — and Docker runs every
task on the **single host the runner is bound to**. That host caps concurrency
(`--concurrency`), and scaling out means running more runners and manually
sharding workspaces across them. There is no resource-aware placement, no
bin-packing, no node draining, and no way to spread tasks across a pool of
machines from one runner.

The other proposed backends don't fill this gap: Firecracker
(proposals/draft/firecracker-backend.md) is still single-host, and the AWS
options — Lambda MicroVMs (proposals/draft/lambda-microvm-backend.md) and
AgentCore (proposals/draft/agent-core-backend.md) — scale out only on
AWS-managed infrastructure with their own image pipelines, IAM, and billing.

Many teams already run a [HashiCorp Nomad](https://www.nomadproject.io/) cluster
as their general-purpose scheduler. This proposal adds a `nomad` backend that
lets a **single runner dispatch tasks across an existing Nomad cluster** —
resource-aware placement, bin-packing, and node draining on infrastructure the
operator already owns — using Nomad's built-in `docker` task driver so existing
workspace images run unchanged.

## Design

### Overview

A new package `internal/runner/backend/nomad` implements `backend.Backend` by
mapping each task to one **Nomad job** named `gritz-<task-id>`. The job has a
single group with a single task that runs the workspace image under Nomad's
`docker` task driver, executes `spec.Cmd` (the driver), and reports the driver's
exit code back through `backend.Wait`. Selection follows the existing seam:

```
gritz runner --backend nomad
```

The runner talks only to the **Nomad server API**; Nomad schedules the
allocation onto one of its **client nodes**. The orchestrator (`runner.Runner`),
the driver, the server API, and the database are untouched — the driver already
connects directly to the server with its task token and neither knows nor cares
what runtime launched it.

The decisive difference from every existing backend: the sandbox does not run on
the runner's machine. One `gritz runner --backend nomad` fronts a whole cluster,
and task capacity is the cluster's capacity, not one host's.

### Nomad API client

The backend uses `github.com/hashicorp/nomad/api` — the standalone, lightweight
client module (not the full `nomad` server tree). The client is constructed from
the environment, mirroring how the Docker backend uses `client.FromEnv`:

```go
cfg := api.DefaultConfig() // reads NOMAD_ADDR, NOMAD_TOKEN, NOMAD_NAMESPACE,
                           // NOMAD_CACERT, NOMAD_REGION, ...
client, err := api.NewClient(cfg)
```

No gritz-specific connection flags are introduced; operators point at their
cluster with the standard `NOMAD_*` variables, exactly as they do with the
`nomad` CLI. Namespace comes from `NOMAD_NAMESPACE` (resolved by
`api.DefaultConfig()`), with an optional per-workspace `nomad.namespace`
override for workspaces that must land in a specific namespace.

### Workspace config

Per the backend-interface pattern, each backend gets its own sibling config
section that it alone validates and consumes (Docker reads `container:`, Lambda
reads `lambda_microvm:`). `workspace.Workspace` gains a `nomad:` section:

```yaml
workspaces:
  pets-workshop:
    nomad:
      image: ghcr.io/icholy/gritz-workspace-debian:latest
      datacenters: [dc1]        # default ["*"]
      namespace: default        # optional; else runner/NOMAD_NAMESPACE default
      cpu_mhz: 2000             # Nomad resource reservation, default 1000
      memory_mb: 2048           # default 1024
      working_dir: /root
      user: ""                 # default image default
      privileged: false
      environment:
        CLAUDE_CODE_OAUTH_TOKEN: ${env:CLAUDE_CODE_OAUTH_TOKEN}
      constraints:              # optional extra placement constraints
        - attribute: '${meta.gpu}'
          value: "true"
      host_volumes:             # optional Nomad host-volume mounts
        - name: agent-cache
          dest: /cache
          read_only: false
    agent:
      type: claude
      ...
```

```go
type Nomad struct {
	Image       string            `yaml:"image"`
	Datacenters []string          `yaml:"datacenters"`
	Namespace   string            `yaml:"namespace"`
	CPUMHz      int               `yaml:"cpu_mhz"`
	MemoryMB    int               `yaml:"memory_mb"`
	WorkingDir  string            `yaml:"working_dir"`
	User        string            `yaml:"user"`
	Privileged  bool              `yaml:"privileged"`
	Environment map[string]string `yaml:"environment"`
	Constraints []NomadConstraint `yaml:"constraints"`
	HostVolumes []NomadHostVolume `yaml:"host_volumes"`
}

type NomadConstraint struct {
	Attribute string `yaml:"attribute"`
	Operator  string `yaml:"operator"` // default "="
	Value     string `yaml:"value"`
}

type NomadHostVolume struct {
	Name     string `yaml:"name"`
	Dest     string `yaml:"dest"`
	ReadOnly bool   `yaml:"read_only"`
}
```

`image` is the same OCI reference the Docker backend consumes — existing
workspace images run unmodified, since Nomad's `docker` driver pulls and runs
them the same way. A workspace may set both `container:` and `nomad:` so one
`workspaces.yaml` serves runners with different backends; `ValidateWorkspace`
checks only the `nomad:` section (`image` required, `cpu_mhz`/`memory_mb`
non-negative), and `RegisterWorkspaces` skips (with a warning) workspaces this
backend can't run.

Registry auth is Nomad's responsibility: the `docker` driver on each client node
uses that node's `docker.auth.config` / credential helper, the same way the
Docker backend relies on the daemon's `~/.docker/config.json`.

### Provisioning files into the container

The Docker backend copies the driver binary and agent config into the container
with `CopyToContainer` (a tar stream) *after* create. Nomad has no
post-placement file-copy API — the runner never touches the client node — so
files must travel **inside the jobspec**. The `docker` driver mounts the
allocation's `local/` directory into the container, and Nomad populates it from
the job before the container starts. The backend uses two mechanisms:

- **Agent config** (`spec.Files`, small JSON) → an inline **`template`** stanza
  per file, rendered into `local/`. Templates carry textual data natively.
- **Driver binary** (`backend.BinaryPath`, ~tens of MB) → an **`artifact`**
  stanza. Inlining a binary in the jobspec is infeasible (job size limits), so
  the runner serves it over HTTP and Nomad's client fetches it.

The runner starts a small read-only HTTP file server (bound to
`--nomad-artifact-addr`, default the runner's advertised address) that serves the
prebuilt driver binary per architecture from `prebuilt.ReadBinary`. The
`artifact` source interpolates the **node's** architecture, which Nomad resolves
at placement time — elegantly solving the arch-selection problem that the Docker
backend solves by inspecting the image and Firecracker solves via host arch:

```hcl
artifact {
  source      = "http://<runner-addr>/prebuilt/${attr.cpu.arch}"
  destination = "local/bin/gritz"
  mode        = "file"
}
```

The `docker` driver then places both into the container via `mounts` that map the
rendered `local/` paths onto the absolute paths the driver expects
(`backend.BinaryPath` and the agent config path), with the binary marked
executable. This keeps `Spec.Files` semantics intact: the backend delivers each
file to its declared absolute path, just through Nomad's staging directory
instead of a tar copy.

Reachability constraint (shared with the artifact server): the runner's
`--server` URL and `--nomad-artifact-addr` must be reachable **from the Nomad
client nodes**, the same way every backend requires the server URL to be
reachable from the sandbox.

### Job specification

Assembled per task with the `api` types (sketch):

```go
job := &api.Job{
	ID:          new("gritz-<task-id>"),
	Type:        new("batch"),
	Namespace:   new(ns),
	Datacenters: ws.Nomad.Datacenters,
	TaskGroups: []*api.TaskGroup{{
		Name:  new("agent"),
		Count: new(1),
		// No reschedule/restart: a task is bound 1:1 to its sandbox. A dead
		// alloc is a terminal outcome the runner observes, not something Nomad
		// silently retries elsewhere.
		ReschedulePolicy: &api.ReschedulePolicy{Attempts: new(0), Unlimited: new(false)},
		RestartPolicy:    &api.RestartPolicy{Attempts: new(0)},
		EphemeralDisk:    &api.EphemeralDisk{Sticky: new(true), Migrate: new(true)},
		Tasks: []*api.Task{{
			Name:   "agent",
			Driver: "docker",
			Config: map[string]any{
				"image":      ws.Nomad.Image,
				"command":    spec.Cmd[0],
				"args":       spec.Cmd[1:],
				"work_dir":   ws.Nomad.WorkingDir,
				"privileged": ws.Nomad.Privileged,
				"mounts":     mounts, // local/bin/gritz -> BinaryPath, config -> config path
			},
			Env:          envMap(ws.Nomad.Environment, spec.Env),
			User:         ws.Nomad.User,
			KillSignal:   "SIGTERM",
			KillTimeout:  new(30 * time.Second), // SIGTERM -> SIGKILL grace, matches Docker
			Templates:    configTemplates(spec.Files),
			Artifacts:    []*api.TaskArtifact{driverBinaryArtifact()},
			Resources:    &api.Resources{CPU: &cpu, MemoryMB: &mem},
			VolumeMounts: hostVolumeMounts(ws.Nomad.HostVolumes),
			Constraints:  constraints(ws.Nomad.Constraints),
		}},
	}},
}
```

`batch` type + zero reschedule/restart is deliberate: Nomad must not
resurrect or relocate a dead allocation behind the runner's back, because the
runner is the sole authority on the task↔sandbox binding and owns the terminal
event. `KillSignal`/`KillTimeout` reproduce the Docker backend's SIGTERM→30s→
SIGKILL so the driver gets the same graceful-stop window and owns its terminal
report.

### Backend method mapping

The `Handle` the runner persists is `{Type: "nomad", ID: "gritz-<task-id>"}`.
ID is the Nomad job ID — unique, stable for the task's life, and enough to
rediscover the current allocation via `client.Jobs().Allocations(jobID)`. `Data`
stays empty: like the Docker backend rediscovering everything from the container
id, the Nomad backend rediscovers the allocation from the job id, so nothing
backend-specific needs persisting.

| Method | Implementation |
|---|---|
| `ValidateWorkspace` | Validate the `nomad:` section: `image` required, resources non-negative, constraints well-formed. |
| `Launch` (fresh, `reuse == nil`) | Build the jobspec and `client.Jobs().Register(job)`. Return `Handle{Type:"nomad", ID: jobID}`. Nomad places the allocation asynchronously; `Wait`/`Probe` observe it. |
| `Launch` (reuse) | Look up the job by `reuse.ID`. If it is absent or purged → `backend.ErrGone`. Otherwise re-`Register` the (possibly updated) jobspec against the same job id so the task restarts in place; return the same handle. |
| `Probe` | `Jobs().Allocations(jobID)`; map the current alloc's client status → `StateRunning`; a `complete`/`failed` alloc → `StateExited`; job/alloc absent → `StateGone`. |
| `Signal` | Resolve the running alloc; `Allocations().Signal(alloc, "agent", "SIGTERM")`. Returns `(true, nil)` if a running alloc was signalled (driver owns the terminal report), `(false, nil)` if already terminal/gone. |
| `Destroy` | `Jobs().Deregister(jobID, purge=true, …)`. Idempotent: a 404 (job already gone) is not an error. |
| `Wait` | Resolve the alloc for `jobID`, then run blocking queries (`Allocations().Info` advancing `WaitIndex`) until the task's `TaskState` is `dead`. Read the exit code from its terminating `Terminated` event. Job/alloc vanished with no recoverable code → `ExitLost`. `ctx` cancel → `ctx.Err()` (`context.Canceled`); the job stays registered for next-boot rehydration. Safe to call after a runner restart — the alloc is re-resolved from the job id. |
| `Close` | Close the `api` client and stop the artifact HTTP server; leaves jobs running, exactly as containers outlive the runner today. |

Exit-code fidelity follows the backend contract: a missing or unreadable
terminating event is reported as `ExitLost` (-1), which the driver-owned-events
invariant treats as "report lost"; the server's terminal-state guard rejects a
spurious `failed` if the driver did report before the alloc died.

### CLI

```
gritz runner --backend nomad \
  [--nomad-artifact-addr http://<runner-ip>:<port>]
```

`--nomad-artifact-addr` has an `GRITZ_NOMAD_ARTIFACT_ADDR` env source.
Connection details (`NOMAD_ADDR`, `NOMAD_TOKEN`, `NOMAD_NAMESPACE`, TLS) all come
from the standard `NOMAD_*` variables via `api.DefaultConfig()` — the backend
introduces no flag for namespace or any other connection setting.
`internal/command/runner.go`'s backend switch gains a `nomad` case that
constructs `nomad.New(...)`.

### Testing

- Unit tests (no cluster): jobspec construction from a workspace (image, env,
  resources, mounts, templates, artifact source, kill signal/timeout),
  `ValidateWorkspace`, alloc-status → `backend.State` mapping, and exit-code
  extraction from a `TaskState` fixture.
- Integration tests in `backend/nomad`, skipped unless a `NOMAD_ADDR` is set —
  mirroring how the Docker e2e tests require a daemon. A `nomad agent -dev`
  instance covers register→place→run, graceful stop, exit-code propagation,
  `Destroy` idempotency, and re-attach after a simulated runner restart.
- The orchestrator needs no new tests: it already runs against `BackendMock`.

### What doesn't change

The orchestrator (`runner.go`), `EventQueue`, proto definitions, database
schema, driver, and task state machine are untouched. The Docker backend is
unaffected. `prebuilt` is reused as-is — its bytes are now served over HTTP for
Nomad clients to fetch instead of tar-copied into a local container.

## Trade-offs

**Filesystem persistence across restarts.** This is the central divergence from
every other backend. Docker adopts the *exact* container so its writable layer
survives a restart; Firecracker reuses `rootfs.ext4`; Lambda suspends/resumes the
VM. Nomad allocations are disposable — a restarted job gets a **fresh allocation
with a fresh container filesystem**, and `ephemeral_disk { sticky }` preserves
only the alloc's `local/`/`data/` dirs on the same node, *not* the container's
writable layer. So for an event-driven task that stops and later resumes, changes
the agent made to its working tree are not automatically present on restart. The
mitigation is to place the agent's working directory on a **sticky ephemeral
disk or a Nomad host volume** so the tree that matters persists across restarts,
while accepting that the rest of the container FS is rebuilt. Whether this is
"good enough" versus the exact-FS guarantee of the other backends is the key open
question below; it reflects Nomad's cattle-not-pets model, not an implementation
gap.

**Reusing `container:` vs. a dedicated `nomad:` section.** Most `container:`
fields (image, env, working_dir, user, privileged) map cleanly to the `docker`
driver, so reusing that section would avoid duplication. But it has no place for
Nomad-only concerns (datacenters, resource reservations, constraints, host
volumes), and the established pattern is one self-contained, backend-validated
section per backend (Docker, Lambda, Firecracker each have their own). A dedicated
`nomad:` section keeps `ValidateWorkspace` honest and one `workspaces.yaml`
portable across a heterogeneous fleet. The cost is repeating image/env/user for
workspaces that target multiple backends.

**Serving the driver binary over HTTP vs. baking it into the image.** Requiring
the workspace image to already contain the gritz binary at `backend.BinaryPath`
would drop the artifact server entirely and the client-reachability requirement
with it. But it forks the artifact pipeline (every workspace image needs a second
build target, kept in lockstep with the gritz version) and breaks portability
with the Docker/Firecracker backends, which inject the binary at runtime. Serving
`prebuilt` over HTTP keeps images backend-agnostic; the cost is a small runner
HTTP endpoint reachable from client nodes and node-arch interpolation in the
artifact source.

**One job per task vs. a dispatched parameterized job.** A single parameterized
"gritz-agent" job dispatched per task would centralize the jobspec, but dispatch
instances are awkward to address 1:1 for signal/stop/adopt and muddy the
`Handle.ID` ↔ sandbox identity that the backend contract depends on. One
named job per task keeps identity trivial (job id *is* the handle) and mirrors
the `gritz-<task-id>` container-naming convention exactly.

**Cluster scheduler vs. direct host control.** Unlike Docker/Firecracker, the
runner does not directly observe processes — it observes Nomad's *view* via the
API, and placement latency, node failures, and preemption are Nomad's to manage.
This is the point of the backend (offload placement to infrastructure the
operator already runs), but it means graceful-stop and liveness go through
blocking queries rather than a local daemon, and a client-node failure surfaces
as a lost allocation (`ExitLost`) rather than a clean exit code.

## Open Questions

1. **Working-tree persistence.** Is sticky ephemeral disk (best-effort,
   same-node) sufficient for event-driven tasks that resume, or should the
   backend *require* a named Nomad host volume / CSI volume for the agent cwd so
   restart-persistence is guaranteed rather than best-effort? Should the `nomad:`
   section make the persistent working dir a first-class field?
2. **Artifact server exposure.** Serving the driver binary means the runner must
   be reachable from every client node. Is a plain HTTP endpoint acceptable, or
   should it be gated (short-lived signed URLs, mTLS) given client nodes are
   already trusted to pull images and reach the server? Could the existing server
   host the prebuilt binaries instead, removing the per-runner endpoint?
3. **Namespaces / multi-tenancy.** Should each runner (or org) map to a Nomad
   namespace for quota and ACL isolation, and should that be derived rather than
   configured per workspace?
4. **Resource defaults.** `cpu_mhz`/`memory_mb` are hard reservations in Nomad;
   too low and agents OOM, too high and the cluster bin-packs poorly. What
   defaults, and should they be tunable per task rather than only per workspace?
5. **CSI / cross-node caches.** Host volumes are node-local; a task placed on a
   different node loses the cache. Is CSI volume support (network-attached,
   follows the alloc) worth the added configuration surface for shared
   dependency caches?