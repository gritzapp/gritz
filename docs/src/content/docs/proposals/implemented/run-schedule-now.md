---
title: "Run a Schedule Now (Manual Trigger)"
description: "Schedules only fire on their cron cadence, so users cannot test a new schedule or kick off its work ad hoc without waiting or hand-copying the template into a task."
---

Issue: https://github.com/icholy/gritz/issues/1481

## Problem

Schedules fire on their cron cadence and nothing else. After writing a schedule you have
no way to see it actually run — you either wait for its next occurrence or hand-copy the
template into a one-off `CreateTask`. That's the wrong shape for the two things people do
right after creating a schedule: testing that the instructions/workspace/runner are correct,
and kicking the work off ad-hoc when it's needed before the next tick.

We want a **"Run now"** action on the Schedules page that fires a schedule immediately,
producing a task identical to what the next scheduled occurrence would have produced. The
manual run is an *extra* one-off fire: it must not disturb the cron cadence — the next
scheduled run stays exactly where it was.

## Design

### Overview

The scheduler worker already turns a due schedule into a real task through a single method,
`Scheduler.fire` (`internal/server/scheduler/scheduler.go`). That method does two separable
things:

1. **Materialize one occurrence** — insert the template task (`sched.Task()`), seed its
   events (`sched.Events(task)`: a `LifecycleKindCreated` event attributed to
   `model.ScheduleActor` plus one wake-carrying `InstructionPayload` event per instruction),
   and build the task-created change notification (`{created task, appended task_events}` with
   `Runner: task.PendingRunner()` so the runner wakes).
2. **Advance the cadence** — recompute `Next(now)` and write `next_run_at`, `last_run_at`,
   `last_task_id` via `AdvanceSchedule`.

A manual run is exactly step 1 with *no* step 2: it materializes the task from the template and
records **nothing** on the schedule row — not `next_run_at`, not `last_run_at`, not
`last_task_id`. The `schedules` row stays purely about the cron cadence. So the design is a small
refactor — split the occurrence out of `fire` into a shared function — plus a new RPC and a UI
button that call it.

The key property we want to guarantee structurally, not by convention, is **"a manual run and
a scheduled run produce an identical task."** The only way to keep that true as the code
evolves is to have both paths call the *same* occurrence-materializing function, rather than
re-deriving the task/events in the handler. Extracting that function — `scheduler.Fire` — is
the load-bearing part of this proposal.

### The shared fire path

Extract the occurrence half of `fire` into an exported function in the `scheduler` package
that takes the store subset it needs, so both the worker and the API handler call it:

```go
// internal/server/scheduler/scheduler.go

// Fire materializes one schedule occurrence inside tx: it creates the template
// task and seeds its events exactly as CreateTask does, and returns the task plus
// the change notification to publish once tx commits. It writes nothing to the
// schedule row — the caller decides what, if anything, happens to the cadence: the
// worker's Scheduler.fire advances next_run_at via AdvanceSchedule, while a
// one-off manual run (RunSchedule) records nothing on the schedule at all. Sharing
// this function is what makes a manual run and a scheduled run produce a
// byte-for-byte identical task.
func Fire(ctx context.Context, tx *sql.Tx, st Store, sched *model.Schedule) (*model.Task, model.Notification, error) {
    task := sched.Task()
    if err := st.CreateTask(ctx, tx, task); err != nil {
        return nil, model.Notification{}, err
    }
    for _, ev := range sched.Events(task) {
        if err := st.CreateEvent(ctx, tx, ev); err != nil {
            return nil, model.Notification{}, err
        }
    }
    return task, model.Notification{
        Type: "change",
        Resources: []model.NotificationResource{
            {Action: "created", Type: "task", ID: task.ID},
            {Action: "appended", Type: "task_events", ID: task.ID},
        },
        OrgID:          task.OrgID,
        Runner:         task.PendingRunner(),
        Time:           time.Now(),
        ChannelMessage: fmt.Sprintf("Task %d created on %s/%s.", task.ID, task.Runner, task.Workspace),
    }, nil
}
```

`Store` is the existing scheduler `Store` interface (`internal/server/scheduler/scheduler.go`),
which already declares the `CreateTask` and `CreateEvent` methods `Fire` needs; `*store.Store`
satisfies it unchanged, so both the worker and the apiserver (whose `s.store` is a
`*store.Store`) pass it as-is, and tests drive the existing `StoreMock`. `Scheduler.fire`
becomes: call `Fire`, then `AdvanceSchedule` (the cadence half is unchanged).

The template → task/events mapping is *already* shared: it lives in `model.Schedule.Task()`
and `model.Schedule.Events()`. This refactor pulls the remaining duplicated piece — the store
writes and the task-created notification literal — up into one function too, so the manual and
scheduled paths can't drift.

### API surface

One new RPC on `GritzService`, placed alongside the existing Schedule RPCs in
`proto/gritz/v1/gritz.proto`:

```proto
rpc CreateSchedule(CreateScheduleRequest) returns (CreateScheduleResponse);
rpc GetSchedule(GetScheduleRequest) returns (GetScheduleResponse);
rpc ListSchedules(ListSchedulesRequest) returns (ListSchedulesResponse);
rpc UpdateSchedule(UpdateScheduleRequest) returns (UpdateScheduleResponse);
rpc DeleteSchedule(DeleteScheduleRequest) returns (DeleteScheduleResponse);
rpc SetScheduleEnabled(SetScheduleEnabledRequest) returns (SetScheduleEnabledResponse);
rpc RunSchedule(RunScheduleRequest) returns (RunScheduleResponse);   // new
```

```proto
// RunScheduleRequest fires a schedule immediately as a one-off, in addition to
// its cron cadence. It records nothing on the schedule row — next_run_at,
// last_run_at, and last_task_id are all left untouched; the row stays purely about
// the cron cadence. Allowed on a disabled schedule — a disabled schedule only means
// "don't fire automatically," and testing a not-yet-enabled schedule is the primary
// use case.
message RunScheduleRequest {
  int64 id = 1;
}

message RunScheduleResponse {
  Task task = 1;  // the task the manual run created — for the UI to link to
}
```

Returning the created task lets the UI navigate straight to the new run. Nothing on the
schedule row changed, so there is no schedule to return.

### Handler

New handler in the existing `internal/server/apiserver/schedule.go`:

Because the manual path writes nothing back to the `schedules` row, the read needs no
`FOR UPDATE` lock — a plain org-scoped `GetSchedule` is enough. The only writes in the
transaction are the task and its events, both inside `scheduler.Fire`.

```go
func (s *Server) RunSchedule(ctx context.Context, req *gritzv1.RunScheduleRequest) (*gritzv1.RunScheduleResponse, error) {
    caller := apiauth.MustCaller(ctx)
    var (
        task *model.Task
        note model.Notification
    )
    err := s.store.WithTx(ctx, nil, func(tx *sql.Tx) error {
        // No FOR UPDATE: nothing is written back to the schedule row, so a plain
        // org-scoped read suffices.
        sched, err := s.store.GetSchedule(ctx, tx, req.Id, caller.OrgID)
        if err != nil {
            return err
        }
        // A manual run materializes a real task on the schedule's target, so it
        // demands the same task-create scope CreateSchedule/UpdateSchedule require —
        // stronger than the task-write tier used for enable/disable. The target is
        // read from the row, not the request, so it can't be spoofed.
        if !caller.Scopes.Allow(authscope.OpTaskCreate,
            authscope.WithTaskWorkspace(sched.Workspace),
            authscope.WithTaskRunner(sched.Runner),
            authscope.WithTaskArchived(false),
        ) {
            return connect.NewError(connect.CodePermissionDenied, errors.New("cannot run schedule"))
        }
        // Same occurrence the scheduler worker fires — identical task and events —
        // and nothing else. The schedule row is untouched, so a manual run works
        // even on a disabled schedule or one whose cron/timezone later became invalid
        // (this path never computes Next()).
        task, note, err = scheduler.Fire(ctx, tx, s.store, sched)
        if err != nil {
            return err
        }
        return tx.Commit()
    })
    if err != nil {
        if connect.CodeOf(err) != connect.CodeUnknown {
            return nil, err
        }
        if errors.Is(err, sql.ErrNoRows) {
            return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("schedule %d not found", req.Id))
        }
        return nil, connect.NewError(connect.CodeInternal, err)
    }
    s.log.InfoContext(ctx, "schedule run manually", "id", req.Id, "task", task.ID)
    // Publish the same task-created notification the scheduler and CreateTask emit,
    // AFTER commit, so the runner wake channel and Web UI never see a rolled-back
    // task. Fire built it with Runner set; stamp in the acting user for the SSE fan-out.
    note.UserID, note.ClientID = caller.ID, caller.ClientID
    s.publish(note)
    return &gritzv1.RunScheduleResponse{Task: task.Proto(s.baseURL)}, nil
}
```

### Store

No new store methods. The manual path writes nothing to the `schedules` table; it only reads a
row (`GetSchedule`) and creates a task + events (via the existing `CreateTask`/`CreateEvent` that
`scheduler.Fire` calls). Because nothing is written back to the schedule, "a manual run never
touches the cadence" is not a property we have to enforce in SQL — there simply is no schedule
write to get wrong.

### Behavior decisions

The issue raises three behaviors; each is settled here.

- **The cron cadence must not advance (`next_run_at`).** This is the core requirement, and it's
  now trivially true: the manual path writes nothing to the schedule row at all. It never calls
  `Next()` and never touches `next_run_at`. A nightly `0 9 * * *` schedule that you "Run now" at
  14:00 still fires next at 09:00 tomorrow. The extra run is purely additive.

- **`last_task_id`/`last_run_at` are *not* updated.** A manual run records nothing on the
  schedule row — the row stays purely about the cron cadence. The run is surfaced entirely
  through the returned task and its `ScheduleActor` provenance event (the created-event on the
  new task records that the schedule fired it), not through the schedule's bookkeeping columns.
  This keeps `last_task_id`/`last_run_at` meaning specifically "the last *scheduled* run": the
  Schedules list's "Last run: #NNNN" link keeps pointing at the last automatic occurrence, so a
  quick ad-hoc test never clobbers the record of where the cadence actually is.

- **Allowed on a disabled schedule.** Yes. `enabled = false` means "don't fire *automatically*"
  — it takes the row out of the scheduler's claim query — but the template is still valid and a
  manual trigger is an explicit human action, not an automatic one. Allowing it directly serves
  the "test right after writing" flow: creates default to inert unless `enabled` is set true
  (`CreateScheduleRequest.enabled`), so the natural loop is *create disabled → Run now to test →
  enable*. Because the manual path writes nothing to the row, running a disabled schedule leaves
  it exactly as it was — `enabled = false`, `next_run_at` NULL — so Run now never silently
  re-enables anything.

### Web UI

Add a **"Run now"** action to each row of the Schedules list
(`webui/src/routes/schedules.index.tsx`), in the existing action button group alongside
Edit/Delete, using the `Play` icon already imported elsewhere (`task-timeline.tsx`):

- A `useMutation(runSchedule)` call fired on click with `{ id: schedule.id }`. The button shows
  a spinner (`Loader2`) while pending, matching the delete flow.
- The button is **always enabled**, including for disabled schedules (that's the test-first
  use case), with `aria-label="Run schedule now"`.
- On success, navigate to the created task (`/tasks/$id` with the returned `task.id`) so the
  user lands on the run they just triggered — the same "watch it run" payoff the create form's
  post-submit navigation gives. Nothing on the schedule row changed, so the list needs no
  refresh (the "Last run" link still points at the last scheduled run, as intended).

There is no confirm dialog: Run now is a low-friction, deliberately-repeatable action (unlike
Delete, which is destructive). Run `pnpm lint` in `webui/` before finishing.

## Implementation Plan

A layer cake — each slice is small, independently reviewable, and safe to merge before the ones
above it land.

1. **Refactor `fire` → shared `Fire`** — Delivers: the exported `scheduler.Fire` function (taking
   the existing `scheduler.Store` interface), with `Scheduler.fire` reduced to `Fire` +
   `AdvanceSchedule`. Depends on: nothing. Verifiable by: the existing scheduler `Tick` tests
   still pass unchanged, driven by the existing `StoreMock` (pure
   refactor — a due schedule still produces exactly one task, the right events, and an advanced
   `next_run_at`).
2. **Proto + RPC handler** — Delivers: `RunSchedule` RPC, `RunScheduleRequest`/`Response`
   messages, `mise run generate`, and the `RunSchedule` handler in
   `internal/server/apiserver/schedule.go` with `OpTaskCreate` authorization. Depends on: (1).
   Verifiable by: handler tests asserting a manual run creates one task with the `ScheduleActor`
   created-event + instruction events, writes nothing to the schedule row (`next_run_at`,
   `last_run_at`, `last_task_id` all unchanged), succeeds on a disabled schedule, and is denied
   for a caller without task-create scope on the target.
3. **Web UI** — Delivers: the "Run now" button on the Schedules list, wired to the `runSchedule`
   mutation with navigation to the created task. Depends on: (2). Verifiable by: rendering the
   list against seeded schedules, clicking Run now, landing on the new task; `pnpm lint` passes.

## Trade-offs

| Approach | Pros | Cons |
|----------|------|------|
| **Extract `Fire`, new `RunSchedule` RPC, write nothing to the schedule row** (proposed) | Manual and scheduled runs share one occurrence function, so an identical task is guaranteed structurally; "don't touch the cadence" is trivially true because there's no schedule write at all; disabled-schedule behavior falls out for free | Adds one RPC and a refactor of `fire`; apiserver takes a dependency on the `scheduler` package for `Fire` |
| **Handler re-derives the task/events inline** (don't extract `Fire`) | No refactor of `fire`; no apiserver→scheduler import | Duplicates the template→task/events/notification wiring; the "identical task" guarantee becomes convention that silently rots the first time either path changes |
| **Record the run on the schedule (`last_run_at`/`last_task_id`)** | The list's "Last run" reflects manual runs too | Needs a new store write; conflates "last scheduled run" with "last ad-hoc test," clobbering the cadence record on a throwaway test — the row should stay purely about the cron cadence |
| **Overload an existing RPC (e.g. a `run_now` flag on `SetScheduleEnabled`)** | No new RPC | Conflates two unrelated actions (toggle vs. fire); muddies the auth tier (enable is task-write, a fire is task-create); awkward response shape |
| **Client-side "copy template into `CreateTask`"** (no backend) | Zero backend work | The manual run wouldn't be attributed to `ScheduleActor` and would drift from the real fire path — the exact thing the issue's "identical task" intent rules out |

## Open Questions

1. **Confirmation before firing.** Run now starts a real container run that costs resources. The
   proposal makes it a single-click action (low friction is the point). If accidental fires
   become a problem, a lightweight confirm dialog (like Delete's) can be added without touching
   the backend.
2. **Concurrent manual runs / overlap.** Like scheduled occurrences, nothing stops "Run now" from
   creating a run while a previous run (manual or scheduled) is still going — they run as
   independent tasks, consistent with the existing "overlapping runs are allowed" decision in the
   scheduled-tasks proposal. No overlap guard is proposed here.
