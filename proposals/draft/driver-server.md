# Driver as a Server

Issue: https://github.com/icholy/gritz/issues/1646

## Problem

The runner has no common way to control the driver inside a sandbox. Docker runs
the driver as the container's main process and observes it through the container
lifecycle. Lambda MicroVMs need an in-VM supervisor (`internal/runner/microvmshim`,
`gritz tool microvm-shim`) that serves the AWS hooks, fetches a spec bundle staged
in S3, spawns the driver and reports its exit over a hand-rolled SSE stream. Every
proposed backend designs another variant: a generic standalone shim
(proposals/draft/generic-shim.md), a shim as the image `CMD` (DigitalOcean, #1588),
exec sessions (Fly Sprites, #1522), `systemd-run` units (exe.dev, #1108).

Problems that follow:

1. The driver assumes one run per process (flags, `driverSecrets()` reading its own
   environment, agent CLIs inheriting `os.Environ()`), so a sandbox that stays up
   between runs needs a supervisor to start a fresh driver per run.
2. `Probe`'s `StateRunning` means "sandbox running", but `Runner.Start`,
   `Runner.Load` and `Runner.Running` read it as "run active". On a backend whose
   sandbox stays up between runs, `Start` would never launch the next run.
3. `lambdamicrovm.Wait` fires `SuspendMicrovm` without waiting for it. A follow-up
   run that sees `SUSPENDING` gets `ErrGone`, which fails the task and drops the
   taskstate record; the VM is then never terminated.
4. Sandboxes are parked the moment a run ends; there is no warm period for quick
   follow-ups (#1196).
5. Lambda stages the bundle in S3 only because it travels through a 16 KB hook
   payload, even though the runner already reaches the VM over the managed proxy.
6. The driver talks to the server directly (`GetTask`, `ListEventsByTask`,
   `ListLinks`, `SubmitRunnerEvents`, `AppendLogChunk`), so every sandbox needs
   egress to the server, and the terminal report races the runner's own view of
   the run: `ExitCode` exists only to tell the runner whether the driver's report
   got through.
7. `Launch` returns a handle or an error, never both. A sandbox the platform
   created but that then failed to come up (a boot timeout, a failed binary copy,
   a cancelled context) is reported as an error alone; the runner has no record of
   it, and the sandbox leaks.

## Design

### Overview

The driver becomes a long-lived gRPC (Connect) server: `gritz driver --serve`.
It is the sandbox's main process on every backend, and it talks only to the
runner.

Key properties:

- **The runner is the driver's only peer.** The driver makes no calls to the
  server. Everything it used to fetch arrives in `Run`; everything it used to
  report (runner events, log chunks) goes back on the `Run` stream, and the runner
  delivers it.
- **The driver has an outbox.** Every message it emits is appended to a durable
  outbox on the sandbox's disk with a sequence number. `Run` streams the outbox
  from the last acknowledged entry, so a runner that crashes and reconnects gets
  everything it missed. The runner acknowledges an entry only once it is durable
  on its side.
- **One `Backend` interface.** The backend manages sandboxes only; it never speaks
  the driver protocol beyond handing out a client. `Start` returns the handle
  alongside any error whenever a sandbox exists, and the runner persists a
  returned handle before looking at the error. The handle is recorded before the
  run starts, even when the sandbox fails to come up.
- **The runner owns the driver protocol.** `runner.go` calls `Run`, `Ack` and
  `Stop`, consumes the stream and parks idle sandboxes.
- **Runs execute in-process**, one at a time. Everything per-process today becomes
  per-run.
- **`Run` is idempotent**, keyed on the task version. A run record on the
  sandbox's disk (`/gritz/run.json`) lets a restarted driver answer for a run it
  can no longer see.
- **There is no shim.** The AWS hook surface stays, but only as acknowledgements
  served by the driver on Lambda.

### Service definition

New module `proto/driver/v1/driver.proto`, generated into
`internal/proto/driver/v1` by the existing `buf.gen.yaml` (protocolbuffers/go +
connectrpc/go). It imports the `gritz.v1` messages it carries (`Task`, `Event`,
`TaskLink`, `RunnerEvent`) but is otherwise separate from `gritz.proto`, so it
evolves on its own cadence.

```protobuf
syntax = "proto3";

package driver.v1;

import "gritz/v1/gritz.proto";
import "google/protobuf/timestamp.proto";

option go_package = "github.com/icholy/gritz/internal/proto/driver/v1;driverv1";

// DriverService is served by `gritz driver --serve` inside the sandbox and
// called by the runner. Runs execute in-process, one at a time. The driver
// never calls the gritz server; it reports through its outbox, which Run
// streams and Ack trims.
service DriverService {
  // Health reports that the server is up, plus the current run record and the
  // event cursor the runner fetches the next run's events from.
  rpc Health(HealthRequest) returns (HealthResponse);

  // Run is attach-or-start, keyed on version:
  //   record.version > version             -> FailedPrecondition (stale)
  //   record.version == version            -> attach, spec ignored
  //   no record / older version, idle      -> start the run (spec required,
  //                                           NotFound without one)
  //   another version running              -> Aborted (busy)
  // The stream first replays every outbox entry after the acknowledged
  // sequence, then follows new entries live. It ends after the run's Finished
  // entry has been sent. Closing it detaches; it does not cancel the run.
  rpc Run(RunRequest) returns (stream RunResponse);

  // Ack acknowledges every outbox entry up to and including seq. The driver
  // drops them from the outbox and will not replay them.
  rpc Ack(AckRequest) returns (AckResponse);

  // Stop gracefully stops the run with the given version: cancel the run
  // context with ErrStop (agents and setup commands SIGTERM their process
  // groups); the run's terminal event and Finished land in the outbox as usual.
  // A no-op if that version is not the active run.
  rpc Stop(StopRequest) returns (StopResponse);
}

message RunRecord {
  enum State {
    STATE_UNSPECIFIED = 0;
    STATE_RUNNING = 1;
    STATE_FINISHED = 2;
  }
  int64 version = 1;
  State state = 2;
  // When the run finished. Drives the idle timeout.
  google.protobuf.Timestamp finished_at = 3;
}

message HealthRequest {}

message HealthResponse {
  // Absent if this sandbox has never started a run.
  optional RunRecord run = 1;
  // ListEventsByTask page token after the last events delivered to the agent.
  // Empty before the first successful run.
  string event_token = 2;
}

// RunSpec carries everything the driver used to read from flags, its
// environment and the server.
message RunSpec {
  // The task as of this run: id, name, version, prompt, shell_session.
  gritz.v1.Task task = 1;
  // Instruction and external events after HealthResponse.event_token, fetched
  // by the runner.
  repeated gritz.v1.Event events = 2;
  // The page token after the last event above. Persisted by the driver once
  // the prompt is delivered.
  string event_token = 3;
  // The task's links. Only set on the first run.
  repeated gritz.v1.TaskLink links = 4;
  // Passed to setup commands and the agent CLI. Not set on the driver
  // process itself.
  map<string, string> env = 5;
  // Workspace secrets and the task token. Added to the run's env (except the
  // token) and masked in shipped log chunks.
  map<string, string> secrets = 6;
  // Agent config and directories. Provisioned once per sandbox, gated by
  // /gritz/.provisioned.
  repeated File files = 7;
}

message File {
  string path = 1;
  bytes data = 2;
  int64 mode = 3;
  bool dir = 4;
}

message RunRequest {
  int64 version = 1;
  // Set to start a run. Omitted to attach.
  RunSpec spec = 2;
}

message RunResponse {
  oneof event {
    // First message on every stream.
    Accepted accepted = 1;
    // Outbox entries, in sequence order.
    Entry entry = 2;
    KeepAlive keep_alive = 3;
  }
}

message Accepted {
  // True if the call attached to an existing run rather than starting one.
  bool attached = 1;
}

message Entry {
  uint64 seq = 1;
  oneof payload {
    // started / stopped / failed, stamped with the run's version.
    gritz.v1.RunnerEvent runner_event = 2;
    // A masked chunk of the run's log, cut at 32 KiB as today.
    LogChunk log_chunk = 3;
    // Last entry of a run. Written after its terminal runner event.
    Finished finished = 4;
  }
}

message LogChunk {
  int64 version = 1;
  bytes data = 2;
}

message Finished {
  int64 version = 1;
}

message KeepAlive {}

message AckRequest {
  uint64 seq = 1;
}

message AckResponse {}

message StopRequest {
  int64 version = 1;
}

message StopResponse {
  // True if a running run with this version was stopped.
  bool stopped = 1;
}
```

`Run` is server streaming and `Ack` is a separate unary call, rather than a
single bidirectional stream, because Connect bidi needs HTTP/2 end to end and
Lambda's managed proxy is HTTP/1.1. Connect handlers serve the Connect, gRPC and
gRPC-Web protocols on a plain `net/http` server.

### The driver server

`gritz driver --serve [--addr :8080]` replaces `gritz driver` as the sandbox
entrypoint. It holds no task identity and no server credentials of its own;
everything arrives in `Run`.

**Per-run construction.** What `internal/command/driver.go` builds once per
process today moves into the `Run` handler, and the server calls go away:

| Today | Driver server |
|---|---|
| `--server` / `--task` / `--token` flags | `RunSpec.task`; no `gritzclient` |
| `GetTask` at the top of `Driver.Run` | `RunSpec.task` (the runner already holds it from its poll) |
| `drainEvents` (`ListEventsByTask` from `cfg.NextEventToken`) | `RunSpec.events`, fetched by the runner from `HealthResponse.event_token` |
| `ListLinks` on the first run | `RunSpec.links` |
| `SubmitRunnerEvents` for started / stopped / failed | `runner_event` outbox entries |
| `logship.Shipper` calling `AppendLogChunk` | the shipper's sender appends `log_chunk` outbox entries; masking and chunk cutting are unchanged |
| `driverSecrets()` reads `GRITZ_SECRETS` via `os.Getenv` | `RunSpec.secrets`; the `redact.Writer` is built per run before the first byte |
| `agent.OpenDriverLog` once | opened per run, still appending to `/gritz/log` |
| SIGTERM handler in `Driver.Run` | run context cancelled with `ErrStop` by `Stop`; SIGTERM to the server process means shut down (stopping an active run first) |
| exit code means "did the driver report?" | the outbox: the terminal event is durable in the sandbox before `Finished` |

The event cursor stays in the driver's config (`cfg.NextEventToken`). The driver
saves `RunSpec.event_token` there after `a.Prompt` returns, as `drainEvents`'
result is saved today, and reports it in `Health`. The at-least-once delivery of
events to the agent is unchanged.

**Per-run environment.** Nothing may inherit the process environment, because
secrets arrive over RPC. `agent.Driver` gains an `Env []string` field, threaded
to:

- `agent/claude.go:72` and `agent/cursor.go:63` (`os.Environ()` today)
- `agent/codex.go`, `agent/copilot.go`, `agent/sloppy.go` (nil `cmd.Env` today,
  which inherits)
- setup commands, `agent/driver.go:288`
- `os.ExpandEnv(cfg.Cwd)`, `agent/driver.go:226`, which becomes `os.Expand` over
  the run env

**Outbox.** `/gritz/outbox/`, one file per entry, using the `internal/x/outbox`
`FileStore` with a `ReadFrom(seq)` added for replay. Entries are appended in the
order the driver produces them, so a run's log chunks and runner events
interleave as they happened. `Ack(seq)` drops every entry up to `seq`. Sequence
numbers are monotonic for the life of the sandbox, not per run, so an
unacknowledged tail from an earlier run is replayed before a new run's entries.

**Run record.** `/gritz/run.json`, written atomically with
`internal/x/atomicio`:

- `{version, running}` before a run starts;
- `{version, finished, finished_at}` after the run's `Finished` entry is in the
  outbox.

On boot, a record still `running` means the process died mid-run. Before
serving, the driver appends a `failed` runner event ("driver restarted mid-run")
and a `Finished` entry for that version, and rewrites the record as finished.
The runner learns about the crash through the outbox like any other outcome. The
record and the outbox live on the sandbox's disk, so they survive driver
restarts and parking, but not sandbox loss.

**Server rules.** At most one run at a time. A `Run` that starts a new version
while the outbox still holds unacknowledged entries from an older run is allowed;
the stream replays them first.

**Authentication.** The server requires `Authorization: Bearer <token>` with a
constant-time compare against `GRITZ_DRIVER_TOKEN` from its environment. The
backend generates 32 random bytes per sandbox when it creates it and stores them in
`Handle.Data`. Lambda cannot inject a per-sandbox value into a snapshot, so on
Lambda the variable is unset and the server relies on the managed proxy's
port-scoped auth tokens, as the shim does today.

```mermaid
sequenceDiagram
    participant P as Platform
    participant D as Driver server
    participant F as Sandbox disk

    P->>D: start process
    D->>F: read /gritz/run.json
    alt state = running (process died mid-run)
        D->>F: outbox += failed(version), Finished(version)
        D->>F: write {version, finished, finished_at: now}
    else finished / absent
        Note over D,F: keep as is
    end
    D->>D: serve
```

### Backend interface

`Backend` and the earlier `Sandbox` split are merged into one interface. It
manages sandboxes; the runner speaks the driver protocol over the client `Dial`
returns.

```go
// Backend runs task sandboxes on a concrete runtime. It manages the sandbox
// itself and never speaks the driver protocol beyond Dial.
type Backend interface {
	ValidateWorkspace(ws *workspace.Workspace) error

	// Start ensures a running sandbox for spec and blocks until the platform
	// reports it running. With reuse == nil it creates one; otherwise it starts
	// or resumes the exact sandbox reuse identifies, and never creates a fresh
	// one. Starting a running sandbox is a no-op.
	//
	// The returned handle is non-nil whenever a sandbox exists, including
	// alongside an error: a sandbox that was created but failed to come up is
	// returned with the error so the runner can record it and destroy or retry
	// it later. A gone reuse sandbox returns (nil, ErrGone); a sandbox in a
	// transitional state (SUSPENDING, pausing) returns (reuse, error).
	Start(ctx context.Context, spec *Spec, reuse *Handle) (*Handle, error)

	// Dial returns an authenticated client for the sandbox's driver server.
	Dial(ctx context.Context, h Handle) (driverv1connect.DriverServiceClient, error)

	// Status reports the platform's view of the sandbox: Up, Parked or Gone.
	// Transitional states (SUSPENDING, pausing) report Parked.
	Status(ctx context.Context, h Handle) (Status, error)

	// Park stops compute while preserving the sandbox, blocking until the
	// platform reports the stable parked state.
	Park(ctx context.Context, h Handle) error

	// Destroy deletes the sandbox. Destroying an absent sandbox is not an error.
	Destroy(ctx context.Context, h Handle) error

	Close() error
}
```

`Start` is one call rather than separate create and start steps because not
every platform has both: Docker separates `ContainerCreate` from
`ContainerStart`, but most VM and sandbox APIs create and boot in a single call.
A backend whose platform does separate them returns the handle from the create
step alongside any error from the start step.

The runner's rule is the same for every call site: if `Start` returned a handle,
write it to `taskstate/` before handling the error.

```go
h, err := r.backend.Start(ctx, spec, reuse)
if h != nil {
	r.record(task, *h)
}
if err != nil {
	return err
}
```

`Spec` shrinks to what the platform needs to create the sandbox: `TaskID` and
`Workspace`. `Cmd`, `Env` and `Files` move into `RunSpec`. `Launch`, `Probe`,
`Signal`, `Wait` and `ExitCode` are removed.

### Runner

`runner.go` takes over what the backend used to hide. A per-task lock serializes
`Start`, `Kill`, `Remove` and the idle park.

| Runner method | With the driver server |
|---|---|
| `Start(task)` | Under the lock. `backend.Start(spec, reuse)` with the recorded handle, if any; write any returned handle to `taskstate/`, then return the error, if any. Then `Dial`, poll `Health` with backoff (timeout → error). A running record for this version: a no-op. Otherwise fetch events after `Health.event_token` (and links on the first run), mint the task token, `Run(version, spec)` until `Accepted`, cancel any idle timer, and hand the stream to `supervise`. |
| `supervise` | Reads the stream. `runner_event` → enqueue on the runner event outbox; `log_chunk` → `AppendLogChunk`; then `Ack(seq)` (batched). `Finished` for the task's current version → release the slot, arm the idle timer, return. On a stream drop: `Status`; gone → failed backstop and remove the record; parked → failed backstop; up → re-dial, `Health` with backoff (timeout → failed backstop), then `Run(version)` to attach and replay. |
| `Load` | For each record: `Status`; gone → as today; parked → `failIfTaskRunning`; up → `Dial`, `Run(version)` to attach. The replay delivers whatever the previous runner process missed, including a `Finished` it never saw. |
| `Running` | `Status` up and `Health` reports a running record for this version. |
| `Kill(task)` | Under the lock: `Stop(version)`; `signalled = stopped`. The terminal event arrives through the stream. |
| `Remove(task)` | Under the lock: cancel the idle timer, `Destroy`, remove the record. |
| `Prune` | Unchanged, on top of `Status`. |

The failed backstop is now only for sandbox loss and an unreachable driver. A
driver crash is reported by the restarted driver through its outbox, and a run
that finished while the runner was down is reported when the runner reattaches.

**Acknowledgement.** An entry is acknowledged once it is durable on the runner's
side: a runner event once `Enqueue` returns (the runner's outbox delivers it to
the server), a log chunk once `AppendLogChunk` succeeds. Entries are processed in
order, so a failed `AppendLogChunk` is retried with backoff before later entries;
a permanent failure drops the chunk and moves on. A runner that crashes after
delivering an entry but before acknowledging it delivers it again on reattach.
The server's version guard already absorbs a repeated runner event. Repeated log
chunks are an open question.

**Event fetching.** The runner fetches the run's events with
`ListEventsByTask` from `Health.event_token`, filtered to the instruction and
external types, exactly as `drainEvents` does today, and the links with
`ListLinks` on the first run (`Health.event_token` empty and no run record).

### Idle parking

`Workspace` gains a backend-agnostic field:

```yaml
workspaces:
  pets-workshop:
    idle_timeout: 5m   # default 0: park as soon as the run finishes
```

When `supervise` sees `Finished`, the runner arms a timer for `idle_timeout`.
When it fires, it takes the task's lock and checks `Health`: it parks only if the
record is finished, `finished_at + idle_timeout` has passed, and the outbox has
been drained, so a `Start` that won the lock in between is never undone. With an
idle timeout of 0 this is today's behavior. `Load` re-derives timers from
`finished_at` for sandboxes that are up and idle.

The semaphore slot is released on `Finished`, not when the sandbox parks, so warm
sandboxes never block new runs. The idle timeout is the only bound on what warm
sandboxes cost.

If the runner restarts mid-park, the next `Start` may find the sandbox in a
transitional state. `backend.Start` returns a plain error: the run fails, the
taskstate record is kept, and a later start or archive proceeds normally.

### Platform implementations

| | Docker | Lambda MicroVMs |
|---|---|---|
| `Start`, fresh | `ContainerCreate` with `Cmd: [BinaryPath, "driver", "--serve"]`, `GRITZ_DRIVER_TOKEN` in `Env`, labels as today; tar-copy the prebuilt binary; `ContainerStart`. Any failure after `ContainerCreate` returns the handle with the error. | `RunMicrovm` with no run-hook payload, then poll until `RUNNING`. Any failure after `RunMicrovm` returns the handle with the error. |
| `Start`, reuse | `ContainerStart` | `RUNNING` → no-op; `SUSPENDED` → `ResumeMicrovm`, poll; `SUSPENDING` → plain error; terminal → `ErrGone` |
| `Dial` | container IP on its network, port 8080, bearer from `Handle.Data` | managed proxy endpoint, minted port-scoped token |
| `Status` | inspect: running → Up, exited → Parked, not found → Gone | `GetMicrovm` |
| `Park` | `docker stop` with an explicit timeout covering a graceful server shutdown | `SuspendMicrovm`, then poll until `SUSPENDED` |
| `Destroy` | `ContainerRemove` (force) | `TerminateMicrovm` |

Lambda changes:

- The image entrypoint (`internal/runner/backend/lambdamicrovm/microvm.Dockerfile`)
  becomes `gritz driver --serve --aws-lambda-hooks`. The flag mounts
  `awsmicrovm.Handler` with nil funcs on `awsmicrovm.HookPort`, which acknowledges
  every hook with 200. No hook does work.
- The run spec travels in `Run` over the proxy, so the `Stager` interface,
  `awsmvm.S3Stager`, `LambdaMicroVM.StagingBucket` and the S3 permission on the
  execution role are removed.
- `internal/runner/microvmshim` and `gritz tool microvm-shim` are deleted.

### Sequences

Start:

```mermaid
sequenceDiagram
    participant S as gritz Server
    participant R as Runner
    participant T as taskstate/
    participant P as Backend / Platform
    participant D as Driver server
    participant F as Sandbox disk

    R->>R: lock(task)
    R->>P: Start(spec, reuse?)
    P->>P: create / start / resume, wait until running
    alt gone
        P-->>R: nil, ErrGone
    else created or transitional, then failed
        P-->>R: handle, error
        R->>T: write {version, handle}
    end
    P-->>R: handle, nil
    R->>T: write {version, handle}
    R->>P: Dial(handle)
    loop until healthy (backoff, timeout -> error)
        R->>D: Health()
    end
    D-->>R: {run, event_token}
    R->>S: ListEventsByTask(event_token), ListLinks (first run)
    R->>S: CreateTaskToken
    R->>D: Run(version, spec)
    alt record.version > version
        D-->>R: FailedPrecondition (stale)
    else record.version == version
        D-->>R: Accepted{attached: true}
    else no record or older, idle
        D->>F: write {version, running}
        D->>D: provision files (marker-gated)
        D->>D: build log, shipper, secret mask
        D-->>R: Accepted{attached: false}
        D->>F: outbox += started(version)
    else another version running
        D-->>R: Aborted (busy)
    end
    R->>R: cancel idle timer, unlock, supervise(stream)
```

Supervise:

```mermaid
sequenceDiagram
    participant S as gritz Server
    participant O as Runner outbox
    participant R as Runner
    participant P as Backend / Platform
    participant D as Driver server
    participant F as Sandbox disk

    loop until Finished(version)
        D->>F: outbox += entry
        D-->>R: Entry{seq, ...}
        alt runner_event
            R->>O: Enqueue
            O->>S: SubmitRunnerEvents
        else log_chunk
            R->>S: AppendLogChunk
        end
        R->>D: Ack(seq)
        D->>F: drop entries <= seq
        opt stream dropped
            R->>P: Status
            alt gone / parked
                R->>O: Enqueue failed (backstop)
            else up
                R->>D: Health() (backoff, timeout -> backstop)
                R->>D: Run(version) (attach, replay after ack)
            end
        end
    end
    R->>R: release slot, arm idle timer
```

Runner crash and reattach:

```mermaid
sequenceDiagram
    participant R as Runner (new process)
    participant T as taskstate/
    participant P as Backend / Platform
    participant D as Driver server
    participant F as Sandbox disk

    Note over D,F: run continued; entries accumulated unacknowledged
    R->>T: list records
    R->>P: Status(handle)
    P-->>R: Up
    R->>P: Dial(handle)
    R->>D: Run(version)
    D->>F: read outbox after last ack
    D-->>R: Accepted{attached: true}
    D-->>R: Entry{seq: n+1}, Entry{seq: n+2}, ...
    Note over R,D: then live entries, as in Supervise
```

Kill:

```mermaid
sequenceDiagram
    participant R as Runner
    participant D as Driver server
    participant F as Sandbox disk

    R->>R: lock(task)
    R->>D: Stop(version)
    alt version is the active run
        D->>D: cancel run context (ErrStop)
        D->>D: agent / setup process groups get SIGTERM, then SIGKILL after WaitDelay
        D->>F: outbox += stopped(version), Finished(version)
        D-->>R: stopped: true
    else not running / other version / unreachable
        D-->>R: stopped: false
    end
    R->>R: unlock
    Note over R,D: stopped and Finished arrive on the supervise stream
```

Idle park:

```mermaid
sequenceDiagram
    participant R as Runner
    participant P as Backend / Platform
    participant D as Driver server

    Note over R: timer armed on Finished, or re-derived by Load from finished_at
    R->>R: timer fires, lock(task)
    R->>D: Health()
    alt run active, outbox not drained, or idle_timeout not elapsed
        Note over R: skip
    else finished and idle long enough
        R->>P: Park (blocks until stable)
        alt Docker
            P->>D: docker stop: SIGTERM, server shuts down, container exits
        else Lambda
            P->>P: SuspendMicrovm, poll until SUSPENDED
        end
    end
    R->>R: unlock
```

### What doesn't change

The taskstate store, the runner event outbox, the server, the database schema,
the task state machine, and `gritz.proto`. The terminal event's meaning is
unchanged: the driver decides `started`, `stopped` and `failed`, and the server's
version guard drops a stale one. Only the path changes, from driver → server to
driver outbox → runner outbox → server.

## Implementation Plan

1. **Lambda transitional-state fix** — Delivers: `tryResume` returns a plain error
   for `SUSPENDING` (keeping `ErrGone` for `TERMINATING`/`TERMINATED`), and `Wait`
   polls until `SUSPENDED` after `SuspendMicrovm`. Depends on: nothing.
   Verifiable by: `lambdamicrovm` unit tests against the fake `Cloud`.
2. **`driver.v1` proto** — Delivers: `proto/driver/v1/driver.proto` and generated
   code. Depends on: nothing. Verifiable by: `mise run generate` produces a
   compiling package.
3. **Per-run environment** — Delivers: `agent.Driver.Env`, threaded to every agent
   CLI, setup commands and `Cwd` expansion; `gritz driver` sets it to
   `os.Environ()`, so behavior is unchanged. Depends on: nothing. Verifiable by:
   agent unit tests asserting the child env.
4. **Driver inputs and outputs behind interfaces** — Delivers: `agent.Driver`
   takes the task, events and links as inputs and reports runner events and log
   chunks through a sink interface. `gritz driver` (one-shot) implements both with
   `gritzclient`, so behavior is unchanged. Depends on: nothing. Verifiable by:
   existing driver tests with a fake sink.
5. **Outbox replay and run record** — Delivers: `FileStore.ReadFrom(seq)` and
   ack-up-to-seq; a package reading/writing `/gritz/run.json` with boot
   reconciliation (`running` → `failed` + `Finished` in the outbox). Depends on:
   nothing. Verifiable by: unit tests in a temp dir.
6. **Driver server** — Delivers: `gritz driver --serve` implementing
   `DriverService` over `agent.Driver` with the outbox sink, per-run log and
   secret mask, bearer auth, `--aws-lambda-hooks`. `gritz driver` (one-shot)
   stays. Depends on: (2), (3), (4), (5). Verifiable by: in-memory Connect client
   tests with a dummy agent: start, attach, replay after a dropped stream, `Ack`
   trimming, stale/busy/not-found, `Stop`, restart reconciliation.
7. **Backend interface and runner** — Delivers: the merged `Backend` interface,
   the runner's driver protocol (`Start`, `supervise`, `Load`, `Kill`), the
   per-task lock, idle-park timers, `Workspace.IdleTimeout`. Depends on: (2),
   (6). Verifiable by: runner tests against a fake `Backend` and an in-memory
   driver server, including a runner restart mid-run and a park racing a start.
8. **Docker on the new interface** — Delivers: Docker `Start`/`Dial`/
   `Status`/`Park`/`Destroy` with the `driver --serve` entrypoint. Depends on:
   (7). Verifiable by: the existing Docker e2e tests passing, plus idle-timeout,
   reuse and runner-restart e2e cases.
9. **Lambda on the new interface** — Delivers: the Lambda implementation, new
   image entrypoint, README update. Depends on: (7). Verifiable by: an image built
   per the README running a task end to end, including suspend/resume.
10. **Remove the shim, staging and one-shot driver** — Delivers: deletion of
    `internal/runner/microvmshim`, `gritz tool microvm-shim`, the `Stager`,
    `awsmvm.S3Stager`, `LambdaMicroVM.StagingBucket`, and the flag-driven
    `gritz driver`. Depends on: (8), (9), and the migration question below.
    Verifiable by: build and tests.

Steps 7–9 land together or behind a flag: the old and new `Backend` interfaces
cannot coexist in `runner.go`.

## Trade-offs

**Driver as server vs. a shim.** A shim (today's `microvmshim`, or the generic
shim proposal) keeps the driver one-shot and isolates each run in its own
process, which gives a fresh environment, a real exit code and crash containment
for free. The cost is a second in-sandbox component, a supervisor contract, and
for the generic shim a second release artifact. Making the driver the server
removes all of that; the price is the per-run refactor of the driver and a
weaker failure mode, below.

**In-process runs vs. a child process per run.** A server that spawns
`gritz driver run` per `Run` would keep today's driver unchanged, but it is the
shim's supervisor role folded into the gritz binary rather than removed. In-process
runs mean a panic in one run kills the server: the restarted driver reports the
run as failed from its run record, but the real exit code is gone.

**Driver reports through the runner vs. directly to the server.** Routing
everything through the runner means the sandbox needs no route to the server for
the driver's own traffic, the runner sees every outcome first-hand (no exit-code
signalling, no race between the driver's report and the runner's backstop), and
the server's inputs come from one component. The cost is that the runner is now
on the path of every log byte, and a runner that is down delays (but does not
lose) the driver's reports until it reattaches.

**Driver outbox vs. runner-side cursor.** The driver holds the acknowledgement
cursor, so the runner persists nothing new per entry and a reattach is just
`Run(version)`. The cost is at-least-once delivery for the entry in flight when
the runner crashes.

**Runner owns the driver protocol vs. a `DriverBackend` adapter.** An adapter
would keep `runner.go` unchanged behind the old `Launch`/`Wait`/`Probe`
interface, but `Launch` could only return the handle after the run started, and
the outbox replay would be hidden behind `Wait`. With one sandbox-only `Backend`,
the handle is persisted as soon as `Start` returns, before the run starts, and
the runner deals with runs directly. The cost is a larger change to `runner.go` and its tests.

**Idle timer in the runner vs. the driver or the platform.** The driver knows
idleness exactly but holds no platform credentials, and on Lambda or DigitalOcean
a process exit does not park. Platform idle policies (Lambda `idlePolicy`,
DigitalOcean `auto_pause`) judge idleness by activity, not by "no active run", so
a quiet agent could be parked mid-run; Lambda's minimum is also 600s. The runner
has both the knowledge (`Health`) and the credentials (via the backend).

**One `Start` returning a handle with its error vs. separate `Create` and
`Start`.** A separate `Create` would let the runner persist the handle before
the sandbox boots, closing the window in which a runner crash mid-boot leaks the
sandbox. But many platforms create and boot in one blocking call, so `Create`
could not be implemented as specified there. Returning the handle alongside the
error covers every failure the backend can observe on all platforms; only a
runner crash during `Start` is left uncovered, which is open question 10.

**Server streaming plus `Ack` vs. bidirectional streaming.** A bidi `Run` would
carry acknowledgements on the same stream, but needs HTTP/2 end to end, which
Lambda's managed proxy does not provide.

**Connect vs. grpc-go.** Connect handlers are plain `net/http`, already used
throughout the codebase, and speak gRPC as well as protocols that pass HTTP/1.1
proxies. grpc-go requires HTTP/2 end to end.

## Open Questions

1. **Agent MCP and shell relay.** The injected `gritz tool agent-mcp` server and
   the debug-shell relay (`shell.Serve`) still dial the server with the task
   token. Are they in scope? The MCP tools are request/response, which the `Run`
   stream cannot carry; they would need their own RPC on the driver that the
   runner proxies, or keep going direct.
2. **Duplicate log chunks.** A chunk delivered but not acknowledged before a
   runner crash is appended twice. Should `AppendLogChunkRequest` gain a sequence
   number the server deduplicates on, or is a rare repeated chunk acceptable?
3. **Docker reachability.** The runner dials the container's IP. That works when
   the runner is on the host or shares a network with the container. Is that
   always true for existing deployments, or does Docker need a published port or
   a bind-mounted unix socket instead?
4. **Migrating existing sandboxes.** Containers and MicroVMs created before
   steps 8/9 run the one-shot driver. Should the runner detect old handles (by
   `Handle.Type` or a container label) and keep the old path until those tasks
   are archived, or fail them with a clear error?
5. **Driver revalidation.** A sandbox that stays up across a gritz release keeps
   serving with the old driver. Should `Health` report the driver version so the
   runner can park and restart stale drivers between runs?
6. **Lambda process exit.** What does Lambda do when the main process of a running
   MicroVM exits (a driver crash)? If the VM stays `RUNNING`, the health timeout
   covers it, but `Start` has no way to restart the process; should the runner
   destroy it and treat it as gone?
7. **Outbox size.** A runner that stays down for a long run leaves the outbox
   growing with log chunks. Should the driver cap it (dropping the oldest log
   chunks, never runner events)?
8. **Health timeout values.** How long should `Start` and `supervise` wait for a
   healthy driver before failing? Docker boots in seconds; a fresh Lambda VM may
   take longer.
9. **Idle-timeout limits.** Should the runner cap `idle_timeout` (warm sandboxes
   hold host memory or account quota outside the concurrency semaphore), and
   should warm sandboxes count toward any limit?
10. **Runner crash during `Start`.** A runner that dies while `Start` is
    blocking never receives the handle, so a sandbox the platform created is not
    recorded. Should `Prune` find such sandboxes by the gritz labels or tags the
    backend sets on create and destroy those with no taskstate record?
