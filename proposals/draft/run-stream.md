# RunStream: follow one run over the driver stream

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

A new type in `internal/runner`, `RunStream`, follows **one run** end to
end over the driver.v1 `Run` stream:

- the readiness wait and the start, bounded by `ReadyTimeout`, on a stream
  that is kept;
- reattaching dropped streams with backoff, with `start` false so that no
  spec is sent;
- the stream protocol checks;
- the ready and `LostTimeout` timeouts, and the idle timeout in a later slice.

`RunStream` knows nothing about sandboxes, and neither the run's version nor
its spec. It reaches the driver only through an `Open` func that `TaskRunner`
supplies, which returns an open `Run` stream. `RunStream` calls it with
`start` true until it has received an `Accepted`, and false after that.
`TaskRunner`'s `Open` dials the sandbox, asks the driver for `Status`, and
opens `Run{version}`, attaching the spec only while `start` is true. When the
driver does not answer, `Open` checks the sandbox's status, and if the
sandbox is not up it returns an error wrapping the sentinel `ErrSandboxDown`,
which tells `RunStream` to give up. Start and reattach are one read loop:
open, read `Accepted`, read until `Finished`, reopen on a drop. The start
stream is kept.

Every outcome is reported through callbacks that `TaskRunner` attaches:
`OnAccepted`, `OnDropped`, `OnFinished`, `OnLost` and `OnUnreachable`.
`RunStream` logs nothing itself. It knows neither the task nor the version
to log with, so the caller logs in its callbacks. Each returns an
`error`, and a non-nil one ends `Run` with that error. Otherwise `Run` returns
nil after a terminal callback (`OnFinished` or `OnLost`), or ctx's cause. `RunStream` never parks the
sandbox. Every park decision, including the lost-timeout case, is made in
`TaskRunner`'s callbacks. `TaskRunner.Run` shrinks to: claim, build the `Open`
and the callbacks, run the stream, then send a `TaskLostEvent` if a callback
recorded a lost run.

### A type in `internal/runner`: `RunStream`

`RunStream` is an exported struct in `internal/runner`, in its own file
(`runstream.go`). `NewRunStream(RunStreamOptions)` builds it, and
`(*RunStream).Run(ctx)` follows the run. There is no separate package: it is
exported so its tests (`runstream_test.go`, `package runner_test`) are
black-box, like the rest of the runner's tests. `RunStream` reads everything
it needs from its options and touches none of `TaskRunner`'s lock, record or
events, and no `backend.Sandbox`, so the separation a package would enforce
holds by construction: the only way in is the options (`Open` included), and
the only way out is the callbacks.

`RunStream` reuses what `internal/runner` already has, rather than copying
it:

- `doubling`, `readyMaxInterval` and `isTerminal` stay where they are, and
  `TestDoubling` stays in `internal/runner/internal_test.go`;
- the defaults for the four timings stay the unexported `default*` constants
  in `taskrunner.go`. `NewRunStream` uses them for zero `RunStreamOptions`
  fields, as `NewTaskRunner` does for `TaskRunnerOptions`, so `TaskManager` and
  testrunner's `NewTaskRunner` hook stay unchanged.

`startRun`, `accept`, `attach` and `attachOnce` move into `runstream.go` as
unexported methods of `RunStream`, which holds the options with their defaults
applied.

The reason `OnLost` is called with is a `LostReason`, with constants
`LostStartFailed`, `LostNoRun`, `LostSuperseded`, `LostSandboxDown` (and
later `LostProtocol`). There is one `LostSandboxDown`, not a parked and a gone
reason, because `RunStream` does not know a sandbox's status. `TaskRunner`'s
`Open` wraps `backend.ErrGone` into its error when the sandbox is gone, so
`OnLost` tells the two apart with `errors.Is`. A bare `Reason` would read
ambiguously in package `runner`, next to `TaskLostEvent.Reason` (the string
reported to the server); the `Lost` prefix ties the type and its constants to
`OnLost`, and reads as the outcome (`LostNoRun`: lost, no run), the way
`backend.StatusUp` reads for `backend.Status`.

### Open, start and ErrSandboxDown

```go
Open func(ctx context.Context, start bool) (*connect.ServerStreamForClient[driverv1.RunResponse], error)
```

`RunStreamOptions.Open` returns an open `Run` stream to the driver.
`RunStream` calls it for every stream, the start's included, and reads what
it returns. Building the `RunRequest`, the driver `Status` probe and the
sandbox status check are all the caller's.

**`start`** means "no `Accepted` received yet", not "first call". With
`RunStreamOptions.Start`, `RunStream` passes `start` true on every `Open`
until a stream is accepted, including the retries of a start that was
rejected as `Unavailable`, and false after that. Without `Start` (a
reattach, e.g. after a runner restart), `start` is always false. The caller
sends the spec only when `start` is true. That matters because the driver
starts a run whenever it gets a spec for a version it has no record of
(`Server.open` in `internal/runner/driverserver/driverserver.go`). A
reattach that carried the spec would run the agent a second time on a driver
that lost its record, instead of reporting `LostNoRun`.

`Start` is the one thing `RunStream` must be told: without it, it cannot
tell a start from a reattach, and the two end differently before their
first `Accepted`. A start that fails then is `LostStartFailed` within
`ReadyTimeout`, while a reattach is `LostNoRun` on `NotFound` and
`OnUnreachable` after `LostTimeout`.

What `Open` returns:

- **a stream**: `RunStream` reads it. A stream that ends with an error is a
  drop, or, while starting, a failed start attempt.
- **an error wrapping `ErrSandboxDown`**: the run's `Finished` can never
  arrive. While starting this is `OnLost(LostStartFailed, err)` at once, with
  no wait for `ReadyTimeout`. After the first `Accepted` it is
  `OnLost(LostSandboxDown, err)`.
- **any other error**: handled like a stream that ended with it. While
  starting, an `Unavailable` one is retried with the ready backoff until
  `ReadyTimeout` and anything else fails the start. After the first
  `Accepted` it is retried with the reattach backoff, and counts towards
  `LostTimeout`.

The sandbox status check is an implementation detail of `TaskRunner`'s
`Open`, and runs only when the driver does not answer:

```go
// sketch of TaskRunner's Open
func (tr *TaskRunner) open(sbx backend.Sandbox, version int64, spec *driverv1.RunSpec) func(context.Context, bool) (*connect.ServerStreamForClient[driverv1.RunResponse], error) {
	return func(ctx context.Context, start bool) (*connect.ServerStreamForClient[driverv1.RunResponse], error) {
		client, err := sbx.Dial(ctx, "")
		if err == nil {
			if _, err = client.Status(ctx, &driverv1.StatusRequest{}); err == nil {
				req := &driverv1.RunRequest{Version: version}
				if start {
					// Only until the first Accepted: a reattach must never
					// start the run again.
					req.Spec = spec
				}
				return client.Run(ctx, req)
			}
		}
		status, stErr := sbx.Status(ctx)
		switch {
		case stErr != nil:
			return nil, fmt.Errorf("%w (status: %w)", err, stErr)
		case status == backend.StatusGone:
			return nil, fmt.Errorf("%w: %w: %w", ErrSandboxDown, backend.ErrGone, err)
		case status != backend.StatusUp:
			return nil, fmt.Errorf("%w (%v): %w", ErrSandboxDown, status, err)
		}
		return nil, err
	}
}
```

`backend.Sandbox.Dial` does no I/O, so a stopped sandbox only shows up when
the driver is asked something: the `Status` probe. The same probe is the
start's readiness wait. A driver that is not listening yet fails it with
`Unavailable` while the sandbox is up, so `Open` returns that error and
`RunStream` backs off and opens again. `RunStream` does not poll `Status`
itself.

### Following a run

```
runner.NewRunStream(opts).Run(ctx)

 Start:  Open(start=true) with backoff on        ┐
         Unavailable, until a stream is accepted │ ReadyTimeout
         read Accepted → OnAccepted ─────────────┘ (ends at Accepted)
         └─ keep reading this stream ─┐
 no Start: Open(start=false) ─────────┤
                                      ▼
 read loop:     KeepAlive* … Finished ── checks ──▶ OnFinished            ▣
   stream ends: NotFound                          ─▶ OnLost(NoRun)         ▣
                FailedPrecondition / Aborted      ─▶ OnLost(Superseded)    ▣
                protocol violation                ─▶ OnLost(Protocol)      ▣
                LostTimeout since last Accepted   ─▶ OnUnreachable(err)
                                                       error → Run returns it
                                                       nil   → go on
                back off; Open(start=false) ── ErrSandboxDown ─▶ OnLost(SandboxDown) ▣
                          └─ Accepted → OnAccepted …

 while starting: ready timeout, ErrSandboxDown,
 or an error other than Unavailable              ─▶ OnLost(StartFailed)   ▣
 ▣ = terminal (OnFinished, OnLost): Run returns the callback's error (nil if none)
 any callback returning an error ends Run with it
```

A started run keeps its start stream. If that stream drops after `Accepted`,
the drop goes through the same read loop as any later stream, and is reopened
with `start` false. The start is only different up to and including
`Accepted`.

### Callback contract

- Callbacks run synchronously on `Run`'s goroutine, one at a time, with
  `Run`'s ctx. They may block, and `OnUnreachable` does (it parks). While a
  callback runs, nothing is read from the stream.
- Every callback returns an `error`. A non-nil one ends `Run` at once, and
  `Run` returns it as is: `RunStream` stops following. `RunStream` exports no
  sentinel for this; a caller that needs to tell its own errors apart
  defines them.
- **Terminal callbacks** are `OnFinished` and `OnLost`. At most one of them
  is called, after which `Run` returns its error (nil if it returned nil).
  `Run` returns nil only after one of them. If ctx is done first, `Run`
  returns `context.Cause(ctx)` and no terminal callback has been called. If a
  non-terminal callback (`OnAccepted`, `OnDropped`, `OnUnreachable`) returns
  an error, `Run` returns it and no terminal callback has been called. These
  are the only ways `Run` returns.
- `OnAccepted` is called once per accepted stream, including the start
  stream, before any other callback for that stream.
- `OnDropped` is called with the error each time `RunStream` is about to
  retry a failed stream, before the backoff sleep: a dropped stream, a
  failed `Open`, or a start attempt rejected as `Unavailable`. It is not
  called for a failure that ends the run (that goes to `OnLost`), and it is
  called after `OnUnreachable` returns nil. `RunStream` does not
  deduplicate: the same error arrives once per retry, and the caller decides
  what to log.
- `OnUnreachable` decides whether to give up on a driver that cannot be
  reached. It is called after each failed reattach once `LostTimeout` has
  passed since the last accepted stream (or since `Run` began, if no stream
  has been accepted). Returning nil means "keep following": `RunStream` backs
  off and reattaches as usual, then calls `OnUnreachable` again after the
  next failure. A later accepted stream resets the clock, and a `Finished`
  still ends the run through `OnFinished`. Returning an error gives up: `Run`
  returns that error.
- `OnFinished`, `OnLost` and `OnUnreachable` are required, and `NewRunStream`
  panics without them, as it does without an `Open`. `OnAccepted` and
  `OnDropped` are optional, and default to no-ops.

### Stream protocol checks

The driver.v1 contract (`proto/driver/v1/driver.proto` and
`driverserver.Server.Run`) is: `Accepted` first and once, then any number of
`KeepAlive`s, then at most one `Finished` carrying the stream's version, and
then the stream ends. `RunStream` reads every stream through a single
function that enforces this contract, and reports a violation as
`OnLost(LostProtocol, *ProtocolError)`:

| Message | Allowed when | Otherwise |
|---|---|---|
| `Accepted` | first message, once | protocol error |
| `Accepted{attached: false}` | on the start stream only (no spec means no start, so the driver answers `NotFound`) | protocol error |
| `KeepAlive` | after `Accepted` | protocol error |
| `Finished` | after `Accepted`, and `Finished.version == version` | protocol error |
| unset `event` oneof | never | protocol error |
| any message | not after `Finished` | ignored; `OnFinished` still called |

After `Finished`, `RunStream` calls `Receive` once more, before
`OnFinished`, and expects the stream to end. A message there is ignored,
since `RunStream` does not log.
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

// ErrSandboxDown is returned, wrapped, by a RunStreamOptions.Open that could
// not reach the driver because its sandbox is not up. It tells the RunStream
// to give up: the run's Finished can never arrive.
var ErrSandboxDown = errors.New("sandbox is not up")

// RunStreamOptions configures a RunStream.
type RunStreamOptions struct {
	// Open opens a Run stream to the driver. start is true until the
	// RunStream has received an Accepted, and only then may the request
	// carry the spec: a reattach that carried it would start the run again
	// on a driver that has lost its record of it. An error wrapping
	// ErrSandboxDown ends the run. Required.
	Open func(ctx context.Context, start bool) (*connect.ServerStreamForClient[driverv1.RunResponse], error)
	// Start makes Run start the run: Open is called with start true until
	// the first Accepted. Otherwise Run reattaches to a run already started,
	// and start is always false.
	Start bool
	// RetryInterval is the first pause between attempts to reach the driver
	// or reattach to the run; defaults to 250ms.
	RetryInterval time.Duration
	// RetryMaxInterval caps the reattach backoff; defaults to 10s.
	RetryMaxInterval time.Duration
	// LostTimeout is how long reattaching goes on with no accepted stream
	// before OnUnreachable is called; defaults to 2m.
	LostTimeout time.Duration
	// ReadyTimeout bounds starting the run, up to its first Accepted;
	// defaults to 1m.
	ReadyTimeout time.Duration

	// Every callback returns an error; a non-nil one ends Run with it.

	// OnAccepted is called when the driver accepts a stream; optional.
	OnAccepted func(ctx context.Context, a *driverv1.Accepted) error
	// OnDropped is called with why a stream failed each time Run is about to
	// retry it, before the backoff; optional.
	OnDropped func(ctx context.Context, err error) error
	// OnFinished is called with the run's Finished; terminal. Required.
	OnFinished func(ctx context.Context, f *driverv1.Finished) error
	// OnLost is called when the run's Finished can never arrive; terminal.
	// Required.
	OnLost func(ctx context.Context, reason LostReason, err error) error
	// OnUnreachable is called after each failed reattach once LostTimeout has
	// passed. Returning nil keeps following; an error ends Run with it, as
	// for every callback. Required.
	OnUnreachable func(ctx context.Context, err error) error
}

// RunStream follows one run on a driver over driver.v1 Run streams, from its
// start or reattach until it ends. It reaches the driver only through its
// options' Open, and reports every outcome through their callbacks. It knows
// neither the run's version nor its spec.
type RunStream struct{ ... }

// NewRunStream returns a RunStream for opts. It panics if Open, OnFinished,
// OnLost or OnUnreachable is nil.
func NewRunStream(opts RunStreamOptions) *RunStream

// Run follows the run. A callback that returns an error ends Run at once,
// and Run returns that error. Otherwise Run returns nil once exactly one
// terminal callback (OnFinished or OnLost) has returned, or ctx's cause.
// Call it once.
func (r *RunStream) Run(ctx context.Context) error

// LostReason says why RunStreamOptions.OnLost was called.
type LostReason int

const (
	// LostStartFailed: the run could not be started, up to its first
	// Accepted; err says why.
	LostStartFailed LostReason = iota + 1
	// LostNoRun: the driver has no run with the version (NotFound).
	LostNoRun
	// LostSuperseded: the driver has moved past the version or runs another
	// one (FailedPrecondition, Aborted).
	LostSuperseded
	// LostSandboxDown: Open returned ErrSandboxDown after the first
	// Accepted; err is Open's error.
	LostSandboxDown
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

// TaskRunnerOptions is unchanged; RunStreamOptions shares its timing
// defaults (the unexported default* constants).

// removed: TaskRunner.run, TaskRunner.startRun, accept, TaskRunner.attach and
// attachOnce, which move into runstream.go as RunStream's methods.
// doubling, readyMaxInterval and isTerminal stay, and RunStream uses them.
```

**How it fits:** `TaskRunner.Run` captures `sbx` and `version` under `tr.mu`,
as it does today. It builds `NewRunStream` with `tr.open(sbx, version, spec)` and
`Start: spec != nil`, its timings, and five closures over a local
`lost string` that log with its logger (with `"task", TaskID` and
`"version"` attached), and calls `Run(runCtx)`. The only callback that
returns an error is `OnUnreachable`, which gives up with the unexported
sentinel `errDriverUnreachable` once it has parked the sandbox. `TaskRunner`
picks that out of `Run`'s result with `errors.Is` and sets `lost = "driver
unreachable"`, as today. On any other error it applies today's rule
unchanged: `context.Cause(runCtx)` is `errRemoved` → `lost = "sandbox
removed before the run finished"`, and anything else means shutdown, so it
returns the error.
Then, as today, it logs and sends a `TaskLostEvent` on ctx if `lost != ""`. A
nil `sbx` (destroyed between `Start` and `Run`) never reaches `RunStream`:
`Run` keeps waiting on the cancelled context. `TaskRunner.park` stays and is
called only from the callbacks.

```go
// sketch of TaskRunner.Run's callbacks
var lost, lastDrop string
log := tr.opts.Log.With("task", tr.opts.TaskID, "version", version)
s := NewRunStream(RunStreamOptions{
	Open: tr.open(sbx, version, spec), Start: spec != nil, /* timings */
	OnAccepted: func(context.Context, *driverv1.Accepted) error {
		lastDrop = "" // log the next drop even if it repeats the last
		return nil
	},
	OnDropped: func(_ context.Context, err error) error {
		if msg := err.Error(); msg != lastDrop {
			log.Warn("run stream dropped, reattaching", "err", err)
			lastDrop = msg
		}
		return nil
	},
	OnFinished: func(ctx context.Context, f *driverv1.Finished) error {
		tr.park(ctx, sbx)
		if !f.GetReported() {
			lost = "driver did not report the run's outcome"
		}
		return nil
	},
	OnLost: func(ctx context.Context, reason LostReason, err error) error {
		// lostLog is today's message for the reason: "failed to start run",
		// "driver has no such run", "sandbox stopped before its run finished".
		log.Error(lostLog(reason), "err", err)
		lost = lostText(reason, err)
		if parkOnLost(reason, err) {
			tr.park(ctx, sbx)
		}
		return nil
	},
	OnUnreachable: func(ctx context.Context, err error) error {
		if parkErr := sbx.Park(ctx); parkErr != nil && !errors.Is(parkErr, backend.ErrGone) {
			log.Warn("failed to park sandbox of lost run", "err", parkErr, "driver", err)
			return nil // keep following; RunStream retries
		}
		log.Error("run lost: driver unreachable, sandbox parked", "err", err)
		return errDriverUnreachable
	},
})
switch err := s.Run(runCtx); {
case errors.Is(err, errDriverUnreachable):
	lost = "driver unreachable"
case err != nil && errors.Is(context.Cause(runCtx), errRemoved):
	lost = "sandbox removed before the run finished"
case err != nil:
	return err // shutdown
}
```

`errDriverUnreachable` is unexported in `taskrunner.go`
(`errors.New("driver unreachable")`).

### Park decisions, mapped

Every outcome `attach` and `startRun` handle today, and the callback it now
goes through. The park decision is the TaskRunner column. It matches today's
behaviour row for row, so `Parks` counts and lost reasons in the existing
tests do not change.

| Outcome (today's code) | Today | Callback | TaskRunner's callback | Lost reason |
|---|---|---|---|---|
| Start fails before `Accepted`: ready timeout, `Open` returning `ErrSandboxDown`, an error other than `Unavailable` (`run`/`startRun`) | park; lost | `OnLost(LostStartFailed, err)` | park | `err.Error()` |
| `Finished{reported: true}` (`attach`) | park | `OnFinished(f)` | park | — |
| `Finished{reported: false}` (`attach`) | park; lost | `OnFinished(f)` | park | "driver did not report the run's outcome" |
| `NotFound` on reattach (`attach`) | park; lost | `OnLost(LostNoRun, err)` | park: nothing runs on it | "driver has no such run" |
| `FailedPrecondition`/`Aborted` on reattach (`attach`) | no park; lost | `OnLost(LostSuperseded, err)` | **no park**: a newer run is active | "driver has no such run" |
| Drop, `Status` = parked (`attach`) | park (let it settle); lost | `Open` → `ErrSandboxDown`; `OnLost(LostSandboxDown, err)` | park | "sandbox stopped before the run finished" |
| Drop, `Status` = gone (`attach`) | no park; lost | `Open` → `ErrSandboxDown` + `backend.ErrGone`; `OnLost(LostSandboxDown, err)` | **no park** (`errors.Is(err, backend.ErrGone)`) | "sandbox stopped before the run finished" |
| Drop, `Status` fails or is up, within `LostTimeout` (`attach`) | back off; reattach | `Open` returns a plain error, or a stream; `OnDropped(err)` | logs the warning (deduplicated) | — |
| `LostTimeout`, park succeeds or `ErrGone` (`attach`) | lost | `OnUnreachable(err)` → `errDriverUnreachable`; `Run` returns it | park | "driver unreachable" (picked out with `errors.Is`) |
| `LostTimeout`, park fails (`attach`) | keep reattaching | `OnUnreachable(err)` → nil | park attempt failed; logged | — (later callback decides) |
| `LostTimeout`, park fails, then the driver comes back with `Finished` (`attach`) | not lost | `OnUnreachable` → nil, …, `OnFinished(f)` | park | per `reported` |
| ctx done (any) | return `ctx.Err()`; no park | none; `Run` returns ctx's cause | — | `errRemoved` → "sandbox removed …"; shutdown → none |
| Protocol violation (new) | ignored / accepted | `OnLost(LostProtocol, err)` | park | "driver protocol error: " + `err` |

`lostText`, `lostLog` and `parkOnLost` are small switches over `LostReason`
in `taskrunner.go`; `parkOnLost` also checks `errors.Is(err, backend.ErrGone)`
for `LostSandboxDown`. The reason texts stay in `TaskRunner`, because they are the
`TaskLostEvent.Reason` the existing tests assert. Because `OnLost` carries the
underlying error next to the `LostReason`, `TaskRunner` can still distinguish
`Aborted` from `FailedPrecondition` with `connect.CodeOf(err)` if it ever needs
to (see [Open Questions](#open-questions)).

### Keep the start stream; what ReadyTimeout bounds

With `Start`, the first accepted stream starts the run, and `RunStream`
keeps reading it as the first stream of the read loop. There is no more
accept-then-close. `ReadyTimeout` bounds the driver coming up and `Accepted`
arriving: the `Open` retries on `Unavailable` (backing off up to
`readyMaxInterval`), whether from the `Status` probe or from a driver with
another stream open, and the wait for the first `Accepted`.

The start streams cannot be opened on a `context.WithTimeout` context,
because cancelling it would kill a stream that should outlive the start. They
are opened on a child of `Run`'s context instead. A timer started at the
beginning of `Run` cancels that child with `errReadyTimeout` if `Accepted`
has not arrived by the deadline, and `Accepted` stops the timer. If the timer
fires just as `Accepted` arrives, the accepted stream is cancelled and goes
through the read loop as a drop. From then on only `LostTimeout` (and later
the idle timeout) apply, and the `LostTimeout` clock starts at that
`Accepted`. A start that fails before `Accepted` goes to
`OnLost(LostStartFailed, err)`. A ready timeout says `"driver server did not
become ready: …"`, so `TestTaskRunnerRun_NotReady` still passes. Any other
failure says `"failed to start run: …"`, without the version, which
`RunStream` does not know; `TaskRunner` logs it in `OnLost` with its logger,
which carries it.

### Idle timeout: a later slice

`RunStream` first works with `driverclient` unchanged. Its stream reader skips
`KeepAlive`s, which it already has to do on the fake, which does not filter
them. A later slice then:

- adds `RunStreamOptions.IdleTimeout` (default `defaultIdleTimeout`, 45s,
  moved from `driverclient`): one timer per stream, reset on every message,
  `KeepAlive` included, that cancels the stream with `errStreamIdle`. An idle stream is a
  drop like any other;
- makes the Docker and Lambda `Dial` return a plain
  `driverv1connect.NewDriverServiceClient`. Their `idleTimeout` fields go,
  and `lambdamicrovm_test`'s `te.be.idleTimeout = time.Second` is dropped;
- deletes `internal/runner/driverclient`. Its idle and `KeepAlive` cases move
  to `runstream_test.go`.

After that slice every stream timeout is in `RunStream`, and the idle path
can be tested on the fake. Today no runner test covers it, because the fake
bypasses `driverclient`.

### Behaviour changes

All of these are deliberate. Everything else, including log texts (now
written by `TaskRunner`'s callbacks), lost reasons and park counts, is
preserved.

1. A started run has one `Run` stream from start to finish, not two
   (`Info.Runs` is one lower). No existing test counts `Runs` across a start.
   The tests that use `WaitRuns` reattach with a nil spec.
2. The protocol checks are new. Today, a non-`Accepted` first message on
   reattach, a duplicate `Accepted`, `attached: false` on reattach, and a
   `Finished` for another version are all accepted. They now end the run as
   lost, and a message after `Finished` is logged. The real driver never sends
   any of these.
3. `Run` returns `context.Cause(ctx)` instead of `ctx.Err()`. On
   shutdown the cause is `context.Canceled`, so `TestTaskRunnerRun_Shutdown`'s
   `ErrorIs(…, context.Canceled)` still holds.
4. The sandbox status check moves from right after a drop to the next
   `Open`, after the reattach backoff, and runs only when the driver does not
   answer `Status`. A run on a stopped sandbox is reported lost one backoff
   later than today.
5. On the start, a sandbox that is not up fails the start at once
   (`ErrSandboxDown`) instead of after `ReadyTimeout`. A start rejected as
   `Unavailable` because another stream is open is retried with the ready
   backoff instead of every `RetryInterval`, and a `Status` probe failing
   with a code other than `Unavailable` fails the start instead of being
   retried until `ReadyTimeout`.
6. A start that fails for a reason other than the ready timeout reports
   `"failed to start run: …"` instead of `"failed to start run N: …"`. No test
   asserts the version in it.

### Testing

The existing runner tests (`taskrunner_test.go`, `taskmanager_*_test.go`, all
`package runner_test`) stay unchanged, and must pass at every slice. They already cover every row of the
mapping table through `TaskRunner`, including the park decisions made in its
callbacks. One test is added there for the one decision that moves from the stream
code into a callback. `TestTaskRunnerRun_DriverUnreachableParkFails` queues
`FailNext(OpPark)` on a sandbox whose driver never answers, and asserts that
the run is still reported "driver unreachable" with `Parks == 1`. This shows
the callback returned nil and `RunStream` asked again.

`RunStream` gets black-box tests in `internal/runner/runstream_test.go`
(`package runner_test`, so they may import `testrunner`, which imports
`runner`). They build sandboxes with `testrunner.NewBackend` on the fake and
drivers with `testrunner.NewDriver`, or `testrunner.NewStubDriver` with a stub
handler written in the test file. Their `Open` is a test helper,
`openSandbox(sbx, version, spec)`, written like `TaskRunner`'s (see [Open,
start and ErrSandboxDown](#open-start-and-errsandboxdown)). It is wrapped to
record each call's `start`, or where a test needs an `Open` to fail. They record callbacks into a slice (`OnDropped`'s errors into a
separate one, as the number of drops depends on timing), call `Run`, and
assert on the recorded calls, `Run`'s error and `fake.Snapshot`. They never reach into `RunStream`'s internals or wrap the
fake's client. Each test asserts its recorded callbacks directly. A test whose
`Run` returns nil also asserts that exactly one terminal callback was called
and that it was last; one whose `Run` returns an error asserts that error.

One helper is added, in its own slice:
**`testrunner.NewStubDriver(t, h driverv1connect.DriverServiceHandler)`**. It
serves `h` over `httptest` and returns the client, like `NewDriver` but with a
handler the test writes (embedding
`driverv1connect.UnimplementedDriverServiceHandler`). The protocol and
start tests need a driver that sends what the real one never does, and only
a real server can produce a `connect.ServerStreamForClient`. The stub goes in
through `BackendOptions.Driver`, the documented way to give the fake a
driver, so the fake still decides whether that driver answers. The fake
itself needs no changes: since parking moved out of `RunStream`, its tests
inject park failures through the callback, not the fake.

| Test | Arrange | Assert (recorded callbacks) |
|---|---|---|
| `TestNewRunStream` | missing `Open`, `OnFinished`, `OnLost`, `OnUnreachable` | panics |
| `TestRunStream_StartFinished` | `NewDriver` with no run; spec; dummy agent that finishes | `OnAccepted{attached: false}`, `OnFinished{reported: true}`; `Runs == 1`, `len(RunSpecs) == 1`; `Parks == 0` (RunStream never parks) |
| `TestRunStream_StartReattach` | spec; `SleepForever`; `Drop` after the first `OnAccepted`, then `StopRun` | two `OnAccepted` (second `attached: true`), `OnFinished`; `Open`'s `start` is `[true, false]`; `Runs == 2`, `len(RunSpecs) == 1` |
| `TestRunStream_StartNotReady` | `DriverNotReady`; short `ReadyTimeout` | `OnLost(LostStartFailed)` with "did not become ready" |
| `TestRunStream_StartBecomesReady` | `DriverNotReady`; `SetDriverReady(true)` once the start has dialed | `OnAccepted`, `OnFinished` |
| `TestRunStream_StartFailed` | parked sandbox; `FailNext(OpDial)`; stub `Run` returning `Internal` | `OnLost(LostStartFailed)` with `ErrSandboxDown` (`errors.Is`) / the dial error / the code, without waiting for `ReadyTimeout` |
| `TestRunStream_StartNoAccepted` | stub whose `Run` blocks without sending; short `ReadyTimeout` | `OnLost(LostStartFailed)` with "did not become ready": the ready timer cut the stream |
| `TestRunStream_StartUnavailableRetried` | stub `Run` returns `Unavailable` once, then accepts and ends the stream; the reattach finishes | two `OnAccepted`, `OnFinished`; `Open`'s `start` is `[true, true, false]`; two `OnDropped` (the `Unavailable`, then the start stream ending); two stub `Run` calls with a spec |
| `TestRunStream_Finished` | reattach; real driver (`reported: true`) and stub (`reported: false`) | `OnAccepted{attached: true}`, `OnFinished` with that `reported` |
| `TestRunStream_Terminal` | reattach v3 to: a driver with no run; a driver at v4; a driver running v2 | `OnLost(LostNoRun)`; `OnLost(LostSuperseded)` ×2, with `connect` codes `FailedPrecondition`/`Aborted` |
| `TestRunStream_SandboxStopped` | reattach; `Set(Parked)` / `Set(Gone)` after `OnAccepted` | `OnLost(LostSandboxDown)` with `ErrSandboxDown`, and `backend.ErrGone` only when gone; sandbox state untouched by RunStream |
| `TestRunStream_OpenErrorRetries` | `Open` fails once with a plain error after a `Drop`, then `StopRun` | no `OnLost`; reattached; `OnFinished`; two `OnDropped`, the second with the `Open` error |
| `TestRunStream_Unreachable` | driver never answers; short `LostTimeout`; `OnUnreachable` returns an error | `Run` returns that error as is; one `OnUnreachable`, no terminal callback; sandbox still `Up` |
| `TestRunStream_UnreachableKeepGoing` | as above, but `OnUnreachable` returns nil, and its 3rd call makes the driver ready (`SetDriverReady(true)`); then `StopRun` | ≥3 `OnUnreachable`, then `OnAccepted`, `OnFinished`: following went on after each nil |
| `TestRunStream_LostTimeoutReset` | `SleepForever`; `Drop` after each `OnAccepted`, for longer than `LostTimeout` in total | no `OnUnreachable`; `OnFinished` after `StopRun` |
| `TestRunStream_Protocol` | stub sends: `KeepAlive` first / `Accepted` twice / `attached: false` on reattach / `Finished` for another version / empty oneof | `OnLost(LostProtocol)` with a `*ProtocolError` (`errors.As`) |
| `TestRunStream_MessageAfterFinished` | stub sends `Accepted`, `Finished`, `KeepAlive` | `OnAccepted`, `OnFinished` |
| `TestRunStream_Cancelled` | `SleepForever`; cancel ctx with a cause after `OnAccepted` | `Run` returns the cause; no terminal callback |
| `TestRunStream_CallbackError` | `OnAccepted` (reattach and start), `OnDropped`, `OnFinished`, `OnLost`, `OnUnreachable` each return an error | `Run` returns that error; no callback after it |

The idle slice adds `TestRunStream_Idle`. A stub that accepts and then goes
silent, with a short `IdleTimeout`, gets two `OnAccepted` and `Runs == 2`. A
stub that sends `KeepAlive`s faster than `IdleTimeout` is not reattached.

## Implementation Plan

Each slice keeps every existing test green and unchanged in behaviour.

1. **testrunner: stub driver** — Delivers: `testrunner.NewStubDriver`.
   Depends on: nothing. Verifiable by: a test that serves a stub through
   `NewBackend` and reads its scripted stream through a fake sandbox's `Dial`.
2. **RunStream** — Delivers: `RunStream`, `NewRunStream`, `Run`,
   `RunStreamOptions`, `LostReason` and `ErrSandboxDown` in
   `internal/runner/runstream.go`, reusing the package's timing defaults,
   `doubling`, `readyMaxInterval` and `isTerminal`. It reaches the driver only
   through `Open`, knows neither version nor spec, and its callbacks return
   errors. Start and reattach are one read loop that keeps the start stream,
   with the ready timer ending at `Accepted`. Otherwise today's behaviour,
   apart from behaviour changes 1 and 4–6 (no protocol checks, no
   `LostProtocol` yet). Every outcome goes through the callbacks, and it
   never parks. Not used by `TaskRunner` yet. Depends on: (1). Verifiable
   by: `runstream_test.go` for every non-protocol row of the mapping table
   (`StartFinished` asserting `Runs == 1`, plus `StartReattach`), with
   `openSandbox` as the `Open`.
3. **TaskRunner on RunStream** — Delivers: `TaskRunner.open`, and
   `TaskRunner.Run` builds the callbacks of the mapping table and runs a
   `RunStream`. It deletes `run`,
   `startRun`, `accept`, `attach` and `attachOnce` from `taskrunner.go`;
   `doubling`, `isTerminal`, `readyMaxInterval`, the timing defaults and
   `TestDoubling` stay. Depends on: (2). Verifiable by: every existing runner
   test unchanged, plus `TestTaskRunnerRun_DriverUnreachableParkFails`.
4. **Protocol checks** — Delivers: `LostProtocol`, `ProtocolError`, the
   per-stream checks and the read after `Finished`, plus `TaskRunner`'s
   mapping of `LostProtocol` (park, "driver protocol error: …"). Depends on:
   (3). Verifiable by: `TestRunStream_Protocol`,
   `TestRunStream_MessageAfterFinished`.
5. **Idle timeout into RunStream; remove driverclient** — Delivers:
   `RunStreamOptions.IdleTimeout` and `defaultIdleTimeout`, plain clients
   from the Docker and Lambda `Dial`, and `driverclient` deleted. Depends on:
   (3). Verifiable by: `TestRunStream_Idle`; the backend tests (which only
   call `Status` and `Run` through `Dial`) and the runner tests unchanged.
6. **Mark implemented** — Delivers: this file moved to
   `proposals/implemented/`. Depends on: (4) and (5).

## Trade-offs

- **Callbacks vs. a returned value** (`RunEnd{Finished, Lost, Park}`). A value
  is simpler to test, but it cannot express the lost-timeout case: "try to
  park; if that fails, keep following" has to run *inside* the loop. With a
  value, either `RunStream` parks itself, or `TaskRunner` retries the park
  without reattaching, and then a driver that comes back and reports
  `completed` gets a contradicting fallback `failed`. `OnUnreachable`
  returning nil (keep following) or an error (give up, returned by `Run`)
  keeps today's behaviour while the decision stays in `TaskRunner`. The cost
  is a contract to state and test: synchronous, at most one terminal
  callback, errors returned as is, `Run`'s ctx.
- **One `OnLost(reason, err)` vs. one callback per outcome** (`OnNoRun`,
  `OnSandboxStopped`, …). Per-outcome callbacks would make each park decision
  its own function, but `TaskRunner`'s choices for six outcomes reduce to two
  switches (`lostText`, `parkOnLost`), and a new `LostReason` is one constant
  rather than a new required field. `OnUnreachable` is separate because it
  alone can let the run go on, and `OnFinished` because it carries the
  `Finished`.
- **A type in `internal/runner` vs. a `runstream` package.** A package
  would have the compiler keep the stream code free of `TaskRunner`'s lock,
  record and events, but it would also copy or move `doubling`, `isTerminal`
  and the timing defaults, and export defaults only to share them.
  `RunStream` takes everything through its options, and its tests are still
  black-box (`package runner_test`). In package `runner` the type can be
  called `RunStream` without stutter.
- **A struct vs. a single function** (`ReadStream(ctx, opts)`). The stream
  code needs its options in every step, so it lives in a struct either way;
  exporting that struct instead of wrapping it in a function keeps one name
  for the concept and the code.
- **`Open` returning a stream vs. `Dial` returning a client, or the sandbox.**
  With a client, `RunStream` would build the `RunRequest` and so know the
  version and spec. With the sandbox it would also check its status after
  each drop, as `attach` does, or need an `OnDropped`-style callback for
  `TaskRunner` to check it. `Open` keeps `RunStream` free of `backend`, the
  version and the spec, and adds no callback. The status check runs only when
  the driver does not answer. The costs are one driver `Status` call per
  stream, the check moving one backoff later (behaviour change 4), and the
  `start` flag `RunStream` must get right so a reattach never carries the
  spec.
- **A `Start` option vs. inferring the start.** `RunStream` could treat
  every run as starting until its first `Accepted`. But then a reattach
  after a runner restart would also be bounded by `ReadyTimeout` and report
  `LostStartFailed`, instead of `LostNoRun` on `NotFound` and
  `OnUnreachable` after `LostTimeout`, which `TestTaskRunnerRun_NoSuchRun`
  and `TestTaskRunnerRun_DriverUnreachable` assert. `Start` is a bool, so
  `RunStream` still knows neither the version nor the spec.
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
