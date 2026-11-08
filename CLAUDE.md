# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build Commands

```bash
mise run build          # Build main binary + prebuilt binaries for linux amd64/arm64
mise run generate       # Generate protobuf code (go tool buf generate)
go build -o gritz ./cmd/gritz  # Build main binary only
```

## Running Tests

Tests require a running PostgreSQL instance. Start it with:

```bash
mise run build    # The prebuild binaries & webui must exist for the tests to pass
mise run up:test  # Start test dependencies
mise run test     # Run all tests
```

Pass extra arguments to `go test` with `--`. Flags alone still run every package; package patterns replace `./...`:

```bash
mise run test -- -run=TestFoo -v                     # ./... filtered to TestFoo
mise run test -- ./internal/e2e/... -run=TestFoo -v  # only ./internal/e2e/...
```

## Architecture

GRITZ is an async agent orchestrator using a central control plane architecture to run multiple Claude Code instances in parallel inside Docker containers.

### Core Components

- **Server** (`internal/server/`) - Connect RPC API + Web UI, stores tasks and logs in PostgreSQL
- **Runner** (`internal/runner/`) - Polls for pending tasks and manages task sandboxes through a backend (`internal/runner/backend/`: `docker`, `lambdamicrovm`, and `local` for tests and development)
- **Driver** (`internal/runner/driverserver/`, `gritz driver`) - Long-lived driver server inside each sandbox; the runner forwards the task's events over the driver.v1 `Run` stream, and the driver starts, restarts and stops runs from the commands among them
- **Agent** (`internal/runner/agent/`) - Agent CLI integrations the driver runs for each run (Claude Code, Codex, Cursor, Copilot, ...)
- **Store** (`internal/store/`) - PostgreSQL persistence layer using pgx driver

### Key Concepts

- **Tasks** are the unit of work - contain workspace reference and prompts to execute
- **Agents** run one-to-one with tasks inside containers named `gritz-{task-id}`
- **Workspaces** define container config (image, volumes, env vars) and MCP servers in `workspaces.yaml`
- The runner reaches the driver through the backend (Docker: an exec tunnel to `/gritz/driver.sock`; Lambda MicroVMs: the managed proxy); the driver reports to the server directly with a per-task token
- Runner auto-injects an `gritz` MCP server (see below)
- **Secrets** listed in a workspace's `secrets:` map are masked in the log shipped to the server (see below)

### Secrets

A workspace's `secrets:` map declares credentials for the sandbox. Each entry becomes an environment variable there — identical to a `container.environment` entry, same `${env:}`/`${sh:}` expansion — but declaring it is also what marks the value as a secret:

- The runner puts them in `backend.Spec.Secrets` (`internal/runner/runner.go`), and each backend carries them into the driver.v1 `RunSpec.secrets` of the run it starts. They travel in the run spec rather than in the agent config because the driver opens its log before reading that config — the mask has to exist before the first shipped byte.
- `internal/runner/driverserver` adds them to the run's environment, adds the run's token (the task JWT, masked as `token`), and hands the map to the log shipper.
- `internal/x/redact` replaces every occurrence of a declared value with `[gritz:masked NAME]`. It performs no detection — callers supply exact values — and matches leftmost-longest through a byte trie, so a value that is a prefix of another cannot mask partially. `internal/runner/logship.Shipper` owns a `redact.Writer` and masks before chunks are cut, so chunk boundaries and a value straddling two writes are both handled; only bytes that are still a live prefix of some secret are held back, and `Flush` drains that tail.
- Only the shipped branch is masked. `/gritz/log` and stderr stay raw — same trust boundary as `gritz shell`, and full fidelity for post-mortems.

When adding workspace config examples or docs, put credentials under `secrets:` and reference them from `commands:` by variable (`${GH_TOKEN}`), so the logged command string carries the name, not the value.

### MCP Server Tools

The runner injects an `gritz` MCP server into each agent, providing these tools:

- `get_my_task` - Get current task instructions, links, and events
- `update_my_task` - Update the current task's name
- `create_link` - Associate external resources (PRs, Jira tickets) with the task
- `report` - Log messages visible in the Web UI

### Event System

Tasks can be notified about external events through the event system:

- **Events** represent external triggers (GitHub PR comments, Jira issue updates, etc.)
- **Links** created with `subscribe=true` route events to tasks when the event URL matches the link URL
- When an event is processed, all tasks with matching subscribed links receive the event
- Events appear in `get_my_task` output and provide additional context to agents
- External pollers (GitHub, Jira) create events and process them to notify linked tasks

Use `create_link` with `subscribe=true` for resources that may need follow-up (PRs awaiting review, issues awaiting response, etc.)

### CLI Subcommands

```
gritz server     # Start server
gritz runner     # Start container orchestrator
gritz driver     # Driver server: runs agents inside the sandbox, started by the runner's backend
gritz mcp        # Local user-facing stdio MCP server that proxies to the server
gritz notify     # Subscribe to server notifications and emit system notifications
gritz tool agent-mcp # In-container MCP server providing gritz tools to the agent
gritz task       # Task CRUD (list, create, update, delete)
gritz containers # List gritz containers
gritz jira       # Poll Jira for issue comments
gritz github     # GitHub integration
```

### Protobuf

Service definitions in `proto/gritz/v1/gritz.proto`; generated code is written to `internal/proto/` (checked in) by `mise run generate`.

The same run writes the API reference pages, one per message and enum of `gritz.v1`, into `docs/src/content/docs/api/` (checked in, never edited by hand) with `protoc-gen-gritz-docs` (`cmd/protoc-gen-gritz-docs`, `internal/protodocs`). Comments on `gritz.v1` elements are published there, so they follow "What belongs in the docs" below:

- A comment directly above an element, or trailing it on the same line, is its documentation, in Markdown. The first sentence becomes the page description.
- Contributor notes (history, storage details, proposal paths) go in a detached comment: a comment separated from the element by a blank line, which the generator skips.
- Field and enum value comments render in a table cell, so they must be inline Markdown: no lists, headings or code blocks. Generation fails on block syntax there.

## Docs

The user and operator docs live in `docs/` (Starlight) and are published to https://docs.gritz.dev. Pages are Markdown in `docs/src/content/docs/`; the file path is the URL. Every page has a `title` and a `description` in its frontmatter. Write plain `.md`; use `.mdx` only when a page needs a component.

```bash
mise run docs             # Dev server
cd docs && pnpm build     # Build docs and diagrams; starlight-links-validator fails on a broken link or anchor
```

The docs build also builds the Motion Canvas diagrams in `diagrams/` (`pnpm diagrams`, into the gitignored `docs/public/diagrams/`), served at `/diagrams/` (index) and `/diagrams/<slug>/`. In an `.mdx` page, `<Diagram slug="create-task" />` (`docs/src/components/Diagram.astro`) plays one inline, with a link to its own page; the build fails if the project doesn't exist. Markdown links to `/diagrams/...` are checked against the built files.

Design proposals are docs pages too, in `docs/src/content/docs/proposals/{draft,implemented,rejected}/`, listed in the Proposals sidebar section and left out of the llms.txt documentation sets. Write and move them with the `proposal` skill. The "What belongs in the docs" rules below don't apply to proposals: they are design history for contributors.

`docs-pages.yml` deploys master to the `gh-pages` root, and `docs-preview.yml` builds every PR that touches `docs/` or `diagrams/` under `/pr/<n>/` (diagrams included) and comments a preview link.

**A PR that changes behaviour a user or operator can see — a flag, a workspace field, an envelope field, a routing semantic, an MCP tool — updates the matching page in the same PR, if that page no longer describes it correctly.** Reviewers ask for it like they ask for tests. Where the matching page doesn't exist yet, there is nothing to update. See "What belongs in the docs" below before writing anything.

| Code | Docs |
| --- | --- |
| `internal/server/customserver`, custom event types | `integrations/custom-events`, `reference/custom-event-envelope` |
| `internal/server/eventrouter` (routing rules, built-in event types) | `concepts/routing-rules`, `reference/event-types` |
| `internal/server/githubserver`, `internal/server/atlassianserver` | `integrations/github`, `integrations/jira` |
| `internal/server/mcpserver`, `gritz mcp` | `integrations/mcp` |
| Agent MCP tools (`internal/runner/agentmcp`) | `using/agent-tools` |
| CLI commands and flags (`internal/command/`) | `reference/cli`, `using/cli` |
| `workspaces.yaml`, the runner, backends, `secrets:` | `operators/*` |
| `proto/gritz/v1/gritz.proto` | `api/*` (generated: edit the proto comments and run `mise run generate`) |

Config examples in the docs follow the `secrets:` rule above: credentials go under `secrets:` and are referenced by variable (`${GH_TOKEN}`), never inline.

### What belongs in the docs

The docs are for people who use or operate gritz, not for people who work on it. They describe what gritz does now, in terms the reader acts on: commands, flags, config fields, UI, event attributes, observable behaviour. None of this applies to the proposals.

- **Most bug fixes need no docs change.** If a careful reader of the page as it stands would already expect the fixed behaviour, the page is correct and stays as it is. Update it only when it described the buggy behaviour, documented a workaround the fix removes, or the fix changes what the user sees or does.
- **Don't document the absence of a bug.** No "X no longer resets Y", "X now correctly …", or "Note: X does not …" for a failure nobody would assume.
- **No history.** No "previously", "now", "as of", "used to", or PR and issue numbers. The page reads as if it had always been this way.
- **No implementation details.** No Go package, file, function, type, or RPC names, SQL tables, interceptors, or internal mechanisms, unless the reader configures or observes them directly. Explain behaviour, not how the code achieves it.
- **Edit in place.** Correct the sentence that's wrong instead of appending a caveat beside it. A behaviour change should usually leave the page about the same length.

Before touching a page, ask: would a user or operator reading the current page be misled? If not, leave it alone, and say in the PR description that no docs change was needed.

## Invariants

Constrain usage; don't support every call order. When a method is only valid in some states (for example after `Boot`, or while no run is claimed), assert the precondition with a panic that names the broken invariant, rather than adding branches, error returns or fallback behaviour for call orders that no production caller makes. Narrower contracts give simpler implementations.

- Don't write tests for these assertions, and don't test call orders that production code can't produce.
- When a change makes a case unreachable, delete its handling in the same change rather than carrying it forward.
- Before adding handling for a scenario, check that some production path can actually reach it. If none can, assert instead.

## No backwards compatibility

gritz is beta software, and the only deployment is ours. Don't design for backwards compatibility, and don't reshape a plan to satisfy it:

- No shims, fallbacks, deprecation periods or dual code paths.
- Don't keep old RPCs, fields, flags or env vars around for older clients, runners or drivers. Server, runner and driver ship together.
- No import or upgrade paths for old local state (JSON files, old directories); just drop it.
- Change or remove things outright and update every caller in the same change.

The one exception is database schema: Postgres and SQLite schema changes go through new migrations. Don't edit a migration that's already on `master`.

## Go style

- Don't pre-size slices that are built with `append`: write `var s []T`, not `make([]T, 0, n)`. Since Go 1.26 the compiler gives small appended slices a stack buffer and copies them to an exactly-sized heap slice on return, so pre-sizing adds noise for no gain. Pre-size only when `n` can be large or the path is hot (DB result pages, log chunks, the log shipper, `redact`).

## Conventional Commits & Releases

Commit messages and PR titles must follow the [Conventional Commits](https://www.conventionalcommits.org/) spec. The format is enforced by `conform` (see `.conform.yaml`):

```
<type>(<optional scope>): <subject>
```

Allowed types: `feat`, `fix`, `chore`, `docs`, `refactor`, `test`, `ci`, `perf`, `build`, `revert`, `proposal`. Breaking changes use `!` after the type (e.g. `feat!: ...`) or a `BREAKING CHANGE:` footer.

Validation runs in CI on every PR via `siderolabs/conform`. To validate locally on each commit, install the opt-in hook once: `mise run install:hooks`.

Releases are driven by [release-please](https://github.com/googleapis/release-please) — `.github/workflows/release-please.yml` continuously opens a "Release PR" against master based on the conventional commits since the last release. Merging that PR creates the version tag, which triggers `release.yml` to build binaries and publish images to GHCR. Once a release's images are published, `release.yml` sends a `release` dispatch to the private `gritzapp/infra` repo, which deploys it to the dokku host. Deployment config and production secrets live there, not here.

- `feat`, `fix`, `perf`, `revert` → visible in the generated CHANGELOG.md
- All other types → hidden from the changelog
- `feat:` → minor bump; `fix:` → patch bump; `feat!:` or `BREAKING CHANGE:` → major bump

## Web UI

The web interface is a React-based UI in `webui/` using TanStack Router, TanStack Query, and shadcn/ui components.

**Always run `pnpm lint` in `webui/` before finishing any frontend change** — it runs ESLint and the Prettier check, as CI does on every webui PR. Run `pnpm format` to fix formatting.

For detailed development guidelines, see the `webui` skill in `.claude/skills/webui/`.
