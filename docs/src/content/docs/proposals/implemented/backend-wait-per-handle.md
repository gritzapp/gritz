---
title: "Per-handle `Backend.Wait` (task-oriented exit observation)"
description: "Backends report sandbox exits through a fleet-wide Watch stream that the Lambda MicroVM API cannot support, so the runner never learns of driver exits, suspends, or reconciles."
---

Issue: https://github.com/icholy/gritz/issues/1089

## Problem

The runner backend observes sandbox exits through a **fleet-oriented** model: one
long-lived `Backend.Watch(ctx, func(HandleExit))` stream emits exits keyed by an
opaque handle *id*, and the runner reverse-resolves each id back to a task via
`taskstate.Store.ByID` (`runner.go:494-526`, `taskstate.go:168`). The Docker
backend gets that stream for free from `docker events` (`docker.go:309`). The
Lambda backend has no equivalent, so `lambdamicrovm.Watch` *manufactures* one:
list all MicroVMs, tag-filter to find its own, keep an SSE stream per VM, and
dedup exits across streams with `watcher.{streams,states,reported}`
(`lambdamicrovm.go:442-544`).

Per #1088 (bug 1), that discovery is **unimplementable against the real Lambda
MicroVMs API**: `run-microvm` takes no tags, `get`/`list-microvms` return no
tags, and a running MicroVM is not a taggable resource — so `Watch`'s tag filter
(`lambdamicrovm.go:497`) matches nothing in production. The runner never learns
of a driver exit, never suspends, never reconciles. `list-microvms` items also
carry no `endpoint`, so even a "discovered" VM can't be streamed without a
`GetMicrovm` per id. There is **no owner-scoped fleet query at all**.

#1089 / #1091 resolve this by introducing a live `Sandbox` object per task, with
`Backend.Marshal`/`Unmarshal` and a per-sandbox blocking `Wait`. This proposal
reaches the same end state with a **much smaller** interface change: replace the
fleet `Watch` with a per-handle blocking `Wait`, and keep the existing `Handle`,
`Launch`, and `Probe` exactly as they are.

## Design

### The reframing

- **Old (fleet):** one `Watch` stream → N opaque ids → reverse-resolve id→task.
- **New (task):** N `Backend.Wait(ctx, handle)` calls, each in its own goroutine
  that *already knows its task id* (the runner spawned it).

The enabling invariant is unchanged from #1089: the runner-local `taskstate`
statefile is the only owner-scoped enumeration of this runner's sandboxes that
exists. Docker *could* enumerate via labels; Lambda *cannot*. So the statefile
enumerates, and the runtime answers **liveness per id only** — never as an
enumerator.

The insight that shrinks the interface: `backend.Handle{Type, ID, Data}`
(`backend.go:31`) is **already** the serializable identity #1089 wants to
`Marshal` out of a live `Sandbox`, and it is **already** what
`taskstate.Record{TaskID, Type, ID, Data}` persists (`taskstate.go:26`). So no
`Sandbox` object, no `Marshal`/`Unmarshal`: `Wait` is a plain method that takes
the persisted `Handle`, and its transient state (SSE stream, auth token, backoff)
lives on the `Wait` call's own stack for the duration of the one call.

### The interface change

The whole backend-surface delta is one method on `backend.Backend`
(`backend.go:96`):

```go
// delete:
Watch(ctx context.Context, handle func(HandleExit)) error

// add:
// Wait blocks until the sandbox identified by h reaches a terminal outcome,
// returning exactly once. It swallows transient failures internally (SSE drops,
// token re-mint, reconnect/arbitrate, backoff). For Lambda it performs the
// suspend-on-driver-exit before returning. It is safe to call on a sandbox this
// process did not start (re-attach after a restart).
Wait(ctx context.Context, h Handle) (ExitCode, error)
```

Everything else on `Backend` stays: `ValidateWorkspace`, `Launch(ctx, spec,
reuse)`, `Probe(ctx, h)`, `Signal(ctx, h)`, `Destroy(ctx, h)`, `Close`.

Deleted from `backend.go`:

- `HandleExit{ID, ExitCode}` (`backend.go:42`) — no id-keyed callback anymore.
- `Exit{TaskID, ExitCode}` (`backend.go:89`) — the runner built this by resolving
  a `HandleExit` id back to a task; the `Wait` goroutine already holds the task
  id and the code.

Kept, and load-bearing for staying smaller than #1091:

- `Handle{Type, ID, Data}` — unchanged; already the value-typed identity.
- The `Sandbox{TaskID, State}` struct (`backend.go:80`) — the runner-composed
  point-in-time view used by `List`/`Prune`. Because we introduce **no** `Sandbox`
  *interface*, the name collision #1091 flags in its "interface details
  unresolved" section never arises.

`ExitCode` becomes a named type carrying the existing driver-owned-events
invariant (today an `int` on `HandleExit`/`Exit`), with a named sentinel for the
report-lost case (replacing the magic `-1`):

```go
// ExitCode reports why a sandbox stopped. 0 means the driver reported its own
// terminal outcome to the server (no runner event owed); non-zero means the report
// was lost and the runner must emit "failed" on the driver's behalf.
type ExitCode int

const ExitLost ExitCode = -1 // driver's report was lost → runner emits "failed"
```

A `backend.ErrGone` sentinel is added for the one-sandbox-per-task invariant
(below): `Launch` returns it when handed a reuse handle whose sandbox no longer
exists, rather than creating a fresh one.

```go
// ErrGone means the sandbox a reuse handle refers to no longer exists. Launch
// returns it instead of creating a fresh sandbox, since the task is bound 1:1 to
// the sandbox its handle references.
var ErrGone = errors.New("backend: sandbox is gone")
```

### The `Wait` contract

`Wait` returns **exactly once**, and its `error` return has exactly one meaning:

1. **Terminal, driver reported** → `(0, nil)`. For Lambda, `Wait` performs the
   suspend (preserve disk, stop compute) *before* returning; the orchestrator only
   ever sees an exited/preserved sandbox, Docker-identically.
2. **Terminal, report lost** → `(ExitLost, nil)` (stream gone and the control
   plane says the sandbox is non-running/terminal; VM reaped by `max_duration`;
   container removed). A rehydrated-already-dead sandbox returns this immediately.
3. **Runner shutting down** → `(_, ctx.Err())` where
   `errors.Is(err, context.Canceled)`. This means *the runner stopped watching*,
   **not** that the sandbox stopped running. The sandbox keeps running and is
   rehydrated next boot; the `supervise` goroutine must **not** emit `failed`.

A well-behaved backend returns a non-nil `error` **only** for outcome 3.
Unrecoverable runtime conditions are expressed as the `(ExitLost, nil)`
report-lost outcome, not as an error, so the runner never has to guess whether a
non-cancel error left the sandbox alive.

### Runner: a supervise goroutine per running sandbox

The persisted `taskstate` store is the source of truth for which sandboxes belong
to this runner; `Probe` derives their liveness; and exit observation is one
fire-and-forget goroutine per running sandbox, parked in `Wait` on the runner's
root context. **The runner needs no new in-memory task state** — no controller
map, no mutex, no `WaitGroup`. It keeps only what it has today: the semaphore
(concurrency), the event queue, and the wake channel.

```go
func (r *Runner) supervise(ctx context.Context, taskID int64, h backend.Handle) {
    code, err := r.backend.Wait(ctx, h)
    if errors.Is(err, context.Canceled) {
        return // shutdown: leave the sandbox alive for next-boot rehydration
    }
    r.sem.Release(1)
    r.wake.Wake()
    if code != 0 {
        // report lost — "failed" is the honest outcome (driver-owned-events).
        r.queue.Enqueue(model.RunnerEvent{TaskID: taskID, Event: model.RunnerEventFailed})
    }
}
```

`Start` (`runner.go:445`) reorders to: resolve reuse → `Launch` → `store.Write`
→ `go supervise`. Because `Wait` is spawned **after** the record is persisted and
is level-triggered (it re-derives liveness from the handle), an exit in the
launch→persist window is not lost, and the goroutine already knows its task — so:

- `launchMu` (`runner.go:39`) is **deleted**. It existed only to serialize
  `Launch`+`Write` against `Monitor`'s `store.ByID`; there is no reverse lookup
  and no global watcher to race against anymore.
- `taskstate.Store.ByID` (`taskstate.go:168`) is **deleted** — nothing resolves
  id→task.

#### One sandbox per task: `Launch` never re-creates a bound sandbox

There is a **1:1 mapping between a task and the sandbox its handle references**.
Once a record exists, the task is bound to *that* sandbox — a specific container
id / MicroVM id whose filesystem/disk is the task's workspace. A fresh sandbox is
a *different*, empty workspace and is not a substitute. So `Launch`'s contract is
tightened:

- **reuse handle present** → reuse the exact recorded sandbox: adopt-and-restart
  it if it is a preserved husk (`StateExited`), or return the running/resumed
  handle. If that sandbox is **gone** (`StateGone` — removed container / terminated
  VM), return `backend.ErrGone`. `Launch` **never silently creates a fresh
  sandbox on the reuse path.**
- **no reuse handle** → create fresh. This is the only path that creates, and it
  happens only on a task's first start.

This is a deliberate change from today: the current Docker `ensure`
(`docker.go:99-123`) falls through to `create` when the recorded (and name-matched)
container has vanished, silently starting a *new* empty container for the task.
That fallback is **removed**. A vanished sandbox is surfaced, not papered over:
`Start` maps `ErrGone` to a `failed` event and removes the now-dangling record
(a subsequent explicit `start`/`restart`, having no handle, is then a legitimate
first-start-fresh — the only way a task re-binds to a new sandbox). The runner's
pre-`Launch` `Probe` short-circuits the common case (`StateGone` → `failed`
without minting a token), but `Launch`'s refuse-to-create-on-reuse guard is the
authoritative one, since `Probe`→`Launch` is racy.

The `start`-command idempotency check keeps its current shape:
`Running(taskID)` = `store.Read` + `Probe` (`runner.go:313-323`). No in-memory
"what's running here" map is required to prevent a duplicate launch: `Load`
(below) completes before `Poll` admits work, polls are sequential, and after boot
only `Start` spawns a `supervise` — and only when `Probe` reports not-running (on
a `Probe` error the handler skips rather than launches, `runner.go:220-222`), so
two `Wait`s can never exist for one task. Graceful shutdown is a single root-ctx
cancel — every `Wait` returns `ctx.Err()` and its goroutine exits; there is
deliberately nothing to drain, since on cancel `Wait` abandons *without*
suspending so the sandbox persists for rehydration.

An in-memory running-set could later cache these `Probe` results as a pure
optimization; it is intentionally out of the core design (see Trade-offs).

### Restart / rehydration: the loader

A `Load` step runs once at boot, *before* `Poll` admits new work. It folds
together today's `Reconcile` (`runner.go:255`) and the global `Monitor`
goroutine (`command/runner.go:221-237`): the statefile enumerates, the runtime
answers liveness per id, and every running sandbox gets the same per-handle
`supervise` goroutine as the live path.

```go
func (r *Runner) Load(ctx context.Context) error {
    recs, err := r.store.List()
    if err != nil {
        return err
    }
    var running int64
    for _, rec := range recs {
        h := backend.Handle{Type: rec.Type, ID: rec.ID, Data: rec.Data}
        st, err := r.backend.Probe(ctx, h)
        if err != nil {
            r.log.Error("load: probe", "task", rec.TaskID, "error", err)
            continue
        }
        switch st {
        case backend.StateRunning: // container up / VM RUNNING
            go r.supervise(ctx, rec.TaskID, h) // Wait RE-ATTACHES
            running++
        case backend.StateExited: // stopped container / SUSPENDED VM (husk preserved)
            r.failIfTaskRunning(ctx, rec.TaskID) // lost-report backstop
        case backend.StateGone: // removed / TERMINATED — the bound sandbox vanished
            r.failIfTaskRunning(ctx, rec.TaskID) // lost-report backstop
            r.store.Remove(rec.TaskID)           // dangling record: nothing to reuse or destroy
        }
    }
    r.sem.Set(running) // may exceed capacity; that's fine
    return nil
}
```

`command/runner.go` then calls `r.Load(ctx)` at startup and **deletes** the
`go r.Monitor(ctx)` goroutine (`command/runner.go:221-232`) and the standalone
`r.Reconcile(ctx)` call (`command/runner.go:235-237`). Probe-then-branch is
preferred over "spawn `Wait` for everything": it yields a synchronous
running-count for the semaphore and avoids opening a doomed SSE stream per dead
VM.

### New `StateGone`

The loader needs to distinguish an **exited husk** (state preserved, resumable —
a stopped container or a `SUSPENDED` VM) from a sandbox that is truly **gone**
(removed container / `TERMINATED` VM). With only `StateExited` today
(`backend.go:71`), the two are conflated, so the loader can't safely drop a
record: dropping the record for a still-`SUSPENDED` VM would leak a billable husk
it can no longer `Destroy`. Add:

```go
const (
    StateUnknown State = iota
    StateRunning
    StateExited // husk preserved: stopped container / SUSPENDED VM
    StateGone   // removed container / TERMINATED VM — nothing to resume or destroy
)
```

- Docker `Probe` (`docker.go:239`): container `NotFound` → `StateGone` (was
  `StateExited`); `exited`/`dead` → `StateExited`.
- Lambda `Probe` (`lambdamicrovm.go:311`): `TERMINATED` / `IsNotFound` →
  `StateGone`; `SUSPENDING`/`SUSPENDED` → `StateExited`;
  `RUNNING`/`PENDING` → `StateRunning`.

`StateExited` and `StateGone` diverge exactly where the one-sandbox-per-task
invariant lives: `StateExited` is a preserved husk that `Start` resumes in place,
while `StateGone` is a vanished binding that `Start`/`Launch` refuse to re-create
(`ErrGone` → `failed` + record removed) and the loader drops. `Prune` treats both
as "no live sandbox" when deciding whether an archived task still needs a
`Destroy`.

### Backend implementations

**Docker** (`docker.go`). `Watch` is replaced by:

```go
func (b *Backend) Wait(ctx context.Context, h backend.Handle) (backend.ExitCode, error) {
    // WaitConditionNotRunning is level-triggered: the daemon returns the stored
    // exit code immediately if the container has already stopped. This is what
    // closes the launch→persist race (and the boot Probe→Wait TOCTOU) without a
    // manual inspect. WaitConditionNextExit is edge-triggered and would block
    // forever on a container that already exited before this call.
    okCh, errCh := b.docker.ContainerWait(ctx, h.ID, container.WaitConditionNotRunning)
    select {
    case <-ctx.Done():
        return 0, ctx.Err()
    case <-errCh:
        // Removed (NotFound) or a wait error: no code to recover → report lost.
        return backend.ExitLost, nil
    case res := <-okCh:
        return backend.ExitCode(res.StatusCode), nil
    }
}
```

`WaitConditionNotRunning` re-derives liveness from the handle: it returns an
already-exited (not-yet-removed) container's stored exit status immediately,
which is exactly what closes the launch→persist race without `launchMu`. A single
`select` must always drain one channel — the SDK's `resultC` is unbuffered, so a
returned-but-undrained result would leak the SDK's wait goroutine (its bare
`resultC <- res` send is not ctx-aware). No id filtering is needed since the
runner only ever `Wait`s on handles it persisted.

`ensure` (`docker.go:99`) changes to enforce the one-sandbox-per-task invariant:
the deterministic-name adoption block (`docker.go:113-120`) is **removed**, and so
is the fall-through to `create` on the reuse path. `Launch` reuses a container
**only** via the handle id recorded in the statefile — if that container exists it
is adopted and (re)started; if it is gone, `Launch` returns `backend.ErrGone`
instead of creating a fresh one. `create` runs only when `reuse == nil` (first
start). The `gritz-{taskID}` name is still assigned at `create` (readability, and
a name conflict there *fails* the create, preventing a duplicate driver for a
task whose sandbox the runner has lost track of), and the `gritz`/`gritz.runner`
labels stay — but both now serve tooling and a future orphan-scanner, not
adoption. Reclaiming a container leaked by a crash in the launch→persist window is
explicitly out of scope (see Open Questions).

**Lambda** (`lambdamicrovm.go`). The per-handle `Wait` *is* today's per-VM
`stream()` loop (`lambdamicrovm.go:590-634`), lifted to the top level and given
the handle's endpoint from `Handle.Data`:

- mint token → `readStream` → on `driver-exited{code}`: `SuspendMicrovm`, return
  `(code, nil)`;
- on a bare drop: `arbitrate` via `GetMicrovm` — RUNNING → reconnect (sticky
  `driver-exited` covers a gap-exit); non-running / `IsNotFound` → return
  `(ExitLost, nil)`;
- `ctx` cancelled → return `ctx.Err()`.

Because one `Wait` owns exactly one VM, the entire fleet-multiplexing apparatus
is **deleted**: `watcher` and its `streams`/`states`/`reported` maps, `sweep`,
`ensureStream`/`removeStream`/`emit`, `listAll`, the `tagRunner` constant and its
filter, and the reconcile ticker. The sweep's real job — catch a VM that went
terminal while disconnected — is already covered by `Wait`'s own
drop→`GetMicrovm`→arbitrate loop plus the loader's startup `Probe`. With the
sweep gone, `defaultReconcile` / `Options.ReconcileInterval`
(`lambdamicrovm.go:83,137`) and the `--lambda-microvm-reconcile` flag
(`command/runner.go:93-98`) are removed.

`Signal`/`Destroy`/`Probe`/`launchFresh` are unchanged, and `tryResume`'s resume
logic (`lambdamicrovm.go:220-251`) is unchanged. What changes is only the
`Launch` wrapper (`lambdamicrovm.go:193-215`): when a reuse handle is present and
`tryResume` reports the VM gone (`resumed == false`), `Launch` now returns
`backend.ErrGone` instead of falling through to `launchFresh` — the one-sandbox-
per-task invariant. `launchFresh` runs only when `reuse == nil`. Note `Signal`
already re-mints its own token and opens its own request (`lambdamicrovm.go:332`),
so it does not depend on sharing state with an in-flight `Wait`.

### `backend_moq.go`

`//go:generate go tool moq -out backend_moq.go . Backend` (`backend.go:13`)
regenerates for the one-method change. There is no second interface to mock (the
`Sandbox` #1091 proposes never exists). The lambda `fakes_test.go` Cloud/Stager
fakes are structurally unaffected; the `watcher`-specific tests in
`lambdamicrovm_test.go` are replaced by per-handle `Wait` tests.

## Trade-offs

**vs. #1091's `Sandbox` interface.** Both proposals produce the *same* runner-side
rewrite (per-sandbox `supervise` goroutine, loader folding in
`Reconcile`+`Monitor`, the shutdown-safe `Wait` contract, elimination of
`launchMu` and the lost-exit race). The difference is entirely in the backend
surface:

| | #1091 (`Sandbox`) | This proposal (`Backend.Wait`) |
|---|---|---|
| Interface change | new `Sandbox` interface (6 methods) + `Backend.Create`/`Marshal`/`Unmarshal` | swap one `Backend` method (`Watch`→`Wait`) |
| Persisted identity | `Marshal(sandbox) []byte` / `Unmarshal` | `Handle{Type,ID,Data}` — already persisted as `Record.Data` |
| `Start`/persist seam | new `Create`→`Start` split | existing `Launch(reuse)→Handle`, already returns the persistable handle |
| Resume endpoint mutation | internal to `Sandbox.Start` | existing `Launch(reuse)→new Handle` (`tryResume`, `lambdamicrovm.go:242-250`) |
| `Sandbox` struct collision | must be reworked/deleted | none — no `Sandbox` interface introduced |
| Mocks | `Backend` + `Sandbox` | `Backend` only |

Both `Start`/`Wait`-split justifications #1089 gives are already satisfied by the
current `Launch(ctx, spec, reuse *Handle) (Handle, error)`: identity is
runtime-allocated and returned to the runner (which persists it before `Wait`),
and resume mutates the endpoint by returning a new handle at the same seam. So the
`Sandbox` object mainly re-packages machinery that `Launch`+`Handle`+`Probe`
already provide.

**vs. a single `Backend.Run(ctx, handle, checkpoint func([]byte) error)`.** A
blocking call that launches *and* waits, with the runner passing its store-writer
in, also avoids a fleet stream. Rejected for the same reason #1089 rejects it:
it buries the "started and committed" seam in a callback and makes
start-failure-vs-exit harder to distinguish. The explicit `Launch` → persist →
`Wait` split keeps the store write in the runner, between two backend calls,
where the only writer lives.

**Keeping `Probe` (not folding it into `Wait`).** The loader needs a synchronous
per-record liveness read to seed the semaphore and to branch
running/exited/gone; the `start`-command idempotency check reuses the same
`Probe`. Probe-then-branch avoids opening a doomed SSE stream per dead VM at boot.

**No in-memory running-set (deferrable optimization).** The store + `Probe` +
`supervise` goroutines are a complete model on their own, so the runner carries
no authoritative in-memory task state. The only thing an in-memory
`map[taskID]` running-set would buy is skipping the `Probe` on the
`start`-command idempotency check during the launch→`started` window. For Docker
that `Probe` is a local `ContainerInspect` (free); for Lambda it is a
`GetMicrovm` (a rate-limited control-plane call) per poll per not-yet-started
task. Because such a set is a pure fast-path in front of the existing `Probe`
check — no interface, `taskstate`-schema, `Wait`-contract, or rehydration impact,
and nothing else needs to know it exists — it is intentionally left out of the
core design and can be added later if `GetMicrovm` volume on Lambda proves to
matter.

**`StateGone` is load-bearing (not separable).** It was tempting to treat
`StateGone` as a follow-up refinement, but the one-sandbox-per-task invariant
needs it: `Start` must tell a resumable husk (`StateExited` → adopt-and-restart)
from a vanished binding (`StateGone` → `ErrGone` → `failed`), and with only
`StateExited` the two are indistinguishable. So `StateGone` ships with this
change. It also lets the loader and `Prune` garbage-collect records for
truly-gone sandboxes instead of re-`Probe`ing a 404 forever.

## Open Questions

1. **Orphan reclamation is explicitly deferred.** A crash between `Launch` (the
   VM/container is started inside it) and the post-`Launch` `store.Write` leaks a
   sandbox the runner has no record of. `Start` adopts **only** the handle id in
   the statefile — Docker's deterministic-name adoption is removed
   (`docker.go:113-120`), because re-discovering a container by name risks
   adopting the wrong one and can silently resume a sandbox whose state the runner
   never recorded. So neither backend self-heals a leaked sandbox now: Docker
   leaks a container (a same-name `create` conflict simply fails the launch,
   preventing a duplicate driver), Lambda leaks a VM billed until `max_duration`
   (no id, no owner-scoped list — #1088). There is no good way to reclaim these
   safely inline, so it is left unhandled here. A future **orphan-scanner** —
   enumerating leaked sandboxes (Docker by `gritz`/`gritz.runner` label; Lambda
   by `ListMicrovms` within a single-tenant scope) and reaping those with no
   statefile record — is the intended fix, tracked separately.

2. **Hot-orphan window + `idlePolicy`** (from #1091 OQ1). Suspend-on-exit is
   runner-driven (the guest holds no AWS creds), so a runner dying *after*
   driver-exit but *before* the suspend leaves a VM hot until restart re-attaches
   `Wait`, reads a sticky-replayed `driver-exited`, and suspends. Two
   consequences: `Wait` must be safe to re-attach to a sandbox this process did
   not start (Docker `ContainerWait` gives this free; the Lambda shim must
   hold/replay the last lifecycle event), and the window is bounded only by
   `max_duration` (hours). Worth reconsidering whether omitting `idlePolicy`
   entirely (`lambdamicrovm.go:282-287`) is right — a bounded idle-suspend is
   arguably the correct safety net for exactly this window, even though its 600s
   floor can't express "never auto-suspend".

3. **Blob-schema drift across a binary upgrade** (from #1091 OQ2). If a future
   `Handle.Data` layout can't be parsed by an older/newer binary, the top-level
   `Handle.ID` / `Record.ID` (`taskstate.go:29`) is a schema-stable terminate key,
   so `Destroy(Type, ID)` still terminates the VM/container even when `Data`
   won't decode. Caveat to confirm: Lambda `Destroy` uses `Data`'s
   `StageBucket`/`StageKey` for S3 cleanup (`lambdamicrovm.go:376-380`), so an
   unparseable `Data` terminates the VM but leaks the staged bundle object. Is
   that acceptable, or should the stage location also be schema-stable?

4. **`ExitCode` type vs bare `int`.** Defining `type ExitCode int` documents the
   "0 = reported / non-zero = lost" invariant on the type; a bare `int` matches
   the current `HandleExit`/`Exit` fields with less churn. Minor; pick one.

5. **Resume stays a `Start`-command path, not a loader path.** The loader's
   `StateExited`-husk branch (task still `RUNNING`) is treated as **report-lost →
   failed**, not auto-resume. Resume happens only when a `start`/`restart` command
   drives `Start` → `Launch(reuse)` → `tryResume` (`lambdamicrovm.go:220`),
   unchanged. Confirm this is the intended boundary and that `Wait` only ever
   attaches to an already-up sandbox.
