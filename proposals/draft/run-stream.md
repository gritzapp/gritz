# ReadStream: follow one run over the driver stream

Issue: https://github.com/gritzapp/gritz/issues/1796

## Problem

`TaskRunner` (`internal/runner/taskrunner.go`) does two jobs. It follows a run
over the driver.v1 `Run` stream, and it decides what to do about how that run
ended. The following part is spread over four functions:

- `startRun` dials the sandbox and polls `Status` with backoff until the
  driver answers. It then calls `accept`, which opens a `Run` stream with the
  spec, reads `Accepted` and **closes the stream**. All of this runs under a
  `ReadyTimeout` context.
- `attach` opens a new stream to follow the run. It loops over `attachOnce`
  with a reattach backoff, a `LostTimeout` counted from the last accepted
  stream, a sandbox `Status` check after each drop, and a park that it retries
  inside the loop.
- `attachOnce` reads one stream until `Finished`. It does not check message
  order, duplicates, or `Finished.version`. `accept` checks only the first
  message.
- The idle timeout lives in `driverclient`'s interceptor, which the Docker and
  Lambda backends wrap around the client `Dial` returns. The fake backend does
  not use that interceptor, so the runner's tests see `KeepAlive`s and never
  hit the idle timeout.

The stream code also parks the sandbox, at five different points, and `Run`
then turns the result into a `TaskLostEvent`. Following the run and deciding
what to do about it are interleaved in the same loops.

## Design

### Summary

A new function in `internal/runner`, `ReadStream`, follows **one run** end to
end on a `backend.Sandbox`:

- the readiness wait and the start, bounded by `ReadyTimeout`, on a stream
  that is kept;
- reattaching dropped streams with backoff, sending the version only;
- the stream protocol checks;
- the ready and `LostTimeout` timeouts, and the idle timeout in a later slice;
- the sandbox `Status` check after a drop.

`ReadStream` returns only ctx's error. Every outcome is reported through
callbacks that `TaskRunner` attaches: `OnAccepted`, `OnFinished`, `OnLost` and
`OnUnreachable`. `ReadStream` never parks the sandbox. Every park decision,
including the lost-timeout case, is made in `TaskRunner`'s callbacks.
`TaskRunner.Run` shrinks to: claim, build the callbacks, run the stream, then
send a `TaskLostEvent` if a callback recorded a lost run.

### A function in `internal/runner`: `ReadStream`

`ReadStream` is a single exported function in `internal/runner`, in its own
file (`readstream.go`), configured by a `ReadStreamOptions` struct. There is no
separate package and no stream type: a run is followed by one call, so there
is no state to keep between calls and nothing for a type to hold. It is
exported so its tests (`readstream_test.go`, `package runner_test`) are
black-box, like the rest of the runner's tests. `ReadStream` reads everything
it needs from its options and touches none of `TaskRunner`'s lock, record or
events, so the separation a package would enforce holds by construction: the
only way in is the options, and the only way out is the callbacks.

`ReadStream` reuses what `internal/runner` already has, rather than copying
it:

- `doubling`, `readyMaxInterval` and `isTerminal` stay where they are, and
  `TestDoubling` stays in `internal/runner/internal_test.go`;
- the defaults for the four timings stay the unexported `default*` constants
  in `taskrunner.go`. `ReadStream` uses them for zero `ReadStreamOptions`
  fields, as `NewTaskRunner` does for `TaskRunnerOptions`, so `TaskManager` and
  testrunner's `NewTaskRunner` hook stay unchanged.

`startRun`, `accept`, `attach` and `attachOnce` move into `readstream.go` as
methods of an unexported `streamReader`, which holds the options with their
defaults applied for the length of one call.

The reason `OnLost` is called with is a `LostReason`, with constants
`LostStartFailed`, `LostNoRun`, `LostSuperseded`, `LostSandboxParked`,
`LostSandboxGone` (and later `LostProtocol`). A bare `Reason` would read
ambiguously in package `runner`, next to `TaskLostEvent.Reason` (the string
reported to the server); the `Lost` prefix ties the type and its constants to
`OnLost`, and reads as the outcome (`LostNoRun`: lost, no run), the way
`backend.StatusUp` reads for `backend.Status`.

### Following a run

```
runner.ReadStream(ctx, opts)

 Spec != nil:                                    ┐
   Dial; Status with backoff until it answers    │ ReadyTimeout
   Run{version, spec}; Unavailable → retry       │ (ends at Accepted)
   read Accepted → OnAccepted ───────────────────┘
   └─ keep reading this stream ─┐
                                ▼
 follow loop:   KeepAlive* … Finished ── checks ──▶ OnFinished            ▣
   stream ends: NotFound                          ─▶ OnLost(NoRun)         ▣
                FailedPrecondition / Aborted      ─▶ OnLost(Superseded)    ▣
                protocol violation                ─▶ OnLost(Protocol)      ▣
                Status parked / gone              ─▶ OnLost(Parked / Gone) ▣
                LostTimeout since last Accepted   ─▶ OnUnreachable(err)
                                                       true  → ▣
                                                       false → go on
                back off; Dial; Run{version}; Accepted → OnAccepted …

 start fails before Accepted                      ─▶ OnLost(StartFailed)   ▣
 ▣ = terminal: ReadStream returns nil
```

A started run keeps its start stream. If that stream drops after `Accepted`,
the drop goes through the same follow loop as any later stream. The start is
only different up to and including `Accepted`.

### Callback contract

- Callbacks run synchronously on `ReadStream`'s goroutine, one at a time,
  with `ReadStream`'s ctx. They may block, and `OnUnreachable` does (it parks). While a
  callback runs, nothing is read from the stream.
- **Terminal callbacks** are `OnFinished`, `OnLost`, and `OnUnreachable` when
  it returns true. Exactly one of them is called, after which `ReadStream`
  returns nil. If ctx is done first, `ReadStream` returns `context.Cause(ctx)`
  and no terminal callback has been called. These are the only two ways
  `ReadStream` returns.
- `OnAccepted` is called once per accepted stream, including the start
  stream, before any other callback for that stream.
- `OnUnreachable` is the only callback that can let the run go on. It is
  called after each failed reattach once `LostTimeout` has passed since the
  last accepted stream (or since `ReadStream` began, if no stream has been
  accepted). Returning false means "keep following": `ReadStream` backs off and
  reattaches
  as usual, then calls `OnUnreachable` again after the next failure. A later
  accepted stream resets the clock, and a `Finished` still ends the run
  through `OnFinished`.
- `OnFinished`, `OnLost` and `OnUnreachable` are required, and `ReadStream`
  panics without them, as it does without a `Sandbox`. `OnAccepted` is
  optional.

### Stream protocol checks

The driver.v1 contract (`proto/driver/v1/driver.proto` and
`driverserver.Server.Run`) is: `Accepted` first and once, then any number of
`KeepAlive`s, then at most one `Finished` carrying the stream's version, and
then the stream ends. `ReadStream` reads every stream through a single
function that enforces this contract, and reports a violation as
`OnLost(LostProtocol, *ProtocolError)`:

| Message | Allowed when | Otherwise |
|---|---|---|
| `Accepted` | first message, once | protocol error |
| `Accepted{attached: false}` | on the start stream only (no spec means no start, so the driver answers `NotFound`) | protocol error |
| `KeepAlive` | after `Accepted` | protocol error |
| `Finished` | after `Accepted`, and `Finished.version == version` | protocol error |
| unset `event` oneof | never | protocol error |
| any message | not after `Finished` | logged; `OnFinished` still called |

After `Finished`, `ReadStream` calls `Receive` once more, before
`OnFinished`, and expects the stream to end. A message there is only logged.
The `Finished` has already been checked, and reporting a reported run as lost
would send the runner's fallback `failed` over an outcome the driver already
reported. A transport error at that point is ignored.

A protocol violation is terminal and is not treated as a drop. A driver that
breaks the contract once will most likely do it again, so reattaching would
only stretch a bug that shows up at once into a `LostTimeout` wait that ends
in "driver unreachable".

### API

```go
package runner // github.com/gritzapp/gritz/internal/runner

// ReadStreamOptions configures a ReadStream.
type ReadStreamOptions struct {
	// Sandbox runs the driver. Required.
	Sandbox backend.Sandbox
	// Version is the run to follow.
	Version int64
	// Spec starts the run; nil reattaches to a run already started.
	Spec *driverv1.RunSpec
	// Log is the logger; defaults to slog.Default().
	Log *slog.Logger
	// RetryInterval is the first pause between attempts to reach the driver
	// or reattach to the run; defaults to 250ms.
	RetryInterval time.Duration
	// RetryMaxInterval caps the reattach backoff; defaults to 10s.
	RetryMaxInterval time.Duration
	// LostTimeout is how long reattaching goes on with no accepted stream
	// before OnUnreachable is called; defaults to 2m.
	LostTimeout time.Duration
	// ReadyTimeout bounds starting the run, up to its Accepted; defaults to
	// 1m.
	ReadyTimeout time.Duration

	// OnAccepted is called when the driver accepts a stream; optional.
	OnAccepted func(ctx context.Context, a *driverv1.Accepted)
	// OnFinished is called with the run's Finished; terminal. Required.
	OnFinished func(ctx context.Context, f *driverv1.Finished)
	// OnLost is called when the run's Finished can never arrive; terminal.
	// Required.
	OnLost func(ctx context.Context, reason LostReason, err error)
	// OnUnreachable is called after each failed reattach once LostTimeout has
	// passed; returning true ends the run, false keeps following. Required.
	OnUnreachable func(ctx context.Context, err error) (stop bool)
}

// ReadStream follows one run on opts.Sandbox's driver over driver.v1 Run
// streams, from its start or reattach until it ends. It returns nil once
// exactly one terminal callback has returned; otherwise it returns ctx's
// cause. It panics if Sandbox, OnFinished, OnLost or OnUnreachable is nil.
func ReadStream(ctx context.Context, opts ReadStreamOptions) error

// LostReason says why ReadStreamOptions.OnLost was called.
type LostReason int

const (
	// LostStartFailed: the run could not be started; err says why.
	LostStartFailed LostReason = iota + 1
	// LostNoRun: the driver has no run with the version (NotFound).
	LostNoRun
	// LostSuperseded: the driver has moved past the version or runs another
	// one (FailedPrecondition, Aborted).
	LostSuperseded
	// LostSandboxParked: the sandbox was parked or stopping after a drop.
	LostSandboxParked
	// LostSandboxGone: the sandbox was gone after a drop.
	LostSandboxGone
	// LostProtocol: the driver broke the stream protocol; err is a
	// *ProtocolError.
	LostProtocol
)

// String returns the reason's name.
func (r LostReason) String() string

// ProtocolError is a Run stream message that breaks the driver.v1 contract.
type ProtocolError struct {
	// Msg is the offending message.
	Msg *driverv1.RunResponse
	// Rule says which rule it broke.
	Rule string
}

// Error describes the violation.
func (e *ProtocolError) Error() string

// TaskRunnerOptions is unchanged; ReadStreamOptions shares its timing
// defaults (the unexported default* constants).

// removed: TaskRunner.run, TaskRunner.startRun, accept, TaskRunner.attach and
// attachOnce, which move into readstream.go as streamReader's methods.
// doubling, readyMaxInterval and isTerminal stay, and ReadStream uses them.
```

**How it fits:** `TaskRunner.Run` captures `sbx` and `version` under `tr.mu`,
as it does today. It calls `ReadStream(runCtx, …)` with its timings, its
logger (with `"task", TaskID` attached) and four closures over a local
`lost string`. On an error it applies today's rule unchanged:
`context.Cause(runCtx)` is `errRemoved` → `lost = "sandbox removed before the
run finished"`, and anything else means shutdown, so it returns the error.
Then, as today, it logs and sends a `TaskLostEvent` on ctx if `lost != ""`. A
nil `sbx` (destroyed between `Start` and `Run`) never reaches `ReadStream`:
`Run` keeps waiting on the cancelled context. `TaskRunner.park` stays and is
called only from the callbacks.

```go
// sketch of TaskRunner.Run's callbacks
var lost string
err := ReadStream(runCtx, ReadStreamOptions{
	Sandbox: sbx, Version: version, Spec: spec, Log: log, /* timings */
	OnFinished: func(ctx context.Context, f *driverv1.Finished) {
		tr.park(ctx, sbx)
		if !f.GetReported() {
			lost = "driver did not report the run's outcome"
		}
	},
	OnLost: func(ctx context.Context, reason LostReason, err error) {
		lost = lostText(reason, err)
		if parkOnLost(reason) {
			tr.park(ctx, sbx)
		}
	},
	OnUnreachable: func(ctx context.Context, err error) bool {
		if parkErr := sbx.Park(ctx); parkErr != nil && !errors.Is(parkErr, backend.ErrGone) {
			log.Warn("failed to park sandbox of lost run", "err", parkErr, "driver", err)
			return false // keep following; ReadStream retries
		}
		lost = "driver unreachable"
		return true
	},
})
```

### Park decisions, mapped

Every outcome `attach` and `startRun` handle today, and the callback it now
goes through. The park decision is the TaskRunner column. It matches today's
behaviour row for row, so `Parks` counts and lost reasons in the existing
tests do not change.

| Outcome (today's code) | Today | Callback | TaskRunner's callback | Lost reason |
|---|---|---|---|---|
| Start fails before `Accepted`: ready timeout, `Dial` error, non-`Unavailable` `Run` error (`run`/`startRun`) | park; lost | `OnLost(LostStartFailed, err)` | park | `err.Error()` |
| `Finished{reported: true}` (`attach`) | park | `OnFinished(f)` | park | — |
| `Finished{reported: false}` (`attach`) | park; lost | `OnFinished(f)` | park | "driver did not report the run's outcome" |
| `NotFound` on reattach (`attach`) | park; lost | `OnLost(LostNoRun, err)` | park: nothing runs on it | "driver has no such run" |
| `FailedPrecondition`/`Aborted` on reattach (`attach`) | no park; lost | `OnLost(LostSuperseded, err)` | **no park**: a newer run is active | "driver has no such run" |
| Drop, `Status` = parked (`attach`) | park (let it settle); lost | `OnLost(LostSandboxParked, err)` | park | "sandbox stopped before the run finished" |
| Drop, `Status` = gone (`attach`) | no park; lost | `OnLost(LostSandboxGone, err)` | **no park** | "sandbox stopped before the run finished" |
| Drop, `Status` fails or is up, within `LostTimeout` (`attach`) | back off; reattach | none (logged in ReadStream) | — | — |
| `LostTimeout`, park succeeds or `ErrGone` (`attach`) | lost | `OnUnreachable(err)` → true | park | "driver unreachable" |
| `LostTimeout`, park fails (`attach`) | keep reattaching | `OnUnreachable(err)` → false | park attempt failed; logged | — (later callback decides) |
| `LostTimeout`, park fails, then the driver comes back with `Finished` (`attach`) | not lost | `OnUnreachable` → false, …, `OnFinished(f)` | park | per `reported` |
| ctx done (any) | return `ctx.Err()`; no park | none; `ReadStream` returns ctx's cause | — | `errRemoved` → "sandbox removed …"; shutdown → none |
| Protocol violation (new) | ignored / accepted | `OnLost(LostProtocol, err)` | park | "driver protocol error: " + `err` |

`lostText` and `parkOnLost` are small switches over `LostReason` in
`taskrunner.go`. The reason texts stay in `TaskRunner`, because they are the
`TaskLostEvent.Reason` the existing tests assert. Because `OnLost` carries the
underlying error next to the `LostReason`, `TaskRunner` can still distinguish
`Aborted` from `FailedPrecondition` with `connect.CodeOf(err)` if it ever needs
to (see [Open Questions](#open-questions)).

### Keep the start stream; what ReadyTimeout bounds

With a spec, the first stream starts the run, and `ReadStream` keeps reading
it as the first attempt of the follow loop. There is no more
accept-then-close. `ReadyTimeout` bounds the driver coming up and `Accepted`
arriving: `Dial`, the `Status` readiness poll (capped at `readyMaxInterval`),
the `Run{version, spec}` call with its `Unavailable` retries, and the wait for
the first `Accepted`.

The start stream cannot be opened on the `ReadyTimeout` context, because
cancelling that context would kill a stream that should outlive it. It is
opened on a child of `ReadStream`'s context instead. A timer started at the beginning
of the start cancels that child with `errReadyTimeout` if `Accepted` has not
arrived by the deadline, and `Accepted` stops the timer. From then on only
`LostTimeout` (and later the idle timeout) apply, and the `LostTimeout` clock
starts at that `Accepted`. A start that fails before `Accepted` goes to
`OnLost(LostStartFailed, err)`. Its error text is unchanged (`"driver server
did not become ready: …"`, `"failed to start run N: …"`), so
`TestTaskRunnerRun_NotReady` still passes.

### Idle timeout: a later slice

`ReadStream` first works with `driverclient` unchanged. Its stream reader skips
`KeepAlive`s, which it already has to do on the fake, which does not filter
them. A later slice then:

- adds `ReadStreamOptions.IdleTimeout` (default `defaultIdleTimeout`, 45s,
  moved from `driverclient`): one timer per stream, reset on every message,
  `KeepAlive` included, that cancels the stream with `errStreamIdle`. An idle stream is a
  drop like any other;
- makes the Docker and Lambda `Dial` return a plain
  `driverv1connect.NewDriverServiceClient`. Their `idleTimeout` fields go,
  and `lambdamicrovm_test`'s `te.be.idleTimeout = time.Second` is dropped;
- deletes `internal/runner/driverclient`. Its idle and `KeepAlive` cases move
  to `readstream_test.go`.

After that slice every stream timeout is in `ReadStream`, and the idle path
can be tested on the fake. Today no runner test covers it, because the fake
bypasses `driverclient`.

### Behaviour changes

All of these are deliberate. Everything else, including log texts, lost
reasons and park counts, is preserved.

1. A started run has one `Run` stream from start to finish, not two
   (`Info.Runs` is one lower). No existing test counts `Runs` across a start.
   The tests that use `WaitRuns` reattach with a nil spec.
2. The protocol checks are new. Today, a non-`Accepted` first message on
   reattach, a duplicate `Accepted`, `attached: false` on reattach, and a
   `Finished` for another version are all accepted. They now end the run as
   lost, and a message after `Finished` is logged. The real driver never sends
   any of these.
3. `ReadStream` returns `context.Cause(ctx)` instead of `ctx.Err()`. On
   shutdown the cause is `context.Canceled`, so `TestTaskRunnerRun_Shutdown`'s
   `ErrorIs(…, context.Canceled)` still holds.

### Testing

The existing runner tests (`taskrunner_test.go`, `taskmanager_*_test.go`, all
`package runner_test`) stay unchanged, and must pass at every slice. They already cover every row of the
mapping table through `TaskRunner`, including the park decisions made in its
callbacks. One test is added there for the one decision that moves from the stream
code into a callback. `TestTaskRunnerRun_DriverUnreachableParkFails` queues
`FailNext(OpPark)` on a sandbox whose driver never answers, and asserts that
the run is still reported "driver unreachable" with `Parks == 1`. This shows
the callback returned false and `ReadStream` asked again.

`ReadStream` gets black-box tests in `internal/runner/readstream_test.go`
(`package runner_test`, so they may import `testrunner`, which imports
`runner`). They build sandboxes with `testrunner.NewBackend` on the fake and
drivers with `testrunner.NewDriver`, or `testrunner.NewStubDriver` with a stub
handler written in the test file. They record callbacks into a slice, call
`ReadStream`, and assert on the recorded calls, `ReadStream`'s error and
`fake.Snapshot`. They never reach into `ReadStream`'s internals or wrap the
fake's client. Each test asserts its recorded callbacks directly, including
that exactly one terminal callback was called and that it was last.

One helper is added, in its own slice:
**`testrunner.NewStubDriver(t, h driverv1connect.DriverServiceHandler)`**. It
serves `h` over `httptest` and returns the client, like `NewDriver` but with a
handler the test writes (embedding
`driverv1connect.UnimplementedDriverServiceHandler`). The protocol and
start tests need a driver that sends what the real one never does, and only
a real server can produce a `connect.ServerStreamForClient`. The stub goes in
through `BackendOptions.Driver`, the documented way to give the fake a
driver, so the fake still decides whether that driver answers. The fake
itself needs no changes: since parking moved out of `ReadStream`, its tests
inject park failures through the callback, not the fake.

| Test | Arrange | Assert (recorded callbacks) |
|---|---|---|
| `TestReadStream_Required` | missing `Sandbox`, `OnFinished`, `OnLost`, `OnUnreachable` | panics |
| `TestReadStream_StartFinished` | `NewDriver` with no run; spec; dummy agent that finishes | `OnAccepted{attached: false}`, `OnFinished{reported: true}`; `Runs == 1`, `len(RunSpecs) == 1`; `Parks == 0` (ReadStream never parks) |
| `TestReadStream_StartReattach` | spec; `SleepForever`; `Drop` after the first `OnAccepted`, then `StopRun` | two `OnAccepted` (second `attached: true`), `OnFinished`; `Runs == 2`, `len(RunSpecs) == 1` |
| `TestReadStream_StartNotReady` | `DriverNotReady`; short `ReadyTimeout` | `OnLost(LostStartFailed)` with "did not become ready" |
| `TestReadStream_StartBecomesReady` | `DriverNotReady`; `SetDriverReady(true)` once the start has dialed | `OnAccepted`, `OnFinished` |
| `TestReadStream_StartFailed` | `FailNext(OpDial)`; stub `Run` returning `Internal` | `OnLost(LostStartFailed)` with the dial error / the code |
| `TestReadStream_StartNoAccepted` | stub whose `Run` blocks without sending; short `ReadyTimeout` | `OnLost(LostStartFailed)` |
| `TestReadStream_StartUnavailableRetried` | stub `Run` returns `Unavailable` once, then accepts and finishes | `OnAccepted`, `OnFinished`; two stub `Run` calls with a spec |
| `TestReadStream_Finished` | reattach; real driver (`reported: true`) and stub (`reported: false`) | `OnAccepted{attached: true}`, `OnFinished` with that `reported` |
| `TestReadStream_Terminal` | reattach v3 to: a driver with no run; a driver at v4; a driver running v2 | `OnLost(LostNoRun)`; `OnLost(LostSuperseded)` ×2, with `connect` codes `FailedPrecondition`/`Aborted` |
| `TestReadStream_SandboxStopped` | reattach; `Set(Parked)` / `Set(Gone)` after `OnAccepted` | `OnLost(LostSandboxParked)` / `OnLost(LostSandboxGone)`; sandbox state untouched by ReadStream |
| `TestReadStream_StatusErrorRetries` | `FailNext(OpStatus)`, then `Drop`, then `StopRun` | no `OnLost`; reattached (`Runs >= 2`); `OnFinished` |
| `TestReadStream_Unreachable` | driver never answers; short `LostTimeout`; `OnUnreachable` returns true | one `OnUnreachable`; nothing else; sandbox still `Up` |
| `TestReadStream_UnreachableKeepGoing` | as above, but `OnUnreachable` returns false until the 3rd call, which makes the driver ready (`SetDriverReady(true)`) and returns false; then `StopRun` | ≥3 `OnUnreachable`, then `OnAccepted`, `OnFinished`: following went on after a false |
| `TestReadStream_LostTimeoutReset` | `SleepForever`; `Drop` after each `OnAccepted`, for longer than `LostTimeout` in total | no `OnUnreachable`; `OnFinished` after `StopRun` |
| `TestReadStream_Protocol` | stub sends: `KeepAlive` first / `Accepted` twice / `attached: false` on reattach / `Finished` for another version / empty oneof | `OnLost(LostProtocol)` with a `*ProtocolError` (`errors.As`) |
| `TestReadStream_MessageAfterFinished` | stub sends `Accepted`, `Finished`, `KeepAlive` | `OnAccepted`, `OnFinished` |
| `TestReadStream_Cancelled` | `SleepForever`; cancel ctx with a cause after `OnAccepted` | `ReadStream` returns the cause; no terminal callback |

The idle slice adds `TestReadStream_Idle`. A stub that accepts and then goes
silent, with a short `IdleTimeout`, gets two `OnAccepted` and `Runs == 2`. A
stub that sends `KeepAlive`s faster than `IdleTimeout` is not reattached.

## Implementation Plan

Each slice keeps every existing test green and unchanged in behaviour.

1. **testrunner: stub driver** — Delivers: `testrunner.NewStubDriver`.
   Depends on: nothing. Verifiable by: a test that serves a stub through
   `NewBackend` and reads its scripted stream through a fake sandbox's `Dial`.
2. **ReadStream, today's behaviour** — Delivers: `ReadStream`,
   `ReadStreamOptions` and `LostReason` in `internal/runner/readstream.go`,
   reusing the package's timing defaults, `doubling`, `readyMaxInterval` and
   `isTerminal`. Same behaviour as today's
   `startRun`/`accept`/`attach`/`attachOnce` (still accept-then-close, no
   protocol checks, no `LostProtocol` yet), but every outcome goes through the
   callbacks and it never parks. Not used by `TaskRunner` yet. Depends on:
   (1). Verifiable by: `readstream_test.go` for every non-protocol row of the
   mapping table (`StartFinished` asserting today's `Runs == 2`).
3. **TaskRunner on ReadStream** — Delivers: `TaskRunner.Run` builds the
   callbacks of the mapping table and calls `ReadStream`. It deletes `run`,
   `startRun`, `accept`, `attach` and `attachOnce` from `taskrunner.go`;
   `doubling`, `isTerminal`, `readyMaxInterval`, the timing defaults and
   `TestDoubling` stay. Depends on: (2). Verifiable by: every existing runner
   test unchanged, plus `TestTaskRunnerRun_DriverUnreachableParkFails`.
4. **Keep the start stream** — Delivers: no accept-then-close, and the ready
   timer that ends at `Accepted`. Depends on: (2). Verifiable by:
   `StartFinished` now asserting `Runs == 1`, plus `StartReattach`. The
   existing runner tests are unchanged.
5. **Protocol checks** — Delivers: `LostProtocol`, `ProtocolError`, the
   per-stream checks and the read after `Finished`, plus `TaskRunner`'s
   mapping of `LostProtocol` (park, "driver protocol error: …"). Depends on:
   (3), and is independent of (4). Verifiable by: `TestReadStream_Protocol`,
   `TestReadStream_MessageAfterFinished`.
6. **Idle timeout into ReadStream; remove driverclient** — Delivers:
   `ReadStreamOptions.IdleTimeout` and `defaultIdleTimeout`, plain clients
   from the Docker and Lambda `Dial`, and `driverclient` deleted. Depends on:
   (3). Verifiable by: `TestReadStream_Idle`; the backend tests (which only
   call `Status` and `Run` through `Dial`) and the runner tests unchanged.
7. **Mark implemented** — Delivers: this file moved to
   `proposals/implemented/`. Depends on: (4)–(6).

## Trade-offs

- **Callbacks vs. a returned value** (`RunEnd{Finished, Lost, Park}`). A value
  is simpler to test, but it cannot express the lost-timeout case: "try to
  park; if that fails, keep following" has to run *inside* the loop. With a
  value, either `ReadStream` parks itself, or `TaskRunner` retries the park
  without reattaching, and then a driver that comes back and reports
  `completed` gets a contradicting fallback `failed`. `OnUnreachable`'s
  `stop bool` keeps today's behaviour while the decision stays in
  `TaskRunner`. The cost is a contract to state and test: synchronous,
  exactly one terminal callback, `ReadStream`'s ctx.
- **One `OnLost(reason, err)` vs. one callback per outcome** (`OnNoRun`,
  `OnSandboxStopped`, …). Per-outcome callbacks would make each park decision
  its own function, but `TaskRunner`'s choices for six outcomes reduce to two
  switches (`lostText`, `parkOnLost`), and a new `LostReason` is one constant
  rather than a new required field. `OnUnreachable` is separate because it
  alone returns a decision, and `OnFinished` because it carries the
  `Finished`.
- **A function in `internal/runner` vs. a `runstream` package with a
  `Stream` type.** A package would have the compiler keep the stream code
  free of `TaskRunner`'s lock, record and events, but it would also copy or
  move `doubling`, `isTerminal` and the timing defaults, and export defaults
  only to share them. A type would hold nothing a single call does not: a run
  is followed once, start to end. `ReadStream` takes everything through its
  options, and its tests are still black-box (`package runner_test`).
- **Protocol violations as drops** (reattach, and let `LostTimeout` decide) are
  more forgiving. But they turn a deterministic driver bug into a two-minute
  hang that ends in a misleading "driver unreachable".
- **Moving the idle timeout in the same slice as the extraction** would put
  every timeout in one place sooner. It is kept separate so the extraction
  touches no backend, and the backend change can be reviewed and reverted on
  its own.

## Open Questions

- `Aborted`/`FailedPrecondition` on the **start** stream go to
  `OnLost(LostStartFailed, err)`, and `TaskRunner` parks, as `startRun` does
  today. Should `TaskRunner` follow the reattach rule there instead, and not
  park when `connect.CodeOf(err)` shows that a newer run is active? It cannot
  happen with today's claim, so this proposal keeps the current behaviour.
