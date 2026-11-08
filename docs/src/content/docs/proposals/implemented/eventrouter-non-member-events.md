---
title: "Eventrouter: support events from non-org-member users"
description: "The event router only routes external events from actors who are oauth-linked gritz org members, so activity by unlinked GitHub or Jira users is silently dropped."
---

Issue: https://github.com/icholy/gritz/issues/1255

## Problem

The eventrouter only routes an external event when the user who performed the
action (the GitHub commenter, the Jira commenter, the labeler, etc.) is an
**oauth-linked member** of an gritz org. This falls out of two gates:

1. **Webhook handlers drop unlinked actors.** Both the GitHub and Atlassian
   handlers resolve the acting user to an gritz user before routing —
   `GetUserByGitHubUserID` (`internal/server/githubserver/webhook.go:53`) and
   `GetUserByAtlassianAccountID` (`internal/server/atlassianserver/webhook.go:81`).
   If the actor has no linked account, the handler logs `no linked account` and
   returns without routing.

2. **The router only considers the actor's member orgs.** `Router.Route`
   short-circuits when `input.UserID == ""`
   (`internal/eventrouter/eventrouter.go:97`) and then fetches rules via
   `ListRoutingRulesForUser`, whose SQL `JOIN org_members` restricts results to
   orgs the actor belongs to (`internal/store/sql/queries/org.sql:89-93`).

The result: to trigger a rule, the actor must have oauth-linked their GitHub and
Jira accounts to gritz **and** be a member of the org. We want the option to
create rules that fire for **non-member** actors — for example, an external
contributor commenting `@gritz-bot` on a PR, or an outside reporter labeling an
issue — without every such actor having to link accounts and join the org.

Non-member routing must be **opt-in per rule**, so the change never widens who
can trigger an existing rule. Only rules whose author explicitly enabled the new
toggle become reachable by non-members.

## Design

Two new pieces, exactly as sketched in the issue:

1. A new `Orgs []int64` field on `eventrouter.InputEvent`, populated by the
   integrations, naming the orgs the event belongs to **independent of the
   actor's membership**.
2. A new per-rule opt-in, `Public`, surfaced as a toggle in the rule
   editor UI.

The router combines them: an org's rules are eligible for an event if the actor
is a **member** of that org (existing behavior — all the org's rules apply), or
if the org appears in `input.Orgs` and the rule has `Public = true`.

### `eventrouter.InputEvent.Orgs`

Add the field to the struct in `internal/eventrouter/eventrouter.go`:

```go
type InputEvent struct {
	Source      string
	Type        string
	Description string
	Data        string
	URL         string
	UserID      string
	// Orgs names the orgs this event belongs to, resolved by the webhook
	// handler independent of the actor's membership (GitHub: the orgs sharing
	// the App installation; Atlassian: the org in the webhook's ?org= param).
	// It gates non-member routing: a rule with Public can fire for one
	// of these orgs even when UserID is empty or the actor is not a member.
	Orgs  []int64
	Attrs Attrs
	Meta  any
}
```

`UserID` becomes optional: with non-member routing, an event may route with no
linked actor at all.

### `RoutingRule.Public`

**Proto** (`proto/gritz/v1/gritz.proto`). The next free field number is `11`
(reserved: `3,4,6,7,8`; used: `1,2,5,9,10`):

```protobuf
message RoutingRule {
  reserved 3, 4, 6, 7, 8;
  string source = 1;
  string type = 2;
  CreateTaskAction create = 5;
  bool wakeup = 9;
  repeated RuleCondition conditions = 10;
  // public lets this rule fire for actors who are not members of the
  // org (and need not be oauth-linked). Defaults false — rules are member-only
  // unless explicitly opted in.
  bool public = 11;
}
```

**Model** (`internal/model/routing_rule.go`). Add the field and plumb it through
`Proto()` / `RoutingRuleFromProto()`:

```go
type RoutingRule struct {
	Source          string            `json:"source,omitempty"`
	Type            string            `json:"type,omitempty"`
	Conditions      []Condition       `json:"conditions,omitempty"`
	Create          *CreateTaskAction `json:"create,omitempty"`
	Wakeup          bool              `json:"wakeup,omitempty"`
	Public bool              `json:"public,omitempty"`
}
```

Rules are stored as JSONB in `orgs.routing_rules`, so the new field needs no
schema migration — existing rows decode with `Public = false`, which is
the safe default (member-only).

### Router: combine member and non-member orgs

The member orgs and the event's other orgs are fetched in a **single store
call** that returns each org **tagged with whether the actor is a member**. The
store doesn't decide policy — it just labels the two sources; the router then
uses the `Public` flag to decide, per rule, whether a non-member org's
rule is eligible. Keeping the flag decision in the router (next to `Match`)
rather than in SQL is what lets the same result set serve both the member and
non-member cases, and keeps the ruleless-org default-rule fallback working.

**Store** (`internal/store/sql/queries/org.sql`). The query becomes a `UNION` of
a member branch and an event-orgs branch, each selecting a constant `is_member`
column so the caller can tell them apart:

```sql
-- name: ListRoutingRulesForEvent :many
-- Member orgs (joined via org_members) are returned with is_member = TRUE and
-- all their rules. The orgs in $2 (the event's orgs) that the actor is NOT a
-- member of are returned with is_member = FALSE; the caller keeps only their
-- public rules. Membership wins on overlap (the NOT EXISTS drops a
-- passed org the actor already belongs to). An empty $2 reduces this to today's
-- member-only behavior; an empty user id ($1) yields just the non-member branch.
SELECT o.id, o.routing_rules, TRUE AS is_member
FROM orgs o
JOIN org_members m ON m.org_id = o.id
WHERE m.user_id = $1 AND o.archived = FALSE
UNION
SELECT o.id, o.routing_rules, FALSE AS is_member
FROM orgs o
WHERE o.id = ANY($2::BIGINT[])
  AND o.archived = FALSE
  AND NOT EXISTS (
      SELECT 1 FROM org_members m WHERE m.org_id = o.id AND m.user_id = $1
  );
```

The method is **renamed** — `ListRoutingRulesForUser` no longer fits now that it
also takes the event's orgs — to `ListRoutingRulesForEvent`, returning the org id,
its rules, and the membership tag:

```go
// OrgRoutingRules pairs an org's rules with whether the routing actor is a
// member of it. Non-member orgs contribute only their Public rules.
type OrgRoutingRules struct {
	OrgID    int64
	IsMember bool
	Rules    []model.RoutingRule
}

func (s *Store) ListRoutingRulesForEvent(ctx context.Context, tx *sql.Tx, userID string, orgs []int64) ([]OrgRoutingRules, error)
```

**Router** (`internal/eventrouter/eventrouter.go`). The augmentation collapses to
the relaxed guard, one call, and a per-org eligibility check in the existing
match loop:

```go
func (r *Router) Route(ctx context.Context, input InputEvent) (int, error) {
	if input.URL == "" {
		return 0, nil
	}

	orgs, err := r.Store.ListRoutingRulesForEvent(ctx, nil, input.UserID, input.Orgs)
	if err != nil {
		return 0, err
	}

	reg := cmp.Or(r.Registry, DefaultSchemaRegistry)
	matched := map[int64]*model.RoutingRule{}
	for _, org := range orgs {
		rules := org.Rules
		if len(rules) == 0 && org.IsMember {
			// Default-rule fallback stays member-only.
			rules = reg.DefaultRules()
		}
		for i := range rules {
			// Member org: every rule is eligible. Non-member org: only rules
			// that opted in via Public.
			if !org.IsMember && !rules[i].Public {
				continue
			}
			if Match(rules[i], input) {
				matched[org.OrgID] = &rules[i]
				break
			}
		}
	}

	// ... unchanged from here: link lookup, wake/create.
}
```

Key properties:

- **Member vs. non-member orgs are distinguishable.** The store tags each org
  with `IsMember`; the router uses the `Public` flag to gate rules from
  non-member orgs, so a flagged rule reaches *all* the event's orgs while an
  unflagged rule stays confined to the actor's member orgs — exactly the "all of
  them vs. just the user orgs" distinction the flag is meant to control.
- **Member orgs keep today's semantics** — all rules apply, and the ruleless-org
  `reg.DefaultRules()` fallback (the `gritz:` body-prefix wakeup defaults) still
  runs. The fallback is gated on `org.IsMember`, so a ruleless non-member org
  routes nothing; non-member routing always requires an explicit opt-in rule.
- **Overlap resolves in favor of membership.** An org that is both a member org
  and in `input.Orgs` is returned only by the member branch (the event-orgs
  branch's `NOT EXISTS (... org_members ...)` excludes it), so it arrives once
  with `IsMember = TRUE` and its full rule set — a member is never down-scoped to
  the non-member rule subset, and the same org is never processed twice.

The old early-return on `input.UserID == ""` is replaced by the `input.URL == ""`
guard alone. An empty `UserID` matches no `org_members` row, so the first branch
returns nothing and the second branch (driven by `input.Orgs`) still routes.

### Integrations populate `Orgs`

**Atlassian** (`internal/server/atlassianserver/webhook.go`). The org is already
known — it's the `?org=` query parameter the handler parses and validates. Set
it on the event and stop dropping unlinked actors:

```go
input.Orgs = []int64{orgID}

user, err := h.Store.GetUserByAtlassianAccountID(r.Context(), nil, meta.AuthorAccountID)
switch {
case err == nil:
	input.UserID = user.Id
case errors.Is(err, sql.ErrNoRows):
	// Unlinked actor: leave UserID empty and let the router decide via
	// Public rules for input.Orgs.
default:
	// ... internal error
}

routed, err := h.Router.Route(r.Context(), *input)
```

**GitHub** (`internal/server/githubserver/webhook.go`). GitHub webhooks don't
carry an gritz org, but they do carry the App installation, and orgs record
their `github_installation_id` (shared across orgs since migration
`20260621000001_share_github_installation.sql`). Resolve the installation to org
ids:

- Add `InstallationID int64` to `GitHubMeta` and set it from
  `event.GetInstallation().GetID()` in each `toInputEvent` case (mirroring how
  `AuthorID` is already carried on `GitHubMeta`).
- Add a store query to map an installation to its (non-archived) orgs:

  ```sql
  -- name: ListOrgIDsByGitHubInstallation :many
  SELECT id FROM orgs
  WHERE github_installation_id = $1 AND archived = FALSE;
  ```

- In the handler, populate `Orgs` from the installation and stop dropping
  unlinked actors:

  ```go
  input.Orgs, err = h.Store.ListOrgIDsByGitHubInstallation(r.Context(), nil, meta.InstallationID)
  // ... handle error

  user, err := h.Store.GetUserByGitHubUserID(r.Context(), nil, meta.AuthorID)
  switch {
  case err == nil:
      input.UserID = user.ID
      // (existing cached-username refresh stays here)
  case errors.Is(err, sql.ErrNoRows):
      // Unlinked actor: route via Public rules for input.Orgs.
  default:
      // ... internal error
  }

  totalRouted, err := h.Router.Route(r.Context(), *input)
  ```

Restricting non-member routing to the installation's orgs is deliberate: a
non-member event can only reach orgs that have actually installed the GitHub App
on the repo the event came from, not arbitrary orgs.

### Web UI: the toggle

`RoutingRuleFormValues` (`webui/src/lib/routing-rules.ts`) gains an
`public: boolean` field (default `false` in `emptyRoutingRule`), wired
through `formValuesFromRoutingRule` and `buildRoutingRule`. The rule editor
(`webui/src/components/routing-rule-form.tsx`) gains a checkbox next to the
existing **Wake up** toggle, labeled e.g. *"Public"* with help text explaining
that the rule can then be triggered by users who are not org members and need
not have linked their GitHub/Jira accounts.

## Implementation Plan

An ordered stack of small PRs. Each foundational layer is safe to merge before
the ones above it land — the field simply goes unused until the router and
handlers consume it.

1. **Proto + model field** — Delivers: `public` on the `RoutingRule`
   proto (field 11) plus regenerated code, and `Public` on
   `model.RoutingRule` plumbed through `Proto()` / `RoutingRuleFromProto()`.
   Depends on: nothing. Verifiable by: `mise run generate` produces a clean
   diff; a model round-trip unit test (`model → proto → model`) preserves the
   flag.

2. **Store: `ListRoutingRulesForEvent` UNION** — Delivers: the renamed store
   method (`ListRoutingRulesForUser` → `ListRoutingRulesForEvent`) taking
   `(userID, orgs)`, the `UNION` query with the `is_member` column, and the
   `OrgRoutingRules{OrgID, IsMember, Rules}` return type. Depends on: nothing
   (the flag filter now lives in the router, so this layer is independent of the
   proto field). Verifiable by: store unit tests — member orgs return
   `IsMember=true` with all rules; passed orgs return `IsMember=false`; an org
   that is both a member org and passed in returns once with `IsMember=true`;
   archived orgs are excluded from both branches.

3. **`InputEvent.Orgs` + router** — Delivers: the `Orgs` field on `InputEvent`,
   the relaxed entry guard (`input.URL == ""` only), the call to
   `ListRoutingRulesForEvent`, and the per-org eligibility check (non-member orgs
   contribute only `Public` rules; default-rule fallback gated on
   `IsMember`). Depends on: (1) (reads `Public`), (2). Verifiable by:
   eventrouter unit tests — a member still matches all rules; a non-member with
   `Orgs` matches only a `Public` rule; an empty `UserID` with `Orgs`
   routes; a member org that also appears in `Orgs` still uses its full rule
   set; a ruleless non-member org routes nothing.

4. **Store: orgs by GitHub installation** — Delivers: the
   `ListOrgIDsByGitHubInstallation` query and store method (non-archived only).
   Depends on: nothing (independently mergeable). Verifiable by: a store unit
   test over orgs sharing an installation id, excluding archived orgs.

5. **GitHub webhook wire-up** — Delivers: `InstallationID` on `GitHubMeta`, the
   handler populating `input.Orgs` from the installation, and the change from
   dropping unlinked actors to routing them (empty `UserID`). Depends on: (3),
   (4). Verifiable by: webhook handler tests — a linked member routes as before;
   an unlinked actor routes only through a `Public` rule on an org
   sharing the installation.

6. **Atlassian webhook wire-up** — Delivers: the handler setting
   `input.Orgs = []int64{orgID}` and routing unlinked actors instead of dropping
   them. Depends on: (3). Verifiable by: webhook handler tests mirroring the
   GitHub cases, using the `?org=` param.

7. **Web UI toggle** — Delivers: `public` in the form values/build/parse
   helpers and the checkbox in the rule editor. Depends on: (1) (consumes the
   regenerated proto). Verifiable by: `pnpm lint` in `webui/` and rendering the
   editor against a rule with the flag set/unset.

## Trade-offs

- **Member routing stays actor-scoped; non-member routing is org/installation-scoped.**
  A linked member's event continues to route to *all* their member orgs
  regardless of which installation/repo produced it (today's behavior, left
  unchanged). Non-member routing is narrower — it only reaches orgs named in
  `input.Orgs` (the installation's orgs for GitHub, the `?org=` org for
  Atlassian). This asymmetry keeps the change strictly additive: no existing
  member's routing behavior changes, and non-members are confined to orgs that
  demonstrably own the integration the event came from.

- **Opt-in per rule rather than per org.** A per-org "allow non-members" setting
  would be simpler to store but coarser — it would open *every* rule in the org
  to non-members. Per-rule keeps the blast radius tight: an org can, say, accept
  non-member `@bot` mentions on PRs while still gating issue-label rules to
  members. Reusing the existing JSONB rule shape also avoids a schema migration.

- **One UNION query vs. two store calls.** An earlier sketch fetched member orgs
  and non-member orgs with two calls (`ListRoutingRulesForUser` +
  `GetRoutingRulesByOrgs`) and filtered `Public` in Go. Folding both
  into a single `UNION` (`ListRoutingRulesForEvent`) is one round trip and keeps
  the membership-wins exclusion in SQL (`NOT EXISTS (... org_members ...)`).
  `GetRoutingRulesByOrgs` stays as-is for its existing callers.

- **Tag membership, filter the flag in Go — not in SQL.** The `UNION` returns an
  `is_member` column rather than pre-filtering the non-member branch's JSONB to
  `public` rules. Keeping the flag decision in the router (beside
  `Match`) means the store returns a faithful view of each org's rules — which is
  what lets a member org keep *all* its rules and still feed the ruleless-org
  default-rule fallback, while non-member orgs are narrowed to opted-in rules.
  Pushing the filter into SQL (a `jsonb_array_elements` unnest) would collapse
  that distinction and couple the store query to the JSONB rule shape.

## Open Questions

- **Should member routing also intersect with `input.Orgs`?** Currently a linked
  member routes to all their orgs even if the event's installation isn't tied to
  some of them. Scoping member routing to `input.Orgs` too would be more
  consistent, but it changes long-standing behavior and risks breaking existing
  setups where a member relies on cross-installation routing. Proposed: leave
  member routing unchanged for now.

- **De-duplication when an actor is both a member and matches a non-member rule
  in the same org.** Handled inside the query: the non-member branch's
  `NOT EXISTS (... org_members ...)` excludes any org the actor belongs to, and
  `UNION` collapses identical rows, so a shared org is emitted once with its full
  member rule set.

- **Archived-org safety for Atlassian.** Both branches of the query already
  filter `o.archived = FALSE`, so an archived org named in `input.Orgs` (e.g. the
  Atlassian `?org=` param) routes nothing centrally, regardless of what the
  handler passes. No per-handler guard is needed.

- **UI discoverability / safety.** Should enabling **Allow non-members** show a
  warning, given it widens who can trigger the rule (and, for create-task rules,
  who can cause a container to spin up)? Rate-limiting or restricting
  non-member create-task rules is out of scope here but worth flagging.
