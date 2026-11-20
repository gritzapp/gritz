---
title: "A2A server"
description: "An A2A client has no way to hand work to a gritz workspace and follow it to completion, and an A2A task's lifecycle doesn't match a gritz task's or a run's."
---

Issue: https://github.com/gritzapp/gritz/issues/2259

## Problem

An [A2A](https://a2a-protocol.org) client should be able to hand work to a gritz
workspace and poll until it's done. A2A's "task" doesn't line up with a gritz
task, though. A gritz task is long-lived: it takes many instructions and events,
and it reaches `COMPLETED` every time a run's sandbox exits, until an event wakes
it again. An A2A task is one unit of work. It ends once and is then immutable.

Take an A2A task to fix some code. The agent opens a PR, works through review
comments over several runs, and declares the A2A task complete only when the PR
is merged. So neither the gritz task's status nor a run can tell us an A2A
task's state. It needs its own state, and the agent moves it.

## Design

**A gritz task is an A2A context, and a new entity, the a2atask, is an A2A task.
A gritz task owns any number of a2atasks. Each one spans any number of runs, and
only the agent (with a new MCP tool), an A2A cancel, or the end of the gritz
task closes it.** Each workspace is its own A2A agent, with its own agent card
and JSON-RPC endpoint. The endpoint is a thin adapter over new Connect RPCs, the
same way `/mcp` adapts the Connect API (`mcpserver.Handler`). So API key scopes,
org scoping and notifications work as they do for every other caller.

### Model

| A2A | gritz |
| --- | --- |
| `contextId` | gritz task ID (decimal string) |
| task (`id`) | a2atask ID (decimal string) |
| agent | workspace |
| user message | instruction event carrying the a2atask ID |
| `status.message` | the message the agent passed with its last state change |

- A gritz task can have several open a2atasks at once.
- Tasks created from the UI, the CLI, schedules or routing rules have no
  a2atask. An A2A client can still pass such a task's ID as `contextId` and open
  one in it.
- "Task" keeps its gritz meaning everywhere: proto, store, CLI, MCP tools, UI.
  The new entity is always `A2ATask` / `a2a_task` in code.

### States

An a2atask uses A2A's state names. gritz uses five of them:

| State | Terminal | Entered by |
| --- | --- | --- |
| `working` | no | creation; a client message to an `input-required` a2atask |
| `input-required` | no | the agent |
| `completed` | yes | the agent |
| `rejected` | yes | the agent |
| `failed` | yes | the agent; the gritz task's sandbox failing |
| `canceled` | yes | `tasks/cancel`; cancelling the gritz task |

An a2atask is created in `working`, not `submitted`. Sandbox starts and exits
don't move it, because it outlives the run. `submitted`, `auth-required` and
`unknown` are never used.

Once an a2atask is terminal, nothing moves it again. A client message, an agent
state change or a cancel on it returns an error.

### Requests

The JSON-RPC endpoint handles `message/send`, `tasks/get` and `tasks/cancel`.
Every other method returns `-32601` (method not found). The agent card says
streaming and push notifications are unsupported, so a client won't call
`message/stream`, `tasks/resubscribe` or `tasks/pushNotificationConfig/*`.

The message's text parts are joined with blank lines and become the instruction
text. A message with a `file` or `data` part returns `-32005`
(ContentTypeNotSupportedError). `message.metadata` and
`configuration.blocking` are ignored: every `message/send` returns as soon as
the work is queued. `configuration.historyLength` is ignored and `history` is
always empty (see open questions).

- **`message/send`, no `contextId`, no `taskId`:** `CreateA2ATask` with the
  endpoint's workspace. In one transaction this creates a gritz task in that
  workspace (no name, so the agent names it; no auto-archive, as a `CreateTask`
  without one), an a2atask in it, and the message as a
  waking instruction event. The response is the new A2A `Task`, `working`, with
  `contextId` set.
- **`message/send`, `contextId`, no `taskId`:** `CreateA2ATask` with
  `task_id = contextId`. It opens another a2atask in that gritz task, appends
  the instruction, and wakes the task (`Task.Start`, the same as
  `UpdateTask{start: true}`). The task must belong to the endpoint's workspace.
  If it doesn't, the request gets `-32001` (TaskNotFoundError), so other
  workspaces' task IDs can't be probed through this endpoint.
- **`message/send`, `taskId`:** `SendA2AMessage`. It appends the instruction
  with the a2atask's ID and wakes the gritz task. An `input-required` a2atask
  goes back to `working`. A terminal a2atask returns `-32004`
  (UnsupportedOperationError), and a `contextId` that isn't the a2atask's gritz
  task returns `-32602` (invalid params).
- **`tasks/get`:** `GetA2ATask`. Returns `status` (state, the agent's last
  message, the time of the last state change). Clients poll it.
- **`tasks/cancel`:** `CancelA2ATask`. Moves the a2atask to `canceled` and
  appends an a2atask event that reaches the agent (below). It does not wake the
  gritz task, and the current run, if any, carries on. A terminal a2atask
  returns `-32002` (TaskNotCancelableError).

An a2atask ID that doesn't exist, belongs to another org, or belongs to a task
in another workspace returns `-32001`.

### Agent side

A new agent MCP tool, registered for every run because an a2atask can be opened
in the middle of one:

```
update_a2a_task(a2a_task_id: int, state: "completed" | "failed" | "input-required" | "rejected", message: string)
```

It calls `UpdateA2ATask`. `message` is required. It becomes the A2A
`status.message`, the only way the result, the reason for failing, or the
question for the client reaches the client (artifacts are out of scope). A
terminal a2atask, including a canceled one, returns a tool error naming its
state.

The issue says "tools". This is one tool with a `state` enum rather than four
near-identical tools, because the parameters are the same for all four states.

`get_my_task` gains an `a2a_tasks` list of the task's a2atasks: id, state, and
created time. An instruction event that carries an a2atask ID renders in the
prompt and in `get_my_task` as:

```
### A2A Task 17 — Instruction — 2026-11-20 15:22 UTC
<text>

Report the outcome with update_a2a_task(a2a_task_id=17, ...).
```

### Gritz task lifecycle

Each of these closes the task's open (`working` and `input-required`) a2atasks
in the same transaction as the status change, and appends one a2atask event per
a2atask it closes:

- **Sandbox failure.** `SubmitRunnerEvents`, when the fold moves the task to
  `FAILED`, moves them to `failed`, with the lifecycle event's failure message
  as the status message.
- **Cancel.** `CancelTask` moves them to `canceled`. This happens when the user
  cancels, not when the runner confirms the stop, so `tasks/get` reflects the
  user's intent at once.

`COMPLETED` closes nothing. It is the normal state between wakes.

### Events

A new `Event.payload` arm records every a2atask state change, including
creation, on the task's timeline:

```proto
// A2ATaskPayload records that one of the task's a2atasks was opened or changed
// state.
message A2ATaskPayload {
  int64 a2a_task_id = 1;
  A2ATaskState state = 2;
  string message = 3;  // The status message set with the change; empty for none.
  Actor actor = 4;     // "user" (an A2A client or an operator), "agent" or "runner".
}
```

The driver's `promptEvents` passes an a2atask event to the agent only when its
state is `canceled`. That is how a cancel "notifies the agent": a running agent
gets it the way it gets any mid-run event, and a stopped one sees it in its next
run's brief. The agent's own changes, and the failures the runner causes, are
not to-agent.

`InstructionPayload` gains `int64 a2a_task_id`, zero for an ordinary
instruction.

### Schema

```sql
-- migrate:up

-- A2A tasks. See proposals/draft/a2a-server.md.
CREATE TABLE a2a_tasks (
    id             BIGSERIAL PRIMARY KEY,
    org_id         BIGINT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    task_id        BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    state          TEXT NOT NULL,           -- working, input-required, completed, ...
    status_message TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_a2a_tasks_task_id ON a2a_tasks(task_id);

-- migrate:down

DROP TABLE IF EXISTS a2a_tasks;
```

The runner's SQLite state needs no change: a2atasks never reach the runner.

### Endpoint and agent card

```
GET  /a2a/{org_id}/{workspace}/.well-known/agent-card.json   public
POST /a2a/{org_id}/{workspace}                               JSON-RPC, Bearer
```

The org is in the path because workspace names are only unique within an org,
and the card is fetched before the client has a token. The JSON-RPC route is
wrapped in `RequireResourceAuth`, like `/mcp`, so an unauthenticated request
gets the 401 challenge that starts the OAuth flow. Both an org API key and an
OAuth token are bound to one org. A path `org_id` other than the caller's
returns 404, so it can't be used to discover other orgs' workspaces.

The card is built from the workspace's registered row (`ListWorkspaces`). It is
served only while the workspace is registered, and is a 404 otherwise. Because
the card endpoint is unauthenticated, it reads the store directly instead of
going through the Connect API:

```json
{
  "protocolVersion": "0.3.0",
  "name": "<workspace name>",
  "description": "<workspace description>",
  "url": "https://gritz.dev/a2a/12/<workspace>",
  "preferredTransport": "JSONRPC",
  "version": "<gritz version>",
  "capabilities": {"streaming": false, "pushNotifications": false},
  "defaultInputModes": ["text/plain"],
  "defaultOutputModes": ["text/plain"],
  "skills": [{"id": "task", "name": "<workspace name>", "description": "<workspace description>", "tags": ["gritz"]}],
  "securitySchemes": {
    "apiKey": {"type": "http", "scheme": "bearer", "description": "A gritz org API key."},
    "oauth": {"type": "oauth2", "flows": {"authorizationCode": {
      "authorizationUrl": "https://gritz.dev/oauth/authorize",
      "tokenUrl": "https://gritz.dev/oauth/token",
      "scopes": {}
    }}}
  },
  "security": [{"apiKey": []}, {"oauth": []}]
}
```

### Proto

```proto
service GritzService {
  // ...
  rpc CreateA2ATask(CreateA2ATaskRequest) returns (CreateA2ATaskResponse);
  rpc GetA2ATask(GetA2ATaskRequest) returns (GetA2ATaskResponse);
  rpc ListA2ATasks(ListA2ATasksRequest) returns (ListA2ATasksResponse);
  rpc SendA2AMessage(SendA2AMessageRequest) returns (SendA2AMessageResponse);
  rpc CancelA2ATask(CancelA2ATaskRequest) returns (CancelA2ATaskResponse);
  rpc UpdateA2ATask(UpdateA2ATaskRequest) returns (UpdateA2ATaskResponse);
}

// A2ATaskState is an a2atask's state, named after A2A's TaskState.
enum A2ATaskState {
  A2A_TASK_STATE_UNSPECIFIED = 0;
  A2A_TASK_STATE_WORKING = 1;
  A2A_TASK_STATE_INPUT_REQUIRED = 2;
  A2A_TASK_STATE_COMPLETED = 3;
  A2A_TASK_STATE_FAILED = 4;
  A2A_TASK_STATE_CANCELED = 5;
  A2A_TASK_STATE_REJECTED = 6;
}

// A2ATask is a unit of work an A2A client handed to a task. It spans any number
// of the task's runs.
message A2ATask {
  int64 id = 1;
  int64 task_id = 2;  // The owning task: the A2A contextId.
  A2ATaskState state = 3;
  string status_message = 4;  // The message set with the last state change.
  google.protobuf.Timestamp created_at = 5;
  google.protobuf.Timestamp updated_at = 6;
}

message CreateA2ATaskRequest {
  // Where to open the a2atask: an existing task, or a new task in a workspace.
  oneof context {
    int64 task_id = 1;
    string workspace = 2;
  }
  // The first message, appended as an instruction that wakes the task.
  string text = 3;
}
message CreateA2ATaskResponse { A2ATask a2a_task = 1; Task task = 2; }

message GetA2ATaskRequest { int64 id = 1; }
message GetA2ATaskResponse { A2ATask a2a_task = 1; Task task = 2; }

message ListA2ATasksRequest { int64 task_id = 1; }
message ListA2ATasksResponse { repeated A2ATask a2a_tasks = 1; }

message SendA2AMessageRequest { int64 id = 1; string text = 2; }
message SendA2AMessageResponse { A2ATask a2a_task = 1; }

message CancelA2ATaskRequest { int64 id = 1; }
message CancelA2ATaskResponse { A2ATask a2a_task = 1; }

// UpdateA2ATaskRequest is the agent closing an a2atask or asking its client
// for input. state is one of INPUT_REQUIRED, COMPLETED, FAILED or REJECTED.
message UpdateA2ATaskRequest { int64 id = 1; A2ATaskState state = 2; string message = 3; }
message UpdateA2ATaskResponse { A2ATask a2a_task = 1; }
```

Authorization reuses the task scopes, checked against the owning task in the
transaction that loads it. `CreateA2ATask` with `workspace` needs
`task.create` (with `task.workspace`), as `CreateTask` does. With `task_id`,
and for `SendA2AMessage`, `CancelA2ATask` and `UpdateA2ATask`, it needs
`task.write` on the owning task. `GetA2ATask` and `ListA2ATasks` need
`task.read`. The agent's task token already holds `task.write` and `task.read`
on its own task, so `update_a2a_task` and `get_my_task` need no new scope, and
an agent can't touch another task's a2atasks.

`GetA2ATaskResponse` and `CreateA2ATaskResponse` return the owning `Task` so
the endpoint can check its workspace without a second round trip.

### API

```go
package model // github.com/gritzapp/gritz/internal/model

// A2ATaskState is an a2atask's state.
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

// A2ATask is a unit of work an A2A client handed to a task.
type A2ATask struct { // new
	ID            int64
	OrgID         int64
	TaskID        int64
	State         A2ATaskState
	StatusMessage string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Message records a client message: an input-required a2atask goes back to working. It fails on a terminal a2atask.
func (t *A2ATask) Message() error // new

// Cancel moves the a2atask to canceled. It fails on a terminal a2atask.
func (t *A2ATask) Cancel() error // new

// SetState applies the agent's state change. It fails on a terminal a2atask and panics on a state the agent can't set.
func (t *A2ATask) SetState(state A2ATaskState, message string) error // new

// Close moves an open a2atask to state when its task ends, and reports whether it was open.
func (t *A2ATask) Close(state A2ATaskState, message string) bool // new

// Event returns the a2atask event recording the a2atask's current state.
func (t *A2ATask) Event(actor Actor) *Event // new

// Proto converts the a2atask to its proto message.
func (t *A2ATask) Proto() *gritzv1.A2ATask // new

// A2ATaskPayload is the body of an a2atask event.
type A2ATaskPayload struct { // new
	A2ATaskID int64        `json:"a2a_task_id"`
	State     A2ATaskState `json:"state"`
	Message   string       `json:"message,omitempty"`
	Actor     Actor        `json:"actor"`
}

// AgentActor is the actor for a change the task's agent made.
func AgentActor() Actor // new

type InstructionPayload struct {
	Text      string `json:"text"`
	URL       string `json:"url,omitempty"`
	A2ATaskID int64  `json:"a2a_task_id,omitempty"` // new
}
```

```go
package store // github.com/gritzapp/gritz/internal/store

// CreateA2ATask inserts t and sets its ID and timestamps.
func (s *Store) CreateA2ATask(ctx context.Context, tx *sql.Tx, t *model.A2ATask) error // new

// GetA2ATask returns the org's a2atask by ID.
func (s *Store) GetA2ATask(ctx context.Context, tx *sql.Tx, id, orgID int64) (*model.A2ATask, error) // new

// GetA2ATaskForUpdate returns the org's a2atask by ID, locking its row.
func (s *Store) GetA2ATaskForUpdate(ctx context.Context, tx *sql.Tx, id, orgID int64) (*model.A2ATask, error) // new

// UpdateA2ATask writes t's state and status message.
func (s *Store) UpdateA2ATask(ctx context.Context, tx *sql.Tx, t *model.A2ATask) error // new

// ListA2ATasksByTask returns the task's a2atasks, oldest first.
func (s *Store) ListA2ATasksByTask(ctx context.Context, tx *sql.Tx, taskID, orgID int64) ([]*model.A2ATask, error) // new

// CloseA2ATasks moves the task's open a2atasks to state and returns the ones it closed.
func (s *Store) CloseA2ATasks(ctx context.Context, tx *sql.Tx, taskID, orgID int64, state model.A2ATaskState, message string) ([]*model.A2ATask, error) // new
```

```go
package a2aserver // github.com/gritzapp/gritz/internal/server/a2aserver

// Route is the mux pattern for the JSON-RPC endpoint.
const Route = "POST /a2a/{org}/{workspace}"

// CardRoute is the mux pattern for the agent card.
const CardRoute = "GET /a2a/{org}/{workspace}/.well-known/agent-card.json"

// Workspaces looks up registered workspaces for the unauthenticated card route.
type Workspaces interface {
	// GetWorkspace returns the org's registered workspace by name.
	GetWorkspace(ctx context.Context, orgID int64, name string) (*model.Workspace, error)
}

// Options configures the A2A endpoint.
type Options struct {
	Service    gritzv1connect.GritzServiceHandler
	Workspaces Workspaces
	BaseURL    string
	Version    string
}

// Handler serves the JSON-RPC endpoint, translating A2A methods to gritz RPCs.
func Handler(opts Options) http.Handler

// CardHandler serves a workspace's agent card.
func CardHandler(opts Options) http.Handler
```

```go
package agentmcp // github.com/gritzapp/gritz/internal/runner/agentmcp

// AddTools registers update_a2a_task beside the existing tools, and get_my_task lists a2a_tasks.
func (s *Server) AddTools(server *mcp.Server) // changed
```

**How it fits:** `server.Handler` mounts `a2aserver.CardHandler` publicly and
`a2aserver.Handler` behind `RequireResourceAuth`, both built from the wrapped
Connect service, as `/mcp` is. The JSON-RPC handler checks the path org against
`apiauth.MustCaller`, decodes the request, and calls `CreateA2ATask`,
`SendA2AMessage`, `GetA2ATask` or `CancelA2ATask` on the service. It maps
Connect codes to A2A errors (`NotFound` to `-32001`, `FailedPrecondition` on a
terminal a2atask to `-32004` or `-32002`). The apiserver handlers drive
`model.A2ATask` transitions and write the a2atask and instruction events, and
for wakes the task, command and lifecycle events, in one transaction, the way
`UpdateTask` does. `CancelTask` and `SubmitRunnerEvents` call
`store.CloseA2ATasks` when the task is cancelled or fails. In the sandbox,
`update_a2a_task` calls `UpdateA2ATask` with the task token, and `agentprompt`
renders the new instruction header and the canceled a2atask event.

### Web UI

The task page lists the task's a2atasks (ID, state, status message) next to its
links, and the timeline renders a2atask events. There is no UI to create or
cancel an a2atask. Those come from A2A clients.

### Docs

- A new `integrations/a2a` page: the endpoint and card URLs, auth, what each
  request does, the state table, and what's unsupported.
- `using/agent-tools`: `update_a2a_task`, and `a2a_tasks` in `get_my_task`.
- `reference/api` picks up the proto comments automatically.

## Implementation Plan

1. **Schema and model.** Delivers: the `a2a_tasks` migration, `model.A2ATask`
   and its transitions, `A2ATaskPayload` and `InstructionPayload.a2a_task_id`
   (model and proto), and the store methods. Depends on: nothing. Verifiable
   by: model unit tests for every transition; store tests against Postgres.
2. **Connect RPCs.** Delivers: the six RPCs with scope checks and events, and
   `ListA2ATasks`. Depends on: (1). Verifiable by: apiserver tests: creating in
   a workspace or an existing task wakes it; a message to `input-required`
   reopens it; a terminal a2atask rejects messages, updates and cancels; scopes
   deny another task's a2atasks.
3. **Task lifecycle.** Delivers: `CancelTask` and `SubmitRunnerEvents` closing
   open a2atasks. Depends on: (1). Verifiable by: apiserver tests for cancel
   (pending and running) and sandbox failure; a `COMPLETED` exit leaves them
   `working`.
4. **Agent side.** Delivers: `update_a2a_task`, `a2a_tasks` in `get_my_task`,
   the instruction header, and canceled a2atask events in `promptEvents`.
   Depends on: (2). Verifiable by: agentmcp and agentprompt tests; an e2e test
   in which a fake agent completes an a2atask.
5. **A2A endpoint.** Delivers: `a2aserver` (card and JSON-RPC) mounted in
   `server.Handler`, and the `integrations/a2a` and `using/agent-tools` docs.
   Depends on: (2), (4). Verifiable by: handler tests for each method and
   error code against a mocked service (`Options.Wrap`); an e2e test driving
   `message/send` then `tasks/get` until `completed`; pointing a real A2A
   client at the dev stack.
6. **Web UI.** Delivers: the a2atask list on the task page and timeline
   rendering of a2atask events. Depends on: (2). Verifiable by: viewing an
   A2A-created task in the dev stack.

## Trade-offs

- **An A2A task as a run** was ruled out by the PR example: one A2A task spans
  many runs.
- **An A2A task as an instruction event** is close in shape, but it would make
  every instruction from every source an A2A task, and an event is immutable
  where an a2atask has state.
- **Renaming the gritz "task"** would free the name, but it touches the proto,
  store, CLI, MCP tools, web UI and docs, and no replacement name fits.
- **A2A state derived from gritz task status** can't express "the run ended but
  the work isn't done". So the state is stored, and the agent moves it.
- **Starting in `submitted` and moving to `working` when the run starts** would
  tie the a2atask to runner events, which the design otherwise keeps it out of.
  To a polling client the gap is cosmetic.
- **One `update_a2a_task` tool vs one tool per state.** The four tools would
  take identical parameters. One tool with a `state` enum keeps the tool list
  short, and the enum spells out the allowed states anyway.
- **Closing on cancel vs on the runner's confirmation.** Closing in `CancelTask`
  means `tasks/get` reports `canceled` while the sandbox is still stopping. The
  user's decision is final, and nothing the agent does in that window can
  reopen the a2atask.
- **Org ID in the path vs a per-org hostname or slug.** gritz already
  identifies orgs by ID in URLs (`?org=12`), and the card has to be reachable
  without a token.

## Open Questions

- **Archiving.** Archiving (including auto-archive) doesn't end a gritz task,
  so an archived task's open a2atasks stay `working` until an event unarchives
  and wakes it. Should archiving cancel them instead? The current proposal
  leaves them open, matching "only the agent, a cancel, or the task ending".
- **`history`.** `tasks/get` could return the a2atask's client messages and
  status messages as `history` by querying instruction and a2atask events by
  `a2a_task_id`. That needs a JSONB index on events. Is it worth it in v1?
- **SDK.** [a2a-go](https://github.com/a2aproject/a2a-go) provides the A2A
  types and a JSON-RPC server. Using it saves hand-writing the wire types for
  three methods, but adds a dependency whose request-handler interface assumes
  an in-process task store and event queue, which don't fit here. The proposal
  defaults to using only its types package, if that can be imported alone, and
  writing the three-method dispatcher by hand.
- **Agent card skills.** A card needs at least one skill. One skill per
  workspace, built from its description, is the minimum. Should
  `workspaces.yaml` gain an optional `a2a:` block for skills and examples?
