# Run an image script in the Lambda MicroVM validate hook

Issue: https://github.com/gritzapp/gritz/issues/1825

## Problem

On a fresh Lambda MicroVM, the first read of a file in the image comes from
snapshot storage on demand, at about 3 to 5 MiB/s. Tools with many files are
slow on their first run. On a cold VM the `mcp-atlassian` stdio MCP server takes
more than 30s to start and misses Claude Code's MCP connect timeout. Once warm,
it connects in about 3s.

Lambda's `/validate` image hook is the fix. The
[AWS docs](https://docs.aws.amazon.com/lambda/latest/dg/microvms-images.html)
describe it as "Called after the build completes, on a new MicroVM started from
the created image". Lambda tracks which snapshot regions are read during
validation and prefetches them on every later launch: "run mock payloads during
validation. This enables Lambda to track accessed regions of your snapshot and
optimize their retrieval during MicroVM startup."

`gritz driver --aws-lambda-hooks` serves `/validate` with `&awsmicrovm.Handler{}`
(`internal/command/driver.go`), so the hook returns 200 straight away. Nothing
reads the image's files, so nothing gets prefetched. An image also has no way to
plug its own workload into the hook.

## Design

### A well-known script: `/gritz/validate`

When the driver serves `/validate`, it runs the executable at `/gritz/validate`
if the image has one. If the file is absent, the hook returns 200 as it does
today. There is no flag or config for the path.

Why `/gritz/validate`:

- `/gritz/` is already gritz's directory in the sandbox: `/gritz/log`
  (`agent.DefaultLogPath`), `/gritz/run.json` (`runrecord.Path`),
  `/gritz/.provisioned` (`driverserver.ProvisionedPath`) and, on Docker,
  `/gritz/driver.sock` (`docker.DriverAddr`). A file there obviously belongs to
  gritz and won't clash with the image's own layout.
- It is named after the hook, so the name says when it runs.
- It doesn't depend on the image's `PATH`, `WORKDIR` or user, unlike a bare
  command name or a path relative to the working directory.
- A fixed path means nothing in the image build or the workspace config can get
  out of sync with the driver. An image that ships the script gets warmed up,
  and an image that doesn't behaves exactly as it does today.

The path is a constant, `validatescript.Path`, so the README, the example
Dockerfile and the code all name the same value.

A missing script means no prefetching, and nothing would otherwise show it.
So the driver logs whether it found the script. It logs at startup (`validate
script found` / `no validate script, /validate will not warm the image`, with
the path) and again when the first `/validate` call arrives. Both log lines go
to the build's CloudWatch log group, `/aws/lambda-microvms/<image-name>`.

### A non-blocking hook: 503 while running

Warmup can take minutes, and AWS says a hook must not hold a request open:
"When returning HTTP 503, return it immediately rather than holding the request
open ... If the timeout elapses while a request is held open, Lambda ends the
build." Lambda keeps retrying a 503 until `validateTimeoutInSeconds` (1 to 3600)
runs out. The hook therefore works as a small state machine. It runs the script
at most once and remembers the result:

| State | `/validate` reply |
|---|---|
| no script | 200 |
| first call | start the script in the background, 503 |
| script running | 503 |
| script exited 0 | 200 (every later call too) |
| script failed (non-zero exit, signal, exec error) | 500 with the error (every later call too) |

Lambda retries, and nothing guarantees that two calls won't overlap, so a mutex
guards the state. The first call to reach it starts the script. Later calls only
read the state.

Today `awsmicrovm.Handler` can only give a hook 200 (nil) or 500 (error). It
needs a way to say "not yet". The proposal adds a sentinel error:

```go
// ErrInProgress is returned by a hook that has not finished yet; the handler
// replies 503 so Lambda retries.
var ErrInProgress = errors.New("awsmicrovm: hook in progress")
```

`Handler.serve` checks `errors.Is(err, ErrInProgress)` and replies
`503 Service Unavailable`. Any other error still gets 500. The hook func
signatures stay as they are. A sentinel is the smallest change that keeps
`awsmicrovm` independent of what the application does. It also works for
`Ready` should an image ever need it. Only `/ready` and `/validate` treat 503
as "retry". Microvm hooks (`run`, `resume`, ...) never return `ErrInProgress`,
and the `Handler` doc says it is for build hooks only.

### Failure policy: a failing script fails the build

If the script exits non-zero, `/validate` returns 500 and the image build
fails. The reasons:

- It catches broken images before any task runs on them. If `claude --version`
  fails during the build, it will also fail at the start of every task. The
  build log is the cheapest place to find that out.
- The hook is called *validate*. AWS describes it as confirming "the
  application works correctly when resumed", and authors will expect failures
  to count.
- Warmup that is expected to fail is the author's call, and the shell already
  handles it. Without task credentials, an MCP server may start, read all of its
  files and then exit non-zero. The author writes `|| true` on that line, or
  leaves out `set -e`. The other way round doesn't work: if gritz ignored
  failures, an author who wanted a build gate would have nothing to opt into.

The script being present but not executable is also a failure: exec fails with
`permission denied`, which gives a 500. The driver doesn't skip it quietly,
because an author who added the file clearly meant it to run.

### What the script gets

Validation happens at image build time, outside any task. The script gets:

- **Environment:** the driver's own `os.Environ()`, which is the image's `ENV`
  plus whatever Lambda sets for the process. This is the same base that
  `driverserver.Options.Environ` gives every run.
- **No task context:** no run, no task token, no `RunSpec.env` or
  `RunSpec.secrets`, no workspace `environment` or `secrets`, no agent config
  and no injected `gritz` MCP server. Nothing has called `Run` on the driver
  yet.
- **Process:** `/gritz/validate` run with no arguments, as the driver's user,
  with the driver's working directory and stdin from `/dev/null`.
- **Output:** stdout and stderr both go to the driver's stderr (the
  `*os.File`, so no copy goroutine is involved). That puts them in the build's
  CloudWatch logs next to the driver's own lines. The output is not masked:
  there are no secrets to mask, and `/gritz/log` and the log shipper aren't
  involved, because there is no task to ship to.

The script runs on a MicroVM *resumed from* the snapshot, after the snapshot was
taken. So it can only warm the image up. Files it writes and processes it leaves
running are not in the image. Caches that have to persist, such as `npm ci` or
`uv sync` output, belong in a Dockerfile `RUN` step. The script's job is to
*read* what tasks will read.

### Process cleanup

The script runs in its own process group (`Setpgid: true`). This follows the
setup commands in `internal/runner/agent/driver.go` and the agent CLIs
(`claude.go`, `codex.go`, ...). The driver kills the whole group:

- **when the script exits**, with `SIGKILL` to `-pid`, so a backgrounded MCP
  server or language server it started doesn't keep running and doesn't keep
  reading files during later validation. `ESRCH` (nothing left in the group) is
  ignored.
- **when the driver shuts down**. The command's signal context is the script's
  context, and `cmd.Cancel` sends `SIGTERM` to the group, with a `WaitDelay`
  backstop, just like the setup commands.

The script must outlive the HTTP request that started it, so it runs under the
context the command passes to `validatescript.New`, not under `r.Context()`.
`internal/command/driver.go` therefore creates the `signal.NotifyContext` before
it starts the hook server.

The driver has no timeout of its own. `validateTimeoutInSeconds` already bounds
the build. When it runs out, Lambda ends the build and discards the VM, which
takes the process group with it. A second timer inside the driver would only
add something else to keep in sync.

### Driver wiring

```go
if cmd.Bool("aws-lambda-hooks") {
	script := validatescript.New(ctx, validatescript.Options{Environ: os.Environ()})
	// ...listen on awsmicrovm.HookPort...
	go http.Serve(l, &awsmicrovm.Handler{Validate: script.Validate})
}
```

The `--aws-lambda-hooks` usage changes to "Serve the AWS Lambda MicroVM
lifecycle hooks on their port, running /gritz/validate on /validate". The other
hooks stay nil and keep acknowledging with 200.

### Comment fixes in `awsmicrovm`

Several comments in `internal/x/awsmicrovm/handler.go` get the build hooks
wrong. They say the hooks run "before any MicroVM exists" and that `/validate`
lets the image assert its health "before the snapshot is finalized".
`/ready` runs on the build VM and gates the snapshot. `/validate` runs after
the snapshot, on a new MicroVM started from the image. These comments are
corrected:

- the `HookReady`/`HookValidate` constant block: build-time hooks called during
  `create-microvm-image`. `/ready` gates the snapshot on the build VM.
  `/validate` runs afterwards on a MicroVM started from the image, and Lambda
  records the snapshot regions read during it for prefetch. Both may return 503
  to ask for a retry.
- `ReadyHookRequest`: drop "no MicroVM exists yet". It runs on the build VM.
  Say instead that the body carries no MicroVM id.
- `ValidateHookRequest`: "delivered to the /validate build hook on a MicroVM
  started from the newly created image, after the snapshot".
- `Handler`: document `ErrInProgress` → 503 next to nil → 200 and error → 500.

(The AWS page's intro says "Hooks run before Lambda takes the snapshot", but
the hook table is the more specific statement. It matches the prefetch
behaviour that this proposal relies on.)

### Docs

`internal/runner/backend/lambdamicrovm/README.md`:

- Replace "The hook server acknowledges every hook with 200 and does no work"
  with a description of `/validate` and the script.
- Raise the example's `validateTimeoutInSeconds` from 60 to 900. A cold
  `mcp-atlassian` alone takes more than 30s at 3 to 5 MiB/s, so 60s fails a
  realistic warmup. Explain that the value has to cover the script's cold run
  and can go up to 3600.
- Add a "Warming the image" section with the Dockerfile lines, the example
  script below, the environment the script gets, the failure policy, and a
  pointer to the build log group for its output.

`microvm.Dockerfile` gets an optional, commented-out example:

```dockerfile
# Optional: warm the image. On /validate the driver runs /gritz/validate, and
# Lambda prefetches what it reads on every later launch. See README.md.
# COPY validate /gritz/validate
# RUN chmod 0755 /gritz/validate
```

An example `validate` script:

```sh
#!/bin/sh
# Runs once during create-microvm-image, on a MicroVM started from the image.
# Read what tasks will read; there are no credentials or task env here.
set -eu

# The agent CLI: a hard failure here means the image is broken.
claude --version

# Start a stdio MCP server long enough for it to import everything. Without
# credentials it may exit non-zero after initializing, which is fine. The
# driver kills anything left in the process group when this script exits.
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"gritz-validate","version":"0"}}}' |
	timeout 120 mcp-atlassian >/dev/null || true
```

### API

```go
package awsmicrovm // github.com/gritzapp/gritz/internal/x/awsmicrovm

// ErrInProgress is returned by a build hook that has not finished; the handler
// replies 503 so Lambda retries. // new
var ErrInProgress error
```

```go
package validatescript // github.com/gritzapp/gritz/internal/runner/validatescript

// Path is where an image puts the script the driver runs on /validate.
const Path = "/gritz/validate"

// Options configures a Script. Every field takes its default when zero.
type Options struct {
	// Path is the script. Defaults to Path.
	Path string
	// Environ is the script's environment.
	Environ []string
	// Output receives the script's stdout and stderr. Defaults to os.Stderr.
	Output *os.File
	// Log is the logger. Defaults to slog.Default().
	Log *slog.Logger
}

// Script runs an image's validate script at most once and remembers the result.
type Script struct{ ... }

// New checks for the script and logs whether it was found; ctx bounds the
// script's run.
func New(ctx context.Context, opts Options) *Script

// Validate is an awsmicrovm.Handler.Validate hook: it starts the script on the
// first call and returns awsmicrovm.ErrInProgress while it runs, nil once it
// exits 0, and its error once it fails.
func (s *Script) Validate(ctx context.Context, req awsmicrovm.ValidateHookRequest) error
```

**How it fits:** `gritz driver --aws-lambda-hooks` builds a `validatescript.Script`
under its signal context and sets it as `awsmicrovm.Handler.Validate`. The
handler maps the hook's `ErrInProgress` to 503, an error to 500, and nil to
200. `validatescript` imports `awsmicrovm` for the request type and the
sentinel. `awsmicrovm` knows nothing about gritz.

## Implementation Plan

1. **`awsmicrovm` 503 + comment fixes**: Delivers: `ErrInProgress`, the 503
   mapping in `Handler.serve`, and the corrected build-hook comments.
   Depends on: nothing. Verifiable by: a handler test showing a hook returning
   `ErrInProgress` gets 503. Nothing returns it yet, so behaviour is unchanged.
2. **`validatescript` package**: Delivers: `Script` with the state machine,
   process group, kill on exit and on context cancel, and output to the
   given file. Depends on: (1). Verifiable by: tests with temp scripts. Absent
   gives nil. A running script gives `ErrInProgress` and runs only once.
   Exit 0 gives nil on every later call. Exit 1 gives an error on every later
   call. A backgrounded child is gone after the script exits.
3. **Driver wiring**: Delivers: `gritz driver --aws-lambda-hooks` serving
   `Validate: script.Validate`, the signal context moved ahead of the hooks,
   the startup log line, and the updated flag usage. Depends on: (2).
   Verifiable by: running the driver locally with a `/gritz/validate` script
   and calling `POST :9000/aws/lambda-microvms/runtime/v1/validate` with
   curl. It should answer 503, 503, then 200.
4. **Docs**: Delivers: the README changes (timeout raised to 900, a "Warming
   the image" section, the hook description) and the commented
   `microvm.Dockerfile` lines. Depends on: (3). Verifiable by: building an
   image with the example script. Check that the script's output shows up in
   `/aws/lambda-microvms/<image-name>`, then compare cold MCP start time on a
   VM launched from it.

## Trade-offs

- **A flag or workspace setting for the path:** rejected. The script belongs to
  the image, and so does the knowledge of what to warm. A configurable path is
  one more thing the image build and the entrypoint flags have to agree on. A
  fixed path is zero-config, and an image without the script is unaffected.
- **Commands in workspace config:** rejected. The image is built with
  `create-microvm-image`, outside any workspace. One image can serve several
  workspaces, and the runner is not involved in the build.
- **Blocking until the script exits:** simpler, but AWS explicitly says not to
  hold hook requests open, and doing so ends the build when the timeout runs
  out. A 503 state machine is the documented contract.
- **Warming in `/ready` (before the snapshot):** the page cache would be in the
  memory snapshot. But Lambda only tracks prefetch regions during validation,
  and processes started before the snapshot would be frozen into every VM.
  `/validate` is the hook built for this.
- **Ignoring script failures:** makes warmup never-fatal, but takes away the
  only cheap build-time check that the image works. `|| true` gives authors
  per-command control.
- **A driver-side timeout:** rejected. `validateTimeoutInSeconds` already
  bounds the build, and when it runs out Lambda discards the VM.

## Open Questions

- Does the validate MicroVM have the build's network egress? The script
  shouldn't need the network to read files that are already in the image, but
  some tools phone home on start (update checks). If egress is missing, those
  tools may stall until their own timeouts and use up the validate budget.
- Is 900s a good README default for `validateTimeoutInSeconds`, or should the
  example use the 3600s maximum, given that a timed-out build just has to be
  rerun?
