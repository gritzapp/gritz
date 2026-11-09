# Maximum Orgs Per User

Issue: https://github.com/icholy/gritz/issues/739

## Problem

A user can be a member of an unbounded number of orgs. Several code paths fan out over `ListOrgsByMember(user.ID)` per inbound request or webhook event, so an unbounded membership count makes that fan-out a per-event cost.

The driving concern is the routing rework in `#717` (see `proposals/implemented/routing-rules-create-tasks.md`): `Router.Route` will call `ListRoutingRulesForUser` for every inbound webhook event and evaluate rules + lookup links per member org. A user in a pathological number of orgs makes each event proportionally more expensive.

It is also a general abuse guardrail. Today a single user can — via the public `CreateOrg` RPC — keep creating orgs unboundedly, each of which adds an `org_members` row keyed to them.

This proposal adds a per-user cap on org memberships, enforced at the points where a user gains a membership.

## Design

### 1. The cap key: a single `org_members` row counts

Every membership-growth path in the codebase ends with one `org_members` row inserted, whether the user is owner or member:

- `Server.CreateOrg` (`internal/server/apiserver/org.go:16`) — creates the org, then inserts the caller as `role = "owner"` (`internal/server/apiserver/org.go:29`).
- `Server.AddOrgMember` (`internal/server/apiserver/org.go:80`) — the org owner adds another user by email; inserts that user as `role = "member"` (`internal/server/apiserver/org.go:104`).
- `StoreUserResolver.Provision` (`internal/server/storeauth.go:44`) — first-login bootstrap: creates the user's default org and inserts them as `role = "owner"` (`internal/server/storeauth.go:63`).

There is no separate invite/accept flow today. There is no other RPC and no background path that inserts into `org_members`. (Confirmed by grepping `AddOrgMember` and `CreateOrg` outside `_test.go` files — the matches are the three call sites above and the store implementations they delegate to.)

Because `CreateOrg` always adds the creator as a member row, **owning and being-a-member are not separate counts** — they are the same row in `org_members`. The cap is therefore a single number: the count of `org_members` rows for `user_id`. No new column, no separate "owned" counter.

The fan-out we are bounding is `ListOrgsByMember`, whose SQL excludes archived orgs (`internal/store/sql/queries/org.sql:11-16`). The cap query should match: count membership rows joined to non-archived orgs only. Archived orgs are not load-bearing for the routing cost, so they should not consume cap slots.

```sql
-- name: CountOrgMembershipsByUser :one
SELECT COUNT(*)
FROM org_members om
JOIN orgs o ON o.id = om.org_id
WHERE om.user_id = $1 AND o.archived = FALSE;
```

### 2. Where the check goes

The cap is enforced at the two user-callable RPCs that grow a membership:

| Path | Whose count is checked | Why |
|---|---|---|
| `Server.CreateOrg` (`internal/server/apiserver/org.go:16`) | The caller — they are about to become owner+member of a new org | Membership grows by one for the caller |
| `Server.AddOrgMember` (`internal/server/apiserver/org.go:80`) | The **target user** (looked up by email at `org.go:92`), not the caller | The added user is the one gaining a membership; the caller (org owner) is unchanged |

`StoreUserResolver.Provision` (`internal/server/storeauth.go:44`) is **exempt**. It runs at first login for a brand new user whose membership count is zero, and it would be a bootstrap bug to block the user's default-org creation on a cap. Skipping it costs nothing — the check would always pass — but making the exemption explicit removes any temptation to add the check there "for symmetry" and trip a future code path.

`store.AddOrgMember` (`internal/store/org.go:85`) and `store.CreateOrg` (`internal/store/org.go:14`) stay un-aware of the cap. The store layer is also called by `teststore.CreateOrg` and the apiserver tests directly; pushing the check into the store would break tests that intentionally create many orgs for the same synthetic user and would mix policy into persistence. Policy stays at the apiserver layer.

#### Shape of the check

In both handlers, before the existing insert:

```go
count, err := s.store.CountOrgMembershipsByUser(ctx, tx, targetUserID)
if err != nil {
    return nil, connect.NewError(connect.CodeInternal, err)
}
if count >= s.maxOrgsPerUser {
    return nil, connect.NewError(connect.CodeResourceExhausted,
        fmt.Errorf("user is already in the maximum of %d orgs", s.maxOrgsPerUser))
}
```

The check uses the same `tx` as the subsequent insert when one is in flight. For `CreateOrg`, the existing `s.store.WithTx` block (`internal/server/apiserver/org.go:25-37`) already wraps create+addMember; the count runs inside that tx. For `AddOrgMember`, today it doesn't open a tx; the cap check + insert should be wrapped in `WithTx` so both run against the same connection.

### 3. What counts: membership, not ownership-only

Recommend: **count `org_members` rows for the user**, not a separate "owned" count.

- Ownership without membership doesn't exist in this codebase. Every `CreateOrg` path inserts both the `orgs` row (with `Owner = user.ID`) and the `org_members` row in the same transaction. There is no way to be an owner without also being a member.
- The cost the issue is bounding is `ListOrgsByMember` fan-out, which is keyed on `org_members`. The thing we care about and the thing we count should be the same.
- It also gives the user a single, intuitive cap: "you can be in up to N orgs". Whether they own them or were added is orthogonal.

We do **not** add a separate owner-only sub-cap (e.g. "and at most M of those can be ones you own"). It is a layer of policy without a current motivating problem and would require a second count query; defer until there's a real abuse pattern around org *creation* specifically.

### 4. The limit value

Recommend: **default of `20`, configurable via env/CLI flag.**

20 is comfortably above any realistic real-user count (the current usage pattern is "one default org per user, occasional invites"), and low enough to bound the per-event routing fan-out. The number is a guardrail; pick something we'd be embarrassed to ship higher and that we know is conservative for the routing cost.

Configurable rather than constant because:

- Self-hosters may want it tighter (smaller team, stricter abuse posture) or looser (one shared instance for many internal teams).
- It lets us raise it in production without a code release if we ever bump into the limit.
- It matches the existing pattern in `internal/command/server.go` — every server tunable is a `cli` flag with `Sources: cli.EnvVars(...)`. Examples: `GRITZ_ARCHIVE_BATCH` (`server.go:130`), `GRITZ_ARCHIVE_POLL` (`server.go:124`).

Plumbing:

```go
// internal/command/server.go (alongside the existing flags)
&cli.IntFlag{
    Name:    "max-orgs-per-user",
    Usage:   "Maximum number of orgs a single user can be a member of",
    Value:   apiserver.DefaultMaxOrgsPerUser, // 20
    Sources: cli.EnvVars("GRITZ_MAX_ORGS_PER_USER"),
},
```

```go
// internal/server/apiserver/apiserver.go
const DefaultMaxOrgsPerUser = 20

type Options struct {
    // ... existing fields ...
    MaxOrgsPerUser int
}

func New(opts Options) *Server {
    // ... existing assignments ...
    if opts.MaxOrgsPerUser <= 0 {
        opts.MaxOrgsPerUser = DefaultMaxOrgsPerUser
    }
    return &Server{
        // ... existing fields ...
        maxOrgsPerUser: opts.MaxOrgsPerUser,
    }
}
```

A non-positive `MaxOrgsPerUser` value falls back to the default. We deliberately do **not** support `0` as "unlimited" — `<= 0` looks like a config typo and silently disabling the guardrail is worse than picking a sane default. If genuinely unlimited is ever needed (e.g. for tests that want to ignore the cap), the test path can set a very high value.

### 5. Existing over-limit users — grandfather

Recommend: **grandfather existing memberships, only block new ones beyond the cap.**

The check is `count >= cap` on the path that adds the (count + 1)th row. If a user already has more than `cap` memberships, every subsequent attempt to add another is rejected — but their existing memberships are not touched. No migration, no notification, no UI change for them; they simply can't grow further until they leave some.

Stricter alternatives (force-remove memberships, lock the user, block access to the over-limit orgs) are user-hostile for an issue that is about future growth — the existing fan-out cost is already incurred and bounded by the data they already have. Grandfathering is the proportionate posture.

**Whether any current user is actually over the proposed cap of 20:** a one-shot SQL is sufficient to answer before the PR lands:

```sql
SELECT user_id, COUNT(*) AS n
FROM org_members om
JOIN orgs o ON o.id = om.org_id
WHERE o.archived = FALSE
GROUP BY user_id
HAVING COUNT(*) > 20;
```

Given the current product surface (one default org per user at signup, owner-added members), the realistic expectation is "zero users affected at cap = 20". If the query surfaces real over-limit users, raise the default or set `GRITZ_MAX_ORGS_PER_USER` higher in production for the rollout. This is a release-time check, not a design question — the design tolerates either answer.

### 6. Error surfacing

Recommend: **`connect.CodeResourceExhausted`** with a human-readable message that names the cap value.

`CodeResourceExhausted` is the gRPC-standard code for "quota/limit reached" and is exactly the semantic match for this guardrail (see the gRPC code reference cited in the `grpc` skill at `.claude/skills/grpc/SKILL.md`). The codebase doesn't use `ResourceExhausted` anywhere yet — every error today is `InvalidArgument` / `NotFound` / `Internal` / `FailedPrecondition` / `PermissionDenied` / `Unauthenticated`. Adding `ResourceExhausted` for the first time here is appropriate: it's the conventional code for this case, and future quota-style limits (max tasks, max keys) will want the same code, so picking it now establishes the convention.

The error message includes the cap so the UI doesn't have to fetch it from a separate endpoint:

```
user is already in the maximum of 20 orgs
```

For `AddOrgMember`, where the cap is on the **invitee** rather than the caller, the message names that fact so the org owner understands what they hit:

```
user %q is already in the maximum of 20 orgs
```

The Web UI does not need a new RPC to surface the cap — it already calls `AddOrgMember` / `CreateOrg` and renders error messages from the response. The message text is enough.

### 7. Enforcement atomicity

There is a benign race. Two concurrent `CreateOrg` calls (or `AddOrgMember` calls targeting the same user) can both query `CountOrgMembershipsByUser`, both see `count == cap - 1`, and both commit, producing one row over cap.

Recommend: **accept the race; in-tx count is enough.**

Reasoning, consistent with the issue's "guardrail not security boundary" framing:

- The cap is a soft guardrail against unbounded growth. Going to `cap + 1` (or even `cap + 2`) for a brief moment until the user removes a membership doesn't break any invariant. The fan-out cost the cap defends against is `O(N)` in the user's member-org count — being at `21` instead of `20` for a few minutes is indistinguishable in practice.
- The realistic concurrent-add scenario is a single user who slammed the API twice in milliseconds. The webhook routing path doesn't write `org_members`, so it doesn't contribute to the race surface.
- Closing the race properly requires either a `pg_advisory_xact_lock(hashtext('user_orgs:' || user_id))` at the top of each tx, or a partial unique constraint backed by a counter — both are real engineering for a benign overshoot. Not worth it for v1.

So: do the count inside the same `WithTx` as the insert (gives a consistent snapshot within the tx), don't add a lock, accept rare overage. If overage actually surfaces in production we can revisit.

The Connect server runs handlers concurrently — there's no global lock and there shouldn't be one — so this is the only realistic race surface, and it stays at the size described.

### 8. Test plan sketch

Tests live in `internal/server/apiserver/org_test.go` alongside the existing org RPC tests, which already use `teststore.New(t)` (real Postgres) and `srv.CreateOrg` / `srv.AddOrgMember` end-to-end.

- **CreateOrg at cap is rejected.** Set `MaxOrgsPerUser: 2` in `Options`. Provision the default org (via `teststore.CreateOrg`, which gets the user to 1 membership). Call `srv.CreateOrg` once (now at 2). Call again; assert `connect.CodeResourceExhausted` and that no new org was created.
- **AddOrgMember at target's cap is rejected.** Set the cap low. Use the existing `TestAddAndListOrgMembers` setup (`org_test.go:101`) to wire owner + invitee. Manually inflate the invitee's membership count to the cap (e.g. add them to extra orgs via `teststore`). Call `srv.AddOrgMember`; assert `connect.CodeResourceExhausted` and the error message names the invitee.
- **Archived orgs don't count.** Provision N memberships at cap, archive one (via `srv.DeleteOrg`), then `CreateOrg` and assert it succeeds — the archived org freed a slot. (This pins the join condition in `CountOrgMembershipsByUser`.)
- **Grandfathered user can't grow.** Pre-seed a user at `cap + 5` memberships via the store, then `CreateOrg` and assert rejection. Existing memberships remain untouched (assert `ListOrgsByMember` length is still `cap + 5`).
- **Provision is exempt.** Call `StoreUserResolver.Provision` for a brand-new user with `MaxOrgsPerUser: 0` in `Options` (which falls back to the default per §4, so this also pins the fallback). Assert the default org is created — the cap path didn't fire.
- **Default fallback for cap.** Construct `apiserver.New(Options{...})` without `MaxOrgsPerUser` set, assert the server enforces the default at `DefaultMaxOrgsPerUser`. (Unit test on `New`.)

## Trade-offs

### Where to enforce: apiserver vs store

**Chosen: apiserver layer.** The check is policy (a per-user cap with a configurable value), the store is persistence. Pushing the check into `store.AddOrgMember` would mean every test that inflates membership counts via `teststore` has to thread the cap through, and the `Provision` path would have to special-case bypass at the storage layer. Keeping policy at the handler boundary matches the existing pattern (cf. `DeleteOrg`'s "cannot delete your default org" check at `org.go:71`, which is in the handler, not the store).

### Count membership vs separate owner counter

**Chosen: count `org_members` rows only.** Owners are always also members in this codebase (`CreateOrg` adds the owner row in the same tx), so a separate count would be redundant. Adding an "owner-only" sub-cap is a different policy with no current motivating problem.

### Limit value: constant vs configurable

**Chosen: configurable with a default.** Matches the existing flag style in `internal/command/server.go`, lets ops tune in production without a release, and self-hosters can pick a posture that fits their deployment.

### Atomicity: in-tx count vs advisory lock vs unique constraint

**Chosen: in-tx count only.** A `pg_advisory_xact_lock(hashtext('user_orgs:' || user_id))` would close the race, and a backing counter column with a `CHECK` constraint could close it at the DB level — but the failure mode here is a one-or-two-row overshoot under a same-user double-click race, not a correctness violation. The cap is a guardrail, not a security boundary; the in-tx count is proportionate.

### Error code: `ResourceExhausted` vs `FailedPrecondition`

**Chosen: `ResourceExhausted`.** It's the gRPC-standard code for quota-style limits. The codebase doesn't use it today, but introducing it here is the right place — establishes the convention for future quota limits.

`FailedPrecondition` would also be defensible (the codebase already uses it for "cannot delete your default org") and lets the cap reuse an existing code. We prefer `ResourceExhausted` because the semantics are more precise: this isn't "the system isn't in the required state" (which can be made true by changing some other state); it's specifically "you have hit a quota". The distinction matters for any future client that wants to retry-with-backoff vs. surface-and-stop.

## Open Questions

1. **Exact default value.** §4 proposes `20`. Defensible alternatives: `10` (tighter), `50` (looser). Easy to change post-merge by setting the env var; the design doesn't depend on the specific number.
2. **Whether to surface the cap in `GetProfile`.** The Web UI could pre-emptively grey out "Create Org" when the user is at the cap. This is purely cosmetic — the API error already explains the cap on attempt — and we can defer it to a follow-up. If we add it, it slots into `GetProfileResponse` as `max_orgs_per_user` (`internal/server/apiserver/apiserver.go:82-92`).
3. **Whether `DeleteOrg` of an over-cap user's org should be unlocked even from a non-default org.** Today `DeleteOrg` only blocks deletion of the user's *default* org (`org.go:71`). The grandfather scenario doesn't change that — over-cap users can still delete their non-default orgs to free slots — so probably no change needed. Worth confirming in implementation review.