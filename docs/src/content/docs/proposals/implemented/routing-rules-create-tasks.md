---
title: "Routing Rules That Create Tasks"
description: "Routing rules can only wake existing tasks via subscribed links, so an external event cannot create a new task."
---

Issue: https://github.com/icholy/gritz/issues/717

## Problem

Routing rules today only ever *wake existing tasks*. The webhook flow looks up the commenter's gritz user, calls `Router.Route`, and that function derives candidate orgs implicitly from the set of subscribed links that already match the event URL (`internal/eventrouter/eventrouter.go:42-82`):

```go
linksByOrg, err := r.find(ctx, input)   // links matching the event URL
if err != nil {
    return 0, err
}
if len(linksByOrg) == 0 {
    return 0, nil                        // ← no task exists → give up here
}
```

When no link matches, the function returns early — rules are never consulted, no org is considered, no task can be created. There is no path for "an event arrived, no task exists for it yet, but an org has a rule that says: when this kind of event arrives, spin one up".

This proposal reworks routing so that an org's rules can opt into creating a task on first contact, with subsequent events for the same resource waking the created task via the existing path.

## Design

### Overview

1. Load rules for every org the commenter is a member of in one query (`ListRoutingRulesForUser`). Routing then proceeds per org even when no link covers the URL.
2. Extend `model.RoutingRule` with an embedded create-task action carrying `workspace`, `runner`, and `prompt`.
3. For each org, pick the **first matching rule** (rule ordering is significant). Drop orgs with no match. Then look up subscribed links — but only for orgs whose rule matched (`FindSubscribedLinksForOrgs`).
4. If the org has a subscribed link covering the URL, wake (existing `attach` path). Otherwise, if the matched rule has the create action, create the task and its subscribed link in a single transaction. Sequential redeliveries are absorbed by the routing-level link lookup on the second event, which sees the just-committed link and takes the wake path.

The "commenter must be a linked gritz user" constraint stays unchanged. It is the security boundary: only events authored by a linked gritz user may create or wake tasks.

### 1. Org resolution — keep it bound to the commenter

Both webhook handlers already resolve the commenter to an gritz user before invoking `Route`:

- `internal/server/webhookserver/github.go:52` — `GetUserByGitHubUserID(extracted.githubUserID)`; `sql.ErrNoRows` drops the event.
- `internal/server/webhookserver/atlassian.go:77` — `GetUserByAtlassianAccountID(extracted.atlassianAccountID)`; same drop behaviour.

Routing extends from there. Candidate orgs are the orgs the commenter belongs to, loaded together with each org's routing rules in one query (§4, `ListRoutingRulesForUser`). Each candidate org then evaluates its own rules and its own links independently.

**Rejected alternative — resolve orgs by GitHub installation id.** The `orgs` table already stores the installation id (`internal/store/org.go:171`, `SetOrgGitHubInstallation`), so an installation-keyed lookup is technically available. We reject it because it would let *any* commenter in an installed repo trigger task creation. The current invariant — "only linked gritz users can trigger tasks" — is a deliberate security feature, not a limitation. Keying on the installation breaks that invariant.

The Atlassian path inherits the same posture: account id → user → `ListOrgsByMember`. See §6 for Atlassian parity.

### 2. `RoutingRule` schema extension

Today (`internal/model/routing_rule.go`):

```go
type RoutingRule struct {
    Source  string `json:"source,omitempty"`
    Type    string `json:"type,omitempty"`
    Prefix  string `json:"prefix,omitempty"`
    Mention string `json:"mention,omitempty"`
}
```

The existing match fields (`source`, `type`, body `prefix`, `mention`) already cover scoping for v1: a create-rule like `github + issue_comment + mention: icholy-bot` is enough to target the realistic case. What we lack is a place to carry the create-task config.

Proposed shape:

```go
type RoutingRule struct {
    Source  string             `json:"source,omitempty"`
    Type    string             `json:"type,omitempty"`
    Prefix  string             `json:"prefix,omitempty"`   // matches Data (legacy)
    Mention string             `json:"mention,omitempty"`
    Create  *CreateTaskAction  `json:"create,omitempty"`   // NEW: if set, rule may create
}

type CreateTaskAction struct {
    Workspace string `json:"workspace"`
    Runner    string `json:"runner"`
    Prompt    string `json:"prompt,omitempty"`
}
```

Backward-compatible: rules without `create` keep behaving exactly as today (`create == nil` means wake-only). Adding fields to the JSON-stored rule list does not require a migration — `GetOrgRoutingRules` (`internal/store/org.go:191`) `json.Unmarshal`s into the current struct and skips unknown fields anyway. Existing rows continue to parse.

Proto mirror (`proto/gritz/v1/gritz.proto:532-537`):

```protobuf
message RoutingRule {
    string source = 1;
    string type = 2;
    string prefix = 3;
    string mention = 4;
    CreateTaskAction create = 5;    // NEW
}

message CreateTaskAction {
    string workspace = 1;
    string runner = 2;
    string prompt = 3;
}
```

`model.RoutingRule.Proto` / `RoutingRuleFromProto` (`internal/model/routing_rule.go:17-34`) gain the new field. The set/get RPCs (`GetRoutingRules` / `SetRoutingRules`) don't change shape — they ship the new field automatically.

**Deferred: URL-filter field.** A separate URL-prefix match (distinct from the existing body `prefix`) is a cleanly additive follow-up — it can be added later without breaking the v1 shape. For v1, create-rules scope via the existing match fields; users who need repo-level scoping can lean on `mention` or `prefix`.

### 3. `MatchRule` is unchanged

`internal/eventrouter/rule.go:13` stays exactly as it is — `Source`, `Type`, body `Prefix`, and `Mention` are sufficient gates for v1.

### 4. Reworked `Route` flow

#### 4a. Data access — two queries, both org-keyed

Two new store methods replace the current `ListOrgsByMember` + `GetRoutingRulesByOrgs` + `FindSubscribedLinksForUser` triple used by routing today.

**`ListRoutingRulesForUser(ctx, tx, userID string) (map[int64][]model.RoutingRule, error)`** — returns rules for every org the user is a member of, keyed by org id. It **must include orgs with no configured rules** (which fall back to `defaultRules`), so the join is grounded in `org_members`. Since rules are stored as a JSON column on the `orgs` row today (`GetOrgRoutingRules` unmarshals `org.routing_rules` from `internal/store/org.go:191`), the query is a plain membership join — there is no LEFT-JOIN concern, because the rules column is always present on `orgs`:

```sql
-- name: ListRoutingRulesForUser :many
SELECT o.id, o.routing_rules
FROM orgs o
JOIN org_members m ON m.org_id = o.id
WHERE m.user_id = $1 AND o.archived = FALSE;
```

> If routing rules are ever normalized into their own table, this query must become `LEFT JOIN routing_rules` so rule-less member orgs aren't dropped — they still need to fall back to `defaultRules`.

`ListOrgsByMember` (`internal/store/org.go:48`) stays in place for its other callers; the routing path simply stops using it.

**`FindSubscribedLinksForOrgs(ctx, tx, url string, orgIDs []int64) (map[int64][]*model.Link, error)`** — org-scoped, filters `subscribe = TRUE` in SQL, groups by org. Replaces `FindSubscribedLinksForUser` in the routing path:

```sql
-- name: FindSubscribedLinksForOrgs :many
SELECT l.id, l.task_id, l.relevance, l.url, l.title, l.subscribe, l.created_at, t.org_id
FROM task_links l
JOIN tasks t ON l.task_id = t.id
WHERE l.url = $1 AND l.subscribe = TRUE AND t.archived = FALSE
  AND t.org_id = ANY($2::BIGINT[])
ORDER BY t.org_id, l.created_at DESC;
```

`FindSubscribedLinksForUser` (`internal/store/sql/queries/link.sql:23-29`) has no other callers in the repo, so it can be retired with this change. The renamed in-router helper `links` replaces `find` (`internal/eventrouter/eventrouter.go:86`).

#### 4b. Control flow — first matching rule per org

The replacement for `internal/eventrouter/eventrouter.go:42-82`:

```go
func (r *Router) Route(ctx context.Context, input InputEvent) (int, error) {
    if input.URL == "" || input.UserID == "" {
        return 0, nil
    }

    rulesByOrg, err := r.Store.ListRoutingRulesForUser(ctx, nil, input.UserID)
    if err != nil {
        return 0, err
    }

    // 1. First matching rule per org; orgs with no match are dropped.
    matched := map[int64]*model.RoutingRule{}
    for orgID, rules := range rulesByOrg {
        if len(rules) == 0 {
            rules = defaultRules
        }
        for i := range rules {
            if input.MatchRule(rules[i]) {
                matched[orgID] = &rules[i]
                break
            }
        }
    }
    if len(matched) == 0 {
        return 0, nil
    }

    // 2. Link lookup runs only for orgs that have a matching rule.
    orgIDs := make([]int64, 0, len(matched))
    for orgID := range matched {
        orgIDs = append(orgIDs, orgID)
    }
    linksByOrg, err := r.links(ctx, input.URL, orgIDs)
    if err != nil {
        return 0, err
    }

    // 3. Wake if a subscribed link exists; otherwise create if the matched rule opts in.
    var n int
    for orgID, rule := range matched {
        if links := linksByOrg[orgID]; len(links) > 0 {
            event := &model.Event{Description: input.Description, Data: input.Data, URL: input.URL, OrgID: orgID}
            if err := r.Store.CreateEvent(ctx, nil, event); err != nil {
                r.Log.Error("failed to create event", "org_id", orgID, "error", err)
                continue
            }
            for _, link := range links {
                if err := r.attach(ctx, link.TaskID, event); err != nil {
                    r.Log.Error("failed to attach event to task", "event_id", event.ID, "task_id", link.TaskID, "error", err)
                    continue
                }
                n++
            }
            continue
        }
        if rule.Create == nil {
            continue
        }
        created, err := r.create(ctx, input, orgID, rule)
        if err != nil {
            r.Log.Error("failed to create task from rule", "org_id", orgID, "error", err)
            continue
        }
        if created {
            n++
        }
    }
    return n, nil
}
```

Notes on this flow:

- The link query is scoped to **only the orgs with a matching rule**, not all member orgs. Member orgs with no matching rule never trigger a link query.
- Per-org isolation still falls out naturally: each org has its own `matched[orgID]` rule and its own `linksByOrg[orgID]` slice; nothing crosses between orgs.
- The wake branch reuses the existing `attach` helper (`internal/eventrouter/eventrouter.go:117-159`), so log lines, notifications, and the "webhook started task" audit log are unchanged for the existing case.
- **First matching rule wins per org. Rule ordering is significant.** A wake-only rule ordered ahead of a create-rule shadows the create-rule — this is intentional. Users who want create behaviour for a specific event-shape order the create-rule first. If precedence semantics become unwieldy, explicit precedence (priority field, "most specific match wins", etc.) is a follow-up.
- **The routing-level link lookup is the dedup boundary.** `FindSubscribedLinksForOrgs` filters `subscribe = TRUE` in SQL. For a given (org, URL), the first event that goes through `create` commits a subscribed link; the next event's lookup sees it and goes wake. There is no "neither wake nor create" gap.

### 5. Atomic create + redelivery dedup

When a create-rule fires, the new task and its subscribed `Link` to the event URL must be created in the **same transaction**. The link is the dedup key: once it exists, the next event for this URL takes the wake path via `links` → `attach` and no duplicate task is created.

`internal/server/apiserver/task.go:103-115` already shows the pattern for creating a task + audit log in one tx via `Store.WithTx`. The router-side helper extends that pattern with the link create:

```go
func (r *Router) create(ctx context.Context, input InputEvent, orgID int64, rule *model.RoutingRule) error {
    var (
        task         *model.Task
        notification model.Notification
    )
    err := r.Store.WithTx(ctx, nil, func(tx *sql.Tx) error {
        task = &model.Task{
            Name:         routedTaskName(input),
            Runner:       rule.Create.Runner,
            Workspace:    rule.Create.Workspace,
            Instructions: buildInstructions(input, rule),
            Status:       model.TaskStatusPending,
            Command:      model.TaskCommandStart,
            Version:      1,
            OrgID:        orgID,
        }
        if err := r.Store.CreateTask(ctx, tx, task); err != nil {
            return err
        }
        if err := r.Store.CreateLink(ctx, tx, &model.Link{
            TaskID:    task.ID,
            URL:       input.URL,
            Relevance: "trigger",
            Subscribe: true,
            CreatedAt: time.Now().UTC(),
        }); err != nil {
            return err
        }
        if err := r.Store.CreateLog(ctx, tx, &model.Log{
            TaskID: task.ID, Type: "audit",
            Content: "webhook created task",
        }); err != nil {
            return err
        }
        notification = /* task created + log appended */
        return tx.Commit()
    })
    if err != nil {
        return err
    }
    r.publish(ctx, notification)
    return nil
}
```

Be precise about what each piece buys:

- **The same transaction** gives *atomicity* — task and its dedup link commit together, never a half-created task whose link is missing.
- **Dedup for sequential redeliveries** (webhook retries on non-2xx, back-to-back events for the same resource) comes from the routing-level link lookup in `Route`: once `create`'s tx commits, the next event's `FindSubscribedLinksForOrgs` call sees the subscribed link and takes the wake path instead of calling `create` again. No in-tx re-check is needed — it would only narrow the genuinely-concurrent window, which we already accept as a v1 limitation.
- **Known limitation, accepted for v1.** Two genuinely *overlapping* transactions (e.g., the same webhook redelivered with millisecond spacing while the first tx is still open) can each pass the routing-level lookup and produce a duplicate task. This is rare in practice and recoverable by cancelling one of the tasks. We do not engineer around it in v1.

If duplicates actually show up in production we can revisit — options include a `pg_advisory_xact_lock` keyed on `(orgID, url)` at the top of the tx, or a `UNIQUE (org_id, url) WHERE subscribe = TRUE` partial index after deciding whether multi-task subscriptions are a pattern we want to keep supporting (see Trade-offs below).

**No migration is required.** No columns, indexes, or constraints change on `task_links`.

### 6. Prompt sourcing

The created task gets a short prompt:

1. **A one-line preamble** that orients the agent. Sketch:

   ```
   You were created by a routing rule in response to a {source} {type} event.
   ```

   The preamble deliberately does **not** embed `{description}`, `{url}`, or `{data}`. The subscribed `Link` attached to the task in the same transaction (§5) carries the URL as `url` and the event description as `title`, so both are reachable via `get_my_task`. The agent fetches the body specifics from the source itself rather than reading an inlined payload.

2. **The rule's `Prompt`** appended verbatim as a second instruction. If `Prompt` is empty, only the preamble is sent (acceptable degenerate case).

The task's `Name` is left empty — the agent populates it on first run via `update_my_task`.

The preamble is inlined into the task literal in `Router.create`; appending the rule's prompt is a two-line `if` immediately after. No `buildInstructions` helper — the construction is small enough to read in place.

Templating the rule prompt with `{description}` / `{url}` / etc. is **not** proposed. If we later need it, it's an additive change to that block.

### 7. Atlassian parity

Both `webhookserver/atlassian.go` and `webhookserver/github.go` already produce `eventrouter.InputEvent` with a resolved `UserID`. The org resolution change in §1 is implemented inside `Route` (not in the handlers), so Atlassian inherits the same behaviour with no handler-side changes: account id → user → `ListOrgsByMember` → per-org rule evaluation → create or wake.

`extractAtlassianWebhookEvent` (`internal/server/webhookserver/atlassian.go:117`) uses the issue browse URL as the routing URL; a create-rule on `source: atlassian` + `mention: <account-id>` targets the natural case (the bot was @-mentioned in a Jira comment).

We **do not** scope create-rules to GitHub-only; Atlassian gets the same capability in the same change.

### 8. UI impact

The routing-rule editor lives in a shared form (`webui/src/components/routing-rule-form.tsx`) used by `/routing/new` and `/routing/$index` after PR #730. PR #730 is the structural prep; a friendlier event-type dropdown (source-aware fields replacing the raw `source`/`type` inputs) is mid-redesign.

This proposal adds three fields the friendlier form will need to surface:

- `create` — a toggle that reveals the action sub-form. Off: today's wake-only rule.
- `create.workspace` and `create.runner` — selects backed by the existing workspace listing (the Create Task screen already does this; reuse the pattern).
- `create.prompt` — multi-line text area.

The proposal does **not** design that form; it flags that the friendlier event-type rework should layer these on, not the old raw-field form. Also, comment #4584625992 on the issue plans to move Routing Rules out of Settings into a top-level Events section; the new fields land in whichever home that refactor settles on (it does not block this work).

### 9. Default rules

`defaultRules` (`internal/eventrouter/eventrouter.go:35-37`) stays a single prefix-only wake rule. The default behaviour for an org with no configured rules is unchanged: comments starting with `gritz:` wake matching tasks, no creation. To opt into creation an org configures a rule with `create` set.

### 10. Test plan sketch

`internal/eventrouter` already has table-driven tests in `eventrouter_test.go`; the new cases extend it:

- **Route — wake unchanged** — `TestRouteCreatesEventAndStartsTask` (current) and `TestRouteMultipleOrgs` (current) keep passing without modification (they exercise the membership-grounded query and wake path equally).
- **Route — create on first event** — set up an org with a create-rule, no link, fire an event matching the rule's match fields; assert one task created with the expected workspace/runner/instructions and one subscribed link pointing at the event URL.
- **Route — second event wakes the created task** — replay the same event; assert no second task created, and the existing task transitions through `attach`.
- **Per-org isolation** — user belongs to org A (matching link, no create-rule) and org B (matching create-rule, no link). Assert A wakes its task and B creates a new one; the link in A does not suppress creation in B.
- **First matching rule wins** — org with `[wake-only rule that matches, create-rule that also matches]`. Assert the wake-only rule shadows the create-rule (no task created when no link exists). Reorder to `[create-rule first, wake-only second]` and assert the create-rule fires.
- **Rule-less org uses `defaultRules`** — org member of the user with `routing_rules = []`. Fire an event whose body starts with `gritz:` and matches a subscribed link in the org; assert the wake path runs. Fire one without the `gritz:` prefix and assert nothing happens. (Confirms the membership join returns the org and the fallback is applied.)
- **Link query scoped to matched orgs** — user is a member of orgs A, B, C, only B has a matching rule. Assert `FindSubscribedLinksForOrgs` is called with `[B]` only. (Easiest to assert via the store moq.)
- **Redelivery dedup** — fire the same `Route` call twice sequentially against an org with a create-rule. Assert exactly one task and one subscribed link exist after both return. The second call sees the link committed by the first and takes the wake path (the dedup happens at the routing-level link lookup, not inside the tx).
- **Create-rule that doesn't match** — rule with `mention: bot` against an event without the mention; assert no task is created.

The dedup and rule-less-org tests belong in `internal/eventrouter` with `teststore.New(t)` — `teststore` already spins up real Postgres, so the tx behaviour and SQL join are exercised end-to-end.

## Trade-offs

### Org resolution: linked-commenter vs installation id

**Chosen: linked-commenter (`ListOrgsByMember`).** The invariant "only linked gritz users can trigger tasks" is a security feature. Installation-id keying would let any commenter in an installed repo trigger creation — strictly broader and a strict regression in posture. See §1.

### Dedup: routing-level lookup vs advisory lock vs unique constraint

**Chosen: routing-level link lookup only.** A `pg_advisory_xact_lock` keyed on `(orgID, url)` would close the overlapping-tx gap, and a `UNIQUE (org_id, url) WHERE subscribe = TRUE` partial index would close it at the DB level — but both are over-engineering for v1:

- The advisory lock adds a store method, a per-create round trip, and surface area to test, all to defend against a race we have no evidence of yet.
- The unique constraint forbids multiple tasks in the same org subscribing to the same URL (a pattern `task_links` allows today and that parent/child task setups can legitimately produce), and requires a backfill/dedup of any existing rows that would collide.

The routing-level `FindSubscribedLinksForOrgs` lookup absorbs sequential webhook redeliveries — once `create` commits, the next call's lookup sees the link and takes the wake path. The remaining gap (genuinely overlapping transactions racing past that lookup) is rare and recoverable by cancelling one of the tasks. Revisit if duplicates actually show up. (An earlier draft of this proposal added an in-tx re-check inside `create`; it was dropped because it only narrows the same overlapping-tx window we already accept, while doubling the link-lookup count on every create.)

### Rule shape: embedded `create` block vs flat fields

**Chosen: an embedded `CreateTaskAction` struct (`create` in JSON, optional sub-message in proto).** Two reasons:

1. Presence-as-discriminator: `create != nil` cleanly says "this rule may create a task". A flat `create_task bool` + flat `workspace`/`runner`/`prompt` would be functionally equivalent but invites invalid combinations (`create_task = false` but `workspace = "x"`).
2. The action fields are conceptually a unit. Grouping them helps the UI render the action as one collapsible block.

### Prompt templating

**Chosen: no templating in the rule's `prompt`.** The auto-generated preamble already carries `{source, type, description, url, data}`; the rule's prompt is appended verbatim. Templating is an additive change later if a real need surfaces.

## Open Questions

1. **Exact preamble text.** §6 sketches it. The wording is easy to iterate on later; the structural decision is "preamble is its own instruction, separate from the rule's prompt".
2. **Notification message for created tasks.** `attach` publishes `"Task N woken by event M: …"`. The create path should publish an analogous `"Task N created by routing rule for event …"`. Wording belongs in implementation review.
3. **Rule limits or validation.** Should the API reject create-rules whose `workspace`/`runner` don't exist on the org at save time? `apiserver.CreateTask` validates this at task-create time (`internal/server/apiserver/task.go:78-84`); doing it in `SetRoutingRules` is friendlier but adds a foreign-key-like check across the rules JSON column. Deferred — not addressed in v1.
4. **Auto-archive defaults for created tasks.** `Task.ArchiveAfter` (`internal/model/task.go:77`) defaults to "never". For long-lived event-driven tasks this may be fine; for one-shot triggers a non-zero default could be useful. Could be added later as `create.archive_after` without breaking the v1 shape.
