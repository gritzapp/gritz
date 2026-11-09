# Slim sandbox binary

Issues: https://github.com/gritzapp/gritz/issues/1150, https://github.com/gritzapp/gritz/issues/1661

## Problem

Every sandbox gets the full `gritz` binary at `backend.BinaryPath`
(`/usr/local/bin/gritz`). That binary contains the server, the embedded web UI,
the runner, the Docker client, the AWS SDK, pgx, SQLite, OTel/gRPC and go-github.
Only these subcommands run inside a sandbox:

| Command | Started by |
| --- | --- |
| `gritz driver` | the Docker container's `Cmd`, the MicroVM image's `ENTRYPOINT`, the local backend |
| `gritz tool proxy` | the Docker backend's `execConn`, for every driver.v1 connection (`docker exec`) |
| `gritz tool agent-mcp` | the agent, as the injected `gritz` MCP server (`internal/runner/spec.go`) |
| `gritz tool git-credential` | git, on every fetch and push, when a workspace configures it as a credential helper |
| `gritz tool github-mcp` | the agent, when a workspace configures it as an MCP server |

`gritz tool microvm-shim`, which #1661 measured, was removed in 1233735b.
`gritz tool proxy` is new since then.

The binary reaches a sandbox in one of two ways:

- **Docker.** For every task, `docker.Backend.create` calls `prebuilt.ReadBinary(arch)`.
  If the runner is a static Linux binary for the image's architecture,
  `ReadBinary` reads the runner's own executable (`os.Executable()`). Otherwise
  it reads `gritz-linux-<arch>` from the prebuilt directory (`GRITZ_PREBUILT_DIR`,
  or `prebuilt/` in the config directory). `gritz download` fills that directory
  from the *latest* GitHub release's `gritz_linux_<arch>.tar.gz` archives. The
  runner image puts both arches in `/app/prebuilt`. The backend then holds the
  whole file in memory, copies it into a tar `bytes.Buffer`, and runs
  `CopyToContainer`. Each container's writable layer keeps its own copy until
  the container is pruned.
- **Lambda MicroVMs.** The operator bakes a `linux/arm64` `gritz` binary into the
  image (`internal/runner/backend/lambdamicrovm/microvm.Dockerfile`). Today
  that is a full binary built with `go build ./cmd/gritz`, or one taken from the
  release archive.

The **local** backend runs its own executable (`os.Executable()` in
`internal/command/runner.go`). The tests and e2e harness run
`prebuilt/gritz-linux-<arch>`, which `mise run build` and `test.yml` build.

Every build site uses `CGO_ENABLED=0 go build` with no stripping:
`mise run build`, `mise run dev`, `Dockerfile`, `test.yml` and
`.goreleaser.yaml`. The release build only adds `-X …version.Version`.

### Why the binary is so big

#1661's analysis still holds. All three causes are still present on `master`
(741fea19):

1. **`internal/command` is one package.** Each `var XCommand = &cli.Command{Action: …}`
   is initialized at package level, so any `main` that imports the package keeps
   every subcommand's `Action` reachable, along with everything those actions call.
   A `main` that registers only `command.DriverCommand` still lists all 813
   packages of the full binary in `go list -deps`, and comes to 55.8 MB.
2. **`text/template` execution turns off dead-method elimination.**
   `text/template.(*state).evalField` calls `reflect.Value.MethodByName`, so the
   linker keeps every exported method of every reachable type. The sandbox
   reaches it from two places, and the full binary from three:
   - urfave/cli's help printer. v3.14.0 removes it with the
     `urfave_cli_no_template` build tag. We are on v3.10.1.
   - `agentprompt.promptTemplate` (`PROMPT.md`). It runs `.Task.GetId`
     and similar methods by name.
   - Full binary only: `golang.org/x/net/trace`, which comes in through grpc from
     the OTel OTLP exporters. `-tags grpcnotrace` removes it.

   The saving appears only when every caller is gone. Removing just one saves
   nothing (see the table below).
3. **No stripping.** DWARF and the symbol tables account for about 30% of every
   binary.

`gritzclient/client_moq_wait.go` is a non-test file, and it imports
`gotest.tools/v3/poll`. That brings `go-cmp` and gotest.tools, which itself
imports `text/template`, into every binary that uses `gritzclient`. The linker
drops all of it and the template is never executed, so this costs nothing
today. It is worth moving behind a test-only file anyway.

### Measurements

These sizes are fresh. They were taken on `master` at 741fea19 with go1.27.3,
`GOOS=linux CGO_ENABLED=0`, using scratch code only. All sizes are `stat -c %s`,
in MB = 10^6 bytes. "Stripped" means `-trimpath -ldflags="-s -w"`.

The template fix is the same in every row that has it: urfave/cli v3.14.0
built with `-tags urfave_cli_no_template`, plus `agentprompt.Render`
rewritten with a `strings.Builder` so it no longer uses `text/template`. The
full binary also needs `grpcnotrace`.

| # | Configuration | amd64 | amd64 stripped | arm64 | arm64 stripped |
| --- | --- | ---: | ---: | ---: | ---: |
| F0 | full `./cmd/gritz` (today) | **65.39** | 46.08 | **62.00** | 43.38 |
| F1 | F0 + `grpcnotrace` | 64.91 | 45.72 | 61.42 | 42.99 |
| F2 | F0 + cli v3.14.0 + tag + `agentprompt` rewrite | 65.47 | 46.14 | 62.02 | 43.38 |
| F3 | F2 + `grpcnotrace`: every template caller gone | 52.01 | 36.91 | 49.17 | 34.73 |
| F4 | F3 + both sandbox binaries embedded, gzipped (see Design) | 61.58 | 46.47 | 58.74 | 44.30 |
| A0 | driver only, `main` imports `command.DriverCommand` | 55.77 | 38.87 | 52.77 | 36.57 |
| A1 | A0 + template fix + `grpcnotrace` | 41.79 | 29.26 | 39.44 | 27.53 |
| B0 | driver only, its file copied into `package main` (no `internal/command`) | 27.73 | 18.92 | 26.22 | 17.83 |
| B1 | B0 + template fix | 19.75 | 13.49 | 18.48 | 12.58 |
| P0 | `driver` + `tool proxy` (the minimum the Docker backend needs) | 27.75 | 18.93 | 26.22 | 17.83 |
| P1 | P0 + template fix | 19.77 | 13.50 | 18.48 | 12.58 |
| S0 | **sandbox**: `driver` + `tool {agent-mcp,git-credential,github-mcp,proxy}`, no `internal/command` | 28.14 | 19.20 | 26.54 | 18.02 |
| S1 | S0 + template fix | **20.37** | **13.91** | **19.06** | **12.91** |
| S1−proxy | S1 without `tool proxy` | 20.36 | 13.90 | 19.06 | 12.91 |

Compressed, with `gzip -9` and `xz -9`:

| Binary | Stripped | gzip | xz |
| --- | ---: | ---: | ---: |
| F0 amd64 | 46.08 | 14.12 | 10.27 |
| F3 amd64 | 36.91 | 11.78 | 8.68 |
| S1 amd64 | 13.91 | 5.04 | 3.88 |
| S1 arm64 | 12.91 | 4.53 | 3.30 |

What the numbers show:

- **`internal/command` is now the largest single cost.** Importing it for
  `DriverCommand` alone costs 28 MB (A0 vs B0). #1661 measured 15.5 MB, so the
  coupling has grown with the runner's SQLite state, the new servers, and so on.
- **The template fix only pays off once every caller is gone.** F2 is no smaller
  than F0, and F1 saves only the trace package itself. F3 saves 13.4 MB on the
  full binary and 7.8 MB on the sandbox binary (S0 → S1).
- **`tool proxy` costs almost nothing.** It is about 10 KB (S1 vs S1−proxy),
  because `internal/x/stdioproxy` uses only `io` and `net`.
  `agent-mcp`, `git-credential` and `github-mcp` together add 0.6 MB on top of
  the driver (S1 vs P1), mostly the MCP SDK. A driver-only binary is therefore
  not worth having separately from the sandbox binary.
- **Nothing heavy is linked into the sandbox binary any more.** S1 has 18
  modules and 345 packages: connect, protobuf, the MCP SDK, urfave/cli,
  coder/websocket and creack/pty (reverse shell), plus small helpers. There is no
  AWS, Docker, pgx or grpc. `internal/x/awsmicrovm` no longer imports the AWS
  SDK, and with `microvm-shim` gone so are #1661's compile-time-only
  `lambdamicrovm`/`workspace` edges.
- Stripping saves about 30% of every configuration.

The result is 65.4 → 13.9 MB per Docker task on amd64 (−79%), and 62.0 → 12.9 MB
in the MicroVM image on arm64.

## Design

Ship a separate **sandbox binary** that contains only the in-sandbox commands,
built with the linker triggers removed and stripped. Embed it in the `gritz`
binary, so the runner always carries the sandbox binary of its own release.
Publish it as a release asset for MicroVM images.

### The sandbox binary

- Move `driver.go`, `tool.go`, `agent_mcp.go`, `git_credential.go`,
  `github_mcp.go` and `proxy.go` from `internal/command` to a new package,
  `internal/command/sandboxcmd`. None of them uses anything else in
  `internal/command`; they compile unchanged as their own package.
- Add `cmd/gritz-sandbox`. Its root command is named `gritz` and registers only
  `sandboxcmd.DriverCommand` and `sandboxcmd.ToolCommand`, with
  `version.String()` as its version.
- In the sandbox it is still installed as `/usr/local/bin/gritz`
  (`backend.BinaryPath`). Every in-sandbox command line therefore stays the same:
  the container `Cmd`, the `docker exec` proxy, the injected MCP server, and
  workspaces' `!gritz tool git-credential` helpers.
- The full `gritz` binary drops `driver` and `tool`, along with `download`
  (see below). Nothing on the host runs them.

### Removing the linker triggers

- `agentprompt.Render` is written in plain Go with a `strings.Builder`. That is
  already the style of `renderHeader` and `renderLink`. `PROMPT.md` and
  `promptTemplate` are deleted. The existing `agentprompt` golden tests pin
  the output byte for byte.
- urfave/cli goes to ≥ v3.14.0. Every build uses the tags
  `urfave_cli_no_template,grpcnotrace`. They go in `mise.toml`'s
  `[env] GOFLAGS`, which covers `mise run build/dev/install/test/lint`, and are
  also set in `Dockerfile`, `test.yml`, `lint.yml` and `.goreleaser.yaml`. #1661's
  follow-up confirmed that help output stays byte-identical under the tag, and
  gritz uses no custom help templates.
- **Guards.**
  - A CI step fails the build if
    `go build -ldflags=-dumpdep ./cmd/gritz ./cmd/gritz-sandbox` reports
    `<ReflectMethod>`. Without it, a new template caller would silently undo the
    saving.
  - A test runs `--help` on every command of both binaries under the tag,
    because a custom `*HelpTemplate` panics in tagged builds.
  - A `sandboxbin` test asserts that each embedded binary stays under a size
    budget (6 MB gzipped). It also checks that `go list -deps ./cmd/gritz-sandbox`
    contains no `internal/server`, `internal/store`, `internal/runner/backend`,
    `aws-sdk-go-v2` or `docker/docker`.
- `gritzclient/client_moq_wait.go` moves into a `_test.go`-only helper package,
  so `gotest.tools` and `go-cmp` leave the binaries' module graph.

### Build flags

Every artifact built for release or shipped into a sandbox uses
`-trimpath -ldflags="-s -w -X …version.Version=<tag>"`: the release archives,
the images and the embedded sandbox binaries. Panics still print function names
and line numbers, because `.gopclntab` is kept. We lose DWARF in shipped
binaries, which only matters for attaching delve to a release build. Local
`go build` stays unstripped.

### Building and embedding

A new package `internal/runner/sandboxbin` embeds
`gritz-sandbox-linux-amd64.gz` and `gritz-sandbox-linux-arm64.gz`. Both files
are gitignored and produced by `go generate ./internal/runner/sandboxbin`,
which runs a `build.sh` next to them. The script runs, for each arch:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath \
  -ldflags "-s -w -X github.com/gritzapp/gritz/internal/version.Version=${VERSION:-dev}" \
  -o - ./cmd/gritz-sandbox | gzip -9 > gritz-sandbox-linux-$arch.gz
```

`go generate` is the single entry point every build site calls before building
`./cmd/gritz`. That is the same contract the web UI already has: `go build`
fails until `internal/server/webui` exists.

| Build site | Change |
| --- | --- |
| `mise run build` | a `build:sandbox` task (with `sources`/`outputs`, like `build:webui`) runs the generate step; `build` depends on it. The `prebuilt/` outputs go away. |
| `mise run dev`, `mise run install`, `mise run lint` | depend on `build:sandbox` |
| `test.yml` | the "Build prebuilt binaries for runner tests" step becomes `go generate ./internal/runner/sandboxbin` |
| `lint.yml` | writes empty placeholder `.gz` files, as it already does for the web UI's `index.html` |
| `Dockerfile` | the builder stage runs the generate step with `VERSION`, then builds `gritz`. The `prebuilt/` builds, `/app/prebuilt` and `GRITZ_PREBUILT_DIR` are removed from the runner image. |
| `.goreleaser.yaml` | a `before.hooks` entry runs the generate step with `VERSION={{ .Tag }}` |

gzip comes from the standard library. xz would save another 2.3 MB per pair
but needs a third-party decoder. The embedded payload is 9.6 MB, so the full
binary goes from 65.4 MB today to 46.5 MB stripped (F4). The runner and server
images therefore shrink even though they now carry both sandbox binaries.

### Delivery per backend

- **Docker.** `create` calls `sandboxbin.Binary(info.Architecture)` instead of
  `prebuilt.ReadBinary`. `Binary` decompresses each arch once (`sync.Once`)
  and returns the cached 13–14 MB slice. An unknown arch is an error, as it is
  today. The `os.Executable()` shortcut, the prebuilt directory,
  `GRITZ_PREBUILT_DIR`, `gritz download` and `internal/runner/prebuilt` are
  deleted. The runner always carries the sandbox binary from its own build, on
  any host OS and arch, including macOS. That also removes today's skew:
  `gritz download` fetches the *latest* release, not the runner's.
- **Local.** `local.Options.Binary` stays a path. When `gritz runner --backend local`
  starts, it writes `sandboxbin.Binary(runtime.GOARCH)` to
  `<local-root>/bin/gritz` (mode 0755) and passes that path. It fails on a
  non-Linux host. The tests (`local_test.go`, `internal/e2e`, docker's
  `sandbox_test.go`) do the same through a small test helper in `sandboxbin`,
  instead of reading `prebuilt/`.
- **Lambda MicroVMs.** GoReleaser gains a second build, `id: gritz-sandbox`
  (`main: ./cmd/gritz-sandbox`, linux only, same flags and tags), and a second
  archive, `gritz-sandbox_linux_{amd64,arm64}.tar.gz`. Both appear in
  `checksums.txt`. With `-trimpath` the build is reproducible, so these binaries
  are byte-identical to the embedded ones. The MicroVM recipe copies the
  `gritz` entry of `gritz-sandbox_linux_arm64.tar.gz` into the image, and the
  `microvm.Dockerfile` comment changes to
  `go build -trimpath -ldflags="-s -w" -o gritz ./cmd/gritz-sandbox`. Operators
  still rebuild the image on upgrade.
- **install.sh** is unchanged. It installs `gritz_<os>_<arch>.tar.gz`, the host
  CLI, and the new archives have a different name prefix.

Server, runner and driver still ship together: one release builds all of them,
and the runner can only hand a sandbox the driver it was built with.

### API

```go
package sandboxcmd // github.com/gritzapp/gritz/internal/command/sandboxcmd (new; files moved from internal/command)

// DriverCommand runs the driver server.
var DriverCommand *cli.Command

// ToolCommand groups the in-sandbox tools: agent-mcp, git-credential, github-mcp and proxy.
var ToolCommand *cli.Command
```

```go
package sandboxbin // github.com/gritzapp/gritz/internal/runner/sandboxbin (new)

// Arches lists the linux architectures embedded.
var Arches = []string{"amd64", "arm64"}

// Binary returns the sandbox binary for linux/arch, decompressed once and cached.
func Binary(arch string) ([]byte, error)
```

```go
package command // github.com/gritzapp/gritz/internal/command

var DriverCommand *cli.Command        // removed: moved to sandboxcmd
var ToolCommand *cli.Command          // removed: moved to sandboxcmd
var AgentMcpCommand *cli.Command      // removed: moved to sandboxcmd, unexported
var GitCredentialCommand *cli.Command // removed: moved to sandboxcmd, unexported
var GitHubMCPCommand *cli.Command     // removed: moved to sandboxcmd, unexported
var ProxyCommand *cli.Command         // removed: moved to sandboxcmd, unexported
var DownloadCommand *cli.Command      // removed
```

```go
package prebuilt // github.com/gritzapp/gritz/internal/runner/prebuilt — removed
```

`agentprompt.Render` keeps its signature. Only its implementation changes.

**How it fits:** `cmd/gritz-sandbox` registers the two `sandboxcmd` commands.
`go generate` builds it into `sandboxbin`. `cmd/gritz` no longer registers
them. The Docker backend and the runner's local-backend setup call
`sandboxbin.Binary`.

## Implementation Plan

1. **Strip release builds.** Delivers: `-trimpath -ldflags="-s -w"` in
   `.goreleaser.yaml` and `Dockerfile`. Depends on: nothing. Verifiable by:
   `goreleaser build --snapshot`, then checking that the archive binary is
   about 46 MB and that a panic still prints file:line.
2. **Render the prompt without `text/template`.** Delivers: a plain-Go
   `agentprompt.Render`, and `PROMPT.md` deleted. Depends on: nothing.
   Verifiable by: the existing golden tests passing unchanged.
3. **cli v3.14.0, build tags and guards.** Delivers: the upgrade, the
   `urfave_cli_no_template,grpcnotrace` tags at every build site, the
   `<ReflectMethod>` CI check for `./cmd/gritz`, and the `--help` test. Moves
   `client_moq_wait.go` to test-only code. Depends on: (2). Verifiable by: the
   CI check passing, and `./cmd/gritz` at about 52 MB unstripped (F3).
4. **`sandboxcmd` and `cmd/gritz-sandbox`.** Delivers: the moved command files,
   the new `main`, and the dependency guard. `cmd/gritz` still registers
   `driver`/`tool` from `sandboxcmd`, because it is still the binary that
   sandboxes run. Depends on: (3). Verifiable by:
   `go build ./cmd/gritz-sandbox` at about 20 MB, and the guard test.
5. **Embed and deliver to Docker.** Delivers: `sandboxbin` with `go generate`,
   build-site wiring (mise, CI, `Dockerfile`, GoReleaser hook), the Docker
   backend on `sandboxbin.Binary`, and the size-budget test. Deletes
   `internal/runner/prebuilt`, `gritz download`, `GRITZ_PREBUILT_DIR` and the
   runner image's `/app/prebuilt`. Updates `operators/runner` (Driver binaries),
   `operators/deployment`, `reference/cli`, `using/cli` and the CLI list in
   `CLAUDE.md`. Depends on: (4). Verifiable by: `mise run dev`, then running a
   task and checking `docker exec gritz-<id> ls -l /usr/local/bin/gritz` (about
   14 MB) and that the task completes. The docker backend tests pass on the
   embedded binary.
6. **Local backend and tests on the sandbox binary; drop `driver`/`tool` from
   `gritz`.** Delivers: `gritz runner --backend local` writing the embedded
   binary, the test helper, and the e2e harness switched over. `cmd/gritz` no
   longer registers `sandboxcmd`. Depends on: (5). Verifiable by:
   `mise run test` (e2e included), and `gritz --help` no longer listing
   `driver`/`tool`.
7. **Release the sandbox archives for MicroVMs.** Delivers: the GoReleaser
   `gritz-sandbox` build and archive, an updated `microvm.Dockerfile` and
   backend README, and updates to the `operators/backends` "Building the image"
   section and the `operators/deployment` release list. Depends on: (4).
   Verifiable by: `goreleaser release --snapshot --clean` listing
   `gritz-sandbox_linux_{amd64,arm64}.tar.gz` in `checksums.txt`, with
   contents byte-identical to the embedded files after `gunzip`.

## Trade-offs

**Strip only (F0 → 46.1 MB).** Trivial, and part of the recommendation, but on
its own the sandbox still gets the server, the web UI and the AWS SDK.

**One binary with the linker triggers fixed (F3: 52.0 MB, 36.9 MB stripped).**
This keeps the "runner copies itself" model and needs no new artifact. It is
still 2.7× the sandbox binary, and the server surface stays inside every
untrusted sandbox. The fixes are prerequisites of the recommendation anyway
(S0 → S1), so this option is a subset of the plan, not an alternative to it.

**Split `internal/command` without a second `main`.** This saves nothing. The
full binary needs every command regardless. Splitting matters only for a `main`
that leaves commands out, which is why the split happens as part of
`cmd/gritz-sandbox`. Splitting the host commands into per-command packages
would also stop A0-style coupling for future binaries, but it has no customer
today.

**A driver-only binary plus a separate tools binary.** The Docker backend needs
`tool proxy` for every connection, and every agent needs `tool agent-mcp`. The
tools add 0.6 MB on top of the driver (P1 vs S1), so a second binary buys
nothing.

**Delivery: embed vs. a prebuilt directory.** The alternative is to keep
`prebuilt/` and `gritz download` with `gritz-sandbox-linux-<arch>` files, and
ship them in the runner image and next to the release archives. That leaves
the full binary at 36.9 MB instead of 46.5 MB stripped. But the runner can no
longer copy itself, so every runner started from a release archive on Linux
would need `gritz download` or an extra file. `download` also fetches the latest
release rather than the runner's own, which is a skew the embed removes. The
embed costs 9.6 MB in a binary downloaded once per host, against 51 MB saved per
task.

**Keeping `driver`/`tool` in the full binary.** They would add no size, because
the code is linked anyway, and would keep `--backend local` working on macOS.
But nothing on the host runs them, and two binaries that can both act as a
driver invite someone to run the wrong one in a sandbox. Layer (4) keeps them
temporarily; layer (6) removes them.

**Compression.** xz or zstd would shrink the embedded payload further: 7.2 MB
instead of 9.6 MB for xz. That needs a third-party decoder in the runner for a
saving that only affects the one-time download. UPX is rejected for the reasons
in #1661: a per-exec decompression cost on every `git-credential` call, no
page-cache sharing, AV false positives, and broken `go version -m`.

**Further trimming, not pursued here.** #1661 found two more savings, not
re-measured here: a 5-RPC driver client instead of the 52-RPC
`GritzServiceClient` (about −1.2 MB in a driver-only build), and moving
`DummyAgent` out (about 0 MB in the sandbox binary, because `agent-mcp` needs
the MCP SDK anyway). Both can follow once the size-budget test makes the gains
visible.

## Open Questions

- Should `--backend local` keep working on macOS? With `driver` dropped from
  `gritz`, it needs a `darwin` sandbox build. That would mean embedding host-arch
  builds or building one on demand in dev. The local backend is for tests and
  development only, and `mise run dev` uses Docker, so the proposal says no.
- The server image carries the 9.6 MB embedded payload it never uses. A build
  tag could leave `sandboxbin` empty for `gritz server`-only builds. That would
  make two variants of the same binary, so the proposal accepts the bytes
  instead.
- For MicroVM images, is a release asset the right handoff? An alternative is a
  `gritz sandbox-binary --arch arm64 -o gritz` command that writes the runner's
  embedded copy. It cannot drift from the runner, but it adds a host command
  that exists only for image builds.