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
and serves both the task records and the event outbox; there is no separate
task store or outbox store type.

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

CREATE TABLE outbox (
    seq     INTEGER PRIMARY KEY AUTOINCREMENT,  -- AUTOINCREMENT: a seq is never reused, even after a drop
    payload BLOB    NOT NULL,
    dead    INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX outbox_live ON outbox (seq) WHERE dead = 0;
```

### Task records

The `internal/runner/taskstate` package is deleted. `taskstate.Record` moves to
`runnerstore.TaskRecord` with the same fields, and the four store operations
become `Store` methods with a `Task` suffix, since the same type also carries
the outbox methods. `TaskManagerOptions.Store` and `TaskRunnerOptions.Store`
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
`database/sql`, the SQLite driver and `internal/x/outbox`, and no backend code.
A failed `WriteTask` still ends the runner through `TaskFatalEvent`
(`taskrunner.go`); only the error source changes.

### outbox

`internal/x/outbox` does not change: the `Store` interface, the `Outbox[T]`
engine and `FileStore` all stay as they are, so the package remains available
to other callers (for example the driver). `runnerstore.Store` implements
`outbox.Store` directly, and the runner passes it as
`RunnerEventOutboxOptions.Store`.

| Method          | Query |
|-----------------|-------|
| `Append`        | `INSERT INTO outbox (payload) VALUES (?)` |
| `Peek`          | `SELECT seq, payload FROM outbox WHERE dead = 0 ORDER BY seq LIMIT 1` |
| `Drop(false)`   | `DELETE FROM outbox WHERE seq = (SELECT min(seq) FROM outbox WHERE dead = 0)` |
| `Drop(true)`    | `UPDATE outbox SET dead = 1 WHERE seq = (SELECT min(seq) FROM outbox WHERE dead = 0)` |
| `Len`           | `SELECT count(*) FROM outbox WHERE dead = 0` |

There is no in-memory mirror; the partial index makes the head lookup cheap,
and the queue only holds events that have not yet reached the server. The
`outbox` table sits next to the migrations that create it, and
`internal/x/outbox` stays stdlib-only with no knowledge of SQL.

The runner stops using `outbox.FileStore`; the type itself is kept.

### Importing existing state

Existing runners have JSON files on disk. For one release, `gritz runner` takes
a hidden `--import-state-dir` flag, defaulting to the old `--state-dir` default
(`/var/lib/gritz/tasks`). On startup, if that directory exists:

1. Decode every `<id>.json` task record and every live `<seq>.json` outbox
   record. Both formats are a single JSON object, so `Import` reads them
   directly and does not depend on the deleted `taskstate` package. Dead-lettered
   outbox records are not imported; they remain in the renamed directory.
2. Insert them into the database in one transaction, keeping outbox `seq`
   values so order is preserved.
3. Rename the task directory to `tasks.imported` and the outbox directory to
   `outbox.imported`, so the import runs once and the files remain for a
   rollback.

The release after that deletes `Import` and the flag.

### API

```go
package runnerstore // github.com/gritzapp/gritz/internal/runner/runnerstore (new)

// Store is the runner's durable state: task records and the event outbox.
type Store struct{ ... }

// Open opens the SQLite database at path, creating it if needed, and applies
// pending migrations.
func Open(path string) (*Store, error)

// Close closes the database.
func (s *Store) Close() error

// Import moves the legacy file-backed task records and outbox under taskDir
// and outboxDir into the database, then renames both directories. It does
// nothing when taskDir does not exist.
func (s *Store) Import(taskDir, outboxDir string) error

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

// Append durably appends payload to the outbox tail.
func (s *Store) Append(payload json.RawMessage) error

// Peek returns the outbox head without removing it.
func (s *Store) Peek() (outbox.Record, bool, error)

// Drop removes the outbox head, marking it dead instead when dead is true.
func (s *Store) Drop(dead bool) error

// Len reports the number of live outbox records.
func (s *Store) Len() (int, error)
```

```go
package taskstate // github.com/gritzapp/gritz/internal/runner/taskstate (removed)
```

```go
package runner // github.com/gritzapp/gritz/internal/runner

type RunnerEventOutboxOptions struct {
	Store    outbox.Store // now required
	StoreDir string       // removed
	// Client, Backoff, Log unchanged
}
```

**How it fits:** `internal/command/runner.go` calls `runnerstore.Open` with
`--state-db`, then `Import`, and passes the one `*runnerstore.Store` as both
`TaskManagerOptions.Store` and `RunnerEventOutboxOptions.Store`, closing it on
exit.
`internal/runner/testrunner` opens a database in `t.TempDir()` the same way;
tests that inject failures with `outbox.StoreMock` are unchanged.

## Implementation Plan

1. **runnerstore package** — Delivers: `Store`, `Open`, `Close`, connection pragmas,
   embedded migrations with `user_version`, `0001_init.sql`; the driver becomes
   a direct dependency. Depends on: nothing. Verifiable by: tests that open a
   fresh file, reopen it (migrations not reapplied), and check the tables exist.
2. **Outbox methods** — Delivers: `Append`, `Peek`, `Drop` and `Len` on
   `Store`, satisfying `outbox.Store`. Depends on: (1). Verifiable by: tests
   mirroring the `FileStore` behaviour tests (FIFO order, dead-lettering, seq
   never reused, reopen keeps records) against `Store`.
3. **Task methods** — Delivers: `TaskRecord` and `WriteTask`, `ReadTask`,
   `RemoveTask`, `ListTasks`. Depends on: (1). Verifiable by: the existing
   `taskstate` tests ported to `Store`.
4. **Runner wire-up and import** — Delivers: `--state-db`, `Store.Import`,
   `--import-state-dir`, the runner and its tests moved to
   `runnerstore.Store`/`TaskRecord`, the `taskstate` package deleted,
   `RunnerEventOutboxOptions.StoreDir` removed, `testrunner` on SQLite,
   `docker-compose.dev.yml` env updated.
   Depends on: (2), (3). Verifiable by: runner tests pass; an import test that
   lays out legacy files, runs `Import`, and checks the rows and renamed
   directories; running the dev compose stack across a restart with a sandbox
   up.
5. **Remove the import** (next release) — Delivers: deletion of
   `Store.Import` and `--import-state-dir`. Depends on: (4) having shipped in
   a release. Verifiable by: build and tests pass.

## Trade-offs

- **SQLite only vs. SQLite and Postgres.** Postgres support would let a runner
  keep no local disk, but it means two schemas, two query sets (sqlc generates
  per engine) and a dialect switch, for state that only one runner process ever
  reads. SQLite only keeps the deployment to one binary and one volume.
- **Server-side taskstate** (`proposals/draft/server-side-taskstate.md`) moves
  the task records to the server's Postgres for durability across a runner's
  disk. This proposal keeps the runner the owner of its own state on its own
  disk. The two are alternatives for the task records; if that proposal is accepted
  later, the outbox half of this one still applies.
- **ncruces/go-sqlite3 vs. modernc.org/sqlite vs. mattn/go-sqlite3.** mattn
  needs cgo, which complicates the cross-compiled prebuilt binaries. modernc is
  also cgo-free; ncruces is chosen because it is already in the module graph.
- **Hand-written `database/sql` vs. sqlc.** There are nine short queries, so
  hand-written queries avoid a second sqlc config and generated package. If the
  schema grows, a second `sql:` entry with `engine: "sqlite"` can be added
  later.
- **Hand-rolled migrations vs. dbmate.** dbmate's SQLite driver needs cgo. A
  `user_version` loop over embedded files is about 30 lines.
- **Importing vs. requiring a drained runner.** Requiring operators to stop all
  tasks and delete the state directory before upgrading would avoid the import
  code, but running sandboxes would be orphaned from their records and queued
  events lost. The import is kept for one release only.
- **One shared database vs. a file per store.** One file keeps both stores on
  the same volume by construction and allows a future change to write a task
  record and an outbox event in one transaction. This proposal does not add
  such transactions; both store interfaces stay as they are.

## Open Questions

- Should dead-lettered outbox rows be kept forever (as `dead/` files are today)
  or pruned after some age?
- Is one release enough for the legacy import, or should it stay until a
  specific version?
- Should `--state-dir` be kept as a deprecated alias that maps to
  `<state-dir>/../runner.db`, so existing `GRITZ_STATE_DIR` settings keep
  working without a config change?
