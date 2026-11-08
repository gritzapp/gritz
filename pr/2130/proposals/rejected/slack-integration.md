# Slack Integration

Issue: https://github.com/icholy/gritz/issues/439

## Problem

gritz has GitHub and Jira integrations that let users link their accounts and receive webhook events routed to tasks. There is no equivalent Slack integration. Users who coordinate work in Slack cannot receive notifications on their tasks when someone mentions gritz in a Slack channel or thread.

## Design

This proposal adds Slack integration using the Slack Events API for inbound events, following the same pattern as the existing GitHub integration: OAuth account linking on the user, a webhook handler that verifies requests and routes events to tasks via notify links.

Outbound messaging (posting to Slack) is not included. Workspaces that want agents to respond in Slack can configure a Slack MCP server in their workspace definition.

### Slack App Model

Slack uses a single App with:

- **One signing secret** per app (global, like GitHub's webhook secret)
- **Per-workspace bot tokens** issued when the app is installed to a Slack workspace
- **Events API** delivering events to a single Request URL

The signing secret is configured as a global server flag (same model as GitHub). The bot token from OAuth is stored per-org to map Slack workspaces to gritz orgs, but is not used by gritz itself — it exists only to establish the workspace-to-org mapping.

### Database Migration

New migration `internal/store/sql/migrations/016_slack.sql`:

```sql
ALTER TABLE users ADD COLUMN slack_user_id TEXT;
CREATE UNIQUE INDEX idx_users_slack_user_id ON users(slack_user_id);

ALTER TABLE orgs ADD COLUMN slack_team_id TEXT NOT NULL DEFAULT '';
```

`slack_user_id` on the user follows the same pattern as `github_user_id` and `atlassian_account_id`. `slack_team_id` on the org maps a Slack workspace to an org for event routing.

### Store Layer

Add to `internal/store/sql/queries/user.sql`:

```sql
-- name: GetUserBySlackUserID :one
SELECT id, email, name, github_user_id, github_username, slack_user_id, default_org_id, created_at, updated_at
FROM users
WHERE slack_user_id = $1;

-- name: LinkSlackAccount :exec
UPDATE users SET
    slack_user_id = $2,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: UnlinkSlackAccount :exec
UPDATE users SET
    slack_user_id = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1;
```

Add to `internal/store/sql/queries/org.sql`:

```sql
-- name: SetOrgSlackTeamID :exec
UPDATE orgs SET
    slack_team_id = $2,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1;

-- name: ClearOrgSlackTeamID :exec
UPDATE orgs SET
    slack_team_id = '',
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1;
```

All existing user queries must add `slack_user_id` to their SELECT lists.

### Model Updates

Update `internal/model/user.go`:

```go
type User struct {
    ID               string    `json:"id"`
    Email            string    `json:"email"`
    Name             string    `json:"name"`
    GitHubUserID     int64     `json:"github_user_id"`
    GitHubUsername   string    `json:"github_username"`
    SlackUserID      string    `json:"slack_user_id"`
    DefaultOrgID     int64     `json:"default_org_id"`
    CreatedAt        time.Time `json:"created_at"`
    UpdatedAt        time.Time `json:"updated_at"`
}

func (u *User) HasSlack() bool {
    return u.SlackUserID != ""
}

func (u *User) SlackAccountProto() *gritzv1.SlackAccount {
    if !u.HasSlack() {
        return nil
    }
    return &gritzv1.SlackAccount{
        SlackUserId: u.SlackUserID,
        CreatedAt:   timestamppb.New(u.CreatedAt),
    }
}
```

### Proto Definitions

Add to `proto/gritz/v1/gritz.proto`:

```protobuf
// In service GritzService:
rpc GetSlackAccount(GetSlackAccountRequest) returns (GetSlackAccountResponse);
rpc UnlinkSlackAccount(UnlinkSlackAccountRequest) returns (UnlinkSlackAccountResponse);
rpc GetSlackInstallation(GetSlackInstallationRequest) returns (GetSlackInstallationResponse);
rpc UninstallSlack(UninstallSlackRequest) returns (UninstallSlackResponse);

// Messages:
message SlackAccount {
  string slack_user_id = 1;
  google.protobuf.Timestamp created_at = 2;
}

message GetSlackAccountRequest {}
message GetSlackAccountResponse {
  SlackAccount account = 1;
}

message UnlinkSlackAccountRequest {}
message UnlinkSlackAccountResponse {}

message SlackInstallation {
  string team_id = 1;
}

message GetSlackInstallationRequest {}
message GetSlackInstallationResponse {
  SlackInstallation installation = 1;
}

message UninstallSlackRequest {}
message UninstallSlackResponse {}
```

### Slack OAuth Flow

Create `internal/slackauth/slackauth.go` mirroring `internal/ghauth/ghauth.go`:

```go
package slackauth

type Config struct {
    ClientID     string
    ClientSecret string
    RedirectURL  string
    Log          *slog.Logger
    OnSuccess    func(w http.ResponseWriter, r *http.Request, teamID string, userID string)
}

type Handler struct {
    oauth     *oauth2.Config
    log       *slog.Logger
    onSuccess func(w http.ResponseWriter, r *http.Request, teamID string, userID string)
    mux       *http.ServeMux
}
```

Key differences from `ghauth`:

- **OAuth endpoint:** `https://slack.com/oauth/v2/authorize` / `https://slack.com/api/oauth.v2.access`
- **Scopes (bot):** `app_mentions:read`, `channels:history`
- **User scopes:** None required (user identity comes from the `authed_user` field in the OAuth response)
- **State cookie:** `gritz_slack_state` (same TTL and security flags as GitHub)
- **Routes:** `/login` and `/callback` (mounted at `/slack/`)
- **Token response:** Slack's `oauth.v2.access` returns `team.id` and `authed_user.id` in a single response — no separate user fetch needed

The `/callback` handler:
1. Exchange code for token via `https://slack.com/api/oauth.v2.access`
2. Extract `team.id` and `authed_user.id` from the response
3. Call `OnSuccess` with both values
4. The server's `OnSuccess` callback stores the team ID on the org and links the Slack user ID to the user

### Webhook Handler

Create `internal/webhook/slack.go` mirroring `internal/webhook/github.go`:

```go
type SlackHandler struct {
    Log           *slog.Logger
    Store         *store.Store
    SigningSecret string
}
```

Endpoint: `POST /webhook/slack`

**Request verification:** Slack uses a different signing scheme than GitHub:
1. Concatenate `v0:{timestamp}:{body}` where timestamp is the `X-Slack-Request-Timestamp` header
2. Compute HMAC-SHA256 using the app's signing secret
3. Compare against `X-Slack-Signature` header (prefixed with `v0=`)
4. Reject if timestamp is more than 5 minutes old (replay protection)

**URL verification challenge:** Slack sends a `url_verification` event when the Request URL is first configured. The handler must respond with the `challenge` value:

```go
if payload.Type == "url_verification" {
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{"challenge": payload.Challenge})
    return
}
```

**Processing steps for `event_callback` type:**

1. Verify request signature
2. Parse the outer envelope (`type`, `team_id`, `event`)
3. Extract inner event (see supported events below)
4. Enforce `gritz:` prefix on message text (same pattern as GitHub)
5. Look up user by `slack_user_id` using `GetUserBySlackUserID`
6. If not found, ignore (user hasn't linked their Slack account)
7. Call `findLinksByOrg()` to find matching notify links
8. Create event and route to tasks using `routeEventToLinks()`

**Supported Slack event types:**

- `app_mention` — Someone mentioned @gritz in a channel. Extract `user` (Slack user ID), `text` (message body), and `channel` (channel ID). Construct the URL as `https://slack.com/archives/{channel}/p{ts}` (Slack message permalink format).
- `message` (in threads only) — A reply in a thread where gritz was previously mentioned. Extract the same fields plus `thread_ts`.

The `extractSlackEvent` function:

```go
type slackEvent struct {
    description  string
    data         string
    url          string
    slackUserID  string
}
```

**URL matching:** The event URL is constructed from the channel ID and message timestamp. For tasks to receive Slack events, they need a notify link with a matching Slack permalink URL. This is the same provider-agnostic link matching used by the GitHub integration — `FindNotifyLinksByURLForUser` matches on exact URL regardless of the provider.

**Deduplication:** Use the Slack `event_id` field from the outer envelope. Slack may retry delivery, so the handler should check for duplicate `event_id` values.

### CLI Flags

Add to `internal/command/server.go`:

```go
&cli.StringFlag{
    Name:    "slack-client-id",
    Usage:   "Slack App OAuth client ID",
    Sources: cli.EnvVars("GRITZ_SLACK_CLIENT_ID"),
},
&cli.StringFlag{
    Name:    "slack-client-secret",
    Usage:   "Slack App OAuth client secret",
    Sources: cli.EnvVars("GRITZ_SLACK_CLIENT_SECRET"),
},
&cli.StringFlag{
    Name:    "slack-signing-secret",
    Usage:   "Slack App signing secret (for webhook verification)",
    Sources: cli.EnvVars("GRITZ_SLACK_SIGNING_SECRET"),
},
```

Configuration passed to server:

```go
type SlackConfig struct {
    ClientID      string
    ClientSecret  string
    SigningSecret string
}
```

Add `Slack *SlackConfig` field to both `Options` and `Server`.

### Server HTTP Routes

Add to `server.Handler()`:

```go
if s.slack != nil {
    sh := slackauth.New(slackauth.Config{
        ClientID:     s.slack.ClientID,
        ClientSecret: s.slack.ClientSecret,
        RedirectURL:  s.baseURL + "/slack/callback",
        Log:          s.log,
        OnSuccess: func(w http.ResponseWriter, r *http.Request, teamID, userID string) {
            caller := apiauth.Caller(r.Context())
            if caller == nil {
                http.Error(w, "not authenticated", http.StatusUnauthorized)
                return
            }
            if err := s.store.SetOrgSlackTeamID(r.Context(), nil, caller.OrgID, teamID); err != nil {
                http.Error(w, "failed to install Slack", http.StatusInternalServerError)
                return
            }
            if err := s.store.LinkSlackAccount(r.Context(), nil, caller.ID, userID); err != nil {
                http.Error(w, "failed to link Slack account", http.StatusInternalServerError)
                return
            }
            http.Redirect(w, r, "/ui/settings", http.StatusFound)
        },
    })
    mux.Handle("/slack/", alice.New(s.auth.RequireAuth(), s.auth.AttachUserInfo()).Then(http.StripPrefix("/slack", sh)))

    mux.Handle("/webhook/slack", &webhook.SlackHandler{
        Log:           s.log,
        Store:         s.store,
        SigningSecret: s.slack.SigningSecret,
    })
}
```

Both OAuth and webhook routes are conditional on Slack config because the signing secret is global (same model as GitHub).

### Settings UI

Add Slack section to `webui/src/routes/settings.tsx`:

**Slack Account Card** — mirrors the GitHub Account card:

- If linked: show Slack user ID with an "Unlink" button (calls `unlinkSlackAccount` RPC)
- If not linked: show "Link Slack Account" button (links to `/slack/login`)
- Uses `getSlackAccount` query

**Slack Workspace Card** — per-org:

- If installed: show connected workspace with an "Uninstall" button (calls `uninstallSlack` RPC)
- If not installed: show "Install to Slack" button (links to `/slack/login`)
- The OAuth flow handles both workspace installation and user linking in a single step

## Trade-offs

### Global signing secret vs per-org secret

Slack apps have a single signing secret per app, so unlike Jira (where each org has its own instance with its own webhook secret), all orgs share the same Slack app and signing secret. This is the same model as the GitHub integration.

The alternative — supporting multiple Slack apps (one per org) — adds complexity for little benefit. Most deployments will use a single Slack app installed to multiple workspaces.

### No built-in outbound messaging

This proposal only handles inbound events (Slack to gritz). Outbound messaging (gritz to Slack) is left to workspace configuration — workspaces can add a Slack MCP server to give agents the ability to post messages. This keeps the server-side integration focused on the same concern as GitHub and Jira: routing external events to tasks via the link/event system.

### Event URL construction

Slack events don't include a permalink URL — the handler constructs one from the channel ID and message timestamp (`https://slack.com/archives/{channel}/p{ts_without_dot}`). This constructed URL must exactly match notify links on tasks. This is somewhat fragile but is the standard Slack permalink format.

An alternative is to call the `chat.getPermalink` API for each event, but this adds latency, requires a bot token, and an API call per webhook.

### Bot token not stored

Since outbound messaging is handled by workspace MCP servers rather than the gritz server, there's no need to store the Slack bot token. The OAuth flow only needs the `team.id` (to map workspace to org) and `authed_user.id` (to link the user). The bot token from the OAuth response is discarded.

## Open Questions

1. **Slack user display name:** Should we cache the Slack display name on the user record (like `github_username`)? This would require parsing the OAuth response for additional user info.

2. **Channel-level links vs message-level links:** Should tasks be linkable to entire channels (receive all `gritz:` prefixed messages in a channel) or only to specific message threads? Channel-level links would be simpler for users but noisier. Message-level links require the agent to already have a Slack permalink to link to.

3. **Multiple workspace support:** Can a single org be connected to multiple Slack workspaces? The current design stores one `slack_team_id` per org. Supporting multiple workspaces would require a separate table.