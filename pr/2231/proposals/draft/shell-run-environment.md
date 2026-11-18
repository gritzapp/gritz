# Run environment for gritz shell

Issue: https://github.com/gritzapp/gritz/issues/2221

## Problem

A `gritz shell` session gets a different environment from the agent it is meant
to help debug.

The driver builds each run's environment with
[`driverserver.Env`](https://github.com/gritzapp/gritz/blob/master/internal/runner/driverserver/driverserver.go):
the driver's own environ (`Options.Environ`, set to `os.Environ()`), then
`RunSpec.env`, then `RunSpec.secrets`. The executor hands it to the run as
`agent.Driver.Env`, and the driver passes it to the setup `commands:` and to the
agent CLI.

The reverse shell is the one process in a run that doesn't get it.
[`shell.Serve`](https://github.com/gritzapp/gritz/blob/master/internal/shell/shell.go)
starts `$SHELL` with `exec.CommandContext` and leaves `cmd.Env` nil, so the
shell inherits the driver process's environ: the image's variables and the
workspace's `container.environment`, nothing more. It is missing everything
that only the run environment carries:

- `GRITZ_TASK_ID`, `GRITZ_TOKEN` and `GRITZ_SERVER`, which the runner puts in
  `RunSpec.env` (`internal/runner/spec.go`). Without them `gritz tool
  git-credential` and `gritz tool github-mcp` fail in the shell, so an operator
  can't reproduce a failed `git push` or GitHub MCP call the way the agent made
  it.
- The workspace's `secrets:`, carried in `RunSpec.secrets`.
- Any other `RunSpec.env` entry the runner adds (`RunEnv`), and the task
  variables proposed in [#2218](https://github.com/gritzapp/gritz/pull/2218),
  which go in `RunSpec.env` too.

Today the only way to see what the agent saw is to read
`/proc/<pid>/environ` of a process that no longer runs.

## Design

### A shell is a run, so it already has a run environment

The premise "the shell should borrow the environment of the current or last
run" turns out not to apply: a shell session is not a side channel next to a
run, it *is* a run.

`OpenShell` (`internal/server/apiserver/shell.go`) only accepts a terminal
task. It sets `tasks.shell_session` and issues `START` in one transaction. The
runner opens a `Run` stream with a fresh `RunSpec`, exactly as it would to
start the agent. The driver loop starts a run from that spec
(`loop.startLocked`), the executor builds that run's `agent.Driver` with
`Env: Env(e.environ, spec)`, and only then does `Driver.run` see
`task.GetShellSession()` and call `shell.Serve` instead of `runAgent`. A shell
session never overlaps an agent run, and it never borrows one.

So the shell run already holds the environment the agent would have had, built
from the same spec by the same function: `Driver.Env`. It just doesn't pass it
on. The whole change is to pass it:

- `shell.ServeOptions` gains `Env []string`, the shell's complete environment.
  `Serve` sets `cmd.Env = opts.Env`.
- `Driver.run` passes `Env: d.Env`.
- `Serve` picks the shell binary from `opts.Env` instead of the driver's own
  `os.Getenv("SHELL")`: `cmp.Or(envx.Expand("${SHELL}", opts.Env), "/bin/sh")`.
  A workspace that sets `SHELL` in `RunSpec.env` gets it for the shell as well.

`Env` is the complete environment, not a set of additions: `Serve` stops
reading the driver's environ altogether. The driver's environ is the first
layer of `Driver.Env` anyway, so nothing the shell has today is lost. `Serve`
doesn't merge anything itself, so there is still one place that defines a run's
environment (`driverserver.Env`), and the shell can't drift from the agent's.

Which spec, concretely: the one the loop holds when it starts the shell run.
That is the spec from the `Run` stream the runner opened to start it, so its
token is the one the runner minted for this busy period, and its secrets are
the workspace's secrets as the runner has them now. `GRITZ_TOKEN` in the shell
is the same token the driver uses to dial the relay (`Driver.Token`), since both
come from `spec.GetToken()`.

### No run yet

The issue asks what a shell gets when the sandbox has never run. It can happen:
a task can reach a terminal state without its sandbox ever starting a run, for
example when it was stopped before a runner claimed it, or when the sandbox
failed to launch. `OpenShell` then brings the sandbox up for the first time, and
the shell run is its first run.

That case needs no handling. The environment comes from the shell run's own
spec, never from an earlier run, so a first-run shell gets the same complete
environment as any other. The loop never starts a run without a spec
(`startLocked` returns early when `l.spec` is nil and waits for a stream that
brings one), so `Driver.Env` always has the `RunSpec.env` and `RunSpec.secrets`
layers when `shell.Serve` runs.

What a first-run shell doesn't have is anything the setup `commands:` would
have produced: a cloned repository, installed tools, files under the working
directory. See the differences below.

### Invariant

In production `Driver.Env` is never empty: it always holds at least the
`GRITZ_*` entries of `RunSpec.env`. `Serve` asserts that rather than falling
back to the driver's environ:

```go
if opts.Env == nil {
	panic("shell: ServeOptions.Env is nil; the driver passes the run's environment")
}
```

No test covers the panic, per `CLAUDE.md`. Existing `internal/shell` tests that
call `Serve` directly pass `os.Environ()` (plus whatever they assert on).

### Intentional differences from the agent

The shell gets the run environment. It deliberately doesn't get the rest of
what happens around the agent process:

- **No per-agent additions.** Some agent integrations append to the run
  environment for their own CLI only. `claude.go` adds `IS_SANDBOX=1` and
  `DISABLE_AUTOUPDATER=1`, and the dummy agent adds its MCP servers' `env`.
  These configure one agent binary, not the sandbox, and the shell isn't tied to
  an agent type. An operator who runs `claude` by hand in the shell can set them.
- **No setup commands.** A shell run doesn't call `Driver.setup`. Setup
  commands are the agent's preparation and they advance
  `SetupCommandsCompleted` in the agent config, so a debug session running them
  would change what the next agent run does. The shell inspects the sandbox as
  the last run left it.
- **No masking.** The shell's PTY stream goes to the operator through the
  relay. It is not written to the shipped log, so the mask built from
  `driverserver.Secrets` doesn't apply, the same as it doesn't apply to
  `/gritz/log` and stderr. This is the trust boundary
  [`/operators/secrets/`](/operators/secrets/) already describes: the shell runs
  as the driver, which can read every secret anyway. Putting them in the shell's
  environment makes them easier to reach, not reachable for the first time.
- **Working directory unchanged.** The shell still starts in the driver's
  working directory, not the agent's expanded `cwd`. See Open Questions.

### Effect on the task variables proposal

[#2218](https://github.com/gritzapp/gritz/pull/2218) says task variables "don't
appear in `gritz shell` sessions". That holds today only because the shell
doesn't get `RunSpec.env` at all, not as a decision about variables. With this
proposal they appear in the shell with no extra work, since they travel in
`RunSpec.env`. Whichever of the two lands second updates that sentence.

### API

```go
package shell // github.com/gritzapp/gritz/internal/shell

// ServeOptions configures Serve. A nil Log falls back to slog.Default.
type ServeOptions struct {
	ServerURL string
	Token     string
	Session   string
	// Env is the shell's complete environment; it must not be nil. // new
	Env []string
	Log *slog.Logger
}

// Serve runs an interactive debug shell for a rendezvous session, started with
// opts.Env as its environment and $SHELL from opts.Env, else /bin/sh.
func Serve(ctx context.Context, opts ServeOptions) error // changed: environment
```

`agent.Driver` and `driverserver` have no API change: `Driver.Env` keeps its
meaning, and its doc comment adds the shell to its consumers ("the complete
environment of the agent CLI, the setup commands and a debug shell").

**How it fits:** the executor builds `agent.Driver` with
`Env: driverserver.Env(environ, spec)`, as it does today. `Driver.run` passes
`d.Env` to `shell.Serve` when the task has a `shell_session`, and `Serve` sets it
as the shell's `cmd.Env`.

## Implementation Plan

1. **Shell takes its environment** — Delivers: `ServeOptions.Env`, `cmd.Env =
   opts.Env`, `$SHELL` read from `opts.Env`, and the nil assertion.
   `Driver.run` passes `d.Env` in the same PR, because a nil `Env` panics and
   the only production caller has to supply it. The `Driver.Env` doc comment
   adds the shell. Docs: the "A shell in the sandbox" section of
   `/operators/debugging/` says that the shell has the agent's environment
   (the workspace's environment and secrets, plus `GRITZ_TASK_ID`, `GRITZ_TOKEN`
   and `GRITZ_SERVER`), so tools like `git push` with the gritz credential
   helper behave as they did for the agent. Depends on: nothing.
   Verifiable by: an `internal/shell` test that serves with an `Env` holding a
   marker variable, runs `echo $MARKER` through the relay and reads the value
   back, and checks that a variable set only in the test process's environ is
   absent. A driver test runs a shell-session task and checks the shell sees
   `GRITZ_TASK_ID` and a `RunSpec.secrets` value.
2. **End-to-end check** — Delivers: an `internal/e2e` case on the local
   backend that opens a shell on a finished task whose workspace declares a
   secret, and asserts `GRITZ_TASK_ID`, `GRITZ_SERVER` and the secret are set in
   the session. Depends on: (1). Verifiable by: the test itself.

Layer 1 is small enough to review on its own. Layer 2 is split out only so the
e2e harness work doesn't hold up the fix.

## Trade-offs

- **Pass `Driver.Env` vs. rebuild the environment in `Serve`.** `Serve` could
  take the `RunSpec` and call `driverserver.Env` itself. That would make
  `internal/shell` depend on the driver protocol, which the package doc rules
  out ("do not depend on the driver, agent, or server packages"), and it would
  rebuild a value the run already holds. A plain `[]string` keeps `Serve` a
  library function.
- **Borrow the last agent run's environment.** The issue suggested taking the
  environment from the current or last run the driver holds. There is never a
  current run besides the shell run itself (`OpenShell` requires a terminal
  task). The last agent run's spec may have an expired token, outdated secrets,
  or not exist at all. The shell run's own spec is fresher and always there.
- **Layer the run env over the shell's inherited environ in `Serve`.** Same
  result today, since `Driver.Env` starts with the driver's environ, but it
  would be a second definition of the run environment and would drift if
  `driverserver.Env` ever changed. Replacing the environment keeps one
  definition.
- **Leave secrets out of the shell.** It would keep secrets one step further
  away, but the shell runs as the driver, which holds them anyway, and the
  point of the issue is to reproduce what the agent saw. A failed authenticated
  setup command or MCP call often can't be reproduced without them.
- **Run the setup commands before the shell.** It would make a first-run shell
  look like an agent run's sandbox, but it would change agent state from a debug
  session, could take minutes, and could fail. Inspecting the sandbox as it was
  left is what `gritz shell` is for.

## Open Questions

- **Start in the agent's working directory?** The agent runs in
  `envx.Expand(cfg.Cwd, d.Env)` from the agent config. The config is
  provisioned before the shell starts, so the shell run could load it and start
  there too, which would put the operator straight in the repository. This is
  the same "set up like the agent's" idea, applied to the directory. It's left
  out of this proposal because it adds a config load to the shell path and the
  issue is about the environment. Worth doing in the same change?
- **`TERM`.** The shell keeps whatever `TERM` the driver's environ has, usually
  none, so full-screen programs may misbehave. Forwarding the operator's `TERM`
  in the attach handshake is a separate change to the `shellwire` protocol and
  is out of scope here.