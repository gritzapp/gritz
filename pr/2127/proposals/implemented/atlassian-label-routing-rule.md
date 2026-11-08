# Atlassian Routing Rule for Jira Label Added

Issue: https://github.com/icholy/gritz/issues/809

## Problem

Users want to trigger an gritz task by adding a label to a Jira issue (e.g.
add an `gritz` label to a ticket and have an agent pick it up). Today the
Atlassian integration only reacts to comments, so there is no way to start a
task from a label change. Issue #809 asks for this and explicitly requests a
proposal first, including the open question of whether the Jira webhook even
carries label changes.

It does. Jira fires a `jira:issue_updated` webhook whenever an issue field
changes, and the payload contains a `changelog` listing each changed field as a
`from`/`fromString` → `to`/`toString` item. Label changes appear as a changelog
item with `field == "labels"`, where `toString` is the new space-separated label
set and `fromString` is the previous one; the added labels are the set
difference.

## Current State

### What Atlassian events are parsed

`internal/x/atlassian/webhook.go` models only a thin slice of the payload:

```go
type WebhookPayload struct {
    WebhookEvent string   `json:"webhookEvent"`
    Comment      *Comment `json:"comment"`
    Issue        *Issue   `json:"issue"`
}
```

There is no `changelog`, no top-level `user`, and `IssueFields` carries only
`Summary`.

`internal/server/atlassianserver/webhook.go` converts the payload to an
`eventrouter.InputEvent` in `toInputEvent`. It handles exactly one event type:

```go
const (
    EventTypeCommentCreated = "comment_created"
)
```

The `switch payload.WebhookEvent` has a single `case EventTypeCommentCreated`.
A `jira:issue_updated` payload falls through to `return nil, nil`, i.e. it is
silently ignored. So label-add events are not surfaced at all today.

This is the same gap the assignment work
(`proposals/implemented/routing-rules-assignment.md`, #741) called out for
Jira: that proposal added the GitHub assignment path
(`internal/server/githubserver/webhook.go`, `EventTypeIssueAssigned` /
`EventTypePullRequestAssigned`) but explicitly **deferred** the Atlassian
`jira:issue_updated` extractor. The leftover marker is in
`internal/eventrouter/rule.go`:

```go
case "atlassian":
    // Jira assignment matching is deferred — extractor does not emit
    // assignment events yet.
    return false
```

So no `jira:issue_updated` parsing exists on the Atlassian side at all.

### Routing-rule match fields

`model.RoutingRule` (`internal/model/routing_rule.go`) and its proto
(`proto/gritz/v1/gritz.proto`, `message RoutingRule`, fields 1–7) already
expose: `Source`, `Type`, `Prefix`, `Mention`, `Assignee`, `URLPrefix`, plus a
`Create` action (`CreateTaskAction`). Matching lives in `InputEvent.MatchRule`
(`internal/eventrouter/rule.go`): each non-empty rule field must equal (or
prefix-match) the corresponding `InputEvent` field; empty fields are wildcards.
`Prefix`/`Mention` are checked against `InputEvent.Data`, `URLPrefix` against
`InputEvent.URL`, `Type`/`Source` are exact-string, and `Assignee` is matched
via the source-specific `matchAssignee`.

### The gap

1. No `jira:issue_updated` parsing, so label additions never become an
   `InputEvent`.
2. Even if they did, there is no `InputEvent.Type` value for "label added", and
   no rule field that targets a *specific* label value (e.g. only trigger on
   `gritz`, not on any label).

## Design

### 1. Parse `jira:issue_updated` and detect added labels

Extend `internal/x/atlassian/webhook.go` (this is also the struct the deferred
assignment work will want, so the shape is chosen to serve both):

```go
type WebhookPayload struct {
    WebhookEvent string     `json:"webhookEvent"`
    Comment      *Comment   `json:"comment"`
    Issue        *Issue     `json:"issue"`
    User         *User      `json:"user"`      // actor of the update
    Changelog    *Changelog `json:"changelog"` // present on jira:issue_updated
}

type Changelog struct {
    Items []ChangelogItem `json:"items"`
}

type ChangelogItem struct {
    Field      string `json:"field"`      // e.g. "labels"
    FromString string `json:"fromString"` // previous space-separated labels
    ToString   string `json:"toString"`   // new space-separated labels
}

// AddedLabels returns labels present in ToString but not FromString.
func (c *Changelog) AddedLabels() []string { ... }
```

Labels in Jira changelog items are space-separated strings; `AddedLabels` splits
both sides on whitespace and returns the set difference (new − old). `User`
reuses the existing `User` type (`AccountID`, `DisplayName`).

### 2. New `InputEvent.Type` value

Add to the event-type contract block in
`internal/server/atlassianserver/webhook.go`:

```go
const (
    EventTypeCommentCreated = "comment_created"
    EventTypeLabelAdded     = "label_added"
)
```

`label_added` is source-scoped (rules already match on `Source == "atlassian"`
alongside `Type`), so it stays short and symmetric with the existing
`comment_created` rather than introducing a `jira_`-prefixed name. This mirrors
the precedent set by `routing-rules-assignment.md` §2, which deliberately uses
the bare `issue_assigned` rather than Jira's wire `jira:issue_updated`.

In `toInputEvent`, add a `case "jira:issue_updated"` (Jira's real
`webhookEvent`). For each added label it emits one `InputEvent`:

```go
&eventrouter.InputEvent{
    Source:      "atlassian",
    Type:        EventTypeLabelAdded,
    Description: fmt.Sprintf("%s added label %q to %s",
        payload.User.DisplayName, label, payload.Issue.Key),
    Data:        label,                 // the label value (see §3)
    URL:         payload.Issue.BrowseURL(),
    Meta:        AtlassianMeta{AuthorAccountID: payload.User.AccountID,
        AuthorDisplayName: payload.User.DisplayName},
}
```

The actor is `payload.User` (the person who edited the issue), used the same way
the comment path uses `payload.Comment.Author`: to resolve the gritz owner via
`GetUserByAtlassianAccountID` in `ServeHTTP`. Because `toInputEvent` currently
returns a single `*InputEvent`, supporting multiple added labels requires
changing its signature to `([]*eventrouter.InputEvent, error)` and looping the
`Route` call in `ServeHTTP` (the owner lookup is per-actor, so it can be done
once and applied to each event). Recommended: return a slice — cleaner than a
second parse, and the comment case simply returns a one-element slice.

### 3. Targeting a specific label in a routing rule

**Decision: reuse the existing `Prefix` field; do not add a new match field.**

`MatchRule` already compares `rule.Prefix` as a prefix of `InputEvent.Data`. By
putting the label value in `InputEvent.Data`, a rule can target a specific label
with fields that already exist:

```
Source = "atlassian"
Type   = "label_added"
Prefix = "gritz"        // empty = any label
```

This needs no model/proto/migration/UI change and matches the established
pattern where `Prefix`/`Mention` filter on `Data`. The trade-off is that
`Prefix` is a prefix, not an exact match, so `Prefix = "gritz"` would also match
a label `gritz-urgent`. Given labels are short identifiers, this is acceptable
and arguably useful; an exact-match field can be added later if needed.

Alternative considered — a dedicated `Label` match field on `RoutingRule` (model
+ proto field 8 + `MatchRule` clause + `webui/src/components/routing-rule-form.tsx`
+ `webui/src/lib/routing-rules.ts` + proto regen for both `webui` and `n8n-node`):
clearer intent and exact matching, but more surface area for a value the existing
`Prefix` field already handles. Recommend starting with `Prefix` reuse and
revisiting only if exact matching is required. (Note: unlike the assignment work,
which needed a new `Assignee` field because assignment matches a structured
payload field with empty `Data`, label-add naturally carries its value in `Data`,
so `Prefix` reuse is a genuine fit here, not a hack.)

### 4. Task context passed to the agent

Both routing paths consume the same `InputEvent` fields:

- **Wake path** (`Router.attach` via a subscribed link): `Description`, `Data`,
  `URL` are copied into a `model.Event` and attached to the task.
- **Create path** (`Router.create`, when the matched rule has a
  `CreateTaskAction`): the task gets a preamble instruction
  `"You were created by a routing rule in response to a atlassian label_added
  event."` plus the optional `Create.Prompt`, and a subscribed `Link` to the
  issue URL.

With the values from §2 the agent sees which label was added, the issue key, and
the issue `browse` URL, which is enough to call the Atlassian MCP tools
(`jira_get_issue`, etc.) for full context.

### 5. Idempotency / dedup

`jira:issue_updated` fires on every field change and can fire repeatedly, so
duplicate triggers are the main risk:

- Emitting only the **set difference** (in `toString`, not in `fromString`)
  means re-saving an issue without changing labels produces no `labels`
  changelog item and therefore no event.
- For the **wake** path, `Router.Route` looks up subscribed links by URL
  (`FindSubscribedLinksForOrgs`) and de-dups tasks; re-firing simply re-wakes the
  same task, which is idempotent by design.
- For the **create** path, `Router.create`'s doc comment already describes the
  existing dedup: once the first create commits a subscribed `Link` for the URL,
  the next event for that URL takes the wake path. So add-label → (task created
  with link) → add another label on the same issue → wakes the same task. The
  known v1 limitation (genuinely-concurrent overlapping txns) carries over
  unchanged; no new dedup work is required for labels.

A residual edge: removing the label and re-adding it later fires a fresh
`label_added`, which correctly re-wakes (or, if the prior task was deleted,
re-creates). That matches existing comment/assignment behaviour.

## Trade-offs

- **Polling instead of webhooks** (cf. the `gritz jira` poller): would avoid
  webhook setup but adds latency and load and duplicates the existing
  webhook-based comment path. Rejected — the webhook already exists and carries
  the data.
- **Generic `issue_updated` rule + agent-side filtering**: emit a single
  `issue_updated` event and let the agent decide. Rejected — pushes routing
  logic into prompts and triggers tasks on unrelated field changes.
- **Reuse the comment-trigger mechanism** (ask users to comment a magic
  string): already possible today, but is exactly the workflow #809 wants to
  avoid; labels are the requested UX.
- **New `Label` field vs `Prefix` reuse**: see §3. Reuse chosen for minimal
  surface area; the label value lives naturally in `Data`.

## Implementation Sketch

- [ ] `internal/x/atlassian/webhook.go`: add `User`, `Changelog`,
      `ChangelogItem`, and `(*Changelog).AddedLabels()`; tests in
      `internal/x/atlassian/webhook_test.go`.
- [ ] `internal/server/atlassianserver/webhook.go`: add `EventTypeLabelAdded`;
      handle `case "jira:issue_updated"` in `toInputEvent`; emit one
      `InputEvent` per added label; change `toInputEvent` to return a slice and
      loop `Route` in `ServeHTTP`.
- [ ] `internal/server/atlassianserver/webhook_test.go`: fixture for a
      `jira:issue_updated` payload with a `labels` changelog item; assert one
      `label_added` event per added label with `Data` = label value and
      `URL` = browse URL.
- [ ] No `model.RoutingRule` / proto / migration / UI change (reusing
      `Prefix`). Document `Source=atlassian`, `Type=label_added`,
      `Prefix=<label>` as the rule recipe; optionally add a friendly
      `atlassian:label_added` entry to `webui/src/lib/routing-rules.ts`
      `EVENT_TYPES`.
- [ ] (Optional, deferred) dedicated `Label` match field for exact matching:
      proto field 8, `model.RoutingRule` + `*FromProto`/`Proto`, `MatchRule`,
      and the webui form.

## Open Questions

- Should `jira:issue_updated` also surface label **removals** (e.g.
  `label_removed`) for symmetry, or is add-only enough for #809? (Add-only
  proposed; removal is an additive follow-up.)
- Confirm the actor location: the proposal reads the editor from top-level
  `payload.User`. This is the same assumption `routing-rules-assignment.md` made
  for `jira:issue_updated`; worth validating against a real payload during
  implementation since the existing `webhook_test.go` does not yet cover it.
- Is prefix-matching on the label acceptable, or do we need exact match from day
  one? (Prefix reuse now, exact field later if needed.)
- Should this PR also land the deferred Jira **assignment** extractor at the same
  time, since both require the same `Changelog`/`User` parsing additions to
  `internal/x/atlassian/webhook.go`? They are independent features but share the
  parsing plumbing.