# Stream runner events instead of pull + SSE wake

Issue: https://github.com/gritzapp/gritz/issues/1871

## Problem

The runner replicates its tasks' event log into its SQLite database
(`proposals/draft/runner-event-replica.md`). On `master`, `taskevents.Log.Run`
does that by pulling:

1. read the stored cursor (`runnerstore.EventReplicationCursor`);
2. page `ListRunnerEvents` from it with `gritzclient.ListRunnerEvents` until
   `more` is false, storing each page with `AppendEvents`;
3. wait for `Log.Wake()` or `PollInterval` (30s), and repeat.

Two more mechanisms tell it when to pull again:

- **The SSE wake.** `gritz runner` holds a second long-lived connection to the
  server, the `/events?runner=` subscription (`gritzclient.NotificationClient`),
  whose handler calls `events.Wake()` (and `m.Wake()` for `Poll`). The server
  filters it on `Notification.Runner`, which every publish site sets from
  `Task.PendingRunner()`: "the task has a command and isn't archived". That
  answers the `ListRunnerTasks` question, not "this runner's tasks have new
  events", so an archive, an instruction added without a start, or any event
  written while the task has no command wakes nothing.
- **The runner-side poll.** `more == false` means nothing is readable *now*,
  not that the replica is caught up. The server's xid watermark
  (`e.xid < pg_snapshot_xmin(pg_current_snapshot())`) holds committed rows back
  while an older transaction is running, and releases them when it ends. That
  transaction may write no event and publish no notification, so only the poll
  picks the rows up, up to 30s later. The poll also covers a lost wake: a
  dropped SSE connection, a notification `LocalPubSub` dropped for a full
  buffer, or a write on another server instance (#1512).

Delivery latency therefore depends on which of three paths fires. The server
knows when there is something new to read (it publishes the notifications and
owns the watermark) but the decision to read again is made by the runner, on
the other side of an RPC boundary, from a wake that is filtered on the wrong
question.

## Design

Turn the pull into a server stream. The runner opens one
`FollowRunnerEvents(page_token)` call. The server sends every page after the
token, then keeps the stream open and sends each newly visible page with its
`next_page_token` as events become readable. The runner stores each page with
its token, exactly as it does now, and on reconnect resumes from the stored
token.

```
server                                                       runner
 FollowRunnerEvents handler                                    taskevents.Log
  ├─ Subscribe(org) ◀── pubsub ◀── every publish               │
  ├─ drain: store.ListRunnerEvents(token) until !More ──page──▶ AppendEvents(events, token)
  ├─ wait: notification | recheck (5s) | keepalive (15s) ─────▶ (empty page: ignored)
  └─ end: server shutdown | max age (CodeUnavailable / OK)      reconnect from stored token
```

The stream replaces the runner's use of `ListRunnerEvents`, its SSE
subscription and its poll. The page and token format, the watermark query
(`store.ListRunnerEvents`, `runnerEventSource`) and the runner's replica
(`runnerstore`, `Log.Tasks()`, `Stream.Read`) don't change: only how a page
gets from one to the other.

### Proto

```proto
service GritzService {
  // FollowRunnerEvents sends the events of the runner's tasks after
  // page_token, in log order, from transactions that can no longer commit
  // behind it, and keeps sending as new events become readable. Each
  // response is a page and the token that resumes after it. A response with
  // no events is a keep-alive; its next_page_token is unchanged. The server
  // ends the stream on shutdown (UNAVAILABLE) and after a maximum age (OK);
  // the client reconnects from the last token it stored.
  rpc FollowRunnerEvents(FollowRunnerEventsRequest) returns (stream FollowRunnerEventsResponse);

  // ListRunnerEvents is removed.
}

message FollowRunnerEventsRequest {
  string runner = 1;  // Runner ID (required)
  // Opaque; empty for the start of the log, otherwise a next_page_token the
  // stream sent.
  string page_token = 2;
  int32 page_size = 3;  // Max events per response; 0 for the server default
}

message FollowRunnerEventsResponse {
  repeated Event events = 1;
  // Resumes after the last event in this response. Pass it back unchanged.
  string next_page_token = 2;
}
```

`more` has no equivalent: the server sends the pages that are readable back to
back, and the runner has nothing to decide between them. `ListRunnerEventsRequest`
and `ListRunnerEventsResponse` are removed with the RPC; the store method of
that name stays, as the handler's page read.

### Server: the per-stream loop

`apiserver.Server.FollowRunnerEvents` reuses `ListRunnerEvents`' checks and
store call, in a loop:

```go
func (s *Server) FollowRunnerEvents(ctx context.Context, req *gritzv1.FollowRunnerEventsRequest, stream *connect.ServerStream[gritzv1.FollowRunnerEventsResponse]) error {
	caller := apiauth.MustCaller(ctx)
	// same OpTaskRead / WithTaskRunner(req.Runner) check and "runner is required" as today
	notes, unsubscribe, err := s.subscriber.Subscribe(ctx, caller.OrgID)
	defer unsubscribe()
	stream.ResponseHeader().Set("X-Accel-Buffering", "no")
	token := req.PageToken
	for {
		// drain: ListRunnerEvents(token) until !More; send each non-empty page,
		// token = page.NextToken
		// wait for one of:
		//   <-notes          drain any further queued notifications, then loop
		//   recheck timer    loop
		//   keepalive ticker send {next_page_token: token}
		//   max age          return nil
		//   <-s.closing      return connect.NewError(connect.CodeUnavailable, ...)
		//   <-ctx.Done()     return ctx.Err()
	}
}
```

- **Auth and scoping** are `ListRunnerEvents`' unchanged: the caller must hold
  `OpTaskRead` for `WithTaskRunner(req.Runner)`, and every read is scoped to
  `caller.OrgID` and `req.Runner`. They are checked once, when the stream
  opens. A token still names a position in that runner's log only, so a forged
  one can only skip or repeat the runner's own events. A bad token or page size
  ends the stream with `InvalidArgument`, as the unary call does.
- **Subscribe before the first read.** A notification published between a
  drain and the wait is then already queued, so the next wait returns at once
  instead of missing it. The loop doesn't miss rows either way: every drain
  reads from the token, whatever woke it.
- **Every org notification wakes the stream.** A drain is one indexed query
  that usually returns an empty page, and an org has a handful of runners, so
  the loop doesn't try to decide which notifications concern it. That removes
  the runner filter: `Notification.Runner`, `Task.PendingRunner()` (12 call
  sites) and `/events?runner=` have no other user and are deleted. A burst of
  notifications costs one drain: the loop empties `notes` before draining.
  `LocalPubSub` dropping a notification for a full buffer is harmless for the
  same reason.
- **The recheck timer** covers rows the watermark held back. A notification is
  published after its transaction commits, but its rows may still sit above
  `pg_snapshot_xmin` because an older, unrelated transaction is running; when
  that one ends, nothing is published. Every `recheckInterval` (5s) without a
  wake the loop drains anyway. The timer resets after every drain, so an
  active stream doesn't add reads. It is also what bounds delivery when a
  notification never reaches this server instance (below).
- **Keepalives.** Every 15s with nothing else sent, the loop sends a response
  with no events and the current token. It carries no new information, so the
  runner needs no message type for it and can't mistake it for progress.
- **Proxy buffering.** Connect flushes each message, but nginx buffers
  responses by default. The handler sets `X-Accel-Buffering: no`, as
  `sse.NewServerWriter` does for `/events`. The stream needs no HTTP/2:
  Connect server streams work over HTTP/1.1, so it goes through the same
  proxy path as the unary API.

**Interceptors must wrap streaming handlers.** `FollowRunnerEvents` is the
first streaming RPC on `GritzService`. `apiauth.RequireUserInterceptor` and
`apiauth.ObservabilityInterceptor` are `connect.UnaryInterceptorFunc`s, which
pass streaming handlers through unchecked: an unauthenticated stream would
reach `MustCaller` and panic. Both become full `connect.Interceptor`s whose
`WrapStreamingHandler` does what the unary wrapper does (the task-id
attribute doesn't apply: the request is not available before the handler
receives it). `otelconnect` already handles streams.

**Server shutdown.** `http.Server.Shutdown` waits for active handlers to
return but doesn't cancel their contexts, so an open stream would hold
shutdown for its full 30s timeout and then be cut mid-response.
`apiserver.Server.Shutdown()` closes a `closing` channel, and `gritz server`
registers it with `httpServer.RegisterOnShutdown`. Every follower returns
`CodeUnavailable` at once, and the runner reconnects, to the new instance
during a deploy. (`/events` has the same problem today; the same hook could end
SSE streams, but that is out of scope here.)

**Maximum stream age.** A stream is authenticated when it opens and then lives
indefinitely. The handler ends it cleanly (`return nil`) after `maxStreamAge`
(15 min, plus up to 10% jitter so a fleet of runners doesn't reconnect in
step). That bounds how long a deleted API key keeps reading, and spreads
streams across server instances after one of them restarts. The runner
reconnects at once after a clean end.

### Runner: `taskevents.Log` follows the stream

`Log.Run` keeps its first step (report every stored unarchived task) and
replaces the sync-then-wait loop:

1. Read the cursor with `EventReplicationCursor`.
2. Open `FollowRunnerEvents(runner, page_token: cursor)`.
3. For each response with events, `AppendEvents(events, next_page_token)`,
   wake blocked `Read`s, and report the returned `TaskChange`s on `Tasks()`
   — the body of today's `sync`, per response instead of per page fetched.
   Responses without events are keep-alives and are dropped.
4. When the stream ends, go to 1. The stored cursor is the resume point, so a
   reconnect needs no state beyond what step 3 committed.

A store error ends `Run`, as now. A stream that ends cleanly (max age)
reconnects at once. Any other end is logged and retried with exponential
backoff from 1s to 30s with jitter, reset once a stream delivers a response,
so a server restart costs about a second and an outage doesn't produce a
reconnect storm.

**Idle watchdog.** A half-open connection would block `Receive` forever. `Log`
cancels a stream that receives nothing for `idleTimeout` (45s, three server
keep-alives) and reconnects, the same rule as `driverclient`'s
`DefaultIdleTimeout` for the driver's `Run` stream.

**The stream needs a client without a timeout.** `gritzclient.New` sets
`http.Client.Timeout` (30s by default), which would cut every stream at 30s.
A negative `Options.Timeout` disables it, and `gritz runner` builds a second
client that way for `taskevents`; unary calls keep their timeout.
`RetryInterceptor` already passes streams through, so retry stays the
reconnect loop's job.

**What goes away on the runner.** `Log.Wake`, `Options.PollInterval`,
`DefaultPollInterval` and `gritzclient.ListRunnerEvents`. Once slice 6 of the
replica plan has removed `Poll`, the SSE subscription's only remaining job is
`events.Wake()`, so `gritz runner` no longer constructs a `NotificationClient`
and keeps one connection to the server for events. `NotificationClient` stays
for `gritz notify` and `gritz mcp`.

### Operational points

- **Proxy idle timeouts.** nginx closes a proxied response that is silent for
  `proxy_read_timeout` (60s by default); Cloudflare at 100s. The 15s
  keep-alive is the same interval `/events` uses for the same reason, and it
  also feeds the runner's 45s watchdog.
- **Multiple server instances (#1512).** The stream holds no server-side
  state beyond its token, so it can reconnect to any instance. The pubsub is
  still in-process: a write on instance A publishes nothing to a stream on
  instance B. Correctness doesn't depend on notifications, only latency: such
  a stream sees the rows at its next recheck, within 5s, instead of up to 30s
  today. When #1512 makes `pubsub.Subscriber` cross-process, the handler gets
  instant wakes across instances with no change.
- **Load.** Per connected runner: one goroutine, one pubsub subscription, a
  drain per org notification and one per 5s of quiet. Today it is one SSE
  goroutine and a `ListRunnerEvents` call per relevant notification and per
  30s. The extra reads are each an empty-range scan on `idx_events_xid_id`
  (but see the open question on idle cursors).
- **Watermark stalls.** A long transaction anywhere holds back every runner,
  as today. The stream doesn't make that worse; it notices the release within
  `recheckInterval` instead of `PollInterval`.

### API

```go
package apiserver // github.com/gritzapp/gritz/internal/server/apiserver

// Options gains the subscriber the follow streams wake from. // changed
type Options struct {
	// ... existing fields
	Subscriber pubsub.Subscriber // new
}

// FollowRunnerEvents streams the runner's events after the request's token. // new
func (s *Server) FollowRunnerEvents(ctx context.Context, req *gritzv1.FollowRunnerEventsRequest, stream *connect.ServerStream[gritzv1.FollowRunnerEventsResponse]) error

// Shutdown ends every open follow stream with CodeUnavailable. // new
func (s *Server) Shutdown()

// ListRunnerEvents is removed. // removed
```

```go
package server // github.com/gritzapp/gritz/internal/server

// Options gains the subscriber passed to the API server. // changed
type Options struct {
	// ... existing fields
	Subscriber pubsub.Subscriber // new
}

// Shutdown ends the API server's open streams. // new
func (s *Server) Shutdown()
```

```go
package apiauth // github.com/gritzapp/gritz/internal/auth/apiauth

// RequireUserInterceptor fails unary calls and streams with no caller. // changed: returns connect.Interceptor
func RequireUserInterceptor() connect.Interceptor

// ObservabilityInterceptor tags unary calls and streams with the caller's org. // changed: returns connect.Interceptor
func ObservabilityInterceptor() connect.Interceptor
```

```go
package model // github.com/gritzapp/gritz/internal/model

// Notification.Runner is removed. // removed
// Task.PendingRunner is removed. // removed
```

```go
package gritzclient // github.com/gritzapp/gritz/internal/gritzclient

// Options.Timeout below zero disables the client timeout, for streams. // changed
type Options struct {
	// ... existing fields
	Timeout time.Duration
}

// ListRunnerEvents is removed. // removed
```

```go
package taskevents // github.com/gritzapp/gritz/internal/runner/taskevents

// Options configures a Log. // changed
type Options struct {
	Store    *runnerstore.Store
	Client   gritzclient.Client // built with no timeout; used for FollowRunnerEvents
	RunnerID string
	Log      *slog.Logger
	// PollInterval is removed. // removed
}

// DefaultPollInterval is removed. // removed

// Run reports every stored unarchived task, then follows the server's stream
// from the stored cursor, reconnecting until ctx ends or the store fails. // changed
func (l *Log) Run(ctx context.Context) error

// Wake is removed. // removed
```

```go
package testrunner // github.com/gritzapp/gritz/internal/runner/testrunner

// FollowRunnerEvents serves the log from the request's offset, then blocks
// and sends each Add as it happens. It is the server side of the stream. // new
func (s *Server) FollowRunnerEvents(ctx context.Context, req *gritzv1.FollowRunnerEventsRequest, stream *connect.ServerStream[gritzv1.FollowRunnerEventsResponse]) error

// Client returns a client connected to s over an httptest server, closed with
// t's cleanup. // changed: was a ClientMock wrapping ListRunnerEvents
func (s *Server) Client(t testing.TB) gritzclient.Client

// ListRunnerEvents is removed. // removed
```

`connect.ServerStreamForClient` has no exported constructor, so a
`ClientMock` can't fake a stream: the fake implements the handler side and is
served through `gritzv1connect.NewGritzServiceHandler` (embedding
`UnimplementedGritzServiceHandler`), which also exercises the real wire path.

**How it fits:** `gritz server` passes its `LocalPubSub` as both publisher and
subscriber, and registers `server.Shutdown` with `httpServer.RegisterOnShutdown`.
`FollowRunnerEvents` reads pages with the unchanged `store.ListRunnerEvents`.
`gritz runner` builds a no-timeout `gritzclient` for `taskevents.New` and no
longer starts a `NotificationClient`; `Log.Run` stores each streamed page with
`runnerstore.AppendEvents` and feeds `Tasks()` and `Stream.Read` as before.

## Implementation Plan

1. **Streaming-aware auth interceptors** — Delivers: `RequireUserInterceptor`
   and `ObservabilityInterceptor` as `connect.Interceptor`s that also wrap
   streaming handlers. Depends on: nothing. Verifiable by: interceptor tests
   calling `WrapStreamingHandler` with and without a caller in the context.
2. **`FollowRunnerEvents` on the server** — Delivers: the proto, the handler
   (subscribe, drain, recheck timer, keep-alive, max age,
   `X-Accel-Buffering`), `apiserver.Options.Subscriber`, `Server.Shutdown` and
   its `RegisterOnShutdown` wiring in `gritz server`. `ListRunnerEvents` stays
   for now; nothing calls the stream yet. Depends on: (1). Verifiable by:
   apiserver tests: the backlog arrives in pages with resumable tokens; an event
   committed with a notification arrives without waiting for the timer; a row
   held behind an open transaction arrives within the recheck interval after it
   ends, with no notification; a quiet stream receives keep-alives with an
   unchanged token; `Shutdown` ends the stream with `Unavailable`; another
   runner's scope is denied; an unauthenticated stream is rejected.
3. **`taskevents` follows the stream** — Delivers: `Log.Run` over
   `FollowRunnerEvents` with backoff and the idle watchdog; `Wake`,
   `PollInterval` and `DefaultPollInterval` removed; `gritzclient.Options.Timeout`
   below zero; `gritz runner` building the no-timeout client (its SSE handler
   keeps calling `m.Wake()` only); `testrunner.Server` serving the stream over httptest.
   Depends on: (2). Verifiable by: `taskevents` tests against the fake: the
   replica matches the server's log; a dropped stream resumes from the stored
   token without duplicates; a stream that goes silent is cancelled and
   reopened; events added while connected arrive with no wake.
4. **Remove `ListRunnerEvents`** — Delivers: the RPC, its messages,
   `gritzclient.ListRunnerEvents` and the fake's unary method removed;
   regenerated `internal/proto` and `webui/src/gen`. Depends on: (3).
   Verifiable by: build and the existing store tests, which keep covering
   `store.ListRunnerEvents`.
5. **Drop the runner's SSE subscription** — Delivers: `gritz runner` without a
   `NotificationClient`; `/events?runner=`, `Notification.Runner` and
   `Task.PendingRunner` removed with their call sites. Depends on: (3), and
   slice 6 of `runner-event-replica.md` (removes `Poll`, the last reason for
   `m.Wake()` from a notification). Verifiable by: notifyserver and runner
   tests; a task started from the web UI reaches the runner with no SSE
   connection open.

## Trade-offs

**Against keeping pull plus wake.**

- *For the stream:* one connection instead of an RPC loop plus an SSE
  subscription; the server, which publishes notifications and owns the
  watermark, decides when to read again, so the runner has no wake, no poll
  interval and no filter to keep correct; held-back rows and missed wakes
  arrive within 5s instead of 30s, without the runner making a call per check;
  the misleading `PendingRunner` filter goes away instead of being fixed.
- *Against it:* a goroutine, a subscription and periodic reads per connected
  runner on the server; the first streaming RPC on `GritzService`, so
  interceptors, shutdown and proxy buffering need care that unary calls didn't;
  a second `gritzclient` without a timeout; reconnect and watchdog logic in
  the runner in place of a loop that simply called again. Streams are also
  harder to poke at with `curl` than a unary call.

**Long-poll `ListRunnerEvents`.** Add a `wait` to the unary call: when the page
would be empty, the server holds the request until a notification or the
recheck, up to ~25s, then answers. It keeps the unary path (interceptors,
retry, no keep-alives below the proxy timeout) and the same server-side wake.
Rejected: each wait re-authenticates and re-dials a request, a backlog still
costs a round trip per page, and the runner keeps a "call again" loop with its
own timing; shutdown still has to release held requests. The stream gives the
same latency with one request per connection.

**Bidirectional stream with acks.** The runner could ack each stored page.
Rejected: the token is already the ack, held by the runner; the server stays
stateless and needs no HTTP/2.

**Postgres `LISTEN` per stream.** Each handler could `LISTEN` on a channel the
event insert `NOTIFY`s, which would also solve multiple instances. Rejected
here: it costs a dedicated connection per runner, and #1512 is the place to
make pubsub cross-process for every subscriber at once.

**A "held rows" probe instead of a fixed recheck.** The loop could ask whether
any of the runner's committed rows sit above the watermark, and recheck at 1s
only then, sleeping until a notification otherwise. Rejected for now: a second
query to maintain, and the fixed timer also covers notifications lost to
#1512. It can replace the timer later without a runner change.

**Narrower wake filter.** Make `Notification.Runner` "the task's runner" at
every publish site and wake only on those. Rejected: it must be right at every
publish site, and a missed one costs a recheck interval, to save a cheap
empty read per notification.

## Open Questions

- **Idle cursors scan other runners' rows.** An empty page echoes the
  request's token, so a runner whose tasks are quiet keeps a cursor far behind
  the watermark, and each drain scans every event written since, by any
  runner, before the `tasks.runner` join discards them. Today that happens
  every 30s; the stream makes it every notification and every 5s. The token
  could advance to the watermark on an empty page (every row below
  `pg_snapshot_xmin` is final, so nothing can appear behind it), which bounds
  each scan to recent rows. That is a change to `runnerEventSource` and the
  stream-page contract and would be its own slice.
- **Values.** 5s recheck, 15s keep-alive, 45s watchdog, 15 min max age are
  first guesses; the recheck in particular trades read load for latency of
  watermark-held rows.
