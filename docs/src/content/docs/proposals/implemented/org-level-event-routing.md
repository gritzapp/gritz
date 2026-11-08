---
title: "Org-Level Event Routing Configuration"
description: "Event filtering is hardcoded in the webhook handlers, requiring a gritz: prefix for everyone, so orgs cannot customize triggers or use @mentions and the Router does not own filtering."
---

Issue: https://github.com/icholy/gritz/issues/458

## Problem

Event routing currently uses hardcoded filtering rules baked into the webhook handlers. Both `extractGitHubWebhookEvent` and `extractAtlassianWebhookEvent` require comments to start with the `gritz:` prefix before they are routed to tasks. This logic lives in the webhook layer, outside the `eventrouter.Router`, which means:

1. **No per-org customization** — All orgs share the same `gritz:` prefix rule. An org cannot change or disable the prefix, nor add alternative trigger mechanisms.
2. **No @mention routing** — There's no way to trigger events by mentioning a user (e.g. `@BotUser fix the tests`), which would be a natural fit since the runner already provisions per-user MCP server permissions.
3. **Filtering is in the wrong layer** — The webhook handlers decide what gets routed *before* the Router sees the event. The Router should own filtering so it can apply per-org rules.

## Design

### Overview

Move all event filtering from the webhook handlers into the `eventrouter.Router`. Introduce an `org_routing_rules` table that lets each org configure how events are matched. The Router resolves which orgs an event could be routed to, loads their routing rules, and applies them before creating events.

### 1. Routing Rules Model

Each org can have multiple routing rules. A rule defines a **trigger type** and its **configuration**. Rules are evaluated in order; an event matches an org if *any* rule matches.

Two initial rule types:

- **prefix** — Match comments that start with a configurable string (generalizing the current `gritz:` behavior).
- **mention** — Match comments that @mention a specific username.

```go
// internal/model/routing_rule.go

type RoutingRuleType string

const (
    RoutingRulePrefix  RoutingRuleType = "prefix"
    RoutingRuleMention RoutingRuleType = "mention"
)

type RoutingRule struct {
    Type  RoutingRuleType `json:"type"`
    Value string          `json:"value"` // the prefix string or the username
}
```

### 2. Database Schema

New migration — add a JSON column to the `orgs` table:

```sql
ALTER TABLE orgs ADD COLUMN routing_rules JSONB NOT NULL DEFAULT '[]';
```

The column stores an array of routing rule objects:

```json
[
    {"type": "prefix", "value": "gritz:"},
    {"type": "mention", "value": "BotUser"}
]
```

No data migration is needed. The default empty array `[]` means "use default behavior" (see section 5). This is simpler than a dedicated table — routing rules are a small, org-scoped configuration that is always read as a unit. A JSON column avoids extra joins and keeps the rules co-located with the org they belong to.

### 3. Store Layer

Add to `internal/store/sql/queries/org.sql`:

```sql
-- name: GetOrgRoutingRules :one
SELECT routing_rules FROM orgs WHERE id = $1;

-- name: SetOrgRoutingRules :exec
UPDATE orgs SET
    routing_rules = $2,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: GetRoutingRulesByOrgs :many
SELECT id, routing_rules FROM orgs WHERE id = ANY($1::BIGINT[]);
```

The batch query `GetRoutingRulesByOrgs` is critical — the Router needs rules for all candidate orgs in a single query to avoid N+1 lookups during routing.

In the Go store layer, the `routing_rules` column is scanned into `[]model.RoutingRule` using `json.Unmarshal`. The `Org` model gains a `RoutingRules []RoutingRule` field.

### 4. Proto Definitions

```protobuf
enum RoutingRuleType {
    ROUTING_RULE_TYPE_UNSPECIFIED = 0;
    ROUTING_RULE_TYPE_PREFIX = 1;
    ROUTING_RULE_TYPE_MENTION = 2;
}

message RoutingRule {
    RoutingRuleType type = 1;
    string value = 2;
}

// RPCs
rpc GetRoutingRules(GetRoutingRulesRequest) returns (GetRoutingRulesResponse);
rpc SetRoutingRules(SetRoutingRulesRequest) returns (SetRoutingRulesResponse);

message GetRoutingRulesRequest {}
message GetRoutingRulesResponse {
    repeated RoutingRule rules = 1;
}

message SetRoutingRulesRequest {
    repeated RoutingRule rules = 1;
}
message SetRoutingRulesResponse {
    repeated RoutingRule rules = 1;
}
```

All RPCs are org-scoped via `caller.OrgID` from the auth context. The API is set/get rather than CRUD — the client sends the full list of rules, and the server replaces the JSON column. This matches the JSON column approach: rules are always read and written as a unit.

### 5. Default Behavior (No Rules Configured)

When an org's `routing_rules` column is an empty array `[]` (the default), the Router falls back to the current hardcoded behavior: prefix match on `gritz:`. This ensures backward compatibility — existing orgs work identically without any migration or configuration.

```go
func defaultRules() []model.RoutingRule {
    return []model.RoutingRule{
        {Type: model.RoutingRulePrefix, Value: "gritz:"},
    }
}
```

Once an org sets custom rules, the defaults are no longer applied. This is an explicit opt-in: if you configure rules, you own the full set. If you want to keep `gritz:` alongside a mention rule, you add both.

### 6. Router Changes

The `eventrouter.Router` currently receives a pre-filtered event (webhook handlers already checked for `gritz:` prefix). The new flow:

**Before (current):**
```
Webhook Handler → filter (gritz: prefix) → Router.Route(event)
                                              → findLinksByOrg(url, userID)
                                              → create events, route to tasks
```

**After (proposed):**
```
Webhook Handler → parse (no filtering) → Router.Route(event)
                                           → findCandidateOrgs(url, userID)
                                           → loadRoutingRules(orgIDs)
                                           → for each org: applyRules(event, rules)
                                           → create events, route to matching tasks
```

#### Updated Event struct

The Router needs the raw comment body (not pre-filtered) and knowledge of the event source to apply platform-specific mention patterns:

```go
type Event struct {
    Type        EventType  // EventTypeGitHub, EventTypeAtlassian
    Description string     // human-readable summary
    Data        string     // raw comment body (unfiltered)
    URL         string     // issue/PR/ticket URL for link matching
    UserID      string     // gritz user ID
}
```

No structural change — the `Data` field just now contains the *unfiltered* comment body.

#### Updated Route method

```go
func (r *Router) Route(ctx context.Context, event *Event) (int, error) {
    // Step 1: Find all subscribed links grouped by org (unchanged)
    linksByOrg, err := r.findLinksByOrg(ctx, event.URL, event.UserID)
    if err != nil {
        return 0, err
    }

    // Step 2: Load routing rules for all candidate orgs (NEW)
    orgIDs := maps.Keys(linksByOrg)
    rulesByOrg, err := r.loadRoutingRules(ctx, orgIDs) // uses GetRoutingRulesByOrgs
    if err != nil {
        return 0, err
    }

    // Step 3: For each org, check if the event matches that org's rules
    total := 0
    for orgID, links := range linksByOrg {
        rules := rulesByOrg[orgID]
        if !matchesAnyRule(event, rules) {
            continue // event doesn't match this org's routing rules
        }
        // Create event and route to tasks (unchanged)
        n, err := r.createAndRoute(ctx, event, orgID, links)
        if err != nil {
            return total, err
        }
        total += n
    }
    return total, nil
}
```

#### Rule matching

```go
func matchesAnyRule(event *Event, rules []*model.RoutingRule) bool {
    for _, rule := range rules {
        if matchesRule(event, rule) {
            return true
        }
    }
    return false
}

func matchesRule(event *Event, rule *model.RoutingRule) bool {
    body := strings.TrimSpace(event.Data)
    switch rule.Type {
    case model.RoutingRulePrefix:
        return strings.HasPrefix(body, rule.Value)
    case model.RoutingRuleMention:
        return containsMention(event.Type, body, rule.Value)
    default:
        return false
    }
}
```

### 7. Mention Matching

@mention syntax differs between platforms:

- **GitHub**: `@username` in markdown — match `@<value>` as a word boundary.
- **Jira/Atlassian**: `[~accountid:<account-id>]` in webhook comment bodies. For example: `[~accountid:712020:7a3600f2-2543-459b-8178-fa99fde08d48]`.

```go
func containsMention(eventType EventType, body string, value string) bool {
    switch eventType {
    case EventTypeGitHub:
        // Match @username with word boundary
        pattern := `(?i)(?:^|[\s(])@` + regexp.QuoteMeta(value) + `(?:$|[\s,.)!?])`
        matched, _ := regexp.MatchString(pattern, body)
        return matched
    case EventTypeAtlassian:
        // Jira webhook comments use [~accountid:<id>] format
        return strings.Contains(body, "[~accountid:"+value+"]")
    default:
        return false
    }
}
```

For GitHub mention rules, the `value` field stores the **GitHub username** (e.g. `icholy`). For Atlassian mention rules, the `value` field stores the **Atlassian account ID** (e.g. `712020:7a3600f2-2543-459b-8178-fa99fde08d48`). The user model already has `github_username` and `atlassian_account_id` fields that can populate the UI when creating mention rules.

> **Note:** Compiling regexps per-event is fine for webhook traffic volumes. If it becomes a concern, pre-compile patterns when loading rules.

### 8. Webhook Handler Changes

Both webhook handlers are simplified. They stop filtering and pass the raw event to the Router:

**GitHub handler (`internal/webhook/github.go`):**

```go
func extractGitHubWebhookEvent(event any) *extractedEvent {
    switch e := event.(type) {
    case *github.IssueCommentEvent:
        return &extractedEvent{
            description: fmt.Sprintf("..."),
            data:        e.GetComment().GetBody(), // no gritz: prefix check
            url:         e.GetIssue().GetHTMLURL(),
            githubUserID: e.GetSender().GetID(),
        }
    case *github.PullRequestReviewCommentEvent:
        // same — remove prefix check
    case *github.PullRequestReviewEvent:
        // same — remove prefix check, keep action=="submitted" check
    }
    return nil
}
```

The `action == "submitted"` check for PR reviews stays in the handler — it's event-type filtering (structural), not content filtering (routing). Similarly, the handler still ignores unsupported webhook event types. Only the `gritz:` prefix check moves to the Router.

**Atlassian handler (`internal/webhook/atlassian.go`):**

```go
func extractAtlassianWebhookEvent(body []byte) *extractedEvent {
    // Keep: only process comment_created events
    // Remove: gritz: prefix check
}
```

### 9. ProcessEvent Changes

The `processEventInternal` method in `server.go` also needs updating. Currently it routes events that already exist in the DB. Since the event is already created (and was already filtered at creation time), `ProcessEvent` does not need to re-apply routing rules — it just routes to matching subscribed links as it does today.

No changes needed for `ProcessEvent`.

### 10. Settings UI

Add a "Routing Rules" section to the org settings page:

- List current rules with type, value, and priority
- "Add Rule" form with type selector (prefix/mention) and value input
- Delete button per rule
- Help text explaining the default `gritz:` behavior when no rules are configured
- For mention rules, suggest usernames from org members' linked GitHub/Atlassian accounts

### 11. Implementation Order

1. Database migration (add `routing_rules` JSONB column to `orgs`)
2. Store layer (queries + JSON scanning)
3. Model (`RoutingRule` type, `Org.RoutingRules` field)
4. Proto definitions + generate
5. Server RPC handlers (get/set routing rules)
6. Router changes (load rules, apply filtering)
7. Webhook handler changes (remove prefix filtering)
8. Settings UI

## Trade-offs

### JSON column vs dedicated table

**Chosen: JSON column on `orgs`.** Routing rules are a small, org-scoped configuration that is always read and written as a unit. A JSON column is simpler:
- No extra table, joins, or foreign keys
- Rules are co-located with the org they belong to
- The API is naturally set/get (replace the whole list) rather than CRUD
- Batch loading across orgs is a straightforward `SELECT id, routing_rules FROM orgs WHERE id = ANY(...)`

The trade-off is that individual rules can't be queried or constrained at the DB level (e.g. no unique index on type+value). Duplicate prevention must happen in application code. This is acceptable given the small size and simple structure of the rules list.

### Per-org rules vs global rules with org overrides

**Chosen: per-org only.** A global/default rule set with per-org overrides adds complexity (inheritance, precedence). The `defaultRules()` fallback for unconfigured orgs achieves the same practical effect with simpler semantics: either you have custom rules or you get the default.

### Mention value: platform-native identifiers

**Chosen: GitHub username for GitHub, Atlassian account ID for Atlassian.** Each platform uses the identifier that appears in its webhook payloads:
- GitHub comments contain `@username`, so mention rules use the GitHub username (e.g. `icholy`).
- Jira comments contain `[~accountid:<id>]`, so mention rules use the Atlassian account ID (e.g. `712020:7a3600f2-2543-459b-8178-fa99fde08d48`).

The trade-off is that Atlassian account IDs are opaque to users, but the UI can display the associated username alongside the ID. GitHub usernames can change, but this is rare.

### Regex matching vs simple string matching for mentions

**Chosen: regex with word boundaries.** Simple `strings.Contains(body, "@"+username)` would match substrings (e.g. `@bot` matching `@bottega`). Word boundary regex is slightly more expensive but correct. The overhead is negligible at webhook traffic volumes.

### Filtering all events vs only comment events

**Chosen: filter all events that reach the Router.** Even though only comment events currently pass through, applying rules uniformly means future event types (e.g. PR status changes) would also respect org routing rules. If an org doesn't want noise from non-comment events, they can configure prefix/mention rules that naturally filter them out.

## Open Questions

1. **Rule limits:** Should there be a maximum number of routing rules per org? Unbounded rules could lead to performance issues if an org creates hundreds. A practical limit (e.g. 50) would be reasonable.

2. **Audit logging:** Should routing rule changes be audit-logged? The current system has audit logs for task state changes. Routing rule changes affect event flow and could be important to trace.

3. **Rule testing/preview:** Should there be an API to test a rule against a sample event body without actually creating the rule? This would help users verify their prefix or mention patterns before deploying them.
