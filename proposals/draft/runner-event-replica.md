# Runner and driver as replicas of the task event log

Issue: https://github.com/gritzapp/gritz/issues/1818

## Problem

The runner reads the current state of each task, not what changed.
`TaskManager.Poll` (`internal/runner/taskmanager.go`) calls `ListRunnerTasks`,
which returns every task with `command != NONE`, and acts on each command. A
command stays set until a later runner event (`started` / `stopped` /
`failed`) clears it in `Task.ApplyRunnerEvent`, so the runner sees the same
command on every poll until then. Much of the runner exists only to cope with
that repeated delivery:

- the restart guard that compares the record's version with `task.Version`
  (#1520);
- restart taking several poll passes: kill, `ErrRunning`, release the slot,
  retry on the next poll;
- a stop whose kill failed being retried on every poll, with no limit (#1742);
- a restart stuck in `Restarting`, waiting for a `started` that never arrives
  (#1763).

The task already has an append-only history, the `events` table, but it can't
be followed with a cursor today. `events.id` comes from a sequence and is
assigned when the row is inserted, while the row becomes visible only when its
transaction commits. Two transactions can therefore commit out of id order:

| time | Tx A (task 42) | Tx B (task 43) | reader polls | gets |
|---|---|---|---|---|
| t1 | `INSERT`, gets id 10 | | | |
| t2 | | `INSERT`, gets id 11 | | |
| t3 | | `COMMIT` | | |
| t4 | | | `id > 9` | `[11]`, cursor = 11 |
| t5 | `COMMIT` | | | |
| t6 | | | `id > 11` | `[]` |

Row 10 commits behind the cursor and is never read. The driver already has
this bug: `Driver.drainEvents` (`internal/runner/agent/driver.go`) follows
the task's stream with `Config.NextEventToken` and assumes "because event ids
are monotonic, every future injectable event has a higher id than the saved
token". Ids are monotonic in insert order, not commit order, so an
instruction committed late can be skipped and never reach the agent.

## Design

Treat the task event log as the source of truth, and make the runner and the
driver replicas of it:

```
server (Postgres events)  ──ListRunnerEvents──▶  runner (SQLite events)  ──Run stream──▶  driver (/gritz/events.jsonl)
       log of record            runner's cursor:           replica of its tasks       driver's position:        replica of one task
                                opaque token from server                              last event id it stored
```

- The **server** writes every change to a task, including every command, as an
  event, and serves events below a commit-safe watermark.
- The **runner** copies the events of its tasks into its SQLite database, in
  the order the server returns them. Its cursor is an opaque token the server
  hands back with each page, stored with that page, so the server keeps no
  state about runners and the runner never sees how the log is ordered. A new
  package, `taskevents`, owns the copying and gives each
  `TaskRunner` a per-task stream with no missing history, in order. The
  `TaskRunner` acts only on sandbox lifecycle.
- The **driver** receives its task's events over the `Run` stream, stores them
  in the sandbox, and owns all run behaviour: start, restart, stop, and the
  instructions and external events it hands to the agent.

Each hop works the same way: the reader says where it is, and the writer sends
what comes after.

### Server: a commit-safe read with an xid watermark

Each event records the transaction that wrote it:

```sql
-- migrate:up
ALTER TABLE events ADD COLUMN xid xid8 NOT NULL DEFAULT pg_current_xact_id();
CREATE INDEX idx_events_xid_id ON events (xid, id);

-- migrate:down
DROP INDEX idx_events_xid_id;
ALTER TABLE events DROP COLUMN xid;
```

Adding the column with a volatile default rewrites the table. Every existing
row gets the migration's own xid, which is below every later xid, so existing
history sorts before everything written afterwards.

`xid8` (Postgres 13+) is the 64-bit transaction id: the 32-bit `xid` plus its
wraparound count. It only increases, so `<`, `ORDER BY` and the index work.
Rows written by one transaction share an xid; `id` orders the rows within it.

The runner's read returns only rows from transactions older than the oldest
transaction still running:

```sql
-- name: ListRunnerEvents :many
SELECT e.*
FROM events e
JOIN tasks t ON t.id = e.task_id
WHERE t.runner = $1 AND t.org_id = $2
  AND (e.xid, e.id) > ($3, $4)
  AND e.xid < pg_snapshot_xmin(pg_current_snapshot())
ORDER BY e.xid, e.id
LIMIT $5;
```

`pg_snapshot_xmin` is the oldest running transaction. Every transaction below
it has committed or rolled back, so the rows below it can no longer change.
Rows from newer transactions are held back until everything older has
finished. With the example above, the poll at t4 returns nothing, because row
11's xid is not below Tx A's, and once Tx A commits the poll returns
`(A, 10), (B, 11)`.

- **Order** is the order transactions first wrote, not the order they
  committed. It is stable once rows are below the watermark, which is all a
  cursor needs.
- **Rollbacks need nothing.** A rolled-back transaction stops holding back the
  watermark when it ends, and its rows were never visible. There is no gap
  detection and no timeout.
- **Writers have no rules.** Every insert gets its xid from the column default;
  nothing takes a lock in a particular order.

### Server: an opaque cursor

The `(xid, id)` pair stays on the server. `ListRunnerEvents` is paginated with
`internal/x/pagination`, the same keyset plumbing as `ListTasks` and
`ListEventsByTask`, and the runner's cursor is its opaque page token:

- The source (`runnerEventSource`) serves only the backward (live-follow)
  walk: ascending `(xid, id)` after the token's cursor, below the watermark.
  The forward walk returns `pagination.ErrUnsupportedDirection`, the mirror
  of `ListTasksPage`, which serves only the forward walk.
- The request's `page_token` is empty for the start of the log, and otherwise
  the `next_page_token` of the previous page. The store substitutes a
  cursorless backward token for an empty one.
- The response's `next_page_token` is the page's `PrevToken`: it resumes after
  the page's last row, and an empty page returns the request's token
  unchanged. `more` reports whether another page is available now, so the
  runner pages until `more` is false.
- The token's encoding (`pagination.Token[runnerEventCursor]`, base64 JSON) is
  the server's, and the server may change it. The runner stores and returns
  the string without decoding it.

`Event` carries no xid on the wire, and neither the runner nor the driver
orders anything by it. A token names a position in the runner's own tasks'
log, and the read is scoped by the runner's identity, so a forged token can
only skip or repeat that runner's events.

### Server: command events

The runner can only replicate what is in the log. Today a command is set in
places that write no event, or an event that doesn't say so: a start through
`UpdateTask` is recorded as `Updated`, and the wake from the event router,
`CreateTask`, and the server-issued stops in `ApplyRunnerEvent` set the command
with no event at all.

A sixth event type carries the command:

```go
// model
const EventTypeCommand = "command"

// CommandPayload is the body of a command event: Task.Command changed.
type CommandPayload struct {
	Command TaskCommand `json:"command"` // start, restart, stop, none
	Version int64       `json:"version"`
	Runner  string      `json:"runner"`
}
```

Every site that changes a task's command writes a command event in the same
transaction, next to the lifecycle event it already writes, the same way
lifecycle events are written. A model method, shaped like
`RunnerEvent.LifecycleEvent`, builds it:

```go
// CommandEvent returns the command event for a change from `from` to
// t.Command, and false when the command didn't change.
func (t *Task) CommandEvent(from TaskCommand) (*Event, bool)
```

Each site snapshots the task with `from := task.Clone()` before the mutation
(`Start`, `Restart`, `Cancel`, `ApplyRunnerEvent`, the event-router wake) and
passes `from.Command`; a site that creates a task with a command passes
`TaskCommandNone`. The store is unchanged. Two cases fall out of the rule:

- **Withdrawals.** Cancelling a `PENDING` task writes a `none` command event
  for that version, so a runner that already acted on the start learns it was
  withdrawn.
- **Clears.** When a runner event consumes the command, a `none` event is
  written too.

Lifecycle events don't change; they remain the history users see. A restart
writes both. `ListEventsByTask` callers that show history (the web UI
timeline, `get_my_task`) filter command events out.

### Runner: `taskevents`, a per-task stream over a replica

Replication is hidden from the rest of the runner. A new package,
`internal/runner/taskevents`, copies the runner's events from the server into
SQLite and exposes each task's events as a stream. A `TaskRunner` reads one
task's stream and never sees the cursor or duplicates.

`taskevents` guarantees:

- **A stream has the task's whole history.** `Log` replicates from an empty
  cursor, so by the time a task's first event is stored, nothing earlier is
  missing. The stream keeps growing for as long as the task exists.
- **Positions are event ids.** A reader keeps its own position as an
  `events.id` and passes it to `Read`. How an id maps to a place in the log
  stays inside `taskevents`. Ids are stable across a rebuilt runner database,
  so the driver's `Hello.last_event_id` goes straight into `Read`.
- **`Read` is stateless.** Several readers can follow one stream from different
  positions. The `TaskRunner` folds from its own position, and the driver
  stream forwards from the driver's.
- **`Read` blocks** until there is at least one event after the position, and
  wakes when new events for the task are stored.

`Log.Tasks()` is how the runner learns which tasks it should have a
`TaskRunner` for, so there is no second source of truth such as
`ListRunnerTasks`. `Log` folds each task's command events and archive
lifecycle events (`Archived`, `AutoArchived`, `Unarchived`) into a
`TaskChange{ID, Command, Version, Archived}`: the task's full state, not a
delta.

- **After each stored page**, every task with a command or archive event in
  it is reported with its state after the page. `TaskManager` creates a
  `TaskRunner` for a task it doesn't have that isn't archived, and drops the
  `TaskRunner` of an archived one.
- **At boot**, `Log` reports every stored task that isn't archived. Archived
  tasks get no `TaskRunner`.
- **Reports may repeat** the state a task is already in (a boot report of a
  task the consumer already has, a page with a command that doesn't change
  it), so the consumer applies them idempotently.

An archived task's events are still replicated and stored, so unarchiving
needs no fetch and the cursor stays simple.

**Bootstrap** is the same loop from an empty database: the cursor starts
empty and the runner replicates the whole history of its tasks, archived
ones included. There is no snapshot and no separate fill-in path.

**No compaction.** Replicated events are never deleted. The row any reader
last saw is always there for `Read` to resume from.

**No task reassignment.** `tasks.runner` is set when the task is created and
never changes. `ListRunnerEvents` joins on it, so a task's whole history goes
to one runner.

#### Inside `taskevents`

The replica lives in `runnerstore` (`internal/runner/runnerstore`), the
runner's SQLite database. `internal/x/sqlite` opens it (WAL, `synchronous =
FULL`) and applies the schema from migrations embedded in the package;
queries are generated with sqlc.

The `events` table has one row per replicated event, in the order it arrived,
with the event's `id`, `task_id` and the event, protobuf-encoded. `seq` is the
local position: arrival order is the server's log order, so `seq` increases
along the log with no gaps. A single-row `replication` table holds the
cursor. The `tasks` table projects each task's last command and archive
state from its events, so `Tasks()` reports and boot need no fold over the
log.

```sql
CREATE TABLE events (
    seq     INTEGER PRIMARY KEY,  -- local arrival order
    id      INTEGER NOT NULL UNIQUE,
    task_id INTEGER NOT NULL,
    event   BLOB    NOT NULL      -- gritz.v1.Event, protobuf-encoded
);
CREATE INDEX events_task_seq ON events (task_id, seq);

CREATE TABLE replication (
    id     INTEGER PRIMARY KEY CHECK (id = 1),
    cursor TEXT NOT NULL          -- opaque; ListRunnerEventsResponse.next_page_token
);

CREATE TABLE tasks (
    id       INTEGER PRIMARY KEY,
    command  INTEGER,             -- gritz.v1.TaskCommand of the last command event
    version  INTEGER,             -- version of the last command event
    archived INTEGER              -- result of the last archive event; NULL reads as not archived
);
```

**Replication.** `Log.Run` is a loop that replaces `Poll`:

1. At boot, report every stored task that isn't archived on `Tasks()`.
2. Read the cursor with `EventReplicationCursor` (empty when none is stored).
3. Call `ListRunnerEvents(page_token: cursor)`.
4. Store each non-empty page with `AppendEvents`: in one transaction, insert
   the page's events, upsert the `tasks` row of every task with a command or
   archive event in it, and set `cursor` to the response's `next_page_token`.
5. Repeat while the response's `more` is true, then wait for a wake or the
   poll interval (30s by default). The runner SSE notification
   (`proposals/implemented/runner-sse-events.md`) triggers a sync sooner.
   The interval is not only a fallback for a lost notification: `more ==
   false` means nothing is readable right now, not that the replica is
   caught up. The server's xid watermark can hold committed events back and
   release them later with no notification, and the interval picks them up.

A failed fetch is logged and retried at the next wake or interval; a store
error ends `Run`.

The events, the task projection and the cursor commit together, so the cursor
never runs ahead of the stored events and no stored event sits behind a stale
cursor. A crash before the commit loses all three, and the next fetch repeats
the page. Every fetched event is stored, including ones the runner has no use
for.

After storing a page, `Log` wakes the blocked `Read`s of the tasks in it and
sends the `TaskChange`s `AppendEvents` returned on `Tasks()`. The upsert
returns each touched task's whole row, so a report is the task's full state
whatever the page held: a command-only page keeps the archive state, and an
archive-only page keeps the command.

`Stream.Read(after)` reads:

```sql
SELECT e.event FROM events e
WHERE e.task_id = ?
  AND (? = 0 OR e.seq > (SELECT a.seq FROM events a WHERE a.id = ?))
ORDER BY e.seq;
```

Position `0` returns the task's whole log. An `after` that isn't stored yet
returns nothing rather than the whole log, so a position that outlives the
database (a driver's last event id after the runner's database was rebuilt)
resumes once that event is replicated. When there is nothing to return,
`Read` waits for the next page with an event for the task. It takes that
wake-up signal before reading, so a page stored between the read and the
wait isn't missed.

### Runner: `TaskRunner` acts on its stream

**A fold, not a queue.** A `TaskRunner` doesn't track which events it has
applied. It reads its stream from `0` and folds command events into the
state it wants, and drives the sandbox toward that state. Folding gives the
same result however often it runs, so a crash between storing and acting
needs no recovery: on boot, each `TaskRunner` folds its stream again.

The `TaskRunner` acts only on sandbox lifecycle:

| Folded state for the task | `TaskRunner` does |
|---|---|
| Latest command is `start` / `restart` for version *v*, and the driver has not reported run *v* ended | Ensure the sandbox is up (acquire a slot, create or boot), and keep a `Run` stream open to its driver |
| Latest command is `stop`, and no sandbox is up | Enqueue `stopped` for the version itself, as `Poll` does today, since no driver exists to report it |
| The driver reported the run ended and no newer start is wanted | Close the stream, park the sandbox, release the slot |

Everything else (stopping a running run, restarting it, instructions) happens
in the driver. The `TaskRunner` forwards the events and the driver acts on
them. Slots stay with `TaskManager`: a `TaskRunner` that wants a sandbox asks
for one and waits until it gets it, so admission control no longer loops
through `ErrRunning`.

`Load` still reconciles sandboxes against their records at boot, and the
lost-run backstops stay.

### Driver: `Run` becomes a bidirectional stream

`Run` no longer starts anything. It opens the connection the runner forwards
the task's events over:

1. The `TaskRunner` opens the stream once the sandbox is up, and sends the
   `RunSpec` as its first message.
2. The driver sends `Hello` with the id of the last event it has stored, and
   its run record.
3. The `TaskRunner` loops on `Stream.Read(ctx, pos)`, starting from
   `hello.last_event_id`, and sends each batch as `TaskEvents`. That sends the
   events the driver is missing, then new ones as they are replicated.
4. The driver stores each event, then acts on it.

The driver's position is an `events.id`; `0` means "send everything".

**The `RunSpec` is sent on every stream.** Env, secrets, files and the token
come from the runner's workspace config, not from events, so the runner sends
them as the first message each time it opens `Run`. The driver keeps the
latest one in memory and uses it for any run it starts on that stream. The
spec arrives before any event, so the secrets are known before a run starts
and before its log ships a byte. A workspace config change reaches the driver
the next time the stream opens.

**Driver storage.** The driver appends every received event to
`/gritz/events.jsonl` and fsyncs before acting on it. Its position is the id of
the last line, read back on boot. The file lives next to `/gritz/run.json`, so
it survives a driver restart and a parked sandbox being booted again.

**What the driver does with each event:**

| Event | Driver |
|---|---|
| `command: start v` | If the run record is older than *v* and nothing is running, start run *v* |
| `command: restart v` | Stop the running run with `ErrStop`, then start run *v* |
| `command: stop v` | Stop run *v* with `ErrStop` if it is running (replaces the `Stop` RPC) |
| `command: none v` | Withdraw a start of *v* that hasn't begun |
| `instruction`, `external` | Store; rendered into the next run's prompt |
| everything else | Store only |

Commands for versions the run record has already passed are ignored, so a
resent stream (position lost, or a crash between storing and acting) is
harmless.

**The prompt reads from the local file.** `drainEvents`, the
`ListEventsByTask` walk and `Config.NextEventToken` go away. The driver keeps a
"prompted up to" id in its config and renders the instruction and external
events stored after it. That fixes the skipped-instruction bug above, because
the file's order comes from the runner's commit-safe replication.

**Runner events still go to the server directly.** The driver keeps calling
`SubmitRunnerEvents` with the task token for each `started` / `stopped` /
`failed`, as today, and records whether the terminal one was accepted in the
run record's `reported` flag. Moving runner events onto the `Run` stream is out
of scope.

**`RunEnded`** replaces `Finished`. The driver sends it once the run's process
is done and its terminal runner event has been submitted, or has failed to
submit. It tells the runner it can park, and carries `reported`: when it is
false, the runner's lost-run backstop reports the run, as it does for
`Finished.reported` today.

**Keep-alives** stay: the driver sends `KeepAlive` every 15s and
`driverclient` cancels a stream that goes quiet (`DefaultIdleTimeout`).

**The task token** is still needed by the driver for `SubmitRunnerEvents`,
log shipping (`logship`) and the agent's `gritz` MCP server, which all talk to
the server directly.

### Transport

Connect bidi streams need HTTP/2. The driver server already accepts
unencrypted HTTP/2 (`driverserver.ListenAndServe`). The Docker backend's
client transport (`docker.sandbox.Dial`) is HTTP/1.1 and needs
`Protocols.SetUnencryptedHTTP2(true)`. The Lambda MicroVM proxy supports bidi
streams, but forwards to the plaintext driver over HTTP/1.1 — on which
connect-go rejects bidi with 505 — unless the request carries
`X-aws-proxy-force-h2: true`. The backend's `tokenTransport` sets that header on
every request rather than relying on an upstream h2c connection, once opened,
carrying later requests.

### Proto

`gritz.v1`:

```proto
service GritzService {
  // ListRunnerEvents returns the events of the runner's tasks after the
  // cursor, in log order, from transactions that can no longer commit behind
  // it. Replaces ListRunnerTasks.
  rpc ListRunnerEvents(ListRunnerEventsRequest) returns (ListRunnerEventsResponse);
}

message ListRunnerEventsRequest {
  string runner = 1;
  // Opaque; empty for the start of the log, otherwise a previous
  // response's next_page_token.
  string page_token = 2;
  int32 page_size = 3;
}

message ListRunnerEventsResponse {
  repeated Event events = 1;
  // Resumes after the last event in this page; the request's page_token when
  // the page is empty. Pass it back unchanged.
  string next_page_token = 2;
  // Whether more events are available right now past this page.
  bool more = 3;
}

message Event {
  // ... existing fields
  oneof payload {
    // ... existing arms
    CommandPayload command = 10;
  }
}

message CommandPayload {
  TaskCommand command = 1;
  int64 version = 2;
  string runner = 3;
}
```

`driver.v1`:

```proto
service DriverService {
  // Run is the runner's connection to the driver. The driver sends Hello
  // first; the runner sends the task's events after its position. At most
  // one Run stream is open at a time.
  rpc Run(stream RunRequest) returns (stream RunResponse);
  // Status and Stop are removed.
}

message RunRequest {
  oneof msg {
    // The task's events, in log order.
    TaskEvents events = 3;
    // First message on every stream.
    RunSpec spec = 5;
  }
}

message TaskEvents {
  repeated gritz.v1.Event events = 1;
}

message RunResponse {
  oneof msg {
    KeepAlive keep_alive = 3;
    Hello hello = 7;
    RunEnded run_ended = 8;
  }
}

message Hello {
  // Id of the last event the driver has stored; 0 for none.
  int64 last_event_id = 1;
  optional RunRecord run = 2;
}

message RunEnded {
  int64 version = 1;
  // Whether the driver's SubmitRunnerEvents call for the run's terminal
  // event succeeded. False makes the runner report the run lost.
  bool reported = 2;
}
```

`RunRequest.version`, `Accepted` and `Finished` are removed with the
attach-or-start semantics; `RunEnded` takes `Finished`'s place. `RunSpec` itself is unchanged; it moves into the
`RunRequest` oneof as the stream's first message.

### API

```go
package model // github.com/gritzapp/gritz/internal/model

// EventTypeCommand is the discriminator of command events. // new
const EventTypeCommand = "command"

// CommandPayload is the body of a command event: Task.Command changed. // new
type CommandPayload struct {
	Command TaskCommand
	Version int64
	Runner  string
}
```

```go
package store // github.com/gritzapp/gritz/internal/store

// ListRunnerEventsParams mirrors the RPC's pagination fields. // new
type ListRunnerEventsParams struct {
	OrgID     int64
	Runner    string
	PageSize  int32  // 0 → default (200); max 1000
	PageToken string // opaque; empty for the start of the log
}

// ListRunnerEvents returns a page of the runner's tasks' events in (xid, id)
// order, below the xid watermark, walking only the backward (live-follow)
// direction. PrevToken resumes after the page; an empty page echoes the
// request's token. // new
func (s *Store) ListRunnerEvents(ctx context.Context, tx *sql.Tx, p ListRunnerEventsParams) (*pagination.Page[*model.Event], error)
```

```go
package model // github.com/gritzapp/gritz/internal/model

// CommandEvent returns the command event for a change from `from` to
// t.Command, and false when the command didn't change. // new
func (t *Task) CommandEvent(from TaskCommand) (*Event, bool)
```

```go
package runnerstore // github.com/gritzapp/gritz/internal/runner/runnerstore

// Open opens the database at path, creating it and applying the embedded
// migrations. // new
func Open(path string) (*Store, error)

// TaskChange is a task's state after a page that had a command or archive
// event for it. It may repeat the state the task was already in. // new
type TaskChange struct {
	ID       int64
	Command  gritzv1.TaskCommand // of the last command event; zero when none
	Version  int64
	Archived bool
}

// AppendEvents stores a page of replicated events, the cursor that follows
// it, and the page's effect on the tasks table in one transaction. It returns
// the state after the page of every task with a command or archive event in
// it. // new
func (s *Store) AppendEvents(ctx context.Context, events []*gritzv1.Event, cursor string) ([]TaskChange, error)

// EventReplicationCursor returns the stored replication cursor, or "" when
// no page is stored. // new
func (s *Store) EventReplicationCursor(ctx context.Context) (string, error)

// TaskEventsAfter returns the task's events after the event with id afterID,
// in log order; afterID 0 returns all of them. // new
func (s *Store) TaskEventsAfter(ctx context.Context, taskID, afterID int64) ([]*gritzv1.Event, error)

// UnarchivedTasks returns the state of every stored task that isn't
// archived. // new
func (s *Store) UnarchivedTasks(ctx context.Context) ([]TaskChange, error)
```

```go
package taskevents // github.com/gritzapp/gritz/internal/runner/taskevents  // new

// Log is the runner's replicated copy of its tasks' event logs.
type Log struct{ ... }

// Options configures a Log.
type Options struct {
	Store        *runnerstore.Store
	Client       gritzclient.Client
	RunnerID     string
	PollInterval time.Duration // 0 → 30s
	Log          *slog.Logger
}

// New returns a Log that replicates the runner's tasks from the server into
// opts.Store.
func New(opts Options) *Log

// Run reports every stored unarchived task, then replicates until ctx ends.
func (l *Log) Run(ctx context.Context) error

// Wake makes Run check the server for new events now.
func (l *Log) Wake()

// Tasks reports the full state of tasks: every unarchived stored task at
// boot, then each task with a command or archive event in a stored page. A
// report may repeat a task's current state; the consumer applies them
// idempotently.
func (l *Log) Tasks() <-chan runnerstore.TaskChange

// Stream returns the task's event stream.
func (l *Log) Stream(taskID int64) *Stream

// Stream is one task's events, in order, with no gaps.
type Stream struct{ ... }

// Read returns the events after the event with id after (0 for the start),
// blocking until at least one exists or ctx ends.
func (s *Stream) Read(ctx context.Context, after int64) ([]*gritzv1.Event, error)
```

```go
package runner // github.com/gritzapp/gritz/internal/runner

// TaskManagerOptions takes the Log the TaskManager creates TaskRunners from. // changed
type TaskManagerOptions struct {
	// ... existing fields
	Log *taskevents.Log // new
}

// Poll is removed; TaskManager creates and drops TaskRunners from Log.Tasks(). // removed

// TaskRunnerOptions takes the task's stream. // changed
type TaskRunnerOptions struct {
	// ... existing fields
	Stream *taskevents.Stream // new
}
```

```go
package eventfile // github.com/gritzapp/gritz/internal/runner/eventfile  // new

// Path is where the driver stores its task's events inside the sandbox.
const Path = "/gritz/events.jsonl"

// File is the driver's append-only copy of its task's events.
type File struct{ ... }

// Open opens the file at path, creating it if absent, and reads the last
// stored id.
func Open(path string) (*File, error)

// Append stores events and fsyncs before returning.
func (f *File) Append(events []*gritzv1.Event) error

// LastID returns the id of the last stored event, or 0.
func (f *File) LastID() int64

// After returns the stored events after id, in stored order.
func (f *File) After(id int64) ([]*gritzv1.Event, error)
```

```go
package driverserver // github.com/gritzapp/gritz/internal/runner/driverserver

// Run serves the runner's bidi stream: Hello, then stores and acts on the
// events it receives, and sends RunEnded and keep-alives. // changed
func (s *Server) Run(ctx context.Context, stream *connect.BidiStream[driverv1.RunRequest, driverv1.RunResponse]) error

// Status is removed. // removed
// Stop is removed. // removed
```

**How it fits:** the server's call sites that change a task's command write
command events, and
`ListRunnerEvents` serves them. `gritz runner` constructs a `taskevents.Log`
on the `runnerstore.Store` and runs it. The runner's SSE notification calls
`Log.Wake`. `Log.Run` pages events into the store and wakes blocked `Read`s.
`TaskManager` creates a `TaskRunner` with `Log.Stream(id)` for each active
task from `Log.Tasks()`, and drops it when the task is archived. Each `TaskRunner` folds its stream's command events and boots
or parks the sandbox with a slot from `TaskManager`. While the sandbox is up
it holds a `Run` stream, sends `Stream.Read(hello.last_event_id)`, then keeps
forwarding. `driverserver` writes each batch to `eventfile.File`, then acts
on it. The driver submits runner events to the server itself, as today, and
sends `RunEnded` up the stream when a run is over.

## Implementation Plan

1. **`events.xid`** — Delivers: the migration, and reading it in the store.
   Depends on: nothing. Verifiable by: migration up and down; store test that rows carry the inserting transaction's xid.
2. **Command events** — Delivers: `EventTypeCommand`, `CommandPayload`, the
   proto arm, `Task.CommandEvent`, and every call site that changes a task's
   command writing one; history views filter them out. Depends on: nothing.
   Verifiable by: caller tests (apiserver RPCs, event router, runner events)
   for each command path (start, restart, cancel of `PENDING`, wake,
   server-issued stop, clear, and an unchanged command writing none).
3. **`ListRunnerEvents`** — Delivers: the query and RPC, with the watermark
   and the opaque page token. Depends on: (1). Verifiable by: a store test with
   two concurrent transactions that commit out of id order; neither row is
   returned until both have committed, and then both are, in xid order; an
   empty page returns the request's page token.
4. **`taskevents`** — Delivers: `runnerstore` with its `events`,
   `replication` and `tasks` tables, `Log` replicating into them, `Tasks()`
   and `Stream.Read`. Nothing constructs a `Log` yet; `Poll` still drives
   behaviour. Depends on: (3). Verifiable by: `taskevents` tests against a
   fake server: the local events match the server's, a restart resumes from
   the stored cursor, `Tasks()` reports command, archive and unarchive
   changes, boot skips archived tasks, `Read` blocks until an event after the
   position is stored, and the poll interval picks up events with no wake.
5. **Driver HTTP/2** — Delivers: unencrypted HTTP/2 on the Docker driver
   client, and `X-aws-proxy-force-h2: true` on every Lambda MicroVM driver
   request. Depends on: nothing. Verifiable by: the existing Docker and Lambda
   MicroVM backend tests.
6. **Runner acts on events** — Delivers: `gritz runner` constructing and
   running a `taskevents.Log` on its `runnerstore`; `TaskManager` creating and
   dropping `TaskRunner`s from `Log.Tasks()` (full-state reports, applied
   idempotently); each `TaskRunner` acting on its task's latest command and
   version through the existing driver `Run` and `Stop` RPCs, so the driver
   protocol is unchanged; `Poll` removed and the runner no longer calls
   `ListRunnerTasks` (the RPC stays on the server for older runners until a
   later cleanup); a shared fake server for runner tests that serves
   `ListRunnerEvents` from an in-memory log. Depends on: (2), (4).
   Verifiable by: `runner_test` with the fake backend and the fake server for
   start, restart, stop with no sandbox, and slot exhaustion.
7. **Driver event file and bidi `Run`** — Delivers: `eventfile`, the bidi
   `Run` with the `RunSpec` as its first message, `Hello`, `RunEnded` in
   place of `Finished`, and the `TaskRunner` forwarding `Stream.Read` from the
   driver's position. The runner still issues start, restart and stop.
   Depends on: (5), (6). Verifiable by: driverserver tests; a reconnect
   resends only what the file lacks.
8. **Driver acts on commands** — Delivers: start/restart/stop driven by the
   command events on the stream; the runner stops issuing them; `Stop` RPC
   removed. Depends on: (7). Verifiable by: driverserver tests per command,
   including a resent stream that repeats commands.
9. **Prompt from the event file** — Delivers: the prompt renders from
   `eventfile`; `drainEvents` and `NextEventToken` removed. Depends on: (7).
   Verifiable by: agent prompt tests for first run and wake.

## Trade-offs

- **A per-task row lock instead of the watermark.** Lock the task row
  (`FOR UPDATE`) before inserting any event, so id order matches commit order
  within a task, and keep one cursor per task. Rejected: it only holds if every
  event insert takes the lock, and the runner would keep a cursor per task
  instead of one.
- **A sequence per runner.** A counter row per runner, incremented in each
  command write, so the runner can follow `seq > N`. Rejected: it needs a
  `runners` table and serializes command writes per runner.
- **Gap detection on `events.id`** (Marten's high water mark). Rejected: a gap
  from a rollback and a gap from a slow transaction look the same, so it needs
  either a timeout, which can skip a slow commit, or inspection of running
  sessions. The xid watermark gets that answer from Postgres directly.
- **The runner follows `(xid, id)` itself.** `ListRunnerEvents` takes and
  returns the pair, and the runner stores each event's xid and finds its
  cursor in the newest row. Rejected: it spreads the server's ordering into
  the proto, `model.Event` and the runner's schema, and every local read has
  to compare pairs. With an opaque cursor the runner sees a plain sequence.
- **A server-assigned, commit-ordered `seq`.** A sequencer numbers rows below
  the watermark under a transaction-level advisory lock, so every consumer
  could follow `seq > N`. Rejected for now: `ListRunnerEvents` becomes a write
  (or a sequencer loop has to run), all runners contend on one lock, and every
  event row is updated once. The opaque cursor allows it later without a
  runner change.
- **The server tracks each runner's position** (acked command queue).
  Rejected: the runner owns its position; the server read stays stateless.
- **The driver reads from the server itself.** The driver already has a task
  token and could follow `ListEventsByTask` with the watermark. Rejected: two
  paths into the sandbox, and the runner would still need the events to manage
  lifecycle. One stream keeps the runner as the sandbox's only peer.
- **Runner events over the `Run` stream.** The driver could send `started` /
  `stopped` / `failed` to the runner instead of calling `SubmitRunnerEvents`.
  Out of scope: without the RPC's response the driver would need acks or
  sequence numbers to know an event was stored, and the driver keeps a direct
  connection to the server for log shipping and MCP anyway.
- **Cost of the watermark.** It is cluster-wide: the oldest writing transaction
  anywhere holds back every runner. A slow migration or a session idle in a
  transaction delays delivery until it ends. `idle_in_transaction_session_timeout`
  and statement timeouts bound it. Read-only transactions have no xid and don't
  hold it back.

## Open Questions

- **Reads on a standby.** Not checked how `pg_current_snapshot()` behaves on a
  hot standby; `ListRunnerEvents` may need to run on the primary.
- **Live delivery to a running agent.** The driver now receives instructions
  while a run is active. Handing them to the agent mid-run (channels) is left
  to a follow-up.
