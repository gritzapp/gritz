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
 test ──RPCs (web UI / MCP)──▶ apiserver (httptest, teststore Postgres)
                                 ▲   │ SSE notifications
          SubmitRunnerEvents,    │   ▼
          logs, MCP tools        │ runner (runner.Serve, temp runner.db)
                                 │   │ local backend
                                 │   ▼
                               driver process (prebuilt gritz driver --root DIR)
                                     │
                                     ▼
                               dummy agent running the instruction's e2escript
```

Only two things are fake:

- **The sandbox** is a directory on the runner host, and the driver is an
  ordinary child process. The local backend provides it.
- **The agent** is the existing dummy agent (`internal/runner/agent/dummy.go`).
  It runs a script that the test puts in the task's instructions.

Everything else is production code: the apiserver behind its real auth, the
runner's real wiring on its own SQLite `runner.db`, the prebuilt driver binary
the Docker backend ships, the real driver.v1 transport over a Unix socket, the
injected `gritz` MCP server, log shipping and secret masking.

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
| `Status` | `gone` if the directory is missing; `up` if the pid file names a live process whose `/proc/<pid>/cmdline` carries `--root DIR`; `suspended` otherwise. |
| `Open` | Rehydrates from the handle, with no I/O. |
| `Close` | Does nothing. Drivers outlive the backend, as containers outlive a Docker client. |
| `ValidateWorkspace` | Accepts every workspace. The local backend reads no runtime section (`container:`, `lambda_microvm:`). |

**Who reaps the driver.** `Boot` starts a goroutine that `Wait`s for the process
and then removes the pid file. That goroutine belongs to the OS process, not to
the `Backend` value, so after `h.RestartRunner()` the old backend's goroutine
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

### The dummy agent runs a script from its instructions

The workspace-level `agent: dummy:` config (`DummyOptions`: `Sleep`,
`ToolCalls`, `Commands`, `Error`) applies to every task and every run, so a test
can't make run 1 block and run 2 exit. The script moves into the instructions:
the test adds an instruction that carries a script, and the dummy runs the script
it finds in its prompt.

**Encoding.** A script is a fenced block with the info string `gritz-e2e`, with
one JSON step per line:

````
```gritz-e2e
{"sh":"echo $GRITZ_RUN_VERSION >> runs"}
{"tool":"report","args":{"message":"ready"}}
{"block":true}
```
````

`internal/e2e/e2escript` owns both the encoding and the parser, so the format is
defined in one place. It is a normal package without `testing`, because the dummy
agent (production code) imports it.

**Steps:**

| Step | The dummy |
|---|---|
| `Sh(cmd)` | Runs `sh -c cmd` in its working directory (the sandbox directory) with the run's environment. It uses the same process group, `SIGTERM` on cancel and log tee as setup commands (`Driver.setup`). A non-zero exit fails the run. |
| `Sleep(d)` | Sleeps for `d`, or until the run is stopped. |
| `Block()` | Waits until the run is stopped, then returns the context's error. With `ErrStop` as the cause, the run reports `stopped`. |
| `Tool(name, args)` | Calls the tool on the injected `gritz` MCP server, through the existing `connectMCP`. A tool error fails the run. |
| `Error(msg)` | Ends the run with `msg` as its error, so it reports `failed` with that reason. |

**Which instructions are new in this run.** The dummy parses its prompt and
nothing else. The prompt already contains exactly the instructions that are new
to the run, because that is what `agentprompt.Render` gets from the driver. On
the first run the brief includes every instruction from the head of the task. On
a wake it includes the instruction and external events after the driver's
prompted-up-to position: `Config.NextEventToken` today, and the event-file
position after runner-event-replica slice 11. Instruction text is rendered
verbatim (`event-instruction.golden`), so the fence survives. If several new
instructions carry scripts, their steps run in event order.

The position advances only when a run's prompt returns without error
(`Driver.runAgent` saves it after `a.Prompt`). So a run that is stopped,
restarted or fails hands its script to the next run again. The restart scenario
depends on this, and it is the real at-least-once behaviour a Claude agent gets
too.

**Fallback.** A prompt with no `gritz-e2e` block runs the workspace's
`DummyOptions` as today, so `agent: dummy:` stays the default and existing users
of it are unaffected. A malformed block fails the run with the parse error, which
shows in the task's `failed` reason.

**`GRITZ_RUN_VERSION`.** Nothing exposes the run version to the agent today. The
agent's environment has only `GRITZ_TASK_ID`, `GRITZ_TOKEN` and `GRITZ_SERVER`
(`TaskManager.spec`), and the version is known only to the driver: the run record
in `Executor.Start`, and `task.GetVersion()` in `Driver.Run`. `Executor.execute`
adds `GRITZ_RUN_VERSION=<version>` to the run's environment, after the spec's env
and secrets so neither can override it. Setup commands and the agent both see
it. Scripts use it to record which run they were (`echo $GRITZ_RUN_VERSION >>
runs`), and it is useful to real agents and hooks too.

### Harness

`internal/e2e` stands the system up per test:

```go
func TestStart(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, nil)
	task := h.CreateTask()
	task.Script(e2escript.Sh("echo $GRITZ_RUN_VERSION > ran"))

	task.Start()

	got := task.WaitStatus(gritzv1.TaskStatus_COMPLETED)
	assert.Equal(t, got.Version, int64(1))
	assert.Equal(t, task.ReadFile("ran"), "1\n")
}
```

**`e2e.New(t, opts)`** builds the following, and tears it all down with
`t.Cleanup`:

1. **Org and keys.** `teststore.New(t)` and `teststore.CreateOrg` create a fresh
   org with a user. Two `xat_` keys are made for that user with `store.CreateKey`,
   the same bootstrap `--no-auth` uses (`command/server.go`): a user key for the
   harness's own client, and a runner key for the runner. Isolation comes from the
   org, as for every teststore test.
2. **Server.** `server.New(server.Options{...}).Handler()` on `httptest.NewServer`.
   It uses `apiauth.New` with `server.NewStoreKeyValidator`, a fresh app key from
   `apiauth.CreateAppPrivateKey()` (task tokens), and a local pubsub feeding
   `notifyserver`, so SSE notifications are live. The archiver and scheduler
   aren't started; no scenario needs them.
3. **Runner.** `runner.Serve` (below) with a `gritzclient` on the runner key, the
   local backend on a fresh root, `runner.db` in a temp directory, runner id
   `e2e`, and one workspace `e2e` whose agent is the dummy. The runner registers
   that workspace with the server itself (`RegisterWorkspaces`), so `CreateTask`
   finds it through the production path. `runner.Serve` subscribes to the
   harness server's notifications, so commands reach the runner without waiting
   for the poll.
4. **Cleanup**, in reverse order: stop the runner and wait for `Serve` to return;
   `Destroy` every sandbox under the root, which kills the drivers; remove the
   root; close the server; `teststore` drops the org.

**`runner.Serve`.** `gritz runner`'s wiring is all inline in the CLI action
(`internal/command/runner.go`): it builds the `runnerstore`, `taskevents.Log`,
`RunnerEventOutbox` and `TaskManager`, then runs `RegisterWorkspaces`,
`HandleEvents`, `Load`, the outbox, the notification client and `Run`, in that
order. The harness has to run the same sequence, not a copy of it. The sequence
moves into `runner.Serve(ctx, ServeOptions)`. The command keeps flag parsing,
config loading and backend selection, and calls `Serve`. `Serve` returns when
`ctx` ends, leaving sandboxes up as a shutdown does today.

**One harness per test.** Each harness has its own org, server, runner,
`runner.db` and sandbox root, so tests can call `t.Parallel()`. They share one
Postgres. The xid watermark is cluster-wide, so a parallel test's open
transaction can delay another test's events by the length of that transaction.
That is real behaviour and bounded by short transactions.

**Commands go through the same RPCs the web UI uses** (`webui/src/routes/tasks.$id.tsx`,
`tasks.new.tsx`), on the user key:

| Harness | RPC |
|---|---|
| `h.CreateTask()` | `CreateTask{draft: true, runner: "e2e", workspace: "e2e"}`, as the new-task page does |
| `Task.Script(steps...)` | `UpdateTask{add_instructions: [e2escript.Encode(steps...)]}` |
| `Task.AddInstruction(text)` | `UpdateTask{add_instructions: [text]}` |
| `Task.Start()` | `UpdateTask{start: true}` |
| `Task.Cancel()`, `Restart()`, `Archive()`, `Unarchive()` | `CancelTask`, `RestartTask`, `ArchiveTask`, `UnarchiveTask` |
| `Task.Get()` | `GetTask` |
| `Task.Events()` | `ListEventsByTask`, every page |
| `Task.Links()`, `Task.Log()` | `ListLinks`; `ListLogChunksByTask`, concatenated |

The composer sends `add_instructions` and `start` in one `UpdateTask`. The
harness splits them so a test can add a script before deciding how to start it,
which needs both halves of that RPC anyway. A wake is `Script(...)` then
`Start()`.

**Waits** poll every 20ms until `Options.Timeout` (default 30s), then fail
through `h.t` with the last state they saw:

- `WaitStatus(status)` returns the task. On failure it reports the status,
  version and command last seen, plus the last few events.
- `WaitEvent(match)` returns the first event `match` accepts. On failure it
  reports every event.
- `WaitSandbox(backend.StatusUp | StatusSuspended | StatusGone)` reads
  `local.Backend.Open(handle).Status()` on the harness's own backend value over
  the same root. On failure it reports the status last seen. A task that never
  had a sandbox reports `gone`.
- `WaitFile(name, want)` waits until the sandbox file holds `want` (an addition
  to the decided API). Scenarios need a precise "the script has got this far"
  before they act, and `WaitStatus(RUNNING)` fires at `started`, before the
  agent runs.

`ReadFile(name)` reads `Root/<id>/<name>` and fails the test if it's missing.

**Only observable outcomes.** Tests assert on task status and version, the
event timeline, links, the shipped log, sandbox state through the backend
interface, and the files the script wrote. They never open `runner.db`, read
`/gritz/run.json` or `events.jsonl`, or call runner or driver methods. That is
what lets these tests survive runner-event-replica slices 9–11 unchanged.

**Options:**

```go
type Options struct {
	Concurrency  int               // runner slots; default 2
	SuspendAfter time.Duration     // the e2e workspace's suspend_after (from slice 10); 0 → workspace default
	Commands     []string          // the workspace's setup commands
	Secrets      map[string]string // the workspace's secrets
	PollInterval time.Duration     // the runner's; default 1s, a backstop to SSE
	Timeout      time.Duration     // every wait's; default 30s
}
```

**The driver binary** is `prebuilt/gritz-linux-$GOARCH`, which the tests already
require (`mise run build`, as `TestTaskManagerStart` and the Docker backend tests
do). The prebuilt can be stale against the source; see Open Questions.

### Scenarios

Each scenario is one top-level test in `internal/e2e`. "Run file" means a script
that appends `$GRITZ_RUN_VERSION` to `runs`, so `runs` records which runs the
agent executed in this sandbox.

| Test | Arrange / act | Assert |
|---|---|---|
| `TestStart` | `Script(Sh(append runs))`; `Start` | `COMPLETED` at v1; `runs` = `1`; timeline has `SANDBOX_STARTED`, `SANDBOX_EXITED` |
| `TestCancel_MidRun` | `Script(Sh(append runs), Block())`; `Start`; `WaitFile(runs, 1)`; `Cancel` | `CANCELLED`; `runs` = `1`; one `SANDBOX_STARTED` |
| `TestRestart_MidRun` | as above, then `Restart` | `WaitFile(runs, 1 2)`: run 2 got the same, uncompleted script; `Get().Version` = 2; `Cancel` → `CANCELLED` |
| `TestWake_MidRun` (#1868) | `Script(Sh(append runs), Sleep(3s))`; `Start`; `WaitFile(runs, 1)`; `Script(Sh(append runs))`; `Start` | right after the wake, `Get()` is `RUNNING` at v1 with command `start`, so the wake landed on the running run and the test fails rather than passing vacuously if run 1 already ended; then `COMPLETED` at **v2**; `runs` = `1 2`; run 1's script did not run again |
| `TestCancel_PendingStart` | `Concurrency: 1`. A: `Script(Sh(append runs), Block())`, `Start`, `WaitFile`. B: `Script(Sh(touch ran))`, `Start`, `WaitStatus(PENDING)`, `Cancel` | B `CANCELLED`. Then cancel A, and start a third task C to `COMPLETED`; C's start is later in the log than B's cancel, so it acts as a barrier. After C completes, B has no `SANDBOX_STARTED` and `WaitSandbox(gone)`: B never ran, even once the slot was free |
| `TestArchive` | `Script(Sh(touch f))`; `Start`; `COMPLETED`; `Archive` | `WaitSandbox(gone)`. Then `Unarchive`, `Script(Sh(test ! -e f && append runs))`, `Start` → `COMPLETED`; a fresh sandbox, not a revived one |
| `TestRunnerRestart_MidRun` | `Script(Sh(append runs), Block())`; `Start`; `WaitFile`; `h.RestartRunner()`; `Cancel` | `CANCELLED`; `runs` = `1`; one `SANDBOX_STARTED`. The new runner reattached and carried the cancel to the driver, without starting the run again |
| `TestSuspendAfter` (slice 10) | `SuspendAfter: 300ms`. `Script(Sh(append runs))`; `Start`; `COMPLETED`; `WaitSandbox(suspended)`; `Script(Sh(append runs))`; `Start` | `COMPLETED` at v2; `runs` = `1 2` in the same file, so the directory survived and nothing re-created it; `WaitSandbox(suspended)` again |
| `TestSuspendAfter_Window` (slice 10) | default `suspend_after`; two runs back to back | the sandbox stays `up` between them and after; `runs` = `1 2` |

Other scenarios worth having:

| Test | Arrange / act | Assert |
|---|---|---|
| `TestAgentError` | `Script(Error("boom"))`; `Start` | `FAILED`; `SANDBOX_FAILED` carries `boom` |
| `TestTools` | `Script(Tool("report", {message}), Tool("create_link", {url, subscribe: true}), Tool("update_my_task", {name}))` | a report event with the message; the subscribed link; the new name. Covers the injected MCP server, the task token, and the `BinaryPather` path |
| `TestSecretsMasked` | `Secrets: {E2E_SECRET: s3cret}`; `Script(Sh(echo $E2E_SECRET))` | `Log()` contains `[gritz:masked E2E_SECRET]` and not `s3cret` |
| `TestSetupCommands` | `Commands: [echo setup >> setup]`; two runs | `setup` = `setup` once: commands ran on the first run only |
| `TestRestart_Completed` | complete run 1, `Restart` | `COMPLETED` at v2 in the same sandbox |
| `TestDriverDies_MidRun` | `Script(Sh(append runs), Sh(kill -9 $PPID))` (the dummy's `sh` is a child of the driver) | `FAILED`; the sandbox `suspended`. Covers the lost-driver path (`TaskLostEvent` today, `drive`'s reattach timeout after slice 9) |
| `TestConcurrent` | `Concurrency: 2`, three tasks each `Sh(append runs)` | all `COMPLETED`; each sandbox's `runs` = `1`: no cross-talk between sandboxes |

`TestDriverDies_MidRun` waits out the runner's `LostTimeout` (10s by default), so
`ServeOptions` carries it and the harness sets it to 2s.

### Porting the existing tests

The categories:

- **(a) Covered.** A scenario covers it. Delete it once the scenario lands.
- **(b) Keep.** It covers tricky local logic that end-to-end tests can't pin
  precisely or can't reach (fault injection, broken disks, encoding edge cases).
  Keep it as a focused unit test.
- **(c) Intermediate.** It tests a design that runner-event-replica slices 8–11
  remove anyway. Delete it with that code in the slice, without porting it. Where
  the behaviour lives on, a scenario or a (b) test in the slice takes over.

#### `internal/runner` (`taskmanager_start_test.go`)

| Test | | Notes |
|---|---|---|
| `TestTaskManagerStart` | a | `TestStart`, with no Docker daemon needed. Docker specifics stay in `backend/docker`'s tests |
| `TestTaskManagerSpec_PreCreatesLogDir`, `_Secrets`, `TestTaskManagerRunSpec_RunEnv` | b | Spec building: the 0777 directories for non-root containers, secrets kept out of `Env`, Lambda's `RunEnv`. End-to-end runs as one user with no `RunEnv`, so it can't see them. Rewrite against the spec builder, with no manager or fake backend |
| `TestTaskManagerFollow_StopWithoutSandbox` | c | Slice 9: `RunLoop`'s "stop with no `Run` open". The outcome is in `TestCancel_PendingStart` |
| `TestTaskManagerFollow_StopSignalled` | c | `Kill` and the driver `Stop` RPC go in slice 8. `TestCancel_MidRun` |
| `TestTaskManagerFollow_Start` | c | `handled`. "Act only on the latest command" moves to the driver loop's tests (b, slice 8) |
| `TestTaskManagerFollow_Restart` | c | Kill-then-start goes in slice 8. `TestRestart_MidRun` |
| `TestTaskManagerFollow_SlowDriver` | c | `handled` before readiness. A real driver's start-up latency is in every scenario |
| `TestTaskManagerFollow_StartStillRunning` | c | `ErrRunning` goes in slice 9 |
| `TestTaskManagerFollow_StartWaitsForSlot` | a | `TestCancel_PendingStart` (A holds the only slot), `TestConcurrent` |
| `TestTaskManagerFollow_StopSupersedesWaitingStart` | a | `TestCancel_PendingStart` |
| `TestTaskManagerFollow_Archive` | a | `TestArchive` |
| `TestTaskManagerFollow_ResumesFromHandled` | c | `handled` goes in slice 9. `TestRunnerRestart_MidRun` |
| `TestTaskManagerStart_Create`, `_Restart`, `TestTaskManagerWait`, `_Done` | c | `TaskRunner.Start`/`Wait` become `RunLoop` in slice 9. `TestStart`, `TestRestart_Completed` |
| `TestTaskManagerStart_CreateFails`, `_BootFails`, `_NotReady` | c → b | The API goes in slice 9. "A boot failure or a driver that never answers reports `failed` for the version and releases the slot" stays, and only fault injection reaches it. Slice 9 writes it as a `RunLoop` test |
| `TestTaskManagerStart_Gone` | c | Replaced by `TestDriverDies_MidRun` and slice 9's gone-sandbox rule |
| `TestTaskManagerKill`, `TestTaskManagerRemove`, `TestTaskManagerAfterDestroy` | c | `Kill` goes in slice 8; `Destroy` becomes archive handling in `RunLoop`. `TestArchive` |

#### `internal/runner` (`taskmanager_load_test.go`)

| Test | | Notes |
|---|---|---|
| `TestTaskManagerLoad_RestartReattaches` | a | `TestRunnerRestart_MidRun` |
| `TestTaskManagerLoad` (up) | a | `TestRunnerRestart_MidRun` |
| `TestTaskManagerLoad` (suspended, gone, status error, open fails), `TestTaskManagerLoad_VersionScopedBackstop` | b | The lost-run backstop (`failIfTaskRunning`) stays in slice 9. It needs a sandbox that died while no runner was watching, which is fault injection. Revisit with the `sandboxes.version` open question in runner-event-replica |
| `TestTaskManagerPrune` | c | `Prune` goes in slice 9 |
| `TestTaskManagerRun` | c + b | The prune half goes. The fatal half (a failed durable write ends `Run` with `FatalError`) stays |
| `TestTaskManagerRunning` | c | Goes with `TaskRunner.Running` |
| `TestTaskManagerDie_StopWithoutSandbox`, `TestTaskManagerHandleEvents_Fatal` | b | A broken disk can't be produced end to end |
| `TestTaskManagerGracefulShutdown_NoFatalCause` | a | `h.RestartRunner()` shuts down gracefully in every runner-restart scenario |
| `TestTaskManagerHandleEvents_Lost` | c | `TaskLostEvent` goes in slice 9. `TestDriverDies_MidRun` |

#### `internal/runner` (`taskrunner_test.go`, `internal_test.go`, `eventoutbox_test.go`)

| Test | | Notes |
|---|---|---|
| `TestNewTaskRunner` | delete | It tests constructor panics, which `CLAUDE.md` says not to test. Delete it in the first port PR |
| `TestTaskRunnerRun_Finished` | a | `TestStart` |
| `TestTaskRunnerRun_ForwardsEvents` | a | Every scenario after slice 8 depends on the driver receiving its commands over the forwarded stream, and `TestRunnerRestart_MidRun` resumes from `Hello`. The driver skips what it has stored (`TestRun_ReopenedStream`, b) |
| `TestTaskRunnerRun_Reattach` | b | A dropped stream with the driver still alive is reconnect-with-backoff logic in `drive`, and only a fault can cause it. Slice 9 keeps a version of it |
| `TestTaskRunnerRun_DriverUnreachable`, `TestTaskRunnerSend_Shutdown` | c | `TaskLostEvent`. `TestDriverDies_MidRun` |
| `TestTaskRunnerRun_NoSuchRun` | c | Attach-by-version goes in slice 8 |
| `TestTaskRunnerRun_NotReady` | c → b | As `_NotReady` above |
| `TestTaskRunnerRun_Shutdown` | a | `TestRunnerRestart_MidRun`: the sandbox stays up across the restart |
| `TestTaskRunnerBoot`, `_Create`, `_BootFails`, `_Concurrent`, `TestTaskRunnerRestore` | c | Boot, claiming and `ErrRunning` are reshaped by `RunLoop` in slice 9 |
| `TestDoubling` | b | Backoff arithmetic |
| `TestRunnerEventOutbox_Permanent`, `TestTaskManagerEventsSurviveRestart` | b | Retry classification and durability |

#### `internal/runner/driverserver`

| Test | | Notes |
|---|---|---|
| `TestRun` | a | `TestStart` |
| `TestRun_ProvisionsFiles`, `_ProvisionsOnce`, `_ProvisionFailure` | b | File modes regardless of umask, the provisioned marker, and the failure path |
| `TestRun_AttachDoesNotProvision`, `_Idempotent`, `_ReattachAfterDrop`, `_Stale`, `_Busy`, `_NotFound`, `_NextVersion` | c | Opening `Run` by version goes in slice 8. Slice 8's own driver-loop tests replace them (b): each command against the run record, a resent stream doing nothing, a fresh file acting only on its latest command, v5 then a pending v6 on one stream |
| `TestStop_NotRunning`, `TestStop_NoRun` | c | The `Stop` RPC goes in slice 8 |
| `TestRun_StoresEvents`, `_EventsSurviveRestart`, `_ReopenedStream` | b | Driver storage rules |
| `TestRun_KeepAlive`, `_SecondStream`, `_NotReported`, `TestNew_Reconcile`, `TestShutdown` | b | Transport and crash rules no scenario can time precisely |
| `TestRun_Env`, `TestEnv`, `TestSecrets` | b | Layering order; `GRITZ_RUN_VERSION` joins `TestRun_Env` |
| `TestRun_MasksSecretsInShippedLog` | a | `TestSecretsMasked`. The masking itself stays covered in `redact` and `logship` |
| `TestListenAndServe`, `_Unix` | b | The local backend depends on `_Unix`'s stale-socket handling |

#### Other runner packages

| Package / test | | Notes |
|---|---|---|
| `backend/fake` (`fake_test.go`, all 38) | c | They go with the fake (see below) |
| `testrunner` | c | Shrinks to what (b) tests use (`NewRunnerStore`, `Server`, the event builders), then goes into those packages' own test files |
| `taskevents` (all) | b | Replication, cursor, boot and blocking `Read` |
| `runnerstore`: `TestHandled`, `TestAppendEvents_CommandEventID` | c | Their columns are dropped in slice 9 |
| `runnerstore`: the rest, `TestTaskChanges` | b | Projection and storage |
| `eventfile`, `runrecord` (all) | b | Encoding, torn lines and reconcile |
| `driverclient`: `TestRun_HidesKeepAlives`, `_IdleStreamCancelled`, `_LiveStreamKept` | b | |
| `driverclient`: `TestStop_NotWatched` | c | The `Stop` RPC |
| `agent`: `TestDriverRun_DrainsEventsToTail`, `TestDrainEvents_RequestsServerSideTypeFilter`, `TestDriverRun_EventTokenNotAdvancedOnError` | c | `drainEvents` goes in slice 11 |
| `agent`: `TestDriverRun`, `_AgentConfiguredError` | a | `TestStart`, `TestAgentError` |
| `agent`: the other `TestDriverRun_*`, `TestDriverSetup_Cancel`, `TestRun_ForksIntoShell` | b | Submit-failure handling, log tee and shipping, the shell fork |
| `backend/docker`, `backend/lambdamicrovm`, `TestSpecClone` | b | Backend contracts; no scenario uses those backends |

**The fake backend.** After the port, the only users of `backend/fake` are the
(b) runner tests that need fault injection: a failing boot, a driver that never
answers, a dropped stream, and `Load` finding a dead sandbox. That is about six
tests. They move to the moq `backend.BackendMock`/`SandboxMock` that
`backend_moq.go` already generates, as the testing skill prefers, and `fake`
(759 lines) and `fake_test.go` (1209 lines) are deleted.

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
- **Intermediate runner tests**: (c), deleted inside slices 8–9 as #1890 says.
- **Slow tests**: the scenarios need no Docker daemon. `TestDial_TunnelsThroughExec`
  stays Docker's own concern.
- **`driverserver` and fake backend tests**: `driverserver` keeps its (b) storage
  and transport rules and gains slice 8's driver-loop rules. The fake goes.

The harness lands before slices 9–11. Those slices can then verify themselves
with scenarios rather than new fake-backend runner tests that #1890 would only
delete again.

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
package e2escript // github.com/gritzapp/gritz/internal/e2e/e2escript  // new

// Step is one action of a script.
type Step struct {
	Sh    string          `json:"sh,omitempty"`
	Sleep Duration        `json:"sleep,omitempty"`
	Block bool            `json:"block,omitempty"`
	Tool  string          `json:"tool,omitempty"`
	Args  json.RawMessage `json:"args,omitempty"`
	Error string          `json:"error,omitempty"`
}

// Duration is a time.Duration encoded as a string ("1.5s").
type Duration time.Duration

// Sh runs cmd with sh -c in the sandbox directory.
func Sh(cmd string) Step

// Sleep sleeps for d, or until the run is stopped.
func Sleep(d time.Duration) Step

// Block sleeps until the run is stopped.
func Block() Step

// Tool calls the named tool on the gritz MCP server with args, JSON-encoded.
func Tool(name string, args any) Step

// Error ends the run with msg as its error.
func Error(msg string) Step

// Encode returns steps as a gritz-e2e fenced block.
func Encode(steps ...Step) string

// Parse returns the steps of every gritz-e2e block in text, in order.
func Parse(text string) ([]Step, error)
```

```go
package agent // github.com/gritzapp/gritz/internal/runner/agent

// Prompt runs the e2escript steps in the prompt, or the configured
// DummyOptions when it has none. // changed
func (a *DummyAgent) Prompt(ctx context.Context, prompt string, resume bool) error
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

// Options configures a Harness; zero values take defaults.
type Options struct {
	Concurrency  int
	SuspendAfter time.Duration
	Commands     []string
	Secrets      map[string]string
	PollInterval time.Duration
	Timeout      time.Duration
}

// Harness is a server, a runner on the local backend and an org, for one test.
type Harness struct{ ... }

// New stands up a harness and registers its teardown with t.Cleanup.
func New(t *testing.T, opts *Options) *Harness

// CreateTask creates a draft task in the harness's workspace.
func (h *Harness) CreateTask() *Task

// RestartRunner stops the runner and starts a new one on the same runner.db
// and sandbox root.
func (h *Harness) RestartRunner()

// Task is a task in the harness's org.
type Task struct {
	ID int64
	// ... unexported
}

// Script adds an instruction carrying steps.
func (t *Task) Script(steps ...e2escript.Step)

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

// ReadFile returns the content of name in the task's sandbox directory.
func (t *Task) ReadFile(name string) string

// WaitStatus waits until the task has status and returns it.
func (t *Task) WaitStatus(status gritzv1.TaskStatus) *gritzv1.Task

// WaitEvent waits until an event matches and returns it.
func (t *Task) WaitEvent(match func(*gritzv1.Event) bool) *gritzv1.Event

// WaitSandbox waits until the task's sandbox has status want.
func (t *Task) WaitSandbox(want backend.Status)

// WaitFile waits until name in the task's sandbox directory holds want.
func (t *Task) WaitFile(name, want string)
```

**CLI:** `gritz driver --root DIR`. `gritz runner --backend local --local-root
DIR`, where the local backend's binary is the running executable.

**How it fits:** `e2e.New` creates the org and keys in teststore, serves
`server.New(...).Handler()` on httptest, builds a `local.Backend` on a fresh
root, and runs `runner.Serve` with it on a goroutine. `runner.Serve` is what
`gritz runner` calls after parsing its flags. The runner's `TaskRunner`s create
and boot local sandboxes; `Boot` starts `gritz driver --root DIR` from
`prebuilt/`. The driver resolves every path under `--root`, sets
`GRITZ_RUN_VERSION`, and runs the dummy agent, which `e2escript.Parse`s its
prompt and runs the steps. `Tool` steps start the injected MCP server from
`BinaryPather.BinaryPath()`. `Task` methods call the user-facing RPCs, and
`WaitSandbox` asks the harness's own `local.Backend` for the sandbox's status.

## Implementation Plan

The runner is still changing (runner-event-replica slice 7 has landed; 8–11
haven't). Slices 1–7 don't depend on the runner's internals, so they can land
now, and the scenarios then act as the regression net for slices 8–11. Slices
that must wait are marked.

1. **Driver `Root`.** Delivers: `driverserver.Options.Root` and
   `ExecutorOptions.Root` in place of `RecordPath`, `EventsPath`, `LogPath`,
   `Config` and `FileRoot`; `gritz driver --root`; `testrunner.NewDriver` and
   `driverserver_test.go`'s `setup` set only `Root`. Depends on: nothing.
   Verifiable by: the existing `driverserver` tests on `Root`; a test that a run
   writes its record, event file, log, config and provisioned files under it.
   Touches the same files as runner-event-replica slice 8, so land it first or
   rebase it on 8.
2. **`GRITZ_RUN_VERSION`.** Delivers: `Executor.execute` adds it to the run's
   environment. Depends on: nothing. Verifiable by: `TestRun_Env` sees it, and a
   spec env or secret of that name doesn't override it.
3. **Local backend.** Delivers: `internal/runner/backend/local`,
   `backend.BinaryPather` and `TaskManager.spec` using it, and `gritz runner
   --backend local --local-root`. Depends on: (1). Verifiable by: `local` tests
   modelled on Docker's `TestSandbox_Lifecycle` against the prebuilt driver
   (create → suspended, boot → up, a `Run` stream over `Dial`, suspend →
   suspended with files kept, boot again, destroy → gone); a killed driver
   reports suspended; a second `Backend` on the same root sees and suspends a
   sandbox the first booted; two sandboxes run side by side.
4. **Scripted dummy.** Delivers: `internal/e2e/e2escript` and
   `DummyAgent.Prompt` running a script found in the prompt, falling back to
   `DummyOptions`. Depends on: nothing. Verifiable by: `e2escript` tests
   (`Encode`/`Parse` round trip, several blocks in order, other fences ignored,
   malformed lines rejected); a dummy test per step against a stdio MCP server,
   and the fallback.
5. **`runner.Serve`.** Delivers: the runner's wiring moved out of
   `command/runner.go` into `runner.Serve`, with `LostTimeout` in its options.
   Depends on: nothing. Verifiable by: `gritz runner` behaving as before against
   a dev server; this slice adds no tests. Slice 9 changes what `Serve` calls
   (`Load`, `Run`), so whichever lands second rebases.
6. **Harness and `TestStart`.** Delivers: `internal/e2e` (`New`, `CreateTask`,
   `RestartRunner`, the `Task` methods and waits) and `TestStart`. Depends on:
   (1)–(5). Verifiable by: `TestStart` passes under `mise run test`, also with
   `-count=5 -race`; a deliberately broken assertion reports the last state seen.
7. **First scenarios.** Delivers: `TestCancel_MidRun`, `TestRestart_MidRun`,
   `TestWake_MidRun`, `TestCancel_PendingStart`, `TestArchive`,
   `TestRunnerRestart_MidRun`, `TestAgentError`, `TestTools`,
   `TestSecretsMasked`, `TestSetupCommands`, `TestRestart_Completed`,
   `TestDriverDies_MidRun`, `TestConcurrent`. All of them describe behaviour
   today's runner already has (archive through `Prune`, reattach through `Load`,
   the lost driver through `TaskLostEvent`). A scenario that fails has found a
   bug and lands with its fix, or is skipped with an issue link. Several PRs of
   two or three scenarios each. Depends on: (6). Verifiable by: the scenarios
   pass.
8. **Delete constructor-panic tests.** Delivers: `TestNewTaskRunner` removed.
   Depends on: nothing.
9. **runner-event-replica slices 8–11 (not part of this proposal), verified with
   scenarios.** Each slice deletes its (c) tests with the code and keeps or adds
   only the (b) tests listed above. The behaviour lists under their "Verifiable
   by" are covered by the scenarios from (7), which must keep passing, instead of
   new `runner_test` cases on the fake backend. Slice 9 adds its `RunLoop` fault
   tests on `backend.SandboxMock`.
10. **`suspend_after` scenarios.** Delivers: `Options.SuspendAfter`,
    `TestSuspendAfter`, `TestSuspendAfter_Window`. **Waits for runner-event-replica
    slice 10.** Verifiable by: the scenarios pass.
11. **Port pass.** Delivers: every (a) test deleted. **Waits for slice 11**, the
    last of runner-event-replica, as #1890 asks: until then the code under test
    still moves, and (a) tests are cheap to keep while it does. Depends on: (7),
    (10). Verifiable by: `mise run test` passes, and coverage of
    `internal/runner/...` from the e2e package (`-coverpkg`) doesn't drop on the
    paths the deleted tests covered.
12. **Delete pass.** Delivers: the remaining (b) runner tests moved off
    `backend/fake` onto the moq mocks; `backend/fake` and `fake_test.go` deleted;
    `testrunner` reduced to nothing, its last helpers moved into the test files
    that use them. Closes #1890. Depends on: (11). Verifiable by: `mise run test`;
    nothing imports `backend/fake` or `testrunner`.

## Trade-offs

- **The Docker backend instead of a local one.** It is more realistic, but it
  needs a daemon in CI, takes seconds per container (#1859: ~9.5s to boot
  four), and its isolation is exactly what the scenarios don't test. The Docker
  backend keeps its own contract tests. The local backend runs the same prebuilt
  driver binary, so only the container boundary is missing.
- **An in-process driver** (as `testrunner.NewDriver` does). It is faster, but it
  can't die, be suspended or survive a runner restart as a process does. It
  shares the test's `HOME` and working directory, and it never runs the
  `gritz driver` command or the socket transport. `TestRunnerRestart_MidRun` and
  `TestDriverDies_MidRun` need a real process.
- **`testrunner.Server` and `ClientMock` instead of the apiserver.** They miss
  command events, the xid watermark, the run boundary and the server's version
  guards: everything #1868 was about. They also make every test restate the
  server's behaviour, which is how the two sides drifted apart.
- **The script in the workspace config** (`DummyOptions`). That config is fixed
  per runner, so run 1 and run 2 can't differ, and two tests can't share a
  harness setup. Instructions are per task and per run, and reach the agent
  through the real prompt path.
- **Passing the script to the dummy as structured data** rather than parsing the
  prompt. That would need a new channel from the driver to the agent. The
  rendered prompt is what real agents get, so parsing it also tests that the
  right instructions reached the run.
- **Asserting on runner internals** (`runner.db`, fake-backend counters). That is
  more precise, but those are exactly the assertions that churn with every
  runner-event-replica slice. Observable state is what has to survive them.
- **A harness-local copy of the runner wiring** instead of `runner.Serve`. It is
  less churn now, but it would drift from `gritz runner`, and the harness would
  stop testing the real assembly order (`HandleEvents` before `Load`, `Load`
  before `Run`).
- **Sandbox state from the backend interface** rather than from the runner. The
  harness asks its own `local.Backend` value, which is the same view an operator
  has of the platform (`docker ps`), and it doesn't depend on the runner.

## Open Questions

- **A stale prebuilt.** The driver comes from `prebuilt/`, built by `mise run
  build`. A developer who changes the driver and runs `go test ./internal/e2e`
  without rebuilding tests the old driver, silently. A `TestMain` could instead
  `go build` the binary once per package run (cached, a few seconds), at the cost
  of departing from the decided "use the prebuilt". Alternatively, the harness
  could compare the prebuilt's embedded VCS revision with the test binary's and
  fail on a mismatch.
- **Amending runner-event-replica.** Slice 9 and 10's "Verifiable by" lists
  name `runner_test` cases on the fake backend. Should that proposal be edited
  to point at the scenarios once slice 7 here lands?
- **`gritz runner --backend local` for users.** It runs agents with the runner's
  own privileges and filesystem. Should the flag be hidden, or refuse to start
  without an explicit `--local-unsafe`-style acknowledgement?
- **Workspace environment on the local backend.** It ignores `container:`, so
  `container.environment` doesn't reach the run. Secrets and setup commands
  cover the scenarios; should the local backend apply `container.environment`
  through `RunEnv`, as Lambda applies its section?
- **CI time.** About twenty scenarios of a few seconds each, run in parallel,
  should add well under a minute. `TestDriverDies_MidRun` waits out `LostTimeout`.
  If the suite grows, should it get its own `mise` task and CI job?
