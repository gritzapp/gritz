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
- It does not import `driverserver`. A sandbox's driver is an `http.Handler`
  the test provides (see [Driver hookup](#driver-hookup)).
- It does not implement `backend.RunEnver`. The one test that needs it embeds
  `*fake.Backend` in a test type that adds `RunEnv`, as `runEnvBackend` embeds
  `*backend.BackendMock` today.
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
  `OpPark`, `OpStatus`, `OpDestroy`.

A fourth method, `Drop(id)`, closes every connection the runner has open to
the sandbox's driver without changing its state (a network blip). It replaces
`hs.CloseClientConnections()` in `TestTaskRunnerRun_Reattach`.

### Observations

`Snapshot()` returns a copy of the fake's state:

- `Sandboxes`: every sandbox the fake has held, in creation order, gone ones
  included. Each is an `Info` with its handle, task, state, the `Spec` it was
  created from (nil for `Add`), and how many times each operation took effect
  on it (`Starts`, `Parks`, `Destroys`, `Dials`). These counters replace
  `sb.StartCalls()`, `ParkCalls()`, `DestroyCalls()` and `DialCalls()`. A call
  that `FailNext` failed is not counted, and neither is a call that made no
  change (`Start` on an up sandbox, `Park` on a parked one).
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

`Options.Driver func(backend.Handle) http.Handler` gives the handler that
serves a sandbox's driver.v1 API. The fake calls it once per sandbox, on the
first `Dial`, and serves the result on an `httptest.Server` it owns. `Dial`
returns `driverv1connect.NewDriverServiceClient` over that server. In
`runner_test` the handler is a real `driverserver.Server`'s (`srv.Handler()`
on a mux), built by the same code as today's `newDriver`, so the fake never
imports `driverserver`. Wrapping the handler (a `ready` gate, a Connect
interceptor that records `RunRequest.Spec`) needs no fake support.

The fake's server makes the driver follow the sandbox's state:

- A request to a sandbox that is not up gets `503`, which Connect reports as
  `CodeUnavailable`, the same as a driver that is not answering. A nil
  `Driver`, or a `Driver` that returns nil, gives a sandbox whose driver never
  answers (`TestTaskRunnerRun_DriverUnreachable`).
- Moving a sandbox out of up (`Park`, `Destroy`, or `Set` to `Parked` or
  `Gone`) closes the runner's open connections to it, as stopping a container
  kills its driver. An attached `Run` stream drops, and `attach` then sees
  the new `Status`. This is what lets a test make a sandbox vanish under a run
  with one call.
- `Close` closes every driver server.

The in-process driver outlives the sandbox's state. Parking the fake does not
stop the run inside `driverserver`, and starting the fake again does not
restart it. A test that needs the run to end stops it through the driver, as
today (`stopRun`). Tests that start a run on the driver themselves
(`serveDriver` today) serve the same handler on their own `httptest.Server` as
well. That test-side client is not gated, because the test stands inside the
sandbox.

`Dial`'s JWT is sent as a bearer token. The test driver ignores it, as a
driver with no runner key does.

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
	// Driver returns the handler serving the driver.v1 API of the sandbox
	// with handle h; nil means its driver never answers.
	Driver func(h backend.Handle) http.Handler
}

// New returns a Backend with no sandboxes.
func New(opts Options) *Backend

// ValidateWorkspace accepts every workspace.
func (b *Backend) ValidateWorkspace(ws *workspace.Workspace) error

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
)

// String returns the op's name.
func (o Op) String() string

// Add puts a sandbox with id in state into the fake, as a previous runner process would have left it, and returns its handle.
func (b *Backend) Add(id string, state State) backend.Handle

// Set moves sandbox id to state without going through the runner.
func (b *Backend) Set(id string, state State)

// FailNext makes the next op call on sandbox id return err; id "" matches any sandbox.
func (b *Backend) FailNext(op Op, id string, err error)

// Drop closes every open connection to sandbox id's driver.
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
	// Starts, Parks, Destroys and Dials count the calls that took effect.
	Starts, Parks, Destroys, Dials int
}
```

**How it fits:** a `runner_test` test calls `fake.New` with a `Driver` built
from a real `driverserver`, and passes the result as
`TaskManagerOptions.Backend` or `TaskRunnerOptions.Backend`. It arranges
sandboxes with `Add`, `Set` and `FailNext`, drives the runner through its
exported API, and asserts on `Snapshot` and `Wait`, next to the
`taskstate.Store` and the `gritzclient.ClientMock` it already owns.

### Exported runner API the tests need

The rewrites below need three additions to `internal/runner`. The `TaskRunner`
and `TaskManager` surfaces stay as #1758 left them otherwise.

```go
package runner // github.com/gritzapp/gritz/internal/runner

// ErrRunning is returned by TaskRunner.Start when a Run still follows the task's previous run. // new: replaces errRunning
var ErrRunning = errors.New("sandbox is still running")

// Record returns the task's record as the TaskRunner holds it, and false if it has no sandbox. // new
func (tr *TaskRunner) Record() (taskstate.Record, bool)

// Wait blocks until every Run goroutine the manager started has returned, or ctx is done. // new
func (m *TaskManager) Wait(ctx context.Context) error
```

- **`ErrRunning`.** `TestTaskRunnerStart` and `TestTaskRunnerStart_Concurrent`
  assert that a second `Start` refuses with it. It is part of `Start`'s
  documented contract, and `Poll` already branches on it.
- **`TaskRunner.Record`.** `assertTracked` checks two things: the record in
  `taskstate/`, and the `TaskRunner`'s in-memory copy (`tr.rec`, `tr.sbx !=
  nil`). The store is the test's own, so the first needs nothing. The second is
  what the refactor's rule is about: "no sandbox" is now `tr.sbx == nil`, not a
  missing registry entry. In `TestTaskManagerHandleEvents_Fatal` the store's
  directory is removed, so a missing record proves nothing, and only the
  `TaskRunner` can show that the failed write left it with no sandbox. `Version()` cannot stand in,
  because a version-0 record (`TestTaskManagerLoad_VersionScopedBackstop`) and
  no sandbox both read as 0. `Record` is a read under `tr.mu`, like `Version`.
- **`TaskManager.Wait`.** "The Run is done" is what `waitIdle` and
  `waitReleased` wait for, and every negative assertion after a run needs it:
  `attach` parks the sandbox *before* `Run` sends its `TaskLostEvent`. So
  "sandbox parked, outbox empty" passes even when an event is about to arrive.
  The fake sees the park; only the manager sees the goroutine return. A
  `sync.WaitGroup` around `m.run`'s goroutine (`Done` deferred, so it runs
  after `Release` and `Wake`, and on the shutdown path that skips them) gives
  that without exposing `m.sem`. It is also the missing step in
  `gritz runner`'s shutdown, where the deferred `m.Close()` closes the backend
  while Run goroutines may still be unwinding (see
  [Open Questions](#open-questions)).

Calling `TaskManager.Start` directly without holding a slot, as the
rewritten tests do, is harmless: `safesem.Release` floors at zero. So
`startTask`'s `TryAcquire` goes away. Tests about admission go through `Poll`.

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
| `tr.sbx`, `tr.rec` (`track`, `assertTracked`, Restore and Fatal tests) | ~25 tests | the store, plus `TaskRunner.Record()` | `Record` |
| `m.tasks` (`registered`, Prune's key list) | Start_Gone, Prune, Run | `m.Runner(id)` pointer identity: the same pointer is kept, a new one was dropped. `TestTaskManagerRun` waits on `Snapshot().Destroyed` instead of polling the registry | — |
| `tr.running` (`claim`) | StartStillRunning, Prune, Remove_Running, `runAsync` | `tr.Restore` on an up sandbox claims, as `Load` does. "Claimed but not up" is `Restore` on `Up`, then `be.Set(id, fake.Parked)` | — |
| `tr.running` (`waitIdle`) | Run_Finished, Run_Shutdown, RestartReattaches | a synchronous `tr.Run` returning is idle already (the `running` reset is deferred inside `Run`). A manager's Run goroutines use `m.Wait`. "Claimed" as an assertion (Restore test) is probed with `Start` returning `ErrRunning` | `Wait`, `ErrRunning` |
| `m.sem` (`startTask`, `waitReleased`) | ~10 tests | `m.Start` directly (`Release` floors at zero), then `m.Wait` | `Wait` |
| `m.sem` (Load's seeding check, StartStillRunning's "slot is free") | Load, StartStillRunning | behaviour, through `Poll` at `Concurrency: 1`: a pending START for a second task gets no `Create` while the slot is held, and gets one once it is free | — |
| `m.spec` | Spec_PreCreatesLogDir, Spec_Secrets | `Snapshot().Specs` after `m.Start`; the real Docker backend is no longer needed | — |
| `m.runSpec` | RunSpec_RunEnv | the `RunRequest.Spec` the driver receives, recorded by a Connect interceptor on the test's driver handler; `RunEnv` from a test type embedding `*fake.Backend` | — |
| `m.run` | Remove_Running | `Load` of a record whose fake sandbox is up | — |
| `tr.send` | HandleEvents_Lost, Send_Shutdown | a real lost run (`be.Set(id, fake.Gone)` under an attached run). For Send_Shutdown, a `NewTaskRunner` whose `Events` nobody reads, and a ctx cancelled while `Run` is blocked sending | — |
| `m.events` | Restore test's `NewTaskRunner` | a channel the test makes | — |
| `errRunning` | TaskRunnerStart, Start_Concurrent | `ErrRunning` | `ErrRunning` |
| `errRemoved` | none directly | only the lost reason string is asserted | — |
| `tr.opts` (defaults), `doubling` | TestNewTaskRunner, TestDoubling | keep a small `internal_test.go` in `package runner` with these two tests only; the panics half of TestNewTaskRunner moves out | — |
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
fake and the three additions above. They assume these helpers in
`helpers_test.go`, ported from today's:

- `testStore(t, recs...)`, `submitted`, `waitQueued`: unchanged.
- `testManager(t, be, store, tune...)`: unchanged, but with `runner.` prefixes.
- `driverHandler(t, version, ready) http.Handler`: today's `newDriver`,
  returning the mux instead of serving it.
- `serveDriver(t, taskID, version, cfg) (http.Handler, driverv1connect.DriverServiceClient)`:
  as today, but it also returns the handler for `fake.Options.Driver`. The
  client is the test's own, over its own `httptest.Server`.
- `drivers(h) func(backend.Handle) http.Handler`: every sandbox gets driver
  `h`.

`assertTracked` itself becomes:

```go
// assertTracked asserts that the task has a record and its TaskRunner holds
// that record, or, with want nil, that it has neither.
func assertTracked(t *testing.T, m *runner.TaskManager, store *taskstate.Store, taskID int64, want *taskstate.Record) {
	t.Helper()
	rec, ok, err := store.Read(taskID)
	assert.NilError(t, err)
	held, has := m.Runner(taskID).Record()
	if want == nil {
		assert.Assert(t, !ok, "task %d has a record", taskID)
		assert.Assert(t, !has, "task %d has a sandbox", taskID)
		return
	}
	assert.Assert(t, ok, "no record for task %d", taskID)
	assert.DeepEqual(t, rec, *want)
	assert.Assert(t, has, "task %d has no sandbox", taskID)
	assert.DeepEqual(t, held, *want)
}
```

`m.Runner` registers a `TaskRunner` for a task that has none. The old helper
told "no `TaskRunner`" apart from "a `TaskRunner` with no sandbox". #1758 made
that difference meaningless, so the two cases merge here.

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
	assertTracked(t, m, store, 1, &taskstate.Record{TaskID: 1, Version: 1, Type: "fake", ID: "c1"})
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Created)
	assert.Equal(t, sbx.Dials, 0)

	// Act - the task's sandbox is removed.
	assert.NilError(t, m.Runner(1).Destroy(t.Context()))

	// Assert
	assert.DeepEqual(t, be.Snapshot().Destroyed, []string{"c1"})
	assertTracked(t, m, store, 1, nil)
}
```

Replaced: `testSandbox(nil)` + `StartFunc`, `BackendMock.CreateFunc`,
`startTask` (the slot), `sb.DialCalls()`, `sb.DestroyCalls()`, and the private
`assertTracked`.

#### `waitIdle`: a runner restart mid-run

```go
// TestTaskManagerLoad_RestartReattaches covers a runner restart mid-run: the
// first runner process shuts down while it follows the run, and the next one
// reattaches to it from the record in Load.
func TestTaskManagerLoad_RestartReattaches(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going on an up sandbox, recorded at its
	// version, followed by a runner that then shuts down.
	h, client := serveDriver(t, 1, 3, sleepForever)
	be := fake.New(fake.Options{Driver: drivers(h)})
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
		wantClaimed bool
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
			got, ok, err := store.Read(rec.TaskID)
			assert.NilError(t, err)
			held, has := tr.Record()
			if tt.wantSandbox {
				assert.Assert(t, ok, "record was deleted")
				assert.DeepEqual(t, got, rec)
				assert.Assert(t, has, "TaskRunner has no sandbox")
				assert.DeepEqual(t, held, rec)
			} else {
				assert.Assert(t, !ok, "record was kept")
				assert.Assert(t, !has, "TaskRunner kept a sandbox")
			}
			// Restore never destroys, even a gone sandbox.
			assert.Assert(t, cmp.Len(be.Snapshot().Destroyed, 0))
			// A claimed TaskRunner refuses the next Start.
			err = tr.Start(t.Context(), backend.Spec{TaskID: 1, Version: 4})
			assert.Equal(t, errors.Is(err, runner.ErrRunning), tt.wantClaimed)
		})
	}
}
```

The `gone` row and the new `never existed` row are the gone-sandbox restore:
a sandbox that vanished since the record was written, and a record whose ID
the platform never knew. Both give `StatusGone` through the stale-handle rule
with no mock. Before, each was a `StatusFunc` returning the constant. The
claim is probed through `Start`, which only acts at the end of the test. The
"claimed" (`tr.running`) and "has a sandbox" (`tr.sbx`) assertions no longer
read fields.

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
	be := fake.New(fake.Options{Driver: drivers(driverHandler(t, 3, nil))})
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
	assertTracked(t, m, store, 8, nil)
	assert.Equal(t, m.Runner(8), tr)

	// Act - the task is started again.
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
	assertTracked(t, m, store, 8, &taskstate.Record{TaskID: 8, Version: 3, Type: "fake", ID: "c2"})
	// The runner never destroyed the vanished sandbox; it only dropped it.
	assert.Assert(t, cmp.Len(snap.Destroyed, 0))
}
```

Before, this test needed two mocks wired differently (`OpenFunc` returning
`gone`, `CreateFunc` returning `fresh`) and a `StartFunc` returning `ErrGone`.
Now it needs one `Set`. The fresh sandbox's ID (`c2`, not a second `c1`) and
the empty `Destroyed` are behaviour the mocks could not express.

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
4. **Runner exports** — Delivers: `ErrRunning` (renamed from `errRunning`),
   `TaskRunner.Record`, and `TaskManager.Wait` with its WaitGroup. Depends on:
   nothing (parallel to 1–3). Verifiable by: the existing internal tests,
   updated for the rename, plus internal tests for `Record` after
   Start/Destroy and for `Wait` returning once a run finishes.
5. **TaskRunner tests to `runner_test`** — Delivers: `helpers_test.go`
   (`package runner_test`), `taskrunner_test.go` moved and rewritten on the
   fake, and `internal_test.go` keeping `TestDoubling` and the defaults half of
   `TestNewTaskRunner`. Depends on: (2), (3), (4). Verifiable by: `mise run
   test -- ./internal/runner/...`, and renaming `tr.sbx` locally to confirm
   the moved file still compiles.
6. **TaskManager start/stop tests** — Delivers: the Start, Poll, Kill,
   Remove and Spec tests from `taskmanager_test.go` moved to `runner_test` on
   the fake, the Spec tests without the Docker backend. Depends on: (5).
   Verifiable by: the runner test suite.
7. **TaskManager load/prune/events tests** — Delivers: the Load, Prune, Run,
   HandleEvents, Die and outbox tests moved. `taskmanager_test.go` no longer
   exists in `package runner`. Depends on: (6). Verifiable by: the runner test
   suite, and `grep -L 'package runner$' internal/runner/*_test.go` leaving
   only `internal_test.go` and `eventoutbox_test.go` (if that one stays
   internal).
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
- **`http.Handler` instead of a dial func or a client.** A dial func
  (`func(h) (DriverServiceClient, error)`) would hand the fake a client it
  cannot gate on state, so a parked fake sandbox's driver would keep answering
  and a gone sandbox could not drop its streams. Owning the server costs the
  fake an `httptest.Server` per dialed sandbox, and gives the "a stopped
  sandbox's driver is unreachable" behaviour the runner's `attach` logic is
  built around.
- **No `Before`/hook API.** A hook run before each op (for "the record is
  written before `Start`" assertions, or blocking `Create`) is more general
  than `FailNext`, but it brings back per-test closures. A record that survives
  a `FailNext(OpStart)` already shows the write came first, and
  `TestTaskRunnerStart_Concurrent`'s 10ms `Create` sleep is not needed:
  `tr.mu` serializes the two Starts either way.
- **`TaskManager.Wait` over a `Claimed()` accessor.** `TaskRunner.Claimed()`
  would expose the claim flag and still need polling to wait on it. `Wait`
  is a blocking primitive with a production use.
- **`m.Runner` in `assertTracked`.** It registers a `TaskRunner` for an
  unknown task. A `TaskManager.Registered(id)` accessor would avoid that, but
  would expose the registry purely for tests. Since #1758, "no `TaskRunner`"
  and "no sandbox" mean the same thing to every caller.
- **Helpers stay in `helpers_test.go`.** A `runnertest` package now would
  export test helpers with one importer, and turn their signatures into an
  API to maintain.

## Open Questions

- **Wire `TaskManager.Wait` into `gritz runner`?** `internal/command/runner.go`
  returns from `m.Run` and its deferred `m.Close()` closes the backend while
  Run goroutines may still be unwinding. Calling `m.Wait` with a short
  timeout in between would make shutdown ordered. That is a behaviour change,
  so it would be its own `fix:` PR, not part of this stack.
- **Keep `eventoutbox_test.go` internal?** It only uses exported
  `NewRunnerEventOutbox`, so it can move with slice 7 or stay. Proposed:
  move it, so the only internal test file is `internal_test.go`.
- **Persistent failures.** `FailNext` fails one call. No test today needs "every
  `Status` on c7 fails", but Prune retries would. Add `FailAlways(op, id,
  err)` when a test needs it, or make `FailNext` take a count?
- **`backendtest` timing.** The fake's state-table test is shaped to be
  lifted into a conformance suite. Should that follow right after slice 3, to
  catch drift between the fake and Docker early, or wait until after the
  migration?
