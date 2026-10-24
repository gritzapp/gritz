# TaskRunner owns its sandbox lifecycle

Issue: https://github.com/gritzapp/gritz/issues/1758

## Problem

`TaskManager` (`internal/runner/taskmanager.go`) and `TaskRunner`
(`internal/runner/taskrunner.go`) are tied together by one rule: **a task has a
`TaskRunner` if and only if it has a `taskstate/` record**. Most of the coupling
listed in #1758 exists only to keep that rule true:

- The sandbox comes before the `TaskRunner`. `TaskManager.create` calls
  `backend.Create` outside any per-task lock. It relies on `Poll` handling each
  task at most once per pass, and only then builds the `TaskRunner`
  (`newTaskRunner` writes the record) and registers it.
- Removing a sandbox removes the `TaskRunner` too. `TaskRunner.remove` deletes
  the record and then calls back into the manager through `taskEnv.forget`.
  Every other operation then has to check `removed`, because a caller may still
  hold a pointer to a `TaskRunner` that is no longer registered.
- Because a missing `TaskRunner` means "no sandbox", every manager entry point
  (`Running`, `Kill`, `Remove`, `version`, `Start`) has its own `tr == nil`
  branch. `Start` also calls `idle()` first, so it does not mint a token for a
  task that is still running.
- Reattach skips `Start`, so `Load` uses `claim()` to mark the run, builds the
  `TaskRunner` with `loadTaskRunner`, and drops gone sandboxes with `dropGone()`.
- `taskEnv` has two callbacks into the manager (`die`, `forget`) and the
  manager's outbox (`queue`), next to plain dependencies.

#1757 (driver WAL, layer 6) adds `taskstate.Record.Seq`, which `attach` writes
during the run. Under today's rule the record, and the object that writes it,
can disappear in the middle of an attach. This proposal changes the rule first,
so `Seq` can be added cleanly afterwards.

#1758's "Possible cuts" are superseded by the decisions below.

## Design

### The new rule

A `TaskRunner` is the runner's object for one **task**, not for one sandbox. It
can exist before its sandbox and can outlive it. While it exists it owns the
whole sandbox lifecycle: create, record, version stamp, start, destroy, and
clearing a sandbox that is gone. The registry holds one `TaskRunner` per task the
runner has seen. Only `Prune` removes an entry, and only once the task is
archived or deleted.

| | Today | Proposed |
|---|---|---|
| Registry entry exists | iff a record exists | from the first command for the task until `Prune` drops it |
| Who calls `backend.Create` | `TaskManager.create`, with no lock | `TaskRunner.Start`, under `tr.mu` |
| Destroy / sandbox gone | delete the record, `forget` the `TaskRunner`, set `removed` | delete the record and set `sb = nil`; the `TaskRunner` stays |
| "No sandbox" | `m.tasks[id] == nil` | `tr.sb == nil` |
| Reporting to the manager | `env.die`, `env.queue.Enqueue` | an events channel of sealed `TaskEvent` types, drained by `HandleEvents` |
| Prune | its own goroutine; archived check under `tr.mu` | between `Poll` passes in `TaskManager.Run`; archived check outside the lock |

### TaskRunner state

```go
type TaskRunner struct {
	opts TaskRunnerOptions

	// mu serializes every method. Run holds it only to claim, to advance
	// Seq, and to release.
	mu sync.Mutex
	// sb is the task's sandbox, or nil when there is none. Guarded by mu.
	sb backend.Sandbox
	// rec is sb's record as last written to taskstate/; zero when sb is nil.
	rec taskstate.Record
	// running is set by Start or Restore (the claim) until Run returns.
	running bool
	// cancel cancels the Run in progress, or is nil.
	cancel context.CancelCauseFunc
}
```

`sb` and `rec` change together, under `mu`, and only in these places:

- **set**: by `Start` (after `backend.Create` and the record write) and by
  `Restore`;
- **cleared** (`clear()`: `store.Remove`, then `sb = nil`, `rec = {}`): by
  `Destroy`, `Retire`, `Restore` (when the handle cannot be opened or the
  sandbox is gone), and `Start` (when it finds the sandbox gone).

`clear` always clears the in-memory state. A failed `store.Remove` is only
logged: what is left on disk is the record of a sandbox that is gone or
destroyed. The next `Start` overwrites it, and if there is no next `Start`,
`Load` drops it at the next boot when it finds the sandbox gone.

`Run` never sets or clears the sandbox. The only record field it writes is
`Seq` (see [Driver WAL](#driver-wal-1757)).

`removed`, `forget`, `idle`, `claim`, `dropGone`, `destroyIf`, `newTaskRunner`,
`loadTaskRunner`, `taskEnv`, and the manager's `create`, `lookup`, `register`
and `forget` are deleted.

### Start

`Start(ctx, spec backend.Spec)` runs entirely under `tr.mu`:

1. If a claimed run is still active, `Start` returns `ErrRunning` without
   doing anything.
2. **Existing sandbox:** stamp the record with `spec.Version`, keeping `Seq`
   (written only if it changed, as today). Then `sb.Start`. If that returns
   `backend.ErrGone`, `clear()` and return `backend.ErrGone`. Any other error
   is returned, and the sandbox and record are kept so `Destroy` can still
   remove them. If `sb.Start` succeeds, go to step 4.
3. **No sandbox:** `backend.Create(ctx, &spec)`. If it fails, nothing exists
   (that is `Create`'s contract). Otherwise write the record
   `{handle, spec.Version, Seq: 0}`. If that write fails, send a
   `TaskFatalEvent`, destroy the new sandbox on a best-effort basis (on
   `context.WithoutCancel(ctx)`, which today's code leaks), and return. Then
   `sb.Start`. An `ErrGone` here is also cleared and returned.
4. `running = true`: this claims the `TaskRunner` for the `Run` the manager
   starts next.

**Decision: `Start` on a gone sandbox fails, as it does today.** It clears the
sandbox and returns `backend.ErrGone`. The manager's existing error path gives
back the slot and reports `failed` with `"sandbox is gone"`. The next start
finds no sandbox and creates a fresh one. The only difference from today is
what makes that next start create: before, it was the missing `TaskRunner`;
now it is the `TaskRunner`'s nil `sb`. A task never moves to a fresh
workspace without the user being told.

### Restore (reattach at Load)

`Restore(ctx, rec)` adopts a record a previous process wrote. It is for a
`TaskRunner` with no sandbox, and it claims the run in the same lock
acquisition that installs the sandbox:

1. `backend.Open(handle)`. On error: `store.Remove(rec.TaskID)`, return the
   error.
2. Set `sb` and `rec`, including `rec.Seq`. Then `sb.Status`. On error, keep
   both and return the error. The sandbox may still be there, so nothing is
   claimed and nothing is cleared, as today.
3. `StatusGone`: `clear()`. `StatusUp`: `running = true`. Return the status.

`Load` becomes the following. It requires `HandleEvents` to be running
already (see [Events](#events)):

```go
for _, rec := range recs {
	tr := m.taskRunner(rec.TaskID) // get-or-create
	st, err := tr.Restore(ctx, rec)
	if err != nil { log; continue }
	switch st {
	case backend.StatusUp:
		m.run(ctx, tr, nil) // Run reattaches to rec.Version from rec.Seq
		running++
	case backend.StatusParked, backend.StatusGone:
		m.failIfTaskRunning(ctx, rec.TaskID, rec.Version)
	}
}
```

The claim happens inside `Restore` rather than through a separate exported
`Claim`, so it is atomic with the status check. Like `Start`, `Restore` reports
whether there is a run for `Run` to follow. A start-then-claim pair never
exists where a `Start` could squeeze in between.

### Run

`Run(ctx, spec *driverv1.RunSpec)` must follow a successful `Start`, or a
`Restore` that returned `StatusUp`. Without that claim it panics. It locks once
to capture `sb` and `rec.Version` and to install `cancel`, and once more at the
end to release `running`. In between it follows the run on the captured `sb`,
exactly as `run`/`startRun`/`attach` do today. The differences:

- If `sb` is nil when `Run` captures it, `Destroy` ran between the claim and
  `Run`. That is reported lost (`"sandbox removed before the run finished"`).
  It replaces today's `removed` check.
- A lost run is sent as `TaskLostEvent{TaskID, Version, Reason}` instead of
  being enqueued directly. The send selects on `Run`'s `ctx` parameter (the
  runner's root context), not on the run context that `Destroy` cancels. It
  happens before `running` is released, so the event is never sent after a new
  claim.
- **Decision: `attach` does not clear a gone sandbox.** It reports the run lost
  and leaves the record in place. If that `TaskLostEvent` is dropped at
  shutdown, the record is what `Load`'s backstop needs at the next boot (see
  [Coverage of a dropped TaskLostEvent](#coverage-of-a-dropped-tasklostevent)).
  The gone sandbox is cleared later by whichever comes first: the next `Start`
  (which fails with `ErrGone`), `Restore` at boot, or `Retire`. Clearing a
  gone sandbox always goes with a report: `Start`'s `ErrGone` becomes the
  manager's `failed`, `Restore`'s goes with the backstop, and `Retire`'s task
  is archived.

### Kill, Running, Version, Destroy

- `Kill` and `Running` return `false, nil` when there is no sandbox. They only
  query and never clear.
- `Version()` returns `rec.Version`, or 0 when there is no sandbox. It no
  longer has an error. The restart idempotency guard in `Poll` uses it as
  today.
- `Destroy` returns nil when there is no sandbox. Otherwise it calls
  `sb.Destroy`, cancels a `Run` with `errRemoved`, and calls `clear()`. If
  `sb.Destroy` fails, the sandbox and record are kept and the error is
  returned.

### Retire and Prune

`Retire(ctx) (bool, error)` destroys the sandbox if the `TaskRunner` is idle.
Under `tr.mu`:

- If a run is claimed, or `sb.Status` reports `StatusUp`, it returns `false`.
- Otherwise it destroys the sandbox if there is one, calls `clear()`, and
  returns `true`. A `TaskRunner` with no sandbox and no run just returns
  `true`.

`Prune` no longer runs on its own goroutine. `TaskManager.Run` (below) calls
it between `Poll` passes. `Poll` waits for every handler it starts
(`g.Wait()`), so between passes no handler holds a `*TaskRunner`. The only
other holders are the `Run` goroutines, which hold one only while a run is
claimed, and then `Retire` refuses. So `prune` can delete the registry entry
right after `Retire` reports `true`:

```go
func (m *TaskManager) prune(ctx context.Context, taskID int64, tr *TaskRunner) {
	if stale, err := m.stale(ctx, taskID); err != nil || !stale { // GetTask: archived or NotFound
		return
	}
	if ok, err := tr.Retire(ctx); ok {
		m.mu.Lock()
		delete(m.tasks, taskID)
		m.mu.Unlock()
	}
}
```

Prune doesn't need a terminal state on the `TaskRunner`. No `Start` can reach
a `TaskRunner` once it is dropped: the next pass's get-or-create builds a
fresh one. Today's race, where a `Start` waiting for the lock of a pruned
`TaskRunner` gets `errRemoved` and reports `failed`, cannot happen.

**Decision: the archived check runs outside the TaskRunner's lock.** Holding
the lock across `GetTask` never stopped the task from being unarchived after
the check. What it actually guaranteed was that no run started between the
check and the destroy. With Prune serialized with Poll, no `Start` runs during
`prune` at all, and `Retire` still refuses a claimed run or an up sandbox.

Prune now calls `GetTask` for every registered `TaskRunner`, including those
with no sandbox. Today it only does so for sandboxes that are not up. The
extra calls are bounded by the concurrency limit (up sandboxes) plus the
`TaskRunner`s with no sandbox, which auto-archive keeps small.

### TaskManager.Run

`Run(ctx)` takes over the poll loop from `internal/command/runner.go`. It
polls, runs `Prune` if the prune ticker (`PollInterval`, the interval of
today's autoprune goroutine) has fired since the last pass, and then waits for
`WakeC`, `PollInterval` or `ctx`:

```go
func (m *TaskManager) Run(ctx context.Context) error {
	prune := time.NewTicker(m.pollInterval)
	defer prune.Stop()
	for {
		if err := m.Poll(ctx); err != nil {
			m.log.Error("failed to poll tasks", "err", err)
		}
		select {
		case <-prune.C:
			if err := m.Prune(ctx); err != nil {
				m.log.Error("failed to prune sandboxes", "err", err)
			}
		default:
		}
		select {
		case <-m.wake:
		case <-time.After(m.pollInterval):
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}
```

`PollInterval` moves into `TaskManagerOptions`. The command keeps its
`--poll` flag and passes the value through. The command still matches the
returned cause against `FatalError` to choose its exit code, and `Fatal`
stays in `TaskManagerOptions` unchanged.

A slow Prune delays the next poll. It calls `GetTask` for every registered
`TaskRunner`, and `sb.Destroy` for each one it retires. While it runs, new
commands, and the wakes from released slots, wait. `wakeup.Chan` coalesces
wakes, so none is lost, only delayed. This is the price of dropping the
terminal state, and it is bounded by one prune pass per `PollInterval`.

### Registry and the manager

- `m.taskRunner(taskID)` does get-or-create under `m.mu`: it copies
  `m.opts` (the `TaskRunnerOptions` template), sets `TaskID`, and calls
  `NewTaskRunner`. `Start`, `Kill`, `Running`, `Remove`, the restart guard and
  `Load` all use it. None of them has a `tr == nil` branch.
- `m.Start(ctx, task)`: `tr := m.taskRunner(task.ID)`, `spec := m.spec(...)`,
  `tr.Start(ctx, *spec)`, then `m.run(ctx, tr, m.runSpec(spec))`. The `idle`
  pre-check is gone. Minting a token for a task that turns out to be running
  is accepted.
- `NewTaskManager` builds the template from `opts.Backend`, `opts.Store`,
  `m.events` and `log`. It leaves the durations zero, so `NewTaskRunner`
  applies its defaults. Tests set `m.opts.LostTimeout` and the others before
  the first `TaskRunner` is created, instead of reaching into `m.env`.

### Events

```go
type TaskEvent interface{ taskEvent() }
type TaskFatalEvent struct{ TaskID int64; Err error }
type TaskLostEvent struct{ TaskID, Version int64; Reason string }
```

Only this package can implement `TaskEvent`, so the manager's type switch is
exhaustive. A `TaskRunner` sends with:

```go
select {
case tr.opts.Events <- ev:
case <-ctx.Done():
	tr.log.Warn("task event dropped at shutdown", "task", ..., "event", ev)
}
```

`HandleEvents(ctx)` drains the channel until `ctx` is done.
`TaskFatalEvent` → `m.die(ev.Err)`. `TaskLostEvent` →
`m.queue.Enqueue(RunnerEvent{TaskID, Version, Failed, Reason})`, which calls
`m.die` if it fails. It never touches a `TaskRunner`, so sends made while
holding `tr.mu` (from `Start`, and from the `Seq` write) cannot deadlock.

**Decision: the event loop is the exported `HandleEvents(ctx)`, and the
command starts it before `Load`.** The command runs
`go m.HandleEvents(ctx)`, then `m.Load(ctx)`, then `m.Run(ctx)`, all on the
runner's root context. Since the loop and every sender use that same context,
they stop together: no sender can block on a loop that has stopped. `Load`'s
Run goroutines are the first possible senders, so the doc comments of both
`HandleEvents` and `Load` say that `HandleEvents` must be running before
`Load` is called. Starting the loop in the command keeps `Load` a pure
rehydration step and keeps the manager's goroutines visible where the
command's other goroutines (`queue.Run`, the notification client) are
started. The test helper starts `HandleEvents` too.

**Decision: the channel is unbuffered.** Events are rare (a lost run, a dead
disk), and the handler is one `Enqueue` or one `cancel`. A buffer would save
the sender almost nothing. It would also mean events could sit in the buffer
when shutdown arrives, lost without the sender knowing. With no buffer, a send
either reaches the handler or the sender sees `ctx.Done()` and logs the drop.

#### Coverage of a dropped TaskLostEvent

A dropped event matters only for a run whose `failed` was never enqueued. `Run`
never clears the record, so at the next boot `Load` sees that sandbox's record
again:

- **Sandbox up** (driver gone or `FailedPrecondition`/`Aborted`): `Restore`
  claims it and `Run` reattaches to `rec.Version`. It reaches the same
  conclusion again and sends the event again. Covered.
- **Sandbox parked or gone** (every other lost path parks or finds it gone),
  **and the task is still `RUNNING` on `rec.Version`**: `failIfTaskRunning`
  enqueues `failed(rec.Version)`. Covered.
- **Run lost before the driver reported `started`** (a `startRun` failure):
  the task is `PENDING` or `RESTARTING`, not `RUNNING`, so the backstop skips
  it. A pending `START` recovers: the next poll finds the sandbox not running
  and starts it again. A `RESTART` hits the restart guard
  (`Version() == task.Version`) and waits for a `started` that never comes.
  Today's code has the same gap when the runner stops between the version
  stamp and `started`. It is tracked in #1763 and is not part of this
  refactor.
- **Sandbox removed** (`Destroy` cancelled the run): the record is gone on
  purpose. Only `Prune` and `Remove` destroy, and only for archived or deleted
  tasks. Not covered, and it doesn't need to be.

A dropped `TaskFatalEvent` only happens when the runner is already stopping.
The runner then exits 0 instead of with the `FatalError`, and the broken disk
fails the next write after the restart.

### Driver WAL (#1757)

With this design, #1757's layer 6 changes only `TaskRunner`:

- `attach` sends `rec.Seq` as `after_seq`, and advances it through
  `tr.advance(sb, seq)`. That locks `tr.mu` and writes `rec.Seq` **only if
  `tr.sb == sb`**, so a sandbox that was destroyed (or replaced) during the
  attach does not get its record back. A failed write sends a
  `TaskFatalEvent`. `Trim` is called after the write, outside the lock.
- The lock is short: a claimed `TaskRunner` refuses `Start` right away, so
  only `Kill`, `Destroy`, `Running` and `Retire` compete with the `Seq` write.
- `Start` keeps `Seq` when it restarts an existing sandbox, because the WAL
  index stays monotonic across that sandbox's runs. It is 0 for a new sandbox
  (a new WAL). `Restore` reads it from the record.

### API

```go
package runner // github.com/gritzapp/gritz/internal/runner

// TaskRunnerOptions configures a TaskRunner. // new
type TaskRunnerOptions struct {
	// TaskID is the task the TaskRunner runs.
	TaskID int64
	// Backend creates and opens the task's sandbox. Required.
	Backend backend.Backend
	// Store holds the task's record. Required.
	Store *taskstate.Store
	// Events receives the TaskRunner's events. Required.
	Events chan<- TaskEvent
	// Log is the logger; defaults to slog.Default().
	Log *slog.Logger
	// RetryInterval is the first pause between driver attempts; defaults to 250ms.
	RetryInterval time.Duration
	// RetryMaxInterval caps the reattach backoff; defaults to 10s.
	RetryMaxInterval time.Duration
	// LostTimeout is how long Run reattaches without a good message; defaults to 2m.
	LostTimeout time.Duration
	// ReadyTimeout is how long a started driver has to accept its run; defaults to 1m.
	ReadyTimeout time.Duration
}

// TaskRunner owns one task's sandbox, its record and the run followed on it. // changed: may have no sandbox
type TaskRunner struct{ ... }

// NewTaskRunner returns a TaskRunner with no sandbox; it panics if a required option is missing. // new
func NewTaskRunner(opts TaskRunnerOptions) *TaskRunner

// Start creates the sandbox if there is none, stamps its record and starts it, claiming tr for Run. // changed: was Start(ctx, version)
func (tr *TaskRunner) Start(ctx context.Context, spec backend.Spec) error

// Restore adopts a record from a previous process and reports its sandbox's status, claiming tr for Run if it is up. // new
func (tr *TaskRunner) Restore(ctx context.Context, rec taskstate.Record) (backend.Status, error)

// Run follows the claimed run, starting it with spec or reattaching if spec is nil. // changed: requires a claim, reports through Events
func (tr *TaskRunner) Run(ctx context.Context, spec *driverv1.RunSpec) error

// Kill asks the driver to stop the recorded run and reports whether it stopped one. // changed: false without a sandbox
func (tr *TaskRunner) Kill(ctx context.Context) (bool, error)

// Running reports whether the sandbox is up. // changed: false without a sandbox
func (tr *TaskRunner) Running(ctx context.Context) (bool, error)

// Version returns the version the record is stamped with, or 0 without a sandbox. // changed: no error
func (tr *TaskRunner) Version() int64

// Destroy destroys the sandbox and deletes its record, keeping tr. // changed: tr stays usable
func (tr *TaskRunner) Destroy(ctx context.Context) error

// Retire destroys the sandbox if tr is idle, reporting whether tr is now idle with no sandbox. // new
func (tr *TaskRunner) Retire(ctx context.Context) (bool, error)

// ErrRunning is returned by Start while a Run follows the previous run. // changed: was errRunning
var ErrRunning = errors.New("sandbox is still running")

// TaskEvent is something a TaskRunner reports to its manager. // new
type TaskEvent interface{ taskEvent() }

// TaskFatalEvent reports a durable write failure that must terminate the runner. // new
type TaskFatalEvent struct {
	TaskID int64
	Err    error
}

// TaskLostEvent reports a run whose outcome the driver did not report. // new
type TaskLostEvent struct {
	TaskID  int64
	Version int64
	Reason  string
}

type TaskManagerOptions struct {
	// ... existing fields, Fatal unchanged.

	// PollInterval is the fallback pause between Poll passes and the Prune interval. // new
	PollInterval time.Duration
}

// HandleEvents handles the TaskRunners' events until ctx is done; start it before Load. // new
func (m *TaskManager) HandleEvents(ctx context.Context)

// Load rehydrates the registry from taskstate/; HandleEvents must already be running. // changed: doc states the ordering
func (m *TaskManager) Load(ctx context.Context) error

// Run polls until ctx is done, pruning between passes, and returns ctx's cause. // new: was the command's poll loop
func (m *TaskManager) Run(ctx context.Context) error
```

Removed (all unexported): `taskEnv`, `newTaskRunner`, `loadTaskRunner`,
`errRunning`, `TaskRunner.{claim,idle,dropGone,destroyIf,remove,gone,lock}`,
`TaskManager.{env,create,lookup,register,forget}`.

```go
package taskstate // github.com/gritzapp/gritz/internal/runner/taskstate

type Record struct {
	// ... existing fields.

	// Seq is the last WAL seq the runner has made durable. // new in #1757 layer 6, written only by TaskRunner
	Seq uint64 `json:"seq,omitempty"`
}
```

**How it fits:** `NewTaskManager` builds a `TaskRunnerOptions` template that
points `Events` at its unbuffered `events` channel. The `gritz runner` command
starts `go m.HandleEvents(ctx)`, then calls `Load`, which calls `Restore` on a
get-or-created `TaskRunner` for each record, and then calls `m.Run(ctx)`
instead of its own loop and autoprune goroutine. `Run` alternates `Poll` and
`Prune`. `Poll`'s handlers get-or-create, build the `backend.Spec`, and call
`Start`, then `m.run` calls `TaskRunner.Run` on its own goroutine, which holds
the concurrency slot. `Prune` checks the server, calls `Retire`, and drops the
entry. `TaskRunner`s talk back only through `TaskFatalEvent` and
`TaskLostEvent`, which `HandleEvents` turns into `die` or an outbox `failed`.

### Visualization

`diagrams/src/projects/proposals/taskrunner-owns-sandbox.ts`, on a new layout
(`diagrams/src/layouts/taskrunner.tsx`) showing the server, the runner's
`TaskManager` (registry, events channel, outbox), a `TaskRunner` with its
`taskstate/` record, and the sandbox. View it with `mise run motioncanvas`.

- **`taskrunner-start`**: a `START` creates the registry entry, the manager
  builds the spec, and `Start` creates the sandbox, writes the record and
  claims the run under its lock. `Run` then starts the run.
- **`taskrunner-gone`**: the sandbox vanishes mid-run. `attach` sends a
  `TaskLostEvent` that the manager turns into an outbox `failed`, and the
  record stays. The next `START` finds the sandbox gone, clears it and fails
  with `ErrGone`, and the start after that creates a fresh sandbox.
- **`taskrunner-restore`**: after a runner restart, the command starts
  `HandleEvents`, then `Load`'s `Restore` adopts the record and claims the run
  on an up sandbox. `Run` reattaches from `Seq`.
- **`taskrunner-prune`**: between two `Poll` passes, `Run` runs `Prune`. The
  archived check happens outside the lock, `Retire` destroys the parked
  sandbox and clears the record, and the registry drops the entry. The next
  pass creates a fresh `TaskRunner`.

## Implementation Plan

Each layer merges on its own and leaves the runner working. The existing
`taskmanager_test.go` and `taskrunner_test.go` keep passing at every layer,
with their helpers (`track`, `seed`, `testManager`) updated as they go.

1. **Task events** — Delivers: `TaskEvent`, `TaskFatalEvent`, `TaskLostEvent`;
   the manager's unbuffered `events` channel and exported `HandleEvents`, which
   the command starts before `Load` and `testManager` starts too;
   `TaskRunner.write` and `Run` send events instead of calling
   `env.die`/`env.queue`; `die` and `queue` leave `taskEnv`. Depends on:
   nothing. Verifiable by: tests that a lost run's `failed` reaches the outbox
   through the channel, that a failed record write sets the `FatalError` cause,
   and that a send on a cancelled ctx returns.
2. **TaskManager.Run** — Delivers: `TaskManager.Run(ctx)` with the poll loop
   moved from `internal/command/runner.go`; `TaskManagerOptions.PollInterval`;
   `Prune` runs on a ticker between `Poll` passes, and the autoprune goroutine
   goes. Prune's logic is unchanged here. Depends on: nothing. Verifiable by: a
   test that `Run` prunes an archived task's parked sandbox and returns the
   `FatalError` cause once `die` fires; running the runner against a local
   server.
3. **Restore** — Delivers: `TaskRunner.Restore`, used by `Load`; removes
   `loadTaskRunner`, `claim` and `dropGone`. Depends on: (1). Verifiable by:
   `TestTaskManagerLoad*` (up claims and reattaches, parked/gone backstop, gone
   and unopenable records dropped).
4. **Start creates the sandbox** — Delivers: nullable `sb`;
   `TaskRunner.Start(ctx, backend.Spec)` calls `backend.Create` and writes the
   record under `tr.mu`, destroying the sandbox if the write fails; get-or-create
   `m.taskRunner`; removes `m.create`, `newTaskRunner` and `idle`. `forget` and
   `removed` still exist. Depends on: (3). Verifiable by:
   `TestTaskManagerStart_*` and a test that two concurrent `Start`s on one task
   create one sandbox.
5. **TaskRunner outlives its sandbox** — Delivers: `clear()` replaces `remove`;
   `Destroy`, and `Start`'s `ErrGone`, keep the `TaskRunner`; `Retire` and the
   new `prune`, which drops the entry after `Retire`; removes `forget`,
   `removed`, `destroyIf`, `lookup` and every `tr == nil` branch. Depends on:
   (2), (4). Verifiable by: `TestTaskManagerRemovedTaskRunner` rewritten as
   "operations after Destroy see no sandbox"; `TestTaskManagerStart_Gone`
   (fails with `ErrGone`, then the next `Start` creates one new sandbox);
   `TestTaskManagerPrune` checking that the entry is dropped and the next
   `Start` gets a fresh `TaskRunner`.
6. **TaskRunnerOptions** — Delivers: exported `TaskRunnerOptions` and
   `NewTaskRunner` (panics, defaults, no side effects); the manager's `opts`
   template; removes `taskEnv`; tests tune timeouts through `m.opts`. Depends
   on: (5). Verifiable by: `NewTaskRunner` table test (panics per missing dep,
   defaults applied, no record written); the existing suites with `m.env`
   replaced.

#1757's layer 6 (`Record.Seq`, `tr.advance`) goes on top of (6).

## Trade-offs

**Start on a gone sandbox: fail vs. recreate.** Recreating in the same call
would make "gone" and "no sandbox" behave the same, and would save a second
start. But an idle task would then silently get a fresh workspace, and a
start that fails for an unrelated reason would hide the loss. Failing, as
today, tells the user explicitly that the workspace is gone, and the next
start creates a fresh one. The cost: a run already reported lost when its
sandbox vanished gets a second `failed` when the next start finds the sandbox
gone.

**attach clearing a gone sandbox.** Clearing at once would free the record
sooner, but it would remove exactly what `Load`'s backstop needs when the
`TaskLostEvent` is dropped. Keeping it costs nothing: whatever happens next
(`Start`, `Restore`, `Retire`) clears it.

**Serializing Prune with Poll vs. a terminal state.** With Prune on its own
goroutine, a `Poll` handler can hold a pointer to a `TaskRunner` that Prune
has just dropped. Its `Start` would then create a sandbox nothing tracks, so
the `TaskRunner` would need a write-once `retired` flag for `Start` to refuse
(an earlier draft of this proposal had one). Running Prune between passes
removes the race instead of guarding against it, and removes a state and an
error the manager would otherwise handle. The cost is that a slow Prune delays
the next poll.

**`HandleEvents` started by the command vs. by `Load` or `NewTaskManager`.**
Starting the loop inside `Load` would tie it to the senders' context without a
new method, but it would hide a goroutine inside a rehydration step.
`NewTaskManager` would need a context of its own and a `Close` to stop it. An
exported `HandleEvents` costs one ordering rule (before `Load`), which both doc
comments state and the command and test helper follow.

**Unbuffered vs. buffered events.** A buffer would let `Start` release `tr.mu`
a few microseconds sooner. It would also add a new way to lose events
(buffered at shutdown) without the sender seeing it.

**Callbacks vs. a channel.** That choice was settled. The channel makes the
direction explicit: `TaskRunner` → manager only, with a closed set of
messages. The cost is the "never take a `TaskRunner` lock in a handler" rule,
which a lone `HandleEvents` keeps easy to enforce.

**More `GetTask` calls in Prune.** Checking the server before `Retire`, for
every registered `TaskRunner`, costs a few extra calls each pass compared with
today's check of `Status` first. In return the server call no longer holds
`tr.mu`. Now that Prune runs between polls, these calls also add to how long
the next poll is delayed.

## Open Questions

- Should `TaskManager.Remove` stay? Only tests call it. Its tests could call
  `TaskRunner.Destroy` on `m.taskRunner(id)` directly.
- Should a `TaskRunner` with no sandbox that sees no command for a long time
  (a task that is never archived) be dropped earlier than `Prune`'s archived
  check? This proposal relies on auto-archive.
- Should Prune skip a pass, or cap the number of `GetTask` calls per pass, when
  the registry is large, to bound how long it delays the next poll?
