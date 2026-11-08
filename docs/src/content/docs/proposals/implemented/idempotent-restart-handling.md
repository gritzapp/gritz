---
title: "Idempotent restart handling"
description: "A restart command can be processed twice because it persists until the new run starts, so the second pass SIGKILLs the fresh run and wrongly fails the task."
---

Issue: https://github.com/icholy/gritz/issues/1520

## Problem

A task (1421) was marked `Failed` with reason `sandbox exited with status code 137`
even though the run actually completed successfully. The 137 was not an OOM kill or a
real failure — the runner processed a single restart command twice and the second pass
SIGKILLed the run the first pass had just launched.

The restart command is designed to survive on the server until the new run's `started`
event consumes it (`internal/runner/runner.go`). But the driver takes ~12s to boot
(dockerd under sysbox), so any poll landing in the launch→`started` window still sees
`Command=Restart` and re-runs the handler.

The root cause is that the restart handler is not idempotent. The `Start` handler
guards against a double-launch ("sandbox already running, do nothing"), but the
`Restart` handler has no equivalent guard, so its `Kill` fires against the run the
previous pass launched.

Reconstructed runner log (task 1421):

```
14:13:31 INFO killing container container=96074ccc2eec...   <- pass 1: old container already exited
14:13:31 INFO adopting existing container task=1421          <- pass 1: launches run v5
14:13:36 INFO killing container container=96074ccc2eec...   <- pass 2: kills the run it just launched
14:14:06 WARN SIGTERM timed out, sending SIGKILL
14:14:14 ERROR sandbox exited without reporting task=1421 exitCode=137
14:14:15 INFO adopting existing container task=1421          <- pass 2: relaunch (started rejected, zombie run)
```

## Design

Make the restart handler idempotent.

The taskstate record already carries the version of the run it launched
(`taskstate.Record.Version`, stamped by `Start` with `task.Version`). That is exactly
the bit needed to tell "old run to kill" from "new run already launched for this
restart":

- `Restart()` bumps `task.Version` to N when the command is issued.
- Pass 1 kills the old run (recorded version N-1) and calls `Start`, which writes a
  record stamped with version **N**.
- Pass 2 reads a record whose version already equals `task.Version` (N) — the restart
  has already been carried out, so it must **not** kill or relaunch. It waits for the
  new run's `started` to consume the command, exactly like the `Start` handler's
  already-running guard.

Change the `TaskCommandRestart` arm of `Runner.Poll` to read the record first and short
circuit when it is already on the current version:

```go
case model.TaskCommandRestart:
    g.Go(func() error {
        // Idempotency guard: if the record is already stamped with the task's
        // current version, this restart's new run has already been launched by
        // an earlier poll pass. Do nothing and wait for its "started" event to
        // consume the command — killing here would SIGKILL the run we just
        // started (issue #1520).
        rec, ok, err := r.store.Read(task.ID)
        if err != nil {
            r.log.Error("restart: failed to read task record", "task", task.ID, "err", err)
            return nil
        }
        if ok && rec.Version == task.Version {
            r.log.Debug("restart already processed, waiting for started", "task", task.ID, "version", task.Version)
            return nil
        }

        // First pass: kill the old run, then start the new one.
        if _, err := r.Kill(ctx, task); err != nil {
            r.log.Error("failed to kill task for restart", "task", task.ID, "err", err)
        }
        // ... existing sem.TryAcquire + Start + failed-on-error path unchanged
        return nil
    })
```

Notes on correctness:

- **No record / pruned sandbox** (restart of a completed/failed task whose sandbox was
  removed): `ok == false` on the first pass, so it proceeds to kill (a no-op — `Kill`
  returns `false, nil` with no handle) and start, which writes the version-N record.
  The second pass then sees version N and short circuits.
- **`Start` fails after `Kill`**: `Start` writes the record only after `Launch`
  succeeds, so on failure the record stays at N-1 and a `failed` event is enqueued. The
  `failed` fold clears the command to `None`, so no second restart pass runs.
- **Legacy version-0 records**: a record written before run-versioning unmarshals to
  version 0, which never equals a live task's version (≥1), so the guard is a no-op and
  behaviour is unchanged (kill+start) — safe.

This mirrors the existing `Start` handler and needs no schema or proto change; the
record already carries `Version`.

## Implementation Plan

1. **Idempotent restart handler** — Delivers: the version guard in the
   `TaskCommandRestart` arm of `Runner.Poll` so a duplicate restart pass is a no-op.
   Depends on: nothing (uses the existing `taskstate.Record.Version`). Verifiable by: a
   runner unit test that issues a restart, runs two poll passes within the
   launch→`started` window, and asserts exactly one kill + one launch (and that the
   task is not marked `Failed`).

## Trade-offs

- **Version guard vs. re-probing the backend.** An alternative is to have the restart
  handler probe the backend for a running sandbox (like the `Start` handler does) and
  skip the kill if one is running. That is racier and semantically wrong for restart —
  restart *wants* to kill a running old run; only a run already launched *for this
  restart* must be spared. The record version distinguishes the two precisely and with
  no extra backend round-trip, which is why the issue calls it out as the fix.
