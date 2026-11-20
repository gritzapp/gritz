# Experimental A2A server

Issue: https://github.com/gritzapp/gritz/issues/2259

## Problem

An [A2A](https://a2a-protocol.org) client should be able to hand work to gritz
and poll it until it's done. A2A's "task" doesn't line up with a gritz task. A
gritz task is long-lived: it takes many instructions and events, and it passes
through `COMPLETED` each time a run's sandbox exits, until an event wakes it
again. An A2A task is one unit of work. It ends once, and after that it can't
change.

Take an A2A task to fix some code. The agent opens a PR, works through review
comments over several runs, and declares the A2A task complete only when the PR
is merged. Neither the gritz task's status nor a run can tell us the A2A task's
state. It needs its own state, and the agent moves it.

This is an experiment. The A2A code stays out of the main API. A2A messages
arrive as external events, the way GitHub and Jira webhooks do. The only
additions outside the new `a2aserver` package are one RPC, one agent tool, and
the workspace capability that gates them.

## Design

### Model

| A2A | gritz |
| --- | --- |
| context (`contextId`) | a gritz task holding a subscribed link to `urn:gritz:a2a:context:{contextId}` |
| task (`id`) | an **a2atask**: a row in `a2a_tasks`, owned by `a2aserver` |
| agent | an agent name in the endpoint path, matched by routing rules |
| user message | an external event, source `a2a`, type `message` |
| `CancelTask` | an external event, source `a2a`, type `cancel` |
| `status.message` | the message the agent passed with its last state change |

- "Task" keeps its gritz meaning. The new entity is `A2ATask` / `a2a_task` in
  code, and "a2atask" in prose.
- An a2atask belongs to one context, spans any number of runs, and a context
  can have several open at once.
- The gritz task model, the main proto messages, the event payloads and
  `get_my_task` don't change. The context appears on the gritz task as an
  ordinary link, and A2A messages appear as ordinary external events.
- A gritz task created from the UI, the CLI or a schedule joins a context by
  holding a subscribed link to the context's URN.

### States

An a2atask uses A2A's `TaskState` names. gritz uses six of them:

| State | Terminal | Entered by |
| --- | --- | --- |
| `working` | no | creation; a client message to an `input-required` a2atask |
| `input-required` | no | the agent |
| `completed` | yes | the agent |
| `rejected` | yes | the agent |
| `failed` | yes | the agent; `GetTask` finding the context's gritz task failed; the message not reaching any task |
| `canceled` | yes | the client's `CancelTask` |

`canceled` only ever means the client called `CancelTask`. An a2atask is created
in `working`: sandbox starts and exits don't move it, so `submitted` would be a
state nothing ever leaves. `auth-required` is never used. On the wire the states
are A2A 1.0's `TASK_STATE_*` values.

Once an a2atask is terminal, nothing moves it again.

### Endpoint and agent card

```
GET  /a2a/{org_id}/{agent}/.well-known/agent-card.json   public
POST /a2a/{org_id}/{agent}                               JSON-RPC 2.0, Bearer
```

`{agent}` is a name the operator chooses, matching `^[a-z][a-z0-9_-]{0,63}$`.
gritz doesn't register agents: every name is served, and it reaches the routing
rules as the `agent` attribute. An operator publishes an agent by writing a rule
that matches its name, which is also how a rule picks the workspace and runner
for it. A name no rule matches still serves a card, but every new context sent
to it fails (see [Requests](#requests)). This is the default answer to the
endpoint open question in the issue; see [Open Questions](#open-questions).

The org is in the path because the card is fetched before the client has a
token. The JSON-RPC route is wrapped in `RequireResourceAuth`, like `/mcp`, so an
unauthenticated request gets the 401 challenge that starts the OAuth flow the
server already runs for MCP clients. Both an org API key and an OAuth token are
bound to one org, and a path `org_id` other than the caller's returns 404.

The card is static apart from the URLs and the name:

```json
{
  "name": "<agent>",
  "description": "A gritz agent. Each message is delivered to a coding agent running in a sandbox; poll GetTask for the outcome.",
  "version": "<gritz version>",
  "supportedInterfaces": [
    {"url": "https://gritz.dev/a2a/12/<agent>", "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}
  ],
  "capabilities": {"streaming": false, "pushNotifications": false},
  "defaultInputModes": ["text/plain"],
  "defaultOutputModes": ["text/plain"],
  "skills": [{"id": "task", "name": "<agent>", "description": "Hand a unit of work to a gritz agent.", "tags": ["gritz"]}],
  "securitySchemes": {
    "apiKey": {"httpAuthSecurityScheme": {"scheme": "Bearer", "description": "A gritz org API key."}},
    "oauth": {"oauth2SecurityScheme": {"oauth2MetadataUrl": "https://gritz.dev/.well-known/oauth-authorization-server", "flows": {"authorizationCode": {
      "authorizationUrl": "https://gritz.dev/oauth/authorize",
      "tokenUrl": "https://gritz.dev/oauth/token",
      "scopes": {},
      "pkceRequired": true
    }}}}
  },
  "securityRequirements": [{"schemes": {"apiKey": {}}}, {"schemes": {"oauth": {}}}]
}
```

### Requests

The endpoint handles `SendMessage`, `GetTask` and `CancelTask`. Every other
method returns `-32601` (method not found). A request whose `A2A-Version` header
(or parameter) isn't `1.0` returns `-32009` (VersionNotSupportedError).
`SendMessage` and `CancelTask` need `event.create`, as `/webhook/custom` does;
`GetTask` needs `event.read`.

The message's `text` parts are joined with blank lines. A message with a `raw`,
`url` or `data` part returns `-32005` (ContentTypeNotSupportedError).
`message.metadata`, `referenceTaskIds` and `configuration` are ignored:
`SendMessage` always returns at once, and `history` is always empty (see
[Trade-offs](#trade-offs)).

Every event `a2aserver` routes has:

| Field | Value |
| --- | --- |
| `Source` | `a2a` |
| `Type` | `message` or `cancel` |
| `URL` | `urn:gritz:a2a:context:{contextId}` |
| `Description` | `A2A message` or `A2A cancel` |
| `Data` | the message text; for a cancel, a fixed sentence telling the agent the client cancelled the a2atask |
| `Details` | `a2a_task_id`, `context_id`, `agent` |
| `Attrs` | `body`, `url`, `agent` |
| `Orgs` | the caller's org |
| `UserID` | empty, so only the org's public rules fire, as for custom events |

`model.RoutingKey` returns a URN unchanged, so the URN is the routing key.

- **`SendMessage`, no `taskId`:** a new a2atask. If the message has no
  `contextId`, `a2aserver` generates a UUID. A client-supplied `contextId` is
  accepted if it matches `^[A-Za-z0-9._-]{1,128}$` and returns `-32602`
  (invalid params) otherwise. That is what lets a client address a context a
  gritz task joined by holding its link. `a2aserver` inserts the a2atask
  (`working`) and routes a `message` event.
  - A subscribed link to the URN in the matched rule's namespace delivers it
    to the gritz task holding the link.
  - Otherwise a rule with a create action creates the gritz task, in the
    workspace and runner the rule names, with a subscribed `trigger` link to
    the URN. This is `eventrouter.Router.create`, unchanged.
  - If routing reaches no task (no rule matched, or a wake-only rule found no
    link), the a2atask moves to `failed` with the status message
    `no gritz task accepted the message`, and the response carries it.

  The response is the a2atask as an A2A `Task`.
- **`SendMessage`, `taskId`:** a message to an existing a2atask. An unknown ID
  returns `-32001` (TaskNotFoundError), a terminal a2atask returns `-32004`
  (UnsupportedOperationError), and a `contextId` other than the a2atask's
  returns `-32602`. `a2aserver` routes a `message` event to the a2atask's
  context. If it reaches no task, the request returns `-32603` and the a2atask
  is unchanged. Otherwise an `input-required` a2atask moves back to `working`.
- **`GetTask`:** returns the a2atask (see [Failure](#failure)).
- **`CancelTask`:** moves the a2atask to `canceled` and routes a `cancel` event
  to its context. The gritz task and its current run carry on; whether a
  stopped gritz task wakes for the cancel is the matching rule's `wakeup`. A
  terminal a2atask returns `-32002` (TaskNotCancelableError).

An a2atask in another org or under another `{agent}` path returns `-32001`.

The response `Task` is:

```json
{
  "id": "<a2atask uuid>",
  "contextId": "<contextId>",
  "status": {
    "state": "TASK_STATE_INPUT_REQUIRED",
    "message": {"messageId": "<status_message_id>", "role": "ROLE_AGENT", "parts": [{"text": "<status_message>"}]},
    "timestamp": "<updated_at>"
  }
}
```

`status.message` is left out while the agent hasn't set one.

### Event types and routing rules

`a2aserver` registers two built-in schemas on `eventrouter.DefaultSchemaRegistry`,
as `githubserver` and `atlassianserver` do:

- `a2a:message` (label "A2A: Message"), attrs `body`, `url`, `agent`.
- `a2a:cancel` (label "A2A: Cancel"), attrs `url`, `agent`.

`SchemaRegistry.Validate` gains two rules:

- An `a2a` rule must be `public`, as a `custom` rule must: A2A events have no
  actor.
- An `a2a:cancel` rule can't have a create action. A cancel is only meaningful
  to the gritz task already holding the context link.

A typical org needs two rules, shown here in the routing rule JSON's fields:

```yaml
- source: a2a
  type: message
  public: true
  wakeup: true
  conditions:
    - {attr: agent, op: equals, value: fixer}
  create:
    workspace: gritz
    runner: default
- source: a2a
  type: cancel
  public: true
  conditions:
    - {attr: agent, op: equals, value: fixer}
```

Rules match first-per-org, so the message rule both creates the gritz task for
a new context and wakes it for every later message. The cancel rule doesn't
wake: a running agent gets the cancel mid-run, and a stopped one sees it in its
next run's brief.

### Agent side

A new workspace capability, `a2a`, gates both additions, as `github_token`
gates `get_github_token`:

- `agentauth.CapabilityA2A = "a2a"`, accepted by `ValidCapability`.
- `agentauth.Scopes` adds `a2a_task.update`, constrained to the task's ID and
  `task.archived:"false"` like the task scopes.
- `agentmcp` registers `update_a2a_task` only when the capability is set.

The tool:

```
update_a2a_task(a2a_task_id: string, state: "completed" | "failed" | "input-required" | "rejected", message: string)
```

`message` is required. It becomes the A2A `status.message`, the only way a
result, a reason for failing, or a question reaches the client. A terminal
a2atask, including one the client cancelled, returns a tool error naming its
state. The tool's description tells the agent the ID comes from the
`a2a_task_id` detail of an `a2a` event.

The agent learns about a2atasks only from the events it receives: the
`message` and `cancel` events render like any external event, with the details
as the JSON block `agentprompt` already prints.

The tool calls the new RPC, which `apiserver` implements by checking the scope
and handing off to `a2aserver`:

```proto
service GritzService {
  // ...

  // UpdateA2ATask moves an A2A task the calling task received to a state the
  // agent sets. The task must hold a subscribed link to the A2A task's context.
  rpc UpdateA2ATask(UpdateA2ATaskRequest) returns (UpdateA2ATaskResponse);
}

// A2ATaskState is a state an agent can move an A2A task to.
enum A2ATaskState {
  A2A_TASK_STATE_UNSPECIFIED = 0;
  A2A_TASK_STATE_COMPLETED = 1;
  A2A_TASK_STATE_FAILED = 2;
  A2A_TASK_STATE_INPUT_REQUIRED = 3;
  A2A_TASK_STATE_REJECTED = 4;
}

message UpdateA2ATaskRequest {
  int64 task_id = 1;         // The calling task.
  string a2a_task_id = 2;    // The A2A task's ID, from an a2a event's details.
  A2ATaskState state = 3;
  string message = 4;        // Shown to the A2A client as the status message. Required.
}

message UpdateA2ATaskResponse {}
```

Errors: `PermissionDenied` without `a2a_task.update` on `task_id`; `NotFound`
for an a2atask outside the caller's org, or one whose context `task_id` holds no
subscribed link to; `FailedPrecondition` for a terminal a2atask;
`InvalidArgument` for an unspecified state or an empty message.

### Failure

Nothing closes an a2atask when its gritz task fails. Instead, `GetTask` checks
an open a2atask's context: it looks up the gritz tasks in the org holding a
subscribed link to the URN, archived ones included. If every one of them is
`FAILED` or `CANCELLED`, the context's open a2atasks move to `failed`, with the
status message `the gritz task working on this context failed`, and that is
persisted. If no task holds the link any more (deleted, or the link removed),
the same applies.

A gritz task that fails and is restarted before any poll keeps its a2atasks
open: the check sees its current status, not its history.

### Schema

```sql
-- migrate:up

-- A2A tasks. See proposals/draft/a2a-external-events.md.
CREATE TABLE a2a_tasks (
    id                UUID PRIMARY KEY,
    org_id            BIGINT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    agent             TEXT NOT NULL,           -- the {agent} path segment
    context_id        TEXT NOT NULL,
    state             TEXT NOT NULL,           -- working, input-required, completed, ...
    status_message    TEXT NOT NULL DEFAULT '',
    status_message_id UUID,                    -- the A2A messageId of status_message; NULL while it is empty
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_a2a_tasks_context ON a2a_tasks(org_id, context_id);

-- migrate:down

DROP TABLE IF EXISTS a2a_tasks;
```

There is no foreign key to `tasks`: an a2atask knows its context, and the link
says which gritz task holds it. The runner's SQLite state doesn't change.

### API

```go
package model // github.com/gritzapp/gritz/internal/model

// A2ATaskState is an A2A task's state, named after A2A's TaskState.
type A2ATaskState string // new

const ( // new
	A2ATaskStateWorking       A2ATaskState = "working"
	A2ATaskStateInputRequired A2ATaskState = "input-required"
	A2ATaskStateCompleted     A2ATaskState = "completed"
	A2ATaskStateFailed        A2ATaskState = "failed"
	A2ATaskStateCanceled      A2ATaskState = "canceled"
	A2ATaskStateRejected      A2ATaskState = "rejected"
)

// IsTerminal reports whether no further change can move the state.
func (s A2ATaskState) IsTerminal() bool // new

// A2ATask is a unit of work an A2A client handed to a context.
type A2ATask struct { // new
	ID              uuid.UUID
	OrgID           int64
	Agent           string
	ContextID       string
	State           A2ATaskState
	StatusMessage   string
	StatusMessageID uuid.UUID
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ContextURL returns the context's URN, the URL of its events and links.
func (t *A2ATask) ContextURL() string // new

// SetState moves the a2atask to state with a status message. It fails on a terminal a2atask.
func (t *A2ATask) SetState(state A2ATaskState, message string) error // new
```

```go
package store // github.com/gritzapp/gritz/internal/store

// CreateA2ATask inserts t.
func (s *Store) CreateA2ATask(ctx context.Context, tx *sql.Tx, t *model.A2ATask) error // new

// GetA2ATaskForUpdate returns the org's a2atask by ID, locking its row.
func (s *Store) GetA2ATaskForUpdate(ctx context.Context, tx *sql.Tx, id uuid.UUID, orgID int64) (*model.A2ATask, error) // new

// UpdateA2ATask writes t's state and status message.
func (s *Store) UpdateA2ATask(ctx context.Context, tx *sql.Tx, t *model.A2ATask) error // new

// FailOpenA2ATasks moves the context's open a2atasks to failed with message.
func (s *Store) FailOpenA2ATasks(ctx context.Context, tx *sql.Tx, orgID int64, contextID, message string) error // new

// ListLinkedTaskStatuses returns the status of every task in the org, archived or not, holding a subscribed link with routingKey.
func (s *Store) ListLinkedTaskStatuses(ctx context.Context, tx *sql.Tx, orgID int64, routingKey string) ([]model.TaskStatus, error) // new

// TaskHasSubscribedLink reports whether the task holds a subscribed link with routingKey.
func (s *Store) TaskHasSubscribedLink(ctx context.Context, tx *sql.Tx, taskID int64, routingKey string) (bool, error) // new
```

```go
package a2aserver // github.com/gritzapp/gritz/internal/server/a2aserver

// Route is the mux pattern of the JSON-RPC endpoint.
const Route = "POST /a2a/{org}/{agent}"

// CardRoute is the mux pattern of the agent card.
const CardRoute = "GET /a2a/{org}/{agent}/.well-known/agent-card.json"

// Options configures a Server.
type Options struct {
	Log       *slog.Logger
	Store     *store.Store
	Publisher pubsub.Publisher
	BaseURL   string
	Version   string
}

// Server serves the A2A endpoint and owns the a2a_tasks table.
type Server struct{ ... }

// New returns a new Server.
func New(opts Options) *Server

// RegisterSchemas records the a2a:message and a2a:cancel event types on reg.
func RegisterSchemas(reg *eventrouter.SchemaRegistry)

// Handler serves the JSON-RPC endpoint. It must be mounted behind apiauth's RequireResourceAuth.
func (s *Server) Handler() http.Handler

// CardHandler serves the agent card.
func (s *Server) CardHandler() http.Handler

// UpdateTask applies the agent's state change to an a2atask the task holds the context link for.
func (s *Server) UpdateTask(ctx context.Context, orgID, taskID int64, id uuid.UUID, state model.A2ATaskState, message string) error
```

```go
package apiserver // github.com/gritzapp/gritz/internal/server/apiserver

// A2ATasks applies agent state changes to A2A tasks.
type A2ATasks interface { // new
	// UpdateTask applies the agent's state change to an a2atask the task holds the context link for.
	UpdateTask(ctx context.Context, orgID, taskID int64, id uuid.UUID, state model.A2ATaskState, message string) error
}

type Options struct {
	// ...
	A2A A2ATasks // new
}

// UpdateA2ATask checks a2a_task.update on the task and hands the change to Options.A2A.
func (s *Server) UpdateA2ATask(ctx context.Context, req *gritzv1.UpdateA2ATaskRequest) (*gritzv1.UpdateA2ATaskResponse, error) // new
```

```go
package agentauth // github.com/gritzapp/gritz/internal/auth/agentauth

// CapabilityA2A allows updating the A2A tasks a task receives via the UpdateA2ATask RPC.
const CapabilityA2A = "a2a" // new
```

```go
package authscope // github.com/gritzapp/gritz/internal/auth/authscope

// OpA2ATaskUpdate is the right to move an A2A task the task received.
var OpA2ATaskUpdate = []string{"a2a_task", "update"} // new
```

```go
package agentmcp // github.com/gritzapp/gritz/internal/runner/agentmcp

// AddTools registers update_a2a_task when the workspace has the a2a capability.
func (s *Server) AddTools(server *mcp.Server) // changed
```

**How it fits:** `server.New` constructs an `a2aserver.Server` and passes it to
`apiserver.New` as `Options.A2A`. `server.Handler` mounts `CardHandler`
publicly and `Handler` behind `RequireResourceAuth`. The JSON-RPC handler
checks the path org against `apiauth.MustCaller`, decodes the request, reads
and writes `a2a_tasks` through the store, and routes events with its own
`eventrouter.Router`, as `customserver` does. `a2aserver`'s `init` calls
`RegisterSchemas` on the default registry. In the sandbox, `update_a2a_task`
calls `UpdateA2ATask` with the task token; `apiserver` loads the task for its
scope attributes, checks `OpA2ATaskUpdate`, and calls `a2aserver.UpdateTask`,
which checks the link and moves the a2atask in one transaction.

### Wire types

`a2aserver` hand-writes JSON structs for the subset it uses (`Message`, `Part`,
`Task`, `TaskStatus`, the three requests, `AgentCard` and its parts) and the
JSON-RPC envelope. A2A 1.0's wire format is the ProtoJSON form of its
`a2a.proto`, so the structs follow its camelCase field names and enum strings.

## Implementation Plan

1. **Schema and store.** Delivers: the `a2a_tasks` migration, `model.A2ATask`
   and its states, and the store methods. Depends on: nothing. Verifiable by:
   model tests for `SetState`; store tests against Postgres, including
   `ListLinkedTaskStatuses` over archived tasks.
2. **Event types.** Delivers: `a2aserver.RegisterSchemas` and the two new
   `SchemaRegistry.Validate` rules. Depends on: nothing. Verifiable by: rule
   validation tests (non-public `a2a` rule and `a2a:cancel` with create
   rejected); the event types show in the rule editor.
3. **A2A endpoint.** Delivers: `a2aserver` with the card, `SendMessage`,
   `GetTask` and `CancelTask`, mounted in `server.Handler`. Depends on: (1),
   (2). Verifiable by: handler tests for each method and error code; an e2e
   test where a create rule makes a gritz task for a new context, and a second
   message wakes the same task.
4. **Agent side.** Delivers: the `a2a` capability and scope, the
   `UpdateA2ATask` RPC, and `update_a2a_task`. Depends on: (3). Verifiable by:
   apiserver tests (no capability, no link, terminal a2atask); agentmcp tests
   that the tool is registered only with the capability; an e2e test in which
   a fake agent completes an a2atask and `GetTask` returns `completed`.
5. **Failure on poll.** Delivers: the `GetTask` check that fails a context's
   open a2atasks. Depends on: (3). Verifiable by: handler tests for a `FAILED`
   holder, a `CANCELLED` holder, a holder restarted before the poll, and no
   holder.
6. **Docs.** Delivers: an `integrations/a2a` page (endpoint, card, auth, rules,
   states, what's unsupported); the `a2a` capability in `operators/*`;
   `update_a2a_task` in `using/agent-tools`; the two event types in
   `reference/event-types`. Depends on: (4). Verifiable by: `pnpm build`;
   pointing a real A2A client at the dev stack by following the page.

## Trade-offs

- **External events vs a first-class entity in the main API**
  ([#2260](https://github.com/gritzapp/gritz/pull/2260)). That design added RPCs
  for every A2A operation, lifecycle hooks in `CancelTask` and
  `SubmitRunnerEvents`, and an `a2a_tasks` list in `get_my_task`. Routing A2A
  messages as events reuses link subscription, routing rules and the existing
  prompt rendering, and keeps the main API to one RPC. The cost is that a2atask
  state lives beside the gritz task rather than in its transactions: failure is
  detected lazily on poll, and an a2atask insert and its routing are not atomic.
- **Lazy failure on `GetTask` vs closing a2atasks on the gritz task's status
  change.** Closing eagerly needs hooks in `CancelTask` and `SubmitRunnerEvents`,
  the surface this experiment avoids. Lazy failure also leaves a failed task
  that is restarted before anyone polls able to finish its work.
- **Returning at once from `SendMessage`.** A2A 1.0 makes `SendMessage` block
  until a terminal or interrupted state unless the client sets
  `returnImmediately`. A gritz a2atask can take days, longer than any HTTP
  request, so the server always returns `working` at once and the client polls.
  This deviates from the spec for clients that don't set `returnImmediately`.
- **Empty `history`.** Returning the a2atask's messages would mean querying
  events by a detail value. The status message carries what a polling client
  needs.
- **Free agent names vs registered agents.** Registering agents would need a
  new table or config and an RPC to manage it. A path segment plus a rule
  condition reuses the routing rules the operator already writes.
- **Hand-written wire types vs a2a-go or generated types.** The server uses
  three methods. a2a-go's request handler assumes an in-process task store and
  event queue, which don't fit routing through events. Generating from
  `a2a.proto` with `buf` would add googleapis dependencies to the proto module
  for about ten messages.
- **One tool with a `state` enum vs one tool per state.** The four tools would
  take the same parameters.
- **Mapping an A2A task to a run**, **to an instruction event**, or **renaming
  the gritz task** were ruled out in the issue.

## Open Questions

- **Endpoint and agent card shape.** This proposal serves one agent per path
  name and lets rules match the `agent` attribute. The alternative is one agent
  per org at `/a2a/{org_id}`, which is simpler but leaves a client one card and
  the operator one workspace choice per org, short of matching on message text.
- **Non-text parts.** Jira's remote-agent messages carry a `data` part
  (`invocationType`, among others). This proposal rejects them with `-32005`.
  Passing them through as JSON in `details` would let Jira work as a client.
- **Two gritz tasks holding the same context link.** Routing is by link, so both
  receive the context's events, and either can update its a2atasks. This
  proposal fails the a2atasks only when every holder has failed. Should a
  second subscribed link to an `a2a` context URN be refused instead?
- **Archived holders.** Routing skips archived tasks, so a message to a context
  whose gritz task was archived (including auto-archived) creates a new gritz
  task through the create rule, with none of the old task's history. Is that
  acceptable, or should `a2aserver` reject messages to such a context?