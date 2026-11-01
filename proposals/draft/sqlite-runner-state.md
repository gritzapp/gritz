# SQLite runner state

Issue: https://github.com/gritzapp/gritz/issues/1826

## Problem

The runner now has a SQLite database, `internal/runner/runnerstore`
(`--state-db`, default `/var/lib/gritz/runner.db`), holding its replica of its
tasks' event logs (`proposals/draft/runner-event-replica.md`). Its other
durable state is still in two hand-rolled file stores:

- `internal/runner/taskstate`: the task→sandbox-handle records
  (`taskstate.Record{TaskID, Version, Type, ID, Data}`), one atomic
  `<id>.json` file per task under `--state-dir` (default
  `/var/lib/gritz/tasks`).
- `internal/x/outbox.FileStore`: the runner event outbox, one atomic
  `<seq>.json` file per record plus a `dead/` subdirectory, in an `outbox/`
  directory derived from `filepath.Dir(--state-dir)`
  (`internal/command/runner.go`).

So the runner has three stores and two flags for one volume. The file stores
re-implement crash safety (`internal/x/atomicio` temp file + fsync + rename),
directory scans and filename parsing that SQLite already gives us, they can't
be updated together with the replica, and inspecting them means reading
directories of JSON files.

## Design

Move both file stores' data into `runnerstore`: two new migrations, sqlc
queries and methods on the existing `runnerstore.Store`. No second database and
no new store type. The runner stops using `internal/x/outbox`, which is left as
it is. Everything about the database itself is already settled and
unchanged: the ncruces driver and `Open`'s options in `internal/x/sqlite`
(WAL, `synchronous = FULL`, 5s busy timeout), the single connection, the
embedded migrations tracked in `PRAGMA user_version`, and sqlc's `sqlite`
entry in `sqlc.yaml`.

### Schema

`internal/runner/runnerstore/sql/migrations/0005_sandboxes.sql`:

```sql
CREATE TABLE sandboxes (
    task_id INTEGER PRIMARY KEY,
    version INTEGER NOT NULL,  -- task.Version of the run the sandbox is hosting; expected to go once
                               -- the driver owns its runs (runner-event-replica.md slices 7–8)
    type    TEXT    NOT NULL,  -- backend handle type; informational
    id      TEXT    NOT NULL,  -- backend handle id
    data    BLOB               -- opaque backend handle data; never decoded
);
```

`0006_runner_events.sql`:

```sql
CREATE TABLE runner_events (
    seq     INTEGER PRIMARY KEY AUTOINCREMENT,  -- FIFO order; never reused, even after a delete
    task_id INTEGER NOT NULL,
    event   TEXT    NOT NULL,                   -- model.RunnerEventType
    version INTEGER NOT NULL,
    reason  TEXT    NOT NULL,
    dead    INTEGER NOT NULL DEFAULT 0          -- 1 once dead-lettered
);
CREATE INDEX runner_events_pending ON runner_events (seq) WHERE dead = 0;
```

`model.RunnerEvent.Reconcile` has no column: the runner never sets it, so an
event read back has it false, as every event the runner sends does today.

### Queries

`sql/queries/sandboxes.sql`: `UpsertSandbox` (`INSERT ... ON CONFLICT
(task_id) DO UPDATE`), `GetSandbox`, `DeleteSandbox`, `ListSandboxes`
(`ORDER BY task_id`).

`sql/queries/runner_events.sql`: `InsertRunnerEvent`, `GetOldestRunnerEvent`
(`WHERE dead = 0 ORDER BY seq LIMIT 1`), `DeleteRunnerEvent` (by seq),
`MarkRunnerEventDead` (by seq), `CountRunnerEvents` (`WHERE dead = 0`).

### Sandboxes

The `taskstate` package is deleted. `taskstate.Record` becomes
`runnerstore.Sandbox` with the same fields, and `Write`/`Read`/`Remove`/`List`
become `Store` methods. The mutex, the per-task paths and `parseRecordName`
go with the package. Behaviour is unchanged: `RemoveSandbox` is a no-op for
an absent record, and a failed `WriteSandbox` still ends the runner through
`TaskFatalEvent` (`TaskRunner.write`). `TaskManagerOptions.Store` goes:
`TaskManagerOptions.RunnerStore`, which already holds the replica behind
`TaskLog`, serves the sandboxes too, and the `TaskManager` hands it to each
`TaskRunner` as `TaskRunnerOptions.Store`. `Load` reads `Sandboxes` where it
read `List`.

### Runner event outbox

The outbox keeps its role: it is the durable queue of runner events
(started / stopped / failed) waiting to be submitted. The runner no longer
builds it from `outbox.Outbox[model.RunnerEvent]` and a `FileStore`.
`NewRunnerEventOutbox` stays in `internal/runner` but returns a runner-owned
`*RunnerEventOutbox` over `*runnerstore.Store`, with the same delivery loop:

- `Enqueue` adds the event with `AddRunnerEvent`, then wakes `Run`
  (`internal/x/wakeup`).
- `Run` submits the oldest pending event with `SubmitRunnerEvents` until none
  is left, then waits for a wake or for ctx to end. Its first pass delivers
  whatever survived a restart.
- On success the event is deleted; on a permanent error (`isPermanentError`,
  unchanged) it is marked dead; on a transient error the loop sleeps for the
  backoff interval and retries the same event. The backoff resets when the
  queue empties. A failed store call is logged and retried after the backoff.
- `Len` counts pending events, for `testrunner`'s `Queued`/`WaitQueued`.

The engine's "undecodable payload" case goes away: rows are typed columns.

`TaskManagerOptions.Queue` becomes `*RunnerEventOutbox`; the
`m.queue.Enqueue(...)` call sites don't change.

### CLI

`--state-dir` and `GRITZ_STATE_DIR` are removed. `--state-db`'s usage drops
"keep it on the same volume as --state-dir": it is now the runner's only
state.

### Upgrading

The old `--state-dir` records and `outbox/` directory are ignored and can be
deleted.

### API

```go
package runnerstore // github.com/gritzapp/gritz/internal/runner/runnerstore

// Sandbox is the persisted task→sandbox-handle mapping. // new; was taskstate.Record
type Sandbox struct {
	TaskID  int64
	// Version is the task.Version of the run the sandbox is hosting. It is
	// expected to go once the driver owns its runs (runner-event-replica.md
	// slices 7–8).
	Version int64
	Type    string          // backend handle type; informational
	ID      string          // backend handle id
	Data    json.RawMessage // opaque, backend-defined; never decoded
}

// WriteSandbox inserts or replaces the record for rec.TaskID. // new
func (s *Store) WriteSandbox(ctx context.Context, rec Sandbox) error

// Sandbox returns the record for taskID; ok is false when there is none. // new
func (s *Store) Sandbox(ctx context.Context, taskID int64) (rec Sandbox, ok bool, err error)

// RemoveSandbox deletes the record for taskID, if any. // new
func (s *Store) RemoveSandbox(ctx context.Context, taskID int64) error

// Sandboxes returns every record, in ascending task id order. // new
func (s *Store) Sandboxes(ctx context.Context) ([]Sandbox, error)

// AddRunnerEvent appends ev to the runner event queue. // new
func (s *Store) AddRunnerEvent(ctx context.Context, ev model.RunnerEvent) error

// OldestRunnerEvent returns the oldest pending event and its seq; ok is false
// when none is pending. // new
func (s *Store) OldestRunnerEvent(ctx context.Context) (seq int64, ev model.RunnerEvent, ok bool, err error)

// DeleteRunnerEvent deletes the event at seq. // new
func (s *Store) DeleteRunnerEvent(ctx context.Context, seq int64) error

// MarkRunnerEventDead dead-letters the event at seq. // new
func (s *Store) MarkRunnerEventDead(ctx context.Context, seq int64) error

// CountRunnerEvents returns the number of pending events. // new
func (s *Store) CountRunnerEvents(ctx context.Context) (int, error)
```

```go
package taskstate // github.com/gritzapp/gritz/internal/runner/taskstate (removed)
```

```go
package runner // github.com/gritzapp/gritz/internal/runner

type RunnerEventOutboxOptions struct {
	Store *runnerstore.Store // was outbox.Store; now required
	// StoreDir removed; Client, Backoff, Log unchanged
}

// RunnerEventOutbox delivers runner events to the server in order, at least
// once. // new; replaces *outbox.Outbox[model.RunnerEvent]
type RunnerEventOutbox struct{ ... }

// NewRunnerEventOutbox returns a RunnerEventOutbox over opts.Store. // no longer returns an error
func NewRunnerEventOutbox(opts RunnerEventOutboxOptions) *RunnerEventOutbox

// Enqueue durably adds ev to the queue and wakes Run.
func (o *RunnerEventOutbox) Enqueue(ev model.RunnerEvent) error

// Run delivers queued events until ctx is cancelled.
func (o *RunnerEventOutbox) Run(ctx context.Context)

// Len returns the number of undelivered events.
func (o *RunnerEventOutbox) Len() (int, error)

type TaskManagerOptions struct {
	Store *taskstate.Store // removed
	// RunnerStore is TaskLog's store, and the runner's sandbox records. Required.
	RunnerStore *runnerstore.Store
	Queue *RunnerEventOutbox // was *outbox.Outbox[model.RunnerEvent]
	// other fields unchanged
}

type TaskRunnerOptions struct {
	Store *runnerstore.Store // was *taskstate.Store
	// other fields unchanged
}
```

**How it fits:** `internal/command/runner.go` opens the one
`*runnerstore.Store` from `--state-db` and passes it to `taskevents.New` and
as `TaskManagerOptions.RunnerStore`, as it does today, and now also as
`RunnerEventOutboxOptions.Store`. The `TaskManager`
still owns the `taskevents.Log`. In `testrunner`, `NewStore` and
`Options.Store` go: tests seed sandboxes into the `NewRunnerStore` store with
`WriteSandbox`, and `QueueStore` becomes a `*runnerstore.Store` that defaults
to the manager's `RunnerStore`. The two `taskmanager_load_test.go` tests that
make the first persisted event fail ("no space left on device") pass a
`QueueStore` they have already closed, in place of an `outbox.StoreMock`.

## Implementation Plan

1. **Sandboxes in runnerstore** — Delivers: `0005_sandboxes.sql`, its
   queries, `Sandbox` and the four sandbox methods; the runner,
   `testrunner` and their tests on `*runnerstore.Store`; the `taskstate`
   package deleted. The runner reads `--state-db` for records and still
   accepts `--state-dir` for the outbox location. Depends on: nothing.
   Verifiable by: the `taskstate` tests ported to `runnerstore`, and the
   runner's Load/Prune tests passing unchanged in substance.
2. **Runner events in runnerstore** — Delivers: `0006_runner_events.sql`, its
   queries and the five runner event methods; the runner-owned
   `RunnerEventOutbox` and its loop; `RunnerEventOutboxOptions.Store` as a
   `*runnerstore.Store`, `StoreDir` removed; `TaskManagerOptions.Queue` and
   `testrunner` on the new type; the runner's `internal/x/outbox` imports
   removed. Depends on: (1), which owns the migration before it. Verifiable
   by: `runnerstore` tests for FIFO order, delete vs. mark dead, seq never
   reused after a delete, and events surviving a reopen, with every column
   round-tripped; `eventoutbox_test.go` on a real store, checking a transient
   error is resubmitted and a permanent one is not, and that the queue drains;
   the failing-enqueue `taskmanager_load_test.go` cases on a closed store.
3. **Remove `--state-dir`** — Delivers: `--state-dir` and `GRITZ_STATE_DIR`
   removed, unused since (2), along with the runner's last references to the
   file stores (comments, and `--state-db`'s usage, which mentions it);
   `GRITZ_STATE_DIR` dropped from `docker-compose.dev.yml`.
   `internal/x/outbox` and `internal/x/atomicio` stay as they are. Depends
   on: (2). Verifiable by: the build with no references to `--state-dir`,
   and a dev compose runner restarted with a sandbox up re-attaching it.

## Trade-offs

- **A runner-owned delivery loop vs. the `outbox` engine.** Keeping
  `outbox.Outbox` would only need an `outbox.Store` over the database, but
  its queue would hold opaque JSON payloads behind a generic interface. The
  runner's queue instead sits in the same database as `handled` and
  `sandboxes`, as typed rows the runner writes itself, so a later step can
  write a handled position and the runner event it emits in one transaction.
  The cost is the engine's ~100-line loop, rewritten in `eventoutbox.go`.

## Open Questions

- Should dead `runner_events` rows be kept forever, as `dead/` files are today, or
  pruned after some age?
