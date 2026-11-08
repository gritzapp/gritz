---
title: "Runner Backend Interface"
description: "The runner is hard-wired to Docker through a client field and direct API calls, so supporting other runtimes such as Kubernetes or Firecracker would require rewriting orchestration logic."
---

Issue: TBD

## Problem

The runner (`internal/runner/`) turns task commands into running agent sandboxes, and today it only knows how to do that with Docker. The Docker client is a direct field on `runner.Runner`, and Docker API calls — container create/start/kill/list/remove, image pulls, the `die` event stream, network repair, tar-based file injection — are spread across `Poll`, `Start`, `Kill`, `Monitor`, `Reconcile`, `Prune`, and the `containerbuild` package.

We want to be able to run task sandboxes on other runtimes — Kubernetes, AWS ECS, Firecracker microVMs, Bedrock AgentCore — without rewriting the runner's orchestration logic. Implementing those backends is out of scope here; this proposal defines the seam: an interface the current Docker implementation moves behind, that other runtimes could plausibly implement.

Two prior changes make this seam clean:

- **Socket proxy elimination** (proposals/implemented/eliminate-runner-socket-proxy.md): the driver and the injected MCP server connect directly to the server with a server-minted task JWT. The runner no longer shares a Unix socket — or any filesystem — with the sandbox, so a sandbox no longer has to be co-located with the runner.
- **Driver-owned events** (proposals/implemented/driver-owned-events.md): the driver reports `started`/`stopped`/`failed` itself. The only thing the runner needs from the runtime is "the sandbox for task X exited, zero or non-zero".

## Design

### The seam

What the runner does today splits cleanly into two layers:

**Backend-agnostic orchestration** (stays in `runner.Runner`):

- `Poll` and command dispatch (start / stop / restart state machine)
- Concurrency control (`safesem.Semaphore`) and wake-up signalling
- Event buffering and retry (`EventQueue`)
- Task token minting (`CreateTaskToken` RPC, `runner.go:386`)
- Agent config construction, including the injected `gritz` MCP server (`runner.go:416-436`)
- Reconcile policy (an exited sandbox whose task is still `running` → `failed`)
- Prune policy (remove sandboxes whose tasks are archived or deleted)
- Workspace registration

**Sandbox mechanics** (moves behind the interface):

- Find-or-create a container for a task (`find` at `runner.go:313`, `create` at `runner.go:376`, `containerbuild.Builder`)
- Image pull with registry auth (`dockerx.ImageEnsure`)
- Architecture detection and driver binary injection (`prebuilt.ReadBinary`, tar copy)
- SIGTERM → 30s → SIGKILL escalation (`Kill`, `runner.go:341`)
- Exit observation (`Monitor`'s docker `die` event stream, `runner.go:512`)
- Listing and removing containers by label
- Network repair (`dockerx.RepairNetworks`, `runner.go:486`)

### The `backend.Backend` interface

New package `internal/runner/backend`:

```go
package backend

// File is a file provisioned into the sandbox before it starts.
type File struct {
	Path string // absolute path inside the sandbox
	Data []byte
	Mode int64
	Dir  bool
}

// Spec describes the sandbox for a single task. The orchestrator owns
// everything in it; backends interpret the workspace's runtime-specific
// config section (container: for Docker) and provision the rest.
type Spec struct {
	TaskID    int64
	Workspace *workspace.Workspace
	Cmd       []string // driver invocation
	Env       []string // GRITZ_TASK_ID / GRITZ_TOKEN / GRITZ_SERVER
	Files     []File   // agent config; the driver binary is the backend's job
}

type State int

const (
	StateUnknown State = iota
	StateRunning
	StateExited
)

// Sandbox is a point-in-time view of a task's sandbox.
type Sandbox struct {
	TaskID int64
	State  State
}

// Exit reports that a task's sandbox exited. ExitCode carries the
// driver-owned-events invariant: 0 means the driver reported its outcome,
// non-zero means the report was lost.
type Exit struct {
	TaskID   int64
	ExitCode int
}

// Backend runs task sandboxes on a concrete runtime (Docker today;
// Kubernetes, ECS, Firecracker, AgentCore later).
type Backend interface {
	// Start ensures a sandbox exists for spec.TaskID and starts it. It may
	// reuse a previous sandbox (and its filesystem) for the same task; the
	// driver tolerates both fresh and reused filesystems via its
	// SetupCommandsCompleted marker.
	Start(ctx context.Context, spec *Spec) error

	// Stop gracefully stops the task's sandbox, escalating to a hard kill
	// after a backend-chosen grace period. It reports whether a running
	// sandbox was signalled — in that case the driver owns the terminal
	// event report.
	Stop(ctx context.Context, taskID int64) (signalled bool, err error)

	// Running reports whether the task's sandbox is currently running.
	Running(ctx context.Context, taskID int64) (bool, error)

	// List returns all sandboxes owned by this runner.
	List(ctx context.Context) ([]Sandbox, error)

	// Remove deletes the task's sandbox and its associated state.
	Remove(ctx context.Context, taskID int64) error

	// Watch invokes handle for every sandbox exit this backend observes,
	// at most once per exit, until ctx is cancelled or the underlying
	// stream fails. Missed exits (e.g. while the runner is down) are
	// covered by the orchestrator's Reconcile, not by Watch.
	Watch(ctx context.Context, handle func(Exit)) error

	Close() error
}
```

The interface is deliberately coarse and intent-level. It does not expose create-vs-start phases, file copies into existing sandboxes, image management, or signals — those are runtime mechanics that differ too much across the target runtimes (ECS tasks have no create-then-start; a Kubernetes pod can't receive files after creation; Firecracker has no image pull).

### Mapping the current code

| Today (`internal/runner/runner.go`) | After |
|---|---|
| `find` (313), `create` (376-467), `Start` (469-505), `containerbuild.Builder` | `docker.Backend.Start` — find-or-create by label, `ImageEnsure`, arch-matched `prebuilt.ReadBinary`, tar copy of `spec.Files` + driver binary, `RepairNetworks` on reuse, `ContainerStart` |
| `Kill` (341-374) | `docker.Backend.Stop` — SIGTERM, 30s, SIGKILL; same `signalled` semantics |
| `isRunning` (327-336) | `docker.Backend.Running` |
| `Monitor`'s event stream (513-520) | `docker.Backend.Watch` — docker `die` events filtered by runner label |
| `ContainerList` in `Reconcile` (247-273) and `Prune` (558-565) | `docker.Backend.List` |
| `ContainerRemove` in `Prune` (589) | `docker.Backend.Remove` |
| docker client ownership, `Close` (113-115) | `docker.Backend` constructor / `Close` |

`runner.Runner` keeps its public methods (`Poll`, `Monitor`, `Reconcile`, `Prune`, `Start`, `Kill`) with the same orchestration logic, but their bodies call the backend:

- `Poll`'s three command branches are unchanged except `r.Kill` → `r.backend.Stop`, `r.isRunning` → `r.backend.Running`, and `r.Start` → build spec + `r.backend.Start`.
- `Monitor` becomes a thin wrapper: `r.backend.Watch(ctx, handle)` where `handle` keeps today's logic verbatim — release a semaphore slot, wake the poll loop, emit nothing on exit 0, enqueue `failed` on non-zero (`runner.go:524-544`).
- `Reconcile` calls `List`, counts `StateRunning` for `sem.Set`, and applies today's lost-report policy to `StateExited` entries.
- `Prune` calls `List`, applies today's archived-task policy, and calls `Remove`.

Building the spec replaces the first half of today's `create`:

```go
func (r *Runner) spec(ctx context.Context, task *model.Task) (*backend.Spec, error) {
	ws, err := r.workspaces.Get(task.Workspace)
	// ... CreateTaskToken RPC (unchanged, runner.go:386-393)
	// ... ws.AgentConfig() + gritz MCP server injection (unchanged, runner.go:416-432)
	return &backend.Spec{
		TaskID:    task.ID,
		Workspace: ws,
		Cmd:       []string{"/usr/local/bin/gritz", "driver", "--server", r.serverURL, ...},
		Env:       []string{...},
		Files: []backend.File{
			{Path: path.Dir(agent.ConfigPath(task.ID)), Mode: 0777, Dir: true},
			{Path: agent.ConfigPath(task.ID), Data: cfgData, Mode: 0666},
		},
	}, nil
}
```

One behavioral nit: today the token is only minted when a container is created; with find-or-create inside the backend, the orchestrator mints one on every `Start`. The extra `CreateTaskToken` RPC on restart-with-reuse is cheap, and the reused container keeps running on its original token exactly as it does today.

### Driver binary provisioning is the backend's job

`spec.Cmd` references `/usr/local/bin/gritz`; making that binary present and executable is part of the backend's `Start` contract, not a `File` in the spec. Two reasons:

1. **Architecture selection is runtime-specific.** The Docker backend learns the arch from `ImageEnsure`'s inspect result and reads the matching prebuilt binary. A Kubernetes backend can't know node architecture when building a pod spec.
2. **Injection mechanics are runtime-specific.** Docker copies a tar into the created container. Kubernetes/ECS sandboxes will need a different strategy — the binary baked into the workspace image, or a bootstrap/init step that downloads it (plausibly from the server; see Open Questions).

The `prebuilt` package stays as-is and becomes a dependency of the Docker backend.

### Exit reporting contract

`Exit.ExitCode` carries the driver-owned-events invariant, so every backend must be able to surface the driver process's exit status: Docker has it in `die` event attributes, Kubernetes in `containerStatuses[].state.terminated.exitCode`, ECS in `DescribeTasks` container exit codes, Firecracker from the supervising process.

A backend that cannot observe exit codes can degrade to always reporting non-zero: after a successful run the driver's terminal ack has already moved the task to a terminal status, so the spurious `failed` is rejected by the status guard in `internal/model/task.go` — and when the report really was lost, `failed` is the honest outcome. Correctness comes from the state machine, not from backend fidelity.

`Watch` is shaped as a blocking call with a callback rather than a channel so poll-based backends (ECS `DescribeTasks`, AgentCore) fit as naturally as stream-based ones (docker events, Kubernetes watch). The existing restart-on-error loop in `internal/command/runner.go:147-158` is unchanged.

### Workspace config

No schema changes. The `container:` section of `workspaces.yaml` is reinterpreted as *the Docker backend's config section*; `agent:`, `commands:`, and `capabilities:` are backend-agnostic. Future backends add sibling sections (`kubernetes:`, `ecs:`, ...) to `workspace.Workspace`, and a workspace configures only the section for the backend its runner uses. The `container.image is required` validation stays where it is until a second backend exists (see Open Questions).

### CLI

```
gritz runner --backend docker
```

A new `--backend` flag (env `GRITZ_BACKEND`, default `docker`) selects the implementation. `internal/command/runner.go` constructs the backend and passes it in:

```go
be, err := dockerbackend.New(dockerbackend.Options{RunnerID: runnerID, Log: log})
// ...
r, err := runner.New(runner.Options{
	Client:    client,
	ServerURL: serverAddr,
	Backend:   be,
	// ...
})
```

`runner.New` drops its Docker client construction (`runner.go:77-80`); `Runner.Close` delegates to `backend.Close`. Until a second backend exists the flag accepts only `docker`, but the construction seam is established.

### Package layout

```
internal/runner/
├── runner.go               backend-agnostic orchestrator
├── eventqueue.go           unchanged
├── backend/
│   ├── backend.go          Backend interface, Spec, File, Sandbox, Exit
│   └── docker/
│       └── docker.go       Docker implementation (absorbs containerbuild
│                           and the Docker halves of runner.go)
├── prebuilt/               unchanged (consumed by the Docker backend)
└── workspace/              unchanged
```

`internal/runner/containerbuild` is absorbed into `backend/docker`. `internal/x/dockerx` stays as low-level helpers used only by the Docker backend.

### Testing

`runner_test.go` currently mocks the server (`gritzclient.ClientMock`) but requires a real Docker daemon and the prebuilt binaries. With the seam in place:

- The orchestrator (`Poll` dispatch, reconcile/prune policy, monitor handling, semaphore accounting) gets unit tests against a moq-generated `BackendMock` — same pattern as `gritzclient.ClientMock` and the existing `dockerx` moq interfaces — with no Docker dependency.
- The existing end-to-end tests move to `backend/docker` (and stay as the integration tests for `runner` wired to the real Docker backend).

### No proto, schema, or driver changes

The server API, database schema, driver (`internal/agent/driver.go`), task state machine, and event system are untouched. The driver already connects to the server by URL + token and neither knows nor cares what runtime launched it — that was the point of the two prerequisite changes.

## Trade-offs

**Coarse find-or-create `Start` vs. fine-grained lifecycle methods.** A CRI-style interface (`Create`/`Start`/`CopyFiles`/`EnsureImage`/`Wait`) was considered and rejected: it forces every runtime to emulate Docker's lifecycle phases, several of which don't exist elsewhere (ECS has no create-then-start; Kubernetes can't copy files into a created-but-unstarted pod). Intent-level methods let each backend map to native primitives. The cost is that the orchestrator can no longer distinguish reuse from creation — acceptable, since nothing in the orchestration logic depends on the distinction.

**Filesystem reuse becomes best-effort.** Today restart reuses the same container, so setup commands aren't re-run (`SetupCommandsCompleted` persists in the config file inside the container). The contract says `Start` *may* reuse; immutable-sandbox backends will recreate and re-run setup. The driver already handles both paths, so this is a per-backend performance property, not a correctness one.

**Workspace config stays Docker-shaped.** Designing a universal sandbox schema (generic volumes, networking, privileges) now, with one implementation, would be speculation. Per-backend config sections cost some duplication across workspaces later but keep each section honest about what its runtime actually supports.

**Exit codes as the contract, not an abstract "reported" flag.** The driver-owned-events proposal defines the invariant in exit-code terms and all four candidate runtimes can surface one. An abstract boolean would just move the mapping into every backend without removing it.

**One backend per runner process.** The `--backend` flag binds a runner to a single runtime. Heterogeneous fleets run one runner per backend, which matches how runners already shard by `runner_id` and keeps the per-task scheduling model unchanged. A workspace-level backend selector can be layered on later without touching this interface.

## Open Questions

1. **Per-backend workspace validation.** Should `Backend` grow a `ValidateWorkspace(*workspace.Workspace) error` so each backend validates its own config section at startup, or is that premature before a second backend exists?
2. **Driver binary distribution for remote backends.** Kubernetes/ECS sandboxes can't receive a tar copy from the runner. Should the server serve the prebuilt binaries over HTTP (so a bootstrap/init container can fetch them with the task token), or do we require workspace images for those backends to bake the binary in?
3. **Sandbox GC for remote backends.** `Prune` assumes sandboxes persist after exit and need explicit removal (Docker containers do). Runtimes that garbage-collect their own sandboxes (ECS, AgentCore) would implement `Remove` as a no-op — is `List`/`Remove` the right shape, or should prune policy move behind the interface entirely?
4. **Concurrency accounting.** The semaphore counts sandboxes started by this runner process and reconciles from `List` at startup. For backends with their own schedulers (Kubernetes), runner-side concurrency may be redundant with cluster-side limits — keep it as a uniform throttle, or let backends opt out?
