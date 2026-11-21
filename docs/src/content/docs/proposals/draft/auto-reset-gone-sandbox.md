---
title: Auto-reset a gone sandbox
description: "A Lambda MicroVM lives for at most 8 hours, so the first start of a task after that fails with a gone sandbox instead of running."
---

Issue: https://github.com/gritzapp/gritz/issues/2282

## Problem

A Lambda MicroVM runs for at most `max_duration_seconds`, 8 hours by default and
at most. After that the platform terminates it, suspended or not. A task is
bound to its sandbox, so the task's next start — a user's restart, a PR comment
routed to it, a schedule — boots a VM that no longer exists, and the run fails:

```
Sandbox failed: microvm vm-0123 is TERMINATED (...): backend: sandbox is gone
```

The failure saves nothing. The VM's disk is already gone, and the runner already
dropped its record, so the *next* start creates a fresh sandbox. All the failure
does is throw away the wake that caused it: the user has to notice and restart
the task, and a PR comment that arrived overnight is never acted on until they
do. The [operator docs](/operators/backends/#lambda-microvms) work around it by
telling operators to keep tasks that wake up over days on Docker.

### Today's code

[`TaskRunner.boot`](https://github.com/gritzapp/gritz/blob/master/internal/runner/taskrunner.go)
reuses the task's sandbox if it has one. When `Sandbox.Boot` returns
`backend.ErrGone`, `boot` calls `clear` (deleting the record and leaving the
`TaskRunner` with no sandbox) and returns `ErrGone`; `RunLoop` reports the run
`failed` with the error as its reason. Every backend reports `ErrGone` the same
way: Lambda for a terminating, terminated or missing VM, Docker and `local` for
a missing container or directory.

`boot` is not the only place a gone sandbox is noticed:

- **`Restore`**, at runner startup, finds the sandbox `StatusGone`, calls
  `clear`, and the manager's `Load` fails the task if it was running. The next
  start then finds no sandbox and creates one **silently** — the same expired VM
  fails the run or doesn't, depending on whether the runner restarted in between.
- **`lost`**, when a driver stops answering mid-run, clears a gone sandbox's
  record and fails the run. That is a run killed partway through, not a run that
  never started; this proposal leaves it alone (see
  [Not in scope](#not-in-scope)).

[Reset sandbox](/proposals/implemented/reset-task-sandbox/) gave users a manual
way to drop a sandbox so the next start creates a fresh one, and listed
auto-reset as future work: the runner creates the new sandbox itself, without the
`RESET` command, so reset's clobber window doesn't apply.

## Design

When a run's boot finds the task's sandbox gone and the workspace sets
`auto_reset: true`, the runner creates a fresh sandbox and starts the same run in
it, instead of failing the run. It reports
that it did, and the task's timeline shows **"Sandbox reset: …"** with the
reason, before the run's "Sandbox started". The run's version, its command and
the task's history are unchanged; the agent starts with a clean session in the
new sandbox and sees the whole history through `get_my_task`, exactly as after a
manual reset.

The three questions the issue leaves open:

- **Where the option lives: the workspace, `auto_reset: true | false`, default
  `false`.** Whether a run may land in a fresh sandbox is a property of
  the work, not of the runtime or the runner: a workspace whose setup commands
  rebuild everything from the clone is fine with it; one whose agent keeps
  unpushed state on disk may want a human to look first. A workspace is also the
  unit the runner already reads per run (`backend.Spec.Workspace`), so the
  option needs no new plumbing. Auto-reset is opt-in: a run that unasked
  lands in a fresh sandbox is a change in what a workspace's tasks can assume,
  so the operator who knows what the workspace's sandboxes hold turns it on.
  Leaving it off keeps today's behaviour.
- **Which backends: any that reports `ErrGone`.** The handling is in
  `TaskRunner.boot`, which is backend-agnostic; a Lambda-only option would need
  the runner to ask which backend it has, for no benefit. A Docker container
  removed by hand or by a host prune is the same situation as an expired VM.
- **What the user sees: a `SANDBOX_RESET` lifecycle entry by the runner, with
  the reason as its message.** It is the entry a manual reset ends with, without
  the "Sandbox reset requested by …" before it, so the timeline reads "Sandbox
  reset: previous sandbox gone: microvm vm-0123 is TERMINATED (…)", then "Sandbox
  started". Without `auto_reset`, the user sees the "Sandbox failed: … sandbox is gone"
  entry they see today.

### Workspace config

`internal/runner/workspace/workspace.go`:

```go
type Workspace struct {
	// ...
	// AutoReset makes a run whose sandbox is gone at boot start in a fresh
	// sandbox instead of failing; without it, the run fails and the next start
	// creates a fresh sandbox.
	AutoReset bool `yaml:"auto_reset"`
}
```

The field is top-level, beside
`secrets:` and `capabilities:`, because it is backend-agnostic, unlike
`container:` and `lambda_microvm:`.

```yaml
workspaces:
  example:
    auto_reset: true
    lambda_microvm:
      # ...
```

The workspace is read when the run boots, so an operator who changes it changes
the next boot of every task in the workspace, like any other workspace field.

### Runner event

The runner reports the replacement itself, through its `RunnerEventOutbox`, as
it reports a manual reset: no driver can, since the old sandbox is gone and the
new one's driver is not up yet. It reuses the `reset` runner event, with the
run's version and the reason the old sandbox was gone.

The `reset` fold gains a second arm. With a `RESET` command in flight, it clears
it, as today (a manual reset). Otherwise it applies only when the event's
version is the task's current version, and changes no field: it exists so
`SubmitRunnerEvents` appends the lifecycle event, which it does only for an
applied event (an automatic reset during a run's boot).

`internal/model/task.go`:

```go
// applyRunnerEventReset records a sandbox reset. With a reset in flight, it
// consumes the reset command once the runner reports the sandbox deleted (a
// manual reset). Otherwise it applies only at the task's current version and
// changes nothing, so the lifecycle entry is recorded: the runner replaced the
// run's gone sandbox at boot and the run carries on in the new one (an
// automatic reset). The status is untouched either way.
func (t *Task) applyRunnerEventReset(e *RunnerEvent) bool {
	if t.Command == TaskCommandReset {
		t.Command = TaskCommandNone
		return true
	}
	return e.Version == t.Version
}
```

`RunnerEvent.LifecycleEvent` maps a `reset` with a `Reason` to
`lifecycle(LifecycleKindSandboxReset, "previous sandbox gone: " + e.Reason)`,
and one without to the message-less entry a manual reset records today.
`LifecyclePayload.Summary` and the web UI's `lifecycleSummary` render
`SANDBOX_RESET` with a message as "Sandbox reset: <message>" (a manual reset's
entry has none, and renders as today). `LifecyclePayload`'s and the proto's
`message` comments gain `SANDBOX_RESET`, and `RunnerEvent.Reason`'s comments
gain the automatic reset. `SubmitRunnerEvents` needs no change: the status
doesn't change, so the status-changed guard keeps it silent on the channel.

The version guard scopes it to its run, so a report that lands after a newer
start is dropped. An outbox redelivery at the current version appends the entry
twice, for an automatic reset and also for a manual one whose command is
already clear; that is the only cost of at-least-once here, and it is rare.

No proto change beyond comments: `RunnerEvent.event` is a string, `reason`
exists, and so does the lifecycle kind.

### Runner

`boot` branches on the workspace's option when `Sandbox.Boot` returns
`ErrGone`:

```go
// boot brings up the task's sandbox for run spec.Version. With no sandbox,
// boot creates one from spec and records its handle; with one, it stamps the
// record with spec.Version and reuses it, since a task is bound 1:1 to its
// sandbox. Then it boots the sandbox.
//
// If the sandbox is gone, boot drops it, leaving tr with no sandbox, and
// follows the workspace's auto_reset: with it, boot reports the run's sandbox
// reset and boots a fresh one; without it, boot returns backend.ErrGone, the
// caller reports the run failed, and the next boot creates a fresh sandbox. Any other failure to boot keeps the sandbox and its
// record, so it can still be destroyed.
func (tr *TaskRunner) boot(ctx context.Context, spec *backend.Spec) error {
	if tr.sbx == nil {
		if err := tr.create(ctx, spec); err != nil {
			return err
		}
	} else if rec := tr.record(tr.sbx, spec.Version); !reflect.DeepEqual(rec, tr.rec) {
		if err := tr.write(ctx, rec); err != nil {
			return err
		}
	}
	gone := tr.sbx.Boot(ctx)
	if !errors.Is(gone, backend.ErrGone) {
		return gone
	}
	tr.clear(ctx)
	if !spec.Workspace.AutoReset {
		return gone
	}
	if err := tr.enqueue(model.RunnerEvent{
		TaskID:  tr.opts.TaskID,
		Event:   model.RunnerEventReset,
		Version: spec.Version,
		Reason:  gone.Error(),
	}); err != nil {
		return err
	}
	if err := tr.create(ctx, spec); err != nil {
		return err
	}
	return tr.sbx.Boot(ctx)
}
```

A sandbox created a moment ago can't have outlived its lifetime, so the second
`Boot` gets no special case: if it returns `ErrGone`, the run fails with it like
any other boot error. `RunLoop` is unchanged — it already treats every `boot`
error as the run's failure, and once the fresh sandbox's driver is up the run
reports `started` as usual.

The order is report, then create, so a crash in between leaves the report with
no new sandbox: the next start finds no record and creates one, and the timeline
already says why. A `create` that fails after the report shows as "Sandbox
reset: …" then "Sandbox failed: …", which is what happened.

**`Restore` stops clearing a gone sandbox.** It keeps the sandbox and its record,
so the next start's `boot` gets `ErrGone` from it and takes the path above. One
code path then decides every gone-at-boot case, and the expired VM behaves the
same whether or not the runner restarted while the task was idle — with `auto_reset`
the timeline gets its entry, and without it the run fails, neither of which
happens today. `Load` is unchanged: it already treats `StatusGone` like
`StatusSuspended`, failing a task that was running. A kept gone sandbox is
otherwise harmless: `destroy` on it is a no-op, so a manual reset or an archive
clears it as usual. `Restore`'s doc comment and its `StatusGone` case go.

### Not in scope

- **A sandbox lost mid-run.** A VM that expires while its driver is running kills
  the run partway; `lost` fails it, as today, and the next start creates a fresh
  sandbox. Restarting the run automatically could repeat whatever the agent had
  already done (a pushed commit, a posted comment), which is a decision for the
  user, not the runner.
- **Replacing a VM before it expires.** The runner could recreate a suspended VM
  nearing `max_duration_seconds`, but that throws away a disk the next run could
  still have used. The point of this proposal is to make expiry harmless, not to
  avoid it.
- **A task-level override.** Nothing per task needs it yet: the workspace
  decides what its sandboxes hold.

## Implementation Plan

1. **Workspace option** — Delivers: the `auto_reset` workspace field. Read by
   nothing yet. Depends on: nothing. Verifiable by:
   review; (3)'s scenarios exercise both values.

2. **Automatic `reset` runner event** — Delivers: the `reset` fold's arm for no
   `RESET` in flight, the `LifecycleEvent` message for a `reset` with a reason,
   the `SANDBOX_RESET` message rendering in `Summary` and
   `webui/src/lib/timeline.ts`, and the `message`, `reason` and `event` comments
   in the model and the proto. Dormant until (3): no runner reports a `reset`
   outside a manual reset. Depends on: nothing. Verifiable by: `internal/model`
   tests for the fold with no `RESET` in flight (applied at the task's version
   with no field changed, dropped at another version) beside the manual-reset
   cases, and for `Summary` with and without a message; `pnpm lint` in
   `webui/`.

3. **Replace a gone sandbox at boot** — Delivers: the `boot` change, and
   `Restore` keeping a gone sandbox. Depends on: (1), (2). Verifiable by:
   three `internal/e2e/scenario` tests on the `local` backend that delete a
   task's sandbox between runs and restart it: with `auto_reset: true`, the run
   completes in a new sandbox and the timeline has "Sandbox reset: previous
   sandbox gone: …"; with the default, the run fails with the gone error; and
   with `auto_reset: true` and the runner restarted while the task is idle, the
   run is replaced the same way (the `Restore` change).

4. **Docs** — Delivers: `auto_reset` in the workspace fields table of
   `operators/workspaces-yaml`, and the Lambda section of `operators/backends`
   saying what happens after `max_duration_seconds` instead of steering
   long-lived tasks to Docker. Depends on: (3). Verifiable by: `cd docs && pnpm
   build`.

## Trade-offs

- **Opt-in vs. on by default.** The failure protects little: the sandbox is
  already gone and the next start creates a fresh one regardless, so what it
  costs is the wake — a review comment that sits unanswered until someone
  restarts the task. But a run that lands in a fresh sandbox unasked is a new
  thing for a workspace's tasks to cope with, and the operator is the one who
  knows whether its setup rebuilds what the agent needs. Opt-in leaves every
  existing workspace as it is; one whose sandboxes are disposable turns it on,
  and the timeline entry keeps the reset from being silent.
- **Workspace vs. backend config (`lambda_microvm:`).** Under the backend it
  would read naturally next to `max_duration_seconds`, but the behaviour is the
  runner's and holds for every backend; a Docker workspace would need its own
  copy under `container:`.
- **Workspace vs. a runner flag.** A runner serves many workspaces whose
  sandboxes hold different things, and a flag can't tell them apart.
- **Workspace vs. per task.** A task-level field needs a proto field, a column,
  a migration, and UI to set it, for a choice that follows from the workspace's
  setup. A per-task override can come later if a real case needs it.
- **Going through the `RESET` command.** The runner could ask the server to reset
  the task and then start it, reusing the manual path end to end. That costs a
  server round trip per expiry, reintroduces the clobber window, and still ends
  in the same `clear` and `create`. The runner already knows the sandbox is gone
  and holds the run; it acts on it directly.
- **A new lifecycle kind (`SANDBOX_REPLACED`).** It would distinguish an
  automatic reset from a manual one without a message, but the outcome is the
  same and users already know "Sandbox reset" from the task page. The missing
  "Sandbox reset requested by …" before it and the reason after it tell the two
  apart.
- **A new `replaced` runner event vs. reusing `reset`.** A separate event would
  keep `reset`'s fold strict — it applies only by consuming the `RESET`
  command, so a redelivered manual reset is dropped once the command is clear.
  Reusing `reset` gives up that guard at the current version (a redelivered
  manual reset appends a second entry), in exchange for one runner event per
  lifecycle kind and no new event type for the server, the runner and the
  proto comment to carry. The duplicate is rare and harmless.

## Open Questions

None. The default is settled above; a workspace whose sandboxes are disposable
sets `auto_reset: true`.
