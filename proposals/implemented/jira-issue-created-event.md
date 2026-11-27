# Jira: Issue Created Event

Issue: https://github.com/gritzapp/gritz/issues/2219

## Problem

Some Jira issues are filed by automation rather than people: a security scanner
opening one ticket per finding, an alerting tool opening one per incident. Each
new ticket should be able to start a gritz task.

gritz can't see a new issue today. `toInputEvents` in
[`internal/server/atlassianserver/webhook.go`](https://github.com/gritzapp/gritz/blob/master/internal/server/atlassianserver/webhook.go)
handles two `webhookEvent` values:

- `comment_created` → Jira: Issue Comment
- `jira:issue_updated` → Jira: Label Added and/or Jira: Issue Assigned, derived
  from the payload's `changelog`

Every other `webhookEvent`, including `jira:issue_created`, falls through to
`return nil, AtlassianMeta{}, nil` and the handler answers `ignored`.

Setting a label or assignee while creating the issue doesn't help. Jira sends
that as a single `jira:issue_created` with the fields already set and no
`changelog`, so `AddedLabels` and `NewAssignee` both return nil and neither
existing event fires. A person has to touch the ticket afterwards before gritz
sees it.

## Design

A new built-in event type, **Jira: Issue Created** (`source: atlassian`,
`type: issue_created`), emitted once per `jira:issue_created` webhook. Routing
rules narrow it with conditions as usual; an event that matches no rule creates
no task.

The existing Jira events are unchanged. No polling, and nothing is posted back
to Jira.

### Payload parsing

[`internal/x/atlassian/webhook.go`](https://github.com/gritzapp/gritz/blob/master/internal/x/atlassian/webhook.go)
gains the `jira:issue_created` constant, and `IssueFields` gains the two fields
the event reads at creation time:

- `Labels []string` (`json:"labels"`): the labels the issue was created with.
- `Assignee *User` (`json:"assignee"`): the assignee it was created with. Jira
  sends `"assignee": null` for an unassigned issue, which decodes to `nil`.

`payload.user` is already parsed (`WebhookPayload.User`) and, for this webhook,
is the issue's creator.

### The event

A new `case atlassian.WebhookEventIssueCreated` in `toInputEvents`, built the
same way as the `jira:issue_updated` branch:

- **Ignored** (no events) when `payload.Issue` or `payload.User` is nil, the
  account ID is empty, or `Issue.BrowseURL()` is empty, matching the other
  branches.
- **Description:** `"<creator display name> created <ISSUE-KEY>"`.
- **Data:** the issue's description, trimmed.
- **URL:** `Issue.BrowseURL()`.
- **Meta:** `AtlassianMeta{AuthorAccountID, AuthorDisplayName}` from the
  creator.

Attributes, beside the derived `body` and `url`:

| Attr | Label | Value |
| --- | --- | --- |
| `body` | Issue Description | The issue's description (derived). |
| `url` | Issue URL | The issue's URL (derived). |
| `label` | Label | Every label the issue was created with; empty when none. |
| `assignee` | Assignee | Account ID of the assignee it was created with; empty when unassigned. |
| `user` | User | Account ID of who created it. |

`label` and `assignee` cover the "created already labelled or assigned" case
that Label Added and Issue Assigned can't see, so one rule (`label equals
gritz`) can be paired with a second on Issue Created to catch both paths.

### Routing an unlinked creator

Automation files issues as an integration user, which has no linked gritz
account. The handler already handles this: `GetUserByAtlassianAccountID`
returns `sql.ErrNoRows`, `UserID` stays empty, and the event routes on
`Orgs: [orgID]`, so only [public rules](/concepts/agents/#members-and-public)
act on it. No handler change is needed; a test pins the behaviour for this
event type.

### Filtering

The event fires for every new issue the webhook carries, so narrowing happens
in two places:

1. **Jira's webhook JQL filter.** The filter applies to every event a webhook
   sends, so narrowing an existing webhook to one project's new issues would
   also drop that webhook's comments and updates. The operator instead creates
   a second webhook with the same URL and secret, subscribed only to
   **Issue → created**, with its own JQL (e.g. `project = SEC AND reporter =
   "<integration user>"`).
2. **Routing rule conditions.** e.g. `url prefix …/browse/SEC-` and
   `user equals <integration user's account ID>`, with **Public** on. Rules are
   ordered and the first match wins, so it goes before broader Jira rules.

### Schema

[`internal/server/atlassianserver/schema.go`](https://github.com/gritzapp/gritz/blob/master/internal/server/atlassianserver/schema.go)
registers "Jira: Issue Created" with attrs ordered `body`, `url`, `label`,
`assignee`, `user`, each `AttrDef` written inline with copy for this event, as
the file does for the others. The webui reads event types from the schema
registry, so no frontend change.

### Docs

- `reference/event-types`: an `issue_created` section under **Jira** with the
  attribute table.
- `integrations/jira`:
  - Mention new issues in the intro.
  - Webhook setup subscribes **Issue → created** as well as **updated**.
  - A short paragraph on narrowing one event with a second webhook (same URL
    and secret, its own events and JQL).
  - Events table row: `Issue created | Jira: Issue Created | the issue`.
  - Labels or an assignee set at creation arrive on Issue Created, not as Label
    Added or Issue Assigned.
  - Example: a public rule that starts a task for issues an integration user
    files in one project.

### API

```go
package atlassian // github.com/gritzapp/gritz/internal/x/atlassian

// WebhookEventIssueCreated is the webhook event Jira sends when an issue is created. // new
const WebhookEventIssueCreated = "jira:issue_created"

// IssueFields represents the fields of a Jira issue.
type IssueFields struct {
	Summary     string   `json:"summary"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`   // new
	Assignee    *User    `json:"assignee"` // new
}
```

```go
package atlassianserver // github.com/gritzapp/gritz/internal/server/atlassianserver

// EventTypeIssueCreated is the event type emitted for a newly created issue. // new
const EventTypeIssueCreated = "issue_created"
```

**How it fits:** `WebhookHandler.ServeHTTP` calls `toInputEvents`, whose new
case reads `Issue.Fields.Labels` and `Issue.Fields.Assignee` and returns one
`issue_created` event; the handler routes it exactly like the others.
`RegisterSchemas` registers its schema on the default registry at `init`.

## Implementation Plan

One feature PR is enough; the change is a few dozen lines. If split:

1. **Parse creation fields** — Delivers: `WebhookEventIssueCreated`,
   `IssueFields.Labels` and `IssueFields.Assignee`. Depends on: nothing.
   Verifiable by: a `internal/x/atlassian` test that parses raw
   `jira:issue_created` JSON once with `"assignee": null` and once with an
   assignee object, checking `Labels` and `Assignee`.
2. **Emit and register Jira: Issue Created, with docs** — Delivers: the
   `toInputEvents` case, the schema, and the docs pages above. Depends on: (1).
   Verifiable by:
   - `TestToAtlassianInputEvents` cases: created labelled and assigned; created
     unlabelled and unassigned (both attrs empty); no user (ignored).
   - A handler test: an issue created by an unlinked account routes with an
     empty `UserID` and `Orgs: [org]`.
   - `TestSchemaRegistration`: the new type registers with exactly `body`,
     `url`, `label`, `assignee`, `user`.

PR title: `feat(atlassian): add Jira issue created event`.

## Trade-offs

- **Fire Label Added / Issue Assigned from `jira:issue_created`** instead of a
  new type. Rejected: those events mean "changed", and a rule like "start a task
  when labelled `gritz`" would then also fire for issues created with that
  label, a behaviour change for existing rules. A separate type keeps creation
  opt-in; a rule on each type covers both paths.
- **One event per created issue, not one per label or assignee.** Creation is
  one fact; `label` and `assignee` are conditions on it.
- **Filter only in gritz rules** instead of a second Jira webhook. Works, but
  every new issue on the site then crosses the wire and is routed for nothing.
  The second webhook is optional and documented, not required.

## Open Questions

- **Creator vs reporter for `user`.** `payload.user` is who triggered the
  webhook. An integration can create an issue with a different `reporter`.
  Proposed: `user` is the creator, consistent with the other Jira events where
  `user` is the actor. A `reporter` attr can follow if someone needs it.
- **Summary in Data.** The other issue events carry only the description.
  Automation often puts the useful line in the summary. Keep Data as the
  description for consistency (the agent reads the full issue anyway), or
  prepend the summary?