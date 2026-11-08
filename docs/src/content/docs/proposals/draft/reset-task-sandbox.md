---
title: "Reset a task's sandbox"
description: "A task is bound to its sandbox, so a container that exists but cannot start is a dead end: every restart re-adopts it and fails, and users have no way to drop the binding."
---

Issue: https://github.com/icholy/gritz/issues/1541

## Problem

A task is bound 1:1 to the sandbox its taskstate record references. `Runner.Start`
resolves that record and passes the handle to `backend.Launch` as `reuse`, and the
Docker backend's `ensure` **never** falls through to create: it adopts the exact
recorded container or returns `backend.ErrGone`. That is deliberate — it is what makes
a restart preserve the sandbox's disk.

The cost is that a container that exists but can no longer be started is a dead end.
Every restart re-adopts it and fails identically:

```
adopting existing container task=1541 container=0ea8ada2...
failed to start task ... failed to pre-register with sysbox-fs: Initialization error for container-id 0ea8ada2...
```

Nothing in the API or the web UI drops the binding. The only recoveries today are to
archive the task and wait for `Runner.Prune`, or to get a shell on the runner host and
`docker rm` the container by hand — neither of which is available to a user of the
hosted UI.

## Design

Add a **reset** operation that deletes a finished task's sandbox and nothing else. It
does not start, stop, or restart anything: it drops the task→sandbox binding so that
the *next* start — whenever the user or an event asks for one — takes
`backend.Launch`'s create-fresh path instead of adopting the dead container.

Two constraints follow from "it must not start the task", and they shape everything
below:

- **Reset is only valid on a terminal task** (`COMPLETED`, `FAILED`, `CANCELLED`) —
  the same precondition `OpenShell` already enforces. A running task's sandbox is
  live; deleting it would be a stop, which is what `CancelTask` is for. This drops all
  of the kill/orphaned-run machinery a "recreate" would need.
- **The task's status never changes.** A reset task stays exactly as terminal as it
  was. The only observable state change is that its sandbox is gone.

The runner already has the mechanism: `Runner.Remove` destroys the backend sandbox and
deletes the taskstate record, and `Runner.Start` creates a fresh sandbox when no record
exists. What is missing is a way for a user to ask for it.

### Command and completion event

The runner learns about work through the task's command slot, so reset is a fourth
`TaskCommand`. Unlike the other three, no existing runner event consumes it — `started`
and `stopped` both describe a *run*, and reset does not produce one. It therefore gets
its own completion event, `"reset"`.

That is cheaper than it sounds: `RunnerEvent.event` is already a plain string in the
proto (`"started"`, `"stopped"`, `"failed"`), and `SubmitRunnerEvents` is fully generic
— it folds, updates, appends whatever `LifecycleEvent` returns, and notifies. A new
event type is one `RunnerEventType` constant, one fold arm, and one `LifecycleEvent`
case. No handler changes.

The full round trip:

1. `ResetSandbox` sets `command = RESET` on a terminal task. Status and version are
   untouched — there is no new run to scope, and the old run is long over, so nothing
   needs orphaning.
2. `PendingRunner()` is non-empty (command != none), so the notification wakes the
   task's runner immediately.
3. The runner destroys the sandbox, drops the record, and enqueues `{event: "reset",
   version: task.Version}`.
4. The fold clears the command back to `NONE`, leaving the status alone.

### Proto

`proto/gritz/v1/gritz.proto`:

```proto
enum TaskCommand {
  NONE = 0;
  RESTART = 1;
  STOP = 2;
  START = 3;
  RESET = 4;  // delete the sandbox; does NOT start the task
}

enum LifecycleKind {
  // ...
  LIFECYCLE_KIND_SANDBOX_RESET = 11;
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

message ResetSandboxRequest {
  int64 task_id = 1;
}

message ResetSandboxResponse {}
```

`RunnerEvent.event` is an untyped string, so `"reset"` needs no proto change (the
comment on the field gains it). `tasks.command` is a plain
`integer DEFAULT 0 NOT NULL` with no check constraint and `ListTasksForRunner` filters
on `command != 0`, so **no migration is needed** and the new command reaches the runner
through the existing query.

The RPC is named for the sandbox rather than the task (`ResetSandbox(task_id)`, like
`OpenShell(task_id)`) precisely because of the ambiguity raised in review: `ResetTask`
would read as resetting the task itself.

### Model

`internal/model/task.go`:

```go
// TaskCommandReset asks the runner to delete the task's sandbox. It is not a
// run command: nothing is started, stopped, or restarted, and the task's
// status is unchanged. It is consumed by the runner's "reset" event.
TaskCommandReset TaskCommand = TaskCommand(gritzv1.TaskCommand_RESET)

// RunnerEventReset reports that the runner destroyed the task's sandbox and
// dropped its record. It is the completion event for TaskCommandReset.
RunnerEventReset RunnerEventType = "reset"
```

The transition pair — note that unlike `Restart`, it touches neither status nor
version:

```go
// CanReset returns true if the task's sandbox can be deleted. Only a finished
// task qualifies: a live sandbox must be stopped (Cancel), not deleted. A
// command already in flight takes the slot, so reset waits its turn.
func (t *Task) CanReset() bool {
	return !t.Archived && t.Status.IsTerminal() && t.Command == TaskCommandNone
}

// Reset sets the reset command, asking the runner to delete the task's
// sandbox. The task's status and version are deliberately untouched: no run is
// starting or ending, so there is nothing to scope. The next start (whenever it
// comes) finds no record and creates a fresh sandbox.
func (t *Task) Reset() bool {
	if !t.CanReset() {
		return false
	}
	t.Command = TaskCommandReset
	return true
}
```

The fold arm in `ApplyRunnerEvent`, which only clears the command:

```go
// applyRunnerEventReset consumes the reset command once the runner reports the
// sandbox destroyed. The status is deliberately untouched — the task is as
// terminal after the reset as it was before it. Guarding on the command makes
// this idempotent: a redelivered reset (the outbox is at-least-once) is a no-op.
func (t *Task) applyRunnerEventReset() bool {
	if t.Command != TaskCommandReset {
		return false
	}
	t.Command = TaskCommandNone
	return true
}
```

`RunnerEvent.LifecycleEvent` maps `reset` to
`lifecycle(LifecycleKindSandboxReset, "")`, and `Task.Proto` reports
`Actions.Reset: t.CanReset()`. `LifecyclePayload.Summary` (Go) and `lifecycleSummary`
(TS) gain a "Sandbox reset" case. No other fold arm changes: `RESET` never coexists
with a run, so `started`/`stopped`/`failed` need no new handling.

`CanArchive` already requires `Command == None`, so a task with a reset in flight
cannot be archived by hand or by the archiver worker (`archiver.go` goes through
`t.Archive()`) — the reset completes first, then the archive proceeds. Nothing to
change.

### Server

`internal/server/apiserver/task.go` gains `ResetSandbox`, structurally a copy of
`RestartTask`:

- the same `OpTaskWrite` gate — the coarse `AllowOp` pre-check plus the per-row
  `Allow(OpTaskWrite, task.ScopeAttr()...)` inside the transaction;
- `task.Reset()` inside `WithTx` on the row from `GetTaskForUpdate`, returning
  `CodeFailedPrecondition` with the status/command in the message when it refuses (a
  non-terminal task, or one with a command already in flight);
- a `LIFECYCLE_KIND_SANDBOX_RESET` event with the user actor and `from == to` status,
  so the timeline records who asked;
- `notification.Runner = task.PendingRunner()` to wake the runner, and
  `{Action: "reset", Type: "task"}`. The SSE consumer switches on the resource `type`,
  not the action, so the web UI invalidates with no client change.

No channel message: nothing about the task's outcome changed.

The runner's completion event needs no server work at all — `SubmitRunnerEvents` folds
it, appends the runner-actor `SANDBOX_RESET` lifecycle event, and publishes the
`updated`/`appended` notification through the existing generic path.

### Runner

Reset is not a run command, so it is handled before the `switch` on command in
`Runner.Poll` — no semaphore slot (nothing is launched), no `Start`, no version guard
(a repeated destroy is harmless: `Destroy` is idempotent and `Remove` with no record is
a no-op):

```go
case model.TaskCommandReset:
	g.Go(func() error {
		// Delete the sandbox and drop its record so the next start creates a
		// fresh one. Nothing is launched here — reset is not a run command.
		// Remove destroys via the backend first and deletes the record only on
		// success, so a failed destroy leaves the task tracked and the command
		// set: the next poll retries.
		if err := r.Remove(ctx, task.ID); err != nil {
			r.log.Error("failed to reset sandbox", "task", task.ID, "err", err)
			return nil
		}
		r.log.Info("sandbox reset", "task", task.ID)
		// Report completion so the server clears the command. Losing this event
		// would strand the command, so a failed durable enqueue is fatal, as
		// everywhere else in Poll.
		if err := r.queue.Enqueue(model.RunnerEvent{
			TaskID:  task.ID,
			Event:   model.RunnerEventReset,
			Version: task.Version,
		}); err != nil {
			r.die(fmt.Errorf("persist %s for task %d: %w", model.RunnerEventReset, task.ID, err))
		}
		return nil
	})
```

Behaviour notes:

- **No record** (the sandbox was already pruned, or the task never ran): `Remove`
  returns nil without touching the backend and the reset still reports complete. Reset
  is idempotent from the user's point of view.
- **The sandbox is somehow still running.** The server's terminal-status precondition
  means this shouldn't happen, but `Destroy` is `ContainerRemove` with `Force: true`,
  so it would be removed rather than the runner wedging. The removed container's
  `supervise` goroutine (if any) sees `Wait` return `ExitLost` and enqueues a
  version-scoped `failed`, which folds a still-`RUNNING` task to `FAILED` — the
  correct outcome for a sandbox that disappeared.
- **Retry is bounded by the command's lifetime.** The command survives on the server
  until a `reset` event consumes it, so a transient Docker failure is retried on each
  poll until it succeeds.

### Web UI

- `webui/src/lib/task.ts`: `canResetTask(task) => task.actions?.reset ?? false`.
- `webui/src/components/task-sidebar.tsx`: a `SidebarAction` row, icon `Trash2`, label
  **"Reset sandbox"**, `destructive`. Because it discards the sandbox's filesystem (the
  clone, any uncommitted work), it is the first task action gated behind a confirmation
  — a small `Dialog` (the primitive is already vendored at `components/ui/dialog.tsx`)
  spelling out that the sandbox and everything in it is deleted, that the task itself is
  untouched, and that **the next run builds a fresh sandbox from the workspace image**.
- `webui/src/routes/tasks.$id.tsx`: `useMutation(resetSandbox, { onSuccess: refetchAll })`,
  wired through as `onReset` / `resetPending`.
- `webui/src/components/command-badge.tsx`: the two `Record<TaskCommand, string>` maps
  are exhaustive, so both gain a `RESET` entry (`'reset'`, amber).
- `webui/src/lib/timeline.ts`: `LifecycleKind.SANDBOX_RESET` → summary "Sandbox reset",
  category `'updated'`.

### Rollout

`Runner.Poll`'s switch has no `default` arm, so a runner older than the command
silently ignores it: the task would sit terminal with a stuck `RESET` command, blocking
archive until someone restarts it (which overwrites the command). The runner slice
therefore lands and deploys **before** the RPC that can issue the command.

## Implementation Plan

1. **Proto + model** — Delivers: the `RESET` command, the `"reset"` runner event,
   `LIFECYCLE_KIND_SANDBOX_RESET`, `TaskActions.reset`, the `ResetSandbox` RPC
   messages, `Task.CanReset`/`Task.Reset`, the fold arm, the `LifecycleEvent` case, and
   both `Summary` cases. Regenerates `internal/proto`, `taskcommand_string.go`,
   `client_moq.go`, and `webui/src/gen`. Depends on: nothing (no migration —
   `tasks.command` is an unconstrained integer). Verifiable by:
   `internal/model/task_test.go` cases for the transition (allowed from each terminal
   status; refused while archived, non-terminal, or with a command in flight), for the
   fold clearing the command without touching status or version, and for a redelivered
   `reset` being a no-op.

2. **Runner reset handler** — Delivers: the `TaskCommandReset` arm in `Runner.Poll`.
   Dormant until slice 3 — nothing can issue the command yet — so it is safe to merge
   and deploy on its own. Depends on: (1). Verifiable by: runner tests with the
   `backend.BackendMock` asserting that a reset poll calls `Destroy`, deletes the
   taskstate record, launches nothing, and enqueues a version-stamped `reset`; that a
   task with no record still reports complete; and that a failing `Destroy` leaves the
   record intact and enqueues nothing.

3. **`ResetSandbox` RPC** — Delivers: the server handler, its lifecycle event and
   notification. Depends on: (1), and on (2) being deployed to the runners. Verifiable
   by: an `apiserver` test asserting the command is set with status and version
   unchanged, the emitted `SANDBOX_RESET` lifecycle event, the `PendingRunner`
   notification, and the `FailedPrecondition` (running task, command in flight) and
   `PermissionDenied` paths.

4. **Web UI** — Delivers: the "Reset sandbox" sidebar action with its confirmation
   dialog, plus the command badge and timeline labels. Depends on: (3). Verifiable by:
   resetting a wedged task from the task page, seeing the sandbox disappear from
   `gritz containers` with the task still `failed`, then restarting it into a fresh
   container.

## Trade-offs

- **Reset (destroy-only) vs. recreate (destroy + start).** An earlier draft of this
  proposal bundled the start, so the command was consumed by the new run's `started`
  and no new runner event was needed. It was rejected in review: the name doesn't say
  whether the task starts, and bundling means you cannot clear a wedged sandbox without
  also committing to a run. Splitting them keeps each operation's name honest — reset
  deletes, restart runs — and the cost is one string-valued runner event, which is
  small because `SubmitRunnerEvents` is generic. Terminal-only also removes the
  kill-the-live-run, orphan-its-events, re-acquire-the-semaphore complexity that the
  recreate design carried.
- **Naming.** "Reset sandbox" over "recreate" (implies a start), "destroy/delete
  sandbox" (accurate but reads like it might delete the task's history), and
  "ResetTask" (implies the task's own state is reset). The noun is deliberately the
  sandbox in every surface: RPC, command badge, button, and timeline entry.
- **A command-slot value vs. a `sandbox_reset` column.** The command slot is a
  single-slot mailbox, so a `Start`/`Restart` issued in the window between
  `ResetSandbox` and the runner's poll overwrites the pending reset (see Open
  Questions). A dedicated boolean column would be orthogonal to commands and compose
  with a concurrent start, at the cost of a migration, a widened
  `ListTasksForRunner`, and a `PendingRunner` that considers it. Given the window is
  one poll — the reset notification wakes the runner immediately — and the failure mode
  is a visibly-lost intent rather than a stuck task, the command slot is the right
  first cut.
- **Making `RESTART` auto-recreate on a failed adopt.** Would fix this class of wedge
  with no new API, but it silently discards a sandbox's disk on any transient adopt
  error — exactly the state the reuse path exists to protect. Deleting a filesystem
  should be explicit and user-initiated.
- **Doing nothing (`docker rm` on the host).** Works for a self-hosted operator and is
  what happened on task 1541, but it is unavailable to a hosted user and leaves the
  taskstate record dangling until the next `Load` reconciles it.

## Open Questions

- **The clobber window.** `Restart`/`Start` don't check the command slot, so a wake
  (routing rule, schedule, `OpenShell`) landing between `ResetSandbox` and the runner's
  poll replaces `RESET` with `START` and the reset never happens. Making `CanStart`/
  `CanRestart` false while a reset is pending would close it but would drop inbound
  event wakes, which seems worse. Options: accept it (the user sees the sandbox is
  still there and resets again), or move to the `sandbox_reset` column above.
- **Should reset be offered for a running task as "cancel, then reset"?** Today the
  user must cancel first and then reset — two clicks, but each with obvious semantics.
  A combined action would need the recreate design's orphaned-run handling.
- **A stale `gritz-{task-id}` container with no record.** `Remove` destroys only the
  *recorded* handle, so if the record was lost while the container survived (a wiped
  taskstate dir, a re-homed runner), reset reports success and the next start fails on
  the Docker name conflict. Should reset additionally delete by name, or is that a
  separate reconciliation concern?
- **Confirmation UX.** No task action confirms today. Is a dialog right, or is
  destructive styling enough?
