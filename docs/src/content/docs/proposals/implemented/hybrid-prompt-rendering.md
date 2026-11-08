---
title: "Hybrid Prompt Rendering: Converge Init/Wake, Render Events as Markdown"
description: "With event-native task data and typed external events, there is no defined way for the agent prompt to compose and render the brief and wake context from the event stream."
---

Issue: https://github.com/icholy/gritz/issues/1408

Data-side companions:
- https://github.com/icholy/gritz/issues/1406 (reshapes the `GetTaskDetails`
  *data* to be event-native).
- https://github.com/icholy/gritz/issues/1410 (persists `source`/`type` on
  `ExternalPayload`, which the renderer uses for the external-event label).

This proposal is the **rendering** side: given the event-native data #1406 lands
and the `source`/`type` fields #1410 adds, how the prompt composes and renders
it. It does **not** redesign the RPC or the `ExternalPayload` proto.

## As shipped

Shipped as the layer-cake effort #1408 (L1–L5). Two deliberate divergences from
the design below:

- **Flat event stream, not a `## Context` / `## Instructions` partition.** The
  design partitions the stream by arm — context (external, lifecycle, report)
  under `## Context`, instructions last under `## Instructions`. As shipped,
  both the brief and the wake render a single **flat `renderEvent` stream** in
  stream order, with no section headers. The partition (and its splitter helper)
  was dropped: the per-event `### …` headers already frame each block, the
  per-event timestamps preserve true order, and one flat loop keeps init and
  wake byte-identical without a re-bucketing step. The standing links are
  appended at the end of the first-run brief rather than folded into a
  `## Context` group.
- **`Options` flattened into one shared event loop (follow-up #1421).** Rather
  than threading separate init/wake event collections, `Options` is
  `{Started, Prompt, Task, Events, Links}` and both paths share one
  `{{ range .Events }}` loop over `renderEvent`. The dead `RenderEvent` /
  `renderMessage` helpers and, in L5, the confirmed-dead `get_my_task` bootstrap
  fallback arm were removed; the first run always renders the brief (nil-safe).

## Problem

The bootstrap prompt the driver hands the agent (`internal/agent/agentprompt`)
has two structurally different branches and both lean on JSON blobs:

- **First run** renders the full brief. `RenderBrief` builds the flattened
  `taskDetailsToMap` shape (`id, name, status, workspace, namespace, url,
  instructions, links, events`) and emits it as one `json.MarshalIndent` object.
  Note `instructions` is a *projection of the instruction events* — the same
  data appears twice, once re-bucketed into `instructions` and once raw inside
  `events`. This is exactly the legacy adapter shape #1406 sets out to delete.
- **Wake** renders `Events` as a **JSON array** via `RenderEvent` (protojson →
  re-indented through `encoding/json`), under the line `The task received new
  events:`.
- A third **bootstrap** branch (no brief yet) just tells the agent to go call
  `get_my_task` itself.

Three problems:

1. **The init brief is the old-task shape.** It renders the flattened
   `taskDetailsToMap` JSON — the adapter #1406 is removing — including the
   duplicated `instructions` projection. It should be event-native.
2. **JSON-only is a poor cold read.** A model opening the task for the first
   time gets a wall of protojson: nested single-key objects, RFC3339
   timestamps, `subscribe: true` booleans, quoted field names. It's parseable
   but it doesn't *frame* anything — there's no "here is what you were asked,
   here is what happened, here is what changed."
3. **Init and wake are divergent code paths.** They render the same underlying
   primitive — a task header plus a list of typed events — through two different
   templates and two different Go functions (`RenderBrief` vs `RenderEvent`),
   with duplicated standing-instruction trailers. Every change to how an event
   reads has to be made twice.

### Where this sits in the stack

This continues the arc of #946 (`wake-prompt-event-injection.md`, inject events
into the wake prompt) and #1398 (`first-run-brief-injection.md`, inject the full
brief into the first run). Those two shipped the *plumbing* — the driver now
hands the agent its context instead of making it pull it. Both deliberately
rendered **raw JSON** ("the same shape `get_my_task` returns") as the
expedient first cut. This proposal is the *rendering* follow-up they left open:
now that the data is handed over, make it read well and make the two paths one.

## Design

### Recommendation in one line

**One renderer, one structure, hybrid markdown.** Collapse `RenderBrief` and
`RenderEvent` into a single event renderer that emits a prose-framed markdown
block per event, and render both the init brief and the wake update through the
same header + event-list + trailer structure. Init and wake differ only in
*which slice of the stream* they show and *one framing sentence* — not in
format, and not in code path.

### The assumed input shape (from #1406)

This proposal assumes #1406's event-native `GetTaskDetails`: a **thin task
header** plus the **raw event stream** plus links, with the flattened
`instructions` projection gone. The event-native MCP projection
(`proposals/draft/event-native-mcp-tools.md`) already specifies a flat,
`type`-tagged event object:

```jsonc
{ "id": 43, "type": "instruction", "wake": true,
  "created_at": "2023-11-14T22:15:00Z",
  "text": "Implement the first-run brief.",
  "url": "https://github.com/icholy/gritz/issues/1398" }
```

The renderer keys off the arm discriminator (`Payload.Type()` —
`instruction`/`external`/`report`/`lifecycle`/`link`, the constants in
`internal/model/event.go`). Critically, **the renderer already reads this way
today**: it walks `*gritzv1.Event` and switches on the set oneof arm. So the
renderer can be built against the *current* proto and needs no wire change —
its only coupling to #1406 is that RenderBrief stops reading the flattened
`instructions`/task-object fields and reads the thin header + stream instead.
That makes this proposal buildable in parallel with #1406 and a clean consumer
of it.

### The event renderer: `renderEvent(*Event) string`

A single Go function, replacing `RenderEvent` and the per-message JSON
normalization in `renderMessage`. It switches on the arm and emits a markdown
block. The mapping:

| Arm | Header | Body | Footer |
| --- | --- | --- | --- |
| `instruction` | `### Instruction — {time}` | `text` | `Source: {url}` (if set) |
| `external` | `### {description} — {time}` + `{source} · {type}` label line (if set) | `data` content body (if set), then the `details` map (if set) as an indented-JSON block | `Source: {url}` (if set) |
| `lifecycle` | `### {summary} — {time}` | — | — (actor already folded into `summary`) |
| `link` | `### Link: {title} — {time}` | `relevance` | `{url}` · `(subscribed)` if `subscribe` |
| `report` | `### Report — {time}` | `content` | — |

Notes:

- **Timestamps** render human-readable UTC (`2023-11-14 22:15 UTC`) rather than
  RFC3339. Still fully deterministic — we format it ourselves — but easier to
  read cold. (This is the format the web UI timeline already speaks.)
- **`lifecycle`** reuses `LifecyclePayload.Summary()` (e.g. *"Sandbox exited
  (Running -> Completed)"*, *"Updated name, status by alice"*) — the same
  human line the timeline shows — so the model needn't decode an enum. This
  mirrors the `summary`-beside-structure decision in
  `event-native-mcp-tools.md`.
- **`external` source/type label** (from #1410). #1410 adds `source` (e.g.
  `github`, `jira`) and `type` (e.g. `issue_comment`,
  `pull_request_review_comment`, `pull_request_review`, `issue_assigned`) string
  fields to `ExternalPayload` — today the webhook captures them but the router
  drops them, so the web UI timeline *guesses* the source by URL regex. When set,
  the renderer prints a `{source} · {type}` label line under the header (source
  display-name-cased — `github` → `GitHub` — with a raw fallback), so the model
  sees *"GitHub · pull_request_review_comment"* instead of inferring it from the
  URL. The label is omitted when the fields are empty (pre-#1410 events), so the
  renderer degrades gracefully.
- **`external.data`** is the content body of the event — the comment/review text
  itself. It renders **as-is** (it is already prose/markdown) directly under the
  label/`Source:` lines, so the model reads what the human actually wrote rather
  than only a one-line `description`.
- **`external.details`** is an **opaque map** — different external sources
  populate different keys (GitHub review comments set `path`/`line`/`side`/
  `diff_hunk`; other sources set their own or none), so the renderer cannot know
  which to promote. It renders the whole map as **one indented-JSON block** under
  the content, untouched. This *is* the hybrid: the envelope (header, label,
  `Source:`) and the `data` body are prose markdown, and the opaque `details`
  payload is JSON. See the worked example below.
- **`report`** never enters the brief today (reports are from-agent, not
  to-agent) but the renderer handles the arm so the same function can render a
  full timeline elsewhere without a second code path.

#### Worked example: an external event with source/type, content, and `details`

The richest case is a GitHub review comment: it carries a `source`/`type`
(from #1410), a `data` content body (the comment text), and an opaque `details`
map (`path`/`line`/`side`/`diff_hunk`). Today it renders as a nested protojson
object inside the events array; the hybrid frames it in prose — labelled header,
the comment text as-is — and renders only the opaque `details` map as JSON:

**Before** (protojson, as the wake JSON array emits it, with #1410's fields):

~~~~json
{
  "id": "51",
  "createdAt": "2023-11-14T22:20:00Z",
  "external": {
    "source": "github",
    "type": "pull_request_review_comment",
    "description": "icholy commented on driver.go",
    "url": "https://github.com/icholy/gritz/pull/1394#discussion_r512",
    "data": "This nil check needs a test before we merge — can you add one that covers the wake path?",
    "details": {
      "path": "internal/agent/driver.go",
      "line": "218",
      "side": "RIGHT",
      "diff_hunk": "@@ -215,7 +215,7 @@ func Render(opts Options) {\n-\tTaskDetails: brief,\n+\tTaskDetails: details, // nil on wake"
    }
  }
}
~~~~

**After** (hybrid — labelled prose envelope + content, opaque `details` as JSON):

~~~~
### icholy commented on driver.go — 2023-11-14 22:20 UTC
GitHub · pull_request_review_comment
Source: https://github.com/icholy/gritz/pull/1394#discussion_r512

This nil check needs a test before we merge — can you add one that covers the wake path?

```json
{
  "diff_hunk": "@@ -215,7 +215,7 @@ func Render(opts Options) {\n-\tTaskDetails: brief,\n+\tTaskDetails: details, // nil on wake",
  "line": "218",
  "path": "internal/agent/driver.go",
  "side": "RIGHT"
}
```
~~~~

The header carries the `{source} · {type}` label (`GitHub ·
pull_request_review_comment`) from #1410 instead of the model inferring the
source from the URL; the `data` content body renders as-is so the model reads
the actual comment; and only the opaque `details` map is emitted as an
indented-JSON block (keys sorted by `json.MarshalIndent`). The renderer does not
interpret the `details` keys — no `diff_hunk`-to-fence promotion, no folding
`side` into `line`, no per-key bullets — because `details` is source-defined and
opaque. A GitHub source that sets `path`/`line` and a different source that sets
entirely different keys both render correctly through the same untouched block.
When `source`/`type` are empty (pre-#1410 events), the label line is simply
omitted.

### The converged structure

Both prompts render through the same skeleton. The ordering is deliberate:
**operational guidance first, task instructions last, context in the middle.**
A model reads top-to-bottom, so the standing "how to work" rules frame
everything that follows, and the actual instruction — the thing to act on — sits
closest to where the model starts generating.

```
{header}          # Task {id} · {name}  + workspace/namespace/url (no status)
{how-to-work}     # standing operational guidance — FIRST (init only, see below)
{framing}         # one sentence: "first run, full history" vs "new since last run"
{context}         # non-instruction events: external / lifecycle / report, + links
{instructions}    # to-agent instruction events — LAST
{workspace prompt}
```

Note `header` drops the `Status` line: a task reading this prompt is by
definition running, so status is noise. The header trims to task id/name,
workspace/namespace, and the task url.

**Events are grouped by role, not interleaved chronologically.** Within the
event section the renderer partitions the stream into two groups —
**context** (external, lifecycle, report) rendered under `## Context`, and
**instructions** rendered last under `## Instructions` — instead of one
timestamp-ordered list. This is what lets "instructions last" hold in *both*
init and wake with one code path: on a wake whose new events include both an
external comment and a new instruction, the external lands in `## Context` and
the instruction lands in `## Instructions` at the end — exactly where a
first-run instruction lands. Each block still carries its timestamp, so the
true order stays legible even though the layout groups by role. (Within a group,
events keep stream order.)

The **only** structural differences between init and wake:

1. **Framing sentence** — init: *"This is your first run… the full history
   follows."* wake: *"Since your last run, the task received new events."*
2. **Which events** — init shows the full to-agent slice #1406 hands the brief;
   wake shows just the events newer than the driver's saved cursor (#946). Both
   feed the *same* context/instructions partition.
3. **How-to-work + links** — rendered on init only. The wake resumes the *same*
   session, which already read the header, the guidance, and the links on its
   first turn; re-injecting them every wake is noise. This is a concrete quality
   win from converging: today the standing-guidance block is duplicated across
   all three template branches. (If a wake could ever start a *fresh* harness
   session, the guidance must render there too — see [Open Questions](#open-questions).)

Everything else — the header, the context/instructions partition, the per-event
markdown, the timestamp format — is identical, produced by identical code.

### Before / after

#### Init (first-run brief)

**Before** (`prompt-first-run-brief.golden`, abridged):

```
Here is your task brief:

{
  "events": [
    {
      "id": "43",
      "createdAt": "2023-11-14T22:15:00Z",
      "instruction": {
        "text": "Implement the first-run brief.",
        "url": "https://github.com/icholy/gritz/issues/1398"
      }
    },
    {
      "id": "42",
      "createdAt": "2023-11-14T22:13:20Z",
      "external": {
        "description": "PR review requested",
        "url": "https://github.com/icholy/gritz/pull/1394"
      }
    }
  ],
  "id": 1302,
  "instructions": [
    {
      "text": "Implement the first-run brief.",
      "url": "https://github.com/icholy/gritz/issues/1398"
    }
  ],
  "links": [ { "id": "7", "taskId": "1302", "relevance": "the PR this task opened",
      "url": "https://github.com/icholy/gritz/pull/1394", "title": "feat(agent): first-run brief",
      "subscribe": true, "createdAt": "2023-11-14T22:14:10Z" } ],
  "name": "first-run-brief L2",
  "namespace": "team-core",
  "status": "RUNNING",
  "url": "https://gritz.dev/ui/tasks/1302",
  "workspace": "gritz"
}

If the task does not have a name, use gritz:update_my_task to set one.
... (standing instructions) ...
```

**After:**

```
# Task 1302 · first-run-brief L2

- Workspace: gritz · Namespace: team-core
- Task: https://gritz.dev/ui/tasks/1302

## How to work this task
If the task does not have a name, use gritz:update_my_task to set one.
If you have questions, problems, or take no action, respond on the platform from the most recent instruction or event url, suffixing your message with (task 1302).
When you create a resource (PR, issue, comment), record it with gritz:create_link and subscribe=true so you receive replies. Use subscribe=false only for reference links you didn't create.
Prefer web URLs a user can visit over API URLs.
Use gritz:report to log important observations. Your text responses are not visible to users — only tool calls are.
If you need to re-check for updates mid-run, call gritz:get_my_task.

This is your first run on this task. Its full context is below — you already
have everything you need and do not need to call get_my_task to begin.

## Context

### PR review requested — 2023-11-14 22:13 UTC
GitHub · pull_request_review
Source: https://github.com/icholy/gritz/pull/1394

### Link: feat(agent): first-run brief — 2023-11-14 22:14 UTC
The PR this task opened. https://github.com/icholy/gritz/pull/1394 (subscribed)

## Instructions

### Instruction — 2023-11-14 22:15 UTC
Implement the first-run brief.
Source: https://github.com/icholy/gritz/issues/1398
```

The instruction now uses a fixed `### Instruction — {time}` header with the
text in the **body**: instruction text is often long markdown, so putting it in
the title would break the layout. And the instruction sits **last**, directly
above where the model begins working. The standing "how to work" guidance is the
first thing after the header, framing everything the model reads.

#### Wake (new events)

**Before** (`prompt-wake-events.golden`):

```
The task received new events:

[
{
  "id": "42",
  "createdAt": "2023-11-14T22:13:20Z",
  "external": {
    "description": "PR review requested",
    "url": "https://github.com/icholy/gritz/pull/1394"
  }
},
{
  "id": "43",
  "createdAt": "2023-11-14T22:15:00Z",
  "instruction": {
    "text": "keep going",
    "url": "https://github.com/icholy/gritz/issues/2"
  }
}
]

Continue working on the task.
```

**After:**

```
# Task 1302 · first-run-brief L2

Since your last run, the task received new events:

## Context

### PR review requested — 2023-11-14 22:13 UTC
GitHub · pull_request_review
Source: https://github.com/icholy/gritz/pull/1394

## Instructions

### Instruction — 2023-11-14 22:15 UTC
keep going
Source: https://github.com/icholy/gritz/issues/2

Continue working on the task.
```

The `## Context` / `## Instructions` partition and the per-event blocks are
**byte-for-byte the same rendering** as the init brief above — that's the
convergence. The new external comment lands in `## Context` and the new
instruction lands last in `## Instructions`, exactly where a first-run
instruction sits. A wake with nothing pending keeps today's terse fallback
(`The task was updated. Continue.`).

### Code shape

`internal/agent/agentprompt` after:

- `renderEvent(*gritzv1.Event) string` — the single arm-switch renderer above.
  Replaces `RenderEvent`, `renderMessage`, and the JSON-normalization comment
  block. No more protojson-through-`encoding/json` round trip.
- `renderHeader(*gritzv1.Task) string` (id/name + workspace/namespace/url, no
  status) and `renderLinks([]*TaskLink) string` — small helpers.
- A partition helper splits a `[]*Event` into `(context, instructions)` by arm
  so both paths render `## Context` then `## Instructions` from the same code.
- `RenderBrief(resp)` becomes: header + how-to-work + framing + `renderEvent`
  over the context group + `renderEvent` over the instruction group. Reads the
  thin header + stream from #1406, no longer builds the `map[string]any` with the
  duplicated `instructions` key.
- `PROMPT.md` collapses to one structure with a shared how-to-work partial. The
  `{{- if .Started -}}` split shrinks to: pick the framing sentence and the
  event slice; render how-to-work + links only when `!Started`. The
  context/instructions partition and per-event rendering are shared.

`Options` is unchanged (`Started`, `Prompt`, `Events`, `TaskDetails`), so
`internal/agent/driver.go:218` needs no change. The rendering funcs registered
on the template change; the driver contract does not.

## Implementation Plan

A layer cake of rendering-only slices. No schema, no proto, no RPC changes here
— every layer is a pure change to `internal/agent/agentprompt` verifiable by its
golden and unit tests (`go test ./internal/agent/agentprompt/ -run
TestRenderGolden [-update]`). Layers land one at a time; the driver contract
(`Options`) is stable throughout.

1. **Hybrid event renderer.** Delivers: `renderEvent(*Event) string` covering
   all five arms (table above), plus per-arm unit tests, plus the
   human-timestamp formatter. The external arm renders the `{source} · {type}`
   label when those fields are set and omits it otherwise, so this layer lands
   against the current proto and the label simply lights up once #1410 populates
   the fields. Not yet wired into the template. Depends on: nothing (works
   against the current `Event` proto; #1410 is a soft dependency for the label
   only). Verifiable by: table unit tests, one per arm, asserting the markdown
   block for each — including an external event with and without source/type.

2. **Wake path renders via `renderEvent`.** Delivers: the wake branch of
   `PROMPT.md` loops `renderEvent` instead of emitting a JSON array; add the
   header line. Depends on: (1). Verifiable by: updated `prompt-wake-events*`
   goldens; the JSON array is gone.

3. **Brief renders via `renderEvent` + header/links helpers.** Delivers:
   `RenderBrief` rewritten to header + framing + `renderEvent` list + links,
   reading the event-native thin header/stream (drops the flattened
   `instructions`/`map[string]any`). Depends on: (1); coordinates with #1406 for
   the input shape. Verifiable by: updated `prompt-first-run-brief.golden`; no
   duplicated instructions.

4. **Converge `PROMPT.md`.** Delivers: init and wake share one skeleton (header
   → how-to-work → framing → `## Context` → `## Instructions`); the
   context/instructions partition helper is shared; how-to-work + links are
   defined once and rendered init-only; the three-way branch shrinks to
   framing + slice selection. Depends on: (2), (3). Verifiable by: all goldens;
   the wake goldens no longer duplicate the standing-guidance block, and
   instructions render last in both paths.

5. **Prompt-quality polish.** Delivers: how-to-work moved to the top and worded
   as a single deduplicated block with the task id interpolated inline; `Status`
   dropped from the header; drop the stale `get_my_task`-bootstrap branch if the
   brief is now always injected on first run; consistent section headers.
   Depends on: (4). Verifiable by: goldens + a read-through.

Ordering rationale: (1) is a self-contained, well-tested unit with no callers,
so it merges risk-free. (2) and (3) each independently convert one path and can
land in either order (both depend only on (1)). (4) is the convergence and needs
both paths converted first. (5) is cosmetic cleanup on top.

## Trade-offs

**Hybrid markdown vs. keeping JSON.** The current `RenderEvent` was deliberately
built to be *byte-for-byte identical* to the `get_my_task` events array so "the
agent parses one format, not two." Moving to markdown drops that parity. We
judge the parity not worth its cost: the prompt is **read by a model**, not
parsed by code — nothing does `JSON.parse` on the bootstrap prompt — whereas
`get_my_task`'s output *is* consumed structurally. The two audiences differ, so
the two renderings should differ. The existing `RenderBrief` doc comment already
anticipates exactly this ("the two renderings are meant to diverge — this one is
free to grow a more readable form for a model reading it cold"); this proposal
cashes that in.

**Full prose vs. hybrid.** A pure-prose narrative reads smoothly for one event
but gets ambiguous across many, and it discards fields a model wants verbatim
(URLs, diff hunks, status transitions). The hybrid keeps prose *framing* around
each event and renders the opaque bits (external `details`, `data`) as JSON
verbatim rather than trying to prettify keys it can't know. Rejected pure prose
for fidelity; rejected pure JSON for readability.

**One template vs. two.** Converging means one template must branch internally
(full-vs-new slice, trailer-or-not). That's a little more conditional logic in
`PROMPT.md` than two flat branches. But the alternative is what we have — every
event-rendering change made twice, and a standing-instruction block copy-pasted
three times that has already drifted between branches. One structure with a
minimal branch is the lesser evil.

**Human timestamps vs. RFC3339.** RFC3339 is machine-canonical; a model reading
cold parses `2023-11-14 22:15 UTC` faster and the web UI already speaks it. Both
are deterministic. Minor, but it's a free readability win while we're in here.

**Render client-side (driver) vs. server-side.** We keep rendering in
`agentprompt`, driven by the driver, rather than having the server hand back a
finished prompt string. Server-side rendering would couple the C2 to agent
prompt wording; keeping it driver-side keeps prompt iteration cheap and is
consistent with where #946/#1398 put it. #1406 reshapes the *data*, not the
rendering location.

## Open Questions

- **Report/lifecycle in the init brief.** Today the brief carries only the
  to-agent slice (instruction + external). Should a first run also see recent
  `lifecycle`/`report` events for context (e.g. "sandbox failed last run"), or
  does that belong only to the full timeline? This is really a #1406
  *which-slice* question; the renderer handles all arms regardless. If they are
  included, they render under `## Context`, not `## Instructions`.
- **How-to-work on every wake?** This proposal renders the standing "how to
  work this task" guidance init-only, assuming the wake resumes a session that
  already saw it at the top of turn one. If a wake can ever start a *fresh*
  harness session (no prior context), the guidance must render there too —
  confirm against the driver's session-resume behavior.
- **Multiple new instructions on one wake.** If a wake drains more than one new
  instruction, they all render under `## Instructions` in stream order. That is
  consistent, but a burst of instructions with intervening external events loses
  the strict interleaving. The per-event timestamps preserve the true order;
  confirm that grouping-over-interleaving is the right call for the multi-
  instruction wake, or whether a single combined instruction should be preferred
  upstream.
