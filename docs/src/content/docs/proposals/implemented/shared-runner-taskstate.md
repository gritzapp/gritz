---
title: "Shared Runner-Local Taskstate Store"
description: "Docker treats container labels as the task-to-sandbox database, which does not generalize, so each new backend would invent its own bookkeeping for which sandbox belongs to a task."
---

Issue: https://github.com/icholy/gritz/issues/1075

> This change lands **before** the lambda-microvm backend (PR #1054,
> `feat/lambda-microvm-backend`). It introduces the shared `taskstate` store and
> converts the **Docker** backend to use it now; the in-flight microvm backend
> will **adopt** this store when it lands rather than carrying its own. The
> relationship is forward-looking, not an extraction of existing microvm code.

## Problem

A runner backend has to answer one question for every task it hosts — *which
sandbox belongs to this task, and is it still alive?* — and today the only
backend, **Docker**, answers it by treating the runtime itself as the database.

Container **labels** are the index. `find` is a label-filtered `ContainerList`
on every call; `List` re-lists by `gritz=true`/`gritz.runner=<id>` and parses
the `gritz.task` label, with an error branch for a malformed value; `Watch`
reconstructs the task id and exit code from docker event-attribute strings.
There is no runner-local record of the task→container mapping at all — the daemon
*is* the source of truth.

This has worked because Docker hands back rich, queryable metadata. But it does
not generalize, and the runtime-as-truth approach is about to multiply:

- **More backends are arriving.** The lambda-microvm backend is in review
  (PR #1054), and firecracker / agentcore are proposed
  (`proposals/draft/`). None of them get Docker's label-query convenience. AWS
  assigns a microVM's id and there is *no deterministic name to re-derive*, so
  that backend necessarily keeps a runner-local handle somewhere; firecracker
  and agentcore will face the same. Without a shared answer, **each backend
  invents its own** task→sandbox bookkeeping (the in-flight microvm branch
  already adds a per-task JSON store under
  `internal/runner/backend/lambdamicrovm/taskstate`), and the runner ends up with
  several divergent strategies — *label-as-truth* here, *local-store-as-truth*
  there — that the orchestrator has to reason about uniformly.
- **Discovery and liveness are entangled.** The Docker path mixes its *liveness*
  check with task-id string parsing (`strconv.ParseInt` on a label, an error
  branch for a bad value) that has nothing to do with whether a container is
  running.
- **A naive per-task store has a torn-write trap.** The straightforward
  one-file-per-task JSON approach a new backend reaches for (`os.WriteFile` +
  skip-unreadable-on-`List`) silently drops a tracked task if a crash interrupts
  a write. Solving this once, correctly, beats each backend re-solving it.
- **There is no backend-agnostic place** that records "task N maps to handle H on
  backend B" — which is exactly the mapping every present and future backend
  needs.

Rather than let that divergence set in, introduce the shared mapping **now**,
adopt it in the Docker backend, and give every future backend (microvm first)
one place to plug into.

## Design

### Overview

Introduce a single shared runner-level package, `internal/runner/taskstate`, and
make it the **single source of truth** for the task→sandbox-handle mapping for
**every** backend. The Docker backend adopts it in this change; the
lambda-microvm backend adopts it when #1054 lands; firecracker / agentcore use
it from day one.

The store records, per task, which backend owns the sandbox and a backend-produced
**handle** — an explicit `ID` (the container id for Docker; the microVM id for a
microVM) plus opaque `Data` the store never decodes. Crucially, **the runner owns
the store and every mutation of it.** Backends no longer persist, discover, or
reconcile anything: the `Backend` interface is rewritten down to per-sandbox
runtime ops — **launch / probe / signal / destroy** — that take and return
handles, and the runner composes `List` / `Running` / `Remove` / reconcile over
them on top of the store. This is the explicit architectural choice: taskstate is
a *runner* responsibility, not a per-backend one, and the interface change is part
of the design rather than a deferred option.

Five decisions frame the design.

1. **Runner-owned, not the server.** Sandbox-handle state is a runner concern. The
   server already owns the task's logical state (status, events, links) and is
   deliberately ignorant of *how* a runner sandboxes a task. We do **not** push
   the mapping into the server: a handle is meaningful only to the runner that
   created it (a container id is local to one daemon; a microVM id to one
   account/region), and persisting it server-side would couple the server to backend
   internals it has no reason to know. The store lives on the runner's local
   filesystem.

2. **The store is the only source of truth; tags/labels become informational.**
   Docker container labels (`gritz.task`, `gritz.runner`, `gritz=true`) — and
   the equivalent tags a microVM backend sets — are **kept**, but only for human
   visibility (`docker ps`, the AWS console, ad-hoc `aws lambda list-microvms`).
   They are **never read** for discovery or for any lifecycle decision. This is
   the decisive simplification: today the Docker backend's correctness *depends*
   on the daemon faithfully returning the right labels; after this change the
   labels are decoration and the store is authority.

3. **Backing store: atomic per-task JSON files.** One
   `<state-dir>/tasks/<id>.json` per task, with an **atomic** `Write`: marshal to
   a temp file in the same directory, `fsync`, then `os.Rename` over the target
   (rename is atomic on a single filesystem). This closes the torn-write window
   where `List` would skip a half-written file and a tracked task vanishes —
   designed in from the start rather than discovered later. Explicitly **no
   sqlite / no `modernc.org/sqlite`** dependency: the runner binary is baked into
   every container/microVM image at `backend.BinaryPath`, and one-file-per-task
   JSON with atomic rename is sufficient for the handful of concurrent tasks a
   single runner hosts. A directory of small JSON files is also trivially
   inspectable and recoverable by hand, which matters for an
   operability-sensitive runner.

4. **A handle with an explicit id; the store is a dependency-free leaf.** A
   sandbox handle has two parts: a backend-produced **id** that is unique and
   stable for the sandbox's lifetime, and opaque **data** the store never decodes.
   The id is the legitimate index key; the data carries whatever the backend needs
   for cleanup but not for identity. The `backend` package defines:

   ```go
   // Handle identifies a backend's sandbox. ID is the index key (a container id,
   // an AWS microVM id, ...); Data is backend-defined and never decoded by the
   // store or the runner.
   type Handle struct {
       ID   string          `json:"id"`
       Data json.RawMessage `json:"data,omitempty"`
   }
   ```

   - **Docker:** `ID` = container id, `Data` = nil (identity *is* the whole
     handle).
   - **microVM** (when #1054 adopts the store): `ID` = the AWS microVM id that
     `ListMicrovms` returns, `Data` = `{image_arn, stage_bucket, stage_key}` — the
     fields `Destroy` needs for staged-object cleanup, not for identity.

   The store persists the **decomposed** form so it never has to import the
   `backend` package or decode anything — it stays a dependency-free leaf with a
   plain string-keyed reverse index:

   ```go
   // package taskstate
   type Record struct {
       TaskID  int64           `json:"task_id"`
       Backend string          `json:"backend"`        // "docker", "lambda-microvm", ...
       ID      string          `json:"id"`             // == backend.Handle.ID; reverse-index key
       Data    json.RawMessage `json:"data,omitempty"` // == backend.Handle.Data; opaque
   }
   // ByID(id) (Record, bool) is backed by a map[string]int64 (id → task id).
   ```

   The runner translates between `backend.Handle{ID,Data}` and
   `taskstate.Record`: on `Launch` it splits the returned handle into the record;
   when calling `Probe`/`Signal`/`Destroy` it reassembles `Handle{rec.ID,
   rec.Data}`. This is what makes the `Watch`→task resolution sound — see decision
   5 — instead of byte-matching opaque JSON (which would be fragile to key
   ordering/whitespace, and impossible for a poll-based `Watch` that only sees ids).

   The `Backend` field stays informational — which backend produced the handle (a
   decode guard / tooling aid). It is **not** a namespacing key: task ids are
   globally unique, so a flat `tasks/<id>.json` directory needs no per-backend
   partitioning.

5. **The runner owns taskstate mutations; the `Backend` interface is rewritten.**
   The `Backend` interface today mixes runtime ops with discovery and persistence
   (`Start`/`Stop`/`Running`/`List`/`Remove`/`Watch`). It is replaced by a
   handle-oriented interface that does runtime work only and never touches the
   store:

   ```go
   type Backend interface {
       ValidateWorkspace(ws *workspace.Workspace) error

       // Launch ensures a sandbox exists for the spec and starts it, returning
       // the Handle the RUNNER persists. If reuse is non-nil, the backend may
       // adopt the sandbox it identifies (preserving its filesystem) instead of
       // creating fresh, or clean up its stale resources. The backend performs
       // no persistence of its own.
       Launch(ctx context.Context, spec *Spec, reuse *Handle) (Handle, error)

       // Probe reports the liveness of a single handle.
       Probe(ctx context.Context, h Handle) (State, error)

       // Signal gracefully stops the sandbox identified by h (SIGTERM →
       // SIGKILL / terminate-microvm), reporting whether a running sandbox was
       // signalled — the driver then owns the terminal event report.
       Signal(ctx context.Context, h Handle) (signalled bool, err error)

       // Destroy deletes the sandbox identified by h.
       Destroy(ctx context.Context, h Handle) error

       // Watch streams sandbox exits keyed by handle ID until ctx is cancelled.
       // It emits only the id (not the full Handle) so a poll-based backend that
       // sees only ids — e.g. ListMicrovms — can report without reconstructing
       // Data. The runner resolves id→task via the store and dedups; Watch
       // performs no persistence. (Stays per-backend — see below.)
       Watch(ctx context.Context, handle func(HandleExit)) error

       Close() error
   }

   // HandleExit carries only the handle id — enough for the runner's id→task
   // lookup, and all a poll-based Watch can produce.
   type HandleExit struct {
       ID       string
       ExitCode int
   }
   ```

   `Spec`, `State` (`StateRunning`/`StateExited`/`StateUnknown`), and the
   orchestrator's `Sandbox`/`Exit` task-keyed views are unchanged; what moves is
   *who* maps task↔handle. The runner (`runner.Runner`) gains a
   `*taskstate.Store` and becomes the only writer, translating between
   `backend.Handle` and `taskstate.Record` at the boundary:

   ```
   // runner-side, composed over the backend + store
   handleOf(rec) = backend.Handle{ID: rec.ID, Data: rec.Data}

   Start(taskID, spec):
       rec, ok := store.Read(taskID)
       var reuse *backend.Handle
       if ok {
           h := handleOf(rec)
           if backend.Probe(h) == StateRunning { return }           // idempotent
           reuse = &h                                                // adopt / cleanup
       }
       h := backend.Launch(spec, reuse)
       store.Write(Record{taskID, backendName, h.ID, h.Data})       // ← runner owns the mutation

   List():     for rec in store.List():
                   emit Sandbox{rec.TaskID, backend.Probe(handleOf(rec))}
   Running(t): backend.Probe(handleOf(store.Read(t))) == StateRunning
   Remove(t):  backend.Destroy(handleOf(store.Read(t))); store.Remove(t)
   Kill(t):    backend.Signal(handleOf(store.Read(t)))
   Monitor:    backend.Watch(func(e HandleExit) {                   // see Watch section
                   if rec, ok := store.ByID(e.ID); ok { enqueue(rec.TaskID, e.ExitCode) }
               })
   ```

   So the orchestrator's `Reconcile`/`Prune`/`Monitor` no longer call a backend
   `List`/`Remove`/`Watch` that hides a store; they call these runner-owned
   wrappers, and the store mutation lives in exactly one place.

   - **Docker's `Launch`** inspects `reuse` (whose `ID` is a container id): if the
     container exists it reuses it (`RepairNetworks` + `ContainerStart`), else it
     creates `gritz-{taskID}`; on a name conflict it adopts the existing
     container by name and returns its id. **`Probe`** is
     `ContainerInspect(handle.ID)`
     (`running`→`StateRunning`, `exited`/`dead`/not-found→`StateExited`),
     replacing the label-filtered `ContainerList` in `find` and the
     label-parsing loop in `List`. No Docker code parses `gritz.task` anymore.
   - **A microVM backend** (future) implements `Launch` (stage bundle +
     `RunMicrovm`; `reuse` carries the prior handle so the backend can delete the
     stale staged object — exactly the in-flight branch's `Start` stale-handle
     cleanup) and `Probe` as a lookup in one `ListMicrovms` call (its current
     `microvmsByID` reconcile). It implements only these four methods and inherits
     List/Running/Remove/reconcile from the runner.

   A future refinement is a batched `ProbeAll(ctx, []Handle) []State` so a
   list-based backend answers a whole `List` in one `ListMicrovms` call rather
   than N probes; the single-handle `Probe` is the floor (see Open Questions).

### `Watch` stays per-backend

`Watch` is the one runtime op that is **not** collapsed into the
launch/probe/signal/destroy quartet, and it stays per-backend. Docker keeps its
push-based `die` event stream (`docker.Events` filtered to this runner's
containers); a microVM backend keeps its `ListMicrovms` poll loop. The reasons the
two differ are intrinsic — Docker has a real event bus, Lambda does not — and
forcing a common abstraction would either throw away Docker's push semantics or
impose a poll on a backend that doesn't need one.

What *does* change is that `Watch` now reports exits keyed by **handle id**, not
by a task id the backend parsed out of runtime metadata. The runner resolves
id→task via `store.ByID` (the `map[string]int64` reverse index) and applies the
same dedup/enqueue logic `Monitor` has today. Emitting only the id is what keeps
this sound for both styles of backend: Docker's `Watch` stops doing
`strconv.ParseInt` on `gritz.task` event attributes and just reports the
container id from the `die` event; a microVM `Watch` reports the microVM id its
`ListMicrovms` poll already returns, with no need to reconstruct the opaque
`Data`. The runner ignores any id it doesn't track. (This is why the handle has an
explicit `ID` rather than being an opaque blob the runner would have to
byte-match — see decision 4.)

### Package layout

```
internal/runner/
├── taskstate/                  new shared, atomic-write store (dependency-free leaf)
│   └── taskstate.go            Record{TaskID, Backend, ID, Data}; Write/Read/Remove/List/ByID
├── runner.go                   owns *taskstate.Store; translates backend.Handle↔Record; composes List/Running/Remove/reconcile
└── backend/
    ├── backend.go              Handle{ID, Data}; rewritten Backend interface (Launch/Probe/Signal/Destroy/Watch)
    └── docker/                 implements the four ops; Handle.ID = container id, Data = nil
```

Neither side imports the other: `taskstate` never imports `backend` (it stores
the decomposed `{ID, Data}`, never `backend.Handle`), and backends never import
`taskstate`. Only `runner.go` depends on both and translates at the boundary.
When the lambda-microvm backend lands, it adds `backend/lambdamicrovm/`
implementing the same four ops with `Handle.ID` = microVM id and `Data` =
`{image_arn, stage_bucket, stage_key}`, and drops both the standalone
`backend/lambdamicrovm/taskstate` package *and* its own List/Watch store-scanning
its branch currently carries.

### Rollout for the Docker backend

This is the only backend that changes on disk today, and the transition is
deliberately conservative:

- **New state dir.** The runner gains a single flat state directory
  (e.g. `<state-dir>/tasks/<id>.json`, default `/var/lib/gritz/tasks`). It is
  **not** namespaced by backend — task ids are globally unique, so one `tasks/`
  directory suffices regardless of which backend a runner runs. On first run after
  upgrade the store is empty.
- **The runner writes the record.** After `backend.Launch` returns a handle, the
  runner writes the `{container_id}` record; on `Remove` the runner calls
  `backend.Destroy` and deletes the record. The backend itself persists nothing.
- **Pre-existing containers.** Containers created by the old (label-only) code
  before the upgrade are not in the store, so the first `List` won't see them.
  This is benign in practice because the container name `gritz-{taskID}` is
  deterministic: the next `Start` for such a task calls `Launch` with no `reuse`,
  hits the existing name, adopts the running container, and the runner records it
  — self-healing the gap. (See the first trade-off for the general statement.)

## Trade-offs

**Tags are informational, so a wiped state dir loses live sandboxes.** Because
nothing is ever discovered from labels/tags, a *fresh* runner with an empty or
deleted `tasks/` directory will **not** rediscover sandboxes that a prior
instance started on a shared daemon/account. This is the cost of decision #2 and
it is **accepted**: the state dir is persistent across same-host restarts (it
lives on the host, not in the sandbox), which covers the real recovery case (a
runner process restart re-reads its own `tasks/`). Cross-host failover or a
deliberately wiped state dir is **out of scope** — recovering that would require
re-opening tag-based discovery, which is exactly what this change removes. (An
optional boot-time reseed is listed as an Open Question.)

**Create-then-record orphan gap.** There is a window between creating a sandbox
and writing its record. A crash in that window orphans an untracked sandbox. How
exposed a backend is depends on whether its handle is deterministic:

- **Docker is self-protected.** The container name `gritz-{taskID}` is
  *deterministic*. A lost store-write after `Launch` created the container means
  the next `Start` calls `Launch` again (with no `reuse` handle, since the record
  was lost) and hits a **name conflict**, which *surfaces* the orphan instead of
  silently spawning a duplicate. `Launch` resolves the conflict by adopting the
  existing container (inspect by name, return its id) and the runner re-records
  it, turning the gap into a self-heal. This is why the Docker adoption in this
  change is safe even though the runner's store-write can lag the create.
- **A microVM backend will not be self-protected.** Its id is **AWS-assigned**
  and only known *after* `RunMicrovm` returns, so `Launch` can only return the
  handle once the VM exists and the runner's record is necessarily written
  *after* that. A crash between `Launch` returning the handle and the runner's
  store-write succeeding orphans a running, untracked microVM — and with tags no
  longer a discovery fallback (precisely the open question the microvm proposal
  raises), nothing will ever find it again. The only backstop is
  `max_duration_seconds` (≤ 8 h, surfaced as billing): the VM self-expires. That
  backend will lean on `max_duration` as the reaper. This is called out here
  because it is a direct consequence of decision #2 that the microvm backend
  inherits when it adopts the store — not a property of the Docker change.

**`RepairNetworks` is not removed by this change.** It would be tempting to read
this as "stop using the daemon as truth ⇒ delete the messy Docker reconcile
code." But `RepairNetworks` exists because the Docker backend **reuses** a task's
container across restarts (to preserve its filesystem), and a reused container's
network endpoint id drifts after `docker compose down && up`. That complexity
comes from container *reuse*, not from label-based discovery, so it is orthogonal
and stays exactly as is. This change removes label *parsing*, not container
reuse.

**A single flat state directory.** The store is one flat `tasks/<id>.json`
directory, not partitioned by backend. Task ids are globally unique, so there is
no id collision to disambiguate and no reason to namespace the path by backend
(or to carry a `<backend>/` path segment). The `Record.Backend` field stays in
the record as informational metadata — which backend produced the handle — but it
is not a directory key.

## Open Questions

1. **Boot-time tag reseed as an opt-in recovery path.** Should the shared store
   optionally **seed/repair itself from runtime tags at startup** — re-opening
   the discovery fallback *only at boot* — to cover the wiped-dir / cross-host
   case (and, for a microVM backend, the orphan gap), while staying store-only
   during steady-state? This would be a single `reseed()` that lists by tag once
   on start and writes any missing records, then never consults tags again. It
   restores resilience at the cost of re-introducing the tag dependency the
   design just removed (and needs a tag→handle reconstruction per backend).
   Recommendation: ship **store-only** first; add reseed behind a flag if the
   orphan/failover cases prove painful in practice.

2. **Residual `Backend` interface signature details.** The *shape* is decided —
   the runner owns the store and the interface is the handle-oriented
   Launch/Probe/Signal/Destroy/Watch quartet above (the former "keep the current
   interface" option is rejected per maintainer direction). What remains is the
   exact signatures:
   - **`reuse *Handle` on `Launch` vs. a separate `Adopt`.** Folding "create
     fresh" and "reuse/cleanup the prior sandbox" into one `Launch(spec, reuse)`
     keeps the runner's `Start` to a single call, but overloads `Launch` with two
     intents. The alternative is an explicit `Adopt(handle) (State, error)` the
     runner calls before deciding to reuse. Recommend the `reuse` parameter — it
     matches both Docker reuse and the microVM stale-cleanup with one method.
   - **Single `Probe` vs. batched `ProbeAll`.** Ship `Probe(h) (State, error)` as
     the floor; add `ProbeAll([]Handle) ([]State, error)` if/when a list-based
     backend (microVM) makes N-probes-per-`List` a measurable cost. The runner's
     `List` can prefer `ProbeAll` when the backend implements it.
   - **Should the runner pass the tracked id-set into `Watch`?** The id-based
     matching itself is **decided**: `Watch` emits `HandleExit{ID, ExitCode}` and
     the runner resolves via `store.ByID` (decision 4). What's left is purely a
     poll-efficiency question — should the runner hand a polling backend the set
     of ids it currently tracks so it can narrow its `ListMicrovms` scope, or
     should the backend keep emitting every terminal id it sees and let the runner
     filter? Leaning to the latter (keeps the backend stateless) unless poll cost
     argues otherwise.

3. **Coordination with the in-flight microvm backend (#1054).** Since this lands
   first, #1054 will be updated to use `internal/runner/taskstate` with a
   `{microvm_id, ...}` handle and drop its standalone
   `backend/lambdamicrovm/taskstate` package. Its on-disk path
   (`tasks/<id>.json`) is unchanged; the record becomes
   `{TaskID, Backend, ID: microvmID, Data: {image_arn, stage_bucket, stage_key}}`.
   Should that conversion happen *in* #1054 (rebased
   on this), or as a follow-up immediately after both land? No production microvm
   store exists yet, so there is no data migration either way — purely a
   merge-ordering choice for the maintainer.
