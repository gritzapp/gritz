# Event Workflows

Issue: https://github.com/gritzapp/gritz/issues/2203

## Problem

The issue describes a coordination model: a workflow declares triggers and agents, an agent declares subscriptions and link types, and tasks talk to each other by emitting events on a bus. It is a model, not a design. This proposal is the design: what is stored, what the API looks like, and how the router changes.

What the model replaces:

- A routing rule (`model.RoutingRule`) does three jobs: admission (`Public`, conditions), delivery policy (`Wakeup`) and spawning (`Create`). `Router.Plan` picks the first matching rule per org, so two workflows that care about the same event can't both act.
- What a subscribed link delivers is decided by whichever rule matched, not by the task that created the link. A link is only a URL (`task_links.routing_key`), and it only works when some rule matches the event first.
- A namespace (`tasks.namespace`, `RoutingRule.Namespace`) is a filter that `Router.Apply` applies to the link lookup. Nothing is published to it.
- Agents have four tools (`get_my_task`, `update_my_task`, `create_link`, `report`). None of them publishes anything another task can receive.
- `schedules` carries its own copy of the task template (`workspace`, `runner`, `namespace`, `instructions`, `auto_archive`) that `CreateTaskAction` also carries.

## Design

### Overview

- A **workflow** is a named YAML definition stored per org. It declares triggers, agents and exports. An agent is a task template plus `subscribe` and `links`.
- A **workflow instance** is one run of a workflow. Every task belongs to exactly one instance and records which agent of the instance it is. A plain task (`CreateTask`) is an instance of an anonymous one-agent workflow.
- The instance is the task's event bus. Agents publish to it with a new `emit` tool; the server publishes `system` events to it.
- A **trigger** decides when an instance starts. Every trigger match publishes a `system` event `workflow.trigger` on the instance's bus. Agents start by subscribing to it, or to any other event.
- **`start`** on a trigger or a subscription says whether a matching event may create an instance or a task, and which existing one handles it otherwise.
- `eventrouter.Router` is rewritten around one operation, `Publish`: resolve instances for the triggers that match, then start tasks for and deliver to the subscriptions that match. There is no rule order and no first match.
- Subscriptions are materialised in a `subscriptions` table when an instance starts and when a task gets a subscribed link.
- An instance is open until a person closes it. A closed instance receives no events.
- Routing rules, `tasks.namespace` and the schedule's task template are removed.

Answers to the two questions on the issue:

- **Starting a workflow by hand** is a `StartWorkflow` RPC. It is exposed as a `start_workflow` tool on the server MCP (so `mcp__gritz__start_workflow` in a client), as `gritz workflow start`, and as a **Start** button on the workflow's page. See [Starting and closing an instance](#starting-and-closing-an-instance).
- **Two agents whose triggers match the same external event** no longer arises. Triggers belong to the workflow, and an event that matches resolves to an instance of it; which agents start there is decided by their subscriptions on that instance's bus.

### Workflow definition

The complete set of keys:

```yaml
workflow: feature            # name, unique in the org
export:                      # optional
  - type: pr.reviewed        # filter on the instance bus (source is always task)
    namespace: reviews       # destination named namespace
triggers:                    # optional
  - source: github           # a filter, plus:
    type: label_added
    filter:
      label: build-it
    start: unique            # optional, default never
agents:
  implementer:
    workspace: default
    runner: main
    prompt: Implement the item.
    auto_archive: 24h        # optional, as Task.auto_archive
    capabilities:            # optional
      - task_tools
    subscribe:
      - type: work.item      # a filter, plus:
        start: unique        # optional, default never
        link: work.item      # optional link type
        wake: false          # optional, default true
    links:
      github.pr:
        subscribe:
          - source: github   # a filter, plus:
            type: pull_request_review_comment
            wake: false      # optional, default true
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

- Workflow, agent and namespace names match `^[a-z][a-z0-9_-]{0,63}$`. Event types emitted by agents and link type names match `^[a-z][a-z0-9_.]{0,63}$`.
- A filter on `github`, `atlassian` or `custom` must name a `(source, type)` registered in `eventrouter.DefaultSchemaRegistry.With(orgCustomTypes...)`, and each condition's attr must be one the type declares. This is `SchemaRegistry.Validate` applied to filters instead of rules.
- A filter on `system` must name `workflow.trigger`, `task.completed` or `task.failed`.
- A filter on `task` has no schema; its type and conditions are not checked, on the instance bus or on a named namespace.
- A trigger's filter is on `github`, `atlassian`, `custom` or `schedule`, or on `task` with a `namespace`. It cannot be on the instance bus or on `system`.
- `start` is `never`, `once`, `unique` or `always`.
- A subscription's `link` must name an entry in the agent's `links`.
- An export has no `source` other than `task` and must name a `namespace`.
- `capabilities` entries must be `task_tools`.

`SetCustomEventTypes` re-validates every stored workflow of the org under `LockOrg`, as it re-validates routing rules today. `PutWorkflow` takes the same lock.

### Start

`start` has the same four values on a trigger and on a subscription. On a trigger the thing started is an instance; on a subscription it is a task.

| `start` | Trigger | Subscription |
| --- | --- | --- |
| `never` | The open instance of the workflow keyed by the event's subject, if any. | Every live task of the agent in the instance. |
| `once` | The workflow's open instance with the empty key; create it if there is none. | The agent's live task with the empty key; start it if there is none. |
| `unique` | The open instance keyed by the event's subject; create it if there is none. | The agent's live task keyed by the event's subject; start it if there is none. |
| `always` | A new instance with no key. | A new task with no key. |

- An instance's key is `(org, workflow, key)` and is shared by all of the workflow's triggers. A task's key is `(instance, agent, key)` and is shared by all of the agent's subscriptions. Two `unique` entries on one agent that match events with the same subject reach the same task.
- `once` uses the empty string as its key. An event with no subject has the empty string as its subject, so `unique` on such an event behaves as `once`, and `never` on a trigger finds the `once` instance.
- `always` stores no key (`NULL`), so no later lookup finds what it started.
- An instance is live while it is not closed. A task is live while it is not archived. A lookup that finds nothing live starts a replacement (`once`, `unique`) or drops the event (`never`). A dropped event is not an error and is not recorded.
- A lookup and the start that follows it are serialised by `pg_advisory_xact_lock` on a hash of the key, which closes the concurrent-duplicate window that `Router.create` documents as a v1 limitation. Unarchiving a task whose key has since been reused leaves two live tasks with one key; a lookup returns both and both receive the event.

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
    start_key TEXT,                      -- NULL: started by `always`, by hand, or a plain task
    spec JSONB NOT NULL,                 -- snapshot of model.Workflow at start
    closed_at TIMESTAMP,                 -- NULL: open
    created_at TIMESTAMP NOT NULL DEFAULT (now() AT TIME ZONE 'UTC')
);
CREATE INDEX idx_workflow_instances_org_workflow ON workflow_instances (org_id, workflow, id DESC);
CREATE INDEX idx_workflow_instances_start_key ON workflow_instances (org_id, workflow, start_key)
    WHERE closed_at IS NULL AND start_key IS NOT NULL;

ALTER TABLE tasks ADD COLUMN instance_id BIGINT REFERENCES workflow_instances(id) ON DELETE CASCADE;
ALTER TABLE tasks ADD COLUMN agent TEXT NOT NULL DEFAULT 'task';
ALTER TABLE tasks ADD COLUMN start_key TEXT;   -- NULL: started by `always`, or a plain task
INSERT INTO workflow_instances (id, org_id, spec) SELECT id, org_id, '{}' FROM tasks;
SELECT setval('workflow_instances_id_seq', (SELECT COALESCE(MAX(id), 1) FROM workflow_instances));
UPDATE tasks SET instance_id = id;
ALTER TABLE tasks ALTER COLUMN instance_id SET NOT NULL;
CREATE INDEX idx_tasks_instance_agent ON tasks (instance_id, agent, start_key) WHERE archived = FALSE;

ALTER TABLE task_links ADD COLUMN type TEXT NOT NULL DEFAULT 'url';
ALTER TABLE task_links RENAME COLUMN routing_key TO subject;
ALTER INDEX idx_task_links_routing_key RENAME TO idx_task_links_subject;

CREATE TABLE subscriptions (
    id BIGSERIAL PRIMARY KEY,
    org_id BIGINT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    instance_id BIGINT NOT NULL REFERENCES workflow_instances(id) ON DELETE CASCADE,
    agent TEXT NOT NULL,                 -- the subscribing agent
    link_id BIGINT REFERENCES task_links(id) ON DELETE CASCADE,  -- NULL: an entry of the agent's subscribe list
    namespace TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL,
    type TEXT NOT NULL,
    subject TEXT NOT NULL DEFAULT '',    -- link subscriptions: the link's subject
    from_agent TEXT NOT NULL DEFAULT '',     -- the filter's `agent`
    from_workflow TEXT NOT NULL DEFAULT '',  -- the filter's `workflow`
    conditions JSONB NOT NULL DEFAULT '[]',
    public BOOLEAN NOT NULL DEFAULT FALSE,
    wake BOOLEAN NOT NULL DEFAULT TRUE,
    start TEXT NOT NULL DEFAULT 'never',     -- agent entries only
    link_type TEXT NOT NULL DEFAULT ''       -- agent entries only: the entry's `link`
);
CREATE INDEX idx_subscriptions_lookup ON subscriptions (org_id, source, type, namespace);
CREATE INDEX idx_subscriptions_instance_id ON subscriptions (instance_id);
```

Decisions behind this:

- **The instance snapshots the definition.** `workflow_instances.spec` is a copy of `workflows.spec` taken when the instance starts. The instance's subscriptions, link types, exports, task templates and capabilities all come from the snapshot. Editing or deleting a workflow affects instances started afterwards. Triggers are the exception: they are read from `workflows.spec`, since they decide which instance an event goes to.
- **The instance bus is the instance id.** Namespaces are one text column. An instance bus is written `i/<instance id>`, which the namespace name pattern can't produce, and external events use `''`.
- **Subscriptions are rows, triggers are not.** There are two kinds of row:
  - One per entry of each agent's `subscribe` list, written when the instance is created and never changed. It belongs to the instance and the agent, not to a task, because the entry has to match when the agent has no task yet.
  - One per filter of a link type, written when a task gets a subscribed link of that type, with `subject` set. It belongs to the link, and through it to the task.

  Both are looked up by index at publish time. Triggers belong to definitions, and an org has few workflows, so `Publish` loads the org's `workflows.spec` rows and matches triggers in Go, as `Plan` does with `orgs.routing_rules` today.
- **Liveness is a join.** The subscription lookup joins `workflow_instances` on `closed_at IS NULL`, and link subscriptions also join `task_links` and `tasks` on `archived = FALSE`. Closing an instance or archiving a task ends its subscriptions without rewriting rows, and unarchiving restores a link's.
- **Keys are columns.** `workflow_instances.start_key` and `tasks.start_key` hold the key a `once` or `unique` start used. Nothing else de-duplicates: links carry no claim.
- **Plain tasks.** `CreateTask` and a draft task create an instance with `workflow = ''`, no key and a one-agent spec: the agent is named `task`, its `workspace` and `runner` come from the request, it has no `subscribe` entries, and its `links` are the org's default link types (below).

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
  - `workflow.trigger` is published on an instance's bus each time one of the workflow's triggers resolves an event to that instance, and by `StartWorkflow`. From a trigger it carries the matched event's `Subject`, `LinkType`, `Description`, `Body`, `URL`, `Details` and `Attrs`, plus the attrs `trigger_source` and `trigger_type` (the matched event's source and type) and `manual: "false"`. From `StartWorkflow` it has no subject, its body is the person's instruction, and its only attr is `manual: "true"`.
  - `task.completed` and `task.failed` are published by `SubmitRunnerEvents` in the transaction that folds the task to `COMPLETED` or `FAILED`, on the task's instance bus, stamped with the task as emitter. `task.failed` carries the runner's reason as its body. A task that is woken and finishes again publishes `task.completed` again.

### Trust

`RoutingRule.Public` becomes `public` on a filter. It applies to `github` and `atlassian` only:

- The router resolves candidate orgs as `ListRoutingRulesForEvent` does today: orgs the actor is a member of (`Member = true`) and orgs in `InputEvent.Orgs` (`Member = false` unless also a member).
- A filter on `github` or `atlassian`, in a trigger, a subscription or a link type, matches an event with `Member = false` only when it sets `public: true`.
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
}
```

`CreateLink` resolves the link against the task's agent in the instance snapshot:

1. With no `type`, `model.ResolveSubject` infers it: a built-in type, or `url` for any other URL. A subject that isn't a URL has no inferred type, and the call is rejected with `InvalidArgument`.
2. With a built-in `type`, the subject is normalised by it and must be of that type. With a type the agent declares, the subject is stored as given.
3. `subscribe: false` accepts any built-in type, `url`, and any type the agent declares. The link is a row and a `link` event, as today.
4. `subscribe: true` requires an entry for the type in the agent's `links`, otherwise `FailedPrecondition`. In one transaction the handler inserts the link and one `subscriptions` row per filter in the entry's `subscribe`, each with `subject` set to the link's subject.

A subscription with `link: <type>` creates the same link from the router. Every task that receives an event through that entry, whether the entry started it or it was already running, gets a subscribed link of that type to the event's subject, unless it already holds one. `link` is best-effort and never affects whether the entry matches:

- A built-in type fits when the event's `LinkType` is that type.
- A type the agent declares fits any non-empty subject.
- When the subject doesn't fit, or the event has none, no link is created.

An entry names one link type. An agent that wants either of two kinds of resource linked writes one entry per type; with `start: once` or `unique` they resolve to the same task, and the entry whose type fits creates the link.

The web UI renders a subject as a hyperlink when it is a URL.

**Default link types.** `orgs.default_links` holds a `links:` map in the same shape as an agent's. It is the `links` of every plain task's agent, and is edited as YAML on the settings page through `GetDefaultLinks` / `SetDefaultLinks`, validated like a workflow's. It starts empty: a plain task's links are references until the org adds entries.

### Publish

`Router.Publish` runs in one transaction per org. Publishing on the bus of a closed instance does nothing.

1. **Match triggers.** Skipped for an event on an instance bus. Otherwise load every workflow of the org and match each one's triggers in definition order.
2. **Resolve instances.** Each matching trigger resolves an instance according to its `start` (see [Start](#start)), taking the key lock first for `once` and `unique`. An instance created earlier in the transaction is found by later lookups, so two triggers of one workflow that resolve to the same key give one instance. Creating an instance inserts the `workflow_instances` row with the snapshot and key, and one `subscriptions` row per `subscribe` entry of each agent.
3. **Publish `workflow.trigger`.** For each distinct instance resolved in step 2, publish `workflow.trigger` on its bus through a nested `Publish` in the same transaction. Several triggers resolving one event to one instance give one `workflow.trigger`.
4. **Find subscriptions.** Select `subscriptions` by `(org_id, source, type, namespace)` joined to open instances, then filter in Go on `subject`, `from_agent`, `from_workflow`, `public` and `conditions`. An instance created in step 2 is included, so its agents' subscriptions on the event itself match too.
5. **Resolve tasks.** A link subscription's task is the one holding the link. An agent entry's tasks are resolved by its `start`, per instance, taking the key lock first for `once` and `unique`. A task started earlier in the transaction is found by later lookups.
6. **Start tasks** for the entries that resolved to a start (below).
7. **Deliver** to the remaining resolved tasks through today's `Router.attach`: an `external` event row, and `task.Start()` when waking. A task is delivered an event once however many subscriptions resolved to it; it wakes if any of them has `wake`. A task started in step 6 is not also delivered the event.
8. **Link.** For each agent entry with `link`, each task it resolved to gets the link, as described under [Links](#links).
9. **Export.** For a `task` event on an instance bus, each matching export in the instance snapshot publishes a copy with `Namespace` set to the destination, through a nested `Publish`. Exports don't apply to the copy, so this is one level deep.

Starting a task replaces `Router.create`. As `create` does today, it inserts the task (`Runner`, `Workspace` and `AutoArchive` from the agent in the snapshot, plus `InstanceID`, `Agent` and `StartKey`), the triggering `external` event, the `Created` lifecycle event, the command event, and an instruction event with the agent's `prompt`.

`Publish` returns an `Outcome` listing the instances resolved (and whether each was created), the tasks started and the tasks delivered to. `Route` calls `Publish` per candidate org and passes each `Outcome` to `OnRouteOutcome`; `githubserver`'s reaction is a rocket when the outcome started a task and eyes when it woke one.

There are no limits on what an event may cause: no hop count, no budget on starts or wakes. Nested publishes within one transaction are bounded by the definitions (a trigger publishes `workflow.trigger`, an export publishes one copy). A loop across transactions, through `emit` or through GitHub, runs until a person closes the instance or cancels or archives the tasks involved.

### Starting and closing an instance

```proto
rpc ListWorkflows(ListWorkflowsRequest) returns (ListWorkflowsResponse);
rpc GetWorkflow(GetWorkflowRequest) returns (GetWorkflowResponse);
rpc PutWorkflow(PutWorkflowRequest) returns (PutWorkflowResponse);
rpc DeleteWorkflow(DeleteWorkflowRequest) returns (DeleteWorkflowResponse);
rpc StartWorkflow(StartWorkflowRequest) returns (StartWorkflowResponse);
rpc ListWorkflowInstances(ListWorkflowInstancesRequest) returns (ListWorkflowInstancesResponse);
rpc CloseWorkflowInstance(CloseWorkflowInstanceRequest) returns (CloseWorkflowInstanceResponse);
rpc GetDefaultLinks(GetDefaultLinksRequest) returns (GetDefaultLinksResponse);
rpc SetDefaultLinks(SetDefaultLinksRequest) returns (SetDefaultLinksResponse);
rpc EmitEvent(EmitEventRequest) returns (EmitEventResponse);

// Workflow is a named set of triggers and agents, stored as YAML.
message Workflow {
  string name = 1;
  string source = 2;               // The YAML definition.
  repeated string agents = 3;      // Agent names, in definition order. Read-only.
  google.protobuf.Timestamp created_at = 4;
  google.protobuf.Timestamp updated_at = 5;
}

// WorkflowInstance is one run of a workflow.
message WorkflowInstance {
  int64 id = 1;
  string workflow = 2;
  optional string start_key = 3;   // The subject the instance is keyed by; unset when it has no key.
  google.protobuf.Timestamp created_at = 4;
  google.protobuf.Timestamp closed_at = 5;  // Unset while the instance is open.
}

message PutWorkflowRequest {
  string source = 1;               // The workflow's name is the YAML's `workflow` key.
}

message StartWorkflowRequest {
  string name = 1;
  string instruction = 2;          // The body of workflow.trigger.
}

message StartWorkflowResponse {
  int64 instance_id = 1;
  repeated Task tasks = 2;         // The tasks the start created.
}

message ListWorkflowInstancesRequest {
  string workflow = 1;
  bool include_closed = 2;
}

message CloseWorkflowInstanceRequest {
  int64 instance_id = 1;
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

- `StartWorkflow` (`OpTaskCreate`) always inserts a new instance with no key and publishes `workflow.trigger` on it with `manual: "true"` and the instruction as its body. It matches no trigger entry, so no `start` applies and no later trigger match finds the instance. Each agent subscription that matches starts or delivers as usual.
- `CloseWorkflowInstance` (`OpTaskCreate`) sets `closed_at`. From then on triggers don't find the instance, none of its subscriptions match, so no task starts in it and its tasks receive no events from any path, link subscriptions included. Closing does not cancel or archive tasks: a task that is mid-run finishes, and `UpdateTask` still delivers a person's instruction to it. Nothing closes an instance implicitly, and a closed instance is not reopened. It is exposed in the web UI, the CLI and the server MCP; the agent MCP has no tool for it.
- `PutWorkflow` and `DeleteWorkflow` need `OpOrgWrite`; the reads need `OpOrgRead`. These are the scopes `SetRoutingRules` and `GetRoutingRules` use.
- `EmitEvent` needs `OpTaskWrite` on the task, the check `CreateLink` does. The server sets the source to `task`, the namespace to the task's instance bus and the emitter from the task row. The caller can set neither. The response is the publish's outcome, which is how an agent sees whether its event reached anyone.
- `Task` gains `instance_id`, `agent`, `workflow` and `capabilities`, and field 17 (`namespace`) is reserved. `CreateTaskRequest.namespace` is reserved.
- `GetRoutingRules`, `SetRoutingRules`, `RoutingRule`, `RuleCondition` and `CreateTaskAction` are deleted.

`TestEvent` runs `Router.Plan` for a dry run and `Router.Publish` for a fire. Its response replaces `TestEventMatch` with the plan:

```proto
message TestEventResponse {
  bool fired = 2;
  reserved 1;                              // Previously: matches
  repeated TestEventInstance instances = 3; // Instances that triggers resolved the event to.
  repeated TestEventStart starts = 4;      // Tasks that would start, or started.
  repeated TestEventDelivery deliveries = 5;
}

message TestEventInstance {
  string workflow = 1;
  int64 instance_id = 2;                   // Unset for an instance a dry run would create.
  bool created = 3;                        // Whether the event creates the instance.
}

message TestEventStart {
  string workflow = 1;
  string agent = 2;
  int64 instance_id = 3;                   // Unset in an instance a dry run would create.
  int64 task_id = 4;                       // Set only when fired.
}

message TestEventDelivery {
  int64 task_id = 1;
  bool wake = 2;
  int64 event_id = 3;                      // Set only when fired.
}
```

`Plan` is the read half of `Publish`, so a dry run follows the `workflow.trigger` each trigger match would publish and reports the tasks it would start and deliver to, including in an instance that doesn't exist yet (matched against the snapshot it would take). It reads keys and subscriptions, so unlike today it reports what would happen to the instances and tasks that exist, not only what a rule says. `TestEventRequest` accepts `source: schedule` with a type and no URL. It does not accept `task` events.

### Agent tools

`internal/runner/agentmcp` changes:

- `create_link`: `url` becomes `subject`, and `type` is added.
- `emit` is added, calling `EmitEvent` with `type`, `subject`, `body` and `attrs`, and returning the ids of the tasks the event started and was delivered to.
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

`Schedule`, `CreateScheduleRequest` and `UpdateScheduleRequest` lose the same five fields and gain `event_type`. `scheduler.Fire` no longer creates a task: it publishes `{Source: "schedule", Type: sched.EventType}` through the router in the tick's transaction. `RunSchedule` publishes the same event. `last_task_id` is set to the first task the publish started and cleared when it started none, so the schedules list shows a tick that reached no trigger.

A schedule event has no subject, so a trigger on one with `start: unique` or `once` resolves every tick to the same instance, and `start: always` gives each tick its own.

### CLI, MCP and web UI

- **CLI** (`internal/command`): `gritz workflow list`, `get <name>`, `apply -f <file>`, `delete <name>`, `start <name> [-m <instruction>]`, `instances <name> [--closed]` and `close <instance-id>`. `gritz task create` loses `--namespace`.
- **Server MCP** (`internal/server/mcpserver`): `list_workflows`, `get_workflow`, `put_workflow`, `delete_workflow`, `start_workflow`, `list_workflow_instances` and `close_workflow_instance` are added. `get_routing_rules` and `set_routing_rules` are removed. The schedule tools take `event_type` in place of the template fields. `test_event` returns the new plan.
- **Web UI** (`webui/src/routes`): a **Workflows** page lists workflows; a workflow's page has a YAML editor, the **Start** button with an instruction field, and the workflow's instances with their key, their tasks, whether each is open, and a **Close** button. **Events → Routing Rules** and `routing.new` / `routing.$index` are removed; `routing.testevent` moves under Events and renders instances, starts and deliveries. The task page shows the workflow, agent and instance in place of the namespace, and lists the instance's other tasks. The new task and schedule forms lose their template and namespace fields; the settings page gains the default link types editor.

### API

```go
package model // github.com/gritzapp/gritz/internal/model

// Workflow is a parsed workflow definition. // new
type Workflow struct {
	Name     string                   `json:"workflow"`
	Export   []Export                 `json:"export,omitempty"`
	Triggers []Trigger                `json:"triggers,omitempty"`
	Agents   map[string]WorkflowAgent `json:"agents"`
}

// WorkflowAgent is a task template with its subscriptions and link types. // new
type WorkflowAgent struct {
	Workspace    string              `json:"workspace"`
	Runner       string              `json:"runner"`
	Prompt       string              `json:"prompt,omitempty"`
	AutoArchive  time.Duration       `json:"auto_archive,omitempty"`
	Capabilities []string            `json:"capabilities,omitempty"`
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

// Start says whether a matching event may create an instance or a task, and which existing one handles it. // new
type Start string

const (
	StartNever  Start = "never"
	StartOnce   Start = "once"
	StartUnique Start = "unique"
	StartAlways Start = "always"
)

// Trigger is a filter that resolves an event to an instance of the workflow. // new
type Trigger struct {
	Filter
	Start Start `json:"start"`
}

// Subscription is a filter whose events are delivered to an agent's tasks. // new
type Subscription struct {
	Filter
	Start Start  `json:"start"`
	Link  string `json:"link,omitempty"`
	Wake  bool   `json:"wake"`
}

// LinkSubscription is a filter delivered to the task holding a link, scoped to the link's subject. // new
type LinkSubscription struct {
	Filter
	Wake bool `json:"wake"`
}

// LinkType is what a subscribed link of one type subscribes its task to. // new
type LinkType struct {
	Subscribe []LinkSubscription `json:"subscribe,omitempty"`
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

// StoredSubscription is one subscriptions row: an agent's entry in an instance, or a link's filter. // new
type StoredSubscription struct{ ... }

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
	Instances  []InstanceMatch
	Starts     []TaskStart
	Deliveries []Delivery
}

// Outcome is what publishing an event did. // new
type Outcome struct {
	Event   Event
	Plan    Plan
	TaskIDs []int64 // tasks started
}

// Plan resolves instances and tasks for ev without writing. // changed
func (r *Router) Plan(ctx context.Context, tx *sql.Tx, ev Event) (*Plan, error)

// Publish starts instances and tasks for ev and delivers it, inside tx. // new
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

// ListWorkflowInstances returns a workflow's instances, newest first. // new
func (s *Store) ListWorkflowInstances(ctx context.Context, tx *sql.Tx, orgID int64, workflow string, includeClosed bool) ([]*model.WorkflowInstance, error)

// FindOpenInstances returns the workflow's open instances with that key. // new
func (s *Store) FindOpenInstances(ctx context.Context, tx *sql.Tx, orgID int64, workflow, key string) ([]*model.WorkflowInstance, error)

// CloseWorkflowInstance marks the org's instance closed. // new
func (s *Store) CloseWorkflowInstance(ctx context.Context, tx *sql.Tx, id, orgID int64) error

// CreateSubscriptions inserts an instance's or a link's subscriptions. // new
func (s *Store) CreateSubscriptions(ctx context.Context, tx *sql.Tx, subs []*model.StoredSubscription) error

// FindSubscriptions returns the live subscriptions for a bus, source and type. // new
func (s *Store) FindSubscriptions(ctx context.Context, tx *sql.Tx, orgID int64, namespace, source, typ string) ([]*model.StoredSubscription, error)

// FindLiveTasks returns the agent's unarchived tasks in the instance; a non-nil key selects those started with it. // new
func (s *Store) FindLiveTasks(ctx context.Context, tx *sql.Tx, instanceID int64, agent string, key *string) ([]*model.Task, error)

// LockStartKey serialises lookups and starts on one instance or task key for the transaction. // new
func (s *Store) LockStartKey(ctx context.Context, tx *sql.Tx, scope string) error

// FindSubscribedLinksForOrgs, ListRoutingRulesForEvent, GetOrgRoutingRules, SetOrgRoutingRules: removed
```

**How it fits:** `githubserver`, `atlassianserver` and `customserver` build an `InputEvent` and call `Router.Route`, as today. `apiserver` calls `Router.Publish` directly from `EmitEvent` (a `task` event), `StartWorkflow` (`workflow.trigger` on the instance it just created), `SubmitRunnerEvents` (`task.completed`, `task.failed`) and `TestEvent`; `scheduler.Fire` calls it for `schedule` events. `Publish` calls itself for the `workflow.trigger` of each instance a trigger resolved and for each export copy. It reads definitions, keys and subscriptions through the store, and writes instances, tasks, events, links and subscriptions with the store methods `Router.create` and `Router.attach` already use. `CreateLink` and the router's `link` handling both expand a link type into `subscriptions` rows through one helper in `eventrouter`.

## Implementation Plan

Each slice updates the docs pages its change makes wrong, per the table in `CLAUDE.md`.

1. **Workflow model and parser.** Delivers: `model.Workflow` and its parts, `ParseWorkflow`, `MatchFilter`, `SchemaRegistry.ValidateWorkflow`. Depends on: nothing. Verifiable by: unit tests parsing the issue's four examples and rejecting each invalid key.
2. **Schema.** Delivers: the `workflows`, `workflow_instances` and `subscriptions` tables, `tasks.instance_id`, `tasks.agent` and `tasks.start_key`, the `task_links` columns, and their store methods. Depends on: nothing. Verifiable by: the migration running up and down against a copy of production data; store tests.
3. **Every task has an instance.** Delivers: `CreateTask`, `Router.create` and `scheduler.Fire` insert an instance for the task; `Task.instance_id` and `Task.agent` on the wire. Depends on: (2). Verifiable by: e2e tests asserting the instance on each creation path.
4. **Workflow CRUD.** Delivers: `ListWorkflows`, `GetWorkflow`, `PutWorkflow`, `DeleteWorkflow`, `gritz workflow list/get/apply/delete`, the server MCP tools, and the Workflows page with its editor. Definitions are stored and validated but nothing reads them. Depends on: (1), (2). Verifiable by: applying the issue's examples and reading them back.
5. **Typed links.** Delivers: `model.ResolveSubject` replacing `RoutingKey`, `CreateLinkRequest.subject` and `type`, the `create_link` tool change, link rendering in the web UI. Links don't expand to subscriptions yet; routing still reads `task_links.subject` as the key. Depends on: (2). Verifiable by: unit tests for inference and rejection; an e2e task linking a pull request and being woken by a rule.
6. **Publish, unused.** Delivers: `Router.Plan` and `Router.Publish` over triggers, instance and task keys, `workflow.trigger`, subscriptions and entry links, with the link type expansion helper, beside the existing `Route`. No production caller. Depends on: (1), (2), (3), (5). Verifiable by: router tests covering each example in the issue, each `start` value on a trigger and on a subscription, shared keys across entries, and closed instances.
7. **Cutover.** Delivers: `Route`, `TestEvent` and `CreateLink` switch to `Publish` and the expansion helper; routing rules, `tasks.namespace`, their RPCs, MCP tools and UI routes are deleted; `orgs.default_links` and its editor are added; the test event form renders the plan. Before it merges, our org's rules are rewritten as workflows by hand. Tasks live at the cutover keep their links as references and have no subscriptions. Depends on: (4), (6). Verifiable by: e2e tests for the "mention the bot" and reviewer examples against GitHub webhooks.
8. **Emit, task system events and exports.** Delivers: `EmitEvent` and the `emit` tool, `task.completed` and `task.failed` from `SubmitRunnerEvents`, export republishing. Depends on: (7). Verifiable by: an e2e test of the fan-out example with a planner and two implementers.
9. **Manual start and closing.** Delivers: `StartWorkflow`, `ListWorkflowInstances`, `CloseWorkflowInstance`, `gritz workflow start/instances/close`, the server MCP tools, the Start and Close buttons, and the instance list on the workflow page. Depends on: (7). Verifiable by: starting the fan-out example by hand and seeing `workflow.trigger` as the planner's first event; closing the instance and seeing a review comment no longer wake its implementer.
10. **Schedules emit events.** Delivers: the schedule migration, `scheduler.Fire` publishing, the reduced schedule API, form and MCP tools. Before it merges, our schedules are paired with workflows by hand. Depends on: (7). Verifiable by: scheduler tests and the nightly example.
11. **Task tools.** Delivers: `Task.capabilities`, the `task.instance` scope attribute, `ListTasks.instance_id`, and the three read-only agent tools. Depends on: (7). Verifiable by: scope tests; an e2e planner reading a failed implementer's log.

## Trade-offs

- **Triggers on the workflow instead of on agents.** With triggers on agents, one list did two jobs: creating instances from outside events and starting tasks inside an instance. Two agents matching one external event then needed a rule (two instances), and an agent's overlapping triggers needed another (alternatives). Triggers on the workflow only resolve instances, `workflow.trigger` hands the event to the instance, and starting tasks is left to subscriptions.
- **Triggers don't put the matched event on the instance bus.** Republishing the event itself would make triggers a gate on which external events agents can subscribe to, and link subscriptions would bypass that gate. Publishing a `system` event keeps triggers and subscriptions independent. The cost is that an agent starting from a trigger subscribes to `workflow.trigger` and not to the original type, and tells triggers apart by attribute.
- **`start` keys instead of claims.** A claim on a link held back triggers across agents and workflows, dropped the held event, and tied creation control to linking. `start` with a key on the instance and on the task separates the two: a link is only a subject and its subscriptions, and a repeated event reaches the task that already handles it. The cost is that nothing excludes one workflow because another is handling the same subject; overlapping workflows both run, and a claim at the workflow level is left for later.
- **An explicit close instead of a derived end.** An instance could end when its last task is archived, or on an event the workflow names. Both make "is there an instance for this subject" depend on timing an author doesn't control. An instance that stays open until closed is predictable. The cost is that someone has to close it before the same subject can start a fresh one, and open instances accumulate.
- **No limits.** A hop count stamped on events needs every emitted event to name its cause and is lost when a loop passes through GitHub. Budgets on starts and wakes bound every loop shape but also stop a legitimate burst, and a per-instance task budget conflicts with instances that stay open. This design has none: a loop runs until a person closes the instance or stops its tasks.
- **No table of published events.** Storing every published event once, with its deliveries, would answer "what did this event do" after the fact, including for events that were dropped. It also stores every webhook an org receives and adds a second event table beside `events`. This design keeps today's rule (an event is stored on the tasks it reaches), puts the plan in the test-event report, and returns an emitted event's outcome from `emit`.
- **A dry run for external events only.** Testing a `task` event needs an instance or a namespace to publish it in, an emitting agent, and new request fields. `Plan` over external and schedule events reuses the request `TestEvent` has. The cost is that a mistyped filter on an agent-emitted type can only be seen from `emit`'s result or a live instance.
- **Materialised subscriptions instead of matching snapshots at publish time.** An external event is matched in every open instance, so matching from definitions would load every open instance's snapshot per event. Rows written when an instance starts and when a link is subscribed make the lookup one indexed query, at the cost of a table that duplicates what the snapshots imply.
- **Snapshot per instance instead of following the definition.** An instance that followed edits would need rules for a deleted workflow, a renamed agent and the keys of its existing tasks. The snapshot costs a copy of the spec per instance, including one per plain task, and means an instance left open runs its old definition until it is closed.
- **A lock instead of a unique index on keys.** A partial unique index on `(instance_id, agent, start_key)` over unarchived tasks would enforce one live task per key, but unarchiving a task whose key was reused would then fail. The advisory lock serialises the find-or-start, and a lookup tolerates more than one result.
- **YAML as the stored and edited form.** The API carries the definition as text, as the server-managed workspaces draft does for workspace configs, so the proto doesn't mirror the workflow shape and the CLI, MCP tool and editor all handle one format. The web UI gives up the structured rule form.
- **The `external` event arm is reused for `task`, `system` and `schedule` events.** Renaming the arm and the `events.type` discriminator would touch the store, the runner's event replica and the web UI timeline for no change in behaviour.
- **`public` stays a filter key rather than becoming a matchable attribute.** An attribute would let `filter:` express it, but the default (members only) is not a condition a filter writes, so it would be an implicit condition on an attribute. A key with a default says the same thing directly.

## Open Questions

1. **What `workflow.trigger` carries.** The attrs `trigger_source`, `trigger_type` and `manual` are this proposal's names; the issue lists them as not yet confirmed. A matched event that has its own `manual`, `trigger_source` or `trigger_type` attr would collide with them.
2. **Tasks live at the cutover.** They lose their subscriptions, because nothing maps a rule-driven link onto a link type. Is that acceptable for our deployment, or should the cutover migration expand existing subscribed links against `orgs.default_links`?
3. **A manual start that starts nothing.** `StartWorkflow` on a workflow where no agent subscribes to `workflow.trigger` with a `start` creates an open instance with no tasks. Should it be rejected with `FailedPrecondition`?
4. **Closing a plain task's instance.** `CloseWorkflowInstance` accepts any instance, so a plain task's instance can be closed to stop its link events without archiving it. The web UI only offers Close on a workflow's page.
5. **Instances are never deleted.** Closed instances and their `subscriptions` rows stay until the org is deleted. The lookup skips them by join, so they cost storage and not matching time.
6. **Naming.** "Agent" also names `internal/runner/agent` (the CLI integrations); this design uses `model.WorkflowAgent` in Go and `agent` in YAML and on the wire. The `task_tools` capability and the `i/<id>` bus name are placeholders.