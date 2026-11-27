# Reset a task's sandbox

Issue: https://github.com/gritzapp/gritz/issues/1958

## Problem

A task is bound 1:1 to the sandbox its runner-store record references. The task's
[`TaskRunner`](https://github.com/gritzapp/gritz/blob/master/internal/runner/taskrunner.go)
holds that sandbox for the life of the task: `boot` creates one only when it has
none, and otherwise stamps the record with the new version and boots the sandbox
it already has. That is deliberate — it is what makes a restart keep the sandbox's
disk (the clone, the agent's session, installed tooling).

The cost is that a sandbox that exists but can no longer boot is a dead end. Every
start or restart boots the same sandbox and fails identically, and `RunLoop` reports
the run `failed`:

```
failed to start container: ... failed to pre-register with sysbox-fs: Initialization error for container-id 0ea8ada2...
```

`boot` only falls through to create when the sandbox is *gone* (`backend.ErrGone`
drops the record). A sandbox that is present but broken, or one whose filesystem
state has gone bad (a confused agent session, a broken checkout, an outdated image),
is kept forever.

Nothing in the API or the web UI drops the binding. The only recoveries today are to
archive the task (its `TaskRunner` destroys the sandbox) and lose it, to create a new
task and lose its links, subscriptions and event history, or to get a shell on the
runner host and `docker rm` the container by hand — none of which is available to a
user of the hosted UI.

## Design

Add a **reset** operation that deletes a finished task's sandbox and nothing else. It
does not start, stop, or restart anything: it drops the task→sandbox binding so that
the *next* start — whenever the user or an event asks for one — finds the
`TaskRunner` with no sandbox and creates a fresh one, exactly as a new task's first
run does. The task keeps its instructions, links, subscriptions and event history,
so the agent in the new sandbox starts with a clean session but sees the whole
history through `get_my_task`.

Three constraints shape everything below:

- **Reset is only valid on a terminal task** (`COMPLETED`, `FAILED`, `CANCELLED`) —
  the same precondition `OpenShell` enforces. A running task's sandbox is live;
  deleting it would be a stop, which is what `CancelTask` is for. Terminal-only means
  reset never has to kill a run or orphan its events.
- **The task's status and version never change.** A reset task stays exactly as
  terminal as it was, on the same run. The only observable state change is that its
  sandbox is gone.
- **The RPC is `ResetSandbox(task_id)`**, named for the sandbox rather than the task
  (like `OpenShell(task_id)`): `ResetTask` would read as resetting the task itself.

The runner already has the mechanism: `TaskRunner.destroy` destroys the sandbox and
`clear` deletes its record, which is what an archived task's `RunLoop` does. What is missing is
a command that asks for it without archiving.

### Command and completion event

Runners learn what to do from the task's command events: every change to a task's
`command` or `version` writes a `CommandPayload` event in the same transaction
(`Task.CommandEvent`), the runner replicates its tasks' events into `runnerstore`
(`taskevents.Log`), and each task's `TaskRunner` acts on the latest `TaskChange`. So
reset is a fourth `TaskCommand`, `RESET`, and it rides that path with no changes to
replication: `runnerstore.TaskChanges` already takes the latest command event of any
value, and the runner's `tasks` table stores the command as a plain integer.

The driver owns the run events (`started`, `stopped`, `failed`), but reset is not a
run and no driver takes part in it — the sandbox is suspended, and is about to be
deleted. The runner reports it itself, through its `RunnerEventOutbox`, the same way
`TaskRunner.next` already reports `stopped` for a stop no driver is around to report.
It gets its own completion event, `"reset"`: `RunnerEvent.event` is a plain string in
the proto, and `SubmitRunnerEvents` is generic — it folds, updates, appends whatever
`LifecycleEvent` and `CommandEvent` return, and notifies. A new event type is one
`RunnerEventType` constant, one fold arm and one `LifecycleEvent` case; the handler
does not change.

The full round trip:

1. `ResetSandbox` sets `command = RESET` on a terminal task with no command in flight,
   and writes a `RESET_REQUESTED` lifecycle event and the command event. Status and
   version are untouched.
2. `PendingRunner()` is non-empty (the command is not `NONE`), so the notification
   wakes the task's runner, whose `taskevents.Log` syncs and hands the `TaskChange`
   to the task's `TaskRunner` (creating one, with no sandbox, if the task had none).
3. `TaskRunner.next` sees `RESET`, destroys the sandbox, enqueues
   `{event: "reset", version: c.Version}`, and then deletes the sandbox's record.
4. The fold clears the command back to `NONE`, leaving the status alone, and
   `SubmitRunnerEvents` appends the runner's `SANDBOX_RESET` lifecycle event and the
   clearing command event.

### Proto

`proto/gritz/v1/gritz.proto`:

```proto
enum TaskCommand {
  NONE = 0;
  RESTART = 1;
  STOP = 2;
  START = 3;
  RESET = 4;  // Delete the task's sandbox; does not start the task.
}

enum LifecycleKind {
  // ...
  LIFECYCLE_KIND_SANDBOX_FAILED = 10;
  LIFECYCLE_KIND_RESET_REQUESTED = 11;  // A user asked for the sandbox to be reset.
  LIFECYCLE_KIND_SANDBOX_RESET = 12;    // The runner deleted the sandbox.
}

message TaskActions {
  bool archive = 1;
  bool cancel = 2;
  bool restart = 3;
  bool start = 4;
  bool unarchive = 5;
  bool reset = 6;
}

service GritzService {
  // ...
  rpc ResetSandbox(ResetSandboxRequest) returns (ResetSandboxResponse);
}

// ResetSandboxRequest deletes a finished task's sandbox, so that its next run
// starts in a fresh one. The task's status, version and history are unchanged.
message ResetSandboxRequest {
  int64 task_id = 1;
}

message ResetSandboxResponse {}
```

The comment on `RunnerEvent.event` gains `"reset"`. Two lifecycle kinds, like
`RESTARTED` (the user's request) and `SANDBOX_STARTED` (the runner's report), so the
timeline never claims a sandbox was reset before it was — and a request that was
superseded (see [The clobber window](#the-clobber-window)) shows as a request with
no completion after it.

`tasks.command` is `integer DEFAULT 0 NOT NULL` with no check constraint on the server,
and `INTEGER` in the runner store, so **no migration is needed** in either.

### Model

`internal/model/task.go`:

```go
// TaskCommandReset asks the runner to delete the task's sandbox. It is not a
// run command: nothing is started, stopped or restarted, and the task's status
// and version are unchanged. The runner's "reset" event consumes it.
TaskCommandReset TaskCommand = TaskCommand(gritzv1.TaskCommand_RESET)

// RunnerEventReset reports that the runner deleted the task's sandbox and its
// record. It is the completion event for TaskCommandReset.
RunnerEventReset RunnerEventType = "reset"
```

`internal/model/event.go` gains `LifecycleKindResetRequested` and
`LifecycleKindSandboxReset`, and `LifecyclePayload.Summary` renders them as "Sandbox
reset requested" and "Sandbox reset".

The transition pair — unlike `Restart`, it touches neither status nor version:

```go
// CanReset reports whether the task's sandbox can be deleted. Only a finished
// task qualifies: a live sandbox is stopped (Cancel), not deleted. A command
// already in flight holds the slot, so reset waits its turn.
func (t *Task) CanReset() bool {
	return !t.Archived && t.Status.IsTerminal() && t.Command == TaskCommandNone
}

// Reset sets the reset command, asking the runner to delete the task's
// sandbox. Status and version are deliberately untouched: no run starts or
// ends. The next start finds no sandbox and creates a fresh one.
func (t *Task) Reset() bool {
	if !t.CanReset() {
		return false
	}
	t.Command = TaskCommandReset
	return true
}
```

The fold arm in `ApplyRunnerEvent`, after the existing version guard:

```go
// applyRunnerEventReset consumes the reset command once the runner reports the
// sandbox deleted. The status is deliberately untouched. A reset event with no
// reset pending — a redelivery (the outbox is at-least-once), or a duplicate
// the runner reported for a repeated TaskChange — is a no-op.
func (t *Task) applyRunnerEventReset() bool {
	if t.Command != TaskCommandReset {
		return false
	}
	t.Command = TaskCommandNone
	return true
}
```

`RunnerEvent.LifecycleEvent` maps `reset` to `lifecycle(LifecycleKindSandboxReset, "")`,
and `Task.Proto` reports `Actions.Reset: t.CanReset()`. No other fold arm changes:
`RESET` is only set on a terminal task with no command, and a terminal task's
`started`, `stopped` and `failed` folds are already no-ops for it.

`CanArchive` already requires `Command == NONE`, so a task with a reset in flight
cannot be archived by hand or by the archiver (`ListTasksDueForArchive` filters on
`command = 0`) — the reset completes first, then the archive proceeds. Nothing to
change. Likewise a task can never be both archived and have `RESET` pending.

### Server

`internal/server/apiserver/task.go` gains `ResetSandbox`, structurally a copy of
`RestartTask`:

- the same `OpTaskWrite` gate — the coarse `AllowOp` pre-check plus the per-row
  `Allow(OpTaskWrite, task.ScopeAttr()...)` inside the transaction;
- `task.Reset()` inside `WithTx` on the row from `GetTaskForUpdate`, returning
  `CodeFailedPrecondition` with the status and command in the message when it refuses
  (a non-terminal or archived task, or one with a command in flight);
- a `LIFECYCLE_KIND_RESET_REQUESTED` event with the user actor and `from == to`
  status, then `task.CommandEvent(from)` — the command changed, so it always writes
  one, which is what the runner acts on;
- `notification.Runner = task.PendingRunner()` to wake the runner, and resources
  `{Action: "reset", Type: "task"}` and `{Action: "appended", Type: "task_events"}`.
  The web UI's SSE consumer keys on the resource type, so it refetches with no client
  change.

No channel message: nothing about the task's outcome changed.

The runner's completion event needs no server work — `SubmitRunnerEvents` folds it,
appends the runner-actor `SANDBOX_RESET` lifecycle event and the clearing command
event, and publishes `updated`/`appended` through the existing path. One guard is
needed: its terminal-status `switch` would stamp `TaskStatus` and a "Task N failed."
channel message on the reset's notification, since the task is still `FAILED`. The
handler runs that `switch` only when the fold changed the status
(`task.Status != original.Status`). No existing event is affected: every fold that
lands on a terminal status changes it.

The server side is idempotent: the fold consumes a `reset` event only while the task
has `RESET` in flight, and otherwise returns false, so `SubmitRunnerEvents` writes
nothing and publishes nothing for it. A duplicate or stale report — an outbox
redelivery, a second report for a repeated `RESET` report (see below), or a fresh
`TaskRunner` acting on a `RESET` left in its store — is harmless.

### Runner

Reset is handled in `TaskRunner.next`, beside the two reports that already need no
driver (a stop no run is open to report, and an archive). It takes no slot and boots
nothing:

```go
case c.Command == gritzv1.TaskCommand_RESET && seq > h.reset:
	// Destroy the sandbox, report the reset, and only then delete its
	// record, so the record marks a reset in progress until the report is
	// durable. A failed destroy is retried with backoff.
	if err := tr.destroy(ctx); err != nil {
		if ctx.Err() != nil {
			return c, false, context.Cause(ctx)
		}
		tr.opts.Log.Error("failed to reset sandbox", "task", tr.opts.TaskID, "err", err)
		timer = time.After(retry.NextBackOff())
		break
	}
	h.reset = seq
	if err := tr.enqueue(model.RunnerEvent{
		TaskID:  tr.opts.TaskID,
		Event:   model.RunnerEventReset,
		Version: c.Version,
	}); err != nil {
		return c, false, err
	}
	tr.clear(ctx)
```

`destroy` only destroys the sandbox (a no-op without one) and leaves the record to
its caller: the archive case calls `destroy` then `clear`, and the reset case
enqueues its report in between.

The case sits after `c.Archived` (which cannot coexist with `RESET`) and is mutually
exclusive with the start and stop cases. `next`'s doc comment gains the third report it
handles. A reset is not versioned (two resets of the same run are both legitimate and
both must be reported, or the second would strand its command), so it is keyed on the
report instead: `Change` numbers each report (`seq`, which replaces `hasChange`), and
`handled.reset` is the number of the last one `next` reset for. Every new `RESET`
report is carried out, even one that repeats the last, but a wake with no new report
is not — and there always is one: the manager hands a `TaskRunner` its first report
before `RunLoop` starts, so without the key every reset on a new `TaskRunner` would be
reported twice.

The order — destroy, report, delete the record — makes the record the durable marker
of a reset in progress, and the command the guard on the old sandbox:

- A crash before the report is enqueued leaves the record and the `RESET` command.
  The restarted runner's Load opens the record and finds the sandbox gone (dropping
  the record) or still there, and `next` resets again — `Destroy` on a gone sandbox is
  a no-op — and reports it.
- A crash after the enqueue leaves the report to the outbox. If the record survived
  too, Load finds the sandbox gone and drops it.
- The command is cleared only by the report, which is enqueued only once the sandbox
  is gone, so no start can be issued against a reset whose sandbox still exists, and
  none can boot it. A destroy that keeps failing keeps the command in flight, which
  also blocks archiving.

Behaviour notes:

- **No sandbox.** The task never ran on this runner, its sandbox already went with a
  previous reset, or Load found it gone and dropped the record: `next` reports the
  reset without touching the backend or the store. Reset is
  idempotent from the user's point of view.
- **A `RESET` that arrives during a busy period.** The task turns terminal when the
  driver's `stopped` or `failed` folds, which can be just before the driver goes idle
  and `drive` returns. `session` forwards the command event to the driver, whose loop
  ignores any command but start, restart and stop; once `busy` has suspended the
  sandbox, `next` sees `RESET` and destroys it — the same "waits for the drive to end"
  rule the archive follows. The driver needs no change.
- **A repeated report.** `taskevents.Log` reports every stored task again when the
  runner boots, so a runner restarted before the clear replicated sees `RESET` twice.
  A second report that lands after the fold finds no reset in flight and is ignored.
  A second `RESET` report before the fold enqueues a second `reset` event at the same
  version. If it lands after the user has reset again at that version, it clears the
  second reset before the runner acts on it — harmless, because nothing can have
  created a sandbox in between: a sandbox is only created by a boot for a start, and a start bumps the
  version, which the second event's version guard would then reject.
- **Retry is bounded by the command's lifetime.** The command stays on the server
  until a `reset` event consumes it, and `next` retries a failed destroy with the
  backoff it already uses for an archive's. A runner restart re-reports the task's
  stored state, so a reset in flight when the runner stopped is carried out after
  Load.
- **A durable-write failure is fatal**, as everywhere else in the `TaskRunner`: `enqueue`
  returns a `fatalError`, and the manager terminates the runner.

The next start then takes the existing path: `wantsRun(c) && c.Version > h.driven`,
`boot` with `tr.sbx == nil`, `create` from the workspace's current spec, a fresh
record. The new driver's event file is empty, so the runner forwards the task's whole
log; the driver acts only on the latest command, a start the run record has not
reached.

### The clobber window

`Start` and `Restart` don't check the command slot, so a start or restart that lands
between `ResetSandbox` and the runner acting on it — a routing-rule wake, a schedule,
`OpenShell`, a user's restart — replaces `RESET` with `START` or `RESTART` at a new
version. If the runner's next sync carries both command events, `TaskChanges` keeps
only the latest, and the reset is never carried out: the run boots the old sandbox.

**Decision: accept the window.** It lasts from the commit to the runner's next sync,
which the reset's own notification triggers, so in practice it is the length of one
`ListRunnerEvents` round trip, or a runner outage. Closing it would cost more than it
saves:

- Refusing `Start`/`Restart` while a reset is pending would drop inbound event wakes
  for that window, which is worse than a lost reset.
- A `sandbox_reset` flag beside the command slot composes with a concurrent start, but
  needs migrations on both stores, a wider `PendingRunner`, and a runner that destroys
  before boot in `RunLoop` as well as in `next`.

Either way the outcome is visible, not silent: the timeline shows "Sandbox reset
requested" with no "Sandbox reset" after it. If the runner never saw the `RESET`, the
run boots the old sandbox, and if that is still wedged it fails again and the user
resets again. If the runner did act on it before the start replicated, the sandbox is
gone and the run gets a fresh one; only the completion entry is missing, because the
start moved the version and the guard rejects the `reset` event.

### A leftover `gritz-{id}` container with no record

`destroy` acts on the *recorded* sandbox, so if the record was lost while the
container survived, reset reports success and the next start fails on Docker's name
conflict for `gritz-{task-id}`.

**Decision: out of scope for reset; reset never deletes by name.** Reset goes through
`backend.Sandbox`, which is backend-agnostic and has no by-name lookup (Lambda microVMs
have no task-derived name), and the runner store and the containers live on the same
host, so the record is lost only when an operator wipes the runner's state. That is a
reconciliation concern for the Docker backend's `Create`, separate from reset, and the
failure is visible in the run's `SANDBOX_FAILED` message.

### Web UI

- `webui/src/lib/task.ts`: `canResetTask(task) => task.actions?.reset ?? false`.
- `webui/src/components/task-sidebar.tsx`: a `SidebarAction` row after Restart, icon
  `Trash2`, label **"Reset sandbox"**, `destructive`, shown when `canResetTask` — so a
  running task does not offer it. Clicking it resets the task directly, with no
  confirmation dialog, like every other task action.
- `webui/src/routes/tasks.$id.tsx`: `useMutation(resetSandbox, { onSuccess: refetchAll })`,
  passed to the sidebar as `onReset` / `resetPending`.
- `webui/src/components/command-badge.tsx`: `commandStyles` and `commandLabels` are
  exhaustive `Record<TaskCommand, string>` maps, so both gain a `RESET` entry
  (`'reset'`, amber).
- `webui/src/lib/timeline.ts`: `lifecycleSummary` renders `RESET_REQUESTED` as
  "Sandbox reset requested" and `SANDBOX_RESET` as "Sandbox reset"; `lifecycleCategory`
  falls through to `'updated'` for both.

### Running tasks

Reset is not offered on a running task, combined or otherwise. The user cancels, then
resets — two actions, each with obvious semantics, and the second only appears once the
first has landed. A combined "cancel and reset" would need a command that outlives the
run's `stopped` fold, and a runner that destroys after the drive ends, which is the
recreate design this proposal rejected. A user who wants a fresh run resets, then
restarts.

### Not in scope

No CLI subcommand and no `gritz mcp` tool: the wedge is diagnosed from the task page,
and the RPC is there for either to call later.

### Future work: auto-reset

When a task's sandbox vanishes on its own — a Lambda microVM expiring after 8 hours,
for example — the task should get a new sandbox automatically. The runner would
destroy the stale record and create a new sandbox itself, without going through
`ResetSandbox` or the `RESET` command, so the clobber window does not apply. Nothing
here blocks it: the runner side of reset is `TaskRunner.destroy` and `clear` followed by the
existing create-on-boot path, both of which auto-reset can use without the command.

## Implementation Plan

1. **Proto + model** — Delivers: the `RESET` command, `LIFECYCLE_KIND_RESET_REQUESTED`
   and `LIFECYCLE_KIND_SANDBOX_RESET`, `TaskActions.reset`, the `ResetSandbox` RPC
   messages (answered by the embedded `UnimplementedGritzServiceHandler` until slice 3),
   `TaskCommandReset`, `RunnerEventReset`, `Task.CanReset`/`Task.Reset`, the fold arm,
   the `LifecycleEvent` case, and the Go `Summary` cases. Regenerates `internal/proto`, `taskcommand_string.go` and
   `webui/src/gen`, and adds the `RESET` entries the exhaustive `command-badge.tsx`
   maps need to compile. Depends on: nothing (no migration). Verifiable by:
   `internal/model/task_test.go` cases for the transition (allowed from each terminal
   status; refused while archived, non-terminal, or with a command in flight), for the
   fold clearing the command without touching status or version, for a stale-version
   `reset` being rejected, and for a redelivered `reset` being a no-op.

2. **Runner reset handler** — Delivers: the `RESET` case in `TaskRunner.next`. Dormant
   until slice 3, since nothing issues the command yet. Depends on: (1). Verifiable by:
   `internal/runner` tests driving a `TaskRunner` with a `backend.BackendMock` sandbox
   and a `RESET` `TaskChange`, asserting that it destroys the sandbox, deletes the
   record, takes no slot, boots nothing and enqueues a version-stamped `reset`; that a
   task with no sandbox still reports complete; that a failing `Destroy` keeps the
   record and is retried; and that a later start creates a new sandbox.

3. **`ResetSandbox` RPC** — Delivers: the server handler, its lifecycle and command
   events, the notification, and the status-changed guard on `SubmitRunnerEvents`'
   channel message. Depends on: (1), and (2) for the end-to-end path. Verifiable by:
   `internal/e2e/api` tests asserting the command is set with status and version
   unchanged, the `RESET_REQUESTED` and command events, the `FailedPrecondition`
   (running task, command in flight, archived) and `PermissionDenied` paths, and a
   duplicate `reset` event being ignored; and `internal/e2e/scenario` tests, which
   replace (2)'s `TaskRunner` unit tests, that fail a run, reset, see `SANDBOX_RESET`
   and the command cleared with the task still `failed`, then restart it into a newly
   created sandbox — plus a task with no sandbox, a repeated reset, a destroy that
   fails (the reset is not reported until a retry succeeds), a reset issued while the
   runner is down, and a runner restarted while the destroy is failing. This slice
   also moves the record's deletion after the report.

4. **Web UI** — Delivers: the "Reset sandbox" sidebar action and the timeline
   labels. Depends on: (3). Verifiable by: resetting a failed
   task on the dev stack (`mise run dev`), seeing its container disappear from
   `gritz containers` with the task still `failed`, then restarting it into a fresh
   container.

## Trade-offs

- **Reset (destroy-only) vs. recreate (destroy + start).** An earlier draft bundled the
  start, so the command was consumed by the new run's `started` and no new runner event
  was needed. It was rejected in review: the name doesn't say whether the task starts,
  and bundling means you cannot clear a wedged sandbox without also committing to a
  run. Splitting them keeps each operation's name honest — reset deletes, restart runs
  — and the cost is one string-valued runner event the runner reports itself.
  Terminal-only also removes the kill-the-live-run and orphan-its-events complexity
  the recreate design carried.
- **Naming.** "Reset sandbox" over "recreate" (implies a start), "destroy/delete
  sandbox" (accurate but reads like it might delete the task's history), and
  "ResetTask" (implies the task's own state is reset). The noun is the sandbox in every
  surface: RPC, button and timeline entries.
- **The runner reports the completion, not the driver.** The driver owns run events
  because it owns runs; reset has no run and no driver to speak of (the sandbox is
  suspended, and then gone). Booting the driver only to have it report its own
  deletion would cost a boot — impossible for exactly the wedged sandbox this exists
  for.
- **A command-slot value vs. a `sandbox_reset` column.** See
  [The clobber window](#the-clobber-window): the slot needs no migration and no change
  to replication, and its failure mode is a visibly lost intent rather than a stuck
  task.
- **Making `boot` recreate on a failed boot.** Would fix this class of wedge with no
  new API, but it silently discards a sandbox's disk on any transient boot error —
  exactly the state reuse exists to protect — and does nothing for a sandbox that
  boots but whose contents have gone bad. Deleting a filesystem should be explicit and
  user-initiated.
- **Doing nothing (`docker rm` on the host).** Works for a self-hosted operator, but it
  is unavailable to a hosted user and leaves the runner-store record dangling until the
  next boot finds the sandbox gone.