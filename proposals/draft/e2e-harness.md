# End-to-end tests on a local backend

Issue: https://github.com/gritzapp/gritz/issues/1890 (related)

## Problem

No test runs the server, the runner and the driver together. The apiserver
tests (`internal/server/apiserver/*_test.go`) use a real Postgres store but stop
at the RPC handlers. The runner tests (`internal/runner/*_test.go`) run a real
`TaskManager`, but against `testrunner.Server`, an in-memory stand-in for
`ListRunnerEvents`, and a `gritzclient.ClientMock` that accepts every runner
event and reports every task `COMPLETED`. Their sandboxes come from
`backend/fake`, and the drivers are either real `driverserver`s served in-process
by `testrunner.NewDriver` (with their own `ClientMock`) or scripted stubs. The only
test that uses a real backend, `TestTaskManagerStart`, needs a Docker daemon and
still talks to a mocked server.

The bugs that matter live in the seams between those pieces. The run boundary in
#1868 was a server bug that only showed up once the runner consumed the server's
command events. Neither side's tests could see it, because each side tested
against its own idea of the other. The runner-event-replica work
(`proposals/draft/runner-event-replica.md`) moves behaviour across those seams
one slice at a time, and each slice has rebuilt the test plumbing to keep the
existing tests passing. #1890 lists what that has left behind: `testrunner` grown
piecemeal, polling helpers that only exist for intermediate designs, follower and
`handled` tests that slices 8–9 make obsolete, and `driverserver` and fake backend
tests reworked twice.

The many small runner tests are also hard to trust. They assert on
`WaitHandled`, the outbox's length, fake-backend counters (`Boots`, `Runs`) and
`AssertTracked`'s probe `Boot`, which is the runner's internal bookkeeping rather
than what a user sees. When the runner's design changes they all change with it,
so they don't tell us whether behaviour survived the change.

We want a few real end-to-end tests over many small, questionable ones. They
should exercise the real server, runner and driver, use fakes only where there is
no other way, and assert only what a user can observe. Then we want to port the
existing tests onto them and delete what they cover.

## Design

```
 test ──RPCs (web UI / MCP)──▶ h.Server: apiserver (httptest, teststore Postgres)
                                 ▲   │ SSE notifications
          SubmitRunnerEvents,    │   ▼
          logs, MCP tools        │ h.Runner: runner.Serve (temp runner.db)
                                 │   │
                                 │   ▼ h.Backend: local backend
                               driver process (prebuilt gritz driver --root DIR)   black box
                                     │
                                     ▼
                               puppet agent, driven by the test
```

Only two things are fake:

- **The sandbox** is a directory on the runner host, and the driver is an
  ordinary child process. The local backend provides it.
- **The agent** is a new `puppet` agent (below). It has no behaviour of its own:
  the test drives each run through a control channel to the harness.

Everything else is production code: the apiserver behind its real auth, the
runner's real wiring on its own SQLite `runner.db`, the prebuilt driver binary
the Docker backend ships, the real driver.v1 transport over a Unix socket, the
injected `gritz` MCP server, log shipping and secret masking.

**The driver is a black box.** It has no public API of its own: the runner and
the API server configure it, and the harness has no handle on it and no mocks
of it. Tests see it only through its effects:

- the files its scripts write in the sandbox directory;
- what it reports to the server: runner events with their versions, log chunks,
  and the events its tool calls create.

Tests interfere with it only from outside: its calls to the server through
`h.Server.Mock`, and its sandbox through `h.Backend`, including `KillDriver`.

**Faults are injected at the seams, with moq mocks rather than hand-written
fakes.** A mock installed on the server or the backend replaces the methods it
sets, for every caller, until it is removed. Every other method reaches the
real implementation.

### Local backend

`internal/runner/backend/local` implements `backend.Backend` by using the runner
host as the sandbox, with no isolation. It is for tests and development
(`gritz runner --backend local`), never for running untrusted agents.

Each sandbox is a directory `Root/<task id>`. The handle is
`backend.Handle{Type: "local", ID: "<task id>"}`, so the harness can find a task's
directory without reading the runner's records. Naming by task id mirrors
Docker's `gritz-<task id>` container names.

| Operation | Local backend |
|---|---|
| `Create` | `mkdir Root/<id>` (fails if it exists, as a Docker name clash does). Nothing is copied: the driver binary is run in place. |
| `Boot` | No-op if up; `ErrGone` if the directory is missing. Otherwise starts `Binary driver --addr unix://DIR/gritz/driver.sock --root DIR` as a child process and writes its pid to `DIR/gritz/driver.pid`. The process runs with working directory `DIR` and environment `Environ` plus `HOME=DIR/home`, in its own process group (`Setpgid`), with stdout and stderr appended to `DIR/gritz/driver.out`. Boot returns once the process has started; driver readiness stays the runner's job, as with Docker. |
| `Dial` | A driver client over the socket: unencrypted HTTP/2 with prior knowledge and the JWT as bearer, the same transport the Docker backend uses minus the exec tunnel. |
| `Suspend` | `SIGTERM` to the driver, which stops its run and reports it, as `docker stop` does. Waits for it to exit, then `SIGKILL`s the process group after `suspendTimeout`. The directory stays, so the next `Boot` finds the driver's files where it left them. |
| `Destroy` | `SIGKILL` the process group, then remove the directory. A missing directory is not an error. |
| `Kill` (local only) | `SIGKILL` the process group and leave the directory. Not part of `backend.Sandbox`; `h.Backend.KillDriver` uses it to produce a real lost run. |
| `Status` | `gone` if the directory is missing; `up` if the pid file names a live process whose `/proc/<pid>/cmdline` carries `--root DIR`; `suspended` otherwise. |
| `Open` | Rehydrates from the handle, with no I/O. |
| `Close` | Does nothing. Drivers outlive the backend, as containers outlive a Docker client. |
| `ValidateWorkspace` | Accepts every workspace. The local backend reads no runtime section (`container:`, `lambda_microvm:`). |

**Who reaps the driver.** `Boot` starts a goroutine that `Wait`s for the process
and then removes the pid file. That goroutine belongs to the OS process, not to
the `Backend` value, so after `h.Runner.Restart()` the old backend's goroutine
still reaps a driver that a new backend instance in the same test process
suspends. A zombie therefore never looks up. In development, a restarted
`gritz runner` process leaves its drivers to `init`. They are in their own process
group, so a Ctrl-C to the runner doesn't reach them, and the new runner finds
them through the pid file.

**Socket path length.** A Unix socket path is limited to 107 bytes, and
`t.TempDir()` paths with long test names exceed that. `New` rejects a `Root`
whose longest socket path would be too long, and the harness puts its root under
`os.MkdirTemp("", "gritz-e2e-")` rather than `t.TempDir()`.

**The injected MCP server's binary.** The runner points the injected `gritz` MCP
server at `backend.BinaryPath` (`/usr/local/bin/gritz`), which is where Docker
copies the binary and where the Lambda image has it (`TaskManager.spec`). On the
local backend the binary is wherever `Options.Binary` says. A new optional
interface, shaped like `backend.RunEnver`, lets a backend say where:

```go
// BinaryPather is implemented by a Backend whose sandboxes have the gritz binary
// somewhere other than BinaryPath.
type BinaryPather interface {
	BinaryPath() string
}
```

`TaskManager.spec` uses it for `McpServer.Command` when the backend implements
it.

### Per-sandbox driver paths

Every path the driver and the agent use must sit inside the sandbox directory so
that parallel sandboxes on one host don't collide. Here is what the driver and
agent touch today, and what makes each per sandbox:

| Path | Defined by | How it becomes per sandbox |
|---|---|---|
| `/gritz/run.json` | `runrecord.Path` | `Root` (below) |
| `/gritz/events.jsonl` | `eventfile.Path` | `Root` |
| `/gritz/log` | `agent.DefaultLogPath` | `Root` |
| `/tmp/gritz/<id>.json`, the agent config store | `agent.DefaultConfigStore` | `Root` |
| `RunSpec.files` (the config store directory and file, the `/gritz` directory) and `/gritz/.provisioned` | `TaskManager.spec`, `driverserver.ProvisionedPath` | `Root` (today `FileRoot`) |
| `/gritz/driver.sock` | `docker.DriverAddr` | `--addr`, already a flag |
| `/usr/local/bin/gritz` in the agent config | `backend.BinaryPath` | `backend.BinaryPather` (above) |
| `$HOME` (the agent CLIs' `os.UserHomeDir()` in `codex.go`, `cursor.go`, `sloppy.go`; `claude`'s `~/.claude`) | the process environment | the local backend sets `HOME=DIR/home` on the driver, and runs inherit it through `Options.Environ` |
| The agent's and setup commands' working directory | `agent.Config.Cwd`, defaulting to `.` (`interface.go`); setup commands set no `Dir` | the driver's working directory, which the local backend sets to `DIR` |
| `/gritz/driver.pid`, `/gritz/driver.out` | new, local backend only | inside `DIR` |

Most of this already exists. `driverserver.Options` and `ExecutorOptions` have
`RecordPath`, `EventsPath`, `LogPath`, `Config` and `FileRoot`, but only the tests
set them (`testrunner.NewDriver` and `driverserver_test.go`'s `setup`), and they
build the same layout by hand. These five options are replaced by one:

```go
// Root is the directory every in-sandbox path is resolved under: the run record,
// the event file, the driver log, the agent config store, the RunSpec files and
// the provisioned marker. Defaults to "/".
Root string
```

`gritz driver` gains `--root` to set it. A sandbox's paths are its in-container
paths under `Root`, so the runner keeps sending absolute paths in `RunSpec.files`
and needs no change. Docker and Lambda keep the default `/`.

### The puppet agent

The test needs per-run control of what the agent does: keep run 1 going until
the test has cancelled it, let run 2 finish, make run 3 fail, call a tool at a
chosen moment. Doing that with scripts (a fenced block in the instructions that
the dummy agent parses and runs) or with timed sleeps makes scenarios depend on
timing and on a script format, and the dummy's workspace-level `DummyOptions`
can't vary per run at all.

Instead, a new agent type, **`puppet`** (`internal/runner/agent`, next to
`claude`, `codex` and `dummy`), is driven entirely from outside:

- **It has no behaviour of its own.** When the driver prompts it, it reports the
  run (task, run version, prompt) to a control endpoint the harness runs, then
  does what the test tells it until the test ends the run: complete it, fail it,
  call a gritz MCP tool, and so on. A stop, restart or shutdown cancels it as it
  would any agent.
- **The workspace points it at the harness**, e.g. `agent: puppet: url: …`.
  Under the local backend the driver runs on the runner host, so it can reach
  an `httptest` endpoint in the test process.
- **The test gets a handle on each run** as it happens, keyed by run version, and
  steps it explicitly. Scenarios stop sleeping and polling files to know where a
  run is: a run stays open exactly until the test releases it, so "cancel
  mid-run" is guaranteed to be mid-run, and the prompt each run received is
  captured for assertions (which instructions reached which run).
- **The driver stays a black box.** The puppet is just the agent the real driver
  runs; nothing reaches into the driver.
- **The dummy agent is unchanged** and keeps its existing uses.

**Open:** the control protocol (transport, message shapes, how a run waits for
its next command, timeouts, cleanup of runs a test never releases, reconnects
after `KillDriver`) and the harness API for it (the run handle and its methods).
Settle both when this slice is built. Scenarios below are still written in terms
of the earlier scripted-dummy design (`Script`, `Sh`, `Block`, `Error`,
`WaitFile`); their behaviours stand and will be re-expressed against the puppet's
API.

**`GRITZ_RUN_VERSION`.** Nothing exposes the run version to the agent today. The
agent's environment has only `GRITZ_TASK_ID`, `GRITZ_TOKEN` and `GRITZ_SERVER`
(`TaskManager.spec`), and the version is known only to the driver: the run record
in `Executor.Start`, and `task.GetVersion()` in `Driver.Run`. `Executor.execute`
adds `GRITZ_RUN_VERSION=<version>` to the run's environment, after the spec's env
and secrets so neither can override it. Setup commands and the agent both see
it. It is useful to the puppet, to real agents and to hooks.

### Harness

`internal/e2e` stands the system up per test. A `Harness` exposes one handle per
component it owns:

```go
type Harness struct {
	Server  *Server  // the API side: URL, Client, CreateOrg, Mock
	Org     *Org     // the default org, for tests that need only one
	Backend *Backend // the local backend; nil unless Options.Runner
	Runner  *Runner  // the runner; nil unless Options.Runner
}
```

**The runner is opt-in.** `Options.Runner` turns on the backend and the runner.
Without it, `h.Backend` and `h.Runner` stay nil and the harness is a server and
its orgs. Server-only tests (scope checks, the RPCs' own behaviour) leave it off
and act as the runner themselves with `Org.SubmitRunnerEvents`, on the org's
runner key. A test must not do both: with the runner on, hand-submitted runner
events race the real runner's, so a test that enables the runner reaches every
status through it.

`Org` isn't a component but a caller: an org and the user who owns it, with
that user's client. Tests that need several orgs on one server (permissions, cross-org,
org membership) create more with `h.Server.CreateOrg()`.

Each handle holds `t`, and its methods fail the test with `t.Fatal` instead of
returning errors. None of them exposes its real instance. The escape hatches go
through the same path as everything else:

- `h.Org.Client` is a real API client, and `h.Server.Client(token)` makes one
  for any other credential.
- `h.Backend` is itself a `backend.Backend`.
- Mocks (`h.Server.Mock`, `h.Backend.Mock`, `h.Backend.MockSandbox`) replace
  methods for every caller, and apply to those escape hatches too.

There is no driver handle (see above).

```go
func TestStart(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(nil)
	task.AddInstruction("do the thing")

	task.Start()
	// The test lets the puppet's run 1 complete (API open, see "The puppet agent").

	got := task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, got.Version, int64(1))
}
```

#### Setup and teardown

**`e2e.New(t, opts)`** builds the following, and tears it all down with
`t.Cleanup`:

1. **Server.** `server.New(server.Options{...}).Handler()` on `httptest.NewServer`,
   over `teststore.New(t)`. It uses `apiauth.New` with
   `server.NewStoreKeyValidator`, a fresh app key from
   `apiauth.CreateAppPrivateKey()` (user and task tokens), and a local pubsub
   feeding `notifyserver`, so SSE notifications are live. `apiauth.New` gets a
   `DevUser`, as `--no-auth` does, so it skips OIDC discovery; like the
   `--no-auth` dev user it has no org and no scopes, so a request without a
   token is authenticated but allowed nothing. `h.Server`'s mock router goes in
   `server.Options.Wrap`. The archiver and scheduler aren't started; no scenario
   needs them.
2. **Default org.** `h.Server.CreateOrg()` makes `h.Org` (below). Isolation
   comes from the org, as for every teststore test.
3. **Backend** (only with `Options.Runner`). A `local.Backend` on a fresh root,
   wrapped by `h.Backend`. The runner is given the wrapper.
4. **Runner** (only with `Options.Runner`). `runner.Serve` (below) with a `gritzclient` on `h.Org`'s runner key,
   the backend wrapper, `runner.db` in a temp directory, runner id `e2e`, and one
   workspace `e2e` whose agent is the dummy. The runner registers that workspace
   with the server itself (`RegisterWorkspaces`), so `CreateTask` finds it
   through the production path. The runner subscribes to the harness server's
   notifications, so commands reach the runner without waiting for the poll.
5. **Cleanup**, in reverse order: remove the mocks; stop the runner and wait for it;
   `Destroy` every sandbox under the root, which kills the drivers; remove the
   root; close the server; `teststore` drops the org. Without `Options.Runner`
   only the mocks and the server are torn down.

**`runner.Serve`.** `gritz runner`'s wiring is all inline in the CLI action
(`internal/command/runner.go`). It builds the `runnerstore`, `taskevents.Log`,
`RunnerEventOutbox` and `TaskManager`, then runs `RegisterWorkspaces`,
`HandleEvents`, `Load`, the outbox, the notification client and `Run`, in that
order. The harness has to run the same sequence, not a copy of it.

The sequence moves into `runner.Serve(ctx, ServeOptions)`. The command keeps
flag parsing, config loading and backend selection, and calls `Serve`. `Serve`
returns when `ctx` ends and leaves sandboxes up, as a shutdown does today.

**One harness per test.** Each harness has its own org, server, runner,
`runner.db` and sandbox root, so tests can call `t.Parallel()`. They share one
Postgres. The xid watermark is cluster-wide, so another test's open transaction
can delay this test's events for as long as that transaction stays open. That is
real behaviour, and short transactions bound it.

#### `h.Server`

Things that don't belong to any one org.

| Method | What it does |
|---|---|
| `URL` | the server's base URL |
| `Store` | the server's `*store.Store`, an escape hatch: a test may use it to arrange or assert state no RPC can reach, and says why in a comment. The call under test never goes through it |
| `Client(token)` | a `gritzclient.Client` calling the server with `token`, for credentials no harness method returns: a key that was deleted, a bad key |
| `CreateOrg()` | a fresh org with its own user (`teststore.CreateOrg`), and a runner key in it; returns an `*Org` |
| `Mock(m)` | see below |

There is no `Real` and no handler on `h.Server`: the call under test reaches the
server only as a client.

#### `Org`

An org and its owner. `CreateTask`, `WaitTask` and `WaitEvent` live here
rather than on `h.Server` because they act as a caller, and the caller decides
what is visible.

Users are explicit: a `Member` is a user acting in an org, with `UserID`,
`Email`, a `Client` and, when `AddOrgMember` added it, the `Resp`. Users come
from orgs. Every `CreateOrg` provisions a user with a unique email, so a test
adds another org's owner by email; there is no separate user-provisioning
method:

```go
other := h.Server.CreateOrg()
member := h.Org.AddOrgMember(&gritzv1.AddOrgMemberRequest{Email: other.Owner.Email})
```

| Method | What it does |
|---|---|
| `ID` | the org |
| `Owner` | the `*Member` `CreateOrg` made, owning the org; its `Resp` is nil |
| `Client` | shorthand for `Owner.Client`: a `gritzclient.Client` making real API calls as the owner, acting in the org with admin scopes. It is the escape hatch for RPCs no method wraps. It goes through the same path as any client, so it gets mocked methods too |
| `CreateTask(req)` | `CreateTask`, as the new-task page does, filling the harness defaults where `req` leaves them unset (`DefaultRunner` and `DefaultWorkspace`, both `e2e`); always `draft`, since a `bool` has no unset value. `req` may be nil. Returns a `*Task`, which keeps its `*Org` |
| `CreateKey(req)` | `CreateKey`, as the keys page does (admin scopes); returns a `*Key` with its `ID` and a `Client` on it |
| `CreateTaskToken(req)` | `CreateTaskToken`, as the runner calls it; returns a `*TaskToken` with its `Token` and a `Client` on it |
| `UpdateTask(req)` | `UpdateTask`, as the task page does |
| `ArchiveTask(req)` | `ArchiveTask`, as the task page does |
| `CancelTask(req)` | `CancelTask`, as the task page does |
| `SubmitRunnerEvents(req)` | `SubmitRunnerEvents`, as the org's runner calls it with its runner key |
| `RegisterWorkspaces(req)` | `RegisterWorkspaces`, as the org's runner calls it on startup with its runner key |
| `WaitRunnerEvents(runner, count)` | polls `ListRunnerEvents` until `runner`'s log holds at least `count` events, and returns the page token that resumes after them |
| `CreateOrg(req)` | `CreateOrg`, as the org switcher does, then registers the harness's workspace in the new org; returns an `*Org` whose owner is this org's owner |
| `GetTask(req)` | `GetTask`; returns the task |
| `ListTasks(req)` | `ListTasks`; returns the tasks |
| `ListEventsByTask(req)` | `ListEventsByTask`; returns one page of events |
| `GetEvent(req)` | `GetEvent`; returns the event |
| `ListExternalEvents(req)` | `ListExternalEvents`; returns the events |
| `TestEvent(req)` | `TestEvent`, as the test event dialog does; returns the response. With `Fire` it is the only RPC that writes external events |
| `CreateLink(req)` | `CreateLink`, as the agent's `create_link` tool does; returns the link |
| `SetRoutingRules(req)` | `SetRoutingRules`; returns the rules as stored |
| `GetRoutingRules(req)` | `GetRoutingRules`; returns the rules |
| `AppendLogChunk(req)` | `AppendLogChunk`, as the driver's log shipper does |
| `ListLogChunksByTask(req)` | `ListLogChunksByTask`; returns one page of chunks |
| `CreateSchedule(req)` | `CreateSchedule`, filling `DefaultRunner` and `DefaultWorkspace` where `req` leaves them unset; returns the schedule |
| `GetSchedule(req)` | `GetSchedule`; returns the schedule |
| `ListSchedules(req)` | `ListSchedules`; returns the schedules |
| `ListWorkspaces(req)` | `ListWorkspaces`; returns the workspaces |
| `ListOrgs(req)` | `ListOrgs`; returns the owner's orgs |
| `ListKeys(req)` | `ListKeys`; returns the keys |
| `AddOrgMember(req)` | `AddOrgMember`, as the owner; returns the added user as a `*Member` acting in this org |
| `ListOrgMembers(req)` | `ListOrgMembers`; returns the members |
| `Subscribe()` | subscribes to the org's notifications on the server's `/events` stream as its owner, and returns once it is live; `Notifications.Next()` waits for the next one |
| `CreateScopedKey(scopes)` | a new `xat_` key holding `scopes` (`authscope.Scopes`, as `keys.scopes` stores them), written to the store since no RPC narrows a key; returns a `*Key` |
| `WaitTask(id, cond)` | polls `GetTask` until `cond` accepts the task, and returns it |
| `WaitEvent(id, match)` | polls `ListEventsByTask` until an event matches, and returns it |

**Shorthand methods mirror their RPC.** A method that is one RPC takes that
RPC's request, so a test can set any field and the harness never grows
parameter lists. It returns what the test works with: a wrapper when there is
behaviour to attach (`*Task`, `*Key`, `*TaskToken` and `*Member` with their `Client`), otherwise the
unwrapped value (the task, the keys). A wrapper built from one RPC
carries that RPC's response in `Resp` (`Task.Resp` is the `CreateTaskResponse`,
`Key.Resp` the `CreateKeyResponse`, `TaskToken.Resp` the `CreateTaskTokenResponse`, `Member.Resp` the `AddOrgMemberResponse`), so a test can reach anything the shorthand
didn't pick out. A `*Key` from `CreateScopedKey` has a nil `Resp`, since no RPC
made it, and neither has an org's `Owner`. Methods that aren't a single RPC
(`CreateScopedKey`, `WaitTask`, `WaitEvent`, `Server.CreateOrg`) keep their own
shapes.

**Setup goes through methods; `Client` is for the call under test.** A call
that arranges the test, or reads state to assert on, goes through an `Org` (or
`Server`) method that returns the useful value and fails the test with
`t.Fatal` itself. A test calls `Client` directly only for the call under test,
whose result or error it asserts. Methods are added when a test first needs
them, not ahead of it.

```go
token := h.Org.CreateTaskToken(&gritzv1.CreateTaskTokenRequest{TaskId: task.ID})
_, err := token.Client.GetTask(t.Context(), &gritzv1.GetTaskRequest{Id: task.ID})
```

A `Member`'s `Client` carries the app JWT `/auth/token` issues the
web UI (`apiauth.NewAppClaims`, signed with the harness's app key), not a key:
keys carry no user, and the org and profile RPCs act on the caller's user id.
The JWTs live for `apiauth.AppTokenTTL` (5 minutes), which bounds a test's
length.

The runner registers its workspaces with the server (below). `h.Org`'s runner
key is the one the runner is given; it stays internal. Without
`Options.Runner` (and until `h.Runner` lands), `CreateOrg` registers the
`DefaultWorkspace` on `DefaultRunner` itself with that key, so every org can
`CreateTask`, and `Org.SubmitRunnerEvents` uses the same key to act as the
runner.

```go
// Mock installs m, replacing any previous mock: calls go to m's non-nil
// <Method>Func fields, and every method left nil goes to the real server.
// Mock(nil) removes it.
func (s *Server) Mock(m *gritzclient.ClientMock)
```

**The mock type.** `gritzclient.Client` is `gritzv1connect.GritzServiceClient`,
and `buf.gen.yaml` generates with connect's `simple` option. The client and
handler interfaces therefore have the same 52 methods, with the same request and
response types (`SubmitRunnerEvents(context.Context, *v1.SubmitRunnerEventsRequest)
(*v1.SubmitRunnerEventsResponse, error)` in both). So the existing
`gritzclient.ClientMock` (`client_moq.go`) already implements
`GritzServiceHandler`, and `Mock` takes it. No new mock is generated.

**How it works:**

- **A router, not an interceptor.** An interceptor can't answer for a mock:
  it would have to return a `*connect.Response[Res]`, `connect.NewResponse` is
  generic, `Res` is only known by reflection there, and `connect.AnyResponse`
  can't be implemented outside connect. So `Mock` is backed by a router that
  wraps the RPC implementation instead. `server.Options.Wrap` hands the harness
  the apiserver, and the harness returns a `ClientMock` whose every
  `<Method>Func` is a `reflect.MakeFunc` that routes the call. The wrapped
  implementation sits behind the Connect interceptors and the `/mcp` endpoint,
  so callers are still authenticated, and a bad token is still rejected, before
  a mock answers. Authorization is not: scopes are checked inside each handler
  (`AllowOp`, `Allow`), so a mocked method answers callers the real one would
  deny.
- **Routing.** For each call, the router checks the installed mock's
  `<Method>Func` field by reflection. That is fine in test code, and keeps
  working as RPCs are added.
  - If the field is set, the router calls the mock's method,
    `SubmitRunnerEvents(ctx, req)`, and the generated handler wraps its result.
  - Otherwise it calls the real handler.
- **Typed, with no generics.** Each `<Method>Func` has the method's real request
  and response types, so a mock needs no type assertions.
- **Replace, not wrap.** A mocked method replaces the real one for every caller
  (the runner, the driver, the injected MCP server and the test itself) until
  the mock is replaced or removed. It doesn't call the real method; there is no
  `next`. To make a call fail once and then behave, the test calls
  `h.Server.Mock(nil)` once it sees the failure in `m.<Method>Calls()`, and the
  real method answers again.
  Call-through is a possible later addition (Open Questions), not part of this
  design.
- **The harness's own calls too.** `Task` methods and the waits use
  the org's `Client`, so they see the mock as well. `WaitTask` reads `GetTask`,
  so a test that mocks `GetTask` removes the mock before waiting.
- **Free call recording.** The router calls the mock's method, not the bare
  `Func` field, so `m.<Method>Calls()` records every request sent while the mock
  was installed, whoever sent it.
- **One mock at a time.** `Mock` returns nothing. A second `Mock(m2)` replaces
  the first, and it is `m2`'s methods that are mocked from then on. A test that
  wants to change one method copies the fields it keeps. Cleanup removes any
  mock still installed.
- **Streaming RPCs.** `GritzService` has none today (`/events` SSE is plain
  HTTP), so there is nothing to mock. With `simple`, a streaming method's client
  and handler signatures differ (`*connect.ServerStreamForClient[Res]` versus a
  `*connect.ServerStream[Res]` argument), so `ClientMock` won't fit one. See Open
  Questions.

```go
// The driver's terminal report fails, and so does the runner's report of the
// unreported run, until the mock is removed.
m := &gritzclient.ClientMock{
	SubmitRunnerEventsFunc: func(context.Context, *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
		return nil, connect.NewError(connect.CodeInternal, errors.New("e2e: rejected"))
	},
}
h.Server.Mock(m)
```

#### `h.Backend`

`h.Backend` is a `backend.Backend`, and it is what the runner is given. It wraps
the `local.Backend`, which it doesn't expose: like `h.Org.Client`, the handle
itself is the escape hatch (`h.Backend.Open(handle)`), and it goes through the
same mocks the runner sees. Its `Create` and `Open` return wrapped sandboxes, so
mocks also apply to sandboxes the runner restores at `Load`.

| Method | What it does |
|---|---|
| `Dir(taskID)` | the task's sandbox directory, `Root/<id>` |
| `WaitSandbox(taskID, state)` | polls the sandbox's `Status()` until it is `backend.StatusUp`, `StatusSuspended` or `StatusGone`. A task that never had a sandbox reports `gone` |
| `KillDriver(taskID)` | `SIGKILL`s the driver's process group through `local.Backend.Kill`, leaving the directory: a real lost run |
| `Mock(m)` | installs `m`, replacing any previous mock: the `Backend` methods whose `<Method>Func` is set (`Create`, `Open`, `ValidateWorkspace`) go to it. `Mock(nil)` removes it |
| `MockSandbox(taskID, m)` | replaces the methods of the task's sandbox whose `<Method>Func` is set (`Boot`, `Dial`, `Status`, `Suspend`, `Destroy`, `Handle`), replacing the task's previous sandbox mock; `MockSandbox(taskID, nil)` removes it. It applies to whichever sandbox the task has or gets, created or opened |

```go
// Mock installs m, replacing any previous mock: m's non-nil methods replace
// the backend's. Mock(nil) removes it.
func (b *Backend) Mock(m *backend.BackendMock)

// MockSandbox installs m for the task's sandbox, replacing any previous one:
// m's non-nil methods replace the sandbox's. MockSandbox(taskID, nil) removes
// it.
func (b *Backend) MockSandbox(taskID int64, m *backend.SandboxMock)
```

These use the moq mocks `backend` already generates (`backend_moq.go`), with the
same rules as `h.Server.Mock`: a mocked method replaces the real one, there is no
call-through, a new mock replaces the previous one, `nil` removes it, calls are
recorded, and cleanup removes any mock left installed. There are only a handful
of methods, so the wrapper checks each `Func` field directly rather than by
reflection. Backend operations are plain calls with no streams, so there is no
streaming question here.

```go
// The task's first boot fails; the next one is real.
m := &backend.SandboxMock{BootFunc: func(context.Context) error {
	return errors.New("e2e: boot failed")
}}
h.Backend.MockSandbox(task.ID, m)
task.Start()
task.WaitStatus(gritzv1.TaskStatus_FAILED)
assert.Assert(t, cmp.Len(m.BootCalls(), 1))
h.Backend.MockSandbox(task.ID, nil)
```

#### `h.Runner`

`Restart()` stops the runner and waits for `runner.Serve` to return. It then
starts a new one on the same `runner.db` and the same `h.Backend`, so installed
mocks stay in place.

The runner has no escape hatch. No client path leads into it, and its internals
are what the tests must not depend on. If a setup need appears that only the
runner can meet, one can be added then.

#### `Task`

A `Task` holds its `Org` and the task id. Its methods are implemented through
the org's `Client` and `h.Backend`, and the commands go through the RPCs the web UI uses
(`webui/src/routes/tasks.$id.tsx`, `tasks.new.tsx`):

| Method | Through |
|---|---|
| `AddInstruction(text)` | `UpdateTask{add_instructions: [text]}` |
| `Start()` | `UpdateTask{start: true}` |
| `Cancel()`, `Restart()`, `Archive()`, `Unarchive()` | `CancelTask`, `RestartTask`, `ArchiveTask`, `UnarchiveTask` |
| `Get()`, `Events()` | `GetTask`; `ListEventsByTask`, every page |
| `Links()`, `Log()` | `ListLinks`; `ListLogChunksByTask`, concatenated |
| `WaitStatus(status)` | `Org.WaitTask` |
| `WaitEvent(match)` | `Org.WaitEvent` |
| `ReadFile(name)`, `WaitFile(name, want)` | a file in `h.Backend.Dir(id)` |

The composer sends `add_instructions` and `start` in one `UpdateTask`. The
harness splits them so a test can add instructions before deciding how to start
the task. A wake is `AddInstruction(...)` followed by `Start()`. Driving the
agent's runs is the puppet's API (open).

`WaitFile` is an addition to the decided API. Scenarios need to know exactly
that the script has got this far before they act, and `WaitStatus(RUNNING)`
fires at `started`, before the agent runs.

#### Waits and assertions

**Waits** poll every 20ms until `Options.Timeout` (default 30s), then fail
through `t` with the last state they saw:

- `WaitTask` reports the status, version and command, plus the last few events.
- `WaitEvent` reports every event.
- `WaitSandbox` reports the last status.
- `WaitFile` reports the file's content.

**Only observable outcomes.** Tests assert on:

- task status and version;
- the event timeline, links and the shipped log;
- sandbox state through the backend;
- the files the script wrote.

Tests never open `runner.db` or call into the driver. The
driver's own files in the sandbox directory (`gritz/run.json`,
`gritz/events.jsonl`, `gritz/log`) are its internals, so tests don't read them
either. This is what lets the tests survive runner-event-replica slices 9–11
unchanged.

**Options:**

```go
type Options struct {
	Runner       bool              // stand up h.Backend and h.Runner; off → both nil (server-only)
	Concurrency  int               // runner slots; default 2
	SuspendAfter time.Duration     // the e2e workspace's suspend_after (from slice 10); 0 → workspace default
	Commands     []string          // the workspace's setup commands
	Secrets      map[string]string // the workspace's secrets
	PollInterval time.Duration     // the runner's; default 1s, a backstop to SSE
	Timeout      time.Duration     // every wait's; default 30s
}
```

The harness sets the runner's `LostTimeout` and `ReadyTimeout`
(`runner.ServeOptions`) to 2s, so scenarios about lost or unready drivers
don't wait out the 10s defaults. The runner fields (`Concurrency` through
`PollInterval`) apply only with `Runner`.

**The driver binary** is `prebuilt/gritz-linux-$GOARCH`, which the tests already
require (`mise run build`, as `TestTaskManagerStart` and the Docker backend tests
do). The prebuilt can be stale against the source; see Open Questions.

### Scenarios

Each scenario is one top-level test in `internal/e2e`. They are written in
terms of the earlier scripted-dummy design; the behaviour each one checks stands,
and the steps will be re-expressed against the puppet's API once it is settled. "Append runs" is
`Sh("echo $GRITZ_RUN_VERSION >> runs")`, so the file `runs` records which runs
the agent executed in this sandbox.

| Test | Arrange / act | Assert |
|---|---|---|
| `TestStart` | `Script(append runs)`; `Start` | `COMPLETED` at v1; `runs` = `1`; timeline has `SANDBOX_STARTED`, `SANDBOX_EXITED` |
| `TestCancel_MidRun` | `Script(append runs, Block())`; `Start`; `WaitFile(runs, 1)`; `Cancel` | `CANCELLED`; `runs` = `1`; one `SANDBOX_STARTED` |
| `TestRestart_MidRun` | as above, then `Restart` | `WaitFile(runs, 1 2)`: run 2 got the same, uncompleted script; `Get().Version` = 2; `Cancel` → `CANCELLED` |
| `TestWake_MidRun` (#1868) | `Script(append runs, Sleep(3s))`; `Start`; `WaitFile(runs, 1)`; `Script(append runs)`; `Start` | Right after the wake, `Get()` is `RUNNING` at v1 with command `start`, so the wake landed on the running run; if run 1 had already ended, the test fails instead of passing vacuously. Then `COMPLETED` at **v2**; `runs` = `1 2`; run 1's script did not run again |
| `TestCancel_PendingStart` | `Concurrency: 1`. A: `Script(append runs, Block())`, `Start`, `WaitFile`. B: `Script(Sh(touch ran))`, `Start`, `WaitStatus(PENDING)`, `Cancel` | B is `CANCELLED`. Then cancel A, and run a third task C to `COMPLETED`. C's start is later in the log than B's cancel, so it acts as a barrier. Afterwards B has no `SANDBOX_STARTED`, and `h.Backend.WaitSandbox(B, gone)`: B never ran, even once the slot was free |
| `TestArchive` | `Script(Sh(touch f))`; `Start`; `COMPLETED`; `Archive` | `WaitSandbox(gone)`. Then `Unarchive`, `Script(Sh(test ! -e f), append runs)`, `Start` → `COMPLETED` at v2 with `runs` = `2`: a fresh sandbox, which got the task's whole log and acted only on its latest command |
| `TestRunnerRestart_MidRun` | `Script(append runs, Block())`; `Start`; `WaitFile`; `h.Runner.Restart()`; `Cancel` | `CANCELLED`; `runs` = `1`; one `SANDBOX_STARTED`: the new runner reattached and carried the cancel to the driver, without starting the run again |
| `TestSuspendAfter` (slice 10) | `SuspendAfter: 300ms`. `Script(append runs)`; `Start`; `COMPLETED`; `WaitSandbox(suspended)`; `Script(append runs)`; `Start` | `COMPLETED` at v2; `runs` = `1 2` in the same file, so the directory survived and nothing re-created it; `WaitSandbox(suspended)` again |
| `TestSuspendAfter_Window` (slice 10) | default `suspend_after`; two runs back to back | the sandbox stays `up` between them and after; `runs` = `1 2` |

Further scenarios, several using mocks:

| Test | Arrange / act | Assert |
|---|---|---|
| `TestWake_CancelledAtBoundary` | as `TestWake_MidRun`, then `Cancel` while run 1 sleeps | `CANCELLED`; `runs` = `1`: the pending start of v2 was superseded and never ran |
| `TestRestart_CancelledBeforeStart` | `Script(append runs, Sh("trap '' TERM; while :; do sleep 0.1; done"))`; `Start`; `WaitFile`; `Restart`; `Cancel` | `CANCELLED`; `runs` = `1`. Run 1 ignores `SIGTERM` and outlives the restart until `Sh`'s kill delay, so the cancel lands before run 2 can start. No run reports v2, so the runner reports it `stopped` |
| `TestRunnerRestart_PendingWake` | as `TestWake_MidRun`, then `h.Runner.Restart()` while run 1 sleeps | `COMPLETED` at v2; `runs` = `1 2`: the pending start survived the dropped stream |
| `TestDriverDies_MidRun` | `Script(append runs, Block())`; `Start`; `WaitFile`; `h.Backend.KillDriver(task.ID)` | `FAILED`; the sandbox `suspended`. Then `Script(append runs)`, `Start` → `COMPLETED` at v2 with `runs` = `1 2`: the sandbox booted again, the driver reconciled its interrupted run and acted on the new start |
| `TestUnreportedRun` | `Script(append runs, Sleep(2s))`; `Start`; `WaitFile(runs, 1)`; `h.Server.Mock` with `SubmitRunnerEvents` failing with `CodeInternal` (example above), which the driver's client doesn't retry and the runner's outbox does; wait until `m.SubmitRunnerEventsCalls()` holds the driver's `stopped` and then the runner's `failed`; `h.Server.Mock(nil)` | `FAILED`: the driver went idle with its run unreported, the runner reported it `failed`, and the outbox delivered that report once the real method answered again |
| `TestSandboxFails` (table: `h.Backend.Mock` with `CreateFunc`, `h.Backend.MockSandbox` with `BootFunc`) | `Concurrency: 1`; the mock returns an error; `Script(append runs)`; `Start`; `WaitStatus(FAILED)`; remove the mock with `nil` | the failure's reason is the mock's error, and the mock recorded one call. Then a second task runs to `COMPLETED`, so the slot was released, and the first task, started again, `COMPLETED`s |
| `TestDriverUnreachable` | `h.Backend.MockSandbox` with a `DialFunc` returning an error; `Start` | `FAILED` once `ReadyTimeout` passes; the sandbox `suspended` |
| `TestAgentError` | `Script(Error("boom"))`; `Start` | `FAILED`; `SANDBOX_FAILED` carries `boom` |
| `TestTools` | `Script(Tool("report", {message}), Tool("create_link", {url, subscribe: true}), Tool("update_my_task", {name}))` | a report event with the message, the subscribed link, and the new name. Covers the injected MCP server, the task token and the `BinaryPather` path |
| `TestEnv` | `Secrets: {E2E_SECRET: s3cret}`; `Script(Sh(env > env))` | `env` has `GRITZ_RUN_VERSION=1`, `GRITZ_TASK_ID`, `E2E_SECRET=s3cret`, and `HOME` inside the sandbox directory |
| `TestSecretsMasked` | `Secrets: {E2E_SECRET: s3cret}`; `Script(Sh(echo $E2E_SECRET; echo $GRITZ_TOKEN))` | `Log()` has `[gritz:masked E2E_SECRET]` and `[gritz:masked token]`, and neither value |
| `TestSetupCommands` | `Commands: [echo setup >> setup]`; two runs | `setup` = `setup`: the commands ran on the first run only; `Log()` has their output |
| `TestCancel_DuringSetup` | `Commands: [sleep 60]`; `Start`; `WaitStatus(RUNNING)`; `Cancel` | `CANCELLED` well before 60s |
| `TestRestart_Completed` | complete run 1, then `Restart` | `COMPLETED` at v2 in the same sandbox |
| `TestConcurrent` | `Concurrency: 2`, three tasks, each `Script(append runs)` | all `COMPLETED`; each sandbox's `runs` = `1`: no cross-talk between sandboxes |

### Porting the existing tests

The categories:

- **(a) Covered.** A scenario covers it. Delete it once the scenario lands.
- **(b) Keep.** It covers tricky local logic that end-to-end tests can't pin
  precisely or can't reach (broken disks, encoding edge cases). Keep it as a
  focused unit test.
- **(c) Intermediate.** It tests a design that runner-event-replica slices 9–11
  remove anyway. Delete it with that code in the slice, without porting it. Where
  the behaviour lives on, a scenario takes over.

Slice 8 has landed, so the tables use the names on `master` after it. Mocks make
most fault-injection tests (a), not (b).

#### `internal/runner` (`taskmanager_start_test.go`)

| Test | | Notes |
|---|---|---|
| `TestTaskManagerStart` | a | `TestStart`, without a Docker daemon. Docker specifics stay in `backend/docker`'s tests |
| `TestTaskManagerSpec_PreCreatesLogDir`, `_Secrets`, `TestTaskManagerRunSpec_RunEnv` | b | Spec building: the 0777 directories for non-root containers, secrets kept out of `Env`, Lambda's `RunEnv`. End to end everything runs as one user with no `RunEnv`, so these can't be seen there. Rewrite against the spec builder, with no manager or fake backend |
| `TestTaskManagerFollow_StopWithoutSandbox` | c | The follower goes in slice 9. Its rule, a stop with no `Run` open is reported by the runner, is in `TestRestart_CancelledBeforeStart` |
| `TestTaskManagerFollow_StopRunOpen` | a | `TestCancel_MidRun` |
| `TestTaskManagerFollow_Start` | c | `handled`. Acting on the latest command: `TestArchive` (the whole log), `TestWake_CancelledAtBoundary` |
| `TestTaskManagerFollow_Restart` | a | `TestRestart_MidRun` |
| `TestTaskManagerFollow_RestartCancelled` | a | `TestRestart_CancelledBeforeStart` |
| `TestTaskManagerFollow_SlowDriver` | c | `handled` before readiness. A real driver's start-up latency is in every scenario |
| `TestTaskManagerFollow_StartRunOpen` | c | A follower and a `Run` goroutine become one `RunLoop` in slice 9. `TestWake_MidRun` |
| `TestTaskManagerFollow_StartWaitsForSlot`, `_StopSupersedesWaitingStart` | a | `TestCancel_PendingStart`, `TestConcurrent` |
| `TestTaskManagerFollow_Archive` | a | `TestArchive` |
| `TestTaskManagerFollow_ResumesFromHandled` | c | `handled` goes in slice 9. `TestRunnerRestart_MidRun` |
| `TestTaskManagerStart_Create`, `_Restart`, `TestTaskManagerWait`, `_Done` | c | `TaskRunner.Start`/`Wait` become `RunLoop`. `TestStart`, `TestRestart_Completed` |
| `TestTaskManagerStart_CreateFails`, `_BootFails` | a | `TestSandboxFails` |
| `TestTaskManagerStart_NotReady` | a | `TestDriverUnreachable` |
| `TestTaskManagerStart_Gone` | c | `TestDriverDies_MidRun` |
| `TestTaskManagerRemove`, `TestTaskManagerAfterDestroy` | c | `Destroy` becomes archive handling in `RunLoop`. `TestArchive` |

#### `internal/runner` (`taskmanager_load_test.go`)

| Test | | Notes |
|---|---|---|
| `TestTaskManagerLoad_RestartReattaches`, `TestTaskManagerLoad` (up) | a | `TestRunnerRestart_MidRun` |
| `TestTaskManagerLoad` (suspended, gone, status error, open fails), `TestTaskManagerLoad_VersionScopedBackstop` | b | The lost-run backstop (`failIfTaskRunning`) stays in slice 9. It needs a sandbox that died while no runner was running, which `h.Runner.Restart()` (stop and start in one call) can't arrange. Rewrite on the local backend: a directory with no driver is suspended, no directory is gone. Revisit with runner-event-replica's `sandboxes.version` open question |
| `TestTaskManagerPrune` | c | `Prune` goes in slice 9 |
| `TestTaskManagerRun` | c + b | The prune half goes. The fatal half (a failed durable write ends `Run` with `FatalError`) stays |
| `TestTaskManagerDie_StopWithoutSandbox`, `TestTaskManagerHandleEvents_Fatal` | b | A broken disk can't be produced end to end |
| `TestTaskManagerGracefulShutdown_NoFatalCause` | a | Every `h.Runner.Restart()` shuts down gracefully |
| `TestTaskManagerHandleEvents_Lost` | c | `TaskLostEvent` goes in slice 9. `TestDriverDies_MidRun`, `TestDriverUnreachable` |

#### `internal/runner` (`taskrunner_test.go`, `internal_test.go`, `eventoutbox_test.go`)

| Test | | Notes |
|---|---|---|
| `TestNewTaskRunner` | delete | It tests constructor panics, which `CLAUDE.md` says not to test. Delete it in the first port PR |
| `TestTaskRunnerRun_Finished`, `_ForwardsEvents` | a | `TestStart`; every scenario depends on the driver receiving its commands over the forwarded stream |
| `TestTaskRunnerRun_Reattach` | b | Reconnecting after a dropped stream, with the driver still alive, needs a stream cut mid-flight. That takes a `Dial` that wraps the real client, which is call-through (Open Questions). Keep it until call-through exists |
| `TestTaskRunnerRun_DriverUnreachable`, `_NotReady` | a | `TestDriverUnreachable` |
| `TestTaskRunnerRun_NothingToRun` | a | `TestRunnerRestart_MidRun`, `TestArchive` (unarchive) |
| `TestTaskRunnerRun_Reopen` | a | `TestWake_MidRun`, `TestRunnerRestart_PendingWake` |
| `TestTaskRunnerRun_StopNeverRun` | a | `TestRestart_CancelledBeforeStart` |
| `TestTaskRunnerRun_NotReported` | a | `TestUnreportedRun` |
| `TestTaskRunnerRun_Shutdown` | a | `TestRunnerRestart_MidRun`: the sandbox stays up across the restart |
| `TestTaskRunnerBoot`, `_Create`, `_BootFails`, `_Concurrent`, `TestTaskRunnerRestore` | c | Boot, claiming and `ErrRunning` are reshaped by `RunLoop` in slice 9 |
| `TestTaskRunnerSend_Shutdown` | c | `TaskLostEvent` |
| `TestDoubling` | b | Backoff arithmetic |
| `TestRunnerEventOutbox_Permanent`, `TestTaskManagerEventsSurviveRestart` | b | Retry classification and durability |

#### `internal/runner/driverserver`

The driver is a black box, so its tests port to scenarios. At most one small
table test of the driver loop's decision rules (`loop.actLocked` and
`loop.idle`) stays, and only for the cases no scenario reaches.

| Test | | Notes |
|---|---|---|
| `TestRun`, `TestRun_NothingToDo` | a | `TestStart`, `TestRunnerRestart_MidRun` |
| `TestRun_ProvisionsFiles`, `_ProvisionsOnce` | a | Every scenario needs the provisioned agent config. `TestSetupCommands` needs it provisioned once, because its progress lives in that file |
| `TestRun_ProvisionFailure` | a (gap) | No scenario reaches it. The failure leaves the start pending, and the loop logs it and retries; we accept the gap |
| `TestRun_ResentStream`, `_StaleStart`, `_ResentCommandIgnored` | a | `TestRunnerRestart_MidRun`, `TestRunnerRestart_PendingWake`, `TestSuspendAfter` |
| `TestRun_WholeLog` | a | `TestArchive`: the unarchived task's fresh sandbox gets the whole log |
| `TestRun_PendingStart` | a | `TestWake_MidRun` |
| `TestRun_Restart`, `_RestartIdle` | a | `TestRestart_MidRun`, `TestRestart_Completed` |
| `TestRun_Stop`, `_StopSupersedesPendingStart` | a | `TestCancel_MidRun`, `TestWake_CancelledAtBoundary` |
| `TestRun_ReattachAfterDrop`, `_PendingAfterDrop` | a | `TestRunnerRestart_MidRun`, `TestRunnerRestart_PendingWake`: to the driver, a restarted runner's stream is a reopened one |
| `TestRun_NoSpec`, `TestNew_PendingAfterRestart`, `TestShutdown`, `TestShutdown_Pending` | b → loop table | A driver that restarted with a stored start, or that shuts down mid-run or with a start pending, needs timing no scenario controls. These four become rows of the loop table test |
| `TestRun_WaitsForFirstBatch`, `_SecondStream` | a | Every scenario's stream; a second stream only arises in the restart race, which the runner retries |
| `TestRun_KeepAlive` | a (gap) | No scenario stays quiet for longer than the runner's 45s idle timeout; see Open Questions |
| `TestRun_StoresEvents`, `_EventsSurviveRestart`, `_ReopenedStream` | a | `TestSuspendAfter` (the driver restarts with its file), `TestRunnerRestart_MidRun`, `TestDriverDies_MidRun` |
| `TestRun_NotReported` | a | `TestUnreportedRun` |
| `TestNew_Reconcile` | a | `TestDriverDies_MidRun` |
| `TestRun_Env`, `TestEnv`, `TestSecrets` | a | `TestEnv` |
| `TestRun_MasksSecretsInShippedLog` | a | `TestSecretsMasked`. The masking itself stays covered in `redact` and `logship` |
| `TestListenAndServe`, `_Unix` | a | Every scenario serves on a Unix socket. TCP differs only in the address `netx.Listen` gets |

#### Other runner packages

| Package / test | | Notes |
|---|---|---|
| `backend/fake` (`fake_test.go`) | c | They go with the fake (see below) |
| `testrunner` | c | Shrinks to what (b) tests use (`NewRunnerStore`, `Server`, the event builders), which then moves into those packages' own test files |
| `taskevents` (all) | b | Replication, cursor, boot and blocking `Read` |
| `runnerstore`: `TestHandled`, `TestAppendEvents_CommandEventID` | c | Their columns are dropped in slice 9 |
| `runnerstore`: the rest, `TestTaskChanges` | b | Projection and storage |
| `eventfile`, `runrecord` (all) | b | Encoding, torn lines and reconcile. The packages are shared code; their tests don't drive the driver |
| `driverclient` (all) | b | The runner's side of the stream: hidden keep-alives and the idle timeout |
| `agent`: `TestDriverRun_DrainsEventsToTail`, `TestDrainEvents_RequestsServerSideTypeFilter`, `TestDriverRun_EventTokenNotAdvancedOnError` | c | `drainEvents` goes in slice 11 |
| `agent`: `TestDriverRun`, `_AgentError`, `_AgentConfiguredError`, `_SetupCommandError`, `_Stop`, `_SetupCommandOutputTeed`, `_ShipsLogToServer`, `_MasksSecretsInShippedLog`, `_MasksTokenInShippedLog`, `TestDriverSetup_Cancel` | a | `TestStart`, `TestAgentError`, `TestSetupCommands`, `TestCancel_MidRun`, `TestCancel_DuringSetup`, `TestSecretsMasked` |
| `agent`: `TestDriverRun_StartedSubmitError`, `_StoppedSubmitError`, `_FailedSubmitError`, `_GetTaskError` | a | `TestUnreportedRun`, plus a row per RPC mocked to fail (`SubmitRunnerEvents` on `started`/`failed`, `GetTask`) if the outcomes differ |
| `agent`: `TestDriverRun_LogsToSink` | a | The raw `/gritz/log` is the driver's internals; `Log()` shows what ships |
| `agent`: `TestRun_ForksIntoShell` | b | The shell relay, which no scenario drives |
| `backend/docker`, `backend/lambdamicrovm`, `TestSpecClone` | b | Backend contracts; no scenario uses those backends |

**The fake backend.** After the port, nothing needs `backend/fake`. Fault
injection moves to `h.Backend.Mock` and `MockSandbox`. The (b) runner tests that still need a
backend (the `Load` backstop, the fatal-write tests) use the local backend
directly. `fake` (≈750 lines) and `fake_test.go` (≈1150) are deleted.

#### Server tests

No server test is deleted. The apiserver, store, model, event router and archiver
tests already use a real Postgres store and pin the server's state machine
precisely, one transition per test. Examples are `command_test.go`'s command
event per path, `TestSubmitRunnerEvents_RunBoundaryCommandEvent`,
`TestListRunnerEvents_CommitOutOfIDOrder` and `TestTask_RunVersion_*`. Those are
(b). The scenarios add what no server test can show: that the runner and driver
act on those events as intended. The #1868 seam is the example, with
`TestWake_MidRun` sitting on top of `TestSubmitRunnerEvents_RunBoundaryCommandEvent`.

#### Relation to #1890

#1890 plans a test cleanup pass after the last runner-event-replica slice lands.
This proposal supplies the replacement coverage that makes the deletions safe,
and sorts its known areas:

- **`testrunner` grown piecemeal**: most of it is (c) or (a). What (b) needs
  moves into the packages that use it.
- **Polling and waiting tests**: `WaitHandled` and `AssertTracked` go with
  (c). `teststore.WaitRunnerEvents` stays (xid watermark, b). The harness's waits
  replace the rest and poll only for observable state.
- **Intermediate runner tests**: (c), deleted inside slices 9–11 as #1890 says.
- **Slow tests**: the scenarios need no Docker daemon. `TestDial_TunnelsThroughExec`
  stays Docker's own concern.
- **`driverserver` and fake backend tests**: the driver is a black box, so its
  tests port to scenarios. At most a table test of the loop's decision rules
  stays. Mocks replace the fake, which goes.

The harness lands before slices 9–11. Those slices can then verify themselves
with scenarios rather than with new fake-backend runner tests that #1890 would
only delete again.

### API

```go
package local // github.com/gritzapp/gritz/internal/runner/backend/local  // new

// HandleType is the backend.Handle.Type of local sandboxes.
const HandleType = "local"

// Options configures a Backend.
type Options struct {
	// Root holds one directory per sandbox, named after its task id.
	Root string
	// Binary is the gritz binary each sandbox runs as its driver.
	Binary string
	// Environ is the base environment of every driver; HOME is set per sandbox.
	Environ []string
	Log     *slog.Logger
}

// Backend runs each sandbox as a directory and a driver process on this host,
// with no isolation.
type Backend struct{ ... }

// New returns a Backend over opts.Root, rejecting a root too long for the
// drivers' socket paths.
func New(opts Options) (*Backend, error)

// Create makes the task's sandbox directory.
func (b *Backend) Create(ctx context.Context, spec *backend.Spec) (backend.Sandbox, error)

// Open returns the sandbox for a local handle, without I/O.
func (b *Backend) Open(h backend.Handle) (backend.Sandbox, error)

// ValidateWorkspace accepts every workspace.
func (b *Backend) ValidateWorkspace(ws *workspace.Workspace) error

// BinaryPath returns opts.Binary, which the injected MCP server runs.
func (b *Backend) BinaryPath() string

// Kill sends SIGKILL to the sandbox's driver process group and leaves its
// directory, so the sandbox reads as suspended.
func (b *Backend) Kill(h backend.Handle) error

// Close does nothing; drivers outlive the backend.
func (b *Backend) Close() error
```

```go
package backend // github.com/gritzapp/gritz/internal/runner/backend

// BinaryPather is implemented by a Backend whose sandboxes have the gritz binary
// somewhere other than BinaryPath. // new
type BinaryPather interface {
	// BinaryPath returns the path of the gritz binary inside the sandbox.
	BinaryPath() string
}
```

```go
package driverserver // github.com/gritzapp/gritz/internal/runner/driverserver

type Options struct {
	// Root is the directory every in-sandbox path is resolved under: the run
	// record, event file, driver log, agent config store, RunSpec files and
	// provisioned marker. Defaults to "/". // new
	Root string
	// RecordPath, EventsPath, LogPath, Config and FileRoot are removed. // removed
	// ... Environ, NewClient, KeepAlive, Log unchanged
}

type ExecutorOptions struct {
	// Root is as Options.Root. // new
	Root string
	// RecordPath, LogPath, Config and FileRoot are removed. // removed
	// ... Environ, NewClient, Log unchanged
}
```

```go
package agent // github.com/gritzapp/gritz/internal/runner/agent

// PuppetAgent is driven entirely by a control endpoint: it reports each run
// and does what it is told until the run ends. // new; API open
type PuppetAgent struct{ ... }
```

```go
package server // github.com/gritzapp/gritz/internal/server

type Options struct {
	// Wrap, when set, wraps the RPC implementation behind the Connect
	// interceptors and the MCP endpoint, so callers are authenticated before
	// it runs. Tests use it to mock RPCs. // new
	Wrap func(gritzv1connect.GritzServiceHandler) gritzv1connect.GritzServiceHandler
	// ... existing fields
}
```

```go
package runner // github.com/gritzapp/gritz/internal/runner

// ServeOptions configures Serve.
type ServeOptions struct { // new
	Client       gritzclient.Client
	ServerURL    string
	Token        string // the runner's key, for the notification stream
	Backend      backend.Backend
	StateDB      string // runner.db path
	Workspaces   *workspace.Config
	RunnerID     string
	Concurrency  int
	PollInterval time.Duration
	LostTimeout  time.Duration // 0 → TaskRunner's default
	ReadyTimeout time.Duration // 0 → TaskRunner's default
	Log          *slog.Logger
}

// Serve runs the runner until ctx ends: it opens the state DB, registers the
// workspaces, restores sandboxes, delivers runner events, follows server
// notifications and drives the tasks. It returns a FatalError when a durable
// write fails. // new
func Serve(ctx context.Context, opts ServeOptions) error
```

```go
package e2e // github.com/gritzapp/gritz/internal/e2e  // new

// The runner and workspace CreateTask uses where its request leaves them unset.
const (
	DefaultRunner    = "e2e"
	DefaultWorkspace = "e2e"
)

// Options configures a Harness; zero values take defaults.
type Options struct {
	// Runner stands up h.Backend and h.Runner; without it both are nil and
	// the test may act as the runner with Org.SubmitRunnerEvents.
	Runner       bool
	Concurrency  int
	SuspendAfter time.Duration
	Commands     []string
	Secrets      map[string]string
	PollInterval time.Duration
	Timeout      time.Duration
}

// Harness is a server, and optionally a local backend and a runner, for one
// test.
type Harness struct {
	Server  *Server
	Org     *Org     // the default org
	Backend *Backend // nil unless Options.Runner
	Runner  *Runner  // nil unless Options.Runner
}

// New stands up a harness and registers its teardown with t.Cleanup.
func New(t *testing.T, opts *Options) *Harness

// Server is the harness's API server.
type Server struct {
	// URL is the server's base URL.
	URL string
	// Store is the server's store, an escape hatch for state no RPC can reach.
	Store *store.Store
	// ... unexported
}

// Client returns a client that calls the server with token.
func (s *Server) Client(token string) gritzclient.Client

// CreateOrg creates an org with its own user, and registers the harness's
// workspace in it.
func (s *Server) CreateOrg() *Org

// Mock installs m, replacing any previous mock: calls go to m's non-nil
// <Method>Func fields, and every method left nil goes to the real server.
// Mock(nil) removes it.
func (s *Server) Mock(m *gritzclient.ClientMock)

// Org is an org with its owner.
type Org struct {
	ID int64
	// Owner is the user CreateOrg made, owning the org.
	Owner *Member
	// Client is shorthand for Owner.Client.
	Client gritzclient.Client
	// ... unexported
}

// Member is a user acting in an org.
type Member struct {
	UserID string
	Email  string
	// Client makes real API calls as the user, acting in the org with admin
	// scopes, as the web UI does.
	Client gritzclient.Client
	// Resp is the AddOrgMember response that added the user; nil for an org's
	// owner.
	Resp *gritzv1.AddOrgMemberResponse
}

// CreateTask creates a task, filling the harness defaults where req leaves
// them unset. It is always a draft. req may be nil.
func (o *Org) CreateTask(req *gritzv1.CreateTaskRequest) *Task

// WaitTask waits until cond accepts task id, and returns it.
func (o *Org) WaitTask(id int64, cond func(*gritzv1.Task) bool) *gritzv1.Task

// WaitEvent waits until one of task id's events matches, and returns it.
func (o *Org) WaitEvent(id int64, match func(*gritzv1.Event) bool) *gritzv1.Event

// CreateKey creates a key in the org, with the admin scopes the RPC grants.
func (o *Org) CreateKey(req *gritzv1.CreateKeyRequest) *Key

// Key is an xat_ key in an org.
type Key struct {
	ID string
	// Client calls the server with the key.
	Client gritzclient.Client
	// Resp is the CreateKey response the key was made from; nil for a key
	// from CreateScopedKey, which doesn't go through the RPC.
	Resp *gritzv1.CreateKeyResponse
}

// CreateScopedKey creates a key in the org holding scopes. Its Resp is nil.
func (o *Org) CreateScopedKey(scopes authscope.Scopes) *Key

// TaskToken is a task JWT, as the runner mints it.
type TaskToken struct {
	Token string
	// Client calls the server with the token.
	Client gritzclient.Client
	// Resp is the CreateTaskToken response the token was made from.
	Resp *gritzv1.CreateTaskTokenResponse
}

// CreateTaskToken mints a task token, as the runner does.
func (o *Org) CreateTaskToken(req *gritzv1.CreateTaskTokenRequest) *TaskToken

// UpdateTask updates a task, as the task page does.
func (o *Org) UpdateTask(req *gritzv1.UpdateTaskRequest)

// ArchiveTask archives a task, as the task page does.
func (o *Org) ArchiveTask(req *gritzv1.ArchiveTaskRequest)

// SubmitRunnerEvents reports runner events as the org's runner does, with its
// runner key.
func (o *Org) SubmitRunnerEvents(req *gritzv1.SubmitRunnerEventsRequest)

// GetTask returns the task.
func (o *Org) GetTask(req *gritzv1.GetTaskRequest) *gritzv1.Task

// ListKeys returns the org's keys.
func (o *Org) ListKeys(req *gritzv1.ListKeysRequest) []*gritzv1.Key

// AddOrgMember adds a user to the org as its owner does, and returns the user
// acting in the org.
func (o *Org) AddOrgMember(req *gritzv1.AddOrgMemberRequest) *Member

// ListOrgMembers returns the org's members.
func (o *Org) ListOrgMembers(req *gritzv1.ListOrgMembersRequest) []*gritzv1.OrgMember

func (o *Org) CancelTask(req *gritzv1.CancelTaskRequest)
func (o *Org) RegisterWorkspaces(req *gritzv1.RegisterWorkspacesRequest)
func (o *Org) WaitRunnerEvents(runner string, count int) string
func (o *Org) CreateOrg(req *gritzv1.CreateOrgRequest) *Org
func (o *Org) ListTasks(req *gritzv1.ListTasksRequest) []*gritzv1.Task
func (o *Org) ListEventsByTask(req *gritzv1.ListEventsByTaskRequest) []*gritzv1.Event
func (o *Org) GetEvent(req *gritzv1.GetEventRequest) *gritzv1.Event
func (o *Org) ListExternalEvents(req *gritzv1.ListExternalEventsRequest) []*gritzv1.Event
func (o *Org) TestEvent(req *gritzv1.TestEventRequest) *gritzv1.TestEventResponse
func (o *Org) CreateLink(req *gritzv1.CreateLinkRequest) *gritzv1.TaskLink
func (o *Org) SetRoutingRules(req *gritzv1.SetRoutingRulesRequest) []*gritzv1.RoutingRule
func (o *Org) GetRoutingRules(req *gritzv1.GetRoutingRulesRequest) []*gritzv1.RoutingRule
func (o *Org) AppendLogChunk(req *gritzv1.AppendLogChunkRequest)
func (o *Org) ListLogChunksByTask(req *gritzv1.ListLogChunksByTaskRequest) []*gritzv1.LogChunk
func (o *Org) CreateSchedule(req *gritzv1.CreateScheduleRequest) *gritzv1.Schedule
func (o *Org) GetSchedule(req *gritzv1.GetScheduleRequest) *gritzv1.Schedule
func (o *Org) ListSchedules(req *gritzv1.ListSchedulesRequest) []*gritzv1.Schedule
func (o *Org) ListWorkspaces(req *gritzv1.ListWorkspacesRequest) []*gritzv1.RegisteredWorkspace
func (o *Org) ListOrgs(req *gritzv1.ListOrgsRequest) []*gritzv1.Org

// Notifications is a subscription to an org's notifications on the /events
// stream.
type Notifications struct{ ... }

// Subscribe subscribes as the org's owner and returns once the subscription
// is live.
func (o *Org) Subscribe() *Notifications

// Next waits for the next notification and returns it.
func (n *Notifications) Next() model.Notification

// Backend is the harness's local backend, and the backend.Backend the runner
// is given.
type Backend struct{ ... }

// Create, Open, ValidateWorkspace and Close implement backend.Backend, through
// any installed mocks.

// Dir returns the task's sandbox directory.
func (b *Backend) Dir(taskID int64) string

// WaitSandbox waits until the task's sandbox has status want.
func (b *Backend) WaitSandbox(taskID int64, want backend.Status)

// KillDriver kills the task's driver with SIGKILL, leaving its sandbox.
func (b *Backend) KillDriver(taskID int64)

// Mock installs m, replacing any previous mock: m's non-nil methods replace
// the backend's. Mock(nil) removes it.
func (b *Backend) Mock(m *backend.BackendMock)

// MockSandbox installs m for the task's sandbox, replacing any previous one:
// m's non-nil methods replace the sandbox's. MockSandbox(taskID, nil) removes
// it.
func (b *Backend) MockSandbox(taskID int64, m *backend.SandboxMock)

// Runner is the harness's runner.
type Runner struct{ ... }

// Restart stops the runner and starts a new one on the same runner.db and
// backend.
func (r *Runner) Restart()

// Task is a task in an org.
type Task struct {
	ID int64
	// Resp is the CreateTask response the task was made from.
	Resp *gritzv1.CreateTaskResponse
	// ... unexported
}

// Script adds an instruction carrying steps.

// AddInstruction adds an instruction with text.
func (t *Task) AddInstruction(text string)

// Start starts the task, or wakes it.
func (t *Task) Start()

// Cancel cancels the task.
func (t *Task) Cancel()

// Restart restarts the task.
func (t *Task) Restart()

// Archive archives the task.
func (t *Task) Archive()

// Unarchive unarchives the task.
func (t *Task) Unarchive()

// Get returns the task.
func (t *Task) Get() *gritzv1.Task

// Events returns the task's events, oldest first.
func (t *Task) Events() []*gritzv1.Event

// Links returns the task's links.
func (t *Task) Links() []*gritzv1.TaskLink

// Log returns the task's shipped log.
func (t *Task) Log() string

// WaitStatus waits until the task has status and returns it.
func (t *Task) WaitStatus(status gritzv1.TaskStatus) *gritzv1.Task

// WaitEvent waits until one of the task's events matches and returns it.
func (t *Task) WaitEvent(match func(*gritzv1.Event) bool) *gritzv1.Event

// ReadFile returns the content of name in the task's sandbox directory.
func (t *Task) ReadFile(name string) string

// WaitFile waits until name in the task's sandbox directory holds want.
func (t *Task) WaitFile(name, want string)
```

**CLI:** `gritz driver --root DIR`. `gritz runner --backend local --local-root
DIR`, where the local backend's binary is the running executable.

**How it fits:**

- **Setup.** `e2e.New` serves `server.New(...).Handler()` on httptest, with
  `h.Server`'s mock router in `Options.Wrap`, and creates `h.Org` and its keys
  in teststore. It wraps a `local.Backend` on a fresh root in
  `h.Backend`, and runs `runner.Serve` with that wrapper on a goroutine.
- **The runner.** `gritz runner` calls `Serve` after parsing its flags. The
  runner's `TaskRunner`s create and boot local sandboxes through `h.Backend` and
  its mocks. `Boot` starts `gritz driver --root DIR` from `prebuilt/`.
- **Inside the sandbox.** The driver resolves every path under `--root`, sets
  `GRITZ_RUN_VERSION`, and runs the puppet agent, which reports the run to the
  harness and does what the test tells it. (Was: the dummy `e2escript.Parse`s its
  prompt and runs the steps. `Tool` steps start the injected MCP server from
  `BinaryPather.BinaryPath()`.
- **Every call to the server** comes from the harness's `Client`, the runner,
  the driver or the MCP server. Each passes the mock router, which sends it
  to the installed `ClientMock` or to the real handler.
- **The test** drives tasks through `Task`, which calls the user-facing RPCs and
  reads the sandbox directory through `h.Backend`.

## Implementation Plan

runner-event-replica slice 8 (the driver acts on commands) has landed; slices
9–11 haven't. Slices 1–9 below don't depend on the runner's internals. They can
land now, and the scenarios then act as the regression net for slices 9–11.
Slices that must wait are marked.

1. **Driver `Root`.** Delivers: `driverserver.Options.Root` and
   `ExecutorOptions.Root` in place of `RecordPath`, `EventsPath`, `LogPath`,
   `Config` and `FileRoot`; `gritz driver --root`; `testrunner.NewDriver` and
   `driverserver_test.go`'s `setup` set only `Root`. Depends on: nothing.
   Verifiable by: the existing `driverserver` tests on `Root`.
2. **`GRITZ_RUN_VERSION`.** Delivers: `Executor.execute` adds it to the run's
   environment. Depends on: nothing. Verifiable by: `TestRun_Env` sees it, and a
   spec env or secret of that name doesn't override it.
3. **Local backend.** Delivers: `internal/runner/backend/local` (with `Kill`),
   `backend.BinaryPather` with `TaskManager.spec` using it, and `gritz runner
   --backend local --local-root`. Depends on: (1). Verifiable by: `local` tests
   modelled on Docker's `TestSandbox_Lifecycle` against the prebuilt driver.
   - create → suspended, boot → up, a `Run` stream over `Dial`;
   - suspend → suspended with files kept, boot again, destroy → gone;
   - a killed driver reports suspended;
   - a second `Backend` on the same root sees and suspends a sandbox the first
     booted;
   - two sandboxes run side by side.
4. **Puppet agent.** Delivers: the `puppet` agent type and the harness's control
   endpoint, with the protocol and the run-handle API settled then (open in this
   proposal). Depends on: nothing. Verifiable by: an agent test driving one run
   through each command against a stdio MCP server.
5. **`runner.Serve`.** Delivers: the runner's wiring moved out of
   `command/runner.go` into `runner.Serve`, with `LostTimeout` and
   `ReadyTimeout` in its options. Depends on: nothing. Verifiable by: `gritz
   runner` behaving as before against a dev server; this slice adds no tests.
   Slice 9 changes what `Serve` calls (`Load`, `TaskManager.Run`), so whichever
   lands second rebases.
6. **Server wrapping.** Delivers: `server.Options.Wrap`. Depends on: nothing.
   Verifiable by: the e2e server tests, which mock through it.
7. **Harness and `TestStart`.** Delivers: `internal/e2e` with the `Server`,
   `Backend`, `Runner` and `Task` handles, their waits, and `TestStart`; no mocks
   yet. Depends on: (1)–(6). Verifiable by: `TestStart` passes under `mise run
   test`, also with `-count=5 -race`; a deliberately broken assertion reports the
   last state seen.
8. **Mocks and fault scenarios.** Delivers: `Server.Mock`, `Backend.Mock`,
   `Backend.MockSandbox`, `KillDriver`, and `TestUnreportedRun`,
   `TestSandboxFails`, `TestDriverUnreachable`, `TestDriverDies_MidRun`.
   Depends on: (7). Verifiable by: the scenarios pass; a test that a mocked
   method records its calls, that a second mock replaces the first, and that
   after `Mock(nil)` the real implementation answers again.
9. **The other scenarios.** Delivers: every scenario above except
   `TestSuspendAfter*`, in PRs of two or three. All of them describe behaviour
   the runner already has after slice 8: archive through `Prune`, reattach
   through `Load`, the lost driver through `TaskLostEvent`. A scenario that fails
   has found a bug, and lands with its fix or is skipped with an issue link.
   Depends on: (7), and (8) for those with mocks. Verifiable by: the scenarios
   pass.
10. **Delete constructor-panic tests.** Delivers: `TestNewTaskRunner` removed.
    Depends on: nothing.
11. **runner-event-replica slices 9–11 (not part of this proposal), verified
    with scenarios.** Each slice deletes its (c) tests with the code. The
    behaviour lists under their "Verifiable by" are covered by the scenarios from
    (8) and (9), which must keep passing, instead of new `runner_test` cases on
    the fake backend.
12. **`suspend_after` scenarios.** Delivers: `Options.SuspendAfter`,
    `TestSuspendAfter`, `TestSuspendAfter_Window`. **Waits for runner-event-replica
    slice 10.** Verifiable by: the scenarios pass.
13. **Port pass.** Delivers: every (a) test deleted, including most of
    `driverserver_test.go`, and the driver-loop table test for the cases no
    scenario reaches. **Waits for slice 11**, the last of runner-event-replica,
    as #1890 asks: until then the code under test still moves, and (a) tests are
    cheap to keep while it does. Depends on: (9), (12). Verifiable by: `mise run
    test` passes; coverage of `internal/runner/...` from the e2e package
    (`-coverpkg`) doesn't drop on the paths the deleted tests covered, apart from
    the gaps named above.
14. **Delete pass.** Delivers: the remaining (b) runner tests moved onto the
    local backend; `backend/fake` and `fake_test.go` deleted; `testrunner`
    reduced to nothing, its last helpers moved into the test files that use them.
    Closes #1890. Depends on: (13). Verifiable by: `mise run test`; nothing
    imports `backend/fake` or `testrunner`.

## Trade-offs

- **The Docker backend instead of a local one.** It is more realistic, but it
  needs a daemon in CI, takes seconds per container (#1859: ~9.5s to boot
  four), and its isolation is exactly what the scenarios don't test. The Docker
  backend keeps its own contract tests. The local backend runs the same prebuilt
  driver binary, so only the container boundary is missing.
- **An in-process driver** (as `testrunner.NewDriver` does). It is faster, but it
  can't die, be suspended or survive a runner restart the way a process does. It
  shares the test's `HOME` and working directory, and it never runs the
  `gritz driver` command or the socket transport. `TestRunnerRestart_MidRun` and
  `TestDriverDies_MidRun` need a real process.
- **A `Driver` handle, or mocks of the driver.** These would reach the driver's
  internals, which runner-event-replica keeps moving, and the driver has no
  public API to wrap. Everything a driver can see go wrong reaches it from
  outside, through the server's RPCs and the sandbox, so mocks there can inject
  every fault without opening it up.
- **Hand-written fakes for fault injection** (`backend/fake`, `testrunner.Server`).
  A fake restates the real component, and drifts from it. A moq mock replaces
  only the methods a test sets, for only as long as it is installed. It is
  generated, so it can't drift, and it records its calls.
- **Untyped hooks with call-through** (a `next` handler, `connect.AnyRequest`).
  Hooks would need a type assertion each and a second, untyped API next to the
  mocks the repo already generates. Mocks are typed with no generics. Their only
  loss is call-through, which "remove the mock once the failure is seen" covers for
  every scenario but a stream cut mid-flight.
- **Exposing the real instances** (`Real` on each handle). Tests that hold the
  apiserver or the backend directly bypass the mocks, and drift towards
  asserting on internals. `h.Org.Client` and `h.Backend` itself give the same
  reach through the same path every other caller takes.
- **`testrunner.Server` and `ClientMock` instead of the apiserver.** They miss
  command events, the xid watermark, the run boundary and the server's version
  guards: everything #1868 was about. They also make every test restate the
  server's behaviour, which is how the two sides drifted apart.
- **The dummy agent running a script from its instructions** (a fenced block of
  steps such as `sh`, `sleep`, `block`, `tool`, `error`). It gives per-run
  behaviour through the real prompt path, but scenarios then depend on timing
  (sleeps, polling files to know where a run is) and on a script format shared
  by the test and the agent. The puppet gives the same per-run control
  explicitly, with no timing.
- **The dummy dumping its prompts to a file.** Enough to assert which
  instructions reached which run, but it can't control how long a run lasts or
  how it ends. The puppet captures prompts as well.
- **The workspace-level dummy config** (`DummyOptions`). Fixed per runner, so
  run 1 and run 2 can't differ.
- **Asserting on runner internals** (`runner.db`, fake-backend counters). That is
  more precise, but those are exactly the assertions that churn with every
  runner-event-replica slice. Observable state is what has to survive them.
- **A harness-local copy of the runner wiring** instead of `runner.Serve`. It
  is less churn now, but it would drift from `gritz runner`, and the harness
  would stop testing the real assembly order (`HandleEvents` before `Load`,
  `Load` before `Run`).

## Open Questions

- **The puppet's protocol and API.** See "The puppet agent": transport, message
  shapes, timeouts, cleanup and the harness's run handle are open.
- **A stale prebuilt.** The driver comes from `prebuilt/`, built by `mise run
  build`. A developer who changes the driver and runs `go test ./internal/e2e`
  without rebuilding tests the old driver, silently. A `TestMain` could instead
  `go build` the binary once per package run (cached, a few seconds), at the cost
  of departing from the decided "use the prebuilt". Alternatively, the harness
  could compare the prebuilt's embedded VCS revision with the test binary's and
  fail on a mismatch.
- **Call-through.** A mocked method replaces the real one. Some cases need to
  wrap the real one instead: a `Dial` that cuts the real `Run` stream mid-flight,
  so the runner reconnects to a driver that is still alive
  (`TestTaskRunnerRun_Reattach`), or a submit that fails only for `stopped`.
  Would a later `Mock` option make the real implementation reachable from the
  mock, for example a `Real` passed to the `Func`?
- **Streaming RPCs.** `GritzService` has none, so `Mock` covers every RPC today.
  With connect's `simple` option, a streaming method's client signature (it
  returns a `*connect.ServerStreamForClient`) differs from its handler's (it
  takes a `*connect.ServerStream`), so `ClientMock` can't stand in for one. When
  the first streaming RPC arrives, either generate a moq mock of
  `gritzv1connect.GritzServiceHandler` for `Mock` to take instead (its fields
  then carry the handler's streaming signatures, which `WrapStreamingHandler`
  can route to), or leave streaming RPCs unmockable until a scenario needs one.
- **Gaps from porting `driverserver`.** Keep-alives matter only for streams
  quiet for longer than the runner's 45s idle timeout, and no scenario is that
  slow. Provisioning failures are unreachable from outside. Accept both, or add
  one slow scenario (skipped under `-short`) for keep-alives?
- **Amending runner-event-replica.** Slice 9 and 10's "Verifiable by" lists
  name `runner_test` cases on the fake backend. Should that proposal be edited
  to point at the scenarios once slice 9 here lands?
- **`gritz runner --backend local` for users.** It runs agents with the runner's
  own privileges and filesystem. Should the flag be hidden, or refuse to start
  without an explicit `--local-unsafe`-style acknowledgement?
- **Workspace environment on the local backend.** It ignores `container:`, so
  `container.environment` doesn't reach the run. Secrets and setup commands
  cover the scenarios; should the local backend apply `container.environment`
  through `RunEnv`, as Lambda applies its section?
- **CI time.** About thirty scenarios of a few seconds each, run in parallel,
  should add about a minute. If the suite grows, should it get its own `mise`
  task and CI job?
