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

### A draft is born completed

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
no runner, driver or agent change.** The whole backend diff is one new request field and
the condition it drives in `CreateTask`:

```protobuf
message CreateTaskRequest {
  // ...
  // Create the task without starting it: no command is set, so no runner picks
  // it up and no sandbox is launched. The first UpdateTask with start (e.g. the
  // first instruction from the task page composer) is what starts it. Defaults
  // to false — an ordinary create, started immediately.
  bool draft = 8;
}
```

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
// A draft is born completed with no command and no run — no runner picks it
// up and nothing is launched.
if req.Draft {
	task.Status = model.TaskStatusCompleted
	task.Command = model.TaskCommandNone
	task.Version = 0
}
```

`draft` is the whole rule: it alone decides whether the task starts. The polarity is what
makes the field safe to add — proto3 bools default to `false`, and `false` is *"not a
draft, start it"*, so every existing client keeps today's behavior byte for byte. (A
`start bool` would have had the opposite default and silently stopped every caller's tasks;
that is why the earlier draft of this proposal rejected an explicit flag and used the
implicit *no instructions ⇒ don't start* rule instead. Inverting the polarity removes the
objection.)

`CreateTaskRequest` is otherwise unchanged, so the scheduler
(`internal/model/schedule.go:76-78`) and the event router
(`internal/eventrouter/eventrouter.go:405-406`), which build their task rows directly,
are untouched — as are every apiserver test fixture and `gritz task create`.

Version 0 is "never provisioned" — the value
`proposals/implemented/task-run-versions.md` reserved for *"any future
create-without-start flow"*. `Start()` bumps it to 1, so the first real run is run 1 just
as it is for an ordinary create; seeding at 1 instead would number the first run 2, with
no run 1 having existed.

The `Created` lifecycle event is still written, so the timeline is not empty; its
`ToStatus` reads `Completed`. The create notification carries `Runner:
task.PendingRunner()` — `""` for a draft, which the SSE runner filter
(`internal/server/notifyserver/sse.go:85`) drops for runner subscribers and delivers to
UI subscribers. Exactly right: the UI should see the new task, no runner should be woken
for it.

### What falls out for free

Everything a completed task can do, a draft can do, with no new arms in any switch:

- **Archive.** `CanArchive()` is `IsDone() && command == NONE` — true. An abandoned draft
  is one click from gone, with no cancel-then-archive dance.
- **Auto-archive.** `ListTasksDueForArchive` matches `status IN (5,6,7) AND command = 0`,
  so a draft created with an auto-archive delay is reaped if it is never used. The
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
the row and needs no code. It is a slightly odd first impression for a task that has never
run, and an earlier draft of this proposal relabelled it **draft**; that was dropped as
unnecessary for the first pass. The badge stays as-is.

## Implementation Plan

1. **Server: `draft` on `CreateTaskRequest`** — Delivers: the proto field and the
   conditional in `CreateTask`. Depends on: nothing. Verifiable by: an apiserver test that
   creates a task with `draft: true` and asserts `status == COMPLETED`, `command == NONE`,
   `version == 0`, `actions.start == true`, `actions.archive == true`, that
   `ListRunnerTasks` does not return it, and that a subsequent `UpdateTask{start: true,
   add_instructions: [...]}` flips it to `PENDING`/`START` at version 1 with a waking
   instruction event. Safe to merge alone: `draft` defaults to false, so every existing
   caller — and every existing test fixture — is untouched.

2. **Web UI: drop the instructions field** — Delivers: the create page without the
   textarea, passing `draft: true`, and composer autofocus on the task page. Depends on:
   (1). Verifiable by: create a task, land on the task page, send an instruction from the
   composer, watch the container come up — and confirm no container is launched before
   that.

## Trade-offs

**Completed vs. a dedicated idle state.** The first version of this proposal (#1607)
introduced an *idle* state — `PENDING` + `TaskCommand.NONE`, a pair that is currently
unreachable — plus `Task.IsIdle`, a new arm in `CanStart`, and a version-bump exception in
`Start`. It buys an honest status ("pending, with nothing pending") and a first run at
version 1. It costs a model change with two new transitions to test, and it leaves a draft
un-archivable without cancelling it first. Being born completed costs one request field
and a branch in one handler, with zero new transitions, so the entire risk surface is the
create path. The price is a "completed" badge on a task that never ran, which the UI shows
as-is.

**Reusing `COMPLETED` vs. a new `IDLE` status.** A new `TaskStatus` would be
self-describing, but it is a proto enum change plus a DB value plus an arm in every status
switch (`IsTerminal`, `CanCancel`, `CanArchive`, `CanRestart`, the three runner-event
folds), and older clients would render it as "unknown".

**An explicit `draft` flag vs. the implicit "no instructions ⇒ don't start" rule.** The
implicit rule needs no proto change, and it is close to tautological — a task with no
instructions has nothing to start. But it couples two independent things, it cannot
express "load these instructions but don't run yet", and it forces every existing test
fixture that creates an instruction-less task to start passing one. The flag costs a proto
field and buys a rule you can read at the call site. A `start bool` would have been the
obvious spelling and is the wrong one: proto3 bools default to `false`, so it would
silently stop every existing caller's tasks. `draft` has the safe polarity — `false` is
today's behavior.

**Not starting at all vs. starting an empty agent.** Letting the empty task start and the
agent sit idle needs no backend change, but it burns a container and an agent session to
produce a run that ends before the user's first instruction.

**Removing the field vs. making it optional.** Keeping an optional textarea would be a
one-line variant on top of the same backend work, since `CreateTask` would still have to
handle the empty case. The issue asks for removal, and one instruction input is better
than two.

## Open Questions

- ~~Is the green **completed** badge on a brand-new task acceptable?~~ Yes — settled. A
  "draft" relabel was considered and dropped as unnecessary for the first pass.
- ~~Should drafts be created at `Version: 0`?~~ Yes — settled while writing slice (1).
  It is what `task-run-versions.md` reserved 0 for, and it keeps the first real run at
  version 1 rather than numbering it 2 with no run 1 having existed. `taskstate` treats
  version 0 as a legacy record (`internal/runner/taskstate/taskstate.go:30`), which is
  moot: a task with no command never reaches the runner, and `Start()` bumps it to 1
  before it does.
- Should MCP `create_task` expose `draft`, so an agent can hand a prepared task to a human
  without running it? Nothing needs it yet.
- `draft` alone decides, so `draft: false` with no instructions still starts a sandbox for
  an agent with nothing to do — exactly today's behavior, and reachable via `gritz task
  create` and MCP `create_task`. Left as-is to keep the flag the single source of truth;
  rejecting it with `InvalidArgument`, or falling back to the implicit rule, are both
  one-liners if the footgun is worth closing.
