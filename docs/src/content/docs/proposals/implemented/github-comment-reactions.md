---
title: "Emoji Reactions for Matched GitHub Comments"
description: "When a GitHub comment is routed to a task, the user gets no immediate acknowledgement while the agent spins up, so they assume the bot ignored it."
---

Issue: https://github.com/icholy/gritz/issues/691

## Problem

When a GitHub comment matches one of an org's routing rules and gets routed to a task, the user has no immediate feedback in GitHub that the bot saw their comment. The agent might take 10–60s to spin up and post its first message; until then the comment sits unacknowledged. Users assume nothing happened and comment again.

GitHub's native [Reactions API](https://docs.github.com/en/rest/reactions) is a perfect fit for an instant ack: drop a 👀 on the comment the moment routing decides it matches.

## Design

### Overview

When `eventrouter.Router.Route()` finds that an `InputEvent` matches an org's routing rules, it calls an optional `OnRouteOutcome` callback with the routing outcome. The GitHub server registers a callback that recognizes the event's GitHub metadata and asynchronously adds a reaction to the originating comment using that org's GitHub App installation token. The reaction is a side-effect of *matching*, decoupled from the existing event/task flow.

This builds on the webhook refactors now on master:

- **PR #775 / #776** parse GitHub and Atlassian webhooks directly into `eventrouter.InputEvent`. `InputEvent` already has a `Meta any` field and a `Type` field (e.g. `"issue_comment"`, `"pull_request_review_comment"`, `"pull_request_assigned"`).
- **PR #777** flattened the metadata structs — `GithubUser`/`AtlassianUser` are gone.
- **PR #780** moved webhook handling into the per-source server packages. The GitHub webhook handler, the `toInputEvent` extractor, and `GitHubMeta{AuthorID, AuthorLogin}` now all live in `internal/server/githubserver` (`webhook.go`) — the **same package** as `Server` and `WebhookHandler`. `toInputEvent` sets `Meta = GitHubMeta{...}` on every GitHub event, and the handler resolves identity via `input.Meta.(GitHubMeta)`.
- **PR #774** filters comment actions: only `created`/`edited` route; `deleted` is dropped in the extractor before routing. So we never react to a comment that no longer exists — handled upstream, not a risk here.
- **PR #782** adds the `githubx.AppTokenCache` design: `githubserver.Server` holds an `*githubx.AppTokenCache` and consumers call `cache.Client(installationID)` to get a `*github.Client` backed by a cached, auto-refreshing installation transport. The reactor uses this instead of minting a token and building a client by hand.

Because `Meta`, `GitHubMeta`, and `Type` already exist, this proposal adds **no new field to `InputEvent`** and **no GitHub-specific type to `eventrouter`**. The reaction target rides along as a few more flat fields on `GitHubMeta`, and `eventrouter` gains only a generic, purpose-agnostic `OnRouteOutcome` callback and a `RouteOutcome` struct. Since `GitHubMeta` and the reaction callback are colocated in `githubserver`, the callback reads `GitHubMeta` directly — no cross-package reference.

### 1. Carry the comment coordinates on `githubserver.GitHubMeta`

Extend the existing flat `GitHubMeta` with the three fields the Reactions API needs. No nested struct, no separate `Reaction any` — just flat fields, zero-valued for events with no reactable comment:

```go
// internal/server/githubserver/webhook.go

type GitHubMeta struct {
    AuthorID    int64
    AuthorLogin string

    // Owner, Repo, and CommentID locate the comment for GitHub's Reactions API.
    // Populated only for issue_comment and pull_request_review_comment events;
    // left zero for assignments and review submissions (which have no reactable
    // comment). The callback keys off InputEvent.Type, not these fields, to
    // decide whether and how to react.
    Owner     string
    Repo      string
    CommentID int64
}
```

Populate them in `toInputEvent`, alongside the author fields that are already set. Only the two comment events get coordinates.

For `*github.IssueCommentEvent`:

```go
Meta: GitHubMeta{
    AuthorID:    *event.Comment.User.ID,
    AuthorLogin: login,
    Owner:       event.GetRepo().GetOwner().GetLogin(),
    Repo:        event.GetRepo().GetName(),
    CommentID:   event.GetComment().GetID(),
},
```

For `*github.PullRequestReviewCommentEvent`:

```go
Meta: GitHubMeta{
    AuthorID:    *event.Comment.User.ID,
    AuthorLogin: login,
    Owner:       event.GetRepo().GetOwner().GetLogin(),
    Repo:        event.GetRepo().GetName(),
    CommentID:   event.GetComment().GetID(),
},
```

For `*github.PullRequestReviewEvent` (submission) and the `assigned` `*github.IssuesEvent` / `*github.PullRequestEvent`: leave `Owner`/`Repo`/`CommentID` zero — keep setting the author fields exactly as today. GitHub's REST API has no reaction endpoint for review *submissions* (only for the individual review *comments*, which arrive on the separate `pull_request_review_comment` webhook), and assignments have no reactable comment.

### 2. Promote the event-type strings to named constants

The callback decides which Reactions endpoint to call by switching on `InputEvent.Type`, so the `"issue_comment"` / `"pull_request_review_comment"` strings become a contract between the extractor (producer) and the callback (consumer), not just internal labels. Promote them to named constants in `githubserver` and use them in `toInputEvent`:

```go
// internal/server/githubserver/webhook.go

const (
    EventTypeIssueComment             = "issue_comment"
    EventTypePullRequestReviewComment = "pull_request_review_comment"
    EventTypePullRequestReview        = "pull_request_review"
    EventTypeIssueAssigned            = "issue_assigned"
    EventTypePullRequestAssigned      = "pull_request_assigned"
)
```

Both the extractor and the callback live in `githubserver`, so they share these constants directly.

### 3. Generic `OnRouteOutcome` callback on the Router

`eventrouter` stays purpose-agnostic: it knows nothing about reactions, GitHub, or interfaces. It just exposes an optional callback and a generic outcome struct:

```go
// internal/eventrouter/eventrouter.go

// RouteOutcome describes what the Router did with an InputEvent for one org. It
// gives the callback the routing context — which org matched, the rule, the
// affected tasks, and whether a task was created — so it can react differently
// per case:
//
//	Created                       -> a task was created
//	!Created && len(TaskIDs) > 0  -> existing task(s) were woken
//	!Created && len(TaskIDs) == 0 -> matched, but nothing was created or woken
type RouteOutcome struct {
    Input   InputEvent         // the routed event, including its Meta
    OrgID   int64              // the org whose routing rule matched
    Rule    *model.RoutingRule // the rule that matched
    TaskIDs []int64            // tasks created or woken
    Created bool               // whether a task was created (vs woken / matched-only)
}

type Router struct {
    Log       *slog.Logger
    Store     *store.Store
    Publisher pubsub.Publisher

    // OnRouteOutcome, if set, is called synchronously once per matched org
    // after routing handles that org, with the request context. The Router
    // imposes no concurrency or lifetime policy — the callback owns that (e.g.
    // spawning its own goroutine). Optional — nil disables it (e.g. the
    // Atlassian router leaves it unset).
    OnRouteOutcome func(ctx context.Context, outcome RouteOutcome)
}
```

`eventrouter` already imports `model`, so `RouteOutcome` needs no new import and carries nothing source-specific.

In `Route()`, the matched rule per org is already computed (the `matched` map). The per-org loop then either **wakes** subscribed tasks or **creates** a task. The shape: declare a `RouteOutcome` at the top of each iteration, update it as the branch runs (record woken/created task IDs, set `Created`), and fire `OnRouteOutcome` once at the end. The callback now fires for **all** matched orgs — including the matched-but-no-action case (no subscribed link, no create action), so a rule can react even when it neither woke nor created a task. Error paths (event-create / task-create failure) still `continue` past the callback. This turns the loop's previous `else { continue }` into a fall-through that fires the callback with an empty outcome.

```go
// internal/eventrouter/eventrouter.go (Route, per matched org)

for orgID, rule := range matched {
    // Built at the top, updated by whichever branch runs, passed to the callback
    // at the end. Defaults to "matched, nothing done" (no TaskIDs, Created false).
    outcome := RouteOutcome{Input: input, OrgID: orgID, Rule: rule}

    if links := linksByOrg[orgID]; len(links) > 0 {
        event := &model.Event{Description: input.Description, Data: input.Data, URL: input.URL, OrgID: orgID}
        if err := r.Store.CreateEvent(ctx, nil, event); err != nil {
            r.Log.Error("failed to create event", "org_id", orgID, "error", err)
            continue
        }
        seen := map[int64]bool{}
        for _, link := range links {
            if seen[link.TaskID] {
                continue
            }
            seen[link.TaskID] = true
            if err := r.attach(ctx, link.TaskID, event); err != nil {
                r.Log.Error("failed to attach event to task", "event_id", event.ID, "task_id", link.TaskID, "error", err)
                continue
            }
            outcome.TaskIDs = append(outcome.TaskIDs, link.TaskID)
            n++
        }
    } else if rule.Create != nil {
        taskID, err := r.create(ctx, input, orgID, rule)
        if err != nil {
            r.Log.Error("failed to create task from rule", "org_id", orgID, "error", err)
            continue
        }
        outcome.TaskIDs = []int64{taskID}
        outcome.Created = true
        n++
    }
    // else: matched a rule but no subscribed link and no create action —
    // outcome stays empty, and we still fire the callback below.

    if r.OnRouteOutcome != nil {
        r.OnRouteOutcome(ctx, outcome)
    }
}
```

`r.create` is changed to return `(int64, error)` — the new task's ID — so it can populate `outcome.TaskIDs`.

`Route` calls `OnRouteOutcome` **synchronously, with the request context** — it spawns no goroutine and does not alter the context. `eventrouter` deliberately imposes no concurrency or lifetime policy; that's the caller's decision (see the `WebhookHandler` glue below, which runs the synchronous `react` in a goroutine so a slow GitHub round-trip doesn't block the webhook response). A nil `OnRouteOutcome` is a no-op, so the Atlassian router (which leaves it unset) is unaffected.

### 4. The reaction logic on `githubserver.Server`

The reaction is a plain synchronous method on `githubserver.Server` that returns an error — no goroutines, no logging, no context juggling (the async glue lives in `WebhookHandler()`, below). `GitHubMeta` and the event-type constants live in the same package, so it reads them directly:

```go
// internal/server/githubserver/webhook.go (or a sibling file in the package)

// react adds the 👀 reaction to the comment that triggered the outcome. It is a
// plain synchronous function: it does the work and returns an error. It owns no
// concurrency or lifetime policy — the WebhookHandler glue (below) runs it in a
// goroutine and logs the error. Returns nil (not an error) when there's nothing
// to do: a non-GitHub Meta, an event with no reactable comment (CommentID == 0),
// an org with no installation, or a non-reactable event type.
func (s *Server) react(ctx context.Context, outcome eventrouter.RouteOutcome) error {
    meta, ok := outcome.Input.Meta.(GitHubMeta)
    if !ok || meta.CommentID == 0 {
        return nil
    }
    org, err := s.store.GetOrg(ctx, nil, outcome.OrgID)
    if err != nil {
        return err
    }
    if org.GitHubInstallationID == 0 {
        return nil
    }
    // s.tokens is the *githubx.AppTokenCache the Server holds. Client returns a
    // *github.Client backed by the cached, auto-refreshing installation
    // transport — no manual token mint or client construction here.
    client := s.tokens.Client(org.GitHubInstallationID)

    const content = "eyes"
    switch outcome.Input.Type {
    case EventTypeIssueComment:
        _, _, err = client.Reactions.CreateIssueCommentReaction(ctx, meta.Owner, meta.Repo, meta.CommentID, content)
    case EventTypePullRequestReviewComment:
        _, _, err = client.Reactions.CreatePullRequestCommentReaction(ctx, meta.Owner, meta.Repo, meta.CommentID, content)
    default:
        return nil
    }
    return err
}
```

GitHub's reaction endpoint is idempotent at the user level — a given GitHub user (the App's bot identity) can only have one of each reaction type on a given comment, so duplicate calls (e.g. an `edited` redelivery) are harmless: the API returns the existing reaction with 200.

### 5. Wiring

`githubserver.Server.WebhookHandler()` constructs the Router today as `&eventrouter.Router{Log, Store, Publisher}`. Assign `OnRouteOutcome` to a small glue closure that holds all the dirty work — running the synchronous `react` in a goroutine (since `eventrouter` calls the callback inline), detaching from the request context, applying a timeout, and logging the error:

```go
func (s *Server) WebhookHandler() http.Handler {
    return &WebhookHandler{
        Router: &eventrouter.Router{
            Log:       s.log,
            Store:     s.store,
            Publisher: s.publisher,
            OnRouteOutcome: func(ctx context.Context, o eventrouter.RouteOutcome) {
                // react is synchronous; the goroutine + detached context +
                // timeout keep a slow GitHub round-trip off the webhook path.
                go func() {
                    ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
                    defer cancel()
                    if err := s.react(ctx, o); err != nil {
                        s.log.Warn("github reaction failed", "org_id", o.OrgID, "url", o.Input.URL, "error", err)
                    }
                }()
            },
        },
        Store:         s.store,
        WebhookSecret: s.config.WebhookSecret,
    }
}
```

Keeping `react` a plain `func(...) error` makes it directly unit-testable (call it, assert the error and the API call) without spawning goroutines or stubbing timeouts; the glue is trivial enough to leave untested.

The Atlassian webhook router constructs its `Router` without `OnRouteOutcome`, so it's a clean no-op there.

### 6. Configuration

v1: hardcoded `"eyes"`. Two natural extension points if we want to customize later, neither requiring an interface or signature change:

- **Per-org**: add a `reaction_emoji` column to `orgs` and read it in `react` (we already fetch the org for its installation ID).
- **Per-rule**: `RouteOutcome` already carries the matched `*model.RoutingRule`, so a per-rule `Emoji string` field could be honored directly.

Start simple and add config if users ask.

### 7. Tests

- `githubserver/webhook_test.go`: assert `toInputEvent` populates `Owner`/`Repo`/`CommentID` (and the right `Type`) for issue comments and PR review comments, and leaves the comment coordinates zero for review submissions and assignments.
- `eventrouter_test.go`: set a fake `OnRouteOutcome` that records the `RouteOutcome`s it receives; assert it fires once per matched org with the right `OrgID`/`Rule`, and that `TaskIDs`/`Created` are correct for each case — created (`Created` true, the new id), woken (`Created` false, the woken ids), and matched-only (no subscribed link, no create action → empty `TaskIDs`, `Created` false). Assert it does not fire when no rule matches.
- `githubserver`: test `react` against a stubbed GitHub API (`httptest.Server`); verify the issue-comment vs review-comment endpoint is chosen by `Type`, and that a non-GitHub `Meta`, a zero `CommentID`, a non-reactable `Type`, and an org with no installation ID are each no-ops.

### 8. Implementation order

1. Add `Owner`/`Repo`/`CommentID` to `githubserver.GitHubMeta` and the event-type constants; populate the coordinates in `toInputEvent` for the two comment events.
2. Add `RouteOutcome` and the `OnRouteOutcome` field to `eventrouter.Router`, change `r.create` to return the new task ID, restructure the per-org loop (build the outcome, fire for all matched orgs including matched-only), and add the synchronous call.
3. Implement `Server.react` and assign it in `WebhookHandler()`.
4. Tests.

No migrations, no proto changes, no UI changes.

## Trade-offs

**A single `OnRouteOutcome` func, not an interface or registry.** `eventrouter` exposes one optional `func(ctx, RouteOutcome)` rather than a `Reactor` interface and a slice of registered reactors. The router stays completely purpose-agnostic — it has no notion of "reactions", just "here's what I did, do whatever you want." The GitHub-specific behavior lives entirely in `githubserver` as a `Server` method. This is less machinery than an interface + registry for what is, today, a single consumer per router; a second concern on the same router can compose by wrapping (`OnRouteOutcome: func(ctx, o){ s.react(ctx, o); s.somethingElse(ctx, o) }`).

**Meta and callback colocated in `githubserver`.** The GitHub webhook extractor, `GitHubMeta`, and the reaction callback all live in one package, so the callback reads `GitHubMeta` and the event-type constants directly — no cross-package reference. `eventrouter` still stays free of GitHub types: it only gains the generic `OnRouteOutcome`/`RouteOutcome`.

**Comment coordinates as flat fields on `GitHubMeta`, not a nested struct or `Reaction any`.** PR #777 deliberately flattened these metadata structs, so the reaction target follows suit: three flat fields, zero when absent. No discriminator field, no marker interface, no extra allocation.

**Dispatch on `InputEvent.Type`, not a discriminator field.** The event type already distinguishes issue comments from PR review comments, and the callback needs exactly that distinction to pick the endpoint. Reusing `Type` avoids a redundant field that could drift out of sync. The cost is that the callback now depends on those type strings, which is why they're promoted to named constants shared by extractor and callback.

**React on match, not after task start.** The whole point is fast feedback. The callback fires the moment routing matches and wakes/creates the task — within a webhook round-trip, rather than after the runner schedules a container. v1 reacts on match regardless of what happens downstream: a 👀 means "matched and accepted".

**Pass a `RouteOutcome`, not just the input.** The callback gets the org that matched and the matched rule — not only the raw `InputEvent`. This keeps it from re-deriving routing context the Router already computed and leaves room for richer behavior later (e.g. per-rule emoji) without changing the signature.

**Async lives in the wiring, not the router or the logic.** Source API round-trips can be hundreds of milliseconds, so the reaction must not block the webhook response — but `eventrouter` stays policy-free (`Route` calls `OnRouteOutcome` synchronously with the request context) and `react` stays a pure, testable `func(...) error`. The concurrency/lifetime policy lives in one place: the `WebhookHandler` glue closure, which runs `react` in a goroutine with `context.WithoutCancel` + a timeout and logs the error. A missed reaction is a degraded experience, not a broken one.

**Installation token, not OAuth user token.** Reactions posted via the installation token (obtained through `AppTokenCache.Client`) appear under the GitHub App's bot identity (e.g. `gritz-app[bot]`), which is the correct attribution — it's the bot acknowledging the comment, not the user who triggered it. OAuth tokens also tie to a specific linked user and would fail if that user un-links.

**Hardcoded emoji in v1.** 👀 is the most idiomatic "I see this and am working on it" reaction (used by github-actions[bot], Linear, many CI bots). Per-org or per-rule customization is easy to add later without breaking the v1 contract.

**Skip PR review submissions.** GitHub's REST API supports reactions on individual review comments but not on review submissions themselves. We could fall back to a reaction on the underlying PR, but a 👀 on a whole PR is confusing — it would imply we noticed the PR, not the review. Better to leave the comment coordinates zero for review submissions (the callback's `CommentID == 0` check skips them); the individual review comments still get reactions via the `pull_request_review_comment` webhook.

## Open Questions

1. **Should we react to atlassian/Jira matches too?** Jira issue comments support reactions via the [Atlassian REST API](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issue-comments/) — `POST /rest/api/3/issue/{issueIdOrKey}/comment/{id}/reactions`. With the generic `OnRouteOutcome`, this slots in cleanly: add the comment coordinates to `atlassianserver`'s `AtlassianMeta`, and set the Atlassian router's `OnRouteOutcome` to an Atlassian-server method that asserts `AtlassianMeta` and posts the reaction. No change to `InputEvent`, `Router`, `eventrouter`, or the GitHub callback. Worth doing in a v2 once the GitHub side is proven.

2. **Should the reaction get removed/updated when the task finishes?** Some bots (e.g. github-actions) update reactions to reflect state: 👀 while running, 🎉 on success, 👎 on failure. Appealing, but it adds a lot of state management: we'd track the reaction ID per comment, plumb task-completion events back to the server, and handle deletion failures. Not worth it in v1; can be added later if there's demand.

3. **What about "I matched but couldn't start the task" cases?** The callback fires once the org's wake/create branch runs to the bottom of the iteration — skipped only when the branch hits an early error `continue` (event creation failed, `create` failed) or the org matched a rule with no subscribed link and no create action. A per-task attach failure inside the wake loop is logged but still lets the reaction fire. So a reaction means "I matched and routing acted on it", not "every task started successfully" — acceptable, and errors are already logged and visible in gritz.

4. **Rate limits?** GitHub Apps get 5,000 requests/hour per installation, shared across all reads/writes. A reaction is one extra request per matched comment. At realistic comment volumes this is negligible, but worth keeping in mind if an org has a high-volume routing rule.
