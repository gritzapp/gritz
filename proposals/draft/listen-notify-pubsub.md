# LISTEN/NOTIFY Pubsub

Issue: https://github.com/icholy/xagent/issues/1512

## Problem

The notification bus (`internal/pubsub`) is an in-process, in-memory fan-out
keyed by org ID (`pubsub.LocalPubSub`). Publishers — API mutations
(`apiserver`), the event router, the scheduler, and the archiver — call
`Publisher.Publish`, which delivers directly to Go channels held by subscribers
connected to the **same** server process over the `/events` SSE endpoint
(`notifyserver`). Subscribers are the runner (waking on `for_runner` work), the
web UI, `xagent notify`, and the MCP channel bridge.

Because the bus lives entirely inside one process, a notification published on
one machine never reaches a subscriber connected to another. This pins the
deployment to a single server machine — a constraint currently enforced only by
a comment in `fly.toml`:

```toml
auto_stop_machines = "stop"
# Must stay at 1: the notification system uses in-process pubsub, so a second
# machine would miss notifications published on the other ...
min_machines_running = 1
```

The result is no HA, no overlapping rolling deploys, and a hard ceiling on SSE
fan-out capacity — all riding on an invisible deployment invariant.

The server already depends on PostgreSQL for everything else. Postgres
`LISTEN`/`NOTIFY` gives us a cross-process pub/sub bus for free: any machine can
`NOTIFY`, and every machine `LISTEN`ing receives it. This removes the
single-machine constraint without adding new infrastructure (Redis, NATS).

## Design

### Preserve the interfaces

The `pubsub.Publisher` and `pubsub.Subscriber` interfaces
(`internal/pubsub/pubsub.go`) stay exactly as they are:

```go
type Publisher interface {
	Publish(ctx context.Context, n model.Notification) error
}
type Subscriber interface {
	Subscribe(ctx context.Context, orgID int64) (<-chan model.Notification, func(), error)
}
```

Every publisher call site, the `Ignore` gating (`apiserver.go:91`,
`eventrouter.go:286`), the `notifyserver` SSE layer, and all clients remain
untouched. The only change is which implementation is wired in
`internal/command/server.go` (`pubsub.NewLocalPubSub()` →
`pubsub.NewPostgresPubSub(...)`).

### `PostgresPubSub`

A new implementation, `internal/pubsub/postgres.go`, satisfies both interfaces.
It layers Postgres transport on top of the existing in-process fan-out rather
than replacing it:

- **Local delivery** is still `LocalPubSub` — the buffered per-org channels, the
  slow-subscriber drop, and the context/cancel cleanup are all reused verbatim.
  `PostgresPubSub` embeds a `*LocalPubSub` and delegates `Subscribe` to it
  unchanged.
- **Cross-process transport** is a single global Postgres channel,
  `xagent_notify`, carrying the JSON-encoded `model.Notification` (which already
  has `json` tags and includes `OrgID`) as the payload.

#### Publish

```go
func (ps *PostgresPubSub) Publish(ctx context.Context, n model.Notification) error {
	data, err := json.Marshal(n)
	if err != nil {
		return err
	}
	_, err = ps.db.ExecContext(ctx, "SELECT pg_notify('xagent_notify', $1)", string(data))
	return err
}
```

`pg_notify` runs on the ordinary `*sql.DB` pool (`store.Open`, pgx stdlib
driver) — no dedicated connection needed for publishing. The publisher does
**not** deliver locally in-line; it relies on the round-trip so that *every*
process, including the one that published, receives the notification through the
same path. This gives one delivery code path and consistent ordering, at the
cost of a sub-millisecond DB round-trip.

Note: when `pg_notify` is executed inside a transaction, delivery happens on
commit. Publish uses the pool directly (not a caller transaction), so the notify
is sent immediately.

#### Listen

`database/sql`'s pooled connections can't be used for `LISTEN` (you can't
guarantee you get the same connection back, and the pool may reset it). The
listener therefore takes a **dedicated** connection via `pgx` directly. On
construction, `PostgresPubSub` opens one `*pgx.Conn`, issues
`LISTEN xagent_notify`, and runs a background goroutine:

```go
func (ps *PostgresPubSub) listen(ctx context.Context) {
	for {
		notif, err := ps.conn.WaitForNotification(ctx)
		if err != nil {
			// reconnect with backoff, re-issue LISTEN, continue
			continue
		}
		var n model.Notification
		if err := json.Unmarshal([]byte(notif.Payload), &n); err != nil {
			ps.log.Warn("bad notify payload", "err", err)
			continue
		}
		ps.local.Publish(ctx, n) // fan out to this process's subscribers
	}
}
```

`ps.local.Publish` is the existing `LocalPubSub.Publish` — it fans the decoded
notification out to the org's connected SSE subscribers on this machine, keeping
the slow-subscriber-drop semantics identical to today.

#### Reconnect

The dedicated listen connection is a new failure mode the in-memory bus didn't
have. If `WaitForNotification` errors (connection dropped), the goroutine
reconnects with capped backoff and re-issues `LISTEN`. Notifications emitted
during the gap are lost — acceptable because the current bus already drops on
slow subscribers and has no replay, and every consumer already reconciles via
polling (the runner's fallback poll interval, the web UI's queries). This is
called out under Trade-offs.

### Wiring

`internal/command/server.go` currently builds the bus from nothing:

```go
ps := pubsub.NewLocalPubSub()
```

It becomes (the `*sql.DB` and DSN are already in scope from `store.Open`):

```go
ps, err := pubsub.NewPostgresPubSub(ctx, pubsub.PostgresOptions{
	DB:  db,        // pooled *sql.DB for pg_notify
	DSN: dbPath,    // for the dedicated pgx LISTEN connection
})
if err != nil {
	return fmt.Errorf("failed to initialize pubsub: %w", err)
}
defer ps.Close()
```

`ps` is passed to `notifyserver` as the `Subscriber` and to `server.Options` /
`githubserver` / `atlassianserver` as the `Publisher`, exactly as
`NewLocalPubSub()` is today.

### `fly.toml`

Once cross-process delivery works, the single-machine pin is removed:
`min_machines_running` can go above 1 and `auto_stop_machines`/rolling deploys
become safe. The blocking comment is deleted. (Flipping the machine count is an
ops change, not part of the code PRs — but the constraint it encodes is what
this proposal exists to lift.)

## Implementation Plan

1. **`PostgresPubSub` implementation** — Delivers: `internal/pubsub/postgres.go`
   with `NewPostgresPubSub`, `Publish` (via `pg_notify` on the pool), a
   dedicated `pgx` LISTEN connection feeding the embedded `LocalPubSub`,
   reconnect-with-backoff, and `Close`. `Subscribe` delegates to `LocalPubSub`.
   Depends on: nothing (reuses existing `LocalPubSub` and interfaces).
   Verifiable by: a unit test against the test Postgres — publish through one
   `PostgresPubSub`, assert a subscriber on a **second** `PostgresPubSub`
   instance (separate connections, same DB) receives it. This is the real
   cross-process behavior the in-memory bus can't do.

2. **Wire into the server** — Delivers: swap `NewLocalPubSub()` for
   `NewPostgresPubSub(...)` in `internal/command/server.go`, threading the
   existing `*sql.DB` and DSN, with `Close` on shutdown. Depends on: (1).
   Verifiable by: server starts; SSE `/events` still streams `ready` + `change`
   events end to end (runner wakes, web UI updates) against a single machine —
   no behavior regression.

3. **Lift the single-machine constraint** — Delivers: remove the "must stay at
   1" comment and raise `min_machines_running` in `fly.toml`. Depends on: (2).
   Verifiable by: deploy ≥2 machines; a mutation served by machine A wakes a
   runner / web UI SSE client connected to machine B.

`LocalPubSub` is retained (not deleted) — it remains the local-delivery core
inside `PostgresPubSub` and is useful for tests and any single-process context.

## Trade-offs

- **Round-trip latency.** Delivery now costs a Postgres `NOTIFY` + `LISTEN`
  round-trip (sub-millisecond on a local DB) instead of an in-memory channel
  send. Irrelevant for these human-facing, low-frequency notifications.

- **8000-byte payload limit.** Postgres caps a `NOTIFY` payload at 8000 bytes.
  `model.Notification` is small (a type string, a short resources slice, a few
  IDs/strings), so it fits comfortably today. `ChannelMessage` is the only
  free-form field; if it ever risked exceeding the limit, the fallback is
  store-and-forward: write the notification to a table and `NOTIFY` only its id,
  then `SELECT` the row on receipt. Not needed now; noted so the limit isn't a
  surprise later.

- **New failure mode: the listen connection.** A dropped dedicated connection
  means missed notifications until reconnect. Mitigated by backoff + re-`LISTEN`,
  and bounded by the fact that all consumers already reconcile via polling. The
  in-memory bus never had this failure mode, but it also couldn't scale past one
  machine — this is the cost of that capability.

- **Alternatives considered.**
  - *External broker (Redis/NATS pub/sub).* Solves the same problem but adds a
    new piece of infrastructure to run, secure, and pay for. Postgres is already
    a hard dependency; LISTEN/NOTIFY reuses it.
  - *Per-org Postgres channels (`LISTEN xagent_notify_<orgID>`).* Avoids
    decoding notifications for orgs with no local subscribers, but requires
    dynamic `LISTEN`/`UNLISTEN` as subscribers come and go, plus channel-name
    sanitization. At current volume the single-channel-plus-in-process-fan-out
    design is simpler and the wasted decode is negligible. Can be revisited if
    org count and notification volume grow.
  - *Keep in-memory, add sticky routing / broadcast between machines.* Rebuilds
    a message bus by hand; LISTEN/NOTIFY already is one.

## Open Questions

- **Machine count target.** What should `min_machines_running` become — 2 for
  redundancy, or is the primary goal simply enabling overlapping rolling deploys
  rather than steady-state multi-machine? (Affects only step 3.)
- **Reconnect backoff policy.** Concrete backoff bounds and whether a repeatedly
  failing listen connection should surface as an unhealthy `/health` signal.
- **Observability.** Should we add a metric/log for notify round-trip and for
  listen reconnects, to catch silent delivery gaps in a multi-machine setup?
