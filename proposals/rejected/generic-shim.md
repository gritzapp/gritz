# Generic Standalone Shim Binary

Issue: https://github.com/icholy/gritz/issues/1221

> **Status: rejected.**

## Problem

The in-sandbox shim (`internal/microvmshim/`) is the supervisor that runs inside
a task's sandbox: it provisions the task's files once (gated by the
`/gritz/.provisioned` marker), spawns and supervises the driver per run/resume,
and reports the driver's exit as a `driver-exited{code}` event over the
`/gritz/lifecycle` SSE stream. It holds no cloud credentials and makes no
control-plane calls — all lifecycle authority lives with the runner. That job is
backend-agnostic, but the shim today is not:

1. **It ships inside the full `gritz` binary.** The shim is invoked as
   `gritz tool microvm-shim`, so getting the shim into a sandbox means getting
   the entire multi-call binary in (~59 MB, measured on a current
   `CGO_ENABLED=0` linux/amd64 build). Every backend forks its own mechanism
   for moving those tens of MB: Docker tar-copies the binary into the
   container, the Nomad draft (proposals/draft/nomad-backend.md) stands up a
   per-runner artifact HTTP server with `${attr.cpu.arch}` interpolation, and
   the exe.dev draft (proposals/draft/exe-dev-backend.md, #1108) uploads it
   over SFTP.

2. **It is coupled to the Lambda MicroVMs backend.** The package imports
   `internal/runner/backend/lambdamicrovm` (for `Bundle`, `DriverExited`, and
   the control-surface paths) and `internal/x/awsmicrovm` (for the AWS
   lifecycle hooks). The generic control surface (`ControlHandler()`) and the
   AWS hook surface (`HooksHandler()`) are already separate handlers on
   separate ports — but they cannot be shipped separately.

3. **The managed-backend drafts have no solid working-tree persistence
   story.** Nomad's `sticky` ephemeral disk is best-effort and does not
   preserve the container's writable layer (nomad-backend.md Open Question
   #1); exe.dev's disk-preserving stop is unconfirmed (exe-dev-backend.md Open
   Question #1). Both proposals hinge their reuse-on-event lifecycle on a
   platform capability they cannot rely on.

This proposal extracts the shim into a small, backend-agnostic, standalone
binary — a new `cmd/shim` target — that is generic by default, serves the AWS
Lambda hook surface only behind a flag, downloads the driver from the server
when it is not already present, and acts as the long-lived in-sandbox
supervisor that makes working-tree persistence trivial for the managed
stock-image backends.

## Design

### Overview

```
cmd/shim                       new standalone binary (sibling to cmd/gritz,
                               cmd/migrate, cmd/dummymcp)
internal/shim/                 neutral package: Bundle, File, lifecycle event
                               types, the shim Server — imports NO backend
proto/shim/v1/shim.proto       the runner↔shim control contract (Connect)
internal/proto/shim/v1/        generated code
internal/microvmshim/          deleted — absorbed into internal/shim
internal/command/microvm_shim.go  deleted after the Lambda image migrates
```

The shim binary is dumb by design: serve the control surface, provision files
once, fetch the driver if it is missing, spawn/supervise/stop the driver,
stream its exit. No task knowledge, no cloud credentials, no control-plane
calls, no backend imports. Everything that knows about AWS, Nomad, or exe.dev
stays runner-side.

### Why a separate binary: the long-lived-machine persistence model

For the managed stock-image backends (Nomad, exe.dev), the shim is not just a
packaging cleanup — it is the working-tree persistence mechanism. Instead of
relying on a platform's disk-preserving suspend/stop (best-effort on Nomad,
unconfirmed on exe.dev), **the machine stays running for the task's whole
active life**:

- The shim is the sandbox's entrypoint (PID 1 or a long-lived unit). It does
  not exit when the driver exits.
- It provisions the task's files once, then spawns and re-spawns the driver
  per event against a **live filesystem that is never torn down**. The working
  tree, the driver's `SetupCommandsCompleted` marker, and dependency caches
  persist trivially — no snapshot, no sticky disk, no stop/start verb needed.
- On driver exit it publishes `driver-exited{code}` on the lifecycle stream;
  the runner's `Wait` returns, and the backend maps "shim up, driver exited"
  to `StateExited` — the same husk-preserved contract Docker gets from an
  exited container and Lambda gets from a suspended VM.
- The next routed event is a `Launch(reuse)`: the backend calls the shim's
  `Run` again and the driver restarts in place.

This resolves the two persistence open questions by sidestepping them: the
answer to "does the platform preserve the disk across a stop?" becomes "we
never stop the machine while the task is active."

The model has two named costs:

- **Idle compute between events.** A Nomad allocation keeps its resource
  reservation and an exe.dev VM keeps billing per-second while the shim idles
  between events. A **max-idle / max-lifetime reaper becomes mandatory**: a
  runner-side prune policy destroys sandboxes whose driver has been exited
  longer than a TTL. The next event then finds the sandbox gone
  (`backend.ErrGone`) and the task gets a fresh sandbox with setup re-run —
  the same degradation as any lost sandbox today.
- **The guarantee is bounded by node uptime, not absolute.** Node drain,
  preemption, and OOM still tear the machine down. The `ExitLost` fallback
  and the reconcile policy stay exactly as they are; the shim narrows the
  window in which the working tree can be lost, it does not close it.

Docker does not need any of this — an exited container preserves its
filesystem at zero cost — so the Docker backend does not adopt the shim (see
What doesn't change).

### Generic by default, `--aws-lambda-microvm` for the Lambda surface

`microvmshim.Server` already splits its two surfaces: `HooksHandler()` serves
the AWS lifecycle hooks (`/aws/lambda-microvms/runtime/v1/*` on
`awsmicrovm.HookPort`), and `ControlHandler()` serves the generic gritz
control surface on the ingress port. The flag simply decides whether the hook
server is mounted:

```
shim [--addr :8080]                          # generic: control surface only
shim --aws-lambda-microvm [--hook-addr :9000]  # + AWS lifecycle hooks
```

- **Generic mode** serves only the control surface: run (provision once +
  spawn), stop, lifecycle stream. The bundle arrives in the `Run` request
  body — no S3 staging, no payload URL, because the runner can reach the shim
  directly (Nomad alloc address, exe.dev). The shim authenticates control
  requests against the `GRITZ_TOKEN` in its environment (set by the backend
  at machine creation, exactly the token already minted into `spec.Env`) with
  a constant-time compare — the runner presents the same task token as a
  bearer. No new credential is introduced.
- **`--aws-lambda-microvm` mode** additionally serves `HooksHandler()` on the
  hook port. The `/run` hook keeps its current shape: fetch the staged bundle
  from the presigned payload URL (the 16 KB hook payload is too small for an
  agent config), provision, spawn; `/resume` re-spawns against the preserved
  disk. Control-surface auth stays with AWS's port-scoped managed-proxy
  tokens, as today — snapshots are built per-image, not per-task, so no task
  token can be baked into the environment.

One binary, one runtime flag, no build tags. The `awsmicrovm` hook handlers
are thin stdlib-only HTTP handlers, so carrying them unconditionally costs
nothing measurable.

### The shim downloads the driver if necessary

The shim is small; the driver (`gritz driver`, the full ~59 MB multi-call
binary, changing every release) is fetched at runtime. On `Run`, if the
bundle's `Cmd[0]` (i.e. `backend.BinaryPath`) does not exist on disk, the shim
downloads it before spawning:

```
GET {GRITZ_SERVER}/prebuilt/{runtime.GOARCH}
Authorization: Bearer {GRITZ_TOKEN}
```

written to `Cmd[0]` atomically (temp file + rename, mode 0755). Because the
shim runs *on* the node it selects the architecture natively with
`runtime.GOARCH` — no image inspection (Docker), no `${attr.cpu.arch}`
jobspec interpolation (Nomad), no per-image assumptions (exe.dev). Because
the filesystem lives as long as the machine, the download happens once per
sandbox and every subsequent spawn reuses the cached binary.

The "if necessary" clause is what lets one shim serve both packaging models:

| Backend | Driver present at first spawn? | Effect |
|---|---|---|
| Lambda MicroVMs | yes — baked into the snapshot at image build (microvm.Dockerfile) | download skipped entirely; no egress dependency |
| Nomad | no — stock workspace image | fetched from the server at first spawn, cached on the live FS |
| exe.dev | no — stock base image (`exeuntu`) | same |

**Server-side**, a new authenticated endpoint serves the prebuilt binaries.
The server does not serve them today, but its image already ships them:
the Dockerfile builds `prebuilt/gritz-linux-{amd64,arm64}` and sets
`GRITZ_PREBUILT_DIR=/app/prebuilt`. The endpoint is a thin handler over
`prebuilt.ReadBinary(arch)`:

```go
// internal/server/server.go — Bearer task-JWT auth, like the driver's other calls
mux.Handle("GET /prebuilt/{arch}", alice.New(s.auth.RequireAuth()).Then(s.prebuiltHandler()))
```

Any valid task token may fetch (the binary is not a per-task secret; the gate
keeps the artifact off the open internet). This answers two standing open
questions at once:

- **runner-backend-interface.md Open Question #2** ("Could the existing
  server host the prebuilt binaries instead, removing the per-runner artifact
  endpoint?") — yes.
- **nomad-backend.md Open Question #2** — the per-runner
  `--nomad-artifact-addr` file server and its client-node reachability
  requirement disappear from the Nomad design; the sandbox already must reach
  the server, and now that is the only reachability requirement.

Driver injection collapses from three forked mechanisms into one pattern:
**get the small shim in via the platform's native small-file mechanism, then
the shim HTTP-GETs the driver.** The shim itself travels easily precisely
because it is small: baked into the Lambda snapshot as the entrypoint, fetched
by a Nomad `artifact` stanza, uploaded over exe.dev SFTP and launched via
`systemd-run` — mechanisms that are all painless at ~10 MB and painful at
~59 MB.

### The neutral `internal/shim` package

The shim binary must not import backend packages or it won't stay small (and
the dependency direction would be backwards — backends are runner-side). The
neutral types currently living backend-side move into `internal/shim`:

- `Bundle` (from `lambdamicrovm`) — `Cmd`, `Env`, `Files`, `WorkingDir`,
  `User`. Its `Files` element type moves too; `backend.File` becomes a type
  alias (`type File = shim.File`) so `backend.Spec` and every backend compile
  unchanged.
- `DriverExited` / the `driver-exited` event identifier (from
  `lambdamicrovm`) and the control-surface paths duplicated today in
  `lambdamicrovm` and `microvmshim`.
- The `Server` itself (provision-once, spawn/supervise/stop, the sticky-replay
  `lifecycle` broadcaster) — moved from `internal/microvmshim`, with the AWS
  hook methods (`runHook`, `resumeHook`, …) kept behind the existing
  `HooksHandler()` seam.

Resulting import graph:

```
cmd/shim                      → internal/shim, internal/x/awsmicrovm (hooks handler)
internal/shim                 → stdlib, internal/proto/shim/v1 (no backend, no runner)
internal/runner/backend/*     → internal/shim (types + generated client)
```

`internal/x/awsmicrovm` stays where it is: the hook `Handler` is consumed by
`cmd/shim`, the SigV4 `Client` by the lambda backend — both stdlib-thin.

### The control protocol: Connect vs. hand-rolled HTTP + SSE

Today's control surface is ad-hoc: two hand-rolled HTTP endpoints, a JSON
bundle with implicit structure, and an SSE stream whose event names and
payloads are string constants kept in sync between `microvmshim` and
`lambdamicrovm` by convention. That was fine while the shim and the runner
shipped in the same binary — the contract could never skew against itself.
With a standalone shim, **the runner↔shim protocol becomes an independently
shipped wire contract**, and the question is whether it should be typed and
versioned like the rest of the codebase (Connect RPC, `proto/gritz/v1/`) or
stay hand-rolled to protect the binary size.

Measured on this repo (Go 1.26, `CGO_ENABLED=0`, linux/amd64):

| Variant | Size | Stripped (`-s -w`) |
|---|---|---|
| Minimal shim: `net/http` + `internal/x/sse` + `os/exec` | 8.3 MB | 5.7 MB |
| + `connectrpc.com/connect` + `google.golang.org/protobuf` runtime (small dedicated proto) | 13.4 MB | 9.2 MB |
| + connect linking the full `gritzv1` generated package instead | 15.9 MB | — |
| `google.golang.org/grpc` server alone, before any generated code | 14.6 MB | — |
| Full `cmd/gritz` today, for scale | 58.9 MB | — |

Three conclusions fall out:

1. **grpc-go is rejected.** The full gRPC stack costs more than the entire
   rest of the shim before a single generated message, and requires HTTP/2
   end-to-end. It buys nothing over Connect here.
2. **Connect-over-HTTP/1.1 is the light middle path.** connect-go serves the
   Connect protocol on a plain `net/http` server with no grpc-go dependency;
   unary calls are plain POSTs and **server-streaming works over HTTP/1.1**
   (a streamed response body — the same transport shape as today's SSE, so
   anything that passes the SSE stream today, including AWS's managed proxy,
   should pass it; validating that against the real proxy is an
   implementation-plan checkpoint). Cost: **+5.1 MB (+3.5 MB stripped)** over
   the hand-rolled baseline.
3. **A dedicated `proto/shim/v1` matters.** Reusing `gritz.proto` would link
   every message in the main API into the shim (+2.5 MB more) and couple the
   shim's release cadence to the whole API surface. The shim gets its own
   tiny, frozen-slow proto module.

**Recommendation: Connect.** The size delta is real but does not threaten any
delivery mechanism (13 MB moves over a Nomad artifact, SFTP, or a snapshot
bake just as easily as 8 MB — the cliff is at 59 MB, not 13), while the thing
Connect buys — a typed, versioned contract with protobuf field-evolution
rules — is exactly the mitigation the new version-skew surface needs. It also
replaces the ad-hoc JSON bundle with a schema'd message and the string-typed
SSE events with a `oneof`, and matches how everything else in the codebase
talks. If review weighs the size goal higher, the fallback is the existing
HTTP + SSE surface moved verbatim into `internal/shim` — everything else in
this proposal is independent of the choice.

The contract (sketch):

```protobuf
// proto/shim/v1/shim.proto
syntax = "proto3";
package shim.v1;

service ShimService {
  // Run provisions the bundle's files (once per machine, marker-gated),
  // fetches the driver if Cmd[0] is absent, and spawns the driver. Called
  // again on a later event, it re-spawns against the live filesystem.
  rpc Run(RunRequest) returns (RunResponse);
  // Stop gracefully stops the driver: SIGTERM → grace → SIGKILL. The exit is
  // published on Lifecycle like any other completion.
  rpc Stop(StopRequest) returns (StopResponse);
  // Lifecycle streams lifecycle events. The last driver-exited is sticky:
  // a new stream replays it immediately, so an exit during a runner
  // disconnect is delivered on reconnect. Keep-alives flow every 15s.
  rpc Lifecycle(LifecycleRequest) returns (stream LifecycleEvent);
}

message File { string path = 1; bytes data = 2; int64 mode = 3; bool dir = 4; }
message Bundle {
  repeated string cmd = 1;
  repeated string env = 2;
  repeated File files = 3;
  string working_dir = 4;
  string user = 5;
}
message RunRequest { Bundle bundle = 1; }
message RunResponse {}
message StopRequest {}
message StopResponse {}
message LifecycleRequest {}
message LifecycleEvent {
  oneof event {
    DriverExited driver_exited = 1;
    KeepAlive keep_alive = 2;
  }
}
message DriverExited { int32 code = 1; }
message KeepAlive {}
```

The sticky-replay, keep-alive, and reset-on-spawn semantics of the current
`lifecycle` broadcaster carry over unchanged — only the framing changes. On
the Lambda path, the staged S3 bundle becomes the serialized `Bundle` message
so the `/run` hook and the generic `Run` share one decode. Runner-side, the
generated `ShimServiceClient` replaces the hand-rolled SSE consumer in
`lambdamicrovm.Wait`, and Nomad/exe.dev backends consume the same client.

### Release and build

`cmd/shim` becomes a second cross-arch release artifact:

- `mise run build` adds `shim-linux-{amd64,arm64}` (built `CGO_ENABLED=0
  -ldflags "-s -w"` — the shim has no reason to carry symbol tables).
- `release.yml` uploads `gritz-shim-linux-{amd64,arm64}` next to the
  existing `gritz-linux-*` assets; the server Dockerfile copies them into
  `GRITZ_PREBUILT_DIR` alongside the driver binaries so the server can also
  serve the shim itself (e.g. a Nomad `artifact` source) without a detour
  through GitHub releases.
- `microvm.Dockerfile` changes its entrypoint from
  `["/usr/local/bin/gritz", "tool", "microvm-shim"]` to
  `["/usr/local/bin/shim", "--aws-lambda-microvm"]`, still baking the full
  `gritz` binary at `backend.BinaryPath` for the driver.
- `gritz tool microvm-shim` remains as a thin wrapper around the moved
  `internal/shim` server until existing MicroVM snapshots are rebuilt, then
  is deleted.

### What doesn't change

The runner backends, orchestrator (`runner.go`), driver, server API, task
state machine, database schema, and `gritz.proto` are untouched except for
the driver-injection seam moving into the shim:

- **Docker** keeps tar-copying the full binary into a local container —
  container semantics already give free husk persistence with zero idle cost,
  so the shim would add a hop for no benefit there.
- **Lambda MicroVMs** keeps its exact lifecycle (suspend on driver exit,
  resume re-spawns, terminate on archive) and its S3 bundle staging; only the
  in-VM binary and the stream framing change.
- The `backend.Backend` contract, `taskstate`, `ExitLost`, and the
  reconcile/prune policies are unchanged. The long-lived-machine model maps
  onto the existing states (`StateRunning` / `StateExited` / `StateGone`)
  without any new verbs.

## Implementation Plan

1. **Extract `internal/shim`** — Delivers: the neutral package (Bundle, File,
   event types, the moved `Server` + `lifecycle` broadcaster);
   `internal/microvmshim` deleted; `lambdamicrovm` and `backend.File`
   re-pointed via alias; `gritz tool microvm-shim` now wraps
   `internal/shim`. Pure move, no behavior change. Depends on: nothing.
   Verifiable by: existing `microvmshim`/`lambdamicrovm` tests pass relocated.
2. **Server `/prebuilt/{arch}` endpoint** — Delivers: Bearer-authenticated
   handler over `prebuilt.ReadBinary`. Depends on: nothing. Verifiable by:
   handler unit tests (auth required, arch routing, 404 on unknown arch).
3. **`proto/shim/v1` + Connect surfaces** — Delivers: the proto module,
   generated code, `ShimService` implementation in `internal/shim` replacing
   the hand-rolled control endpoints, generated client consumed by
   `lambdamicrovm.Wait`/`Signal`; S3-staged bundle switches to the proto
   encoding. Includes validating a Connect server-stream through the AWS
   managed proxy. Depends on: (1). Verifiable by: shim server unit tests
   (fake `Process`, in-memory Connect client), existing lambda backend tests.
4. **Generic mode: `Run` + driver download + env-token auth** — Delivers:
   bundle-in-body `Run`, download-if-absent from `/prebuilt/{arch}` with
   atomic install, constant-time `GRITZ_TOKEN` bearer check. Depends on:
   (2), (3). Verifiable by: unit tests with an `httptest` prebuilt server
   (present → no fetch; absent → fetch, cache, re-spawn without re-fetch).
5. **`cmd/shim` target + release artifacts** — Delivers: the binary, the
   `--aws-lambda-microvm` / `--addr` / `--hook-addr` flags, mise build +
   `release.yml` + server Dockerfile changes. Depends on: (3), (4).
   Verifiable by: built artifacts exist and `shim --aws-lambda-microvm`
   serves both surfaces.
6. **Lambda image migration** — Delivers: `microvm.Dockerfile` + README
   switched to the standalone shim; `gritz tool microvm-shim` deleted once
   snapshots are rebuilt. Depends on: (5). Verifiable by: an image built per
   the README runs a task end-to-end on the lambda backend.
7. **Runner-side idle reaper** — Delivers: max-idle/max-lifetime prune policy
   for husk sandboxes (required before any long-lived-machine backend
   ships). Depends on: (1)–(6) only conceptually; independent code. Verifiable
   by: orchestrator unit tests against `BackendMock`.

The Nomad and exe.dev backends then build on (4)–(7) in their own proposals,
which shrink accordingly (no artifact server, no SFTP'd driver, no
platform-suspend dependency).

## Trade-offs

**A second release artifact vs. one binary for everything.** The shim adds a
cross-arch build (`gritz-shim-linux-{amd64,arm64}`) to every release on top
of the existing prebuilt driver builds — more CI surface and one more thing
to version. The alternative (keep shipping the shim inside `gritz`) is what
we have: every backend must move ~59 MB into the sandbox by its own bespoke
mechanism, and the Nomad/exe.dev designs each grow infrastructure (artifact
server, SFTP of the full binary) just to deliver bytes that are 85% dead
weight in-sandbox.

**A new version-skew surface.** Today one binary cannot skew against itself;
after this, shim↔runner (control protocol) and shim↔driver (spawn contract:
Cmd/Env/Files) are independently shipped. Mitigations: the shim is dumb and
its protocol is deliberately narrow (three RPCs, one load-bearing event), so
it should change rarely; protobuf field evolution covers additive change; and
the shim↔driver contract is just "exec this argv with this env", the most
stable interface in computing. The Lambda snapshot already has this skew in
practice (a baked shim meets newer runners); making the contract explicit is
an improvement, not a regression.

**Connect (+5 MB) vs. hand-rolled HTTP+SSE.** Made explicit in the Design
section with measurements. Chosen: Connect, because the skew surface above is
exactly where a typed, versioned contract pays, and 13 MB clears every
delivery mechanism as easily as 8 MB. The fallback costs nothing to keep
open: the current HTTP+SSE surface moves into `internal/shim` verbatim.

**Long-lived machine vs. platform suspend.** Keeping the machine up buys a
persistence guarantee that no longer depends on unconfirmed platform verbs,
at the price of idle compute between events and a mandatory reaper. Lambda
keeps its suspend model (strictly better: preserved state at zero compute)
— the shim supports both models, it does not force the long-lived one.

**Driver staleness on long-lived machines.** "Download if absent" means a
sandbox that lives across a server deploy keeps its cached driver. That
matches Docker today (a reused container keeps its injected binary), so it is
not a regression — but the shim could do better cheaply (revalidate with
`If-None-Match`/ETag per `Run`), which Docker cannot. Left as an open
question rather than complexity-by-default.

**Serving binaries from the server vs. per-runner artifact endpoints.** The
server becomes the single artifact origin (it already ships the prebuilt
directory in its image). This centralizes egress on the server for driver
downloads — tens of MB per new sandbox. Acceptable at current scale, cheap to
cache later (the response is immutable per version); the alternative
(per-runner endpoints) spreads the load but adds a reachability requirement
per runner and forked infrastructure per backend, which is what this proposal
is removing.

## Open Questions

1. **Driver revalidation policy.** Should `Run` revalidate a cached driver
   against the server (ETag / version header) instead of a bare existence
   check, so long-lived sandboxes pick up new driver releases at the next
   event? Presence-only matches Docker's semantics; revalidation is a small
   addition but makes driver updates observable mid-task.
2. **Reaper knobs and ownership.** Max-idle / max-lifetime for husk sandboxes:
   per-workspace config, runner flags, or backend-specific defaults? And
   should the reaper stop short of `Destroy` where a cheaper preservation
   exists (e.g. exe.dev stop, if it turns out to exist)?
3. **Shim version visibility.** Should the shim report its version (a field on
   `RunResponse` or a `LifecycleEvent`) so the runner can log or refuse
   too-old shims? Cheap insurance on the new skew surface, but it needs a
   compatibility policy to be more than logging.
4. **Managed-proxy validation.** Connect server-streaming through AWS's
   managed proxy is expected to behave like the current SSE stream (both are
   long-lived streamed response bodies), but it must be validated against the
   real proxy before layer (3) commits the lambda backend to it — if the
   proxy misbehaves, the lambda leg keeps SSE while generic mode uses Connect,
   or everything falls back to SSE.
5. **Serving the shim binary itself.** Should `/prebuilt/` also expose the
   shim artifacts (for Nomad `artifact` stanzas and similar), or should those
   backends fetch from GitHub releases directly? Serving both from the server
   keeps one authenticated origin; GitHub releases avoid version coupling
   between the deployed server and the shim a backend injects.
