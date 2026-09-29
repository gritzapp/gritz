# Create empty task

Issue: https://github.com/icholy/gritz/issues/1606

## Problem

The create-task page (`webui/src/routes/tasks.new.tsx`) has a required **Instructions**
textarea. Every task therefore has to be born with its first instruction typed into a
form that is not the place you actually talk to the task — the task page's chat composer
is. The two inputs do the same thing, in two different shapes, and the form one is the
worse of the pair: no timeline context, no history, and it is the only thing standing
between "pick a runner and a workspace" and "have a task".

The ask is to drop the field: creating a task should produce an **empty task**, and the
first instruction should be sent from the composer on the task page like every
instruction after it.

## Design

The field cannot simply be deleted. `CreateTask` hardcodes `Command: TaskCommandStart`
(`internal/server/apiserver/task.go:97`), so a task created with no instructions is
immediately picked up by the runner and a container is launched for an agent that has
nothing to do.

So the task has to be born in a state the runner ignores and the composer can start.
**That state already exists: completed.**

### An empty task is born completed

A task with nothing to do has, trivially, nothing left to do. Creating it `COMPLETED`
with no command means:

- The runner's work queue is `WHERE runner = $1 AND org_id = $2 AND command != 0 AND
  archived = FALSE` (`internal/store/sql/queries/task.sql:38`) — no command, so no runner
  ever sees it and no container is launched.
- `CanStart()` already returns `true` for `COMPLETED`, and `Start()` already does the
  right thing: `PENDING` + `TaskCommandStart` + a version bump.
- The composer already sends `start: true` with the instruction
  (`webui/src/routes/tasks.$id.tsx:142`), and `UpdateTask` already appends the instruction
  event with `Wake: req.Start && task.CanStart()` and then calls `task.Start()`
  (`internal/server/apiserver/task.go:218-234`).

So the first instruction from the composer produces a `PENDING`/`START` row that the
runner picks up — the same row a freshly created task presents today. **No model change,
no proto change, no runner, driver or agent change.** The whole backend diff is the
condition in `CreateTask`:

```go
task := &model.Task{
	Name:      req.Name,
	Runner:    req.Runner,
	Workspace: req.Workspace,
	Namespace: req.Namespace,
	Status:    model.TaskStatusPending,
	Command:   model.TaskCommandStart,
	Version:   1,
	OrgID:     caller.OrgID,
}
// A task created with nothing to do has nothing to do: born completed, with no
// command for a runner to pick up. The first instruction from the composer
// (UpdateTask with start) is what starts it, exactly as it would resume any
// other completed task.
if len(req.Instructions) == 0 {
	task.Status = model.TaskStatusCompleted
	task.Command = model.TaskCommandNone
	task.Version = 0
}
```

Version 0 is "never provisioned" — the value
`proposals/implemented/task-run-versions.md` reserved for *"any future
create-without-start flow"*, noting that it makes "has this task ever run?" a column
predicate for free. That predicate is load-bearing here: `COMPLETED` at version 1 is what
a task that ran once and finished looks like, so nothing else distinguishes an empty task.
`Start()` bumps it to 1, so the first real run is run 1 either way.

The rule is implicit — *no instructions ⇒ nothing to start* — which is what every
existing caller already means. `CreateTaskRequest` is unchanged, so the scheduler
(`internal/model/schedule.go:76-78`) and the event router
(`internal/eventrouter/eventrouter.go:405-406`), which build their task rows directly and
always have instructions or events, are untouched. `gritz task create` without
instructions and MCP `create_task` get empty tasks for free.

The `Created` lifecycle event is still written, so the timeline is not empty; its
`ToStatus` reads `Completed`. The create notification carries `Runner:
task.PendingRunner()` — `""` for an empty task, which the SSE runner filter
(`internal/server/notifyserver/sse.go:85`) drops for runner subscribers and delivers to
UI subscribers. Exactly right: the UI should see the new task, no runner should be woken
for it.

### What falls out for free

Everything a completed task can do, an empty task can do, with no new arms in any switch:

- **Archive.** `CanArchive()` is `IsDone() && command == NONE` — true. An abandoned empty
  task is one click from gone, with no cancel-then-archive dance.
- **Auto-archive.** `ListTasksDueForArchive` matches `status IN (5,6,7) AND command = 0`,
  so an empty task created with an auto-archive delay is reaped if it is never used. The
  create page's shortest option is 1 hour, so this can't race the user; and reaping
  abandoned empty tasks is the behavior you'd want anyway.
- **Restart** and **start** are both offered; **cancel** is not (nothing to cancel).

### Web UI

`tasks.new.tsx` loses the `instruction` state, the `Instructions` textarea, and the
`!instruction.trim()` guard in `handleSubmit`; `createTask` is called with no
`instructions`. The page keeps name, runner, workspace, namespace and auto-archive, and
still navigates to `/tasks/$id` on success — which now lands on a task whose composer is
the next thing the user touches. Autofocus the composer there, so arriving from the create
page puts the cursor where the instruction goes.

The new task renders with the green **completed** badge. That is the honest projection of
the row and needs no code, but it is a slightly odd first impression for a task that has
never run. To soften it, an `isDraftTask` helper in `webui/src/lib/task.ts` relabels the
badge **draft** in the list, the sidebar and the `StatusDot`.

The predicate is `version == 0`, not the status: a task that ran once and finished is
*also* `COMPLETED` at version 1, so the status cannot tell the two apart — which is why an
empty task is created at version 0 (see above). The first `Start()` bumps it to 1, so the
label self-clears. This is cosmetic and deliberately kept as the last, optional slice.

## Implementation Plan

1. **Server: create without instructions leaves the task completed** — Delivers: the
   conditional in `CreateTask`. Depends on: nothing. Verifiable by: an apiserver test that
   creates a task with no instructions and asserts `status == COMPLETED`, `command ==
   NONE`, `actions.start == true`, `actions.archive == true`, that `ListRunnerTasks` does
   not return it, and that a subsequent `UpdateTask{start: true, add_instructions: [...]}`
   flips it to `PENDING`/`START` with a waking instruction event. Safe to merge alone:
   no existing caller creates an instruction-less task in production.

   Note the test-fixture fallout: `createTestTask` (`internal/server/apiserver/event_test.go:27`)
   and most of the ~66 `CreateTaskRequest` literals across the apiserver tests pass no
   instructions. The ones that only need a row to hang events or links on are unaffected;
   the ones that assert `PENDING`/`START` or exercise the runner queue must now pass an
   instruction. This is a mechanical sweep and belongs in this slice.

2. **Web UI: drop the instructions field** — Delivers: the create page without the
   textarea, and composer autofocus on the task page. Depends on: (1). Verifiable by:
   create a task, land on the task page, send an instruction from the composer, watch the
   container come up — and confirm no container is launched before that.

3. **Web UI: "draft" badge (optional)** — Delivers: `isDraftTask(task)` in
   `webui/src/lib/task.ts` and the relabelled badge/dot. Depends on: (1). Verifiable by:
   a freshly created empty task reads as draft in the list and the sidebar; it reads as
   completed again after its first run.

## Trade-offs

**Completed vs. a dedicated idle state.** The first version of this proposal (#1607)
introduced an *idle* state — `PENDING` + `TaskCommand.NONE`, a pair that is currently
unreachable — plus `Task.IsIdle`, a new arm in `CanStart`, and a version-bump exception in
`Start`. It buys an honest status ("pending, with nothing pending") and a first run at
version 1. It costs a model change with two new transitions to test, and it leaves an
empty task un-archivable without cancelling it first. Being born completed costs three
lines in one handler and zero new transitions, so the entire risk surface is the create
path. The price is a "completed" badge on a task that never ran, and a first run numbered
version 2 — neither of which the UI surfaces today.

**Reusing `COMPLETED` vs. a new `IDLE` status.** A new `TaskStatus` would be
self-describing, but it is a proto enum change plus a DB value plus an arm in every status
switch (`IsTerminal`, `CanCancel`, `CanArchive`, `CanRestart`, the three runner-event
folds), and older clients would render it as "unknown".

**Implicit "no instructions ⇒ don't start" vs. an explicit flag.** A `start` field on
`CreateTaskRequest` would be explicit, but proto3 bools default to `false`, so every
existing client would silently stop starting its tasks; `optional bool` avoids that at the
cost of three-valued logic for a distinction no caller has ever wanted.

**Not starting at all vs. starting an empty agent.** Letting the empty task start and the
agent sit idle needs no backend change, but it burns a container and an agent session to
produce a run that ends before the user's first instruction.

**Removing the field vs. making it optional.** Keeping an optional textarea would be a
one-line variant on top of the same backend work, since `CreateTask` would still have to
handle the empty case. The issue asks for removal, and one instruction input is better
than two.

## Open Questions

- Is the green **completed** badge on a brand-new task acceptable, or is slice (3) part of
  the deal? If it is, is the label **draft**, **empty**, or **new**?
- ~~Should empty tasks be created at `Version: 0`?~~ Yes — settled while writing slice (1).
  It is what `task-run-versions.md` reserved 0 for, it keeps the first real run at
  version 1, and it is the only thing that distinguishes an empty task from one that ran
  once and completed, which slice (3) needs. `taskstate` treats version 0 as a legacy
  record (`internal/runner/taskstate/taskstate.go:30`), which is moot: a task with no
  command never reaches the runner, and `Start()` bumps it to 1 before it does.
- Should MCP `create_task` make `instruction` optional, so an agent can hand a prepared
  empty task to a human? Nothing needs it yet.
