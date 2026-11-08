# Inject the Full Task Brief Into the First-Run Prompt

Issue: https://github.com/icholy/gritz/issues/1398

## Problem

The wake path no longer depends on a tool call: the driver drains
instruction + external **events** and injects them straight into the wake prompt
(`proposals/draft/wake-prompt-event-injection.md`, shipped as #946 L1–L4, via
`internal/agent/agentprompt`). But the **first run** still opens with a bootstrap
instruction that tells the agent to go fetch its own context:

```
Use gritz:get_my_task to fetch your task instructions and execute them.
...
```

(`internal/agent/agentprompt/PROMPT.md`, the `{{- else -}}` branch of the
`Started` conditional.)

So the very first turn of every task pays a `get_my_task` round-trip just to
learn what it was asked to do — and it inherits the exact reliability hole #946
set out to close on the wake path: the bootstrap only works if the prompt nags
the model into calling the tool and the model complies. A harness that renders
the `<invoke>` block as literal text and exits leaves a task that never learned
its own instructions.

The obvious fix — reuse the event-only injection the wake path already has —
**loses information**. `get_my_task` (served by `taskDetailsToMap` in
`internal/agentmcp/xmcp.go`) returns more than events: **id, name, status,
workspace, namespace, url, instructions, links**, and events. Injecting only the
events would drop name/status/workspace/namespace/url/links, so dropping the
`get_my_task` instruction would not be lossless.

This proposal renders the **full brief** — every piece of context
`get_my_task` carries — into the first-run prompt, so the bootstrap instruction
can be dropped without losing a field. It completes the original #946 vision (a
C2-rendered task brief) for the one path that still pulls its context instead of
receiving it.

## Design

### What "full brief" means: the fields to preserve

`get_my_task` renders `taskDetailsToMap(resp)` through
`mcpx.JSONResult` — i.e. `json.MarshalIndent(taskDetailsToMap(resp), "", "  ")`.
The map (`internal/agentmcp/xmcp.go:157`) is:

```go
map[string]any{
    "id":           resp.Task.Id,
    "name":         resp.Task.Name,
    "status":       resp.Task.Status.String(),
    "workspace":    resp.Task.Workspace,
    "namespace":    resp.Task.Namespace,
    "url":          resp.Task.Url,
    "instructions": instructions, // projected from instruction events
    "links":        links,
    "events":       events,
}
```

with `instructions`, `links`, and `events` each `protojson`-marshaled with
`Indent: "  "` (`instructions` is the `GetInstruction()` payload projected out of
the instruction events; `links` and `events` are the raw messages).

The invariant the first-run brief must hold is **losslessness of information**:
it must carry every one of these fields, so that an agent handed the brief knows
everything it would have learned by calling `get_my_task`. That is a constraint
on *content*, not on *format* — the brief does **not** have to be byte-identical
to the tool's JSON. The first cut renders the same structured shape (it is the
cheapest lossless rendering and the model already parses it), but the brief is
free to grow a more readable form over time (see [Drift is
intended](#drift-is-intended-not-a-hazard)).

### The duplicated brief renderer

Per the settled design, the mapping is **duplicated** into `agentprompt` rather
than shared with `agentmcp`. `agentprompt` gains a self-contained brief renderer:

```go
// RenderBrief renders a task's full brief for injection into the first-run
// prompt. It deliberately DUPLICATES the field set agentmcp.taskDetailsToMap
// exposes (id, name, status, workspace, namespace, url, instructions, links,
// events) rather than sharing it: agentprompt depends only on the proto package
// to avoid an import cycle (see the package doc), and the two renderings are
// meant to diverge — this one is free to grow a more readable form for a model
// reading it cold, while get_my_task is free to be reshaped for its own callers.
func RenderBrief(resp *gritzv1.GetTaskDetailsResponse) (string, error)
```

The initial implementation builds the same `map[string]any` and marshals it the
same way `taskDetailsToMap` does (`protojson` + `Indent: "  "` for the nested
messages, then `json.MarshalIndent`), so v1 of the brief is byte-for-byte the
`get_my_task` shape. It is registered as a `RenderBrief` template func alongside
the existing `RenderEvent` (`internal/agent/agentprompt/agentprompt.go:40`).

Why duplicate instead of share? Two independent reasons, and the second is the
important one:

1. **Dependency direction.** `agentprompt`'s package doc is explicit: it "takes
   its inputs as parameters so it depends only on the proto package and not on
   internal/agent (avoiding an import cycle)." Importing `agentmcp` (which pulls
   in the MCP SDK, auth, and the client) to reuse one unexported helper would
   invert that dependency direction and drag a heavy surface into a package whose
   whole point is to be a thin, self-contained renderer.

2. **The two consumers want different things.** `get_my_task`'s output is
   consumed by an agent making a deliberate mid-run *pull* ("what's my current
   state?"); the first-run brief is *pushed* into a cold prompt and wants to read
   as naturally as possible to a model that has no prior context. These two
   pressures point in different directions and will pull the renderings apart on
   purpose — see below.

### Drift is intended, not a hazard

An earlier draft treated the duplication's drift as a risk to be pinned shut with
a byte-equality parity test. That is the wrong frame. The two renderings are
**meant to drift**:

- The **brief** will get *more readable* over time — prose framing, ordered
  sections, dropped noise — because it is prompt text a model reads cold. That
  evolution should happen freely in `agentprompt` without being chained to the
  tool's wire shape.
- **`get_my_task`** will itself be *reshaped* to reflect the current design
  (events-first) rather than continuing to serve as an adapter to the old
  task-shaped JSON. That evolution should happen freely in `agentmcp` without
  being chained to the prompt.

A parity test would actively obstruct both: every readability improvement to the
brief or cleanup of the tool would trip a red test whose only message is "these
two are no longer identical" — precisely the state we *want* them to reach. So
this proposal **does not add an equivalence test**, and it does not add the
exported `agentmcp` wrapper an equivalence test would have needed.

The one property that does matter — that the brief stays **lossless** — is
guarded directly and cheaply, without coupling to the tool: the `agentprompt`
golden test (`internal/agent/agentprompt/testdata/prompt-first-run.golden`)
renders a field-complete fixture, and the driver test asserts the first-run
prompt contains the task's name, url, and instruction text and no longer contains
the `get_my_task` bootstrap line. If a future edit drops a field from the brief,
the golden diff shows it and the driver assertion can pin the fields that must
survive. This checks the brief against *its own contract* (carry the task's
context), not against the tool — so it keeps holding as the two drift apart.

### The driver fetch change

The first-run brief needs task + links + events, which is exactly
`GetTaskDetails` — and the driver already holds the client (`d.Client`) and
already calls this shape of RPC. `runAgent` (`internal/agent/driver.go:147`)
gains a first-run-only fetch:

```go
var details *gritzv1.GetTaskDetailsResponse
if !cfg.Started {
    details, err = d.Client.GetTaskDetails(ctx, &gritzv1.GetTaskDetailsRequest{Id: d.TaskID})
    if err != nil {
        return fmt.Errorf("failed to fetch task brief: %w", err)
    }
}
```

This runs **only** when `!cfg.Started`; a wake run leaves `details` nil and is
completely unchanged. The server already filters `GetTaskDetails` events to
instruction + external (`internal/server/apiserver/task.go:205`), so the brief's
`events`/`instructions` carry the same arms the tool does — no client-side
filtering needed.

The existing `drainEvents` call still runs on the first run too, unchanged: it
seeds `NextEventToken` to the tail so the *next* wake injects only genuinely new
events. So a first run makes two reads — `GetTaskDetails` (for the brief) and the
token-seeding `drainEvents` (which the first run already does today). They serve
different jobs (full brief vs. cursor seed) and neither subsumes the other; the
[Trade-offs](#trade-offs) weigh collapsing them.

### The `Options` extension

`agentprompt.Options` (`internal/agent/agentprompt/agentprompt.go:44`) gains one
field carrying the brief input:

```go
type Options struct {
    Started bool
    Prompt  string
    Events  []*gritzv1.Event

    // TaskDetails is the full task brief rendered into the first-run prompt in
    // place of the get_my_task bootstrap instruction. It is nil on wake runs
    // (Started == true), where the wake branch renders Events instead.
    TaskDetails *gritzv1.GetTaskDetailsResponse
}
```

The driver passes it through on the first run:

```go
prompt, err := agentprompt.Render(agentprompt.Options{
    Started:     cfg.Started,
    Prompt:      cfg.Prompt,
    Events:      events,
    TaskDetails: details, // nil on wake
})
```

`Render` is otherwise unchanged — it still just executes the template against
`opts`.

### The first-run `PROMPT.md` branch

The `{{- else -}}` (first-run) branch of `PROMPT.md` swaps the "call
`get_my_task`" instruction for the rendered brief. The surrounding operational
guidance the agent still needs — how to respond on external platforms, the
`create_link`/`subscribe` convention, the `(task {id})` suffix, "your text
responses are not visible" — is retained; only the *fetch-your-own-context*
sentences go away, because the context is now inline:

```
Here is your task brief:

{{ RenderBrief .TaskDetails }}

If the task does not have a name, use gritz:update_my_task to set one.

Each instruction has a 'text' field with the task and an optional 'url' field with the source URL.
If you have questions, problems, or take no action, respond on the platform from the most recent instruction or event url.
When responding on external platforms, always suffix your message with (task {id}) with your task id.

When creating links with gritz:create_link, ALWAYS set subscribe=true for resources you create ...
When done, use gritz:create_link for any URLs you created (PRs, issues, etc).
Always use web URLs that users can visit, not API URLs.
Use gritz:report to log important observations.
If you need to re-check for updates mid-run, call gritz:get_my_task.

Your text responses are NOT visible to users - only tool calls matter.
```

The `Started` / `Events` wake branches are untouched. The golden files under
`internal/agent/agentprompt/testdata/` are updated: `prompt-first-run.golden`
now shows the rendered brief, and a new fixture covers a first run whose brief
carries instructions/links/events.

### `get_my_task` stays

The tool is unchanged and still registered (`internal/agentmcp/xmcp.go:47`).
Dropping the *instruction to call it on the first run* is not the same as
removing the tool. It remains the mid-run refresh: a long-running agent asking
"did anything change while I was working?" pulls the current brief on demand. The
first-run prompt even points at it for exactly that (the trailing "If you need to
re-check for updates mid-run, call gritz:get_my_task" line). What goes away is
the *dependency* on that call for the agent to learn its task at all — the same
reliability win #946 delivered for the wake path, now on the first run.

Because the brief no longer has to mirror the tool, `get_my_task` is also freed
to evolve on its own — e.g. to be reshaped so it reflects the events-first design
directly instead of adapting to the old task-shaped JSON. That rework is out of
scope here; this proposal only stops making the first-run prompt *depend* on the
tool's current shape.

## Implementation Plan

1. **`agentprompt.RenderBrief` + `Options.TaskDetails`** — Delivers: the
   duplicated brief renderer in `internal/agent/agentprompt` (carrying every
   field `taskDetailsToMap` exposes: id/name/status/workspace/namespace/url/
   instructions/links/events), registered as a `RenderBrief` template func, plus
   the nil-safe `TaskDetails` field on `Options`. Depends on: nothing (pure
   addition; nothing renders it yet). Verifiable by: a unit test calling
   `RenderBrief` on a field-complete fixture `GetTaskDetailsResponse` and
   asserting every field is present in the output (a losslessness check against
   the brief's own contract — not an equivalence check against `get_my_task`).

2. **First-run branch renders the brief** — Delivers: the `PROMPT.md` first-run
   branch change (brief in place of the `get_my_task` bootstrap instruction,
   operational guidance retained) and the updated/added golden files. Depends on:
   (1). Verifiable by: the `agentprompt` golden tests — `prompt-first-run.golden`
   now contains the rendered brief; a new golden covers a brief with
   instructions/links/events. The wake goldens are unchanged.

3. **Driver fetches and injects the brief** — Delivers: the first-run-only
   `GetTaskDetails` fetch in `runAgent` and passing `details` through
   `Options.TaskDetails`; wake runs pass nil and are unchanged. Depends on: (1),
   (2). Verifiable by: a driver test asserting the first-run prompt contains the
   fetched brief's fields (name, url, instruction text) and no longer contains
   "Use gritz:get_my_task to fetch your task instructions", while the wake prompt
   is byte-for-byte what it was before.

Layer 1 is the self-contained foundation (dead code plus its own unit test, safe
to merge alone); layers 2 and 3 wire it into the template and the driver. The
behavior change lands only when 2 and 3 merge.

## Trade-offs

**Duplicate the mapping vs. share `taskDetailsToMap`.** Chosen: duplicate
(settled in #1398), and — per the follow-up direction — *without* a parity test
pinning the two together. Sharing would keep one source of truth but force
`agentprompt` (deliberately dependency-minimal to avoid an import cycle) to
import `agentmcp`'s heavy MCP/auth/client surface, and, more fundamentally, would
fuse two renderings whose consumers pull in different directions. The brief is
prompt text meant to get more readable for a cold model; `get_my_task` is a tool
result meant to be reshaped toward the events-first design. Coupling them — by
shared code or by an equivalence test — would make each evolution fight the
other. The residual cost of duplication is a second place to edit when a *new
required field* appears; the brief's own golden/losslessness test catches an
accidental omission there without demanding the two stay identical.

**No equivalence test vs. a byte-equality parity guard.** An earlier draft
proposed asserting `RenderBrief` is byte-identical to `get_my_task`. Rejected:
the two are *expected* to drift (readability work on the brief, a redesign of the
tool), so an equality test would go red on exactly the changes we want and force
churn to keep two intentionally-diverging things in lockstep. Instead the brief
is guarded against its own contract — losslessness — by the golden and driver
tests. Miss risk: a *new* field added to the task could be added to `get_my_task`
and forgotten in the brief, with no cross-check to catch it. Accepted, because the
brief's job is to be lossless *for the agent's needs*, and a genuinely
agent-relevant new field would be noticed when the brief is exercised; a
low-value field diverging is exactly the drift we are opting into.

**Two first-run reads (`GetTaskDetails` + token-seeding `drainEvents`) vs. one.**
The first run now fetches the brief *and* seeds the event cursor. We could try to
collapse them — e.g. build the brief from the `drainEvents` page — but that page
carries only instruction + external events, not the task row (name/status/
workspace/namespace/url) or links, so it cannot produce the full brief. Going the
other way (derive the seed token from `GetTaskDetails`) fails too: `GetTaskDetails`
returns no pagination token. The two calls answer different questions; keeping
both is simpler than a merged endpoint and the first-run cost is one extra read
on a path that already makes several.

**Backward compatibility.** The change is confined to the first-run prompt text.
`get_my_task` is unchanged, so an agent (or a harness quirk) that still calls it
on the first run gets the same answer it always did — the brief is now redundant
for that agent, not conflicting. Wake runs are byte-for-byte unchanged (the wake
branch, `Events`, and `drainEvents` are untouched; `TaskDetails` is nil there).
A container recreate resets `Started` to false and simply re-renders the brief on
the fresh first run. There is no schema, proto, or API change — `GetTaskDetails`
already exists and is already called elsewhere.

## Open Questions

- **Reshape `get_my_task` in a follow-up.** Now that the first-run prompt no
  longer depends on the tool's exact shape, `get_my_task` can be reworked to
  reflect the events-first design directly instead of adapting to the old
  task-shaped JSON (`taskDetailsToMap`). That is deliberately out of scope for
  this proposal but is the natural next step this change unblocks.

- **How far to push brief readability, and when.** v1 renders the same structured
  JSON shape for minimal cost. A more readable prose/sectioned brief is the
  motivating future work — worth deciding whether that lands as a fast follow or
  waits until there is evidence the JSON blob hurts first-run quality.

- **Should the first-run brief also carry pending wake events?** On the first run
  `drainEvents` returns the current tail's events (used only to seed the token
  today). The brief's `events` field already includes instruction + external
  events from `GetTaskDetails`, so they are covered — but if a new external event
  lands between the `GetTaskDetails` read and the token seed, it is delivered on
  the next wake, not the first run. This matches the existing first-run/wake
  split and is left as-is; flagged only so the overlap is deliberate.

- **Trim the retained operational guidance?** The first-run branch keeps the
  external-platform / `create_link` / `report` guidance. Some of it may be better
  as static system-prompt content than per-run prompt text, but moving it is
  orthogonal to this change and out of scope.