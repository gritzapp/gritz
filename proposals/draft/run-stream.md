# RunStream: follow one run over the driver stream

Issue: https://github.com/gritzapp/gritz/issues/1796

## Problem

`TaskRunner` (`internal/runner/taskrunner.go`) does two jobs. It follows a run
over the driver.v1 `Run` stream, and it acts on how that run ended. The
following part is split across four functions:

- `startRun` dials the sandbox and polls `Status` with backoff until the driver
  answers. It then calls `accept`, which opens a `Run` stream with the spec,
  reads `Accepted` and **closes the stream**. Both steps run under a
  `ReadyTimeout` context.
- `attach` then opens a new stream to follow the run. It loops over
  `attachOnce`, with a reattach backoff, the `LostTimeout` since the last
  accepted stream, a sandbox `Status` check after each drop, and a park that
  it retries inside the loop.
- `attachOnce` reads one stream until `Finished`. It does not check the order
  of messages, duplicates, or `Finished.version`. `accept` checks only the
  first message.
- The idle timeout is not in the runner at all. It lives in
  `driverclient`'s interceptor, which the Docker and Lambda backends wrap
  around the client `Dial` returns. The fake backend does not use that
  interceptor, so the runner's tests see `KeepAlive`s and never hit an idle
  timeout.

`run` and `attach` also park the sandbox. Depending on the outcome, they park
it at five different points, and `Run` then turns the result into a
`TaskLostEvent`. So the "how did it end" decision and the "what do we do about
it" decision are mixed together.

## Design

### Summary

An exported `RunStream` follows **one run** end to end. Its inputs are a
`backend.Sandbox`, a version and an optional spec. It covers:

- the readiness wait and the start, bounded by `ReadyTimeout`;
- reattaching dropped streams with backoff, sending the version only;
- the stream protocol checks;
- the ready and `LostTimeout` timeouts (the idle timeout too, as a later step:
  see [Idle timeout](#3-idle-timeout-stays-in-driverclient-for-now-moves-later));
- the sandbox `Status` check after a drop.

It returns how the run ended as a `RunEnd` value. `TaskRunner.Run` shrinks to
four steps: claim, `RunStream.Run`, park if `RunEnd.Park`, and send a
`TaskLostEvent` if `RunEnd.Lost != ""`.

### Where it lives: `internal/runner`

`RunStream` goes in `internal/runner/runstream.go`, next to `TaskRunner`, and
not in a package of its own.

- **The dependencies are already there.** `RunStream` needs `backend.Sandbox`
  (`Dial`, `Status`, and `Park` in one case, see
  [§1](#1-park-on-the-lost-timeout-runstream-keeps-it-inside-its-loop)),
  `driverv1`, `connect` and the `doubling` backoff. `internal/runner` already
  imports all of these. A new package would need `doubling` and the default
  timings (`defaultRetryInterval` ...) to move with it, and those are shared
  with `TaskRunnerOptions`.
- **It has one consumer.** Only `TaskRunner.Run` follows runs. Nothing outside
  the runner (backends, `driverserver`, the CLI) needs `RunStream`. A separate
  package would exist only to be imported by `internal/runner`.
- **Exporting it is enough for black-box tests.** Because `RunStream` is
  exported, `package runner_test` can build one directly on a
  `fake.Backend` sandbox, with the `testrunner` helpers it already uses. A
  package boundary is not needed for that.
- **No stutter.** It stays `runner.RunStream` / `runner.RunEnd`, not
  `runstream.RunStream`.

The cost is that the compiler does not stop `RunStream` from touching
`TaskRunner`'s state. The constructor keeps that boundary instead: it takes a
`backend.Sandbox` and plain values, never a `*TaskRunner`. `RunStream` has no
lock and no access to the record or the events channel. If a second consumer
ever shows up (for example a `gritz shell` that follows a run), moving the
file to `internal/runner/runstream` is a mechanical change.

`driverclient` was also considered as a home, since it is the package about
driver clients. It is the wrong layer. The backends import it to build the
client `Dial` returns, while `RunStream` sits above `Dial`. Putting
`RunStream` there would make `driverclient` import `backend`, and the
backends would then import a package that imports their own interface.

### Following a run

```
NewRunStream(sbx, version, spec, opts).Run(ctx)

 spec != nil:                                    ┐
   Dial; Status with backoff until it answers    │ ReadyTimeout
   Run{version, spec}; Unavailable → retry       │ (ends at Accepted)
   read Accepted ────────────────────────────────┘
   └─ keep reading this stream ─┐
                                ▼
 follow loop:   read KeepAlive* … Finished ── checks ──▶ RunEnd{Finished, …}
   on a drop:   NotFound / FailedPrecondition / Aborted ─▶ RunEnd{Lost, Park?}
                Status != up                             ─▶ RunEnd{Lost, Park?}
                LostTimeout since last Accepted: Park until it succeeds
                                                         ─▶ RunEnd{Lost}
                otherwise back off; Dial; Run{version}; read Accepted …
```

A started run keeps its start stream. If that stream drops after `Accepted`,
the drop goes through the same follow loop as any later stream. The start
step is only different up to and including `Accepted`.

### Stream protocol checks

The driver.v1 contract (`proto/driver/v1/driver.proto` and
`driverserver.Server.Run`) is: `Accepted` first and once, then any number of
`KeepAlive`s, then at most one `Finished` carrying the stream's version, and
then the stream ends. `RunStream` reads each stream through one function that
enforces this contract and returns a `*ProtocolError` when a message breaks
it:

| Message | Allowed when | Otherwise |
|---|---|---|
| `Accepted` | first message, once | `ProtocolError` |
| `Accepted{attached: false}` | on the start stream only (no spec means no start: the driver answers `NotFound`) | `ProtocolError` |
| `KeepAlive` | after `Accepted` | `ProtocolError` |
| `Finished` | after `Accepted`, and `Finished.version == version` | `ProtocolError` |
| any message | not after `Finished` | logged as a `ProtocolError`; the `Finished` stands |
| unset `event` oneof | never | `ProtocolError` |

After `Finished`, `RunStream` calls `Receive` once more and expects the stream
to end. A message at that point is logged and nothing else happens: the
`Finished` has already been checked, and turning a reported run into a lost
one would make the runner's fallback `failed` contradict the outcome the
driver already reported. A transport error at that point is ignored.

Any other `ProtocolError` ends the run as lost, with
`Lost: "driver protocol error: …"` and `Park: true`. The driver broke the
contract, so nothing it says next can be trusted. Reattaching would most
likely repeat the error until `LostTimeout`. Parking stops the sandbox, so it
is not left up with nothing following it, which is the same rule `attach`
applies to `NotFound`.

### API

```go
package runner // github.com/gritzapp/gritz/internal/runner

// RunStream follows one run on a sandbox's driver over driver.v1 Run streams,
// from its start or reattach until it ends. // new
type RunStream struct{ ... }

// RunStreamOptions tunes a RunStream. Zero fields take the defaults of the
// TaskRunnerOptions fields of the same name. // new
type RunStreamOptions struct {
	// Log is the logger; defaults to slog.Default().
	Log *slog.Logger
	// RetryInterval is the first pause between attempts to reach the driver.
	RetryInterval time.Duration
	// RetryMaxInterval caps the reattach backoff.
	RetryMaxInterval time.Duration
	// LostTimeout is how long reattaching goes on with no accepted stream.
	LostTimeout time.Duration
	// ReadyTimeout bounds starting a run, up to its Accepted.
	ReadyTimeout time.Duration
}

// NewRunStream returns a RunStream for run version on sbx that starts the run
// with spec, or reattaches to it if spec is nil. It panics if sbx is nil. // new
func NewRunStream(sbx backend.Sandbox, version int64, spec *driverv1.RunSpec, opts RunStreamOptions) *RunStream

// Run follows the run until it ends and reports how it ended. Its only error
// is ctx's cause. Call it once. // new
func (s *RunStream) Run(ctx context.Context) (RunEnd, error)

// RunEnd is how a followed run ended. // new
type RunEnd struct {
	// Finished reports whether the driver sent the run's Finished.
	Finished bool
	// Lost is why the driver's report of the run's outcome is missing, or ""
	// if the driver reported it.
	Lost string
	// Park reports whether the sandbox should be parked: nothing runs on it.
	Park bool
}

// ProtocolError is a Run stream message that breaks the driver.v1 contract. // new
type ProtocolError struct {
	// Msg is the offending message.
	Msg *driverv1.RunResponse
	// Reason says which rule it broke.
	Reason string
}

// Error describes the violation. // new
func (e *ProtocolError) Error() string

// TaskRunnerOptions is unchanged; TaskRunner passes its RetryInterval,
// RetryMaxInterval, LostTimeout and ReadyTimeout through to RunStreamOptions.
```

Removed (unexported): `TaskRunner.run`, `TaskRunner.startRun`, `accept`,
`TaskRunner.attach`, `attachOnce`. `isTerminal` and `doubling` stay, now used
by `RunStream`. `TaskRunner.park` stays and is now called only from `Run`.

**How it fits:** `TaskRunner.Run` captures `sbx` and `version` under `tr.mu`,
as it does today, and builds
`NewRunStream(sbx, version, spec, tr.opts.runStream())`. It calls `Run(runCtx)`
on the result. On an error it applies today's rule unchanged:
`context.Cause(runCtx)` is `errRemoved` → lost `"sandbox removed before the
run finished"`, and anything else means shutdown, so it returns the error.
Otherwise it parks if `end.Park`, logs, and sends a `TaskLostEvent` if
`end.Lost != ""`. A nil `sbx` (destroyed between `Start` and `Run`) never
reaches `RunStream`: `Run` keeps waiting on the cancelled context.
`TaskManager` and the testrunner hook that shortens `TaskRunnerOptions` timings
do not change.

### Decisions

#### 1. Park on the lost timeout: RunStream keeps it inside its loop

**Recommendation:** keep today's behaviour. When `LostTimeout` has passed
since the last accepted stream, `RunStream` calls `Sandbox.Park` itself and
returns `RunEnd{Lost: "driver unreachable"}` only once the park succeeds or
returns `backend.ErrGone`. While the park fails, it keeps reattaching with
the same backoff. If the driver comes back and its `Finished` arrives, the
run ends as `Finished` and is not lost. `Park` is false in this `RunEnd`,
because the sandbox is already parked, so `TaskRunner` does not make a second,
no-op `Park` call. That keeps `Parks` counts in the existing tests unchanged.

This is the one place where `RunStream` changes the sandbox. In every other
outcome, parking is a *consequence* of the end, decided by `TaskRunner`, done
best effort and only logged on failure. On the lost timeout, a successful
park is the *condition* for declaring the end: "give up on the driver only
once nothing can still be running." That condition is part of following the
run.

**Trade-offs:**

- *A `Park func(ctx) error` hook in `RunStreamOptions`* keeps `RunStream`
  read-only on the sandbox, but its only possible value is `sbx.Park`, which
  `RunStream` already holds. It would add an option with a nil case, and that
  nil case would need its own semantics.
- *Returning `{Lost, Park: true}` and retrying the park in `TaskRunner`* stops
  reattaching during the park retries. The driver reports to the server
  itself, so a driver that comes back and reports `completed` while
  `TaskRunner` is still retrying the park would be followed by the runner's
  fallback `failed` for the same version. Today's loop catches that case by
  reattaching and seeing `Finished{reported: true}`. `TaskRunner` would also
  need a second backoff loop of its own.

#### 2. The start stream is kept; ReadyTimeout bounds it up to Accepted

**Recommendation:** with a spec, the first stream starts the run and
`RunStream` keeps reading it as the first attempt of the follow loop. There is
no more accept-then-close.

`ReadyTimeout` still bounds the whole start, now measured up to the first
`Accepted`: `Dial`, the `Status` readiness poll (capped at
`readyMaxInterval`), the `Run{version, spec}` call including its `Unavailable`
retries, and the wait for `Accepted`. The start stream cannot be opened on the
`ReadyTimeout` context, because cancelling that context would kill a stream
that should outlive it. It is opened on a child of the run context instead. A
timer started at the beginning of the start step cancels that stream with
`errReadyTimeout` if `Accepted` has not arrived by the deadline, and is
stopped at `Accepted`. After `Accepted`, only `LostTimeout` and the idle
timeout apply, and the `LostTimeout` clock starts at that `Accepted`.

A start that fails before `Accepted` is a failed start, exactly as today:
`RunEnd{Lost: err.Error(), Park: true}`. The error text is unchanged
(`"driver server did not become ready: …"`, `"failed to start run N: …"`), so
`TestTaskRunnerRun_NotReady` keeps passing.

**Trade-off:** each started run saves one `Run` call and one detach/attach
cycle on the driver, and the gap after start where no stream is open goes
away. In exchange, `RunStream` needs the ready timer instead of a plain
`context.WithTimeout`. Keeping accept-then-close would be simpler, but it
leaves the split that #1796 asks to remove, and it is the only reason
`attach` exists separately from the start.

#### 3. Idle timeout: stays in driverclient for now, moves later

**Recommendation:** the extraction leaves `driverclient` alone. Moving the
idle timeout into `RunStream` is its own later slice (6 in the plan), not part
of the extraction.

When that slice lands, `RunStreamOptions.IdleTimeout` (default
`driverclient.DefaultIdleTimeout`, 45s) is a timer per stream. It is reset on
every message, `KeepAlive` included, and cancels the stream with
`ErrStreamIdle`. `RunStream` already has to skip `KeepAlive`s, because the
fake does not filter them. The Docker and Lambda backends' `Dial` then return
a plain `driverv1connect.NewDriverServiceClient`. Their `idleTimeout` fields
go away, `lambdamicrovm_test`'s `te.be.idleTimeout = time.Second` is dropped,
and `driverclient` is deleted along with its tests, which move to
`RunStream`'s idle tests.

**Trade-offs:** moving the timeout puts every stream timeout in one place and
makes the idle path testable on the fake. Today no runner test covers it,
because the fake bypasses `driverclient`. The `KeepAlive` handling also
becomes the same in production and in tests. Doing it in the same step,
though, would mix a pure extraction with a change to two backends and their
tests. As a separate slice it can be reviewed, and reverted, on its own.

#### 4. No callbacks: only the returned RunEnd

**Recommendation:** `RunStream` has no `OnAccepted` or other callbacks.
Nothing in `TaskRunner` acts on progress today. Its only uses of it are the
`lastGood` reset and the logs, and both move into `RunStream`. A callback with
no caller can't be tested for anything meaningful, and it fixes a concurrency
contract (which goroutine, whether the callback may block, whether it may take
`tr.mu`) before anything needs it.

**Trade-off:** #1757 (driver WAL) will need `TaskRunner` to persist
`Record.Seq` while the run is still going. That is when a hook gets added,
shaped by what it needs (for example `OnEvent(seq)`), as a small addition to
`RunStreamOptions`. Until then, the returned value is the whole interface.

#### 5. Mapping today's outcomes to RunEnd

| Outcome (today's code) | Today | `RunEnd` | `TaskRunner.Run` |
|---|---|---|---|
| Start fails before `Accepted`: ready timeout, `Dial` error, non-`Unavailable` `Run` error (`run`/`startRun`) | park; lost = `err.Error()` | `{Lost: err.Error(), Park: true}` | park, `TaskLostEvent` |
| `Finished{reported: true}` (`attach`) | park; not lost | `{Finished: true, Park: true}` | park |
| `Finished{reported: false}` (`attach`) | park; lost | `{Finished: true, Lost: "driver did not report the run's outcome", Park: true}` | park, `TaskLostEvent` |
| `NotFound` on reattach (`attach`) | park; lost | `{Lost: "driver has no such run", Park: true}` | park, `TaskLostEvent` |
| `FailedPrecondition` / `Aborted` on reattach (`attach`) | no park (a newer run is active); lost | `{Lost: "driver has no such run"}` | `TaskLostEvent` |
| Drop, `Status` = parked (`attach`) | park (let it settle); lost | `{Lost: "sandbox stopped before the run finished", Park: true}` | park, `TaskLostEvent` |
| Drop, `Status` = gone (`attach`) | lost | `{Lost: "sandbox stopped before the run finished"}` | `TaskLostEvent` |
| Drop, `Status` fails (`attach`) | error folded in; retry | not an end; retry | — |
| Drop, `Status` = up, within `LostTimeout` (`attach`) | back off; reattach | not an end; reattach | — |
| `LostTimeout`, park succeeds or `ErrGone` (`attach`) | lost | `{Lost: "driver unreachable"}` (already parked) | `TaskLostEvent` |
| `LostTimeout`, park fails (`attach`) | keep reattaching | not an end; keep reattaching and parking | — |
| `ctx` done (any) | return `ctx.Err()` | error (`ctx`'s) | `errRemoved` → lost; else return the error |
| Protocol violation (new) | ignored / accepted | `{Lost: "driver protocol error: …", Park: true}` | park, `TaskLostEvent` |

**Behaviour changes**, all deliberate:

1. A started run has one `Run` stream from start to finish, not two
   (`Info.Runs` is one lower). No existing test counts `Runs` across a start.
   The tests that use `WaitRuns` reattach with a nil spec.
2. `ReadyTimeout` now ends at the kept stream's `Accepted` instead of at
   `accept`'s close. The bound is the same.
3. The protocol checks are new. Today, a first message that is not
   `Accepted` (on reattach), a duplicate `Accepted`, `attached: false` on
   reattach, and a `Finished` for another version are all accepted. Under the
   new checks they end the run as lost, and a message after `Finished` is
   logged. The real driver never sends any of these.
4. The `ctx.Err()` returned on shutdown becomes `context.Cause(ctx)`.
   `TaskRunner` already reads the cause, and the shutdown cause is
   `context.Canceled`, so `TestTaskRunnerRun_Shutdown`'s
   `ErrorIs(…, context.Canceled)` still holds.

Log lines keep their text and fields, now under `RunStream`. `RunStream`'s
logger is `TaskRunner`'s logger with `"task", TaskID` attached.

### Testing

The existing tests (`taskrunner_test.go`, `taskmanager_*_test.go`, all
`package runner_test`) stay unchanged and must pass at every slice. Together
they already cover each row of the table through `TaskRunner`.

`RunStream` gets its own black-box tests in
`internal/runner/runstream_test.go` (`package runner_test`). They build
sandboxes on `testrunner.NewBackend`, call
`runner.NewRunStream(sbx, …).Run(ctx)` and assert on the returned `RunEnd`
and on `fake.Snapshot`. They never import `internal/runner` internals or wrap
the fake's client.

Two test helpers are needed. Each is added and tested in its own slice:

- **`testrunner.NewStubDriver(t, h driverv1connect.DriverServiceHandler)`**
  serves `h` over `httptest` and returns the client, like `NewDriver` but
  with a handler the test writes (embedding
  `driverv1connect.UnimplementedDriverServiceHandler`). The protocol tests
  need a driver that sends messages the real one never does, and
  `connect.ServerStreamForClient` is a concrete type, so only a real server
  can produce one. The stub goes in through `BackendOptions.Driver`, the
  documented way to give the fake a driver, so the fake still decides
  whether that driver answers.
- **`fake.Backend.Fail(op, id, err)`** fails every call of `op` on sandbox
  `id` until `Fail(op, id, nil)` clears it, and **`Info.Failed[op]`** counts
  the calls an injected failure failed. With these, the "park keeps failing"
  case can be set up, and a test can wait for it with
  `be.Wait(… Failed[OpPark] >= 2 …)`, without queueing an arbitrary number of
  `FailNext` errors or sleeping.

Cases, one test per outcome (table-driven where the setup matches):

| Test | Arrange | Assert |
|---|---|---|
| `TestRunStream_StartFinished` | `NewDriver` with no run; spec; dummy agent that finishes | `{Finished: true, Park: true}`; `Runs == 1`, `len(RunSpecs) == 1` (start stream kept) |
| `TestRunStream_StartReattach` | spec; `SleepForever`; `Drop` after `WaitRuns(1)`, then `StopRun` | `{Finished: true, Park: true}`; `Runs == 2`, `len(RunSpecs) == 1` (reattach sends no spec) |
| `TestRunStream_StartNotReady` | `DriverNotReady`; short `ReadyTimeout` | `Lost` contains `"did not become ready"`, `Park` |
| `TestRunStream_StartBecomesReady` | `DriverNotReady`; `SetDriverReady(true)` after `Dials >= 2` | `{Finished: true, Park: true}` |
| `TestRunStream_StartNoAccepted` | stub whose `Run` blocks without sending; short `ReadyTimeout` | `Lost` starts with `"failed to start run"`, `Park` |
| `TestRunStream_StartUnavailableRetried` | stub `Run` returns `Unavailable` once, then acts as a driver | `{Finished: true, …}`; two `Run` calls |
| `TestRunStream_Finished` | reattach; `reported` true and false (stub for false) | `Finished`, `Park`, `Lost` per table |
| `TestRunStream_Terminal` | reattach to a driver with no run / a newer run (v4 vs v3) / a running v3 when following v4 | `NotFound` → `Park`; `FailedPrecondition`, `Aborted` → no `Park`; `Lost: "driver has no such run"` |
| `TestRunStream_SandboxStopped` | reattach; `Set(Parked)` / `Set(Gone)` after `WaitRuns(1)` | `Lost: "sandbox stopped …"`; `Park` only for parked |
| `TestRunStream_StatusErrorRetries` | `FailNext(OpStatus)` then `Drop` | keeps reattaching (`Runs` grows), then finishes |
| `TestRunStream_LostTimeout` | driver never answers; short `LostTimeout` | `{Lost: "driver unreachable"}`, `Park` false, sandbox `Parked`, `Parks == 1` |
| `TestRunStream_LostTimeoutParkFails` | as above, plus `Fail(OpPark)`; wait `Failed[OpPark] >= 2`; `SetDriverReady(true)`; driver run finishes | `{Finished: true, Park: true}`: reattaching went on during park retries and saw `Finished` |
| `TestRunStream_LostTimeoutResetByAccepted` | `SleepForever`; `Drop` repeatedly, each after a new `WaitRuns`, past `LostTimeout` in total | still following (not lost) until `StopRun` |
| `TestRunStream_Protocol` | stub sends: `KeepAlive` first / `Accepted` twice / `attached: false` on reattach / `Finished` for another version / empty oneof | `Lost` has prefix `"driver protocol error"`, `Park` |
| `TestRunStream_MessageAfterFinished` | stub sends `Accepted`, `Finished`, `KeepAlive` | `{Finished: true, Park: true}` (logged only) |
| `TestRunStream_Cancelled` | `SleepForever`; cancel `ctx` with a cause | error is the cause; `Parks == 0`, sandbox `Up` |
| `TestNewRunStream` | nil sandbox | panics |

Slice 6 adds `TestRunStream_Idle`: a stub that accepts and then goes silent
with a short `IdleTimeout`. `RunStream` reattaches (`Runs == 2`). A stub that
sends `KeepAlive`s faster than `IdleTimeout` is not reattached.

## Implementation Plan

Each slice keeps every existing test green, unchanged.

1. **fake: persistent failures** — Delivers: `fake.Backend.Fail(op, id, err)`
   and `Info.Failed`, documented in the package comment. Depends on: nothing.
   Verifiable by: new cases in `fake_test.go` (fails until cleared, precedence
   over `FailNext`, `Failed` counts, `Wait` wakes on a failed call).
2. **testrunner: stub driver** — Delivers: `testrunner.NewStubDriver`.
   Depends on: nothing. Verifiable by: a test serving a stub through
   `NewBackend` and reading its scripted stream through a fake sandbox's
   `Dial`.
3. **Extract RunStream, behaviour preserved** — Delivers: `runstream.go`
   with `RunStream`, `RunStreamOptions`, `RunEnd`, `NewRunStream`, `Run`.
   `startRun`/`accept`/`attach`/`attachOnce` move into it unchanged in
   behaviour (still accept-then-close, no new checks), and `TaskRunner.Run`
   switches to claim → `RunStream.Run` → park if `Park` → `TaskLostEvent` if
   `Lost`. Depends on: (1), (2). Verifiable by: the existing runner tests
   unchanged, plus `runstream_test.go` for every row of the mapping table
   except the protocol rows (start-stream `Runs` asserted as today's 2).
4. **Keep the start stream** — Delivers: no accept-then-close, and the ready
   timer that ends at `Accepted`. Depends on: (3). Verifiable by:
   `TestRunStream_StartFinished` now asserting `Runs == 1`, plus
   `StartNoAccepted` and `StartReattach`. Existing tests unchanged.
5. **Protocol checks** — Delivers: `ProtocolError`, the per-stream checks,
   and the read-after-`Finished`. Depends on: (3) (independent of (4)).
   Verifiable by: `TestRunStream_Protocol` and
   `TestRunStream_MessageAfterFinished` on the stub driver.
6. **Move the idle timeout into RunStream** — Delivers:
   `RunStreamOptions.IdleTimeout`; the Docker and Lambda `Dial` return plain
   clients, their `idleTimeout` fields go, and `driverclient` is deleted.
   Depends on: (3). Verifiable by: `TestRunStream_Idle`; the backend tests,
   which only call `Status` and `Run` through `Dial`, still pass;
   `TestTaskManager*` unchanged.
7. **Mark the proposal implemented** — Delivers: move this file to
   `proposals/implemented/`. Depends on: (4)–(6).

## Trade-offs

- **Its own package** (`internal/runner/runstream`) would make the compiler
  enforce the boundary with `TaskRunner`, but it would move `doubling` and the
  default timings, make `TaskRunnerOptions` and `RunStreamOptions` share
  defaults across packages, and only `internal/runner` would ever import it.
  See [Where it lives](#where-it-lives-internalrunner).
- **A function instead of a type** (`FollowRun(ctx, sbx, version, spec, opts)
  (RunEnd, error)`) would carry the same information. The type is what #1796
  asks for, and it is where per-run state (`lastGood`, the backoff, which
  stream is the start stream) lives without being passed between helpers. It
  is also where the #1757 hooks will go.
- **Errors instead of `RunEnd.Lost` strings** (typed sentinel errors such as
  `ErrDriverUnreachable`) would be more structured, but the only consumer of
  the reason is the `TaskLostEvent.Reason` text, and existing tests assert
  those exact strings. A typed reason can be added to `RunEnd` if a consumer
  ever branches on it.
- **Treating protocol violations as drops** (reattach, and let `LostTimeout`
  decide) is more forgiving, but a driver that breaks the contract once will
  most likely do it again, so that would turn a bug that shows up at once
  into a two-minute hang that ends with "driver unreachable".

## Open Questions

- Should `Aborted`/`FailedPrecondition` on the **start** stream keep parking
  (today's `startRun` behaviour, kept here), or follow the reattach rule and
  not park because a newer run is active? It cannot happen with today's
  `TaskRunner` claim, so this proposal keeps the current behaviour and leaves
  the question open.
- Should `RunEnd` carry the `ProtocolError` itself (for example
  `RunEnd.Err error`) so `TaskRunner` can log it with structure? This
  proposal folds it into `Lost` and logs it inside `RunStream`.
