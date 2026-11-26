# Smarter Schedule Polling

Issue: https://github.com/gritzapp/gritz/issues/1665

## Problem

`scheduler.Scheduler.Run` (`internal/server/scheduler/scheduler.go`) ticks on a fixed
`time.Ticker` (`--schedule-poll` / `GRITZ_SCHEDULE_POLL`, default `scheduler.DefaultInterval`
= 10s) and runs `ClaimDueSchedules` every tick, whether or not anything can be due. Schedules
are minute-resolution cron (`model.cronParser` has no seconds field), so the vast majority of
ticks claim nothing. In production this loop is the single largest source of traces: ~8,650
parentless traces per 24h (~6/min), each a bundle of `otelsql` `begin_tx` / `query` / `rows` /
`commit` / `reset_session` spans with no indication that they belong to the scheduler.

The fixed interval is also imprecise in the other direction: a fire lands anywhere up to 10s
after its `next_run_at`, and a tick that fills `--schedule-batch` waits a full interval before
claiming the remainder.

## Design

### Overview

Replace the ticker with a single `time.Timer` that is re-armed after every tick to the earliest
upcoming `next_run_at`, capped at a maximum sleep. Schedule writes in the API server poke a
coalescing wake channel so a newly created / re-timed schedule is not stuck behind a long
sleep. The claim query, its `FOR UPDATE SKIP LOCKED`, and the fire-and-advance transaction are
unchanged.

```
          ┌─────────── wake (schedule created/updated/enabled/deleted) ───────────┐
          ▼                                                                       │
  ┌──────────────┐   Tick: claim + fire + advance + NextScheduleDelay   ┌─────────┴───┐
  │ timer / wake │ ───────────────────────────────────────────────────► │ re-arm timer │
  └──────────────┘ ◄─────────────────────────────────────────────────── └─────────────┘
                     sleep = clamp(delay, minSleep, maxSleep)
```

### Store: `NextScheduleDelay`

New query in `internal/store/sql/queries/schedule.sql`:

```sql
-- name: NextScheduleDelay :one
-- Seconds from now until the earliest enabled schedule is due, measured on the
-- database clock. NULL when no schedule is enabled. Negative or zero when a row is
-- already due (e.g. it is locked by another instance mid-fire). clock_timestamp()
-- rather than NOW(): this runs at the end of the tick's transaction, and NOW() is
-- frozen at transaction start, which would overstate the delay by the time spent
-- firing. MIN over idx_schedules_due is a single index probe.
SELECT EXTRACT(EPOCH FROM MIN(next_run_at) - (clock_timestamp() AT TIME ZONE 'UTC'))::float8
FROM schedules
WHERE enabled = TRUE
  AND next_run_at IS NOT NULL;
```

The predicate matches the partial index `idx_schedules_due` exactly, so no migration is needed.
Store method:

```go
// NextScheduleDelay returns how long until the earliest enabled schedule is due, on
// the database clock. ok is false when there are no enabled schedules.
func (s *Store) NextScheduleDelay(ctx context.Context, tx *sql.Tx) (d time.Duration, ok bool, err error)
```

It is added to the `scheduler.Store` interface (and the moq regenerated).

The query runs **inside the tick's transaction, after the fires**. Under `READ COMMITTED` each
statement takes a fresh snapshot and sees the transaction's own uncommitted writes, so the
`AdvanceSchedule`s just made are reflected, and it costs one extra `query` span in an existing
trace instead of a second transaction.

### Scheduler loop

`Tick` keeps its exactly-once semantics but reports what it learned:

```go
// TickResult is what one Tick learned about the schedule set.
type TickResult struct {
	Fired     int            // schedules fired and committed
	Full      bool           // the claim returned BatchSize rows; more may be due
	NextDelay *time.Duration // nil when no schedule is enabled
}

func (s *Scheduler) Tick(ctx context.Context) (TickResult, error)
```

`Run` becomes:

```go
func (s *Scheduler) Run(ctx context.Context) error {
	timer := time.NewTimer(0) // tick once at startup
	defer timer.Stop()
	reason := reasonStartup
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		case <-s.wake:
			reason = reasonWake
			timer.Stop()
		}
		res, err := s.tick(ctx, reason)
		if err != nil && !errors.Is(err, context.Canceled) {
			s.log.Error("scheduler tick failed", "err", err)
		}
		var d time.Duration
		d, reason = s.nextSleep(res, err)
		timer.Reset(d)
	}
}
```

`nextSleep` is a pure function (unit tested in isolation):

| Tick outcome | Sleep | Next reason |
| --- | --- | --- |
| error | `min(errorBackoff, maxSleep)` (`errorBackoff` = 5s) | `retry` |
| `Full` | `0` — drain the backlog immediately | `batch` |
| `NextDelay == nil` (no enabled schedules) | `maxSleep` | `cap` |
| `*NextDelay <= 0` (due rows we could not claim — locked by another instance) | `minSleep` (1s) | `due` |
| `*NextDelay > maxSleep` | `maxSleep` | `cap` |
| otherwise | `*NextDelay` | `due` |

`minSleep` is the hot-loop guard: it is only reached when the database says something is due
but our `SKIP LOCKED` claim did not get it, which means another instance holds the row and will
advance it within its transaction. It is never applied to a positive delay, so it does not cost
precision on the normal path.

### Wake channel

```go
// Wake asks the scheduler to re-tick and recompute its next wakeup. It never
// blocks. Wakes coalesce: a buffered channel of capacity 1 means a burst of
// schedule edits costs at most one extra tick, and a wake arriving mid-tick is
// kept and runs another tick right after, so a change committed after this
// tick's NextScheduleDelay read is never missed.
func (s *Scheduler) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
```

A wake runs a full `Tick`, not just the delay query. That keeps a single code path; the claim on
an index with nothing due is an index probe that returns zero rows.

**Who calls `Wake`.** `apiserver.Server` gets an optional dependency, threaded through
`server.Options` the same way `Publisher` is:

```go
// ScheduleWaker is notified after a schedule write commits so the scheduler can
// recompute its next wakeup. *scheduler.Scheduler implements it.
type ScheduleWaker interface{ Wake() }
```

`CreateSchedule`, `UpdateSchedule`, `SetScheduleEnabled` and `DeleteSchedule` call
`s.wakeScheduler()` next to their existing post-commit `s.publish(...)`. Only creates, updates
and enables can move the earliest `next_run_at` *earlier* (the case that matters for
correctness); deletes and disables only make it later, and waking for them just lets the timer
lengthen instead of firing into an empty tick. Waking on all four keeps the rule simple: "every
schedule write wakes". `RunSchedule` does not touch `next_run_at` and does not wake. The
scheduler's own `AdvanceSchedule` needs no wake — the same tick reads `NextScheduleDelay` after
advancing.

In `internal/command/server.go` the scheduler is constructed before `server.New` (so it can be
passed in) and its goroutine started where it is today. It is only assigned to
`server.Options.ScheduleWaker` when the scheduler is enabled, to avoid a typed-nil
`*scheduler.Scheduler` inside a non-nil interface.

**Why not the existing pubsub.** `apiserver` already publishes `change` notifications with
`Type: "schedule"` resources at exactly these points, so subscribing to `pubsub.LocalPubSub`
looks free. It isn't a good fit: `Subscribe` is keyed by org, so the scheduler would need a new
subscribe-all API; `Publish` drops on a full buffer (correct for SSE, wrong for a wake signal,
though a 1-slot coalescing channel tolerates drops anyway); and it is in-process only, so it
buys nothing over a direct call for multi-replica. A direct `Wake()` is explicit, has no
filtering, and is trivially testable with a fake. (A thin `Publisher` decorator that calls
`Wake()` whenever it sees a `schedule` resource is a reasonable alternative that needs zero
`apiserver` changes; it trades explicitness for fewer touch points. See Open Questions.)

### Multiple replicas

The server runs as a single replica today, but the scheduler is already designed to be correct
across instances, and this change must not regress that.

**Change propagation.** A wake is local to the replica that served the write. That is
sufficient whenever every replica runs the scheduler, because *any* scheduler can fire *any*
schedule: the replica that took the write re-ticks, sees the new earliest `next_run_at`, and
sleeps exactly until it. Other replicas' timers are stale but only in the "too late" direction,
and they don't need to be on time because the writer's replica is.

The cases where a change is only picked up at the `maxSleep` cap are:

- the write was served by a replica running with `--schedule-poll=0` (an API-only replica);
- the process crashed between the write committing and `Wake()`;
- a schedule row was modified out of band (manual SQL, a future migration).

**The cap is the cross-replica fallback**, and this proposal deliberately stops there.
Postgres `LISTEN/NOTIFY` would close the gap — `pg_notify('gritz_schedules', '')` issued inside
the write transaction is delivered only on commit, which is exactly the right semantics — but it
needs a dedicated, long-lived `pgx.Conn` outside the `database/sql` pool `store.Open` builds via
`otelsql`, reconnect/re-`LISTEN` handling (and a forced tick after reconnect, since notifications
during the outage are lost), and it is incompatible with PgBouncer in transaction-pooling mode.
None of that is justified while there is one replica and every replica runs the scheduler. If
API-only replicas appear, it slots in cleanly as one more caller of `Wake()` (see
Implementation Plan, layer 6).

**`FOR UPDATE SKIP LOCKED`.** The claim, the in-transaction fire, and `AdvanceSchedule` are
unchanged, so the exactly-once argument in the package doc still holds. What changes is timing:
with fixed polling, replicas tick at uncorrelated phases; with precise wakeups, every replica
computes the same earliest `next_run_at` and wakes at the same instant. That is still correct —
`SKIP LOCKED` partitions the due rows, and each schedule is advanced in the transaction that
fired it — but the losers then read `NextScheduleDelay` while the winner's transaction is still
open, see the old (due) `next_run_at`, and take the `minSleep` branch. Cost: at most one extra
empty tick per replica per fire, 1s later. If that ever matters, a few hundred ms of random
jitter on `due` sleeps would spread the replicas out; not worth it at one replica, noted only.

### Clock skew and precision

- **When to wake is measured on the database clock.** `NextScheduleDelay` returns a *duration*
  computed entirely inside Postgres (`MIN(next_run_at) - clock_timestamp()`), and the Go side
  only feeds it to a monotonic `time.Timer`. Server wall-clock skew therefore has no effect on
  wake timing; only the query round-trip (sub-ms to a few ms) and timer latency do.
- **Never early.** The claim compares against `NOW()`, which is the claim transaction's start
  time — necessarily after the timer fired, which is after `next_run_at` on the DB clock. The
  float is converted with sub-microsecond rounding *up* (`time.Duration(math.Ceil(secs*1e6)) *
  time.Microsecond`) so the delay is never truncated a microsecond short. In the pathological
  case where we do wake a hair early (e.g. the database clock is stepped), the claim returns
  nothing, the next `NextScheduleDelay` is a tiny positive delay, and the timer re-arms for it —
  the fire is a few ms late, never lost.
- **Late by milliseconds instead of up to 10s.** That is well inside the minute cron grid.
- **Latent double-fire under server/DB skew (fix included).** `Scheduler.fire` computes the
  next occurrence as `sched.Next(now)` with `now := time.Now()` — the *server* clock — while the
  claim decided the row was due by the *database* clock. If the server clock is behind the
  database by more than `NOW() - next_run_at` at claim time, `Next(now)` returns the occurrence
  that was just fired, `AdvanceSchedule` writes `next_run_at` back to the same instant, and the
  next tick fires it again. With 10s polling this needs skew larger than the tick's lateness
  (0–10s) and is rare; with wakeups landing within milliseconds of `next_run_at`, *any* negative
  skew triggers it. Fix: advance from whichever is later:

  ```go
  base := now
  if sched.NextRunAt != nil && sched.NextRunAt.After(base) {
  	base = *sched.NextRunAt
  }
  next, err := sched.Next(base)
  ```

  `Next` is strictly-after, so the stored `next_run_at` always moves forward past the claimed
  occurrence regardless of skew, and the skip-only/no-backfill behavior is preserved (when the
  server clock is ahead, `now` still wins). This ships as its own `fix:` PR first, since it is a
  correctness bug independent of the rest.

### Flags: repurpose `--schedule-poll` as the cap

`--schedule-poll` / `GRITZ_SCHEDULE_POLL` keeps its name, env var and "0 disables the
scheduler" meaning, and becomes the **maximum** sleep between ticks:

```go
&cli.DurationFlag{
	Name:    "schedule-poll",
	Usage:   "Maximum time the scheduler sleeps between checks for due schedules; it wakes earlier for the next due schedule or any schedule change. 0 disables the scheduler.",
	Value:   scheduler.DefaultMaxSleep,
	Sources: cli.EnvVars("GRITZ_SCHEDULE_POLL"),
},
```

`scheduler.DefaultInterval` (10s) is replaced by `DefaultMaxSleep = time.Minute` and
`Options.Interval` is renamed `Options.MaxSleep`. Rationale:

- Keeping the flag avoids breaking deployments (the infra repo should be checked for an explicit
  `GRITZ_SCHEDULE_POLL`; any value there keeps working, with the new meaning).
- An existing small value degrades gracefully: `--schedule-poll=10s` behaves like today for idle
  periods while still firing precisely.
- 1 minute bounds the fallback latency (API-only replica, lost wake, out-of-band edit) to one
  cron grid step, while cutting idle ticks from 8,640/day to ≤1,440/day per replica. Idle ticks
  are not the cost driver any more once they're properly traced (below), so there is little to
  gain from a 5–10 minute cap and real latency to lose in the fallback paths.

`--schedule-batch` is unchanged; a full batch now re-ticks immediately rather than waiting.

### Observability

**Each tick is its own root trace** with a named span, and the `otelsql` spans become its
children:

```go
var tracer = otel.Tracer("github.com/gritzapp/gritz/internal/server/scheduler")

ctx, span := tracer.Start(ctx, "scheduler.tick",
	trace.WithNewRoot(),
	trace.WithSpanKind(trace.SpanKindInternal),
	trace.WithAttributes(attribute.String("gritz.scheduler.reason", string(reason))),
)
defer span.End()
// on completion:
span.SetAttributes(
	attribute.Int("gritz.scheduler.fired", res.Fired),
	attribute.Bool("gritz.scheduler.batch_full", res.Full),
	attribute.Float64("gritz.scheduler.next_delay_seconds", ...), // omitted when nil
)
```

Root, not a child of anything: a tick is a unit of background work, not part of the request
that woke it. When the reason is `wake`, it would be nice to record a **span link** to the
request that called `Wake()`, which would need `Wake(ctx)` to stash the caller's
`SpanContext` (see Open Questions). Each fired task's `CreateTask` already happens inside this
span, so "which tick created task 123" becomes answerable in Honeycomb.

**Idle ticks are still traced.** After this change an idle tick only happens at the cap
(≤1,440/day/replica at the default), on a schedule write (a user action worth seeing), or on a
`minSleep` retry (an interesting contention signal). The cap tick is also the self-healing
heartbeat — its regular presence in traces is how we know the scheduler is alive. Suppressing
idle ticks would need either a `otelsql.SpanFilter` keyed on a context marker or a
non-sampled parent, both of which add machinery for a volume that is now small. Expected volume
is roughly `fires + schedule writes + 1,440` traces/day per replica, versus 8,650 today.

Logging is unchanged (`"scheduler fired schedules"` at Info when `Fired > 0`); add a Debug log
of the computed sleep and reason.

### Archiver

`archiver.Archiver` has the same ticker shape, but should **not** adopt sleep-until in this
change:

- Its deadline is `updated_at + auto_archive`, and `updated_at` moves on essentially every task
  write (status changes, restarts, edits). The set of wake sources is "every task mutation
  path", not four handlers, so the plumbing is much broader.
- Precision doesn't matter — archiving a task a minute late is invisible — and the archiver is
  disabled by default (`--archive-poll` defaults to 0) with a 1-minute `DefaultInterval`, so its
  trace volume is already ~6x lower than the scheduler's when enabled.

The cheap, worthwhile part does carry over: wrap `Archiver.Tick` in an `archiver.tick` root span
the same way, so its queries stop showing up as anonymous parentless traces. If its volume ever
matters, the next step would be a `NextArchiveDelay` (`MIN(updated_at + auto_archive)` over
`idx_tasks_archive_due`'s predicate) with the cap as the only wake source — no wake plumbing —
which alone would make idle ticks rare.

## Implementation Plan

1. **Fix advance under clock skew** (`fix(scheduler): ...`) — Delivers: `fire` advances from
   `max(now, sched.NextRunAt)`. Depends on: nothing. Verifiable by: a scheduler unit test with a
   claimed schedule whose `NextRunAt` is after the test's `now` asserting `AdvanceSchedule`
   receives the following occurrence, not the same one.
2. **Tick root span** — Delivers: `scheduler.tick` root span with `reason` / `fired`
   attributes around today's ticker loop (and the same `archiver.tick` span). Depends on:
   nothing. Verifiable by: a test using an in-memory span recorder (as in
   `internal/x/otelx/mux_test.go`) asserting one root span per tick with DB spans as children;
   in Honeycomb, parentless `otelsql` traces from the scheduler disappear.
3. **`NextScheduleDelay` query** — Delivers: sqlc query, `Store.NextScheduleDelay`, added to
   `scheduler.Store` + regenerated moq. Unused by the loop. Depends on: nothing. Verifiable by:
   store tests against Postgres covering no schedules (`ok=false`), only disabled schedules,
   future, past, and that it sees uncommitted advances in the same tx.
4. **Adaptive loop** — Delivers: `TickResult`, `nextSleep`, timer-based `Run`, `Wake()`,
   `DefaultMaxSleep`, `Options.MaxSleep`, `--schedule-poll` usage/default change. Depends on:
   (3). Verifiable by: table test of `nextSleep`; `Run` tests with `StoreMock` asserting a tick
   at startup, an immediate re-tick on `Full`, and a re-tick on `Wake()`; existing `Tick` tests
   updated for the new return value.
5. **Wake on schedule writes** — Delivers: `ScheduleWaker` in `server.Options` /
   `apiserver.Options`, `wakeScheduler()` calls in the four handlers, scheduler constructed
   before `server.New` in `server.go`. Depends on: (4). Verifiable by: `apiserver` schedule tests
   with a counting fake waker asserting one wake per committed write and none on validation
   failure or `RunSchedule`; manual end-to-end: create a schedule for the next minute and
   observe it fire within ms of the minute boundary with no intervening idle ticks.
6. **(Deferred) `LISTEN/NOTIFY` wake source** — Delivers: `pg_notify` in schedule write
   transactions and a listener goroutine on a dedicated `pgx.Conn` that calls `Wake()` and
   forces a tick on (re)connect. Depends on: (5). Only if API-only replicas are introduced.
   Verifiable by: two scheduler instances against one database, write via one, observe the
   other re-arm.

Layers 1–3 are independent and can land in any order; 1 should land first regardless.

## Trade-offs

| Option | Pros | Cons |
| --- | --- | --- |
| **Sleep-until-earliest + local wake + 1m cap** (proposed) | Precise fires; idle ticks drop ~6x and each is one named trace; no new infrastructure; correct with any number of replicas that all run the scheduler | Changes on an API-only replica (or lost wakes) wait up to the cap; adds a timer state machine to an otherwise trivial loop |
| **Keep fixed polling, just add the root span / longer interval** | Smallest change | A longer interval makes fires later (60s poll = up to 60s late); doesn't fix batch-full latency; still mostly empty ticks |
| **Postgres `LISTEN/NOTIFY` now** | Cross-replica wakes with commit semantics | Dedicated connection outside the `otelsql` pool, reconnect logic, PgBouncer-transaction-mode incompatible; solves a problem we don't have at one replica |
| **Subscribe the scheduler to `LocalPubSub`** | Reuses existing `schedule` notifications | Needs a subscribe-all API; drop-on-full semantics; in-process only, so no multi-replica benefit over a direct call |
| **Sleep-until with no wake (cap only)** | No `apiserver` changes | A schedule created to run in 30s could fire up to a minute late; a wake is ~20 lines |
| **Advisory lock so only one replica schedules** | Removes the simultaneous-wake contention | Unnecessary with `SKIP LOCKED`; adds leader-failover latency |

## Open Questions

1. **Default cap.** 1 minute is proposed. Should it be longer (5m) given idle ticks are now
   cheap and well-labelled, accepting a longer worst case for the fallback paths?
2. **Explicit `ScheduleWaker` vs. a `Publisher` decorator** that wakes on any `schedule`
   notification. The decorator needs no `apiserver` changes but makes the wake an implicit side
   effect of publishing.
3. **Span link from wake to request.** Worth changing the signature to `Wake(ctx)` so the tick
   can link back to the `UpdateSchedule` (etc.) request that caused it?
4. **Infra.** Does `gritzapp/infra` set `GRITZ_SCHEDULE_POLL` explicitly? If so, should it be
   removed to pick up the new default when this ships?
5. **Global `otelsql` noise.** Independently of the scheduler, should `store.Open` set
   `OmitConnResetSession` / `OmitRows` in `otelsql.SpanOptions`? That would shrink every trace,
   not just the scheduler's, but is a separate decision.
