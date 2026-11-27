# Agent Skills Discovery

Issue: https://github.com/icholy/gritz/issues/635

## Problem

The repo carries a set of curated skills in `.claude/skills/` — `gritz-task`, `grpc`, `webui`, `testing`, `proposal` — that teach an agent how to drive gritz. The `gritz-task` skill in particular is consumer-facing: it documents how an agent creates and manages gritz tasks via the MCP tools. Today the only way for a user running their own Claude Code / Cursor / etc. against a deployed gritz to pick that knowledge up is to clone the repo and copy the SKILL.md by hand.

Cloudflare's [Agent Skills Discovery RFC](https://github.com/cloudflare/agent-skills-discovery-rfc) (v0.2.0) standardises this. A site advertises skills at `/.well-known/agent-skills/index.json`; tools like `npx skills add <url>` fetch that index, verify SHA-256 digests, and install the skills into the user's agent. If `https://gritz.dev` served this endpoint, equipping an agent with the gritz skills would collapse to:

```
npx skills add https://gritz.dev
```

## Design

### What we publish

A curated subset of `.claude/skills/` is exposed. The initial subset is **just `gritz-task`** because that is the only one useful to consumers of a deployed gritz — the others (`grpc`, `webui`, `testing`, `proposal`) are workflow guidelines for contributors to this repo and would only add noise for downstream users.

The curated list is an explicit allowlist in code, not a directory convention. Future skills opt in by being added to the list — this keeps internal-only guidelines from leaking out the discovery endpoint by accident.

### Source-of-truth and embedding

Canonical skill files stay in `.claude/skills/<name>/SKILL.md`. That is the path Claude Code already discovers project skills from, and we do not want to move them.

`go:embed` cannot reach paths above its package directory, so we cannot embed `.claude/skills/` directly from `internal/server/`. Two approaches were considered:

1. **Top-level embed file** — a Go file at the module root with `//go:embed all:.claude/skills` that re-exports an `embed.FS`. Works, but introduces a package at the module root just for one embed, which is unusual for this codebase.
2. **Mirror under the consumer package** — a new `internal/agentskills/` package contains a `data/` subdirectory mirroring the published skills, populated by `go generate` (or `mise run`) from the canonical `.claude/skills/` sources. The mirrored content is committed.

We pick (2). It keeps the embed local to the package that owns the feature, makes the published surface explicit (you can `ls internal/agentskills/data/` to see exactly what ships), and gives us a natural place to enforce the allowlist. The cost is the mirror step; we add a CI check that fails if the mirror is out of date.

Layout:

```
internal/agentskills/
  agentskills.go              // package: published list, embed FS, index, handler
  agentskills_test.go         // tests for index building, digest, handler responses
  data/
    gritz-task/SKILL.md      // mirrored from .claude/skills/gritz-task/SKILL.md
generate.go                   // existing file; add `go:generate` directive
mise.toml                     // add `sync:skills` task that runs the mirror script
```

The mirror is a tiny Go program (or a 10-line shell loop driven by `mise`) that, for each name in the allowlist, copies `.claude/skills/<name>/SKILL.md` to `internal/agentskills/data/<name>/SKILL.md`. CI runs it and `git diff --exit-code`s.

### Package shape

```go
package agentskills

//go:embed all:data
var skillsFS embed.FS

// Published is the allowlist of skill names that are exposed over the
// discovery endpoint. Order is the order they appear in the index.
var Published = []string{
    "gritz-task",
}

type Handler struct {
    baseURL string          // e.g. "https://gritz.dev"
    index   []byte          // pre-computed index.json bytes
    indexETag string        // strong ETag based on the index hash
    skills  map[string][]byte // name -> SKILL.md bytes
}

func New(baseURL string) (*Handler, error)
func (h *Handler) Routes(mux *http.ServeMux) // registers /.well-known/agent-skills/*
```

`New` is called once at server startup. It walks `Published`, reads each SKILL.md from the embedded FS, computes its SHA-256, builds the index JSON, and stores both. There is no per-request filesystem work and no caching surprises.

### Index format

The handler produces JSON conforming to schema v0.2.0:

```json
{
  "$schema": "https://schemas.agentskills.io/discovery/0.2.0/schema.json",
  "skills": [
    {
      "name": "gritz-task",
      "type": "skill-md",
      "description": "Create gritz tasks using the MCP tools. Use when the user wants to create a task for the gritz system.",
      "url": "/.well-known/agent-skills/gritz-task/SKILL.md",
      "digest": "sha256:<hex>"
    }
  ]
}
```

Field sourcing per skill:

- `name` — directory name (e.g. `gritz-task`).
- `description` — pulled from the SKILL.md YAML frontmatter `description:` field. This is the same field Claude Code uses for skill activation, so we get one source of truth.
- `type` — hard-coded to `"skill-md"`. The current skills are single-file. If a skill ever grows scripts/resources we will switch its entry to `"archive"` and serve a `.tar.gz`; that is a follow-up, not v1.
- `url` — path-absolute, served from the same origin. Easier to reason about than relative URLs and works from any base URL (production, local dev, fly preview).
- `digest` — `sha256:` + lowercase hex of the SKILL.md bytes. Computed at startup.

A small struct (`name`, `description` parsed from frontmatter) is computed once at `New`; the only YAML the package needs to handle is the small frontmatter block, which we parse with `gopkg.in/yaml.v3` (already a transitive dep — verify, otherwise use a 30-line hand-rolled parser since the frontmatter shape is fixed).

### HTTP surface

Routes registered on the same `otelx.NewMux` instance built in `internal/server/server.go`:

| Path | Handler | Auth | Cache |
|---|---|---|---|
| `GET /.well-known/agent-skills/index.json` | `Handler.serveIndex` | none (public) | `Cache-Control: public, max-age=300`, strong `ETag` derived from index hash, supports `If-None-Match` → 304 |
| `GET /.well-known/agent-skills/<name>/SKILL.md` | `Handler.serveSkill` | none (public) | `Cache-Control: public, max-age=300`, strong `ETag` = `"sha256-<hex>"`, supports `If-None-Match` → 304 |

Wiring in `internal/server/server.go` mirrors the existing OAuth well-known registration:

```go
// in (*Server).Handler()
if skills, err := agentskills.New(s.baseURL); err == nil {
    skills.Routes(mux)
}
```

Both endpoints are public — they are intentionally discoverable. They sit outside the `auth.RequireAuth()` chain alongside the existing `/.well-known/oauth-*` endpoints. CORS already applies via the existing `handleCORS` middleware when the server runs with `--cors`; that is sufficient since the protocol does not require credentialed cross-origin reads.

`Content-Type`:

- `index.json` → `application/json; charset=utf-8`
- `SKILL.md` → `text/markdown; charset=utf-8`

`SKILL.md` is served as the raw bytes that were digested — no rewriting, no template expansion — so the SHA-256 in the index matches what a client downloads.

### Frontmatter trimming

Each `.claude/skills/<name>/SKILL.md` opens with YAML frontmatter that contains `name` and `description`. The published file is served **byte-for-byte identical** to the source. This is important: the digest in the index is the digest of the served bytes. We do not strip or rewrite frontmatter for publication. If a published skill needs a different description from the in-repo one, we update the source.

### CLI / discovery story

The discovery endpoint is the only contract. We do not ship a custom CLI; users invoke whatever client they already use. Examples (verified against the RFC, not against a specific client release):

```
# Hypothetical npx skills client (vercel-labs/skills supports URLs that point at index.json hosts)
npx skills add https://gritz.dev

# Manual fetch + install
curl https://gritz.dev/.well-known/agent-skills/index.json
curl https://gritz.dev/.well-known/agent-skills/gritz-task/SKILL.md \
  -o ~/.claude/skills/gritz-task/SKILL.md
```

The README gains a short "Use gritz from your agent" section that documents the `npx skills add` form and the manual form.

### Tests

`internal/agentskills/agentskills_test.go`:

- Index round-trip: build a `Handler`, decode `index.json`, assert each entry's `name`, `description`, `url`, and that `digest` matches the SHA-256 of the served SKILL.md body.
- 200 + ETag on first fetch, 304 on `If-None-Match` with the same ETag.
- `Content-Type` headers correct for both endpoints.
- Unknown skill name → 404.
- A skill listed in `Published` whose file is missing from `data/` → `New` returns an error (fail fast at startup, not at request time).

Per the project testing guidelines, the handler tests use `httptest.NewServer` and a real `http.Client`; no mocked transports.

### CI: mirror freshness

A new `mise run check:skills` step:

1. Runs the mirror.
2. `git diff --exit-code internal/agentskills/data` — fails if a published skill's source under `.claude/skills/` has changed without the mirror being regenerated.

Wired into the existing `lint` / `test` job in `.github/workflows/`.

## Trade-offs

- **Mirror vs. top-level embed.** Mirroring adds a sync step and a CI check, but it makes the published surface auditable in one place. A top-level embed would avoid duplication but blurs the line between "developer-only skills" and "published skills" — a contributor adding a SKILL.md under `.claude/skills/` would not expect it to be published. We pay the mirror cost to keep that line clear.
- **Allowlist vs. opt-out marker.** An alternative is a `published: true` flag in each SKILL.md's frontmatter. Allowlist is simpler and avoids polluting the frontmatter that Claude Code already consumes. The cost is one extra place to edit when publishing a new skill — acceptable for the volume we expect.
- **`skill-md` only, no archives in v1.** Archives let a skill ship supporting scripts and references. None of the current skills need that. Adding archive support upfront would force us to pick a packaging tool (tar+gzip) and a deterministic bundling strategy now, for zero current value. Deferred until a published skill actually needs more than one file.
- **No signing.** The RFC's security section recommends digest verification (which we provide) plus client-side origin allowlists (which is the client's job). We are not setting up artifact signing at this stage; HTTPS + digest is the bar the RFC sets and is sufficient for the trust level here (skills come from the same origin as the gritz server the user is already trusting).
- **Public, unauthenticated endpoint.** The index reveals what skills the deployment publishes — that is the point. If a deployment ever needed to gate skills behind auth, we would expose a separate authenticated route; the public `.well-known/` is for things meant to be discovered.

## Open Questions

- **Naming collisions.** `gritz-task` is fine as a global name today; if other projects publish a same-named skill, installers will conflict. Do we prefix (`gritz/gritz-task`)? The RFC does not mandate namespacing. Leaving as `gritz-task` for v1 and revisiting if a real collision shows up.
- **Versioning published skills.** The RFC's discovery schema is versioned but individual skills are not. If we change `gritz-task` in a breaking way (renamed MCP tool, removed field), installed copies will diverge silently. Worth considering a `version` field on each skill entry — non-standard but additive — or simply documenting that skills are a moving target and clients should re-add periodically.
- **Cache TTL.** `max-age=300` is a guess. Skill files change rarely; we could go higher (an hour, a day) and rely on digest changes flowing through. Lower keeps clients fresher. Pick one before merging.
- **Should the runner's workspace skills also be published?** A workspace can ship its own SKILL.md set via mounted volumes. Out of scope for v1 (this proposal is about static, server-embedded skills) but worth flagging as a future direction: per-workspace `.well-known/agent-skills/`.