# Enrich GitHub PR review-comment events with code location

Issue: https://github.com/icholy/gritz/issues/1306

## Problem

When a GitHub PR review comment wakes a task, the event the agent sees carries the
comment body and URL but not the file path or line number(s) the comment is anchored
to. To find the code under discussion the agent has to fetch the comment via the GitHub
API or guess the location from the body — a wasted round trip on every review-comment
wake, and a wrong guess whenever the body doesn't name the file.

GitHub's `pull_request_review_comment` webhook payload already carries `path`,
`line`/`original_line`, `start_line`, `side`, and `diff_hunk`. Today
`toInputEvent` (`internal/server/githubserver/webhook.go`) extracts only the comment
body (into `Data`), the comment URL, and a short description string — the location
fields are dropped in the webhook-to-event conversion. The web UI timeline is location-blind
for the same reason.

We already have the data at the webhook boundary; we're just discarding it.

## Background: what reaches the agent today

The pipeline from webhook to agent is:

1. `toInputEvent` builds an `eventrouter.InputEvent`. For a review comment it sets
   `Description`, `Data` (the body), `URL` (the comment HTML URL), `Attrs`
   (routing-only), and `Meta` (`GitHubMeta`, source-native identity).
2. `Router.Route` → `Router.attach` (`internal/eventrouter/eventrouter.go`) copies **only**
   `Description`, `URL`, and `Data` into a `model.ExternalPayload` and persists it as an
   `external` event. `Attrs` and `Meta` are routing/identity concerns and are **not
   persisted** — they never reach the agent.
3. The persisted event's payload is the typed `gritzv1.ExternalPayload`
   (`{description, url, data}`), stored as JSONB in the `events.payload` column.
4. `get_my_task` (`internal/agentmcp/xmcp.go`, `taskDetailsToMap`) marshals each event
   with `protojson` and hands the raw JSON to the agent. **Whatever fields exist on the
   `ExternalPayload` proto are surfaced to the agent for free.**
5. The web UI (`webui/src/components/task-timeline.tsx`, `ExternalRow`) renders
   `description`, `data`, and `url` from the same proto.

So the only channel that survives to both the agent and the UI is the persisted
`ExternalPayload`. Location information must live there to be useful — stuffing it into
`Meta` would drop it at step 2.

## Design

> **Revised after review.** An earlier draft added a structured, per-source
> `CodeLocation` message to `ExternalPayload`. Review feedback (icholy on #1307) was that
> this couples the shared payload to specific event types — the payload schema should not
> grow a GitHub-shaped message. This version replaces it with a **generic, source-defined
> details bag**: `ExternalPayload` learns nothing about "code location"; it just carries
> an opaque `map<string, string>` of extra context, and the GitHub extractor is the only
> code that knows the `path`/`line`/… keys. (The map is named `details`, not `attributes`,
> to stay well clear of the router's routing-only `Attrs` — see Where it lives.)

Add an optional, generic **details** map to `ExternalPayload`, populate it in the
GitHub webhook extractor for review comments, and render it in the timeline. Because the
agent and UI both read the `ExternalPayload` proto directly, one map reaches both with no
per-consumer plumbing, and the payload stays source-agnostic.

### Which details to keep

The map is opaque to the schema, but the GitHub extractor and the consumers agree on a
key convention. The minimum useful anchor is `path` + `line`; we also set `side`,
`start_line`, and `diff_hunk`:

| Key          | Source (go-github `PullRequestComment`) | Why keep it |
|--------------|------------------------------------------|-------------|
| `path`       | `Path`                                   | The file — required to locate the code at all. |
| `line`       | `Line`, falling back to `OriginalLine`   | The line the comment anchors to. GitHub sends `line == nil` when the comment is on an outdated diff; `original_line` still points at the line in the reviewed commit, so it's the right fallback for populating the key. |
| `start_line` | `StartLine` (omitted when single-line)   | Multi-line comments anchor to a range; without the start the agent only sees the last line. |
| `side`       | `Side` (`LEFT`/`RIGHT`)                   | Disambiguates a deletion (old side) from an addition/context (new side) on the same displayed line. |
| `diff_hunk`  | `DiffHunk`                               | The actual diff context the comment is attached to — the single biggest token-saver: the agent sees the exact code without fetching anything. |

All values are strings (`line` → `"42"`; unset keys are simply absent). `diff_hunk` can be
large for a wide multi-line range; see Open Questions on capping.

**`line`/`start_line` are capture-time hints, `diff_hunk` is the durable anchor.** The
line numbers are correct against the diff at the instant the webhook arrived, but the
stored event is never updated — as soon as the branch moves, a line that was current can
become stale, and vice versa. We therefore record no freshness claim about the numbers
(an earlier draft had an `outdated` key; it's dropped — a boolean captured once would tell
a later reader exactly the wrong thing as the branch evolves). The guidance for the agent,
carried in the extractor doc and worth surfacing in the UI, is: treat `line`/`start_line`
as hints for a quick jump, and locate the code by matching the **`diff_hunk` content**,
which is self-describing and survives line renumbering.

We deliberately do **not** set `commit_id`/`original_commit_id`, `position`,
`start_side`, or `subject_type` in the first cut — they add weight without clearly paying
for themselves. `commit_id` is a candidate for a follow-up (see Open Questions). Because
the mechanism is a generic map, adding a key later is a one-line change with no schema
churn.

### Where it lives on the event

A generic string map on `ExternalPayload`.

`proto/gritz/v1/gritz.proto`:

```protobuf
message ExternalPayload {
  string description = 1;
  string url = 2;
  string data = 3;
  // details carries optional, source-defined key/value context for consumers
  // (the agent via get_my_task, the web UI timeline). The payload does not
  // interpret it: GitHub review comments populate path/line/side/diff_hunk; other
  // sources may set their own keys or none. Distinct from the router's matchable
  // Attrs, which are not persisted.
  map<string, string> details = 4;
}
```

The Go mirror in `internal/model/event.go`:

```go
type ExternalPayload struct {
    Description string            `json:"description"`
    URL         string            `json:"url"`
    Data        string            `json:"data"`
    Details     map[string]string `json:"details,omitempty"`
}
```

`omitempty` means existing JSONB rows (which have no `details` key) round-trip
unchanged. `SetPayloadProto` / `EventPayloadFromProto` copy the map through.

**Why a generic map, and not the alternatives:**

- **Not a structured `CodeLocation` message** (the earlier draft) — that bakes a
  GitHub-shaped, code-review-specific type into the payload every external event shares.
  The generic map keeps `ExternalPayload` free of per-source knowledge: the meaning of
  `path`/`line` lives only in the GitHub extractor (producer) and a light UI convention,
  not in the wire schema.
- **Not "another URL"** — the review-comment `url` we already store *is* a deep link: it
  anchors to the exact diff line for a human who clicks it. A second URL would duplicate
  that and still couldn't carry a machine-readable line, side, or the diff hunk. The gap
  is *consumable* location data, which the details map fills while the existing `url`
  keeps serving the human deep-link.
- **Not `Data`** — `Data` is the comment body verbatim; agent and UI treat it as the
  human's message. Prepending `path:line` pollutes the body, is lossy (no room for a diff
  hunk), and forces every consumer to parse it back out.
- **Not (only) `Description`** — `Description` is a one-line human summary. We *do* fold
  the path and line into it as a cheap readability win (below), but it can't carry a diff
  hunk or machine-readable keys. Structured-ish context belongs in the map.
- **Distinct from routing `Attrs` — and named to stay that way** — the router already has
  `Attrs map[string][]string`, but those are matchable dimensions consumed by the matcher
  and **dropped after routing** (see the task's "keep routing separate" constraint). The
  new `details` map is persisted payload the router does not interpret; it is not a
  routing dimension, so nothing here adds `path`/`diff_hunk` to the rule surface. The name
  is deliberately `details`, not `attributes`: `InputEvent.Attributes` sitting beside
  `InputEvent.Attrs` — one persisted, one routing-only-and-dropped — would be too easy to
  confuse. `Details` vs `Attrs` reads as two clearly different things.

### Carrying it through the router

`ExternalPayload` is built inside `internal/eventrouter/eventrouter.go`, which today reads
only three fields off `InputEvent`. `InputEvent` therefore needs to carry the map too —
`Meta` won't do, because the router drops it. Add a field:

```go
// eventrouter.InputEvent

// Details is source-defined key/value context copied verbatim into the
// persisted ExternalPayload for consumers (agent, UI). It is distinct from
// Attrs: Attrs are matchable routing dimensions the matcher reads and the
// router drops after routing; Details is persisted payload the router does
// not interpret.
Details map[string]string
```

copied into the payload as:

```go
Payload: &model.ExternalPayload{
    Description: input.Description,
    URL:         input.URL,
    Data:        input.Data,
    Details:     input.Details,
},
```

The router forwards it the same way it forwards `Data`, without interpreting it. The name
`Details` (not `Attributes`) keeps it visually distinct from the routing-only `Attrs`
right beside it on `InputEvent`.

**There are two `ExternalPayload` construction sites, and both must copy `Details`:**

- `Router.attach` (~`eventrouter.go:242`) — the **wake path**: an event routed to an
  already-subscribed task.
- `Router.create` (~`eventrouter.go:337`) — the **rule-created-task path**: the trigger
  event emitted first on the timeline of a task a routing rule just created.

Miss the second and code location silently vanishes whenever a rule spins up a fresh task
from a review comment — exactly the case where the agent has the least context. The router
slice's test must cover **both** paths (see Implementation Plan).

### GitHub extractor

In the `*github.PullRequestReviewCommentEvent` arm of `toInputEvent`, build the map from
the comment and fold `path:line` into the description:

```go
c := event.Comment
line := c.GetLine()
if line == 0 { // GitHub sends a null line for comments on an outdated diff
    line = c.GetOriginalLine() // fall back, but record no freshness claim
}
details := map[string]string{
    "path": c.GetPath(),
    "line": strconv.Itoa(line),
}
if s := c.GetStartLine(); s != 0 {
    details["start_line"] = strconv.Itoa(s)
}
if s := c.GetSide(); s != "" {
    details["side"] = s
}
if h := c.GetDiffHunk(); h != "" {
    details["diff_hunk"] = h
}

description := fmt.Sprintf("%s reviewed PR #%d", login, number)
if path := c.GetPath(); path != "" {
    description = fmt.Sprintf("%s reviewed PR #%d (%s:%d)", login, number, path, line)
}
// ... InputEvent{ ..., Description: description, Details: details }
```

The description enrichment means even the existing timeline row and the existing `data`
channel immediately read better, before any UI change lands.

### Web UI

`ExternalPayload.details` flows to the generated TS client automatically. The timeline
needs three small changes:

1. `webui/src/lib/timeline.ts` — add an optional `details` map to the `external`
   `TimelineItem` variant and populate it in `eventsToTimeline` from the proto.
2. `webui/src/components/task-timeline.tsx` (`ExternalRow`) — when `details.path` is
   present, render a monospace `path:line` chip (linking to `item.url`, which already
   deep-links to the comment) between the description and the body. Optionally render
   `diff_hunk` in a collapsed `<pre>` — since the numbers are only capture-time hints, the
   hunk is the more reliable thing to show. The UI applies a light convention over
   well-known keys; the schema stays generic.
3. Run `pnpm lint` in `webui/`.

### PR review events (`pull_request_review`) and inline comments

No change needed. When a reviewer submits a review with inline comments, GitHub delivers
one `pull_request_review` (`submitted`) event whose body is the **summary** — it carries
no `path`/`line` — plus a separate `pull_request_review_comment` (`created`) event for
**each** inline comment, each carrying its own location. Enriching the review-comment arm
therefore already covers the inline-comment case end to end; the review-summary event has
no location to enrich.

### Atlassian / Jira

Not applicable today, and the generic map means no schema decision is forced. The
Atlassian integration (`internal/server/atlassianserver/webhook.go`) produces
`comment_created` and `label_added` events. Jira is an issue tracker: its comment payload
(`internal/x/atlassian/webhook.go`, `Comment{ID, Body, Author}`) has no file/line anchor,
and there is no Bitbucket integration in the tree. If a code-review source (Bitbucket PR
comments) is added later, its extractor simply sets the same `path`/`line`/… keys on the
same `details` map — no proto or model change.

### Backward compatibility

- **Stored events**: `events.payload` is schemaless JSONB; no migration. Existing rows
  have no `details` key, so the `omitempty` Go field and the unset proto field leave
  them reading exactly as before.
- **Proto**: adding a `map` field 4 to `ExternalPayload` is additive; older clients ignore
  the unknown field.
- **Agent / UI**: both already tolerate an absent field — `get_my_task` omits an empty map
  from the JSON, and `ExternalRow` guards on `details?.path` being present.

### Out of scope: routing attrs

This proposal is about the payload the agent consumes, not new rule conditions. Adding a
`path` routing dimension (so a rule could match comments on a given file) is a separate
concern and is deferred. It would be a cheap addition to the routing `Attrs` if desired
later, but it changes the routing surface and should be proposed on its own. The persisted
`details` map is intentionally kept distinct from routing `Attrs` for exactly this
reason.

## Implementation Plan

1. **Proto + model: `details` map** — Delivers: the `ExternalPayload.details`
   map field, regenerated proto, and the Go `map[string]string` mirror wired through
   `SetPayloadProto` / `EventPayloadFromProto` with the `omitempty` JSON tag. Depends on:
   nothing. Verifiable by: `ExternalPayload` proto↔model round-trip unit tests, including
   an old-shape payload (no `details`) unmarshalling cleanly.
2. **Router plumbing** — Delivers: `InputEvent.Details` and its copy into the persisted
   `ExternalPayload` at **both** construction sites — `Router.attach` (wake path) and
   `Router.create` (rule-created-task path). Depends on: (1). Verifiable by: `eventrouter`
   tests that route an `InputEvent` with `Details` down each path and assert the
   persisted event carries them — including the `create` path, so a rule-created task
   doesn't silently drop the location.
3. **GitHub extractor** — Delivers: `toInputEvent` populating `Details` for
   `pull_request_review_comment` (path, line with the `original_line` fallback, start_line,
   side, diff_hunk) and folding `path:line` into the description. Depends on: (2).
   Verifiable by: a webhook table test asserting the built `InputEvent` for a sample
   review-comment payload, covering both the current-diff case (`line` set) and the
   null-`line` case where the key is populated from `original_line`.
4. **Web UI** — Delivers: the `details` map in `timeline.ts` and the `path:line` chip
   (plus optional collapsed diff-hunk) in `ExternalRow`. Depends on: (1). Verifiable by:
   rendering the timeline against an event with `details`, and `pnpm lint`.

## Trade-offs

- **Generic details map vs. a structured per-source message.** Chosen (after review): a
  generic `map<string, string>`. It keeps `ExternalPayload` free of GitHub/code-review
  specifics — the shared payload is what every external event uses, so it shouldn't grow a
  type only review comments populate. Cost: values are stringly-typed and the key set is a
  convention rather than a schema, so producer and consumers must agree on names; the
  consumer does a little more work than reading a typed field.
- **Details map vs. "another URL".** Chosen: the map. The existing `url` already
  deep-links a human to the exact line; a second URL can't carry a machine-readable
  line/side or the diff hunk, which is precisely the consumable data the agent needs.
- **One generic map vs. the string channels (`Data`/`Description`).** Chosen: the map,
  with a `path:line` fold into `Description` as a hedge. The map keeps the comment body
  clean and carries the diff hunk without swamping `data`, while the description fold
  improves the human line even for consumers that never read the map.
- **Include `diff_hunk` vs. not.** Chosen: include it — the biggest single saver, giving
  the agent the anchored code with zero API calls. Cost: it can be sizeable; it is optional
  and only present on review comments, and can be capped (Open Questions).

## Open Questions

- **Cap `diff_hunk`?** A comment on a wide multi-line range can produce a long hunk. Do we
  store it whole, or truncate to the last N lines around the anchor? Leaning whole in the
  first cut, revisit if payloads get large.
- **Carry the commit SHA?** A `commit_id` key would let the agent `git show` the exact
  reviewed revision — a durable anchor that pins the line numbers to the commit they were
  captured against, unlike the bare `line`. Cheap to add later given the generic map;
  deferred until `diff_hunk`-based location proves insufficient in practice.