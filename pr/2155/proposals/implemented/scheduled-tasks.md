# Recurring Scheduled Tasks

Issue: https://github.com/icholy/gritz/issues/1456

## Problem

Every gritz task is created imperatively — a human in the Web UI, an external poller
(GitHub/Jira), or an agent calls `CreateTask` and the runner picks the row up on its next
poll (`internal/runner/runner.go`). There is no way to say "run this task every weekday at
09:00" or "kick off a dependency-update run every night."

Recurring work is common: nightly dependency bumps and changelog grooming, daily "summarize
what merged yesterday" reports, weekly triage digests, polling-style automations where a
webhook isn't available, and re-running a triage task every few hours until something is
resolved. Users emulate this today with external cron (host crontab, GitHub Actions
`schedule:`, Fly cron machines) shelling out to `gritz task create`. That works but lives
outside the product: schedules aren't visible in the UI, aren't org/tenant scoped, aren't
covered by the auth/scope model, and every integration reinvents "what time is it, did I
already run this."

We want first-class, user-defined **recurring scheduled tasks**: a user specifies a schedule
(cron expression + timezone) and a task template (workspace, runner, instructions), and
gritz materializes a real task run on each occurrence — correctly and exactly-once across
multiple C2 server instances.

## Design

### Overview

A schedule is a stored template plus a cron spec. A server-side `scheduler` worker — modeled
directly on the existing `archiver` (`internal/server/archiver/archiver.go`) — ticks
periodically, claims the schedules whose `next_run_at` has passed, creates a normal task for
each through the exact same path as `CreateTask`, and advances `next_run_at` to the next
occurrence. The runner then picks up those tasks with no changes at all.

This deliberately reuses two patterns already in the codebase:

- **The archiver worker shape.** A single `Run(ctx)` goroutine ticking on a `time.Ticker`,
  each tick pulling a bounded batch of due rows and acting on each inside a transaction that
  re-reads under a row lock. See `Archiver.Run` / `Archiver.Tick` / `Archiver.archive`.
- **The `CreateTask` flow.** A schedule firing is *nothing more than* a `CreateTask`: insert a
  `tasks` row with `Status = Pending`, `Command = Start`, `Version = 1`, then seed the event
  stream with a `LifecycleKindCreated` event and one `InstructionPayload` event per
  instruction (`internal/server/apiserver/task.go:69-141`). The scheduler calls the same store
  methods, so scheduled tasks are indistinguishable from hand-created ones downstream.

The key structural property is that **scheduling cadence is fully decoupled from execution**.
The scheduler's only job is "it's time — create the task and compute the next time." It never
waits on a task to finish, never talks to a runner, and never touches container lifecycle. A
schedule occurrence is one task-row creation, full stop.

### Schedule specification

**Format.** Standard 5-field cron (`minute hour day-of-month month day-of-week`), plus the
common macros (`@daily`, `@hourly`, `@weekly`, `@monthly`, `@yearly`). We intentionally do *not*
support a seconds field: the scheduler tick granularity (§ scheduler) and the whole use-case
are minute-resolution, and a 5-field spec is what users expect from crontab and GitHub Actions.

**Parsing / validation.** Add `github.com/robfig/cron/v3` (no cron dependency exists in
`go.mod` today). Parse with a fixed parser so the accepted grammar is explicit and stable:

```go
var cronParser = cron.NewParser(
    cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)
```

`cronParser.Parse(expr)` is called at create/update time; a parse error is returned to the
caller as `connect.CodeInvalidArgument` so an invalid expression can never be stored. The
resulting `cron.Schedule` also computes the next occurrence: `sched.Next(after)`.

**Timezone.** A schedule stores an IANA timezone name (e.g. `America/Toronto`, `UTC`),
validated at write time with `time.LoadLocation`. Occurrence computation is always done in that
location:

```go
loc, _ := time.LoadLocation(s.Timezone)          // validated on write
next := cronSched.Next(after.In(loc)).UTC()      // stored as UTC
```

`next_run_at` / `last_run_at` are persisted as `timestamp without time zone` holding **UTC**,
matching the existing convention: every timestamp column in the schema is naive-UTC and
compared against `NOW() AT TIME ZONE 'UTC'` (see `ListTasksDueForArchive` in
`internal/store/sql/queries/task.sql:51`). The location is only used to interpret the cron
fields; the stored instants are absolute. This keeps "09:00 in `America/Toronto`" correct
across DST because `Next` is evaluated in the location each time (see § DST below).

### Database schema

New migration `internal/store/sql/migrations/20260718000001_schedules.sql`:

```sql
-- migrate:up

CREATE TABLE schedules (
    id           BIGSERIAL PRIMARY KEY,
    org_id       BIGINT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    created_by   TEXT   NOT NULL REFERENCES users(id),  -- who set it up (attribution only)
    name         TEXT   NOT NULL DEFAULT '',

    -- Task template. Same shape CreateTask takes.
    workspace    TEXT   NOT NULL,
    runner       TEXT   NOT NULL,
    namespace    TEXT   NOT NULL DEFAULT '',
    instructions JSONB  NOT NULL DEFAULT '[]',  -- [{text, url}], seeded as instruction events
    auto_archive BIGINT NOT NULL DEFAULT 0,     -- microseconds; passed through to each task

    -- Schedule spec.
    cron_expr    TEXT   NOT NULL,
    timezone     TEXT   NOT NULL DEFAULT 'UTC',
    enabled      BOOLEAN NOT NULL DEFAULT TRUE,

    -- Scheduler bookkeeping.
    next_run_at  TIMESTAMP,                     -- next fire time (UTC); NULL when disabled/paused
    last_run_at  TIMESTAMP,                     -- last time we fired
    last_task_id BIGINT REFERENCES tasks(id) ON DELETE SET NULL,  -- most recent run, for the UI

    created_at   TIMESTAMP NOT NULL DEFAULT (NOW() AT TIME ZONE 'UTC'),
    updated_at   TIMESTAMP NOT NULL DEFAULT (NOW() AT TIME ZONE 'UTC')
);

-- The scheduler's claim query scans only enabled, due rows. Partial index keeps it index-only
-- and tiny even with many paused/future schedules — same shape as idx_tasks_archive_due.
CREATE INDEX idx_schedules_due
    ON schedules (next_run_at)
    WHERE enabled = TRUE AND next_run_at IS NOT NULL;

-- Org-scoped list, matching idx_tasks_org_id.
CREATE INDEX idx_schedules_org_id ON schedules (org_id);

-- migrate:down

DROP TABLE IF EXISTS schedules;
```

Notes on choices, all following existing conventions:

- **`org_id` + `created_by`.** A schedule **belongs to the org**, not to a user — tenancy is
  `org_id`, exactly like `tasks` (`internal/store/sql/schema.sql:167-182`), and every list/get
  query filters on it while the FK cascades on org deletion. `created_by` is recorded purely for
  attribution/audit (who set it up, shown in the UI); it carries **no permission semantics** and
  is never consulted when a schedule fires — see § auth.
- **`instructions JSONB`.** The `tasks` table no longer has an instructions column — the
  `20260614000002_drop_task_instructions.sql` migration moved instructions into the event
  stream. But a *schedule* is a template that must remember what instructions to seed on every
  occurrence, so it stores them. On fire they become `InstructionPayload` events on the new
  task, identical to `CreateTask`.
- **`auto_archive BIGINT` (microseconds).** Same encoding and rationale as `tasks.auto_archive`
  (`20260523000001_task_archive_after.sql`): round-trips cleanly to/from `time.Duration`.
  Scheduled runs are the archetypal "nobody owns this" task, so most schedules will set a short
  auto-archive so each occurrence self-cleans.
- **`next_run_at` is stored, not derived.** The scheduler advances it transactionally on every
  fire, so the due-scan is a cheap index range read. Computing "is it due" at query time would
  mean re-parsing cron for every row on every tick.

### Model

New `internal/model/schedule.go`, following `internal/model/task.go`'s style:

```go
type Schedule struct {
    ID          int64
    OrgID       int64
    CreatedBy   string
    Name        string

    Workspace   string
    Runner      string
    Namespace   string
    Instructions []ScheduleInstruction   // {Text, URL}
    AutoArchive time.Duration

    CronExpr    string
    Timezone    string
    Enabled     bool


    NextRunAt   *time.Time      // UTC
    LastRunAt   *time.Time      // UTC
    LastTaskID  *int64

    CreatedAt   time.Time
    UpdatedAt   time.Time
}

// Next returns the first occurrence strictly after `after`, evaluated in the schedule's
// timezone, as UTC. Returns the zero value + error if the cron/timezone is invalid.
func (s *Schedule) Next(after time.Time) (time.Time, error)

// Validate parses cron_expr and loads timezone, returning a user-facing error on failure.
func (s *Schedule) Validate() error
```

`Instruction` already exists conceptually in the proto (`message Instruction { text, url }`);
we add the matching Go struct here (or reuse the generated one) for the JSONB round-trip.

### Store

New `internal/store/schedule.go` + `internal/store/sql/queries/schedule.sql`, mirroring
`task.go` / `task.sql`:

- `CreateSchedule`, `GetSchedule`, `GetScheduleForUpdate` (`... FOR UPDATE`, like
  `GetTaskForUpdate`), `ListSchedules` (org-scoped), `UpdateSchedule`, `DeleteSchedule`.
- The claim query — the heart of multi-instance correctness:

```sql
-- name: ClaimDueSchedules :many
-- Lock and return up to $1 due, enabled schedules, skipping any row another server
-- instance already holds. FOR UPDATE SKIP LOCKED makes the claim atomic across instances:
-- two schedulers ticking at the same instant partition the due set instead of both firing
-- the same schedule. The locks are held until the caller's transaction commits, by which
-- point next_run_at has been advanced past now, so the row is no longer due.
SELECT id, org_id, created_by, name, workspace, runner, namespace, instructions,
       auto_archive, cron_expr, timezone, enabled,
       next_run_at, last_run_at, last_task_id, created_at, updated_at
FROM schedules
WHERE enabled = TRUE
  AND next_run_at IS NOT NULL
  AND next_run_at <= (NOW() AT TIME ZONE 'UTC')
ORDER BY next_run_at
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: AdvanceSchedule :exec
UPDATE schedules
SET next_run_at = $1, last_run_at = $2, last_task_id = $3,
    updated_at = (NOW() AT TIME ZONE 'UTC')
WHERE id = $4 AND org_id = $5;
```

**Why `FOR UPDATE SKIP LOCKED` here rather than the archiver's optimistic `version` check.**
The archiver relies on a plain `FOR UPDATE` re-read plus a `version` comparison, and notes that
the server "runs as a single replica today" so two archivers scanning the same rows is merely
wasteful, not incorrect (`proposals/implemented/auto-archive-tasks.md` §"Multi-replica
considerations"). Schedules raise the stakes: firing mutates a *shared, monotonic*
`next_run_at`, and a duplicated fire is a duplicated real task run — a user-visible side effect,
not an idempotent no-op. `SKIP LOCKED` is the idiomatic Postgres primitive for exactly-once
work-claiming and composes with the row-lock pattern (`GetTaskForUpdate`) the codebase already
uses; it's the natural, minimal upgrade for work that genuinely must run on exactly one
instance.

### Scheduler worker

New package `internal/server/scheduler`, structurally a copy of `internal/server/archiver`:

```go
type Scheduler struct {
    store     *store.Store
    publisher pubsub.Publisher
    interval  time.Duration
    batchSize int
    log       *slog.Logger
}

func (s *Scheduler) Run(ctx context.Context) error {
    t := time.NewTicker(s.interval)   // DefaultInterval = 10s
    defer t.Stop()
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-t.C:
            if err := s.Tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
                s.log.Error("scheduler tick failed", "err", err)
            }
        }
    }
}
```

Each `Tick`:

1. Opens a transaction and calls `ClaimDueSchedules(batchSize)`. The claimed rows are locked
   for the duration of the transaction; concurrent schedulers skip them.
2. For each claimed schedule, inside the *same* transaction:
   a. Create the task exactly as `CreateTask` does — `CreateTask` + `LifecycleKindCreated` event
      (actor `ScheduleActor{ID}`, a new `model.Actor` sibling of `UserActor`/`RunnerActor`) + one
      `InstructionPayload` event per instruction. `org_id` is the schedule's. This is an
      org-level worker action with no per-user permission check — the same way the archiver
      archives with `RunnerActor` (§ auth). The fire is unconditional: the scheduler never
      inspects the previous run, it just creates the task when the schedule comes due.
   b. Compute the next fire time as the first occurrence strictly after `now`
      (`s.Next(time.Now())`, § missed runs) and call `AdvanceSchedule(next, firedAt, newTaskID)`.
3. Commit. Committing both releases the `SKIP LOCKED` locks *and* persists the advanced
   `next_run_at`, so the schedule is no longer due — a crash before commit simply rolls the
   whole tick back and the schedule stays due for the next tick (at-least-once claim, made
   exactly-once by the advance being in the same tx as the fire).
4. For each created task, publish the same `change` notification the manual create path emits
   (`{Action:"created", Type:"task"}` + `{Action:"appended", Type:"task_events"}`) so the Web
   UI and the runner's wake channel react immediately (`internal/pubsub`,
   `internal/command/runner.go` wake loop).

Defaults: `interval = 10s`, `batchSize = 100`, exposed as `--schedule-poll` and
`--schedule-batch` on `gritz server`, alongside the existing `--archive-poll` /
`--archive-batch`. A 10s tick bounds firing latency to well under the minute-resolution cron
grid while keeping DB load negligible (the partial index makes each scan index-only). Wired up
from `internal/command/server.go` next to the archiver goroutine, sharing the same
context-cancellation shutdown.

### Execution: schedule → task run

There is no new execution machinery. A fire is a `tasks` insert with `Command = Start`. The
runner's existing poll (`ListTasksForRunner`, `WHERE command != 0`) claims it, starts the
container, and drives it through the normal lifecycle. `last_task_id` links the schedule to its
most recent run for the UI ("last run: #1234, completed"). Because the schedule row is advanced
and committed the instant the task is created, the schedule's cadence is completely independent
of how long that task takes to run.

### Edge cases

**Missed runs after downtime — always skip.** Missed occurrences are never backfilled. When a
due schedule is claimed, the fire time is recomputed as the first occurrence strictly *after*
`now` (`s.Next(time.Now())`), not after the stored `next_run_at`. All occurrences that elapsed
while the scheduler (or the whole server) was down collapse into at most one fire, and the
schedule simply realigns to the grid. A nightly job that missed three nights runs *once* on
recovery, not three times. There is intentionally no backfill mode: replaying stale occurrences
(a "summarize yesterday" run for three days ago) is rarely what anyone wants, and the extra
policy surface, age-floor tuning, and per-tick draining it would require aren't worth it. If a
user genuinely needs a missed run, they can trigger the task manually.

**Catch-up storms.** With skip-only semantics there is no catch-up storm to bound: every
schedule fires at most once when it comes due, regardless of how long the outage was. The tick
is still capped at `batchSize` tasks total (across *distinct* schedules) as ordinary
back-pressure, but a single schedule can never enqueue a backlog.

**Overlapping runs of the same schedule.** Allowed, always. Each occurrence just creates a new
task row; if the previous run is still going when the next occurrence fires, the two run
concurrently as independent tasks. The scheduler never inspects the prior run — there is no
overlap policy. A schedule that shouldn't run concurrently with itself should be spaced far
enough apart in its cron expression, or made idempotent.

**DST / timezone drift.** Because `Next` is evaluated in the schedule's `time.Location` every
time (never by adding a fixed 24h), local-time semantics hold across DST transitions:

- *Spring forward* (a local time that doesn't exist, e.g. 02:30 on the changeover day): the
  robfig parser rolls forward to the next valid instant, so the job runs once shortly after the
  gap rather than being silently dropped.
- *Fall back* (a local time that occurs twice, e.g. 01:30): the job runs once, on the first
  occurrence — `Next` returns the earliest matching instant.
- Storing UTC instants in `next_run_at` while re-deriving from the location on each advance
  means a schedule can't drift: even if the DB row was written before a tz-database update, the
  next advance recomputes against the current rules. Changing a schedule's `timezone` via
  `UpdateSchedule` recomputes `next_run_at` immediately.

**Invalid schedule at fire time.** If `s.Next()` errors (e.g. a timezone removed from the tz
database), the due occurrence still fires — creating the task never needed `Next()`, only
advancing did — and then the scheduler logs the cause server-side and sets `enabled = FALSE` and
`next_run_at = NULL` so the row drops out of the claim query instead of silently wedging as
permanently due. The reason lives in the server logs.

### API surface

New proto RPCs on `GritzService` (`proto/gritz/v1/gritz.proto`), following the existing
naming (`CreateTask`, `ListTasks`, …):

```proto
rpc CreateSchedule(CreateScheduleRequest) returns (CreateScheduleResponse);
rpc GetSchedule(GetScheduleRequest) returns (GetScheduleResponse);
rpc ListSchedules(ListSchedulesRequest) returns (ListSchedulesResponse);
rpc UpdateSchedule(UpdateScheduleRequest) returns (UpdateScheduleResponse);
rpc DeleteSchedule(DeleteScheduleRequest) returns (DeleteScheduleResponse);
rpc SetScheduleEnabled(SetScheduleEnabledRequest) returns (SetScheduleEnabledResponse);
```

```proto
message Schedule {
  int64 id = 1;
  string name = 2;
  string workspace = 3;
  string runner = 4;
  string namespace = 5;
  repeated Instruction instructions = 6;   // reuses the existing Instruction message
  string cron_expr = 7;
  string timezone = 8;                      // IANA name; default "UTC"
  bool enabled = 9;
  google.protobuf.Duration auto_archive = 10;
  google.protobuf.Timestamp next_run_at = 11;
  google.protobuf.Timestamp last_run_at = 12;
  int64 last_task_id = 13;                  // 0 = never run
  string created_by = 14;
  google.protobuf.Timestamp created_at = 15;
  google.protobuf.Timestamp updated_at = 16;
}

message CreateScheduleRequest {
  string name = 1;
  string workspace = 2;
  string runner = 3;
  string namespace = 4;
  repeated Instruction instructions = 5;
  string cron_expr = 6;
  string timezone = 7;
  bool enabled = 8;                         // explicit: must be true to run; false/unset stores an inert schedule
  google.protobuf.Duration auto_archive = 9;
}
// UpdateScheduleRequest mirrors CreateTask/UpdateTask: id + the mutable fields. Changing
// cron_expr or timezone recomputes next_run_at server-side.
// SetScheduleEnabledRequest { int64 id; bool enabled; } — enabling recomputes next_run_at
// from now; disabling sets next_run_at = NULL so the claim query skips it.
```

Handlers live in a new `internal/server/apiserver/schedule.go`, authorized through the existing
`authscope` model. **Permissions gate the API surface, not the firing.** A schedule belongs to
the org; a caller's scopes only decide whether they may *create/mutate* one. Reuse
`OpTaskCreate` semantics: creating/updating a schedule requires the caller hold task-create
scope for the target `(workspace, runner)` — a schedule is a deferred `CreateTask`, so setting
one up should demand the same permission. Deleting or toggling a schedule
(`DeleteSchedule`/`SetScheduleEnabled`) is a mutation, gated on `OpTaskWrite` — the mutation
tier every other write in the codebase uses (`UpdateTask`, `ArchiveTask`, `DeleteEvent`); there
is no task-delete op, and a `task.read`-only caller must not be able to delete an org schedule
or switch one back on. Listing/getting a schedule is gated on org membership like the task read
op.

Once created, the schedule is an org-owned object and the scheduler fires it **without any
per-user permission check** — the same way the archiver archives with `RunnerActor`, not on
behalf of some user. `created_by` is not consulted at fire time, so a schedule keeps running
even if its creator later loses task-create scope or leaves the org (their membership row is
gone, but the schedule and its org are unaffected). This keeps schedules decoupled from
individual-user lifecycle, which is the whole point of an org-level resource.

Enable/disable is split into its own `SetScheduleEnabled` RPC (rather than only living inside
`UpdateSchedule`) because it's the one-click action the UI toolbar needs most, and it has
distinct side effects (recompute vs null `next_run_at`).

### Web UI

A new "Schedules" section in the v2 UI (`webui/`, TanStack Router/Query per the `webui` skill):
a list view (name, cron, timezone, enabled toggle, next run, last run + status link to
`last_task_id`), a create/edit form (workspace + runner pickers reused from the task-create
form, a cron field with a human-readable "next 3 runs" preview computed from the parsed
expression, timezone select, instructions, auto-archive presets), and
an inline enable/disable switch calling `SetScheduleEnabled`. Run `pnpm lint` in `webui/`
before finishing.

## Implementation Plan

A layer cake — each slice is small, independently reviewable, and safe to merge before the ones
above it land.

1. **Schema migration** — Delivers: the `schedules` table, `idx_schedules_due`,
   `idx_schedules_org_id`, and the regenerated `schema.sql`. Depends on: nothing.
   Verifiable by: `mise run up:test` applies the migration; `migrate:up`/`migrate:down`
   round-trip cleanly.
2. **Model + store** — Delivers: `internal/model/schedule.go` (`Schedule`, `Next`, `Validate`,
   `ScheduleActor`), `schedule.sql` queries (incl.
   `ClaimDueSchedules` with `FOR UPDATE SKIP LOCKED` and `AdvanceSchedule`), and
   `internal/store/schedule.go`. Adds `github.com/robfig/cron/v3` to `go.mod`. Depends on: (1).
   Verifiable by: store unit tests against the test Postgres (`internal/store/schedule_test.go`)
   covering CRUD, `Next` across a DST boundary, and a concurrent-claim test proving two
   `ClaimDueSchedules` callers don't double-claim.
3. **Proto + RPC handlers** — Delivers: the `Schedule` messages/enums and six RPCs in the proto,
   `mise run generate`, and `internal/server/apiserver/schedule.go` with `authscope` wiring.
   Depends on: (2). Verifiable by: handler tests creating/listing/enabling/deleting schedules
   and asserting cron/timezone validation errors surface as `InvalidArgument`.
4. **Scheduler worker** — Delivers: `internal/server/scheduler`, wired into
   `internal/command/server.go` with `--schedule-poll` / `--schedule-batch`. Depends on: (2),
   (3). Verifiable by: a `Tick`-driven test (like `archiver`'s) asserting a due schedule
   produces exactly one task + the right events, advances `next_run_at`, skips missed
   occurrences after a simulated downtime, and fires exactly once under two concurrent
   schedulers.
5. **Web UI** — Delivers: the Schedules list + create/edit views. Depends on: (3). Verifiable
   by: rendering against seeded schedules; `pnpm lint` passes.

## Trade-offs

| Approach | Pros | Cons |
|----------|------|------|
| **Stored `schedules` table + `SKIP LOCKED` scheduler worker** (proposed) | Reuses the archiver worker shape and the `CreateTask` path verbatim; exactly-once across instances via a single Postgres primitive already idiomatic in the code; scheduling fully decoupled from execution; visible in UI, org-scoped, scope-gated | Adds a second background worker and a cron dependency; introduces the first `SKIP LOCKED` usage (archiver used optimistic version only) |
| **Optimistic `version` claim (mirror the archiver exactly)** | Zero new locking concepts; identical to auto-archive | A duplicated fire is a duplicated *real task run*, not an idempotent no-op; correct only while the server is single-replica, which the prompt explicitly rules out |
| **Postgres advisory lock around the whole tick** (one leader) | Dead simple; one scheduler runs at a time | Serializes all scheduling through one instance (no horizontal scale of the scheduler); a stuck leader stalls every schedule; coarser than per-row claiming |
| **External cron → `gritz task create`** (status quo) | No code | Invisible to the product; no tenancy/scope integration; every integration reinvents state; not what the issue asks for |
| **Compute due-ness at read time (no `next_run_at` column)** | No bookkeeping column to advance | Re-parses cron for every row every tick; can't express last-run without more columns anyway |

The proposed shape is the smallest thing that is *correct across instances* while staying
maximally consistent with what already exists: the worker is the archiver, the fire is
`CreateTask`, the tenancy is `org_id` — the only genuinely
new primitive is `FOR UPDATE SKIP LOCKED`, chosen precisely because a schedule fire is a
non-idempotent side effect that the archiver's optimistic check wasn't designed to protect.

## Open Questions

1. **Runner concurrency cap.** Schedules fire unconditionally, so a schedule can overlap itself
   and many schedules can come due at once. Do we need a per-workspace or per-runner concurrency
   cap so a burst of scheduled tasks can't swamp a runner? Likely a separate concern (runner-side
   admission control), noted so the schema doesn't need to carry it prematurely.
2. **Timezone list source.** The UI timezone picker needs the IANA name list. Ship a static
   bundled list, or expose a `ListTimezones` helper RPC derived from the server's tz database
   (so client and server agree on what `time.LoadLocation` will accept)?
3. **Jitter.** Should we add optional per-schedule jitter (e.g. ±N seconds) so many `0 * * * *`
   schedules don't all fire on the exact minute boundary and stampede a runner? Cheap to add to
   `Next`; deferred unless load shows it's needed.