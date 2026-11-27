# GitHub push events with glob path matching

Issue: https://github.com/gritzapp/gritz/issues/1982

## Problem

Routing rules react to comments, reviews, assignments, labels and pull request
open/close, but not to pushes. There is no way to say "create a task whenever a
push to `master` changes `internal/store/sql/migrations/**`".

Two things are missing:

1. **Push events are dropped.** `toInputEvent` in
   [`internal/server/githubserver/webhook.go`](https://github.com/gritzapp/gritz/blob/master/internal/server/githubserver/webhook.go)
   has no `*github.PushEvent` case, so the handler answers `"ignored"`
   (`TestHandleGitHubWebhookIgnoredEventType` asserts this).
2. **No operator can match a path pattern.** `conditionMatch` in
   [`internal/server/eventrouter/rule.go`](https://github.com/gritzapp/gritz/blob/master/internal/server/eventrouter/rule.go)
   knows `equals`, `prefix` and `contains`. `prefix internal/store/sql/migrations/`
   covers the issue's example, but not `**/*.sql`, `docs/**/*.md` or
   `{go.mod,go.sum}`.

## Review of the issue's sketch

The issue's approach holds up: a new `github`/`push` event type whose
multi-valued `path` attr carries every changed file, plus a generic `glob` op.
`conditionMatch` already succeeds when any value matches, so "any changed file
matches" needs no matcher change, and `Plan` stays one rules query plus an
in-memory scan. Checking it against the code turned up these corrections and
gaps:

- **The GitHub App needs a new permission.** `push` deliveries need
  **Repository → Contents: Read**. The App asks for Issues and Pull requests
  only (see `operators/server` in the docs). Each installation has to accept the
  new permission before GitHub sends it pushes. The issue mentions only the
  event subscription.
- **`pusher` can't feed the membership check.** The handler resolves the actor
  through `GitHubMeta.AuthorID` → `Store.GetUserByGitHubUserID`, not through an
  attr. The payload's `pusher` is a `*github.CommitAuthor` (name, email,
  username) with **no user ID**. The ID comes from `sender`, which is what every
  other case already uses. The attr should also be called `user`, the name every
  GitHub event type uses for "who did it"; a separate `pusher` key would be the
  only exception.
- **The compare URL is a poor routing key.** `InputEvent.URL` is persisted as
  the created task's subscribed `trigger` link, and that link is what dedups a
  redelivery (`Router.create`'s comment). `model.RoutingKey` leaves compare URLs
  alone, so a `…/compare/<before>...<after>` URL is unique per push. But a push
  that creates a branch has the compare URL `…/compare/<branch>`, so deleting and
  recreating a branch reuses a URL, and the second push would wake the first
  push's task. The head commit's URL, `https://github.com/<owner>/<repo>/commit/<after>`,
  is unique per tip, stable across redeliveries, and a page a user can open. Use
  it as the URL, and keep the compare URL in `Details`.
- **Branch deletions and tag pushes both need skipping.** The issue skips tags.
  A deletion (`deleted: true`) has no `head_commit` and no commits, so it must
  be skipped too.
- **`push` is the e2e fixture for an unknown type.** `TestTestEventBuiltinInvalid` in
  [`internal/e2e/api/test_event_test.go`](https://github.com/gritzapp/gritz/blob/master/internal/e2e/api/test_event_test.go)
  uses `github`/`push` as its "unknown type" case. It needs a different type
  once `push` is registered.
- **The op list lives in more places than `schema.go` and the UI.** It is also
  in the `model.Condition` doc comment, the `RuleCondition.op` comment in
  `proto/gritz/v1/gritz.proto` (published on the API reference page, and
  regenerated into `webui/src/gen` and `n8n-node/gen`), `CONDITION_OPS`/`OP_LABELS`
  in `webui/src/lib/routing-rules.ts`, the `SchemaRegistry.Validate` doc
  comment, and the operator table in `concepts/routing-rules`.
- **Rules don't need precompiled patterns.** The issue suggests precompiling
  globs when rules load. `doublestar` matches straight from the pattern string,
  and `Plan` loads rules from the store for each event anyway, so there is no
  long-lived rule set to compile into. Revisit only if profiling shows the
  matcher.
- **Agents push too, which can cause loops.** Agents push branches with the
  installation token, so those pushes arrive with `sender` set to the App's bot
  user. It isn't linked to anyone, so only **public** rules act on its pushes.
  A public push rule with no `branch` condition that creates tasks can feed
  itself: the created task pushes a branch, which creates another task. The docs
  should tell readers to scope push rules by branch. The proposal doesn't add a
  guard (see Open Questions).
- **The payload can be incomplete.** The webhook's `commits` array is capped
  (GitHub documents 2048 commits), and GitHub drops deliveries over 25 MB
  entirely. Pushes of merged pull requests are far below either limit. A
  truncated push is routed on the paths it does list. The agent should diff
  `before..after` itself for the full picture (see `Details` below).
- **The changed paths need not be persisted.** `Attrs` are dropped after
  routing; only `Description`, `Data`, `URL` and `Details` reach the
  `ExternalPayload`. Persisting thousands of paths per push would bloat the
  event table. The created task's agent has the repo and the `before`/`after`
  SHAs, so `git diff --name-only` gives it the list.

## Design

### Push event ingestion

`toInputEvent` gains a `*github.PushEvent` case producing `EventTypePush = "push"`:

```go
case *github.PushEvent:
	branch, ok := strings.CutPrefix(event.GetRef(), "refs/heads/")
	if !ok || event.GetDeleted() || event.HeadCommit == nil ||
		event.Sender == nil || event.Sender.ID == nil {
		return nil // tag push, branch deletion, or malformed payload
	}
	...
```

| Field | Value |
| --- | --- |
| `Source` / `Type` | `github` / `push` |
| `Description` | `octocat pushed 3 commits to master` (`pushed to master` when there are no commits, as for a branch created at an existing commit) |
| `Data` (the `body` attr) | the head commit's message, trimmed |
| `URL` (the `url` attr) | `head_commit.url`, i.e. `https://github.com/<owner>/<repo>/commit/<after>` |
| `Attrs["repo"]` | `repository.full_name`, e.g. `gritzapp/gritz` |
| `Attrs["branch"]` | `ref` with `refs/heads/` stripped |
| `Attrs["path"]` | union of `added`, `modified` and `removed` over every entry in `commits`, deduplicated, sorted |
| `Attrs["user"]` | `sender.login` |
| `Details` | `repo`, `branch`, `before`, `after`, `compare`, `forced` (`"true"`/`"false"`), `commits` (count) |
| `Meta` | `GitHubMeta{AuthorID: sender.id, AuthorLogin: sender.login, InstallationID: …, NodeID: ""}` |

`NodeID` is empty: a push has no reactable node, and the reaction code in
`reactions.go` already skips an event with an empty `NodeID`.

`path` covers every commit in the push, including non-`distinct` ones (commits
already on another branch). A rule is about what the push brought onto the
branch, and a non-distinct commit is still new to that branch.

A force push that drops commits lists only the new commits, so `path` does not
include files touched only by the dropped ones. `forced` in `Details` lets a
prompt handle that.

The handler itself is unchanged: `Orgs` from the installation, `UserID` from
`sender`, then `Route`.

`RegisterSchemas` registers the type:

```go
reg.MustRegister(model.EventTypeDef{
	Source: "github",
	Type:   EventTypePush,
	Label:  "GitHub: Push",
	Attrs: []model.AttrDef{
		{Key: "body", Label: "Head Commit Message", Placeholder: "gritz:", Help: "Matched against the message of the push's newest commit."},
		{Key: "url", Label: "Head Commit URL", Placeholder: "https://github.com/owner/repo/commit/", Help: "Matched against the URL of the push's newest commit, e.g. to scope a rule to a single repo."},
		{Key: "repo", Label: "Repository", Placeholder: "owner/repo", Help: "The repository pushed to, as owner/name."},
		{Key: "branch", Label: "Branch", Placeholder: "master", Help: "The branch pushed to, without refs/heads/."},
		{Key: "path", Label: "Changed Path", Placeholder: "internal/store/**", Help: "A file added, modified or removed by the push. A condition holds when any changed file satisfies it."},
		{Key: "user", Label: "User", Placeholder: "octocat", Help: "The GitHub username of the user who pushed (no leading @)."},
	},
})
```

`GetEventTypes` serves the registry and `TestEvent` validates against it, so
the type and its attrs show up in the rule form and the dry run with no further
change.

The `repo` attr is new to GitHub events. The other types scope by repo with
`url prefix https://github.com/owner/repo/`, which works here too, but `repo equals`
is clearer and needed nothing more than reading `repository.full_name`. Adding
`repo` to the other GitHub types is out of scope.

### The `glob` op

`glob` is a generic op: like the others, it applies to any attr of any event
type, such as `branch glob release/*` or `label glob team-*`.

Semantics, from
[`github.com/bmatcuk/doublestar/v4`](https://github.com/bmatcuk/doublestar):

| Pattern | Matches |
| --- | --- |
| `*` | any run of characters except `/` |
| `**` | as a whole path segment, zero or more segments |
| `?` | one character except `/` |
| `[abc]`, `[a-z]`, `[^a]` | one character from the class |
| `{a,b}` | either alternative |
| `\x` | literal `x` |

A pattern matches the whole value, case-sensitively, like `equals`. Because `*`
stops at `/`, `glob` is awkward for `url` and `body`; `prefix` and `contains`
remain the tools for those.

`doublestar` is chosen over `path.Match` (no `**`, no `{}`) and over a
hand-written matcher (more code to own, same behaviour). It has no dependencies.

Matching in `conditionMatch`:

```go
case "glob":
	if doublestar.MatchUnvalidated(c.Value, v) {
		return true
	}
```

`MatchUnvalidated` returns `false` on a malformed pattern instead of an error.
Validation stops malformed patterns at save time, so stored rules never have
one.

Validation in `SchemaRegistry.Validate`:

```go
switch cond.Op {
case "equals", "prefix", "contains":
case "glob":
	if !doublestar.ValidatePattern(cond.Value) {
		return fmt.Errorf("invalid glob %q on attr %q", cond.Value, cond.Attr)
	}
default:
	return fmt.Errorf("unknown op %q on attr %q", cond.Op, cond.Attr)
}
```

`SaveRoutingRules` (`internal/server/apiserver/org.go`) and the custom event
type handler (`event_types.go`) both go through `Validate`, so a bad pattern is
rejected with `InvalidArgument` from the API, the web UI and the MCP admin tools
alike.

Example rule:

```
source=github type=push
  branch equals master
  path   glob   internal/store/sql/migrations/**
create: workspace=gritz, runner=..., prompt="A migration changed; check ..."
```

### Proto

Only a comment changes:

```proto
message RuleCondition {
  string attr = 1;
  string op = 2;  // One of "equals", "prefix", "contains" or "glob".
  string value = 3;
}
```

The comment above `RuleCondition` ("Comparisons are exact and case-sensitive")
gets a sentence on `glob`'s wildcards.

### API

```go
package eventrouter // github.com/gritzapp/gritz/internal/server/eventrouter

// Match reports whether the rule matches the event. // unchanged signature; conditionMatch gains "glob"
func Match(rule model.RoutingRule, event InputEvent) bool

// Validate checks the rule against the event-type registry. // now also rejects malformed glob patterns
func (r *SchemaRegistry) Validate(rule model.RoutingRule) error
```

```go
package githubserver // github.com/gritzapp/gritz/internal/server/githubserver

// EventTypePush is the InputEvent.Type of a branch push. // new
const EventTypePush = "push"
```

**How it fits:** `WebhookHandler.ServeHTTP` gets an `InputEvent` from
`toInputEvent`'s new case and routes it as it does every other event; `Plan`
calls `Match`, which handles `glob` in `conditionMatch`. `SaveRoutingRules`
calls `Validate`, which rejects bad patterns.

### Web UI

`webui/src/lib/routing-rules.ts`:

```ts
export const CONDITION_OPS = ['equals', 'prefix', 'contains', 'glob'] as const

export const OP_LABELS: Record<ConditionOp, string> = {
  equals: 'equals',
  prefix: 'starts with',
  contains: 'contains',
  glob: 'matches glob',
}
```

The condition row in `routing-rule-form.tsx` renders ops from `CONDITION_OPS`,
so the select picks the new op up. A save rejected for a bad pattern shows the
server's error the way other save errors are shown.

### GitHub App configuration

An operator change, not code: in the App settings add **Repository → Contents:
Read** and subscribe to **Push**. Each installation owner then accepts the new
permission; until they do, GitHub doesn't send that installation's pushes.

For our deployment, the App settings change ships with the ingestion slice. Before
that slice deploys, deliveries are answered `"ignored"`, so the order of the two
is harmless.

### Docs

| Page | Change |
| --- | --- |
| `concepts/routing-rules` | `glob` row in the operator table, with the wildcard summary; note that multi-valued attrs such as `path` hold when any value matches (already stated generally) |
| `reference/event-types` | new `### push` section under GitHub with its attrs |
| `integrations/github` | new row in the Events table (URL: the head commit); a sentence on tag pushes and branch deletions being ignored, and on scoping push rules by branch |
| `operators/server` | Contents: Read in the permissions table; Push in the subscribed events |

## Implementation Plan

1. **`glob` condition op (backend)** — Delivers: `doublestar` dependency, the
   `glob` case in `conditionMatch`, glob validation in `SchemaRegistry.Validate`,
   updated comments on `model.Condition`, `Validate` and `RuleCondition.op`
   (regenerated code), and the `glob` row in `concepts/routing-rules`. Depends on:
   nothing. Verifiable by: table tests in `match_test.go` (`**` across segments,
   `*` not crossing `/`, `{a,b}`, any-of-many values) and `schema_test.go`
   (malformed pattern rejected, valid accepted); saving a `label glob team-*`
   rule over the API and firing a matching event with `TestEvent`.
2. **`glob` in the rule form (web UI)** — Delivers: `glob` in `CONDITION_OPS`
   and `OP_LABELS`. Depends on: (1). Verifiable by: `pnpm lint`; creating,
   saving and reloading a rule with a `glob` condition in the UI, and seeing a
   malformed pattern's save error.
3. **Push event extraction** — Delivers: `EventTypePush`, the
   `*github.PushEvent` case in `toInputEvent` (attrs, `Details`, `Meta`,
   skipping tags, deletions and malformed payloads), and replacing the
   `push`-is-ignored cases in `webhook_test.go`. The schema is not registered
   yet, so no rule can select the type and pushes route to nothing. Depends on:
   nothing (parallel to 1–2). Verifiable by: `toInputEvent` table tests over
   push payloads (branch push, tag push, deletion, branch creation with no
   commits, paths deduplicated across commits, `sender` used for `AuthorID`);
   a webhook test asserting a push answers `"processed"`.
4. **Register `github`/`push` and document it** — Delivers: the `push` schema
   in `RegisterSchemas`, the e2e "unknown type" fixture moved off `push`, the
   docs pages `reference/event-types`, `integrations/github` and
   `operators/server`, and (at deploy) the App's Contents: Read permission and
   Push subscription. Depends on: (3), and (1) for the docs example's `glob`.
   Verifiable by: `GetEventTypes` lists `GitHub: Push`; `schema_test.go`
   accepts a `push` rule with `branch`/`path` conditions; a `route_test.go`
   case where a push with a matching `path` creates a task with the head
   commit URL as its trigger link, and a redelivery of the same push doesn't
   create a second one; `cd docs && pnpm build`; end to end, a push to
   `master` touching a matching file in a repo where the App is installed
   creates a task.

## Trade-offs

- **`glob` vs `prefix` alone.** `prefix` already covers "anything under a
  directory", and needs no dependency. It can't express extensions
  (`**/*.sql`), several files (`{go.mod,go.sum}`) or a directory at any depth,
  which are the common cases for path rules. `glob` is the familiar notation
  (GitHub Actions' `paths:`, CODEOWNERS, `.gitignore`).
- **`glob` vs `regex`.** A regex op is more powerful, but path patterns are
  much easier to write and read as globs, and regexes bring escaping mistakes
  and catastrophic patterns into user-saved rules. A `regex` op can be added
  later as another case.
- **Generic op vs path-specific matching.** Making `glob` a general op keeps
  the matcher source-agnostic (`Match` "never switches on the event source")
  and gives `branch glob release/*` for free.
- **Head commit URL vs compare URL.** See the review above: the compare URL is
  not unique for branch creations. The head commit URL can repeat when the same
  tip is pushed to two branches; with a `branch` condition only one push
  matches, and without one the second push wakes the first push's task, which
  is reasonable for "the same commits arrived again".
- **Not persisting the changed paths.** Keeps event rows small; the agent can
  recompute them from `before`/`after`. The cost is that the timeline row
  doesn't show which files matched.
- **One event per push vs one per commit.** Per push matches how GitHub
  delivers it and how a rule thinks about a merge to `master`; per-commit
  events would multiply tasks for a single merge.

## Open Questions

- **Loop guard.** Should pushes whose `sender` is the App's own bot user be
  dropped at ingestion, so a public push rule can't trigger itself through an
  agent's push? It removes a footgun but also stops "an agent pushed to a
  release branch" rules. The proposal relies on docs and branch conditions.
- **Negative path conditions.** "Every changed file is under `docs/`" (e.g.
  skip docs-only pushes) needs an all-of or `not` semantic, which conditions
  don't have. Out of scope here; worth a follow-up if push rules catch on.
- **Changed paths in `Details`.** Should a capped list of changed paths (say,
  the first 100, newline-joined) go into `Details`, so the agent and the
  timeline see them without running git?
- **`repo` on other GitHub events.** Add `repo` to the existing GitHub event
  types for consistency, as a separate change?