# SQLite runner state

Issue: https://github.com/gritzapp/gritz/issues/1826

## Problem

The runner keeps its durable local state in two hand-rolled file stores:

- `internal/runner/taskstate` — the task→sandbox-handle mapping
  (`taskstate.Record{TaskID, Version, Type, ID, Data}`), one atomic
  `<id>.json` file per task under `--state-dir` (default
  `/var/lib/gritz/tasks`).
- `internal/x/outbox.FileStore` — the runner event outbox, one atomic
  `<seq>.json` file per record plus a `dead/` subdirectory, in an `outbox/`
  directory derived from `filepath.Dir(--state-dir)`
  (`internal/command/runner.go`).

Both re-implement the same crash-safety pattern (temp file + fsync + rename via
`internal/x/atomicio`), directory scanning and filename parsing, and the outbox
also mirrors its queue in memory and seeds its sequence counter by scanning two
directories. The outbox location is only on the same volume as the task records
by convention, the two stores cannot be updated together, and inspecting state
means reading a directory of JSON files.

The runner should stay easy to deploy: one binary, one local volume, no
database server.

## Design

Replace both file stores with a single SQLite database file owned by the
runner. SQLite only — there is no Postgres or other dialect for runner state,
so there is one schema, one set of queries, and no dialect abstraction.

### Driver

Use `github.com/ncruces/go-sqlite3` (already in `go.sum`, pulled in by sqlc).
It runs SQLite as WebAssembly through wazero, so it needs no cgo and does not
affect the prebuilt `linux/amd64` and `linux/arm64` binaries
(`mise run build`). It registers a `database/sql` driver
(`github.com/ncruces/go-sqlite3/driver`), so the stores depend only on
`database/sql`.

### Database file and connection

A new flag replaces `--state-dir`:

```
--state-db  Path to the runner's SQLite state database
            (default /var/lib/gritz/runner.db, env GRITZ_STATE_DB)
```

`docker-compose.dev.yml` already mounts the `runner_state` volume at
`/var/lib/gritz`, so the default lands on the same volume with no compose
change beyond replacing `GRITZ_STATE_DIR` with `GRITZ_STATE_DB`.

`runnerstore.Open` opens the file and sets, on every connection:

```sql
PRAGMA journal_mode = WAL;
PRAGMA synchronous = FULL;   -- fsync the WAL on every commit: same durability as today's per-write fsync
PRAGMA busy_timeout = 5000;
```

It sets `db.SetMaxOpenConns(1)`. Both of today's file stores already serialize
all access behind a mutex, and the runner is the only process that opens the
file, so a single connection loses nothing and rules out `SQLITE_BUSY` between
the runner's own connections.

### One store

`runnerstore.Store` is the runner's only durable store. It owns the `*sql.DB`
and serves both the task records and the runner event queue; there is no
separate task store or event store type.

### Schema and migrations

The schema lives in `internal/runner/runnerstore/migrations/` as numbered `.sql`
files embedded with `embed.FS`. `Open` applies them in order inside a
transaction and records progress in `PRAGMA user_version`. The server's
`dbmate` is not reused: its SQLite driver is `mattn/go-sqlite3`, which needs
cgo.

`0001_init.sql`:

```sql
CREATE TABLE task_records (
    task_id INTEGER PRIMARY KEY,
    version INTEGER NOT NULL DEFAULT 0,
    type    TEXT    NOT NULL,
    id      TEXT    NOT NULL,
    data    BLOB                      -- opaque backend handle data; never decoded
) STRICT;

CREATE TABLE runner_events (
    seq       INTEGER PRIMARY KEY AUTOINCREMENT,  -- AUTOINCREMENT: a seq is never reused, even after a drop
    task_id   INTEGER NOT NULL,
    event     TEXT    NOT NULL,                   -- model.RunnerEventType
    version   INTEGER NOT NULL,
    reconcile INTEGER NOT NULL,
    reason    TEXT    NOT NULL,
    dead      INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX runner_events_live ON runner_events (seq) WHERE dead = 0;
```

### Task records

The `internal/runner/taskstate` package is deleted. `taskstate.Record` moves to
`runnerstore.TaskRecord` with the same fields, and the four store operations
become `Store` methods with a `Task` suffix, since the same type also carries
the runner event methods. `TaskManagerOptions.Store` and `TaskRunnerOptions.Store`
become `*runnerstore.Store`, and the call sites in `TaskManager`, `TaskRunner`,
`testrunner` and the runner tests are renamed accordingly.

| Method       | Replaces            | Query |
|--------------|---------------------|-------|
| `WriteTask`  | `taskstate.Write`   | `INSERT INTO task_records (...) VALUES (...) ON CONFLICT (task_id) DO UPDATE SET ...` |
| `ReadTask`   | `taskstate.Read`    | `SELECT ... FROM task_records WHERE task_id = ?` |
| `RemoveTask` | `taskstate.Remove`  | `DELETE FROM task_records WHERE task_id = ?` (still a no-op when absent) |
| `ListTasks`  | `taskstate.List`    | `SELECT ... FROM task_records ORDER BY task_id` |

The mutex, `path`, `parseRecordName` and the stray-file handling go away with
the package. `runnerstore` keeps taskstate's leaf rule: it imports
`database/sql`, the SQLite driver and `internal/model`, and no backend code.
A failed `WriteTask` still ends the runner through `TaskFatalEvent`
(`taskrunner.go`); only the error source changes.

### Runner events

The runner event outbox no longer uses `internal/x/outbox`. The package itself
is unchanged and stays available to other callers (for example the driver);
the runner simply stops importing it.

`runnerstore.Store` stores `model.RunnerEvent` directly, one column per field,
so the queue can be read with `sqlite3` and there is no JSON payload to decode:

| Method              | Query |
|---------------------|-------|
| `AppendRunnerEvent` | `INSERT INTO runner_events (task_id, event, version, reconcile, reason) VALUES (...)` |
| `PeekRunnerEvent`   | `SELECT seq, ... FROM runner_events WHERE dead = 0 ORDER BY seq LIMIT 1` |
| `DropRunnerEvent`   | `DELETE FROM runner_events WHERE seq = ?`, or `UPDATE runner_events SET dead = 1 WHERE seq = ?` when dead |
| `CountRunnerEvents` | `SELECT count(*) FROM runner_events WHERE dead = 0` |

There is no in-memory mirror; the partial index makes the head lookup cheap,
and the queue only holds events that have not yet reached the server.

`runner.RunnerEventOutbox` (`internal/runner/eventoutbox.go`) becomes a
concrete type that owns the delivery loop the runner currently gets from
`outbox.Outbox[model.RunnerEvent]`:

- `Enqueue` calls `AppendRunnerEvent`, then wakes `Run` (`internal/x/wakeup`).
- `Run` drains from the head until the queue is empty, then waits for a wake or
  for ctx to end. The first pass delivers whatever survived a restart.
- Each event is sent with `SubmitRunnerEvents`. On success it is dropped; on a
  permanent error (`isPermanentError`, unchanged) it is dropped as dead; on a
  transient error the loop sleeps for the backoff interval and retries the same
  head. The backoff resets when the queue empties. A failed peek or drop is
  logged and retried after the backoff, as today.
- `Len` calls `CountRunnerEvents`.

The engine's "undecodable payload" case goes away: rows are typed columns, so
there is nothing to decode.

`RunnerEventOutbox` depends on a small `RunnerEventStore` interface rather than
on `*runnerstore.Store`, so the tests that inject a failing append ("no space
left on device", `taskmanager_load_test.go`) keep working with a moq mock in
place of `outbox.StoreMock`.

### Upgrading

There is no backwards compatibility with the file stores. The runner does not
read the old `--state-dir` task files or the `outbox/` directory; `--state-dir`
and `GRITZ_STATE_DIR` are removed. A runner upgraded with sandboxes still up
starts with an empty database, so it no longer tracks those sandboxes and any
undelivered outbox events are lost. Operators drain the runner (let running
tasks finish or stop them) before upgrading and can delete the old directories
afterwards.

### API

```go
package runnerstore // github.com/gritzapp/gritz/internal/runner/runnerstore (new)

// Store is the runner's durable state: task records and the runner event queue.
type Store struct{ ... }

// Open opens the SQLite database at path, creating it if needed, and applies
// pending migrations.
func Open(path string) (*Store, error)

// Close closes the database.
func (s *Store) Close() error

// TaskRecord is the persisted task→sandbox-handle mapping; Data is opaque
// backend handle data that the store never decodes.
type TaskRecord struct {
	TaskID  int64
	Version int64
	Type    string
	ID      string
	Data    json.RawMessage
}

// WriteTask inserts or replaces the record for rec.TaskID.
func (s *Store) WriteTask(rec TaskRecord) error

// ReadTask returns the record for taskID; ok is false when there is none.
func (s *Store) ReadTask(taskID int64) (rec TaskRecord, ok bool, err error)

// RemoveTask deletes the record for taskID; removing an absent record is not an error.
func (s *Store) RemoveTask(taskID int64) error

// ListTasks returns every record, ordered by task id.
func (s *Store) ListTasks() ([]TaskRecord, error)

// AppendRunnerEvent durably appends ev to the tail of the runner event queue.
func (s *Store) AppendRunnerEvent(ev model.RunnerEvent) error

// PeekRunnerEvent returns the oldest live event and its seq; ok is false when
// the queue is empty.
func (s *Store) PeekRunnerEvent() (seq int64, ev model.RunnerEvent, ok bool, err error)

// DropRunnerEvent removes the event at seq, or marks it dead when dead is true.
func (s *Store) DropRunnerEvent(seq int64, dead bool) error

// CountRunnerEvents reports the number of live events.
func (s *Store) CountRunnerEvents() (int, error)
```

```go
package taskstate // github.com/gritzapp/gritz/internal/runner/taskstate (removed)
```

```go
package runner // github.com/gritzapp/gritz/internal/runner

// RunnerEventStore is the durable queue behind a RunnerEventOutbox. // new
//
//go:generate go tool moq -out runnereventstore_moq.go . RunnerEventStore
type RunnerEventStore interface {
	AppendRunnerEvent(ev model.RunnerEvent) error
	PeekRunnerEvent() (seq int64, ev model.RunnerEvent, ok bool, err error)
	DropRunnerEvent(seq int64, dead bool) error
	CountRunnerEvents() (int, error)
}

// RunnerEventOutbox delivers runner events to the server, at least once and in
// order. // new; replaces *outbox.Outbox[model.RunnerEvent]
type RunnerEventOutbox struct{ ... }

type RunnerEventOutboxOptions struct {
	Store    RunnerEventStore // was outbox.Store; now required
	StoreDir string           // removed
	// Client, Backoff, Log unchanged
}

// NewRunnerEventOutbox returns a RunnerEventOutbox over opts.Store. // no longer returns an error
func NewRunnerEventOutbox(opts RunnerEventOutboxOptions) *RunnerEventOutbox

// Enqueue durably appends ev and wakes Run.
func (o *RunnerEventOutbox) Enqueue(ev model.RunnerEvent) error

// Run delivers queued events until ctx is cancelled.
func (o *RunnerEventOutbox) Run(ctx context.Context)

// Len reports the number of undelivered events.
func (o *RunnerEventOutbox) Len() (int, error)

type TaskManagerOptions struct {
	Store *runnerstore.Store // was *taskstate.Store
	Queue *RunnerEventOutbox // was *outbox.Outbox[model.RunnerEvent]
	// other fields unchanged
}
```

**How it fits:** `internal/command/runner.go` calls `runnerstore.Open` with
`--state-db`, passes the one `*runnerstore.Store` as both
`TaskManagerOptions.Store` and `RunnerEventOutboxOptions.Store`, starts
`go queue.Run(ctx)` as today, and closes the store on exit. `TaskManager`'s
`m.queue.Enqueue(...)` call sites do not change. `internal/runner/testrunner`
opens a store in `t.TempDir()`; its `QueueStore` option becomes a
`RunnerEventStore`, so failure-injection tests pass a `RunnerEventStoreMock`.

## Implementation Plan

1. **runnerstore package** — Delivers: `Store`, `Open`, `Close`, connection pragmas,
   embedded migrations with `user_version`, `0001_init.sql`; the driver becomes
   a direct dependency. Depends on: nothing. Verifiable by: tests that open a
   fresh file, reopen it (migrations not reapplied), and check the tables exist.
2. **Runner event methods** — Delivers: `AppendRunnerEvent`,
   `PeekRunnerEvent`, `DropRunnerEvent` and `CountRunnerEvents` on `Store`.
   Depends on: (1). Verifiable by: tests for FIFO order, dead-lettering, seq
   never reused, and reopen keeping events, with every `RunnerEvent` field
   round-tripped.
3. **Task methods** — Delivers: `TaskRecord` and `WriteTask`, `ReadTask`,
   `RemoveTask`, `ListTasks`. Depends on: (1). Verifiable by: the existing
   `taskstate` tests ported to `Store`.
4. **Runner wire-up** — Delivers: `--state-db` replacing `--state-dir`,
   `RunnerEventStore` and the concrete `RunnerEventOutbox` with its own delivery
   loop, the runner and its tests moved to `runnerstore.Store`/`TaskRecord`, the
   `taskstate` package deleted, the runner's `internal/x/outbox` import removed,
   `testrunner` on SQLite, `docker-compose.dev.yml` env updated.
   Depends on: (2), (3). The existing `eventoutbox_test.go` cases (permanent vs.
   transient errors, redelivery after restart) move to the new type. Verifiable by: runner tests pass; running the dev
   compose stack across a restart with a sandbox up.

## Trade-offs

- **SQLite only vs. SQLite and Postgres.** Postgres support would let a runner
  keep no local disk, but it means two schemas, two query sets (sqlc generates
  per engine) and a dialect switch, for state that only one runner process ever
  reads. SQLite only keeps the deployment to one binary and one volume.
- **Server-side taskstate** (`proposals/draft/server-side-taskstate.md`) moves
  the task records to the server's Postgres for durability across a runner's
  disk. This proposal keeps the runner the owner of its own state on its own
  disk. The two are alternatives for the task records; if that proposal is accepted
  later, the runner event half of this one still applies.
- **ncruces/go-sqlite3 vs. modernc.org/sqlite vs. mattn/go-sqlite3.** mattn
  needs cgo, which complicates the cross-compiled prebuilt binaries. modernc is
  also cgo-free; ncruces is chosen because it is already in the module graph.
- **Typed runner events vs. the generic `outbox` engine.** Keeping
  `outbox.Outbox[model.RunnerEvent]` would mean `Store` exposes raw-JSON
  `Append`/`Peek`/`Drop`/`Len` methods and the queue stays a payload blob. A
  runner-owned loop costs roughly the engine's ~100 lines in
  `eventoutbox.go`, in exchange for typed methods and readable rows.
- **Hand-written `database/sql` vs. sqlc.** There are only a handful of short queries, so
  hand-written queries avoid a second sqlc config and generated package. If the
  schema grows, a second `sql:` entry with `engine: "sqlite"` can be added
  later.
- **Hand-rolled migrations vs. dbmate.** dbmate's SQLite driver needs cgo. A
  `user_version` loop over embedded files is about 30 lines.
- **No migration of the file stores.** Importing the old JSON files would let
  a runner upgrade with sandboxes up, at the cost of import code, a flag and a
  follow-up release to delete them. Requiring a drained runner avoids all of
  it.
- **One shared database vs. a file per store.** One file keeps both stores on
  the same volume by construction and allows a future change to write a task
  record and a runner event in one transaction. This proposal does not add
  such transactions.

## Open Questions

- Should dead runner event rows be kept forever (as `dead/` files are today)
  or pruned after some age?
