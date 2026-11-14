# Route CI Results to Tasks

Issue: https://github.com/gritzapp/gritz/issues/1997

## Problem

Gritz can't react to CI results. `toInputEvent` (`internal/server/githubserver/webhook.go`) handles comments, reviews, assignments, PR opened/closed and labels, and returns `nil` for every other delivery. That includes all CI deliveries (`workflow_run`, `check_suite`, `check_run`). There are two use cases:

1. **A failed run on a branch starts a task.** A failed pipeline on `master` creates a task through a create rule, for example to investigate and write a proposal.
2. **A PR's failed checks reach the PR's task.** The task that opened a PR is subscribed to it, and it should wake when that PR's checks fail.

The issue already settles the shape. Every completed run is emitted with `conclusion` as an attribute, next to the check name, branch and so on, and rules filter on those attributes. One webhook produces two event types: a run variant whose URL is the run, for create rules, and a PR variant whose URL is the PR, so that its routing key matches the PR task's subscribed link.

## Design

### 1. Build on `check_run`

| | `workflow_run` | `check_suite` | `check_run` |
|---|---|---|---|
| Granularity | one per workflow run | one per app per commit (Actions: one per workflow run) | one per job / check |
| Name you can filter on | workflow name | none (only the app) | **check name** (`test`, `lint`, `build (linux, 1.26)`) |
| CI systems | GitHub Actions only | any Checks API app | any Checks API app (Actions, CircleCI, Buildkite, …) |
| `pull_requests`, `head_branch` | yes | yes (on the suite) | yes (`pull_requests` on the run, `head_branch` on its embedded `check_suite`) |
| App permission | Actions: read | Checks: read | Checks: read |

**Choose `check_run`, with action `completed`.** It is the only event that carries the individual check name that rules need to filter on. It also covers every CI system that reports through the Checks API, not only Actions. `check_suite` carries no name at all. `workflow_run` stops at the workflow name, so per-job names would need a jobs API round trip from inside the webhook handler, which none of the existing extractors do.

Being per-job has two costs, and the design deals with each:

- **More deliveries.** Every job of every push produces a delivery, successes included. Each one runs `Router.Plan`, which is one `ListRoutingRulesForEvent` query, and is dropped once no rule matches. That load is the same order as the comment traffic we already handle. If it ever matters, a pre-filter can skip routing for orgs that have no CI rule (see Open Questions).
- **Several failing jobs in one workflow run.** Without grouping, a create rule would open one task per failing job. §3 collapses Actions job URLs to their workflow run, so the second failing job wakes the task the first one created instead of creating another.

`workflow_run` stays possible as a later addition if someone needs to filter on the workflow name (see Open Questions).

### 2. Two event types

```go
const (
	// ...existing types...
	EventTypeCheckRunCompleted            = "check_run_completed"
	EventTypePullRequestCheckRunCompleted = "pull_request_check_run_completed"
)
```

Both variants are built from one `*github.CheckRunEvent` with `action == "completed"` and share the same attrs:

| Attr | Source field | Notes |
|---|---|---|
| `conclusion` | `check_run.conclusion` | `success`, `failure`, `cancelled`, `timed_out`, `action_required`, `neutral`, `skipped`, `stale`, `startup_failure` |
| `check` | `check_run.name` | Actions: the job name. Matrix jobs carry their axes, so `prefix` is the useful op |
| `app` | `check_run.app.slug` | `github-actions`, `circleci-checks`, … |
| `branch` | `check_run.check_suite.head_branch` | absent when GitHub sends null (fork pushes, see §4) |
| `user` | `sender.login` | same meaning as on other GitHub types |
| `body` (derived) | `Data` = `check_run.output.title` + `"\n\n"` + `output.summary`, trimmed | often empty for Actions |
| `url` (derived) | `URL` | see below |

`Details` (persisted for the agent and the UI, not matched on) carries `check`, `conclusion`, `branch`, `head_sha`, `app`, `details_url` and `check_run_url` (`check_run.html_url`). The PR variant also carries `pull_request` (the number). With these the agent can open the failing job's log without having to rebuild its URL from the event URL.

They differ in their URL, their description and how many are emitted:

- **`check_run_completed`** (the run variant): one per delivery. `URL = check_run.html_url`. For Actions that is `https://github.com/o/r/actions/runs/R/job/J`, which §3 reduces to the workflow run. Description: `check "test" failed on master (abc1234)`, using a past-tense verb taken from the conclusion and falling back to `completed (<conclusion>)` for conclusions without one.
- **`pull_request_check_run_completed`** (the PR variant): one per PR in `check_run.pull_requests`, after filtering (§4). `URL = repository.html_url + "/pull/" + number`, so `model.RoutingKey` yields the same key as the link the agent created when it opened the PR. Description: `check "test" failed on PR #12 (abc1234)`.

Every delivery always emits the run variant, including runs that belong to a PR. A create rule scopes itself with `branch`, so a run variant from a PR branch doesn't match `branch equals master`.

Both get `EventTypeDef` registrations in `RegisterSchemas` (`schema.go`) with the attrs above and copy that speaks to each variant ("PR URL" against "Check Run URL"). The routing-rule editor renders from `GetEventTypes`, so the web UI needs no change.

`GitHubMeta.AuthorID`/`AuthorLogin` come from `sender`. `NodeID` stays empty, because a check run is not reactable and `react` already returns nil for an empty `NodeID`.

### 3. Group an Actions workflow run's jobs under one routing key

`model.RoutingKey` (`internal/model/url.go`) gains one GitHub web case:

```
github.com/o/r/actions/runs/R/job/J        -> github.com/o/r/actions/runs/R
github.com/o/r/actions/runs/R/attempts/2   -> github.com/o/r/actions/runs/R
```

This uses a new `githubRunRe = ^/([^/]+)/([^/]+)/actions/runs/(\d+)`. The create path stores the link with `RoutingKey: model.RoutingKey(input.URL)`, so a task created for job A of run R is subscribed to `…/actions/runs/R`. The failure of job B in the same run then takes `Apply`'s wake path and attaches to that task. A re-run produces new job IDs under the same run ID, so it also lands on the investigating task. Two jobs failing at the same instant can still race to create two tasks. That is the limitation `Router.create` already documents and accepts for concurrent overlapping transactions.

Non-Actions check-run URLs (`github.com/o/r/runs/ID`) stay one key per check run, because GitHub exposes no web URL for a suite.

### 4. Several PRs, forks, and the handler loop

**One event per PR.** `check_run.pull_requests` lists every open PR whose head is the run's commit, for example when the same branch is open against both `master` and a release branch. The extractor emits one PR-variant event per entry, and the router routes them independently, so each wakes its own subscribed task. Two filters apply:

- Keep only entries whose `base.repo.id == repository.id`. GitHub can include PRs from other repositories that share the head ref, and building a URL for them under `repository.html_url` would point at the wrong PR.
- Deduplicate by number.

**Fork PRs.** For a commit pushed to a fork, GitHub sends an empty `pull_requests` and a null `head_branch`. Such a check run produces only the run variant, with no `branch` attr, so:

- no PR variant is emitted, and a task subscribed to a fork PR is not woken by its CI;
- a create rule with a `branch` condition can't match it, because a condition on an absent attr fails (`conditionMatch`). Fork contributors therefore can't trigger a branch-scoped create rule.

Gritz agents open PRs from branches in the base repo, so use case 2 is covered. Resolving fork PRs would take a `GET /repos/{o}/{r}/commits/{sha}/pulls` (or search) call inside the webhook, which this design doesn't do (see Open Questions).

**Handler.** `toInputEvent` becomes `toInputEvents(webhookEvent any) []eventrouter.InputEvent`, mirroring `atlassianserver`'s `toInputEvents`. The existing cases return a one-element slice. `ServeHTTP` resolves orgs and the actor once, from the first event's `GitHubMeta`, because every event of a delivery shares its sender and installation. It then calls `Router.Route` for each event, as `atlassianserver.WebhookHandler.ServeHTTP` already does.

### 5. Limiting PR wakes to failures

**A subscribed link does not wake a task on its own.** In `Router.Route`, `Plan` runs first, and an org takes part only if one of its rules `Match`es the event (source, type and every condition). `Apply` looks up subscribed links only for orgs that `Plan` matched, and it wakes them only with `match.Rule.Wakeup`. An event that matches no rule is dropped. It is not even attached to the subscribed task's timeline.

So no new mechanism is needed. The rule that wakes PR tasks on failure is:

```json
{
  "source": "github",
  "type": "pull_request_check_run_completed",
  "conditions": [{"attr": "conclusion", "op": "equals", "value": "failure"}],
  "wakeup": true,
  "public": true
}
```

A successful run matches no rule and leaves the PR task untouched. An org that wants successes on the timeline without waking the task adds a second rule for the same type with no conditions and `wakeup: false`, ordered *after* the failure rule (`Plan` takes the first match per org).

Rules can't OR conditions, so `timed_out` or `startup_failure` each need their own rule with the same body. See Open Questions for a derived `failed` attr.

A failed workflow with N failing jobs puts N events on the PR task. The first wakes it if it is done. The rest arrive while it is running: `attach` still calls `task.Start()`, which leaves a running task's status and version unchanged, and sets no `ChannelMessage` because `wasDone` is false. The agent reads all of them from its event stream.

The use-case-1 create rule:

```json
{
  "source": "github",
  "type": "check_run_completed",
  "conditions": [
    {"attr": "conclusion", "op": "equals", "value": "failure"},
    {"attr": "branch", "op": "equals", "value": "master"},
    {"attr": "url", "op": "prefix", "value": "https://github.com/gritzapp/gritz/"}
  ],
  "create": {"workspace": "gritz", "runner": "…", "prompt": "CI failed on master. Investigate and write a proposal."},
  "public": true
}
```

**Why `public: true`.** The CI event's `sender` is whoever GitHub attributes the run to: usually the pusher, but a bot account when a bot pushed, which includes an agent pushing with the App's installation token. For scheduled runs it may be `ghost`. None of these are linked gritz users, so a member-only rule would silently miss them. `Public` routes through the installation's orgs (`input.Orgs`) whatever the actor. That is safe for these rules because the `branch` condition restricts the run variant to refs only people with push access can update, and fork pushes carry no `branch` (§4). The PR variant only reaches tasks that already subscribed to the PR. The schema `Help` copy for both types says this.

### 6. Operational note: GitHub App changes

These are made in the GitHub App settings, outside this repo:

1. **Permissions → Repository → Checks: Read-only.** We only read check runs, and posting results back is out of scope.
2. **Subscribe to events → Check run.** Check suite and Workflow run stay unsubscribed.
3. Changing permissions makes GitHub ask every installation's owner to approve the update. Until an installation approves it, that installation gets no `check_run` deliveries, so nothing breaks, it just stays silent. Announce the change when it ships.
4. Verify in a test installation that `completed` deliveries arrive for check runs created by *other* apps (GitHub Actions in particular), not just our own. GitHub's docs restrict only the `requested_action`/`rerequested` actions to the owning app. Do this before layer 2 merges.

Ship order: deploy the server first. Until the App subscribes, the new extractor just never sees `check_run`. Then flip the App settings.

### API

```go
package githubserver // github.com/gritzapp/gritz/internal/server/githubserver

const (
	// EventTypeCheckRunCompleted is a completed check run, keyed by the run.
	EventTypeCheckRunCompleted = "check_run_completed" // new
	// EventTypePullRequestCheckRunCompleted is a completed check run, keyed by one PR it belongs to.
	EventTypePullRequestCheckRunCompleted = "pull_request_check_run_completed" // new
)

// toInputEvents extracts the routable events from a webhook payload; empty means ignore.
func toInputEvents(webhookEvent any) []eventrouter.InputEvent // replaces toInputEvent (unexported, shown for the contract)
```

```go
package model // github.com/gritzapp/gritz/internal/model

// RoutingKey returns the canonical subscription key for a URL.
// Changed: github.com/{o}/{r}/actions/runs/{id}[/...] reduces to the workflow run.
func RoutingKey(raw string) string
```

**How it fits:** `WebhookHandler.ServeHTTP` calls `toInputEvents`, resolves orgs and the user once, and calls `Router.Route` per event. `Router.create` stores the trigger link through `model.RoutingKey`, so later jobs of the same Actions run take the wake path. `RegisterSchemas` registers the two new `EventTypeDef`s, and they show up in the rule editor through `GetEventTypes`. `eventrouter` itself does not change.

## Implementation Plan

1. **Multi-event extraction** — Delivers: `toInputEvent` → `toInputEvents` returning a slice, and `ServeHTTP` looping `Route` per event as `atlassianserver` does. Behaviour is unchanged. Depends on: nothing. Verifiable by: the existing `webhook_test.go` table, adapted to slices, passes unchanged otherwise.
2. **Run variant** — Delivers: `EventTypeCheckRunCompleted` extraction from `*github.CheckRunEvent` (`completed` only), its attrs/Details/description, and its `EventTypeDef` in `schema.go`. Depends on: (1). Verifiable by: extractor table tests with Actions and non-Actions payloads (null `head_branch` produces no `branch` attr), `schema_test.go`, and a `Route` test where a `conclusion=failure, branch=master` create rule creates a task.
3. **Run grouping in `RoutingKey`** — Delivers: the `actions/runs/{id}` case in `model.RoutingKey`. Depends on: nothing (it can land before or after (2)), and it pays off with (2). Verifiable by: `RoutingKey` table cases for `/job/J` and `/attempts/N`, plus a `Route` test where a second failing job of the same run wakes the created task instead of creating another.
4. **PR variant** — Delivers: `EventTypePullRequestCheckRunCompleted`, one event per same-repo PR (filtered by base repo, deduped), and its `EventTypeDef`. Depends on: (1), (2). Verifiable by: extractor tests for zero, one and two PRs and a foreign-repo entry, plus a `Route` test where a `conclusion=failure` waking rule wakes the PR-subscribed task and a `success` event leaves it untouched and unattached.
5. **App settings + rollout** — Delivers: the Checks: read permission and Check run subscription (§6), installation approvals, and the two example rules configured for the gritz org. Depends on: (2)–(4) deployed. Verifiable by: a deliberately failing check on a test PR waking its task, and a failure on `master` of a test repo creating a task.

## Trade-offs

- **`workflow_run` instead of `check_run`.** One event per workflow means less volume and no grouping logic, but it gives no per-check name and only works with Actions. It fails the filter-by-check-name requirement unless we enrich it with a jobs API call in the webhook path. Rejected for now. It can be added later as a third type if workflow-level filtering is wanted.
- **`check_suite`.** It aggregates the conclusion, but it has no name to filter on and doesn't match Actions' one-suite-per-workflow model in any useful way. Rejected.
- **A router-level "wake only on failure" flag** (for example a per-rule or per-link wake filter). This isn't needed, because rule conditions already gate the wake and an unmatched event never reaches the link lookup (§5). Adding one would duplicate the matcher.
- **Run-variant URL as the commit** (`/commit/SHA`) to group *all* checks of a push. That merges unrelated workflows and other apps' checks into one investigation task, and the issue settled on "the run itself". The workflow-run grouping in §3 gives the useful part.
- **Run variant only for non-PR runs.** It would remove some noise, but a rule can already scope with `branch`, and emitting it unconditionally keeps one predictable event per delivery.

## Open Questions

- **A derived `failed` attr.** Should the extractor add `failed: ["true"]` for `failure`/`timed_out`/`startup_failure`, so that one rule covers every failing conclusion? Without it each conclusion needs its own rule, because conditions only AND.
- **Fork PRs.** Is it worth a `commits/{sha}/pulls` lookup in the webhook path (an installation-token API call per fork-run delivery) to wake tasks subscribed to fork PRs? This design leaves them out.
- **Routing pre-filter.** If `check_run` volume becomes significant, should `ServeHTTP` skip `Route` when none of the installation's orgs has a rule for the type? Today every job delivery costs one rules query.
- **Workflow name.** `check_run` payloads don't carry the Actions workflow name. If rules need it (`workflow equals CI`), add `workflow_run` as a third type later, or look it up from the job.
