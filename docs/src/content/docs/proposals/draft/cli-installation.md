---
title: CLI installation
description: Users have no practical way to install the gritz CLI on their own machine, macOS in particular.
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

The binary already cross-compiles for macOS: `CGO_ENABLED=0 GOOS=darwin go build
./cmd/gritz` succeeds for `amd64` and `arm64` (SQLite is the pure-Go
`ncruces/go-sqlite3`), and
[`internal/x/notify`](https://github.com/gritzapp/gritz/blob/master/internal/x/notify/notify.go)
already handles macOS through `osascript`. Windows does not build:
[`internal/shell`](https://github.com/gritzapp/gritz/tree/master/internal/shell)
uses `syscall.SIGWINCH` and `syscall.Kill`.

## Design

Three install paths, all fed by one set of release artifacts:

| Path | Command | Platforms |
| --- | --- | --- |
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

archives:
  - formats: [tar.gz]
    name_template: gritz_{{ .Os }}_{{ .Arch }}

checksum:
  name_template: checksums.txt

release:
  mode: keep-existing   # release-please wrote the notes
  replace_existing_artifacts: true

changelog:
  disable: true
```

Each release then carries:

- `gritz_linux_amd64.tar.gz`, `gritz_linux_arm64.tar.gz`,
  `gritz_darwin_amd64.tar.gz`, `gritz_darwin_arm64.tar.gz`, each holding a
  single `gritz` binary (plus the `README.md` and `CHANGELOG.md` GoReleaser
  includes by default)
- `checksums.txt` covering the archives

The bare `gritz-linux-amd64` and `gritz-linux-arm64` assets are no longer
published. `gritz download` reads the linux archives instead (below).

The `trigger-deploy` job keeps `needs: [release-please, release, docker]`, so a
deploy still waits for the assets.

### `gritz download` reads the archives

[`prebuilt.Download`](https://github.com/gritzapp/gritz/blob/master/internal/runner/prebuilt/prebuilt.go)
looks up release assets by the names in `prebuilt.BinaryNames` and writes each
one straight to the `prebuilt` directory. It changes to:

1. For each of `amd64` and `arm64`, find the asset `gritz_linux_<arch>.tar.gz`
   in the latest release.
2. Stream it through `compress/gzip` and `archive/tar`, and write the entry
   named `gritz` to `prebuilt/gritz-linux-<arch>` with mode `0755`. An archive
   without that entry is an error.

The files in the `prebuilt` directory keep their names, so `BinaryPath`,
`ReadBinary`, the Docker backend, `mise run build`, the `Dockerfile` and the
tests that read `prebuilt/gritz-linux-<arch>` are unchanged. Only the release
asset names move. `BinaryNames` becomes a list of architectures.

`Download` does not verify `checksums.txt`: it fetches over TLS from the GitHub
API, as it does today. Verifying is cheap to add if wanted; see Open Questions.

A `gritz download` from a binary older than this change fails against a release
made after it, with `asset gritz-linux-amd64 not found`. Downloading the new
release fixes it, so nothing older is kept around.

### API

```go
package prebuilt // github.com/gritzapp/gritz/internal/runner/prebuilt

// BinaryNames lists the prebuilt binary file names. // removed
var BinaryNames []string // removed

// Arches lists the architectures Download fetches driver binaries for.
var Arches = []string{"amd64", "arm64"} // new

// Download extracts the gritz binary from each linux archive of the latest
// release into the prebuilt directory. // changed: reads gritz_linux_<arch>.tar.gz
func Download(ctx context.Context, repo string) error
```

**How it fits:** `gritz download` calls `Download` as it does today; nothing
else uses `BinaryNames`.

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
runs the unsigned binary without a Gatekeeper prompt. Rerunning the script
upgrades in place.

Serving it from the docs site keeps it versioned with the repository and
deployed by the existing `docs-pages.yml`. PR previews publish it under
`/pr/<n>/install.sh`, which is handy for testing changes to it.

### mise

mise's `github:` backend picks a release asset whose name contains the OS and
architecture, which `gritz_<os>_<arch>.tar.gz` satisfies. No registry entry or
extra artifact is needed: `mise use -g github:gritzapp/gritz` works as soon as
the archives exist, and `mise upgrade` updates it. aqua and ubi users get the
same for free.

### Docs

- [`using/cli`](/using/cli/): replace the paragraph about release binaries and
  `mise run install` with an Install section listing the three paths, and say
  how each one updates.
- [`operators/runner`](/operators/runner/): point at the same install paths
  instead of `mise run install` and the bare `gritz-linux-*` release binaries.
  A runner on macOS still needs `gritz download` for the linux driver binaries,
  so the "Driver binaries" section stays; its file names in the `prebuilt`
  directory don't change.
- [`operators/deployment`](/operators/deployment/): list the archives and
  `checksums.txt` as the release's binary assets instead of `gritz-linux-amd64`
  and `gritz-linux-arm64`.
- `README.md`: a one-line install command near the top.

## Implementation Plan

1. **GoReleaser release artifacts and `gritz download`** — Delivers:
   `.goreleaser.yaml`, the `release` job running GoReleaser, `prebuilt.Download`
   extracting from the linux archives, and the `operators/deployment` and
   `operators/runner` lines that name the release assets. These land together:
   a release made without the bare assets breaks the old `Download`. Depends on:
   nothing. Verifiable by: `goreleaser release --snapshot --clean` locally
   produces the four archives and `checksums.txt`; a unit test extracts `gritz`
   from a snapshot-shaped archive and rejects one without it; after the next
   release, `gritz download` fills the `prebuilt` directory and the Docker
   backend starts a task with it.
2. **Install script** — Delivers: `docs/public/install.sh`. Depends on: (1)
   having shipped in a release. Verifiable by: running it from the PR preview URL
   on Linux and macOS, with and without `GRITZ_VERSION`, and with a tampered
   checksum.
3. **Install docs** — Delivers: the rest of the `using/cli`, `operators/runner`
   and README changes, including mise. Depends on: (2). Verifiable by:
   `cd docs && pnpm build`, and following each path from the page.

## Trade-offs

- **Keep the hand-written workflow and add `go build` steps for darwin.** Fewer
  moving parts, but archives and checksums would be hand-rolled shell, and
  every later addition (another platform, a package manager) more of it.
  GoReleaser does all of it from one file and is the convention mise and aqua
  expect.
- **Keep publishing the bare linux binaries.** Leaves `prebuilt.Download`
  alone, but GoReleaser selects archive inputs by build id, not platform, so
  it takes a second linux-only build that compiles both linux targets again,
  and every release carries the same binaries twice.
- **Make `go install` work.** It would need the built web UI committed, or a
  CLI-only binary that leaves out the server. Committing build output churns
  every UI change through Go history; splitting the binary breaks the
  "the runner copies itself into the sandbox" property and adds a second
  artifact. Neither is worth it for an install path the other three already
  cover.
- **Signing and notarizing the macOS binaries.** Makes archives downloaded
  through a browser run without a Gatekeeper prompt, but needs an Apple
  Developer account and its credentials in CI. The script and mise don't
  trigger Gatekeeper, so this is deferred; see Open Questions.
- **A `gritz upgrade` command.** Each install path already has its own update
  command (rerunning the script, `mise upgrade`), and a self-replacing binary
  would fight mise over the file it manages.

## Open Questions

- Should we sign and notarize the macOS binaries now (GoReleaser supports it
  with `notarize`), or accept that a browser-downloaded archive needs
  `xattr -d com.apple.quarantine` before it runs?
- Should `gritz download` also fetch `checksums.txt` and verify the archives,
  as the install script does?
- Windows: fix `internal/shell` with build tags and ship `windows/amd64` (zip),
  or document WSL as the only Windows path?
- Is `docs.gritz.dev/install.sh` the right URL, or should the script live at
  `gritz.dev/install.sh` (served by the production server) for a shorter
  command?
