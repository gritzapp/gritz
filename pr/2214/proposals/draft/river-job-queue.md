# Adopt River as the Server's Job Queue

Issue: https://github.com/gritzapp/gritz/issues/2213

## Problem

We are planning outbound webhooks: notify an external system (for example a Mastra
workflow) when a task changes state. That needs four things the server doesn't have:

- **Atomic enqueue.** The delivery must be recorded in the same transaction as the task
  state change (an outbox), so a crash between commit and send can't lose it and a
  rolled-back change can't send it.
- **Durable retries.** The receiver will be down sometimes. Deliveries must retry with
  backoff for hours, survive server restarts and deploys, and eventually give up visibly.
- **Tenant isolation.** gritz is multi-tenant. One org's dead or slow endpoint must not
  delay deliveries to other orgs.
- **Visibility.** An operator needs to see what is queued, retrying and dead.

Today the server's post-commit work is either in-process and best-effort (`LocalPubSub`
drops a notification when a subscriber's buffer is full, and GitHub reactions are a
fire-and-forget goroutine with no retry), or a hand-rolled ticker loop (`archiver`,
`scheduler`). Each loop reinvents claiming, batching and error handling, and none has
retries, dead-lettering or a UI.

This proposal evaluates [River](https://github.com/riverqueue/river), a Postgres-backed
job queue for Go, as the foundation. It covers four things: whether River fits our
database stack, an inventory of today's background work with River's fit for each, a
design sketch for outbound webhooks on River, and the operational concerns. It ends with
a recommendation. It is an investigation: nothing here is implemented.

## Design

### Recommendation

**Adopt River (the open-source edition) on the server, scoped to new durable outbound
work, with outbound webhooks as the first consumer.**

- Don't migrate the scheduler, the archiver or the pub/sub to River: they are correct
  today, and River doesn't make them better (see [Inventory](#inventory)).
- After webhooks, move GitHub reactions onto River. It's small and fixes a real
  reliability gap.
- After that, consider asynchronous webhook ingestion, which adds retries and delivery
  dedup to the event router.
- Start without River Pro. Pro's partitioned concurrency limits are the cleanest
  per-tenant fairness answer. Buy Pro if the open-source mitigations below prove
  insufficient.

### Database fit

| Requirement | gritz today | Verdict |
| --- | --- | --- |
| Postgres | Server state is Postgres only (`internal/store`). SQLite is used only by the runner (`internal/runner/runnerstore`). | ✅ |
| pgx | `github.com/jackc/pgx/v5`, but through `database/sql`. `store.Open` calls `otelsql.Open("pgx", dsn)` on the pgx stdlib driver, and `store.WithTx` hands out a `*sql.Tx`. There is no `pgxpool`. | ✅ with `riverdatabasesql`, see below |
| Migrations | dbmate, with embedded `internal/store/sql/migrations/*.sql`, run by `store.Migrate` at every server start. | ✅ River's SQL can be exported into a dbmate migration |
| Go version | `go 1.27` | ✅ |

**No blockers.** One driver decision matters:

- **`riverdatabasesql` (recommended).** The store's transactions are `*sql.Tx`, and
  `InsertTx` must receive the driver's transaction type. `riverdatabasesql` takes a
  `*sql.DB` and lets us call `client.InsertTx(ctx, tx, args, nil)` with the exact `tx` from
  `store.WithTx`. That is what gives the outbox guarantee. It can both insert and work
  jobs.
  - Because `database/sql` can't `LISTEN`, the client runs in poll-only mode: idle jobs
    are found within the fetch poll interval (about one second by default). That's fine
    for webhooks.
  - If we want instant pickup later, `riverdatabasesql.NewWithPgxListener` adds a small
    `pgxpool` used only for `LISTEN`.
- **`riverpgxv5`.** This needs `pgx.Tx`, which means moving the whole store from
  `database/sql` to `pgxpool`: sqlc can generate for `pgx/v5`, but every `*sql.Tx`
  signature, `teststore` and `otelsql` would change. That costs far more than River saves,
  and nothing requires it.

Things to handle when wiring it up (none are blockers):

- **Separate untraced pool for River's polling.** River polls every second and runs
  maintenance queries. On the store's `otelsql` pool, each of those would be a span. Give
  the River client its own `*sql.DB`, opened with plain `sql.Open("pgx", dsn)`, for its
  own queries. `InsertTx` still runs on the store's traced `*sql.Tx`, because `otelsql`'s
  wrapper is still a `*sql.Tx`.
- **Connection limits.** The server never calls `SetMaxOpenConns`, so its pool is
  unbounded. River adds a few steady connections per process (producer fetches, leader
  election, maintenance). Cap the River pool explicitly, for example at 10, and cap the
  store pool while we're there.

### Inventory

Every place that does background, asynchronous, deferred, retried, scheduled or periodic
work today. River runs only where Postgres is, so runner, driver and CLI work is listed
for completeness but is out of scope.

#### Server

| Work | Where | Today | Would River help? | Effort to migrate |
| --- | --- | --- | --- | --- |
| **Recurring schedules** | `internal/server/scheduler` | Ticker (`--schedule-poll`, 10s). Claims due rows with `FOR UPDATE SKIP LOCKED` (`ClaimDueSchedules`), creates the task and advances `next_run_at` in one transaction. Safe across replicas. | **No.** Schedules are user data with per-row cron and timezone. River's open-source periodic jobs are held in memory by the leader and can skip an occurrence across a leader change. Durable periodic jobs are a Pro feature, and even those are code-defined, not per-row. The current design is already transactional and exactly-once per occurrence. | M, no gain. Keep. |
| **Auto-archive** | `internal/server/archiver` | Ticker (`--archive-poll`, off by default). Unlocked scan of `ListTasksDueForArchive`, then a per-task transaction with `GetTaskForUpdate` and a version re-check. Safe across replicas, but every replica repeats the scan. | **Marginal.** It could become a River periodic job (leader-only, so no duplicate scans, and visible in the UI), or a scheduled `ArchiveTask` job per task. The per-task version is a poor fit: `archive_after` moves on every restart, and the indexed scan heals itself. | S as a periodic job. Optional, only once we run multiple replicas. |
| **Change notifications (SSE)** | `internal/server/pubsub` (`LocalPubSub`), `notifyserver` | In-process fan-out per org. A full buffer drops the notification. Notifications **don't cross replicas**. | **No.** This is pub/sub, not a job queue. Multi-replica needs Postgres `LISTEN`/`NOTIFY` or a broker. River's notifier is internal. | n/a |
| **GitHub reactions** | `githubserver.go`, `OnRouteOutcome` → `react` | Fire-and-forget goroutine on a detached 10s context. **No retry.** | **Yes.** A `GitHubReactionArgs` job inserted in the routing transaction gets retries with backoff and is unique by `(org, url, reaction)`. | S. **Second migration.** |
| **Webhook ingestion and routing** | `githubserver/webhook.go`, `atlassianserver/webhook.go`, `customserver/webhook.go` → `eventrouter.Route` | Synchronous, inside the HTTP request. A failed attach or create is logged and skipped, and there is no dedup on `X-GitHub-Delivery`. | **Yes, later.** Persist the raw delivery and enqueue a `RouteEventArgs` job, unique by delivery ID, then return 202. That adds dedup, retries for failed attaches and a fast webhook response. It changes error semantics for senders, so it needs its own design. | M. Third candidate. |
| **Debug shell rendezvous** | `shellserver`, `shellrelay` | In-memory registry, `time.AfterFunc` timeouts, best-effort `ClearShellSession`. | **No.** These are live connections pinned to one process. | n/a |
| **OAuth outbound calls** | `atlassian.FetchMe`, the GitHub OAuth exchange, OIDC refresh | Synchronous, in the request path. | **No.** The user is waiting for the response. | n/a |
| **GitHub installation-token cache** | `githubx.AppTokenCache` | LRU of `ghinstallation` transports, refreshed lazily. | **No.** | n/a |
| **Retention / cleanup** | none | Nothing prunes `events`, `log_chunks` or archived tasks. Rows go only when a task or org is deleted (by cascade). | **Yes, if we add retention.** A leader-run periodic job with bounded deletes per run is the natural shape. | S, when needed. New work, not a migration. |
| **Outbound webhooks** | none | n/a | **Yes.** The primary motivation, below. | New. |

#### Runner, driver and CLI (no Postgres, so River doesn't apply)

| Work | Where | Notes |
| --- | --- | --- |
| Runner event log poll | `runner/taskevents` | `ListRunnerEvents` cursor poll (`--poll`, 30s) plus SSE wakes. Server-side reads are replica-safe (xid cursor). |
| Runner event outbox | `runner/eventoutbox.go` | Durable SQLite outbox to `SubmitRunnerEvents`, with head-of-line retry and a dead letter. Already does what River would do, on the right storage. |
| SSE subscriptions | `gritzclient/notificationclient.go` (runner, `gritz mcp`, `gritz notify`) | Reconnect every 5s. |
| Sandbox lifecycle retries | `runner/taskrunner.go` (`next`, `ready`, `attach`, `acquire`), `driverclient` watchdog | In-process orchestration with backoff, where latency matters. |
| Lambda MicroVM polling and token cache | `backend/lambdamicrovm` | Poll `GetMicrovm` every 1s for up to 5m. Token re-minted lazily. |
| Driver keepalive, log shipping, run loop | `driverserver`, `logship` | Inside the sandbox. |
| CLI log follow, GitHub MCP token rotation | `gritzclient/logs.go`, `githubmcp` | Client-side timers. |
| RPC retries | `gritzclient.RetryInterceptor` | Retries unary calls on `Unavailable` for every client. |

Separately, `internal/x/outbox` (the generic file-backed outbox from
[Generic Durable Outbox](/proposals/implemented/generic-durable-outbox/)) has no importers:
the runner's SQLite `eventoutbox` replaced it. It should be deleted whether or not River
is adopted. Its "Postgres `Store` for server-side consumers" follow-up is what River
would replace.

### Outbound webhooks on River

A sketch, detailed enough to judge fit. The webhook feature itself deserves its own
proposal.

#### Endpoints

```sql
CREATE TABLE webhook_endpoints (
    id                   bigserial PRIMARY KEY,
    org_id               bigint NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    url                  text NOT NULL,
    secret               text NOT NULL,          -- "whsec_" + base64(32 random bytes)
    event_types          text[] NOT NULL,        -- {"task.completed","task.failed",...}
    disabled_at          timestamptz,
    disabled_reason      text,
    consecutive_failures int NOT NULL DEFAULT 0,
    failing_since        timestamptz,
    circuit_open_until   timestamptz,
    probe_until          timestamptz,            -- one half-open probe at a time
    created_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_webhook_endpoints_org ON webhook_endpoints (org_id) WHERE disabled_at IS NULL;
```

The endpoints are managed through new `CreateWebhookEndpoint`, `ListWebhookEndpoints`,
`DeleteWebhookEndpoint` and `RotateWebhookSecret` RPCs, gated by `OpOrgWrite`/`OpOrgRead`,
with matching Web UI and CLI support.

#### Where the job is enqueued

Each task state change that a webhook reports already happens inside a
`store.WithTx` closure that writes a lifecycle event. The webhook enqueue goes right next
to that `CreateEvent`, on the same `tx`:

| Webhook type | Site |
| --- | --- |
| `task.completed`, `task.failed`, `task.cancelled` | `apiserver.SubmitRunnerEvents` (`runner.go`), in the existing `switch task.Status` on terminal statuses |
| `task.archived` | `apiserver.ArchiveTask` (`task.go`) and `archiver.archive` (auto-archive, with `"auto": true` in the payload) |
| `task.cancelled` (pending task) | `apiserver.CancelTask`, when `Task.Cancel` moves a pending task straight to cancelled (running tasks go through `SubmitRunnerEvents`) |
| `task.waiting_for_input` | **No such state exists today**, see [Open Questions](#open-questions) |

Fan-out happens inline, in the transaction: `webhook.EnqueueTx` loads the org's enabled
endpoints subscribed to the type (one indexed query, few rows per org) and calls
`client.InsertManyTx` with one `DeliveryArgs` per endpoint. A rolled-back state change
inserts nothing, and a committed one always has its deliveries. A separate fan-out job
would add a hop without adding safety.

The payload is snapshotted at enqueue time, so every retry sends the identical body:

```json
{
  "type": "task.completed",
  "timestamp": "2026-11-18T00:20:00Z",
  "data": {
    "task_id": 2481, "org_id": 12, "status": "completed", "from_status": "running",
    "name": "...", "workspace": "gritz", "namespace": "", "version": 3,
    "url": "https://gritz.dev/ui/tasks/2481?org=12"
  }
}
```

Keep the payload thin: the receiver calls the API for anything more. The message ID is
derived from the lifecycle event's ID (`msg_<event id>`), so it's stable across retries
and lets the receiver deduplicate.

#### Worker

```go
type DeliveryArgs struct {
	EndpointID int64           `json:"endpoint_id" river:"unique"`
	MessageID  string          `json:"message_id" river:"unique"`
	OrgID      int64           `json:"org_id"`
	Type       string          `json:"type"`
	Body       json.RawMessage `json:"body"`
}

func (DeliveryArgs) Kind() string { return "webhook_delivery" }

func (DeliveryArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       "webhooks",
		MaxAttempts: 8,
		UniqueOpts:  river.UniqueOpts{ByArgs: true}, // (endpoint, message) inserted once
	}
}
```

`DeliveryWorker.Work` does the following:

1. Load the endpoint.
   - Deleted or disabled: return `river.JobCancel(err)`.
   - Circuit open: return `river.JobSnooze(until + jitter)`. A snooze doesn't consume an
     attempt.
2. Sign and `POST` the body (see Signing below), using a hardened client (see HTTP
   below).
3. Classify the response:
   - 2xx: success. Reset `consecutive_failures` and close the circuit.
   - `410 Gone`: disable the endpoint and `JobCancel`.
   - `429` or `503` with `Retry-After`: `JobSnooze(retryAfter)`.
   - Any other non-2xx, a timeout or a connection error: return an error so River
     retries. Bump `consecutive_failures`, and open the circuit past a threshold.
4. Disable the endpoint (and surface it in the UI) once it has been failing continuously
   for 5 days, as Svix does.

`Timeout()` returns 15s. `NextRetry` returns a fixed schedule instead of River's default
(25 attempts at `attempt^4` seconds, about three weeks):
5s, 5m, 30m, 2h, 5h, 10h, 10h, giving up after about 28 hours. After the last attempt the
job is `discarded` and stays visible in River UI for the retention period.

**Signing ([Standard Webhooks](https://www.standardwebhooks.com/)):**

- Headers:
  - `webhook-id: msg_<event id>`
  - `webhook-timestamp: <unix seconds at send time>`
  - `webhook-signature: v1,<base64(HMAC-SHA256(secret, id + "." + timestamp + "." + body))>`
- The secret is stored in `whsec_<base64>` form. `RotateWebhookSecret` keeps the previous
  secret for 24h and sends both signatures, space-separated, which the spec allows.
- Use the reference `standard-webhooks` Go library, or the roughly 20 lines it amounts to.

**HTTP:**

- Dial, TLS and header timeouts. No redirects.
- A response-body read cap.
- An SSRF guard: a `net.Dialer.Control` that rejects loopback, private, link-local and
  metadata addresses after DNS resolution. This is required in multi-tenant mode, where
  the URL is tenant input.

#### Per-tenant fairness

River's open-source fetch order within a queue is priority, then `scheduled_at`: a shared
FIFO. Two failure modes matter.

**A dead or slow endpoint.** Handled in open source:

- **Own queue.** `webhooks` has its own `MaxWorkers` (for example 50), so webhook trouble
  never starves other job kinds, and other job kinds never starve webhooks.
- **Bounded attempts.** Each attempt is capped at 15s. A refused connection fails in
  milliseconds. Retries are scheduled minutes to hours apart, so they don't sit at the
  head of the queue.
- **Per-endpoint circuit breaker.** After 5 consecutive failures, `circuit_open_until` is
  set with exponential growth, capped at 1h.
  - While the circuit is open, the endpoint's jobs snooze without sending, which costs
    one cheap row update per job.
  - A single half-open probe is claimed with
    `UPDATE webhook_endpoints SET probe_until = now() + interval '30s' WHERE id = $1 AND (probe_until IS NULL OR probe_until < now()) RETURNING id`.
    The other jobs re-snooze, so jobs snoozed to the same time don't all hit the endpoint
    together.
  - Worst case: a dead endpoint holds at most 5 workers for at most 15s before its circuit
    opens, then at most one worker per probe.
- **Priority demotion.** New deliveries to an endpoint whose circuit is open are inserted
  at `Priority: 4`, so healthy endpoints' fresh deliveries are fetched first.

**A flood from one tenant** (thousands of state changes at once) still queues FIFO ahead
of other tenants. Open source has no fair-share fetch. Today the rate of task state
changes is bounded by runner concurrency (tasks finish at agent speed), so a flood is at
most a few hundred deliveries, which 50 workers drain in seconds against healthy
endpoints. If that stops being true, **River Pro concurrency limits** are the fix:

```go
ProQueues: map[string]riverpro.QueueConfig{
	"webhooks": {
		MaxWorkers: 50,
		Concurrency: riverpro.ConcurrencyConfig{
			GlobalLimit: 4, // per partition, across all replicas
			Partition:   riverpro.PartitionConfig{ByArgs: []string{"org_id"}},
		},
	},
},
```

That caps each org at 4 in-flight deliveries cluster-wide, whatever its backlog. A
partition by `endpoint_id` would also make the circuit breaker mostly unnecessary.

**Ordering.** Deliveries aren't ordered: a retried `task.failed` can arrive after a later
`task.archived`. The `timestamp`, `version` and `from_status` fields let receivers
reconcile, and the docs should say so. Strict per-endpoint ordering would need Pro
sequences, which we don't need.

### Operational concerns

**Migrations.**

- River ships its own tables: `river_job`, `river_leader`, `river_queue`, `river_client`,
  `river_client_queue` and `river_migration`, plus their supporting types and
  functions. Run `river migrate-get --line main --all --up` (and `--down`) to export
  the SQL into a dbmate migration, `20261118000001_river.sql`, so the single migration
  path stays dbmate. `dbmate dump` then puts the tables in `schema.sql`. sqlc never
  queries them, so their presence there is harmless.
- Every River upgrade that adds a migration needs a new dbmate file, exported the same
  way. Check release notes on each bump, and consider a test that compares River's
  `rivermigrate` latest version against the applied one.
- Not running `rivermigrate` at startup avoids a second migrator.
- dbmate doesn't take an advisory lock today, so replicas that start at the same moment
  race on migrations. That's an existing problem, and it gets slightly more likely with
  bigger migrations. Fix it independently (`pg_advisory_lock` around `store.Migrate`).

**Multiple server replicas.**

- Every replica runs a `river.Client` with the same queues. Jobs are fetched with
  `FOR UPDATE SKIP LOCKED`, so each job runs on one replica at a time (at-least-once,
  which is why the receiver deduplicates on `webhook-id`).
- Leader election (`river_leader`) picks one replica for maintenance: the job cleaner, the
  stuck-job rescuer, scheduled-job promotion and periodic jobs. No extra infrastructure is
  needed.
- River itself is replica-safe. The things that block running multiple replicas are
  elsewhere: `LocalPubSub`, the shell registry, and a per-process generated
  `--auth-app-key` when it's unset.
- **Shutdown.** Start the client from the server's signal context. On `SIGTERM`, call
  `client.Stop(ctx)` with the same 30s budget as `httpServer.Shutdown`, so in-flight
  deliveries finish. A hard-killed job is rescued after `RescueStuckJobsAfter` (default
  1h). Lower it to a few minutes for the `webhooks` queue, keeping it well above the 15s
  timeout.
- **Retention.** The defaults keep completed jobs for 24h and discarded jobs for 7d.
  That's enough for operator debugging, but not a tenant-facing delivery log (see Open
  Questions).

**River UI.**

- `riverui` is open source and mounts as an `http.Handler`.
- It shows jobs across all orgs, and gritz has no operator role, so it can't sit behind
  normal org auth. Options:
  - Run `riverui` as a separate internal-only service in the infra repo (simplest).
  - Add an operator allowlist.
- It isn't tenant-facing.

**Testing.**

- **Enqueue sites.** Handler tests already run real transactions on the shared
  `TEST_DATABASE_URL`. The River tables come from the dbmate migration that the test
  database already gets.
  - `rivertest.RequireInsertedTx` matches by kind only, and test packages share one
    database, so assert with a small `teststore` helper that queries `river_job` by kind
    and `args->>'org_id'` for the test's own org. Each test already creates its own org.
  - Use an insert-only client in tests: a `riverdatabasesql` driver with no workers
    started.
- **Workers.** `rivertest.Worker` runs `DeliveryWorker.Work` against an `httptest.Server`
  inside a test transaction: signing, status classification, snooze and cancel paths, and
  circuit-breaker updates.
- **End to end.** A real client with a short `FetchPollInterval` in an `internal/e2e`
  test: complete a task and assert the endpoint received a correctly signed request.
- `riverdbtest.TestSchema` (a per-test schema, about 50ms) works for full isolation, but
  River documents it as not covered by semver. Use it only if the shared-database helper
  proves flaky.
- `riverpgxv5` test helpers don't apply, because we use `riverdatabasesql`.

**Things that require River Pro** ($125/month for up to 20 developers):

| Pro feature | Do we need it? |
| --- | --- |
| Concurrency limits partitioned by args | Not at first. It's the upgrade path for per-tenant fairness under floods. |
| Durable periodic jobs | No. The scheduler stays as is. |
| Dead letter queue | No. Discarded jobs plus endpoint disabling are enough. |
| Encrypted jobs | Maybe. Payloads hold task names, not credentials. Signing secrets live in `webhook_endpoints`, not in job args. |
| Workflows, sequences, batching, ephemeral jobs | No. |

Everything in the outbound webhook design above works on the open-source edition.

**Maturity.** River is pre-1.0, so expect occasional API changes on upgrades. Renovate
already handles bumps. Migrations ship as versioned SQL, so upgrades stay explicit.

### API

```go
package jobs // github.com/gritzapp/gritz/internal/server/jobs

// Client is the server's River client, shared by every job kind.
type Client = river.Client[*sql.Tx]

// Options configures the server's River client.
type Options struct {
	DB      *sql.DB        // untraced pool for River's own queries
	Workers *river.Workers // every registered worker
	Queues  map[string]river.QueueConfig
	Log     *slog.Logger
}

// New returns a River client on the riverdatabasesql driver.
func New(opts Options) (*Client, error)
```

```go
package webhook // github.com/gritzapp/gritz/internal/server/webhook

// Event types a webhook endpoint can subscribe to.
const (
	TypeTaskCompleted = "task.completed"
	TypeTaskFailed    = "task.failed"
	TypeTaskCancelled = "task.cancelled"
	TypeTaskArchived  = "task.archived"
)

// DeliveryArgs is one signed POST of one message to one endpoint.
type DeliveryArgs struct{ ... }

// Kind names the job kind.
func (DeliveryArgs) Kind() string

// InsertOpts puts deliveries on the webhooks queue, unique per endpoint and message.
func (DeliveryArgs) InsertOpts() river.InsertOpts

// Enqueuer inserts deliveries for a task state change.
type Enqueuer struct{ ... }

// NewEnqueuer returns an Enqueuer using st to find endpoints and c to insert jobs.
func NewEnqueuer(st *store.Store, c *jobs.Client) *Enqueuer

// EnqueueTx inserts one delivery per subscribed endpoint of the task's org, in tx.
func (e *Enqueuer) EnqueueTx(ctx context.Context, tx *sql.Tx, typ string, task *model.Task, ev *model.Event) error

// DeliveryWorker signs and sends deliveries.
type DeliveryWorker struct {
	river.WorkerDefaults[DeliveryArgs]
	// ...
}

// NewDeliveryWorker returns a worker using st for endpoint state and hc for requests.
func NewDeliveryWorker(st *store.Store, hc *http.Client) *DeliveryWorker

// Work delivers one message, snoozing while the endpoint's circuit is open.
func (w *DeliveryWorker) Work(ctx context.Context, job *river.Job[DeliveryArgs]) error

// NextRetry returns the fixed delivery retry schedule.
func (w *DeliveryWorker) NextRetry(job *river.Job[DeliveryArgs]) time.Time

// Timeout caps each attempt.
func (w *DeliveryWorker) Timeout(job *river.Job[DeliveryArgs]) time.Duration

// Sign returns the Standard Webhooks signature header value for body.
func Sign(secret, msgID string, ts time.Time, body []byte) (string, error)
```

**How it fits:**

- `command/server.go` opens the untraced pool, registers `DeliveryWorker` and calls
  `jobs.New`.
- It starts the client on the signal context, next to the archiver and the scheduler,
  and stops it before `httpServer.Shutdown` returns.
- `apiserver.Server` and `archiver.Archiver` get an `*webhook.Enqueuer` and call
  `EnqueueTx` beside the lifecycle `CreateEvent` in their existing transactions.

## Implementation Plan

This plan applies if the recommendation is accepted. Each layer is a small PR.

1. **Delete `internal/x/outbox`.** Delivers: removes the unused package. Depends on:
   nothing. Verifiable by: `go build ./...` and tests.
2. **River schema migration.** Delivers: a dbmate migration exported with
   `river migrate-get`, plus an updated `schema.sql`. Depends on: nothing. Verifiable by:
   migrating up and down on the test database.
3. **River client wiring.** Delivers: the `internal/server/jobs` package, an untraced
   capped pool, start and stop in `gritz server`, a `--jobs-workers` flag (0 makes a
   replica insert-only), and pool caps on the store. No job kinds yet. Depends on: (2).
   Verifiable by: the server starts and stops cleanly and the `river_client` row appears.
4. **GitHub reactions as a job.** Delivers: `GitHubReactionArgs`, inserted in the routing
   transaction, replacing the goroutine. Depends on: (3). Verifiable by: a
   `rivertest.Worker` test against a fake GitHub client, and an eventrouter test that
   asserts the job is inserted. This exercises River in production on a low-stakes kind
   before webhooks.
5. **Webhook endpoints.** Delivers: the `webhook_endpoints` migration, store methods,
   proto RPCs with scopes, and a CLI command. Depends on: nothing. Verifiable by: store
   and handler tests.
6. **Delivery worker.** Delivers: `webhook.DeliveryWorker`, `Sign`, the hardened HTTP
   client, retries, snooze, cancel and the circuit breaker. Depends on: (3), (5).
   Verifiable by: `rivertest.Worker` tests against `httptest`, and signature tests against
   the Standard Webhooks test vectors.
7. **Enqueue at state changes.** Delivers: `webhook.Enqueuer` wired into
   `SubmitRunnerEvents`, `CancelTask`, `ArchiveTask` and the archiver. Depends on: (6).
   Verifiable by: handler tests asserting jobs in the same transaction (none on rollback),
   and an e2e test receiving a signed request.
8. **Web UI and docs.** Delivers: an endpoint management page and docs pages
   (`integrations/outbound-webhooks`, the payload reference). Depends on: (5), (7).
   Verifiable by: `pnpm lint`, `pnpm build` and a manual check against `mise run dev`.
9. **River UI for operators.** Delivers: an internal `riverui` deployment in the infra
   repo. Depends on: (3). Verifiable by: viewing the jobs.

## Trade-offs

- **A hand-rolled Postgres outbox instead of River.** This is the strongest alternative:
  a `webhook_deliveries` table with `next_attempt_at`, claimed with `SKIP LOCKED` exactly
  like `ClaimDueSchedules`.
  - **For:** no dependency, and a natural tenant-facing delivery log. Fairness is fully
    under our control, for example by claiming per-org batches round-robin.
  - **Against:** we would rebuild what River already tested. That means:
    - claiming, retry scheduling, snooze and cancel semantics;
    - rescuing jobs stuck on a dead process;
    - retention cleanup;
    - leader election for periodic work;
    - an operator UI and test helpers.
  - All of that comes back again for the second and third job kinds (reactions, ingestion,
    retention), and that reuse is what tips it.
  - The Generic Durable Outbox proposal already planned a Postgres `Store` for server-side
    consumers. River is that, finished.
- **A managed service (Svix, Hook0).** Turnkey signing, retries, a delivery portal and
  fairness. It adds an external dependency and per-message cost, and we'd still need a
  transactional outbox to hand messages to it reliably, which brings back a queue anyway.
  It's worth revisiting if tenants want a self-serve delivery portal.
- **`riverdatabasesql` versus moving the store to `pgxpool`.** Poll-only latency (about
  1s) against a store-wide rewrite. Poll-only is fine for every candidate job kind.
  `NewWithPgxListener` is a cheap upgrade if we ever need it.
- **Inline fan-out versus a fan-out job.** Inline adds one indexed query to the
  state-change transaction, but needs no extra hop, and the deliveries exist the moment
  the change commits.

## Open Questions

- **What is "waiting for input"?** There's no such task status. A task is pending,
  running, restarting, cancelling, completed, failed, cancelled or draft. Candidates:
  - a run that ends with the agent asking a question. That would need an agent-side
    signal, for example a new MCP tool or a flag on `report`;
  - a new non-terminal status;
  - for now, just `task.completed`, with the receiver reading the latest report.
  This needs a product decision before the webhook proposal.
- **Tenant-facing delivery log.** River's retention (24h completed, 7d discarded) is for
  operators. Do tenants need delivery history and manual redelivery in the UI? If so, add
  a `webhook_deliveries` table written by the worker. River stays the executor.
- **River UI access.** Should it be a separate internal service (recommended) or mounted
  in `gritz server` behind a new operator allowlist?
- **River Pro.** Should we buy it up front for partitioned concurrency limits, or start on
  the open-source mitigations and revisit when a tenant's volume warrants it?
- **Which events are subscribable?** Should `task.created`, `task.restarted` and
  `task.running` be webhook types too? They are cheap to add once the pipeline exists.