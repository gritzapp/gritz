# Run an image script in the Lambda MicroVM validate hook

Issue: https://github.com/gritzapp/gritz/issues/1825

## Problem

On a fresh Lambda MicroVM, the first read of a file in the image comes from
snapshot storage on demand, at about 3 to 5 MiB/s. Tools with many files are
slow the first time they run. On a cold VM the `mcp-atlassian` stdio MCP server
takes more than 30s to start and misses Claude Code's MCP connect timeout. Once
the VM is warm, the same server connects in about 3s.

Lambda's `/validate` image hook exists for this. The
[AWS docs](https://docs.aws.amazon.com/lambda/latest/dg/microvms-images.html)
say it is "Called after the build completes, on a new MicroVM started from the
created image". Lambda tracks which snapshot regions are read during validation
and prefetches them on every later launch: "run mock payloads during
validation. This enables Lambda to track accessed regions of your snapshot and
optimize their retrieval during MicroVM startup."

`gritz driver --aws-lambda-hooks` serves `/validate` with `&awsmicrovm.Handler{}`
(`internal/command/driver.go`), which returns 200 straight away. Nothing reads
the image's files, so nothing gets prefetched. An image also has no way to plug
its own workload into the hook.

## Design

### A driver flag: `--aws-lambda-validate`

`gritz driver` gets a new flag, `--aws-lambda-validate=<path>`, which names an
executable in the image. When it is set, the driver runs that executable on
`/validate`. The image author sets the flag in the `ENTRYPOINT` they already
write:

```dockerfile
COPY validate /usr/local/libexec/gritz-validate
RUN chmod 0755 /usr/local/libexec/gritz-validate
ENTRYPOINT ["/usr/local/bin/gritz", "driver", "--aws-lambda-hooks", "--aws-lambda-validate=/usr/local/libexec/gritz-validate"]
```

- **No default.** If the flag is unset, there is no script and `/validate`
  returns 200 as it does today.
- **It only works with `--aws-lambda-hooks`.** Setting `--aws-lambda-validate`
  without `--aws-lambda-hooks` makes the command fail with an error. A script
  with nothing to call it is a configuration mistake.
- **The path is checked at startup.** When the flag is set,
  `validatescript.New` stats the path. If the file is missing, is not a regular
  file, or has no execute bit, the driver exits with an error naming the path.
  The check runs on the build VM before the snapshot. If it fails, `/ready`
  never answers 200 and the image build fails right there. Because of this
  check, the case where a script silently doesn't run (and nothing gets
  prefetched) can't happen, so the driver doesn't log whether it found the
  script.

gritz doesn't claim any location in the image. The trade-offs section explains
why a fixed path under `/gritz/` is rejected.

### A non-blocking hook: 503 while running

Warmup can take minutes, and AWS says a hook must not hold a request open:
"When returning HTTP 503, return it immediately rather than holding the request
open ... If the timeout elapses while a request is held open, Lambda ends the
build." Lambda keeps retrying a 503 until `validateTimeoutInSeconds` (1 to
3600) runs out. So the hook is a small state machine that runs the script at
most once and remembers the result:

| State | `/validate` reply |
|---|---|
| flag unset | 200 (`Handler.Validate` is nil, as today) |
| first call | start the script in the background, 503 |
| script running | 503 |
| script exited 0 | 200 (every later call too) |
| script failed (non-zero exit, signal, exec error) | 500 with the error (every later call too) |

Lambda retries, and nothing guarantees that two calls won't overlap, so a mutex
guards the state. The first call to reach it starts the script. Later calls only
read the state.

Today `awsmicrovm.Handler` can only map a hook's result to 200 (nil) or 500
(error), and it needs a way to say "not yet". This proposal adds a sentinel
error:

```go
// ErrInProgress is returned by a hook that has not finished yet; the handler
// replies 503 so Lambda retries.
var ErrInProgress = errors.New("awsmicrovm: hook in progress")
```

`Handler.serve` checks `errors.Is(err, ErrInProgress)` and replies
`503 Service Unavailable`. Every other error still gets 500. The hook func
signatures don't change. A sentinel is the smallest change that keeps
`awsmicrovm` free of knowledge about what the application does, and `Ready`
could use it too if an image ever needs that. Only `/ready` and `/validate`
treat 503 as "retry", so the `Handler` doc limits `ErrInProgress` to build
hooks. No microvm hook (`run`, `resume`, ...) returns it.

### Failure policy: a failing script fails the build

If the script exits non-zero, `/validate` returns 500 and the image build fails.
The reasons:

- It catches broken images before any task runs on them. If `claude --version`
  fails during the build, it would fail at the start of every task too, and the
  build log is the cheapest place to find that out.
- The hook is called *validate*. AWS describes it as confirming "the
  application works correctly when resumed", and authors will expect a failure
  to count.
- Warmup that is expected to fail is the author's call, and the shell already
  handles it. Without task credentials, an MCP server may start, read all of its
  files and then exit non-zero. The author writes `|| true` on that line, or
  leaves out `set -e`. If gritz ignored failures instead, an author who wanted a
  build gate would have no way to get one.

A file that is missing or not executable never gets as far as `/validate`,
because the startup check makes the driver exit first.

### What the script gets

Validation runs during the image build, outside any task. The script gets:

- **Environment:** the driver's own `os.Environ()`, meaning the image's `ENV`
  plus whatever Lambda sets for the process. Every run's environment starts
  from the same base (`driverserver.Options.Environ`).
- **No task context:** no run, no task token, no `RunSpec.env` or
  `RunSpec.secrets`, no workspace `environment` or `secrets`, no agent config,
  no injected `gritz` MCP server. Nothing has called `Run` on the driver yet.
- **Process:** the flag's path, run with no arguments, as the driver's user,
  in the driver's working directory, with stdin from `/dev/null`.
- **Output:** stdout and stderr both go to the driver's stderr. That is an
  `*os.File`, so no copy goroutine is needed. The output lands in the build's
  CloudWatch logs (`/aws/lambda-microvms/<image-name>`) next to the driver's
  own lines. It is not masked: there are no secrets to mask, and `/gritz/log`
  and the log shipper aren't involved because there is no task to ship to.

The script runs on a MicroVM *resumed from* the snapshot, after the snapshot
was taken, so all it can do is warm the image up. Files it writes and processes
it leaves behind are not in the image. Caches that must persist, such as
`npm ci` or `uv sync` output, belong in a Dockerfile `RUN` step. The script's
job is to *read* the files tasks will read.

### Process cleanup

The script runs in its own process group (`Setpgid: true`), the same way the
setup commands in `internal/runner/agent/driver.go` and the agent CLIs
(`claude.go`, `codex.go`, ...) run. The driver kills the whole group:

- **When the script exits:** `SIGKILL` to `-pid`. This stops a backgrounded MCP
  server or language server the script started, so it doesn't keep running and
  reading files during the rest of validation. `ESRCH` (nothing left in the
  group) is ignored.
- **When the driver shuts down:** the command's signal context is the script's
  context, and `cmd.Cancel` sends `SIGTERM` to the group, with `WaitDelay` as a
  backstop, as the setup commands do.

The script must outlive the HTTP request that started it, so it runs under the
context the command passes to `validatescript.New`, not `r.Context()`. To make
that context available, `internal/command/driver.go` creates
`signal.NotifyContext` before it starts the hook server.

The driver doesn't set its own timeout. `validateTimeoutInSeconds` already
limits the build. When it runs out, Lambda ends the build and discards the VM,
and the process group goes with it. A second timer inside the driver would just
be one more value to keep in sync.

### Driver wiring

```go
&cli.StringFlag{
	Name:  "aws-lambda-validate",
	Usage: "Executable to run on the AWS Lambda MicroVM /validate image hook, to warm the snapshot (requires --aws-lambda-hooks)",
},
```

```go
if cmd.IsSet("aws-lambda-validate") && !cmd.Bool("aws-lambda-hooks") {
	return errors.New("--aws-lambda-validate requires --aws-lambda-hooks")
}
if cmd.Bool("aws-lambda-hooks") {
	hooks := &awsmicrovm.Handler{}
	if path := cmd.String("aws-lambda-validate"); path != "" {
		script, err := validatescript.New(ctx, validatescript.Options{Path: path, Environ: os.Environ()})
		if err != nil {
			return err
		}
		hooks.Validate = script.Validate
	}
	// ...listen on awsmicrovm.HookPort, http.Serve(l, hooks)...
}
```

The other hooks stay nil and keep returning 200.

### Comment fixes in `awsmicrovm`

The comments in `internal/x/awsmicrovm/handler.go` describe the build hooks
wrongly. They say the hooks run "before any MicroVM exists" and that
`/validate` lets the image assert its health "before the snapshot is
finalized". In fact `/ready` runs on the build VM and gates the snapshot, while
`/validate` runs after the snapshot, on a new MicroVM started from the image.
The fixes:

- The `HookReady`/`HookValidate` constant block: these are build-time hooks
  called during `create-microvm-image`. `/ready` gates the snapshot on the
  build VM. `/validate` runs afterwards on a MicroVM started from the image,
  and Lambda records the snapshot regions read during it for prefetch. Both
  may return 503 to ask for a retry.
- `ReadyHookRequest`: drop "no MicroVM exists yet", since the hook runs on the
  build VM. Say that the body carries no MicroVM id.
- `ValidateHookRequest`: "delivered to the /validate build hook on a MicroVM
  started from the newly created image, after the snapshot".
- `Handler`: document `ErrInProgress` → 503 alongside nil → 200 and error →
  500.

(The intro on the AWS page says "Hooks run before Lambda takes the snapshot".
The hook table is the more specific statement, and it matches the prefetch
behaviour this proposal relies on.)

### Docs

`internal/runner/backend/lambdamicrovm/README.md`:

- Replace "The hook server acknowledges every hook with 200 and does no work"
  with a description of `/validate` and `--aws-lambda-validate`.
- Raise the example's `validateTimeoutInSeconds` from 60 to 900. A cold
  `mcp-atlassian` alone takes more than 30s at 3 to 5 MiB/s, so 60s is too short
  for a realistic warmup. Say that the timeout has to cover the script's cold
  run and can go up to 3600.
- Add a "Warming the image" section. It covers the Dockerfile lines (the
  `COPY` to a path the author picks, plus the flag in `ENTRYPOINT`), the
  example script below, the environment the script gets, the startup check,
  the failure policy, and where the output goes (the build's log group).

`microvm.Dockerfile` gets an optional, commented-out example, next to an
`ENTRYPOINT` variant:

```dockerfile
# Optional: warm the image. With --aws-lambda-validate the driver runs the
# script on /validate, and Lambda prefetches what it reads on every later
# launch. See README.md.
# COPY validate /usr/local/libexec/gritz-validate
# RUN chmod 0755 /usr/local/libexec/gritz-validate
# ENTRYPOINT ["/usr/local/bin/gritz", "driver", "--aws-lambda-hooks", "--aws-lambda-validate=/usr/local/libexec/gritz-validate"]
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

// Options configures a Script.
type Options struct {
	// Path is the script. Required.
	Path string
	// Environ is the script's environment.
	Environ []string
	// Output receives the script's stdout and stderr. Defaults to os.Stderr.
	Output *os.File
}

// Script runs an image's validate script at most once and remembers the result.
type Script struct{ ... }

// New returns a Script for opts.Path, or an error if it is not an executable
// regular file; ctx bounds the script's run.
func New(ctx context.Context, opts Options) (*Script, error)

// Validate is an awsmicrovm.Handler.Validate hook: it starts the script on the
// first call and returns awsmicrovm.ErrInProgress while it runs, nil once it
// exits 0, and its error once it fails.
func (s *Script) Validate(ctx context.Context, req awsmicrovm.ValidateHookRequest) error
```

An empty `Options.Path` is a programming error, so `New` panics on it. The
command only calls `New` when the flag is set.

**How it fits:** when `--aws-lambda-validate` is set, `gritz driver
--aws-lambda-hooks` builds a `validatescript.Script` under its signal context
and sets it as `awsmicrovm.Handler.Validate`. The handler maps the hook's
`ErrInProgress` to 503, any other error to 500, and nil to 200.
`validatescript` imports `awsmicrovm` for the request type and the sentinel,
and `awsmicrovm` knows nothing about gritz.

## Implementation Plan

1. **`awsmicrovm` 503 and comment fixes.** Delivers: `ErrInProgress`, the
   503 mapping in `Handler.serve`, and the corrected build-hook comments.
   Depends on: nothing. Verifiable by: a handler test where a hook returns
   `ErrInProgress` and gets 503. Nothing returns it yet, so behaviour doesn't
   change.
2. **The `validatescript` package.** Delivers: `Script` with the startup
   check, the state machine, the process group, killing the group when the
   script exits or the context is cancelled, and output to the given file.
   Depends on: (1). Verifiable by: tests with temp scripts. A missing or
   non-executable path makes `New` return an error. A running script returns
   `ErrInProgress` and runs only once. Exit 0 returns nil on every later call.
   Exit 1 returns an error on every later call. A backgrounded child is gone
   after the script exits.
3. **Driver wiring.** Delivers: the `--aws-lambda-validate` flag and its check
   that `--aws-lambda-hooks` is also set, `Handler.Validate` set from the
   script, and the signal context moved ahead of the hooks. Depends on: (2).
   Verifiable by: running the driver locally with
   `--aws-lambda-validate=<script>` and repeatedly running `curl -X POST
   :9000/aws/lambda-microvms/runtime/v1/validate`, which should answer 503,
   503, then 200. With a missing path, the driver should exit at startup.
4. **Docs.** Delivers: the README changes (the timeout raised to 900, the
   "Warming the image" section, the hook description) and the commented
   `microvm.Dockerfile` lines. Depends on: (3). Verifiable by: building an
   image with the example script, checking that its output shows up in
   `/aws/lambda-microvms/<image-name>`, and comparing cold MCP start time on a
   VM launched from it.

## Trade-offs

- **A flag (chosen) vs. a fixed path under `/gritz/`.** A fixed
  `/gritz/validate` would put image content in a directory that gritz creates
  and writes to at runtime, and the permissions conflict:
  - Docker backend: the runner creates `/gritz` as a `0777` directory when it
    creates the container, so that a non-root driver can write `driver.sock`,
    `run.json` and `log` (`internal/runner/backend/docker/docker.go`,
    `internal/runner/taskmanager.go`). A `COPY validate /gritz/validate` in the
    image would create `/gritz` as root-owned `0755` instead.
  - Lambda backend: the driver creates `/gritz/*` itself (`provision` and
    `writeFile` in `internal/runner/driverserver/driverserver.go`). If the
    image creates `/gritz` as root-owned `0755` and the driver isn't root, the
    driver can't write `run.json`, `log` or `.provisioned`.

  With the flag, the author picks the path in the `ENTRYPOINT` they already
  write, and gritz claims no location in the image. The flag also makes the
  intent explicit, so a wrong path can fail the build at startup instead of
  quietly turning off prefetching.
- **Commands in workspace config:** rejected. The image is built with
  `create-microvm-image`, outside any workspace. One image can serve several
  workspaces, and the runner is not involved in the build.
- **Blocking until the script exits:** simpler, but AWS explicitly says not to
  hold hook requests open, and a request still open when the timeout runs out
  ends the build. Returning 503 while the script runs is the documented
  contract.
- **Warming in `/ready`, before the snapshot:** the page cache would be in the
  memory snapshot. But Lambda only tracks prefetch regions during validation,
  and processes started before the snapshot would be frozen into every VM.
  `/validate` is the hook meant for this.
- **Ignoring script failures:** warmup could never fail the build, but that
  throws away the only cheap build-time check that the image works. `|| true`
  gives authors control per command.
- **A timeout in the driver:** rejected. `validateTimeoutInSeconds` already
  limits the build, and when it runs out Lambda discards the VM.

## Open Questions

- Does the validate MicroVM have the build's network egress? The script
  shouldn't need the network to read files that are already in the image, but
  some tools phone home on start (update checks). Without egress, those tools
  may hang until their own timeouts and use up the validate time limit.
- Is 900s a good README default for `validateTimeoutInSeconds`, or should the
  example use the 3600s maximum, since a build that times out just has to be
  re-run?
