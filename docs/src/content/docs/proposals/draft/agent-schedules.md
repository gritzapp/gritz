---
title: "Schedules as part of their agent"
description: "A schedule is an org-level resource that names an agent, with its own create, update and delete RPCs, MCP tools and web UI pages, so an agent's definition doesn't say when its tasks are created on a clock."
---

Issue: https://github.com/gritzapp/gritz/issues/2395

## Problem

[Agents](/proposals/implemented/agents/) gathered everything that creates and wakes an agent's tasks into the agent: its template, its triggers and its link kinds. Schedules were left out. A `model.Schedule` is an org-level row that names an agent by `agent_id`, with its own surface:

- Seven RPCs: `CreateSchedule`, `GetSchedule`, `ListSchedules`, `UpdateSchedule`, `DeleteSchedule`, `SetScheduleEnabled`, `RunSchedule`.
- Seven server MCP tools: `list_schedules`, `get_schedule`, `create_schedule`, `update_schedule`, `set_schedule_enabled`, `run_schedule`, `delete_schedule`.
- A Schedules page with its own create and edit forms.

So reading an agent (in the editor, through `get_agent`) doesn't show when it runs, `put_agent` can't define an agent that runs nightly, and `DeleteAgent` fails with `FailedPrecondition` while a schedule names the agent, which the user has to find and delete on another page first.

Already decided in #2395:

- An agent has its own list of schedules, beside its triggers and link kinds. A schedule is not a kind of trigger: triggers get no cron field, and trigger concepts (conditions, `public`, `yield_to`, `link`, first match wins) don't apply to schedules.
- Schedules are created, edited and removed as part of the agent definition (the agent editor, `UpdateAgent`, `put_agent`), and are deleted with the agent. The standalone create, update and delete surface goes away.

This reverses the [Schedules section](/proposals/implemented/agents/#schedules) of the Agents proposal.

## Design

### Overview

- `Agent` gains `schedules`: an ordered list of `AgentSchedule`, each a name, a cron expression, a timezone and an enabled flag.
- A schedule has no instructions of its own. Every task it creates starts with its agent's prompt.
- Schedules stay rows of the `schedules` table. The table keeps each schedule's run state (`next_run_at`, `last_run_at`, `last_task_id`), and the scheduler keeps claiming due rows from it as it does now.
- A schedule's identity is its row `id`. `GetAgent` returns it; `UpdateAgent` matches incoming schedules by it, so renaming, reordering and editing a schedule keep its row and its last run. A schedule without an `id` is new, and a stored schedule the request leaves out is deleted.
- Run state is output only: `CreateAgent` and `UpdateAgent` ignore it. Saving an agent only moves a schedule's `next_run_at` when that schedule's cron expression, timezone or enabled flag changed.
- `RunAgent` creates one task of an agent now. It knows nothing about schedules: there is no way to run a schedule now. `RunSchedule` and the `run_schedule` MCP tool go, and `run_agent` is added. Enable and disable are the `enabled` field of the definition; `SetScheduleEnabled` goes.
- The Schedules page goes. The agent editor gets a Schedules section, and each row of the Agents list shows the agent's next scheduled run and has a run button.
- `DeleteAgent` deletes the agent's schedules. Only unarchived tasks block it.
- Existing schedules already name their agent (`schedules.agent_id NOT NULL`). The migration keeps their rows, IDs and run state. It adds a position column and drops the instructions and the attribution columns.

### Agent definition

Shown as YAML for readability, as in the Agents proposal:

```yaml
name: dependency-bumper
workspace: default
runner: main
prompt: Bump outdated dependencies and open a pull request.
triggers: [...]
links: [...]

schedules:
  - id: 41                       # set by the server; leave it out to add a schedule
    name: Nightly bump
    cron_expr: "0 3 * * *"
    timezone: Europe/Paris
    enabled: true
    next_run_at: 2026-11-28T02:00:00Z   # output only
    last_run_at: 2026-11-27T02:00:00Z   # output only
    last_task_id: 2610                  # output only
  - id: 42
    name: Weekend bump
    cron_expr: "0 9 * * 6"
    timezone: UTC
    enabled: false
```

A schedule keeps only what says when its tasks are created:

- `name`: the name of every task it creates, as today. Optional, and not unique: identity is the `id`.
- `cron_expr` and `timezone`: the 5-field grammar and IANA zone `model.Schedule.Validate` accepts today. An empty timezone is `UTC`.
- `enabled`: whether it fires. A disabled schedule keeps `next_run_at = NULL`, as today.

It loses `instructions`, and the fields that only made sense for a free-standing resource: `agent_id` (implied by its place in the agent), `created_by`, `created_at` and `updated_at` (agents carry no attribution either).

### Scheduled tasks

A task a schedule creates starts with its agent's `prompt`. When the agent has none, it starts with a generated line, as `Router` writes one for a trigger of an agent without a prompt:

```
You were created by schedule "Nightly bump" of agent "dependency-bumper".
```

The agent's `prompt` is thereby the first instruction of every task the agent creates on its own, by trigger or by schedule, and the editor's hint for it says so. Work that needs different instructions on a different clock is a different agent.

The task is otherwise built as today: the agent's workspace, runner, auto-archive and variables (as saved; the runner resolves them), a created lifecycle event with actor `ScheduleActor`, the start command, and one wake-carrying `InstructionPayload`. `Schedule.Task` and `Schedule.Events` become `Agent.Task` and `Agent.Events`, shared with [running an agent](#running-an-agent), which builds the same task with a different name, actor and generated line.

### Identity and run state

The run state stays in the `schedules` row, next to the definition fields, rather than in the agent's JSON:

- The scheduler's claim (`ClaimDueSchedules`, `FOR UPDATE SKIP LOCKED` over the `idx_schedules_due` partial index) and its exactly-once advance in the same transaction stay as they are. Folding schedules into an `agents.schedules` JSONB column would need a side table for run state anyway, keyed by something that survives edits, and a claim that joins through it.
- The row `id` is that key already, and it is what `UpdateAgent` and `put_agent` match schedules by.

`UpdateAgent` reconciles the agent's rows against the request, in the transaction that writes the agent:

1. Lock the agent's stored schedules (`SELECT … FROM schedules WHERE agent_id = $1 FOR UPDATE`).
2. Reject an incoming `id` that isn't one of them with `InvalidArgument` (`schedule 42 is not a schedule of agent 7`). A schedule can't move between agents; it is removed from one and added to the other, and the new one starts with no last run.
3. For each incoming schedule, carry the run state from its stored row with `Schedule.Reschedule`:
   - `last_run_at` and `last_task_id` are always the stored values.
   - `next_run_at` is the stored value if `cron_expr`, `timezone` and `enabled` are unchanged. Otherwise it is `Next(now)` when enabled and `NULL` when disabled.
   - A new schedule has no last run, and `next_run_at` is `Next(now)` when enabled.
4. Delete the stored rows the request left out, update the matched ones, insert the new ones. `position` is the index in the request.

Keeping `next_run_at` when the spec didn't change matters: `UpdateSchedule` today recomputes it from now on every save, which drops an occurrence that is due but not yet claimed (up to one scheduler tick). With schedules saved on every agent edit, an unrelated change to a trigger would hit that window.

Renaming or reordering a schedule therefore changes nothing about when it fires; editing its cron expression, timezone or enabled flag realigns it to the grid from the save, as `UpdateSchedule` and `SetScheduleEnabled` do today.

`CreateAgent` ignores incoming schedule IDs, as it ignores `agent.id`: every schedule is new. Passing `get_agent` output without its `id` to `put_agent` copies an agent with its schedules.

### Locking

The scheduler locks a due schedule row, then share-locks its agent (`LockLiveAgent`) and creates the task. `UpdateAgent` and `DeleteAgent` take the same locks in the same order: the agent's schedule rows first (step 1 above), then the agent row. A save waits for an in-flight fire instead of deadlocking with it, and a tick that runs during a save skips the locked rows and fires them on the next tick, with `next_run_at` unchanged unless the save changed the spec.

`RunAgent` locks no schedule row (it reads none) and share-locks the agent, as `CreateTask` does.

### Running an agent

`RunAgent` creates one task of an agent now and returns it:

```proto
message RunAgentRequest {
  int64 id = 1;  // The agent.
}
```

- **Task.** Named after the agent, built from the agent as a scheduled task is (`Agent.Task`, `Agent.Events`), starting with the agent's prompt or, when it has none, with `You were created by running agent "dependency-bumper".`
- **Scope.** The task-create scope on the agent's workspace and runner, the check `CreateTask` and `RunSchedule` make today (`allowScheduleTarget`, renamed `allowAgentTarget`). Running an agent doesn't change it, so it doesn't need `OpOrgWrite`.
- **Actor.** The created lifecycle event names the calling user, as `CreateTask`'s does. `RunSchedule` attributed its task to `ScheduleActor`; only a fire by the scheduler does now, so the task timeline tells a scheduled task from one someone started.
- **No schedules.** `RunAgent` neither reads nor writes a schedule. There is no way to run a schedule now: with the schedule's instructions gone, a schedule's task differs from the agent's only in its name and generated line, and trying an agent out is running the agent. `RunSchedule` is removed without a replacement.

The handler builds the task itself, as `CreateTask` does, rather than through `scheduler.Fire`, which stays the scheduler's: it always fires a schedule, attributed to `ScheduleActor`.

`CreateTask` stays the way to start a task with instructions of the caller's own. `RunAgent` sends none.

### Enable and disable

`enabled` is a field of the definition, saved with the rest of the agent. `SetScheduleEnabled` is removed. Pausing a schedule becomes an agent edit, gated on `OpOrgWrite` like every agent edit, where `SetScheduleEnabled` needed `OpTaskWrite`. An agent's triggers already create tasks under that same scope, so a schedule needs no separate one.

### Deleting an agent

`DeleteAgent` locks the agent's schedules and the agent, fails with `FailedPrecondition` while an unarchived task belongs to the agent, and otherwise deletes the agent's schedule rows and soft-deletes the agent. `AgentInUse` drops its schedule clause, and the error and the editor's delete dialog only mention tasks.

Schedule rows are hard-deleted: nothing references them (`tasks` doesn't record which schedule created a task), and a soft-deleted agent's schedules would never fire again. `schedules_agent_id_fkey` stays a plain foreign key; agents are soft-deleted, so `ON DELETE CASCADE` would never apply.

Archiving an org still disables its schedules (`DisableSchedules`).

### Overview across agents

The top-level Schedules page is removed. `ListAgents` returns each agent's schedules, so the Agents list adds a **Next run** column: the earliest `next_run_at` among the agent's enabled schedules, with the count of schedules. That answers "what runs next, and when" without a second page or RPC, and each row links to the agent, where its schedules are edited.

### Proto

```proto
service GritzService {
  rpc RunAgent(RunAgentRequest) returns (RunAgentResponse);  // new
  // removed: CreateSchedule, GetSchedule, ListSchedules, UpdateSchedule,
  //          DeleteSchedule, SetScheduleEnabled, RunSchedule
}

message Agent {
  // ... fields 1-9 unchanged; 10 is taken by the workspace features proposal (#2384)
  // Create tasks of the agent on a cron schedule, in this order in the editor.
  repeated AgentSchedule schedules = 11;
}

// AgentSchedule creates a task of its agent each time its cron expression
// matches. The task starts with the agent's prompt.
message AgentSchedule {
  // Set by the server. UpdateAgent keeps the schedule with this id, and its
  // last run; 0 adds a schedule. CreateAgent ignores it.
  int64 id = 1;
  string name = 2;       // The name of every task it creates.
  string cron_expr = 3;
  string timezone = 4;   // IANA name; empty is "UTC".
  bool enabled = 5;
  // Output only; CreateAgent and UpdateAgent ignore them.
  google.protobuf.Timestamp next_run_at = 6;  // Unset when disabled.
  google.protobuf.Timestamp last_run_at = 7;  // Unset when it has never run.
  int64 last_task_id = 8;                     // 0 when it has never run.
}

// RunAgentRequest creates one task of an agent now, named after the agent
// and starting with its prompt.
message RunAgentRequest {
  int64 id = 1;
}

message RunAgentResponse {
  Task task = 1;  // The task the run created.
}

// removed: Schedule, CreateSchedule*, GetSchedule*, ListSchedules*,
//          UpdateSchedule*, DeleteSchedule*, SetScheduleEnabled*, RunSchedule*
```

### Notifications

Schedule changes are agent changes, published as `{updated, agent}`. The `schedule` notification resource goes, with its case in `use-org-sse.ts`. A fire also publishes `{updated, agent, <agent id>}` beside the task notification it already sends, so an open agent page and the Agents list show the new last run and next run. `RunAgent` publishes only the task notification: it changes no run state.

### Server MCP

- `get_agent`, `list_agents` and `put_agent` carry `schedules`. The `put_agent` description gains a sentence: schedules `[{id, name, cron_expr, timezone, enabled}]` each create a task of the agent, starting with its prompt, whenever the cron expression matches; keep a schedule's `id` to keep its last run, leave it out to add one, and leave a schedule out to delete it.
- `delete_agent`'s description says the agent's schedules are deleted with it.
- `run_agent` (`id`) creates a task of the agent now; `run_schedule` has no replacement.
- `list_schedules`, `get_schedule`, `create_schedule`, `update_schedule`, `set_schedule_enabled`, `run_schedule` and `delete_schedule` are removed.

### Web UI

- The agent form gets a **Schedules** section between Triggers and Link kinds, with a card per schedule: name, cron expression (with the grammar hint today's form shows), timezone (the browser's by default for a new one, from the same list), and an enabled switch. Schedules can be added, removed and reordered like triggers, and are saved with the agent.
- A saved schedule's card shows its next run and its last run with a link to the last task, read from the live `getAgent` query by `id` rather than from the form state, so a fire shows up without reseeding the form. It has no run button: a schedule can't be run now, only its agent.
- Each row of the Agents list has a run button (`RunAgent`), on every agent whether or not it has schedules. It takes the user to the created task's page.
- The Agents list adds the Next run column.
- Removed: the Schedules nav entry, `schedules.index.tsx`, `schedules.new.tsx`, `schedules.$id.edit.tsx`, `schedule-form.tsx`. `nextRunLabel`, `DEFAULT_CRON`, `browserTimezone` and `timezoneOptions` in `lib/schedule.ts` stay for the agent form.
- The agent form's hints ("Tasks created by hand or by a schedule get their own instructions" on the prompt, "only created by hand or by a schedule" under Triggers) and the delete-agent dialogs are reworded.

### Schema

New migration:

```sql
-- migrate:up

ALTER TABLE schedules ADD COLUMN position integer NOT NULL DEFAULT 0;

-- Each agent's schedules keep the order the Schedules page listed them in
-- (newest first).
UPDATE schedules s
SET position = o.position
FROM (
    SELECT id, row_number() OVER (PARTITION BY agent_id ORDER BY created_at DESC, id DESC) - 1 AS position
    FROM schedules
) o
WHERE o.id = s.id;

ALTER TABLE schedules DROP COLUMN instructions,
                      DROP COLUMN created_by,
                      DROP COLUMN created_at,
                      DROP COLUMN updated_at;

DROP INDEX idx_schedules_agent_id;
CREATE INDEX idx_schedules_agent_position ON schedules (agent_id, position);
```

Every existing schedule already belongs to a live agent (the cutover pointed them at their org's `default` agent, and `DeleteAgent` refuses an agent with schedules), so they all survive in place: same `id`, same `enabled`, same `next_run_at`, `last_run_at` and `last_task_id`. They fire on the next tick exactly when they would have, and appear on their agent's page.

Their instructions are dropped, not carried over: from the migration on, a schedule's tasks start with the agent's prompt. A schedule whose instructions differed from its agent's prompt needs its agent's prompt updated, or moving to an agent with the right one, by removing it there and adding it to the other. The same goes for a schedule still on a `default` agent.

`updated_at` is dropped: `AdvanceSchedule` and `DisableSchedules` stop writing it. Schedules live only in Postgres; the runner's SQLite state has none.

### API

```go
package model // github.com/gritzapp/gritz/internal/model

type Agent struct {
	// ...
	// Schedules create tasks of the agent on a cron schedule, in order.
	// Nil on agents loaded for routing. // new
	Schedules []*Schedule
}

// Task builds a pending task of the agent named name, as a schedule or a run creates it. // new: replaces Schedule.Task
func (a *Agent) Task(name string) *Task

// Events returns the events seeding task: created by actor, its start command, and the agent's prompt, or generated when the agent has none. // new: replaces Schedule.Events
func (a *Agent) Events(task *Task, actor Actor, generated string) []*Event

// Schedule creates a task of its agent each time its cron expression matches. // changed
type Schedule struct {
	ID       int64
	OrgID    int64
	AgentID  int64
	Name     string
	CronExpr string
	Timezone string
	Enabled  bool

	NextRunAt  *time.Time
	LastRunAt  *time.Time
	LastTaskID *int64
	// removed: CreatedBy, Instructions, CreatedAt, UpdatedAt
}

// Reschedule carries prev's run state onto s, which replaces prev in its agent, realigning NextRunAt from now when the spec or enabled flag changed; prev is nil for a new schedule. // new
func (s *Schedule) Reschedule(prev *Schedule, now time.Time) error

// Proto converts s to its protobuf representation. // changed: returns *gritzv1.AgentSchedule
func (s *Schedule) Proto() *gritzv1.AgentSchedule

// removed: Schedule.Task, Schedule.Events, ScheduleInstruction, ScheduleInstructionsFromProto
```

```go
package store // github.com/gritzapp/gritz/internal/store

// LockAgentSchedules locks the agent's schedules until tx ends and returns them by ID. // new
func (s *Store) LockAgentSchedules(ctx context.Context, tx *sql.Tx, orgID, agentID int64) (map[int64]*model.Schedule, error)

// CreateAgent inserts a and its schedules, and sets their IDs. // changed: writes schedules
func (s *Store) CreateAgent(ctx context.Context, tx *sql.Tx, a *model.Agent) error

// UpdateAgent replaces the stored agent with a: its schedules are updated by ID, inserted without one, and deleted when left out. // changed: writes schedules
func (s *Store) UpdateAgent(ctx context.Context, tx *sql.Tx, a *model.Agent) error

// GetAgent returns the org's agent by ID, with its schedules. // changed
func (s *Store) GetAgent(ctx context.Context, tx *sql.Tx, orgID, id int64) (*model.Agent, error)

// ListAgents returns the org's live agents with their schedules. // changed
func (s *Store) ListAgents(ctx context.Context, tx *sql.Tx, orgID int64) ([]*model.Agent, error)

// DeleteAgent deletes the agent's schedules and soft-deletes it; it returns ErrAgentInUse while an unarchived task belongs to it. // changed
func (s *Store) DeleteAgent(ctx context.Context, tx *sql.Tx, orgID, id int64) error

// removed: CreateSchedule, GetSchedule, ListSchedules, UpdateSchedule, DeleteSchedule, GetScheduleForUpdate
// unchanged: ClaimDueSchedules, AdvanceSchedule, DisableSchedules
```

```go
package scheduler // github.com/gritzapp/gritz/internal/server/scheduler

// Validate checks a schedule's cron expression and timezone. // changed: no agent lookup
func Validate(sched *model.Schedule) error

// Fire creates the task of sched, a schedule of agent, in tx, created by ScheduleActor. It writes nothing to sched. // unchanged
func Fire(ctx context.Context, tx *sql.Tx, st Store, sched *model.Schedule, agent *model.Agent) (*model.Task, model.Notification, error)

// removed: IsInvalidError
// unchanged: Agent, Scheduler
```

**How it fits:** `apiserver.validateAgent` calls `scheduler.Validate` on each schedule. `UpdateAgent` calls `LockAgentSchedules`, rejects foreign IDs, calls `Reschedule` per schedule, then `store.UpdateAgent`. `CreateAgent` calls `Reschedule(nil, now)` per schedule, then `store.CreateAgent`. `DeleteAgent` locks the schedules before the agent. The scheduler's `fire` calls `Fire` and adds the agent to its notification. `RunAgent` share-locks and loads the agent, checks `allowAgentTarget`, and creates `agent.Task(agent.Name)` with `agent.Events` and the caller's `UserActor`.

## Implementation Plan

1. **Schedule columns** — Delivers: the migration; `model.Schedule` without `CreatedBy`, `Instructions`, `CreatedAt`, `UpdatedAt`; scheduled tasks starting with the agent's prompt or the generated line (`Agent.Task`, `Agent.Events`); the `Schedule` proto's `instructions`, `created_by`, `created_at` and `updated_at` removed, with the instructions field of the schedule form and the `instruction` argument of `create_schedule` and `update_schedule`. New schedules go last in their agent. Depends on: nothing. Verifiable by: the migration against a copy of production schedules (positions as expected, run state untouched); scheduler tests for an agent with and without a prompt.
2. **Schedules in the agent, and `RunAgent`** — Delivers: `AgentSchedule` and `Agent.schedules`; `LockAgentSchedules`, schedule reconciliation in `CreateAgent`/`UpdateAgent`, schedules loaded by `GetAgent`/`ListAgents`; `Reschedule`; `DeleteAgent` deleting schedules and no longer blocked by them; the agent notification on fire; `RunAgent` and the `run_agent` MCP tool; the `put_agent` and `delete_agent` descriptions. The standalone RPCs still work on the same rows. Depends on: (1). Verifiable by: `Reschedule` table tests (rename, reorder, cron edit, disable, enable, new); store tests for reconciliation and foreign IDs; handler tests that a save keeps a due `next_run_at`, and of `RunAgent` (name, actor, generated line, scope); an e2e test that edits an agent between two fires.
3. **Web UI** — Delivers: the Schedules section of the agent form with run state (no run button), the run button, which takes the user to the created task, and the Next run column on the Agents list, the reworded hints; the Schedules page, its routes, its form and its nav entry removed. Depends on: (2). Verifiable by: adding, editing, reordering and disabling a schedule on the dev stack, running an agent from the list and landing on its task, and watching a fire update the page; `pnpm lint`.
4. **Remove the standalone surface** — Delivers: `CreateSchedule`, `GetSchedule`, `ListSchedules`, `UpdateSchedule`, `DeleteSchedule`, `SetScheduleEnabled`, `RunSchedule` and the `Schedule` message removed, with their handlers, store methods, MCP tools, e2e harness helpers and the `schedule` notification resource. Depends on: (3). `RunSchedule` goes without a replacement. Verifiable by: the build, and the e2e schedule tests rewritten against `UpdateAgent`, with fires driven through the scheduler.

Between (2) and (4) a schedule can be edited both ways; both write the same row, and (4) follows as soon as the UI has moved. The user docs (`concepts/schedules`, `concepts/agents`, `using/web-ui`, `integrations/mcp`) are updated by the docs task that follows each push to `master`.

## Trade-offs

- **Rows, not JSON.** Storing schedules in `agents.schedules` JSONB would make the agent one document, but the scheduler needs a claimable, lockable row per schedule with its run state. Keeping the rows and reconciling them on save keeps the scheduler untouched and gives every schedule a durable `id` for run now and for links to its last task.
- **Identity by `id`, not by name or position.** Matching by name would make a rename reset the last run and forbid two schedules with the same task name; matching by position would make a reorder swap clocks. An `id` round-trips through `get_agent`/`put_agent` and the editor at no cost, and a client that drops it only loses the last run, never fires twice.
- **Keep `next_run_at` unless the spec changed.** Recomputing on every save, as `UpdateSchedule` does, would let an unrelated agent edit skip a due occurrence.
- **Schedules have no prompt.** The agent's prompt is the one place an agent's own work is described, for triggers and schedules alike, and a schedule is only a clock. Two jobs with different instructions are two agents, which can share a workspace and runner. The cost is a one-time edit of the agents whose schedules carried their own instructions, since the migration drops them.
- **Enable and disable through `UpdateAgent`.** A separate toggle RPC would be a second way to write one field of the definition, with its own scope. The switch sits in the editor, saved with the rest.
- **Run the agent, not a schedule.** Without schedule instructions, a schedule's task differs from its agent's only in its name and generated line, so running a schedule now adds a second way to try the same thing out. `RunAgent` takes only the agent and knows nothing about schedules, and the editor's schedule cards have no Run now button. Building the task client-side through `CreateTask` would duplicate the name and generated line the server applies, and `CreateTask` requires the caller to pass its instructions.
- **Manual runs are the caller's.** `RunSchedule` attributed its task to `ScheduleActor`. Naming the user who pressed run keeps `ScheduleActor` meaning "fired by the clock".
- **No Schedules page.** A read-only overview would keep `ListSchedules` alive for one table. A Next run column on the Agents list covers the overview, and every schedule is one click from its agent.
- **Hard-delete schedules with the agent.** Blocking `DeleteAgent` on schedules made sense when they were separate resources the user might want to repoint. Now they are part of the agent; deleting it means deleting them.
- **Schedule editing needs `OpOrgWrite`.** Creating a schedule used to need task-create scope on the agent's target, and toggling one task-write. A schedule is now agent configuration, and agent triggers already create tasks under `OpOrgWrite`. `RunAgent` keeps the task-create check because it creates a task on the caller's behalf.

## Open Questions

None.
