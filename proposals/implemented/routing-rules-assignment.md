# Assignment-Triggered Routing Rules

Issue: https://github.com/icholy/gritz/issues/741

## Problem

Routing rules today only react to comment/review events — the `(source, type)` pairs the webhook handlers emit. `extractGitHubWebhookEvent` (`internal/server/webhookserver/github.go:145-205`) recognizes `IssueCommentEvent`, `PullRequestReviewCommentEvent`, and `PullRequestReviewEvent`; `extractAtlassianWebhookEvent` (`internal/server/webhookserver/atlassian.go:117-153`) recognizes `comment_created`. Everything else falls through to `return nil` and is dropped at `internal/server/webhookserver/github.go:44-48` / `internal/server/webhookserver/atlassian.go:70-74`.

There is no way to trigger a rule when a ticket/issue is **assigned**. Assignment is a natural, intentful trigger — especially combined with the create-task work from `proposals/implemented/routing-rules-create-tasks.md` (#717): "assign an issue to the bot → spin up a task for it" is a clean dispatch mechanism that's distinct from comment-mention triggers, and that doesn't require typing a magic body prefix.

This proposal extends the webhook extractors to recognize assignment events, adds an `Assignee` match dimension to `RoutingRule`, and shows how these compose with the create-rule work so an assignment can create a task whose subsequent comments wake it via the existing subscribed-link path.

## Design

### Overview

1. Extend `extractGitHubWebhookEvent` to recognize `IssuesEvent` and `PullRequestEvent` with `action: assigned`, emitting new `(source, type)` pairs.
2. Extend `extractAtlassianWebhookEvent` to recognize `jira:issue_updated` payloads whose `changelog` records an assignee change, emitting an assignment type.
3. Add a new `Assignee` field to `model.RoutingRule` (and its proto mirror) plus a parallel field on `eventrouter.InputEvent`. `MatchRule` gains an `assignee` gate, source-aware in the same way `matchMention` is today.
4. The UI's `EVENT_TYPES` dropdown (`webui/src/lib/routing-rules.ts:11-36`) gains three new labels; the form gains an "Assigned to" field driven by the same source-aware copy as the existing mention field.
5. Composition with `proposals/implemented/routing-rules-create-tasks.md`: an assignment create-rule (`source: github`, `type: pull_request_assigned`, `assignee: icholy-bot`, `create: { workspace, runner, prompt }`) creates a task on the first assignment event, and subsequent comments on the same URL wake it via the existing `attach` path.

### 1. New `(source, type)` pairs

Today the GitHub handler sets `Type: r.Header.Get("X-GitHub-Event")` (`internal/server/webhookserver/github.go:74`), giving raw values like `issue_comment`, `pull_request_review_comment`, `pull_request_review`. Assignment is delivered with `X-GitHub-Event: issues` or `pull_request`, with `action: assigned` distinguishing it from `opened`, `closed`, etc. — so the header alone is *not* sufficient as the routing `Type`.

**Recommendation: synthesize a Type that encodes the action.** The handler keeps using the header verbatim for comment/review deliveries, but the extractor returns a synthetic type for assignment deliveries. This keeps existing rules (`type: issue_comment`) untouched while giving assignment its own selectable type in the UI.

The pairs this change adds:

| Source      | Type                     | Trigger                                                        | URL                  |
|-------------|--------------------------|----------------------------------------------------------------|----------------------|
| `github`    | `issue_assigned`         | `IssuesEvent`, `action == "assigned"`                          | `Issue.HTMLURL`      |
| `github`    | `pull_request_assigned`  | `PullRequestEvent`, `action == "assigned"`                     | `PullRequest.HTMLURL`|
| `atlassian` | `issue_assigned`         | `jira:issue_updated` with an `assignee` changelog item         | `Issue.BrowseURL()`  |

`type` matching in `MatchRule` is exact-string today (`internal/eventrouter/rule.go:17-19`); these new types simply slot in.

#### GitHub extraction

`go-github` v68 declares both event structs in `event_types.go` (vendored at `/root/go/pkg/mod/github.com/google/go-github/v68@v68.0.0/github/event_types.go:721-741` and `:1190-1224`). Both carry `Action *string` and `Assignee *User`, populated on action `"assigned"`. The actor (assigner) is `Sender *User`. New cases added to the switch in `extractGitHubWebhookEvent`:

```go
case *github.IssuesEvent:
    if event.GetAction() != "assigned" ||
        event.Issue == nil || event.Issue.HTMLURL == nil ||
        event.Assignee == nil || event.Assignee.Login == nil ||
        event.Sender == nil || event.Sender.ID == nil {
        return nil
    }
    senderLogin := event.Sender.GetLogin()
    assigneeLogin := event.Assignee.GetLogin()
    number := event.Issue.GetNumber()
    return &githubWebhookEvent{
        eventType:      "issue_assigned",
        description:    fmt.Sprintf("%s assigned issue #%d to @%s", senderLogin, number, assigneeLogin),
        data:           "",
        url:            *event.Issue.HTMLURL,
        githubUserID:   *event.Sender.ID,
        githubUsername: senderLogin,
        assignee:       assigneeLogin,
    }

case *github.PullRequestEvent:
    if event.GetAction() != "assigned" ||
        event.PullRequest == nil || event.PullRequest.HTMLURL == nil ||
        event.Assignee == nil || event.Assignee.Login == nil ||
        event.Sender == nil || event.Sender.ID == nil {
        return nil
    }
    senderLogin := event.Sender.GetLogin()
    assigneeLogin := event.Assignee.GetLogin()
    number := event.PullRequest.GetNumber()
    return &githubWebhookEvent{
        eventType:      "pull_request_assigned",
        description:    fmt.Sprintf("%s assigned PR #%d to @%s", senderLogin, number, assigneeLogin),
        data:           "",
        url:            *event.PullRequest.HTMLURL,
        githubUserID:   *event.Sender.ID,
        githubUsername: senderLogin,
        assignee:       assigneeLogin,
    }
```

Two structural changes to `internal/server/webhookserver/github.go` follow from this:

1. `githubWebhookEvent` (`:137-143`) gains an `eventType string` field and an `assignee string` field. The existing cases (`IssueCommentEvent`, `PullRequestReviewCommentEvent`, `PullRequestReviewEvent`) set `eventType` to the header value verbatim — i.e., the extractor takes over what the handler does today, so all branches go through one path.
2. The handler (`:71-79`) reads the type from `extracted.eventType` instead of `r.Header.Get("X-GitHub-Event")`, and copies `extracted.assignee` into `eventrouter.InputEvent.Assignee` (§3).

The header is still read once at the top of `ServeHTTP` for the existing log line (`:46`), but it stops being authoritative for `Type`.

**Security boundary unchanged.** The actor is `Sender`, not `Assignee`. The existing `GetUserByGitHubUserID(extracted.githubUserID)` lookup (`:52`) keeps gating the event: only a sender who is a linked gritz user can produce an `InputEvent`. The assignee is just a match key — they need not be linked.

#### Atlassian extraction

Atlassian webhooks deliver `jira:issue_updated` with a `changelog` block listing per-field deltas. The current `WebhookPayload` struct (`internal/x/atlassian/webhook.go:13-17`) is intentionally minimal — only `webhookEvent`, `comment`, `issue` — so it needs extension:

```go
type WebhookPayload struct {
    WebhookEvent string     `json:"webhookEvent"`
    Comment      *Comment   `json:"comment"`
    Issue        *Issue     `json:"issue"`
    User         *User      `json:"user"`      // actor for non-comment events
    Changelog    *Changelog `json:"changelog"` // present on jira:issue_updated
}

type Changelog struct {
    Items []ChangelogItem `json:"items"`
}

type ChangelogItem struct {
    Field      string `json:"field"`       // e.g. "assignee"
    From       string `json:"from"`        // previous assignee account id (may be empty)
    To         string `json:"to"`          // new assignee account id (empty on unassign)
    FromString string `json:"fromString"`  // previous display name
    ToString   string `json:"toString"`    // new display name
}
```

`extractAtlassianWebhookEvent` gains a case keyed on the webhook event and an assignee changelog item:

```go
case "jira:issue_updated":
    if payload.Issue == nil || payload.User == nil || payload.Changelog == nil {
        return nil, nil
    }
    var assigneeChange *ChangelogItem
    for i := range payload.Changelog.Items {
        if payload.Changelog.Items[i].Field == "assignee" {
            assigneeChange = &payload.Changelog.Items[i]
            break
        }
    }
    if assigneeChange == nil || assigneeChange.To == "" {
        // Either a non-assignee field changed, or the issue was unassigned;
        // both are out of scope for v1.
        return nil, nil
    }
    if payload.User.AccountID == "" {
        return nil, nil
    }
    url := payload.Issue.BrowseURL()
    if url == "" {
        return nil, nil
    }
    description := fmt.Sprintf("%s assigned %s to %s",
        payload.User.DisplayName, payload.Issue.Key, assigneeChange.ToString)
    return &atlassianWebhookEvent{
        eventType:          "issue_assigned",
        description:        description,
        data:               "",
        url:                url,
        atlassianAccountID: payload.User.AccountID, // the actor (assigner)
        assignee:           assigneeChange.To,      // new assignee account id
    }, nil
```

`atlassianWebhookEvent` (`internal/server/webhookserver/atlassian.go:109-115`) gains an `assignee string` field.

**Reassignment.** A "change of assignee" (non-empty `From` and `To`) goes through the same branch — `To != ""` is the only gate. Unassignment (`To == ""`) is deliberately dropped in v1; it doesn't correspond to "spin up a task" and no clear matching semantics ("assigned to nobody" is a different domain).

**Security boundary unchanged.** The actor is `payload.User` (the user who edited the issue, per Jira's webhook contract); `GetUserByAtlassianAccountID(extracted.atlassianAccountID)` (`:77`) still gates routing. The assignee is the match target only.

### 2. The Atlassian event-type naming question

Jira's wire format prefixes its event identifiers with `jira:` (e.g. `jira:issue_updated`), but the existing Atlassian rules use the bare `comment_created`. This proposal keeps that convention: the routing `Type` is `issue_assigned`, not `jira:issue_updated.assignee`. The match dimension users care about — "issue got an assignee" — is what they pick from the UI.

### 3. `Assignee` match dimension

Survey of how mentions match today (`internal/eventrouter/rule.go:31-42`):

```go
func (e InputEvent) matchMention(mention string) bool {
    switch e.Source {
    case "github":
        pattern := `(?i)(?:^|[\s(])@` + regexp.QuoteMeta(mention) + `(?:$|[\s,.)!?])`
        matched, _ := regexp.MatchString(pattern, e.Data)
        return matched
    case "atlassian":
        return strings.Contains(e.Data, "[~accountid:"+mention+"]")
    default:
        return false
    }
}
```

`matchMention` is a *text* check against `e.Data` — the comment body. For GitHub it greps for `@<login>`; for Atlassian it greps for the `[~accountid:…]` macro.

Assignment is fundamentally different: it's a **structured-payload** match against the assignee field. The shape of the identifier happens to be the same (login for GitHub, account id for Atlassian), but the meaning is not. A rule combining both ("PR assigned to me AND a comment mentions me") is a legitimate composition we want to allow.

**Recommendation: add a new `Assignee` field.** Reusing `Mention` would conflate the two semantics, would make the existing `matchMention`'s text-grep behaviour wrong for assignment events (the data field is empty for assignment), and would close off the AND-composition above. The added field is cheap: it's the same shape as `Mention`, the same source-aware identifier semantics, and the same JSON-storage round-trip applies (see §6).

#### Model and proto changes

`internal/model/routing_rule.go`:

```go
type RoutingRule struct {
    Source   string `json:"source,omitempty"`
    Type     string `json:"type,omitempty"`
    Prefix   string `json:"prefix,omitempty"`
    Mention  string `json:"mention,omitempty"`
    Assignee string `json:"assignee,omitempty"` // NEW
}
```

`RoutingRule.Proto()` and `RoutingRuleFromProto()` (`:17-34`) gain the field, plumbing it both directions.

`proto/gritz/v1/gritz.proto:532-537`:

```protobuf
message RoutingRule {
  string source = 1;
  string type = 2;
  string prefix = 3;
  string mention = 4;
  string assignee = 5;  // NEW
}
```

The JSON column `orgs.routing_rules` (unmarshalled in `GetOrgRoutingRules`, `internal/store/org.go:191`) absorbs new optional fields without a migration — `json.Unmarshal` skips unknown fields when decoding old rows, and the new field is `omitempty` when re-encoding.

#### InputEvent and MatchRule

`eventrouter.InputEvent` (`internal/eventrouter/eventrouter.go:17-25`) gains a parallel field:

```go
type InputEvent struct {
    Source      string
    Type        string
    Description string
    Data        string
    URL         string
    UserID      string
    Assignee    string // populated for assignment events; empty otherwise
}
```

`MatchRule` (`internal/eventrouter/rule.go:13-27`) gains an `assignee` gate. Source-aware comparison parallels `matchMention`:

```go
if rule.Assignee != "" && !e.matchAssignee(rule.Assignee) {
    return false
}

func (e InputEvent) matchAssignee(assignee string) bool {
    if e.Assignee == "" {
        return false
    }
    switch e.Source {
    case "github":
        return strings.EqualFold(e.Assignee, assignee)
    case "atlassian":
        return e.Assignee == assignee
    default:
        return false
    }
}
```

GitHub logins are case-insensitive; Jira account ids are opaque strings, exact-match. This mirrors how `matchMention`'s GitHub regex uses `(?i)` while its Atlassian branch is `strings.Contains` over the literal account id.

**Empty Assignee on a non-assignment event = no match.** A rule with `assignee: "icholy-bot"` will only match events whose `InputEvent.Assignee` is set — i.e., assignment events. This is the right default: `assignee` is the discriminator that says "I only want assignment-shaped events".

### 4. Security boundary

The full chain:

- **Assigner** = event actor = `Sender` (GitHub) / `payload.User` (Atlassian). Resolved to an gritz user at the handler level (`github.go:52`, `atlassian.go:77`). Same lookup, same `sql.ErrNoRows` drop, same audit posture.
- **Assignee** = match target. Just an identifier string. Not resolved, not validated against the user table.

Assignment introduces no new bypass. An unlinked user assigning issues cannot route events — they fail the `GetUserByGitHubUserID` / `GetUserByAtlassianAccountID` lookup exactly like an unlinked commenter does today. The "linked gritz user required" invariant from `proposals/implemented/routing-rules-create-tasks.md` §1 carries over verbatim.

A subtle consequence worth naming: an assignment create-rule (§5) means **a linked user can assign-create a task targeting a different account** (the rule's `assignee` value). That's intentional and useful — "assign to the bot account" is the canonical use case — but it means the assignee value in the rule is a trust statement by whoever configured the rule. This is no different from a `mention` rule trusting that the configured username is the bot's.

### 5. Composition with create-task rules (#717)

`proposals/implemented/routing-rules-create-tasks.md` adds `Create *CreateTaskAction` to `RoutingRule` and reworks `Route` to (a) load member orgs' rules, (b) take the first matching rule per org, (c) if a subscribed link exists for the URL → wake, otherwise (d) if the matched rule has `Create` set → create-task-with-link in one tx.

Assignment rules plug in unmodified. A rule like:

```jsonc
{
  "source": "github",
  "type": "pull_request_assigned",
  "assignee": "icholy-bot",
  "create": {
    "workspace": "gritz",
    "runner": "default",
    "prompt": "Review this PR and leave inline comments."
  }
}
```

means: when an assignment event for this org's user assigns icholy-bot to a PR, create a task in workspace `gritz` and subscribe its created `Link` to the PR's HTML URL. The lifecycle continues via the existing wake path:

1. First event for `https://github.com/icholy/gritz/pull/N` is `pull_request_assigned`. The wake-path link lookup (`FindSubscribedLinksForOrgs`) returns no links. The matched rule has `Create` set → `r.create` opens a tx, re-checks (dedup), creates `Task`, creates `Link{URL: PR, Subscribe: true, Relevance: "trigger"}`, commits. (§5 of the create-tasks proposal.)
2. Subsequent comment on the PR is delivered as `issue_comment`. The same `Route` call finds the subscribed link from step 1, matches a wake-shaped rule (`source: github`, `type: issue_comment`, or `defaultRules`' `gritz:` prefix), goes through `attach` (`internal/eventrouter/eventrouter.go:117-156`), wakes the task. No second task is created.

The two pieces interlock cleanly because the dedup key is the URL of the resource, and assignment and comment events for the same PR share that URL.

**Two rules per org if you want both create-on-assign and wake-on-comment.** Example ordering:

```jsonc
[
  { "source": "github", "type": "pull_request_assigned",
    "assignee": "icholy-bot",
    "create": { "workspace": "gritz", "runner": "default", "prompt": "Review." } },
  { "source": "github", "type": "issue_comment", "prefix": "gritz:" }
]
```

Both rules are needed because "first matching rule per org" (create-tasks proposal §4b) selects exactly one. The assignment event matches rule #1 (and creates); the later comment event matches rule #2 (and wakes via the link rule #1 just planted). The two never compete on the same event.

### 6. UI impact

`webui/src/lib/routing-rules.ts` `EVENT_TYPES` (`:11-36`) gains three options:

```ts
{ id: 'github:issue_assigned',         label: 'GitHub: Issue Assigned',  source: 'github',    type: 'issue_assigned' },
{ id: 'github:pull_request_assigned',  label: 'GitHub: PR Assigned',     source: 'github',    type: 'pull_request_assigned' },
{ id: 'atlassian:issue_assigned',      label: 'Jira: Issue Assigned',    source: 'atlassian', type: 'issue_assigned' },
```

The friendlier source-aware form (`webui/src/components/routing-rule-form.tsx`, mid-redesign per the create-tasks proposal §8) gains an "Assigned to" field, visible only when the selected event type is an assignment one. Copy is source-aware like `mentionCopyForSource` (`webui/src/lib/routing-rules.ts:74-95`):

- GitHub: label "Assigned to user", placeholder "icholy-bot", help "GitHub username (no leading @). Matches the new assignee on assignment events."
- Atlassian: label "Assigned to account", placeholder "5b10ac8d82e05b22cc7d4ef5", help "Atlassian account ID of the new assignee."

A small `assigneeCopyForSource` helper next to `mentionCopyForSource` keeps the pattern consistent. **This proposal does not design the field's layout** — it confirms the new shape fits the existing source-aware form pattern, and notes that the field is hidden when the event type doesn't involve assignment (similar in spirit to how the mention field's relevance depends on type).

A `Mention` field and an `Assignee` field can coexist on a single rule. The form should render both when the selected event type plausibly supports both (none of the v1 types currently do — assignment events have no body to mention against — but the form shouldn't preclude the future state).

### 7. Test plan sketch

Tests live next to existing ones:

- **GitHub extractor** — `internal/server/webhookserver/github_test.go` (if absent, create it). Cases:
  - `IssuesEvent` with `action: assigned` produces `eventType: "issue_assigned"`, URL = issue HTML URL, `githubUserID` = sender id, `assignee` = assignee login, description includes both names.
  - `PullRequestEvent` with `action: assigned` analogous.
  - `IssuesEvent` with `action: opened` → `nil` (out of scope).
  - `IssuesEvent` with `action: assigned` but `Sender == nil` → `nil` (no actor → no routing).
- **Atlassian extractor** — `internal/server/webhookserver/atlassian_test.go`. Cases:
  - `jira:issue_updated` with a single `changelog.items` entry for `field: "assignee"`, `to: "<accountId>"` → `eventType: "issue_assigned"`, `assignee` = `to`, actor = `payload.User`.
  - Same payload with `to: ""` (unassign) → `nil`.
  - `jira:issue_updated` with only non-assignee changelog items → `nil`.
  - `comment_created` keeps its existing behaviour (regression guard).
- **Assignee matching** — `internal/eventrouter/rule_test.go`. Table-driven:
  - GitHub: rule `assignee: "icholy-bot"` matches `Assignee: "icholy-bot"` and `Assignee: "Icholy-Bot"` (case-insensitive), does not match `Assignee: "octocat"`, does not match empty `Assignee` (non-assignment event).
  - Atlassian: rule `assignee: "5b10ac…"` matches exact, does not match a different account id, does not match case-fold.
  - Combined-gate: rule `source: github, type: pull_request_assigned, assignee: icholy-bot` matches only when all three are set on the event.
- **Composition with create-rules** — extends `internal/eventrouter/eventrouter_test.go` (the create-tasks proposal adds suites here). Cases:
  - **Assign → create.** Org has a create-rule for `pull_request_assigned` with `assignee: icholy-bot`. Fire a `pull_request_assigned` event with matching assignee. Assert one task + one subscribed link were created (single transaction).
  - **Assign-create → comment-wake.** Same setup. Fire the assignment first (task created). Then fire an `issue_comment` event on the same URL whose body starts with `gritz:`. Assert no second task is created and the existing task transitions through `attach`.
  - **Wrong assignee no-ops.** Same rule. Fire `pull_request_assigned` with `assignee: someone-else`. Assert no task created, no error.
  - **Unlinked assigner drops.** Fire a `pull_request_assigned` whose sender is not in `users.github_user_id`. Assert handler returns `"no linked account"`, `Route` is never called. (Belongs in `webhookserver` tests, not router.)

The redelivery dedup test in the create-tasks proposal already covers assignment redeliveries — the URL is the dedup key, and the assignment URL is the same `Issue.HTMLURL` / `PullRequest.HTMLURL` / `Issue.BrowseURL()` used by other event types.

## Trade-offs

### `Assignee` as a new field vs reusing `Mention`

**Chosen: new field.** Reusing `Mention` is a one-field-fewer trick, not a simplification:

- `matchMention` greps `e.Data` — assignment events have empty `Data`. A `Mention`-reusing implementation would need to branch internally on `Type`, doing assignee-payload matching for `_assigned` types and text-matching otherwise. That branch leaks the new semantic into the old function and complicates the dropping back-compat story for existing mention-only rules.
- `Mention` and `Assignee` can compose. Once we add other actor-vs-target dimensions (review_requested, label_added) the modeling pressure only grows. Starting with a dedicated field keeps that future cheap.
- Storage cost is one optional JSON field. UI cost is one conditional input. Both are low.

### Synthetic `_assigned` type vs adding an `action` match field

**Chosen: synthetic type.** The "raw header → `Type`" pattern is a happy coincidence — comment-type webhooks happen to be self-describing (`issue_comment` is the action). For `issues`/`pull_request`, the header is too coarse and rules `type: "issues"` would match every issue webhook (opened, closed, labeled). Two options:

1. Synthesize `issue_assigned` / `pull_request_assigned` in the extractor.
2. Add a new `Action` field on `RoutingRule` and `InputEvent`, keep `Type` = raw header.

Option 2 is more general but multiplies the UI surface (event-type dropdown plus an action sub-dropdown) and forces every existing rule to grow an extra field even though for comment types `Action == Type` would be redundant. Option 1 keeps the dropdown one-dimensional and matches Atlassian's existing pattern (`comment_created` is its own type, not `issue_updated` + `action: comment`). If the surface of "issues" sub-actions ever justifies a real `Action` field, that's an additive change — `issue_assigned` becomes `issues` + `action: assigned` with a backfill, and a v2 of the rule shape adopts it.

### Drop unassign vs handle it

**Chosen: drop unassign for v1.** "Issue was unassigned from X" doesn't map to "create a task" cleanly and has no clear matching semantics (`assignee: "(none)"`?). If a use case emerges (e.g., a wake rule that re-prompts the bot when it's removed from a ticket) we add a `type: issue_unassigned` extractor case later. The change is additive.

### Single assignee vs assignees array

**Chosen: single assignee per event.** GitHub's `assigned` action fires per-assignee — adding three assignees to an issue produces three webhook deliveries, each with one `Assignee` field. So `InputEvent.Assignee string` matches the wire reality. Jira's changelog also gives one `to` per assignee change. If a future webhook type carries a multi-assignee diff, `Assignee` becomes a slice in a backward-compatible JSON change.

## Open Questions

1. **Atlassian `Comment.Author` shape vs new `payload.User`.** The Atlassian extractor currently reads the actor from `Comment.Author` (`webhook.go:22`, `atlassian.go:130`). The new `jira:issue_updated` branch reads it from a top-level `payload.User`. Jira's webhook contract does put the actor at the top level for non-comment events, but worth confirming against a real `jira:issue_updated` payload during implementation (the existing `webhook_test.go` doesn't cover this case yet).
2. **Should `defaultRules` recognize assignment?** Current default is a single `Prefix: "gritz:"` wake rule (`internal/eventrouter/eventrouter.go:35-37`). Adding a default assignment rule would mean "every org gets assign-to-anyone wake behaviour out of the box", which is probably the wrong default — assignees vary per org. Leave `defaultRules` alone; assignment is opt-in via configured rules.
3. **`Data` on assignment events: empty vs issue title.** The proposal leaves `Data` empty (no comment body). Putting the issue title there would make a body-`prefix` filter ("only assignments on issues whose title starts with [bug]") possible, but feels like overloading the field. Leave empty for v1; revisit if a real use case appears.
4. **Wording of the auto-generated preamble for assign-created tasks.** The create-tasks proposal §6 sketches a preamble; for assignment it should probably name both the assigner and the assignee, e.g. *"You were created by a routing rule because @octocat assigned this PR to @icholy-bot."* Exact wording belongs in implementation review.