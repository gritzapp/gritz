# OpenTelemetry for the runner and driver

Issue: https://github.com/gritzapp/gritz/issues/1966

## Problem

Only `gritz server` sets up OpenTelemetry (`otelx.Setup` in
`internal/command/server.go`). `gritz runner` and `gritz driver` export no
traces or metrics, and neither sends a `traceparent` on the RPCs it makes. So
the path a task run takes, from the runner through the sandbox's driver and back
to the server, can't be followed in a tracing backend. Runner and driver latency
and failures (slow boots, dropped Run streams, unreported runs) show up only in
their logs.

The goal: the runner and the driver export OpenTelemetry data, configured the
same way as the server (the standard `OTEL_*` environment variables), so one task
run is one trace spanning runner, driver and server.

## Design

### What a trace looks like

One trace per **busy period**: one pass of `TaskRunner.RunLoop`, from the moment
it takes a slot for a version until the driver reports Idle and the sandbox is
suspended. Every RPC made on the run's behalf carries the trace context, so the
server's spans (and their `otelsql` children) nest under the runner's and the
driver's:

```
runner  gritz.task.busy                       task.id, task.version, gritz.backend
runner  ├─ gritz.sandbox.create / gritz.sandbox.boot
runner  ├─ gritz.v1.GritzService/...          (otelconnect client spans)
server  │   └─ gritz.v1.GritzService/...      (existing otelhttp + otelconnect spans)
runner  ├─ driver.v1.DriverService/Run        (otelconnect client span, whole stream)
driver  │   └─ driver.v1.DriverService/Run    (otelconnect handler span)
driver  │       └─ gritz.driver.run           task.id, task.version
driver  │           ├─ gritz.agent.setup
driver  │           ├─ gritz.agent.exec       agent name
driver  │           └─ gritz.v1.GritzService/GetTask, SubmitRunnerEvents, AppendLogChunk, ...
server  │               └─ ...
runner  └─ gritz.sandbox.suspend
```

A busy period that resumes a sandbox `Restore` found up at startup is the same
`gritz.task.busy` span without the boot child.

The server needs no change to join the trace: its handler chain is
`otelhttp.NewHandler` (via `otelx.Mux.Handler`) and then otelconnect.
`otelhttp` extracts the incoming `traceparent` and parents its span on it, and
otelconnect sees that span already in the context, so it nests under it rather
than starting a new root.

### `otelx.Setup` takes a service name

Today `Setup` builds providers with `resource.Default()`, so every process would
report as `unknown_service:gritz`. `Setup` gains options:

- `ServiceName` — the default `service.name`: `gritz-server`, `gritz-runner`,
  `gritz-driver`. `OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES` still win
  (the resource is `resource.Merge` of ours with `resource.Environment()`).
- `Attributes` — extra resource attributes: the runner adds `gritz.runner.id`;
  the driver adds `task.id` (the same key `apiauth.ObservabilityInterceptor`
  uses for spans).

`Provider` gains `ForceFlush`, which the driver needs before a sandbox is
suspended (see below).

Everything else stays as today: `Setup` is a no-op unless
`OTEL_EXPORTER_OTLP_ENDPOINT` is set, and the exporters read the rest of the
standard `OTEL_EXPORTER_OTLP_*` variables themselves.

### Client propagation

- `gritzclient.New` adds an otelconnect interceptor after (inside) the retry
  interceptor, so each retry attempt is its own client span with its own
  `traceparent`.
  The CLI commands never call `otelx.Setup`, so for them the global providers
  are no-ops and this costs nothing. The driver's per-run client (built by
  `Executor.newClient`) is a `gritzclient.New` client and gets it too.
- `driverclient.New` adds an otelconnect interceptor outside the liveness
  interceptor, with `WithoutTraceEvents()`: a Run stream lives for a whole busy
  period and gets a KeepAlive every 15s, so per-message span events would only
  hit the SDK's 128-event cap. All three backends build their driver clients
  through `driverclient.New`, so none of them changes.
- `NotificationClient` (the SSE wake-up stream) is not instrumented: it is a
  single connection that lives as long as the runner and carries no task.

### Runner

`internal/command/runner.go` calls `otelx.Setup` after parsing flags, with
`ServiceName: "gritz-runner"` and `gritz.runner.id`, and shuts it down on exit,
exactly as `server.go` does. It also installs `logctx.NewHandler` so runner log
lines carry `trace_id`, matching the server.

Spans, all from `otel.Tracer("github.com/gritzapp/gritz/internal/runner")`:

| Span | Where | Attributes |
|---|---|---|
| `gritz.task.busy` | `RunLoop`, around `acquire` … `busy` (and around the first `busy` for a restored sandbox). New root. | `task.id`, `task.version`, `gritz.backend` |
| `gritz.sandbox.create` / `gritz.sandbox.boot` | `TaskRunner.create` / `TaskRunner.boot` | `gritz.sandbox.id` |
| `gritz.sandbox.suspend` | `TaskRunner.suspend` | |
| `gritz.sandbox.destroy` | `TaskRunner.destroy` (own root: archive is not a busy period) | `task.id` |

A busy period that fails (`tr.fail`) records the error on the busy span and sets
its status to Error, with the same `why` the failed runner event carries. A Run
stream lost to `driverclient.ErrStreamIdle` shows up as the Run client span's
`deadline_exceeded` status, which otelconnect records already.

The slot wait is recorded as a `gritz.slot.wait` event on the busy span rather
than a child span, since a busy span starts only once the report wants a run.

Metrics, from `otel.Meter("github.com/gritzapp/gritz/internal/runner")`:

| Instrument | Kind | Meaning |
|---|---|---|
| `gritz.runner.slots.used` | Int64ObservableGauge | slots held, from `slots` |
| `gritz.runner.sandbox.boot.duration` | Float64Histogram (s) | create + boot time, by `gritz.backend`, `outcome` |
| `gritz.runner.busy.duration` | Float64Histogram (s) | busy-period length, by `gritz.backend`, `outcome` |
| `gritz.runner.outbox.pending` | Int64ObservableGauge | `RunnerEventOutbox.Len` |

plus the RPC metrics otelconnect emits for both clients.

### Driver

The driver is the harder half, for two reasons:

1. **Where its configuration comes from.** The driver's process environment is
   the sandbox's: the workspace's `container.environment` under Docker, the
   runner's own environment under the local backend, and nothing at all under
   Lambda MicroVMs, whose environment reaches runs only through
   `RunSpec.env` (`backend.RunEnver`). Secrets reach it only through
   `RunSpec.secrets`.
2. **Where its exporter can reach.** A runner's `OTEL_EXPORTER_OTLP_ENDPOINT`
   is often `http://localhost:4318`, which means nothing inside a container,
   and a Lambda VM reaches the network only through its egress connector.

So the driver does **not** inherit the runner's OTel configuration. It is
configured per workspace, through the same `OTEL_*` variables, set where every
other sandbox variable is set:

```yaml
workspaces:
  gritz:
    container:
      environment:
        OTEL_EXPORTER_OTLP_ENDPOINT: http://otel-collector:4318
    secrets:
      OTEL_EXPORTER_OTLP_HEADERS: ${env:OTLP_AUTH_HEADER}   # masked in the shipped log
```

The driver sets up OpenTelemetry from the **run environment** the first time it
receives a RunSpec: `Env(environ, spec)`, the same environment its runs see. In
the Run handler, right after `claim`, the server applies the `OTEL_*` variables
from `spec.env` and `spec.secrets` to its own process environment and calls
`otelx.Setup` with `ServiceName: "gritz-driver"` and `task.id`. Applying them to
the process environment, rather than teaching `otelx` to read a map, keeps one
`Setup` with the exporters' full set of `OTEL_*` variables. It is safe because
the run's base environment is `Options.Environ`, captured once at startup, so
nothing the driver sets on itself reaches a run that the run env didn't already
carry. Setup happens once per driver process: a later RunSpec with different
`OTEL_*` values takes effect when the driver next starts (the next Docker boot,
the next VM).

Setting up lazily works because the OTel globals delegate: tracers and meters
obtained before `otel.SetTracerProvider` / `SetMeterProvider` forward to the
real providers once they are set. The only span lost is the handler span of the
first Run stream itself, which started before Setup. Runs started on that stream
still parent on the runner's span, because a non-recording span carries the
remote span context it was started from.

Spans:

- **Run handler.** `Server.Handler` wraps the service in
  `otelconnect.NewInterceptor(otelconnect.WithTrustRemote(), otelconnect.WithoutTraceEvents())`.
  Trusting the remote context is right here and nowhere else: only the runner
  can reach the driver's socket or proxy.
- **`gritz.driver.run`.** Runs don't execute on the Run stream's context; the
  loop starts them from the events it stores. So `loop.open` records the
  stream's span context, and `startLocked` passes it to `Executor.Start`, which
  starts `gritz.driver.run` as its child on the run's own background context.
  A run outlives a detached stream, and its span may end after its parent's,
  which OpenTelemetry allows.
- **Inside the run.** `runAgent` passes the run span's context to the log
  shipper (today `context.Background()`), so `AppendLogChunk` calls nest under
  the run. `agent.Driver.Run` adds `gritz.agent.setup` around `setup` and
  `gritz.agent.exec` around the agent CLI, recording the exit status. The
  driver's `GetTask` / `SubmitRunnerEvents` calls already take the run's
  context, so their client spans nest with no further change.

**Flushing before suspend.** The driver's spans are exported in batches, but the
runner suspends the sandbox as soon as it reads Idle: Docker stops the
container, and Lambda freezes the VM without a signal. So before the Run handler
sends Idle it calls `Provider.ForceFlush`, bounded to 2s, so a busy period's
spans are exported before the sandbox can be suspended. On SIGTERM,
`ListenAndServe` shuts the provider down after the active run has stopped and
reported, so the run's last spans are exported too.

The driver exports no metrics of its own; the otelconnect client metrics on its
gritz client are enough to start. A periodic metric reader in a sandbox that is
suspended for most of its life would mostly export gaps.

### API

```go
package otelx // github.com/gritzapp/gritz/internal/x/otelx

// Options configures Setup. // new
type Options struct {
	// ServiceName is the default service.name; OTEL_SERVICE_NAME overrides it.
	ServiceName string
	// Attributes are extra resource attributes.
	Attributes []attribute.KeyValue
}

// Setup initializes OpenTelemetry tracing and metrics from the OTEL_* environment. // changed: takes opts
func Setup(ctx context.Context, opts Options) (*Provider, error)

// ForceFlush exports every span and metric not yet exported. // new
func (p *Provider) ForceFlush(ctx context.Context) error
```

```go
package driverserver // github.com/gritzapp/gritz/internal/runner/driverserver

// Start starts version's run as a child of the span parent. // changed: takes parent
func (e *Executor) Start(parent trace.SpanContext, version int64, spec *driverv1.RunSpec) error
```

**How it fits:** `server.go`, `runner.go` call `otelx.Setup` at startup;
`driverserver.Server` calls it from its Run handler on the first RunSpec and
shuts it down at the end of `ListenAndServe`.
`gritzclient.New` and `driverclient.New` add otelconnect interceptors.
`TaskRunner` and `Executor` / `agent.Driver` start the spans above.

## Implementation Plan

1. **`otelx.Setup` options** — Delivers: `otelx.Options` (service name, resource
   attributes), `Provider.ForceFlush`; `server.go` passes
   `ServiceName: "gritz-server"`. Depends on: nothing. Verifiable by: a unit
   test that the resource carries the service name, and that
   `OTEL_SERVICE_NAME` overrides it.
2. **Client propagation** — Delivers: otelconnect interceptors in
   `gritzclient.New` and `driverclient.New`. Depends on: nothing. Verifiable by:
   a test with a `tracetest.SpanRecorder` provider and an `httptest` server that
   the request carries a `traceparent` and a client span is recorded; existing
   `driverclient` idle tests still pass.
3. **Runner setup and spans** — Delivers: `otelx.Setup` and `logctx` in
   `runner.go`; `gritz.task.busy` and the sandbox spans in `TaskRunner`.
   Depends on: (1), (2). Verifiable by: a `TaskRunner` test with the fake
   backend and a span recorder asserting one busy span per version with boot
   and suspend children, and Error status on a failed boot; manually, a local
   runner against Jaeger shows server spans under the runner's.
4. **Driver setup and run spans** — Delivers: the driver's server interceptor,
   Setup from the first RunSpec, `ForceFlush` before Idle, `Shutdown` at
   the end of `ListenAndServe`, `gritz.driver.run` parented on the stream, the shipper on the
   run context, and the agent setup/exec spans; a `workspaces.yaml` example in
   the docs. Depends on: (1), (2). Verifiable by: a `driverserver` test that a
   Run stream opened with a `traceparent` yields a `gritz.driver.run` span with
   that parent; an e2e run with the local backend against a collector shows one
   trace across runner, driver and server.
5. **Runner metrics** — Delivers: the four runner instruments. Depends on: (3).
   Verifiable by: a `sdkmetric.ManualReader` test reading the instruments after
   a busy period.

## Trade-offs

- **Forward the runner's `OTEL_*` to the driver.** The runner could copy its own
  `OTEL_*` variables into each RunSpec, so the driver needs no workspace config.
  Rejected: the runner's endpoint is often unreachable from the sandbox
  (`localhost`, a private collector a Lambda VM can't route to), and copying
  `OTEL_EXPORTER_OTLP_HEADERS` would hand the runner's collector credentials to
  every sandbox and every agent, across all workspaces. Per-workspace
  configuration puts the endpoint where its network is known and the credentials
  under `secrets:`, where they are masked and opted into.
- **Relay the driver's spans through the runner.** The driver could send
  finished spans up the Run stream and the runner export them, so the sandbox
  needs no network path or credentials at all. Rejected for now: it needs a
  driver.v1 message type, a buffer for spans of runs that outlive their stream,
  and an exporter shim on both sides. It's worth revisiting if exporting from
  sandboxes proves impractical.
- **`otelx` reads a map instead of the process environment.** That would avoid
  `os.Setenv` in the driver, but `otelx` would have to reimplement each
  `OTEL_EXPORTER_OTLP_*` variable the exporters already parse (per-signal
  endpoints, timeouts, compression, certificates), or the server would lose
  them.
- **A trace per run instead of per busy period.** A busy period can run several
  consecutive versions on one stream (a restart arriving mid-run). Rooting at the
  busy period keeps boot, every run and suspend in one trace, and each
  `gritz.driver.run` still separates the versions.

## Open Questions

- **Linking the trace to the request that caused it.** The busy span is a new
  root, so the server request that created the task or delivered the restart
  (`CreateTask`, a GitHub webhook) is a separate trace. Recording the
  `traceparent` on the event row and adding it as a span link on the busy span
  would join them, but it needs a migration and a `gritz.v1.Event` field. Is
  that worth a follow-up?
- **Server service name.** Does the production deployment already set
  `OTEL_SERVICE_NAME` for the server? If not, layer (1) renames its traces from
  `unknown_service:gritz` to `gritz-server`, which may break saved queries.
- **AWS SDK spans.** `otelaws` could instrument the lambda-microvm backend's
  `RunMicrovm` / `ResumeMicrovm` calls under the boot span. It adds a contrib
  dependency; is it wanted now?
- **Agent telemetry.** Since the run environment carries the workspace's
  `OTEL_*` variables, agents that export OpenTelemetry themselves (Claude Code
  with `CLAUDE_CODE_ENABLE_TELEMETRY=1`) will send to the same collector. Should
  the driver also pass `TRACEPARENT` from `gritz.agent.exec` to the agent
  process, so its spans join the trace?
