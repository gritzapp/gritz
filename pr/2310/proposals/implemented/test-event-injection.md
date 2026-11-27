# Test Event Injection for Routing Rules

Issue: https://github.com/icholy/gritz/issues/1326

## Problem

External events are born only in the webhook handlers and pollers (`githubserver`,
`atlassianserver`). There is no way to exercise the event router
(`eventrouter.Router.Route`, `internal/eventrouter/eventrouter.go:113`) on demand:

- **Verifying a routing rule means triggering a real webhook** — a real comment, PR
  review, or Jira update, with a valid signature and matching org/installation state.
  You can't ask "if an event that looks like *this* arrived, what would my rules do?"
- **Frontend work on event rendering has no way to inject a fake event.** Iterating on
  the event timeline or routing-rules UI means waiting for a real event to land.

We author routing rules in the Web UI (`RoutingRuleForm`, backed by the `GetEventTypes`
schema registry) but have no way to *test* them there. We want to compose an event in
the Web UI — using the same event source/type/attribute schemas that back the rule
editor — fire it, and see what the routing rules do with it.

**Decision already made:** the Web UI form reuses the existing event-type schema
definitions surfaced via `GetEventTypes` (`EventTypeDef` / `AttrDef`,
`internal/eventrouter/schema.go:14`) to construct the event — not a free-form JSON
textarea.

## Design

### Overview

Add a `TestEvent` RPC that feeds a hand-composed synthetic `InputEvent` into the *same*
routing code the webhook handlers use, scoped to the caller's org. It supports two
modes:

- **Dry run (default, `OpOrgRead`)** — runs matching and the subscribed-link lookup and
  reports which rule matched, which subscribed tasks *would* be woken, and whether a
  task *would* be created. Persists nothing; touches no external system.
- **Fire (opt-in, `OpOrgWrite`)** — actually routes the event: wakes/creates real tasks
  and persists real events, no different from a webhook-born event (§3). GitHub reactions
  and other outbound side effects are deliberately *not* fired.

The keystone is that both modes share the real matcher. To make that possible without
duplicating logic, `Route` is split into a pure **plan** phase and an **apply** phase.
Dry run serializes the plan; fire applies it. There is no separate "simulation" matcher
that could drift from production behavior.

### 1. Split `Route` into plan + apply

Today `Route` (`internal/eventrouter/eventrouter.go:113-216`) interleaves matching with
side effects: it builds `matched map[int64]*model.RoutingRule`, looks up subscribed
links for the matched orgs via `FindSubscribedLinksForOrgs`, then for each org either
`attach`es (wake) or `create`s.

Extract the read-only decision into a `Plan` method that returns, per matched org, the
rule that matched. This is a pure refactor — `Route` keeps its exact behavior by
consuming the plan.

`RouteMatch` is deliberately lean: it carries only the rule-matching outcome, not the
subscription lookup or the wake-vs-create prediction. Those are cheap to derive from the
matched rule (`FindSubscribedLinksForOrgs` for the links; wake vs. create is just "links
present → wake, else `rule.Create != nil`") and are computed by whoever needs them —
`Route` when applying, the dry-run handler when building its report — rather than baked
into the match struct. Keeping the two concerns separate means `Plan` is purely "which
rule fires", which is the part the test path and the webhook path must share.

```go
// RouteMatch is the read-only result of evaluating routing rules for one org:
// the rule that matched and its position in the org's configured list. It is
// what a dry run reports and what apply consumes. The subscribed-link lookup and
// the wake-vs-create decision are derived from this by the caller, not stored
// here.
type RouteMatch struct {
    OrgID     int64
    Rule      *model.RoutingRule
    RuleIndex int // index into the org's configured rules (-1 = shipped default)
}

// Plan evaluates routing rules for the event and returns one RouteMatch per org
// whose rules matched, without any side effects and without touching links. When
// orgFilter is non-zero, only that org is evaluated (the test path scopes to
// caller.OrgID); zero preserves the webhook path's all-member-orgs behavior.
func (r *Router) Plan(ctx context.Context, input InputEvent, orgFilter int64) ([]RouteMatch, error)
```

`Route` becomes: `Plan(ctx, input, 0)` → look up subscribed links for the matched orgs
(`FindSubscribedLinksForOrgs`, unchanged) → for each org, `attach` (links present) or
`create` (matched `rule.Create != nil`) → fire `OnRouteOutcome`. The existing
`eventrouter_test.go` suite pins this equivalence.

`orgFilter` is how the test path scopes to a single org. `ListRoutingRulesForEvent`
(`internal/store/org.go`) returns *every* org the actor is a member of plus the event's
`Orgs`; a test event is explicitly about the one org the operator is looking at, so
`Plan` drops all but `orgFilter` when it is set. This keeps the simulation focused and
is the routing-side half of the permission scoping in §4.

### 2. Proto: the `TestEvent` RPC

Add to `GritzService` (`proto/gritz/v1/gritz.proto`, near the other event-routing
RPCs at lines 50-52):

```protobuf
rpc TestEvent(TestEventRequest) returns (TestEventResponse);
```

The request composes a synthetic event from the schema. Attribute values are keyed by
`AttrDef.Key` — the same keys `GetEventTypes` hands the form — so the client sends back
exactly what the schema described. The form exposes a **single value per attr** (a
multi-value UI is deliberately not offered), so `attrs` is a plain `map<string, string>`;
the handler wraps each value into the one-element slice `eventrouter.Attrs` expects. The
`"body"` and `"url"` derived attrs (`InputEvent.Attr`,
`internal/eventrouter/eventrouter.go:58`) are carried as ordinary map entries and
unpacked server-side into `Data` and `URL`.

```protobuf
message TestEventRequest {
  string source = 1;                // registered EventTypeDef.Source, e.g. "github"
  string type = 2;                  // registered EventTypeDef.Type, e.g. "issue_comment"
  map<string, string> attrs = 3;    // matchable values keyed by AttrDef.Key (incl. "body", "url")
  string description = 4;           // optional human description for the event timeline
  bool fire = 5;                    // false = dry run (default); true = fire for real
  map<string, string> details = 6;  // source-defined context persisted verbatim (see §2a)
}

message TestEventResponse {
  repeated TestEventMatch matches = 1; // one per org that matched (0 or 1 for the test path)
  bool fired = 2;                      // whether side effects were applied
}

// TestEventMatch is the per-org dry-run/fire report the handler builds from a
// RouteMatch plus a subscribed-link lookup. rule_index comes from the match; the
// wake/create fields are derived by the handler (they are not stored on
// RouteMatch).
message TestEventMatch {
  int64 org_id = 1;
  int32 rule_index = 2;      // which configured rule matched (-1 = shipped default)
  bool would_wake = 3;       // matched rule's wakeup flag
  repeated TestEventTask wake_tasks = 4; // subscribed tasks that (would) wake
  bool would_create = 5;     // a task would be / was created
  repeated int64 created_task_ids = 6;   // populated only when fired && would_create
  repeated int64 event_ids = 7;          // synthetic event rows written when fired
}

message TestEventTask {
  int64 id = 1;
  string name = 2;
}
```

The handler builds each `TestEventMatch` by taking the `RouteMatch` from `Plan` and
running the same subscribed-link lookup `Route` uses (`FindSubscribedLinksForOrgs` keyed
on `model.RoutingKey(url)`): the matched tasks become `wake_tasks`, `would_create` is
`len(links) == 0 && rule.Create != nil`, and `would_wake` is `rule.Wakeup`. This is the
"reports which rules **and** subscriptions would match" requirement — the rule side from
`Plan`, the subscription side from the shared link lookup — without pushing either into
the lean `RouteMatch`.

The handler maps the request to `eventrouter.InputEvent`:

- `Source`, `Type` copied through and validated against the registry.
- Attr `"body"` → `Data`, `"url"` → `URL`, every other key wrapped into
  `Attrs[key] = []string{value}` (single value per attr).
- `Details` copied straight through to `InputEvent.Details` (see §2a).
- `UserID` = the caller's user id; `Orgs` = `[caller.OrgID]`.
- `Meta` stays nil — it carries source-native identity for `OnRouteOutcome`, which the
  test path never invokes.

No fields beyond these are added to `InputEvent`: a fired test event is an ordinary
event (§3), so nothing marks it as synthetic.

The request is validated before routing: `(source, type)` must resolve via
`DefaultSchemaRegistry.EventTypeFor`, and every attr key must be a valid attr for that
type (`EventTypeDef.hasAttr`) — the same contract `SchemaRegistry.Validate` enforces for
rules (`internal/eventrouter/schema.go:113`). Unknown attr keys are a
`CodeInvalidArgument` error rather than a silently ignored attr, so a typo'd attr can't
masquerade as a non-match. `details` keys are *not* validated against the schema — they
are free-form source-defined context (§2a), so any key is accepted.

### 2a. Details: source-defined context, not matchable attrs

`InputEvent` carries two distinct maps, and the test event must let the operator compose
both:

- **`Attrs`** (`map[string][]string`) are the *matchable* routing dimensions the matcher
  reads and the router drops after routing. These are exactly what `GetEventTypes`
  describes (`EventTypeDef.Attrs`), so the form drives them from the schema (§6).
- **`Details`** (`map[string]string`) are *source-defined* key/value context the router
  does not interpret — it copies them verbatim into the persisted `ExternalPayload`
  (`internal/eventrouter/eventrouter.go:43-48,252,348`) for the agent and UI to render.
  Real examples: the PR review-comment code location writes `path`, `line`,
  `start_line`, `side`, `diff_hunk` into Details
  (`internal/server/githubserver/webhook.go:215-231`).

Details matter specifically to the FE-rendering motivation in the Problem section: the
event timeline renders Details (e.g. the anchored file/line + diff hunk), so exercising
that rendering with a synthetic event is impossible without being able to inject them.
Because Details are not in the schema and never affect matching, they are a **no-op for
dry-run matching** — they change nothing about which rule matches or which task wakes.
They only take effect when a real event row is written, i.e. in fire mode (§5), where
`attach`/`create` already copy `input.Details` onto the `ExternalPayload` — so **no
router change is needed**, only the handler passing them through.

Since Details are free-form and source-defined, the form renders them as structured
key/value rows rather than driving them from the schema — this is still not the rejected
"free-form JSON textarea" (§Trade-offs); it is a typed `map<string, string>`. Extending
the schema to *advertise* a type's known detail keys (an `EventTypeDef.Details` list that
pre-fills `path`/`line`/… for PR review comments) was considered and **declined** for
this feature: Details stay free-form, and the operator enters whatever keys the case
needs.

### 3. No marking — a fired test event is a real event

Fired test events are not tagged or otherwise distinguished from genuine webhook events.
That is deliberate: firing means "run the router for real with a hand-composed event", so
the event row it persists and the task it wakes or creates *are* real by construction —
there is no meaningful "synthetic" state to mark. Adding a `Test` flag to
`ExternalPayload` (and the proto/UI plumbing to badge and sweep it) buys nothing the
existing lifecycle doesn't already cover, so this proposal does **not** add one. The
router (`attach`/`create`) is used exactly as-is; fire mode requires **no** `model` or
proto changes.

**Cleanup falls out of the existing task/event lifecycle:**

- *Dry run* (the common case) persists nothing, so there is nothing to clean up.
- *Fired create* produces an ordinary task; archive or delete it like any other task, and
  its events cascade away (`events.task_id` FK `ON DELETE CASCADE`). The created task
  inherits the matched rule's `AutoArchive` unchanged — `never` when the rule sets none,
  exactly like a real routed create — so there is no test-specific archive behavior.
- *Fired wake* attaches an ordinary event to an existing task; remove it with the
  existing `DeleteEvent` RPC (`proto` line 29) if desired.

No new marking, migration, sweep job, or auto-archive special-casing is introduced.

### 4. Org scoping and permissions

- **Single-org by construction.** The synthetic event is routed only against
  `caller.OrgID` (from the JWT, `apiauth.MustCaller` — the same org binding
  `GetRoutingRules` uses, `internal/server/apiserver/org.go`). The request cannot name
  arbitrary orgs, so a test event can't probe or disrupt another org's rules or tasks.
  This is enforced via `Plan`'s `orgFilter` (§1).
- **Dry run requires `OpOrgRead`** — it is a read-only simulation, the same scope as
  `GetEventTypes` and `GetRoutingRules`.
- **Fire requires `OpOrgWrite`** — it mutates (wakes/creates tasks), matching
  `SetRoutingRules`. The read/write split naturally gates who can cause side effects:
  anyone who can view rules can simulate; only someone who can edit rules can fire.
- Because the caller is a member of `caller.OrgID`, member-rule evaluation works
  unchanged and `Rule.Public` is irrelevant (non-member/public routing is a
  cross-org concern that the single-org scoping sidesteps).

### 5. No external side effects on fire

The production GitHub router sets `OnRouteOutcome` to post an emoji reaction to the
triggering comment (`githubserver.go:170`, `reactions.go:21`). A synthetic event's URL
is operator-typed and may point at nothing — or, worse, at a real comment that shouldn't
be reacted to. The `TestEvent` fire path therefore constructs its `eventrouter.Router`
from the apiserver's `store` + `publisher` **without** `OnRouteOutcome`, exactly as the
Atlassian server already does (`atlassianserver.go:103`). Dry run never applies anything,
so it is side-effect-free by construction. Internal notifications (the pubsub `change`
that refreshes the UI) *do* fire when a real task is woken/created — that is the point of
fire mode.

### 6. Web UI: where the form lives

Routing rules live on the `/events` page (`RoutingRulesCard`, `events.index.tsx`), with
editing at `/routing/new` and `/routing/$index` via the shared `RoutingRuleForm`
(`webui/src/components/routing-rule-form.tsx`).

- **Entry point.** A "Test a routing rule" button on the `RoutingRulesCard`, next to the
  add-rule control, so the tester sits beside the rules it exercises.
- **Route.** A new `/routing/test` route hosting a `TestEventForm`.
- **Form.** `TestEventForm` fetches `getEventTypes` exactly like `RoutingRuleForm`
  (`useQuery(getEventTypes, {})`), presents the source/type selector, then renders one
  input per `AttrDef` — including `body` and `url` — using the schema's `label`, `help`,
  and `placeholder`. This reuses the attr-metadata plumbing already in
  `webui/src/lib/routing-rules.ts` (`eventTypeLabel`, the per-type attr list); it does
  not reuse the *condition* row (op + value), because a test event supplies a plain
  value per attr, not a comparison.
- **Details editor.** A separate "Details" section renders free-form key/value rows (add
  / remove, like a headers editor) feeding `TestEventRequest.details` (§2a). It is
  visually distinct from the schema-driven attr inputs above it, signaling that Details
  are source-defined context (rendered on the event, not matched) rather than routing
  attrs. This is what lets the form exercise event *rendering* (e.g. injecting a PR
  review-comment `path`/`line`/`diff_hunk` to see the timeline render the code location),
  which is a core motivation. If the schema later advertises known detail keys (§2a),
  this section pre-fills them.
- **Results.** On submit (dry run by default) a results panel renders each
  `TestEventMatch`: the matched rule (highlighted by `rule_index` back in the rules
  table), the list of tasks that would wake (linked to their task pages), and a
  would-create indicator. A "Fire for real" action re-submits with `fire=true` behind a
  confirmation, then shows the created/woken task links.
- **Deep link (nice-to-have).** A "Test this rule" button on `RoutingRuleForm`
  navigates to `/routing/test` prefilled with the rule's `source`/`type`, closing the
  author→verify loop.

The frontend calls the RPC through the existing connect-query transport
(`main.tsx` / `lib/transport.ts`) with generated `testEvent` query/mutation hooks, the
same pattern as `setRoutingRules`.

## Implementation Plan

1. **Extract `Plan` from `Route`** — Delivers: `RouteMatch`, `Router.Plan`, and `Route`
   rewritten to `Plan` + apply, with `orgFilter`. Depends on: nothing. Verifiable by:
   the existing `internal/eventrouter/eventrouter_test.go` suite passing unchanged, plus
   a new unit test asserting `Plan` returns the expected matched rule per org for match /
   no-match / ruleless-default cases with no rows written.

2. **Proto: `TestEvent` RPC + messages** — Delivers: the RPC, request/response
   (including the `details` map, §2a), and match/task/attr messages; `mise run generate`.
   Depends on: nothing. Verifiable by: generated code compiles.

3. **Backend handler — dry run** — Delivers: `TestEvent` in apiserver building the
   `InputEvent` (attrs → `Attrs`/`Data`/`URL`, `details` → `Details`), validating it
   against the registry, calling `Plan(input, caller.OrgID)`, then the shared
   subscribed-link lookup to derive `wake_tasks`/`would_create` per match, and
   serializing `matches`. `OpOrgRead`. Depends on: (1), (2). Verifiable by: a handler
   unit test with `teststore` asserting the reported rule + subscription matches for
   seeded rules/links — and that no `events`/`tasks` rows are written.

4. **Backend handler — fire mode** — Delivers: the `fire=true` branch constructing a
   no-`OnRouteOutcome` `Router`, routing the event as-is (no marking, §3), and returning
   the real task/event IDs. `OpOrgWrite`. Depends on: (3). Verifiable by: a handler test
   asserting real events/tasks are created with the composed `Details` persisted on the
   `ExternalPayload`, and no GitHub reaction path runs.

5. **Web UI — `TestEventForm`, route, entry point** — Delivers: `/routing/test`, the
   schema-driven attr form plus the free-form Details key/value editor (§2a), the results
   panel, and the `RoutingRulesCard` button; `pnpm lint` in `webui/`. Depends on: (2) for
   generated hooks, (3)/(4) for the RPC. Verifiable by: composing an event against seeded
   rules and seeing dry-run matches, then firing and seeing the Details render on the
   event.

6. **(Optional) Deep link** — Delivers: the "Test this rule" deep link from
   `RoutingRuleForm`, prefilling `/routing/test` with the rule's `source`/`type`. Depends
   on: (5). Verifiable by: prefilling the form from a rule.

Layer 1 (the `Plan` refactor) and layer 2 (proto) are independent; 3 depends on 1+2; 4
on 3; 5 on 2+3/4. Each is independently mergeable — after (3) the API is usefully
testable via `grpcurl` before any UI exists.

## Trade-offs

**Dry run and fire, not one or the other.** Dry run alone can't validate that a create
rule actually spins up a runnable task, and can't unblock end-to-end FE work on a *live*
event; fire alone is a loaded gun for a feature whose main use is safe inspection. The
read/write permission split lets the safe mode be the default and the ubiquitously
available one, while gating the mutating mode behind the same scope as editing rules.

**Refactor `Route` into plan + apply, rather than a parallel simulator.** A test that
runs different matching code than production is worse than useless — it lies. Sharing one
matcher via `Plan` guarantees the dry-run report and real firing agree, at the cost of a
mechanical refactor pinned by the existing test suite.

**Schema-driven form, not a JSON textarea** (decided upstream). Reusing `GetEventTypes`
means the tester can only compose events the router actually understands, renders the
same labels/help the rule editor shows, and validates attr keys server-side against the
same registry contract as rules. A textarea would let operators hand-craft events that
no real webhook could produce, testing fiction.

**No `OnRouteOutcome` on fired events.** Firing real GitHub reactions against
operator-typed URLs is both useless (no real comment) and dangerous (could react to the
wrong thing). Omitting the callback keeps fire mode's side effects confined to gritz's
own state. The trade-off: fire mode does not exercise the reaction path itself — that
remains only reachable via a real webhook, which is acceptable since reactions are a
thin, separately-tested outbound.

**No marking on fired events** (see §3). A fired test event is a real event by
construction, so it needs no `Test` flag, no proto/`model` change, and no bespoke cleanup
path — the existing task archive/delete and `DeleteEvent` cover it. This keeps fire mode
a zero-schema-change feature.

## Resolved Decisions

The open questions from earlier drafts were settled in review:

1. **Fire mode wakes real pre-existing tasks** (not create-only) — the wake path is a
   first-class thing to test, so a fired event wakes existing subscribed tasks just as a
   real webhook would (folded into §Overview / §5).
2. **Fired test events are not distinguished after the fact** — they are real events and
   appear in the normal feed like any other; no `test` flag or filter (§3).
3. **The form exposes a single value per attr** — `attrs` is a `map<string, string>`; no
   multi-value UI (§2, §6).
4. **No rate-limiting or audit trail for fire mode** — out of scope for this feature.
5. **A fired create task uses the rule's `AutoArchive` unchanged** — `never` when the
   rule sets none, exactly like a real routed create; no test-specific archive (§3).
6. **The schema does not advertise known detail keys** — Details stay free-form; the
   operator enters whatever keys the case needs (§2a).