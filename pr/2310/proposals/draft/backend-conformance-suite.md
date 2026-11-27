# Backend conformance suite and test tiers

Issue: https://github.com/gritzapp/gritz/issues/2308

## Problem

`backend.Backend` and `backend.Sandbox`
([`backend.go`](https://github.com/gritzapp/gritz/blob/master/internal/runner/backend/backend.go),
[`sandbox.go`](https://github.com/gritzapp/gritz/blob/master/internal/runner/backend/sandbox.go))
are one contract with three implementations: `docker`, `lambdamicrovm` and
`local`. Each package tests that contract with its own hand-written copy:

- `docker/sandbox_test.go` and `local/local_test.go` each have a
  `TestSandbox_Lifecycle` that asserts the same sequence (created is suspended,
  boot is idempotent, the driver answers, suspend is idempotent, a suspended
  driver doesn't answer, destroy, then `ErrGone` and a second `Destroy` that
  returns nil). `lambdamicrovm/sandbox_test.go` has a third, against a fake
  cloud.
- The helpers are copied too: `reach` is the same 25 lines in all three
  packages, and `dialReady`, `assertStatus` and `create` are in two.
- Coverage drifts. `local` checks that a second `Backend` on the same root sees
  a sandbox the first one booted, and that two sandboxes run side by side.
  `docker` checks reopen and concurrent use. Neither checks what the other does,
  though the contract promises all of it for every backend.

The tests that need a real runtime have no tier of their own:

- **Docker** tests talk to the local daemon and run in the main
  `go test ./...` of `test.yml` on every PR, starting containers whether or not
  the PR touches Docker. `test.yml` even turns off Postgres durability partly so their container
  creates are faster.
- **Lambda MicroVMs** are only tested against `fakeCloud`, which serves every
  VM from one in-process driver. Nothing in CI ever starts a real MicroVM, so a
  change to the proxy, the hooks or the image is only exercised once it's
  deployed.
- **Tart** ([#1999](https://github.com/gritzapp/gritz/issues/1999)) needs Apple
  silicon and macOS. GitHub's hosted macOS runners can't nest virtualization, so
  it can't run in CI at all, and today there is nowhere to put its tests.

## Design

The contract gets one shared suite, `internal/runner/backend/backendtest`. Each
backend's tests run it against that backend instead of asserting the contract
themselves. Backend-specific tests stay only for what the contract doesn't say:
platform state mappings, validation, and runtime quirks.

Tests split into two tiers, chosen by one environment variable,
`GRITZ_TEST_BACKENDS`:

| Tier | Runs | Where |
| --- | --- | --- |
| Default (`go test ./...`) | the suite against `local`, the e2e scenarios, every test that needs no real runtime (Lambda against `fakeCloud`, Docker's `ValidateWorkspace`) | `test.yml`, every PR |
| Backend | the suite and the backend-specific tests that need a real runtime, for each backend named in `GRITZ_TEST_BACKENDS` | `backend-docker.yml`, `backend-lambda.yml`, a Mac by hand |

### The suite

`backendtest.Run(t, cfg)` runs the contract as parallel subtests. It only goes
through `backend.Backend`, `backend.Sandbox` and the `driverv1connect` client
`Dial` returns. It never looks at containers, directories or VMs. Each subtest
creates its own sandboxes from a spec with a random task id, version 1 and
`cfg.Workspace`, and destroys them on cleanup.

| Subtest | Asserts |
| --- | --- |
| `Lifecycle` | `Create` returns a sandbox whose `Handle` has a non-empty `Type` and `ID` and reports `StatusSuspended`. `Boot` reports `StatusUp`, and a second `Boot` is a no-op. The driver answers a `Run` stream. `Suspend` reports `StatusSuspended`, a second `Suspend` is a no-op, and the driver does not answer. `Boot` again reports `StatusUp` and the driver answers. `Destroy` reports `StatusGone`; then `Boot` and `Suspend` return `ErrGone` and a second `Destroy` returns nil. `Handle` returns the same value at every step. |
| `Reopen` | A second `Backend` from `cfg.New`, as a restarted runner gets, `Open`s the handle of a booted sandbox and gets an equal `Handle` and `StatusUp`, and its driver answers. Suspending through the reopened sandbox shows as `StatusSuspended` through the original. |
| `Stale` | Two `Sandbox` values for one created, never-booted sandbox (from `Create` and `Open`). Destroying it through one leaves the other reporting `StatusGone`, `ErrGone` from `Boot` and `Suspend`, and nil from `Destroy`. `Open` of the destroyed handle succeeds and reports `StatusGone` too. |
| `SideBySide` | Two booted sandboxes. Suspending one leaves the other `StatusUp`, and its driver still answers. |
| `Concurrent` | Four goroutines call `Boot`, `Status`, `Dial` and a `Run` stream on one booted sandbox at the same time. Each sees `StatusUp` and a driver that answers. Run with `-race`. |
| `Open` | `Open` fails for a handle of another `Type` and for one with an empty `ID`. No sandbox is created. |

That is everything the doc comments on `Backend` and `Sandbox` promise and a
test can observe through them. The suite leaves out:

- **`ValidateWorkspace`, `BinaryPath`, `RunEnver`.** Each is per-backend by
  definition.
- **Cleanup when `Create` fails.** Each backend fails at a different step
  (Docker's binary copy), and only a backend-specific test can make that step
  fail.
- **Transitional states** (Lambda's `SUSPENDING`/`TERMINATING`, a paused Docker
  container). A real runtime can't be held in them on demand. They stay as
  backend-specific tests that inject the state (`fakeCloud.add`, `docker pause`).
- **What `Spec.Env`, `Secrets` and `Files` do inside the sandbox.** The suite
  would have to start a run to see them. See [Open Questions](#open-questions).

A "driver answers" check is the `reach` that is copied into all three packages
today: open a `Run` stream, send an empty `RunRequest`, and accept `Hello` or
the driver's "another Run stream is open" refusal. `WaitDriver` polls it until
it succeeds. The "does not answer" check makes one attempt under a 10 s
deadline and expects an error.

The suite's timeouts fit the slowest backend. `WaitDriver` polls for up to
2 minutes, 250 ms apart. Against `local` the polls return as soon as the
driver answers, so the long timeout costs nothing there.

### The gate: an environment variable, not a build tag

`backendtest.Require(t, name)` skips the test unless the comma-separated
`GRITZ_TEST_BACKENDS` contains `name`, the backend's `HandleType` (`docker`,
`lambda-microvm`, `tart`). The skip message names the variable and the value to
set. `local` is never gated: it needs nothing beyond the prebuilt binary the
default tier already requires.

A `//go:build` tag would also keep these tests out of `go test ./...`. An
environment gate is the better fit:

- **Gated tests still compile on every PR.** `go test ./...`, `go vet` and
  `golangci-lint` all build them. A tagged file only compiles when its tag is
  set, so renaming a method in `backendtest` or a backend would break
  `docker/conformance_test.go` silently, and nobody would know until the nightly
  run. gopls also skips tagged files unless the editor is configured for them.
- **One file per test, not per tier.** A backend's real-runtime tests sit next
  to its default-tier tests (Docker's `ValidateWorkspace`, Lambda's `fakeCloud`
  tables) and share their helpers. With a tag, the shared helpers would have to
  move into a third, untagged file, or be duplicated.
- **Skips are visible.** `go test -v` lists each gated test as `SKIP`, with the
  reason. A tagged test just isn't there.
- **The test cache tracks it.** `go test` records the environment variables a
  test reads, so switching `GRITZ_TEST_BACKENDS` reruns the gated tests instead
  of replaying a cached skip.

`testing.Short()` points the wrong way: the default `go test ./...` would still
run every slow test, and only `-short` would skip them.

The gate goes at the top of each gated test, not inside `Run`. Docker's
backend-specific tests need the daemon too, and every gated test then reads the
same way:

```go
func TestConformance(t *testing.T) {
	backendtest.Require(t, docker.HandleType)
	backendtest.Run(t, backendtest.Config{...})
}
```

### Per backend: what moves, what's deleted, what stays

The suite goes in a `conformance_test.go` in each backend package, in
`package <backend>_test`: it only needs the exported constructor.

**`local`** (default tier, ungated):

| Test | Fate |
| --- | --- |
| `TestSandbox_Lifecycle` | Into the suite (`Lifecycle`). The file checks (a suspended sandbox keeps its files, the directory is gone after `Destroy`) stay as `TestSandbox_SuspendKeepsFiles`, in `package local_test`, using the `Root` it passes to `New`. |
| `TestBackend_SecondBackend` | Into the suite (`Reopen`). |
| `TestBackend_SideBySide` | Into the suite (`SideBySide`). |
| `TestOpen` | Foreign type and empty id go into the suite (`Open`). The `../etc` path check stays. |
| `TestBackend_CreateExists`, `TestBackend_Kill`, `TestNew` | Stay: a directory clash, `Kill` and the socket path limit are `local`'s own. |
| `reach`, `dialReady`, `assertStatus`, `create` | Deleted; the remaining tests use `backendtest.WaitDriver`. |

**`docker`** (backend tier, `Require(t, "docker")`, except `TestValidateWorkspace`):

| Test | Fate |
| --- | --- |
| `TestSandbox_Lifecycle` | Into the suite. Its `ContainerInspect` checks (the handle id is the container id, the state is `created`) are deleted: the runner never relies on them. |
| `TestSandbox_Reopen` | Into the suite (`Reopen`). |
| `TestSandbox_Concurrent` | Into the suite (`Concurrent`). |
| `TestOpen` | Into the suite (`Open`), deleted. |
| `TestSandbox_Status` | Stays, reduced to the paused and exited containers it starts out of band. The running and nonexistent cases are in the suite. |
| `TestDial_TunnelsThroughExec` | Stays: the exec tunnel under each network mode is Docker's. |
| `TestSandbox_BootRecreatedNetwork` | Stays: the `compose down && up` repair is Docker's. |
| `TestValidateWorkspace` | Stays, ungated: it needs no daemon. |
| `TestMain` (`GRITZ_PREBUILT_DIR`), `startContainer` | Stay. |
| `reach`, `dialReady`, `assertStatus`, `create`, `newTestBackend` | Deleted. |

**`lambdamicrovm`**: the `fakeCloud` tests stay in the default tier, and the
suite runs against real AWS in the backend tier (`Require(t, "lambda-microvm")`).
The suite can't run against `fakeCloud`, because the fake serves every VM from
one in-process driver that never stops: a suspended VM's driver still answers,
and `SideBySide` can't tell two VMs apart.

| Test | Fate |
| --- | --- |
| `TestSandbox_Lifecycle` (fake) | Stays, cut down to what the suite can't see: the control-plane calls it makes (one `SuspendMicrovm`, one `ResumeMicrovm`, two `TerminateMicrovm`) and the fake's states after each step. It is the only per-PR Lambda lifecycle check. |
| `TestSandbox_Status`, `TestSandbox_Boot`, `TestSandbox_Suspend`, `TestSandbox_SuspendPollsUntilSuspended`, `TestSandbox_Destroy` | Stay: the `MicrovmState` tables, transitional states included, are Lambda's mapping. |
| `TestSandbox_Dial`, `TestSandbox_DialLearnsEndpoint`, `TestSandbox_Concurrent` | Stay: proxy-token minting and reuse, and endpoint discovery. |
| `TestOpen` | The malformed-`Data` case stays; foreign type and empty id are in the suite. |
| `TestTokenTransport_Refresh`, `TestValidateWorkspace`, `TestCreate_InvalidWorkspace` | Stay. |
| `reach` | Deleted; the tests use `backendtest.WaitDriver`. |

The gated `TestConformance` builds the backend as `gritz runner` does, with
`lambdamicrovms.NewFromConfig` from the default AWS config. Its workspace comes
from the environment: `GRITZ_TEST_LAMBDA_MICROVM_IMAGE` (an image built from the
commit under test) and `GRITZ_TEST_LAMBDA_MICROVM_ROLE`, the managed
`INTERNET_EGRESS` connector for `AWS_REGION`, and `max_duration_seconds: 900`.
The duration cap is the leak guard: Lambda terminates a VM at that limit,
suspended or not, so a job killed before its cleanup leaves nothing running for
long. A test that finds either variable unset fails rather than skips. Asking
for `lambda-microvm` without configuring it is a mistake.

**`tart`** ([#1999](https://github.com/gritzapp/gritz/issues/1999)): the
backend's first PR adds a gated `conformance_test.go`, so the suite is its
contract test from the start.

`backend_test.go` (`TestSpecClone`) is unaffected.

### CI

**`test.yml`** keeps running `go test ./...` with `GRITZ_TEST_BACKENDS` unset.
Docker's daemon tests skip; the suite against `local` and the e2e scenarios run.
The Postgres-durability step stays (the store tests benefit too), and its
comment stops mentioning Docker.

**`backend-docker.yml`** (new) runs on:

- `pull_request` and `push` to master, with `paths` set to
  `internal/runner/backend/*.go`, `internal/runner/backend/backendtest/**`,
  `internal/runner/backend/docker/**`, `internal/x/dockerx/**`, `go.mod`,
  `go.sum` and the workflow file itself,
- `schedule`, nightly,
- `workflow_dispatch`.

It builds `prebuilt/gritz-linux-amd64` and runs
`GRITZ_TEST_BACKENDS=docker go tool gotestsum --format github-actions -- -race ./internal/runner/backend/...`.
It needs no Postgres. The nightly run catches changes outside those paths that
still reach the Docker backend, such as the driver or the `gritz tool` tunnel.

**`backend-lambda.yml`** (new) runs on `schedule` (nightly) and
`workflow_dispatch` only. It never runs on `pull_request`, so fork PRs never
see its credentials. It:

1. Gets AWS credentials through GitHub OIDC
   (`aws-actions/configure-aws-credentials`). The role it assumes lives in
   `gritzapp/infra`, scoped to the MicroVM control-plane verbs, image
   create/delete, and one S3 prefix for code artifacts.
2. Builds a `linux/arm64` binary and a MicroVM image from it, with
   `.github/scripts/lambda-test-image.sh`. The script is the README's three
   steps (binary, zip, `create-microvm-image`), waits for the build and prints
   the image ARN. The image has to be built from the commit, because the driver
   protocol ships with the runner.
3. Runs the backend packages with `GRITZ_TEST_BACKENDS=lambda-microvm` and the
   two `GRITZ_TEST_LAMBDA_MICROVM_*` variables.
4. Deletes the image (`if: always()`).

**Tart** runs by hand on an Apple silicon Mac. There is no workflow job until we
have a self-hosted Mac runner.

**Locally**, a new mise task runs the backend tier:

```bash
mise run test:backends docker                 # GRITZ_TEST_BACKENDS=docker, ./internal/runner/backend/...
mise run test:backends docker -- -run=Status  # extra go test args, as mise run test takes them
```

`mise run test` stays the default tier. CLAUDE.md's "Running Tests" section
gains the task and the variable.

### API

```go
package backendtest // github.com/gritzapp/gritz/internal/runner/backend/backendtest

// Config describes the backend under test.
type Config struct {
	// New returns a backend on the runtime under test and registers its Close
	// on t.Cleanup. Every call returns a new Backend over the same sandboxes,
	// as a restarted runner gets.
	New func(t *testing.T) backend.Backend
	// Workspace is the workspace of every sandbox the suite creates.
	Workspace *workspace.Workspace
}

// Run runs the conformance suite for the backend.Backend contract against
// cfg's backend, as parallel subtests of t.
func Run(t *testing.T, cfg Config)

// Require skips t unless GRITZ_TEST_BACKENDS lists name, a backend's
// HandleType.
func Require(t *testing.T, name string)

// WaitDriver waits until the driver behind client answers a Run stream, and
// fails t if it does not within the suite's timeout.
func WaitDriver(t *testing.T, client driverv1connect.DriverServiceClient)
```

**How it fits:** each backend package's `conformance_test.go` calls `Require`
(except `local`), then `Run` with a `New` that wraps the backend's own
constructor. For `local`, `New` closes over one root, so the second backend sees
the first one's sandboxes. Backend-specific tests call `WaitDriver` where they
used their own `reach` and `dialReady`. Nothing outside `_test.go` files imports
`backendtest`.

## Implementation Plan

1. **Suite against `local`** — Delivers: `backendtest` (`Config`, `Run`,
   `Require`, `WaitDriver`), `local/conformance_test.go`, and `local`'s
   contract tests and helpers deleted or slimmed as in the table. Depends on:
   nothing. Verifiable by: `mise run test -- ./internal/runner/backend/...`
   runs the suite against `local` in the default tier, and breaking `local`'s
   `Suspend` idempotence fails `Lifecycle`.
2. **Docker on the suite, in its own tier** — Delivers:
   `docker/conformance_test.go`, `Require` on the daemon tests, Docker's
   duplicated tests and helpers deleted, `backend-docker.yml`, the
   `test:backends` mise task, the CLAUDE.md note and the `test.yml` comment.
   The workflow lands in the same PR as the gate, so Docker coverage never
   drops out of CI. Depends on: (1). Verifiable by: `test.yml` shows the Docker
   daemon tests as skipped, and `backend-docker.yml` runs them and passes on
   the PR itself (its paths match).
3. **Lambda conformance test** — Delivers: `lambdamicrovm/conformance_test.go`
   (gated, workspace from `GRITZ_TEST_LAMBDA_MICROVM_*`), `reach` replaced by
   `WaitDriver`, the fake `TestSandbox_Lifecycle` cut down to the
   control-plane calls, the generic `TestOpen` cases deleted. Depends on: (1).
   Verifiable by: the default tier still passes, and a run against a dev AWS
   account with a hand-built image passes the suite.
4. **Lambda workflow** — Delivers: `.github/scripts/lambda-test-image.sh`,
   `backend-lambda.yml`, and (in `gritzapp/infra`) the OIDC role and S3
   prefix. Depends on: (3). Verifiable by: a `workflow_dispatch` run builds
   the image, passes the suite, and leaves no image or VM behind.
5. **Tart** — Delivers: the Tart backend's `conformance_test.go`, as part of
   [#1999](https://github.com/gritzapp/gritz/issues/1999)'s implementation,
   not a separate PR. Depends on: (1). Verifiable by:
   `mise run test:backends tart` on an Apple silicon Mac.

## Trade-offs

- **Build tag vs environment gate.** See
  [the gate](#the-gate-an-environment-variable-not-a-build-tag). The main cost
  of the env gate is that a gated test has to call `Require` itself. Forgetting
  it fails `test.yml` for Lambda and Tart, which have no runtime there. For
  Docker it doesn't, because hosted runners have a daemon: the test just runs in
  the default tier again, and only review catches it.
- **One variable vs one per backend** (`GRITZ_TEST_DOCKER=1`, ...). One list
  gives a single place to look, one mise task, and lets `Require` take the
  `HandleType` the package already exports. Per-backend settings (Lambda's
  image and role) are separate variables, read only by that backend's test.
- **Running the suite against Lambda's `fakeCloud` on every PR.** Rejected: the
  fake shares one never-stopping driver between VMs, so `Lifecycle` and
  `SideBySide` would fail. Making the fake run a driver per VM and stop it on
  suspend would mean writing a second `local` backend inside the fake. The
  `fakeCloud` tests keep covering the state mapping per PR, and the suite covers
  the real platform nightly.
- **A `Case` table the backend can filter vs one `Run`.** Letting backends opt
  out of cases would let a backend quietly not meet the contract. If a backend
  can't pass a case, either the contract or the backend is wrong. `Run` has no
  knobs.
- **Fewer, longer subtests vs one per promise.** Each subtest creates its own
  sandboxes, which costs real money and minutes on Lambda. Grouping the
  sequential promises in `Lifecycle` keeps the suite at six sandboxes per run
  while each subtest still fails on its own.
- **Keeping Docker in the default tier.** Rejected: it starts real containers
  in every PR's run, and most PRs don't touch it. Path-filtered runs plus a nightly
  run catch Docker regressions without the cost. The catch is that a
  path-filtered workflow can't be a required check, so a red
  `backend-docker.yml` doesn't block a merge.

## Open Questions

- **Spec delivery.** Should the suite also check that `Spec.Files` are
  provisioned and that `Env` and `Secrets` reach the run? That means starting a
  run through the driver with the dummy agent and reading back what it saw. It
  is the one part of the contract nobody tests per backend today, but it would
  make the suite depend on the driver's run protocol.
- **Suspended Lambda driver.** Does the managed proxy refuse a request to a
  suspended MicroVM, or hold it, or resume the VM? The "does not answer"
  assertion in `Lifecycle` assumes it refuses. If not, the contract should say
  nothing about a suspended sandbox's driver, and the assertion goes.
- **Nightly failure alerts.** GitHub emails a scheduled workflow's failure only
  to whoever last edited its cron line. Should the nightly jobs post to a
  channel instead, or fire a gritz custom event that opens a task?
- **Lambda image reuse.** Building an image every night costs a few minutes and
  an S3 upload. Caching one per driver-protocol change would need a key we don't
  have. Is building per run acceptable?