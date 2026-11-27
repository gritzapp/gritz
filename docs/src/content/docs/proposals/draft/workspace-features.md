---
title: "Workspace features"
description: "A workspace gives every task the same environment, secrets, MCP servers, commands and agent harness, so a tool and the credentials it needs can't be declared once and given only to the agents that use it."
---

Issue: https://github.com/gritzapp/gritz/issues/2383

## Problem

A workspace in `workspaces.yaml` gives every task the same secrets, environment,
MCP servers, setup commands and agent harness. Every
[agent](/proposals/implemented/agents/) on that workspace gets all of them. Two
examples of what that forces today:

- To give a Jira MCP server and its token only to the agent that triages Jira
  issues, an operator has to copy the whole workspace under a second name and add
  the server there.
- To run one agent on Codex instead of Claude Code, the operator has to copy the
  whole workspace again.

The copies then drift apart.

The issue settles the shape:

- They are called **features**.
- A feature is declared in `workspaces.yaml`, inside a workspace, and can contain
  environment variables, secrets, MCP servers and commands.
- The workspace keeps its own base environment, secrets, MCP servers and
  commands. Features add to it.
- An agent names the features its tasks get, choosing freely among its
  workspace's features. There is no allow-list.

In addition, a feature may replace the workspace's agent config (the harness and
its settings). This proposal settles the rest: how the server learns about
features, what happens when names collide, the order commands run in, how
features reach the sandbox, and how schedules, manual tasks and the web UI get
them.

## Design

### Overview

- A workspace declares `features:`, a mapping of name to feature. A feature has
  `description`, `environment`, `secrets`, `mcp_servers`, `commands` and an
  optional `agent`.
- A feature's `agent:` has the same shape as the workspace's `agent:`, and
  replaces the workspace's agent config as a whole.
- The runner reports each workspace's features in `RegisterWorkspaces`, beside
  its variable declarations: name and description. The server stores them on
  the `workspaces` row.
- An agent has `features`, an ordered list of names. `CreateAgent` and
  `UpdateAgent` reject a name the agent's workspace doesn't declare. That is the
  only check.
- A task copies its agent's features when it is created, as it copies the
  variables. This applies to every way a task is created: triggers, schedules and
  `CreateTask`.
- The runner gets the task's feature names from `GetRunSpecInputs` and merges
  the workspace base, then each feature in the order the agent lists them, into
  one ordinary workspace for the run. Where two define the same name, the later
  one wins:
  - environment and secrets into the workspace's environment and secrets;
  - MCP servers into the agent config;
  - the agent config itself: the last listed feature with `agent:`, or the
    workspace's if none has one;
  - commands run in the same order: the workspace's, then each feature's.
- Nothing is checked for conflicts when the runner loads the file, and no names
  are reserved. The people who write `workspaces.yaml` and agents decide what a
  collision means, through the order of the agent's list.

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
    agent:                    # required; what a task gets unless a feature replaces it
      type: claude
      cwd: /root/gritz
      mcp_servers:
        github:
          type: stdio
          command: gritz
          args: [tool, github-mcp]

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
      codex:
        description: Run on Codex instead of Claude Code
        secrets:
          OPENAI_API_KEY: ${env:OPENAI_API_KEY}
        commands:
          - mise use -g node@22 npm:@openai/codex
        agent:      # replaces the workspace's agent: as a whole
          type: codex
          cwd: /root/gritz
          codex:
            model: gpt-5-codex
          mcp_servers:
            github:   # the base's github server doesn't carry over, so it is repeated
              type: stdio
              command: gritz
              args: [tool, github-mcp]
```

- Feature names are the mapping's keys.
- `environment` is a backend-agnostic environment map. Workspaces have no such
  base field: their environment is `container.environment` or
  `lambda_microvm.environment`. A feature's `environment` is set in both (see
  [Reaching the sandbox](#reaching-the-sandbox)), so one feature serves every
  backend.
- `secrets` works exactly like the workspace's `secrets:`. Each entry becomes an
  environment variable, and its value is masked in the shipped log.
- `mcp_servers` takes the same entries as `agent.mcp_servers`. They are added to
  whichever agent config the task ends up with.
- `commands` are setup commands, like the workspace's `commands:`.
- `agent` takes everything the workspace's `agent:` takes: `type`, `cwd`,
  `prompt`, `verbose`, `mcp_servers` and the per-harness sections (`claude`,
  `codex`, …). `Agent.Validate` checks it the same way, and a feature's
  `mcp_servers` are checked like the workspace's. The workspace's own `agent:`
  stays required: it is what a task gets when none of its features has one.
- Values go through the same `${env:}`/`${sh:}` expansion as the rest of the
  file, when the runner loads the file. Every feature is expanded, including
  features no task uses. A `${sh:}` in a feature runs at runner start, like the
  ones in the base.
- The order of the `features:` mapping has no effect. The agent's list sets the
  order (see [Merge order](#merge-order)).

### Replacing the agent config

A task's agent config, the `agent.Config` the runner writes into the sandbox, is
built from:

- **The base:** the `agent:` of the last feature in the agent's list that has
  one, or the workspace's `agent:` if none has. A feature's `agent:` replaces
  the workspace's as a whole. There is no field-by-field merge. Nothing from the
  workspace's `agent:` carries over: not `type`, `cwd`, `prompt`, `verbose`, the
  per-harness section, or `mcp_servers`. A feature that wants one of the
  workspace's MCP servers repeats it, as `codex` does above.
- **The MCP servers:** the base's `mcp_servers`, then the feature-level
  `mcp_servers:` of each of the task's features in the agent's order, including
  the feature that supplied the base. A later server replaces an earlier one of
  the same name.
- **The `gritz` server:** injected last, as today. It replaces any server named
  `gritz`.
- **The commands and capabilities:** the commands are the workspace's
  `commands`, then the features' (see [Command order](#command-order)). The
  capabilities are the workspace's `capabilities`. Both are workspace-level, not
  part of `agent:`, so replacing the agent config doesn't change them.

Several features with `agent:` can be declared in one workspace, and an agent
can list more than one of them. The last one listed supplies the agent config;
the earlier ones still contribute their environment, secrets, MCP servers and
commands.

### Merge order

A task's features are merged onto the workspace base in the order its agent
lists them. Where two define the same name, the later one wins. Nothing is
rejected when the runner loads the file.

- **Environment and secrets.** A feature's `environment` and `secrets` replace a
  variable of the same name from the workspace (`secrets`,
  `container.environment`, `lambda_microvm.environment`) or from an earlier
  feature. The name takes the kind the later feature gives it: a name a feature
  sets in `environment` is no longer a secret, and a name it sets in `secrets`
  is masked in the shipped log.
- **The variables gritz sets** (`GRITZ_TASK_ID`, `GRITZ_SERVER`, `GRITZ_TOKEN`
  and `GRITZ_VAR_<NAME>`) take precedence over a feature's `environment`, as
  they do over `container.environment` and `lambda_microvm.environment`. A
  feature's secret of the same name replaces them, as a workspace secret does
  today.
- **MCP servers.** As in [Replacing the agent config](#replacing-the-agent-config):
  the base agent config's servers, then each feature's `mcp_servers:`, later
  wins, then `gritz`.
- **The agent config.** The last listed feature with `agent:`.

Today's [environment precedence](/operators/workspaces-yaml/#environment)
otherwise holds. Features' secrets join the workspace's at step 1, and their
environment joins `lambda_microvm.environment` at step 2 on Lambda MicroVMs and
`container.environment` at step 3 on Docker.

### Command order

The workspace's own `commands` run first, then each of the task's features'
commands, in the order its agent lists them. Within a feature, its commands run
in order. A feature with `agent:` is no exception: its commands run in its
position in the list, before the harness it installs is started.

This is the same order as the rest of the merge. Dependencies between features
are the agent author's to order: in the example above, `playwright` needs
`node`'s pnpm, so an agent lists `[node, playwright]`. The base runs first
because features layer on top of the workspace, for example tooling inside the
repository the base clones.

The runner appends the commands to the workspace's `commands`, and from there
they reach `agent.Config.Commands`. Setup resumption
(`SetupCommandsCompleted`) indexes into that concatenated list. The list is
stable for a task because the task's features are fixed at creation. Editing
`workspaces.yaml` can still change it, as editing the base `commands:` does
today.

### How the server learns about features

**Recommendation: register them with the workspace, as variables are.** Today
`TaskManager.RegisterWorkspaces` sends each workspace's `VariableDecl`s in
`RegisteredWorkspace.variables`. The server validates them, stores them in
`workspaces.variables`, and `validateAgent` checks an agent's variables against
`GetWorkspaceVariables`. Features follow the same path:

- `RegisteredWorkspace` gains `repeated FeatureDecl features`, sorted by name.
  Each `FeatureDecl` has the feature's name and its description. The contents of
  a feature stay on the runner. Secrets, commands and agent configs never leave
  it, and the server needs nothing more to validate a selection.
- `RegisterWorkspaces` stores the declarations in `workspaces.features`.
- `store.GetWorkspaceVariables` becomes `store.GetWorkspace`, which returns the
  registered `model.Workspace` with both its variables and its features.
- `validateAgent` calls `Workspace.CheckFeatures(agent.Features)`. It returns
  `InvalidArgument` if a name isn't declared, listing the declared ones.
- `ListWorkspaces` returns the features, so the CLI, the MCP tools and the web UI
  can show them, and the agent editor can offer them.

### Agents, schedules and manual tasks

- `Agent` gains `repeated string features`, stored in `agents.features`. Its
  order is the merge order.
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
  `CheckFeatures`, and fails with `FailedPrecondition` if a feature was removed
  from the workspace since. `CreateTaskRequest` gets no `features` field. A task
  that needs a different set of features belongs to a different agent.

Triggers and schedules don't check the features again when they create a task,
just as they don't resolve variables. The runner checks both when it starts the
run.

### Reaching the sandbox

**Recommendation: the task carries feature names only. The runner fetches them
with the run's other server-side inputs and folds the features into a copy of
the workspace. Nothing after that knows about features.**

1. `GetRunSpecInputsResponse` gains `repeated string features`, the task's
   features, next to `variables`.
2. `TaskManager.specs` calls `ws.WithFeatures(inputs.Features)` as soon as it
   has the inputs, and builds the run from the workspace it returns. A name the
   workspace doesn't declare fails the run, like a variable that no longer
   resolves, for example `workspace "gritz": unknown feature "jira"`.
3. `WithFeatures` deep-copies the workspace with `Workspace.Clone`, so the
   loaded config is never changed, and applies each feature in order, later
   wins, to the copy's own fields:
   - `environment` into `container.environment`, and into
     `lambda_microvm.environment` when the workspace has `lambda_microvm:`;
     `secrets` into `secrets`. Feature by feature, a name set in one is removed
     from the other, so the later feature decides both the value and whether it
     is a secret.
   - `agent:`: the last feature that has one replaces the workspace's `agent:`.
     Each feature's `mcp_servers` are then merged into that agent's
     `mcp_servers`.
   - `commands` are appended to the workspace's `commands`.

The result is an ordinary workspace, so everything downstream goes through
paths that already exist, with no feature-specific code: `buildSpecs` builds the
agent config with `AgentConfig()`, injects the `gritz` server last, and sends
`secrets` in `RunSpec.secrets`, and each backend reads the environment from the
section it already reads. The driver reads the harness type from the agent
config file, so a feature's `agent.type` needs nothing new in the driver.

**Environment per backend.** A workspace's environment lives in its backend's
section, so that is where a feature's goes:

- **Docker** sets `container.environment` on the container when it creates
  it.
- **Lambda MicroVMs** add `lambda_microvm.environment` to every run's
  environment, under the variables gritz sets.
- **The local backend** (tests and development) reads no workspace environment
  at all, neither the base's nor a feature's. A feature's `secrets` still reach
  it, in `RunSpec.secrets`. If a test needs a feature's `environment` there, the
  smallest change is for the local backend to return `container.environment`
  from `RunEnv`, as Lambda MicroVMs return theirs, which serves the base and the
  features alike.

**Masking.** Feature secrets are merged into the workspace's `secrets`, so they
travel in `RunSpec.secrets` and the driver handles them identically:

- `driverserver.Env` adds them to the run's environment;
- `driverserver.Secrets` hands them to the log shipper;
- the shipper's `redact.Writer` masks them as `[gritz:masked NAME]`.

There is no separate code path that could miss them. A feature that swaps the
harness, like `codex`, brings that harness's credential as a secret, so the
credential is masked like any other. A feature secret that replaces a workspace
secret of the same name is masked under that name; the replaced value is no
longer in the run. The task token's mask is safe too: `driverserver.Secrets`
already writes `token` last.

**Lifetimes** follow the fields a feature is folded into:

- A feature's `secrets` are sent with every run, so an edited value reaches
  existing tasks on their next run. So is its `environment` on Lambda
  MicroVMs.
- Its `environment` on Docker, and its `mcp_servers`, `commands` and `agent`,
  are set up with the sandbox. An edit applies from the next fresh sandbox (a
  new task, or a reset), as edits to `container.environment`, the workspace's
  `agent:` and `commands` do today. A task created with a harness-replacing
  feature keeps that harness for as long as its sandbox lives.

### Schema

New migration:

```sql
-- migrate:up

-- Workspace features. See proposals/draft/workspace-features.md.
-- The workspace's feature declarations as registered, a JSON array of
-- {name, description}.
ALTER TABLE workspaces ADD COLUMN features jsonb NOT NULL DEFAULT '[]';
-- The agent's feature names in merge order, a JSON array.
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
  // The workspace's features, sorted by name.
  repeated FeatureDecl features = 6;  // new
}

message Agent {
  // ...
  // Features of its tasks, by name. Each must be declared by the workspace.
  // They are merged onto the workspace in this order: where two define the
  // same environment variable, secret or MCP server, or both have their own
  // agent config, the later one wins.
  repeated string features = 10;  // new
}

message Task {
  // ...
  // The task's features, copied from its agent when it was created, in merge
  // order. Their environment, secrets, MCP servers and commands are added to
  // the workspace's in its sandbox, and the last one with an agent config
  // replaces the workspace's.
  repeated string features = 21;  // new
}

message GetRunSpecInputsResponse {
  // ...
  repeated string features = 3;  // new: the task's features, as in Task.features
}
```

`CreateAgent`'s and `UpdateAgent`'s comment gains "or names a feature the
workspace doesn't declare". `CreateTask` fails with `failed_precondition` when
one of the agent's features is no longer declared by its workspace.

### CLI, MCP and web UI

- `gritz workspaces` prints each workspace's features: their environment,
  secrets (by name, with masked values), MCP servers, commands and agent config.
- `gritz task` output shows a task's features.
- Server MCP: `put_agent`'s description documents `features` and that their
  order is the merge order, later wins. `list_agents`'s and `create_task`'s
  descriptions say a task gets the agent's features. `list_workspaces` returns
  them.
- **Web UI agent editor** (`agent-form.tsx`): a Features section below the
  variables.
  - It lists the features of the chosen workspace, from `ListWorkspaces`, with
    their descriptions, each as a checkbox.
  - The order is the order they are checked in: checking a feature appends it
    to the list. A checked feature shows its position. To move one to the end,
    uncheck and check it again.
  - Changing the runner or workspace clears the selection, as it clears the
    variables.
  - A saved feature the workspace no longer declares is shown as such, so it can
    be unchecked. The server rejects the save until it is.
- **Web UI views**: the workspaces page lists each workspace's features and
  their descriptions. The task sidebar shows the task's features, in order, next
  to its variables.

### API

```go
package workspace // github.com/gritzapp/gritz/internal/runner/workspace

type Workspace struct {
	// ...
	Features Features `yaml:"features,omitempty"` // new
}

// Feature is a features: entry; the map key is the feature's name. // new
type Feature struct {
	Description string                     `yaml:"description"`
	Environment map[string]string          `yaml:"environment"`
	Secrets     map[string]string          `yaml:"secrets"`
	McpServers  map[string]agent.McpServer `yaml:"mcp_servers"`
	Commands    []string                   `yaml:"commands"`
	// Agent, when set, replaces the workspace's agent config as a whole.
	Agent *Agent `yaml:"agent,omitempty"`
}

// Features are a workspace's features by name. // new
type Features map[string]Feature

// Decls returns the features' declarations, sorted by name. // new
func (f Features) Decls() []model.FeatureDecl

// Clone returns a deep copy of w, which a caller can change without touching w. // new
func (w *Workspace) Clone() *Workspace

// WithFeatures returns a copy of w with the named features applied in order, later wins: their environment and secrets in the workspace's, the last agent: in Agent, their MCP servers in Agent.McpServers and their commands after Commands. It fails on a name w doesn't declare. // new
func (w *Workspace) WithFeatures(names []string) (*Workspace, error)

// Validate also checks each feature's MCP servers and agent config. // changed
func (w *Workspace) Validate() error
```

```go
package model // github.com/gritzapp/gritz/internal/model

// FeatureDecl is a feature a workspace declares. // new
type FeatureDecl struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// FeatureDeclsProto converts decls to their protobuf representation. // new
func FeatureDeclsProto(decls []FeatureDecl) []*gritzv1.FeatureDecl

// FeatureDeclsFromProto converts protobuf feature declarations to the model type. // new
func FeatureDeclsFromProto(pbs []*gritzv1.FeatureDecl) []FeatureDecl

type Workspace struct {
	// ...
	Features []FeatureDecl `json:"features,omitempty"` // new
}

// CheckFeatures fails unless every name is a feature w declares. // new
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
package store // github.com/gritzapp/gritz/internal/store

// GetWorkspace returns a runner's registered workspace with its variables and features, or sql.ErrNoRows. // new
func (s *Store) GetWorkspace(ctx context.Context, tx *sql.Tx, runnerID, name string, orgID int64) (*model.Workspace, error)

// removed: GetWorkspaceVariables
```

**How it fits:**

- `LoadConfig` decodes and validates `features:`. `TaskManager.RegisterWorkspaces`
  sends `Features.Decls()`, and the server's `RegisterWorkspaces` stores them.
- `validateAgent` and `CreateTask` call `store.GetWorkspace`, then
  `taskvar.Resolve` and `Workspace.CheckFeatures`.
- `Router.create`, `Schedule.Task` and `CreateTask` copy `Agent.Features` onto
  the task. `GetRunSpecInputs` returns them.
- `TaskManager.specs` calls `Workspace.WithFeatures` and builds the run from the
  workspace it returns, as from any other.
- The agent editor reads `ListWorkspaces` for the chosen workspace's features
  and saves `Agent.features` in the order they were checked.

## Implementation Plan

1. **Feature config on the runner** — Delivers: `workspace.Feature` (with
   `agent`), `Features`, `Select`, and `Workspace.Validate` checking each
   feature's MCP servers and `agent:` as it checks the base's. `gritz workspaces`
   prints features, with their secret values masked. Depends on: nothing.
   Verifiable by: workspace tests for decoding, `Select` (the order of names, an
   unknown name) and the validation, and running `gritz workspaces` on a file
   with features.
2. **Features reach the sandbox** — Delivers: `GetRunSpecInputsResponse.features`,
   `Workspace.Clone`, `Workspace.WithFeatures` with the agent-config
   replacement and the environment and secret merge, and `TaskManager.specs`
   calling it. The server returns an empty list until tasks have features.
   Depends on: (1). Verifiable by: `Clone` and `WithFeatures` tests:
   - a feature's `agent:` replaces the workspace's, keeping none of its fields or
     servers, and the last of several wins;
   - feature-level servers are added on top in order, later wins;
   - env and secrets, later wins, including a name that changes kind, in both
     backends' environment;
   - command order follows the list;
   - the loaded workspace is left unchanged.

   Plus `specs` tests: the features reach the run's secrets and agent config,
   with `gritz` still injected, and an unknown feature fails the run.
   `WithFeatures` replaces `Select`.
3. **Registered feature declarations** — Delivers: `FeatureDecl` proto and model,
   `RegisteredWorkspace.features`, the `workspaces.features` column,
   `store.GetWorkspace` replacing `GetWorkspaceVariables`, storing them in
   `RegisterWorkspaces`, and features in `ListWorkspaces`. Depends on: (1).
   Verifiable by: store and handler tests.
4. **Agent features** — Delivers: `agents.features`, `Agent.features` with its
   order kept, and `CheckFeatures` in `validateAgent`. Depends on: (3).
   Verifiable by: handler tests for unknown names and the saved order.
5. **Task features** — Delivers: `tasks.features`, `Task.features`, copying in
   `Router.create`, `Schedule.Task` and `CreateTask` (with the
   `FailedPrecondition` check), and `GetRunSpecInputs` returning them. Depends
   on: (2), (4). Verifiable by: router, scheduler and `CreateTask` tests. Plus two
   e2e tests:
   - an agent with a feature that declares a secret and a command that echoes it:
     the task's shipped log shows `[gritz:masked NAME]`, and the agent config has
     the feature's MCP server;
   - an agent with a feature whose `agent:` selects the `dummy` harness: the task
     runs on it.
6. **CLI and MCP** — Delivers: features in `gritz task` output and in the server
   MCP tool descriptions and results, including `put_agent`'s `features`.
   Depends on: (5). Verifiable by: running them against the dev stack.
7. **Web UI** — Delivers: the Features section of the agent editor (a checkbox
   per feature, ordered by when it was checked, cleared on workspace change,
   undeclared saved features flagged). Also features on the workspaces page and
   the task sidebar. Depends on: (4) for the editor, (5) for the sidebar.
   Verifiable by: creating and editing an agent with features against the dev
   stack and seeing a created task show them in order; `pnpm lint`.
8. **Docs** — Delivers:
   - a `features` section in `operators/workspaces-yaml`, covering the agent-config
     replacement, the merge order, the command order and the environment list;
   - a mention in `operators/secrets`;
   - `features` on `concepts/agents`, including that their order matters.

   Depends on: (7). Verifiable by: `cd docs && pnpm build`.

Layers 3 to 5 each add one column, so the SQL above lands as three migrations,
one per layer.

## Trade-offs

- **Later wins vs. rejecting conflicts at load.**
  - Rejecting every conflict when the runner loads the file would catch
    mistakes early. But it needs a rule for every pair of features and the base,
    a separate rule for MCP servers in agent configs, reserved names, and a
    selection-dependent check for agent configs on both the server and the
    runner. It also rules out alternative features that set the same variable,
    say `CLAUDE_MODEL`.
  - Later wins trusts the people who write `workspaces.yaml` and agents, and is
    one rule for everything. The cost: a collision is silent, and the result
    depends on the order of the agent's list.
- **Agent config: replace as a whole vs. merge field by field.**
  - A merge would let a feature change only the model. But then fields would
    mean different things for different harnesses (`claude.model` next to
    `codex.model`), `type` could change under a `prompt` written for another
    harness, and the base's MCP servers would silently carry over.
  - Replacing as a whole makes a feature's `agent:` read as exactly what the task
    runs. The cost is repeating the fields a feature wants to keep, like `cwd`
    and shared MCP servers.
  - Shared servers can instead be declared once as a feature-level
    `mcp_servers:` and listed alongside.
- **Several features with `agent:`: last wins vs. at most one.** Allowing at most
  one would need the server to know which features have `agent:`, and a check on
  both the server and the runner. Last wins is the same rule as everything else.
  The cost: an earlier feature's `agent:` is silently ignored, though the rest of
  that feature still applies.
- **Commands in agent order vs. workspace order.** Workspace order would make the
  agent's list a set and keep dependencies with the workspace author, but it
  needs an order-preserving `features:` mapping and a second ordering beside the
  merge order. Agent order puts the one ordering in one place. The cost: an
  agent author lists dependencies first (`node` before `playwright`).
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
  secrets, MCP servers and commands, plus the agent config. Capabilities stay on
  the workspace and aren't part of `agent:`, so a replaced agent config keeps
  them. Moving them into features would affect token minting in
  `GetRunSpecInputs`, which this design doesn't otherwise touch.

## Open Questions

None.
