# Task Namespaces for Routing Rules

Issue: https://github.com/icholy/gritz/issues/1317

## Problem

The event router's create-task action only fires "when no subscribed task is
found", and that subscription check is **global** across the org. Any subscriber
anywhere suppresses creation.

Concretely, in `Router.Route` (`internal/eventrouter/eventrouter.go:168-201`) the
wake-vs-create decision is:

```go
if links := linksByOrg[orgID]; len(links) > 0 {
    // fan the event out to every subscribed task (wake or attach)
} else if rule.Create != nil {
    // create a new task
}
```

`linksByOrg` comes from `FindSubscribedLinksForOrgs(ctx, nil, key, orgIDs)`
(`eventrouter.go:163`), which matches **any** subscribed link with the same
`routing_key` in the org, with no notion of *why* the task subscribed.

### Motivating scenario

A routing rule on `github` `label_added` (condition `label equals reviewbot`,
create-task action, `wakeup` off) is meant to spawn a fresh code-review task
whenever the `reviewbot` label is applied to a PR. It never fires when the PR was
opened by another gritz task: that implementing task already holds a subscribed
link to the PR, so the label event finds a subscriber, takes the wake/attach
branch, and (because `wakeup` is off) just silently attaches to the implementing
task. No reviewer is created.

The subscription check cannot tell "a task that reviews this PR" from "the task
that implemented this PR". We want to **partition subscription matching** so the
reviewbot rule spawns its reviewer even though an unrelated task subscribes to the
same PR, while events still route to the right subscribers within each partition,
and existing behavior is unchanged for everything that doesn't opt in.

## Design

Introduce a **namespace**: a string label that partitions subscription matching.
A task runs in exactly one namespace. A routing rule operates in one namespace. The
router's subscription lookup — the "is there a subscribed task?" check and the
fan-out that follows it — is scoped to the rule's namespace. Rule *matching* is
partitioned per namespace too, so a rule in a different namespace can create its
task even when a default-namespace task subscribes to the same URL.

The default namespace is the **empty string** `""`. Everything that exists today —
every task, every rule — is in the default namespace, and a default-namespace rule
only ever sees default-namespace subscriptions (which is every subscription today).
Current behavior is therefore bit-for-bit unchanged for anyone who never sets a
namespace.

### Where the namespace lives

The namespace is a property of the **task**. It lives in one place:

```sql
ALTER TABLE tasks ADD COLUMN namespace TEXT NOT NULL DEFAULT '';
```

Links do **not** get a namespace column. A link belongs to a task, and the
subscription query already joins `task_links → tasks`
(`internal/store/sql/queries/link.sql:16-22`), so a link's namespace is simply its
task's namespace — selected onto `model.Link` as a read-only `Namespace` field so
the router can filter on it (see Router changes). This is deliberate:

- **Single source of truth.** A task's subscriptions are always in the task's
  namespace; they cannot drift.
- **The `create_link` edge case falls out for free.** When a task running in a
  non-default namespace calls `create_link`, the link is on that task, so it
  inherits the task's namespace via the join. An agent cannot create a link in a
  *different* namespace — which is exactly the constraint we want. No MCP or proto
  change to `create_link` is needed (see below).

`RoutingRule` gains a namespace field:

```go
type RoutingRule struct {
    Source     string            `json:"source,omitempty"`
    Type       string            `json:"type,omitempty"`
    Conditions []Condition       `json:"conditions,omitempty"`
    Create     *CreateTaskAction `json:"create,omitempty"`
    Wakeup     bool              `json:"wakeup,omitempty"`
    Public     bool              `json:"public,omitempty"`
    // Namespace partitions subscription matching. Empty is the default
    // namespace — the behavior every existing rule already has.
    Namespace  string            `json:"namespace,omitempty"`
}
```

`omitempty` keeps every existing rule's stored JSON in `orgs.routing_rules`
byte-identical, so no data migration of the rules column is required.

A rule's `Create` action does **not** get a separate namespace. A rule that creates
a task always creates it **in the rule's own namespace** — that is the whole point:
the reviewbot rule (namespace `reviewbot`) creates a reviewer task in the
`reviewbot` namespace, whose subscribed link is therefore scoped to `reviewbot`.

### Router changes

Two changes to `Router.Route` (`eventrouter.go:107`).

**1. Match per (org, namespace), not just per org.** Today the loop picks the first
matching rule per org and breaks (`eventrouter.go:127-149`). We change it to pick
the first matching rule *per (org, namespace)*, so rules in different namespaces do
not shadow each other:

```go
// matched is keyed by (orgID, namespace)
type nsKey struct {
    orgID     int64
    namespace string
}
matched := map[nsKey]*model.RoutingRule{}
for _, org := range orgs {
    rules := org.Rules
    if len(rules) == 0 && org.IsMember {
        rules = reg.DefaultRules() // default-namespace fallback, unchanged
    }
    for _, rule := range rules {
        if !org.IsMember && !rule.Public {
            continue
        }
        k := nsKey{org.OrgID, rule.Namespace}
        if _, done := matched[k]; done {
            continue // first match wins within each (org, namespace)
        }
        if Match(rule, input) {
            matched[k] = &rule
        }
    }
}
```

Because every existing rule has `Namespace == ""`, this reduces to the current
"first matching rule per org" behavior when no rule opts into a namespace.

**2. Scope the subscription lookup to the rule's namespace — filtering in the
loop, not per-rule queries.** We keep the **single** batch lookup
`FindSubscribedLinksForOrgs(key, orgIDs)` and extend it to return each link's
`namespace` (the task's, via the existing join). The router then filters the
returned links by the matched rule's namespace inside the wake-vs-create loop — no
extra query per matched rule:

```go
key := model.RoutingKey(input.URL)
linksByOrg, err := r.Store.FindSubscribedLinksForOrgs(ctx, nil, key, orgIDs)
// ...
for k, rule := range matched {
    // links already in hand from the single batch call; filter to this namespace.
    var links []model.Link
    for _, l := range linksByOrg[k.orgID] {
        if l.Namespace == k.namespace {
            links = append(links, l)
        }
    }
    if len(links) > 0 {
        // wake/attach — fan out only to subscribers in THIS namespace
    } else if rule.Create != nil {
        taskID, err := r.create(ctx, input, k.orgID, rule) // creates in rule.Namespace
    }
}
```

The store query is unchanged except that it now also selects the task's namespace,
so each returned link carries the namespace to filter on (no namespace predicate in
SQL — the partitioning happens in Go):

```sql
-- name: FindSubscribedLinksForOrgs :many
SELECT l.id, l.task_id, l.relevance, l.url, l.title, l.subscribe, l.created_at, l.routing_key,
       t.org_id, t.namespace                              -- + t.namespace
FROM task_links l
JOIN tasks t ON l.task_id = t.id
WHERE l.routing_key = sqlc.arg(routing_key) AND l.subscribe = TRUE AND t.archived = FALSE
  AND t.org_id = ANY(sqlc.arg(org_ids)::BIGINT[])
ORDER BY t.org_id, l.created_at DESC;
```

`model.Link` gains a `Namespace` field populated from `t.namespace` so the loop can
filter on it. Keeping one call means the routing key is looked up once regardless of
how many namespaces matched, and the namespace grouping is a cheap in-memory pass
over an already-small result set.

**Fan-out is scoped, not global.** A woken/attached event fans out only to
subscribers **within the matched rule's namespace**, never across all namespaces.
This is the coherent reading of a partition: the `reviewbot` rule's event is a
reviewbot-namespace event, and the implementing task in the default namespace is
not its audience. If a default-namespace rule *also* matches the same event, that
rule fires independently (change 1) and wakes the implementing task on its own
terms. See Trade-offs for the alternative (global fan-out) and why it is rejected.

The `create` path (`eventrouter.go:307`) stamps the namespace onto both the task
and its subscribed link — the link inherits it via the task:

```go
task := &model.Task{
    Runner:    rule.Create.Runner,
    Workspace: rule.Create.Workspace,
    Namespace: rule.Namespace, // <-- new
    // ...
}
```

### How the motivating scenario resolves

The reviewbot rule is authored with `namespace: "reviewbot"`. When the label is
applied to a PR opened by an implementing task:

1. The reviewbot rule matches under `(org, "reviewbot")`.
2. The batch lookup returns the implementing task's link (namespace `""`), but
   filtering to `reviewbot` leaves **no** subscriber for this rule.
3. The create branch runs: a reviewer task is created in namespace `reviewbot`,
   with a subscribed link scoped to `reviewbot`.

The implementing task in the default namespace is untouched. On a redelivery of the
same label event, the batch lookup returns both links; filtering to `reviewbot` now
finds the reviewer's own link and takes the wake path — so the existing per-URL
dedup still holds, but only *within* the reviewbot namespace.

### Proto surface

```proto
message Task {
  // ...
  string namespace = N; // read-only; default ""
}

message CreateTaskRequest {
  // ...
  string namespace = N; // optional; default "" (default namespace)
}

message RoutingRule {
  // ...
  string namespace = N; // optional; default "" (default namespace)
}
```

- `CreateTaskAction` gets **no** namespace field — a created task inherits the
  rule's namespace.
- `CreateLinkRequest` / `TaskLink` are **unchanged** — links inherit the task's
  namespace.
- `GetRoutingRules` / `SetRoutingRules` round-trip the new `RoutingRule.namespace`
  through the existing model converters (`RoutingRule.Proto` /
  `RoutingRuleFromProto` in `internal/model/routing_rule.go`).

### API / handler changes

- **`CreateTask`** (`internal/server/apiserver/task.go:57`) sets
  `Namespace: req.Namespace` on the new task.
- **`SetRoutingRules`** (`internal/server/apiserver/org.go:261`) already validates
  each rule via `eventrouter.DefaultSchemaRegistry.Validate`; add an optional
  namespace format check there (see Open Questions).
- **`GetTask` / task read paths** surface `namespace` for display.

### MCP tools

- **`create_link`** — no change. A link created by an agent inherits its task's
  namespace via the task join. This is the intended, safe behavior: an agent
  running in a non-default namespace cannot leak subscriptions into another
  namespace.
- **`get_my_task`** (`internal/agentmcp/xmcp.go`) — include the task's
  `namespace` in the returned task so an agent can see the partition it runs in.
- No `create_task` MCP tool exists today; nothing to change there.

### Web UI

- **Routing-rule editor** (`webui/src/components/routing-rule-form.tsx`,
  form shape in `webui/src/lib/routing-rules.ts`) — add an optional "Namespace"
  text field (empty = default). It sits alongside the `wakeup` / `public` toggles.
- **New-task form** (`webui/src/routes/tasks.new.tsx`) — add an optional
  "Namespace" field. Default empty.
- **Task detail** — display the namespace when non-empty (a small badge), so a
  task created by a namespaced rule is visibly partitioned.

### Backward compatibility

- New `tasks.namespace` column defaults to `''`; all existing rows are in the
  default namespace.
- `RoutingRule.Namespace` is `omitempty`; existing `orgs.routing_rules` JSON is
  unchanged and needs no data migration.
- Per-(org, namespace) matching reduces to per-org matching when every rule is in
  the default namespace.
- `FindSubscribedLinksForOrgs` keeps its signature and returns the same rows as
  today plus a namespace column; filtering to `""` matches every link when
  everything is in the default namespace.

Net: a deployment that never sets a namespace behaves identically to today.

## Implementation Plan

1. **Schema migration** — Delivers: `tasks.namespace TEXT NOT NULL DEFAULT ''`.
   No index: the subscription lookup keys on `routing_key` + org, and namespace
   filtering happens in Go in the router (step 4 compares `link.Namespace ==
   rule.Namespace` over the batch lookup; step 3 only adds `t.namespace` as a
   selected column via the `links → tasks` join). Nothing predicates or sorts
   tasks by `namespace` in SQL, so an index on it would carry write-side cost for
   no read benefit. Add one later only if a namespace-filtered query appears (e.g.
   a "list tasks in namespace X" view). New dbmate migration under
   `internal/store/sql/migrations/`, schema dumped to
   `internal/store/sql/schema.sql`. Depends on: nothing. Verifiable by: migration
   runs cleanly up and down; `schema.sql` regenerates.

2. **Task model + store** — Delivers: `Namespace` field on `model.Task` and its
   proto converters (`internal/model/task.go`); `CreateTask` insert query carries
   the column. Depends on: (1). Verifiable by: store round-trip unit test creating
   and reading a task with a non-default namespace.

3. **Namespace on returned links** — Delivers: `t.namespace` added to the
   `FindSubscribedLinksForOrgs` SELECT and a `Namespace` field on `model.Link` +
   the `store/link.go` scan. The query keeps its existing signature (one batch call
   keyed by org). Depends on: (1). Verifiable by: store test — a link returned for a
   task in `namespace='reviewbot'` carries that namespace, a default-namespace link
   carries `""`.

4. **Router matching + create** — Delivers: in-loop filtering of the single batch
   lookup by the matched rule's namespace (the wake-vs-create partition) and
   `create` stamping `task.Namespace = rule.Namespace`. Adds `RoutingRule.Namespace`
   to the model. Matching stays **first-match-per-org** for now — per-(org,
   namespace) matching (so a default rule and a namespaced rule matching the same
   event both fire) is deferred as a follow-up; this layer delivers only the
   subscription partitioning + create stamping. Depends on: (2), (3). Verifiable by:
   eventrouter test reproducing the motivating scenario — a default-namespace
   subscriber present, a `reviewbot` create rule (the first-matching rule) still
   creates the reviewer because the default subscriber is filtered out by the
   namespace scope in `Apply`. This is the slice that closes the issue.

5. **Proto + API handlers** — Delivers: `namespace` on `Task`, `CreateTaskRequest`,
   `RoutingRule` in `proto/gritz/v1/gritz.proto` (regenerate); `CreateTask`
   honors `req.Namespace`; `Get/SetRoutingRules` round-trip it; `get_my_task`
   exposes it. Depends on: (4). Verifiable by: handler tests — SetRoutingRules
   round-trip preserves namespace; CreateTask with a namespace persists it.

6. **Web UI** — Delivers: namespace field in the routing-rule editor and the
   new-task form; namespace badge on task detail. Depends on: (5). Verifiable by:
   rendering the editor/task views; `pnpm lint` passes.

## Trade-offs

- **Namespace on tasks vs. on links.** Putting it on links would let a single task
  hold subscriptions in multiple namespaces, but it multiplies the source of truth,
  complicates `create_link` (which namespace?), and has no motivating use. A task
  is a single unit of work in a single context, so the namespace belongs on the
  task; links inherit it. Chosen for simplicity and to make the `create_link` edge
  case trivial.

- **Scoped fan-out vs. global fan-out.** The alternative is: match per namespace,
  but when waking, fan out to subscribers in *all* namespaces. That would keep the
  implementing task in the loop for a reviewbot event. Rejected because it breaks
  the partition — a namespace would no longer be a real boundary, and events would
  leak across contexts. If a default-namespace task genuinely needs the same event,
  a default-namespace rule can match it independently (per-(org, namespace)
  matching makes that possible without any cross-namespace fan-out).

- **Per-(org, namespace) matching vs. keeping first-match-per-org.** Keeping
  first-match-per-org would mean only one rule fires per event even across
  namespaces, so a reviewbot create rule and a default wakeup rule could still
  shadow each other (order-dependent). Matching per (org, namespace) makes
  namespaces truly independent — the direct fix for "two rules in different
  namespaces matching the same event". It is a strict superset of today's behavior
  when all rules are default-namespace.

- **String namespace vs. a namespaces table.** A free-form string keeps the change
  small (one column, one rule field) and matches how `workspace`/`runner` are
  already free-form strings on tasks. A first-class namespaces table (with
  membership, display names, per-namespace config) is heavier and unjustified for
  the current need; it can be layered on later without changing the partition
  semantics.

## Open Questions

- **Namespace format / validation.** Should we constrain namespaces to a slug
  (`^[a-z0-9-]*$`) in `SetRoutingRules` / `CreateTask`, or leave them free-form like
  `workspace`? Constraining now avoids surprises (whitespace, case) later.

- **Fresh reviewer per label application.** Within the `reviewbot` namespace, the
  created reviewer subscribes to the PR, so a *second* `reviewbot` labeling wakes
  the existing reviewer instead of creating a new one. If the desired semantics are
  "a fresh reviewer every time the label is applied", the create action would need
  an option to create a **non-subscribing** link (or none). That is orthogonal to
  namespaces but worth deciding alongside this feature.

- **Do non-member / `Public` rules interact with namespaces?** A public rule can
  carry a namespace like any other; the member/non-member eligibility check
  (`eventrouter.go:141`) is unchanged. Confirm there is no desire to gate namespace
  usage by membership.

- **Surfacing / discovering namespaces in the UI.** Namespaces are implicit (any
  string a rule or task uses). Should the UI offer autocomplete from
  already-used namespaces, or is a free text field sufficient for v1?