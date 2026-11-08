# Restructure tasks as a unified event stream

Issue: https://github.com/icholy/gritz/issues/947

## Problem

A task's history is scattered across separate concepts with separate code
paths, storage, and wakeup behavior, and every consumer has to stitch them back
together:

- **Instructions** live as a JSON array in `tasks.instructions`
  (`internal/store/sql/schema.sql`), and "add instruction" mutates the column
  and sets `command = start` to trigger a restart
  (`apiserver/task.go` `UpdateTask`).
- **Logs** are their own table (`logs`: `id, task_id, type, content,
  created_at`) written through `UploadLogs` / `store.CreateLog`. The `type`
  column already overloads five distinct meanings: `llm` (the agent's `report`
  tool, `agentmcp/xmcp.go:133`), `mcp` (short breadcrumbs the `gritz` MCP tools
  write — "created link: …", "updated child task: …" — `agentmcp/xmcp.go:43`),
  `audit` (task mutations, `apiserver/task.go`), `info` (container lifecycle,
  `apiserver/runner.go:105`), and `error`. All of it is low-volume; the agent's
  tool calls and message transcript are not uploaded anywhere today.
- **External events** are an org-scoped `events` table fanned out to tasks via
  the `event_tasks` join, routed by `eventrouter.Route` matching an event's
  `model.RoutingKey(url)` against subscribed `task_links`.
- **Lifecycle** changes arrive as `RunnerEvent`s (`started`/`stopped`/`failed`)
  through `SubmitRunnerEvents`, folded into `tasks.status` by
  `Task.ApplyRunnerEvent` (`internal/model/task.go`), and *separately* written
  as `audit`/`info` log rows.

The system is already half event-sourced. `proposals/implemented/driver-owned-events.md`
made the driver the source of truth for lifecycle events submitted to an API,
and `ApplyRunnerEvent` is literally a fold over those events into a status. Two
draft proposals are circling the same center of gravity from different sides:

- `proposals/draft/task-change-unifying-logs-and-notifications.md` proposes a
  `model.TaskChange` value type so a single structured fact projects to both an
  audit-log row and a channel notification.
- `proposals/draft/richer-tool-call-logs.md` enriches the `mcp` log channel.

This proposal unifies the four channels into one ordered, typed, per-task event
stream, and folds the `TaskChange` work in as the `lifecycle` event type.

## Design

### The stream: `events`

A single append-only table is the source of truth for everything that happens
to a task. Order is the stream; the `id` (a `BIGSERIAL`) is the ordering key.
It reuses the `events` name freed by dropping the old org-level `events` table
and its `event_tasks` join (see Rollout).

```sql
CREATE TABLE public.events (
    id         bigint  NOT NULL,                       -- BIGSERIAL; stream order
    task_id    bigint  NOT NULL,
    org_id     bigint  NOT NULL,
    type       text    NOT NULL,                       -- materialized from the Event.payload oneof arm; for filtering
    wake       boolean NOT NULL DEFAULT false,         -- does appending this wake an idle task?
    payload    jsonb   NOT NULL,                        -- protojson of the set oneof arm (see Payloads)
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);
CREATE INDEX idx_events_task_id_id ON public.events (task_id, id);
CREATE INDEX idx_events_org_id     ON public.events (org_id);   -- powers the org event feed
```

The whole model reduces to two verbs:

- **append** an event (instruction added, report written, external event
  arrived, lifecycle transition, link created),
- **read** a task's events in order.

Reading a task's stream is one query — `WHERE task_id = $1 ORDER BY id`.
Clients fetch the whole stream each time, exactly as they fetch all
instructions, logs, and events today. Incremental delivery — reading only
what's new since a cursor — is a separate concern (#946) and out of scope here.

#### Event types, `wake`, and the derived direction

The issue's classifying axes — **type** and **wake** — become explicit columns
rather than implied by which table a row lives in. **Direction** (`to_agent` /
`from_agent` / `about_task`) is a convenient way to talk about the types, but it
is strictly a function of `type` (the mapping below), so it is *derived*, not
stored.

| `type`        | direction *(derived)* | `wake` (default) | Replaces today                                    |
| ------------- | --------------------- | ---------------- | ------------------------------------------------- |
| `instruction` | `to_agent`            | `true`           | `tasks.instructions` JSON + `command=start`       |
| `external`    | `to_agent`            | per routing rule | org-level `events` + `event_tasks` fan-out        |
| `report`      | `from_agent`          | `false`          | `logs` rows with `type='llm'` (the `report` tool) |
| `lifecycle`   | `about_task`          | `false`          | `RunnerEvent` fold + `audit`/`info` log rows      |
| `link`        | `about_task`          | `false`          | `task_links` row creation                         |

Because direction is a pure function of `type`, it is not a column — storing it
would only invite drift for no gain. Queries that want a direction filter spell
out the types instead: the brief is `type IN ('instruction','external')`, and
the UI timeline groups by the same mapping.
`wake`, by contrast, is *not* purely a function of type: instructions always wake, reports
never do, but an `external` event wakes only when its routing rule has
`wakeup = true` (`eventrouter.attach`, today's `rule.Wakeup`). So `wake` is set
per-event at append time. This is the unification the issue asks for: **"add
instruction → restart" and "event arrived → notify" become the same
mechanism** — append an event whose `wake` is true, which sets `command=start`
exactly as `Task.Start()` does today (`internal/model/task.go:416`).

#### Payloads

Each type has a typed payload message — the arms of the `Event.payload` oneof
(see API / proto changes) — stored as their protojson encoding in the `payload
jsonb` column. The shapes:

```jsonc
// instruction  — was model.Instruction + actor
{ "text": "rebase onto main", "url": "https://github.com/.../pull/481", "actor": {"kind":"user","name":"icholy"} }

// external     — self-contained; the org event feed reads these by org_id
{ "description": "PR comment from alice on #481", "url": "https://github.com/.../481#issuecomment-…", "data": "{…webhook…}" }

// report       — the agent's report tool (was logs type='llm')
{ "content": "Opened PR #952 with the migration." }

// lifecycle    — the model.TaskChange fact (see below); kind is the LifecycleKind enum
{ "kind": "LIFECYCLE_KIND_SANDBOX_EXITED", "actor": {"kind":"runner"}, "from_status": "RUNNING", "to_status": "COMPLETED", "runner_event": "stopped" }

// link         — was task_links row
{ "link_id": 1027, "relevance": "trigger", "url": "https://github.com/.../issues/947", "title": "icholy commented…", "subscribe": true }
```

### `lifecycle` events absorb the `TaskChange` proposal

`proposals/draft/task-change-unifying-logs-and-notifications.md` already
designed the closed set of "things that happened to a task": `Created`,
`Updated`, `Cancelled`, `Restarted`, `Archived`, `Unarchived`, `AutoArchived`,
`Woken`, `SandboxStarted`, `SandboxExited`, `SandboxFailed`. Minus
`Woken` (below), those are the `LifecycleKind` enum — the `lifecycle` event's
`payload.kind`. The two proposals converge:

- `TaskChange`'s "single structured fact" *is* a `lifecycle` event.
- Its `Log()` projection becomes the timeline rendering of the event (no more
  canned `container exited successfully`; the `to_status` in the payload says
  whether the container completed, re-queued, or was cancelled).
- Its `Notification()` / `ChannelMessage` projection is unchanged — the
  notification is published from the appended event after commit.
- `Woken` is no longer a `lifecycle` event at all: it splits cleanly into the
  `external` event (the trigger) plus whatever `lifecycle` transition the wake
  caused. The "which webhook woke me and what did it say" information the
  `TaskChange` proposal was recovering is now first-class in the `external`
  event's payload, not reconstructed into a log string.

`Task.ApplyRunnerEvent` keeps its exact state-machine logic. The only change:
`SubmitRunnerEvents` appends a `lifecycle` event **and** folds it into the
materialized `status` column in the same transaction, instead of writing an
`info`/`audit` log row beside the status mutation.

### Status stays a projection

The status fold is the canonical example of event-sourcing already living in
the codebase, and it stays. `tasks.status`, `tasks.command`, and
`tasks.version` remain materialized columns — `ListTasks` must not replay
streams to render a list, and the runner poll
(`ListTasksForRunner`: `WHERE command != 0`) must stay an indexed column scan.

The rule: **`lifecycle` events are the source of truth; `status` is a
projection updated in the same transaction by the existing fold.** The
status guard (`ApplyRunnerEvent`'s version check and per-status transition
rules, plus `Start`/`Restart`/`Cancel`) carries over unchanged — it is the fold
function. `command`/`version` remain the runner-coordination channel and are
*not* stream events (they are control state, not history).

This proposal keeps the existing shape: the status mutation and the `lifecycle`
event append sit side by side. Consolidating them into a single
`applyLifecycleEvent(task, ev)` fold — so the projection can never drift from
the stream — is a worthwhile but larger `internal/model/task.go` refactor,
deferred as out of scope to #952 (revisit once the stream is in place).

### Links: event is truth, `task_links` is the index

`link` events are the source of truth for the timeline. But the subscription
matcher needs to answer "which tasks subscribe to `routing_key K`?" with an
index, not a stream scan. So `task_links` survives as a **projection**: when a
`link` event with `subscribe=true` is appended, the same transaction upserts a
row into `task_links` (`task_id, routing_key, subscribe`), keeping the existing
`idx_task_links_routing_key`. `eventrouter` continues to call
`FindSubscribedLinksForOrgs` against that projection unchanged. The UI reads
links from the projection (cheap); the timeline reads them from the stream.

### The agent's brief

The agent's brief — what a run is handed — is just the task's to-agent events,
the `instruction` and `external` types:

```sql
SELECT * FROM events
WHERE task_id = $1 AND type IN ('instruction', 'external')
ORDER BY id;
```

`report`, `lifecycle`, and `link` events are not to-agent, so they never
enter the brief — they are the task's own output and history, surfaced in the
UI timeline (#918) but not pushed back at the agent.

Clients fetch the brief in full on each run, exactly as they fetch all
instructions and events today. Incremental delivery — handing the agent only
the events new since its last run, so it doesn't re-process the whole stream —
is a separate concern (#946) and out of scope here.

### The agent contract

The contract reduces to "consume your events, append events as you work," and
the MCP tools (`internal/agentmcp/xmcp.go`) become thin verbs over
the stream:

| Tool                     | Today                                   | Under the stream                                          |
| ------------------------ | --------------------------------------- | --------------------------------------------------------- |
| `get_my_task`            | `GetTaskDetails` (4 joins)              | `ListEvents` (this task, brief)                           |
| `report`                 | `UploadLogs` type=`llm`                 | appends a `report` event                                  |
| `create_link`            | `CreateLink`                            | appends a `link` event — handler upserts `task_links`     |

The child-task tools (`create_child_task`, `update_child_task`,
`list_child_task_logs`) go away with the child-tasks feature itself (#940).
Those rows are the *stream operations* each remaining tool performs. The agent
reaches the stream through its own identity-scoped surface (#939), not the
general verbs; `CreateTask`/`UpdateTask`/`ListEvents` (defined under API / proto
changes) are the **general** (`GritzService`) surface. Who may append what —
and why the agent can't forge events — is the AuthZ section.

### AuthZ

Two surfaces, two authorization models.

**General surface (`GritzService`).** `CreateTask` / `UpdateTask` /
`ListEvents` serve admin, user, and API-key callers, authorized by the existing
op-level `authscope` engine (`task.create` / `task.write` / `task.read`).
Events reach the stream by riding `UpdateTask`'s `repeated Event` (or
`CreateTask`'s seed), and the handler enforces a type-allowlist: only
user-appendable arms (`instruction`) may be appended this way. `lifecycle` and
`external` are not user-appendable: the runner appends `lifecycle` via
`SubmitRunnerEvents` and `eventrouter` appends `external`, both server-internal.

**Agent surface.** Agent authorization is #915's problem, designed in #939
(`agent-rpc-surface`); this proposal defers to it. The agent does not call the
general verbs. It calls a dedicated, identity-scoped `AgentService` where "my
task" is resolved from the token (no `task_id` parameter) and each method fixes
its event type — `Report` appends a `report`, `CreateMyLink` a `link`, the
driver's `ReportMyTaskEvent` a `lifecycle`. So the agent never holds the general
write surface (`UpdateTask`) at all, and on its own surface the "can't forge
`lifecycle`/`external`" guarantee is *structural* — those types are simply
absent from it. The general surface reaches the same guarantee differently:
`UpdateTask`'s type-allowlist (instruction-only) rejects the rest. Two surfaces,
two enforcement styles, same invariant.

Removing child tasks (#940) collapses this further: with no children there is no
own-or-child matching anywhere. The agent only ever touches its own task, and
its entire write surface is `report` and `link` — it never appends
`instruction`, `wake`, `lifecycle`, or `external`.

### No separate verbose channel

The issue raises a volume worry — high-volume raw output swamping the stream. In
practice there is no such channel today: the `logs` table, despite its name,
holds only low-volume *semantic* rows, and the agent's tool calls and message
transcript are **not** uploaded anywhere. Everything `logs` actually holds has
an event-type home:

- `llm`   → `report` event (the `report` tool's messages)
- `audit` → `lifecycle` event (task mutations)
- `info`  → `lifecycle` event (container lifecycle)
- `error` → `lifecycle` payload field (container failures)
- `mcp`   → *nothing new* — these were short breadcrumbs the `gritz` MCP tools
  wrote ("created link: …", "updated child task: …"), echoes of actions the
  stream now records as first-class `link` / `instruction` events (and the
  child-task ones retire with #940).

So the `logs` table is **dropped outright** — no separate channel, no `volume`
discriminator, no partial indexes, no two-table union for the #918 timeline
(which now reads the one stream). If raw tool-call or transcript streaming is
added later (the domain of `proposals/draft/richer-tool-call-logs.md`) and is
genuinely high-volume, that is future work: it gets its own event type (or a
dedicated store) and its own indexing decision then. This proposal does not
pre-build for output that isn't captured today.

### API / proto changes

`proto/gritz/v1/gritz.proto` redefines `Event` as the stream row — its payload
is a typed `oneof` over the five event shapes, not an opaque `Struct` — and
reduces the **general** read/write surface to three RPCs: `CreateTask`
(create the task + seed its stream), `UpdateTask` (update task fields + append a
`repeated Event` to the stream), and `ListEvents` (read, scoped by `task_id` or
`org_id`). There is no dedicated append verb: on the general surface
`instruction` is the only user-appendable event type, so a generic `AppendEvent`
would have a single real caller and isn't worth minting — appending folds into
`UpdateTask`, which pairs symmetrically with `CreateTask` seeding the stream. The
old per-channel and org-event RPCs — `GetEvent`, `CreateEvent`,
`ListEventsByTask`, `AddEventTask`/`RemoveEventTask`, `UploadLogs`, `ListLogs`,
`CreateLink` — fold into these or go away, and `UpdateTask`'s old narrow
add-instruction-and-restart path generalizes into its new `repeated Event`
append (retiring the standalone `CreateEvent`). The *agent* does not call these
directly; it uses the identity-scoped `AgentService` (#939), whose methods
append/read events on the token's own task.

```proto
message Event {
  int64 id = 1;
  int64 task_id = 2;
  bool wake = 3;
  google.protobuf.Timestamp created_at = 4;
  // The set arm IS the event type — a typed union, not an opaque Struct. There
  // is no separate `type` field on the wire; the db `type` column is
  // materialized from the arm for indexed filtering (like `direction`).
  oneof payload {
    InstructionPayload instruction = 5;
    ExternalPayload    external    = 6;
    ReportPayload      report      = 7;
    LifecyclePayload   lifecycle   = 8;
    LinkPayload        link        = 9;
  }
}

message InstructionPayload { string text = 1; string url = 2; Actor actor = 3; }
message ExternalPayload    { string description = 1; string url = 2; string data = 3; }
message ReportPayload      { string content = 1; }
message LifecyclePayload   { LifecycleKind kind = 1; Actor actor = 2; string from_status = 3; string to_status = 4; string runner_event = 5; }
message LinkPayload        { int64 link_id = 1; string relevance = 2; string url = 3; string title = 4; bool subscribe = 5; }
message Actor              { string kind = 1; string name = 2; }  // kind: user|runner|router|agent

// The closed set of lifecycle transitions (the TaskChange set, minus Woken —
// which splits into an external event plus the transition the wake caused).
enum LifecycleKind {
  LIFECYCLE_KIND_UNSPECIFIED       = 0;
  LIFECYCLE_KIND_CREATED           = 1;
  LIFECYCLE_KIND_UPDATED           = 2;
  LIFECYCLE_KIND_CANCELLED         = 3;
  LIFECYCLE_KIND_RESTARTED         = 4;
  LIFECYCLE_KIND_ARCHIVED          = 5;
  LIFECYCLE_KIND_UNARCHIVED        = 6;
  LIFECYCLE_KIND_AUTO_ARCHIVED     = 7;
  LIFECYCLE_KIND_SANDBOX_STARTED   = 8;
  LIFECYCLE_KIND_SANDBOX_EXITED    = 9;
  LIFECYCLE_KIND_SANDBOX_FAILED    = 10;
}

// Create a task and seed its stream. The task entity (workspace, runner, …)
// can't be an append — a stream needs a task to belong to — but the seed is a
// list of events, typically one InstructionPayload, so creation is event-shaped.
rpc CreateTask(CreateTaskRequest) returns (CreateTaskResponse);  // task fields + repeated Event events

// Update task fields AND append events to an existing stream. A user adding an
// instruction = UpdateTask{events:[{instruction:{…}, wake:true}]} (wake sets
// command=start, subsuming the old add-instruction-and-restart path). The
// handler enforces a type-allowlist: only the instruction arm may be appended
// here; lifecycle and external are server-internal and rejected.
rpc UpdateTask(UpdateTaskRequest) returns (UpdateTaskResponse);  // task fields + repeated Event events

// Read a stream. Scoped by task_id (a task's timeline / the brief, the latter
// filtered to the instruction/external arms) or by org_id (the org event feed,
// external arm only). Filters run on the materialized `type` column.
rpc ListEvents(ListEventsRequest) returns (ListEventsResponse);
```

`CreateTask` stays a distinct RPC because a stream needs a task to belong to —
but instead of a magic "first instruction" field it takes a list of initial
events to seed the stream (typically one `instruction`). On the write side, "add
instruction + restart" becomes `UpdateTask{events:[{instruction:…, wake:true}]}`
(the handler sets `command=start`); the same RPC also carries any task-field
updates, so mutating the task and growing its stream travel together. The
handler's type-allowlist keeps `lifecycle` and `external` out — those are never
user-appendable, appended internally by `SubmitRunnerEvents` (unchanged on the
wire) and `eventrouter`. `create_link` is not on this surface at all; it is an
agent action (a `link` event) on the identity-scoped `AgentService` (#939). The
general surface is authorized by the existing op-level scopes (the user-facing
`authscope` engine); the *agent's* access — and the forge-proof "agent can't
append `lifecycle`/`external`" guarantee — comes from that dedicated
`AgentService` (see The agent contract), structural rather than a type-check.

### Rollout

Existing tasks are **not** migrated — there is no backfill, so old tasks carry
no stream history, and any tasks in flight at cutover are drained or re-created
rather than translated. Reusing the freed `events` name means the old and new
`events` tables can't coexist, so the schema change and the code cutover ship
together rather than as separate additive steps.

1. **Schema.** One migration (next sequence after
   `20260607000002_task_auto_archive.sql`) drops the old org-level `events`,
   `event_tasks`, and `tasks.instructions`; creates the new `events` stream
   table under the reused name; and drops the `logs` table (every log row has an
   event-type home — see No separate verbose channel). Keep `task_links` (the
   subscription index). Regenerate `sqlc`.
2. **Cutover.** In the same deploy, re-point every mutation site that today
   writes instructions/logs/events/links/status to append the corresponding
   `events` row in the same transaction (the `TaskChange` value type from the
   sibling proposal is the natural constructor for `lifecycle` rows), and point
   the brief, `get_my_task`, the org event feed (`ListEvents`, now org-scoped
   over the stream), and the #918 timeline at `ListEvents`. The initial
   instruction becomes the first `instruction` event appended by `CreateTask`;
   there is no denormalized `tasks.instructions` replacement — reads that want a
   task's instructions filter the stream by `type='instruction'` (indexed on
   `task_id`).

## Trade-offs

- **Global `BIGSERIAL` id vs. per-task sequence.** A global id keeps the schema
  trivial and ordering total; filtering by `task_id` preserves order, so the id
  is also monotonic within a task and no per-task sequence is needed. The
  concurrent-insert visibility wrinkle (a higher id committing visible before a
  lower one) only matters for incremental cursor reads, which this proposal
  doesn't do — clients fetch the whole stream each time. Deferred to #946.

- **Materialized projections (`status`, `task_links`) vs. pure replay.**
  Pure event-sourcing would derive status and subscriptions by folding the
  stream on every read. Rejected: `ListTasks` and the runner poll are hot,
  indexed, list-shaped queries that must not replay per-row. Materializing the
  two projections that have hot read paths — and *only* those — keeps the
  source-of-truth/projection split honest without paying replay cost where it
  hurts.

- **Keeping `command`/`version` off the stream.** They are control state for
  runner coordination, not task history, and they mutate in place (a restart
  bumps `version`; the stream would otherwise accumulate churn). Modeling them
  as events would conflate "what the runner should do next" with "what
  happened." Left as columns.

- **Per-task `external` rows vs. a shared `events` row + join.** Fan-out now
  writes one `external` row per subscribed task instead of one org-level
  `events` row joined to N tasks. In the common case an external event maps to a
  single task (1:1), so the fan-out is one row; the multiplied write only
  appears for mass-subscribed resources, which are rare. In return the join and
  the separate org-level table disappear, and the org feed becomes a plain
  `org_id`-scoped scan over `idx_events_org_id`. The same occurrence then shows
  once per subscribed task in the feed — accepted duplication. Accepted.

## Open Questions

- **Merge order with #939.** The agent-surface design composes with this
  proposal (see AuthZ), but #939 (`agent-rpc-surface`) was written against
  today's storage, so its method bodies — `Report` (a `type=llm` log upload),
  `CreateMyLink` (a `task_links` row), `ReportMyTaskEvent` (lifecycle via the old
  `SubmitRunnerEvents` fold) — become event appends if both land (`report`,
  `link`, `lifecycle` events respectively). Those appends stay on the
  `AgentService`; they never route through the general `UpdateTask`, so dropping
  the generic `AppendEvent` doesn't touch them. Whichever merges second rebases
  onto the other.