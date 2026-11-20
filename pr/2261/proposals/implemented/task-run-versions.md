# Task Version as Run Counter (Run-Versions)

Issue: https://github.com/icholy/gritz/issues/1274

## Problem

`tasks.version` is bumped by the task state machine but has no defined
semantics. The only consumer is the guard in `ApplyRunnerEvent`
(`internal/model/task.go`), and the producers that matter bypass it: the
driver hardcodes `Version: 0` on `started`/`stopped`/`failed`
(`internal/agent/driver.go`), and the runner's `supervise` and
`failIfTaskRunning` backstops emit `failed` with version 0
(`internal/runner/runner.go`). The version's one convention — "0 overrides
anything" — is actively harmful: a version-0 `failed` from a dead run can
clobber a live one (#1052), and 0 being the Go zero value means *forgetting*
to set a version silently grants bypass.

Worse, the bump sites don't correspond to anything. `Start`, `Restart`,
`Cancel`, and the zombie-kill paths in `applyRunnerEventStarted` all bump,
so one physical sandbox run can consume several versions and a version can
change while the run it belongs to is still alive. That misalignment is what
blocks run-scoping the events (`run-scoped-runner-events.md`): the moment
events carry real versions, a bump-while-live makes the live run's terminal
event stale. Two examples:

- **Cancel**: the live driver's SIGTERM-induced `stopped` carries run N;
  `Cancel`'s bump puts the task at N+1, the `stopped` is rejected, and the
  task wedges in `Cancelling`.
- **Wake (`Start` on a running task)**: the bump puts the task at N+1 while
  run N is live, so run N's exit `stopped` is rejected and the
  `Running+start → Pending` re-queue never fires. And since each `Start()`
  bumps, k back-to-back wakes burn k versions on what ends up being a single
  follow-up run.

This proposal gives the version a single normative meaning — **the version is
the run counter** — and realigns the bump sites so the counter moves exactly
at run boundaries, never under a live run.

## Design

### 1. Version = run counter

`Task.Version` identifies the latest provisioned run:

- **`0` — never provisioned.** The task exists but no run has ever been
  queued.
- **`N ≥ 1` — run N is the latest provisioned run.** A provisioned run may
  still be cancelled before it starts; the version is never reused.

The invariant that makes run-scoped events workable: **the version never
changes while its run is live, unless the run is deliberately disowned by a
restart.** A live run's terminal event therefore always matches the current
version; a disowned or dead prior run's events are stale by construction.

### 2. Bump sites: provision-time only

The version bumps exactly when the next run is provisioned:

| Transition | Bump | Why |
|---|---|---|
| `Start()` from terminal → `Pending` | **keep** | provisions the next run now; no runner event is coming |
| `Restart()` (terminal → `Pending`, running → `Restarting`) | **keep** | provisions the replacement now; the killed run is disowned — its terminal events *should* be stale (this is today's intentional restart-flow rejection, `internal/runner/runner.go` restart branch) |
| `Start()` on `Running` | **drop** | nothing is provisioned yet — the wake is queued; `command=start` is set and the version stays with the live run |
| `applyRunnerEventStopped`: `Running`+`command=start` → `Pending` | **add** | this is the run boundary: run N's exit is what provisions run N+1. The `stopped` is scoped to N, the task is at N, it applies, and the fold bumps to N+1 with the command kept for the runner |
| `Cancel()` | **drop** | stopping the current run provisions nothing; the live driver's `stopped` (scoped N) must still apply or cancellation wedges in `Cancelling` |
| `applyRunnerEventStarted` zombie paths (archived/cancelled → `Cancelling+stop`) | **drop** | same shape: the zombie's `stopped` must land the task in `Cancelled` |

`CreateTask` (`internal/server/apiserver/task.go`) and the eventrouter's
rule-created tasks already seed `Version: 1` with `Pending+Start` — under
these semantics that reads as "creation provisions run 1", which is exactly
what happens. Version 0 has no producer today; it is the reserved value for
any future create-without-start flow, and for free it makes "has this task
ever run?" a column predicate.

**Back-to-back wakes coalesce.** Task `Running` at N; three instructions
arrive before the runner reacts. Each fold sets `command=start` idempotently;
the version stays N. Run N exits, its `stopped` applies (`Running+start →
Pending`), the fold bumps to N+1, and the runner provisions run N+1 — which
sees all three instructions. One physical run, one version, no phantom runs.
Instructions that land while the task is `Pending`/`Restarting` need no fold
at all (`CanStart` is false): the already-provisioned run will see them when
it starts.

### Relationship to `run-scoped-runner-events.md`

This proposal owns the version *semantics*; that draft owns the rejection
*machinery* (the `ApplyRunnerEvent` verdict, per-event results, ignored-event
timeline entries, driver reaction to rejection). Two amendments to it:

- **§4 (version = run identity)**: its rule kept `Start`'s bump
  unconditionally. Under this proposal, `Start` bumps only in the
  terminal→`Pending` arm; the running-task arm's bump moves to the
  `Running+start → Pending` fold in `applyRunnerEventStopped`. The rest of
  §4 (drop `Cancel` and zombie bumps) is adopted verbatim.
- **§7 (probe-driven start)**: its motivating deadlock — run N's `stopped`
  arriving stale because a wake bumped to N+1 — no longer exists, because the
  wake no longer bumps. The wake path flows through the existing
  `Running+start → Pending` re-queue as the *primary* path, not a legacy one.
  §7's probe remains useful as desync repair (a stale `Running` with no live
  sandbox), but it is no longer load-bearing for the wake flow.

The archiver's change-fence (`internal/server/archiver/archiver.go`) is the
version's only other consumer; it only lists terminal tasks and re-validates
`CanArchive` in its transaction, so the realignment does not affect it.

## Non-goals

Everything that *consumes* run identity is out of scope here: stamping
`events` rows with a run version, marking new-this-run events in
`get_my_task`, per-run timeline grouping, and per-run driver-log capture.
The realigned counter is the primitive those features would build on; none
of them are needed to fix the version itself.

## Implementation Plan

A single model-only layer: `Start()` on a running task stops bumping; the
`Running+start → Pending` fold in `applyRunnerEventStopped` bumps; `Cancel`
and the `applyRunnerEventStarted` zombie paths stop bumping; the guard is
unchanged but its semantics are documented (`N ≥ 1` scoped, `0` unscoped
bypass). Inert while all senders emit 0. Verifiable by: state machine tests —
back-to-back wake coalescing (k folds, one bump at the exit fold); versioned
cancel round-trip (cancel → `stopped` at the pre-cancel version →
`Cancelled`); restart disown (old run's versioned `stopped` rejected, new
run's `started` consumes the command); `0` bypasses; `N != version` rejected.

## Trade-offs

- **A dedicated `runs` table (or run UUID) instead of realigning `version`.**
  Cleaner identity semantics and a home for per-run metadata, but it costs a
  new table, spec plumbing through both backends, and a parallel guard in the
  state machine. After the realignment, `version` already changes at exactly
  the run boundaries, so it serves as run identity with zero new plumbing —
  and a `runs` table can be added later keyed by `(task_id, version)` without
  unwinding anything here.
- **Keeping `Start`'s bump on running tasks (the earlier draft of this
  proposal).** Rejected: it burns a version per wake on a single physical
  run, and it makes the live run's terminal event stale — the same wedge that
  forced dropping `Cancel`'s bump, which run-scoped-runner-events §7 then had
  to work around with a probe. Bumping at the exit fold keeps the counter
  aligned with physical runs and lets the `stopped` apply honestly.
- **Replacing the 0 bypass with a `-1` sentinel.** Rejected: since tasks are
  seeded at version 1 and the counter only increases, a scoped version 0 can
  never occur, so 0 is already unambiguous as "unscoped" — and switching
  sentinels would cost a two-phase rollout (accept both, migrate emitters,
  reject 0) for no semantic gain. The zero-value footgun (an emitter that
  forgets to stamp gets bypass instead of rejection) is accepted; it shrinks
  as run-scoped-runner-events moves every in-tree emitter onto real versions.

## Open Questions

- **`failed` wipes a queued wake.** `applyRunnerEventFailed` unconditionally
  clears the command, so a wake queued behind a run that dies with `failed`
  (instead of `stopped`) is lost — the follow-up run is never provisioned.
  Should `failed` with `command=start` mirror the `stopped` fold (→ `Pending`,
  keep command, bump), at the cost of auto-restarting after a failure? Or is
  the wipe correct and the loss acceptable?
- **Should `Cancel` on `Pending` un-provision the queued run?** Today it
  lands `Cancelled` directly and the provisioned version is simply never
  started. That's consistent with "version = latest provisioned run", but it
  means a version can name a run that never existed physically.