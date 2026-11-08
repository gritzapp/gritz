---
title: "Bedrock AgentCore Backend for the Runner"
description: "The runner only has Docker (and a proposed Firecracker) backend, both on hosts we provision, so there is no serverless, AWS-managed microVM option such as Bedrock AgentCore."
---

Issue: https://github.com/icholy/gritz/issues/943

## Problem

The runner's sandbox runtime is abstracted behind `backend.Backend`
(proposals/implemented/runner-backend-interface.md), which names **Bedrock
AgentCore** as a target runtime alongside Kubernetes, ECS, and Firecracker.
Today Docker is the only implementation, and a Firecracker backend is proposed
for single-host hardware isolation (proposals/draft/firecracker-backend.md).

Both of those backends supervise sandboxes the runner controls directly: a
Docker daemon on the runner host, or local KVM microVMs. Capacity is bounded by
hosts we provision and patch, and isolation is bounded by a kernel we own.

AWS Bedrock AgentCore Runtime is a serverless, session-isolated agent runtime:
each session runs in its own AWS-managed microVM, the platform handles scaling,
and there are no hosts to run. This proposal adds an `agent-core` backend that
runs each task as an AgentCore session: AWS-managed microVM isolation per task,
no runner-owned compute, auto-scaled by the platform.

AgentCore is the most *managed* of the named runtimes and the one that fits the
`backend.Backend` interface least cleanly. The interface was deliberately shaped
to anticipate it — `Watch` is a blocking callback "so poll-based backends (ECS
`DescribeTasks`, AgentCore) fit as naturally as stream-based ones," and three of
that proposal's open questions (driver-binary distribution, sandbox GC, runner
vs. platform concurrency) are exactly AgentCore's. This proposal resolves them
for AgentCore and is honest about the one structural mismatch that remains: the
runner is a *process supervisor*, and AgentCore exposes an *HTTP invocation*, not
a process.

## Background: the AgentCore runtime contract

Three facts about AgentCore Runtime drive the whole design.

1. **An agent runtime is a container image plus a control-plane resource.** You
   build a `linux/arm64` image that runs an HTTP server on `0.0.0.0:8080`
   implementing `POST /invocations` and `GET /ping`, push it to ECR, then call
   `CreateAgentRuntime` (namespace `bedrock-agentcore-control`) with the image
   URI and an execution role. That returns an agent-runtime ARN with a `DEFAULT`
   endpoint.

2. **Work is started by an HTTP invocation, not by launching a process.**
   `InvokeAgentRuntime` (namespace `bedrock-agentcore`, SigV4-signed) takes the
   runtime ARN, a `runtimeSessionId`, and an arbitrary JSON payload that is
   delivered to the container's `/invocations` handler. The response may be a
   single JSON body or a streamed `text/event-stream`.

3. **The unit of isolation is a *session*, addressed by `runtimeSessionId`.**
   Each distinct session id gets a dedicated microVM with its own CPU, memory,
   and filesystem. Invocations carrying the same session id route to the same
   microVM (sticky) for the life of the session; the platform tears the microVM
   down after an idle timeout or the maximum session lifetime (up to 8h). Session
   ids must be ≥33 characters.

The mismatch with the other backends: Docker/Firecracker sandboxes run
*independently* of the runner and are *observed* (die events, pidfd). An
AgentCore microVM is driven by an HTTP request the runner makes. The driver
process gritz wants to run is not a thing AgentCore launches — it is the body
of an `/invocations` handler. The design below bridges that gap with a small
in-image HTTP shim and leans hard on the driver-owned-events invariant for exit
reporting.

## Design

### Overview

A new package `internal/runner/backend/agentcore` implements `backend.Backend`
against the AWS SDK for Go v2 (`bedrockagentcorecontrol` + `bedrockagentcore`).
Selection follows the existing seam:

```
gritz runner --backend agent-core
```

Per task, the backend:

1. Ensures an **agent runtime resource** exists for the workspace's image
   (cached by image digest + gritz version), creating it via
   `CreateAgentRuntime` on first use.
2. Builds an **invocation payload** from `backend.Spec` (cmd, env, files) — the
   AgentCore analog of the Docker backend's tar copy and the Firecracker
   backend's `config.tar` boot manifest.
3. Calls `InvokeAgentRuntime` with `runtimeSessionId` derived from the task id,
   from a supervising goroutine. An in-image **shim** receives the payload,
   provisions the files, and execs the driver — which connects to the server with
   its task token exactly as under Docker.

The orchestrator (`runner.Runner`), the driver, the server API, the database, and
the task state machine are untouched: the driver already connects to the server by
URL + token and neither knows nor cares what launched it. That was the point of
the socket-proxy elimination and driver-owned-events prerequisites.

### The in-image shim and image contract

AgentCore has no file-injection phase (you cannot tar-copy into a session
microVM) and requires the container's entrypoint to be an HTTP server. So, like
the Firecracker backend bakes the driver into the rootfs and boots
`gritz tool vm-init` as PID 1, the AgentCore image bakes the gritz binary in
and runs a new hidden subcommand as its entrypoint:

```
gritz tool agentcore-shim      # beside `tool agent-mcp` and `tool vm-init`
```

`agentcore-shim` is a minimal HTTP server implementing the AgentCore contract:

- `GET /ping` → `200` once the shim is ready (AgentCore health check).
- `POST /invocations` → decode the payload (below), provision `files` if the
  session is fresh, then exec the driver with `cmd`/`env`, streaming its stdout
  as `text/event-stream` and returning the driver's **exit code** in a trailing
  event. The driver reports its own terminal status to the server over the duration;
  the shim's job is liveness + exit-code surfacing, not server communication.

The invocation payload is the shim's equivalent of the Firecracker boot
manifest, carrying exactly what `backend.Spec` holds:

```go
type invocation struct {
	Cmd        []string       `json:"cmd"`         // spec.Cmd
	Env        []string       `json:"env"`         // ws.AgentCore env + spec.Env
	Files      []backend.File `json:"files"`       // spec.Files (Data base64)
	WorkingDir string         `json:"working_dir,omitempty"`
	User       string         `json:"user,omitempty"`
}
```

Files are provisioned only on the first invocation of a session and skipped on
re-invocation of the same (sticky) microVM — gated by a `/gritz/.provisioned`
marker, reproducing the Docker backend's provision-at-create-only semantics so a
restart never clobbers the driver's `SetupCommandsCompleted`/`Started` markers
in `agent.ConfigPath(taskID)`.

Because the entrypoint must be the shim and the binary must already be present,
**AgentCore images must be purpose-built** — unlike the Docker and Firecracker
backends, which consume an unmodified workspace image. The image is
`<workspace base image>` + the host-arch (`linux/arm64`) gritz binary at
`backend.BinaryPath` + `ENTRYPOINT ["gritz","tool","agentcore-shim"]`. gritz
ships a published base image and a `Dockerfile` fragment to make this a
two-line build; full auto-build/push is an open question. The cache key for the
agent runtime resource therefore includes the gritz version, since the image
embeds the driver binary (same reasoning as the Firecracker rootfs cache).

### Workspace config

Per the backend-interface proposal, backends get sibling config sections.
`workspace.Workspace` gains an `agent_core:` section next to `container:` and the
proposed `firecracker:`:

```yaml
workspaces:
  pets-workshop:
    agent_core:
      image: 123456789012.dkr.ecr.us-east-1.amazonaws.com/gritz-workspace:latest
      execution_role: arn:aws:iam::123456789012:role/gritz-agentcore
      region: us-east-1            # default: AWS SDK resolution
      network_mode: PUBLIC         # PUBLIC | VPC (default PUBLIC)
      runtime_arn: ""              # optional: use a pre-created runtime, skip CreateAgentRuntime
      environment:
        CLAUDE_CODE_OAUTH_TOKEN: ${env:CLAUDE_CODE_OAUTH_TOKEN}
    agent:
      type: claude
      ...
```

```go
type AgentCore struct {
	Image         string            `yaml:"image"`          // ECR image URI
	ExecutionRole string            `yaml:"execution_role"` // IAM role ARN the runtime assumes
	Region        string            `yaml:"region"`
	NetworkMode   string            `yaml:"network_mode"`   // PUBLIC | VPC
	RuntimeARN    string            `yaml:"runtime_arn"`    // optional pre-created runtime
	Environment   map[string]string `yaml:"environment"`
}
```

`agent:`, `commands:`, and `capabilities:` stay backend-agnostic. AWS API
credentials are not in `workspaces.yaml`; they come from the standard AWS SDK
credential chain on the runner (env, shared config, instance/IRSA role), so the
same expansion-free config is safe to share across a heterogeneous fleet. A
workspace may set `container:`, `firecracker:`, and `agent_core:` together so one
`workspaces.yaml` serves runners with different backends.

This builds on the validation method the Firecracker proposal adds to `Backend`:

```go
type Backend interface {
	// ValidateWorkspace checks the workspace's config section for this
	// backend. The runner validates at startup and registers only the
	// workspaces its backend accepts; Start re-validates.
	ValidateWorkspace(ws *workspace.Workspace) error
	// ... existing methods unchanged
}
```

The agent-core backend's `ValidateWorkspace` requires `image` and
`execution_role` (unless `runtime_arn` is set) and rejects an unknown
`network_mode`. `RegisterWorkspaces` skips (with a warning) workspaces that fail
validation, so a shared `workspaces.yaml` advertises each workspace only from
runners that can run it. The `container.image is required` check moves into the
Docker backend, as that proposal already establishes.

### Session ids and filesystem reuse

`runtimeSessionId` is the per-task sandbox handle. It must be deterministic
(restart must address the same session for reuse), unique per task and runner,
and ≥33 chars:

```go
func sessionID(runnerID string, taskID int64) string {
	// e.g. "gritz-<runner>-<task>" padded/hashed to a stable ≥33-char id
}
```

Re-invoking the same session id within the session's idle window routes to the
same microVM, so `spec.Files` provisioning is skipped and the driver's setup
commands are not re-run — the same best-effort filesystem reuse the interface
contract allows ("`Start` *may* reuse"). If the idle timeout elapsed, AgentCore
has torn the microVM down; the next invocation provisions a fresh one and setup
re-runs. The driver already tolerates both paths via `SetupCommandsCompleted`,
so this is a per-backend performance property, not a correctness one — identical
to how an immutable-sandbox backend behaves.

As the interface proposal notes, the orchestrator mints a fresh task token on
every `Start`; the shim receives it in the invocation payload, so a reused
session simply runs the driver with the newly minted token. This only tightens
existing behavior.

### Backend method mapping

| Method | Implementation |
|---|---|
| `ValidateWorkspace` | Require `image` + `execution_role` (or `runtime_arn`); validate `network_mode`. |
| `Start` | Ensure the agent runtime resource for `ws.AgentCore.Image` (cache by digest+version; `CreateAgentRuntime` on miss, or use `runtime_arn` verbatim). Build the invocation payload from `spec`. Spawn a supervising goroutine that calls `InvokeAgentRuntime` with `sessionID(taskID)` and holds the streamed response; record the session as running in an in-memory table persisted to `<state-dir>/sessions/<task>.json`. |
| `Stop` | Session not running → `(false, nil)`. Otherwise request session teardown (`StopAgentRuntimeSession` if available, else cancel the in-flight invocation) and return `(true, nil)`. **Caveat:** AgentCore cannot deliver SIGTERM into the microVM, so this is a hard stop — see "Differences." |
| `Running` | Look up the session in the local table and confirm against AgentCore session status; absent/terminated → false. |
| `List` | Return sandboxes from the local session table reconciled against AgentCore session status. Sessions the platform has already GC'd report `StateExited`. |
| `Remove` | Best-effort session teardown; delete the local `<state-dir>/sessions/<task>.json`. AgentCore GCs the microVM itself, so this is mostly local cleanup (resolves interface open question #3 for this backend: `Remove` is near-no-op). |
| `Watch` | A poll loop (not an event stream). When a supervising goroutine's `InvokeAgentRuntime` returns, parse the trailing exit-code event and `handle(Exit{TaskID, ExitCode})`. For sessions whose invocation connection was lost (runner restart), poll session status and emit `Exit{TaskID, -1}` on terminal — "report lost," honestly handled by the state machine. |
| `Close` | Stop watchers and cancel in-flight invocations; the orchestrator's restart loop and `Reconcile` recover state on the next process. |

Exit-code fidelity follows the interface contract: a missing or unparseable exit
record is reported as `-1`. By the driver-owned-events invariant that means
"report lost" — if the driver did report before the microVM died, the status
guard in `internal/model/task.go` rejects the spurious `failed`; if it didn't,
`failed` is the honest outcome. Correctness comes from the state machine, not
from backend fidelity. This is the same fallback the interface proposal
describes for backends that cannot reliably observe exit codes, and AgentCore
exercises it more than Docker does.

### State directory

The backend keeps minimal local state under a per-runner directory (default
`/var/lib/gritz/agent-core/<runner-id>`), since AgentCore holds the heavy state:

```
<state-dir>/
├── runtimes/<image-digest>-<gritz-version>.json   # cached agent-runtime ARN per image
└── sessions/<task-id>.json                          # sessionId, runtime ARN, started-at
```

`sessions/<task-id>` is the sandbox for the `Backend` contract: `List` scans it,
`Remove` deletes it, `Start` reuses it. This is the AgentCore analog of the
Firecracker `tasks/<task-id>` directory, but it holds only handles — there is no
rootfs, no networking, no process to supervise locally.

### Concurrency and scheduling

AgentCore auto-scales sessions; runner-side concurrency (the `safesem.Semaphore`)
is redundant with the platform's own limits (interface open question #4). The
backend keeps the orchestrator's semaphore as a uniform client-side throttle
rather than special-casing it — `Reconcile` counts `StateRunning` sessions from
`List` at startup exactly as for Docker, so no orchestrator change is needed. An
operator can set `--concurrency 0` (unlimited) to defer entirely to AgentCore.

### CLI

```
gritz runner --backend agent-core \
  [--agent-core-state-dir /var/lib/gritz/agent-core] \
  [--agent-core-region us-east-1]
```

All flags have `GRITZ_AGENT_CORE_*` env sources; AWS credentials/region also
resolve through the standard SDK chain. `internal/command/runner.go`'s backend
switch gains an `agent-core` case constructing
`agentcore.New(agentcore.Options{RunnerID, Region, StateDir, Log})`.

`gritz download` is not extended — there is no host kernel or firecracker
binary to fetch; the AWS SDK is compiled in. Instead, `gritz` publishes the
AgentCore base image and build fragment (see image contract above).

### Package layout

```
internal/runner/
├── runner.go                 unchanged orchestrator
├── backend/
│   ├── backend.go            +ValidateWorkspace (from the firecracker proposal)
│   ├── docker/               unchanged (gains ValidateWorkspace)
│   ├── firecracker/          proposed separately
│   └── agentcore/
│       └── agentcore.go      AgentCore implementation
└── workspace/                +AgentCore config section
internal/command/tool.go      +agentcore-shim hidden subcommand
```

### Testing

- Unit tests (no AWS): `ValidateWorkspace`; `sessionID` determinism and length;
  invocation-payload construction (cmd/env/files round-trip, base64 of
  `File.Data`, directory entries); exit-code parsing from the streamed response;
  agent-runtime cache-key derivation. The AWS clients sit behind small
  interfaces so the SDK calls are mocked, matching the `dockerx` moq pattern.
- The `agentcore-shim` HTTP handler is unit-tested in `internal/command` against
  a fake driver binary: `/ping`, payload decode, provision-once gating, exit-code
  surfacing.
- Integration tests in `backend/agentcore`, skipped unless AWS credentials and a
  test execution role are present (an env guard, mirroring how the Docker e2e
  tests require a daemon and the Firecracker tests require `/dev/kvm`). They cover
  runtime creation, invoke→exit, provision-once-across-restart, and the
  lost-connection reconcile path.
- The orchestrator needs no new tests: it already runs against `BackendMock`.

### What doesn't change

The orchestrator (`runner.go`), `EventQueue`, proto definitions, database schema,
driver, and task state machine are untouched. The Docker and Firecracker backends
are unaffected beyond the shared `ValidateWorkspace` addition. `prebuilt` is
reused as-is to source the arm64 driver binary baked into the image.

## Differences vs. the Docker/Firecracker backends

Things that don't translate cleanly, called out explicitly:

- **Purpose-built images, not arbitrary ones.** Docker and Firecracker consume an
  unmodified workspace image; AgentCore requires the entrypoint to be the HTTP
  shim and the binary to be pre-baked, so its images must be built to the
  contract and pushed to ECR. This forks the image pipeline for AgentCore
  workspaces — the largest portability cost of this backend.

- **Hard stop, no graceful SIGTERM.** Docker escalates SIGTERM→SIGKILL and
  Firecracker polls an MMDS stop flag; both let the driver catch the signal and
  own the terminal report. AgentCore exposes no in-microVM signal, so `Stop`
  tears the session down. The driver does not get to flush a final report, so
  stopped tasks rely on the `Exit{-1}` "report lost" → `failed` fallback. For a
  user-initiated stop that is acceptable (the task is being abandoned), but it is
  strictly less graceful than the other two backends.

- **No runner-owned process to observe.** Liveness and exit come from polling
  AgentCore session status and from the held invocation response, not from a
  local process/pidfd/event stream. `Watch` is poll-based and `List`/`Running`
  consult both local handles and the AWS API. This is exactly the poll-based
  shape the interface anticipated, but it means backend state can lag the
  platform between polls.

- **No local sandbox to GC, no networking to manage.** `Remove` is near-no-op and
  there is no TAP/bridge/NAT layer; AgentCore owns the microVM lifecycle and
  egress. The runner manages handles, not infrastructure.

- **Connection-coupled supervision.** The runner drives work via a held HTTP
  invocation. If the runner restarts, that connection drops; recovery depends on
  AgentCore keeping the session/work alive and on `Reconcile` re-adopting it
  (see Risks).

- **server reachability.** The driver inside the microVM must reach the runner's
  `--server` URL. With `network_mode: PUBLIC` that means a public server endpoint;
  `VPC` mode requires the server to be in or peered to that VPC. A localhost server will
  not work — the same class of constraint as Firecracker's "address the server via
  the bridge gateway," but stricter because the microVM is off-host.

- **No volumes / host mounts.** Like Firecracker, `container.volumes` has no
  AgentCore equivalent; shared caches or secrets must be baked into the image or
  fetched in setup commands.

## Trade-offs

**HTTP shim in the image vs. teaching the driver to be an HTTP server.** The
driver could itself implement `/invocations`/`/ping`. Keeping a separate
`agentcore-shim` subcommand keeps the driver runtime-agnostic (it is still just
`exec`'d with cmd/env, identically across all four backends) and confines the
AgentCore contract to one small, testable place — the same reasoning that made
`vm-init` a separate subcommand for Firecracker.

**Lean on driver-owned-events vs. demand exit-code fidelity.** AgentCore's exit
surfacing (a trailing event over a possibly-dropped stream) is less reliable than
a Docker die event. Rather than engineer around that, the design accepts
`Exit{-1}` as the honest "report lost" signal and lets the state machine's status
guard do the reconciliation. This is the contract the interface proposal already
defines; AgentCore simply relies on it more.

**Purpose-built images vs. a universal image.** Requiring AgentCore-specific
images is a real regression in `workspaces.yaml` portability. The alternative —
the runner building and pushing per-workspace images on the fly — reinvents a
build/registry pipeline on the runner and needs Docker/buildkit + ECR push
exactly where AgentCore was supposed to remove host compute. Shipping a base
image + build fragment keeps the runner thin; auto-build is left as an open
question.

**One agent runtime per (image, version) vs. one shared runtime.** A single
shared runtime can't carry per-workspace images, and AgentCore binds a runtime to
one image. Caching a runtime ARN per image digest mirrors the Firecracker base
rootfs cache and keeps `CreateAgentRuntime` off the hot path.

**Keep the runner-side semaphore vs. defer to the platform.** AgentCore scales
itself, so the semaphore is arguably redundant. Keeping it (with `0` = unlimited
as the opt-out) avoids special-casing the orchestrator and preserves a uniform
client-side safety throttle, matching the interface proposal's lean toward "keep
it as a uniform throttle."

## Open Questions

1. **Connection-coupled work lifetime.** When the runner holds
   `InvokeAgentRuntime` open for the task's duration and then restarts, does
   AgentCore keep the session's work running, or does dropping the request cancel
   the `/invocations` handler (and thus the driver)? If the latter, the backend
   needs AgentCore's async/long-running invocation mode (return immediately, poll
   for completion) so the driver survives runner restarts the way containers and
   VMs do. This is the central design risk and must be confirmed against the
   AgentCore API before implementation.

2. **Session id ↔ microVM stickiness guarantees.** Filesystem reuse assumes the
   same `runtimeSessionId` reliably hits the same microVM within the idle window.
   The exact idle/lifetime limits and stickiness guarantees need verification;
   if weaker than assumed, AgentCore is effectively an always-fresh backend and
   setup commands re-run on every restart (still correct, just slower).

3. **Image build/push automation.** Should the release pipeline build and publish
   AgentCore-ready workspace images (base + baked binary + shim entrypoint), or
   should the runner build/push them on demand, or should operators own that
   entirely? The proposal assumes operator-built images + a published base.

4. **Long-running tasks vs. the 8h session ceiling.** AgentCore caps session
   lifetime. Tasks that legitimately run longer (or sit idle awaiting events
   under the event system) may outlive a session. Do we re-invoke to resume
   (relying on filesystem reuse, which the ceiling defeats), or document a hard
   cap for this backend?

5. **Streaming the agent's stdout vs. driver-only reporting.** The shim could
   stream driver stdout back through the invocation response for observability,
   but the driver already reports structured logs to the server. Is the streamed
   response worth anything beyond the trailing exit code, or should `/invocations`
   return only that?

6. **Cost and quotas.** Per-session microVMs and `InvokeAgentRuntime` calls are
   billed and quota-limited per account/region. Should the backend surface
   AgentCore throttling/quota errors as a distinct retryable condition to the
   orchestrator, or treat them as generic `Start` failures?
