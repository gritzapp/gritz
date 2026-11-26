# Linear Event Support

Issue: https://github.com/icholy/xagent/issues/1502

## Problem

xagent routes external events from two sources today: GitHub
(`internal/server/githubserver/`) and Atlassian/Jira
(`internal/server/atlassianserver/`). Both are concrete implementations of the
same pipeline — a signed webhook is parsed into an `eventrouter.InputEvent`,
`Router.Route` matches it against per-org routing rules and subscribed links,
and matching tasks are woken (or a new task is created). Events surface in the
task timeline as `ExternalPayload`s carrying a `source` string.

Teams that manage their work in [Linear](https://linear.app) have no equivalent
integration. A task cannot subscribe to a Linear issue and be woken on a new
comment, nor can a routing rule create a task when a Linear comment mentions the
agent. Because the event system is already source-agnostic — events, rules, and
links key off arbitrary `source`/`type`/attribute strings — adding Linear is a
matter of implementing the same webhook → `InputEvent` → `Router.Route` path
that GitHub and Atlassian already implement.

The Atlassian integration is the closest analog and the model this proposal
follows: OAuth-based account linking, a **per-org** signed webhook endpoint
(`/webhook/atlassian?org=N`), an event extractor (`toInputEvent`), and schema
registration.

## Design

Add an `internal/server/linearserver/` package that mirrors
`atlassianserver`, plus supporting changes to routing-key normalization, schema
registration, the org webhook-secret storage, CLI wiring, HTTP routes, and the
Web UI. The `source` string for all Linear events is `"linear"`.

### Linear webhook & auth model

Linear supports both OAuth2 (for account linking) and signed webhooks:

- **OAuth2** — authorize at `https://linear.app/oauth/authorize`, exchange at
  `https://api.linear.app/oauth/token`. The authenticated actor is fetched via
  the GraphQL `viewer` query (`{ viewer { id name email } }`), returning a
  stable Linear user id. This mirrors `atlassian.FetchMe`.
- **Webhooks** — Linear signs each delivery with HMAC-SHA256 over the raw body
  using a per-webhook signing secret, sent in the `Linear-Signature` header (hex
  digest). Payloads also include a `webhookTimestamp` that should be checked
  against the current time to reject replays. This mirrors Jira's
  `X-Hub-Signature` HMAC verification (`atlassian.VerifyWebhook`).

Like Atlassian, the webhook secret is **per-org** and the webhook URL embeds the
org id (`/webhook/linear?org=N`), so a single deployment serves many
Linear-connected orgs.

### New `internal/x/linear` package

A thin Linear client + payload package, analogous to `internal/x/atlassian`:

```go
package linear

// VerifyWebhook checks the Linear-Signature HMAC-SHA256 header against secret.
func VerifyWebhook(body []byte, signatureHeader, secret string) error

// ParseWebhook decodes a Linear webhook delivery.
func ParseWebhook(body []byte) (*Webhook, error)

// FetchViewer returns the authenticated Linear user (GraphQL `viewer`).
func FetchViewer(ctx context.Context, accessToken string) (*User, error)

// Mentions extracts @-mentions / agent handles from comment/description text,
// mirroring atlassian.Mentions and githubx.Mentions.
func Mentions(text string) []string

type Webhook struct {
	Action    string          // "create", "update", "remove"
	Type      string          // "Comment", "Issue", ...
	CreatedAt time.Time
	Data      json.RawMessage // shape depends on Type
	// ... URL helpers below
}

type User struct {
	ID    string
	Name  string
	Email string
}
```

Linear delivers a webhook per entity change with a top-level `type` (the entity,
e.g. `"Comment"`, `"Issue"`) and `action` (`"create"`, `"update"`, `"remove"`).
The extractor keys off `(type, action)` the way `atlassianserver.toInputEvent`
keys off `payload.WebhookEvent`.

### Event types

Register two Linear event types initially, matching the Jira feature set
(comment + label), with room to grow (state changes, assignment):

```go
// internal/server/linearserver/webhook.go
const (
	EventTypeCommentCreated = "comment_created"
	EventTypeLabelAdded     = "label_added"
)
```

`toInputEvent` maps Linear webhook deliveries onto `eventrouter.InputEvent`:

```go
func toInputEvent(body []byte) (*eventrouter.InputEvent, error) {
	wh, err := linear.ParseWebhook(body)
	if err != nil {
		return nil, err
	}
	switch {
	case wh.Type == "Comment" && wh.Action == "create":
		// data.body, data.user.{id,name}, data.issue.{identifier,url}
		return &eventrouter.InputEvent{
			Source:      "linear",
			Type:        EventTypeCommentCreated,
			Description: fmt.Sprintf("%s commented on %s", authorName, issueIdentifier),
			Data:        commentBody,
			URL:         commentURL, // issue web URL, focused on the comment
			Attrs: eventrouter.Attrs{
				"mention": linear.Mentions(commentBody),
				"user":    {authorID},
			},
			Meta: LinearMeta{AuthorID: authorID, AuthorName: authorName},
		}, nil
	case wh.Type == "Issue" && wh.Action == "update":
		// emit label_added when labels were added on this update
		return &eventrouter.InputEvent{
			Source: "linear",
			Type:   EventTypeLabelAdded,
			Attrs:  eventrouter.Attrs{"label": added, "user": {actorID}},
			URL:    issueURL,
			Meta:   LinearMeta{AuthorID: actorID, AuthorName: actorName},
		}, nil
	}
	return nil, nil // ignored
}

// LinearMeta carries Linear-native identity the router does not interpret.
type LinearMeta struct {
	AuthorID   string
	AuthorName string
}
```

The extractor returns `nil` for actions/types xagent ignores, exactly like the
Atlassian and GitHub extractors.

### Webhook handler

`WebhookHandler.ServeHTTP` mirrors `atlassianserver.WebhookHandler` step for
step:

1. Read `?org=N`, parse the org id.
2. `Store.GetOrgLinearWebhookSecret(ctx, nil, orgID)`; reject if empty.
3. Read the body, `linear.VerifyWebhook(body, r.Header.Get("Linear-Signature"), secret)`.
4. `toInputEvent(body)`; `nil` → `"ignored"`.
5. Set `input.Orgs = []int64{orgID}` (gates non-member `Public` routing).
6. `Store.GetUserByLinearUserID(ctx, nil, meta.AuthorID)` to set `input.UserID`
   for linked actors; unlinked actors keep an empty `UserID` and route only via
   `Public` rules on `input.Orgs`.
7. `Router.Route(ctx, *input)`.

### Routing-key normalization

Linear issue URLs look like:

```
https://linear.app/{workspace}/issue/{TEAM-123}/{slug}
```

and comment permalinks append `#comment-{uuid}`. To make a `create_link`
subscription on an issue match a webhook fired for a comment on that issue, add
a Linear arm to `model.RoutingKey` (`internal/model/url.go`) that reduces any
Linear issue/comment URL to the canonical issue key:

```go
case u.Host == "linear.app":
	// /{workspace}/issue/{IDENTIFIER}[/...] — trailing slug and #comment-… drop.
	if m := linearWebRe.FindStringSubmatch(u.Path); m != nil {
		return fmt.Sprintf("https://linear.app/%s/issue/%s", m[1], m[2])
	}

var linearWebRe = regexp.MustCompile(`^/([^/]+)/issue/([^/]+)`)
```

Fragments and queries are already stripped by `url.Parse` before matching, as
with the existing GitHub/Jira arms. Table-driven cases go into the existing
`TestRoutingKey` in `internal/model/url_test.go`.

### Schema registration

Add `internal/server/linearserver/schema.go` following
`atlassianserver/schema.go`, registered process-wide via `init()`:

```go
func init() { RegisterSchemas(eventrouter.DefaultSchemaRegistry) }

func RegisterSchemas(reg *eventrouter.SchemaRegistry) {
	reg.MustRegister(eventrouter.EventTypeDef{
		Source: "linear",
		Type:   EventTypeCommentCreated,
		Label:  "Linear: Issue Comment",
		Attrs: []eventrouter.AttrDef{
			{Key: "body", Label: "Comment Body", Placeholder: "xagent:"},
			{Key: "url", Label: "Issue URL", Placeholder: "https://linear.app/acme/issue/ENG-"},
			{Key: "mention", Label: "Mention", Placeholder: "@xagent"},
			{Key: "user", Label: "User", Placeholder: "Linear user id"},
		},
	})
	reg.MustRegister(eventrouter.EventTypeDef{
		Source: "linear",
		Type:   EventTypeLabelAdded,
		Label:  "Linear: Label Added",
		Attrs:  []eventrouter.AttrDef{ /* body, url, label, user */ },
	})
}
```

This makes Linear event types selectable in the routing-rule editor without any
further wiring — the editor reads `DefaultSchemaRegistry`.

### Storage

Add a per-org Linear webhook secret and per-user Linear identity, mirroring the
existing Atlassian columns.

Migration (new file under the store's migrations, following the existing
Atlassian columns at `internal/store/sql/schema.sql:92` and `:258`):

```sql
ALTER TABLE public.orgs
	ADD COLUMN linear_webhook_secret text DEFAULT ''::text NOT NULL;

ALTER TABLE public.users
	ADD COLUMN linear_user_id text,
	ADD COLUMN linear_username text;

CREATE UNIQUE INDEX idx_users_linear_user_id
	ON public.users USING btree (linear_user_id);
```

New sqlc queries alongside the Atlassian ones
(`internal/store/sql/queries/org.sql`, `user.sql`):

```sql
-- name: GetOrgLinearWebhookSecret :one
SELECT linear_webhook_secret FROM orgs WHERE id = $1;

-- name: SetOrgLinearWebhookSecret :exec
UPDATE orgs SET linear_webhook_secret = $2 WHERE id = $1;

-- name: GetUserByLinearUserID :one
SELECT ... FROM users WHERE linear_user_id = $1;

-- name: LinkLinearAccount :one
UPDATE users SET linear_user_id = $2, linear_username = $3 WHERE id = $1 RETURNING ...;

-- name: UnlinkLinearAccount :one
UPDATE users SET linear_user_id = NULL, linear_username = NULL WHERE id = $1 RETURNING ...;
```

Regenerate with sqlc; add the corresponding `Store` methods
(`GetOrgLinearWebhookSecret`, `SetOrgLinearWebhookSecret`,
`GetUserByLinearUserID`, `LinkLinearAccount`, `UnlinkLinearAccount`).

### Server type & OAuth

`linearserver.Server` mirrors `atlassianserver.Server` field for field
(`clientID`, `clientSecret`, `baseURL`, `store`, `publisher`, `log`). It
provides:

- `OAuthLink()` → `oauthlink.Handler` with `Provider: "linear"`, the Linear
  authorize/token endpoints, `Scopes: []string{"read"}`, and an `OnSuccess`
  that calls `linear.FetchViewer` then `store.LinkLinearAccount`.
- `WebhookHandler()` → the handler above.
- `WebhookURL(orgID)` → `{baseURL}/webhook/linear?org=N`.
- `GenerateWebhookSecret(ctx, orgID)` / `GetWebhookSecret(ctx, orgID)` — same
  32-byte hex secret generation as `atlassianserver`.
- `UnlinkAccount(ctx, userID)`.

### Proto & RPC

Extend `proto/xagent/v1/xagent.proto` mirroring the Atlassian messages
(`xagent.proto:40`, `:48`, `:542`, `:612`):

```protobuf
rpc UnlinkLinearAccount(UnlinkLinearAccountRequest) returns (UnlinkLinearAccountResponse);
rpc GenerateLinearWebhookSecret(GenerateLinearWebhookSecretRequest) returns (GenerateLinearWebhookSecretResponse);

message LinearAccount {
  string linear_user_id = 1;
  string linear_username = 2;
}

message GenerateLinearWebhookSecretResponse {
  string webhook_secret = 1;
  string webhook_url = 2;
}
```

Add `LinearAccount linear_account` to the settings/user-info response next to
`AtlassianAccount atlassian_account` (`xagent.proto:152`). Regenerate with
`mise run generate`. Implement the handlers next to the Atlassian handlers in
the server, guarding on `s.linear != nil`.

### CLI wiring

Add flags in `internal/command/server.go` next to the Atlassian block
(`server.go:118`):

```
--linear-client-id      Linear OAuth client ID       XAGENT_LINEAR_CLIENT_ID
--linear-client-secret  Linear OAuth client secret   XAGENT_LINEAR_CLIENT_SECRET
```

Conditionally construct the server (`server.go:285`):

```go
if cmd.IsSet("linear-client-id") {
	opts.Linear = linearserver.New(linearserver.Options{
		Store:        st,
		BaseURL:      baseURL,
		Publisher:    ps,
		ClientID:     cmd.String("linear-client-id"),
		ClientSecret: cmd.String("linear-client-secret"),
	})
}
```

### HTTP routes

Register routes in `internal/server/server.go` next to the Atlassian block
(`server.go:146`):

```go
if s.linear != nil {
	link := s.linear.OAuthLink()
	mux.Handle("/linear/login", alice.New(s.auth.RequireAuth()).ThenFunc(link.HandleLogin))
	mux.Handle("/linear/callback", alice.New(s.auth.RequireAuth()).ThenFunc(link.HandleCallback))
	mux.Handle("/webhook/linear", s.linear.WebhookHandler())
}
```

### Web UI

- Add a `linear` case to `externalSourceStyle`
  (`webui/src/components/external-source.tsx`) with a Linear icon and label, so
  Linear events render correctly in the timeline and links list.
- Add a "Linear" section to `webui/src/routes/settings.tsx` mirroring the
  Atlassian one: connect/disconnect account (OAuth), and generate/copy the org
  webhook secret + webhook URL via `GenerateLinearWebhookSecret`.
- Run `pnpm lint` in `webui/` before finishing.

## Implementation Plan

1. **Storage migration + queries** — Delivers: `orgs.linear_webhook_secret`,
   `users.linear_user_id`/`linear_username`, the unique index, and sqlc queries
   + generated `Store` methods. Depends on: nothing. Verifiable by: migration
   runs up/down cleanly; store unit tests for the new getters/setters.

2. **`internal/x/linear` client** — Delivers: `VerifyWebhook`, `ParseWebhook`,
   `FetchViewer`, `Mentions`, and URL/type helpers. Depends on: nothing.
   Verifiable by: table-driven unit tests for signature verification, payload
   parsing, and mention extraction against captured Linear fixtures.

3. **RoutingKey normalization** — Delivers: the `linear.app` arm in
   `model.RoutingKey`. Depends on: nothing. Verifiable by: new cases in
   `TestRoutingKey` (issue URL, comment permalink, and trailing-slug forms all
   normalize to the same issue key).

4. **linearserver: webhook + extractor + schema** — Delivers:
   `linearserver.Server`, `WebhookHandler`, `toInputEvent`, `LinearMeta`, and
   `schema.go` registration. Depends on: (1), (2), (3). Verifiable by: handler
   unit tests posting signed fixtures and asserting the resulting
   `InputEvent`/routing; schema registered in `DefaultSchemaRegistry`.

5. **OAuth account linking** — Delivers: `OAuthLink()`, `LinkLinearAccount`
   wiring, `UnlinkAccount`. Depends on: (1), (4). Verifiable by: OAuth callback
   links a Linear user id to the caller (handler test with a stubbed
   `FetchViewer`).

6. **Proto + RPC handlers** — Delivers: `GenerateLinearWebhookSecret`,
   `UnlinkLinearAccount`, `LinearAccount` in user info. Depends on: (1), (5).
   Verifiable by: RPC handler tests; `mise run generate` produces clean output.

7. **CLI + HTTP wiring** — Delivers: `--linear-client-id/-secret` flags and the
   `/linear/*` + `/webhook/linear` routes. Depends on: (4), (5), (6). Verifiable
   by: server boots with the flags set and serves the routes; end-to-end webhook
   POST wakes a subscribed task.

8. **Web UI** — Delivers: Linear timeline/link styling and the settings section
   (connect account + webhook secret). Depends on: (6). Verifiable by: rendering
   a task with a Linear event; connecting an account and generating a secret in
   the settings page; `pnpm lint` passes.

## Trade-offs

- **Follow the Atlassian pattern vs. a generic "custom webhook" source.** A
  generic inbound-webhook source would avoid per-provider code, but it can't do
  provider-specific signature verification, identity linking, or URL
  normalization — the three things that make routing and subscriptions actually
  work. Mirroring `atlassianserver` keeps Linear a first-class source with the
  same guarantees as GitHub/Jira, at the cost of one more parallel package.
  Extracting a shared "OAuth + per-org webhook secret" skeleton is possible
  later but is deliberately out of scope here to keep the diff reviewable.

- **Per-org webhook secret (Atlassian model) vs. a single global secret (GitHub
  App model).** Linear webhooks are configured per workspace, not via a
  centrally-installed app, so the per-org secret model fits Linear's setup flow
  and matches Jira exactly. This reuses the existing `orgs.*_webhook_secret`
  storage pattern and settings-page UX.

- **Two event types to start (comment, label).** Matches the current Jira
  surface and covers the primary use case (subscribe-and-reply on issues).
  Issue state changes and assignment are natural follow-ups and slot into the
  same `toInputEvent` switch and schema registry without structural change.

## Open Questions

- **Comment permalink shape.** Linear comment URLs vary; confirm the exact
  `#comment-…` fragment form emitted by webhooks so the trigger URL focuses the
  right comment while still normalizing to the issue key. Fixtures captured in
  slice (2) settle this.
- **Mention semantics.** How should the agent be addressed in a Linear comment
  — a Linear user @-mention of a bot account, or a text prefix like `xagent:`?
  This determines what `linear.Mentions` extracts and the default `mention`
  attr placeholder. Jira uses account-id mentions; Linear may prefer a handle.
- **Replay window.** What `webhookTimestamp` skew should `VerifyWebhook` accept
  before rejecting a delivery as a replay?
- **OAuth scopes.** `read` is sufficient for identity + receiving events; a
  future "agent writes back a Linear comment" feature would need a write scope
  and is out of scope here.
