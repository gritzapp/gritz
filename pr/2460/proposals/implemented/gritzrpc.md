# gritzrpc: a protocol for custom agents

Issue: https://github.com/gritzapp/gritz/issues/1944

## Problem

Users have custom agents and harnesses, in particular Mastra (TypeScript)
workflows, that they want to run as gritz tasks. gritz has an integration for
each agent CLI it knows (`internal/runner/agent/claude.go`, `codex.go`,
`cursor.go`, `copilot.go`, `sloppy.go`). Each one wraps one CLI: it builds that
CLI's flags, writes its MCP config format and parses its output.

The one way to run your own program is the `process` agent type
(`internal/runner/agent/process.go`, see
[Custom agent process](/operators/workspaces-yaml/#custom-agent-process)). It
runs a configured command, writes the rendered prompt to its stdin, sets
`GRITZ_RESUME=true` on later runs and tees its stdout and stderr raw into the
driver log; the exit status is the run's outcome. It passes no MCP
configuration, so the program cannot reach the `gritz` MCP server unless it
hard-codes how to.

Every agent, `process` included, receives the same input: one prompt string
rendered by `agentprompt.Render`. That works for a chat-style LLM CLI but not
for a program. A workflow suspended on a PR review needs the typed event that
woke it (its id, URL, source, type and `details`), not a paragraph describing
it.

## Design

Add one generic agent type, `gritzrpc`. The driver spawns a user-supplied
process for each run and speaks **gritzrpc** to it: JSON-RPC 2.0 over the
process's stdin and stdout. gritz sends one `run/start` request carrying the
task, its standing links (on the first run) and the typed events for this run.
The agent may stream `log` notifications back while it works, and its response
to `run/start` ends the run.

The protocol is deliberately small. Everything an agent does *to* gritz
(`report`, `create_link`, `update_my_task`, `get_my_task`) stays on the injected
`gritz` MCP server. gritzrpc does not duplicate those calls; it hands the agent
the MCP server configs in `run/start`, and the agent connects to them with any
MCP client.

### Motivating example: a suspended Mastra workflow

1. A task starts. gritz spawns `node dist/agent.js` and sends `run/start` with
   `resume: false`, the task's instruction events and its standing links.
2. The workflow opens a PR, calls `create_link` with `subscribe=true` through
   the `gritz` MCP server, then reaches a step that `suspend()`s until the PR is
   reviewed. The agent saves the Mastra snapshot (for example in LibSQL storage
   under the workspace), responds `{}` to `run/start`, and exits.
3. gritz shows the task as **completed**. A run that ends with the workflow
   waiting or suspended is a successful run, and there is no separate status.
4. A review comment arrives. The event router matches the subscribed link and
   wakes the task. gritz spawns the process again and sends `run/start` with
   `resume: true` and the `external` event, including its `url`, `source`,
   `type` and `details`.
5. The agent loads the snapshot, calls `run.resume({ step, resumeData })` with
   the event, and continues.

### Transport and framing

- gritz starts `command` with `args` from the workspace config (below). The
  working directory is the agent `cwd` and the environment is the run's
  environment (`Driver.Env`, including declared secrets and `GRITZ_TASK_ID`),
  the same as the CLI agents get.
- **stdin**: gritz → agent messages. **stdout**: agent → gritz messages, and
  nothing else. **stderr**: free-form; it is teed into the driver log with
  `DriverLog.Stderr()`, the same as `claude.go` and `process.go` do, so it
  reaches `/gritz/log`, docker logs and the shipped log with secrets masked.
- Each message is one JSON-RPC 2.0 object, compact-encoded on a single line and
  terminated by `\n` (newline-delimited JSON). There are no `Content-Length`
  headers. JSON escapes newlines inside strings, so a message never spans
  lines. Lines have no length limit (`lines.Read` in `internal/x/lines`).
- There is one process per run, and each process handles exactly one
  `run/start`. When the agent has responded, gritz closes stdin. The agent
  should then exit; if it is still running after `exitGrace` (5s), gritz sends
  SIGTERM to the process group.

stdout is the protocol channel and nothing else. A stdout line that is not a
valid JSON-RPC 2.0 message (not JSON, not an object, or `jsonrpc` not `"2.0"`)
fails the run with `gritzrpc agent: invalid stdout line: <line>`, where the
line is truncated to 256 bytes. gritz then stops the process the same way as on
cancel (`run/cancel`, then SIGTERM and SIGKILL to the group). Agents log through
stderr or the `log` notification (see Trade-offs).

### Methods

| Direction     | Method       | Kind         | Purpose                                        |
| ------------- | ------------ | ------------ | ---------------------------------------------- |
| gritz → agent | `run/start`  | request      | Start the run. The response ends it.           |
| gritz → agent | `run/cancel` | notification | Ask the agent to stop gracefully.              |
| agent → gritz | `log`        | notification | One log line, written to the driver log.       |

#### `run/start`

```json
{"jsonrpc":"2.0","id":1,"method":"run/start","params":{
  "protocolVersion": 1,
  "resume": true,
  "cwd": "/workspace",
  "task": {"id":"2283","name":"…","workspace":"gritz","url":"https://…","version":"7", "...": "…"},
  "events": [
    {"id":"9120","taskId":"2283","wake":true,"createdAt":"2026-11-02T20:31:00Z",
     "external":{"description":"review comment","url":"https://github.com/…#discussion_r1",
                 "source":"github","type":"pull_request_review_comment",
                 "details":{"path":"main.go","line":"12"},"data":"…"}}
  ],
  "links": [
    {"id":"881","taskId":"2283","relevance":"PR","url":"https://github.com/gritzapp/gritz/pull/1950",
     "title":"…","subscribe":true,"createdAt":"2026-11-02T19:58:00Z","routingKey":"…"}
  ],
  "mcpServers": {
    "gritz": {"type":"stdio","command":"<sandbox gritz binary>","args":["tool","agent-mcp","--server","…","--task","2283","…"]}
  }
}}
```

(The example shows `events` and `links` together for brevity; `links` is only
sent with `resume: false`, see below.)

- `protocolVersion` is the integer `gritzrpc.ProtocolVersion`, starting at
  `1`. There is no separate handshake round trip because the version rides on
  the only request. An agent that does not support the version responds with
  error code `-32000` (`CodeUnsupportedVersion`). gritz never negotiates down:
  server, runner and driver ship together, so there is one current version. The
  number exists for the user's agent, which ships separately, so it fails with
  a clear message instead of misreading the params.
- `resume` is `cfg.Started`, the same bit the CLI agents get (and that
  `process` turns into `GRITZ_RESUME`). It is `false` on the task's first run
  and `true` on every later run, including wakes and restarts. The process is
  new on every run, so an agent that needs state across runs must persist it
  itself, for example in the workspace or in an external database. The Docker
  sandbox's writable layer survives between runs of the same task.
- `task` is the `gritzv1.Task` that `Driver.Run` already fetches and passes to
  `Driver.runAgent`, encoded with `protojson`.
- `events` are the `gritzv1.Event` values from `Driver.promptEvents`, which are
  the `instruction` and `external` events stored after the cursor, in event-file
  order and encoded with `protojson`. These are the same events
  `agentprompt.Render` turns into text for the other agents. On the first run
  they are the task brief (the instructions plus external triggers from the
  head of the file). The list may be empty, for example on a restart with
  nothing new.
- `links` are the task's standing links, the `gritzv1.TaskLink` values that
  `Driver.runAgent` already loads as `promptLinks` for `agentprompt.Render`,
  encoded with `protojson`. Like the first-run prompt's brief, they are sent
  only on the first run (`resume: false`), so together with `events` they give
  the agent its task without a `get_my_task` round trip. On later runs the field
  is omitted; an agent that needs the current links calls `get_my_task`.
- `cwd` is the expanded agent cwd (`envx.Expand(cfg.Cwd, d.Env)`), which is
  also the process's working directory. It is included so an agent need not
  trust `process.cwd()` after `chdir`.
- `mcpServers` is `cfg.McpServers` (the workspace's servers plus the injected
  `gritz` server), keyed by name, with the same shape and field names as
  `agent.McpServer`: `type`, `url`, `headers`, `command`, `args`, `env`.

**protojson encoding.** Fields use protojson's default lowerCamelCase names.
`int64` values (`id`, `taskId`, `version`) are JSON **strings**. Timestamps are
RFC 3339 strings. A oneof is a single key named after the set arm
(`"instruction": {…}` or `"external": {…}`); the event itself has no type
field (the `type` inside `external` is the source's event type). Unpopulated
fields are omitted. Agents must ignore unknown fields and skip events whose arm
they do not recognise, because new arms are not a protocol version bump.

**Result.** A successful response is `{"jsonrpc":"2.0","id":1,"result":{}}`.
The result object has no fields yet, and gritz ignores any that the agent
sends. Success means "this run's work is done". That includes a workflow that
is now suspended and waiting on an event, and gritz reports both the same way
(`RunnerEventStopped`, so the task is completed).

**Error.** An error response
`{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"step deploy failed","data":{…}}}`
fails the run. `Prompt` returns `gritzrpc agent: step deploy failed (code
-32001)`, which `Driver.Run` puts in the `RunnerEventFailed` reason, so it shows
in the task timeline. If `data` is present it is logged as an attr. Codes other
than `-32000` are the agent's to choose. gritz does not interpret them.

#### `run/cancel`

```json
{"jsonrpc":"2.0","method":"run/cancel"}
```

No params. gritz sends this when `Prompt`'s context is cancelled. The driver
server's `Executor` is the only thing that cancels a run, always with
`agent.ErrStop` as the cause: `Executor.Stop` on a stop command, and
`Executor.Shutdown` when the driver server shuts down. The agent should wind
down, for example by checkpointing the workflow, and then respond to
`run/start` with either a result or an error. Either way the run is treated as
cancelled (see Cancellation).

#### `log`

```json
{"jsonrpc":"2.0","method":"log","params":{"level":"info","msg":"step finished","attrs":{"step":"plan","ms":1532}}}
```

- `level` is one of `debug`, `info`, `warn` or `error`, mapped to the matching
  `slog.Level`. An absent or unknown level is logged as `info`.
- `msg` is the message. `attrs` is an optional flat object. Each entry becomes
  an `slog` attr: strings stay strings, and any other JSON value is attached as
  its compact JSON text, so nested objects stay readable in the text handler.
- gritz writes the line with `DriverLog.Log(ctx, level, msg, attrs…)` (the
  embedded `slog.Logger`) with the attr `agent=gritzrpc` added. It then flows
  like any other driver log line to stderr, `/gritz/log` and the shipped log,
  where secrets are masked.

Logs are the only agent → gritz output. A `log` line does not create a task
event, is not routed to other tasks, and has no new `Event` payload arm.

#### Anything else

- A request *from* the agent (any message with an `id` and a `method`) gets a
  `-32601` method-not-found error response, because gritz exposes no methods.
- An unknown notification, or a response whose `id` is not `1`, is valid
  JSON-RPC, so it is logged at `warn` level and skipped.
- A stdout line that is not valid JSON-RPC fails the run with the truncated line
  in the error (see Transport and framing).
- If the agent exits without responding to `run/start`, the run fails with
  `gritzrpc agent exited without responding: <exit status>`.
- If the agent responds and then exits non-zero, the response stands and the
  exit status is logged at `warn` level.

These cases are external input from user code, not internal call orders, so
they are handled rather than asserted.

### Event delivery is at-least-once

`Driver.runAgent` advances `cfg.PromptedEventID` in memory when it reads
events, but saves it only after `Prompt` returns `nil`. If a run fails, is
cancelled or crashes, the next run sees the same events again. The same is true
when the agent responded successfully but the config `Save` failed. **Agents
must tolerate redelivery and deduplicate by event `id`.** A Mastra workflow
resumed twice with the same event must recognise the second resume as already
applied, for example by recording handled event ids next to its snapshot. The
protocol does not add acks or a cursor of its own, so the driver's cursor
remains the only one.

### Cancellation

`Agent.Prompt`'s contract stays the same: when `ctx` is cancelled the returned
error has `context.Canceled` in its chain, and `Driver.Run` checks
`context.Cause(ctx) == ErrStop` to report a graceful stop. For gritzrpc:

1. The command is built with `exec.Command`, not `exec.CommandContext`, with
   `Setpgid: true`. `exec`'s one-shot `Cancel` hook, which `claude.go` and
   `process.go` use to SIGTERM the group, cannot express the two-stage stop
   below.
2. `context.AfterFunc(ctx, …)` writes `run/cancel` to stdin and arms a
   `cancelGrace` timer (10s).
3. If the process is still running when the timer fires, gritz sends SIGTERM to
   the process group (`syscall.Kill(-pid, SIGTERM)`), as the CLI agents do. If
   it is still running `killGrace` (5s) later, gritz sends SIGKILL to the group.
4. Once `ctx` is cancelled, `Prompt` returns
   `fmt.Errorf("gritzrpc agent cancelled: %w", ctx.Err())` **whatever the agent
   answered**. A cancelled run never counts as successful, so the event cursor
   is not saved and the next run redelivers the events, which the agent
   deduplicates. Treating a late success as success would make the cursor
   depend on a race between the agent's response and the stop.

Writes to stdin (`run/start`, `run/cancel`) are serialised by a mutex. A write
that fails because the agent already closed stdin is ignored, since exit
handling reports the outcome. `cancelGrace` is fixed at 10s and is not
configurable.

### Typed events reach the agent: widen `Agent.Prompt`

`Driver.runAgent` already holds everything a program needs, and passes all of
it to `agentprompt.Render` only to throw it away after rendering:

```go
prompt, err := agentprompt.Render(agentprompt.Options{
	Started: cfg.Started,
	Prompt:  cfg.Prompt,
	Task:    task,
	Events:  events,
	Links:   promptLinks,
})
// ...
if err := a.Prompt(ctx, prompt, cfg.Started); err != nil {
```

We widen the interface so every agent receives the whole run input. `Turn`
carries the rendered prompt next to the same values `agentprompt.Options` was
built from, minus the workspace prompt, which is only an input to rendering:

```go
type Turn struct {
	Prompt string              // agentprompt.Render's output
	Resume bool                // cfg.Started
	Task   *gritzv1.Task       // the task Driver.Run fetched
	Events []*gritzv1.Event    // promptEvents' slice
	Links  []*gritzv1.TaskLink // promptLinks; nil unless first run
}

Prompt(ctx context.Context, turn Turn) error
```

`Driver.runAgent` renders the prompt as it does today, then calls
`a.Prompt(ctx, Turn{...})` with the rendered string and the same `cfg.Started`,
`task`, `events` and `promptLinks`. The CLI agents and `ProcessAgent` read
`turn.Prompt` and `turn.Resume`, and their behaviour does not change.
`GritzRPCAgent` reads `Task`, `Events`, `Links` and `Resume` and ignores
`Prompt`. `Driver.runAgent` still renders the prompt for every type, because
rendering is cheap and the alternative is a type switch.

We do not special-case the type in the driver (`if cfg.Type == TypeGritzRPC`)
or add an optional `EventAgent` interface checked with a type assertion. Both
are second code paths into the agent. One interface with one input struct
keeps `Driver.runAgent` linear, and the change touches every implementation in
the same PR, as the no-backwards-compatibility rule requires.

### Workspace config

```yaml
workspaces:
  mastra:
    container:
      image: ghcr.io/acme/mastra-agent:latest
    commands:
      - cd /workspace && pnpm install --frozen-lockfile && pnpm build
    secrets:
      OPENAI_API_KEY: ${env:OPENAI_API_KEY}
    agent:
      type: gritzrpc
      cwd: /workspace
      gritzrpc:
        command: node
        args: [dist/agent.js]
      mcp_servers:
        github: { type: http, url: https://api.githubcopilot.com/mcp/ }
```

`workspace.Agent.Validate`, which today only validates `mcp_servers`, gains
type-specific checks. For `type: gritzrpc`, `gritzrpc.command` is required.
`agent.prompt` and `agent.verbose` are rejected because nothing would read
them. `Workspace.AgentConfig` copies `gritzrpc` into `agent.Config.GritzRPC`.
`Driver.runAgent` passes it through `Options.GritzRPC`, and `NewAgent` builds a
`GritzRPCAgent` from it, the same as for every other type.

### API

```go
// Package gritzrpc defines the wire types of gritzrpc, the newline-delimited
// JSON-RPC 2.0 protocol the driver speaks to a custom agent over its stdio.
package gritzrpc // github.com/gritzapp/gritz/internal/runner/agent/gritzrpc

// ProtocolVersion is the version sent in every run/start.
const ProtocolVersion = 1

// Method names.
const (
	// MethodRunStart starts the run; its response ends it.
	MethodRunStart = "run/start"
	// MethodRunCancel asks the agent to stop gracefully.
	MethodRunCancel = "run/cancel"
	// MethodLog carries one log line from the agent.
	MethodLog = "log"
)

// Error codes.
const (
	// CodeUnsupportedVersion is returned by an agent that cannot speak ProtocolVersion.
	CodeUnsupportedVersion = -32000
	// CodeMethodNotFound is returned by gritz for any request from the agent.
	CodeMethodNotFound = -32601
)

// Message is one JSON-RPC 2.0 request, notification or response.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC error object.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error formats the error as "<message> (code <code>)".
func (e *Error) Error() string

// StartParams are the params of run/start.
type StartParams struct {
	ProtocolVersion int
	Resume          bool
	Cwd             string
	Task            *gritzv1.Task
	Events          []*gritzv1.Event
	Links           []*gritzv1.TaskLink
	McpServers      map[string]McpServer
}

// MarshalJSON encodes p with Task, Events and Links in protojson.
func (p StartParams) MarshalJSON() ([]byte, error)

// UnmarshalJSON decodes p with Task, Events and Links from protojson.
func (p *StartParams) UnmarshalJSON(data []byte) error

// McpServer is an MCP server config as sent in run/start.
type McpServer struct {
	Type    string            `json:"type"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// LogParams are the params of log.
type LogParams struct {
	Level string         `json:"level,omitempty"`
	Msg   string         `json:"msg"`
	Attrs map[string]any `json:"attrs,omitempty"`
}
```

```go
package agent // github.com/gritzapp/gritz/internal/runner/agent

// TypeGritzRPC runs a user-supplied process that speaks gritzrpc. // new
const TypeGritzRPC = "gritzrpc"

// Turn is the input of one run. // new
type Turn struct {
	Prompt string
	Resume bool
	Task   *gritzv1.Task
	Events []*gritzv1.Event
	Links  []*gritzv1.TaskLink
}

type Agent interface {
	// Prompt runs one turn and waits for completion. // changed: was Prompt(ctx, prompt string, resume bool)
	Prompt(ctx context.Context, turn Turn) error
	Close() error
}

// GritzRPCOptions contains gritzrpc-specific agent options. // new
type GritzRPCOptions struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

type Options struct {
	// ...
	GritzRPC *GritzRPCOptions // new
}

type Config struct {
	// ...
	GritzRPC *GritzRPCOptions `json:"gritzrpc,omitempty"` // new
}

// GritzRPCAgent implements Agent by speaking gritzrpc to a spawned process. // new
type GritzRPCAgent struct{ ... }

// Prompt spawns the agent, sends run/start and waits for its response.
func (a *GritzRPCAgent) Prompt(ctx context.Context, turn Turn) error

// Close releases any resources held by the agent.
func (a *GritzRPCAgent) Close() error
```

```go
package workspace // github.com/gritzapp/gritz/internal/runner/workspace

type Agent struct {
	// ...
	GritzRPC *GritzRPCConfig `yaml:"gritzrpc,omitempty"` // new
}

// GritzRPCConfig contains gritzrpc-specific agent configuration. // new
type GritzRPCConfig struct {
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
}
```

`puppet.Agent.Prompt` keeps its `(ctx, prompt string, resume bool)` signature:
`puppet` cannot import `agent`, so `newPuppetAgent` in `agent/puppet.go` wraps
it in an unexported adapter that passes `turn.Prompt` and `turn.Resume`.

**How it fits:** the runner's `Workspace.AgentConfig` copies `gritzrpc:` into
`agent.Config`, which reaches the driver in the task config file. The driver
server's `Executor` builds an `agent.Driver` per run; `Driver.Run` fetches the
task and calls `Driver.runAgent`, which builds a `Turn` from the rendered
prompt, `cfg.Started`, the task, `promptEvents`' slice and `promptLinks`.
`NewAgent` returns a `GritzRPCAgent`, whose `Prompt` spawns the process, writes
a `gritzrpc.StartParams` request, and turns `log` notifications into
`DriverLog` lines until the `run/start` response, the process exit, or an
invalid stdout line that fails the run. The `gritzrpc` package holds only wire
types, so a future Go SDK and the test agent can share them without importing
`agent`.

## Implementation Plan

The code layers (1–4) carry no user docs; layer 5 documents the finished
feature in one docs-only PR.

1. **Widen `Agent.Prompt` to `Turn`.** Delivers: the `Turn` struct and the
   `Prompt(ctx, Turn)` signature across `ClaudeAgent`, `Codegritz`,
   `CopilotAgent`, `CursorAgent`, `SloppyAgent`, `ProcessAgent` and
   `DummyAgent`, plus the puppet adapter in `agent/puppet.go`.
   `Driver.runAgent` keeps rendering with `agentprompt.Render` and passes the
   rendered string with `cfg.Started`, `task`, `events` and `promptLinks` as one
   `Turn`. `agentprompt` does not change. No behaviour change. Depends on:
   nothing. Verifiable by: the existing agent, driver and e2e tests pass with
   only their `Prompt` call sites updated; no new tests.
2. **`gritzrpc` wire package.** Delivers: `internal/runner/agent/gritzrpc` with
   the types above, a short package doc, and `StartParams` protojson encoding.
   Depends on: nothing. Verifiable by: one round-trip test of a `StartParams`
   holding an external event and a link, asserting the encoding is one line,
   int64 ids are strings and the oneof arm is a single key.
3. **`GritzRPCAgent`.** Delivers: `TypeGritzRPC`, `GritzRPCOptions`,
   `Options.GritzRPC`, `Config.GritzRPC`, the `NewAgent` case and the agent
   itself (spawn, framing, log mapping, stderr tee, cancel escalation).
   Depends on: (1), (2). Verifiable by: one driver test (`driver_test.go`,
   `setupDriver` with `Config{Type: TypeGritzRPC}`) that runs a task end to end
   against the `ok` test agent and checks the `run/start` it recorded (typed
   events, first-run links, `mcpServers`, `cwd`) and the agent's `log` line in
   the driver log; plus `Prompt` tests through `NewAgent` for the failure
   modes the driver test can't reach: `error`, `exit`, `noise`, `cancel-ok`
   and `cancel-ignore` (see Testing). The cursor not advancing on a failed run
   is already covered for every agent type by
   `TestDriverRun_PromptedEventNotAdvancedOnError`, so it gets no gritzrpc
   test.
4. **Workspace config.** Delivers: `workspace.GritzRPCConfig`, the
   `Workspace.AgentConfig` copy and the type-specific `Agent.Validate` checks.
   Depends on: (3). Verifiable by: workspace tests in the existing
   `testdata/*.yaml` style: one valid `gritzrpc` workspace that checks the
   `AgentConfig` copy, and one with no `gritzrpc.command` that fails
   validation.
5. **Docs.** Delivers: the `gritzrpc` agent type in
   [`operators/workspaces-yaml`](/operators/workspaces-yaml/) (the `type` row,
   a `gritzrpc` options row and a short section next to
   [Custom agent process](/operators/workspaces-yaml/#custom-agent-process)
   with the config example), and a new `reference/gritzrpc` page for people
   writing agents: transport and framing, the three methods with their JSON,
   the protojson conventions, result and error semantics, cancellation and
   its timings, at-least-once delivery and dedupe by event id. Depends on:
   (4). Verifiable by: `cd docs && pnpm build`.

## Testing

The test agent is the test binary itself, using the standard Go helper-process
pattern. A `TestMain` in `internal/runner/agent` checks
`GRITZRPC_TEST_AGENT=<scenario>`. When it is set, the binary acts as a gritzrpc
agent and exits instead of running tests. A test points
`GritzRPCOptions.Command` at `os.Executable()` and selects a scenario through
`Env`. The driver test lives in the same package, so it uses the same agent.

- `ok`: writes the received `StartParams` to the file named by
  `GRITZRPC_TEST_OUT`, sends one `log` line, sends a request and waits for its
  `-32601` error, then responds `{}`. A successful run therefore also proves an
  agent request is answered. Used by the driver test.
- `error`: responds with `{code: -32001, message: "boom"}`; the error message
  and code reach `Prompt`'s error.
- `exit`: exits 3 without responding.
- `noise`: prints a non-JSON stdout line, then blocks. The run fails with the
  truncated line in the error, and the process is stopped.
- `cancel-ok`: blocks until `run/cancel`, then responds `{}`. `Prompt` still
  returns an error with `context.Canceled` in its chain.
- `cancel-ignore`: ignores `run/cancel` and blocks until SIGTERM.

Unknown notifications and unexpected response ids are logged and skipped; they
get no scenario. The scenarios use the `gritzrpc` wire types, so the test agent
also checks that the package is usable from the agent side. No prebuilt binary,
Node toolchain or `mise run build` step is needed. This is the same role the
dummy agent plays for the CLI path, with a real process boundary. Cancel tests
shorten `cancelGrace` through an unexported field so they do not wait 10s.

## Trade-offs

- **A new type vs extending `process`.** `process` already runs a user program,
  but its contract is the rendered prompt on stdin, `GRITZ_RESUME`, raw stdout
  in the log and the exit status as the outcome. Typed events, MCP configs,
  structured logs and a cancel handshake need a framed channel on stdin and
  stdout, which would change that contract for every existing `process`
  workspace. A separate type keeps `process` as the simplest way to run a
  script and leaves gritzrpc free to own stdout. See Open Questions.
- **ACP (Agent Client Protocol), rejected.** ACP is designed for a human
  chatting with a coding agent in an editor. Mapped onto a workflow, nearly
  everything useful (typed events, task context, run outcome) ends up in ACP
  extensions (`_meta`, `_`-prefixed methods), while its fs, terminal, permission
  and session-mode features go unused. Adopting ACP would mean a large spec
  with gritz's real contract hidden in extensions. There is no immediate need
  for ACP interop, and an ACP agent type can be added later if one appears.
- **Typed events vs the rendered prompt.** Sending `agentprompt.Render` output
  would reuse more code (it is what `process` does), but a program would then
  have to parse prose to recover the event id, URL and details it needs to
  resume a workflow. The typed `gritzv1.Event` is what the driver already
  holds.
- **protojson vs hand-written JSON types.** protojson ties the wire format to
  the proto definitions: new fields and arms appear automatically, and the
  timeline, `get_my_task` and gritzrpc agree on shape. The cost is protojson's
  conventions (int64 as strings, lowerCamelCase), which are documented above
  and are what any proto-generated TypeScript type expects anyway.
- **One process per run vs a long-lived agent process.** A long-lived process
  would let an agent keep workflow state in memory, but it would need a
  lifecycle protocol (multiple starts, idle shutdown, crash recovery) and would
  still need persistent state, because sandboxes are restarted, parked and
  moved. One process per run matches the CLI agents and keeps the protocol to
  one request.
- **No version handshake round trip.** `protocolVersion` in `run/start` gives
  the agent everything a handshake would, without a second request and the
  states between the two.
- **Logs only, no custom events.** Agent → gritz events (new `Event` arms,
  routing to other tasks) would need proto, store and UI work and a
  routing-semantics design. Logs cover the immediate need of seeing what the
  workflow is doing, and `report` on the MCP server covers user-visible
  messages.
- **gritz actions stay on MCP.** Duplicating `report` and `create_link` as
  gritzrpc methods would give agents two ways to do the same thing. The MCP
  server already exists, is injected into every run, and any agent framework
  has an MCP client.
- **Suspended = completed.** A distinct "waiting" status would need a new
  `TaskStatus`, runner handling and UI. From gritz's point of view a suspended
  workflow is a finished run whose task will be woken by events, which is
  exactly what "completed" plus subscribed links already means.
- **Strict stdout.** stdout is the protocol channel, and agents log through
  stderr or the `log` notification. Failing the run on a non-protocol line
  surfaces a misbehaving agent on its first run, with the offending line in the
  task timeline, instead of hiding corruption behind warnings: a stray write can
  also interleave with a real message and garble it, and a lenient reader would
  then silently lose a response. The cost is that in Node one dependency's
  `console.log` fails the run, so the SDK redirects `console.log` to stderr.
  Unknown notifications and unexpected response ids are still valid JSON-RPC
  and are only logged, so the protocol can grow without breaking agents.
- **Late success after cancel counts as cancelled.** This can redeliver
  events that the agent did handle, but at-least-once delivery with dedupe by
  event id is already the contract, and it keeps the cursor independent of
  timing.

## Open Questions

- **Should gritzrpc replace `process`?** `process` landed after this proposal
  was written. Under the no-backwards-compatibility rule we could fold it in:
  a program that ignores the protocol can't run under gritzrpc (its stdout
  would fail the run), so the two contracts don't merge cleanly, but a tiny
  wrapper (or an SDK helper) turns any script into a gritzrpc agent. Keeping
  both means two "run your own program" types to document and maintain.
- **Command shape.** `process.command` is one argv list
  (`command: [/usr/local/bin/my-agent, --fast]`); this proposal uses `command`
  plus `args`, matching `mcp_servers`. Should `gritzrpc` follow `process` so the
  two sibling types configure the same way?
- **Validation parity.** `type: process` has no type-specific checks (no
  `command` just ends the run). If `Agent.Validate` gains per-type checks for
  `gritzrpc`, should `process` require `command` and reject `prompt`/`verbose`
  the same way, in the same PR?

## Future work

- A TypeScript SDK (framing, typed `run/start`, `log` helper, and a stdout
  guard that redirects `console.log` and other stray stdout writes to stderr,
  since a non-protocol stdout line fails the run).
- A Mastra adapter that maps `run/start` to `start`/`resume` and persists
  snapshots plus handled event ids.
- Agent → gritz custom events, if logs and `report` turn out to be too little.