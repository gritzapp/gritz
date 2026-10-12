# Driver WAL

Issue: https://github.com/gritzapp/gritz/issues/1755 (part of #1668)

## Problem

`proposals/draft/driver-server.md` has the driver report everything it produces
(runner events, log chunks, `Finished`) through a durable log on the sandbox's
disk, which the runner reads from its own position: `RunRequest.after_seq` in,
`StatusResponse.last_seq` out. That proposal calls it the "response log" and
only sketches it: one file per version under `/gritz/responses/`, each message
fsynced before it is streamed, a version's file deleted once a `Run` arrives
past its `Finished`, and an open question about its size (question 7).

None of it exists yet. Today:

- The driver reports straight to the server. `agent.Driver.submit` calls
  `SubmitRunnerEvents` for started / stopped / failed, and `logship.Shipper`
  calls `AppendLogChunk` for every 32 KiB chunk. A report that does not get
  through is gone; `Finished.reported` only tells the runner that it happened.
- The `Run` stream carries `Accepted`, `KeepAlive` and `Finished` only. A runner
  that loses the stream or restarts cannot get back anything the driver produced
  in the meantime.
- A driver that restarts mid-run can do no more than mark `/gritz/run.json`
  finished and unreported (`runrecord.Reconcile`); nothing on disk says what it
  had already reported.

The parent proposal also leaves the hard parts open: what the log is built on,
what is durable before it is streamed, how a stream follows new entries, what
sequence numbers do across retention and driver restarts, what a crash leaves
at the tail, how large the log may grow, and how it relates to the run record
and boot reconciliation. This proposal settles them for the log alone, which it
calls the **WAL**. Task, event and link fetching moving to the runner, the
runner key and idle parking stay in the parent proposal.

## Design

### Overview

- **One WAL per sandbox**, `/gritz/wal/`, built on
  [`github.com/tidwall/wal`](https://github.com/tidwall/wal) v1.2.1, wrapped by a
  new package `internal/runner/wal`.
- **An entry is one `RunResponse`**: a runner event, a log chunk or a `Finished`.
  `Accepted` and `KeepAlive` belong to one stream and are never stored.
- **The WAL index is the sequence number.** `RunResponse.seq` is the index of
  the entry it was read from. It starts at 1, has no gaps, survives driver
  restarts and retention, and resets only if the WAL directory itself is lost
  (see "Sequence numbers").
- **Every entry is fsynced before `Append` returns, and only appended entries
  are streamed.** The library syncs inside `Write`; the wrapper's durable mark
  advances only after `Write` returns without error.
- **`logship` stays the chunking and masking layer.** It narrows its dependency
  from `gritzclient.Client` to the one method it calls, and the driver gives it
  a client whose `AppendLogChunk` appends to the WAL. `agent.Driver`'s
  `SubmitRunnerEvents` becomes a WAL append the same way. No new sink
  abstraction.
- **Retention is `TruncateFront` behind the runner's `after_seq`**, received on
  `Run` and on a new unary `Trim`. Per-version files and their deletion are gone.
- **The run record stays.** `/gritz/run.json` still says which version is
  running or finished, and when. Boot reconciliation now checks the WAL's tail
  before it writes anything, and the outcome of a crashed run is written to the
  WAL like any other.

### What tidwall/wal does

Verified against the source of v1.2.1 (`wal.go` at `602cbd2`; master's only
later commit touches a test) and with a scratch program against the released
module. The design depends on each of these.

| Behavior | What the source does |
|---|---|
| On-disk format | A directory of segment files named by the zero-padded 20-digit index of their first entry. With `LogFormat: Binary` (default) an entry is `uvarint(len) ‖ data`. No checksum, no per-entry index. |
| Open | Lists the directory, ignoring names that are not 20 digits (optionally `.START` / `.END`). Reads **only the last segment** fully into memory and parses it; earlier segments are parsed lazily on `Read`. `lastIndex = tail.index + entries − 1`. |
| Torn tail at open | `loadNextBinaryEntry` returns `ErrCorrupt` when the length varint is incomplete or the data is shorter than the length; `Open` fails with `ErrCorrupt`. **There is no repair.** A tail of zero bytes (a file extended but never written) is *not* detected: each `0x00` parses as an empty entry (seen in the scratch program: three entries plus four zero bytes opened as seven entries, the last four empty). Garbage with a plausible length is read back as data. |
| `Write` | Requires `index == lastIndex+1` (`ErrOutOfOrder`). Appends to the in-memory tail buffer, `write(2)`s it, sets `lastIndex`, then `fsync`s unless `NoSync`. The whole call holds the log's write lock, so readers (`Read`, `LastIndex`, read lock) never observe an entry before its sync has returned. If the sync fails, `lastIndex` has already moved and the bytes are in the page cache: the log is ahead of what is durable. |
| Segment cycling | When the tail reaches `SegmentSize` the old file is fsynced and closed and a new file `segmentName(lastIndex+1)` is created with `O_CREATE`. The directory is never fsynced. |
| `TruncateFront(i)` | Copies the entries from `i` to the end of `i`'s segment into `segmentName(i).START.TEMP`, fsyncs it, renames it to `.START`, deletes every segment before it, renames `.START` into place, and reloads it if it is the tail. Cost is the size of the retained part of that one segment. `Open` finishes an interrupted truncate from the `.START` file; a stray `.TEMP` is ignored. The directory is never fsynced. |
| Emptying the log | Without `AllowEmpty`, `TruncateFront(lastIndex+1)` returns `ErrOutOfRange`: at least one entry always remains. With it, the log can be emptied and its position survives a restart in the name of an empty segment file. |
| Index across restarts | Derived from the tail segment's name plus its entry count, so it continues where it left off, including after `TruncateFront`. A missing or empty directory starts a new log at index 1. |
| Notification | None. There is no way to wait for an append. |
| Memory | The tail segment's bytes stay in memory, plus up to `SegmentCacheSize` (default 2) read segments. |

### Package `internal/runner/wal`

A thin wrapper over `tidwall/wal` that closes those gaps. It knows nothing
about `RunResponse`; it stores bytes.

**Framing.** Each record the library stores is `crc32c(payload) ‖ payload`
(4 bytes, little-endian, Castagnoli). `Read` verifies the checksum and returns
`ErrCorrupt` on a mismatch. With the library's length prefix this makes every
torn or zero-filled tail detectable: an empty record (shorter than 4 bytes) or a
checksum mismatch is never valid.

**Open and repair.** `Open(dir)`:

1. Removes `*.TEMP` files the library leaves behind after a crash inside
   `TruncateFront`.
2. Finds the tail segment: the file with the highest index, plain or
   `.START` (a `.START` left by an interrupted `TruncateFront` was fsynced
   before it got that name, so it scans clean; the library finishes the
   truncate when it opens).
3. Scans the tail segment with the library's framing (`uvarint` length, then
   data) and the wrapper's checksum, and stops at the first record that is
   incomplete, empty or fails its checksum.
4. If anything follows the valid prefix, truncates the file to that prefix,
   fsyncs it and the directory, and logs how many bytes were cut.
5. Opens the library on the directory with `SegmentSize: 4 MiB`,
   `LogFormat: Binary`, `NoSync: false`, `AllowEmpty: false`.

Only the tail can be torn: earlier segments were fsynced before the library
cycled away from them. Bytes the repair cuts were never acknowledged by `Append`
(a write or sync that failed or never returned), so no stream ever sent them; the
index they held is reused by the next append without anyone having seen the old
entry. A checksum failure *inside* the tail, followed by valid records, is not a
torn write; repair still cuts there, which loses fsynced entries. That is disk
corruption, and the position checks on `Run` (below) turn it into a lost run
instead of a silent gap.

Repair parses tidwall's on-disk format, which ties the wrapper to it. The
format has been `uvarint(len) ‖ data` since v0.1.0 (2020) and the module is
pinned; a test that writes through the library and parses with the repair code
catches a change on upgrade.

**Fsync strategy: one fsync per append.** `NoSync` stays false, so each `Write`
syncs the tail before returning. After a `Write`, `Append` checks whether the
library started a new segment (a file named `segmentName(index+1)` exists) and
if so fsyncs the directory, so the new file's entry survives a power loss.
`TruncateFront` is followed by a directory fsync for the same reason.

Batching (`NoSync: true`, a syncer goroutine and a durable mark that lags the
written one) was considered and rejected for now. The WAL has two writers, the
shipper's sender and the run's runner events, and neither is on the agent's
path: `logship.Shipper.Write` only buffers, and the sender appends at most one
32 KiB chunk per wake-up or one partial chunk per 2s tick. Even a run writing
1 MB/s of output is about 30 fsyncs a second. Per-append sync keeps "appended"
and "durable" the same thing, so there is no second watermark to get wrong.

**The durable mark.** The wrapper keeps its own `last`, set to the index after
`Write` returns nil, and serves `Last`, `Read` bounds and notifications from it,
never from the library's `LastIndex` (which is ahead of the disk after a failed
sync).

**Failure is sticky.** Any error from `Write` or the directory fsync fails the
log: every later `Append` returns `ErrFailed` wrapping the first error. On Linux
a failed fsync cannot be retried meaningfully (the dirty pages may already be
dropped), and a partial `write(2)` leaves bytes in the file the library does not
account for. Reads of entries up to `last` keep working.

**Live follow.** `Changed()` returns a channel that is closed by the next
successful `Append` (close and replace under the wrapper's mutex). A reader
takes the channel, reads up to `Last()`, then waits on it, so an append between
the read and the wait is never missed. There is only ever one stream, but the
broadcast costs nothing more than a single-receiver wake-up and has no lost-wake
cases.

**Retention.** `TruncateFront(i)` drops entries before `i`, clamped to
`[First(), Last()]`: the last entry is never dropped. That is the library's rule
without `AllowEmpty`, kept deliberately: a finished run's `Finished` is the last
entry of the WAL until the next run starts, so it can always be re-sent (see
`Run`). A call that would drop nothing is a no-op, and the cost of one that does
is the retained part of one segment, which is small when the runner is caught up.

**Size.** `Size()` is the bytes on disk: summed from the segment files at `Open`
and after `TruncateFront`, and increased by each record's framed length on
`Append`.

### Entries

An entry's payload is a marshalled `driver.v1.RunResponse` with `seq` left zero;
`seq` is set from the index when the entry is read. One `RunResponse` per entry:

| Case | Appended by | When |
|---|---|---|
| `runner_event` | the run, through `SubmitRunnerEvents` on the driver's client | started, then stopped or failed, as today; one entry per event |
| `log_chunk` | `logship.Shipper`'s sender, through `AppendLogChunk` on the driver's client | each chunk the shipper cuts, masked as today |
| `finished` | `driverserver.execute` | once per run, after the shipper's sender has exited, so it is the run's last entry |

### Sequence numbers

The WAL index is `RunResponse.seq`, and it holds up as one:

- **Monotonic and gapless.** `Write` refuses anything but `lastIndex+1`.
- **Across driver restarts.** The index continues from the tail segment's name
  plus its entry count. A repair cuts only entries no reader saw, so reusing
  their indexes is safe.
- **Across retention.** `TruncateFront` renames the retained part to the first
  retained index and always keeps an entry, so the next index never moves back.
- **Lost directory.** If `/gritz/wal/` is deleted (by the agent, which the
  parent proposal already places inside the trust boundary) the next `Open`
  starts at 1. A runner whose `after_seq` is now past `Last()` gets `OutOfRange`
  from `Run`; see open question 1 for a WAL that regrows past the runner's
  position before it reconnects.

The invariant the driver enforces is `First()−1 ≤ after_seq ≤ Last()`. Outside
it, entries the runner needs are gone or the log is not the one it was reading,
and `Run` and `Trim` return `OutOfRange` before `Accepted`.

### Run, Status and Trim

**Run.** Unchanged attach-or-start rules from the parent proposal. With
`after_seq` set:

1. Check the position invariant (`OutOfRange` otherwise), then
   `TruncateFront(after_seq+1)`.
2. Send `Accepted`.
3. Loop: take `Changed()`; read and send every entry from `after_seq+1` to
   `Last()` with its `seq`; end the stream after sending the `Finished` of the
   requested version. Otherwise wait for the channel, the `KeepAlive` ticker or
   the request's context.
4. If the requested run is already finished and its `Finished` is at or before
   `after_seq` (the runner stored it but crashed before it could act on it),
   re-send that entry, which is always `Last()`, and end the stream.

Entries of an earlier run that the runner has not stored (its tail, its
`Finished`) are replayed first, as the parent proposal describes; only the
requested version's `Finished` ends the stream.

With `after_seq` absent, `Run` behaves as it does today: `Accepted`,
`KeepAlive`s, and the run's `Finished` (seq 0) built from the in-memory run or
the run record. That is how today's runner keeps working until it sends a
position, and why `after_seq` is `optional`.

**Status** adds `last_seq = Last()`.

**Trim** is new: `TruncateFront(after_seq+1)` with the same position check. The
runner calls it after it writes `Seq` to `taskstate/`. `Run` alone cannot bound
the WAL: it carries `after_seq` only when a stream opens, and an attached runner
holds one stream for the whole run. Trim is a retention hint, not an ack: the
driver keeps no delivery state, a missed `Trim` only delays deletion, and every
reconnect is still `Run(version, after_seq)`.

### Size cap

A WAL that stays behind the runner holds only what the runner has not stored
yet, so it is large only while the runner is down or cannot deliver. It is
capped at `MaxBytes`, 128 MiB by default:

- A log chunk that would take `Size()` past the cap is not appended. The driver
  counts the dropped bytes, and the next chunk it does append is prefixed with
  `[gritz:dropped N bytes]\n`, in the style of the mask's `[gritz:masked NAME]`.
- Runner events and `Finished` are always appended. They are a few hundred bytes
  per run.
- The full log is still in `/gritz/log`, which is never capped.

Dropping the oldest chunks instead, as the parent's open question 7 suggests,
is not possible on a log that only truncates from the front: the oldest
undelivered chunks are interleaved with runner events the runner still needs.

### Run record and boot reconciliation

`/gritz/run.json` keeps its role and format: `{version, running}` before a run
starts, `{version, finished, finished_at, reported}` after. The WAL is the
record of what happened, the run record of which run the driver is on. Writes
are ordered so that each one is implied by the one before:

1. Run start: write `{version, running}`, then append `started`.
2. Run end: append the terminal event, the last log chunks, then `Finished`,
   then write `{version, finished}`.

On boot, `driverserver.New` opens the WAL (repairing the tail), reads the
record and replaces `runrecord.Reconcile`:

| Run record | WAL's last entry | Action |
|---|---|---|
| absent or finished | anything | none |
| running, version v | `Finished(v)` | crashed after `Finished` but before the record write: write `{v, finished, finished_at: now}` |
| running, version v | anything else | crashed mid-run: append `failed(v, "driver restarted mid-run")` and `Finished(v)`, then write `{v, finished}` |

`Finished.reported` and the record's `reported` keep their meaning while the
driver still reports directly ("the server acknowledged the terminal event"), so
a crashed run is unreported and the runner's backstop reports it. Once the
runner forwards (layer 7), `reported` means "the terminal event is in the WAL
before this `Finished`", which is true for a reconciled crash: the runner
forwards the `failed` the restarted driver wrote.

The record is kept rather than derived from the WAL's tail. `Status` reports it
on every call, it marks a run running before its first entry exists, it carries
`finished_at` for idle parking, and a lost WAL directory must not make the
driver forget which version it is on.

### Wiring

`logship.New` takes a `logship.Client` (just `AppendLogChunk`) instead of
`gritzclient.Client`. Nothing else in logship changes: masking, chunk cutting,
one request at a time, retries.

`driverserver` builds a per-run `walClient` that embeds the run's
`gritzclient.Client` and overrides two methods:

- `SubmitRunnerEvents` appends one `runner_event` entry per event.
- `AppendLogChunk` appends a `log_chunk` entry (`version`, `data`; the task id is
  the runner's to add), applying the size cap.

`agent.Driver.Client` is the `walClient`, so `Driver.submit` is unchanged, and
`GetTask`, `ListEventsByTask` and `ListLinks` still go to the server.

Until the runner forwards, the driver reports both ways:

- `walClient.SubmitRunnerEvents` appends, then calls the embedded client, whose
  result decides the outcome as today.
- The run has two shippers teed into its log: one on the run's client (today's,
  unchanged) and one on the `walClient`. They mask and cut independently. A
  single shipper writing to both would re-append on every retry of the server
  call.
- WAL errors are logged and otherwise ignored, so a WAL problem cannot change a
  run's outcome while nothing reads the WAL in production.

When the runner forwards (layer 7) the embedded calls and the second shipper go,
and a failed WAL becomes fatal: the driver stops the active run and exits
non-zero, the runner sees an unreachable driver, and its failed backstop applies.
A WAL that cannot write is a broken sandbox.

**Version skew.** A sandbox keeps the driver binary it was created with, and
sandboxes are created by the runner, so a runner only ever meets drivers as old
as itself or older. `Accepted.reports_directly` lets it tell them apart: drivers
from layer 3 up to layer 7 set it, and a runner forwards only on streams where
it is false. A driver from before the WAL leaves it false but sends no
`runner_event` or `log_chunk` entries, so there is nothing to forward, and its
`Finished` has seq 0, which never moves the runner's position.

### Proto

Additions to `proto/driver/v1/driver.proto`, with the field numbers the parent
proposal reserved:

```protobuf
import "gritz/v1/gritz.proto";

service DriverService {
  // ... Status, Run, Stop as today.

  // Trim lets the driver drop WAL entries up to after_seq: the runner has
  // made them durable on its side. A hint; nothing depends on it for
  // correctness. OutOfRange if after_seq is past the WAL's last entry or
  // before its first.
  rpc Trim(TrimRequest) returns (TrimResponse);
}

message StatusResponse {
  optional RunRecord run = 1;
  // Index of the last WAL entry. Zero if the WAL is empty.
  uint64 last_seq = 3;
}

message RunRequest {
  int64 version = 1;
  RunSpec spec = 2;
  // The last seq the runner has made durable on its side (taskstate's Seq).
  // Set: the stream replays every WAL entry after it, then follows new ones,
  // and the driver may drop entries up to it. OutOfRange if it is past the
  // WAL's last entry or before its first. Absent: no replay; the stream
  // carries Accepted, KeepAlive and the run's Finished only.
  optional uint64 after_seq = 3;
}

message RunResponse {
  // WAL index of the entry. Set on runner_event, log_chunk and finished read
  // from the WAL; zero on accepted and keep_alive, which are never stored,
  // and on the Finished of a stream without after_seq.
  uint64 seq = 1;
  oneof event {
    Accepted accepted = 2;
    KeepAlive keep_alive = 3;
    // started / stopped / failed, stamped with the run's version.
    gritz.v1.RunnerEvent runner_event = 4;
    // A masked chunk of the run's log, cut by logship.
    LogChunk log_chunk = 5;
    Finished finished = 6;
  }
}

message Accepted {
  bool attached = 1;
  // True if the driver also reports runner events and log chunks to the
  // server itself; the runner must not forward them.
  bool reports_directly = 2;
}

message LogChunk {
  int64 version = 1;
  bytes data = 2;
}

message Finished {
  int64 version = 1;
  // While the driver reports directly: the server acknowledged the run's
  // terminal runner event. Otherwise: the terminal event is in the WAL
  // before this Finished.
  bool reported = 2;
}

message TrimRequest {
  uint64 after_seq = 1;
}

message TrimResponse {
  // Index of the first entry the WAL still holds.
  uint64 first_seq = 1;
}
```

### API

```go
package wal // github.com/gritzapp/gritz/internal/runner/wal

// DefaultSegmentSize is the target size of a segment file.
const DefaultSegmentSize = 4 << 20

// ErrCorrupt is returned for an entry that fails its checksum.
var ErrCorrupt error

// ErrFailed is returned by every Append after a write or sync has failed.
var ErrFailed error

// ErrNotFound is returned by Read for an index outside [First, Last].
var ErrNotFound error

// Options configures Open.
type Options struct {
	// SegmentSize is the target size of a segment file; zero means DefaultSegmentSize.
	SegmentSize int
	// Log reports repairs; nil means slog.Default().
	Log *slog.Logger
}

// Log is a durable, append-only log of byte entries numbered from 1.
type Log struct{ ... }

// Open opens or creates the log in dir, cutting a torn tail first.
func Open(dir string, opts Options) (*Log, error)

// Append durably appends data and returns its index.
func (l *Log) Append(data []byte) (uint64, error)

// Read returns the entry at index.
func (l *Log) Read(index uint64) ([]byte, error)

// First returns the index of the oldest entry, or 0 if the log is empty.
func (l *Log) First() uint64

// Last returns the index of the newest durable entry, or 0 if the log is empty.
func (l *Log) Last() uint64

// Changed returns a channel that is closed by the next Append.
func (l *Log) Changed() <-chan struct{}

// TruncateFront drops the entries before index, always keeping the last one.
func (l *Log) TruncateFront(index uint64) error

// Size returns the bytes the log occupies on disk.
func (l *Log) Size() int64

// Close syncs and closes the log.
func (l *Log) Close() error
```

```go
package logship // github.com/gritzapp/gritz/internal/runner/logship

// Client is where a Shipper sends its chunks. // new
type Client interface {
	// AppendLogChunk delivers one chunk.
	AppendLogChunk(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error)
}

// New returns a shipper that sends to client. // changed: client was gritzclient.Client
func New(client Client, opts Options) *Shipper
```

```go
package driverserver // github.com/gritzapp/gritz/internal/runner/driverserver

// DefaultWALDir is where the driver keeps its WAL inside the sandbox. // new
const DefaultWALDir = "/gritz/wal"

// DefaultWALMaxBytes is the size past which log chunks are dropped. // new
const DefaultWALMaxBytes = 128 << 20

type Options struct {
	// ... existing fields.

	// WALDir is the WAL's directory; defaults to DefaultWALDir. // new
	WALDir string
	// WALMaxBytes caps the WAL for log chunks; defaults to DefaultWALMaxBytes. // new
	WALMaxBytes int64
}

// New opens the WAL and reconciles the run record against it. // changed
func New(opts Options) (*Server, error)

// Trim drops WAL entries up to the request's after_seq. // new
func (s *Server) Trim(ctx context.Context, req *driverv1.TrimRequest) (*driverv1.TrimResponse, error)

// Close closes the WAL. // new
func (s *Server) Close() error
```

```go
package runrecord // github.com/gritzapp/gritz/internal/runner/runrecord

// Reconcile rewrote a running record as finished. // removed: driverserver.New reconciles against the WAL
func Reconcile(path string, now time.Time) (rec Record, ok, crashed bool, err error)
```

```go
package taskstate // github.com/gritzapp/gritz/internal/runner/taskstate

type Record struct {
	// ... existing fields.

	// Seq is the last WAL seq the runner has made durable; zero replays everything. // new
	Seq uint64 `json:"seq,omitempty"`
}
```

**How it fits:** `gritz driver` calls `driverserver.New`, which opens the
`wal.Log` and reconciles the run record against it; `Close` runs on shutdown.
Each run's `execute` wraps the run's `gritzclient.Client` in an unexported
`walClient`, passes it to `agent.Driver` as `Client` and to `logship.New`, and
appends `Finished` once the shipper's sender has stopped. `Run` and `Trim` read
and truncate the `wal.Log`; `Status` reports its `Last`. On the runner,
`TaskRunner.attach` sends `taskstate.Record.Seq` as `after_seq`, advances `Seq`
as it stores entries, and calls `Trim` after each `Seq` write.

### Visualization

`diagrams/src/projects/proposals/driver-wal.ts`, on a new layout
(`diagrams/src/layouts/driverwal.tsx`) with the server, the runner's
`taskstate/` record and outbox, and the sandbox's WAL and run record. It shows
the final design (the runner forwards). View it with `mise run motioncanvas`.

- **`driver-wal-append`** — logship cuts a chunk, the driver appends and fsyncs
  it, `Changed` wakes the `Run` stream, and the runner stores it and advances
  `Seq`, then `Trim`s.
- **`driver-wal-replay`** — the runner restarts mid-run while the driver keeps
  appending; it reattaches with `Run(after_seq: Seq)`, gets the missed entries
  then live ones, and the WAL is truncated behind it.
- **`driver-wal-crash`** — the driver dies mid-append, the restart cuts the
  torn tail, finds `run.json` running, appends `failed` and `Finished`, and
  the runner's reattach delivers them.

## Implementation Plan

Each layer merges on its own and leaves a working system. Until layer 7 the
driver still reports directly and the WAL is a shadow copy, so layers 1 to 6
cannot change a run's outcome.

1. **`internal/runner/wal`** — Delivers: the package above on
   `github.com/tidwall/wal` v1.2.1: CRC framing, tail repair at `Open`,
   per-append fsync with the directory fsync on a new segment, the durable mark,
   sticky failure, `Changed`, `TruncateFront` that keeps the last entry, `Size`.
   Nothing uses it yet. Depends on: nothing. Verifiable by: unit tests for a
   torn length, torn data, a zero-filled tail and a bad checksum at the tail
   (each cut, then the next `Append` gets the cut index); the index continuing
   across `Close`/`Open` and across `TruncateFront`; `TruncateFront` past the
   end keeping the last entry; `Changed` closing on `Append`; and a format test
   that writes through the library and parses with the repair scanner.
2. **`logship.Client`** — Delivers: `logship.New` takes the one-method
   `Client` interface instead of `gritzclient.Client`. Depends on: nothing.
   Verifiable by: the existing logship tests, unchanged apart from the type.
3. **Proto** — Delivers: `seq`, `runner_event`, `log_chunk`, `LogChunk`,
   `RunRequest.after_seq`, `StatusResponse.last_seq`, `Accepted.reports_directly`
   and `Trim` in `driver.proto`, regenerated. The driver server returns
   `Unimplemented` for `Trim` and sets `reports_directly`. Depends on: nothing.
   Verifiable by: `mise run generate` is clean and the build passes.
4. **Driver writes the WAL** — Delivers: `driverserver.New` opens
   `/gritz/wal/`; the `walClient` appending runner events and log chunks (with
   the size cap) while still calling the server; the second shipper; `Finished`
   appended after the shipper stops; boot reconciliation against the WAL
   replacing `runrecord.Reconcile`. WAL errors are logged only. Depends on: (1),
   (2), (3). Verifiable by: driverserver tests that run a fake agent and read
   the WAL directly (started, chunks, stopped, `Finished`, in order), that a
   killed-then-reopened driver appends `failed` and `Finished` once, and that a
   record left running after a durable `Finished` is not reconciled twice.
5. **Driver serves the WAL** — Delivers: `Run` with `after_seq` (position
   check, truncate, replay, live follow, end on the requested version's
   `Finished`, re-sending it when already stored), `Status.last_seq` and `Trim`.
   Without `after_seq`, `Run` is unchanged. Depends on: (4). Verifiable by:
   driverserver tests with a client that sends `after_seq`: no gaps or
   duplicates across a stream dropped mid-run, an earlier run's tail replayed
   before the current run, `OutOfRange` for a position past `Last` or before
   `First`, and the WAL's `First` moving on `Trim`.
6. **Runner position** — Delivers: `taskstate.Record.Seq`; `attach` sends
   `after_seq: Seq`, advances `Seq` (batched, under the task's lock) for every
   entry with a seq, calls `Trim` after each write, ends only on the requested
   version's `Finished`, and treats `OutOfRange` as a lost run. Entries are not
   forwarded: the driver still reports. Depends on: (5). Verifiable by: runner
   tests against an in-memory driver server: a runner restarted mid-run
   resumes from its stored `Seq`, and the WAL is trimmed behind it.
7. **Runner forwards, driver stops reporting directly** — Delivers, as two
   PRs:
   - **7a** — The runner forwards on streams with `reports_directly` false:
     `runner_event` → event outbox `Enqueue`, `log_chunk` → `AppendLogChunk`
     with the task id (retried in order, dropped on a permanent error), and
     `Seq` advances only once an entry is stored. No driver sets the flag false
     yet, so this is inert. Depends on: (6). Verifiable by: runner tests with a
     fake driver that sends `reports_directly: false`.
   - **7b** — The `walClient` stops calling the server, the second shipper goes,
     `reports_directly` is false, `Finished.reported` means "terminal event in
     the WAL", and a failed WAL is fatal. Depends on: (7a). Verifiable by: a
     task run end to end on Docker whose events and log arrive through the
     runner, including with the runner restarted mid-run.

## Trade-offs

**tidwall/wal vs. a hand-rolled log.** A file per version of length-delimited
messages is simple, but needs its own framing, recovery, segmenting and
retention. tidwall/wal provides segmenting, index-addressed reads,
crash-safe front truncation and index continuity; the wrapper adds the
checksum, tail repair, directory fsyncs, the durable mark and notification. The
cost is that repair parses the library's format, and the library reads its
whole tail segment into memory, which `SegmentSize: 4 MiB` keeps small.

**`AllowEmpty: false`.** Keeping the last entry costs at most one 32 KiB chunk,
and gives two things: a finished run's `Finished` is always there to re-send,
and the index survives a restart through an entry rather than through an empty
segment file's name. `AllowEmpty` would let retention empty the WAL completely,
which buys nothing here.

**Per-append fsync vs. batched `Sync`.** Covered under "Fsync strategy": the
append rate is low and off the agent's path, and one watermark is simpler than
two. The library's `WriteBatch` leaves room to batch the shipper's drains later
without changing the API.

**`Trim` vs. retention on `Run` only.** `Run` carries `after_seq` only when a
stream opens, and an attached runner keeps its stream for the whole run, so
`Run`-only retention would let a long, chatty run hit the cap with everything
already stored by the runner. Reopening the stream periodically would carry the
position too, but makes a protocol rule out of a side effect. `Trim` is one
unary call that the driver may ignore.

**Dropping new chunks at the cap vs. truncating old ones.** A front-only log
cannot drop old chunks without dropping the runner events between them. Dropping
new chunks keeps every lifecycle event, leaves a marker where the gap is, and
`/gritz/log` still has the bytes.

**Two shippers during the transition vs. one shipper with two destinations.**
One shipper writing to the WAL and the server would re-append to the WAL on every
retry of the server call, or need a retry-aware client. Two shippers mask and
cut the same bytes twice, a CPU cost for layers 4 to 6 only.

**A `gritzclient.Client` wrapper vs. a sink interface.** Overriding
`SubmitRunnerEvents` and `AppendLogChunk` on the client the run already uses
leaves `agent.Driver` and logship's sender untouched and introduces no new
abstraction. The cost is that the client's other methods still reach the
server, which is what this proposal wants: moving them is the parent's job.

**Keeping the run record vs. deriving it from the WAL.** See "Run record and
boot reconciliation": the record covers the moment before the first entry,
carries `finished_at`, and survives a lost WAL.

## Open Questions

1. **WAL identity.** A lost `/gritz/wal/` restarts at index 1. If it regrows
   past the runner's `Seq` before the runner reconnects, the runner skips the
   entries in between without noticing. A random WAL id, stored as a file in
   the WAL directory (the library ignores names that are not indexes) and
   reported in `Accepted`, stored by the runner next to `Seq`, would catch it.
   Is it worth a field, given that only the agent can delete the directory?
2. **Cap value.** Is 128 MiB right for both backends? Lambda MicroVM disks may
   be smaller, and a workspace setting may be needed.
3. **Fsync cost on Lambda MicroVMs.** The per-append fsync has not been measured
   on the MicroVM's disk. If it is slow, the shipper's drains are the place to
   batch with `WriteBatch`.
4. **Duplicate log chunks** (the parent's question 2) still apply once the
   runner forwards: chunks stored after the runner's last `Seq` write are sent
   again after a runner crash. The WAL's seq is a natural key if
   `AppendLogChunkRequest` gains one.
