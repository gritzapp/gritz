# Fake runner backend

Issue: https://github.com/gritzapp/gritz/issues/1760

## Problem

The runner tests (`internal/runner/taskmanager_test.go`,
`internal/runner/taskrunner_test.go`) are in `package runner`. They assert on
`TaskRunner`'s and `TaskManager`'s private fields (`tr.sbx`, `tr.rec`,
`tr.running`, `m.tasks`, `m.sem`, `m.store`, ...), so renaming an unexported
field breaks the test build. That happened with `sb` -> `sbx`, fixed in #1779.
They also rebuild sandbox behaviour in each test from `backend.SandboxMock` /
`BackendMock` closures (`testSandbox`, `testBackend`, about 100 `Func` uses).
Each of those is a partial copy of the contract documented on
`backend.Sandbox`.

#1760 moves the tests to `package runner_test`, using only the exported API, on
top of a stateful fake backend. This proposal covers its first step: the fake,
`internal/runner/backend/fake`. It also re-checks #1760's "Replacing internal
access" table against master after #1758, and lists the exported runner API
the migrated tests need.

The fake is built and tested here. Migrating the tests is the later slices of
the [Implementation Plan](#implementation-plan).

## Design

### Package and scope

`internal/runner/backend/fake` is an in-memory `backend.Backend` for tests. It
is a normal package, not a `_test.go` file, so `runner_test` (and later other
packages) can import it. Production code never imports it.

It is deliberately small:

- It implements the `backend.Sandbox` state machine and nothing else of a
  platform. It has no transitional states (`SUSPENDING`, pausing), no images
  and no files. A test that needs a transitional state's effect injects it
  with `FailNext` (a plain error from `Start`, as `backend.Sandbox.Start`
  documents).
- It does not import `driverserver`. A sandbox's driver is a
  `DriverServiceClient` the test provides (see [Driver hookup](#driver-hookup)).
- It implements `backend.RunEnver` unconditionally. `RunEnv` returns
  `Options.RunEnv(ws)` when that is set, and nil otherwise, which
  `TaskManager.runSpec`'s `maps.Copy` treats as no environment. This replaces
  `runEnvBackend`, which embeds `*backend.BackendMock` today.
- `ValidateWorkspace` accepts every workspace.

### State machine

Each sandbox the fake holds is in one of four states:

```
            Start                Park
 created ─────────▶ up ◀───────────────▶ parked
    │                │     Start              │
    │ Destroy        │ Destroy                │ Destroy
    ▼                ▼                        ▼
 ──────────────────────── gone ──────────────────────
```

The table below gives each operation's result per state. It is the contract on
`backend.Sandbox`, applied to the fake:

| Op | created | up | parked | gone |
|---|---|---|---|---|
| `Status` | `StatusParked` | `StatusUp` | `StatusParked` | `StatusGone` |
| `Start` | -> up | no-op | -> up | `ErrGone` |
| `Park` | no-op (stays created) | -> parked | no-op | `ErrGone` |
| `Destroy` | -> gone | -> gone | -> gone | `nil`, no change |
| `Dial` | client | client | client | client |
| `Handle` | fixed | fixed | fixed | fixed |

- **Gone is terminal.** Nothing leaves it. IDs are never reused, so a gone
  sandbox stays in the fake's observations for the rest of the test.
- **Handles share state.** A `fake.Sandbox` value is only `{backend, id}`. All
  state lives in the backend's table, keyed by ID. `Create` and every `Open`
  of the same ID return values that see the same sandbox, so `Open` after
  `Create`, two `Open`s of one handle, and the `Open` of a second
  `TaskManager` on the same fake (a runner restart) all observe and change one
  sandbox.
- **Stale handles.** `Open` does no I/O and does not check the table, as on
  every real backend. A handle whose ID the fake never had, or whose sandbox
  is gone, behaves as gone: `Status` is `StatusGone`, `Start` and `Park` return
  `backend.ErrGone`, `Destroy` returns nil.
- **`Open` validation.** `Open` fails only for a handle of another `Type` or
  with an empty `ID`, as Docker's does. `Data` is ignored, and `Handle()`
  returns the sandbox's own handle (`{Type, ID}`, no `Data`), as Docker's does.
  So a legacy record's `Data` disappears from the record the next time the
  `TaskRunner` writes it, as in production.
- **IDs.** `Create` names sandboxes `c1`, `c2`, ... in call order, skipping IDs
  that `Add` already used. The handle type is `Options.Type`, defaulting to
  `"fake"`.
- **`Create` errors.** A `Create` that fails (through `FailNext`) leaves no
  sandbox, as `backend.Backend.Create` requires. Its spec is still recorded in
  `Specs`.
- **Dial** never fails and never checks the state: it only returns a client.
  Whether the driver answers depends on the state (see
  [Driver hookup](#driver-hookup)).
- **Concurrency.** One mutex guards the table. Every method is safe for
  concurrent use, as `backend.Sandbox` requires. Hooks and driver handlers run
  without the lock held.

### Controls

A test changes the fake from outside the runner through three methods:

- **`Add(id, state)`** puts a sandbox into the fake as if a previous runner
  process had created it, and returns its handle. The test writes the matching
  `taskstate.Record` and then calls `Load` or `TaskRunner.Restore`. This
  replaces `track` / `seed`. A sandbox added `Up` has a driver like any other.
- **`Set(id, state)`** moves a sandbox the way the platform or an operator
  would, without the runner knowing: `Gone` is `docker rm` or a terminated VM,
  `Parked` is a container that exited, `Up` is someone running `docker start`.
  `Set` cannot move a sandbox out of `Gone` and panics on an unknown ID: both
  are test bugs. A `Set` to `Gone` is not recorded in `Destroyed`, which keeps
  "the runner destroyed it" apart from "it vanished".
- **`FailNext(op, id, err)`** makes the next call of `op` on sandbox `id`
  return `err` without changing state. `id` `""` matches any sandbox, and is
  the only form that makes sense for `OpCreate`. Failures queue: two
  `FailNext` calls for the same `(op, id)` fail the next two calls. Failures
  are scoped to one sandbox because `Prune` walks the registry in map order: a
  failure that "the next `Status` anywhere" consumes would pick a random task.
  `Ops` cover every method that can fail: `OpCreate`, `OpOpen`, `OpStart`,
  `OpPark`, `OpStatus`, `OpDestroy`, `OpDial`. `OpDial` matters because a
  real `Dial` can fail (Lambda's does), and `TaskRunner` handles that in
  `Kill`, `startRun` and `attachOnce`; a failed `Dial` returns no client and
  is not counted in `Dials`.

A fourth method, `Drop(id)`, cuts every `Run` call the runner has open on the
sandbox's driver without changing its state (a network blip). It replaces
`hs.CloseClientConnections()` in `TestTaskRunnerRun_Reattach`.

### Observations

`Snapshot()` returns a copy of the fake's state:

- `Sandboxes`: every sandbox the fake has held, in creation order, gone ones
  included. Each is an `Info` with its handle, task, state, the `Spec` it was
  created from (nil for `Add`), and how many calls of each operation
  succeeded on it (`Starts`, `Parks`, `Destroys`, `Dials`). These counters
  replace `sb.StartCalls()`, `ParkCalls()`, `DestroyCalls()` and
  `DialCalls()`. A call that `FailNext` failed, or that returned `ErrGone`, is
  not counted. A no-op is: `Start` on an up sandbox counts, because the runner
  made the call. That is what lets a trailing `Start` prove which sandbox a
  `TaskRunner` holds (see [Probing what a TaskRunner holds](#probing-what-a-taskrunner-holds)).
  `Info.RunSpecs` is a deep copy of the `RunSpec` of every `Run` request
  with one (a request to start a run) sent to the sandbox's driver while up, in
  order (see [Driver hookup](#driver-hookup)). This replaces the tests that
  call `m.runSpec` directly.
- `Specs`: a deep copy of every `Spec` passed to `Create`, in call order,
  including those that failed. This replaces `be.CreateCalls()[0].Spec` and
  the tests that read `m.spec` directly.
- `Destroyed`: the IDs that `Destroy` moved to gone, in order.
- Helpers `Sandbox(id)` and `Live()` (sandboxes that are not gone).

**Waiting without sleeps.** `Wait(ctx, cond)` blocks until `cond` holds for
the current snapshot, or `ctx` is done. Every change to the table (a state
change, a counter, a spec) closes and replaces a `changed` channel, and `Wait`
re-checks `cond` each time it fires. Nothing polls. `WaitState(ctx, id,
state)` is the common case. This replaces `waitDialed` and the `parked`
channel `testSandbox` returns.

### Driver hookup

`Options.Dial func(backend.Handle) driverv1connect.DriverServiceClient`
returns the test's client to a sandbox's driver. In `runner_test` that is the
client `serveDriver` already gives the test, over an `httptest.Server`
serving a real `driverserver.Server`'s handler, so the fake never imports
`driverserver`. Gating that server (a `ready` channel for a slow driver)
needs no fake support.

The fake calls `Options.Dial` on every `Dial`, without its lock held, and
wraps the result in its own `DriverServiceClient`, which makes the driver
follow the sandbox's state:

- `Status` and `Stop` fail with `CodeUnavailable` while the sandbox is not
  up, the same as a driver that is not answering, and call through
  otherwise. A nil `Dial`, or a `Dial` that returns nil, gives a sandbox
  whose driver never answers (`TestTaskRunnerRun_DriverUnreachable`).
- `Run` makes the same check. Then it records the request's `RunSpec`, if it
  has one (a request to start a run), in the sandbox's `Info.RunSpecs`, and
  calls through with a context the fake cancels when the sandbox leaves up
  (`Park`, `Destroy`, or `Set` to `Parked` or `Gone`), as stopping a
  container kills its driver, and on `Drop` and `Close`. An attached `Run`
  stream ends, and `attach` then sees the new `Status`. This is what lets a
  test make a sandbox vanish under a run with one call.

A stream the fake cuts ends with `CodeCanceled`, not the transport error a
real dropped connection gives. `attach` treats it like any drop and
reattaches. `startRun`'s accept loop only retries `CodeUnavailable`, though,
so a test must not cut a sandbox while a start is being accepted: the start
would fail rather than be retried.

The in-process driver outlives the sandbox's state. Parking the fake does not
stop the run inside `driverserver`, and starting the fake again does not
restart it. A test that needs the run to end stops it through the driver, as
today (`stopRun`). Tests that start a run on the driver themselves
(`serveDriver`) use the same client unwrapped. That test-side client is not
gated, because the test stands inside the sandbox.

`Dial`'s JWT is ignored, as a driver with no runner key ignores it.

### API

```go
package fake // github.com/gritzapp/gritz/internal/runner/backend/fake

// Backend is an in-memory backend.Backend whose sandboxes follow the
// backend.Sandbox state machine. It is safe for concurrent use.
type Backend struct{ ... }

// Options configures a Backend.
type Options struct {
	// Type is the handle type of the sandboxes; defaults to "fake".
	Type string
	// Dial returns the test's client to the driver of the sandbox with
	// handle h; nil means its driver never answers.
	Dial func(h backend.Handle) driverv1connect.DriverServiceClient
	// RunEnv returns the environment RunEnv gives runs of a workspace's
	// sandboxes; nil means none.
	RunEnv func(ws *workspace.Workspace) map[string]string
}

// New returns a Backend with no sandboxes.
func New(opts Options) *Backend

// ValidateWorkspace accepts every workspace.
func (b *Backend) ValidateWorkspace(ws *workspace.Workspace) error

// RunEnv returns Options.RunEnv's environment for ws, or nil without one.
func (b *Backend) RunEnv(ws *workspace.Workspace) map[string]string

// Create records spec and adds a created sandbox for it.
func (b *Backend) Create(ctx context.Context, spec *backend.Spec) (backend.Sandbox, error)

// Open returns the sandbox for h, which shares state with every other handle to it.
func (b *Backend) Open(h backend.Handle) (backend.Sandbox, error)

// Close closes the sandboxes' driver servers.
func (b *Backend) Close() error

// State is a sandbox's state in the fake.
type State int

const (
	// Created is a sandbox Create made and nothing has started yet.
	Created State = iota + 1
	// Up is a running sandbox.
	Up
	// Parked is a sandbox whose compute is stopped.
	Parked
	// Gone is a sandbox that no longer exists; it is terminal.
	Gone
)

// String returns the state's name.
func (s State) String() string

// Op names a sandbox or backend operation for FailNext.
type Op int

const (
	OpCreate Op = iota + 1 // Backend.Create
	OpOpen                 // Backend.Open
	OpStart                // Sandbox.Start
	OpPark                 // Sandbox.Park
	OpStatus               // Sandbox.Status
	OpDestroy              // Sandbox.Destroy
	OpDial                 // Sandbox.Dial
)

// String returns the op's name.
func (o Op) String() string

// Add puts a sandbox with id in state into the fake, as a previous runner process would have left it, and returns its handle.
func (b *Backend) Add(id string, state State) backend.Handle

// Set moves sandbox id to state without going through the runner.
func (b *Backend) Set(id string, state State)

// FailNext makes the next op call on sandbox id return err; id "" matches any sandbox.
func (b *Backend) FailNext(op Op, id string, err error)

// Drop cuts every Run call open on sandbox id's driver.
func (b *Backend) Drop(id string)

// Snapshot returns a copy of the fake's current state.
func (b *Backend) Snapshot() Snapshot

// Wait blocks until cond holds for the fake's state, and returns that state.
func (b *Backend) Wait(ctx context.Context, cond func(Snapshot) bool) (Snapshot, error)

// WaitState blocks until sandbox id is in state.
func (b *Backend) WaitState(ctx context.Context, id string, state State) error

// Snapshot is a copy of the fake's state at one point.
type Snapshot struct {
	// Sandboxes is every sandbox the fake has held, in creation order.
	Sandboxes []Info
	// Specs is every spec passed to Create, in call order.
	Specs []backend.Spec
	// Destroyed is the IDs Destroy moved to gone, in order.
	Destroyed []string
}

// Sandbox returns the sandbox with id.
func (s Snapshot) Sandbox(id string) (Info, bool)

// Live returns the sandboxes that are not gone.
func (s Snapshot) Live() []Info

// Info is one sandbox in a Snapshot.
type Info struct {
	Handle backend.Handle
	// TaskID is Spec's task, or 0 for a sandbox Add made.
	TaskID int64
	State  State
	// Spec is the spec the sandbox was created from, or nil for Add.
	Spec *backend.Spec
	// Starts, Parks, Destroys and Dials count the calls that succeeded, no-ops included.
	Starts, Parks, Destroys, Dials int
	// RunSpecs is the spec of every Run request with one sent to the sandbox's driver while up, in order.
	RunSpecs []*driverv1.RunSpec
}
```

**How it fits:** a `runner_test` test calls `fake.New` with a `Driver` built
from a real `driverserver`, and passes the result as
`TaskManagerOptions.Backend` or `TaskRunnerOptions.Backend`. It arranges
sandboxes with `Add`, `Set` and `FailNext`, drives the runner through its
exported API, and asserts on `Snapshot` and `Wait`, next to the
`taskstate.Store` and the `gritzclient.ClientMock` it already owns.

### Exported runner API the tests need

The rewrites below need two additions to `internal/runner`. The `TaskRunner`
and `TaskManager` surfaces stay as #1758 left them otherwise. In particular,
there is no accessor for what a `TaskRunner` holds: tests find that out by
driving it (see [Probing what a TaskRunner holds](#probing-what-a-taskrunner-holds)).

```go
package runner // github.com/gritzapp/gritz/internal/runner

// ErrRunning is returned by TaskRunner.Start when a Run still follows the task's previous run. // new: replaces errRunning
var ErrRunning = errors.New("sandbox is still running")

// Wait blocks until every Run goroutine the manager started has returned, or ctx is done.
// It must not be called while the manager can still start runs (Poll, Load, or Run's loop). // new
func (m *TaskManager) Wait(ctx context.Context) error
```

- **`ErrRunning`.** `TestTaskRunnerStart` and `TestTaskRunnerStart_Concurrent`
  assert that a second `Start` refuses with it. It is part of `Start`'s
  documented contract, and `Poll` already branches on it.
- **`TaskManager.Wait`.** "The Run is done" is what `waitIdle` and
  `waitReleased` wait for, and every negative assertion after a run needs it:
  `attach` parks the sandbox *before* `Run` sends its `TaskLostEvent`. So
  "sandbox parked, outbox empty" passes even when an event is about to arrive.
  The fake sees the park; only the manager sees the goroutine return. A
  `sync.WaitGroup` around `m.run`'s goroutine (`Done` deferred, so it runs
  after `Release` and `Wake`, and on the shutdown path that skips them) gives
  that without exposing `m.sem`.

  `Wait` is only for when nothing can start another run: `Poll`, `Load` and
  `Run`'s loop must all be done or not yet called. Each of them reaches
  `m.run`, whose `WaitGroup.Add` from a zero count while `WaitGroup.Wait` is
  blocked is a misuse of `sync.WaitGroup`. Shutdown meets that condition
  (`Run` has returned). A test meets it by calling `Wait` only after its last
  `Load`, `Poll` or `Start` has returned, and never while `m.Run` is running.

  It is also the missing step in `gritz runner`'s shutdown, where the deferred
  `m.Close()` closes the backend while Run goroutines may still be unwinding.
  Wiring it in there is a separate `fix:` PR (see [Decisions](#decisions)).

Calling `TaskManager.Start` directly without holding a slot, as the
rewritten tests do, is harmless: `safesem.Release` floors at zero. So
`startTask`'s `TryAcquire` goes away. Tests about admission go through `Poll`.

### Probing what a TaskRunner holds

Today's tests read `tr.sbx` and `tr.rec` to see whether a `TaskRunner` still
holds a sandbox. The rewritten tests find out by driving it: they call `Start`
on it and look at what the fake saw.

- **A new `Create`** (one more entry in `Snapshot().Specs`, a new sandbox
  `c2`) means it held no sandbox.
- **No `Create`, and one more `Start` on `c1`** (its `Starts` counter went up)
  means it still held `c1`.
- **`ErrRunning`** means it is claimed, and nothing about the sandbox can be
  learned that way. Only `Restore` on an up sandbox claims without a `Run`, and
  it claims in the same lock acquisition that installs the sandbox, so for
  `Restore` the claim is itself the proof that the sandbox was adopted.

The record half needs no probe: the store is the test's own. The fake half
needs no probe either: `Snapshot` shows what is live and what was destroyed.

The probe changes state. It may create a sandbox, starts one, and restamps
the record with the probe's version, so it is always the **last** thing a test
does with that `TaskRunner`. In the middle of a test, the next act usually
already is the probe: in `Start_Gone`, the second `Start` creating `c2`
proves that the first one left nothing behind. Where it is not, the test
checks only the store and the fake at that point.

`TestTaskManagerHandleEvents_Fatal` uses the same probe. Its store's
directory is removed, so a missing record proves nothing. A trailing
`m.Runner(1).Start` makes a second `Create` (two `Specs`), which proves the
failed write left the `TaskRunner` with no sandbox. The probe's own record
write fails as well and sends a second `TaskFatalEvent`, which is harmless:
the first cause wins.

### Replacing internal access, re-checked

#1760's table predates #1758. On master, `taskEnv`, `lookup`, `register`,
`loadTaskRunner`, `tr.claim` and `m.version` are gone, timings moved to
`TaskRunnerOptions` (set through `TaskManagerOptions.NewTaskRunner`), and
`TaskManager.Runner` replaced the per-task wrappers. Here is every internal the
two test files touch today, and the black-box replacement:

| Internal used today | Used by | Black-box alternative | New API |
|---|---|---|---|
| `TaskRunnerOptions` timings via `testManager`'s `tune` | most | already exported: `TaskManagerOptions.NewTaskRunner` wraps `NewTaskRunner` | — |
| `m.pollInterval`, `m.fatal` assigned after construction | `TestTaskManagerRun` | `TaskManagerOptions.PollInterval`, `.Fatal` | — |
| `m.die` | `TestTaskManagerRun` | a durable write that fails: an `outbox.StoreMock` whose `Append` fails, as `TestTaskManagerDie_StopWithoutSandbox` does | — |
| `m.store` | many | the `*taskstate.Store` the test passed in | — |
| `m.backend.Open`, `recordHandle` (`seed`) | `seed`, assertions on `OpenCalls` | `be.Add`, the record in the store, then `tr.Restore` or `Load`. Assertions on the opened handle become assertions on the fake sandbox's state and counters | — |
| `tr.sbx`, `tr.rec` (`track`, `assertTracked`, Restore and Fatal tests) | ~25 tests | the store for the record, the fake for the sandboxes, and a trailing `Start` for what the `TaskRunner` holds (see [Probing](#probing-what-a-taskrunner-holds)) | — |
| `m.tasks` (`registered`, Prune's key list) | Start_Gone, Prune, Run | `m.Runner(id)` pointer identity: the same pointer is kept, a new one was dropped. `TestTaskManagerRun` waits on `Snapshot().Destroyed` instead of polling the registry | — |
| `tr.running` (`claim`) | StartStillRunning, Prune, Remove_Running, `runAsync` | `tr.Restore` on an up sandbox claims, as `Load` does. "Claimed but not up" is `Restore` on `Up`, then `be.Set(id, fake.Parked)` | — |
| `tr.running` (`waitIdle`) | Run_Finished, Run_Shutdown, RestartReattaches | a synchronous `tr.Run` returning is idle already (the `running` reset is deferred inside `Run`). A manager's Run goroutines use `m.Wait`. "Claimed" as an assertion (Restore test) is probed with `Start` returning `ErrRunning` | `Wait`, `ErrRunning` |
| `m.sem` (`startTask`, `waitReleased`) | ~10 tests | `m.Start` directly (`Release` floors at zero), then `m.Wait` | `Wait` |
| `m.sem` (Load's seeding check, StartStillRunning's "slot is free") | Load, StartStillRunning | behaviour, through `Poll` at `Concurrency: 1`: a pending START for a second task gets no `Create` while the slot is held, and gets one once it is free | — |
| `m.spec` | Spec_PreCreatesLogDir, Spec_Secrets | `Snapshot().Specs` after `m.Start`; the real Docker backend is no longer needed | — |
| `m.runSpec` | RunSpec_RunEnv | the `RunRequest.Spec` the driver receives, recorded by the fake in `Info.RunSpecs`; `RunEnv` from `fake.Options.RunEnv` | — |
| `m.run` | Remove_Running | `Load` of a record whose fake sandbox is up | — |
| `tr.send` | HandleEvents_Lost, Send_Shutdown | a real lost run (`be.Set(id, fake.Gone)` under an attached run). For Send_Shutdown, a `NewTaskRunner` whose `Events` nobody reads, and a ctx cancelled while `Run` is blocked sending | — |
| `m.events` | Restore test's `NewTaskRunner` | a channel the test makes | — |
| `errRunning` | TaskRunnerStart, Start_Concurrent | `ErrRunning` | `ErrRunning` |
| `errRemoved` | none directly | only the lost reason string is asserted | — |
| `tr.opts` (defaults), `tr.sbx` in TestNewTaskRunner | TestNewTaskRunner | the panics half moves to `runner_test`. The defaults half is dropped: it only restates the `default*` constants | — |
| `doubling` | TestDoubling | stays in `internal_test.go`, the only test left in `package runner` | — |
| `SandboxMock.*Calls()`, `BackendMock.*Calls()` | most | `Info` counters, `Snapshot().Specs`, `Destroyed` | — |

Differences from #1760's table:

- **`waitIdle` cannot be "the fake sandbox reaching parked".** As above,
  parking comes before the lost event, so "parked" does not mean "done". Hence
  `TaskManager.Wait`. For a `tr.Run` the test calls itself, `Run` returning is
  enough.
- **`track` / `seed` use `TaskRunner.Restore`** where the test does not want
  `Load`'s side effects (a Run goroutine for every up sandbox, `GetTask` for
  every parked one). `Restore` is exported and does exactly what `seed` did,
  plus the claim of an up sandbox.
- **No timing fields** need adding: #1758 already put them on
  `TaskRunnerOptions`.
- **No accessor for what a `TaskRunner` holds.** The issue left `tr.sbx` to
  "the fake's live sandboxes". Those show what exists, not what the
  `TaskRunner` holds, so the tests add a trailing `Start` probe.

### Shared helpers and `backendtest`

Neither is needed for the fake, so both stay out of this proposal:

- The helpers (`testStore`, `testManager`, `submitted`, `waitQueued`,
  `assertTracked`, the driver helpers) move with the tests into
  `internal/runner/helpers_test.go` in `package runner_test`. They stay
  unexported and test-only. A shared `internal/runner/runnertest` package is
  worth it only once a second package needs them, and nothing does yet.
- The fake's own tests are a table over `(state, op)` → `(result, new state)`,
  the table in [State machine](#state-machine). They are written to take a
  `backend.Backend` plus a way to put a sandbox in a state, so a later
  `backend/backendtest` conformance suite can lift them and run them against
  Docker and Lambda as well. That suite is its own follow-up.

### Validation: rewritten tests

The tests below are today's, rewritten as `package runner_test` against the
fake and the two additions above. They assume these helpers in
`helpers_test.go`, ported from today's:

- `testStore(t, recs...)`, `submitted`, `waitQueued`: unchanged.
- `testManager(t, be, store, tune...)`: unchanged, but with `runner.` prefixes.
- `driverClient(t, version, ready) driverv1connect.DriverServiceClient`:
  today's `newDriver`, returning only the test's client.
- `serveDriver(t, taskID, version, cfg) driverv1connect.DriverServiceClient`:
  as today, returning the test's client, which also goes in
  `fake.Options.Dial`.
- `drivers(client) func(backend.Handle) driverv1connect.DriverServiceClient`:
  every sandbox gets the driver `client` reaches.

`assertRecord` checks the store only, so it can go anywhere in a test.
`assertTracked` adds the probe, so it goes last:

```go
// assertRecord asserts the task's record in store: *want, or none with want
// nil.
func assertRecord(t *testing.T, store *taskstate.Store, taskID int64, want *taskstate.Record) {
	t.Helper()
	rec, ok, err := store.Read(taskID)
	assert.NilError(t, err)
	if want == nil {
		assert.Assert(t, !ok, "task %d has a record", taskID)
		return
	}
	assert.Assert(t, ok, "no record for task %d", taskID)
	assert.DeepEqual(t, rec, *want)
}

// probeVersion is the version assertTracked's probe Start runs.
const probeVersion = 1000

// assertTracked asserts the task's record, as assertRecord does, and that
// its TaskRunner holds sandbox want.ID, or, with want nil, no sandbox. It
// finds out by starting the TaskRunner, which changes its state and the
// record, so it is the last thing a test does with the task.
func assertTracked(t *testing.T, m *runner.TaskManager, be *fake.Backend, store *taskstate.Store, taskID int64, want *taskstate.Record) {
	t.Helper()
	assertRecord(t, store, taskID, want)
	before := be.Snapshot()
	err := m.Runner(taskID).Start(t.Context(), backend.Spec{TaskID: taskID, Version: probeVersion})
	after := be.Snapshot()
	assert.Assert(t, !errors.Is(err, runner.ErrRunning), "task %d's TaskRunner is claimed, so it cannot be probed", taskID)
	if want == nil {
		// A TaskRunner with no sandbox creates one.
		assert.Equal(t, len(after.Specs), len(before.Specs)+1, "task %d's TaskRunner holds a sandbox", taskID)
		return
	}
	// A TaskRunner holding want.ID starts it, and creates nothing.
	assert.NilError(t, err)
	assert.Equal(t, len(after.Specs), len(before.Specs), "task %d's TaskRunner held no sandbox", taskID)
	was, _ := before.Sandbox(want.ID)
	now, _ := after.Sandbox(want.ID)
	assert.Equal(t, now.Starts, was.Starts+1, "task %d's TaskRunner does not hold %s", taskID, want.ID)
}
```

`m.Runner` registers a `TaskRunner` for a task that has none. The old helper
told "no `TaskRunner`" apart from "a `TaskRunner` with no sandbox". #1758 made
that difference meaningless, so the two cases merge here: either way, the
probe creates.

#### `assertTracked`: a sandbox that fails to start

```go
// TestTaskManagerStart_StartFails covers a sandbox that cannot be started after
// Create: the record is kept, and Destroy destroys the sandbox.
func TestTaskManagerStart_StartFails(t *testing.T) {
	t.Parallel()
	// Arrange - the sandbox Start creates refuses to start.
	task := &model.Task{ID: 1, Runner: "test-runner", Workspace: "test", Version: 1}
	boom := errors.New("boom")
	be := fake.New(fake.Options{})
	be.FailNext(fake.OpStart, "", boom)
	store := testStore(t)
	m, _, _ := testManager(t, be, store)

	// Act
	err := m.Start(t.Context(), task)

	// Assert - Start failed with the record and sandbox kept and nothing run.
	// The record surviving the failed start also shows it was written before
	// the sandbox was started.
	assert.ErrorIs(t, err, boom)
	assertRecord(t, store, 1, &taskstate.Record{TaskID: 1, Version: 1, Type: "fake", ID: "c1"})
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Created)
	assert.Equal(t, sbx.Dials, 0)

	// Act - the task's sandbox is removed.
	assert.NilError(t, m.Runner(1).Destroy(t.Context()))

	// Assert - Destroy found c1, so the TaskRunner still held it; now it holds
	// nothing, and the record is gone.
	assert.DeepEqual(t, be.Snapshot().Destroyed, []string{"c1"})
	assertTracked(t, m, be, store, 1, nil)
}
```

Replaced: `testSandbox(nil)` + `StartFunc`, `BackendMock.CreateFunc`,
`startTask` (the slot), `sb.DialCalls()`, `sb.DestroyCalls()`, and the private
`assertTracked`. The first `assertTracked` of the original, in the middle of
the test, becomes `assertRecord` plus the fake's view: a probe there would
start `c1`. The `Destroy` that follows is the drive that shows the
`TaskRunner` still held `c1`. The trailing `assertTracked` probes with a
`Start` that creates `c2`.

#### `waitIdle`: a runner restart mid-run

```go
// TestTaskManagerLoad_RestartReattaches covers a runner restart mid-run: the
// first runner process shuts down while it follows the run, and the next one
// reattaches to it from the record in Load.
func TestTaskManagerLoad_RestartReattaches(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going on an up sandbox, recorded at its
	// version, followed by a runner that then shuts down.
	client := serveDriver(t, 1, 3, sleepForever)
	be := fake.New(fake.Options{Dial: drivers(client)})
	be.Add("c1", fake.Up)
	store := testStore(t, taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"})
	first, _, _ := testManager(t, be, store)
	ctx, cancel := context.WithCancel(t.Context())
	assert.NilError(t, first.Load(ctx))
	waitDialed(t, be, "c1", 1)
	cancel()
	assert.NilError(t, first.Wait(t.Context()))
	// The sandbox stays up for next-boot rehydration.
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Up)
	assert.Equal(t, sbx.Parks, 0)

	// Act - the next runner process loads the record and reattaches, over its
	// own Open of the same sandbox; then the run finishes.
	m, _, queue := testManager(t, be, store)
	assert.NilError(t, m.Load(t.Context()))
	waitDialed(t, be, "c1", sbx.Dials+1)
	stopRun(t, client, 3)
	assert.NilError(t, m.Wait(t.Context()))

	// Assert - the reattached runner saw Finished: the sandbox is parked and
	// no event is owed.
	sbx, _ = be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Parked)
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
}

// waitDialed waits until sandbox id's driver has been dialed at least n times.
func waitDialed(t *testing.T, be *fake.Backend, id string, n int) {
	t.Helper()
	_, err := be.Wait(t.Context(), func(s fake.Snapshot) bool {
		sbx, _ := s.Sandbox(id)
		return sbx.Dials >= n
	})
	assert.NilError(t, err)
}
```

`waitIdle(first.Runner(1))` and `waitReleased(m)` both become `Wait`. One
assertion is dropped: `!first.sem.TryAcquire(1)` ("a shut-down manager still
holds the slot"). The process it describes is exiting, so no caller can
observe it. What matters for the next boot, that the sandbox stays up and
unparked, is still asserted, through the fake. The two managers share one
sandbox only because `Open` shares state by ID, which `BackendMock` faked by
returning one `*SandboxMock` from `OpenFunc`.

#### Gone-sandbox restore

```go
// TestTaskRunnerRestore covers adopting a record at Load: an up sandbox is
// claimed for its Run, a parked one is kept unclaimed, a gone or unopenable one
// leaves no sandbox and no record, and a failed status check keeps both.
func TestTaskRunnerRestore(t *testing.T) {
	t.Parallel()
	rec := taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"}
	tests := []struct {
		name string
		// state is the sandbox's state in the fake; 0 means the fake never
		// had it.
		state       fake.State
		openErr     error
		statusErr   error
		wantStatus  backend.Status
		wantErr     bool
		wantSandbox bool // the sandbox and record are kept
		wantClaimed bool // which also shows the sandbox was adopted
	}{
		{name: "up", state: fake.Up, wantStatus: backend.StatusUp, wantSandbox: true, wantClaimed: true},
		{name: "parked", state: fake.Parked, wantStatus: backend.StatusParked, wantSandbox: true},
		{name: "gone", state: fake.Gone, wantStatus: backend.StatusGone},
		{name: "never existed", wantStatus: backend.StatusGone},
		{name: "status error", state: fake.Up, statusErr: errors.New("boom"), wantErr: true, wantSandbox: true},
		{name: "open fails", state: fake.Up, openErr: errors.New("malformed handle"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - a TaskRunner with no sandbox, and a previous process's
			// record.
			be := fake.New(fake.Options{})
			if tt.state != 0 {
				be.Add("c1", tt.state)
			}
			if tt.openErr != nil {
				be.FailNext(fake.OpOpen, "c1", tt.openErr)
			}
			if tt.statusErr != nil {
				be.FailNext(fake.OpStatus, "c1", tt.statusErr)
			}
			store := testStore(t, rec)
			tr := runner.NewTaskRunner(runner.TaskRunnerOptions{
				TaskID:  rec.TaskID,
				Backend: be,
				Store:   store,
				Events:  make(chan runner.TaskEvent, 1),
			})

			// Act
			st, err := tr.Restore(t.Context(), rec)

			// Assert
			if tt.wantErr {
				assert.Assert(t, err != nil)
			} else {
				assert.NilError(t, err)
				assert.Equal(t, st, tt.wantStatus)
			}
			if tt.wantSandbox {
				assertRecord(t, store, rec.TaskID, &rec)
			} else {
				assertRecord(t, store, rec.TaskID, nil)
			}
			// Restore never destroys, even a gone sandbox.
			assert.Assert(t, cmp.Len(be.Snapshot().Destroyed, 0))

			// Probe, last - a Start shows what the TaskRunner holds: claimed
			// refuses, c1 is started, and no sandbox means a Create.
			before := be.Snapshot()
			err = tr.Start(t.Context(), backend.Spec{TaskID: 1, Version: 4})
			after := be.Snapshot()
			was, _ := before.Sandbox("c1")
			now, _ := after.Sandbox("c1")
			switch {
			case tt.wantClaimed:
				assert.ErrorIs(t, err, runner.ErrRunning)
			case tt.wantSandbox:
				assert.NilError(t, err)
				assert.Equal(t, now.Starts, was.Starts+1, "TaskRunner does not hold c1")
				assert.Equal(t, len(after.Specs), 0, "TaskRunner held no sandbox")
			default:
				assert.NilError(t, err)
				assert.Equal(t, len(after.Specs), 1, "TaskRunner kept a sandbox")
			}
		})
	}
}
```

The `gone` row and the new `never existed` row are the gone-sandbox restore:
a sandbox that vanished since the record was written, and a record whose ID
the platform never knew. Both give `StatusGone` through the stale-handle rule
with no mock. Before, each was a `StatusFunc` returning the constant. The
"claimed" (`tr.running`) and "has a sandbox" (`tr.sbx`) assertions no longer
read fields: one trailing `Start` answers both. It refuses when claimed,
starts `c1` when the `TaskRunner` kept it, and creates when it did not. The
`status error` row shows why a no-op `Start` counts: `c1` is already up, and
the probe still has to see the `TaskRunner` reach it.

#### Gone at Start, then a fresh sandbox

```go
// TestTaskManagerStart_Gone covers a task whose recorded sandbox has vanished:
// Start fails with ErrGone and leaves the TaskRunner with no sandbox, and the
// next Start creates a fresh one.
func TestTaskManagerStart_Gone(t *testing.T) {
	t.Parallel()
	// Arrange - task 8's recorded sandbox, restored parked, is then removed
	// behind the runner's back.
	rec := taskstate.Record{TaskID: 8, Version: 1, Type: "fake", ID: "c1"}
	be := fake.New(fake.Options{Dial: drivers(driverClient(t, 3, nil))})
	be.Add("c1", fake.Parked)
	store := testStore(t, rec)
	m, _, queue := testManager(t, be, store)
	tr := m.Runner(8)
	_, err := tr.Restore(t.Context(), rec)
	assert.NilError(t, err)
	be.Set("c1", fake.Gone)
	task := &model.Task{ID: 8, Runner: "test-runner", Workspace: "test", Version: 2}

	// Act
	err = m.Start(t.Context(), task)

	// Assert - ErrGone surfaced (the caller emits failed and releases the
	// slot), no fresh sandbox was created, the dangling record was removed,
	// and the TaskRunner was kept with no sandbox.
	assert.ErrorIs(t, err, backend.ErrGone)
	assert.Assert(t, cmp.Len(be.Snapshot().Specs, 0))
	assertRecord(t, store, 8, nil)
	assert.Equal(t, m.Runner(8), tr)

	// Act - the task is started again. This Start is also the probe for the
	// first one: a Create means it left the TaskRunner with no sandbox.
	task.Version = 3
	assert.NilError(t, m.Start(t.Context(), task))
	assert.NilError(t, m.Wait(t.Context()))

	// Assert - the same TaskRunner created one fresh sandbox and ran the
	// workspace's agent on it; the driver reported, and the sandbox is parked.
	snap := be.Snapshot()
	assert.Assert(t, cmp.Len(snap.Specs, 1))
	assert.Equal(t, snap.Specs[0].Version, int64(3))
	fresh, _ := snap.Sandbox("c2")
	assert.Equal(t, fresh.State, fake.Parked)
	assert.Equal(t, m.Runner(8), tr)
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
	// The runner never destroyed the vanished sandbox; it only dropped it.
	assert.Assert(t, cmp.Len(snap.Destroyed, 0))
	assertTracked(t, m, be, store, 8, &taskstate.Record{TaskID: 8, Version: 3, Type: "fake", ID: "c2"})
}
```

Before, this test needed two mocks wired differently (`OpenFunc` returning
`gone`, `CreateFunc` returning `fresh`) and a `StartFunc` returning `ErrGone`.
Now it needs one `Set`. The fresh sandbox's ID (`c2`, not a second `c1`) and
the empty `Destroyed` are behaviour the mocks could not express. The original
`assertTracked(t, m, 8, nil)` in the middle becomes `assertRecord`. The
second `m.Start` creating `c2` is the probe that shows the `TaskRunner` held
nothing.

### Visualization

`diagrams/src/projects/proposals/fake-runner-backend.ts`, with the layout in
`diagrams/src/layouts/fakebackend.tsx`. View it with `mise run motioncanvas`.

- `fake-start`: a run on the fake. The `Spec` is recorded, c1 goes created ->
  up -> parked, the driver answers only while c1 is up, and the test waits
  with `TaskManager.Wait` and asserts on `Snapshot`.
- `fake-gone`: `Set(c1, Gone)` under an attached run drops the stream, and the
  run is lost. The next `Start` gets `ErrGone`, the one after creates `c2`,
  and `Destroyed` stays empty.
- `fake-restart`: two `TaskManager`s on one fake. The first reattaches, shuts
  down and is waited for; the second `Open`s the same c1 and follows the run to
  its end.

## Implementation Plan

1. **Fake state machine** — Delivers: `internal/runner/backend/fake` with
   `New`, `Create`, `Open`, `Close`, `ValidateWorkspace`, the sandbox methods
   per the state table (with no driver: `Dial` returns a client that is never
   answered), `State`, `Snapshot`, `Info`, `Wait`, `WaitState`, and `Add`.
   Depends on: nothing. Verifiable by: the fake's table test over every
   `(state, op)`, plus tests that `Open` after `Create` and two `Open`s share
   state, that a stale handle behaves as gone, and that `Wait` wakes on a
   change made from another goroutine.
2. **Fake controls** — Delivers: `Set`, `FailNext` and `Op`. Depends on: (1).
   Verifiable by: fake tests that `Set` moves state and refuses to leave gone,
   that `FailNext` is per-sandbox, queued, and leaves state unchanged, and that
   a failed `Create` leaves no sandbox but records its spec.
3. **Fake driver hookup** — Delivers: `Options.Driver`, the per-sandbox
   driver server gated on `Up`, connections closed on leaving `Up`, and
   `Drop`. Depends on: (1). Verifiable by: a fake test with a stub
   `http.Handler` (no `driverserver`) showing a request answered while up,
   `Unavailable` while parked, and an open stream closed by `Park` and by
   `Set(Gone)`.
4. **Runner exports** — Delivers: `ErrRunning` (renamed from `errRunning`)
   and `TaskManager.Wait` with its WaitGroup, documented as not to be called
   while the manager can still start runs. Depends on: nothing (parallel to
   1–3). Verifiable by: the existing internal tests, updated for the rename,
   plus a test that `Wait` returns once a run finishes and on a ctx that is
   done.
5. **TaskRunner tests to `runner_test`** — Delivers: `helpers_test.go`
   (`package runner_test`), `taskrunner_test.go` moved and rewritten on the
   fake, the panics half of `TestNewTaskRunner` (its defaults half is
   dropped), and `internal_test.go` keeping `TestDoubling` only. Depends on: (2), (3), (4). Verifiable by: `mise run
   test -- ./internal/runner/...`, and renaming `tr.sbx` locally to confirm
   the moved file still compiles.
6. **TaskManager start/stop tests** — Delivers: the Start, Poll, Kill,
   Remove and Spec tests from `taskmanager_test.go` moved to `runner_test` on
   the fake, the Spec tests without the Docker backend. The fake gains
   `Options.RunEnv`, `Info.RunSpecs` and `OpDial`, and its driver hookup
   moves from `Options.Driver` (a handler) to `Options.Dial` (a client it
   wraps; see [Driver hookup](#driver-hookup)). Depends on: (5).
   Verifiable by: the runner test suite.
7. **TaskManager load/prune/events tests** — Delivers: the Load, Prune, Run,
   HandleEvents, Die and outbox tests moved, `eventoutbox_test.go` included.
   No `taskmanager_test.go` is left in `package runner`. Depends on: (6).
   Verifiable by: the runner test suite, and `grep -l '^package runner$'
   internal/runner/*_test.go` listing only `internal_test.go`.
8. **Drop the backend mocks** — Delivers: `Backend` and `Sandbox` removed
   from `backend`'s `//go:generate moq` line, `backend_moq.go` deleted.
   Nothing outside the runner tests uses them. Depends on: (7). Verifiable by:
   `go build ./... && go vet ./...` and the test suite.

## Trade-offs

- **A fake over shared mock helpers.** Better `SandboxMock` builders would
  shrink the closures but still let every test define its own semantics, so a
  test could pass against behaviour no backend has (a mock `Status` saying up
  after a `Park`). The fake has one state machine, and it is the contract on
  `backend.Sandbox`. The cost is that tests can no longer pin an arbitrary
  sequence of results. `FailNext` covers the cases today's tests need.
- **A wrapped client instead of an `http.Handler`.** Slice 3 had the test
  hand the fake an `http.Handler`, served on an `httptest.Server` per
  sandbox, so the fake could gate requests on state and drop connections.
  Recording the `RunSpec` of a request then meant decoding the Connect
  envelope in that gate, which is too brittle for a test fake. Wrapping the
  test's `DriverServiceClient` gates on state just as well. It sees typed
  requests, so recording a spec is one line, and it cuts a `Run` by
  cancelling its context. The cost is that a cut stream ends with
  `CodeCanceled` rather than a transport error (see
  [Driver hookup](#driver-hookup)), and that the fake's gate covers only the
  three methods the client has today.
- **No `Before`/hook API.** A hook run before each op (for "the record is
  written before `Start`" assertions, or blocking `Create`) is more general
  than `FailNext`, but it brings back per-test closures. A record that survives
  a `FailNext(OpStart)` already shows the write came first, and
  `TestTaskRunnerStart_Concurrent`'s 10ms `Create` sleep is not needed:
  `tr.mu` serializes the two Starts either way.
- **`TaskManager.Wait` over a `Claimed()` accessor.** `TaskRunner.Claimed()`
  would expose the claim flag and still need polling to wait on it. `Wait`
  is a blocking primitive with a production use.
- **Probing over a `TaskRunner.Record()` accessor.** An accessor for the
  in-memory record would let `assertTracked` check the `TaskRunner` without
  changing it, and could go anywhere in a test. But it asserts on state
  rather than behaviour, and it would be API that exists for tests only. A
  trailing `Start` checks what matters, that the next run lands on the right
  sandbox, through the API production uses. The cost is that the probe
  changes state and must go last, and that it cannot see past a claim. The
  only claim without a `Run` is `Restore` on an up sandbox, and that claim is
  itself the proof.
- **`m.Runner` in `assertTracked`.** It registers a `TaskRunner` for an
  unknown task. A `TaskManager.Registered(id)` accessor would avoid that, but
  would expose the registry purely for tests. Since #1758, "no `TaskRunner`"
  and "no sandbox" mean the same thing to every caller.
- **Dropping the defaults half of `TestNewTaskRunner`.** Keeping it would
  need either `tr.opts` (internal) or an options accessor. It only restates
  the `default*` constants, so it is dropped rather than kept internal.
- **Helpers stay in `helpers_test.go`.** A `runnertest` package now would
  export test helpers with one importer, and turn their signatures into an
  API to maintain.

## Decisions

Settled in review:

- **`TaskManager.Wait` in `gritz runner`.** `internal/command/runner.go`
  returns from `m.Run`, and its deferred `m.Close()` closes the backend while
  Run goroutines may still be unwinding. Calling `m.Wait` in between is a
  behaviour change, so it is a separate `fix:` PR, not part of this stack.
- **`eventoutbox_test.go` moves** to `runner_test` in slice 7. It only uses
  the exported `NewRunnerEventOutbox`. After the migration, `internal_test.go`
  (`TestDoubling`) is the only test file left in `package runner`.
- **No `FailAlways`, no `FailNext` count** until a test needs one. Every
  failure today's tests inject is a single call.
- **`backendtest` comes after the migration**, as a follow-up outside this
  plan. The fake's state-table test is shaped so that suite can lift it.

## Open Questions

None.
