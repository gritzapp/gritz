---
title: "In-sandbox driver logs at /gritz/log"
description: "The driver scatters its diagnostics across the sandbox container stdio, where nothing collects them, making driver problems hard to debug."
---

Issue: https://github.com/icholy/gritz/issues/1241

## Problem

The driver (`gritz driver`) runs inside the sandbox and scatters all of its
diagnostics across the container's stdio, where nothing collects them:

- The driver's own structured `slog` output goes to `slog.Default()`, which the
  driver command never configures — it inherits the process default handler
  writing to `os.Stderr` (`internal/command/driver.go:40`). This is the setup
  progress, the `loaded config` line, the per-tool-call summary lines parsed
  from the Claude stream (`a.log.Info("tool", …)` in
  `internal/agent/claude.go`), and the terminal `task failed` error.
- Setup command stdout/stderr are wired straight to `os.Stdout`/`os.Stderr`
  (`Driver.setup` in `internal/agent/driver.go`).
- The Claude Code CLI's own stderr is passed straight through to the container's
  stderr (`cmd.Stderr = os.Stderr` in `internal/agent/claude.go`).

None of this reaches an operator except by shelling onto the runner host and
running `docker logs gritz-{id}` — and even that is destroyed the moment the
sandbox is pruned or replaced. `Runner.Prune()` removes containers for archived
tasks, and restarts adopt or recreate the container, so a failed run frequently
cannot be debugged post-mortem at all. The Web UI shows only the one-line
`failed` reason (`Sandbox failed: …`) and deliberate `report` events.

## Design

The sandbox already grows a first-class operator entry point: the driver
reverse-shell (`proposals/implemented/driver-reverse-shell.md`). An operator can
`gritz shell <task-id>` into a finished sandbox and get an interactive shell in
the same filesystem the run used. This proposal makes that shell the log viewer:
**the driver tees everything it emits into a single file inside the sandbox, and
the operator reads it with the shell they already have.**

No logs are shipped to the server. There is no new server-side storage, no new
RPC, no proto change, and no Web UI change. The sandbox filesystem is the store;
the reverse-shell is the viewer.

Keep it simple: one append-only file, no rotation, no per-run files.

### Where logs land: `/gritz/log`

The driver appends all of its output to a single fixed file in the container:

```
/gritz/log
```

The path is a fixed runner/driver convention, mirroring
`agent.DefaultConfigStore` — add an `agent.DefaultLogPath = "/gritz/log"`
constant that both sides reference. `/gritz` is chosen deliberately over the
existing config convention `/tmp/gritz` (`internal/agent/config.go:16`): `/tmp`
is frequently a tmpfs or a directory a setup step may clear, whereas `/gritz`
lives on the container's writable layer, which the runner *preserves across
runs* when it adopts an exited container (`docker.go` `adopt`). Persistence
across runs is exactly the property post-mortem debugging needs.

**The runner pre-creates the directory.** The driver may run as a non-root user,
and `os.MkdirAll("/gritz", …)` creates a directory directly under `/`, which a
non-root driver cannot do — it would fail, and the sink would silently
degrade to a no-op, turning the feature off invisibly. The runner already solves
exactly this for the config dir by shipping a directory entry in the sandbox
spec's `Files` list (`Runner.spec`, `internal/runner/runner.go:495-499`):

```go
Files: []backend.File{
    // Allow non-root agents to write to this directory.
    {Path: path.Dir(agent.DefaultConfigStore.Path(task.ID)), Mode: 0777, Dir: true},
    {Path: agent.DefaultConfigStore.Path(task.ID), Data: cfgData, Mode: 0666},
    // New: pre-create the log dir so a non-root driver can write /gritz/log.
    {Path: path.Dir(agent.DefaultLogPath), Mode: 0777, Dir: true},
}
```

With `/gritz` pre-created `0777`, a non-root driver can create and append to the
file inside it. The driver still runs `os.MkdirAll(path.Dir(agent.DefaultLogPath),
0o777)` as a fallback (e.g. for a directly-invoked driver outside the runner),
then opens the file with `os.OpenFile(agent.DefaultLogPath,
os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)`. Opening in append mode means every
run adds to the end of the same file; nothing is truncated. If the open still
fails, the failure is non-fatal and logged, and the sink degrades to a no-op —
logging to a file is best-effort and must never take down a run.

### Runs are not separated

The same container is reused across runs, and each run has a monotonically
increasing `task.Version`. We deliberately do **not** split logs per run or per
version. Everything appends to the one file in chronological order. To keep runs
readable, the driver writes a single delimiter line at the start of each run
before the first event — e.g.

```
==== run version=<N> pid=<pid> ====
```

using `task.Version`, which the driver already reads at the top of `Driver.Run`
(`internal/agent/driver.go:71-72`). That is the entire "per-run handling": a
grep-able marker, not separate files. An operator scans the tail of one file and
finds the run boundaries by eye.

### How the tee works

Today the driver logger is `slog.Default()`, passed in via
`command/driver.go:40`. The driver command (not the shared `agent` package)
becomes responsible for opening the file and building the logger:

1. **Open the append-only sink.** A single `io.Writer` for the process, `sink`,
   backed by the `/gritz/log` file handle opened in append mode. All three
   streams tee into this one writer so the file is a single chronological
   interleaving of everything the run produced — the same thing `docker logs`
   shows today, plus the parsed tool summaries.

2. **Driver slog → file + stderr.** Replace the default handler with one writing
   to `io.MultiWriter(os.Stderr, sink)`:

   ```go
   handler := slog.NewTextHandler(io.MultiWriter(os.Stderr, sink), nil)
   driver.Log = slog.New(handler)
   ```

   `os.Stderr` is kept in the multiwriter so `docker logs` keeps working exactly
   as today (see below).

3. **Setup commands → file + stdout/stderr.** In `Driver.setup`, replace the
   direct `os.Stdout`/`os.Stderr` wiring with tees:

   ```go
   c.Stdout = io.MultiWriter(os.Stdout, sink)
   c.Stderr = io.MultiWriter(os.Stderr, sink)
   ```

   This is the highest-value change: setup failures are the most opaque today
   (`setup command N failed: exit status 1` with none of the command's output),
   and now the command's actual stdout/stderr sits in the log next to the error.

4. **Claude CLI stderr → file + stderr.** In `claude.go`, replace
   `cmd.Stderr = os.Stderr` with `cmd.Stderr = io.MultiWriter(os.Stderr, sink)`.
   Claude's stdout is *not* teed raw — it is the newline-delimited JSON stream
   the driver consumes and re-emits as `a.log.Info("tool", …)`/`("text", …)`
   summaries, which already flow through the slog tee in step 2. Teeing the raw
   JSON too would double it and bury the readable summaries.

To thread the sink into the setup and claude stages without touching every
call site, the sink is carried on the `Driver` struct (e.g. a new
`LogSink io.Writer` field defaulting to `io.Discard` when unset, so existing
tests and the non-driver code paths are unaffected) and passed into
`NewAgent`/`Options` for the claude stage. When `LogSink` is `io.Discard` the
tees degrade to plain `os.Stdout`/`os.Stderr` behavior.

### Size bounds: none

Per the KISS direction, there is no rotation and no size cap. The file grows
unbounded across the lifetime of the sandbox. This is an accepted trade-off:
sandboxes are not indefinitely long-lived, driver output per run is modest, and
adding rotation is easy to layer on later if a long-lived sandbox is ever
observed to fill its disk (see Trade-offs). Keeping it to a plain append means
the whole feature is a handful of tees and one `OpenFile`.

### Interaction with existing docker-logs behavior

`os.Stdout`/`os.Stderr` remain in every tee, so the container's stdio — and
therefore `docker logs gritz-{id}` — behaves byte-for-byte as it does today.
The file is strictly additive: an operator with host access keeps their existing
workflow, and an operator with only `gritz shell` access gains a durable log
they can `cat`, `tail -f`, `less`, or `grep` inside the sandbox. Nothing about
the runner, server, event stream, or Web UI changes.

### Viewing

No new command, and the use case is **post-mortem inspection**, not live
tailing. A sandbox run is one mode chosen once at startup — either an agent run
or a shell run (`proposals/implemented/driver-reverse-shell.md`) — so an operator
cannot attach a shell to a *live* agent run. Opening a shell provisions a
*replacement* run in the same container; because the log is a single append-only
file on the container's persisted writable layer, that shell run sees the prior
agent run's output already sitting in `/gritz/log`. The operator reads the
completed run's logs after the fact:

```
gritz shell <task-id>
$ less /gritz/log            # scroll back through prior runs
$ tail -n 200 /gritz/log     # the most recent (just-finished) run
$ grep -n '==== run' /gritz/log   # jump between run boundaries
```

## Implementation Plan

1. **Append-only sink + directory setup** — Delivers: the
   `agent.DefaultLogPath` constant plus a small `internal/agent` helper that
   `MkdirAll`s the parent dir (fallback) and opens the log file in
   `O_CREATE|O_WRONLY|O_APPEND` mode, returning an `io.WriteCloser`
   (best-effort: returns an `io.Discard`-backed no-op closer and a nil error on
   filesystem failure). Depends on: nothing. Verifiable by: unit tests over a
   temp path — file is created if absent, existing content is preserved
   (appended, not truncated), and a failed open degrades to the no-op.

2. **Runner pre-creates the log dir** — Delivers: `Runner.spec` adds a
   `backend.File{Path: path.Dir(agent.DefaultLogPath), Mode: 0777, Dir: true}`
   entry so a non-root driver can write the file. Depends on: (1). Verifiable
   by: launching a task with a non-root sandbox user and asserting `/gritz`
   exists `0777` and the driver's log file is populated (not silently
   discarded).

3. **Driver slog + struct wiring + run delimiter** — Delivers:
   `command/driver.go` opens the sink (from layer 1) after the task fetch, sets
   `driver.Log` to a handler over `io.MultiWriter(os.Stderr, sink)`, stores the
   sink on the `Driver` struct (default `io.Discard`), and writes the
   `==== run version=<N> … ====` delimiter before the first event. Depends on:
   (1). Verifiable by: running a driver against a dummy agent and asserting the
   delimiter and the driver's `slog` lines appear in both stderr and
   `/gritz/log`, and that a second run appends below the first.

4. **Setup command tee** — Delivers: `Driver.setup` tees each setup command's
   stdout/stderr into the sink. Depends on: (3). Verifiable by: a unit/e2e test
   with a failing setup command asserting the command's output lands in the log
   alongside the `setup command N failed` error.

5. **Claude stderr tee** — Delivers: `Options`/`NewAgent` carry the sink and
   `claude.go` tees the CLI's stderr into it. Depends on: (3). Verifiable by: a
   test asserting Claude CLI stderr reaches the log while stdout JSON is still
   parsed into tool summaries (and is not duplicated raw).

Layer 2 is independent of layer 3 and can land alongside it. Layers 4 and 5 are
independent of each other and can land in either order once (3) is in.

## Trade-offs

- **In-sandbox file vs. shipping to the server (the issue's literal ask).** The
  issue proposes shipping logs to the server, persisting them, and rendering
  them in the Web UI. That is strictly more capable — it survives sandbox
  destruction and needs no shell — but it is also much larger: a log-ingest RPC
  (or stream), a storage model and migration, retention/GC on the server, and a
  Web UI log viewer, plus back-pressure and ordering concerns on a hot path.
  This proposal takes the deliberately small path: reuse the reverse-shell that
  already exists as the viewer and the container's own disk as the store. It
  does **not** survive `Prune()` of an archived task or a container recreate — a
  known, accepted limitation (see Open Questions). It closes the most common
  gap (debugging a *failed but not yet archived* run, especially opaque setup
  failures) with a few tees and no new surface.

- **One append-only file vs. per-run files.** Per-run/per-version files would
  make "show me run 2" a single `less` and bound each file's size, but they add
  filename bookkeeping and cross-run retention logic. The KISS choice is one
  file with a grep-able run delimiter; an operator finds a run by scrolling or
  `grep -n '==== run'`. Chosen per maintainer direction.

- **No rotation vs. bounded size.** Skipping rotation removes the lumberjack
  dependency wiring and the retention/pruning logic entirely. The cost is
  unbounded file growth on a very long-lived, heavily-reused sandbox. Accepted
  as a deliberate KISS trade-off; rotation can be added later behind the same
  sink without touching the tee call sites if it ever proves necessary.

## Open Questions

- **Pruned/recreated sandboxes are unrecoverable.** When `Runner.Prune()`
  removes an archived task's container, or a backend recreates rather than
  adopts, the log dies with it — the same failure mode the issue calls out, now
  narrowed to *archived* or *recreated* sandboxes rather than *every* sandbox.
  Is closing the gap for live/finished-but-retained sandboxes enough, or is
  server-side shipping still wanted as a follow-up? This design does not
  preclude it: the same `sink` could later also feed a network shipper.
- **Non-Docker backends.** The plan assumes an ordinary writable container
  filesystem reachable by the reverse-shell. The Lambda MicroVM backend already
  uses `/gritz/...` control-surface paths (`lambdamicrovm.go`); confirm the
  `/gritz/log` file does not collide and that the microVM's filesystem persists
  across the adopt/reuse path the same way Docker's does.
- **Secret hygiene.** Setup stdout/stderr and Claude stderr may contain
  secrets, and this writes them to disk inside the sandbox. That disk is only
  reachable by an operator who can already `gritz shell` into the sandbox (the
  same trust boundary as running the agent), so the exposure is arguably
  unchanged — but it should be an explicit, accepted decision, not implicit.
