# Event Workflows

Issue: https://github.com/gritzapp/gritz/issues/2203

## Problem

The issue describes a coordination model: workflows declare agents, agents declare triggers, subscriptions and link types, and tasks talk to each other by emitting events on a bus. It is a model, not a design. This proposal is the design: what is stored, what the API looks like, and how the router changes.

What the model replaces:

- A routing rule (`model.RoutingRule`) does three jobs: admission (`Public`, conditions), delivery policy (`Wakeup`) and spawning (`Create`). `Router.Plan` picks the first matching rule per org, so two workflows that care about the same event can't both act.
- What a subscribed link delivers is decided by whichever rule matched, not by the task that created the link. A link is only a URL (`task_links.routing_key`), and it only works when some rule matches the event first.
- A namespace (`tasks.namespace`, `RoutingRule.Namespace`) is a filter that `Router.Apply` applies to the link lookup. Nothing is published to it.
- Agents have four tools (`get_my_task`, `update_my_task`, `create_link`, `report`). None of them publishes anything another task can receive.
- `schedules` carries its own copy of the task template (`workspace`, `runner`, `namespace`, `instructions`, `auto_archive`) that `CreateTaskAction` also carries.

## Design

### Overview

- A **workflow** is a named YAML definition stored per org. It declares agents and exports. An agent is a task template plus `triggers`, `subscribe` and `links`.
- A **workflow instance** is one run of a workflow. Every task belongs to exactly one instance and records which agent of the instance it is. A plain task (`CreateTask`) is an instance of an anonymous one-agent workflow.
- The instance is the task's event bus. Agents publish to it with a new `emit` tool; the server publishes `system` events to it.
- `eventrouter.Router` is rewritten around one operation, `Publish`: evaluate every trigger and every subscription that matches the event, start tasks, deliver events. There is no rule order and no first match.
- Subscriptions are materialised in a `subscriptions` table when a task starts and when it creates a subscribed link. Claims are read from `task_links`.
- Routing rules, `tasks.namespace` and the schedule's task template are removed.

Answers to the two questions on the issue:

- **Starting a workflow by hand** is a `StartWorkflow` RPC. It is exposed as a `start_workflow` tool on the server MCP (so `mcp__gritz__start_workflow` in a client), as `gritz workflow start`, and as a **Start** button on the workflow's page. See [Starting an instance](#starting-an-instance).
- **Two agents in one workflow whose triggers match the same external event** start two instances with one task each. Each trigger on an external source or a named namespace starts its own instance. Within one agent, triggers are alternatives: an agent starts at most once per event, from its first matching trigger.

### Workflow definition

The YAML shape is the one in the issue. The complete set of keys:

```yaml
workflow: feature            # name, unique in the org
export:                      # optional
  - type: pr.reviewed        # filter on the instance bus (source is always task)
    namespace: reviews       # destination named namespace
agents:
  implementer:
    workspace: default
    runner: main
    prompt: Implement the item.
    auto_archive: 24h        # optional, as Task.auto_archive
    capabilities:            # optional
      - task_tools
    triggers:
      - type: work.item      # a filter, plus:
        link: work.item      # optional link type
    subscribe:
      - type: work.revised   # a filter, plus:
        agent: planner
        wake: false          # optional, default true
    links:
      github.pr:
        claim: owner         # optional, default "default"
        subscribe:
          - source: github
            type: pull_request_review_comment
```

A filter has these keys:

| Key | Meaning |
| --- | --- |
| `source` | `github`, `atlassian`, `custom`, `schedule`, `task` or `system`. Omitted means `task`. |
| `type` | The event type. Required. |
| `namespace` | A named namespace. Only valid with `source: task`. Omitted means the instance's own bus. |
| `agent` | The emitting task's agent name. Only valid with `task` and `system`. |
| `workflow` | The emitting task's workflow name. Only valid with `namespace`. |
| `filter` | Attribute conditions. `attr: value` is `equals`; `attr: {prefix: value}` and `attr: {contains: value}` select the other operators. Parsed into `[]model.Condition` and matched by the existing `conditionMatch`. |
| `public` | Only valid with `github` and `atlassian`. See [Trust](#trust). |

`PutWorkflow` validates the whole definition and rejects it with `InvalidArgument` naming the first offending key:

- Workflow, agent, namespace and claim names match `^[a-z][a-z0-9_-]{0,63}$`. Event types emitted by agents and link type names match `^[a-z][a-z0-9_.]{0,63}$`.
- A filter on `github`, `atlassian` or `custom` must name a `(source, type)` registered in `eventrouter.DefaultSchemaRegistry.With(orgCustomTypes...)`, and each condition's attr must be one the type declares. This is `SchemaRegistry.Validate` applied to filters instead of rules.
- A filter on `system` must name `workflow.started`, `task.completed` or `task.failed`.
- A filter on `task` has no schema; its conditions are not checked.
- A trigger's `link` must name an entry in the agent's `links`.
- An export has no `source` other than `task` and must name a `namespace`.
- `capabilities` entries must be `task_tools`.

`SetCustomEventTypes` re-validates every stored workflow of the org under `LockOrg`, as it re-validates routing rules today. `PutWorkflow` takes the same lock.

### Storage

```sql
-- internal/store/sql/migrations/2026MMDD000001_workflows.sql
-- migrate:up
CREATE TABLE workflows (
    id BIGSERIAL PRIMARY KEY,
    org_id BIGINT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    source TEXT NOT NULL,          -- the YAML as submitted
    spec JSONB NOT NULL,           -- model.Workflow, parsed and validated
    created_at TIMESTAMP NOT NULL DEFAULT (now() AT TIME ZONE 'UTC'),
    updated_at TIMESTAMP NOT NULL DEFAULT (now() AT TIME ZONE 'UTC'),
    UNIQUE (org_id, name)
);

CREATE TABLE workflow_instances (
    id BIGSERIAL PRIMARY KEY,
    org_id BIGINT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    workflow TEXT NOT NULL DEFAULT '',   -- '' for a plain task
    spec JSONB NOT NULL,                 -- snapshot of model.Workflow at start
    created_at TIMESTAMP NOT NULL DEFAULT (now() AT TIME ZONE 'UTC')
);
CREATE INDEX idx_workflow_instances_org_workflow ON workflow_instances (org_id, workflow, id DESC);

ALTER TABLE tasks ADD COLUMN instance_id BIGINT REFERENCES workflow_instances(id) ON DELETE CASCADE;
ALTER TABLE tasks ADD COLUMN agent TEXT NOT NULL DEFAULT 'task';
INSERT INTO workflow_instances (id, org_id, spec) SELECT id, org_id, '{}' FROM tasks;
SELECT setval('workflow_instances_id_seq', (SELECT COALESCE(MAX(id), 1) FROM workflow_instances));
UPDATE tasks SET instance_id = id;
ALTER TABLE tasks ALTER COLUMN instance_id SET NOT NULL;
CREATE INDEX idx_tasks_instance_id ON tasks (instance_id);

ALTER TABLE task_links ADD COLUMN type TEXT NOT NULL DEFAULT 'url';
ALTER TABLE task_links ADD COLUMN claim TEXT NOT NULL DEFAULT '';
ALTER TABLE task_links RENAME COLUMN routing_key TO subject;
ALTER INDEX idx_task_links_routing_key RENAME TO idx_task_links_subject;

CREATE TABLE subscriptions (
    id BIGSERIAL PRIMARY KEY,
    org_id BIGINT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    task_id BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    link_id BIGINT REFERENCES task_links(id) ON DELETE CASCADE,  -- NULL: from the agent's subscribe list
    namespace TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL,
    type TEXT NOT NULL,
    subject TEXT NOT NULL DEFAULT '',    -- set for link subscriptions
    agent TEXT NOT NULL DEFAULT '',
    workflow TEXT NOT NULL DEFAULT '',
    conditions JSONB NOT NULL DEFAULT '[]',
    public BOOLEAN NOT NULL DEFAULT FALSE,
    wake BOOLEAN NOT NULL DEFAULT TRUE
);
CREATE INDEX idx_subscriptions_lookup ON subscriptions (org_id, source, type, namespace);
CREATE INDEX idx_subscriptions_task_id ON subscriptions (task_id);
```

Decisions behind this:

- **The instance snapshots the definition.** `workflow_instances.spec` is a copy of `workflows.spec` taken when the instance starts. Every trigger on the instance bus, every link type expansion and every capability of the instance's tasks reads the snapshot. Editing or deleting a workflow affects instances started afterwards. This settles the "which version governs a live task" item the issue left to the implementation.
- **The instance bus is the instance id.** Namespaces are one text column. An instance bus is written `i/<instance id>`, which the namespace name pattern can't produce, and external events use `''`.
- **Subscriptions are rows, triggers are not.** A task's subscriptions are fixed at two moments (task start, subscribed link creation), so they are expanded into rows then and looked up by index at publish time. Triggers belong to definitions, and an org has few workflows, so `Publish` loads the org's `workflows.spec` rows (for external and named-namespace events) or the one instance snapshot (for instance-bus events) and matches in Go, as `Plan` does with `orgs.routing_rules` today.
- **Claims are columns on the link.** A subscribed link row stores the resolved claim value. "Who holds claim C on subject S" is `task_links JOIN tasks WHERE claim = C AND subject = S AND subscribe AND NOT archived`, the shape of today's `FindSubscribedLinksForOrgs`. Subscriptions are joined against `tasks.archived` the same way, so archiving ends a link's effects and unarchiving restores them without rewriting rows.
- **Plain tasks.** `CreateTask` and a draft task create an instance with `workflow = ''` and a one-agent spec: the agent is named `task`, its `workspace` and `runner` come from the request, and its `links` are the org's default link types (below).

A second migration, in the cutover slice, removes what the model replaces:

```sql
ALTER TABLE orgs DROP COLUMN routing_rules;
ALTER TABLE orgs ADD COLUMN default_links JSONB NOT NULL DEFAULT '{}';
ALTER TABLE tasks DROP COLUMN namespace;
```

The schedule migration is under [Schedules](#schedules).

### Events

`eventrouter.InputEvent` stays the type webhook handlers build. The router derives a bus event from it per org:

```go
// Event is one event published in one org.
type Event struct {
	OrgID       int64
	Namespace   string            // "" for external sources
	Source      string
	Type        string
	Subject     string            // normalised resource the event is about
	LinkType    string            // built-in link type of Subject, "" if none
	Description string
	Body        string
	URL         string            // the event's own URL, if it has one
	Attrs       Attrs
	Details     map[string]string
	Emitter     *model.Emitter    // task and system-about-task events
	Member      bool              // github, atlassian: the actor is a member of OrgID
	Meta        any
}
```

- **Subject.** `model.RoutingKey` becomes `model.ResolveSubject(raw string) (linkType, subject string)`. It keeps the URL normalisation and adds the type: `github.pr`, `github.issue`, `jira.issue`, or `url` for any other absolute `http`/`https` URL, matched exactly as today. A custom event's subject is its envelope `url`, as sent.
- **Persistence.** Events are still stored once per receiving task, in `events`, as the `external` arm. Nothing is stored for an event that reaches no task. `model.ExternalPayload` and the proto message gain `subject`, `namespace` and `emitter`; `source` takes the new values `task`, `system` and `schedule`. `get_my_task` already returns the `instruction` and `external` arms, so agents see bus events with no change to the brief.
- **`emit` attributes.** `emit` takes `attrs` as a flat string map. They are matchable (`Attrs`) and are also copied into `details`, so the receiving agent reads them. `body` and `subject` are derived attributes for every source, as `body` and `url` are today.
- **System events.**
  - `workflow.started` is published on a new instance's bus by `StartWorkflow` and by a trigger that starts an instance. It carries the attr `manual` (`"true"` or `"false"`), and on a manual start its body is the person's instruction.
  - `task.completed` and `task.failed` are published by `SubmitRunnerEvents` in the transaction that folds the task to `COMPLETED` or `FAILED`, on the task's instance bus, stamped with the task as emitter. `task.failed` carries the runner's reason as its body. A task that is woken and finishes again publishes `task.completed` again.

### Trust

`RoutingRule.Public` becomes `public` on a filter. It applies to `github` and `atlassian` only:

- The router resolves candidate orgs as `ListRoutingRulesForEvent` does today: orgs the actor is a member of (`Member = true`) and orgs in `InputEvent.Orgs` (`Member = false` unless also a member).
- A filter on `github` or `atlassian` matches an event with `Member = false` only when it sets `public: true`.
- `custom`, `schedule`, `task` and `system` events are produced by the org itself (its API key, its schedule, its tasks, the server), so they match without `public`. The "custom rules must be public" restriction is removed with the rules.

### Links

`create_link` takes `subject` in place of `url`, and an optional `type`:

```proto
message CreateLinkRequest {
  int64 task_id = 1;
  string relevance = 2;
  reserved 3;         // Previously: url
  string title = 4;
  bool subscribe = 5;
  reserved 6;
  string subject = 7; // The linked resource: a URL, or a subject an agent made up.
  string type = 8;    // The link type. Empty infers it from subject.
}

message TaskLink {
  // ... id, task_id, relevance, title, subscribe, created_at unchanged
  reserved 4, 8;      // Previously: url, routing_key
  string subject = 9; // Normalised by the server.
  string type = 10;
  string claim = 11;  // The claim the link holds; empty when not subscribed.
}
```

`CreateLink` resolves the link against the task's agent in the instance snapshot:

1. With no `type`, `model.ResolveSubject` infers it. A subject that isn't a URL has no inferred type, and the call is rejected with `InvalidArgument`.
2. With a built-in `type`, the subject is normalised by it and must be of that type.
3. `subscribe: false` accepts any built-in type and any type the agent declares. The link is a row and a `link` event, as today.
4. `subscribe: true` requires an entry for the type in the agent's `links`, otherwise `FailedPrecondition`. In one transaction the handler inserts the link with the entry's claim (`default` when omitted) and one `subscriptions` row per filter in the entry's `subscribe`, each with `subject` set to the link's subject.

The web UI renders a subject as a hyperlink when it is a URL.

**Default link types.** `orgs.default_links` holds a `links:` map in the same shape as an agent's. It is the `links` of every plain task's agent, and is edited as YAML on the settings page through `GetDefaultLinks` / `SetDefaultLinks`, validated like a workflow's. It starts empty: a plain task's links are references until the org adds entries.

### Publish

`Router.Publish` runs in one transaction per org:

1. **Load definitions.** For an instance-bus event, the instance snapshot. Otherwise every workflow of the org.
2. **Match triggers**, in definition order. A trigger matches when its filter matches and, if it names a built-in `link` type, the event's `LinkType` is that type. So a `label_added` trigger with `link: github.pr` ignores labels on issues, and a workflow that wants both declares two triggers.
3. **Check claims.** For each matched trigger with `link`, take `pg_advisory_xact_lock` on a hash of `(org, claim, subject)`, then look for a live holder. With a holder the trigger is held and does nothing. The lock closes the concurrent-duplicate window that `Router.create` documents as a v1 limitation. A start earlier in the same transaction counts as a holder, so two triggers that share a claim start one task between them.
4. **Find subscribers.** Select `subscriptions` by `(org_id, source, type, namespace)` joined to unarchived tasks, then filter in Go on `subject`, `agent`, `workflow`, `public` and `conditions`. A task is delivered an event once however many of its subscriptions match; it wakes if any matching subscription has `wake`.
5. **Start tasks** for the triggers that fire (below).
6. **Deliver** to the subscribers found in step 4, through today's `Router.attach`: an `external` event row, and `task.Start()` when waking. Tasks started in step 5 are not also delivered the event.
7. **Export.** For an instance-bus `task` event, each matching export publishes a copy with `Namespace` set to the destination, in the same transaction. Exports don't apply to the copy, so this is one level deep.

Starting a task replaces `Router.create`. For a trigger on an external source or a named namespace it first inserts a `workflow_instances` row. Then, as `create` does today, it inserts the task (`Runner`, `Workspace` and `AutoArchive` from the agent, `InstanceID`, `Agent`), the triggering `external` event, the `Created` lifecycle event, the command event, the link and its `link` event when the trigger has `link`, and an instruction event with the agent's `prompt`. It also inserts the agent's `subscribe` filters, and the link type's when there is a link, into `subscriptions`. For a new instance it then publishes `workflow.started` with `manual: "false"` on the new bus, which can start the instance's other agents.

`Publish` returns an `Outcome` listing the tasks started, the triggers held (with the holding task ids) and the tasks delivered to. `Route` calls `Publish` per candidate org and passes each `Outcome` to `OnRouteOutcome`; `githubserver`'s reaction is a rocket when the outcome started a task and eyes when it woke one.

### Limits

Loops are possible inside an instance, across workflows through named namespaces, and through an external system (a reviewer emits `pr.reviewed`, the implementer pushes, GitHub's event wakes the reviewer). The last kind loses any causal chain on the way through GitHub, so limits are budgets on what an event may cause rather than a hop count on the event:

| Limit | Default | When exceeded |
| --- | --- | --- |
| Tasks per instance | 50 | A trigger on the instance bus is held. |
| Instance starts per workflow | 60 per hour | A trigger on an external source or a named namespace is held. |
| Wakes per task from bus events | 30 per hour | The event is delivered with `wake = false`. |

The first two are counts over `tasks` and `workflow_instances`; the third is a count over the task's recent `external` rows with `wake`. All three are constants in `eventrouter`. A held trigger is reported in the `Outcome` with the limit as its reason and logged at warn level.

### Starting an instance

```proto
rpc ListWorkflows(ListWorkflowsRequest) returns (ListWorkflowsResponse);
rpc GetWorkflow(GetWorkflowRequest) returns (GetWorkflowResponse);
rpc PutWorkflow(PutWorkflowRequest) returns (PutWorkflowResponse);
rpc DeleteWorkflow(DeleteWorkflowRequest) returns (DeleteWorkflowResponse);
rpc StartWorkflow(StartWorkflowRequest) returns (StartWorkflowResponse);
rpc GetDefaultLinks(GetDefaultLinksRequest) returns (GetDefaultLinksResponse);
rpc SetDefaultLinks(SetDefaultLinksRequest) returns (SetDefaultLinksResponse);
rpc EmitEvent(EmitEventRequest) returns (EmitEventResponse);

// Workflow is a named set of agents, stored as YAML.
message Workflow {
  string name = 1;
  string source = 2;               // The YAML definition.
  repeated string agents = 3;      // Agent names, in definition order. Read-only.
  bool manual_start = 4;           // Whether an agent triggers on a manual workflow.started. Read-only.
  google.protobuf.Timestamp created_at = 5;
  google.protobuf.Timestamp updated_at = 6;
}

message PutWorkflowRequest {
  string source = 1;               // The workflow's name is the YAML's `workflow` key.
}

message StartWorkflowRequest {
  string name = 1;
  string instruction = 2;          // The body of workflow.started.
}

message StartWorkflowResponse {
  int64 instance_id = 1;
  repeated Task tasks = 2;         // The tasks the start created.
}

message EmitEventRequest {
  int64 task_id = 1;
  string type = 2;
  string subject = 3;
  string body = 4;
  map<string, string> attrs = 5;
}

message EmitEventResponse {
  repeated int64 started_task_ids = 1;
  repeated int64 delivered_task_ids = 2;
}
```

- `StartWorkflow` (`OpTaskCreate`) inserts an instance and publishes `workflow.started` with `manual: "true"` and the instruction as its body. Each agent with a matching trigger starts, with that event as its first event. A workflow with no such trigger is rejected with `FailedPrecondition`.
- `PutWorkflow` and `DeleteWorkflow` need `OpOrgWrite`; the reads need `OpOrgRead`. These are the scopes `SetRoutingRules` and `GetRoutingRules` use.
- `EmitEvent` needs `OpTaskWrite` on the task, the check `CreateLink` does. The server sets the source to `task`, the namespace to the task's instance bus and the emitter from the task row. The caller can set neither.
- `Task` gains `instance_id`, `agent`, `workflow` and `capabilities`, and field 17 (`namespace`) is reserved. `CreateTaskRequest.namespace` is reserved.
- `GetRoutingRules`, `SetRoutingRules`, `RoutingRule`, `RuleCondition` and `CreateTaskAction` are deleted.

`TestEvent` runs `Router.Plan` for a dry run and `Router.Publish` for a fire. Its response replaces `TestEventMatch` with the plan:

```proto
message TestEventResponse {
  bool fired = 2;
  reserved 1;                              // Previously: matches
  repeated TestEventStart starts = 3;      // Triggers that would start, or started, a task.
  repeated TestEventHeld held = 4;         // Triggers held back.
  repeated TestEventDelivery deliveries = 5;
}

message TestEventStart {
  string workflow = 1;
  string agent = 2;
  int64 task_id = 3;                       // Set only when fired.
}

message TestEventHeld {
  string workflow = 1;
  string agent = 2;
  string reason = 3;                       // "claim" or the limit's name.
  repeated int64 holder_task_ids = 4;      // The live tasks holding the claim.
}

message TestEventDelivery {
  int64 task_id = 1;
  bool wake = 2;
  int64 event_id = 3;                      // Set only when fired.
}
```

A dry run reads claims and subscriptions, so unlike today it reports what would happen to the tasks that exist, not only what a rule says. `TestEventRequest` accepts `source: schedule` with a type and no URL.

### Agent tools

`internal/runner/agentmcp` changes:

- `create_link`: `url` becomes `subject`, and `type` is added.
- `emit` is added, calling `EmitEvent` with `type`, `subject`, `body` and `attrs`.
- `get_my_task` returns `agent`, `workflow` and `instance_id` in place of `namespace`.
- With the `task_tools` capability, three read-only tools are added: `list_workflow_tasks` (`ListTasks` with a new `instance_id` filter), `get_workflow_task` (the task, its links and its `instruction`, `external` and `report` events) and `get_workflow_task_log` (`ListLogChunksByTask`).

`task_tools` is declared on the agent in the workflow, not on the workspace, so the server grants it:

- `Task.capabilities` is derived from the task's agent in the instance snapshot. The runner appends it to the `--capability` flags it already builds from `workspace.Capabilities` in `internal/runner/spec.go`.
- `CreateTaskToken` adds the scope itself from the task row. `agentauth.Scopes` gains `task.read` constrained to a new `task.instance` attribute, and `Task.ScopeAttr` reports the task's instance id. The existing post-load `Allow` checks in `GetTask`, `ListLinks`, `ListEventsByTask` and `ListLogChunksByTask` then admit a sibling task with no handler change. `ListTasks` authorises a request with `instance_id` against the same attribute.

### Schedules

```sql
-- migrate:up
ALTER TABLE schedules ADD COLUMN event_type TEXT NOT NULL DEFAULT '';
UPDATE schedules SET event_type = 'schedule_' || id;
ALTER TABLE schedules
    DROP COLUMN workspace,
    DROP COLUMN runner,
    DROP COLUMN namespace,
    DROP COLUMN instructions,
    DROP COLUMN auto_archive;
```

`Schedule`, `CreateScheduleRequest` and `UpdateScheduleRequest` lose the same five fields and gain `event_type`. `scheduler.Fire` no longer creates a task: it publishes `{Source: "schedule", Type: sched.EventType}` through the router in the tick's transaction. `RunSchedule` publishes the same event. `last_task_id` is set to the first task the publish started and cleared when it started none, so the schedules list shows a tick that matched no trigger.

### CLI, MCP and web UI

- **CLI** (`internal/command`): `gritz workflow list`, `get <name>`, `apply -f <file>`, `delete <name>` and `start <name> [-m <instruction>]`. `gritz task create` loses `--namespace`.
- **Server MCP** (`internal/server/mcpserver`): `list_workflows`, `get_workflow`, `put_workflow`, `delete_workflow` and `start_workflow` are added. `get_routing_rules` and `set_routing_rules` are removed. The schedule tools take `event_type` in place of the template fields. `test_event` returns the new plan.
- **Web UI** (`webui/src/routes`): a **Workflows** page lists workflows; a workflow's page has a YAML editor, the **Start** button with an instruction field, and the workflow's recent instances with their tasks. **Events → Routing Rules** and `routing.new` / `routing.$index` are removed; `routing.testevent` moves under Events and renders starts, held triggers and deliveries. The task page shows the workflow, agent and instance in place of the namespace, and lists the instance's other tasks. The new task and schedule forms lose their template and namespace fields; the settings page gains the default link types editor.

### API

```go
package model // github.com/gritzapp/gritz/internal/model

// Workflow is a parsed workflow definition. // new
type Workflow struct {
	Name   string                   `json:"workflow"`
	Export []Export                 `json:"export,omitempty"`
	Agents map[string]WorkflowAgent `json:"agents"`
}

// WorkflowAgent is a task template with its triggers, subscriptions and link types. // new
type WorkflowAgent struct {
	Workspace    string              `json:"workspace"`
	Runner       string              `json:"runner"`
	Prompt       string              `json:"prompt,omitempty"`
	AutoArchive  time.Duration       `json:"auto_archive,omitempty"`
	Capabilities []string            `json:"capabilities,omitempty"`
	Triggers     []Trigger           `json:"triggers,omitempty"`
	Subscribe    []Subscription      `json:"subscribe,omitempty"`
	Links        map[string]LinkType `json:"links,omitempty"`
}

// Filter selects events by bus, source, type, emitter and attribute conditions. // new
type Filter struct {
	Namespace  string      `json:"namespace,omitempty"`
	Source     string      `json:"source"`
	Type       string      `json:"type"`
	Agent      string      `json:"agent,omitempty"`
	Workflow   string      `json:"workflow,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
	Public     bool        `json:"public,omitempty"`
}

// Trigger is a filter that starts a task, optionally linking the event's subject. // new
type Trigger struct {
	Filter
	Link string `json:"link,omitempty"`
}

// Subscription is a filter whose events are delivered to a task. // new
type Subscription struct {
	Filter
	Wake bool `json:"wake"`
}

// LinkType is what a subscribed link of one type gives its task. // new
type LinkType struct {
	Claim     string         `json:"claim"`
	Subscribe []Subscription `json:"subscribe,omitempty"`
}

// Export republishes matching instance-bus events on the named namespace To,
// which is the YAML entry's `namespace` key. // new
type Export struct {
	Filter
	To string `json:"to"`
}

// Emitter identifies the task an event came from or is about. // new
type Emitter struct {
	TaskID   int64  `json:"task_id"`
	Agent    string `json:"agent"`
	Workflow string `json:"workflow,omitempty"`
}

// WorkflowInstance is one run of a workflow and the bus its tasks share. // new
type WorkflowInstance struct{ ... }

// TaskSubscription is one subscriptions row: a filter bound to a task and, for a link's, a subject. // new
type TaskSubscription struct{ ... }

// ParseWorkflow decodes a YAML workflow definition. // new
func ParseWorkflow(source string) (*Workflow, error)

// Bus returns the namespace of the instance's own bus. // new
func (i *WorkflowInstance) Bus() string

// ResolveSubject normalises a subject and reports its built-in link type. // new
func ResolveSubject(raw string) (linkType, subject string)

// RoutingKey: removed
// RoutingRule, CreateTaskAction: removed
```

```go
package eventrouter // github.com/gritzapp/gritz/internal/server/eventrouter

// Event is one event published in one org. // new
type Event struct{ ... }

// Plan is what publishing an event would do. // new
type Plan struct {
	Starts     []Start
	Held       []Held
	Deliveries []Delivery
}

// Outcome is what publishing an event did. // new
type Outcome struct {
	Event   Event
	Plan    Plan
	TaskIDs []int64 // tasks started
}

// Plan evaluates triggers, claims and subscriptions for ev without writing. // changed
func (r *Router) Plan(ctx context.Context, tx *sql.Tx, ev Event) (*Plan, error)

// Publish starts tasks for and delivers ev inside tx. // new
func (r *Router) Publish(ctx context.Context, tx *sql.Tx, ev Event) (*Outcome, error)

// Route publishes an external event in every org it belongs to. // changed
func (r *Router) Route(ctx context.Context, input InputEvent) ([]Outcome, error)

// MatchFilter reports whether f matches ev. // new
func MatchFilter(f model.Filter, ev Event) bool

// ValidateWorkflow checks a definition against the registry. // new
func (r *SchemaRegistry) ValidateWorkflow(w *model.Workflow) error

// Apply, Match, RouteMatch, RouteOutcome, SchemaRegistry.Validate: removed
```

```go
package store // github.com/gritzapp/gritz/internal/store

// PutWorkflow inserts or replaces the org's workflow of that name. // new
func (s *Store) PutWorkflow(ctx context.Context, tx *sql.Tx, orgID int64, w *model.Workflow, source string) error

// GetWorkflow returns the org's workflow of that name and its YAML source. // new
func (s *Store) GetWorkflow(ctx context.Context, tx *sql.Tx, orgID int64, name string) (*model.Workflow, string, error)

// ListWorkflows returns the org's workflows, by name. // new
func (s *Store) ListWorkflows(ctx context.Context, tx *sql.Tx, orgID int64) ([]*model.Workflow, error)

// DeleteWorkflow deletes the org's workflow of that name. // new
func (s *Store) DeleteWorkflow(ctx context.Context, tx *sql.Tx, orgID int64, name string) error

// CreateWorkflowInstance inserts inst and sets its ID. // new
func (s *Store) CreateWorkflowInstance(ctx context.Context, tx *sql.Tx, inst *model.WorkflowInstance) error

// GetWorkflowInstance returns the org's instance. // new
func (s *Store) GetWorkflowInstance(ctx context.Context, tx *sql.Tx, id, orgID int64) (*model.WorkflowInstance, error)

// CreateSubscriptions inserts a task's subscriptions. // new
func (s *Store) CreateSubscriptions(ctx context.Context, tx *sql.Tx, subs []*model.TaskSubscription) error

// FindSubscriptions returns live tasks' subscriptions for a bus, source and type. // new
func (s *Store) FindSubscriptions(ctx context.Context, tx *sql.Tx, orgID int64, namespace, source, typ string) ([]*model.TaskSubscription, error)

// FindClaimHolders returns the live tasks holding claim on subject. // new
func (s *Store) FindClaimHolders(ctx context.Context, tx *sql.Tx, orgID int64, claim, subject string) ([]int64, error)

// LockClaim serialises triggers on one claim and subject for the transaction. // new
func (s *Store) LockClaim(ctx context.Context, tx *sql.Tx, orgID int64, claim, subject string) error

// FindSubscribedLinksForOrgs, ListRoutingRulesForEvent, GetOrgRoutingRules, SetOrgRoutingRules: removed
```

**How it fits:** `githubserver`, `atlassianserver` and `customserver` build an `InputEvent` and call `Router.Route`, as today. `apiserver` calls `Router.Publish` directly from `EmitEvent` (a `task` event), `StartWorkflow` (`workflow.started`), `SubmitRunnerEvents` (`task.completed`, `task.failed`) and `TestEvent`; `scheduler.Fire` calls it for `schedule` events. `Publish` reads definitions and claims through the store, and writes tasks, events, links and subscriptions with the store methods `Router.create` and `Router.attach` already use. `CreateLink` and the router's task start both expand a link type into `subscriptions` rows through one helper in `eventrouter`.

## Implementation Plan

Each slice updates the docs pages its change makes wrong, per the table in `CLAUDE.md`.

1. **Workflow model and parser.** Delivers: `model.Workflow` and its parts, `ParseWorkflow`, `MatchFilter`, `SchemaRegistry.ValidateWorkflow`. Depends on: nothing. Verifiable by: unit tests parsing the issue's four examples and rejecting each invalid key.
2. **Schema.** Delivers: the `workflows`, `workflow_instances` and `subscriptions` tables, `tasks.instance_id` and `tasks.agent`, the `task_links` columns, and their store methods. Depends on: nothing. Verifiable by: the migration running up and down against a copy of production data; store tests.
3. **Every task has an instance.** Delivers: `CreateTask`, `Router.create` and `scheduler.Fire` insert an instance for the task; `Task.instance_id` and `Task.agent` on the wire. Depends on: (2). Verifiable by: e2e tests asserting the instance on each creation path.
4. **Workflow CRUD.** Delivers: `ListWorkflows`, `GetWorkflow`, `PutWorkflow`, `DeleteWorkflow`, `gritz workflow list/get/apply/delete`, the server MCP tools, and the Workflows page with its editor. Definitions are stored and validated but nothing reads them. Depends on: (1), (2). Verifiable by: applying the issue's examples and reading them back.
5. **Typed links.** Delivers: `model.ResolveSubject` replacing `RoutingKey`, `CreateLinkRequest.subject` and `type`, the `create_link` tool change, link rendering in the web UI. Links don't expand to subscriptions yet; routing still reads `task_links.subject` as the key. Depends on: (2). Verifiable by: unit tests for inference and rejection; an e2e task linking a pull request and being woken by a rule.
6. **Publish, unused.** Delivers: `Router.Plan` and `Router.Publish` over triggers, claims, subscriptions and limits, with the link type expansion helper, beside the existing `Route`. No production caller. Depends on: (1), (2), (3), (5). Verifiable by: router tests covering each example in the issue, held triggers, shared claims and each limit.
7. **Cutover.** Delivers: `Route`, `TestEvent` and `CreateLink` switch to `Publish` and the expansion helper; routing rules, `tasks.namespace`, their RPCs, MCP tools and UI routes are deleted; `orgs.default_links` and its editor are added; the test event form renders the plan. Before it merges, our org's rules are rewritten as workflows by hand. Tasks live at the cutover keep their links as references and have no subscriptions. Depends on: (4), (6). Verifiable by: e2e tests for the "mention the bot" and reviewer examples against GitHub webhooks.
8. **Emit, system events and exports.** Delivers: `EmitEvent` and the `emit` tool, `task.completed` and `task.failed` from `SubmitRunnerEvents`, export republishing. Depends on: (7). Verifiable by: an e2e test of the fan-out example with a planner and two implementers.
9. **Manual start.** Delivers: `StartWorkflow`, `gritz workflow start`, `start_workflow`, the Start button, and the instance list on the workflow page. Depends on: (7). Verifiable by: starting the fan-out example by hand and seeing `workflow.started` as the planner's first event.
10. **Schedules emit events.** Delivers: the schedule migration, `scheduler.Fire` publishing, the reduced schedule API, form and MCP tools. Before it merges, our schedules are paired with workflows by hand. Depends on: (7). Verifiable by: scheduler tests and the nightly example.
11. **Task tools.** Delivers: `Task.capabilities`, the `task.instance` scope attribute, `ListTasks.instance_id`, and the three read-only agent tools. Depends on: (7). Verifiable by: scope tests; an e2e planner reading a failed implementer's log.

## Trade-offs

- **Budgets instead of a hop count.** A hop count stamped on events needs every emitted event to name the event that caused it, and a task that has received several events has no single cause. A loop that passes through GitHub loses the count entirely. Budgets on starts and wakes bound every loop shape without tracking causality. The cost is that a legitimate burst (a planner emitting 60 items) hits the same limit as a loop.
- **No table of published events.** Storing every published event once, with its deliveries, would answer "what did this event do" after the fact, including for events that reached no one. It also stores every webhook an org receives and adds a second event table beside `events`. This design keeps today's rule (an event is stored on the tasks it reaches) and puts the full plan in the test-event report instead.
- **Materialised subscriptions instead of matching definitions at publish time.** Evaluating every live task's agent definition per event would need each live instance's snapshot loaded for every external event. Rows written at the two moments subscriptions can change make the lookup one indexed query, at the cost of a table that duplicates what the snapshot implies.
- **Snapshot per instance instead of a versioned workflow table.** A `workflow_versions` table referenced by instances would store each definition once. The snapshot costs a copy of the spec per instance, including one per plain task, and needs no version bookkeeping or garbage collection.
- **YAML as the stored and edited form.** The API carries the definition as text, as the server-managed workspaces draft does for workspace configs, so the proto doesn't mirror the workflow shape and the CLI, MCP tool and editor all handle one format. The web UI gives up the structured rule form.
- **The `external` event arm is reused for `task`, `system` and `schedule` events.** Renaming the arm and the `events.type` discriminator would touch the store, the runner's event replica and the web UI timeline for no change in behaviour.
- **`public` stays a filter key rather than becoming a matchable attribute.** An attribute would let `filter:` express it, but the default (members only) is not a condition a filter writes, so it would be an implicit condition on an attribute. A key with a default says the same thing directly.
- **An agent starts at most once per event.** The issue's rule is that every matching trigger starts a task. Applied within one agent, two overlapping triggers would start two identical tasks; treating an agent's triggers as alternatives removes that case without affecting two agents matching the same event.

## Open Questions

1. **Subjects that agents make up are org-wide.** A claim is keyed by `(org, claim, subject)`. Two instances of one workflow whose planners both emit `slice-1` share the claim, so the second instance's implementer is held. The issue asks agents for unique subjects; the alternative is to scope claims on link types that aren't built in to the instance, which breaks de-duplication for work items exported to a named namespace.
2. **Limit values and configuration.** The three defaults are guesses, and they are constants. Should a workflow be able to set its own, and should a held trigger or a throttled wake be surfaced to anyone (a `system` event to the instance, a row on the workflow page) rather than only logged?
3. **Events that reach no one.** A trigger held by a claim delivers nothing, and nothing is stored. Is the test-event report enough, or does this need the published-events table described in Trade-offs?
4. **The default claim is shared.** `default` is held by, and holds back, every agent in the org that omits `claim`. The design keeps the issue's rule.
5. **Tasks live at the cutover.** They lose their subscriptions, because nothing maps a rule-driven link onto a link type. Is that acceptable for our deployment, or should the cutover migration expand existing subscribed links against `orgs.default_links`?
6. **Filtering on a task ID.** `Emitter.TaskID` is stored and shown to the agent, but `Filter` has no key for it.
7. **Contracts for exported types.** `task` event types have no schema, so a filter on one is not validated and a typo matches nothing. `orgs.custom_event_types` could hold schemas for types on named namespaces.
8. **Negated conditions.** With no rule order, two workflows that must not both start need a shared claim or mutually exclusive conditions, and the condition language has no `not`.
9. **One form for simple cases.** "Run this prompt nightly" is a schedule and a one-agent workflow. The schedule form could create both.
10. **Naming.** "Agent" also names `internal/runner/agent` (the CLI integrations); this design uses `model.WorkflowAgent` in Go and `agent` in YAML and on the wire. The `manual` attribute of `workflow.started`, the `task_tools` capability and the `i/<id>` bus name are placeholders taken from the issue's examples.
11. **Instances have no end.** An instance is its tasks; nothing marks it finished, and its row is deleted only with its org. Should an instance be archived when all of its tasks are?