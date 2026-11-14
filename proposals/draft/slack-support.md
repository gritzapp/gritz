# Slack Support

Issue: https://github.com/gritzapp/gritz/issues/1951

Supersedes: `proposals/rejected/slack-integration.md` (PR #435, issue #439)

## Problem

Teams that coordinate in Slack can't start or wake a gritz task from Slack.
They should be able to @-mention a bot to create a task or wake one, and the
agent should reply in the same thread as that same bot.

The rejected proposal used one Slack app owned by gritz for inbound events.
Replies went through a separate Slack MCP server with a separate token, so the
bot that users tagged was not the bot that replied. It also subscribed to
`message.*` thread replies and needed history scopes for triggering. It used a
single global signing secret, and it had a gritz-owned OAuth install flow just
to map workspaces to orgs.

Issue #1951 sets a different direction. The user owns the bot. Only explicit
mentions trigger. All orgs share one request URL, and the signing secret is
stored per app.

## Design

### Overview

```
 Slack workspace                     gritz server                          sandbox
 ───────────────                     ────────────                          ───────
 @acme-bot fix the flaky test
   │  app_mention (signed with the app's signing secret)
   └──────────────► POST /webhook/slack
                     ├ look up slack_apps by api_app_id
                     ├ verify X-Slack-Signature with each candidate's secret
                     ├ claim (api_app_id, event_id)  ── duplicate → 200
                     ├ map (team, user) → gritz user via slack_accounts
                     └ eventrouter.Route (Orgs = owning orgs, OrgsOnly)
                          ├ subscribed link on thread key → wake task
                          └ else rule.Create → new task + trigger link ──► agent
                                                                             │
 reply in thread as @acme-bot ◄──── Slack MCP server (SLACK_BOT_TOKEN from secrets:)
```

Each org creates its own Slack app from a manifest that gritz generates. The
org registers the app's **App ID** and **Signing Secret** with gritz. Gritz
uses the signing secret only to verify inbound requests. The app's **Bot User
OAuth Token** (`xoxb-…`) never reaches gritz. It goes into the workspace
`secrets:` map, and a Slack MCP server in the sandbox uses it to reply. The
bot users tag is therefore the bot that replies.

### What carries over from the rejected proposal, and what changes

| Area | Rejected proposal | This proposal | Why |
|---|---|---|---|
| App ownership | One gritz-owned app; gritz OAuth install flow | Each org creates its own app from a gritz-provided manifest | The tagged bot must be the replying bot. With an org-owned app, gritz needs no OAuth client and stores no tokens. |
| Signing secret | One global `--slack-signing-secret` flag | Per-app row in `slack_apps`, found by `api_app_id` | One request URL for every org, no `?org=`, more than one app per org. The integration is always on, with no server flags. |
| Workspace → org mapping | `orgs.slack_team_id` set by OAuth | `slack_apps.org_id`; the app belongs to the org that registered it | The app ID is already in every payload, so there's no extra mapping to keep. |
| Triggers | `app_mention` and `message` thread replies, with a `gritz:` prefix | `app_mention` only | Agreed in #1951: only an explicit mention wakes a task, and no `message.*` subscriptions are needed. |
| Event URL / key | `https://slack.com/archives/{channel}/p{ts}` matched exactly | Thread URL `https://app.slack.com/client/{team}/{channel}/thread/{channel}-{thread_ts}`, normalized by `model.RoutingKey` | The key must name the thread, not the message, so every mention in the thread routes to one task. It needs the team ID because permalinks carry the workspace domain, which isn't in the payload. |
| Account linking | Slack OAuth; `users.slack_user_id` | `/gritz link` slash command, confirmed in the web UI; `slack_accounts(team_id, slack_user_id)` | No gritz-owned OAuth app exists to sign in with. Slack user IDs are only meaningful per team, and one gritz user can be in several workspaces. |
| Dedup | "check for duplicate `event_id`" | `slack_events` claim table keyed by `(api_app_id, event_id)`, released on routing failure | Slack's own retries become the redelivery mechanism. |
| Outbound | Workspace MCP server (no built-in posting) | Same, now with the same bot token | Kept: gritz still doesn't post to Slack. |
| Webhook verification | HMAC `v0:{ts}:{body}`, 5-minute window, `url_verification` | Same | Kept. |
| Settings UI | Account card + workspace install card | Org "Slack Apps" card (manifest, register, delete) + user "Slack Accounts" card | Follows the new ownership model. |

### Database

New migration `internal/store/sql/migrations/20261103000001_slack.sql`:

```sql
-- migrate:up

-- A Slack app an org created from the gritz manifest. app_id is Slack's
-- api_app_id (A0…). It is not globally unique: the webhook verifies against
-- every row for an app_id, so registering someone else's app_id with a wrong
-- secret can't block or hijack the real registration (see Webhook handler).
CREATE TABLE slack_apps (
    id BIGSERIAL PRIMARY KEY,
    org_id BIGINT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    app_id TEXT NOT NULL,
    name TEXT NOT NULL,
    signing_secret TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (org_id, app_id)
);
CREATE INDEX idx_slack_apps_app_id ON slack_apps(app_id);

-- Delivered Events API callbacks, for dropping Slack retries. Rows only need
-- to outlive Slack's retry schedule (immediately, ~1 min, ~5 min); the
-- handler prunes rows older than a day.
CREATE TABLE slack_events (
    app_id TEXT NOT NULL,
    event_id TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (app_id, event_id)
);
CREATE INDEX idx_slack_events_created_at ON slack_events(created_at);

-- migrate:down

DROP TABLE slack_events;
DROP TABLE slack_apps;
```

A second migration, `20261103000002_slack_accounts.sql`, lands with account
linking:

```sql
-- migrate:up

-- A Slack identity linked to a gritz user. Slack user ids are scoped to the
-- team, so the key is (team_id, slack_user_id). A gritz user may link several.
CREATE TABLE slack_accounts (
    team_id TEXT NOT NULL,
    slack_user_id TEXT NOT NULL,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    team_domain TEXT NOT NULL,
    slack_username TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (team_id, slack_user_id)
);
CREATE INDEX idx_slack_accounts_user_id ON slack_accounts(user_id);

-- One-time codes minted by the /gritz link slash command. code_hash is the
-- SHA-256 of the code; the plaintext only travels in the ephemeral reply.
CREATE TABLE slack_link_codes (
    code_hash TEXT PRIMARY KEY,
    team_id TEXT NOT NULL,
    team_domain TEXT NOT NULL,
    slack_user_id TEXT NOT NULL,
    slack_username TEXT NOT NULL,
    expires_at TIMESTAMP NOT NULL
);

-- migrate:down

DROP TABLE slack_link_codes;
DROP TABLE slack_accounts;
```

Queries go in a new `internal/store/sql/queries/slack.sql`:

```sql
-- name: CreateSlackApp :one
INSERT INTO slack_apps (org_id, app_id, name, signing_secret)
VALUES ($1, $2, $3, $4)
RETURNING id, created_at;

-- name: ListSlackApps :many
SELECT id, org_id, app_id, name, created_at FROM slack_apps
WHERE org_id = $1 ORDER BY created_at;

-- name: ListSlackAppsByAppID :many
-- Every registration of an api_app_id across non-archived orgs, with secrets,
-- for webhook verification.
SELECT a.id, a.org_id, a.app_id, a.name, a.signing_secret, a.created_at
FROM slack_apps a JOIN orgs o ON o.id = a.org_id
WHERE a.app_id = $1 AND o.archived = FALSE;

-- name: DeleteSlackApp :exec
DELETE FROM slack_apps WHERE id = $1 AND org_id = $2;

-- name: ClaimSlackEvent :one
-- Inserts the delivery; returns false when it was already claimed.
WITH pruned AS (
    DELETE FROM slack_events WHERE created_at < CURRENT_TIMESTAMP - INTERVAL '1 day'
)
INSERT INTO slack_events (app_id, event_id) VALUES ($1, $2)
ON CONFLICT DO NOTHING
RETURNING TRUE;

-- name: ReleaseSlackEvent :exec
DELETE FROM slack_events WHERE app_id = $1 AND event_id = $2;

-- name: CreateSlackLinkCode :exec
INSERT INTO slack_link_codes (code_hash, team_id, team_domain, slack_user_id, slack_username, expires_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetSlackLinkCode :one
SELECT team_id, team_domain, slack_user_id, slack_username FROM slack_link_codes
WHERE code_hash = $1 AND expires_at > CURRENT_TIMESTAMP;

-- name: ConsumeSlackLinkCode :one
DELETE FROM slack_link_codes
WHERE code_hash = $1 AND expires_at > CURRENT_TIMESTAMP
RETURNING team_id, team_domain, slack_user_id, slack_username;

-- name: UpsertSlackAccount :exec
INSERT INTO slack_accounts (team_id, slack_user_id, user_id, team_domain, slack_username)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (team_id, slack_user_id) DO UPDATE SET
    user_id = EXCLUDED.user_id,
    team_domain = EXCLUDED.team_domain,
    slack_username = EXCLUDED.slack_username,
    created_at = CURRENT_TIMESTAMP;

-- name: ListSlackAccountsByUser :many
SELECT team_id, slack_user_id, user_id, team_domain, slack_username, created_at
FROM slack_accounts WHERE user_id = $1 ORDER BY created_at;

-- name: DeleteSlackAccount :exec
DELETE FROM slack_accounts WHERE team_id = $1 AND slack_user_id = $2 AND user_id = $3;

-- name: GetUserBySlackAccount :one
SELECT u.* FROM users u JOIN slack_accounts s ON s.user_id = u.id
WHERE s.team_id = $1 AND s.slack_user_id = $2;
```

Expired link codes are deleted by the same `CreateSlackLinkCode` call site
(`DELETE … WHERE expires_at < now()` before the insert). Volume is one row per
`/gritz link` invocation.

Signing secrets are stored in plaintext, the same way as
`orgs.atlassian_webhook_secret`. `ListSlackApps` never selects the secret, and
no RPC returns it after creation.

### Routing-rule scoping: `InputEvent.OrgsOnly`

Today a linked actor's event is evaluated against **every** org the actor
belongs to (`ListRoutingRulesForEvent`, the `org_members` branch), and
`input.Orgs` only adds non-member orgs for `Public` rules. That works for
GitHub, where one shared app serves everyone. It's wrong for Slack. A mention
of org A's bot must not fire org B's rules just because the sender belongs to
both orgs: org B's agent would have no token for org A's bot, so it couldn't
reply as the bot that was tagged.

Add one field:

```go
// OrgsOnly restricts routing to the orgs in Orgs. UserID then only decides
// whether the actor is a member of each (all rules) or not (Public rules
// only); the actor's other memberships are not consulted.
OrgsOnly bool
```

`Router.Plan` calls a new `Store.ListRoutingRulesForOrgs(ctx, tx, userID, orgs)`
when `input.OrgsOnly` is set:

```sql
-- name: ListRoutingRulesForOrgs :many
SELECT o.id, o.routing_rules,
       EXISTS(SELECT 1 FROM org_members m
              WHERE m.org_id = o.id AND m.user_id = sqlc.arg(user_id)) AS is_member
FROM orgs o
WHERE o.id = ANY(sqlc.arg(org_ids)::BIGINT[]) AND o.archived = FALSE;
```

The rest of `Plan` (`IsMember`/`Public` filtering) is unchanged. GitHub and
Atlassian leave `OrgsOnly` false. `Plan` panics when `OrgsOnly` is set with an
empty `Orgs`, because no production path builds that event.

### Sender identity and account linking

The webhook maps the sender with `GetUserBySlackAccount(team_id, event.user)`:

- **Linked:** `input.UserID = user.ID`. If the user is a member of the app's
  org, every rule applies. If not, only `Public` rules apply.
- **Not linked:** `UserID` stays empty, and only `Public` rules fire. This is
  the same as unlinked Jira and GitHub actors (`eventrouter-non-member-events`).

`team_id` is the envelope's `team_id`, the workspace the app is installed in.
The slash command payload's `team_id` is the same workspace, so both sides key
the same way.

**Linking flow (`/gritz link`).** The manifest declares a slash command whose
request URL is `POST {base}/webhook/slack/command`. That URL is also shared by
every org. The handler:

1. Parses the form body and looks up `api_app_id` in `slack_apps`. It verifies
   the signature exactly like the events handler (same headers, same scheme).
2. For `text == "link"`, mints a 32-byte random code. It stores the SHA-256 of
   the code with the payload's `team_id`, `team_domain`, `user_id` and
   `user_name`, valid for 10 minutes. It replies with an ephemeral message
   (`{"response_type":"ephemeral","text":"…"}`) linking to
   `{base}/ui/slack/link?code=…`. Any other text gets a short ephemeral usage
   message. The reply is the HTTP response, so gritz needs no token to send it.
3. The web route calls `GetSlackLinkCode` to show "Link Slack user
   **@ilia** in **acme.slack.com** to your gritz account?" and a confirm
   button. The button calls `LinkSlackAccount`, which consumes the code and
   upserts `slack_accounts` for the caller.

Only the invoking Slack user sees the ephemeral reply. The code is single-use
and short-lived, and linking requires a logged-in gritz session. Re-linking the
same Slack identity moves it to the new gritz user (`ON CONFLICT … DO UPDATE`),
just as running OAuth again does for GitHub. Linking is per user, not per org:
one link serves every gritz org that registered an app in that Slack workspace.

### Webhook handler

New package `internal/server/slackserver`, structured like
`atlassianserver/webhook.go`, mounted unconditionally at `POST /webhook/slack`.
Slack needs no server-wide configuration, so there is nothing to gate on.

Steps:

1. **Read the body** (capped with `http.MaxBytesReader`, 1 MiB) and parse the
   outer envelope with `slack.ParseEnvelope`.
2. **`url_verification`:** respond `200 {"challenge": …}`. This body carries no
   `api_app_id`, so the app can't be found and the signature can't be checked.
   The response echoes the request's own value and changes no state, so
   answering it unauthenticated is harmless. It's also required: Slack checks
   the request URL as soon as the app is created from the manifest, before the
   org has registered the signing secret.
3. **`event_callback`:** look up `ListSlackAppsByAppID(api_app_id)`. Keep the
   registrations whose secret passes `slack.Verify`. Verify rejects a missing
   or malformed header, a timestamp more than 5 minutes from now (replay), and
   a signature mismatch (`hmac.Equal` over `v0=` + hex HMAC-SHA256 of
   `v0:{timestamp}:{body}`). If no registration verifies, respond `401`, so the
   failure is visible in the app's Event Subscriptions page.
4. **Dedup:** `ClaimSlackEvent(api_app_id, event_id)`. If the row was already
   claimed, respond `200` and log at debug with `X-Slack-Retry-Num` and
   `X-Slack-Retry-Reason`. The claim decides; the retry headers are only logged.
5. **Extract** the `app_mention` (`toInputEvent`, below). Ignore the event
   (`200`) if the inner type isn't `app_mention`, if `event.user` is empty, or
   if `event.user` is the app's own bot user (`authorizations[0].user_id`).
   That last check stops an agent that posts the bot's mention from waking
   itself.
6. **Identity:** `GetUserBySlackAccount(team_id, event.user)`, as above.
   `input.Orgs` = the distinct `org_id`s of the verified registrations, and
   `input.OrgsOnly = true`.
7. **Route** synchronously with `Router.Route`. On error, call
   `ReleaseSlackEvent` and respond `500` so Slack's next retry processes the
   event again. On success, respond `200`.

**The 3-second budget.** Routing is a handful of indexed queries and one
transaction per woken or created task, the same synchronous path GitHub
webhooks use, and well under 3 seconds in practice. If it ever runs long,
Slack retries. The retry finds the claim and gets `200`, and the first attempt
still completes. If that first attempt then fails, it releases the claim, and
Slack's later retries (~1 and ~5 minutes) redeliver. Slack's retry schedule is
the redelivery queue, so gritz doesn't need a server-side outbox. Acking first
and routing in a goroutine would lose the event on a crash, with no retry left
to recover it.

**Several registrations of one `app_id`.** A Slack app has one signing
secret, so only someone who holds it can produce a verifying signature.
Someone who registers another org's `app_id` with their own secret can only
forge events into their own org. They can't block the real registration,
because the lookup isn't unique. If two orgs both hold the real secret (the
same app registered in a staging org and a production org), both verify, and
the event belongs to both, just as GitHub events belong to every org sharing an
installation.

`toInputEvent`:

```go
// toInputEvent turns a verified app_mention into an InputEvent, or nil to ignore.
func toInputEvent(env *slack.Envelope) (*eventrouter.InputEvent, error) {
    m, err := env.AppMention()
    // ... nil for non-app_mention, empty user, or the bot's own user
    thread := cmp.Or(m.ThreadTS, m.TS)
    url := slack.ThreadURL(env.TeamID, m.Channel, thread)
    return &eventrouter.InputEvent{
        Source:      "slack",
        Type:        EventTypeAppMention,
        Description: fmt.Sprintf("<@%s> mentioned the app in <#%s>", m.User, m.Channel),
        Data:        m.Text,
        URL:         url,
        Attrs: eventrouter.Attrs{
            "user":    {m.User},
            "channel": {m.Channel},
            "app":     {env.APIAppID},
            "team":    {env.TeamID},
        },
        Details: map[string]string{
            "team":      env.TeamID,
            "channel":   m.Channel,
            "ts":        m.TS,
            "thread_ts": thread,
            "user":      m.User,
        },
    }, nil
}
```

`Details` reach the agent through `get_my_task`. `channel` and `thread_ts` are
exactly the arguments `slack_reply_to_thread` takes. The description uses
Slack's `<@U…>`/`<#C…>` syntax because gritz has no token to resolve names. The
agent can resolve them with the MCP server's `slack_get_user_profile`.

### Event schema

`internal/server/slackserver/schema.go` registers one type, following
`atlassianserver/schema.go`:

```go
const EventTypeAppMention = "app_mention"

func RegisterSchemas(reg *eventrouter.SchemaRegistry) {
    reg.MustRegister(eventrouter.EventTypeDef{
        Source: "slack",
        Type:   EventTypeAppMention,
        Label:  "Slack: App Mention",
        Attrs: []eventrouter.AttrDef{
            {Key: "body", Label: "Message Text", Placeholder: "<@U0123ABCD> deploy",
                Help: "Matched against the raw message text, including Slack's <@U…> mention tokens."},
            {Key: "url", Label: "Thread URL", Placeholder: "https://app.slack.com/client/T0123ABCD/C0123ABCD/",
                Help: "Matched against the thread URL, e.g. a prefix to scope a rule to one channel."},
            {Key: "user", Label: "User", Placeholder: "U0123ABCD",
                Help: "Slack user id of the person who mentioned the app."},
            {Key: "channel", Label: "Channel", Placeholder: "C0123ABCD",
                Help: "Slack channel id the mention was posted in."},
            {Key: "app", Label: "App", Placeholder: "A0123ABCD",
                Help: "Slack App ID that was mentioned, for orgs that register more than one app."},
            {Key: "team", Label: "Workspace", Placeholder: "T0123ABCD",
                Help: "Slack team (workspace) id."},
        },
    })
}
```

There's no `mention` attr. Every event of this type is a mention of the app,
and `app` already says which one.

Example rule (create on mention in one channel, open to non-members):

```json
{
  "source": "slack", "type": "app_mention", "public": true, "wakeup": true,
  "conditions": [{"attr": "channel", "op": "equals", "value": "C0123ABCD"}],
  "create": {"workspace": "gritz", "runner": "prod",
             "prompt": "You were mentioned in Slack. Reply in the thread with the slack MCP server."}
}
```

### Routing key

A Slack thread is identified by `team/channel/thread_ts`. A top-level mention
has no `thread_ts`, so its `ts` is used: when the agent replies in the thread,
Slack sets every reply's `thread_ts` to that `ts`, so later mentions in the
thread produce the same key.

The canonical form is a Slack web-client thread URL, which a user can open:

```
https://app.slack.com/client/{team}/{channel}/thread/{channel}-{thread_ts}
https://app.slack.com/client/T0123ABCD/C0123ABCD/thread/C0123ABCD-1730650000.123456
```

`slack.ThreadURL` builds it, and `model.RoutingKey` gets a case for it:

```go
case u.Host == "app.slack.com":
    // /client/{team}/{channel}/thread/{channel}-{ts}[/...] — trailing segments
    // and query are dropped.
    if m := slackThreadRe.FindStringSubmatch(u.Path); m != nil {
        return fmt.Sprintf("https://app.slack.com/client/%s/%s/thread/%s-%s", m[1], m[2], m[2], m[4])
    }

slackThreadRe = regexp.MustCompile(`^/client/([A-Z0-9]+)/([A-Z0-9]+)/thread/([A-Z0-9]+)-(\d+\.\d+)`)
```

Permalinks (`https://acme.slack.com/archives/C…/p1730650000123456?thread_ts=…`)
can't be normalized to this key. They carry the workspace domain, not the team
ID, and `RoutingKey` is a pure function with no lookup. So a subscribed link to
a permalink would silently never match. `CreateLink` rejects
`subscribe=true` on a `*.slack.com/archives/` URL with `InvalidArgument`, and
the message gives the thread-URL form. Unsubscribed reference links to
permalinks are still allowed.

**Tasks created from a mention.** `Router.create` already adds a subscribed
`trigger` link to `input.URL`, which is the thread URL. Every later mention in
that thread wakes the task, with no extra work.

**Agent-started threads.** An agent that posts a new top-level message (for
example, a scheduled task announcing a result) gets `channel` and `ts` back
from `slack_post_message`. It subscribes with:

```
create_link(url="https://app.slack.com/client/{SLACK_TEAM_ID}/{channel}/thread/{channel}-{ts}", subscribe=true)
```

`SLACK_TEAM_ID` is already in the sandbox environment, because the MCP server
requires it (see workspace config). The workspace docs and the example rule
prompt spell this format out.

Thread replies that don't mention the bot never reach gritz: the app has no
`message.*` subscription. This is deliberate (#1951). A task in a busy thread
wakes only when someone addresses the bot.

### App manifest

`slack.Manifest` renders the manifest with the server's base URL and a
user-chosen name. `GetSlackAppManifest` returns it, and the settings UI offers
it both as copyable YAML and as a "Create Slack app" link to
`https://api.slack.com/apps?new_app=1&manifest_json=<url-encoded JSON>`.

```yaml
display_information:
  name: acme-bot
  description: Mention me to start or wake a gritz task.
features:
  bot_user:
    display_name: acme-bot
    always_online: true
  slash_commands:
    - command: /gritz
      url: https://gritz.dev/webhook/slack/command
      description: Link your Slack account to gritz
      usage_hint: link
oauth_config:
  scopes:
    bot:
      # Triggering (used by gritz)
      - app_mentions:read
      - commands
      # Replying (used by the Slack MCP server in the sandbox)
      - chat:write
      - reactions:write
      - channels:history
      - groups:history
      - channels:read
      - users:read
      - users.profile:read
settings:
  event_subscriptions:
    request_url: https://gritz.dev/webhook/slack
    bot_events:
      - app_mention
  org_deploy_enabled: false
  socket_mode_enabled: false
  token_rotation_enabled: false
```

Gritz itself needs only `app_mentions:read` and `commands`. The history scopes
are for the agent, which reads thread context through the MCP server
(`slack_get_thread_replies`). They don't affect what triggers a task, because
there are no `message.*` events. An org that wants a write-only bot can remove
them. Token rotation stays off because a rotating token can't live in a static
workspace secret.

### RPCs

Added to `GritzService`:

```protobuf
rpc CreateSlackApp(CreateSlackAppRequest) returns (CreateSlackAppResponse);
rpc ListSlackApps(ListSlackAppsRequest) returns (ListSlackAppsResponse);
rpc DeleteSlackApp(DeleteSlackAppRequest) returns (DeleteSlackAppResponse);
rpc GetSlackAppManifest(GetSlackAppManifestRequest) returns (GetSlackAppManifestResponse);
rpc GetSlackLinkCode(GetSlackLinkCodeRequest) returns (GetSlackLinkCodeResponse);
rpc LinkSlackAccount(LinkSlackAccountRequest) returns (LinkSlackAccountResponse);
rpc UnlinkSlackAccount(UnlinkSlackAccountRequest) returns (UnlinkSlackAccountResponse);

// A Slack app registered to the caller's org. The signing secret is
// write-only: set on create, never returned.
message SlackApp {
  int64 id = 1;
  string app_id = 2;
  string name = 3;
  google.protobuf.Timestamp created_at = 4;
}

message CreateSlackAppRequest {
  string app_id = 1;          // Basic Information → App ID (A…)
  string signing_secret = 2;  // Basic Information → Signing Secret
  string name = 3;
}
message CreateSlackAppResponse { SlackApp app = 1; }

message ListSlackAppsRequest {}
message ListSlackAppsResponse {
  repeated SlackApp apps = 1;
  string events_url = 2;   // {base}/webhook/slack
  string command_url = 3;  // {base}/webhook/slack/command
}

message DeleteSlackAppRequest { int64 id = 1; }
message DeleteSlackAppResponse {}

message GetSlackAppManifestRequest { string name = 1; }
message GetSlackAppManifestResponse {
  string yaml = 1;
  string create_url = 2;  // api.slack.com/apps?new_app=1&manifest_json=…
}

message SlackAccount {
  string team_id = 1;
  string team_domain = 2;
  string slack_user_id = 3;
  string slack_username = 4;
  google.protobuf.Timestamp created_at = 5;
}

message GetSlackLinkCodeRequest { string code = 1; }
message GetSlackLinkCodeResponse { SlackAccount account = 1; }  // what would be linked

message LinkSlackAccountRequest { string code = 1; }
message LinkSlackAccountResponse { SlackAccount account = 1; }

message UnlinkSlackAccountRequest {
  string team_id = 1;
  string slack_user_id = 2;
}
message UnlinkSlackAccountResponse {}
```

`GetProfileResponse` gains `repeated SlackAccount slack_accounts = 6;` next to
`github_account` and `atlassian_account`.

Permissions follow the existing integration RPCs in `apiserver/org.go`:
`CreateSlackApp`/`DeleteSlackApp` need `OpOrgWrite`, while
`ListSlackApps`/`GetSlackAppManifest` need `OpOrgRead`. The three account RPCs
need `OpAccountWrite` and a user identity (`caller.ID`), like
`UnlinkAtlassianAccount`. `CreateSlackApp` checks that `app_id` matches
`^A[A-Z0-9]+$` and that the secret is non-empty, and returns `AlreadyExists`
on the `(org_id, app_id)` unique violation. An unknown or expired code returns
`NotFound`.

### Settings UI

Changes to `webui/src/routes/settings.tsx`:

- **Slack Apps** (org section, next to "Atlassian Webhook"), with a list of
  registered apps (name, App ID, created) and a delete button on each.
  "Add Slack app" opens a three-step dialog:
  1. Enter a bot name. The dialog shows the manifest (copy button) and a
     **Create Slack app** button (`create_url`, new tab).
  2. Paste the **App ID** and **Signing Secret** from the app's *Basic
     Information* page, then call `CreateSlackApp`.
  3. Install the app to the workspace, then copy the **Bot User OAuth Token**
     into the workspace's `secrets:` as `SLACK_BOT_TOKEN`, along with the team
     ID as `SLACK_TEAM_ID`. The step shows the snippet from "Workspace config"
     below. Gritz never asks for the token.
- **Slack Accounts** (account section, next to "Atlassian Account"), with a
  list of linked identities (`@user` in `team_domain`) and an unlink button on
  each. The empty state says: *"Run `/gritz link` in Slack to link your
  account."*

New route `webui/src/routes/slack.link.tsx` (`/ui/slack/link?code=…`) is the
confirmation page. It calls `GetSlackLinkCode`, shows the identity, and the
confirm button calls `LinkSlackAccount` and then redirects to
`/ui/settings`. `external-source.tsx` gains a `slack` case with a
`SlackIcon`, so Slack events and thread links render with the right icon.

### Workspace config

A new README "Slack" section, plus an example in `gritzapp/workspaces`, use
[`@zencoderai/slack-mcp-server`](https://github.com/zencoderai/slack-mcp-server).
It's the maintained continuation of the archived `@modelcontextprotocol`
reference server. It authenticates with a bot token only (`SLACK_BOT_TOKEN`,
`SLACK_TEAM_ID`), runs over stdio, and its tool set matches what an agent
replying in a thread needs: `slack_reply_to_thread`, `slack_post_message`,
`slack_add_reaction`, `slack_get_thread_replies`, `slack_get_user_profile`.

```yaml
workspaces:
  gritz:
    secrets:
      SLACK_BOT_TOKEN: ${env:ACME_SLACK_BOT_TOKEN}   # Bot User OAuth Token (xoxb-…)
    container:
      environment:
        SLACK_TEAM_ID: T0123ABCD                     # not a secret
    commands:
      - npm install -g @zencoderai/slack-mcp-server
    agent:
      type: claude
      mcp_servers:
        slack:
          type: stdio
          command: slack-mcp
```

The MCP server inherits `SLACK_BOT_TOKEN` and `SLACK_TEAM_ID` from the
sandbox environment, so the token appears nowhere in the agent config. Because
it's declared under `secrets:`, the driver masks it as
`[gritz:masked SLACK_BOT_TOKEN]` in the shipped log. The docs also cover:

- the Slack-created task's `Details` (`channel`, `thread_ts`) as the arguments
  to `slack_reply_to_thread`;
- the thread-URL format for `create_link` when the agent starts a thread
  itself;
- that the token in this workspace must belong to the same app whose App ID is
  registered in the org. Otherwise the bot that replies isn't the bot that was
  tagged.

### API

```go
package slack // github.com/gritzapp/gritz/internal/x/slack

// Envelope is the outer Events API request body.
type Envelope struct {
    Type           string // "url_verification" or "event_callback"
    Challenge      string
    TeamID         string
    APIAppID       string
    EventID        string
    Authorizations []Authorization
    Event          json.RawMessage
}

// Authorization names the installation an event was delivered for.
type Authorization struct {
    TeamID string
    UserID string // the app's bot user
    IsBot  bool
}

// AppMention is the inner event of an app_mention callback.
type AppMention struct {
    User     string
    Text     string
    Channel  string
    TS       string
    ThreadTS string // empty for a top-level mention
}

// SlashCommand is a slash-command request body.
type SlashCommand struct {
    APIAppID   string
    TeamID     string
    TeamDomain string
    UserID     string
    UserName   string
    Command    string
    Text       string
}

// ParseEnvelope decodes an Events API request body.
func ParseEnvelope(body []byte) (*Envelope, error)

// AppMention decodes the inner event, returning nil if it isn't an app_mention.
func (e *Envelope) AppMention() (*AppMention, error)

// ParseSlashCommand decodes a form-encoded slash-command body.
func ParseSlashCommand(body []byte) (*SlashCommand, error)

// Verify checks the request's v0 signature against secret and rejects stale timestamps.
func Verify(secret string, header http.Header, body []byte, now time.Time) error

// ThreadURL returns the web-client URL of the thread rooted at threadTS.
func ThreadURL(team, channel, threadTS string) string

// ManifestOptions parameterizes the app manifest.
type ManifestOptions struct {
    Name       string
    EventsURL  string
    CommandURL string
}

// Manifest returns the app manifest as YAML and as JSON.
func Manifest(opts ManifestOptions) (yaml, json []byte)
```

```go
package slackserver // github.com/gritzapp/gritz/internal/server/slackserver

// EventTypeAppMention is the InputEvent.Type of a Slack app mention.
const EventTypeAppMention = "app_mention"

// Server serves the Slack webhooks and backs the Slack RPCs.
type Server struct{ ... }

// Options configures a Server.
type Options struct {
    Log       *slog.Logger
    Store     *store.Store
    BaseURL   string
    Publisher pubsub.Publisher
}

// New returns a Server.
func New(opts Options) *Server

// EventsHandler returns the Events API handler mounted at /webhook/slack.
func (s *Server) EventsHandler() http.Handler

// CommandHandler returns the slash-command handler mounted at /webhook/slack/command.
func (s *Server) CommandHandler() http.Handler

// EventsURL returns the shared Events API request URL.
func (s *Server) EventsURL() string

// CommandURL returns the shared slash-command request URL.
func (s *Server) CommandURL() string

// Manifest returns the app manifest for a bot named name.
func (s *Server) Manifest(name string) (yaml, createURL string)

// RegisterSchemas records the Slack event types on reg.
func RegisterSchemas(reg *eventrouter.SchemaRegistry)

// Store is the subset of store.Store the webhook handlers use.
type Store interface {
    ListSlackAppsByAppID(ctx context.Context, tx *sql.Tx, appID string) ([]*model.SlackApp, error)
    ClaimSlackEvent(ctx context.Context, tx *sql.Tx, appID, eventID string) (bool, error)
    ReleaseSlackEvent(ctx context.Context, tx *sql.Tx, appID, eventID string) error
    GetUserBySlackAccount(ctx context.Context, tx *sql.Tx, teamID, slackUserID string) (*model.User, error)
    CreateSlackLinkCode(ctx context.Context, tx *sql.Tx, code *model.SlackLinkCode) error
}

// Router routes events to subscribed tasks.
type Router interface {
    Route(ctx context.Context, input eventrouter.InputEvent) (int, error)
}
```

```go
package model // github.com/gritzapp/gritz/internal/model

// SlackApp is a Slack app registered to an org. // new
type SlackApp struct {
    ID            int64
    OrgID         int64
    AppID         string
    Name          string
    SigningSecret string // set only by ListSlackAppsByAppID
    CreatedAt     time.Time
}

// Proto renders the app without its signing secret. // new
func (a *SlackApp) Proto() *gritzv1.SlackApp

// SlackAccount is a Slack identity linked to a gritz user. // new
type SlackAccount struct {
    TeamID        string
    TeamDomain    string
    SlackUserID   string
    SlackUsername string
    UserID        string
    CreatedAt     time.Time
}

// Proto renders the account. // new
func (a *SlackAccount) Proto() *gritzv1.SlackAccount

// SlackLinkCode is a pending /gritz link request; CodeHash is the SHA-256 of the code. // new
type SlackLinkCode struct {
    CodeHash      string
    TeamID        string
    TeamDomain    string
    SlackUserID   string
    SlackUsername string
    ExpiresAt     time.Time
}

// RoutingKey now also normalizes app.slack.com thread URLs. // changed
func RoutingKey(raw string) string
```

```go
package eventrouter // github.com/gritzapp/gritz/internal/server/eventrouter

type InputEvent struct {
    // ...
    // OrgsOnly restricts routing to Orgs; UserID only decides membership within them. // new
    OrgsOnly bool
}
```

```go
package store // github.com/gritzapp/gritz/internal/store

// CreateSlackApp inserts a and sets its ID and CreatedAt. // new
func (s *Store) CreateSlackApp(ctx context.Context, tx *sql.Tx, a *model.SlackApp) error
// ListSlackApps returns the org's apps without secrets. // new
func (s *Store) ListSlackApps(ctx context.Context, tx *sql.Tx, orgID int64) ([]*model.SlackApp, error)
// ListSlackAppsByAppID returns every live registration of appID, with secrets. // new
func (s *Store) ListSlackAppsByAppID(ctx context.Context, tx *sql.Tx, appID string) ([]*model.SlackApp, error)
// DeleteSlackApp removes the org's app. // new
func (s *Store) DeleteSlackApp(ctx context.Context, tx *sql.Tx, orgID, id int64) error
// ClaimSlackEvent records a delivery and reports whether it was new. // new
func (s *Store) ClaimSlackEvent(ctx context.Context, tx *sql.Tx, appID, eventID string) (bool, error)
// ReleaseSlackEvent forgets a delivery so a retry is processed. // new
func (s *Store) ReleaseSlackEvent(ctx context.Context, tx *sql.Tx, appID, eventID string) error
// CreateSlackLinkCode stores a pending link code and prunes expired ones. // new
func (s *Store) CreateSlackLinkCode(ctx context.Context, tx *sql.Tx, c *model.SlackLinkCode) error
// GetSlackLinkCode returns an unexpired code without consuming it. // new
func (s *Store) GetSlackLinkCode(ctx context.Context, tx *sql.Tx, codeHash string) (*model.SlackLinkCode, error)
// LinkSlackAccount consumes the code and links its identity to userID. // new
func (s *Store) LinkSlackAccount(ctx context.Context, tx *sql.Tx, codeHash, userID string) (*model.SlackAccount, error)
// ListSlackAccounts returns the user's linked Slack identities. // new
func (s *Store) ListSlackAccounts(ctx context.Context, tx *sql.Tx, userID string) ([]*model.SlackAccount, error)
// UnlinkSlackAccount removes one of the user's Slack identities. // new
func (s *Store) UnlinkSlackAccount(ctx context.Context, tx *sql.Tx, userID, teamID, slackUserID string) error
// GetUserBySlackAccount returns the gritz user linked to the Slack identity. // new
func (s *Store) GetUserBySlackAccount(ctx context.Context, tx *sql.Tx, teamID, slackUserID string) (*model.User, error)
// ListRoutingRulesForOrgs returns rules for exactly orgs, flagging the user's membership. // new
func (s *Store) ListRoutingRulesForOrgs(ctx context.Context, tx *sql.Tx, userID string, orgs []int64) ([]OrgRoutingRules, error)
```

**How it fits:** `internal/command/server.go` always constructs a
`slackserver.Server` and passes it to `server.Options.Slack` and
`apiserver.Options.Slack`. `server.Handler` mounts `EventsHandler` at
`/webhook/slack` and `CommandHandler` at `/webhook/slack/command`. Both are
unauthenticated, because the Slack signature is the authentication. The events
handler owns an `eventrouter.Router` (no `OnRouteOutcome`: gritz has no token
to react with) and calls `Route` with `OrgsOnly` set. `eventrouter.Router.Plan`
picks `ListRoutingRulesForOrgs` when `OrgsOnly` is set. The apiserver Slack RPCs
call the store directly, plus `slackserver.Manifest`/`EventsURL`/`CommandURL`.
`apiserver.CreateLink` calls `model.RoutingKey` as it does today, and rejects
subscribed Slack permalinks. `slackserver`'s `init` registers its schema on
`eventrouter.DefaultSchemaRegistry`, so `SchemaRegistry.Validate` accepts
`slack`/`app_mention` rules and `GetEventTypes` lists them in the rule editor.

## Implementation Plan

1. **Slack routing key** — Delivers: the `app.slack.com` thread case in
   `model.RoutingKey`, plus `CreateLink` rejecting subscribed `*.slack.com/archives/`
   permalinks. Depends on: nothing. Verifiable by: `RoutingKey` table tests
   and a `CreateLink` handler test.
2. **`internal/x/slack`** — Delivers: `ParseEnvelope`, `AppMention`,
   `ParseSlashCommand`, `Verify`, `ThreadURL`, `Manifest`. Depends on:
   nothing. Verifiable by: unit tests using Slack's documented signing example
   and recorded payloads.
3. **App registration schema + store** — Delivers: the `slack_apps` and
   `slack_events` migration, `slack.sql` queries, `model.SlackApp`, and the
   store methods. Depends on: nothing. Verifiable by: store tests against
   Postgres (claim, release, prune, multiple registrations per `app_id`).
4. **App registration RPCs** — Delivers: `CreateSlackApp`, `ListSlackApps`,
   `DeleteSlackApp`, `GetSlackAppManifest`, and `slackserver.New` wired into
   apiserver. Depends on: (2), (3). Verifiable by: apiserver handler tests,
   including permissions and secret never returned.
5. **Org-scoped routing** — Delivers: `InputEvent.OrgsOnly`,
   `ListRoutingRulesForOrgs`, and the `Plan` branch. Depends on: nothing.
   Verifiable by: eventrouter tests showing a member of two orgs only fires the
   scoped org's rules, and a non-member only fires `Public` rules.
6. **Events webhook + schema** — Delivers: `slackserver.EventsHandler`,
   `RegisterSchemas`, and the `/webhook/slack` mount. Senders are unlinked at
   this stage, so only `Public` rules fire. Depends on: (1), (3), (5).
   Verifiable by: webhook tests for `url_verification`, bad or stale
   signatures, a squatted `app_id`, duplicate `event_id`, release on route
   error, bot self-mention ignored, and create-then-wake in one thread; then
   end to end with a real Slack app.
7. **Account linking backend** — Delivers: the `slack_accounts` and
   `slack_link_codes` migration, store methods, `CommandHandler` at
   `/webhook/slack/command`, `GetSlackLinkCode`, `LinkSlackAccount`,
   `UnlinkSlackAccount`, `GetProfile.slack_accounts`, and the events handler
   resolving `UserID`. Depends on: (6). Verifiable by: handler tests for the
   code lifecycle (single use, expiry) and member-only rules firing for a
   linked sender.
8. **Settings UI: Slack Apps** — Delivers: the org card, the add-app dialog
   with manifest and create link, and delete. Depends on: (4). Verifiable by:
   registering an app through the UI and seeing `url_verification` and an
   event succeed.
9. **Settings UI: Slack Accounts + link page** — Delivers: the account card,
   `/ui/slack/link`, and the `slack` case in `external-source.tsx`. Depends on:
   (7). Verifiable by: running `/gritz link` in Slack, confirming in the UI,
   and seeing the account listed.
10. **Docs** — Delivers: the README "Slack" section and a `gritzapp/workspaces`
    example with the bot token under `secrets:` and the recommended MCP
    server. Depends on: (6). Verifiable by: following the docs from a fresh
    org to an agent replying in thread.

## Trade-offs

**Org-owned app vs one gritz app.** A single gritz app would make setup
one-click. But replying as that bot would mean gritz storing bot tokens and
running an outbound posting service (or handing its token to every sandbox),
which #1951 rules out. Org-owned apps cost a few minutes of setup, which the
manifest reduces to *create from manifest → paste two values → install*.

**`app_id` lookup with multi-candidate verification vs a unique `app_id`.**
A unique constraint is simpler, but anyone who knows an app ID (it appears in
Slack URLs) could register it first and block the real owner. Verifying
against every registration of the ID costs one HMAC per extra row and removes
that problem.

**Synchronous routing with a releasable claim vs ack-then-route.** Acking
first guarantees the 3-second budget. But without a durable server-side queue
(the `outbox` package is filesystem-backed and runner-side), a crash after the
ack loses the event. Routing synchronously keeps Slack's retries as the
recovery path, and the claim absorbs the retries that a slow success causes.

**Slash-command linking vs "Sign in with Slack" (OIDC).** OIDC through the
org's own app would let users link from the settings page, but gritz would
then have to store each app's client ID and client secret and register a
redirect URL in the manifest. That's more credential surface for the same
result. The slash command reuses the signing secret that's already stored,
and its ephemeral reply needs no token. The downside is that linking starts
in Slack rather than in gritz.

**`OrgsOnly` vs reusing the fan-out to member orgs.** Jira and GitHub route a
linked actor's event to all of their orgs. For Slack that would let org B's
rules create tasks for mentions of org A's bot, tasks that can't reply as that
bot. A flag on `InputEvent` keeps the change local, and it leaves GitHub and
Atlassian behaviour alone.

**Thread URL vs permalink as the key.** Permalinks are what users copy, but
the payload has no workspace domain, so gritz can't build one without a token
(`chat.getPermalink`) or a stored domain per app. The `app.slack.com` client
URL is built from payload fields alone, opens the thread in a browser, and
contains the agreed `team/channel/thread_ts`.

**Mentions only.** Waking on every reply in a subscribed thread would feel
more conversational, but it needs `message.*` subscriptions and history
scopes for triggering, and it wakes agents on chatter not meant for them.
#1951 settled this.

## Open Questions

1. **Thread URL shape.** Layer 1 should confirm that
   `app.slack.com/client/{team}/{channel}/thread/{channel}-{ts}` still opens
   the thread in the current web client. If it doesn't, the key keeps the same
   fields under another `app.slack.com` path. Only `ThreadURL` and the
   `RoutingKey` regex change.
2. **Slash command name collisions.** Two gritz apps in one Slack workspace
   would both declare `/gritz`. Should the manifest derive the command from
   the bot name (`/acme-bot`)? The handler doesn't depend on the name.
3. **Enterprise Grid / Slack Connect.** In shared channels, `event.user` may
   belong to another team (`user_team`). This design keys accounts on the
   installing team's `team_id` for both linking and lookup, which is
   consistent but means a user links once per installing workspace. Is that
   acceptable?
4. **DMs.** `app_mention` doesn't fire in DMs with the bot. Supporting
   DM-to-task would need `message.im`. It's out of scope here, but is it
   wanted later?
