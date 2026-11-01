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

Move both file stores into `runnerstore`: two new migrations, sqlc queries and
methods on the existing `runnerstore.Store`. No second database and no new
store type. Everything about the database itself is already settled and
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

`0006_outbox.sql`:

```sql
CREATE TABLE outbox (
    seq     INTEGER PRIMARY KEY AUTOINCREMENT,  -- FIFO order; never reused, even after a drop
    payload BLOB    NOT NULL,                   -- outbox.Record.Payload (JSON model.RunnerEvent)
    dead    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX outbox_live ON outbox (seq) WHERE dead = 0;
```

### Queries

`sql/queries/sandboxes.sql`: `UpsertSandbox` (`INSERT ... ON CONFLICT
(task_id) DO UPDATE`), `GetSandbox`, `DeleteSandbox`, `ListSandboxes`
(`ORDER BY task_id`).

`sql/queries/outbox.sql`: `InsertOutbox`, `GetOutboxHead` (`WHERE dead = 0
ORDER BY seq LIMIT 1`), `DeleteOutbox` (by seq), `MarkOutboxDead` (by seq),
`CountOutbox` (`WHERE dead = 0`).

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

### Outbox

The outbox keeps its role: it is the durable queue of runner events
(started / stopped / failed) waiting to be submitted. `outbox.Outbox`, its
delivery loop, `NewRunnerEventOutbox`'s `Deliver` closure and
`isPermanentError` are unchanged. Only the `outbox.Store` behind it changes:
`Store.Outbox()` returns an `outbox.Store` whose methods run the queries above
against the same database. `Peek` reads the head row, `Drop(false)` deletes it,
`Drop(true)` marks it dead (dead rows stay, as `dead/` files do today), `Len`
counts live rows.

`outbox.FileStore` is deleted; the runner was its only user. `RunnerEventOutboxOptions.StoreDir`
goes, and `Store` becomes required. Tests keep injecting `outbox.StoreMock`
(`taskmanager_load_test.go`, `eventoutbox_test.go`).

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

// Outbox returns the runner event outbox's queue, stored in this database. // new
func (s *Store) Outbox() outbox.Store
```

```go
package taskstate // github.com/gritzapp/gritz/internal/runner/taskstate (removed)
```

```go
package outbox // github.com/gritzapp/gritz/internal/x/outbox

type FileStore struct{ ... }           // removed
func Open(dir string) (*FileStore, error) // removed
```

```go
package runner // github.com/gritzapp/gritz/internal/runner

type RunnerEventOutboxOptions struct {
	Store outbox.Store // now required
	// StoreDir removed; Client, Backoff, Log unchanged
}

// NewRunnerEventOutbox builds the outbox that delivers runner events. // no longer returns an error
func NewRunnerEventOutbox(opts RunnerEventOutboxOptions) *outbox.Outbox[model.RunnerEvent]

type TaskManagerOptions struct {
	Store *taskstate.Store // removed
	// RunnerStore is TaskLog's store, and the runner's sandbox records. Required.
	RunnerStore *runnerstore.Store
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
`rstore.Outbox()` to `NewRunnerEventOutbox`. The `TaskManager`
still owns the `taskevents.Log`. In `testrunner`, `NewStore` and
`Options.Store` go: tests seed sandboxes into the `NewRunnerStore` store with
`WriteSandbox`, and `QueueStore` defaults to that store's `Outbox()`.

## Implementation Plan

1. **Sandboxes in runnerstore** — Delivers: `0005_sandboxes.sql`, its
   queries, `Sandbox` and the four sandbox methods; the runner,
   `testrunner` and their tests on `*runnerstore.Store`; the `taskstate`
   package deleted. The runner reads `--state-db` for records and still
   accepts `--state-dir` for the outbox location. Depends on: nothing.
   Verifiable by: the `taskstate` tests ported to `runnerstore`, and the
   runner's Load/Prune tests passing unchanged in substance.
2. **Outbox in runnerstore** — Delivers: `0006_outbox.sql`, its queries and
   `Store.Outbox()`; `NewRunnerEventOutbox` takes it as a required `Store`,
   `StoreDir` removed. Depends on: (1), which owns the migration numbering
   before it. Verifiable by: `runnerstore` tests for FIFO order,
   dead-lettering, seq never reused after a drop, and events surviving a
   reopen; `eventoutbox_test.go` passing.
3. **Remove the file stores** — Delivers: `--state-dir` and `GRITZ_STATE_DIR`
   removed, `--state-db`'s usage updated, `outbox.FileStore` and its tests
   deleted, `GRITZ_STATE_DIR` dropped from `docker-compose.dev.yml`. `internal/x/atomicio` stays: it is
   still used by `internal/runner/agent/config.go` and
   `internal/runner/runrecord`. Depends on: (2). Verifiable by: the build
   with no references to `--state-dir` or `FileStore`, and a dev compose
   runner restarted with a sandbox up re-attaching it.

## Trade-offs

- **Keep the `outbox` engine vs. a runner-owned delivery loop.** Only storage
  is changing, so the engine stays and runnerstore implements its existing
  `Store` interface. Typed columns per `RunnerEvent` field would make rows
  easier to read with `sqlite3` but means rewriting the delivery loop for no
  behavioural gain.
- **`Store.Outbox()` vs. outbox methods directly on `Store`.** `outbox.Store`'s
  `Append`/`Peek`/`Drop`/`Len` are too generic to sit next to `AppendEvents` on
  `runnerstore.Store`. An unexported view over the same `*sql.DB` keeps the
  interface unchanged and the names clear.

## Open Questions

- Should dead outbox rows be kept forever, as `dead/` files are today, or
  pruned after some age?
