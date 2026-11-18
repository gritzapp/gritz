# Inject the Agent Config Location via a `ConfigStore`

Issue: https://github.com/icholy/gritz/issues/1301

## Problem

`internal/agent/config.go` declares the task config file location as a mutable
package-level variable:

```go
var ConfigDir = "/tmp/gritz"
```

`ConfigPath`, `LoadConfig`, and `SaveConfig` all read this global to derive the
per-task path `"/tmp/gritz/{taskID}.json"`. Because the location is a mutable
global, `setupDriver` in `internal/agent/driver_test.go` overrides it and
restores it on cleanup:

```go
dir := ConfigDir
ConfigDir = t.TempDir()
t.Cleanup(func() { ConfigDir = dir })
```

That shared mutable state is why the whole driver test file must run serially —
its own helper comment says so:

> The tests mutate the global ConfigDir, so they must not run in parallel.

Adding `t.Parallel()` today would let one test's `t.TempDir()` leak into another
through the global, so every `setupDriver`-based test (nine of them) is stuck
running one at a time.

Two constraints shape the fix:

1. **The config file is read-write state, not just input.** The driver persists
   `Started` and `SetupCommandsCompleted` back via `SaveConfig` — in
   `runAgent` after the prompt returns and after every setup command in `setup`
   (`internal/agent/driver.go`). A container restart/resume re-reads the file to
   pick up where it left off. So the location must stay stable and re-writable
   for the life of a run; it cannot be a value handed to the driver once at
   construction and then discarded.

2. **The same location straddles the runner/driver boundary.** The location is
   an *in-sandbox* path used by two different processes:
   - The **runner** (host) names the destination when it packages the config
     into the sandbox — `internal/runner/runner.go` builds the `backend.File`
     entries from `agent.ConfigPath(task.ID)`:
     ```go
     Files: []backend.File{
         {Path: path.Dir(agent.ConfigPath(task.ID)), Mode: 0777, Dir: true},
         {Path: agent.ConfigPath(task.ID), Data: cfgData, Mode: 0666},
     },
     ```
   - The **driver** (in-container) reads and rewrites the file at that same
     path via `LoadConfig`/`SaveConfig`.

   The two processes never share a live variable — they agree by both defaulting
   to `/tmp/gritz`. Any fix must preserve that agreement: it is a fixed
   convention, not per-run runtime state to be threaded across the boundary.

The only agent-package callers of `ConfigPath`/`LoadConfig`/`SaveConfig` are
`driver.go`, `runner.go`, and `driver_test.go`, so the migration surface is
small and fully enumerated. (`command/workspaces.go` and `runner.go` also call a
`LoadConfig`, but that is `workspace.LoadConfig` in a different package —
unrelated.)

## Design

Replace the mutable global with a small value type that carries the directory
and owns the path/load/save logic, and give the driver a field holding one. The
default location becomes an immutable constant that both sides construct from.

### 1. `ConfigStore` type + `DefaultConfigDir` constant

In `internal/agent/config.go`:

```go
// DefaultConfigDir is the in-sandbox location of the task config file. The
// runner writes the file into the sandbox here and the driver reads and
// rewrites it here; it is a fixed convention shared across the runner/driver
// boundary, not runtime state.
const DefaultConfigDir = "/tmp/gritz"

// ConfigStore reads and writes the per-task config file rooted at Dir.
type ConfigStore struct {
    Dir string
}

// NewConfigStore returns a store rooted at DefaultConfigDir.
func NewConfigStore() ConfigStore {
    return ConfigStore{Dir: DefaultConfigDir}
}

// Path returns the config file path for the given task ID.
func (s ConfigStore) Path(taskID int64) string {
    return filepath.Join(s.Dir, fmt.Sprintf("%d.json", taskID))
}

func (s ConfigStore) Load(taskID int64) (*Config, error) { /* current LoadConfig body, using s.Path */ }
func (s ConfigStore) Save(taskID int64, cfg *Config) error { /* current SaveConfig body, using s.Path */ }
```

`Save` keeps the existing write-to-`.tmp`-then-atomic-`rename` behavior and the
`MkdirAll(filepath.Dir(path), 0o777)` — nothing about the persistence semantics
changes, only where the directory comes from.

`ConfigStore` is a plain value (a single string field), so it is cheap to copy
and safe to store on the driver. No pointer, no shared mutable state.

### 2. Driver holds a `ConfigStore`

Add a field to `Driver` (`internal/agent/driver.go`):

```go
type Driver struct {
    TaskID int64
    Client gritzclient.Client
    Log    *slog.Logger
    Config ConfigStore // where the task config file lives
    // ...ServerURL, Token unchanged...
}
```

`runAgent` and `setup` swap the package functions for the field:

```go
cfg, err := d.Config.Load(d.TaskID)
// ...
if err := d.Config.Save(d.TaskID, cfg); err != nil { ... }
```

Because the store lives on the driver and is re-used for every `Save`, constraint
(1) is satisfied directly: the same stable location backs the initial load, each
per-command `Save` in `setup`, and the final `Save` in `runAgent`.

### 3. Command wiring defaults it

`internal/command/driver.go` constructs the driver, so it supplies the default:

```go
driver := &agent.Driver{
    TaskID: cmd.Int64("task"),
    Client: gritzclient.New(/* ... */),
    Log:    slog.Default(),
    Config: agent.NewConfigStore(),
    // ...
}
```

The production driver always runs with `DefaultConfigDir`; only tests inject a
different directory. No new flag or env var is introduced — the location is not
meant to be operator-configurable (see Trade-offs).

### 4. Runner names the path via the store

`internal/runner/runner.go` swaps the free function for a store constructed at
the default:

```go
store := agent.NewConfigStore()
// ...
Files: []backend.File{
    {Path: path.Dir(store.Path(task.ID)), Mode: 0777, Dir: true},
    {Path: store.Path(task.ID), Data: cfgData, Mode: 0666},
},
```

The runner only *names* the path (it already marshals `cfgData` itself), so it
needs `Path` but not `Load`/`Save`. Both sides now derive the path from the same
`DefaultConfigDir` constant, preserving the cross-process agreement that
constraint (2) requires.

### 5. Remove the global and the dead `Tar` method

Once all callers use the store, delete `var ConfigDir` and the package-level
`ConfigPath`/`LoadConfig`/`SaveConfig` functions.

`(*Config).Tar` (`internal/agent/config.go`) also reads the global, but it has
**no callers anywhere in the tree** — it is a leftover from an earlier packaging
path that the current `backend.File` approach replaced. Delete it as part of
this change rather than porting a dead method onto the store.

### 6. Tests run in parallel

`setupDriver` injects a per-test store and drops the global save/restore:

```go
func setupDriver(t *testing.T, cfg *Config) (*Driver, *gritzclient.ClientMock) {
    t.Helper()
    store := ConfigStore{Dir: t.TempDir()}
    assert.NilError(t, store.Save(1, cfg))
    mock := &gritzclient.ClientMock{ /* unchanged */ }
    return &Driver{TaskID: 1, Client: mock, Log: slog.Default(), Config: store}, mock
}
```

Every test that calls `setupDriver` adds `t.Parallel()`. With each run rooted at
its own `t.TempDir()` and nothing touching package state, the file's
non-parallel comment is removed and the tests run concurrently, race-clean.

## Implementation Plan

Each slice is independently reviewable, and 2–4 are independently mergeable once
1 lands (2 and 3 don't depend on each other). Slice 1 is inert: it adds the new
type while leaving the old global and functions in place, so nothing else has to
change in the same PR.

1. **Add `ConfigStore` + `DefaultConfigDir`.** Delivers: the `ConfigStore` type
   with `Path`/`Load`/`Save`, `NewConfigStore()`, and the `DefaultConfigDir`
   constant. The existing `var ConfigDir` and the `ConfigPath`/`LoadConfig`/
   `SaveConfig` functions stay, reimplemented as thin delegations to
   `ConfigStore{Dir: ConfigDir}` so every current caller and the existing tests
   behave identically. Depends on: nothing. Verifiable by: a new `config_test.go`
   round-trips `Save`→`Load` against a `t.TempDir()` store, running with
   `t.Parallel()` and touching no global; the package builds and existing tests
   pass unchanged.

2. **Driver uses the store; tests go parallel.** Delivers: the `Config
   ConfigStore` field on `Driver`, `runAgent`/`setup` switched to
   `d.Config.Load`/`d.Config.Save`, command wiring setting
   `Config: agent.NewConfigStore()`, and `setupDriver` rewritten to inject
   `ConfigStore{Dir: t.TempDir()}` with `t.Parallel()` added to each test.
   Depends on: (1). Verifiable by: `go test -race ./internal/agent` passes with
   the driver tests running in parallel.

3. **Runner names the path via the store.** Delivers: `runner.go` using
   `agent.NewConfigStore().Path(task.ID)` instead of `agent.ConfigPath(task.ID)`
   for the two `backend.File` entries. Depends on: (1). Verifiable by: a runner
   spec test asserts the two `Files` paths are `/tmp/gritz/{id}.json` and its
   parent dir — unchanged from today.

4. **Remove the global and dead code.** Delivers: deletion of `var ConfigDir`,
   the package-level `ConfigPath`/`LoadConfig`/`SaveConfig` delegations, and the
   unused `(*Config).Tar` method. Depends on: (2) and (3). Verifiable by:
   `grep -rn 'ConfigDir\|ConfigPath\|LoadConfig\|SaveConfig' internal/agent`
   shows no references to the removed symbols (only `ConfigStore`/`DefaultConfigDir`);
   the full test suite passes.

## Trade-offs

- **`ConfigStore` type vs. a bare `ConfigDir string` on `Driver`.** A single
  string field on the driver is even smaller, but the runner has no `Driver` —
  it would need a separate free `ConfigPath(dir, taskID)` function, splitting the
  path-derivation logic between a driver method and a package function. The store
  keeps all three operations (`Path`/`Load`/`Save`) in one type that both the
  driver and the runner construct, which is exactly the "consistent home for both
  readers and writers" the second constraint calls for. Chosen for that single
  source of truth.

- **Injecting a store vs. passing the loaded `*Config` in at construction.**
  Handing the driver a pre-loaded `*Config` would not satisfy constraint (1):
  the driver must re-`Save` `Started`/`SetupCommandsCompleted` to a stable path
  across restarts, so it needs the *location*, not just the current contents.
  And the runner would still need the path independently. Rejected.

- **`ConfigStore` vs. an `fs.FS`/filesystem abstraction (e.g. afero).** The only
  axis that varies is the directory; `Save` also needs `MkdirAll` plus an atomic
  temp-file rename, which `fs.FS` does not model. A concrete dir-rooted struct is
  simpler and enough. Rejected as over-engineering.

- **Keeping the global but serializing access (mutex / `t.Setenv`-style).** That
  would still serialize the tests — the opposite of the goal — and leaves shared
  mutable package state in place. Rejected.

- **No operator-facing flag/env for the location.** `DefaultConfigDir` stays a
  constant. The path is an in-sandbox convention the runner and driver must
  agree on; exposing a knob would create a way for the two sides to disagree with
  no benefit. Tests are the only consumer that needs a non-default value, and
  they get it by constructing the store directly.

## Open Questions

- **Field type: `Config ConfigStore` vs. `ConfigDir string` on `Driver`.** The
  design uses a `ConfigStore` field. An alternative is a `ConfigDir string` on
  the driver from which it builds a store internally, which keeps the driver's
  public surface a plain string. Proposed: the `ConfigStore` field, since the
  runner constructs the same type and it keeps one home for the logic — but this
  is a small ergonomic call worth confirming in review.

- **Delete `Tar` now or in a follow-up.** It is dead today, so this proposal
  folds its removal into slice 4. If a future packaging path wants it back, it
  should return as a `ConfigStore` method rather than a `*Config` method reading
  a global. Flagging in case anyone knows of an out-of-tree caller.