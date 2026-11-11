# Ship driver logs to the server

Issue: https://github.com/icholy/gritz/issues/1241

## Problem

The driver tees everything it emits — its own `slog` lines, setup command
stdout/stderr, and the agent CLI's stderr — into a single append-only file at
`/gritz/log` inside the sandbox (`proposals/implemented/driver-logs-to-sandbox.md`).
That closed the most common gap (debugging a failed-but-retained run via
`gritz shell`), but the issue's literal ask is still open, as that proposal's
first Open Question records:

- The log dies with the sandbox. `Runner.Prune()` removes archived tasks'
  containers, and a backend that recreates rather than adopts destroys the
  writable layer — a pruned or recreated run cannot be debugged post-mortem
  at all.
- Reading the log requires an operator who can `gritz shell` into the sandbox
  (or `docker logs` on the runner host). The Web UI still shows only the
  one-line `Sandbox failed: …` reason and deliberate `report` events.

Nothing driver-emitted reaches the server, so nothing survives the sandbox and
nothing is viewable without shell access.

## Design

Leverage the existing `DriverLog`: its `sink` is already the single tee point
every byte flows through (`internal/agent/log.go` — the slog handler,
`Stdout()`/`Stderr()` for setup commands, and the agent CLI's stderr all write
to it). This proposal adds a second consumer next to the `/gritz/log` file: a
**log shipper** that buffers the same bytes and sends them to the server
asynchronously, one chunk per `AppendLogChunk` call, over a new RPC. The
server stores the chunks in a new `log_chunks` table. Before `Driver.Run`
returns, the driver flushes whatever is still buffered.

The `/gritz/log` file is unchanged and remains the in-sandbox copy; the server
transcript is a byte-for-byte mirror of it from the moment the shipper is
wired in. Shipping is best-effort end to end: a slow or unreachable server
never blocks a write, never fails a run, and at worst loses log bytes — never
task state.

### Database: `log_chunks`

A new migration (`internal/store/sql/migrations/`), following the `schedules`
migration shape:

```sql
-- migrate:up

CREATE TABLE log_chunks (
    id         BIGSERIAL PRIMARY KEY,
    org_id     BIGINT NOT NULL REFERENCES orgs(id)  ON DELETE CASCADE,
    task_id    BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    version    BIGINT NOT NULL,  -- the run (task.Version) the bytes belong to; 0 = pre-run preamble
    data       BYTEA  NOT NULL,  -- raw log bytes, opaque to the server
    created_at TIMESTAMP NOT NULL DEFAULT (NOW() AT TIME ZONE 'UTC')
);

-- Keyset reads per task, matching idx_events_task_id_id.
CREATE INDEX idx_log_chunks_task_id_id ON log_chunks (task_id, id);

-- migrate:down

DROP TABLE IF EXISTS log_chunks;
```

Chunks are opaque byte runs, not parsed lines. The server-assigned `id` is the
ordering: the driver ships with a single in-flight sender (below), so insertion
order matches write order within a run. `version` is stamped so a viewer can
filter or delimit runs without parsing the `==== run version=N ====` markers
(which still appear in the byte stream anyway, since `StartRun` writes through
the sink).

Deleting a task cascades its chunks. There is no other retention in v1 (see
Open Questions).

### Proto: `AppendLogChunk` and `ListLogChunksByTask`

Two new unary RPCs on `GritzService` (the codebase has no streaming RPCs;
`UploadLogs` — now a vestigial transport for the MCP `report` tool — is left
untouched):

```proto
rpc AppendLogChunk(AppendLogChunkRequest) returns (AppendLogChunkResponse);
rpc ListLogChunksByTask(ListLogChunksByTaskRequest) returns (ListLogChunksByTaskResponse);

message AppendLogChunkRequest {
  int64 task_id = 1;
  int64 version = 2; // the run these bytes belong to
  bytes data = 3;    // one chunk; the FIFO sender makes call order write order
}

message AppendLogChunkResponse {}

message LogChunk {
  int64 id = 1;
  int64 task_id = 2;
  int64 version = 3;
  bytes data = 4;
  google.protobuf.Timestamp created_at = 5;
}

message ListLogChunksByTaskRequest {
  int64 task_id = 1;
  int32 page_size = 2;    // max chunks per page (default 50, max 200)
  string page_token = 3;  // opaque bidirectional cursor (a boundary id + direction);
                          // empty returns the newest page — the end of the log
}

message ListLogChunksByTaskResponse {
  repeated LogChunk chunks = 1;  // always oldest-first (ascending id), so data concatenates
                                 // into transcript order
  string prev_page_token = 2;    // older page (scroll back); empty when history is exhausted
  string next_page_token = 3;    // newer page (live-follow); ALWAYS populated so a client can
                                 // keep polling the tail for appends
  bool more = 4;                 // whether another page exists beyond `chunks` in the direction
                                 // just walked (older for a prev_page_token request, newer for
                                 // live-follow). Break on !more instead of the
                                 // shorter-than-page_size heuristic, which misfires at an exact
                                 // page_size boundary
}
```

The response mirrors `ListEventsByTaskResponse` field for field — same names,
same numbers, same semantics — so the two paged task streams read identically
to their consumers (the CLI in slice 6, the Web UI in slice 7). Note the
deliberate inversion when mapping from the store: `pagination.Page.NextToken`
walks toward *older* history and is returned as `prev_page_token`, while
`PrevToken` walks toward *newer* rows and becomes `next_page_token`. The
`ListEventsByTask` handler already does exactly this; copy it rather than
re-deriving it.

### Server handlers

`AppendLogChunk` mirrors the `UploadLogs` handler's auth exactly
(`internal/server/apiserver/log.go`): coarse `AllowOp(OpTaskWrite)` gate,
`GetTask` by `(id, org)`, then `Allow(OpTaskWrite, task.ScopeAttr()...)` — so
the driver's narrow task JWT (which already carries `OpTaskWrite` bound to its
task) authorizes it with no scope changes. The handler validates bounds
(reject an empty chunk or one over 1 MiB with `CodeInvalidArgument` — the
driver cuts far smaller chunks, so this is an abuse cap, not tuning), then
inserts it via a new store method `CreateLogChunk(ctx, tx, chunk)`.

The handler publishes **no notification**. `UploadLogs`-driven channel
notifications were already deliberately silenced as high-frequency log spam
(`proposals/implemented/summary-gated-channel-notifications.md`); log chunks
are strictly noisier. A live-tail signal can be added later (see Open
Questions).

`ListLogChunksByTask` requires `OpTaskRead` on the task and pages with the
existing keyset machinery (`internal/pagination`, mirroring
`ListEventsByTaskPage`'s cursor over `(task_id, id)`): `Reverse: true` so an
empty token opens at the tail, bidirectional Asc/Desc queries, default page
size 50, max 200. A reader walks backward for "the end of the log" or forward
from a saved token to follow growth.

### Driver: the shipper

A new `internal/agent` type wraps the network side:

```go
// LogShipper is an io.Writer that mirrors log bytes to the server as
// asynchronous chunks, one per request. Write never blocks and never returns
// an error; a full buffer drops bytes rather than stall the run.
type LogShipper struct { ... }

func NewLogShipper(client gritzclient.Client, taskID int64) *LogShipper
func (s *LogShipper) SetVersion(version int64)
func (s *LogShipper) Run(ctx context.Context)          // background sender
func (s *LogShipper) Flush(ctx context.Context) error  // drain remaining, bounded by ctx
```

Behavior:

- **Buffering.** `Write` appends to an internal buffer (mutex-guarded, no
  channel). A chunk is cut when the buffer reaches 32 KiB (the same chunk size
  as `shell.Serve`'s PTY pump) or when a 2s ticker fires with pending bytes,
  whichever comes first — so a quiet run still ships promptly and a chatty one
  coalesces its writes into a chunk rather than a request per line.
- **Bounded, drop-on-overflow.** Pending chunks are capped at 1 MiB total.
  When the server is unreachable and the cap is hit, new bytes are dropped and
  counted; when shipping resumes, the shipper emits a synthetic
  `[gritz: dropped N log bytes]\n` chunk so the gap is visible in the
  transcript. The complete log is still on disk in `/gritz/log`.
- **Single in-flight sender.** One goroutine (started from `Run`, alongside
  the driver's existing lifetime) sends pending chunks FIFO, one
  `AppendLogChunk` call per chunk, retrying transient failures with capped
  exponential backoff (`cenkalti/backoff`, as the outbox does). Never more
  than one request in flight, so server insertion order is write order. The
  per-request overhead is paid on a keep-alive connection and is negligible
  next to a 32 KiB chunk.
- **Version stamping.** The shipper is constructed in `command/driver.go`
  (where the `gritzclient` already exists) before the task fetch, so it
  buffers from process start with `version = 0`. `DriverLog.StartRun` — which
  already receives `task.Version` — additionally calls `SetVersion`, cutting a
  chunk at the boundary so pre-run preamble and run bytes are not mixed in one
  chunk. Because a request carries exactly one chunk, no request can straddle a
  version boundary and the sender needs no splitting logic.

Wiring is one line in `OpenDriverLog`'s callers' terms: the `DriverLog` sink
becomes `io.MultiWriter(file, shipper)`. Every existing tee — slog handler,
`Sink()`, `Stdout()`, `Stderr()`, `StartRun` — ships automatically because
they all already write through the sink. `DiscardDriverLog` (tests,
directly-invoked drivers) has no shipper and behaves as today.

### Flushing before `Driver.Run` returns

At the end of `Driver.Run`, after the terminal runner event is submitted (so
the `task failed`/`agent stopped` lines are in the buffer), the driver calls
`Flush` with a short deadline (5s) on the parent event context — the same
context that survives the SIGTERM cancellation, for the same reason the
terminal events use it. Flush is best-effort: on deadline the driver logs a
warning to stderr and exits anyway. `DriverLog.Close` (already deferred in
`command/driver.go`) also flushes as a backstop for early-error exits, such as
a failed `GetTask` — though in that case the server is likely unreachable
anyway, and the bytes remain in `/gritz/log`.

### Delivery semantics

At-least-once, in order, best-effort:

- **Ordering** comes from the single in-flight FIFO sender; a failed chunk is
  retried before anything newer is sent (head-of-line blocking, like the
  outbox). Each chunk is its own statement, so the server-assigned ids are
  write order by construction, not by any assumption about how a multi-row
  insert orders its rows.
- **Duplicates** are possible only on an ambiguous failure (e.g. a timeout
  after the server committed): the retried chunk inserts the same bytes twice.
  For a diagnostic transcript this is a visible-but-harmless repeated span,
  accepted rather than engineered away (see Trade-offs).
- **Loss** is possible when the buffer overflows, the flush deadline expires,
  or the driver crashes — bounded to the unshipped tail, and always still
  recoverable from `/gritz/log` while the sandbox lives. The server copy is a
  strict improvement over today, not a replacement for the file.

### Viewing

- **CLI**: `gritz logs <task-id>` currently shells out to
  `docker logs gritz-<id>`, requiring host Docker access. It is repointed at
  `ListLogChunksByTask` — page backward from the tail, print `data` in order —
  so it works from anywhere the CLI can reach the server, for any task the
  caller can read, including pruned ones. `-f` polls forward from the saved
  `next_page_token`.
- **Web UI**: a "Logs" tab on the task page alongside Timeline and Shell,
  rendering the concatenated chunk bytes in a scrollback view. It clones the
  `useTaskTimeline` bidirectional infinite-query shape over
  `listLogChunksByTask` (open at the tail, prepend older pages on scroll-up).
  With no append notification, follow mode is poll-based, reusing the
  existing 30s backstop interval while the task is running.

## Implementation Plan

1. **Schema migration** — Delivers: the `log_chunks` table and
   `idx_log_chunks_task_id_id`. Depends on: nothing. Verifiable by: migration
   runs cleanly up and down.
2. **Store layer** — Delivers: sqlc queries and store methods `CreateLogChunk`
   and the keyset Asc/Desc pair backing `ListLogChunksByTaskPage`, with
   model↔row conversion, following `internal/store/event.go`. Depends on: (1).
   Verifiable by: store unit tests covering insert order, org scoping, cascade
   delete, and pagination in both directions.
3. **Proto + server handlers** — Delivers: the `AppendLogChunk` /
   `ListLogChunksByTask` RPCs — the latter returning the
   `ListEventsByTaskResponse`-shaped page (`chunks`, `prev_page_token`,
   `next_page_token`, `more`) — generated code, and `apiserver` handlers with
   the `UploadLogs`-shaped scope checks and size caps. Depends on: (2).
   Verifiable by: handler tests exercising auth (task-token write, user read),
   bounds rejection, round-tripping bytes through both RPCs, and the token
   direction (a follow poll on `next_page_token` must not walk back into
   existing history).
4. **Driver shipper** — Delivers: `agent.LogShipper` (buffering, chunk
   cutting, overflow drop + marker, FIFO sender with backoff, `Flush`), not
   yet wired. Depends on: (3). Verifiable by: unit tests against a fake client
   — ordering across retries, overflow accounting, flush deadline behavior.
5. **Driver wiring + flush** — Delivers: shipper construction in
   `command/driver.go`, the `io.MultiWriter(file, shipper)` sink,
   `StartRun`→`SetVersion`, and the end-of-`Run` flush. Depends on: (4).
   Verifiable by: a driver test with a dummy agent asserting the server
   received the same bytes `/gritz/log` contains, including the terminal
   failure lines.
6. **CLI** — Delivers: `gritz logs` reading from the server instead of Docker,
   with `-f` polling. Depends on: (3). Verifiable by: running it against a
   completed task without Docker access.
7. **Web UI logs tab** — Delivers: the task-page Logs tab with tail-first
   infinite scrollback and poll-based follow. Depends on: (3). Verifiable by:
   rendering against a task with multi-page logs; `pnpm lint` clean.

Slices 6 and 7 are independent of each other and of 4–5; they can land in any
order once (3) is in.

## Trade-offs

- **In-memory buffer vs. reusing the durable outbox.** The runner's
  `outbox.Outbox[T]` already provides crash-safe, at-least-once FIFO delivery
  and was the obvious candidate. Rejected here: the outbox persists every
  payload to disk before sending, which would write every log byte to disk a
  second time (it is already in `/gritz/log`) and add filesystem queue
  management to the hot path — for data whose durable copy already exists an
  fsync away. Logs are diagnostics, not state transitions; losing an unshipped
  tail on a crash is acceptable where losing a `stopped` event is not. The
  shipper borrows the outbox's *shape* (FIFO, head-of-line retry, backoff,
  permanent-vs-transient classification) without its durability.
- **Mirroring the sink vs. tailing the file.** A file-tailing shipper (persist
  a byte offset, ship `/gritz/log` from the offset) would survive driver
  crashes and never diverge from the file. Rejected for complexity: offset
  persistence, read-while-write coordination, and re-reading bytes the process
  just wrote. Teeing the in-memory sink is a few lines and byte-identical in
  the common case; the crash-tail gap is accepted.
- **A new `log_chunks` table vs. the event stream.** The `logs` table was
  deliberately dropped (`20260614000003_drop_logs.sql`) because every log
  *line* had an event-type home on the unified timeline. That reasoning does
  not transfer: this is a raw byte transcript — high-volume, unparsed,
  order-critical, never rendered as timeline entries — and stuffing chunks
  into JSONB event payloads would spam every timeline consumer and the
  driver's own event drain. A separate table with a separate lifecycle is the
  point, not a regression of that proposal.
- **Unary per-chunk RPC vs. streaming (or batching).** A client-streaming
  upload would shave per-request overhead, but the codebase has zero streaming
  RPCs, the two existing byte-stream surfaces (shell WebSocket, SSE) live
  outside Connect, and cutting at 32 KiB/2s over a keep-alive connection makes
  request overhead negligible. Unary keeps the handler, auth, and retry story
  identical to every other RPC. Sending a *batch* of chunks per request was
  also considered and rejected: it buys nothing on top of the chunk cutting
  that already bounds request rate, and it would make byte ordering — this
  feature's whole correctness property — depend on a multi-row insert
  preserving order. One chunk per request, one chunk per statement, makes id
  order write order by construction. The store method still takes a `*sql.Tx`,
  so a future caller can wrap N inserts in one transaction without a signature
  change.
- **Accepting duplicates vs. a dedup key.** A client-assigned `(version, seq)`
  with a unique index and `ON CONFLICT DO NOTHING` would make retries
  exactly-once, but a restarted run can reuse a version, so `seq` would need a
  per-attempt disambiguator — machinery out of proportion to the failure mode
  (a repeated span of log text after an ambiguous timeout, visible and
  harmless).

## Open Questions

Retention and live tail are still open; secret hygiene has since been settled.

- **Retention.** Chunks grow unbounded until the task is deleted; archived
  tasks keep their logs. Is cascade-on-delete enough, or do we want age-based
  GC or a per-task byte cap (delete oldest chunks past N MiB) before this
  ships? The tail matters most for post-mortems, so a byte cap would be cheap
  and safe.
- ~~**Secret hygiene.**~~ **Resolved: out of scope for v1.** The sandbox-log
  proposal accepted secrets-on-sandbox-disk because the shell was the same
  trust boundary as running the agent. Shipping widens the audience: setup
  output and agent stderr become readable by anyone with `OpTaskRead` in the
  org, persisted in Postgres. That is accepted as-is — v1 ships raw bytes with
  no redaction, filtering, or `toollog.Redact`-style scrubbing at the tee or in
  the shipper. See icholy/gritz#1241 comment 5559907409.
- **Live tail.** V1 follow mode is polling. If a real live tail is wanted, the
  cheap increment is a throttled `task_logs`/`appended` notification on the
  existing SSE channel (UI-only, excluded from channel summaries like
  `UploadLogs` already is) — worth doing only once the Logs tab exists.