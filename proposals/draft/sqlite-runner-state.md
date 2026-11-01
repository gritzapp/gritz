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

`runnerdb.Open` opens the file and sets, on every connection:

```sql
PRAGMA journal_mode = WAL;
PRAGMA synchronous = FULL;   -- fsync the WAL on every commit: same durability as today's per-write fsync
PRAGMA busy_timeout = 5000;
```

It sets `db.SetMaxOpenConns(1)`. Both stores already serialize all access behind
a mutex, and the runner is the only process that opens the file, so a single
connection loses nothing and rules out `SQLITE_BUSY` between the runner's own
connections.

### Schema and migrations

The schema lives in `internal/runner/runnerdb/migrations/` as numbered `.sql`
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

### taskstate

`taskstate.Store` keeps its method set, so `TaskManager`, `TaskRunner` and the
tests' `m.Store.Write/List` call sites do not change. Only construction changes:
`Open(dir)` becomes `New(db)`.

| Method   | Query |
|----------|-------|
| `Write`  | `INSERT INTO task_records (...) VALUES (...) ON CONFLICT (task_id) DO UPDATE SET ...` |
| `Read`   | `SELECT ... FROM task_records WHERE task_id = ?` |
| `Remove` | `DELETE FROM task_records WHERE task_id = ?` (still a no-op when absent) |
| `List`   | `SELECT ... FROM task_records ORDER BY task_id` |

The mutex, `path`, `parseRecordName` and the stray-file handling are removed.
The package stays a leaf: it imports `database/sql` and no backend code. A
failed `Write` still ends the runner through `TaskFatalEvent`
(`taskrunner.go`); only the error source changes.

### outbox

A new `outbox.SQLStore` implements the existing `outbox.Store` interface. The
interface and the `Outbox[T]` engine do not change.

| Method          | Query |
|-----------------|-------|
| `Append`        | `INSERT INTO outbox (payload) VALUES (?)` |
| `Peek`          | `SELECT seq, payload FROM outbox WHERE dead = 0 ORDER BY seq LIMIT 1` |
| `Drop(false)`   | `DELETE FROM outbox WHERE seq = (SELECT min(seq) FROM outbox WHERE dead = 0)` |
| `Drop(true)`    | `UPDATE outbox SET dead = 1 WHERE seq = (SELECT min(seq) FROM outbox WHERE dead = 0)` |
| `Len`           | `SELECT count(*) FROM outbox WHERE dead = 0` |

There is no in-memory mirror; the partial index makes the head lookup cheap,
and the queue only holds events that have not yet reached the server. The
package stays stdlib-only, as its doc comment requires: `SQLStore` takes a
`*sql.DB` and the caller supplies the driver. `SQLStore` expects the `outbox`
table to exist; `runnerdb`'s migrations create it.

`FileStore` is deleted once the import below is removed (its only production
caller is the runner).

### Importing existing state

Existing runners have JSON files on disk. For one release, `gritz runner` takes
a hidden `--import-state-dir` flag, defaulting to the old `--state-dir` default
(`/var/lib/gritz/tasks`). On startup, if that directory exists:

1. Read every record with the current `taskstate` file code and every live and
   dead outbox record with the current `outbox.FileStore` code.
2. Insert them into the database in one transaction, keeping outbox `seq`
   values so order is preserved.
3. Rename the task directory to `tasks.imported` and the outbox directory to
   `outbox.imported`, so the import runs once and the files remain for a
   rollback.

The release after that deletes the import, the flag, the file-backed
`taskstate` code and `outbox.FileStore`.

### API

```go
package runnerdb // github.com/gritzapp/gritz/internal/runner/runnerdb (new)

// Open opens the runner's SQLite state database at path, creating it if
// needed, and applies pending migrations.
func Open(path string) (*sql.DB, error)

// Import moves the legacy file-backed task records and outbox under taskDir
// and outboxDir into db, then renames both directories. It does nothing when
// taskDir does not exist.
func Import(db *sql.DB, taskDir, outboxDir string) error
```

```go
package taskstate // github.com/gritzapp/gritz/internal/runner/taskstate

// Open returns a Store backed by dir. // removed

// New returns a Store backed by db's task_records table. // new
func New(db *sql.DB) *Store

// Write, Read, Remove and List keep their signatures. // unchanged
```

```go
package outbox // github.com/gritzapp/gritz/internal/x/outbox

// SQLStore is a Store backed by an outbox table in a SQL database. // new
type SQLStore struct{ ... }

// NewSQLStore returns a SQLStore over db's outbox table. // new
func NewSQLStore(db *sql.DB) *SQLStore
```

```go
package runner // github.com/gritzapp/gritz/internal/runner

type RunnerEventOutboxOptions struct {
	Store    outbox.Store // now required
	StoreDir string       // removed
	// Client, Backoff, Log unchanged
}
```

**How it fits:** `internal/command/runner.go` calls `runnerdb.Open` with
`--state-db`, then `runnerdb.Import`, then builds `taskstate.New(db)` for
`TaskManagerOptions.Store` and `outbox.NewSQLStore(db)` for
`RunnerEventOutboxOptions.Store`, and closes the database on exit.
`internal/runner/testrunner` opens a database in `t.TempDir()` the same way;
tests that inject failures with `outbox.StoreMock` are unchanged.

## Implementation Plan

1. **runnerdb package** — Delivers: `runnerdb.Open`, connection pragmas,
   embedded migrations with `user_version`, `0001_init.sql`; the driver becomes
   a direct dependency. Depends on: nothing. Verifiable by: tests that open a
   fresh file, reopen it (migrations not reapplied), and check the tables exist.
2. **outbox.SQLStore** — Delivers: `SQLStore` implementing `outbox.Store`.
   Depends on: (1) for the schema in tests. Verifiable by: running the existing
   `FileStore` behaviour tests (FIFO order, dead-lettering, seq never reused,
   reopen keeps records) against `SQLStore`.
3. **SQL-backed taskstate** — Delivers: `taskstate.New(db)` with the queries
   above, alongside `Open` for now. Depends on: (1). Verifiable by: the existing
   `taskstate` tests ported to `New`.
4. **Runner wire-up and import** — Delivers: `--state-db`,
   `runnerdb.Import`, `--import-state-dir`, `RunnerEventOutboxOptions.StoreDir`
   removed, `testrunner` on SQLite, `docker-compose.dev.yml` env updated.
   Depends on: (2), (3). Verifiable by: runner tests pass; an import test that
   lays out legacy files, runs `Import`, and checks the rows and renamed
   directories; running the dev compose stack across a restart with a sandbox
   up.
5. **Remove the file stores** (next release) — Delivers: deletion of
   `taskstate.Open`, the file code in `taskstate`, `outbox.FileStore`,
   `runnerdb.Import` and `--import-state-dir`. Depends on: (4) having shipped in
   a release. Verifiable by: build and tests pass.

## Trade-offs

- **SQLite only vs. SQLite and Postgres.** Postgres support would let a runner
  keep no local disk, but it means two schemas, two query sets (sqlc generates
  per engine) and a dialect switch, for state that only one runner process ever
  reads. SQLite only keeps the deployment to one binary and one volume.
- **Server-side taskstate** (`proposals/draft/server-side-taskstate.md`) moves
  the task records to the server's Postgres for durability across a runner's
  disk. This proposal keeps the runner the owner of its own state on its own
  disk. The two are alternatives for `taskstate`; if that proposal is accepted
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
