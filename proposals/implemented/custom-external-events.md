# Custom External Events

Issue: https://github.com/gritzapp/gritz/issues/1531

## Problem

The event router only routes events that come from integrations gritz supports natively. `githubserver` and `atlassianserver` each parse their own webhook, build an `eventrouter.InputEvent`, and register the `(Source, Type)` pairs they emit on `eventrouter.DefaultSchemaRegistry` from `init`. `SchemaRegistry.Validate` rejects routing rules for unregistered types, so an org can't route events from any other system: Dynatrace problems, Grafana alerts, an internal deploy pipeline, and so on.

This proposal lets an org define its own event types (schemas) and submit events of those types to a generic HTTP endpoint. Those events then go through the same routing rules, wake/create logic and subscribed links as GitHub and Jira events.

## Design

### Overview

- An org defines **custom event types** on a new "Custom Schemas" tab of the events page. Each one has a type name, a label and a list of attrs. Its source is always `custom`.
- Schemas are stored in a new `orgs.custom_event_types` JSONB column and read and written through two new RPCs, `GetCustomEventTypes` and `SetCustomEventTypes`.
- `SetRoutingRules` and `GetEventTypes` use the built-in schemas plus the caller's org's custom schemas, so the routing rule editor and the test-event form pick up custom types with no frontend change.
- External systems `POST` a fixed JSON envelope to `/webhook/custom` with a gritz API key. The handler validates the envelope against the org's schemas, builds an `InputEvent` with `Source: "custom"` and `Orgs: [orgID]`, and calls `Router.Route`. `Route` itself doesn't change.
- You can't remove a schema, or one of its attrs, while a routing rule still uses it. Schema saves and rule saves both lock the org row, so they can't race.

### Schema model

`EventTypeDef` and `AttrDef` move from `internal/server/eventrouter` to `internal/model` and gain json tags. They have to move: the store has to decode stored schemas, and `eventrouter` already imports `store` (`Router.Store *store.Store`), so the store can't import `eventrouter`. `model.RoutingRule` already lives in `model` for the same reason. `eventrouter` uses the model types from then on. `githubserver` and `atlassianserver` registrations change only their type names.

```go
// internal/model/event_type.go
type AttrDef struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Help        string `json:"help,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
}

type EventTypeDef struct {
	Source string    `json:"source"`
	Type   string    `json:"type"`
	Label  string    `json:"label"`
	Attrs  []AttrDef `json:"attrs"`
}
```

A stored custom schema is a `model.EventTypeDef` whose `Source` is always `eventrouter.SourceCustom` (`"custom"`). Built-in types use `github` and `atlassian`, so a custom `(source, type)` pair can never collide with a built-in one. Combining the registries therefore never hits `MustRegister`'s duplicate panic, as long as custom type names are unique within the org. Save-time validation checks that.

**Schema validation on save** (`eventrouter.ValidateCustomEventTypes`). The whole list is rejected with `InvalidArgument` if:

- any `Source` is not `custom`
- a `Type` is empty, doesn't match `^[a-z][a-z0-9_]{0,63}$` (the style of the built-in names, e.g. `comment_created`), or appears twice in the list
- a `Label` is empty
- an attr `Key` is empty, doesn't match the same pattern, or appears twice within its type
- the type has no `body` attr or no `url` attr

**Derived `body`/`url` attrs: reject rather than add.** Every schema has to declare `body` and `url`, because `InputEvent.Attr` always derives them and rules can condition on them. The server rejects a schema that's missing either one instead of adding them for you. That keeps the stored schema identical to what `GetEventTypes` returns, so the server never rewrites a client's data. It also lets each type give `body` and `url` their own label, help and placeholder, the way the built-in types do (e.g. "Comment Body" vs "Issue Description"). The UI pre-fills both rows and doesn't allow removing them or changing their keys, so users never see this error.

### Storage

A JSONB column next to `routing_rules`, not a separate table:

```sql
-- internal/store/sql/migrations/2026MMDD000001_org_custom_event_types.sql
-- migrate:up
ALTER TABLE orgs ADD COLUMN custom_event_types JSONB NOT NULL DEFAULT '[]';

-- migrate:down
ALTER TABLE orgs DROP COLUMN IF EXISTS custom_event_types;
```

Why a column:

- Schemas are small, per-org, and always read and written as a whole list, the same way routing rules are. The RPCs are get/set of the full list, like `GetRoutingRules`/`SetRoutingRules`.
- The integrity checks lock the org row anyway (see below), so they don't need row-level locking in a separate table.
- They are still stored separately from `routing_rules`, so `ListRoutingRulesForEvent` and `Plan`/`Match` are untouched. Routing never reads schemas.

New queries in `internal/store/sql/queries/org.sql`:

```sql
-- name: GetOrgCustomEventTypes :one
SELECT custom_event_types FROM orgs WHERE id = $1;

-- name: SetOrgCustomEventTypes :exec
UPDATE orgs SET custom_event_types = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $1;

-- name: LockOrg :exec
-- Serializes writes that check routing rules against custom event types.
SELECT id FROM orgs WHERE id = $1 FOR UPDATE;
```

### Integrity: rules vs schemas

Two writes have to stay consistent:

1. **`SetRoutingRules`** validates every rule against `DefaultSchemaRegistry.With(orgCustomTypes...)`, a per-request registry with the built-in types plus the org's custom ones.
2. **`SetCustomEventTypes`** validates every stored rule of the org against `DefaultSchemaRegistry.With(newCustomTypes...)`. If any rule fails, the save is rejected with `FailedPrecondition`, e.g. `routing rule 3: attr "severity" not valid for event type "custom"/"problem"`.

The second check doesn't need special cases for "delete", "rename" or "remove attr". Re-validating the existing rules against the proposed schema set covers all three:

- deleting a schema that a rule selects fails with `unknown event type`
- removing an attr that a condition uses fails with `attr ... not valid`
- renaming a type is a delete plus an add, so it's rejected while a rule uses the old name

**Concurrency.** Both handlers run in one transaction that starts with `LockOrg`, then reads the other side (`SetRoutingRules` reads `custom_event_types`, `SetCustomEventTypes` reads `routing_rules`), validates, and writes:

```go
err := s.store.WithTx(ctx, nil, func(tx *sql.Tx) error {
	if err := s.store.LockOrg(ctx, tx, caller.OrgID); err != nil {
		return err
	}
	custom, err := s.store.GetOrgCustomEventTypes(ctx, tx, caller.OrgID)
	if err != nil {
		return err
	}
	reg := eventrouter.DefaultSchemaRegistry.With(custom...)
	for i, rule := range rules {
		if err := reg.Validate(rule); err != nil {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("routing rule %d: %w", i+1, err))
		}
	}
	if err := s.store.SetOrgRoutingRules(ctx, tx, caller.OrgID, rules); err != nil {
		return err
	}
	return tx.Commit()
})
```

Under READ COMMITTED, each statement after the lock sees everything committed before the lock was acquired. So whichever transaction goes second sees what the first one committed, and a rule can't be saved against a type that's being deleted at the same moment.

### Custom rules must be `public`

The ingestion endpoint has no actor: the event is authenticated as coming from the org, not from a user. It routes with an empty `UserID`, so `ListRoutingRulesForEvent` returns the org only through its non-member branch (`is_member = FALSE`). `Plan` then keeps only rules with `Public: true`.

Setting `UserID` to the API key's owner would be wrong. The member branch returns every org the owner belongs to, so a custom event would also be evaluated against the owner's other orgs, and any of them with a same-named custom type would match.

So `SchemaRegistry.Validate` rejects a rule with `Source == "custom"` and `Public == false`:

```
custom event rules must be public: custom events have no actor, so a member-only rule would never fire
```

This is an invariant check, not new routing behaviour. `Route` stays unchanged. The rule form already has a "Public" switch (`routing-rule-form.tsx`), so the only UI cost is that the server returns this error until the switch is on. The `TestEvent` dry run routes as the caller (a member), so it would otherwise match non-public rules that real ingestion never fires. This check also closes that gap: every custom rule is public, so it's eligible on both paths.

### RPCs

```protobuf
service GritzService {
  // ...
  rpc GetCustomEventTypes(GetCustomEventTypesRequest) returns (GetCustomEventTypesResponse);
  rpc SetCustomEventTypes(SetCustomEventTypesRequest) returns (SetCustomEventTypesResponse);
}

message GetCustomEventTypesRequest {}

message GetCustomEventTypesResponse {
  repeated EventTypeDef event_types = 1; // every source is "custom"
  string webhook_url = 2;                // where to POST custom events, e.g. https://gritz.dev/webhook/custom
}

// SetCustomEventTypes replaces the org's custom event types. Rejected with
// InvalidArgument if a schema is malformed, and FailedPrecondition if an
// existing routing rule would no longer validate against the new set.
message SetCustomEventTypesRequest {
  repeated EventTypeDef event_types = 1;
}

message SetCustomEventTypesResponse {
  repeated EventTypeDef event_types = 1;
}
```

`GetCustomEventTypes` needs `OpOrgRead`, and `SetCustomEventTypes` needs `OpOrgWrite`, the same scopes as the routing rule RPCs. The existing `EventTypeDef` message is reused rather than adding a source-less copy. The server requires `source == "custom"`, and the UI sets it as a constant.

**`GetEventTypes`** returns `DefaultSchemaRegistry.With(orgCustom...).EventTypes()` for the caller's org. Its doc comment ("the response is not org-scoped") is updated. The proto ↔ model conversion moves out of the handler into `model.EventTypeDef.Proto` / `model.EventTypeDefFromProto`, which `GetEventTypes` and both new RPCs share.

### Ingestion endpoint

`POST /webhook/custom`, handled by a new package `internal/server/customserver`. Its structure mirrors `atlassianserver`: a `WebhookHandler` with narrow `Router` and `Store` interfaces, so tests can use moq.

**Authentication: a gritz API key.** The endpoint is mounted with `CheckAuth()`:

```go
mux.Handle("POST /webhook/custom", alice.New(s.auth.CheckAuth()).Then(s.custom.WebhookHandler()))
```

The handler:

- returns 401 if `apiauth.Caller` is nil
- returns 401 if `caller.Type == apiauth.AuthTypeCookie`, so a browser session can't be ridden cross-site
- returns 403 unless `caller.Scopes.Allow(authscope.OpEventCreate)`

The org is `caller.OrgID`. `OpEventCreate` is already defined in `authscope` but nothing uses it yet. Every key is `*.*` today, so any key works; once narrower keys exist, an integration key only needs `event.create`.

Why an API key rather than an Atlassian-style per-org secret:

- Keys already exist with hashing, naming, expiry, revocation and org scoping, and they're rejected once their org is archived. A team can create one key per integration ("dynatrace") and revoke it on its own.
- Atlassian's scheme only works because Jira signs its payloads with HMAC. Generic senders can't compute a gritz-specific signature, so a per-org secret would end up as a static bearer token anyway, which is a worse API key.
- A secret in the URL path or query works with senders that can't set headers. The cost is that the credential lands in access logs. Senders like that are an open question (see below).

**Envelope.** The envelope is decoded strictly (`DisallowUnknownFields`, body capped with `http.MaxBytesReader` at 1 MiB):

```json
{
  "type": "problem",
  "url": "https://abc123.live.dynatrace.com/#problems/problemdetails;pid=-123_456V2",
  "body": "CPU saturation on host web-1",
  "attrs": { "severity": ["RESOURCE_CONTENTION"], "tag": ["team:payments", "env:prod"] },
  "details": { "impact": "INFRASTRUCTURE", "state": "OPEN" }
}
```

**Validation.** Any failure returns 400 with a message that names the problem:

- `type` isn't one of the org's custom types
- `url` is empty. `Route` returns 0 for an empty URL, which would silently drop the event, and the URL is the subscription key.
- `url` doesn't parse as an absolute `http`/`https` URL. It's shown as a link in the UI and handed to the agent, so a non-URL string can't be followed. This rejects only values that would be useless anyway, so it doesn't add a new constraint on senders.
- `attrs` contains `body` or `url`. `InputEvent.Attr` derives those from `Data`/`URL`, so such values would be silently ignored.
- `attrs` contains a key the type doesn't declare
- `details` isn't validated, and there is no required-attr flag

**Routing.** The handler builds:

```go
input := eventrouter.InputEvent{
	Source:      eventrouter.SourceCustom,
	Type:        env.Type,
	Description: def.Label,
	Data:        env.Body,
	URL:         env.URL,
	Orgs:        []int64{caller.OrgID},
	Attrs:       env.Attrs,
	Details:     env.Details,
}
```

It calls `Router.Route(ctx, input)` and responds `200 {"routed": n}`, where `n` is the number of tasks woken or created. `UserID` and `Meta` are left empty. `Description` is the type's label (e.g. "Dynatrace Problem"), because the envelope has no description field. The label appears in the timeline and in channel messages ("Task 12 created by routing rule for event: Dynatrace Problem (https://…)").

**`OnRouteOutcome`: none.** GitHub uses it to post a reaction on the triggering comment. A custom source has no channel back to the sender, so the router is built with `OnRouteOutcome` nil, as Atlassian's is. The sender learns the outcome from the response's `routed` count.

### URLs as subscription keys

**Recommendation: option 1.** `url` is required and used exactly as sent, through the existing `model.RoutingKey`, which leaves non-GitHub, non-Jira URLs unchanged. The docs and the UI state that every event about the same resource must send the same URL.

Why:

- **Links have no event type.** When an agent calls `create_link`, `CreateLink` computes `model.RoutingKey(url)` with no idea which custom type, or even which source, the URL belongs to. Per-schema normalization (option 3) could only apply at ingestion. A link the agent creates would get a different key from the event, which breaks the agent subscribing to the resource, one of the main reasons links exist. For option 3 to work on links too, `RoutingKey` would have to become org-aware and guess the schema from the URL. That's host-based matching, which is the hard-coded GitHub/Jira logic made configurable, with all of its ambiguity.
- **Option 2 (separate routing key)** changes `task_links`, `create_link` and the agent-facing contract: the agent would have to learn and pass an opaque key. That's a cross-cutting change for a problem the sender can usually solve.
- **The sender controls the URL.** Unlike a GitHub webhook, the user writes the template that fills the envelope, so they can pick a stable resource URL (Dynatrace's per-problem URL, a Grafana alert rule URL) rather than a per-notification one. Option 1 puts the stability requirement on the side that can meet it.
- **It's a strict subset.** If real sources turn out to need normalization, option 3 or option 2 can be added later without changing what option 1 accepts.

One side effect: a custom event whose URL is a GitHub or Jira URL is normalized like a native one. That helps, because a custom "deploy finished for PR" event can wake the task subscribed to that PR.

### Generated payload template (Dynatrace example)

In scope, client-side only. The Custom Schemas tab shows each type's webhook URL plus an example payload generated from its schema:

- `type` filled in
- `url` and `body` set to their placeholders
- one `attrs` entry per declared attr, with its placeholder as the single value
- an empty `details`

It also shows the equivalent `curl` command (with `Authorization: Bearer <api key>`).

For Dynatrace, the user pastes the example into the custom integration's payload, swaps the placeholder values for Dynatrace placeholders (`{ProblemURL}`, `{ProblemTitle}`, `{ProblemSeverity}`, …), and adds the `Authorization` header. The sender is responsible for JSON-escaping the values it substitutes. Source-specific templates (a built-in Dynatrace mapping) are out of scope. They would bring back per-integration code, which custom events exist to avoid.

### TestEvent

No server change is needed. `TestEvent` doesn't validate against the schema registry, and `test-event-form.tsx` gets its type list from `GetEventTypes`, so custom types show up in the form once `GetEventTypes` includes them. Dry run and fire then work like any other type.

The difference in actor (`TestEvent` routes as the caller, ingestion routes with no actor) doesn't matter, because every custom rule is public. `TestEvent` still takes single-valued attrs (`map<string, string>`). Multi-valued attrs can only be exercised through the real endpoint, which is acceptable for a test tool.

### Web UI

A new **Custom Schemas** tab (`tab=schemas`) on `webui/src/routes/events.index.tsx`, next to `rules` and `recent`.

- **List:** a table of the org's custom types with label, type and attr keys, loaded with `getCustomEventTypes`. It also shows the webhook URL and the example payload/curl described above.
- **Add/edit:** `custom-event-types.new.tsx` and `custom-event-types.$type.tsx` routes, following `routing.new.tsx`/`routing.$index.tsx`, sharing a `custom-event-type-form.tsx` with these fields:
  - type, read-only when editing, since a rename is a delete plus an add
  - label
  - an attr list with key, label, help and placeholder. The `body` and `url` rows are pre-filled and pinned: their keys can't be edited and the rows can't be removed.
- **Remove:** a row action. Every change sends the whole list through `setCustomEventTypes`, the same pattern as `setRoutingRules`. A `FailedPrecondition` from a schema that's still in use appears as the error message. On success, the `getEventTypes` query is invalidated so the rule editor and test form pick up the change.
- External events with source `custom` already fall through to the generic "External" style in `external-source.tsx`. No change is needed.

### Cleanup

The `DefaultSchemaRegistry` doc comment in `internal/server/eventrouter/schema.go` says the server hands it to the store as the "routing-rule translator". That's stale: `DecodeRoutingRules` (`internal/store/org.go`) is a plain unmarshal. The comment is rewritten when the schema types move (PR 1) to say that producers populate it from `init`, and that handlers combine it with an org's custom types through `With`.

### API

```go
package model // github.com/gritzapp/gritz/internal/model

// AttrDef is an attribute dimension a routing rule may condition on. // new (moved from eventrouter)
type AttrDef struct{ Key, Label, Help, Placeholder string }

// EventTypeDef declares a (Source, Type) event kind and its complete attr set. // new (moved from eventrouter)
type EventTypeDef struct {
	Source string
	Type   string
	Label  string
	Attrs  []AttrDef
}

// HasAttr reports whether def declares an attr with the given key. // new (was eventrouter's unexported hasAttr)
func (def EventTypeDef) HasAttr(key string) bool

// Attr returns def's attr with the given key, and false if it has none. // new
func (def EventTypeDef) Attr(key string) (AttrDef, bool)

// Proto converts def to its protobuf representation. // new
func (def EventTypeDef) Proto() *gritzv1.EventTypeDef

// EventTypeDefFromProto converts a protobuf EventTypeDef. // new
func EventTypeDefFromProto(pb *gritzv1.EventTypeDef) EventTypeDef
```

```go
package eventrouter // github.com/gritzapp/gritz/internal/server/eventrouter

// AttrDef, EventTypeDef // removed (now model.AttrDef, model.EventTypeDef)

// SourceCustom is the source of every org-defined event type. // new
const SourceCustom = "custom"

// MustRegister records def as the schema for its (Source, Type) event kind. // changed: takes model.EventTypeDef
func (r *SchemaRegistry) MustRegister(def model.EventTypeDef)

// EventTypeFor returns the registry entry for a (source, type) pair. // changed: returns model.EventTypeDef
func (r *SchemaRegistry) EventTypeFor(source, typ string) (model.EventTypeDef, bool)

// EventTypes returns every registered schema in registration order. // changed: returns []model.EventTypeDef
func (r *SchemaRegistry) EventTypes() []model.EventTypeDef

// With returns a new registry holding r's schemas followed by defs. // new
func (r *SchemaRegistry) With(defs ...model.EventTypeDef) *SchemaRegistry

// Validate checks the rule against the registry; custom rules must also be public. // changed
func (r *SchemaRegistry) Validate(rule model.RoutingRule) error

// ValidateCustomEventTypes checks an org's custom event types before they are saved. // new
func ValidateCustomEventTypes(defs []model.EventTypeDef) error
```

```go
package store // github.com/gritzapp/gritz/internal/store

// LockOrg locks the org row until tx ends. // new
func (s *Store) LockOrg(ctx context.Context, tx *sql.Tx, orgID int64) error

// GetOrgCustomEventTypes returns the org's custom event types. // new
func (s *Store) GetOrgCustomEventTypes(ctx context.Context, tx *sql.Tx, orgID int64) ([]model.EventTypeDef, error)

// SetOrgCustomEventTypes replaces the org's custom event types. // new
func (s *Store) SetOrgCustomEventTypes(ctx context.Context, tx *sql.Tx, orgID int64, defs []model.EventTypeDef) error
```

```go
package customserver // github.com/gritzapp/gritz/internal/server/customserver

// Router routes events to subscribed tasks.
type Router interface {
	Route(ctx context.Context, input eventrouter.InputEvent) (int, error)
}

// Store is the subset of store.Store the webhook handler uses.
type Store interface {
	GetOrgCustomEventTypes(ctx context.Context, tx *sql.Tx, orgID int64) ([]model.EventTypeDef, error)
}

// Envelope is the JSON body of a custom event submission.
type Envelope struct {
	Type    string              `json:"type"`
	Body    string              `json:"body"`
	URL     string              `json:"url"`
	Attrs   map[string][]string `json:"attrs"`
	Details map[string]string   `json:"details"`
}

// ToInputEvent validates env against the org's custom types and builds the event to route.
func ToInputEvent(env Envelope, types []model.EventTypeDef, orgID int64) (eventrouter.InputEvent, error)

// WebhookHandler accepts custom events from API-key-authenticated callers.
type WebhookHandler struct {
	Router Router
	Store  Store
}

// ServeHTTP authenticates, validates and routes one custom event.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request)
```

**How it fits:** `server.New` builds a `customserver.WebhookHandler` with an `eventrouter.Router` (no `OnRouteOutcome`) and the store, and mounts it at `POST /webhook/custom` behind `CheckAuth`. `apiserver.SetRoutingRules` and `SetCustomEventTypes` each call `LockOrg`, read the other column, and validate through `DefaultSchemaRegistry.With(...)`. `SetCustomEventTypes` calls `ValidateCustomEventTypes` first. `GetEventTypes` returns `DefaultSchemaRegistry.With(orgCustom...).EventTypes()`.

## Implementation Plan

1. **Move schema types to `model`** — Delivers: `model.EventTypeDef`/`AttrDef` with json tags, `HasAttr`/`Attr`, proto conversion (used by `GetEventTypes`), `SchemaRegistry.With`, the fixed `DefaultSchemaRegistry` doc comment, and updated `githubserver`/`atlassianserver` registrations. Depends on: nothing. Verifiable by: existing eventrouter, githubserver, atlassianserver and apiserver tests pass unchanged, plus a unit test for `With`.
2. **Custom schema validation** — Delivers: `eventrouter.SourceCustom`, `ValidateCustomEventTypes`, and `Validate` rejecting non-public custom rules. Depends on: (1). Verifiable by: table-driven unit tests over malformed schemas and over custom rules validated against a `With` registry.
3. **Migration + store** — Delivers: the `orgs.custom_event_types` column, `GetOrgCustomEventTypes`/`SetOrgCustomEventTypes`, and `LockOrg`. Depends on: (1). Verifiable by: the migration runs, plus store round-trip tests.
4. **Custom event type RPCs** — Delivers: the `GetCustomEventTypes`/`SetCustomEventTypes` proto and handlers, including the lock and the re-validation of existing rules. Depends on: (2), (3). Verifiable by: handler tests for save, delete-in-use and attr-removal-in-use rejections.
5. **Org-aware rule validation and event types** — Delivers: `SetRoutingRules` in a locked transaction validating against built-in + custom types, and `GetEventTypes` returning the org's custom types. Depends on: (3), (2). Verifiable by: handler tests showing a rule on a custom type saves once the type exists and is rejected otherwise, and that `GetEventTypes` includes it.
6. **Ingestion endpoint** — Delivers: `internal/server/customserver` and the `/webhook/custom` mount. Depends on: (2), (3). Verifiable by: handler tests with moq'd `Router`/`Store` covering each 400/401/403 case and the built `InputEvent`, plus an end-to-end test that creates a type and a public rule with a create action, posts an event with an API key, and checks that a task is created and a second post wakes it.
7. **Custom Schemas tab** — Delivers: the `schemas` tab, list and remove actions, and the new/edit routes with `custom-event-type-form.tsx`. Depends on: (4), (5). Verifiable by: adding a type in the UI, then seeing it in the routing rule editor and test-event form; `pnpm lint`.
8. **Endpoint URL and payload template** — Delivers: the webhook URL, plus the generated example payload and curl command on the tab. Depends on: (7), (6). Verifiable by: pasting the generated curl with a real key and seeing the event route.

PRs 4, 5 and 6 are independent of each other and can land in any order once 2 and 3 are in.

## Trade-offs

- **Table instead of a column.** A `custom_event_types` table with `(org_id, type)` unique would allow per-type CRUD RPCs and DB-enforced uniqueness. It was rejected because the list is small, is always edited as a whole in the UI, is already serialized by the org row lock, and the get/set-list pattern matches routing rules. Uniqueness is enforced by `ValidateCustomEventTypes`.
- **Per-type CRUD RPCs.** These would avoid a last-writer-wins race between two admins editing different schemas at once. Routing rules have the same race today and it hasn't been a problem. Keeping both the same is simpler.
- **Auto-adding `body`/`url`.** Friendlier to API callers, but the server would rewrite client data, and the stored schema could differ from what was submitted. The UI already pins those rows.
- **Routing as the key's owner instead of requiring `public`.** This would let member-only rules fire, but `ListRoutingRulesForEvent`'s member branch would evaluate the event against every org the owner belongs to. Avoiding that means changing `Route`, which the design keeps unchanged.
- **A Connect RPC instead of a plain HTTP handler.** An RPC would get auth and routing for free, but proto can't express `map<string, repeated string>` without a wrapper message. That would change the JSON shape to `{"attrs": {"k": {"values": [...]}}}` and make hand-written templates (Dynatrace) clumsier.
- **URL key options 2 and 3.** See "URLs as subscription keys".

## Open Questions

- **Senders that can't set headers.** Some webhook senders only take a URL. Should the endpoint also accept the key as a query parameter (`/webhook/custom?key=gritz_…`), accepting that it ends up in access logs? The proposal leaves this out until a concrete source needs it.
- **Description.** `InputEvent.Description` is set to the type's label, so every event of a type has the same timeline title. If that's too coarse, an optional schema-level description template (e.g. using an attr value) could come later. The envelope itself stays fixed.
- **Public hint in the rule form.** Should `routing-rule-form.tsx` turn on and lock the Public switch when a `custom` type is selected, instead of relying on the server error? It's a small change, but it breaks the "no form change" property, so it's left as an optional follow-up.
