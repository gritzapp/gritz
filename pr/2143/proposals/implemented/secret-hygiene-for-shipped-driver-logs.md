# Secret hygiene for shipped driver logs

Issue: https://github.com/icholy/gritz/issues/1563

## Problem

`proposals/implemented/ship-driver-logs-to-server.md` mirrors every byte of the
driver's log sink to the server as `log_chunks`, readable via `gritz logs` and
the Web UI's Logs tab. That proposal explicitly deferred secret hygiene out of
v1: the shipper mirrors the sink verbatim, by design.

The deferral changed a trust boundary. On the sandbox's disk the log was
readable only by someone who could already `gritz shell` in — the same boundary
as running the agent. Shipped, it is persisted in Postgres and readable by
anyone with `OpTaskRead` in the org, indefinitely (there is no retention; see
below). The audience for the log is no longer the audience for the sandbox.

A survey of what actually flows through the sink shows this is not
hypothetical. Credential-shaped material enters the shipped transcript through
several distinct doors:

- **The task JWT, structurally, today.** The runner injects its minted task
  token into the agent MCP config as a `--token` argument
  (`internal/runner/runner.go:479-491`). The Copilot agent then logs that
  entire MCP config JSON into the sink on every run
  (`internal/agent/copilot.go:44`), and the dummy agent logs each MCP server's
  argv (`internal/agent/dummy.go:122`). Task tokens have **no expiry** —
  revocation is the `task.archived` scope predicate
  (`internal/auth/apiauth/jwt.go:52-60`) — so a transcript containing one holds
  a live, task-scoped write credential until the task is archived.
- **Setup commands, verbatim.** The driver logs the full setup-command list at
  startup (`d.Log.Info("loaded config", …, "commands", cfg.Commands, …)`,
  `internal/agent/driver.go:176-182`) and each command again as it runs
  (`driver.go:286`). The documented pattern for private repos puts a live token
  inside that string (`examples/workspaces/private-repo.yml`:
  `git clone https://x-access-token:${sh:gh auth token}@github.com/…` — the
  `${sh:}` expansion has already happened by the time the driver sees it).
- **Setup command output.** Setup stdout/stderr are teed into the sink
  (`driver.go:288-292`). Arbitrary shell: a failed authenticated clone echoes
  the credentialed URL back, `set -x` or `env`-style steps dump the whole
  environment — which carries the workspace `environment:` map
  (`CLAUDE_CODE_OAUTH_TOKEN`, `CODEX_API_KEY`, `NPM_TOKEN`, …), `GRITZ_TOKEN`,
  and whatever the image bakes in.
- **The agent's own activity.** Tool-call summaries flow through
  `toollog.Summarize` into the sink. A `Bash` call's `command` field is
  rendered up to 120 runes — plenty for
  `curl -H "Authorization: Bearer …"` or an `export KEY=…`. Claude logs a
  failed tool result's content unbounded (`internal/agent/claude.go:150`), and
  Claude's stderr is teed raw (`claude.go:69`), where MCP startup failures echo
  the gritz server's spawn line — including `--token`. The `verbose` workspace
  option logs every raw stream-JSON line with no truncation at all
  (`claude.go:94-97`, `codex.go:102-105`, `cursor.go:85-88`).
- **Runtime-minted credentials.** `gritz git-credential` and the
  `get_github_token` MCP tool hand the agent GitHub App installation tokens
  (`internal/gitcredential/gitcredential.go:43-46`,
  `internal/agentmcp/xmcp.go:57-63`) precisely so it can use them in shell
  commands — which are then summarized into the log. The driver never sees
  these values, so no amount of "mask what you were given" covers them.

Nothing filters any of this. The only `Redact` in the tree
(`internal/agent/toollog`) is a bulky-field truncator keyed on field names
(`old_string`, `content`, …) — prior art for *where* a hook can live, not a
detection mechanism.

Two facts bound the blast radius and shape the fix. First, the secrets that
matter are almost all *configured* — they enter the sandbox through the
workspace config or the runner's own token minting — so the highest-value
masking is deterministic replacement of known values, not detection. Second,
any org member can already `gritz shell` into any live task's sandbox and read
`/gritz/log` raw (org members hold the wildcard admin scope,
`internal/auth/apiauth/jwt.go:42`), so the *new* exposure from shipping is
specifically the at-rest, outlives-the-sandbox, no-effort copy — which is what
this proposal scrubs.

## Design

**Declare secrets explicitly in the workspace config; mask exactly those
values (plus the task token) in the log shipper, on the shipped branch
only.** A new
`secrets:` map in the workspace is the single source of truth for what a
secret *is* — there is no name heuristic, no pattern library, and no entropy
scanning. The runner injects declared secrets into the sandbox as environment
variables and tells the driver their names; the driver masks their values in
everything it ships, through a small trie-backed writer applied by the log
shipper on its way out. The in-sandbox `/gritz/log` file and the process
stderr (docker logs) stay raw — they are inside the original trust boundary
and keep full fidelity for shell-based post-mortems.

### The `secrets:` workspace config

A new backend-agnostic map on `Workspace`
(`internal/runner/workspace/workspace.go`), sibling to `commands:` and
`capabilities:`:

```yaml
workspaces:
  pets-workshop:
    secrets:
      CLAUDE_CODE_OAUTH_TOKEN: ${env:CLAUDE_CODE_OAUTH_TOKEN}
      GH_TOKEN: ${sh:gh auth token}
    container:
      image: ghcr.io/icholy/gritz-workspace-debian:latest
    commands:
      - git clone https://x-access-token:${GH_TOKEN}@github.com/private/repo.git
```

Semantics:

- Each entry becomes an environment variable in the sandbox, exactly like an
  `environment:` entry. Values go through the existing `${env:}`/`${sh:}`
  expansion for free (`LoadConfig`/`expandNode` walk every scalar).
- Every value is registered for redaction: it never appears in the shipped
  log, replaced by `[gritz:masked NAME]` (e.g. `[gritz:masked GH_TOKEN]`) in
  the style of the shipper's existing `[gritz: dropped N log bytes]` marker.
  Naming the entry costs nothing secret and makes an over-masked log
  debuggable.
- Setup commands and agents reference secrets by variable (`${GH_TOKEN}`,
  expanded by the shell at run time), so the *command strings* the driver logs
  carry the variable name, not the value — the declaration fixes the
  setup-command leak at the source, and the mask catches any echo of the
  expanded value (a failed clone printing the credentialed URL, `set -x`,
  `env` dumps).

Migration is mechanical: move credential entries from `environment:` to
`secrets:` (`examples/workspaces/private-repo.yml` and the default
`workspaces.yaml` template are updated in the docs slice). Undeclared secrets
keep working exactly as today — unmasked — which is the explicit-config
contract: gritz masks what you tell it about.

### Plumbing: runner → driver

The runner's `spec()` (`internal/runner/runner.go`) appends to `Spec.Env`:

- one `NAME=value` pair per secret, and
- `GRITZ_SECRETS=NAME1,NAME2,…` — the declared names, comma-separated (env
  names cannot contain commas).

`Spec.Env` already reaches both backends unchanged
(`backend/docker/docker.go:168` and
`backend/lambdamicrovm/lambdamicrovm.go:243` both
`append(workspaceEnv, spec.Env...)`), so no per-backend work. Names travel by
env rather than through `agent.Config` because `command/driver.go` opens the
shipper *before* the driver loads its config file — the mask must exist
before the first shipped byte — and because keeping values out of the config
JSON avoids a second on-disk copy.

The driver's redaction set is then: the value of each `GRITZ_SECRETS` name
read from its own environment, plus its own `--token` (the task JWT, the one
secret the platform injects rather than the workspace — masked as
`[gritz:masked token]`).

### The mask: a trie-backed `Writer` in the shipper

A thin `internal/redact` package, over a byte trie in `internal/x/bytetrie`:

```go
// Marker returns the replacement marker for a named secret:
// "[gritz:masked NAME]".
func Marker(name string) string

// NewWriter returns a Writer that replaces every occurrence of each value in
// secrets with Marker(name) before writing to w.
func NewWriter(w io.Writer, secrets map[string]string) *Writer

// Write masks p, together with any bytes held back from earlier writes.
func (w *Writer) Write(p []byte) (int, error)

// Flush writes every held byte to the underlying writer, masked or not.
func (w *Writer) Flush() error

// String returns s with every occurrence of each secret value replaced by
// Marker(name).
func String(s string, secrets map[string]string) string
```

`String` is the entry point for callers holding a value rather than a stream;
it runs the same `Writer` over a `strings.Builder`, so the two cannot drift.

Every declared value is a key in one trie, so a single lookup from a position
asks about all the secrets at once — neither the cost nor the buffering grows
with how many are declared or how long they are. Matching is leftmost-longest:
the longest value starting at a position wins, so when one declared secret's
value is a prefix of another's, the shorter cannot fire first and leave the
longer value's tail in the output beside a marker that makes it look masked.
A failed walk resumes one byte past where it started rather than at the byte
that broke it, since a secret can begin inside the run that just failed (`aab`
lies in `aaab` only from its second byte). The one case this order does not
cover is a pair where the *suffix* of one value is the *prefix* of another and
the input carries them overlapped: the leftmost masks whole and the second
value's tail is left beside the marker. No ordering masks both.

The trie is what makes this correct with no bespoke scanning: log bytes arrive
in arbitrary runs, and the `Writer` holds a byte back only while it is still a
live prefix of some secret. So a value straddling two writes is still caught,
while a run carrying no secret is out the far side immediately rather than
trailing the raw log — and the held tail is bounded by the longest declared
value, however much is written.

The mask is applied by `logship.Shipper`, the boundary that ships:
`New(client, taskID, secrets)` takes the map, and the shipper owns a `Writer`
wrapped around an adapter that buffers into the chunk buffer, so `Write` runs
bytes through the mask before they are queued. Because masking happens ahead of
chunk cutting, the 32 KiB chunk boundaries are irrelevant to it — the held
tail lives on the `Writer` across writes *and* chunks, not per request.
`Flush` drains that tail before cutting the final chunk, so nothing arrives a
run late. A value split across a `Flush` goes out unmasked, which is the price
of draining: the driver only flushes where the stream has ended or stalled, so
that tail is the log's, not a secret's.

`agent.OpenDriverLog` is untouched: the sink stays
`io.MultiWriter(file, shipper)`, every existing tee — slog handler, `Sink()`,
`Stdout()`, `Stderr()`, `StartRun` — is masked automatically because they all
write through the sink, and the file and `os.Stderr` branches stay raw because
the shipper is the only writer that masks. `command/driver.go` builds the
secret map and hands it to the shipper.

### What stays raw, deliberately

- `/gritz/log` and stderr/docker-logs: in-sandbox and runner-host surfaces,
  unchanged trust boundary, full fidelity for `gritz shell` post-mortems.
- The Copilot MCP config dump (`copilot.go:44`) and dummy argv line
  (`dummy.go:122`): kept as-is because they are genuinely useful for
  debugging MCP wiring. The token they disclose is the same string as the
  driver's `--token` — `Runner.spec` mints one token per task and reuses it
  for the driver flag, the injected MCP server's `--token` arg, and
  `GRITZ_TOKEN` (`runner.go:485,507,511`) — so the task-token rule masks
  both lines on the shipped branch (a JWT survives `json.MarshalIndent` as a
  contiguous literal, so the mask matches it whole). Accepted
  consequence: `/gritz/log` and docker logs keep the live JWT in those
  lines, the same trust boundary this section already accepts for raw
  surfaces.
- Shell/PTY bytes: never shipped (`shellwire.Data` frames only), out of scope.
- Event payloads (`task failed` reasons, `report` messages): a different,
  lower-volume surface with its own audience; see Open Questions.

## Implementation Plan

1. **Workspace `secrets:` config** — Delivers: the `Secrets` map on
   `Workspace` and runner injection into `Spec.Env` plus `GRITZ_SECRETS`.
   Depends on: nothing. Verifiable by: a runner spec test asserting the
   sandbox env carries the `NAME=value` pairs and the names list.
2. **`internal/x/bytetrie` and `internal/redact`** — Delivers: the byte trie
   with its longest-prefix lookup, then `Marker`, the trie-backed `Writer`
   and `String` over it. Depends on: nothing. Verifiable by: unit tests
   covering overlapping values (the longer masked whole), a value straddling
   writes, the tail draining on `Flush`, an empty declared value, and marker
   output.
3. **Driver wiring** — Delivers: secret-map construction in
   `command/driver.go` (`GRITZ_SECRETS` + `--token`) and the shipper owning
   the mask (`logship.New`'s `secrets`, drained at EOF by `Flush`).
   Depends on: (1), (2). Verifiable by: shipper tests for a secret straddling
   two writes and two chunks and for the held tail draining on `Flush`; and a
   driver test with a dummy agent and a declared secret asserting the value
   appears in `/gritz/log` but only `[gritz:masked …]` in the bytes the fake
   server received — and, for the token rule, that the Copilot MCP config
   dump *does* ship, with `[gritz:masked token]` where the JWT was, pinning
   that the mask (not deleted log lines) is what protects the shipped
   branch.
4. **Docs & examples** — Delivers: `secrets:` in the default `workspaces.yaml`
   template, `examples/workspaces/private-repo.yml` rewritten to the
   `${GH_TOKEN}` pattern, README/CLAUDE.md notes. Depends on: (3). Verifiable
   by: reading the rendered examples — the credential sits under `secrets:`
   and the command references it by variable.

The slices land in order; (2) is independent of (1) and can go in parallel.

## Trade-offs

- **Explicit `secrets:` config vs. detection.** An earlier revision of this
  proposal paired a sensitive-env-name heuristic with a curated pattern set
  (token prefixes, JWTs, PEM blocks). Explicit declaration replaces both: it
  has zero false positives, no pattern set to curate or update, and a
  contract an operator can state in one sentence — *what you list in
  `secrets:` never ships*. The cost is coverage: an undeclared secret ships
  unmasked, and nothing here reaches bytes already in Postgres — that gap is
  bounded by retention (below). Detection can be layered on later without
  unwinding anything here; the reverse is not true.
- **The runtime-minted gap.** GitHub App installation tokens handed out by
  `gritz git-credential` and `get_github_token` are not declared anywhere, so
  the mask cannot know them — echoes of those tokens ship unmasked. This is
  accepted: installation tokens expire within an hour, so an at-rest copy
  goes dead quickly — unlike the no-expiry task JWT (masked as the `--token`
  literal) and workspace credentials (declared). If it proves real in
  practice, the server could register the values *it* minted for a task and
  scrub them on ingest — a contained future addition, since the server knows
  those exact strings and needs no detection.
- **A trie-backed `Writer` vs. a chain of streaming replacements.** The
  first revision of this proposal specified a custom line-buffering writer
  with holdback caps and explicit flush ordering; the second replaced it with
  `github.com/icholy/replace` rules combined by `transform.Chain`, on the
  grounds that a library already handles cross-`Write` straddling with
  bounded memory and the custom scanning layer disappears. That shipped and
  was then withdrawn (#1578): a fixed-string transformer holds back its
  trailing `len(value)-1` bytes unconditionally, whether or not they are a
  partial match, so the shipped log trailed the live one by the sum of those
  holdbacks until `Flush`; and because `transform.Chain` feeds every link
  after the first from a 4096-byte buffer, any second secret of 4097 bytes or
  more filled it and stopped the stream outright. One trie over all the
  values fixes both at the root — a byte is held only while it is still a
  live prefix of a secret, and there is no chain to buffer between. Cost: the
  scanning layer is ours again, about a hundred lines across two packages,
  which is why both are covered by unit tests pinning exactly those two
  behaviours. The `Flush` caveat survives the change and is unavoidable in
  any streaming mask: a value split across a flush goes out unmasked.
- **Masking in the shipper vs. at ingest vs. at read time.** Only the
  driver's side of the boundary knows the secret values (the sandbox env; the
  server never sees it), so masking on the way out is exact where server-side
  filtering would need detection. It also keeps secrets out of Postgres
  entirely — no credentials in backups or `pg_dump`s, and no future read
  surface that must remember to mask. And the shipper cuts chunks at 32 KiB
  offsets with no regard for content, so a server-side scanner would need
  cross-chunk, cross-retry reassembly; masking ahead of the cut makes
  chunking irrelevant.
  The known costs: masking on the way out cannot fix bytes already shipped,
  and a driver-side bug ships raw bytes silently (mitigated by the mask being
  a small, heavily tested writer, and by retention as the backstop).
- **Narrowing read access instead of scrubbing.** Rejected as the primary
  lever: org members hold the wildcard admin scope
  (`internal/auth/apiauth/jwt.go:42`) — there is no role model to narrow
  *with*, and building one is a far bigger project than a mask. More
  fundamentally, any member can already shell into a live task's sandbox and
  read the raw log, so read-narrowing shipped logs would not change who can
  see secrets — only make the sanctioned path less useful. And it does
  nothing about secrets at rest in Postgres.
- **Masking only the shipped branch vs. also `/gritz/log`.** Masking the
  file too would be one more wrapped writer, but it destroys post-mortem
  fidelity inside a boundary that already holds the secrets in env, config
  files, and `/proc` — masking the log there is theater. The asymmetry is the
  point: raw where the secrets already live, masked where the audience
  widened.
## Open Questions

- **Secrets truncated upstream by `toollog.Summarize` are not masked.** The
  mask matches whole values, so it only masks what actually reaches it
  as a contiguous literal. `toollog.Summarize` caps an individual rendered
  value at 120 runes and the whole summary line at 200
  (`internal/agent/toollog/toollog.go:17,19`), and both caps fall at an
  offset determined by the rendered line, not by where a secret sits in it —
  so a credential inside a summarized tool call can arrive at the mask as a
  fragment of *any* length, and a fragment is not the needle. No downstream
  mask can repair this: by the time the bytes arrive the value is already
  gone. An earlier revision registered each value's 16-byte prefix as an
  extra rule to catch it; that was dropped, because it masks only truncations
  that happen to leave ≥16 bytes while implying general coverage it does not
  have. Closing this properly means acting at the truncation site — redact
  before `Summarize` truncates, or stop logging those fields — which is a
  change to `toollog`'s callers, not to the mask. Out of scope here.
- **Retention as the backstop (#1241).** Explicit config is exact but only as
  complete as the declarations; a bounded retention window caps the exposure
  of anything undeclared. There is currently *no* deletion path at all —
  chunks outlive archiving and die only with the task row. Recommendation:
  resolve #1241's open question with age-based GC (delete chunks N days after
  their task is archived — transcripts' diagnostic value decays fast), as a
  separate small proposal.
- **Event payloads.** `task failed` reasons and agent `report` messages can
  carry the same material into the events table (e.g. an error string echoing
  a credentialed URL). Should the driver run its event submissions through
  the same secret map? Cheap once the plumbing exists, but it widens scope
  past the log pipeline this issue is about.