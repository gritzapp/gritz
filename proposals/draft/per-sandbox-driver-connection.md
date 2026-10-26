# Per-sandbox driver connection for runner flows

Issue: https://github.com/gritzapp/gritz/issues/1815

## Problem

The driver server (`proposals/draft/driver-server.md`) is a long-lived process
that runs many versions of a task, one after another, in one sandbox. The
runner's flows in `internal/runner/taskmanager.go` and
`internal/runner/taskrunner.go` still have the shape of the container-per-run
era. #1815 lists the symptoms:

- Booting the sandbox and starting a run are separate steps. `running` and
  `ErrRunning` glue them together.
- Every `Finished` parks the sandbox.
- Restart is `Kill`, then park, then `ErrRunning`, then the next poll, then boot
  again.
- After each stream drop, `attach` decides whether the driver is reachable by
  checking the sandbox's platform status. On top of that, `driverclient` puts a
  separate idle timeout on each stream.
- `TaskRunner` state (`sbx`, `rec`, `running`, `cancel`) is changed under `mu`
  by `Boot`, `Run`, `Kill`, `Destroy`, `Retire` and `Restore`.

### How each flow works today

The table maps each flow as of `3aae118c` and the container-per-run assumption
it still carries.

| Flow | Today | Assumption it carries |
|---|---|---|
| **Boot** | `TaskManager.Start` mints a token and builds the spec. `TaskRunner.Boot` creates the sandbox, or reuses it and stamps the record with the new version. It calls `sbx.Boot` and sets `running`. If `running` is already set, it returns `ErrRunning`. | Booting is the first step of every run: a run needs a sandbox brought up for it. |
| **Start a run** | `TaskRunner.Run` → `run` → `startRun`. It calls `sbx.Dial` (a fresh client), polls `Status` until the driver answers within `ReadyTimeout`, then calls `accept`. `accept` opens `Run(version, spec)`, waits for `Accepted` and **closes the stream**. `attach` then opens a second stream without a spec. `accept` is retried on any `CodeUnavailable`. | A driver serves one run, so a start is "wait for the process to come up, then hand it its spec". `Unavailable` is treated as "not up yet", though connect-go also reports transport failures as `Unavailable`. Such a failure can come after the driver has already started the run. |
| **Follow a run** | `attach` loops on `attachOnce`. Every `attachOnce` calls `sbx.Dial` again. After each drop it calls `sbx.Status`. Parked or gone means the run is lost. Up means back off and retry until `LostTimeout` passes without an accepted stream, then park and report lost. `driverclient` cancels a stream that receives nothing for 45s (`ErrStreamIdle`). `NotFound`, `FailedPrecondition` and `Aborted` all mean "no such run" (`isTerminal`). | Driver liveness equals sandbox liveness, because the driver *was* the container. Each stream is watched for liveness on its own. A version mismatch can only mean the run is gone. |
| **Stop / kill** | `TaskRunner.Kill`: `sbx.Status`, then `sbx.Dial`, then `Stop(rec.Version)`. It returns the driver's `stopped`. `Poll`'s stop handler emits `stopped` itself if nothing was signalled. | Stopping is separate from following: it dials its own client and asks the platform first. |
| **Restart** | `Poll`'s restart handler returns early if `Version() == task.Version` (#1520). Otherwise it calls `Kill`, acquires a slot and calls `Start`. `Boot` returns `ErrRunning` until the old `Run` has seen `Finished` *and parked the sandbox*. The slot is released and a later poll boots the sandbox again (resuming it) for the new version. | A new version needs a new (or resumed) container. |
| **Finish / park** | `attach` calls `park` on every `Finished`, on `NotFound`, and on a parked sandbox. `Run` returns, and only then does `TaskManager.run` release the slot and wake `Poll`. | The sandbox exists for one run. Its compute is released when the run ends. |
| **Retire / destroy** | `Destroy` destroys the sandbox, cancels the `Run` with `errRemoved` (reported lost) and clears the record. `Retire` (from `Prune`) refuses while `running` is set or the sandbox is up. | An up sandbox means a running run. |
| **Restore** | `Load` → `TaskRunner.Restore`. It opens the handle and checks `sbx.Status`. Up sets `running` and starts `Run(nil)`, which reattaches and parks on `Finished`. Parked or gone triggers `failIfTaskRunning`. `Load` seeds the semaphore with the count of up sandboxes. | Up means a run is active, so it gets a slot. |
| **Poll** | Start: skip if `task.Status == Running`, or if `Running()` (the sandbox is up). Otherwise `TryAcquire`, `Start`, and `ErrRunning` releases the slot. Restart: as above. Stop: `Kill`. `Prune` runs between passes. | "Sandbox up" stands in for "run active". driver-server.md, Problem 2, names the same confusion for `Probe`. |

Two more gaps turned up while reading this code:

- **A stop that arrives before a restart is picked up.** `Kill` stops
  `rec.Version`. Suppose the task is at version V+1 with `restart` pending and the
  user stops it before the runner has handled the restart. `Kill` stops run V and
  returns `stopped: true`, so `Poll` does not emit `stopped`. The driver's
  `stopped(V)` is then dropped by the server's version guard (`Task.ApplyRunnerEvent`),
  and the task stays in `cancelling`. This is my reading of the code. I have not
  reproduced it.
- **Retrying the start.** A retried `accept` carries the spec. Version
  idempotency (`driverserver.Server.open` attaches when `record.version ==
  version`) makes that safe *only while the driver still has its record*. A
  driver that lost its record starts the run a second time.

## Design

### Overview

The design follows `golang.org/x/net/http2`: one long-lived connection carrying
many streams, with one goroutine that owns the connection's state. It borrows
the state ownership, the liveness checks and the retry rules. It does not borrow
the framing. The driver protocol stays Connect over whatever transport the
backend provides.

```
TaskManager ── commands ──▶ TaskRunner.serve (one goroutine per task)
   ▲                           │ owns: sandbox, record, run, want, op, conn
   │ TaskEvents                │
   └───────────────────────────┤ while the sandbox is up:
                               ▼
                         driverConn  (one per boot: one Dial, health, streams)
                           │   ▲
                 unary ops │   │ readerMsg (one reader goroutine per open stream)
                           ▼   │
                     driver server in the sandbox
```

- **`TaskRunner.serve` is the one goroutine that owns the task's state**: the
  sandbox, its record, the connection, the current run, the run a command asked
  for, and the one operation in flight. Commands (`Start`, `Restart`, `Stop`,
  `Destroy`, `Retire`, `Restore`, `State`) reach it as messages and get their
  replies on a channel. This replaces `TaskRunner.mu`, `running`, `cancel` and
  `ErrRunning`.
- **`driverConn` is the per-sandbox connection.** It is created when the sandbox
  comes up and closed when it parks, is destroyed, or is found gone. It holds the
  one `DriverServiceClient` from a single `sbx.Dial`, the health-check state and
  the open `Run` stream. Runs are streams on it, so start, restart and stop never
  touch the sandbox.
- **Driver liveness is checked at the connection level**, with a read-idle ping
  modelled on http2's `ReadIdleTimeout`/`PingTimeout`. Platform status is
  consulted only when the connection is lost, at boot, and at restore.
- **A start is retried only when the driver says it never started the run**:
  an explicit refusal, or a `Status` probe showing the run record is still
  below the version. A reattach never carries the spec.
- **Restart is one driver RPC.** `Restart(version, spec)` stops the active run
  and starts the new version under the driver's start lock. An open `Run` stream
  gets `Restarted` and keeps streaming the new run.
- **The sandbox parks only when the task is idle**: no run, no run queued, and
  `IdleTimeout` elapsed. It no longer parks after every `Finished`.

### What is borrowed from http2

All references are to `golang.org/x/net@v0.58.0/http2`, the version in
`go.mod`, read from the module cache.

| http2 | Where | What we take |
|---|---|---|
| `serverConn.serve` owns all connection state. Everything else talks to it over channels (`readFrameCh`, `wantWriteFrameCh`, `serveMsgCh`), timers post messages instead of touching state (`onReadIdleTimer` → `sendServeMsg(readIdleTimerMsg)`), and `serveG.check()` asserts that a function runs on the serve goroutine. | `server.go:766`, `:438`, `:946-966` | `TaskRunner.serve`: one `select` over commands, op results, reader messages and timer messages. Helper goroutines never touch state. They post results. |
| The serve loop never blocks on the network. Writes go out asynchronously and report back on `wroteFrameCh`. Frames are read by `readFrames` on its own goroutine. | `server.go:695`, `:834-861` | Every RPC and platform call runs on a helper goroutine with its own context, and its result comes back as a message. One op is in flight at a time, so a slow `Park` or a `Restart` that waits for the old run to stop never blocks `Stop`, `State` or the health check. |
| One read loop per connection (`clientConnReadLoop.run`) dispatches frames to streams by ID (`streamByID`). A frame for a stream the client already forgot is ignored ("We'd get here if we canceled a request while the server had its response still in flight. So if this was just something we canceled, ignore it."). | `transport.go:1869`, `:2415`, `:1937-1945` | Each stream's reader tags its messages with the stream's ID and the version it was opened for. `serve` drops messages and results from streams it has already replaced. That is how a reattach that lands after a restart is handled (see [Restart](#restart)). |
| Health check: the read loop resets a timer on every frame. After `ReadIdleTimeout` with no frame it pings with `PingTimeout` (`healthCheck`). A failed ping calls `closeForLostPing`, and `cleanup` aborts every stream with the connection error. The server mirrors this in `handlePingTimer`, with one ping in flight (`pingSent`). | `transport_common.go:131-142`, `transport.go:558-571`, `:896-902`, `:1776-1830`, `server.go:915-940` | `ReadIdleTimeout` and `PingTimeout` on `TaskRunnerOptions`. Any message from the driver, on a stream or as a unary reply, counts as a read. The ping is `Status`. A lost ping aborts the open stream and moves the connection to `connLost`. |
| `canRetryError` retries only `errClientConnUnusable` (the request never left), `errClientConnGotGoAway` (the server said it will not process it) and `REFUSED_STREAM`. `shouldRetryRequest` refuses to replay a body that may have been sent. `roundTripViaPool` retries the first failure immediately, then backs off exponentially with 10% jitter. | `transport_common.go:328-367`, `:269-322` | A start or restart is retried with its spec only after an explicit refusal (`Refused` detail, below) or after a `Status` probe shows the version was never started. Any other failure is ambiguous and is never replayed with the spec. A dropped stream is reopened at once, then with backoff. |
| GOAWAY carries `LastStreamID`. On the client, `setGoAway` leaves streams at or below it alone ("The server's GOAWAY indicates that it received this stream") and aborts the rest with `errClientConnGotGoAway`, which `canRetryError` retries. The server writes `maxClientStreamID` into it. | `transport.go:580-614`, `server.go:1277-1279` | The driver's run record version is the "last stream ID". A version above it was never started and is safe to start, and one at or below it must be attached to and never started. `Refused.last_version` and `StatusResponse.run.version` carry it. |
| RST_STREAM ends one stream without touching the connection (`processResetStream` → `abortStream`). The server answers an over-limit stream with `REFUSED_STREAM` before processing it. | `transport.go:2587-2604`, `server.go:1912-1926` | Cancelling a `Run` stream detaches from the run and nothing else, as today. The driver's "another Run stream is open" and "driver is shutting down" are refusals before any work, so they get the `Refused` detail. |

One difference matters throughout. In http2 a stream *is* the request: resetting
it cancels the work. Here the run is work on the driver that outlives any one
stream. `Stop` and `Restart` act on the run, and a stream only follows it. A run
is therefore followed by a succession of `Run` streams on one connection: the
first carries the spec, and every reopen is a spec-less attach.

### State

Everything below is a field of `TaskRunner` that only `serve` reads or writes.

```go
// sandboxState is where the task's sandbox is in its lifecycle.
type sandboxState int

const (
	sandboxNone       sandboxState = iota // no sandbox, no record
	sandboxRestoring                      // Restore: opened from a record, status unknown
	sandboxBooting                        // creating and/or booting
	sandboxUp                             // platform up; conn is non-nil
	sandboxParking                        // Park in flight
	sandboxParked                         // parked husk; record kept
	sandboxDestroying                     // Destroy in flight
)

// connState is the driver connection's health while the sandbox is up.
type connState int

const (
	connConnecting connState = iota // waiting for the first Status reply (ReadyTimeout)
	connHealthy                     // a read within ReadIdleTimeout
	connPinging                     // read-idle: one Status ping in flight (PingTimeout)
	connLost                        // ping failed: streams aborted, recovering until LostTimeout
)

// runState is the state of the run the TaskRunner follows (version).
type runState int

const (
	runNone       runState = iota // no run: the task is idle
	runStarting                   // Run(version, spec) sent, no Accepted yet
	runActive                     // accepted; followed by a stream (or reopening one)
	runStopping                   // Stop accepted by the driver; waiting for Finished
	runRestarting                 // Restart(version, spec) sent; previous run being replaced
)

// opKind is the one operation serve has in flight.
type opKind int

const (
	opNone opKind = iota
	opCreate
	opBoot
	opStart   // Run with spec, until Accepted
	opRestart // Restart RPC
	opStop    // Stop RPC
	opProbe   // Status after an ambiguous start/restart failure
	opPark
	opDestroy
	opRestore // Open + sbx.Status
)
```

`serve` also holds:

- `sbx backend.Sandbox` and `rec taskstate.Record`, as today.
- `conn *driverConn`: the client, `state connState`, `lastRead time.Time` and
  `stream *runStream` (`id`, the `version` it was opened for, `cancel`,
  `lastRead`). It is nil unless `sandbox == sandboxUp`.
- `run runState` and `version int64`: the run being followed. `version` equals
  `rec.Version` whenever `run != runNone`.
- `want *wantedRun`: the run a command asked for that has not been handed to the
  driver yet (spec, run spec, `restart` flag), or nil.
- `op opKind` with the op's `cancel`. There is at most one op.
- `slot slotState` (`slotNone`, `slotHeld`): whether this task holds a
  concurrency slot (see [Poll loop and the semaphore](#poll-loop-and-the-semaphore)).
- `idle *time.Timer`: the park timer, armed only in `runNone` with no `want`.

**The loop.** Each iteration handles one message, then calls `step()`, which
compares what is wanted with what is, and starts the next op if none is in
flight:

| Condition (checked in order) | Next op |
|---|---|
| a `Destroy` is pending | `opDestroy` (cancels any op and the stream first) |
| `want != nil`, no sandbox | `opCreate`, then `opBoot` |
| `want != nil`, `sandboxParked` | `opBoot` |
| `want != nil`, `sandboxUp`, `connHealthy`, `run == runNone` | `opStart` |
| `want != nil` with `restart`, `sandboxUp`, `connHealthy`, `run == runActive` | `opRestart` |
| `run` is `runActive`/`runStopping`/`runRestarting`, `connHealthy`, no stream | open an attach stream (not an op: a reader goroutine) |
| `run == runNone`, `want == nil`, `sandboxUp`, idle timer expired | `opPark` |

A `want` without `restart` while a run is active waits for that run's
`Finished`. That is the queued successor (see [Start](#start-a-run)).

### The connection

**Dial once.** When a boot or restore leaves the sandbox up, `serve` calls
`sbx.Dial(ctx, "")` once and builds a `driverConn` in `connConnecting`. Every
call for that sandbox's lifetime goes through that client: ready pings, starts,
attaches, stops, restarts, health pings, and later `Trim`. `backend.Sandbox.Dial`
keeps its signature. Its doc changes from "per call" to "once per boot".

**Readiness.** `connConnecting` is today's `startRun` `Status` loop: ping with
doubling backoff (`RetryInterval` up to `readyMaxInterval`) until the first
reply, within `ReadyTimeout`. The first reply moves the connection to
`connHealthy` and also tells `serve` the driver's run record, which restore
uses. When the timeout expires, a pending start is reported lost (the run could
not be started), as today, and the sandbox is parked.

**Health check.** This replaces the per-stream checks.

- Every message from the driver resets `conn.lastRead`. That covers each stream
  message (`KeepAlive` included) and each unary reply (`Status`, `Stop`,
  `Restart`, `Trim`). An `AfterFunc` timer posts `readIdleMsg` to `serve`, as
  `onReadIdleTimer` does.
- `readIdleMsg` in `connHealthy`, with no read for `ReadIdleTimeout` (default
  30s, twice the driver's 15s `KeepAlive`): `serve` sends one `Status` ping with
  `PingTimeout` (default 15s, http2's default) and moves to `connPinging`. While a
  stream is open its `KeepAlive`s keep `lastRead` fresh, so in practice pings run
  only while no stream is open, which means between runs on a warm sandbox. This
  matches http2: "if there is no other traffic on the connection, the health
  check will be performed every ReadIdleTimeout interval".
- A ping reply moves the connection back to `connHealthy`.
- A failed ping moves it to `connLost`, as `closeForLostPing` does. The open
  stream is cancelled. Then `serve` recovers. It pings with doubling backoff
  (`RetryInterval` to `RetryMaxInterval`). After each failure it checks
  `sbx.Status`:
  - gone: the active run is lost and the record is cleared;
  - parked: the active run is lost and `Park` is called to let it settle (a
    SUSPENDING VM), as `attach` does today;
  - up: keep trying until `LostTimeout` (default 2m) has passed since
    `lastRead`, then park, and the active run is lost. If the park fails, keep
    retrying both, as `attach` does today.

  A successful ping returns the connection to `connHealthy`, and `step()`
  reopens the stream.
- **Stream stall.** The connection can be healthy while its stream is silent.
  On Docker every call is its own exec tunnel, so a stream can die while pings
  succeed. If the open stream has not read for `ReadIdleTimeout + PingTimeout`
  while pings succeed, `serve` cancels it (the RST_STREAM analogue) and reopens
  it. The driver's 15s `KeepAlive` is what makes "silent" meaningful.

What changes:

- `attach` no longer calls `sbx.Status` after each drop. A dropped stream is
  routine: it is reopened at once, then with backoff. driver-server.md
  ("Supervise") already asks for this.
- **`driverclient` is deleted.** Its idle interceptor, `ErrStreamIdle` and the
  `KeepAlive` swallowing go. The Docker and Lambda backends build a plain
  `driverv1connect.NewDriverServiceClient`, and their `idleTimeout` options go.
  `KeepAlive` messages now reach the reader, which counts them as reads.
- **`KeepAlive` stays on the wire**, unchanged at 15s. It has three uses. It is
  the read that keeps a followed connection from pinging. It is how the driver
  notices a dead stream: a failed write releases `streaming`, which
  driver-server.md open question 12 relies on. And it keeps a stream inside the
  Lambda proxy's 15-minute idle cut. Without it the runner would ping every
  `ReadIdleTimeout` during every run, and the driver would hold half-open
  streams indefinitely.

The connection's physical transport does not change. Docker still tunnels each
HTTP request through its own exec (`DisableKeepAlives`), and Lambda still speaks
HTTP/2 to the managed proxy. `driverConn` is a logical connection: one client,
one health state, one owner. See open question 2 for a real single h2c tunnel on
Docker.

### Streams and the reader

`serve` opens a stream by starting a reader goroutine with
`(id, version, req, ctx)`, where `id` is a per-TaskRunner counter. The reader
calls `client.Run(ctx, req)` and sends a `readerMsg{id, version, ...}` to
`serve` for `Accepted`, for each `Finished` and `Restarted`, for every forwarded
entry (once the WAL lands), and finally `readerDone{id, version, err}`.
`KeepAlive` is reported as a bare read. The channel to `serve` is unbuffered, so
the reader is back-pressured the way `readFrames` waits on `readMore`.

`serve` handles a reader message only if `id == conn.stream.id`. Anything else
comes from a stream it has replaced or cancelled and is dropped, as `streamByID`
does. On a current stream:

| Reader message | Handling |
|---|---|
| `Accepted` | `runStarting` → `runActive` (for the start stream). Resets the reopen backoff. |
| `Restarted{from, version}` | Logged. `serve` set `version` before it sent `Restart`, so this only confirms it. The stream's `version` tag is updated. |
| `Finished{v}` with `v == version` | The run is over. Pre-WAL, `reported == false` sends `TaskLostEvent`, as today. Then `run = runNone`, and see [Finish, idle and park](#finish-idle-and-park). |
| `Finished{v}` with `v < version` | The end of a run that a restart replaced. Logged, otherwise ignored: the server's version guard has already dropped that run's `stopped`. |
| `readerDone` with no `Finished` | Classified as below. |

Classifying a stream that ended without the followed run's `Finished`:

- **Opened for a superseded version** (`version` tag < `serve`'s `version`):
  ignore the error, whatever it is, and reopen for the current version. This
  includes the `FailedPrecondition` that a reattach for V gets once the driver's
  record is at V+1.
- `NotFound`, `FailedPrecondition` or `Aborted` for the current version: the
  run is lost ("driver has no such run"), as `isTerminal` decides today. Only
  `NotFound` parks, as today, because on the other two a newer run is active.
- Anything else (a transport error, the reader's context cut by a lost ping):
  reopen. The first reopen is immediate, later ones back off from
  `RetryInterval` to `RetryMaxInterval`, as `roundTripViaPool` does. Whether the
  driver is unreachable is decided by the connection state, not by counting
  stream failures.

**A reattach never carries the spec.** Every stream after the first is
`Run{version}`, or `Run{version, after_seq}` once the WAL lands. The spec rides
only on `opStart`'s stream, and on `Restart`.

### Start a run

`TaskRunner.Start(ctx, spec, runSpec)` posts `startCmd`. `serve` replies at once:

| State when the command arrives | Effect | Reply |
|---|---|---|
| `run == runNone`, no `want` | `want = spec`. `step()` boots if needed, then `opStart`. | `StartAdmitted`: the caller's slot now belongs to the TaskRunner |
| run V-1 active (normally finishing) | `want = spec`. It starts on `Finished(V-1)`, without parking. | `StartQueued`: the active run's slot carries over and the caller releases its own |
| run V active/starting, or `want.version == V` | nothing | `StartDuplicate` |
| run or `want` above V | nothing | `StartStale` |

A V-1 run being active when start V arrives is the normal follow-up path. The
server bumps the version when the driver's `stopped(V-1)` arrives
(`applyRunnerEventStopped`, `Running` + `start` → `Pending`), and its
notification wakes `Poll`. All of that can happen before the driver has sent
`Finished(V-1)`. Today this returns `ErrRunning` and waits for the next poll
after the park. Now the start is queued and runs on the warm sandbox.

**Booting.** `opCreate` and `opBoot` are today's `Boot` minus the `running`
flag. Create the sandbox and record its handle at `want.version`, or stamp the
existing record. Then `sbx.Boot`. `ErrGone` clears the record and the run is
lost. Any other error keeps the sandbox and its record, and the run is lost. A
boot failure is reported through `TaskLostEvent` (now "the run could not be
started or was lost") followed by `TaskReleasedEvent`, instead of being returned
from `TaskManager.Start`. `Poll` no longer waits for the platform, which is the
"Launch blocks Poll" point in driver-server.md.

**`opStart`.** Stamp the record with `want.version` first (it already is after a
create), so that a runner that restarts reattaches. Set `version`, set
`run = runStarting`, clear `want`, and open the start stream
`Run{version, spec}`. The stream is kept after `Accepted` and becomes the follow
stream. Today's open-accept-close-reopen sequence goes away. Results:

| Result | Meaning | Action |
|---|---|---|
| `Accepted` | started, or attached if the driver already had V | `runActive` |
| error with `Refused` detail | the driver did nothing (another stream open, shutting down) | retry the same request after `RetryInterval`, within `ReadyTimeout`: the REFUSED_STREAM rule |
| `NotFound`/`FailedPrecondition`/`Aborted` | terminal | the run is lost, as today |
| anything else | **ambiguous**: the request may have reached the driver | `opProbe` |

**`opProbe`** is the GOAWAY rule. `Status` returns the run record. If
`record.version >= V` the driver has seen V, so open a spec-less attach (or
treat `>` as stale). If the record is absent or below V and not running another
version, V was never started, so retry `opStart` with the spec. If the probe
itself fails, the connection's health check takes over. The probe is not
retried on its own. While the connection is lost the start stays in
`runStarting`, and once the connection is healthy again `step()` re-probes. The
whole start is bounded by `ReadyTimeout`, as today. After that the run is lost.

### Stop

`TaskRunner.Stop(ctx, version)` replaces `Kill`. It takes the *task's* version,
not the record's:

| State | Effect | Reply |
|---|---|---|
| active/starting run U ≤ V, connection healthy | `opStop`: `Stop(U)`. The run moves to `runStopping` when `stopped` is true. Any `want` ≤ V is dropped. | `stopped = (U == V) && resp.stopped` |
| active run, connection not healthy | nothing | error (the stop command survives and is retried next poll, as today) |
| no active run; `want` ≤ V queued | drop it | `false` |
| otherwise | nothing | `false` |

`Poll` emits `stopped(V)` itself whenever the reply is `false`, as it does
today. Stopping an older run U < V (a stop that overtook a restart) now gives
`false`, so the task lands in `cancelled` instead of waiting for a `stopped(U)`
the server will drop. A queued start that is stopped before it began is simply
dropped. Its slot was the active run's, so nothing is released twice.

Stop never touches the sandbox or calls `sbx.Status`. Without a connection there
is no driver to ask, which is today's "a sandbox that is not up has no driver"
rule.

### Restart

#### Driver: the `Restart` RPC

```protobuf
// Restart replaces the active run with run version, under the driver's start
// lock:
//   record.version >  version            -> FailedPrecondition (stale)
//   record.version == version            -> no-op, attached: true
//   shutting down / another start held   -> Unavailable + Refused
//   a run is active (older version)      -> stop it (ErrStop), wait for it to
//                                           finish and record, then start
//                                           version with spec
//   no run active                        -> start version with spec
// Returns once the new run has started (its run record written). Every open
// Run stream that was following the replaced run gets Restarted and keeps
// streaming the new run.
rpc Restart(RestartRequest) returns (RestartResponse);
```

In `driverserver.Server`:

- A new `startMu` serializes the decisions that start a run: `open`'s start
  branch, `Restart`, and `Shutdown`'s `closed`. `mu` keeps guarding fields and
  is never held while waiting.
- `Restart` takes `startMu`, then `mu`, and checks the table. For an active run
  it sets `active.next = make(chan *run)` and cancels the run with
  `agent.ErrStop`. Then it releases `mu`, still holding `startMu`, waits on
  `active.done`, starts V through the existing `start` path (provision is
  marker-gated, run record `{V, running}`), sends the new `*run` on `next`, and
  returns `RestartResponse{attached: false}`.
- `Run`'s loop: on `r.done`, send `Finished(r.version)` as today. If `r.next`
  is set, wait for the next run, send `Restarted{from: r.version, version:
  next.version}`, set `r = next` and keep looping. Otherwise return.
- The run record is not written for V until the old run has finished and
  recorded. If the driver crashes in between, `Reconcile` marks the old run
  finished (crashed), and the runner's probe sees `record.version < V`, so
  retrying `Restart` is provably safe.
- Idempotent on version: a second `Restart(V)` (a retry after an ambiguous
  failure) waits on `startMu`, finds `record.version == V` and returns
  `attached: true` without starting anything.

The replaced run reports `stopped(U)` itself, as a `Kill` does today. The
server's version guard drops it, because the task is already at V. That is the
same as today, when the `restart` command's `stopped` "is rejected by the status
guard while the restart command is pending".

#### Runner

`TaskRunner.Restart(ctx, spec, runSpec)` posts `startCmd{restart: true}`:

| State | Effect | Reply |
|---|---|---|
| `run == runNone` | as `Start` | `StartAdmitted` |
| run U < V active, connection healthy or recovering | `want = {V, restart}`. `step()` issues `opRestart` once the connection is healthy. | `StartInPlace`: the slot carries over |
| run V, or `want.version == V` | nothing | `StartDuplicate` (this replaces `Poll`'s `Version() == task.Version` guard, #1520) |
| above V | nothing | `StartStale` |

`opRestart`: write the record at V *before* sending (so a runner restart
reattaches to V), set `version = V`, `run = runRestarting`, clear `want`, and
call `Restart(V, spec)`. Results follow the start rules. `Refused` means retry.
`attached` either way means `runActive`, plus a spec-less attach stream if none
is open. Ambiguous means `opProbe`, where `record.version >= V` counts as
restarted. `Unimplemented` comes from a driver binary older than this change (a
sandbox keeps the driver it was created with). For that the fallback is `Stop(U)`
with `want = {V}` queued without `restart`, and V then starts on `Finished(U)`
on the same sandbox, still without parking.

#### A follower mid-reattach when a restart lands

Suppose the stream following U dropped and a reattach `Run{U}` is in flight
when `serve` issues `Restart(V)`. `serve` set `version = V` before sending, so
every outcome is decided by the stream's version tag:

| Order at the driver | What the reattach gets | `serve` |
|---|---|---|
| `Run{U}` before `Restart` takes `startMu` | attached to U, then `Finished(U)` → `Restarted{U→V}` → V's messages | tag updated on `Restarted`. The stream now follows V |
| `Run{U}` while `Restart` waits for U to stop (record still U) | attached to U. `Run` sees `r.next` → same as above | same |
| `Run{U}` after V's record is written | `FailedPrecondition` (stale) | tag U < V: dropped, reopen `Run{V}` |
| `Run{U}` refused (`Restart`'s old stream still open on the driver) | `Unavailable` + `Refused` | tag U < V: dropped, reopen `Run{V}` |
| `Restart` fails ambiguously, reattach for U still open | as in the first rows | `opProbe` decides; the stream is kept if it is following V by then |

No outcome is reported as lost, and no reattach carries a spec. Today
`isTerminal` would turn the third row into "driver has no such run".

### Finish, idle and park

On `Finished(version)` with no `want`:

1. `run = runNone`. `TaskReleasedEvent` hands the slot back.
2. The idle timer is armed for `IdleTimeout`. The runner default is
   `TaskRunnerOptions.IdleTimeout`, proposed at 1m (open question 1). A new
   workspace field `idle_timeout`, as sketched in driver-server.md, overrides
   it.
3. When the timer fires and the task is still idle (`runNone`, no `want`, no
   op), `serve` runs `opPark`. Once the WAL lands it first checks
   `Status.last_seq <= Seq`, as in driver-server.md "Idle parking". When `Park`
   succeeds the connection is closed and the state is `sandboxParked`.

With `want` set (a queued start, or the `Unimplemented` restart fallback), the
next run starts straight away on the same connection and the slot carries over.
Nothing parks.

A `Start` that arrives during `opPark` sets `want`. `step()` waits for the park
to finish, then boots again, as today. Cancelling a platform park halfway is not
attempted.

The slot is now released on `Finished` and not after the park. Warm sandboxes do
not hold slots. driver-server.md makes the same call: "The semaphore slot is
released on `Finished`, not when the sandbox parks".

### Retire and destroy

- **`Destroy`** works in any state. It cancels the op and the stream, calls
  `sbx.Destroy` and clears the record. An active run, or a queued one, is
  reported lost ("sandbox removed before the run finished"), with
  `TaskReleasedEvent` if a slot was held. A failed destroy keeps everything and
  replies with the error, as today.
- **`Retire`** succeeds only when the task is idle: `runNone`, no `want`, no op.
  It then destroys the sandbox whether it is parked **or up** (a warm, idle
  sandbox of an archived task), and stops `serve`. Today an up sandbox is
  refused, because up meant running. `Prune` drops the TaskRunner from the
  registry after that.

### Restore after a runner restart

`Load` calls `TaskRunner.Restore(ctx, rec)`, which runs `opRestore` (`Open`,
then `sbx.Status`) and replies with the status, as today:

- **gone**: the record is cleared, and `Load` runs `failIfTaskRunning`
  (unchanged).
- **parked**: `sandboxParked` with the record kept, and `failIfTaskRunning`.
- **up**: `sandboxUp`. `serve` dials and readies the connection, sets
  `version = rec.Version`, `run = runActive` and `slot = slotHeld`, and opens
  the spec-less attach `Run{rec.Version}` (`after_seq: rec.Seq` once the WAL
  lands), exactly as `Run(nil)` does today. If the run finished while the
  runner was down, the attach gets `Accepted` and then `Finished` right away.
  That releases the slot and arms the idle timer. driver-server.md's `Load`
  re-derives the timer from `finished_at`, and here the timer starts from the
  reattach, which is simpler and parks at most `IdleTimeout` later.

`Load` still seeds the semaphore with the number of up sandboxes, and each
restored run hands its slot back through `TaskReleasedEvent`, so the count is
correct once every reattach has settled.

### Poll loop and the semaphore

`Poll` keeps its shape and one pass per task. Its handlers lose every
sandbox-status check:

```go
case model.TaskCommandStart:
	if task.Status == model.TaskStatusRunning {
		return nil // a follow-up for the live run: the server bumps the version at its stopped
	}
	m.start(ctx, task, tr.Start)
case model.TaskCommandRestart:
	m.start(ctx, task, tr.Restart)
case model.TaskCommandStop:
	stopped, err := tr.Stop(ctx, task.Version) // err: retried next poll
	if err == nil && !stopped {
		m.enqueue(stopped(task.ID, task.Version))
	}
```

`m.start` checks `tr.State` first and skips when `Version >= task.Version` and
a run is active or queued, so no token is minted for a duplicate. Then it calls
`TryAcquire(1)` (skip on failure, as today), builds the spec and calls `fn`.
`StartAdmitted` keeps the slot. `StartQueued`, `StartInPlace`, `StartDuplicate`
and `StartStale` release it. Errors come only from a TaskRunner that is not
serving or a done `ctx`. They release the slot and are logged. Run failures
arrive later as `TaskLostEvent`.

`HandleEvents` gains `TaskReleasedEvent`, which calls `sem.Release(1)` and
`wake.Wake()`. That moves the release out of `TaskManager.run`. `TaskManager.run`
and the `runs` WaitGroup of `Run` goroutines go away. The manager's goroutines
are now the `serve` loops, started by `Runner(ctx, taskID)` when it registers a
TaskRunner. `Wait` blocks until every held slot has been released, which is what
its tests use it for.

`Prune` is unchanged apart from the `Retire` semantics above.

### What the server sees

There are no server, schema or proto changes in `gritz.proto`. The task state
machine sees the same runner events:

| Situation | Today | Proposed |
|---|---|---|
| Start, run finishes | `started(V)`, `stopped(V)` (driver) | same |
| Follow-up start | `stopped(V)` bumps to V+1. The runner parks, the next poll resumes, then `started(V+1)` | same events. No park or resume in between |
| Restart V → V+1 | the driver sends `stopped(V)` (dropped by the version guard). The runner parks, a later poll boots, then `started(V+1)` | same events, sent by the driver within one `Restart` call |
| Stop with no live run | runner `stopped(V)` | same. A stop that overtakes a restart now also gets the runner's `stopped(V)` |
| Boot or start failure, lost run | runner `failed(V)` | same event, now sent through `TaskLostEvent` for boot failures too |

Fewer parks and resumes mean faster `started` after a restart or follow-up. No
status is added.

### WAL and forwarding (#1755, PR #1757)

The WAL design is unchanged. This is where it plugs in:

- **Position.** `rec.Seq` lives in the record that `serve` owns. Every attach
  `serve` opens is `Run{version, after_seq}`. Within a process it uses the
  in-memory durable position, which is what the reader has delivered. After a
  runner restart it uses `rec.Seq` from disk. `opStart` and `Restart` carry no
  `after_seq` of their own. The stream that follows them starts from the
  current position.
- **Per-message forwarding happens in the reader goroutine**, in order, before
  it reports the entry. A `runner_event` is passed to the outbox `Enqueue` and a
  `log_chunk` to `AppendLogChunk` (retried with backoff, as in driver-server.md
  "Position"). Only then does it send `readerMsg{seq}`. So `serve` never blocks
  on the server, and a slow `AppendLogChunk` cannot hold up `Stop` or `State`.
  Back-pressure stays on the reader and the driver's WAL.
- **`serve` advances `Seq`**: batched `rec.Seq` writes (a `taskstate` write
  failure stays fatal through `TaskFatalEvent`). After each write it fires
  `Trim(after_seq)` as a helper call whose reply only counts as a read. Trim is
  a hint, as #1757 says, so a failure is logged.
- **Stale streams and Seq.** Entries are forwarded before `serve` sees them, so
  a reader whose stream `serve` later drops as superseded may already have
  forwarded entries. That is harmless: they are in WAL order. Its `seq`s are
  still applied to the position when they are higher. The tag check only
  decides run-state transitions.
- **`Restarted` is per-stream** (`seq` 0, never logged), like `Accepted` and
  `KeepAlive`. On replay the end-of-stream rule ("only the requested version's
  `Finished` ends the stream") already does the right thing, because a reattach
  after a restart requests V. The replaced run's tail and its `Finished(U)` are
  replayed and forwarded, then V's entries follow.
- **`Accepted.reports_directly`** (version skew) is read by the reader on
  `Accepted` and decides whether it forwards, per stream, exactly as #1757
  describes.
- **Idle park** checks `last_seq` from the most recent `Status`, which the
  health ping keeps fresh.

### Proto

Additions to `proto/driver/v1/driver.proto`. Field numbers avoid those that
driver-server.md and #1757 reserve.

```protobuf
service DriverService {
  // ... Status, Run, Stop as today (Trim from #1757).

  // Restart: see above.
  rpc Restart(RestartRequest) returns (RestartResponse);
}

message RestartRequest {
  int64 version = 1;
  RunSpec spec = 2;
}

message RestartResponse {
  // True if version was already the run record's: nothing was stopped or
  // started.
  bool attached = 1;
}

message RunResponse {
  oneof event {
    // ... accepted = 2, keep_alive = 3, (runner_event = 4, log_chunk = 5),
    // finished = 6.
    // Sent after the Finished of a run that Restart replaced; the stream
    // follows the new run from here. Per-stream, never logged.
    Restarted restarted = 7;
  }
}

message Restarted {
  int64 from_version = 1;
  int64 version = 2;
}

// Refused is the error detail of a Run or Restart the driver turned away before
// doing anything: no run was started, attached to, or stopped, so the call
// may be retried unchanged.
message Refused {
  enum Reason {
    REASON_UNSPECIFIED = 0;
    // Another Run stream is open.
    REASON_STREAM_OPEN = 1;
    // Shutdown has begun; no run starts.
    REASON_SHUTTING_DOWN = 2;
  }
  Reason reason = 1;
  // The run record's version, 0 if none: no version above it was ever
  // started.
  int64 last_version = 2;
}
```

The driver attaches `Refused` with `connect.NewErrorDetail` to its two existing
`CodeUnavailable` returns in `open`, and to `Restart`'s refusals. The code stays
`Unavailable`, so an older runner behaves as before.

### API

```go
package runner // github.com/gritzapp/gritz/internal/runner

// TaskRunner is the runner's object for one task. Its state is owned by the
// goroutine running Serve; its methods send commands to that goroutine.
type TaskRunner struct{ ... }

// TaskRunnerOptions configures a TaskRunner.
type TaskRunnerOptions struct {
	// ... TaskID, Backend, Store, Events, Log, RetryInterval,
	// RetryMaxInterval, LostTimeout, ReadyTimeout as today.

	// ReadIdleTimeout is how long the connection may go without a read
	// before it is pinged; defaults to 30s. // new
	ReadIdleTimeout time.Duration
	// PingTimeout is how long a ping may take before the connection is lost;
	// defaults to 15s. // new
	PingTimeout time.Duration
	// IdleTimeout is how long an idle sandbox stays up before it is parked;
	// defaults to 1m. // new
	IdleTimeout time.Duration
}

// NewTaskRunner returns a TaskRunner with no sandbox; nothing runs until Serve.
func NewTaskRunner(opts TaskRunnerOptions) *TaskRunner

// Serve owns the TaskRunner's state until ctx is done or Retire stops it. // new
func (tr *TaskRunner) Serve(ctx context.Context) error

// Start asks for run spec.Version, booting the sandbox if needed. // new; replaces Boot and Run
func (tr *TaskRunner) Start(ctx context.Context, spec backend.Spec, run *driverv1.RunSpec) (StartResult, error)

// Restart asks for run spec.Version in place of the active run. // new
func (tr *TaskRunner) Restart(ctx context.Context, spec backend.Spec, run *driverv1.RunSpec) (StartResult, error)

// Stop stops the active run if it is at or below version, and reports whether
// run version itself was stopped. // new; replaces Kill
func (tr *TaskRunner) Stop(ctx context.Context, version int64) (bool, error)

// Restore adopts a record a previous process wrote and reports its sandbox's status.
func (tr *TaskRunner) Restore(ctx context.Context, rec taskstate.Record) (backend.Status, error)

// Destroy destroys the sandbox in any state; an active run is lost.
func (tr *TaskRunner) Destroy(ctx context.Context) error

// Retire destroys the sandbox of an idle task, up or parked, and stops Serve.
func (tr *TaskRunner) Retire(ctx context.Context) (bool, error)

// State returns a snapshot of the TaskRunner's state. // new; replaces Version and Running
func (tr *TaskRunner) State(ctx context.Context) (TaskRunnerState, error)

// StartResult is what a Start or Restart did with the caller's slot. // new
type StartResult int

const (
	// StartAdmitted: the run was admitted; the TaskRunner holds the slot.
	StartAdmitted StartResult = iota
	// StartQueued: the run follows the active one, whose slot it inherits.
	StartQueued
	// StartInPlace: the run replaces the active one, whose slot it inherits.
	StartInPlace
	// StartDuplicate: the run is already active or queued.
	StartDuplicate
	// StartStale: a newer run is active or queued.
	StartStale
)

// TaskRunnerState is a snapshot of a TaskRunner. // new
type TaskRunnerState struct {
	Sandbox SandboxState
	Run     RunState
	// Version is the run followed, or the record's version with no run.
	Version int64
	// Queued is the version of a queued run, or 0.
	Queued int64
}

// SandboxState is the exported view of the sandbox's lifecycle state. // new
type SandboxState int

// RunState is the exported view of the run's state. // new
type RunState int

// TaskReleasedEvent reports that the task gave back its concurrency slot. // new
type TaskReleasedEvent struct{ TaskID int64 }

// removed: ErrRunning, (*TaskRunner).Boot, Run, Kill, Running, Version
```

```go
package runner

// Runner returns the task's TaskRunner, registering one and starting its
// Serve on ctx if the task has none. // changed: takes ctx
func (m *TaskManager) Runner(ctx context.Context, taskID int64) *TaskRunner

// Start builds the task's specs and asks its TaskRunner for the run; run
// failures are reported as TaskLostEvent, not returned. // changed
func (m *TaskManager) Start(ctx context.Context, task *model.Task) (StartResult, error)

// Wait blocks until every held concurrency slot is released, or ctx is done. // changed
func (m *TaskManager) Wait(ctx context.Context) error
```

```go
package driverclient // removed
```

```go
package driverserver // github.com/gritzapp/gritz/internal/runner/driverserver

// Restart replaces the active run with the request's version. // new
func (s *Server) Restart(ctx context.Context, req *driverv1.RestartRequest) (*driverv1.RestartResponse, error)
```

**How it fits:** `TaskManager.Runner` constructs a `TaskRunner` and starts its
`Serve` on the runner's root context. `Poll` calls `Start`, `Restart` and `Stop`.
`Load` calls `Restore`, and `Prune` calls `Retire`. `serve` creates a
`driverConn` per boot from `sbx.Dial` and drives the driver through it. It sends
`TaskLostEvent`, `TaskReleasedEvent` and `TaskFatalEvent` to `HandleEvents`. The
backends build a plain driver client.

### Testing

All tests go through `TaskManager` and `TaskRunner` on the fake backend, with
the `testrunner` helpers (`NewDriver` for a real driver server, `NewStubDriver`
for scripted misbehaviour). There is no harness for `driverConn` or `serve`. The
fake's `Info` counters (`Boots`, `Parks`, `Dials`, `Runs`, `RunSpecs`) are the
observable surface for "never touched the sandbox" and "never resent the spec".
New fake support:

- `client.Restart` passes through like `Stop` (counted, failing when the
  sandbox is not up).
- `Info.Restarts` counts it.

Representative tests, named after their slice:

- `TestTaskManagerPoll_FollowUpWarm`: a run finishes, a start for V+1 arrives,
  and `Boots == 1`, `Parks == 0`, `len(RunSpecs) == 2`.
- `TestTaskManagerPoll_RestartInPlace`: a restart while running gives
  `Restarts == 1`, `Parks == 0`, and the stream sees `Restarted`.
- `TestTaskRunnerStart_AmbiguousNotResent`: a stub driver records the start and
  then cuts the connection before `Accepted`. Expect `Status` probed and a
  spec-less attach (`len(RunSpecs) == 1`).
- `TestTaskRunnerStart_RefusedRetried`: the stub refuses twice with `Refused`,
  then accepts.
- `TestTaskRunnerRestart_StaleReattach`: the stub answers the first reattach
  with `FailedPrecondition` after a restart. Expect no `TaskLostEvent` and a
  reattach for V.
- `TestTaskRunnerConn_PingLost`: `SetDriverReady(false)` on an up sandbox.
  After `LostTimeout` the sandbox is parked and the run lost.
  `SetDriverReady(true)` before then resumes the run with no event.
- `TestTaskRunnerConn_OneDial`: start, drop the stream twice, stop. `Dials == 1`.
- `TestTaskManagerIdlePark`: parked only after `IdleTimeout`, and the slot is
  released at `Finished`.

## Implementation Plan

Each slice is one PR, and each leaves a working runner. Slices 1–2 touch only
the driver and are inert for today's runner. Slice 3 is a pure refactor. Slices
marked **behaviour change** change what users or the server can observe.

1. **Driver: `Refused` detail** — Delivers: the `Refused` message, attached to
   `open`'s two `Unavailable` returns, with `last_version`. Depends on: nothing.
   Verifiable by: driverserver tests that read the detail from a second `Run`
   while a stream is open and after `Shutdown`.
2. **Driver: `Restart` RPC and `Restarted`** — Delivers: the proto additions,
   `startMu`, `Restart`, and `Run` continuing across `r.next`. Fake client
   pass-through and `Info.Restarts`. Depends on: (1). Verifiable by: driverserver
   tests. Idempotent on version. Stale version is `FailedPrecondition`. An open
   stream gets `Finished(U)`, `Restarted`, then V's `Finished`. A `Run{U}` during
   the stop window attaches and follows into V. A crash between stop and start
   leaves `record.version < V`.
3. **Runner: `serve` owns the TaskRunner's state** — Delivers: the goroutine,
   commands as messages, the state enums, `Serve`, and `Runner(ctx, id)`.
   `Boot`/`Run`/`Kill`/`Running`/`Version` are kept as thin wrappers with
   today's semantics (including `ErrRunning` and park-on-`Finished`), so
   `TaskManager` is untouched apart from starting `Serve`. Depends on: nothing.
   Verifiable by: the existing taskrunner and taskmanager tests, unchanged.
4. **Runner: one connection per boot, connection-level health** — Delivers:
   `driverConn`, a single `Dial`, readiness, the read-idle ping, `connLost`
   recovery, stream-stall detection, reopening without `sbx.Status`, stream IDs
   and stale-result dropping. `driverclient` is deleted, and the backends' idle
   options with it. **Behaviour change**: lost-run detection timing (a lost run
   is detected by ping and `LostTimeout` instead of per-drop status checks).
   Depends on: (3). Verifiable by: `TestTaskRunnerConn_*`, plus the existing
   `Reattach`/`DriverUnreachable` tests adjusted for timing.
5. **Runner: start retry rules** — Delivers: the start stream kept as the follow
   stream, `Refused`-only retries and `opProbe`. **Behaviour change** (narrow):
   an ambiguous start is probed instead of being resent with its spec. Depends
   on: (1), (4). Verifiable by: `TestTaskRunnerStart_AmbiguousNotResent` and
   `_RefusedRetried`.
6. **Runner: slots released on `Finished`** — Delivers: `TaskReleasedEvent`,
   with `HandleEvents` releasing the slot and waking `Poll`. `TaskManager.run`
   and its WaitGroup are removed, and `Wait` is redefined. The sandbox still
   parks on `Finished`. **Behaviour change**: the slot is free before the park
   completes. Depends on: (3). Verifiable by: a test where a slow `Park` no
   longer blocks a second task's start at `Concurrency: 1`.
7. **Runner: runs on the warm connection** — Delivers: `Start`/`StartResult`,
   the queued successor, the idle timer and `opPark`, `Retire` destroying idle
   up sandboxes, and `Restore` arming the timer after `Finished`. `Poll`'s start
   handler loses `Running()` and `ErrRunning`. It adds the workspace field
   `idle_timeout` (driver-server.md, "Idle parking"), which overrides
   `IdleTimeout`. **Behaviour change**: no park between runs within
   `IdleTimeout`. Depends on: (4), (6). Verifiable by:
   `TestTaskManagerPoll_FollowUpWarm`, `TestTaskManagerIdlePark`, and updated
   `Prune`/`Load` tests.
8. **Runner: `Stop` by task version** — Delivers: `Stop(version)` replacing
   `Kill`, and dropping a queued run. **Behaviour change**: a stop that
   overtakes a restart is reported `stopped` by the runner. Depends on: (7).
   Verifiable by: a manager test with a pending restart that is stopped before
   the runner handles it.
9. **Runner: restart in place** — Delivers: `TaskRunner.Restart`, `opRestart`,
   the `Unimplemented` fallback, and the mid-reattach rules. `Poll`'s restart
   handler loses the `Version()` guard and `Kill`. The `Boot`/`Run`/`Kill`
   wrappers from (3) and `ErrRunning` are deleted. **Behaviour change**: restart
   no longer parks or reboots. Depends on: (2), (5), (8). Verifiable by:
   `TestTaskManagerPoll_RestartInPlace`, `TestTaskRunnerRestart_StaleReattach`,
   and a fallback test against a stub driver that returns `Unimplemented`.

The WAL layers from #1757 that touch the runner (`Seq`, `Trim`, forwarding) go
on top of (4). Within that stack the position belongs to `serve` and forwarding
to the reader, as described above, so they do not depend on (5)–(9). If the WAL
lands first, (4) has to carry `after_seq` through the reopen path. Either order
works.

## Trade-offs

- **Keep `mu` and clean up in place (#1796).** #1796 already collapsed following
  into one loop. That fixes the reading of the code but not the shape. `mu`
  cannot be held across a `Park` or a `Restart` that waits for the old run
  without blocking `Stop`, and releasing it mid-operation is what made
  `running`, `cancel` and `ErrRunning` necessary. A single owner with async ops
  makes "one op in flight, commands answered immediately" the default. It costs
  a message protocol, and reply semantics must be spelled out (the
  `StartResult` table).
- **A goroutine per run instead of per sandbox.** A per-run goroutine (today's
  `Run`) cannot outlive the run. That is exactly why the sandbox parks on
  `Finished` and why restart has to wait for it. A per-task owner holding a
  per-boot `driverConn` matches the driver's own lifetime.
- **Real HTTP/2 multiplexing or a bidi stream.** A single bidirectional stream
  carrying commands and events would be closer to http2. Connect bidi needs
  HTTP/2 end to end, and the Lambda proxy does not provide that
  (driver-server.md, "Service definition"). We keep unary calls plus the server
  stream and borrow only the semantics.
- **Use http2 PING instead of `Status`.** Transport pings are not reachable
  through connect-go's client. The Lambda proxy terminates HTTP/2, so a PING
  would only reach the proxy. On Docker each request is its own exec. `Status`
  goes end to end to the driver, which is what liveness has to mean.
- **Keep `driverclient`'s per-stream idle timeout.** It duplicates the
  connection's check and gives a second, inconsistent answer to "is the driver
  there". The stream-stall rule covers the one case it caught that the ping
  does not.
- **Retry starts by version idempotency alone.** That is simpler and correct
  while the driver has its record. The probe costs one `Status` per ambiguous
  failure, and it makes a duplicate run impossible even when the record is gone.
- **Restart as `Stop` and then `Run` from the runner.** That needs no driver
  change. But it races: between the old run's `Finished` and the new `Run`, a
  reattach or the idle timer can interleave, and the driver cannot refuse
  stale work atomically. The `Unimplemented` fallback does exactly this, for old
  drivers only.
- **Park on `Finished` with idle timeout 0 as the default.** That keeps today's
  costs but brings back the park/resume on every follow-up. The server bumps the
  version only at `stopped`, so the follow-up's start arrives just after
  `Finished`.

## Open Questions

1. **`IdleTimeout` default.** Is 1m right? It has to cover the `stopped` →
   notification → poll → start round trip, which is usually seconds but up to
   `PollInterval` (30s) without a notification. Warm sandboxes hold Lambda
   compute or Docker memory outside the semaphore (driver-server.md open
   question 9). Should there be a runner-wide cap on warm sandboxes?
2. **A real single connection on Docker.** Should `docker.sandbox.Dial` keep one
   exec tunnel open and speak prior-knowledge h2c over it (the driver already
   serves `SetUnencryptedHTTP2`)? Then one `driverConn` is one exec, and
   stream stalls and lost pings become the same event. The cost is a
   long-lived exec per warm sandbox.
3. **Stream takeover.** With one owner per sandbox, a second `Run` from the
   runner always means the first stream is dead to it. Should the driver cancel
   the old stream instead of returning `Refused{STREAM_OPEN}`? That would also
   close driver-server.md open question 12 (half-open streams behind a proxy).
   This proposal keeps the refusal, because it is the one place REFUSED_STREAM
   semantics are exact.
4. **GOAWAY on driver shutdown.** Should the driver send a per-stream
   `GoingAway{last_version}` when `Shutdown` begins (an external `docker stop`,
   a platform suspend), so the connection goes straight to checking platform
   status instead of waiting for a lost ping? It is cheap, but nothing in this
   design depends on it.
5. **Restart while a stop is in flight.** The server issues `restart` only from
   `Running`/terminal states, so a restart that arrives during `runStopping`
   (the user stopped, then restarted before `Finished`) has U already stopping.
   `Restart` then waits for a run that is already ending, which is fine. Should
   the runner skip `Restart` and queue V as a plain start instead, to avoid
   holding the driver's `startMu` while the stop finishes?
6. **Token waste.** `m.start` checks `State` before minting a token, but a
   command can still lose a race between the `State` call and its own
   `Start` (a duplicate restart from two poll passes cannot happen, since passes
   are serial; a `Finished` landing in between can). Is an occasional unused
   token acceptable, as it is today?
7. **Restore of an unreported finished run on a warm sandbox.** Before the WAL,
   reattaching to a finished, unreported run sends `TaskLostEvent` on every
   runner restart until the sandbox parks. Today the park follows at once.
   Should restore park immediately when the driver reports a finished
   unreported run, instead of arming the idle timer?
8. **`Retire` of an up idle sandbox.** Destroying it is right for an archived
   task. Should `Prune` also park warm sandboxes of tasks that are only
   completed, or leave that to the idle timer alone?
