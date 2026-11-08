---
title: "Auto-Archive Tasks After Configurable Timeout"
description: "Automatically created tasks reach a terminal status and are never archived, so the runner never prunes their containers and they leak resources in proportion to workflow volume."
---

Issue: https://github.com/icholy/gritz/issues/633

## Problem

Tasks are increasingly being created automatically from workflows (Jira pollers, GitHub webhooks, routing rules, scheduled agents, parent tasks spawning children, etc.) rather than by humans in the UI. Nobody remembers to archive these because no human "owns" them — they reach a terminal status (`COMPLETED`, `FAILED`, `CANCELLED`) and then sit forever with `archived = false`.

The runner's `Prune()` loop only removes containers for **archived** tasks (`internal/runner/runner.go`). Unarchived terminal tasks therefore keep their `gritz-{task-id}` containers around in Docker indefinitely. The result is a resource leak proportional to workflow volume.

We need a per-task auto-archive timeout that lets the system reclaim these containers without anyone having to remember.

## Design

### Overview

Add a per-task `archive_after` interval and a server-side `archiver` worker. The worker scans terminal tasks whose deadline has passed and archives them through the same code path as a manual archive — same row update, same `change` notification, same downstream effects. The runner's existing `Prune()` loop then reaps the container on its next tick.

An earlier revision of this proposal tried to avoid the worker by computing "archived" at read time through a SQL helper function. That worked, but every query that touched `archived` had to be rewritten, the runner started relying on a derived value rather than a row state, and unarchive needed special handling to prevent the predicate from flipping the row straight back. A small periodic worker is the simpler shape: one writer, real row transitions, no query-site coupling.

### Database schema

New migration `20260522000001_task_archive_after.sql`:

```sql
ALTER TABLE tasks
    ADD COLUMN archive_after interval;

-- Helps the archiver's tick query find eligible rows cheaply.
CREATE INDEX idx_tasks_archive_due
    ON tasks (updated_at)
    WHERE NOT archived AND archive_after IS NOT NULL;
```

`archive_after` is a nullable `interval`:

- `NULL` — never auto-archive (current behaviour; explicit opt-out for tasks a human is following)
- non-null — eligible for auto-archive once `updated_at + archive_after < NOW()` and `status IN (COMPLETED, FAILED, CANCELLED)` with no pending command

`updated_at` is already maintained by every status transition and is the right anchor: it advances on each restart, so a re-run extends the deadline naturally. `interval` (rather than e.g. `archive_at` timestamp) is chosen because the deadline is a property of *how long the task should linger after finishing*, not a fixed wall-clock instant — restarts and command transitions reset the clock for free.

If a client doesn't specify `archive_after` at creation time, the task is created with `NULL` (never auto-archive). Callers that want auto-archive opt in per task.

The partial index keeps the worker's scan touching only rows that are candidates (not archived, with a timeout set). On a system with mostly archived history this is a small fraction of the table.

### Proto changes

In `proto/gritz/v1/gritz.proto`:

```proto
import "google/protobuf/duration.proto";

message Task {
  // ... existing fields ...
  // Auto-archive this task once it has been in a terminal status
  // (COMPLETED, FAILED, CANCELLED) for this long. Unset = never auto-archive.
  google.protobuf.Duration archive_after = 15;
}

message CreateTaskRequest {
  // ... existing fields ...
  google.protobuf.Duration archive_after = 6;
}

message UpdateTaskRequest {
  // ... existing fields ...
  // Tri-state: unset = leave alone; zero duration = clear (never archive);
  // positive = set new timeout.
  google.protobuf.Duration archive_after = 6;
}
```

### Model changes

In `internal/model/task.go`:

```go
type Task struct {
    // ... existing fields ...
    ArchiveAfter *time.Duration `json:"archive_after,omitempty"`
}
```

The existing `Archive()` helper, `applyRunnerEventStarted` cancellation behaviour, etc. are unchanged — `archived` remains a real boolean on the row, written either by a human via the API or by the archiver worker.

### Store changes

- `CreateTask` and `UpdateTask` learn to persist `archive_after`.
- `GetTask`, `ListTasks`, `ListTasksForRunner`: gain `archive_after` in their `SELECT` lists so it round-trips to clients. Their filter on `archived` is unchanged.
- New query `ListTasksDueForArchive(limit)`:

  ```sql
  -- name: ListTasksDueForArchive :many
  SELECT id, version
  FROM tasks
  WHERE NOT archived
    AND archive_after IS NOT NULL
    AND command = 0
    AND status IN (5, 6, 7)  -- COMPLETED, FAILED, CANCELLED
    AND updated_at + archive_after < NOW()
  ORDER BY updated_at
  LIMIT $1;
  ```

  Returns just `(id, version)` so the worker can perform conditional updates without a second read.

### Archiver worker

New package `internal/server/archiver`:

```go
type Archiver struct {
    store     *store.Store
    publisher Publisher
    interval  time.Duration
    batchSize int
    logger    *slog.Logger
}

func (a *Archiver) Run(ctx context.Context) error {
    t := time.NewTicker(a.interval)
    defer t.Stop()
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-t.C:
            if err := a.tick(ctx); err != nil {
                a.logger.Error("archiver tick failed", "err", err)
            }
        }
    }
}
```

Each tick:

1. Calls `ListTasksDueForArchive(batchSize)`.
2. For each `(id, version)`, runs the same transactional path as the manual archive handler (`internal/server/apiserver/task.go:ArchiveTask`): begin tx, `GetTaskForUpdate`, validate with the model helper, set `archived = true`, commit, publish the `change` notification with `Action: "archived"`.
3. Skips quietly on `ErrConcurrentUpdate` (the row was already archived or restarted between the list and the update) or `ErrNotFound`.
4. Logs how many tasks were archived for visibility.

Defaults: `interval = 1m`, `batchSize = 100`. Both exposed as `--archive-poll` and `--archive-batch` flags on `gritz server`. The 1-minute default keeps the deadline granularity well below the runner's 5-second prune cadence (so containers are reaped within ~1 minute of expiry) without putting meaningful load on the database — the partial index makes each scan a few microseconds of index-only access.

Wired up from `internal/command/server.go` alongside the HTTP server using the existing context-cancellation shutdown path: the archiver goroutine exits on `ctx.Done()`.

### Manual archive / unarchive

- **ArchiveTask** unchanged — same handler, same notification.
- **UnarchiveTask** sets `archived = FALSE` and clears `archive_after` in the same update. Without clearing the timeout, the worker would re-archive the task on its next tick. Clearing it explicitly says "I want this task back as an active item; don't auto-archive it again."

### Multi-replica considerations

The server runs as a single replica today. Two replicas running the archiver concurrently is safe in correctness terms — each archive operation uses the existing optimistic `version` check, so a duplicate attempt becomes a `ErrConcurrentUpdate` no-op — but both replicas would scan the same rows on every tick, doubling the (still tiny) DB load and producing duplicate `change` notifications until the losing tx fails. If/when a second replica is introduced, wrap the tick in a Postgres advisory lock (`pg_try_advisory_lock(...)`) so only one replica drives the scan. Out of scope for this proposal; called out so the design doesn't need rework later.

### Container cleanup

The runner's `Prune()` loop is unchanged. Because the archiver writes `archived = true` on the row, `GetTask` returns the real archived value and `Prune()` removes the container on its next 5-second tick. No runner changes.

### CLI changes

`gritz task create` and `gritz task update` accept `--archive-after <duration>` (e.g. `24h`, `30m`). Standard Go `time.ParseDuration` syntax. Omitted at create time = never auto-archive; `0` on update = clear the field.

`gritz task list` already shows enough; we don't add new columns by default.

`gritz server` gains `--archive-poll` (default `1m`) and `--archive-batch` (default `100`).

### Web UI

The task creation form gets an optional "Auto-archive after" field with a few sensible presets (1h / 24h / 7d / Never). The task detail page shows the current setting and lets it be edited inline.

The UI changes are minimal — most automated tasks set the value via the API at creation time and don't need a human to touch it.

### MCP changes

The gritz MCP server's `create_child_task` tool gets an `archive_after` parameter (string, parsed as `time.Duration`). Agents creating ephemeral helper tasks should pass a short value (e.g. `1h`) so those tasks self-clean.

The `update_my_task` tool also gets `archive_after`, so an agent that knows it's wrapping up can set a short timeout itself.

## Trade-offs

| Approach | Pros | Cons |
|----------|------|------|
| **Per-task `archive_after` + server worker** (proposed) | One writer; real row transitions; reuses the manual-archive code path including notifications; runner unchanged; query sites unchanged | Adds the first periodic background worker to the server; needs a flag for poll interval; small added duplicate-work risk under multi-replica (mitigation noted) |
| **Per-task `archive_after` + read-time predicate** | No background worker; eventually consistent for free; runner-visible value updates instantly when the deadline crosses | Every read query touching `archived` must use a SQL helper function; unarchive needs special handling to keep the predicate from flipping the row back; "archived" stops being a real row state |
| **Runner-side cleanup of unarchived tasks** | No server changes | Conflates "archived" with "cleaned up"; runner reaches into task lifecycle decisions it doesn't own; multiple runners would each need the same policy |
| **Auto-archive on terminal transition (immediately)** | Trivial implementation | Defeats the purpose of `archived = false` as a UI inbox; humans can't review failures before they vanish |
| **Hard-delete instead of archive** | Reclaims disk faster (logs, links, events go too) | Loses audit trail and breaks `parent` references from child tasks; archived rows are cheap to keep |

The worker is slightly more machinery than the read-time predicate, but it keeps `archived` as a real row state and leaves every existing query untouched. The cost of "we now run a goroutine that ticks once a minute" is small and self-contained in one new package; the cost of "every query that ever reads `archived` has to know about the predicate, and unarchive has to fight it" is spread across the codebase.

`interval` rather than `archive_at` timestamp means restarts and updates extend the deadline naturally (because `updated_at` advances), which matches the intent: "X time after the task is actually done."

## Open Questions

1. **Unarchive semantics.** Proposed: `UnarchiveTask` also clears `archive_after` so the worker doesn't re-archive the task on its next tick. Alternative: leave `archive_after` alone but reset `updated_at` (restarts the timer). Or: introduce a second column `archive_after_disabled bool` that unarchive sets. The proposed behaviour is simplest; the unarchive flow is rare enough that "you must re-set the timeout if you still want it" feels acceptable.

2. **Should child tasks inherit `archive_after` from their parent?** Right now a parent must pass it explicitly to `create_child_task`. Inheriting would mean a workflow only needs to set the timeout once at the top, which is convenient but introduces non-obvious behaviour. Likely answer: don't inherit; require explicit value, since the callers creating child tasks know their own intent.

3. **What about archived tasks with unarchived children?** A parent archived (manually or by the worker) might still have running or terminal-but-unarchived children. The current archive operation doesn't cascade. Most likely we leave this alone (children get archived independently when their own deadline passes), but should confirm there's no UI assumption that an archived parent implies archived descendants.

4. **Poll interval default.** 1 minute keeps deadline granularity well below the 5-second runner prune cadence without hammering the DB. Anything faster gives no user-visible benefit; anything slower starts to feel sluggish for short `archive_after` values like `5m`. Worth confirming before merging.
