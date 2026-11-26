---
title: "Agents"
description: "Routing rules are one org-wide ordered list that mixes admission, wake policy and task creation, schedules and rules each carry their own task template, and a subscribed link delivers whatever the matching rule allows rather than what its task asked for."
---

Issue: https://github.com/gritzapp/gritz/issues/2355

## Problem

- **One ordered list per org.** `Router.Plan` takes the first matching `model.RoutingRule` per org, so two unrelated jobs (an implementer and a reviewer) can't both react to the same event.
- **A rule does three jobs.** Admission (`Public`, `Conditions`), wake policy (`Wakeup`) and spawning (`Create`) live on the same rule. A subscribed link only delivers an event if some rule matches it first, and the rule, not the task that made the link, decides whether it wakes.
- **Namespaces are a workaround.** `tasks.namespace` and `RoutingRule.Namespace` exist so that a subscribed task doesn't suppress an unrelated rule's create. They are a filter on the link lookup, and every rule and task has to agree on one.
- **The task template is duplicated.** `CreateTaskAction` (on rules) and `model.Schedule` each carry workspace, runner, prompt, auto-archive and variables.
- **Links are untyped URLs.** `create_link` accepts any URL, and `model.RoutingKey` has to guess what resource a URL names (web or API form, comment anchors, workflow run jobs) so that it matches the URL a webhook produces later.

[Event Workflows](/proposals/rejected/event-workflows/) addressed this with workflows, instances, buses and `start` modes. This proposal keeps its claims and drops the rest.

## Design

### Overview

- An **agent** is a named, org-scoped task template plus everything that creates or wakes its tasks: **triggers**, **link kinds** and **schedules**. The org has no routing rules or schedules of its own.
- Every task belongs to exactly one agent.
- **Triggers** create tasks. They never wake an existing one. Each agent's triggers are evaluated independently of every other agent's; within an agent, the first matching trigger wins.
- **Link kinds** are the only links an agent's tasks may subscribe to. A kind names a registered **link type**, and lists the events a subscribed link of that kind delivers. Delivery no longer depends on any trigger matching.
- **Link types** are registered by the integrations, beside their event types. A link type recognizes and canonicalizes URLs of one resource (`github.pr`, `jira.issue`). Each event type declares which link types it is about. `model.RoutingKey` is replaced by the link types.
- A link kind can give its links a **claim**. A trigger with `yield_to` creates no task while an unarchived task holds one of those claims on the event's resource.
- Namespaces, `orgs.routing_rules` and the schedule's task template are removed.

### Agent definition

Agents are structured proto messages (see [Proto](#proto)). Shown here as YAML for readability:

```yaml
name: implementer
workspace: default
runner: main
prompt: Implement the issue and open a pull request.
auto_archive: 24h
variables:
  REPO: gritz

triggers:
  - source: atlassian
    type: label_added
    conditions:
      - { attr: label, op: equals, value: build-it }
    yield_to: [pr-owner]      # optional claim names
    link: jira.issue          # optional: kind of the subscribed link the new task gets
    public: false             # optional, as RoutingRule.Public

links:
  - type: jira.issue          # kind defaults to the type: "jira.issue"
    events: [comment_created]
  - type: github.pr
    kind: owned-pr            # needed because a second github.pr entry exists
    claim: pr-owner
    events: [pull_request_review_comment, pull_request_review, pull_request_closed, pull_request_check_run_completed]
  - type: github.pr           # kind "github.pr": a PR the agent only watches
    events: [pull_request_closed]

schedules:
  - name: nightly
    cron_expr: "0 3 * * *"
    timezone: America/Toronto
    instructions: Re-check open PRs.
```

### Link types

A link type is registered on `eventrouter.SchemaRegistry` by the integration that owns it, next to its event types:

| Link type | Registered by | URLs it accepts | Canonical URL |
| --- | --- | --- | --- |
| `github.issue` | `githubserver` | issue web and API URLs, with comment anchors | `https://github.com/{o}/{r}/issues/{n}` |
| `github.pr` | `githubserver` | PR web and API URLs, `/files`, `/commits/…`, anchors | `https://github.com/{o}/{r}/pull/{n}` |
| `github.workflow_run` | `githubserver` | run, job and attempt URLs | `https://github.com/{o}/{r}/actions/runs/{id}` |
| `jira.issue` | `atlassianserver` | browse and REST URLs by key | `https://{site}/browse/{KEY}` |
| `custom.url` | `customserver` | any absolute http(s) URL | the URL unchanged |

Each `model.EventTypeDef` gains `LinkTypes []string`, the link types its event's URL can name. `issue_comment` names both `github.issue` and `github.pr`; `push` names none, so it can trigger but never be delivered through a link. A custom event type may declare `custom.url`.

The canonicalization in `model.RoutingKey` moves, per resource, into the link type's `Canonical` function. The router derives an event's link key by running its URL through each of its event type's link types; at most one accepts it.

### Triggers

A trigger is today's routing rule selector (`source`, `type`, `conditions`) and `public`, plus:

- `yield_to`: claim names. The trigger creates nothing while an unarchived task in the org holds a link with one of these claims on the event's canonical URL. The event is not forwarded to the holder by the trigger; the holder receives it only if its own link kind lists the event type.
- `link`: a link kind of this agent. The created task gets a subscribed link of that kind to the event's canonical URL, carrying the kind's claim. If the event's URL isn't accepted by the kind's type, the task is still created without the link.

When a trigger matches, the router creates a task from the agent's template, with the event as its first event, as `Router.create` does now with `CreateTaskAction`. The prompt is the agent's `prompt`.

### Link kinds and delivery

- An entry's kind is its `kind`, or its `type` when `kind` is empty. Kinds are unique within the agent.
- `events` lists event types (by `type`; the source is the link type's). Empty means every event type that declares the link type.
- `claim` is stamped on every link of this kind.
- `public` (per kind, as on triggers) allows delivery of events from actors who aren't org members.

`create_link` takes a `kind`. A subscribed link must name one of the task's agent's kinds, and its URL must be accepted by the kind's type; the stored URL is the canonical one. A link with `subscribe=false` is a plain reference: it takes no kind, and its URL is stored as given.

An event is delivered to a subscribed link when the event's canonical URL equals the link's URL, the event type is listed by the link's kind, and admission passes. Delivery always wakes the task. Each matching task receives the event once, however many of its links match.

Triggers and delivery are independent: an event can be delivered to a subscribed task and also create a task through another agent's trigger. `yield_to` is the way to prevent that.

### Claims

A claim is a name stamped on a link by its kind. A task holds a claim on a URL while it is unarchived and has a subscribed link with that claim and URL. Claims are org-wide strings: a trigger in one agent can yield to a claim held by another agent's tasks.

```yaml
# implementer: the PRs it opens carry pr-owner
links:
  - { type: github.pr, kind: owned-pr, claim: pr-owner, events: [pull_request_review_comment] }

# responder: answers @gritz mentions on PRs nobody owns
triggers:
  - source: github
    type: issue_comment
    conditions: [{ attr: mention, op: equals, value: gritz }]
    yield_to: [pr-owner]
    link: github.pr
links:
  - { type: github.pr, events: [issue_comment] }
```

### Schedules

A schedule belongs to an agent and creates a new task of that agent each time it fires. It keeps `name`, `cron_expr`, `timezone`, `enabled`, its bookkeeping, and `instructions`, which are the created task's first instructions in place of the agent's prompt. `RunSchedule` is unchanged.

### Manual tasks

`CreateTask` names an agent. The caller supplies only the instructions; workspace, runner, auto-archive and variables come from the agent. `draft` is unchanged.

### Validation

`SchemaRegistry.ValidateAgent` runs on create and update:

- Every trigger passes today's `SchemaRegistry.Validate` checks (registered type, ops, attrs, custom triggers must be public).
- A trigger's `link` names a kind of this agent whose type is one of the trigger event type's `LinkTypes`.
- Every link entry's `type` is registered, its kind is unique in the agent, and every listed event type declares that link type.

### Schema

New migration:

```sql
CREATE TABLE agents (
    id           bigserial PRIMARY KEY,
    org_id       bigint NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name         text NOT NULL,
    workspace    text NOT NULL,
    runner       text NOT NULL,
    prompt       text NOT NULL DEFAULT '',
    auto_archive bigint NOT NULL DEFAULT 0,
    variables    jsonb NOT NULL DEFAULT '{}',
    triggers     jsonb NOT NULL DEFAULT '[]',
    links        jsonb NOT NULL DEFAULT '[]',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, name)
);

ALTER TABLE tasks ADD COLUMN agent_id bigint REFERENCES agents(id);
ALTER TABLE tasks DROP COLUMN namespace;

ALTER TABLE task_links ADD COLUMN kind text;   -- NULL for plain reference links
ALTER TABLE task_links ADD COLUMN claim text;
ALTER TABLE task_links DROP COLUMN routing_key;
CREATE INDEX task_links_subscribed ON task_links (url) WHERE subscribe;

ALTER TABLE schedules ADD COLUMN agent_id bigint NOT NULL REFERENCES agents(id) ON DELETE CASCADE;
ALTER TABLE schedules DROP COLUMN workspace, DROP COLUMN runner, DROP COLUMN namespace,
                      DROP COLUMN auto_archive, DROP COLUMN variables;

ALTER TABLE orgs DROP COLUMN routing_rules;
```

The task keeps its own `workspace`, `runner`, `auto_archive` and `variables`, copied from the agent at creation, so editing an agent doesn't move running tasks. The link's `claim` is copied from its kind at creation so the yield lookup is a plain indexed query; which events a link receives is read from the agent's current kind at routing time.

At the cutover the migration creates an agent named `default` per org with no triggers or links, assigns every existing task to it, and then sets `tasks.agent_id` `NOT NULL`. Existing subscribed links have no kind and stop receiving events. Existing schedules and routing rules are dropped and must be recreated as agents.

### Router

`Router.Plan` and `Router.Apply` keep their split (the test-event path needs a side-effect-free plan) but change shape:

1. **Admission.** For each org in `input.Orgs`, note whether the actor is a member, as today.
2. **Delivery.** Compute the event's canonical URL per link type. Find subscribed links with that URL in those orgs, joined to their task's agent. Keep a link when its kind lists the event type and (the actor is a member or the kind is public). Attach the event to each distinct task, waking it.
3. **Triggers.** For each agent in each org, take its first trigger that matches (selector, conditions, public). If the trigger has `yield_to`, look up unarchived tasks holding one of those claims on the event's canonical URL; if any, record the yield and stop. Otherwise create a task of the agent, attach the event, and create the trigger's link.

There is no rule order across agents and no first match across the org.

### Agent tools

- `create_link` gains `kind`, required when `subscribe` is true. The error for an unknown kind lists the agent's kinds.
- The first-run brief renders the agent's link kinds (kind, type, claim) so the agent knows what to pass.

### Proto

```proto
service GritzService {
  rpc CreateAgent(CreateAgentRequest) returns (CreateAgentResponse);
  rpc GetAgent(GetAgentRequest) returns (GetAgentResponse);
  rpc ListAgents(ListAgentsRequest) returns (ListAgentsResponse);
  rpc UpdateAgent(UpdateAgentRequest) returns (UpdateAgentResponse);
  rpc DeleteAgent(DeleteAgentRequest) returns (DeleteAgentResponse);
  // removed: GetRoutingRules, SetRoutingRules
}

message Agent {
  int64 id = 1;
  string name = 2;
  string workspace = 3;
  string runner = 4;
  string prompt = 5;
  google.protobuf.Duration auto_archive = 6;
  map<string, string> variables = 7;
  repeated Trigger triggers = 8;
  repeated LinkKind links = 9;
}

message Trigger {
  string source = 1;
  string type = 2;
  repeated RuleCondition conditions = 3;
  bool public = 4;
  repeated string yield_to = 5;
  string link = 6;  // a kind of the same agent
}

message LinkKind {
  string type = 1;            // a registered link type, e.g. "github.pr"
  string kind = 2;            // defaults to type
  string claim = 3;
  repeated string events = 4; // event types of the link type's source; empty = all
  bool public = 5;
}

message LinkTypeDef {
  string name = 1;   // "github.pr"
  string source = 2; // "github"
  string label = 3;
}
```

Changed messages:

- `CreateTaskRequest`: `agent` (name) replaces `workspace`, `runner`, `auto_archive`, `namespace` and `variables`.
- `Task`: `agent` added, `namespace` removed.
- `CreateLinkRequest`, `TaskLink`: `kind` and `claim` added; `routing_key` removed.
- `Schedule`, `CreateScheduleRequest`, `UpdateScheduleRequest`: `agent` replaces the template fields.
- `EventTypeDef`: `link_types` added. `GetEventTypesResponse` adds `repeated LinkTypeDef link_types`.
- `TestEventMatch` is replaced by per-agent results: the agent, the trigger index, whether it created or yielded (with the holding task IDs), and the tasks the event was delivered to with the link kind that matched.
- `RoutingRule`, `CreateTaskAction` and the routing-rule RPCs are removed.

### CLI, MCP and web UI

- `gritz agent list|get|create|update|delete`; `gritz task create --agent`.
- Server MCP: `list_agents`, `get_agent`, `put_agent`, `delete_agent` replace `get_routing_rules` and `set_routing_rules`; `create_task` and the schedule tools take `agent`.
- Web UI: an Agents page replaces Routing Rules and Schedules. An agent's page has its template, triggers, link kinds and schedules; the trigger and link forms are driven by `GetEventTypes` (link kinds offer only event types that declare the chosen link type). The create-task form picks an agent. The task page shows each link's kind and claim.

### API

```go
package model // github.com/gritzapp/gritz/internal/model

// Agent is a named task template with the triggers, link kinds and schedules that create and wake its tasks. // new
type Agent struct {
	ID          int64
	OrgID       int64
	Name        string
	Workspace   string
	Runner      string
	Prompt      string
	AutoArchive time.Duration
	Variables   map[string]string
	Triggers    []Trigger
	Links       []LinkKind
}

// Kind returns the link kind named name, and false if the agent has none. // new
func (a *Agent) Kind(name string) (LinkKind, bool)

// Trigger creates a task of its agent when a matching event arrives. // new
type Trigger struct {
	Source     string
	Type       string
	Conditions []Condition
	Public     bool
	YieldTo    []string
	Link       string
}

// LinkKind is a kind of subscribed link an agent's tasks may create. // new
type LinkKind struct {
	Type   string
	Kind   string
	Claim  string
	Events []string
	Public bool
}

// Name returns Kind, or Type when Kind is empty. // new
func (k LinkKind) Name() string

// LinkTypeDef describes a resource that links can name. // new
type LinkTypeDef struct {
	Name   string
	Source string
	Label  string
	// Canonical returns the resource's canonical URL, and false if raw isn't one.
	Canonical func(raw string) (string, bool)
}

type EventTypeDef struct {
	// ...
	LinkTypes []string // new
}

type Link struct {
	// ...
	Kind  string // new
	Claim string // new
	// removed: RoutingKey, Namespace
}

type Task struct {
	// ...
	AgentID int64 // new
	// removed: Namespace
}

// removed: RoutingRule, CreateTaskAction, RoutingKey
```

```go
package eventrouter // github.com/gritzapp/gritz/internal/server/eventrouter

// MustRegisterLinkType records def, panicking on a duplicate name. // new
func (r *SchemaRegistry) MustRegisterLinkType(def model.LinkTypeDef)

// LinkType returns the link type named name. // new
func (r *SchemaRegistry) LinkType(name string) (model.LinkTypeDef, bool)

// LinkTypes returns every registered link type in registration order. // new
func (r *SchemaRegistry) LinkTypes() []model.LinkTypeDef

// CanonicalURL returns the canonical URL of an event of type def at raw, and false if none of its link types accepts it. // new
func (r *SchemaRegistry) CanonicalURL(def model.EventTypeDef, raw string) (string, bool)

// ValidateAgent checks an agent's triggers and link kinds against the registry. // new
func (r *SchemaRegistry) ValidateAgent(a *model.Agent) error

// removed: Validate(model.RoutingRule)

// Delivery is a task an event is delivered to and the link kind that matched. // new
type Delivery struct {
	TaskID int64
	Kind   string
}

// TriggerMatch is an agent whose trigger matched, and whether it created or yielded. // new
type TriggerMatch struct {
	OrgID        int64
	Agent        *model.Agent
	TriggerIndex int
	YieldedTo    []int64 // tasks holding a yield_to claim; empty if the trigger creates
}

// Plan is the side-effect-free result of routing an event. // changed
type Plan struct {
	Deliveries []Delivery
	Triggers   []TriggerMatch
}

// Plan computes deliveries and trigger matches without writing anything. // changed
func (r *Router) Plan(ctx context.Context, input InputEvent) (*Plan, error)

// Apply attaches the planned deliveries and creates tasks for the non-yielded triggers. // changed
func (r *Router) Apply(ctx context.Context, input InputEvent, plan *Plan) ([]RouteOutcome, error)
```

```go
package store // github.com/gritzapp/gritz/internal/store

// CreateAgent inserts a and sets its ID. // new
func (s *Store) CreateAgent(ctx context.Context, tx *sql.Tx, a *model.Agent) error

// GetAgent returns the org's agent by name. // new
func (s *Store) GetAgent(ctx context.Context, tx *sql.Tx, orgID int64, name string) (*model.Agent, error)

// ListAgentsForOrgs returns the agents of every org in orgIDs. // new
func (s *Store) ListAgentsForOrgs(ctx context.Context, tx *sql.Tx, orgIDs []int64) ([]*model.Agent, error)

// UpdateAgent replaces the stored agent with a. // new
func (s *Store) UpdateAgent(ctx context.Context, tx *sql.Tx, a *model.Agent) error

// DeleteAgent removes the org's agent and its schedules. // new
func (s *Store) DeleteAgent(ctx context.Context, tx *sql.Tx, orgID, id int64) error

// FindSubscribedLinks returns subscribed links on url in orgIDs, with their task's agent. // changed (was FindSubscribedLinksForOrgs, keyed by routing key)
func (s *Store) FindSubscribedLinks(ctx context.Context, tx *sql.Tx, url string, orgIDs []int64) ([]*LinkWithAgent, error)

// FindClaimHolders returns unarchived tasks in orgID holding one of claims on url. // new
func (s *Store) FindClaimHolders(ctx context.Context, tx *sql.Tx, orgID int64, url string, claims []string) ([]int64, error)

// removed: ListRoutingRulesForEvent, GetRoutingRules, SetRoutingRules
```

**How it fits:** `githubserver`, `atlassianserver` and `customserver` register their link types in the same `RegisterSchemas` that registers their event types. The agent RPC handlers call `ValidateAgent` then the store. `CreateLink` resolves the task's agent, finds the kind, and canonicalizes the URL with the kind's link type. The webhook servers call `Router.Route`, which runs `Plan` (`ListAgentsForOrgs`, `FindSubscribedLinks`, `FindClaimHolders`) and `Apply`. The scheduler loads the schedule's agent to build the task.

## Implementation Plan

1. **Link types in the registry** — Delivers: `model.LinkTypeDef`, `EventTypeDef.LinkTypes`, `MustRegisterLinkType`, `CanonicalURL`, and registrations in `githubserver`, `atlassianserver`, `customserver` with the canonicalization moved out of `model.RoutingKey`. Depends on: nothing. Verifiable by: table tests of every link type's `Canonical` against the `RoutingKey` cases.
2. **Agents table and store** — Delivers: the `agents` migration and store methods. Depends on: nothing. Verifiable by: store tests.
3. **Agent model, validation and RPCs** — Delivers: `model.Agent`, `ValidateAgent`, the agent proto and RPCs, `LinkTypeDef` in `GetEventTypes`. Depends on: (1), (2). Verifiable by: handler tests, including the validation errors.
4. **Cutover migration** — Delivers: `tasks.agent_id`, the `default` agents, link `kind`/`claim`, dropping `routing_key`, `namespace` and `orgs.routing_rules`; `CreateTask` takes `agent`; `CreateLink` takes `kind` and canonicalizes; the router rewritten to `Plan`/`Apply` over agents with delivery, triggers and claims; `TestEvent` reporting per agent; routing-rule RPCs and model removed. Depends on: (3). Verifiable by: router tests per case (delivery by kind, first trigger wins, two agents on one event, yield, trigger link), and the e2e harness creating a task from a trigger and waking it through its link.
5. **Schedules on agents** — Delivers: `schedules.agent_id`, template columns dropped, scheduler builds tasks from the agent. Depends on: (4). Verifiable by: scheduler tests and `RunSchedule`.
6. **Agent tools** — Delivers: `kind` on `create_link`, link kinds in the first-run brief. Depends on: (4). Verifiable by: agentmcp and prompt tests.
7. **CLI and server MCP** — Delivers: `gritz agent`, `--agent` on `gritz task create`, the agent MCP tools, `agent` on `create_task` and the schedule tools. Depends on: (4), (5). Verifiable by: running them against the dev stack.
8. **Web UI** — Delivers: the Agents page with triggers, link kinds and schedules, agent picker on create-task, kind and claim on the task's links, the new test-event report; Routing Rules and Schedules pages removed. Depends on: (7). Verifiable by: exercising the pages against the dev stack; `pnpm lint`.
9. **Docs** — Delivers: `concepts/routing-rules` replaced by an agents page; event types, CLI, MCP and agent-tools pages updated. Depends on: (8). Verifiable by: `cd docs && pnpm build`.

Layer 4 is large because the router, links and tasks change together and there is no compatibility path. It can be split by landing the router behind the new types first while `orgs.routing_rules` is still read, but that would be a dual code path, which the project avoids.

## Trade-offs

- **Event Workflows.** Instances, buses, `start` modes, `emit` and exports solved inter-task coordination too, at the cost of a new execution model. Agents solve the routing problems with the concepts that already exist (rules, links, schedules), regrouped. Task-to-task events can be added later as a source that triggers and link kinds match.
- **First match within an agent, independent across agents.** Keeps a single agent's behaviour as predictable as today's rule list, while removing cross-job interference.
- **Link kinds fixed by the agent, not chosen by the task.** The agent author decides what a task can subscribe to and which claims it can hold; the task only picks a kind. A task can't subscribe to arbitrary events or claim resources the author didn't intend.
- **Integrations own link types.** Replaces a single URL normalizer that had to know every integration with one function per resource next to the event types that produce it. Validation can then check that a link kind's events can actually name its resource.
- **Claims last until archive.** A task passes through `COMPLETED` between runs, so terminal status can't release a claim. Archiving (manual or auto-archive) or deleting the link releases it.
- **Yield drops the event.** Forwarding a yielded event to the holder would bypass the holder's link kind; the holder already hears what its kind lists.

## Open Questions

1. **Deleting an agent with unarchived tasks.** Reject with `FailedPrecondition`, or archive its tasks?
2. **Editing a link kind.** Events are read from the current kind, but the claim is copied at link creation. Should changing a kind's claim rewrite existing links' claims?
3. **Delivering without waking.** Rules had `Wakeup: false` to attach an event without starting a run. Is a per-kind `wake` flag needed, or does every delivery wake?
4. **Validating `yield_to`.** Should a claim name in `yield_to` have to exist on some agent's link kind in the org?
5. **Cutover.** Live tasks lose their subscriptions because their links have no kind. Acceptable for our deployment, or should the migration assign kinds to existing links of the `default` agent?
6. **Removing links** (#1853). With claims, a mistaken link blocks other agents' triggers until archive; a `delete_link` tool becomes more important.
