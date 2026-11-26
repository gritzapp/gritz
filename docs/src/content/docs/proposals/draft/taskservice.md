---
title: Service packages, starting with taskservice
description: Task creation and state changes are written straight to the store from the API server, the event router, the scheduler and the archiver, so the rules for changing a task are copied across a dozen call sites, and gritz server's wiring is a long hand-written function.
---

Issue: https://github.com/gritzapp/gritz/issues/2266

## Problem

A task changes in two steps: a method on `model.Task` decides the transition
(`Start`, `Cancel`, `Restart`, `Archive`, `Unarchive`, `ApplyRunnerEvent`), and
the caller writes the result. Writing it is the same sequence everywhere: open a
transaction, `GetTaskForUpdate`, authorize, clone the task, apply, `UpdateTask`,
append a `LifecyclePayload` event, append `Task.CommandEvent(from)`, commit, then
publish a `change` notification. Each caller writes that sequence itself, against
`*store.Store`:

| Site | Change |
| --- | --- |
| [`apiserver.CreateTask`](https://github.com/gritzapp/gritz/blob/master/internal/server/apiserver/task.go) | create (draft or pending), created + command + instruction events |
| `apiserver.UpdateTask` | name, auto-archive, instructions, `Start` |
| `apiserver.ArchiveTask`, `UnarchiveTask`, `CancelTask`, `RestartTask` | one transition each |
| [`apiserver.OpenShell`](https://github.com/gritzapp/gritz/blob/master/internal/server/apiserver/shell.go) | `Start` plus `ShellSession`, recorded as `restarted` |
| [`apiserver.SubmitRunnerEvents`](https://github.com/gritzapp/gritz/blob/master/internal/server/apiserver/runner.go) | `ApplyRunnerEvent`, sandbox lifecycle + command events |
| [`eventrouter.Router.create`](https://github.com/gritzapp/gritz/blob/master/internal/server/eventrouter/eventrouter.go) | create, with the trigger event, a link and the rule's prompt |
| `eventrouter.Router.attach` | external event, and `Start` if it wakes |
| [`scheduler.Fire`](https://github.com/gritzapp/gritz/blob/master/internal/server/scheduler/scheduler.go) (the scheduler's tick and `apiserver.RunSchedule`) | create, events from `model.Schedule.Events` |
| [`archiver.archive`](https://github.com/gritzapp/gritz/blob/master/internal/server/archiver/archiver.go) | auto-archive, guarded by the version it listed |
| [`server.New`'s `onShellClose`](https://github.com/gritzapp/gritz/blob/master/internal/server/server.go) | `ClearShellSession` |

The copies have already drifted. The three create paths build the same task
three ways (`apiserver` inline, the router inline, `model.Schedule.Task`) and
order their seed events by hand. Some gate rejections become `Internal`, while
`OpenShell`'s becomes `FailedPrecondition`. The router's wake writes no
lifecycle event. Every copy builds its notification's resources and channel
message differently. The pending reset handler (`ResetSandbox`, #1958) and the
task state machine (#2264) would each have to touch every site again.

Wiring has a matching problem. `gritz server`'s `Action`
([`internal/command/server.go`](https://github.com/gritzapp/gritz/blob/master/internal/command/server.go))
builds about fifteen components by hand, in dependency order, and starts the
archiver, the scheduler and the HTTP server in their own goroutines.
`server.New` builds more on the inside (the shell registry, the API server, an
event router), and three webhook servers each build their own
`eventrouter.Router`. The e2e harness
([`internal/e2e/harness/e2e.go`](https://github.com/gritzapp/gritz/blob/master/internal/e2e/harness/e2e.go))
repeats a subset of the same wiring.

## Design

### Service packages

A **service** is a package under `internal/service/` that owns the writes for one
resource: it opens the transaction, applies the domain rules, writes the rows
and events, and publishes the notifications. Transports (Connect handlers,
webhook servers) and workers (the archiver, the scheduler) call it. Below it,
`internal/store` stays a persistence layer with no rules. `model` keeps its
pure decision methods (`Task.Start`, `Task.Cancel`, ...), and the service is the
only thing that calls them on a stored task.

The service is write-side only. Reads (`GetTask`, `ListTasksPage`,
`ListRunnerEvents`) stay on the store: they have no rules to share.

The first service is `internal/service/taskservice`. Other resources follow
the same shape later (see [Follow-ups](#follow-ups)); this proposal moves tasks
only.

### Transactions

The callers don't all need the same transaction. Most handlers change one task
and nothing else. Three callers change a task beside writes of their own:

- the router's create also writes a link, and its attach writes an external
  event;
- the scheduler fires a batch of schedules and advances each one in one
  transaction;
- `RunSchedule` fires one.

So the service has one entry point, `InTx`, and every change is a method on the
`Tx` it hands out. `InTx` begins the transaction, runs the callback, commits if
the callback returns nil, and only then publishes the notifications the changes
queued. Callers no longer write `return tx.Commit()` or publish anything
themselves. A caller's own writes go through `Tx.SQL()` and the store as
before. There is one path, not a with-tx and a without-tx variant of each
method.

The same contract applies to every caller: publish after commit, so a
subscriber never sees a change that rolled back. Today only the scheduler is
careful about this; the handlers publish after `WithTx` returns, which is the
same thing, written out by hand at each site.

### Actors and authorization

The handlers authorize in two steps: a coarse `AllowOp` gate before the
transaction, then `Scopes.Allow(OpTaskWrite, task.ScopeAttr()...)` against the
row loaded `FOR UPDATE`. The coarse gate stays in the handler; it needs no row.
The instance check moves into the service, because the service is what loads the
row.

Each change takes an `Actor`: who to record on the lifecycle event, who to stamp
on the notification (`UserID`, `ClientID`), and, for a user, the scopes to check
the task against. A server-internal actor (router, schedule, archiver) carries
no scopes and is not checked. Scopes are a pointer, so an authenticated caller
with an empty scope list is denied, never mistaken for a system actor.

`RunSchedule` is the one mixed case: the created event's actor is
`model.ScheduleActor`, as today, while the notification carries the user who
asked. With an `Actor` struct, that is one literal.

### Errors

The service returns three sentinel errors (wrapped): `ErrNotFound`,
`ErrPermissionDenied` and `ErrInvalidTransition`. The handlers map them to
Connect codes with the messages and codes they use today: `NotFound`,
`PermissionDenied` ("cannot write task", or "cannot submit runner events"), and
`Internal` for a rejected transition, except `FailedPrecondition` in
`OpenShell`. Turning every `ErrInvalidTransition` into `FailedPrecondition` is a
user-visible change, so it is left out of this refactor (see
[Open Questions](#open-questions)).

### What moves where

- **Create.** `Tx.Create` takes the task's attributes and seed events and
  writes them in today's order: the row, then the trigger external event (router
  only), the `created` lifecycle event, the command event, the link and its event
  (router only), then one instruction event per instruction. Without a trigger
  or a link, that is exactly the order `CreateTask` and `Fire` write now, and
  with them it is exactly the router's. `Create` sets `Status`, `Command` and
  `Version` from `Draft`, so the three hand-built tasks become one.
  `model.Schedule.Events` is deleted. `model.Schedule.Task` stays as the source
  of the attributes. Variable resolution (`taskvar.Resolve`) stays with the
  callers that do it today: `CreateTask` resolves request variables, and
  `scheduler.Validate` resolves a schedule's variables when it is saved.
- **User transitions.** `CancelTask`, `RestartTask`, `ArchiveTask`,
  `UnarchiveTask` and `UpdateTask` become a coarse gate, one `Tx` call and error
  mapping. The notifications and channel messages they build (for example
  "Task %d restart requested.") move with them.
- **Shell.** `OpenShell` calls `Tx.OpenShell`, which keeps the terminal-status
  precondition and sets `ShellSession` in the same transaction. Seeding the
  rendezvous (`shells.Seed`) stays in the handler, after `InTx` returns.
  `onShellClose` calls `Tx.ClearShellSession`.
- **Runner events.** `SubmitRunnerEvents` keeps its per-event loop and runs one
  `InTx` per event. `Tx.ApplyRunnerEvent` does the fold, the sandbox lifecycle
  and command events, the terminal `TaskStatus`/channel message, the
  `Ignore`-on-rejection notification and the "runner event received" log line.
- **Router.** `Router.create` becomes a call to `Tx.Create` with `Trigger`,
  `Link` and the rule's prompt as its one instruction. `Router.attach` becomes
  `Tx.Attach`, which writes the external event and, when it wakes, starts the
  task. The no-wake path's notification moves with it. Routing (matching rules
  and links) stays in `eventrouter`. `Router` gets a `Tasks *taskservice.Service`
  field. Its `Store` field stays for the reads it does while routing.
- **Scheduler.** `Tick` runs its batch in one `InTx`. `ClaimDueSchedules` and
  `AdvanceSchedule` go through `tx.SQL()`. `scheduler.Fire(ctx, tx
  *taskservice.Tx, sched)` turns a schedule into `CreateParams` and returns the
  task. It no longer returns a notification, because `Create` queues it.
  `scheduler.Store` loses `CreateTask` and `CreateEvent`.
- **Archiver.** `archive` becomes `Tx.AutoArchive(ctx, ref, version)`, which
  keeps the version guard and reports whether it archived.
- **Reset.** The `ResetSandbox` handler (#1958) is written against the service
  as `Tx.Reset`. If it lands first, it moves with the user transitions.

Each `Tx` method is a thin wrapper over one private helper that does the shared
sequence: load the row `FOR UPDATE`, authorize, clone, apply, write the row,
write the lifecycle and command events, and queue the notification. That helper
is where the state machine plugs in (see below).

Log lines like "task created" and "task cancelled" move into the service along
with the writes.

### API

```go
package taskservice // github.com/gritzapp/gritz/internal/service/taskservice

// ErrNotFound reports that the task does not exist in the org.
var ErrNotFound = errors.New("task not found")

// ErrPermissionDenied reports that the actor's scopes do not allow the change.
var ErrPermissionDenied = errors.New("permission denied")

// ErrInvalidTransition reports that the task's state does not allow the change.
var ErrInvalidTransition = errors.New("invalid transition")

// Options configures a Service.
type Options struct {
	Log       *slog.Logger
	Store     *store.Store
	Publisher pubsub.Publisher
}

// Service makes every change to a task and the events that record it.
type Service struct{ ... }

// New returns a Service.
func New(opts Options) *Service

// InTx runs f in one transaction, commits it if f returns nil, then publishes the notifications its changes queued.
func (s *Service) InTx(ctx context.Context, f func(tx *Tx) error) error

// Tx is a transaction in which tasks are changed.
type Tx struct{ ... }

// SQL returns the underlying transaction, for the caller's own writes beside its task changes.
func (tx *Tx) SQL() *sql.Tx

// Actor is who makes a change.
type Actor struct {
	// Actor is recorded on the lifecycle events the change writes.
	Actor model.Actor
	// UserID and ClientID are stamped on the change's notification.
	UserID   string
	ClientID string
	// Scopes, when set, must allow the change on the task.
	Scopes *authscope.Scopes
}

// User returns the Actor for an authenticated caller.
func User(u *apiauth.UserInfo) Actor

// Ref names a task within an org.
type Ref struct {
	ID    int64
	OrgID int64
}

// CreateParams describes a task to create.
type CreateParams struct {
	OrgID       int64
	Name        string
	Runner      string
	Workspace   string
	Namespace   string
	AutoArchive time.Duration
	Variables   map[string]string
	// Draft creates the task without starting it.
	Draft bool
	// Trigger, when set, is the external event that caused the task, written first.
	Trigger *model.ExternalPayload
	// Link, when set, is linked to the task before its instructions.
	Link *model.Link
	// Instructions seed the task's stream.
	Instructions []model.InstructionPayload
	// ChannelMessage replaces the default "Task N created" agent-channel line.
	ChannelMessage string
}

// Create creates a task and seeds its stream.
func (tx *Tx) Create(ctx context.Context, a Actor, p CreateParams) (*model.Task, error)

// UpdateParams is a partial update; zero fields are left alone.
type UpdateParams struct {
	Name            string
	AutoArchive     *time.Duration
	AddInstructions []model.InstructionPayload
	// Start starts the task after the instructions are added.
	Start bool
}

// Update changes a task's attributes and optionally starts it.
func (tx *Tx) Update(ctx context.Context, a Actor, ref Ref, p UpdateParams) (*model.Task, error)

// Cancel cancels a task.
func (tx *Tx) Cancel(ctx context.Context, a Actor, ref Ref) (*model.Task, error)

// Restart restarts a task.
func (tx *Tx) Restart(ctx context.Context, a Actor, ref Ref) (*model.Task, error)

// Archive archives a task.
func (tx *Tx) Archive(ctx context.Context, a Actor, ref Ref) (*model.Task, error)

// Unarchive unarchives a task.
func (tx *Tx) Unarchive(ctx context.Context, a Actor, ref Ref) (*model.Task, error)

// AutoArchive archives a task if it is still at version, reporting whether it did.
func (tx *Tx) AutoArchive(ctx context.Context, ref Ref, version int64) (bool, error)

// OpenShell starts a finished task with the given debug-shell session.
func (tx *Tx) OpenShell(ctx context.Context, a Actor, ref Ref, session string) (*model.Task, error)

// ClearShellSession clears a closed debug-shell session from its task.
func (tx *Tx) ClearShellSession(ctx context.Context, session string, orgID int64) error

// Attach appends an external event to a task and starts it if the event wakes.
func (tx *Tx) Attach(ctx context.Context, ref Ref, ev *model.ExternalPayload, wake bool, channelMessage string) (*model.Event, error)

// ApplyRunnerEvent folds a runner's report into its task, reporting whether it was accepted.
func (tx *Tx) ApplyRunnerEvent(ctx context.Context, a Actor, ev *model.RunnerEvent) (bool, error)
```

```go
package scheduler // github.com/gritzapp/gritz/internal/server/scheduler

// Store persists schedules.
type Store interface {
	ClaimDueSchedules(ctx context.Context, tx *sql.Tx, limit int) ([]*model.Schedule, error)
	AdvanceSchedule(ctx context.Context, tx *sql.Tx, id int64, orgID int64, adv store.ScheduleAdvance) error
	UpdateSchedule(ctx context.Context, tx *sql.Tx, sched *model.Schedule) error
	GetWorkspaceVariables(ctx context.Context, tx *sql.Tx, runnerID, name string, orgID int64) ([]taskvar.Decl, error)
	// WithTx, CreateTask, CreateEvent: removed
}

// Options configures the Scheduler.
type Options struct {
	Tasks *taskservice.Service // new
	// ...
}

// Fire creates the task for one occurrence of sched in tx.
func Fire(ctx context.Context, tx *taskservice.Tx, sched *model.Schedule) (*model.Task, error) // changed: takes a taskservice.Tx, returns no notification
```

```go
package eventrouter // github.com/gritzapp/gritz/internal/server/eventrouter

type Router struct {
	Tasks *taskservice.Service // new
	// Log, Store, Publisher, OnRouteOutcome unchanged
}
```

```go
package archiver // github.com/gritzapp/gritz/internal/server/archiver

type Options struct {
	Tasks *taskservice.Service // new; replaces Publisher
	// Store stays, for ListTasksDueForArchive
}
```

```go
package model // github.com/gritzapp/gritz/internal/model

// removed
func (s *Schedule) Events(task *Task) []*Event
```

`apiserver.Options`, `server.Options`, `customserver.Options`,
`githubserver.Options` and `atlassianserver.Options` each gain
`Tasks *taskservice.Service`.

**How it fits:** `gritz server` (and the e2e harness) constructs one
`taskservice.Service` from the store and the publisher it already has, and
passes it to `server.New`, the webhook servers, the archiver and the scheduler.
`server.New` passes it to the API server and to the router it builds for
`TestEvent`, and its `onShellClose` uses it. The webhook servers pass it to the
routers they build. Handlers keep the coarse scope gate and error mapping, and
call `Tasks.InTx`. The router and the scheduler call `InTx` with their own
writes beside the task changes.

### Tests

Tests go through the existing public surfaces. The refactor is verified by the
suites that already exercise these paths. Their assertions don't change; only
the construction of a `Router`, `Archiver` or `Scheduler` gains a `Tasks` field:

- `internal/e2e/api`: `task_test`, `lifecycle_test`, `command_test`,
  `taskscope_test`, `shell_test`, `runner_test`, `publish_test`,
  `schedule_test`, `custom_event_test` and `routing_rules_test`;
- `internal/server/eventrouter`'s `route_test`, `internal/server/archiver`'s
  `Tick` tests, and the runner scenarios in `internal/e2e/scenario`.

`taskservice` gets no unit tests of its own that duplicate these. The one
change to tests: `internal/server/scheduler`'s `Tick` tests use a `StoreMock`
that stubs `CreateTask` and `CreateEvent`. They move to `teststore`, like the
archiver's, and `store_moq_test.go` goes away. `TestValidate` stays on a mock
of the smaller `Store`.

### Relation to the task state machine

The [task state machine proposal](https://github.com/gritzapp/gritz/pull/2265)
(#2264) changes *what* a task change writes: one `transition` event, with
state as a fold, instead of a lifecycle event beside a command event. This
proposal changes *where* that write happens. They fit together:

- **The `Tx` methods are the transition kinds.** `Create` is `create` (plus
  `start`), `Update` with `Start`, `Attach` with a wake and `OpenShell` are
  `start`, `Restart` is `restart`, `Cancel` is `cancel`, `Archive`,
  `AutoArchive` and `Unarchive` are their kinds, and `ApplyRunnerEvent` is the
  `run_*` kinds. "A rejected input writes nothing" is already the service's
  `ErrInvalidTransition` (or `ApplyRunnerEvent`'s `false`). The state machine's
  open question about name updates is whether `Update` without `Start` is a
  transition.
- **The fold plugs into one helper.** The state machine's layer 2 ("append a
  transition at every site that changes a task: task API, shell, runner events,
  event router, archiver, schedules") and layer 5 ("stop writing lifecycle and
  command events") each touch the ten sites in the table above. With the service
  in place, each touches the private helper in `taskservice` and nothing else.
  Its backfill check ("folding a task's transitions reproduces its row after
  each RPC") has a single write path to check.

**taskservice comes first.** It is mechanical and changes no behaviour, so its
layers are small and safe to land. Landing the state machine first would rewrite
the ten sites once for transitions and then again to move them into the service,
and its reviewers would read ten copies of the same change. The state machine's
open questions don't block the service: its API is shaped by the transition
kinds, which that proposal has settled. Its states and table are still open,
but they live inside the helper.

### fx

[go.uber.org/fx](https://pkg.go.dev/go.uber.org/fx) is a reflection-based
dependency injection framework: components register constructors
(`fx.Provide`), fx builds them in dependency order, and `fx.Lifecycle` hooks
start and stop them.

**What it would replace in `gritz server`:**

- the ordering in `Action`: `store.New`, the user resolver, `apiauth.New`,
  `oauthflow.New`, `pubsub.NewLocalPubSub`, `notifyserver.New`, the three webhook
  servers, `server.New`, and now `taskservice.New`, become `fx.Provide` entries;
- the archiver's, the scheduler's and the HTTP server's `go func` blocks, the
  `signal.NotifyContext`, and the 30s `Shutdown`, which become `OnStart`/`OnStop`
  hooks and `app.Run()`;
- `defer db.Close()` and the OpenTelemetry shutdown, which become `OnStop` hooks.

**What it would not replace:**

- flag parsing: urfave/cli stays and supplies a config value;
- the conditional parts: GitHub and Atlassian when their client IDs are set,
  the archiver and the scheduler when their poll interval is non-zero, and the
  `--no-auth` dev user and key. These become `fx.Options` assembled with the
  same `if` statements, or optional dependencies. The typed-nil interface trap
  `server.New` already guards against (`apiOpts.GitHub`) becomes easier to hit
  with optional injection;
- `server.New`'s own internal wiring (the shell registry, the API server, the
  `TestEvent` router), unless it is split into providers too.

**What it costs:**

- **Errors move from compile time to start-up.** A missing or duplicate
  provider is a runtime error with a `dig` trace. It takes an `fx.ValidateApp`
  test to get that back before deploy.
- **Constructors don't fit as they are.** Every component takes an `Options`
  struct with optional fields defaulted by `cmp.Or`. Each needs an adapter
  function or an `fx.In` struct, which ties the package to fx.
- **Wiring is no longer readable top to bottom.** "Who builds X, from what" is
  answered by searching for providers of a type, not by reading one function.
- **New dependencies:** `go.uber.org/fx` and `go.uber.org/dig` (`zap` is already
  indirect). A slog-backed `fxevent` logger is needed to keep its start-up log
  out of a second format.
- **Lifecycle details:** `OnStart` hooks have a start timeout, so
  `ListenAndServe` and the workers still run in goroutines started by the hooks.

**What it would buy:** the e2e harness could build the server from the same
module as `gritz server` (`fxtest.New(t, ...)`, swapping in `teststore` and the
`Wrap` hook with `fx.Decorate`) instead of keeping a second wiring list in step.
That is the strongest argument for fx, and it grows with each service.

**Recommendation: don't adopt fx now.** The graph is about fifteen nodes, built
once, in one function. The taskservice adds one node and about five edges, which
plain constructors handle in a few lines. Paying for fx's runtime errors,
adapters and indirection to save that isn't worth it yet. Revisit fx when
follow-up services make the harness and `gritz server` wiring lists hard to keep
in step, or when `Action` no longer reads top to bottom. The decision is
separable: nothing in the taskservice depends on how it is constructed, and if
fx is adopted later, `taskservice.New(Options)` becomes one more provider.

A smaller step that doesn't need fx can go with the taskservice wiring: build
one `eventrouter.Router` in `gritz server` and pass it to the webhook servers
and `server.New`, instead of four `&eventrouter.Router{...}` literals that each
need `Tasks` added. That is listed as an optional layer below.

### Follow-ups

Not part of this plan, but they would use the same shape once `taskservice`
has set it:

- **Schedules**: schedule CRUD and `Validate` from `apiserver/schedule.go`, and
  the scheduler's claim/advance half (a `scheduleservice` calling
  `taskservice.Tx.Create`);
- **Links**: `apiserver.CreateLink` and the router's create both write the
  `task_links` row and its event;
- **Orgs, members and keys**: `apiserver/org.go`, `apiserver/key.go`, and the
  user resolver's provisioning;
- **Events**: instruction and report events written outside a task change.

## Implementation Plan

Each layer is behaviour-preserving. The existing suites pass with the same
assertions; only their construction of the moved components changes, plus the
scheduler test move in (3).

1. **taskservice with Create, for the API** — Delivers: `internal/service/taskservice`
   with `Service`, `InTx`, `Tx`, `Actor`, `Ref`, the error sentinels and
   `Create`. `apiserver.CreateTask` calls it. `gritz server`, `server.New` and the
   e2e harness construct and pass the service. Depends on: nothing. Verifiable
   by: `internal/e2e/api` `task_test`, `taskscope_test` and `publish_test`.
2. **User transitions** — Delivers: `Update`, `Cancel`, `Restart`, `Archive`,
   `Unarchive` and the shared private helper. The five `apiserver` handlers call
   them. Depends on: (1). Verifiable by: `lifecycle_test`, `command_test`,
   `taskscope_test`, and the runner scenarios that restart and cancel.
3. **Scheduled creates** — Delivers: `scheduler.Fire` on a `taskservice.Tx`,
   `Tick` in one `InTx`, `RunSchedule` on the service, and the smaller
   `scheduler.Store`. `model.Schedule.Events` is deleted, and the scheduler's
   `Tick` tests move from `StoreMock` to `teststore`. Depends on: (1).
   Verifiable by: the scheduler tests and `internal/e2e/api` `schedule_test`.
4. **Router** — Delivers: `Tx.Create` with `Trigger` and `Link`, and `Tx.Attach`.
   `Router.create` and `Router.attach` call them, and `Router` gains `Tasks`,
   threaded through `customserver`, `githubserver`, `atlassianserver` and
   `server.New`. Depends on: (2) (`Attach` wakes through the helper). Verifiable
   by: `eventrouter`'s `route_test`, and `internal/e2e/api` `custom_event_test`,
   `routing_rules_test`, `event_test` and `github_test`.
5. **Runner events and shell** — Delivers: `ApplyRunnerEvent`, `OpenShell` and
   `ClearShellSession`. `SubmitRunnerEvents`, `OpenShell` and `onShellClose`
   call them. Depends on: (2). Verifiable by: `internal/e2e/api` `runner_test`
   and `shell_test`, and the `internal/e2e/scenario` suite.
6. **Archiver** — Delivers: `AutoArchive`. `archiver.archive` calls it, and
   `archiver.Options.Publisher` is removed. Depends on: (2). Verifiable by: the
   archiver's `Tick` tests.
7. **Close the door** — Delivers: a lint rule (for example `forbidigo` with type analysis, in `.golangci.yml`)
   that rejects calls to `(*store.Store).CreateTask`, `UpdateTask`,
   `GetTaskForUpdate` and `ClearShellSession` outside `internal/service/taskservice`
   and `internal/store`, so the next task write can't bypass the service.
   Depends on: (3)–(6). Verifiable by: `golangci-lint run` passing, and failing
   on a deliberate call from `apiserver`.
8. **One router (optional)** — Delivers: `gritz server` and the harness build one
   `eventrouter.Router` and pass it to the webhook servers and `server.New`,
   replacing the four literals. Depends on: (4). Verifiable by: the same suites
   as (4).

(2) to (6) are independent of each other once their dependency has landed, and
can be reviewed in parallel. The `ResetSandbox` handler (#1958) lands after (2),
as `Tx.Reset`. The state machine's layer 2 lands after (7).

If fx is adopted later, it is its own stack: an fx module for `gritz server`
with an `fx.ValidateApp` test; then lifecycle hooks replacing the goroutines and
the shutdown; then the e2e harness building from the same module with
`fxtest`.

## Trade-offs

**Methods on `Tx` rather than on `Service`.** Single-call methods on `Service`
(`s.tasks.Cancel(ctx, ...)`) would read one line shorter in the handlers. But
the router and the scheduler need several writes in one transaction, so a
`Service` method set would need a second, tx-taking variant of `Create`, plus
a way to defer its notification until the caller commits. One surface with
`InTx` covers both, and the "publish after commit" rule lives in one place.

**`tx *sql.Tx` parameters, like the store.** The store's convention (nil opens
a transaction, non-nil joins one) could carry over. Then each method would have
two behaviours, and a caller that passes its own transaction would have to
collect and publish the notifications itself, which is the duplication this
removes.

**Authorization in the service.** The instance check could stay in the handlers
if the service took a `func(*model.Task) error` hook. Every user-facing caller
would pass the same hook, and the router, scheduler and archiver would pass
nil. `Actor` names that distinction instead. The coarse `AllowOp` gate stays in
the handlers because it needs no row.

**An interface for the service.** Callers take `*taskservice.Service`, not an
interface: everything that uses it is tested against `teststore`, so there is
nothing to mock. That is also why the scheduler's store mock goes away.

**Moving the decision methods off `model.Task`.** `Start`, `Cancel` and the rest
could move into the service too. They are pure and the runner side reads the
same model, and the state machine will replace them with the fold anyway, so
they stay where they are.

## Open Questions

- **`FailedPrecondition` for rejected transitions.** Cancelling a finished task
  or archiving a running one is a client error reported as `Internal`. Once
  every rejection is `ErrInvalidTransition`, mapping it to `FailedPrecondition`
  everywhere is a one-line change. Do it as a follow-up `fix:` after (2) (with a
  docs check of `reference/api`), or leave it for the state machine?
- **The router's wake has no lifecycle event.** `Attach` keeps that, to
  preserve behaviour. The state machine records it as `start`. Fine to wait for
  it, or record `restarted` from the service now?
- **Package path.** `internal/service/taskservice` as the issue names it, or
  `internal/server/taskservice` beside its callers? The service is server-only,
  but `internal/service/` keeps services from reading as transports.
- **Enforcement.** Is a `forbidigo` rule (layer 7) worth carrying, or is review
  enough once there is one obvious place to write a task?
