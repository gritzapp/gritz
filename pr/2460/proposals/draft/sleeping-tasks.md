# Sleeping tasks and delayed triggers

Issue: https://github.com/gritzapp/gritz/issues/2459

## Problem

A task only runs when something starts it: its creation, a user, or a waking event delivered through a link kind. Nothing starts a task because time has passed.

- **Waiting inside a task.** A coding agent that has to wait for a deploy, a CI run, a rate limit or a follow-up check can't say "start me again in 20 minutes". It either polls inside a live run, holding the sandbox and spending tokens, or it ends its run and hopes a waking event comes.
- **Waiting before a task starts.** A trigger starts its task the moment an event matches. A burst of events on one resource (several review comments in a row, a Jira issue edited three times) can't settle first, and nobody gets a window to look at the task before it starts.

This proposal adds both, as two parts that share one mechanism and land independently:

- **Part A, the sleep tool.** A `sleep` tool in the injected `gritz` MCP server. The task ends its run, goes to a new `SLEEPING` status, and is started again at the requested time, or earlier by a waking event.
- **Part B, delayed triggers.** A `delay` on an agent trigger. The task is created as a draft holding its link and claim, collects the events delivered to it, and starts when the delay has passed.

## Design

### The shared mechanism: `tasks.wake_at`

Both features need the server to start a task at a given time, durably, exactly once, from any server instance. Schedules already have this for creating tasks (`schedules.next_run_at`, `ClaimDueSchedules`, [`internal/server/scheduler`](https://github.com/gritzapp/gritz/tree/master/internal/server/scheduler)). Tasks get the same thing:

- A nullable column **`tasks.wake_at`**: the time at which the server starts the task. `Task.WakeAt` in the model, zero when unset.
- A worker, **`internal/server/waker`**, built like the scheduler: one goroutine ticking every 10 seconds. Each tick, in one transaction, it claims up to 100 due tasks with `FOR UPDATE SKIP LOCKED`, starts each, clears its `wake_at`, writes the events, and commits. It publishes the change notifications after the commit, with `Runner` set so the runner's wake channel picks the task up at once.

This is one mechanism, not two. The timer lives in the database, so it survives a server restart: a task whose `wake_at` passed while no server was up is started on the first tick after boot, once. Claim and fire commit together, so two servers ticking at the same instant can't start a task twice.

`wake_at` is set only in three states. The model keeps it that way, and `store.UpdateTask` panics on any other combination (see [Invariants](#invariants)):

| Status | Meaning of `wake_at` | Set by |
| --- | --- | --- |
| `RUNNING`, command `NONE` | the run asked to sleep; when it exits the task goes to `SLEEPING` | the `sleep` tool (part A) |
| `SLEEPING` | when it wakes | the run exiting with a sleep pending (part A) |
| `DRAFT` | when the delayed trigger starts it | the router creating the task (part B) |

The claim query takes unarchived tasks with a due `wake_at`:

```sql
-- name: ClaimDueTasks :many
-- Lock and return up to $1 unarchived tasks whose wake_at has passed, skipping
-- rows another server instance holds. The caller starts each and clears its
-- wake_at in the same transaction, so on commit the rows are no longer due.
SELECT id, org_id
FROM tasks
WHERE wake_at IS NOT NULL
  AND wake_at <= (NOW() AT TIME ZONE 'UTC')
  AND archived = FALSE
ORDER BY wake_at
LIMIT $1
FOR UPDATE SKIP LOCKED;
```

Firing one task (`waker.fire`) reloads it under the lock and calls `Task.Wake(model.TimerActor)`. That starts it whatever state it is in:

- `SLEEPING` or `DRAFT`: `Start()`, so it goes to `PENDING` at the next version with a `START` command.
- `RUNNING` (the run asked to sleep but is still going at its deadline): `Start()` queues a start, and the task runs again as soon as the run exits. The agent ran past its own deadline, so starting it straight away is what it asked for.

The waker writes the command event and a `WOKEN` lifecycle event ([below](#what-the-agent-is-told-when-it-wakes)). The waker never sees a `wake_at` in any other state, because every transition out of the three states above clears it.

#### Migration

```sql
-- migrate:up

-- Sleeping tasks and delayed triggers. See proposals/draft/sleeping-tasks.md.
-- When the server starts the task: a sleeping task's wake-up, or a delayed
-- trigger's draft's start. NULL when nothing is pending.
ALTER TABLE tasks ADD COLUMN wake_at timestamp without time zone;

CREATE INDEX tasks_wake_at_idx ON tasks (wake_at)
  WHERE wake_at IS NOT NULL AND archived = FALSE;

-- migrate:down

DROP INDEX IF EXISTS tasks_wake_at_idx;
ALTER TABLE tasks DROP COLUMN IF EXISTS wake_at;
```

The column follows the table's other times (`created_at`, `updated_at`): `timestamp without time zone`, in UTC. The partial index keeps each tick's scan tiny, as `schedules`' does.

The trigger's `delay` lives in `agents.triggers`, which is JSON, so it needs no migration. The runner's SQLite state needs none either: the runner acts on command events, and neither feature adds a command.

### Part A: the sleep tool

#### The tool

The injected `gritz` MCP server ([`internal/runner/agentmcp`](https://github.com/gritzapp/gritz/tree/master/internal/runner/agentmcp)) gets a fifth tool, always available:

```
sleep
  Stop working and be started again later. Use it to wait for something that
  takes time (a deploy, a CI run, a rate limit, a follow-up check) instead of
  polling. After calling it, end your turn: the task sleeps once this run ends
  and is started again at the time given, or earlier if a waking event arrives.
  Calling it again replaces the earlier time.

  duration  string  How long to sleep, as a Go duration ("20m", "2h"). Set
                    exactly one of duration and until.
  until     string  When to wake, as an RFC 3339 time.
  reason    string  Required. What you are waiting for, shown to users while
                    the task sleeps.
```

- **Both a duration and a time.** Agents usually think in durations ("check again in 15 minutes"). An absolute time fits "after the rate limit resets at 14:00", where the agent would otherwise do clock arithmetic. Setting both, or neither, is an error.
- **Bounds: 1 minute to 7 days**, checked by the server against its own clock, so a sandbox with a skewed clock can't get around them. Below a minute, the run boundary costs more than the wait and polling is fine. Above 7 days the task is effectively abandoned, and it's better for it to finish and auto-archive. A time in the past, or closer than a minute, is an error, not a clamp, so the agent learns how the tool works. (Open question 1.)
- **`reason` is required.** It is what a user sees in the task list and timeline while the task sleeps, the only answer to "why isn't this running?".

The tool calls a new RPC, `SleepTask`, with the task's own token (`OpTaskWrite` on the task's own id, like `update_my_task`'s `UpdateTask`). On success it returns:

```
Sleeping until 2026-11-27 23:40 UTC. End your turn now without further tool
calls. You will be started again then, or earlier if a waking event arrives.
```

The tool doesn't stop the run. The agent ends its turn, the agent CLI exits, and the run ends the way every run ends. Killing the run from the driver would cut the agent off before it reads the tool result and saves its session. If the agent ignores the instruction and keeps working past its deadline, the waker queues a start and the task runs again right after this run (see above), which is still correct.

#### Precondition: a running run with nothing pending

`Task.CanSleep` holds only for an unarchived `RUNNING` task with command `NONE`. Every other state is reachable from production through a race, so `SleepTask` fails with `FailedPrecondition` instead of panicking:

- **A start is pending** (`RUNNING` with `START`): a waking event arrived during the run, so the task is started again as soon as the run exits. The tool's error says so: "an event is already pending; you will be started again as soon as this run ends, so there is nothing to sleep for."
- **Cancelling or restarting**: the run is being stopped by a user.

#### The `SLEEPING` status

A sleeping task gets a new status, `SLEEPING`, and doesn't reuse `COMPLETED` with a `wake_at`:

- It is not terminal: `IsTerminal` stays completed, failed and cancelled. So the auto-archiver, which only takes statuses 5, 6 and 7 (`ListTasksDueForArchive`), skips sleeping tasks with no change, and the task can't be archived and lose its sandbox while it waits to wake.
- It says what is happening. A completed status with a timer on the side reads as finished in every list, filter and badge that already exists, and each of them would need a special case.

The run's exit makes the transition. `applyRunnerEventStopped` for `RUNNING` with command `NONE` goes to `SLEEPING` when `WakeAt` is set and to `COMPLETED` otherwise. The sandbox lifecycle event records it as usual: `Sandbox exited (Running -> Sleeping)`. The status change writes no command event: the command stays `NONE` and the version stays the same.

How the other transitions treat a sleep:

| Action | From | Result | `wake_at` |
| --- | --- | --- | --- |
| Run exits (`stopped`) | `RUNNING`, sleep pending | `SLEEPING` | kept |
| Run fails (`failed`) | `RUNNING`, sleep pending | `FAILED` | cleared |
| Timer fires | `SLEEPING` | `PENDING`, `START`, version + 1 | cleared |
| Timer fires | `RUNNING`, sleep pending | `RUNNING`, `START` queued | cleared |
| Waking event | `SLEEPING` | `PENDING`, `START`, version + 1 | cleared |
| Waking event | `RUNNING`, sleep pending | `RUNNING`, `START` queued | cleared |
| Non-waking event | either | unchanged | kept |
| User start ("Wake now") | `SLEEPING` | `PENDING`, `START`, version + 1 | cleared |
| User restart | `SLEEPING` | `PENDING`, `RESTART`, version + 1 | cleared |
| User cancel | `SLEEPING` | `CANCELLED` | cleared |
| User cancel | `RUNNING`, sleep pending | `CANCELLING` | cleared |
| User archive | `SLEEPING` | `COMPLETED`, archived | cleared |
| User reset sandbox | `SLEEPING` | not allowed (not terminal) | — |

Notes on the less obvious rows:

- **A failed run drops its sleep.** A failure needs a person, and waking the task on a timer would run a broken sandbox again unattended. (Open question 6.)
- **Archiving a sleeping task completes it.** An archived task never wakes, so it can't stay `SLEEPING`. Archiving moves it to `COMPLETED` along with the archive, so unarchiving it later gives an ordinary completed task, not a sleeping one with no timer. The runner destroys its sandbox as for any archive.
- **Cancel** on a sleeping task needs no runner action, like cancelling a `PENDING` one: it goes straight to `CANCELLED`.
- **Start, restart, cancel and archive** are all in `TaskActions` for a sleeping task, so the web UI, CLI and MCP offer them without new RPCs. The web UI labels start "Wake now" on a sleeping task.

#### Events during the sleep

A **waking event wakes the task early, and cancels the timer.** Delivery goes through `attach` in the event router, which calls `Task.Wake(model.RouterActor)` in place of `Start()`. For a sleeping task that clears `wake_at`, so the timer does not also fire later. The other choice, leaving the timer set, would start the task a second time for nothing, often in the middle of the run the event started. An agent that still needs to wait after handling the event calls `sleep` again. That is one call, and the wake prompt tells it its sleep was cut short (below). (Open question 2.)

A **non-waking event** is added and nothing else happens, as today. The task sees it in the wake prompt when the timer fires.

#### What the agent is told when it wakes

Two lifecycle kinds:

- `LIFECYCLE_KIND_SLEEP_REQUESTED`, written by `SleepTask`. Its `message` is the reason, and the timeline renders it as `Sleep requested until 23:40 UTC: waiting for the staging deploy`.
- `LIFECYCLE_KIND_WOKEN`, written wherever a sleep ends in a start: by the waker (actor `timer`), by `attach` (actor `router`), and by `UpdateTask` with `start` from a user (actor `user`). Its `from_status` is `Sleeping` (or `Running`, for a sleep that hadn't begun yet), and its `message` says when the task was due to wake.

Today the driver puts only instruction and external events in the prompt (`Driver.promptEvents`). It now includes `WOKEN` events as well, and `agentprompt.renderEvent` renders them in words for the agent rather than as the timeline summary:

```
### Woken — 2026-11-27 23:40 UTC
The sleep you asked for, until 2026-11-27 23:40 UTC, is over.
```

```
### Woken — 2026-11-27 23:12 UTC
Your sleep until 2026-11-27 23:40 UTC was cut short by the events below. It is no
longer pending: call gritz:sleep again if you still need to wait.
```

The early-wake `WOKEN` event is written in the same transaction just before the waking event's `CommandPayload`. In the prompt it sits beside the external event that woke the task, ordered by event id. A wake by the timer with no other events renders the events branch of `PROMPT.md` with only the `WOKEN` block, so the agent is never woken with the bare "The task was updated. Continue."

The reason isn't repeated in the wake prompt. Every agent resumes its session on a wake (`Turn.Resume`), so the agent still has the reason in the transcript of the run that called `sleep`.

#### More than one sleep

A task has at most one pending sleep: it is one column. A second `sleep` call in the same run replaces the time, last call wins, and writes another `SLEEP_REQUESTED` event so the timeline shows the change. Allowing several timers would need a table and rules for which one wakes the task. Nothing needs that: an agent waiting for two things sleeps until the first, checks, and sleeps again.

#### The sandbox and the run while sleeping

No run is live. Sleeping costs what a completed task costs: the sandbox is left as the backend leaves any task between runs, and the next run reuses it, setup done, session resumable. That is the reason to sleep in gritz rather than in the sandbox. A `sleep 1200` in the agent's shell holds a live run and a Lambda MicroVM for the whole wait, and is lost if the runner restarts.

#### How it shows

- **Web UI.** The status badge reads `Sleeping`, with the wake time as a relative tooltip ("wakes in 18 min") and the reason from the latest `SLEEP_REQUESTED` event. The task list's status column and status filter gain `Sleeping`. A running task with a pending sleep keeps the `Running` badge and adds "sleeps after this run".
- **CLI.** `gritz task list` shows `SLEEPING` in the status column. `gritz task` shows `Wakes: <time>` when `wake_at` is set, and its status filter accepts `sleeping`. `gritz task update --start` wakes the task now.
- **Server MCP.** `get_task` and `list_tasks` include `wake_at` (RFC 3339) when it is set. `list_tasks`' status filter accepts `SLEEPING`, and `update_task` with `start` wakes it.

#### Interaction with auto-archive

A sleeping task isn't terminal, so it is never auto-archived while it sleeps. The 7-day bound is what keeps sleeping tasks from piling up. Once it wakes and its next run completes, the usual auto-archive delay runs from that completion, so an agent that sleeps in a loop is archived only after the run in which it stops sleeping.

### Part B: delayed triggers

#### The field

```go
type AgentTrigger struct {
	// ... existing fields
	// Delay holds the created task as a draft this long before starting it.
	// Zero starts it at once.
	Delay time.Duration `json:"delay,omitempty"`
}
```

```protobuf
message AgentTrigger {
  // ... existing fields
  // How long the created task waits as a draft before it starts, from 1m to
  // 24h. Unset starts it at once. During the delay it holds its link and claim
  // and collects the events delivered to it.
  google.protobuf.Duration delay = 8;
}
```

- **Bounds: 0, or 1 minute to 24 hours**, checked where the agent is saved, beside the existing trigger checks (link kind exists, `yield_to` names a claim). The waker ticks every 10 seconds, so anything under a minute isn't meaningfully a delay. 24 hours covers debouncing and "give a human a window". A longer wait is a schedule's job. (Open question 3.)
- **Agent editor.** The trigger row gets a "Delay" duration input next to the link and prompt, using the same input and parsing as the agent's auto-archive field, empty for none. The trigger's summary in the agents list and routing view adds "after 10m".
- **`put_agent`.** Each trigger object takes `delay` as a Go duration string ("10m"), like `auto_archive`. The tool description gains one clause: "delay, a duration from 1m to 24h the created task waits as a draft, collecting events for its link, before it starts".

#### Lifecycle

When a trigger with a delay matches, `eventrouter.Router.create` creates the task as it does today, except that the task is a draft (status `DRAFT`, command `NONE`, version 0) with `wake_at` set to now plus the delay. Everything else is unchanged and in the same transaction: the triggering external event first, the `Created` lifecycle event (to status `Draft`), the trigger's link with its claim, and the agent's and trigger's prompts as instructions. A draft has no command, so `CommandEvent(nil)` writes none. The notification has no `Runner`, so no runner is woken. Its channel message reads `Task 123 created by agent "reviewer" for event: …, starts at 23:40 UTC`.

When the delay has passed, the waker starts the draft: `PENDING`, `START`, version 1, `wake_at` cleared, with a `WOKEN` lifecycle event (actor `timer`, from status `Draft`, timeline summary `Started after trigger delay`). From then on it is an ordinary task.

**In the meantime** the draft looks like any draft, plus its start time:

- The web UI badge reads `Draft` with "starts in 7 min", and the task page shows the triggering event and everything delivered since.
- A user can **start it early**: start is already a draft action. It clears `wake_at` and starts the task at once.
- A user can **get rid of it** by archiving it. A draft is already archivable, and there is no delete. Archiving clears `wake_at` and, since `FindSubscribedLinks` skips archived tasks, releases the draft's claim. Unarchiving gives a plain draft with no start time, which a user starts by hand.
- `cancel` doesn't apply (`CanCancel` is false for a draft today), and stays that way: archive is how a draft is abandoned.

#### What the task gets when it starts

Its first run renders the first-run brief, which reads every instruction and external event from the head of the task's event file (`promptEvents` with a zero cursor). So the task gets the triggering event exactly as it arrived, followed by every event delivered to it during the delay, in order. Nothing is merged or dropped. The brief lists the events, and the agent decides what a burst of five comments means.

#### Events during the delay, claims and debouncing

The draft holds its link from creation, so for the whole delay:

- **Delivery.** Events on the resource that the link's kind lists are delivered to the draft (`FindSubscribedLinks` doesn't filter on status) and added to it.
- **A waking event doesn't start a delayed draft.** `Task.Wake` returns false for a `DRAFT` with `wake_at` set, so `attach` only adds the event. That is what makes the delay a debounce: the task starts once, at the deadline, with every event that arrived. Starting it on the first waking event would make the delay useless for the case it exists for. A draft without `wake_at` (made with `create_task` as a draft) is started by a waking event, as today. (Open question 5.)
- **Claims.** The link carries its kind's claim from creation, so any trigger that `yield_to`s that claim yields to the draft for the whole delay. That is intended. A delayed task has, for every routing purpose, already taken the resource. It just hasn't started.

Together these give debouncing, with no debounce feature. The trigger yields to its own link's claim, and the link kind lists the event type:

```yaml
triggers:
  - source: github
    type: pull_request_review_comment
    link: github.pr
    yield_to: [owner]
    delay: 10m
links:
  - type: github.pr
    claim: owner
    events:
      - type: pull_request_review_comment
        wake: true
```

The first review comment creates a draft that holds `owner` on the PR. The next comments within ten minutes find the claim held, so the trigger yields, and they reach the draft through its link instead. The task starts at ten minutes with all of them. After that the link wakes the running task as usual, and the trigger still yields for as long as the task is unarchived.

The delay is **fixed**, counted from the first event. Later events don't push the start back. A sliding window would never start a task on a busy resource, and it would need delivery to update `wake_at`. (Open question 4.)

#### The agent changes, or the task is archived, during the delay

- **Agent edited.** The task copied its runner, workspace, variables, features and auto-archive at creation, and its start time is stamped in `wake_at`. Editing the agent, including the trigger's delay, doesn't move the draft. That is how every copied field already behaves. A link kind removed from the agent stops delivering to the draft's link, as it does for any task (`Plan` skips a link whose kind is gone).
- **Agent deleted.** `DeleteAgent` refuses while the agent has unarchived tasks ("archive them first"). A pending draft counts, so the user archives it, which is the explicit choice to abandon it.
- **Task archived.** Above: the start time is cleared and the claim is released. The waker's query skips archived tasks regardless.

#### Schedules: out of scope

A schedule already decides when its task is created, so a delay on it would just shift the cron expression. A schedule's task has no link, so it has no claim to hold and nothing to collect. Nothing calls for it, and adding it later is the same field on `model.Schedule`.

### Invariants

Following CLAUDE.md, the states that can't happen are asserted, not handled:

- `wake_at` set outside `RUNNING`/`NONE`, `SLEEPING` and `DRAFT`, or `SLEEPING` without `wake_at`: `store.UpdateTask` and `store.CreateTask` panic, naming the invariant. Every model transition keeps it, so the check guards the model rather than handling a case.
- `Task.Wake` with `TimerActor` on a task without `wake_at`: panics. The waker only fires tasks it claimed with a due `wake_at` and holds locked, so this is unreachable.
- `Task.Sleep` without `CanSleep`: panics. `SleepTask` checks `CanSleep` first and returns `FailedPrecondition`, the reachable case.

No backwards compatibility: `SLEEPING` and `wake_at` ship with the server, runner and driver together. Older clients have nothing to fall back to.

### Proto

```protobuf
enum TaskStatus {
  // ... existing values
  // The task's run asked to sleep and ended; it starts again at wake_at, or
  // earlier when a waking event arrives or a user starts it.
  SLEEPING = 9;
}

message Task {
  // ... existing fields
  // When the server starts the task: a sleeping task's wake-up, a delayed
  // trigger's draft's start, or the end of a sleep a running task has asked
  // for. Unset when nothing is pending.
  google.protobuf.Timestamp wake_at = 22;
}

enum LifecycleKind {
  // ... existing values
  LIFECYCLE_KIND_SLEEP_REQUESTED = 13;  // message: the reason
  LIFECYCLE_KIND_WOKEN = 14;            // message: when the task was due
}

service GritzService {
  // ... existing rpcs
  // Puts the task to sleep once its current run ends. Fails with
  // FailedPrecondition unless the task is running with nothing pending.
  rpc SleepTask(SleepTaskRequest) returns (SleepTaskResponse);
}

message SleepTaskRequest {
  int64 task_id = 1;
  oneof wake {
    // From 1m to 7d.
    google.protobuf.Duration duration = 2;
    // From 1m to 7d from now.
    google.protobuf.Timestamp until = 3;
  }
  // What the task is waiting for. Required.
  string reason = 4;
}

message SleepTaskResponse {
  Task task = 1;
}

message AgentTrigger {
  // ... existing fields
  google.protobuf.Duration delay = 8;
}
```

The lifecycle kind numbers follow the existing ones (the last is `SANDBOX_RESET`). Use whatever is next when the PR lands. `TaskActions` doesn't change: its flags are computed from the model.

### API

```go
package model // github.com/gritzapp/gritz/internal/model

// TaskStatusSleeping is a task whose run asked to sleep and ended. // new
const TaskStatusSleeping TaskStatus = TaskStatus(gritzv1.TaskStatus_SLEEPING)

// ActorKindTimer is the server starting a task at its wake_at. // new
const ActorKindTimer = "timer"

// TimerActor is the actor of events the waker writes. // new
var TimerActor = Actor{Kind: ActorKindTimer}

// LifecycleKindSleepRequested records a sleep the task's run asked for. // new
// LifecycleKindWoken records a sleep or a trigger delay ending in a start. // new

type Task struct {
	// ... existing fields
	// WakeAt is when the server starts the task; zero when nothing is pending. // new
	WakeAt time.Time
}

// CanSleep reports whether the task's run may ask to sleep. // new
func (t *Task) CanSleep() bool

// Sleep records that the task sleeps until until once its run exits. // new
func (t *Task) Sleep(until time.Time)

// Wake starts the task for a timer or a waking event, and reports whether it did. // new
func (t *Task) Wake(actor Actor) (woken *Event, ok bool)

// Start, Restart, Cancel and Archive clear WakeAt, and accept SLEEPING. // changed
```

`Wake` returns the `WOKEN` lifecycle event to write beside the command event, or nil when the task wasn't sleeping or delayed (a plain waking event on a completed task writes none, as today). It returns false only for a delayed draft woken by an event.

```go
package store // github.com/gritzapp/gritz/internal/store

// ClaimDueTasks locks and returns up to limit unarchived tasks whose wake_at has passed. // new
func (s *Store) ClaimDueTasks(ctx context.Context, tx *sql.Tx, limit int) ([]TaskRef, error)

// TaskRef names a task by id and org. // new
type TaskRef struct{ ID, OrgID int64 }
```

```go
package waker // github.com/gritzapp/gritz/internal/server/waker — new

// Store is the subset of *store.Store the waker uses.
type Store interface {
	WithTx(ctx context.Context, tx *sql.Tx, f func(tx *sql.Tx) error) error
	ClaimDueTasks(ctx context.Context, tx *sql.Tx, limit int) ([]store.TaskRef, error)
	GetTaskForUpdate(ctx context.Context, tx *sql.Tx, id, orgID int64) (*model.Task, error)
	UpdateTask(ctx context.Context, tx *sql.Tx, task *model.Task) error
	CreateEvent(ctx context.Context, tx *sql.Tx, event *model.Event) error
}

// Options configures the Waker.
type Options struct {
	Store     Store
	Publisher pubsub.Publisher
	Interval  time.Duration
	BatchSize int
	Log       *slog.Logger
}

// Waker periodically starts tasks whose wake_at has passed.
type Waker struct{ ... }

// New returns a Waker; Interval and BatchSize default to 10s and 100.
func New(opts Options) *Waker

// Run blocks until ctx is cancelled, calling Tick at the interval.
func (w *Waker) Run(ctx context.Context) error

// Tick starts one batch of due tasks in a single transaction.
func (w *Waker) Tick(ctx context.Context) error
```

```go
package agentmcp // github.com/gritzapp/gritz/internal/runner/agentmcp

// AddTools registers the sleep tool beside the existing four. // changed
func (s *Server) AddTools(server *mcp.Server)
```

**How it fits:** `gritz server` constructs a `waker.Waker` beside the scheduler and archiver and runs it. The `SleepTask` handler checks `CanSleep`, calls `Sleep` and writes `SLEEP_REQUESTED`. The runner's `stopped` fold moves the task to `SLEEPING`. The waker, the router's `attach` and `UpdateTask` with `start` all call `Wake` or `Start`, which clear `wake_at`. The router's `create` sets `Status: DRAFT` and `WakeAt` for a trigger with a `Delay`. The driver includes `WOKEN` events in its prompt events.

### Relation to the task state machine proposal

[Model tasks as a state machine of transition events](/proposals/draft/task-state-machine/) folds statuses and commands into states and transitions. This design maps onto it directly: `Sleeping` is a state with no live run and nothing for the runner, `sleep` and `wake` are transitions, and `wake_at` is part of the projection. Whichever of the two lands second makes that translation. Neither blocks the other.

## Implementation Plan

Layers 1–2 are shared. Part A (3–8) and part B (9–11) each build only on them, so either part can land first, or alone.

1. **Schema and model field.** Delivers: the `wake_at` migration and index, `Task.WakeAt` read and written by the store, the `Task.wake_at` proto field, and the invariant check in `UpdateTask`/`CreateTask`. Nothing sets it yet. Depends on: nothing. Verifiable by: the migration up and down, and store round-trip tests.
2. **Waker.** Delivers: `ClaimDueTasks`, `internal/server/waker`, the `timer` actor, the `WOKEN` lifecycle kind and its timeline summary, wired into `gritz server`. Depends on: (1). Verifiable by: a concurrent claim test like `TestClaimDueSchedules_Concurrent`, and waker tests with a mocked store, on a task whose `wake_at` the test sets directly.
3. **`SLEEPING` status and transitions.** Delivers: the status, `CanSleep`, `Sleep`, `Wake`, and the transition table above in `model.Task`, plus the `stopped` and `failed` folds. Depends on: (2). Verifiable by: table tests in `task_test.go` covering every row.
4. **`SleepTask` RPC.** Delivers: the RPC with bounds and precondition, and the `SLEEP_REQUESTED` event. Depends on: (3). Verifiable by: apiserver tests, then an e2e test in which a task sleeps, its run exits to `SLEEPING`, and the waker starts it.
5. **Early wake in the router.** Delivers: `attach` calls `Wake`, clearing the timer and writing `WOKEN` (actor `router`); `UpdateTask` with `start` on a sleeping task writes `WOKEN` (actor `user`). Depends on: (3). Verifiable by: eventrouter tests for waking and non-waking events on sleeping tasks.
6. **Wake prompt.** Delivers: `promptEvents` includes `WOKEN` events, and `renderEvent` renders both variants. Depends on: (5). Verifiable by: `agentprompt` golden tests.
7. **The `sleep` tool.** Delivers: the agentmcp tool calling `SleepTask`. Depends on: (4), (6). Verifiable by: xmcp tests, and in the dev stack, a task that sleeps 1m and wakes with the `WOKEN` prompt.
8. **Surfaces.** Delivers: the web UI badge, tooltip, filter and "Wake now", the CLI status and `Wakes:` line, and `wake_at` in the server MCP task output. Depends on: (3). Verifiable by: `pnpm lint`, and viewing a sleeping task in the dev stack.
9. **Trigger `delay` field.** Delivers: `AgentTrigger.Delay`, its proto field, bounds validation on save, and `put_agent`'s `delay`. Nothing reads it yet. Depends on: nothing beyond the existing agent code. Verifiable by: agent save and validation tests.
10. **Delayed creation.** Delivers: `create` makes a delayed draft with `wake_at`, `Wake` refuses a delayed draft for an event, and the waker starts it with the `Started after trigger delay` summary. Depends on: (2), (9). Verifiable by: eventrouter tests, including the debounce example: a second matching event yields and is delivered to the draft, and the first run's brief has both.
11. **Agent editor and draft display.** Delivers: the delay input on triggers, "after 10m" in summaries, and "starts in" on delayed drafts. Depends on: (9), (10). Verifiable by: `pnpm lint`, and creating a delayed task in the dev stack.

The user docs (`using/agent-tools`, `concepts/agents`, `reference/event-types`) are left to the docs task that reviews each push to `master`, per CLAUDE.md.

## Trade-offs

- **One `wake_at` column and one worker, not two mechanisms.** Part A and part B both mean "start this task at T". Separate tables or workers would duplicate the claim-and-fire loop and its exactly-once argument for no difference in behaviour. They differ only in the state the task waits in, which the model already distinguishes.
- **Reusing schedules for one-shot timers.** A schedule creates a new task, belongs to an agent, and is edited through the agent. A wake-up belongs to one task and starts it. Bending schedules to fit would mean task-owned schedules that start instead of create. The scheduler's structure is reused instead.
- **`COMPLETED` with a `wake_at` instead of a new status.** Fewer enum values, but the auto-archiver, every status filter and every badge would need an exception, and a waiting task would read as finished. The status is cheap, and it is the honest description.
- **Sleeping inside the sandbox** (a blocking tool or a shell `sleep`). This holds the run, the sandbox and, on Lambda, the billed MicroVM for the whole wait. It is lost on a runner restart, and events can't interrupt it cleanly.
- **Having the driver stop the run when `sleep` is called.** It would cut the agent off before it reads the result and before its session is saved. Ending the turn is the agent's job, and a run that overruns is already handled by the waker queuing a start.
- **Leaving the timer set after an early wake.** This would start the task again for nothing, possibly in the middle of the run the event started. Cancelling it costs the agent one more `sleep` call when it still needs to wait, and the wake prompt tells it so.
- **A debounce feature on triggers** (a window, a key). The draft's link and claim already do it with existing semantics. A separate feature would need its own state and its own idea of "the same resource", which claims already have.
- **A delayed task as `PENDING` with a start time, instead of a draft.** `PENDING` means a runner should act. A draft already means "exists, holds its links, doesn't run until started", is archivable, and has UI.

## Open Questions

1. **Sleep bounds.** Recommended: 1 minute to 7 days, out-of-range is an error. Longer, or a clamp instead of an error?
2. **Early wake cancels the timer.** Recommended: yes, as above. The alternative keeps the timer, so the task also wakes at the original time.
3. **Trigger delay bounds.** Recommended: 1 minute to 24 hours.
4. **Fixed or sliding delay.** Recommended: fixed from the first event. A sliding window, where each delivered event pushes `wake_at` back up to a cap, would be a second field (`max_delay`) and could land later.
5. **Waking events on a delayed draft.** Recommended: deferred to the deadline, which is what makes the debounce work. The alternative starts it early, so the delay only applies when no waking event follows. Should a link kind entry be able to opt into starting a delayed draft early?
6. **A failed run with a pending sleep.** Recommended: the failure clears the sleep. The alternative keeps it, so a flaky run retries itself at the wake time.
7. **A start time for `create_task` drafts.** Users and orchestrating agents could create a draft that starts at a given time with the same column. It is out of scope here, but it would be one optional field on `CreateTaskRequest` once part B lands.