---
title: "Version-Stamped Runner and Driver Events"
description: "Driver and runner failure events are sent unscoped (version 0), so a failed event from a dead run can overwrite the state of a live run."
---

Issue: https://github.com/icholy/gritz/issues/1052

## Problem

`Task.Version` now has a normative meaning — it is the run counter, bumping
exactly at run boundaries and never under a live run
(`proposals/draft/task-run-versions.md`, landed in PR #1293). The guard in
`ApplyRunnerEvent` (`internal/model/task.go`) already treats a scoped version
`N ≥ 1` as "applies only to run N" and `0` as the unscoped bypass:

```go
if e.Version != 0 && e.Version != t.Version {
    return false
}
```

But the emitters that most need scoping still hardcode `0`, so the guard never
fires for them:

- The **driver** submits `started` / `stopped` / `failed` with an unset
  `Version` (`internal/agent/driver.go`) — the Go zero value, `0`, bypass.
- The **runner's** two backstops — `supervise`'s lost-report `failed` and
  `Load`'s `failIfTaskRunning` `failed` (`internal/runner/runner.go`) — emit
  `failed` with no version, also `0`.

Because these are unscoped, a `failed` from a *dead* run can still clobber a
*live* one: a boot-time `failIfTaskRunning` for run N's exited husk, or a
`supervise` `failed` for a run that was already superseded by a restart, lands
on whatever run the task is at now and flips it to `Failed`. This is the
suspected trigger of issue #1052 (the version-0 `failed` from a stale run that
put a task into a `Failed` state its live container's `started` then bounced
off).

The run-counter realignment made the version *safe* to stamp — a live run's
version never changes out from under it — but nothing yet stamps it on these
events. This proposal does exactly that and nothing more.

## Relationship to `run-scoped-runner-events.md`

That draft (issue #1052) is a two-halves design: **version scoping** (every
event carries its run's version) and **explicit rejection** (a per-event
verdict on `SubmitRunnerEvents`, ignored-event timeline entries, and a driver
that reacts to rejection). Its §4 (version = run identity) has already been
extracted and landed as the run-counter realignment (`task-run-versions.md`,
PR #1293).

This proposal extracts **only the stamping halves of its §5 and §6** — the
runner scoping its backstop events and the driver stamping its events — and
stops there. Explicitly **not** in scope, and left to that draft:

- The `ApplyRunnerEvent` bool→verdict refactor (§1).
- `RunnerEventResult` / `results` on `SubmitRunnerEventsResponse` (§2).
- Recording rejected events as `ignored` timeline entries (§3).
- The driver **reacting** to a rejection — abort-on-rejected-`started`,
  exit-code handling (§6's second half).
- The probe-driven start branch (§7).

The value of the stamping on its own: it disarms #1052's clobber trigger — a
backstop `failed` scoped to a dead run is simply dropped by the existing guard
instead of overwriting the current run — without waiting on the verdict
machinery. The cost is that "dropped" is currently *silent* (no timeline
entry, no driver reaction); closing that observability gap is the remaining
scope of `run-scoped-runner-events.md` and is deliberately left there.

## Design

No proto or wire changes. `RunnerEvent` already carries `Version`
(`internal/model/task.go`), and the two `Poll` emits that already stamp
`task.Version` — the no-sandbox `stopped` in the stop branch and the
dispatch-failure `failed` in the start/restart branches — are already correctly
scoped and stay exactly as they are. `SubmitRunnerEvents` is unchanged: it
already reads `e.Version` and calls `ApplyRunnerEvent`.

### 1. Driver stamps the run version on all three events

`Driver.Run` (`internal/agent/driver.go`) currently submits `started` *before*
calling `d.run`, which is where the existing `GetTask` lives (it is already
fetched for the shell-session fork). Reorder so the task is fetched once at the
top of the run, remember `task.Version` as this run's version, and stamp it on
`started`, `stopped`, and `failed`:

```go
resp, err := d.Client.GetTask(ctx, &gritzv1.GetTaskRequest{Id: d.TaskID})
if err != nil {
    return err // no version, no run — the runner reads the non-zero exit
}
version := resp.GetTask().GetVersion()
// ...stamp `version` on each model.RunnerEvent before submit...
```

The single `GetTask` result is reused for the existing shell-session fork, so
this is one fetch, not two. The window between the runner's launch decision and
this read is benign: a bump in that window means the run was superseded before
it started, so the versioned `started` is correctly rejected (dropped) rather
than reviving a run the server has moved past.

Note: this hoist moves the `GetTask` call ahead of the `started` submit. Today
a `GetTask` failure surfaces as a terminal `failed`; after the hoist it returns
before `started` is emitted, and the runner's `supervise` backstop (now itself
version-scoped, below) reports the lost run. This is the same "driver could not
report" bit the runner already reads from the exit code.

### 2. Runner scopes its backstop events to the run that exited

`taskstate.Record` (`internal/runner/taskstate/taskstate.go`) is a runner-local
per-task JSON file — not a wire type — so it gains a `Version` field with no
proto or compatibility concern:

```go
type Record struct {
    TaskID  int64           `json:"task_id"`
    Version int64           `json:"version,omitempty"` // task.Version at launch
    Type    string          `json:"type"`
    ID      string          `json:"id"`
    Data    json.RawMessage `json:"data,omitempty"`
}
```

`Runner.Start` already writes this record before spawning `supervise`; it
stamps `task.Version` into it. The version then flows to both backstops:

- **`supervise`** is spawned from two sites, each with the version already in
  scope — `Start` (`task.Version`) and `Load`'s re-attach path (`rec.Version`).
  Thread the version through as a parameter and emit the lost-report `failed`
  with it instead of `0`.
- **`failIfTaskRunning`** is called from `Load`, which already holds the `rec`;
  pass `rec.Version` in and emit the `failed` with it instead of `0`.

**Legacy records keep the bypass.** A record written by an older runner has no
`version` key, so it unmarshals to the zero value `0` — the existing unscoped
bypass. Those backstops behave exactly as today until the record is rewritten
by a launch under the new runner. No migration is required.

## Behavioral Consequence

There is exactly one behavioral change, and it is the point of the proposal:
**the stale guard in `ApplyRunnerEvent` becomes active for these events.**

Previously every driver and backstop `failed`/`stopped`/`started` carried `0`
and applied unconditionally. Now they carry the run's version, so:

- A backstop `failed` scoped to a **dead run N** — a `supervise` `failed` for a
  run a restart already superseded, or a boot-time `failIfTaskRunning` `failed`
  for an exited husk whose task was re-commanded while the runner was down — no
  longer matches the task's current version `N+1`. The guard **drops** it
  instead of clobbering the live run. This disarms issue #1052's suspected
  trigger.
- A backstop `failed` scoped to the **still-current run N** — a genuine lost
  report where the task is still at N — matches and applies, exactly as before.
  The backstop still does its job; it only stops firing when the run it belongs
  to has already been replaced.

A dropped event is, for now, dropped **silently**: with no verdict machinery in
this scope, there is no timeline entry recording the superseded `failed` and no
driver reaction. That is acceptable here — the alternative to a silent drop was
a silent clobber — and the observability gap (recording ignored events) is the
remaining scope of `run-scoped-runner-events.md`. Debug/observability tooling
for these drops is explicitly out of scope and left to a separate follow-up.

## Implementation Plan

Both slices depend only on the already-landed run-counter semantics (PR #1293)
and are independent of each other; either can merge first. Each is inert-safe:
a new emitter against the current guard just starts scoping correctly, and any
still-version-0 emitter continues to bypass.

1. **Runner: version-scoped backstops** — Delivers: `taskstate.Record.Version`,
   stamped in `Runner.Start`; `supervise` takes a version parameter and emits
   its lost-report `failed` with it; `failIfTaskRunning` takes the record's
   version and emits with it. Depends on: nothing beyond PR #1293. Verifiable
   by: runner tests — a `failed` for an exited run whose task was bumped to a
   newer version does not apply; a `failed` for the still-current run does; a
   legacy `Version: 0` record still bypasses.
2. **Driver: version-stamped events** — Delivers: `GetTask` hoisted to the top
   of `Driver.Run`, `task.Version` remembered and stamped on `started` /
   `stopped` / `failed`, the single fetch reused for the shell-session fork.
   Depends on: nothing beyond PR #1293. Verifiable by: driver tests against a
   fake client — all three events carry the fetched version; a stale `started`
   (task already bumped) is rejected by the server's guard.

## Trade-offs

- **Stamping without the verdict/rejection machinery.** The full
  `run-scoped-runner-events.md` design pairs scoping with an explicit per-event
  verdict, ignored-event timeline entries, and a driver that aborts on a
  rejected `started`. Shipping the stamping alone means a superseded event is
  dropped silently rather than recorded. Chosen anyway because the stamping is
  what disarms the #1052 clobber, it is a small, self-contained change, and the
  silent drop strictly improves on the silent clobber it replaces. The verdict
  half remains valuable and is left intact in its own draft.
- **Reusing `Version` as run identity instead of a run UUID.** Inherited from
  the run-counter proposal; after that realignment `Version` already changes at
  exactly the run boundaries, so no new schema or spec plumbing is needed to
  scope these events.
- **`GetTask` for the driver's version instead of a `--version` sandbox flag.**
  A baked flag would be permanently stale for containers the Docker backend
  adopts without refreshing `Cmd`/`Env`/`Files`; `GetTask` (already called)
  works uniformly across fresh and adopted sandboxes and all backends.

## Open Questions

- **Driver's exit path when `GetTask` fails before `started`.** After the
  hoist, a `GetTask` failure returns before any `started` is emitted, leaving
  the run to the runner's `supervise` backstop. Is relying on the exit-code
  signal sufficient, or should the driver still emit a `failed` in that
  narrow window? Proposed: rely on the exit code, since a failed `GetTask`
  already means the driver has no working server connection to report through.
