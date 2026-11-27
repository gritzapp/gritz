---
title: "Workspace features"
description: "A workspace gives every task the same environment, secrets, MCP servers and commands, so a tool and the credentials it needs can't be declared once and given only to the agents that use it."
---

Issue: https://github.com/gritzapp/gritz/issues/2383

## Problem

A workspace in `workspaces.yaml` gives every task the same secrets, environment,
MCP servers and setup commands. Every [agent](/proposals/draft/agents/) on that
workspace gets all of them. To give a Jira MCP server and its token only to the
agent that triages Jira issues, an operator today has to copy the whole workspace
under a second name and add the server there. The two copies then drift apart.

The issue settles the shape:

- They are called **features**.
- A feature is declared in `workspaces.yaml`, inside a workspace, and can contain
  environment variables, secrets, MCP servers and commands.
- The workspace keeps its own base environment, secrets, MCP servers and
  commands. Features add to it.
- An agent names the features its tasks get, choosing freely among its
  workspace's features. There is no allow-list.

This proposal settles the rest: how the server learns feature names, what happens
when names collide, the order commands run in, how features reach the sandbox,
and how schedules and manual tasks get them.

## Design

### Overview

- A workspace declares `features:`, an ordered mapping of name to feature. A
  feature has `description`, `environment`, `secrets`, `mcp_servers` and
  `commands`.
- The runner reports each workspace's feature names and descriptions in
  `RegisterWorkspaces`, beside its variable declarations. The server stores them
  on the `workspaces` row.
- An agent has `features`, a list of names. `CreateAgent` and `UpdateAgent`
  reject a name the agent's workspace doesn't declare, and reject duplicates.
- A task copies its agent's features when it is created, as it copies the
  variables. This applies to every way a task is created: triggers, schedules and
  `CreateTask`.
- The runner gets the task's feature names from `GetRunSpecInputs`. It merges the
  features into the run: environment into the run's environment, secrets into the
  run's secrets, MCP servers into the agent config, commands after the
  workspace's own.
- Names never collide. The runner refuses to load a `workspaces.yaml` where two
  features, or a feature and the workspace base, define the same environment
  variable, secret or MCP server name. Because of that, the merge doesn't depend
  on which features a task has or what order they come in.

### Configuration

```yaml
workspaces:
  gritz:
    description: The gritz repo
    secrets:
      CLAUDE_CODE_OAUTH_TOKEN: ${env:CLAUDE_CODE_OAUTH_TOKEN}
    container:
      image: ghcr.io/gritzapp/workspace-debian:latest
    commands:
      - git clone https://github.com/gritzapp/gritz
    agent:
      type: claude
      cwd: /root/gritz

    features:
      node:
        description: Node.js 22 and pnpm
        commands:
          - mise use -g node@22 pnpm@10
      jira:
        description: Jira MCP server for the gritz site
        environment:
          JIRA_SITE: gritz.atlassian.net
        secrets:
          JIRA_API_TOKEN: ${env:JIRA_API_TOKEN}
        mcp_servers:
          jira:
            type: stdio
            command: jira-mcp
            args: [--site, "${JIRA_SITE}"]
      playwright:
        description: Playwright MCP server with a headless Chromium
        commands:   # needs node
          - pnpm dlx playwright install --with-deps chromium
        mcp_servers:
          playwright:
            type: stdio
            command: pnpm
            args: [dlx, "@playwright/mcp", --headless]
```

- Feature names are lowercase: they start with a letter, contain only `a-z`,
  `0-9`, `-` and `_`, and are at most 64 bytes long. They are unique within a
  workspace (YAML mapping keys).
- `environment` is a backend-agnostic environment map. Workspaces have no such
  base field: their environment is `container.environment` or
  `lambda_microvm.environment`. A feature can't usefully carry a
  backend-specific section, because it only reaches the sandbox through the run
  (see [Reaching the sandbox](#reaching-the-sandbox)).
- `secrets` works exactly like the workspace's `secrets:`. Each entry becomes an
  environment variable, and its value is masked in the shipped log.
- `mcp_servers` takes the same entries as `agent.mcp_servers`.
- `commands` are setup commands, like the workspace's `commands:`.
- Values go through the same `${env:}`/`${sh:}` expansion as the rest of the
  file, when the runner loads the file. Every feature is expanded, including
  features no task uses. A `${sh:}` in a feature runs at runner start, like the
  ones in the base.
- The order of the `features:` mapping is significant: it is the order the
  commands of the selected features run in (see [Command order](#command-order)).
  `workspace.Features` decodes the mapping through a `yaml.Node` to keep that
  order.

### Name conflicts

**Recommendation: reject every conflict when the runner loads the file, whatever
any agent selects.** `Workspace.Validate` fails, and with it `LoadConfig` and
`gritz workspaces`, when:

- **Environment and secrets.** An environment variable name is defined twice
  across the workspace's `secrets`, `container.environment` and
  `lambda_microvm.environment`, and every feature's `environment` and `secrets`.
  This also covers a name that one feature sets as `environment` and another as
  a secret, and a name set as both within one feature. The base's own
  `container.environment` / `secrets` overlap is not checked (unchanged).
- **Reserved names.** A feature's `environment` or `secrets` defines a name that
  starts with `GRITZ_`. That prefix holds `GRITZ_TASK_ID`, `GRITZ_TOKEN`,
  `GRITZ_SERVER` and the `GRITZ_VAR_` task variables.
- **MCP servers.** An MCP server name is defined twice across
  `agent.mcp_servers` and every feature's `mcp_servers`, or a feature or the base
  names a server `gritz`. The runner injects its own `gritz` server and today
  silently replaces one of that name. The base now rejects it too.

The checks cover every pair of features, not just pairs some agent selects
together. Any combination is then conflict-free, so:

- the server can accept any set of declared names without knowing what the
  features contain;
- the merged environment doesn't depend on the order of an agent's list;
- a change to `workspaces.yaml` can't make an existing agent's selection
  conflict.

Today's [environment precedence](/operators/workspaces-yaml/#environment) still
holds. A feature's `environment` sits with the variables gritz sets (step 2), and
its secrets sit with the workspace's (step 1). Since names can't collide, the
precedence never decides between a feature and the base.

### Command order

**Recommendation: the workspace's own `commands` run first, then each selected
feature's commands, in the order the features are declared in the workspace.**
Within a feature, its commands run in order.

The order of the agent's `features` list has no effect. Dependencies between
features belong to the workspace author: in the example above, `playwright`
needs `node`'s pnpm, so `node` is declared first. The author knows that; an agent
author picking `[playwright, node]` shouldn't have to. The base runs first
because features layer on top of the workspace, for example tooling inside the
repository the base clones.

The runner appends the commands to `agent.Config.Commands`. Setup resumption
(`SetupCommandsCompleted`) indexes into that concatenated list. The list is
stable for a task because the task's features are fixed at creation. Editing
`workspaces.yaml` can still change it, as editing the base `commands:` does
today.

### How the server learns feature names

**Recommendation: register them with the workspace, as variables are.** Today
`TaskManager.RegisterWorkspaces` sends each workspace's `VariableDecl`s in
`RegisteredWorkspace.variables`. The server validates them, stores them in
`workspaces.variables`, and `validateAgent` checks an agent's variables against
`GetWorkspaceVariables`. Features follow the same path:

- `RegisteredWorkspace` gains `repeated FeatureDecl features`: name and
  description, in declaration order. The contents of a feature stay on the
  runner. Secrets and commands never leave it, and the server has no use for
  them.
- `RegisterWorkspaces` rejects an invalid feature name with `InvalidArgument`, as
  it does an invalid variable declaration. It stores the declarations in
  `workspaces.features`.
- `store.GetWorkspaceVariables` becomes `store.GetWorkspace`, which returns the
  registered `model.Workspace` with both its variables and its features.
- `validateAgent` calls `Workspace.CheckFeatures(agent.Features)`. Each name must
  be declared and appear at most once; otherwise `InvalidArgument`, naming the
  unknown feature and listing the declared ones.
- `ListWorkspaces` returns the features, so the CLI, the MCP tools and the web UI
  can show them.

### Agents, schedules and manual tasks

- `Agent` gains `repeated string features`, stored in `agents.features`. Its
  order is kept as saved but has no effect.
- A task copies its agent's features when it is created, into `tasks.features`
  and `Task.features`, as it copies `variables`. Editing an agent doesn't change
  its existing tasks. A task's features can't be changed later.
- **Triggers**: `Router.create` copies `agent.Features` as saved, as it copies
  `agent.Variables`.
- **Schedules**: `Schedule.Task(agent)` copies `agent.Features`. A schedule has
  no feature field of its own. Its tasks get the features of the agent it names.
- **Manual tasks**: `CreateTask` copies `agent.Features`. It already loads the
  workspace again to resolve variables ("the workspace may have been
  unregistered or changed its declarations since"). It now also calls
  `CheckFeatures`, and fails with `FailedPrecondition` on a feature the
  workspace no longer declares. `CreateTaskRequest` gets no `features` field. A
  task that needs a different set of features belongs to a different agent.

Triggers and schedules don't check the features again when they create a task,
just as they don't resolve variables. The runner checks both when it starts the
run.

### Reaching the sandbox

**Recommendation: the task carries feature names only. The runner fetches them
with the run's other server-side inputs and merges the features into the specs
it already builds.**

1. `GetRunSpecInputsResponse` gains `repeated string features`, the task's
   features, next to `variables`.
2. `TaskManager.specs` calls `ws.Select(inputs.Features)`. It returns the named
   features in declaration order, or an error naming a feature the workspace
   doesn't declare. Like a variable that no longer resolves, that error fails
   the run with `workspace "gritz": unknown feature "jira"`.
3. `buildSpecs` takes the selected features in `specInput.Features` and merges
   them:
   - each feature's `environment` into `backend.Spec.Env`, and from there into
     `RunSpec.env`;
   - each feature's `secrets` into `backend.Spec.Secrets`, and from there into
     `RunSpec.secrets`;
   - each feature's `mcp_servers` into `agent.Config.McpServers`, before the
     `gritz` server is added;
   - each feature's `commands` appended to `agent.Config.Commands`, after the
     workspace's own.

All of this goes through paths that already exist. No backend changes:
`RunSpec.env` and `RunSpec.secrets` reach every run on every backend, and the
agent config file is provisioned as it is today.

**Masking.** Feature secrets travel in `RunSpec.secrets` next to the workspace's
own secrets, so the driver handles them identically:

- `driverserver.Env` adds them to the run's environment;
- `driverserver.Secrets` hands them to the log shipper;
- the shipper's `redact.Writer` masks them as `[gritz:masked NAME]`.

There is no separate code path that could miss them. Because names can't
collide, no feature secret can shadow a workspace secret. The task token's mask
is safe too: `driverserver.Secrets` already writes `token` last.

Lifetimes follow the base fields. A feature's `environment` and `secrets` are
sent with every run, so an edited value reaches existing tasks on their next run.
Its `mcp_servers` and `commands` are in the agent config provisioned with the
sandbox, so they apply from the next fresh sandbox (a new task, or a reset).

### Schema

New migration:

```sql
-- migrate:up

-- Workspace features. See proposals/draft/workspace-features.md.
-- The workspace's feature declarations as registered, a JSON array of {name, description}.
ALTER TABLE workspaces ADD COLUMN features jsonb NOT NULL DEFAULT '[]';
-- The agent's feature names, a JSON array.
ALTER TABLE agents ADD COLUMN features jsonb NOT NULL DEFAULT '[]';
-- The task's feature names, copied from its agent at creation, a JSON array.
ALTER TABLE tasks ADD COLUMN features jsonb NOT NULL DEFAULT '[]';

-- migrate:down

ALTER TABLE tasks DROP COLUMN IF EXISTS features;
ALTER TABLE agents DROP COLUMN IF EXISTS features;
ALTER TABLE workspaces DROP COLUMN IF EXISTS features;
```

Existing workspaces, agents and tasks have no features, which is what they
already have. The runner's SQLite state doesn't change, because the features are
fetched for each run.

### Proto

```proto
// FeatureDecl is a feature a workspace declares; see workspaces.yaml features:.
message FeatureDecl {
  string name = 1;
  string description = 2;
}

message RegisteredWorkspace {
  // ...
  // The workspace's features, in declaration order.
  repeated FeatureDecl features = 6;  // new
}

message Agent {
  // ...
  // Features of its tasks, by name. Each must be declared by the workspace,
  // at most once; the order has no effect.
  repeated string features = 10;  // new
}

message Task {
  // ...
  // The task's features, copied from its agent when it was created. Their
  // environment, secrets, MCP servers and commands are added to the
  // workspace's in its sandbox.
  repeated string features = 21;  // new
}

message GetRunSpecInputsResponse {
  // ...
  repeated string features = 3;  // new: the task's features, as in Task.features
}
```

`CreateAgent`'s and `UpdateAgent`'s comment gains "or names a feature the
workspace doesn't declare". `CreateTask` fails with `failed_precondition` when
the agent's workspace no longer declares one of its features.

### CLI, MCP and web UI

- `gritz workspaces` prints each workspace's features with their environment
  names, secret names (never values), MCP servers and commands.
- `gritz task` output shows a task's features.
- Server MCP: `put_agent`'s description documents `features`; `list_agents`'s
  and `create_task`'s descriptions say a task gets the agent's features;
  `list_workspaces` returns them.
- Web UI: the workspaces page lists each workspace's features and their
  descriptions. The task sidebar shows the task's features next to its
  variables.

### API

```go
package workspace // github.com/gritzapp/gritz/internal/runner/workspace

type Workspace struct {
	// ...
	Features Features `yaml:"features,omitempty"` // new
}

// Feature is a features: entry: what a task that selects it gets on top of the workspace. // new
type Feature struct {
	Name        string                     `yaml:"-"`
	Description string                     `yaml:"description"`
	Environment map[string]string          `yaml:"environment"`
	Secrets     map[string]string          `yaml:"secrets"`
	McpServers  map[string]agent.McpServer `yaml:"mcp_servers"`
	Commands    []string                   `yaml:"commands"`
}

// Features are a workspace's features in declaration order. // new
type Features []Feature

// UnmarshalYAML decodes a features: mapping, keeping its order. // new
func (f *Features) UnmarshalYAML(node *yaml.Node) error

// Decls returns the features' names and descriptions, in declaration order. // new
func (f Features) Decls() []model.FeatureDecl

// Select returns the named features in declaration order, failing on a name the workspace doesn't declare. // new
func (w *Workspace) Select(names []string) ([]Feature, error)

// Validate also checks feature names and rejects conflicting environment, secret and MCP server names. // changed
func (w *Workspace) Validate() error
```

```go
package runner // github.com/gritzapp/gritz/internal/runner

type specInput struct {
	// ...
	// Features are the task's features, from GetRunSpecInputs, as Workspace.Select returned them. // new
	Features []workspace.Feature
}
```

```go
package model // github.com/gritzapp/gritz/internal/model

// FeatureDecl is a feature a workspace declares. // new
type FeatureDecl struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Validate checks the feature's name. // new
func (d FeatureDecl) Validate() error

// FeatureDeclsProto converts decls to their protobuf representation. // new
func FeatureDeclsProto(decls []FeatureDecl) []*gritzv1.FeatureDecl

// FeatureDeclsFromProto converts protobuf feature declarations to the model type. // new
func FeatureDeclsFromProto(pbs []*gritzv1.FeatureDecl) []FeatureDecl

type Workspace struct {
	// ...
	Features []FeatureDecl `json:"features,omitempty"` // new
}

// CheckFeatures fails unless every name is a feature w declares, each at most once. // new
func (w *Workspace) CheckFeatures(names []string) error

type Agent struct {
	// ...
	Features []string // new
}

type Task struct {
	// ...
	Features []string `json:"features,omitempty"` // new
}
```

```go
package backend // github.com/gritzapp/gritz/internal/runner/backend

type Spec struct {
	// ...
	// Secrets are the workspace's and the task's features' declared secrets, by name. // changed (doc only)
	Secrets map[string]string
}
```

```go
package store // github.com/gritzapp/gritz/internal/store

// GetWorkspace returns a runner's registered workspace with its variables and features, or sql.ErrNoRows. // new
func (s *Store) GetWorkspace(ctx context.Context, tx *sql.Tx, runnerID, name string, orgID int64) (*model.Workspace, error)

// removed: GetWorkspaceVariables
```

**How it fits:**

- `LoadConfig` decodes and validates `features:`. `TaskManager.RegisterWorkspaces`
  sends `Features.Decls()`, and the server's `RegisterWorkspaces` validates and
  stores them.
- `validateAgent` and `CreateTask` call `store.GetWorkspace`, then
  `taskvar.Resolve` and `Workspace.CheckFeatures`.
- `Router.create`, `Schedule.Task` and `CreateTask` copy `Agent.Features` onto
  the task. `GetRunSpecInputs` returns them.
- `TaskManager.specs` calls `Workspace.Select` and hands the result to
  `buildSpecs`, which merges it into the specs.

## Implementation Plan

1. **Feature config on the runner** — Delivers: `workspace.Feature`, `Features`
   with order-preserving decoding, `Select`, and the name and conflict checks in
   `Workspace.Validate`, including `gritz` as a reserved MCP server name in the
   base. `gritz workspaces` prints features. Depends on: nothing. Verifiable by:
   workspace tests for decoding order and for each conflict, and running
   `gritz workspaces` on a file with features.
2. **Features reach the sandbox** — Delivers: `GetRunSpecInputsResponse.features`,
   `specInput.Features`, and the merge in `buildSpecs`. The server returns an
   empty list until tasks have features. Depends on: (1). Verifiable by:
   `buildSpecs` tests (env, secrets in `RunSpec.secrets`, MCP servers, command
   order), and a `specs` test where the returned features name an unknown
   feature.
3. **Registered feature declarations** — Delivers: `FeatureDecl` proto and
   model, `RegisteredWorkspace.features`, the `workspaces.features` column (the
   first of the migration's three statements), `store.GetWorkspace` replacing
   `GetWorkspaceVariables`, validation in `RegisterWorkspaces`, and features in
   `ListWorkspaces`. Depends on: (1). Verifiable by: store and handler tests.
4. **Agent features** — Delivers: `agents.features`, `Agent.features`, and
   `CheckFeatures` in `validateAgent`. Depends on: (3). Verifiable by: handler
   tests for unknown and duplicate names.
5. **Task features** — Delivers: `tasks.features`, `Task.features`, copying in
   `Router.create`, `Schedule.Task` and `CreateTask` (with the
   `FailedPrecondition` check), and `GetRunSpecInputs` returning them. Depends
   on: (2), (4). Verifiable by: router, scheduler and `CreateTask` tests, plus an
   e2e test: an agent with a feature that declares a secret and a command that
   echoes it. The task's shipped log shows `[gritz:masked NAME]`, and the agent
   config has the feature's MCP server.
6. **CLI and MCP** — Delivers: features in `gritz task` output and in the server
   MCP tool descriptions and results. Depends on: (5). Verifiable by: running
   them against the dev stack.
7. **Web UI** — Delivers: features on the workspaces page and the task sidebar.
   Depends on: (5). Verifiable by: viewing both against the dev stack;
   `pnpm lint`.
8. **Docs** — Delivers: a `features` section in `operators/workspaces-yaml`
   (including the conflict rules, the command order and the environment list),
   a mention in `operators/secrets`, and agents' `features` where agents are
   documented. Depends on: (5). Verifiable by: `cd docs && pnpm build`.

Layers 3 to 5 each add one column, so the SQL above lands as three migrations,
one per layer.

## Trade-offs

- **Reject conflicts statically vs. resolve them.**
  - *Last one wins* (in the agent's order, or the base wins) would allow two
    alternative features that both set, say, `CLAUDE_MODEL`. But a value would
    then depend on which other features an agent picked, and a feature could
    silently replace a workspace secret.
  - *Checking only the selected set when an agent is saved* would need the
    server to know every feature's names. It would also let a later
    `workspaces.yaml` change break saved agents.
  - Rejecting every conflict at load keeps the server's job to names and keeps
    the merge independent of order. The cost: alternatives must use distinct
    names, or live in separate workspaces.
- **Commands in workspace order vs. agent order.** Agent order would let each
  agent decide, but dependencies between features are a property of the
  features, which the workspace author writes. Declaration order makes the
  agent's list a set, which also makes validation and display simpler.
- **Features on the task vs. read from the agent at run time.** Reading the
  agent on each run would apply an agent edit to running tasks, which agents
  deliberately avoid for workspace, runner and variables. Copying keeps a task's
  sandbox stable and its setup-resume index valid.
- **Names in `GetRunSpecInputs` vs. in the runner event stream.** The task's
  variables already come through `GetRunSpecInputs` when a run starts. Putting
  features there too means no change to `runnerstore`, its SQLite state, or the
  runner event payloads.
- **Backend-agnostic `environment` on features only.** Adding a backend-agnostic
  base `environment:` to workspaces would be symmetrical but isn't needed for
  features, and would add a fourth place to set a base variable. Features can't
  have a backend-specific section because they reach the sandbox through the run,
  not through sandbox creation.
- **No per-task or per-schedule override.** Letting `CreateTask` or a schedule
  add features would make the agent no longer the full description of its tasks,
  and would reintroduce the template fields the agents proposal removed. A
  different set of features means a different agent.
- **No capabilities in features.** The issue scopes features to environment,
  secrets, MCP servers and commands. Capabilities stay on the workspace. Moving
  them into features would affect token minting in `GetRunSpecInputs`, which
  this design doesn't otherwise touch.

## Open Questions

None.
