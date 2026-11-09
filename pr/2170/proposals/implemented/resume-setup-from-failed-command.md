# Resume Setup From Failed Command

Issue: https://github.com/icholy/gritz/issues/751

## Problem

Container setup re-runs **every** setup command on restart, including ones that already succeeded. When a non-idempotent command (e.g. `git clone`) succeeded on a prior run, re-running it on restart fails for a spurious reason and masks the original failure — so a restart can never recover.

The setup loop in `internal/agent/driver.go:58-73` is gated by a single all-or-nothing flag:

```go
if !cfg.Setup {
    for _, command := range cfg.Commands {
        d.Log.Info("Running setup command", "command", command)
        c := exec.CommandContext(ctx, "sh", "-c", command)
        c.Stdout = os.Stdout
        c.Stderr = os.Stderr
        if err := c.Run(); err != nil {
            return fmt.Errorf("setup command failed: %w", err)
        }
    }
    cfg.Setup = true
    if err := SaveConfig(d.TaskID, cfg); err != nil {
        return fmt.Errorf("failed to save config: %w", err)
    }
}
```

`cfg.Setup` is set to `true` **only** after every command in `cfg.Commands` returns zero. If command N fails, the flag stays `false` and the next run re-executes commands `0..N` from the top — re-running every already-succeeded, non-idempotent command. The failure trace in issue #751 (task 698) shows this exactly: run 1's `git clone` succeeded and `mise install` failed (transient 403); run 2's restart re-ran from index 0 and failed at `git clone` with `fatal: destination path 'gritz' already exists and is not an empty directory`. The real failure is never reached.

## Design

### Overview

Persist a per-command progress marker alongside the existing per-task config. Save it **after each successful command**, not just at the end. On (re)start, begin the loop at the saved resume point and skip already-completed commands. Set `cfg.Setup = true` only when the last command completes (unchanged semantics for the "fully done" state).

The per-task config lives in the container's writable layer at `/tmp/gritz/<task-id>.json` (`internal/agent/config.go:13,68-70`) and is preserved across container restarts because a restart reuses the same container — see [Where progress is stored](#where-progress-is-stored) below. So the progress marker rides alongside the existing fields and survives exactly the same restart cycles that today's `cfg.Setup` survives.

### Config schema

`internal/agent/config.go:15` extends the `Config` struct with one new agent-managed field, sitting next to `Setup` / `Started`:

```go
type Config struct {
    // ... existing runner-provided fields (Commands, Cwd, McpServers, …) unchanged

    // Agent-managed state
    Setup                  bool `json:"setup,omitempty"`
    SetupCommandsCompleted int  `json:"setup_commands_completed,omitempty"`
    Started                bool `json:"started,omitempty"`
}
```

`SetupCommandsCompleted` is the count of commands that have completed successfully (equivalently, the index of the next command to run). When `SetupCommandsCompleted == len(cfg.Commands)`, setup is done.

`SaveConfig` (`internal/agent/config.go:88-108`) already writes atomically via `tmp + rename`, so partial saves under SIGKILL are impossible — the driver will either see the post-update file or the pre-update file. No changes required there. The same goes for `LoadConfig` (`internal/agent/config.go:72-86`); the new field is plain JSON.

### Reworked setup loop

`internal/agent/driver.go:58-73` becomes:

```go
// Run setup commands if not already done.
if !cfg.Setup {
    for i := cfg.SetupCommandsCompleted; i < len(cfg.Commands); i++ {
        command := cfg.Commands[i]
        d.Log.Info("Running setup command", "index", i, "command", command)
        c := exec.CommandContext(ctx, "sh", "-c", command)
        c.Stdout = os.Stdout
        c.Stderr = os.Stderr
        if err := c.Run(); err != nil {
            return fmt.Errorf("setup command %d failed: %w", i, err)
        }
        cfg.SetupCommandsCompleted = i + 1
        if err := SaveConfig(d.TaskID, cfg); err != nil {
            return fmt.Errorf("failed to save config: %w", err)
        }
    }
    cfg.Setup = true
    if err := SaveConfig(d.TaskID, cfg); err != nil {
        return fmt.Errorf("failed to save config: %w", err)
    }
}
```

Key properties:

- **Save after each command, not at the end.** The original loop saved progress once after all commands; the new loop saves once per command. The atomic-rename guarantee in `SaveConfig` means the worst case under a hard kill is "we re-run the most recent command" — which is acceptable because the user-visible bug is re-running *all* commands.
- **Resume from `SetupCommandsCompleted`.** The loop ranges from the saved index to `len(cfg.Commands)`. Already-completed commands are simply not visited. If `SetupCommandsCompleted >= len(cfg.Commands)` (e.g. the on-disk count somehow exceeds the current list), the `for` no-ops naturally and the loop falls through to set `cfg.Setup = true`. No defensive clamp is needed.
- **`cfg.Setup = true` semantics unchanged.** Only set after the last command completes; existing callers (the `setup` log line at `driver.go:54`) still mean "fully set up."
- **One final save.** Strictly redundant with the per-command save after the last command, but kept for symmetry with today's loop and to make the "fully done" transition obvious in the file (`SetupCommandsCompleted == len(Commands) && Setup == true`).

### Where progress is stored

The progress marker rides in the same per-task JSON file (`/tmp/gritz/<task-id>.json`) that already carries `cfg.Setup`. A restart is a stop-and-start of the **same container**, not a removal — so the writable layer (including `/tmp/gritz/<task-id>.json`) survives:

- `Runner.Kill` (`internal/runner/runner.go:348-382`) sends SIGTERM via `dockerx.ContainerKill` (`internal/x/dockerx/container.go:43-53`) and falls back to SIGKILL on timeout. Neither path calls `ContainerRemove`; the container is stopped, not deleted.
- `Runner.Start` (`internal/runner/runner.go:482-518`) calls `r.find` first. On a hit (`ok == true`, line 489), it reuses the existing stopped container and just `ContainerStart`s it — no fresh `r.create`, no rewrite of the cfg.
- `r.create` (`runner.go:384-480`) — the only path that writes a fresh cfg.json — runs **only** when `r.find` misses, i.e. when the container has been removed out-of-band (e.g. operator `docker rm gritz-<task-id>` or `Runner.Prune` of an archived task).

So #751's restart cycle is the container-reuse path: cfg.json persists, the workspace volume persists, and the resume index in `cfg.SetupCommandsCompleted` is exactly the right thing for the driver to pick up. The re-create branch is the genuinely-gone case where starting from `SetupCommandsCompleted: 0` is correct (because the cfg.json is freshly written by `r.create` with the zero value), so we don't need to handle it specially.

The driver reads the cfg with `LoadConfig` (`internal/agent/driver.go:45`) on every start, mutates `SetupCommandsCompleted` during setup, and persists with `SaveConfig` after each successful command.

No new storage location is introduced; no migrations are required; no server-side schema changes.

### Backward compatibility

Existing on-disk configs predate the new field. `SetupCommandsCompleted` is `omitempty`, so an older cfg.json simply has the JSON zero value (`0`) when read by the new driver:

| On-disk state (old cfg.json) | New driver interprets as | Result |
|---|---|---|
| `Setup: true` | Fully set up | Skip loop entirely (line 59 `if !cfg.Setup` short-circuits). Identical to today. |
| `Setup: false`, no `SetupCommandsCompleted` | `SetupCommandsCompleted: 0` | Start setup from index 0. Identical to today. |
| `Setup: false` + `SetupCommandsCompleted > 0` (new container) | Resume from saved index | Resumes correctly. |

The first row is the most important one — already-setup tasks must not re-run setup just because the binary has been upgraded; the leading `if !cfg.Setup` guarantees that. The second row covers mid-flight upgrades: a task that was partway through setup under the old binary will start from 0 under the new binary on next restart, which is exactly today's behaviour, so the upgrade itself never regresses anything.

No code-level migration step is needed.

### Commands-changed: not handled by design

`cfg.Commands` is **immutable for the lifetime of a container**. `Runner.create` builds the cfg from `ws.AgentConfig()` (`internal/runner/runner.go:417`) and writes it once at container-create time (`runner.go:434-437,474-475`). Subsequent `Runner.Start` calls hit the `ok == true` branch (`runner.go:489`) and never rewrite the cfg. A change to `workspaces.yaml` only affects newly created containers; an existing container keeps the `Commands` list it was created with until it's removed and re-created (at which point the cfg.json is freshly written by `r.create` with `SetupCommandsCompleted: 0`, again correct).

So there is no in-place commands-list drift for a saved `SetupCommandsCompleted` to misalign against, and the design does not need to defend against it. If that invariant is ever relaxed (e.g. a future feature refreshes `cfg.Commands` on restart, or a workspace-update flow rewrites cfg.json from outside the driver), a drift guard would be an additive follow-up — store a hash of the commands list alongside the count, and reset on mismatch. Out of scope here.

### Logging

- `d.Log.Info("Running setup command", "index", i, "command", command)` — adds `index` so the resume point is visible in logs without grepping for command text.
- `d.Log.Info("loaded config", ..., "setup_completed", cfg.SetupCommandsCompleted)` — extend the existing `loaded config` log line at `driver.go:50-56` to include the resume index.
- No new metrics. The driver doesn't currently emit metrics for setup; if we ever want to alert on "setup keeps restarting," that's a separate follow-up.

### Tests

The current driver tests (`internal/agent/driver_test.go`) only cover `cfg.prompt()`. The reworked loop benefits from a small extraction so it is testable without booting a container — e.g. a method `(*Driver).runSetup(ctx context.Context, cfg *Config) error` (or a free function `runSetupCommands(ctx, log, taskID, cfg) error`), called from `Driver.Run`. With that in place, the test plan is:

1. **Mid-list failure leaves the right resume index.** Configure `Commands: ["true", "true", "false", "true"]`; run the loop with a temp `ConfigDir`. Assert it returns an error from command index 2, and the on-disk cfg has `SetupCommandsCompleted == 2` and `Setup == false`.

2. **Restart resumes from the saved index.** Seed a cfg with `SetupCommandsCompleted: 2` and `Commands: ["fail-if-run-1", "fail-if-run-2", "touch /tmp/marker3", "touch /tmp/marker4"]`. Run the loop. Assert commands 0 and 1 are not executed (no error from "fail-if-run-*"), commands 2 and 3 are, and the final cfg has `Setup == true` and `SetupCommandsCompleted == 4`.

3. **Backward-compat: `cfg.Setup == true`.** Seed a cfg with `Setup: true` and no `SetupCommandsCompleted`; assert the loop body is skipped entirely (no commands executed even if `Commands` contains a failing command).

4. **Backward-compat: `cfg.Setup == false`, no `SetupCommandsCompleted`.** Seed a cfg with `Setup: false`, `SetupCommandsCompleted: 0`; assert the loop runs every command from index 0.

5. **Last-command-failure does not flip `Setup`.** `Commands: ["true", "false"]`; assert error returned, on-disk cfg has `SetupCommandsCompleted == 1`, `Setup == false`.

6. **Out-of-range index no-ops to "done".** Seed `SetupCommandsCompleted: 99`, `Commands: ["fail-if-run"]`; assert the loop runs no commands (the `for` no-ops because `99 >= 1`), no panic, and the final cfg has `Setup == true`.

Tests 1, 2, 5, 6 cover the new behaviour; 3 and 4 lock down backward-compat. A `setup_test.go` colocated with the extracted helper is the natural home.

## Trade-offs

| Approach | Pros | Cons |
|---|---|---|
| **Count only (recommended)** | Minimal schema change (one scalar field); matches the issue's suggested shape; backward-compat is trivial via `omitempty` zero values; correct under the current code paths because `cfg.Commands` is immutable per container | If `cfg.Commands` ever becomes mutable in place (not today), the saved index could misalign. The listed con doesn't apply today — see [Commands-changed: not handled by design](#commands-changed-not-handled-by-design). If the invariant relaxes, drift detection is an additive follow-up. |
| **Count + hash of commands list** | O(1) drift check if `cfg.Commands` ever becomes mutable in place; any change resets the whole sequence | An entire field, hash helper, mismatch-reset branch, and warning log exist purely to guard against a scenario that can't occur with today's code paths. Premature defence; extra surface area to maintain. |
| **List of completed command strings** (`SetupCompleted []string`) | If `cfg.Commands` ever becomes mutable in place, the resume index is the longest prefix of `Commands` matching `SetupCompleted` — natural "append a new step" semantics | More on-disk state (full command text duplicated); resume logic is a prefix walk rather than a single integer comparison. Same "guarding a non-existent scenario" problem as the hash variant. |
| **Make every setup command idempotent** | No agent-side change at all | The user has to rewrite every setup script to handle "already done" (e.g. `git clone || true`). Some commands cannot be made cleanly idempotent. Doesn't fix the *symptom* (re-running every command wastes minutes per restart). Punts the cost onto every workspace author. |

The count-only design is the minimum change that fixes #751. Drift detection would be defending against a scenario that the runner's current code paths make impossible — `cfg.Commands` is written once at container-create time and never rewritten on subsequent starts — so it's premature here. If a future change relaxes that invariant, adding a hash field is additive and non-breaking — the count field is a strict subset of any future scheme.

## Open Questions

1. **Should we expose the resume index over the API / web UI?** The web UI surfaces task state via the existing `Task`/log streams, not via the agent cfg.json (which lives inside the container). The setup loop's progress is already visible through the `Running setup command` log lines; lifting the resume index out of the container would require a server-side schema change and is out of scope for this proposal. Easy follow-up if useful.

2. **Should `cfg.Setup = true` write `SetupCommandsCompleted = len(cfg.Commands)` as well?** Strictly redundant — once `Setup` is true, the loop is skipped — but it would make the "fully done" state self-consistent in the file. The reworked loop does this naturally (the per-command save before the final `Setup = true` save already records the full count). Calling it out so reviewers don't object that the redundant write is wasteful.

3. **Per-command timeout / max retry?** Out of scope. The current loop has no per-command timeout (it inherits the driver `ctx`); the resume design works independently of any future timeout/retry policy. The trigger in issue #751 was a transient GitHub-rate-limit failure that would have recovered on its own with a manual restart — once resume is in place, the manual restart is enough.