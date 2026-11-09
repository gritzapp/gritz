---
title: CLI installation
description: Users have no practical way to install the gritz CLI on their own machine, macOS in particular, or to keep it in step with the server.
---

Issue: https://github.com/gritzapp/gritz/issues/2183

## Problem

The `gritz` binary is the CLI as well as the server, runner and driver. People
run it locally for `gritz login`, `gritz mcp`, `gritz notify`, `gritz task`,
`gritz logs` and `gritz shell`, but there is no way to install it that works for
them:

- The [release workflow](https://github.com/gritzapp/gritz/blob/master/.github/workflows/release.yml)
  runs `go build` twice and attaches `gritz-linux-amd64` and `gritz-linux-arm64`
  as bare binaries. There is no macOS build, no archive and no checksum.
- [`using/cli`](/using/cli/) and [`operators/runner`](/operators/runner/) point at
  `mise run install`, which needs a checkout plus Go, Node and pnpm.
- `go install github.com/gritzapp/gritz/cmd/gritz@latest` fails: the server
  embeds the built web UI with `//go:embed webui` in
  [`internal/server/static.go`](https://github.com/gritzapp/gritz/blob/master/internal/server/static.go),
  and `internal/server/webui/` is gitignored.
- Server, runner and driver ship together and break compatibility freely (see
  "No backwards compatibility" in `CLAUDE.md`). A user's CLI does not ship with
  them, and nothing tells the user it has fallen behind.

The binary already cross-compiles for macOS: `CGO_ENABLED=0 GOOS=darwin go build
./cmd/gritz` succeeds for `amd64` and `arm64` (SQLite is the pure-Go
`ncruces/go-sqlite3`), and
[`internal/x/notify`](https://github.com/gritzapp/gritz/blob/master/internal/x/notify/notify.go)
already handles macOS through `osascript`. Windows does not build:
[`internal/shell`](https://github.com/gritzapp/gritz/tree/master/internal/shell)
uses `syscall.SIGWINCH` and `syscall.Kill`.

## Design

Four install paths, all fed by one set of release artifacts:

| Path | Command | Platforms |
| --- | --- | --- |
| Homebrew | `brew install --cask gritzapp/tap/gritz` | macOS |
| Install script | `curl -fsSL https://docs.gritz.dev/install.sh \| sh` | Linux, macOS |
| mise | `mise use -g github:gritzapp/gritz` | Linux, macOS |
| Manual | download an archive from the GitHub release | Linux, macOS |

`mise run install` stays as the contributor path and leaves the user docs.

### Release artifacts with GoReleaser

Add `.goreleaser.yaml` and replace the two `go build` steps and the
`softprops/action-gh-release` step in the `release` job with
`goreleaser/goreleaser-action`. release-please still owns the version, the tag,
the release and its notes. GoReleaser runs on the checked-out tag and only adds
assets to the release that already exists.

```yaml
version: 2

before:
  hooks:
    # The web UI must be built before the binary embeds it. The job keeps
    # its pnpm and Node setup steps.
    - sh -c "cd webui && pnpm install --frozen-lockfile && pnpm exec vite build"

builds:
  - id: gritz
    main: ./cmd/gritz
    env: [CGO_ENABLED=0]
    goos: [linux, darwin]
    goarch: [amd64, arm64]
    ldflags:
      - -X github.com/gritzapp/gritz/internal/version.Version={{ .Tag }}
  # A linux-only build for the bare binaries: archives select builds by id,
  # not by platform. It recompiles the two linux targets, a few seconds each.
  - id: gritz-bare
    main: ./cmd/gritz
    env: [CGO_ENABLED=0]
    goos: [linux]
    goarch: [amd64, arm64]
    ldflags:
      - -X github.com/gritzapp/gritz/internal/version.Version={{ .Tag }}

archives:
  - id: tarball
    ids: [gritz]
    formats: [tar.gz]
    name_template: gritz_{{ .Os }}_{{ .Arch }}
  # The bare linux binaries `gritz download` and the Docker backend fetch by name.
  - id: bare
    ids: [gritz-bare]
    formats: [binary]
    name_template: gritz-{{ .Os }}-{{ .Arch }}

checksum:
  name_template: checksums.txt

release:
  mode: keep-existing   # release-please wrote the notes
  replace_existing_artifacts: true

changelog:
  disable: true

homebrew_casks:
  - name: gritz
    repository:
      owner: gritzapp
      name: homebrew-tap
      token: "{{ .Env.HOMEBREW_TAP_TOKEN }}"
    homepage: https://docs.gritz.dev
    description: Async agent orchestrator CLI
    hooks:
      post:
        install: |
          if OS.mac?
            system_command "/usr/bin/xattr", args: ["-dr", "com.apple.quarantine", "#{staged_path}/gritz"]
          end
```

Each release then carries:

- `gritz_linux_amd64.tar.gz`, `gritz_linux_arm64.tar.gz`,
  `gritz_darwin_amd64.tar.gz`, `gritz_darwin_arm64.tar.gz`
- `gritz-linux-amd64`, `gritz-linux-arm64`, unchanged, because
  [`prebuilt.BinaryNames`](https://github.com/gritzapp/gritz/blob/master/internal/runner/prebuilt/prebuilt.go)
  looks them up by exact name
- `checksums.txt` covering all of the above

The `trigger-deploy` job keeps `needs: [release-please, release, docker]`, so a
deploy still waits for the assets.

### Homebrew tap

A new public repository, `gritzapp/homebrew-tap`, holds a `Casks/gritz.rb` that
GoReleaser rewrites on every release. GoReleaser v2 deprecates `brews` (formulae)
in favour of `homebrew_casks` for prebuilt binaries, so this uses a cask.

The binaries are not signed or notarized. Homebrew quarantines cask downloads,
so the cask's post-install hook clears the quarantine attribute on the binary.
Without it Gatekeeper refuses to run `gritz` the first time.

The workflow needs a token that can push to `gritzapp/homebrew-tap`, passed as
`HOMEBREW_TAP_TOKEN`. The existing deploy GitHub App (`DEPLOY_APP_ID`,
`DEPLOY_APP_PRIVATE_KEY`) can mint one with
`actions/create-github-app-token` scoped to that repository, the same way
`trigger-deploy` does for `gritzapp/infra`.

### Install script

`docs/public/install.sh` is served by the docs site at
`https://docs.gritz.dev/install.sh`. It:

1. Maps `uname -s` and `uname -m` to `linux|darwin` and `amd64|arm64`, and
   exits with a message on anything else.
2. Downloads `gritz_${os}_${arch}.tar.gz` and `checksums.txt` from
   `https://github.com/gritzapp/gritz/releases/latest/download/`, or from
   `releases/download/${GRITZ_VERSION}/` when `GRITZ_VERSION` is set.
3. Verifies the archive with `sha256sum -c` (or `shasum -a 256 -c` on macOS).
4. Extracts `gritz` into `${GRITZ_INSTALL_DIR:-$HOME/.local/bin}` and prints a
   hint when that directory is not on `PATH`.

It uses only `curl`, `tar` and the checksum tool, needs no root, and does not
edit shell profiles. Files downloaded with `curl` are not quarantined, so macOS
runs the binary without the cask's workaround.

Serving it from the docs site keeps it versioned with the repository and
deployed by the existing `docs-pages.yml`. PR previews publish it under
`/pr/<n>/install.sh`, which is handy for testing changes to it.

### mise

mise's `github:` backend picks a release asset whose name contains the OS and
architecture, which `gritz_<os>_<arch>.tar.gz` satisfies. No registry entry or
extra artifact is needed: `mise use -g github:gritzapp/gritz` works as soon as
the archives exist. aqua and ubi users get the same for free.

### Version skew warning

`Ping` already returns the server's version (`PingResponse.version`). Use it in
two places:

- `gritz version` prints the CLI's version and, when an org is configured, the
  server's:

  ```
  gritz v0.41.0
  server v0.43.0 (acme-labs)
  ```

- `gritzclient` checks once per process. The server adds a `Gritz-Version`
  response header to every Connect response; a client interceptor reads it on
  the first response and, when the CLI is an older release than the server,
  writes one line to stderr:

  ```
  gritz: this CLI is v0.41.0, the server is v0.43.0; upgrade with your installer (https://docs.gritz.dev/using/cli/)
  ```

  Only tagged releases compare: a CLI or server whose `version.String()` is a
  VCS revision (a dev build) never warns. `gritz mcp` writes the line to stderr
  like every other command, which MCP clients log without interfering with the
  stdio protocol.

This proposal does not add a `gritz upgrade` command: each install path has its
own update command (`brew upgrade`, rerunning the script, `mise upgrade`), and a
self-replacing binary would fight Homebrew and mise over the file they manage.

### API

```go
package version // github.com/gritzapp/gritz/internal/version

// Header is the response header the server sets to its version.
const Header = "Gritz-Version" // new

// Older reports whether release a is older than release b; it is false
// when either is not a release tag.
func Older(a, b string) bool // new
```

```go
package gritzclient // github.com/gritzapp/gritz/internal/gritzclient

// VersionCheckInterceptor warns once on w when the server is a newer
// release than this binary.
func VersionCheckInterceptor(w io.Writer) connect.Interceptor // new
```

**How it fits:** `internal/server/server.go` sets `version.Header` on every
response with a small unexported interceptor added beside the otel and auth
interceptors. The client constructor in `gritzclient` adds
`VersionCheckInterceptor(os.Stderr)` alongside `RetryInterceptor`.
`VersionCommand` calls `Ping` when a config with an org is present.

### Docs

- [`using/cli`](/using/cli/): replace the paragraph about release binaries and
  `mise run install` with an Install section listing the four paths, and say
  how each one updates.
- [`operators/runner`](/operators/runner/): point at the same install paths
  instead of `mise run install`. A runner on macOS still needs `gritz download`
  for the linux driver binaries, so the "Driver binaries" section stays.
- `README.md`: a one-line install command near the top.

## Implementation Plan

1. **GoReleaser release artifacts** — Delivers: `.goreleaser.yaml` without the
   `homebrew_casks` block, and the `release` job running GoReleaser. Depends on:
   nothing. Verifiable by: `goreleaser release --snapshot --clean` locally lists
   the archives, the bare linux binaries and `checksums.txt`; the next release
   carries them and `gritz download` still works against it.
2. **Install script** — Delivers: `docs/public/install.sh`. Depends on: (1)
   having shipped in a release. Verifiable by: running it from the PR preview URL
   on Linux and macOS, with and without `GRITZ_VERSION`, and with a tampered
   checksum.
3. **Homebrew tap** — Delivers: the `gritzapp/homebrew-tap` repository, the
   `homebrew_casks` block and the token step in the workflow. Depends on: (1).
   Verifiable by: the next release pushes `Casks/gritz.rb`, and
   `brew install --cask gritzapp/tap/gritz` runs `gritz version` on macOS
   without a Gatekeeper prompt.
4. **Install docs** — Delivers: the `using/cli`, `operators/runner` and README
   changes, including mise. Depends on: (2), (3). Verifiable by:
   `cd docs && pnpm build`, and following each path from the page.
5. **Server version header** — Delivers: `version.Header` and the server's
   interceptor that sets it. Depends on: nothing. Verifiable by: a unit test
   on the interceptor; `curl -i` against the dev stack shows the header.
6. **CLI version check** — Delivers: `version.Older`,
   `gritzclient.VersionCheckInterceptor` and the server line in `gritz version`.
   Depends on: (5). Verifiable by: unit tests for `Older` and the interceptor
   with a stubbed handler; a dev build against the dev stack prints no warning.

Layers 5 and 6 are independent of 1 to 4 and can land in either order relative
to them.

## Trade-offs

- **Keep the hand-written workflow and add `go build` steps for darwin.** Fewer
  moving parts, but checksums, archives and the cask would all be hand-rolled
  shell. GoReleaser does all of it from one file and is the convention mise,
  aqua and Homebrew taps expect.
- **A Homebrew formula instead of a cask.** GoReleaser v2 deprecates `brews`.
  A formula would also work on Linuxbrew, but Linux users have the script and
  mise, so the cask's macOS-only reach is acceptable.
- **Make `go install` work.** It would need the built web UI committed, or a
  CLI-only binary that leaves out the server. Committing build output churns
  every UI change through Go history; splitting the binary breaks the
  "the runner copies itself into the sandbox" property and adds a second
  artifact. Neither is worth it for an install path the other four already
  cover.
- **Signing and notarizing the macOS binaries.** Removes the quarantine hook and
  makes manual browser downloads run without a Gatekeeper prompt, but needs an
  Apple Developer account and its credentials in CI. Deferred; see Open
  Questions.
- **A `gritz upgrade` command.** Covered above: it conflicts with the package
  managers that own the file. The skew warning points at the user's installer
  instead.
- **Fail requests when versions differ, instead of warning.** Strict, but it
  would break every CLI for a few minutes on each deploy and for anyone who has
  not upgraded yet, including for commands whose RPCs did not change.

## Open Questions

- Should we sign and notarize the macOS binaries now (GoReleaser supports it
  with `notarize`), or live with the cask's quarantine hook and an unsigned
  manual download?
- Windows: fix `internal/shell` with build tags and ship `windows/amd64` (zip),
  or document WSL as the only Windows path?
- Should the server header carry a minimum supported CLI version too, so a
  breaking RPC change can turn the warning into an error for that release only?
- Is `docs.gritz.dev/install.sh` the right URL, or should the script live at
  `gritz.dev/install.sh` (served by the production server) for a shorter
  command?
