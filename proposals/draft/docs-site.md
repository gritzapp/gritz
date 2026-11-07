# docs.gritz.dev docs site

Issue: https://github.com/gritzapp/gritz/issues/2034

## Problem

gritz has no user documentation. What exists is spread across `README.md` (runner
quick start, secrets, debugging, events), `CLAUDE.md`, skills, the
`gritzapp/workspaces` repo and implemented proposals. Proposals describe designs
for contributors, not how to use a feature. A user who wants to route alerts from
their monitoring system into gritz has to read `proposals/implemented/custom-external-events.md`
and `internal/server/customserver/webhook.go` to find the envelope format and the
rule that custom rules must be public.

Agents have the same problem, and they are a large share of gritz's readers: the
agents gritz runs, and the agents users drive gritz from over MCP.

#2034 asks for a public, static docs site at `docs.gritz.dev`, written as Markdown
in this repo. These decisions are already made, and this proposal designs within
them:

- **Generator:** Starlight (Astro). Source is Markdown in `docs/`, built with pnpm
  like `webui/` and `diagrams/`.
- **Hosting:** GitHub Pages from the existing `gh-pages` branch. Docs publish to
  the root and `docs.gritz.dev` becomes the Pages custom domain. The Motion Canvas
  diagrams move to `/diagrams/`, and their PR previews to `/diagrams/pr/<n>/`. Old
  `gritzapp.github.io/gritz/<diagram>` URLs may break.
- **Audience:** users of a gritz deployment first, plus a separate operators
  section. Contributor docs stay in `CLAUDE.md`, skills and `proposals/`.
- **Agent accessible:** `llms.txt` and `llms-full.txt`, raw Markdown for each page,
  and static HTML that needs no JS. Serving the docs through the gritz MCP server
  is a later layer.
- **First content:** custom events.

## Design

### Layout of `docs/`

`docs/` is a standalone pnpm project, set up like `diagrams/`: its own
`package.json`, `pnpm-lock.yaml` and `pnpm-workspace.yaml` (with the
`allowBuilds: esbuild: true` that Astro needs too).

```
docs/
  package.json            # astro, @astrojs/starlight, starlight-llms-txt, starlight-links-validator
  pnpm-lock.yaml
  pnpm-workspace.yaml
  astro.config.mjs        # site, base, sidebar, plugins
  tsconfig.json
  public/
    CNAME                 # docs.gritz.dev
    .nojekyll             # Pages must not run Jekyll: it drops `_astro/`
  src/
    content.config.ts     # Starlight's docs collection
    content/docs/         # the Markdown pages; the path is the URL
      index.md
      concepts/...
      integrations/...
      reference/...
      operators/...
    pages/
      [...slug].md.ts     # raw Markdown endpoint (see "Agent access")
    components/
      Diagram.astro       # diagram embed (later layer)
```

`astro.config.mjs` sets these:

- `site: 'https://docs.gritz.dev'`, used for absolute URLs in `llms.txt`, the
  sitemap and canonical links.
- `base: process.env.DOCS_BASE ?? '/'`. PR previews build with
  `DOCS_BASE=/pr/<n>/`. Unlike `diagrams/site.ts`, Astro writes absolute URLs, so a
  preview needs its own base rather than relative links.
- `sidebar`: written out explicitly, one group per section. Each group uses
  `autogenerate: { directory }`, so a page shows up in its group as soon as its file
  lands, and a group without pages yet is left out.
- `editLink.baseUrl: 'https://github.com/gritzapp/gritz/edit/master/docs/'`.
- Plugins: `starlight-llms-txt`, and `starlight-links-validator` so a broken
  internal link fails the build.
- When `DOCS_BASE` is set (a preview), `head` adds
  `<meta name="robots" content="noindex">`.

Pages are `.md` by default. `.mdx` is allowed only on pages that need a component
(today, only the diagram embed). This keeps the raw Markdown we serve to agents as
close as possible to what they would read on GitHub.

A `mise` task mirrors `motioncanvas`:

```toml
[tasks.docs]
description = "Start the docs dev server for docs/"
run = "cd docs && pnpm install --frozen-lockfile && pnpm dev"
```

### Page list and sidebar

Sections are listed in sidebar order. The slug is the file path under
`src/content/docs/` and the URL path. ★ marks the first content to land.

**Start here**
- `index` — what gritz is: agents running in sandboxes, driven by tasks and events.
  Links to each section, and to `llms.txt` for agents.
- `getting-started` — using a deployment: sign in, pick an org, create a task from
  the Web UI, watch it run, follow up with another instruction, archive it.

**Concepts** (`concepts/`)
- `tasks` — instructions, the task lifecycle and statuses, restarts, auto-archive,
  archived tasks, the task log.
- `workspaces` — the user's view: what a workspace provides (image, repos, agent,
  MCP servers), how to pick one, and that operators define them.
- `runners` — the user's view: where a task runs, and choosing a runner.
- `events-and-links` — links, `subscribe=true`, how an event on a linked URL wakes
  a task, and the event timeline. Moved from the README's `## Events`, with
  `images/events.svg`.
- `routing-rules` — event source and type, conditions (`equals`/`prefix`/`contains`),
  first match wins, wake versus create, `public`, the test-event form.
- `namespaces` — task namespaces and how they scope routing.
- `schedules` — scheduled tasks and "run now".
- `orgs-members-keys` — orgs, members, API keys and what they can do.

**Using gritz** (`using/`)
- `web-ui` — a tour of the task list, task page, events, routing and settings.
- `cli` — `gritz task`, `gritz logs`, `gritz shell`, `gritz notify`, and how to
  configure a server URL and API key (`~/.config/gritz/config.yaml`).
- `agent-tools` — the tools gritz injects into every agent (`get_my_task`,
  `update_my_task`, `create_link`, `report`), so users can write instructions that
  use them.

**Integrations** (`integrations/`)
- `github` — installing the GitHub App, linking accounts, PR and issue comment
  events, reactions, and agent GitHub tokens.
- `jira` — linking Atlassian, and issue and comment events.
- `custom-events` ★ — custom event types (Custom Schemas), the `/webhook/custom`
  endpoint, API keys, the envelope, public rules, the subscription URL, and
  troubleshooting by HTTP status.
- `mcp` — connecting Claude Code, Cursor or any MCP client to `/mcp` (OAuth), or to
  `gritz mcp` (stdio). Covers the tools (`list_workspaces`, `create_task`,
  `get_task`, `list_tasks`, `update_task`, `archive_task`) and channels.

**Reference** (`reference/`)
- `custom-event-envelope` ★ — the `POST /webhook/custom` request and response:
  fields, limits (1 MiB), status codes.
- `event-types` — the built-in `(source, type)` pairs and their attrs, for writing
  conditions.
- `cli` — every `gritz` subcommand and flag.

**Operators** (`operators/`)
- `overview` — server, runner, driver and sandbox, with `images/architecture.svg`.
  Who talks to whom, and what each needs to reach.
- `server` — running `gritz server`: Postgres, auth, the GitHub App and Atlassian
  config, base URL, and the server's MCP and OAuth endpoints.
- `runner` — running `gritz runner`: API key, `gritz download`, the config file.
  Moved from the README's Quick Start.
- `backends` — Docker and Lambda MicroVMs, and when to use which. `local` is left
  out because it is for tests and development.
- `workspaces-yaml` — a `workspaces.yaml` reference: container, commands, agent
  (`type`, `verbose`), MCP servers, and `${env:}`/`${sh:}` expansion. Links to
  `gritzapp/workspaces` for examples.
- `secrets` — the `secrets:` map and what is masked where. Moved from the README's
  `## Secrets`.
- `deployment` — the published GHCR images, running the runner as a Docker Compose
  service (links `gritzapp/workspaces/runner`), upgrades (server, runner and driver
  ship together).
- `debugging` — `gritz logs`, `verbose: true`, `gritz shell`, `gritz containers`
  and `prune`. Moved from the README's `## Debugging`.

This list is the target. The layers below bring it in a few pages at a time, and
because of autogenerate the sidebar never links to a missing page.

### What moves out of `README.md`

| README section | Goes to | README keeps |
|---|---|---|
| Intro + Features + Web UI screenshot | `index` | yes, plus a "Docs: https://docs.gritz.dev" line under the tagline |
| Quick Start | `operators/runner` (runner setup), `getting-started` (Web UI) | a short "Running a runner" pointer to the docs |
| Workspace Examples | `operators/workspaces-yaml` | no |
| Secrets | `operators/secrets` | no |
| Docker Compose Runner | `operators/deployment` | no |
| Debugging | `operators/debugging` (and `using/cli` for `gritz logs`) | no |
| Local Development, Build | — (contributor docs) | yes |
| Events | `concepts/events-and-links` | no |
| Architecture, Schema images | `operators/overview` takes architecture | yes, for contributors |

When it's done, the README is a landing page for the repo: what gritz is, a link
to the docs, and how to build and develop it. The `CLAUDE.md` architecture notes
stay where they are, since they are for contributors.

### Hosting on `gh-pages`

One branch is served by Pages, written by four workflows. Each writes only its own
subtree:

| Workflow | Trigger | Writes | `clean-exclude` |
|---|---|---|---|
| `docs-pages.yml` (new) | push to master, `docs/**` | `/` | `diagrams/`, `pr/` |
| `docs-preview.yml` (new) | PR, `docs/**` | `/pr/<n>/` | — |
| `diagrams-pages.yml` | push to master, `diagrams/**` | `/diagrams/` | `pr/` (that is, `/diagrams/pr/`) |
| `diagrams-preview.yml` | PR, `diagrams/**` | `/diagrams/pr/<n>/` | — |

How the four avoid clobbering each other:

- All four keep `concurrency: group: gh-pages, cancel-in-progress: false`, so
  pushes to the branch are serialized.
- `JamesIves/github-pages-deploy-action` only cleans inside its `target-folder`,
  and `clean-exclude` is relative to that folder. The root docs deploy is the only
  one that cleans `/`, and it excludes the other three subtrees. The diagrams
  deploy cleans `/diagrams/` except `pr/`. A preview cleans only its own
  `pr/<n>/`.
- `CNAME` and `.nojekyll` come from `docs/public/`, so every root deploy writes them
  again. Nothing else writes them, so nothing can delete them.
- The diagrams change is just a `target-folder: diagrams` on `diagrams-pages.yml`,
  and `PREVIEW_DIR: diagrams/pr/<n>` on `diagrams-preview.yml`. `site.ts` already
  uses only relative URLs, so the same build works at both paths.
- Both preview workflows build the PR comment URL from
  `gh api repos/$GITHUB_REPOSITORY/pages --jq .html_url`. That returns
  `https://docs.gritz.dev/` once the custom domain is set, so the comment step
  doesn't change.

`docs-preview.yml` is a copy of `diagrams-preview.yml` with these changes:

- The build step sets `DOCS_BASE: /pr/${{ github.event.pull_request.number }}/`.
- The sticky comment header is `docs-preview`, so a PR that touches both `docs/`
  and `diagrams/` gets two separate comments.
- The build runs for fork PRs as well, as a check. Only the deploy, removal and
  comment steps keep the same-repo condition. `diagrams-preview.yml` skips the whole
  job for forks, but a broken docs link should fail a fork PR too.

**Moving off the old layout.** The `gh-pages` root currently holds the diagrams
site (`index.html`, `create-task/`, `proposals/`, chunks) and the diagram previews
under `pr/<n>/`:

- Once `diagrams-pages.yml` has moved, the old root files go stale. The first
  `docs-pages.yml` deploy deletes them, because they aren't in `diagrams/` or
  `pr/`.
- Old diagram previews in `/pr/<n>/` would survive that deploy, since `pr/` is
  excluded, and their PRs' close events now clean `diagrams/pr/<n>/` instead. So
  the diagrams-move layer finishes with a one-time commit to `gh-pages` that removes
  the root `pr/`. Open PRs get their preview back at the new path on their next
  push.
- The `motioncanvas` skill's URLs (`gritzapp.github.io/gritz/` and
  `.../pr/<number>/proposals/<flow>/`) change to `/diagrams/...` in the same layer.

**Custom domain.**

- `docs/public/CNAME` contains `docs.gritz.dev`. For a branch-based Pages source,
  the CNAME file at the branch root sets the custom domain.
- After that, `gritzapp.github.io/gritz/...` redirects to `docs.gritz.dev/...`. A
  redirected old diagram URL like `/create-task/` will 404. That breakage is
  accepted.
- After GitHub has issued the certificate, HTTPS is enforced through the API
  (`gh api -X PUT repos/gritzapp/gritz/pages -F https_enforced=true`) or with the
  checkbox in repo settings.

**The one manual step outside the repo** is a DNS record in the `gritz.dev` zone:

```
docs.gritz.dev.  CNAME  gritzapp.github.io.
```

It has to exist before the CNAME file is deployed. Otherwise Pages redirects the
working `github.io` URL to a host that doesn't resolve, and certificate issuance
stalls. Verifying `gritz.dev` for the `gritzapp` org (a TXT record) protects
against subdomain takeover. It's recommended but not required; see Open Questions.

### Embedding diagrams

Docs pages can show diagrams, with some limits. The diagrams are a separate Vite
build with a JS player, deployed on its own, so the docs never bundle Motion
Canvas.

- **Links (from the start).** A page links to `/diagrams/<slug>/`, which is an
  ordinary Markdown link. It works without JS, in raw Markdown and in `llms.txt`.
- **Embeds (later layer).** A `Diagram.astro` component renders
  `<figure><iframe src="/diagrams/<slug>/?embed" loading="lazy"></iframe><figcaption><a href="/diagrams/<slug>/">…</a></figcaption></figure>`.
  `site.ts` gets an `?embed` mode, read by `controls.ts`, that hides the
  "All diagrams" link and the heading. Without JS, or in raw Markdown, the reader
  still gets the caption link. Pages that embed a diagram are `.mdx`. Their raw
  Markdown keeps the `<Diagram slug="create-task" />` line, which an agent can read
  as a reference.
- **Version skew.** The iframe points at the root `/diagrams/`, so a docs PR
  preview embeds master's diagrams, and a docs page can reference a diagram that
  hasn't merged yet. Rather than adding cross-deploy plumbing, the link validator
  isn't run on `/diagrams/` paths, and reviewers check the embed after both have
  merged.

### Agent access

- **Static HTML.** Starlight renders every page as static HTML. JS is only needed
  for search (Pagefind), the theme toggle and tabs, and none of these gate any
  content. The rule: no content may depend on a client-side component.
- **Raw Markdown per page.** `src/pages/[...slug].md.ts` is a static Astro endpoint.
  It runs `getStaticPaths` over `getCollection('docs')` and returns
  `# ${entry.data.title}\n\n${entry.body}` with `Content-Type: text/markdown`.
  Pages pick the type up from the `.md` extension.
  - The URL rule: take the page URL, drop the trailing slash, and append `.md`. For
    example, `https://docs.gritz.dev/integrations/custom-events/` →
    `https://docs.gritz.dev/integrations/custom-events.md`. The root page is `/index.md`.
  - A Starlight `Head` override adds
    `<link rel="alternate" type="text/markdown" href="….md">` to each page, so a
    crawler on the HTML page can find the source.
- **`llms.txt` / `llms-full.txt`.** We adopt `starlight-llms-txt`. It generates
  `/llms.txt`, `/llms-full.txt` (every page, concatenated) and `/llms-small.txt`
  (minified) from the same content collection, and respects `site` and `base`. We
  configure it like this:
  - `projectName: 'gritz'` and a `description` that says what gritz is, for the
    `llms.txt` header.
  - `customSets` for **Users** (everything except `operators/**`) and **Operators**
    (`operators/**`), so an agent can fetch only the half it needs.
  - `promote: ['index', 'concepts/**']`, so `llms-full.txt` starts with the mental
    model.

  What to check when adopting it (layer 7): `llms.txt` should link each page's
  `.md` URL as well as the sets. If the plugin's `llms.txt` lists only the sets, we
  turn its `llms.txt` off and write `src/pages/llms.txt.ts`, about 40 lines over
  the same collection, in the llmstxt.org format. Each section heading gets a list
  of `[title](….md): description` entries. We keep the plugin's `llms-full.txt` and
  `llms-small.txt` either way.
- Every page has a `description` in its frontmatter. Starlight puts it in the HTML
  meta tag, and `llms.txt` uses it for each entry.

### Docs through the gritz MCP server (later layer, sketch)

The server's user-facing MCP server (`internal/server/mcpserver`, served at `/mcp`
and proxied by `gritz mcp`) gets two read-only tools. That way an agent driving
gritz can read the docs for the server it's talking to without leaving MCP.

- The Markdown is embedded in the binary instead of fetched from `docs.gritz.dev`.
  Server, runner and driver ship together, so docs embedded at build time match
  that server's version exactly. It also needs no network access and works for
  air-gapped deployments.
- A Go package in `docs/` embeds `src/content/docs`. `node_modules` is never
  matched, because the embed pattern is that one directory.
- `list_docs` returns slug, title and description for every page. That's
  `llms.txt` as structured data.
- `read_doc(slug)` returns the same text as the `.md` endpoint.
- No search tool at first, because the docs are small enough to list. Revisit if
  they grow.

```go
package docs // github.com/gritzapp/gritz/docs

// Page is one docs page's metadata and Markdown source.
type Page struct {
	Slug        string
	Title       string
	Description string
	Body        string
}

// Pages returns every embedded docs page, sorted by slug.
func Pages() []Page

// Lookup returns the page with the given slug.
func Lookup(slug string) (Page, bool)
```

```go
package mcpserver // github.com/gritzapp/gritz/internal/server/mcpserver

// AddTools registers the user-facing gritz tools, now including list_docs and read_doc.
func AddTools(server *mcp.Server, service gritzv1connect.GritzServiceHandler, opts ...Option) // changed
```

**How it fits:** `docs.Pages` parses the frontmatter of each embedded file once.
`AddTools` registers `list_docs` and `read_doc`, which call `docs.Pages` and
`docs.Lookup` and never touch the `GritzServiceHandler`. Before that layer, nothing
on the Go side changes.

### First content: custom events

The first pages are `integrations/custom-events` and
`reference/custom-event-envelope`. Both stay generic: the running example is an
alerting system posting an `alert` type, not any particular vendor. Examples use
`https://gritz.dev/...` URLs, which is the deployment users read about; a
self-hosted deployment finds its own URL in the same place in its Web UI.

**`integrations/custom-events`** shows how to get events from any system into
gritz:

1. Define a type on Events → Custom Schemas. It has a `type` name matching
   `^[a-z][a-z0-9_]{0,63}$`, a label, and attrs that must include `body` and `url`.
   The example is `alert`, with the attrs `body`, `url`, `severity` and `service`.
2. Create an API key (`/ui/keys/new`). The webhook takes it as `Authorization:
   Bearer xat_…`. The webhook URL is on Settings → Organisation, in the Custom
   Events card (`GetOrgSettingsResponse.custom_webhook_url`). For gritz.dev it is
   `https://gritz.dev/webhook/custom`.
3. `POST` the envelope (`type`, `body`, `url`, `attrs`, `details`; unknown fields are
   rejected) and read `{"routed": n}` from the response:

   ```bash
   curl https://gritz.dev/webhook/custom \
     -H "Authorization: Bearer $GRITZ_API_KEY" \
     -H "Content-Type: application/json" \
     -d '{
       "type": "alert",
       "url": "https://alerts.example.com/alerts/4211",
       "body": "checkout-api error rate above 5% for 10 minutes",
       "attrs": {"severity": ["critical"], "service": ["checkout-api"]},
       "details": {"runbook": "https://wiki.example.com/runbooks/checkout-api"}
     }'
   ```

   The key is referenced by variable, so the command can be shared or logged
   without the value.
4. Add a **public** rule for the type: `custom/alert`, `severity equals critical`,
   wake + create in a workspace and runner, with a prompt such as "Investigate this
   alert and report the likely cause." Custom events have no actor, so a
   member-only rule is rejected when saved. Rules are checked in order and the
   first match wins, so a second, condition-free wake-only rule lets follow-up
   events (for example a "resolved" post) reach the task without creating tasks
   for alerts gritz never saw fire.
5. `url` is the subscription key. Every event about the same resource must send
   the same URL, byte for byte, or it won't wake the task the first event created.

The page ends with a troubleshooting table taken from `webhook.go`'s errors:

- 400 unknown type, missing or relative `url`, undeclared attr, `body`/`url` sent
  as attrs, invalid JSON
- 401 bad key
- 403 a token without the `event:create` scope (for example a task token)
- `routed: 0`: no matching public rule, or no subscribed link and no create action

**`reference/custom-event-envelope`** is the precise contract: each envelope
field and its type, the 1 MiB limit, the response body, and every status code.

### Keeping docs in sync with code

- **Same PR.** A PR that changes behaviour a user or operator can see (a flag, a
  workspace field, an envelope field, a routing semantic, an MCP tool) also updates
  the matching page under `docs/`. Reviewers ask for it the same way they ask for
  tests. A new `## Docs` section in `CLAUDE.md` says this, lists which areas map to
  which docs sections, and repeats the `secrets:` rule for examples. Agents writing
  PRs read `CLAUDE.md`, so they pick the rule up there.
- **The build is the check.** `docs-preview.yml` builds every PR that touches
  `docs/`, and `starlight-links-validator` fails it on a broken internal link or
  anchor. That catches renamed pages and stale cross-links, but not stale content.
- **Previews make review cheap.** The sticky comment links the rendered pages, so
  a reviewer reads the docs as users will see them.
- **Generated reference later.** `reference/cli` and `reference/event-types` are
  handwritten at first. Both have a source of truth in code (the urfave/cli command
  tree; `eventrouter.DefaultSchemaRegistry`). Generating them is an open question,
  not a first-layer requirement.

### API

Nothing in Go changes until the MCP layer, which is sketched above with its API.
The other layers only add `docs/`, workflows, `mise.toml` entries, the `CLAUDE.md`
section and the README edit.

## Implementation Plan

1. **Move diagrams under `/diagrams/`** — Delivers: `target-folder: diagrams` (with
   `clean-exclude: pr/`) in `diagrams-pages.yml`, `PREVIEW_DIR: diagrams/pr/<n>` in
   `diagrams-preview.yml`, updated URLs in the `motioncanvas` skill, and after
   merge a one-time commit to `gh-pages` that removes the root `pr/`. Depends on:
   nothing. Verifiable by: `gritzapp.github.io/gritz/diagrams/` serving the index
   after merge; a test PR touching `diagrams/` getting a preview at
   `/diagrams/pr/<n>/` that is removed when the PR closes.
2. **Scaffold `docs/`** — Delivers: the Starlight project, `astro.config.mjs` (site,
   `DOCS_BASE`, the sidebar groups, the edit link, the links validator), an
   `index.md`, the `mise run docs` task, and no deploy. Depends on: nothing.
   Verifiable by: `cd docs && pnpm build` and the dev server rendering the index.
3. **DNS record** (manual, outside the repo) — Delivers: `docs.gritz.dev CNAME
   gritzapp.github.io.` Depends on: nothing. Verifiable by:
   `dig +short docs.gritz.dev` resolving to GitHub Pages.
4. **Publish docs at the root with the custom domain** — Delivers:
   `docs-pages.yml` (root deploy, `clean-exclude: diagrams/` and `pr/`),
   `public/CNAME`, `public/.nojekyll`, and HTTPS enforced after the certificate is
   issued. Depends on: (1), (2), (3). Verifiable by: `https://docs.gritz.dev/`
   serving the index; `https://docs.gritz.dev/diagrams/` still serving diagrams;
   the old root diagram files gone from `gh-pages`; a later diagrams deploy leaving
   the docs alone, and the other way round.
5. **Custom events docs** — Delivers: `integrations/custom-events` and
   `reference/custom-event-envelope`. Depends on: (4). Verifiable by: the pages
   live on `docs.gritz.dev`; following them from scratch against gritz.dev, from
   type to key to rule to `curl`, and getting `{"routed":1}` and a new task, then
   a second post waking it.
6. **Docs PR previews** — Delivers: `docs-preview.yml` (build for every PR,
   deploy/remove/comment for same-repo PRs, `DOCS_BASE=/pr/<n>/`, `noindex`).
   Depends on: (4). Verifiable by: a PR touching `docs/` getting a `docs-preview`
   comment linking a working `/pr/<n>/`, with its CSS and links resolving under the
   base, removed when the PR closes; a broken link failing the check.
7. **Agent access** — Delivers: the `[...slug].md.ts` endpoint, the
   `rel="alternate"` head link, `starlight-llms-txt` with the Users/Operators sets,
   and a custom `llms.txt` endpoint only if the plugin's lacks per-page links.
   Depends on: (4). Verifiable by: `curl
   https://docs.gritz.dev/integrations/custom-events.md`, `/llms.txt` and
   `/llms-full.txt` returning Markdown; `curl` of a page's HTML containing its full
   text.
8. **Docs-in-sync rule** — Delivers: the `## Docs` section in `CLAUDE.md`, and the
   `README.md` intro gaining the docs link. Depends on: (6), whose build check and
   previews the rule points reviewers at. Verifiable by: review only.
9. **Operators section + README trim** — Delivers: `operators/*` (overview,
   server, runner, backends, `workspaces-yaml`, secrets, deployment, debugging),
   with the README's Quick Start, Workspace Examples, Secrets, Docker Compose
   Runner and Debugging sections moved there and replaced by links. Depends on:
   (8). Verifiable by: the README containing only what the table above keeps; the
   links validator passing.
10. **Concepts + using gritz** — Delivers: `getting-started`, `concepts/*` (with
    `## Events` moved out of the README), `using/*`. Depends on: (8). Verifiable
    by: preview review; a new user creating and following up a task using only
    these pages.
11. **Integrations + reference** — Delivers: `integrations/github`,
    `integrations/jira`, `integrations/mcp`, `reference/event-types`,
    `reference/cli`. Depends on: (10). Verifiable by: preview review; connecting
    Claude Code to `/mcp` by following the page.
12. **Diagram embeds** — Delivers: the `?embed` mode in `diagrams/site.ts` and
    `controls.ts`, `Diagram.astro`, and one embed (for example `create-task` on
    `concepts/tasks`). Depends on: (4), (10). Verifiable by: the embedded diagram
    playing on the page, the caption link working without JS, and the raw `.md`
    keeping the reference.
13. **Web UI links** — Delivers: a "Docs" link in the Web UI navigation, and links
    to `integrations/custom-events` from the Custom Events card on Settings →
    Organisation and from the Custom Schemas tab. Depends on: (5). Verifiable by:
    `pnpm lint`; clicking through.
14. **Docs over MCP** — Delivers: the `docs` Go package (embed and frontmatter
    parsing), and `list_docs`/`read_doc` in `mcpserver.AddTools`. Depends on: (7).
    Verifiable by: unit tests over `docs.Pages`/`Lookup`; calling `read_doc` with
    `integrations/custom-events` through `gritz mcp`.

## Trade-offs

- **Subtree ownership versus separate branches or Pages artifacts.** Only one Pages
  site per repo is possible. Switching to `actions/deploy-pages` artifacts would
  mean every deploy rebuilds the whole site, including all live previews, from a
  single workflow. Keeping `gh-pages` with per-subtree `target-folder` and
  `clean-exclude` lets each of the four workflows stay independent, and reuses the
  pattern the diagrams already rely on.
- **Docs previews at `/pr/<n>/`, not `/docs/pr/<n>/`.** Docs own the root, so their
  previews sit next to it, the same way diagram previews sit inside `/diagrams/`.
  Prefixing `/docs/` would just add path noise.
- **`DOCS_BASE` versus relative URLs.** Astro and Starlight write absolute,
  base-prefixed URLs. A build-time base is the supported way to serve under a
  subpath. Rewriting the output to relative URLs, as `diagrams/site.ts` does, would
  fight the framework.
- **Raw Markdown as an endpoint, not by copying `src/`.** Copying the sources into
  `public/` would publish frontmatter and the content-collection layout unchanged.
  The endpoint serves exactly the page set Starlight renders, under the same slugs,
  with the title as a heading.
- **`starlight-llms-txt` over a handwritten generator.** It's maintained by a
  Starlight core contributor, already does the full/small variants and the custom
  sets, and follows content changes. The only gap we might hit (per-page links in
  `llms.txt`) has a small fallback that doesn't drop the plugin.
- **Embedding docs in the binary for MCP, not fetching from the site.** The version
  always matches, it works offline, and the tools need no HTTP client. The cost is
  that the binary's docs are as of its build. Since server and runner ship
  together, that's correct anyway.
- **No docs-diff CI gate.** A rule like "this PR touches `internal/server/customserver`
  but not `docs/`" would mostly flag refactors. The `CLAUDE.md` rule plus review is
  cheaper, and `starlight-links-validator` catches the structural breakage
  mechanically.

## Open Questions

- **Where is `gritz.dev` DNS managed,** and who adds the record? It's probably in
  `gritzapp/infra`, next to the dokku host. Should we also verify `gritz.dev` for
  the `gritzapp` org (TXT record) to rule out subdomain takeover? That's a second
  record, but also outside the repo.
- **Generated reference pages.** Should `reference/cli` (from the urfave/cli tree)
  and `reference/event-types` (from `DefaultSchemaRegistry`) be generated by a
  `go generate` step that writes Markdown into `docs/`, with CI checking that it's
  current? Then they can't drift.
- **Search for agents.** Is `list_docs` plus `read_doc` enough over MCP, or do
  agents need a `search_docs` (over the embedded text) from the start?
