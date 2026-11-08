# Auto-Repair Stale Container Network Attachment

Issue: https://github.com/icholy/gritz/issues/661

## Problem

Starting an existing task container can fail with:

```
failed to start container: Error response from daemon: failed to set up container networking: network <id> not found
```

When the docker-compose stack is torn down and brought back up (`docker compose down && up`), the compose-managed network (e.g. `gritz-config_default`) is recreated and gets a **new** network ID. Any pre-existing `gritz-<task-id>` container still pins the **old** network ID in its `NetworkSettings.Networks[<name>].NetworkID`, even though the endpoint is keyed by network NAME. On the next `ContainerStart`, Docker tries to attach to the old ID, fails, and leaves the container in `Exited`. Every retry fails the same way until the container is manually removed — which throws away its logs and the files we copied into it at create time.

The runner today wraps the failure in `internal/runner/runner.go`:

```go
if err := r.docker.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
    return fmt.Errorf("failed to start container: %w", err)
}
```

…and the task goes to `FAILED` with no recovery path.

## Design

### Overview

**Proactively** detect a stale network attachment before calling `ContainerStart` and repair the container in place by re-attaching it to the live network(s) by NAME. The check runs only for existing containers (`r.find` hit) and only when the workspace declares at least one network — so containers created in the same call, and workspaces with no custom networks, pay nothing extra.

The runner already knows the desired networks for the task via `ws.Container.Networks` (`internal/runner/workspace/workspace.go:167`). The proactive check compares each name's currently-attached endpoint ID (from `ContainerInspect`) against the live network ID (from `NetworkInspect`), and reattaches only the ones that drift.

### Detection: proactive

Two strategies were considered:

1. **Proactive** — before starting an existing container, inspect it, list each `NetworkSettings.Networks` entry, look up the live network ID for that name, and reattach if they differ.
2. **Reactive** — start the container normally; only reattach if `ContainerStart` returns a "network … not found" error.

Proactive is the recommended approach:

- **No reliance on a daemon error string.** The reactive path matches on `"failed to set up container networking" + "not found"`; that wording is part of dockerd's stable user-facing surface but it's still an untyped, free-text string. The proactive path compares network IDs, which are first-class fields on the Docker API — Moby can't change those without a major-version SDK break.
- **Catches latent drift before it bites a user.** The reactive path only triggers after the start has already failed. The proactive path repairs the attachment in the same `Runner.Start` call that would have failed, so the task never enters a failed state for this reason at all.
- **Same repair primitive either way.** The actual fix (`NetworkDisconnect` + `NetworkConnect` by name) is identical. Only the trigger differs.

Costs of going proactive:

- One extra `ContainerInspect` plus one `NetworkInspect` per declared network on each start of an existing container. Both are cheap, low-latency Docker API calls (no daemon-level work beyond a map lookup) and run only on the `find` hit path. Containers created in the same `Start` skip the check.
- Slightly more code than the reactive substring match. The trade is centralised in one helper in `dockerx`, so the surface area of the change is contained.

The proactive check is **not** combined with a reactive fallback. If the proactive check passes but the start still fails with a network error (e.g. the network is torn down in the millisecond gap between `NetworkInspect` and `ContainerStart`), the task fails — same as today. The race window is small enough that adding a fallback would be cargo-culted complexity; the next start attempt's proactive check would catch it.

### Repair helper

One new helper in `internal/x/dockerx/network.go`. It detects drift and repairs it in a single pass, returning the names of the networks it actually touched so the caller can log a meaningful message:

```go
// RepairNetworks brings the container's attachment to each of the named
// networks back in sync with the live Docker network registry. For each name,
// it compares the endpoint ID pinned in the container's NetworkSettings
// against the live network's ID, and — if they differ, or the endpoint is
// missing entirely — force-disconnects and reconnects by name.
//
// Used to recover from stale endpoint IDs left behind after
// `docker compose down && up` recreates the network under the same name with
// a fresh ID.
//
// Disconnect uses Force=true so endpoints pinned to a no-longer-existent
// network ID can be dropped (a non-force disconnect would itself fail with
// "network not found"). If the container isn't currently attached at all,
// only the connect step runs.
//
// Returns the list of network names that were repaired (empty if everything
// was already in sync). An error from NetworkInspect for a specific name
// (i.e. the named network does not exist in the live registry) is returned
// without attempting any partial repair — we cannot attach to a network
// that doesn't exist.
func RepairNetworks(ctx context.Context, c *client.Client, containerID string, networks []string) ([]string, error) {
    if len(networks) == 0 {
        return nil, nil
    }
    info, err := c.ContainerInspect(ctx, containerID)
    if err != nil {
        return nil, fmt.Errorf("inspect container: %w", err)
    }
    var repaired []string
    for _, name := range networks {
        live, err := c.NetworkInspect(ctx, name, network.InspectOptions{})
        if err != nil {
            return repaired, fmt.Errorf("inspect network %q: %w", name, err)
        }
        endpoint, attached := info.NetworkSettings.Networks[name]
        if attached && endpoint.NetworkID == live.ID {
            continue
        }
        if attached {
            if err := c.NetworkDisconnect(ctx, name, containerID, true); err != nil && !isNotConnectedErr(err) {
                return repaired, fmt.Errorf("disconnect %q: %w", name, err)
            }
        }
        if err := c.NetworkConnect(ctx, name, containerID, nil); err != nil {
            return repaired, fmt.Errorf("connect %q: %w", name, err)
        }
        repaired = append(repaired, name)
    }
    return repaired, nil
}
```

`NetworkConnect`'s last argument is `*network.EndpointSettings`. Passing `nil` produces the same default endpoint we get today from `Container.NetworkingConfig()` (the workspace passes `&network.EndpointSettings{}` per network — no aliases, no fixed IP, no driver opts — so `nil` is equivalent).

Returning the repaired list (rather than a boolean or nothing) keeps detection and repair fused in one round of inspect calls and lets the caller log exactly which networks were touched without re-inspecting.

### Wire-up in `Runner.Start`

`internal/runner/runner.go:462` becomes:

```go
func (r *Runner) Start(ctx context.Context, task *model.Task) error {
    c, ok, err := r.find(ctx, task.ID)
    if err != nil {
        return err
    }

    var containerID string
    if ok {
        r.log.Info("starting existing container", "task", task.ID, "name", fmt.Sprintf("gritz-%d", task.ID))
        containerID = c.ID

        // Existing containers can have a stale network ID baked in after
        // `docker compose down && up`. Refresh any drifted attachments
        // before starting.
        ws, err := r.workspaces.Get(task.Workspace)
        if err != nil {
            return fmt.Errorf("get workspace: %w", err)
        }
        repaired, err := dockerx.RepairNetworks(ctx, r.docker, containerID, ws.Container.Networks)
        if err != nil {
            return fmt.Errorf("failed to repair network attachment: %w", err)
        }
        if len(repaired) > 0 {
            r.log.Warn("repaired stale network attachments",
                "task", task.ID, "container", containerID, "networks", repaired)
        }
    } else {
        containerID, err = r.create(ctx, task)
        if err != nil {
            return err
        }
    }

    if err := r.docker.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
        return fmt.Errorf("failed to start container: %w", err)
    }
    return nil
}
```

Key properties:

- Repair runs **only** on the `ok == true` branch (existing container). A container we just created in this `Start` call cannot have a stale attachment.
- Repair is skipped if the workspace declares no networks — most workspaces today fall into this category, so they pay zero overhead.
- Only networks that actually drifted are reattached. If three networks are configured and only one moved, only one is touched.
- The check failing (e.g. one of the configured networks doesn't exist in the live registry) returns an error before we attempt to start, so the task fails fast and visibly rather than failing mid-start with the same opaque error we have today.

### Why reattach in place, not remove-and-recreate

Once we've detected the drift, two repair strategies are possible. The proposal recommends **reattach in place**.

| Aspect | Reattach in place (proposed) | Remove and recreate |
|---|---|---|
| Container logs | Preserved | Lost |
| Files copied at create (`/usr/local/bin/gritz`, agent config, prebuilt binary, directory perms — see `Runner.create` lines 451-456) | Preserved | Re-copied (extra work, ~5-30MB depending on prebuilt) |
| New JWT minted? | No — existing token still valid | Yes — `r.proxy.TaskToken(task)` runs again |
| Image pull if registry changed | No | Yes (`dockerx.ImageEnsure`) |
| Concurrency cost | One disconnect + one connect per stale network (typically 1) | Full create path (create + copy files + start) |
| Code path | One helper + an inline check in `Start` | Reuse existing `r.create()` |
| Robustness if container itself is corrupt | Weaker — only repairs the network endpoint | Stronger — fresh container |
| Idempotency on partial failure | Disconnect+Connect is naturally idempotent | Removal must succeed before recreate; partial removal is awkward |

The corruption scenario for "remove and recreate" is theoretical — a stale network ID does not imply a corrupt filesystem layer. In practice the container is fine and the only stale state is the network endpoint, so the cheaper, log-preserving repair is the right default.

If reattach itself fails (e.g. the network really is gone with no replacement) the start surfaces an error and the task fails as before — the same outcome as the current behaviour, with no regression.

### Edge cases

**Multiple networks.** `RepairNetworks` checks each declared network independently and reattaches only the ones whose endpoint ID drifted (or that the container isn't attached to at all). Healthy networks are not perturbed.

**Container attached to networks not in `ws.Container.Networks`.** The proactive check only looks at networks declared by the workspace, so a manually-added network attachment (e.g. for debugging) is invisible to the check and left alone. If that manual network is itself stale the start will still fail; that's an acceptable boundary because the runner doesn't own those attachments.

**Primary `NetworkMode` is immutable after create.** This is the key safety property that makes reattach viable. `container.HostConfig.NetworkMode` (set at create time and read by Docker as the primary network) cannot be changed without recreate. The workspace builder (`internal/runner/containerbuild/builder.go:50-54`) does **not** set `NetworkMode`, so each task container's primary attachment is Docker's default `bridge` network, and all workspace-configured networks come in through `NetworkingConfig.EndpointsConfig` — exactly the surface that `NetworkDisconnect`/`NetworkConnect` can mutate. If a future workspace setting introduces a non-default `NetworkMode`, reattach would not fix a stale primary network and the runner would have to fall back to remove-and-recreate. The proposal does not introduce that fallback because the workspace config does not expose `NetworkMode` today; it is called out so the precondition is explicit.

**Retry limits / loops.** The proactive check runs once per `Start` call. There is no in-`Start` retry. If repair fails, `Start` returns an error, the task goes to `FAILED`, and the runner does **not** keep retrying within the same poll tick. The next poll cycle (if the task is restarted) gets a fresh attempt and re-runs the check from scratch — same retry shape as today. This bounds the recovery work and avoids any chance of a tight infinite loop on a truly broken host.

**Concurrent starts on the same container.** `Runner.Start` for a given task can only be invoked once per poll cycle (the poll loop iterates tasks; the runner's `sem` slots are acquired before `Start`). Two simultaneous `Runner.Start` calls for the same task ID would already be a bug independent of this change. `NetworkDisconnect`/`NetworkConnect` themselves are serialised inside dockerd per container, so even in a race the worst case is one of the two repairs no-ops the other.

**Container vanished between `find` and the inspect.** `ContainerInspect` returns "no such container", `RepairNetworks` returns the error, and `Start` fails fast. This is no worse than the current behaviour where `ContainerStart` would have raised the same condition.

**Network truly gone with no replacement.** `NetworkInspect` returns "network not found", surfaces as an error from `RepairNetworks` (with any networks repaired up to that point in the returned slice), and `Start` fails before attempting `ContainerStart`. The error message names the missing network, so a human can investigate. This is strictly better than today's opaque "network &lt;id&gt; not found" failure.

**Workspace config drift.** If `ws.Container.Networks` is edited after a container was created with the old set, `RepairNetworks` will see networks the container isn't attached to and connect them. That's the correct behaviour — the user's edit should take effect on the next start. Removed networks are not detached (out of scope: we don't want a network repair pathway to start tearing down attachments).

### Logging and observability

- A `Warn` log when `RepairNetworks` returns a non-empty list (`repaired stale network attachments`) makes the repair visible in the runner logs without lifting it to `Error`. Should be rare in steady state; if it fires often it's an early signal of stack churn worth investigating.
- No log on the happy path where `RepairNetworks` returns nothing.
- No new metric is added in this proposal; if the repair turns out to fire often in production, adding a `gritz_runner_network_repairs_total` counter is a follow-up.

### Tests

- Unit test for `RepairNetworks` using a fake Docker client that returns mismatched IDs / missing attachments. Assertions cover the returned `repaired` slice as well as the recorded Disconnect/Connect calls.
- Integration test in `internal/runner` exercising the full repair path: create a network, create a container attached to it, remove the network, recreate it under the same name (new ID), call `Runner.Start`, assert the container is running and its `NetworkSettings.Networks[<name>].NetworkID` equals the new network's ID. Gated behind the existing test docker dependency.
- Integration test for the no-op path: container with up-to-date attachments starts cleanly, `RepairNetworks` returns an empty slice, no Disconnect/Connect calls are issued.

## Trade-offs

| Approach | Pros | Cons |
|---|---|---|
| **Proactive precheck + reattach in place** (proposed) | Doesn't depend on matching daemon error strings; catches drift before the task fails; only reattaches the networks that actually drifted; preserves logs and copied files | One extra `ContainerInspect` plus N `NetworkInspect` per start of an existing container with declared networks; slightly more code than the reactive variant |
| **Reactive reattach in place** | Zero overhead on the happy path | Relies on substring match against a daemon error message; task transiently fails-then-recovers rather than recovering pre-emptively; same repair primitive otherwise |
| **Reactive remove-and-recreate** | Simpler — reuses `r.create()`; recovers from any container-level corruption, not just network | Loses container logs and copied state; ~5-30MB of extra file copy per repair; re-mints JWT; more wall-clock latency on the recovery |
| **Do nothing, document `docker rm` as the manual fix** | Zero code | The original status quo; every dev environment hits this after `docker compose down && up` |

The proactive variant pays a small fixed cost (two Docker API calls per existing-container start when networks are configured) to remove the reliance on free-text error matching and to convert a transient failure into a silent repair. That's the right trade for a runner whose job is to keep tasks alive across stack churn.

## Open Questions

1. **Should repair also trigger on `NetworkMode` mismatches (not just `EndpointsConfig`)?** Today no workspace sets a non-default `NetworkMode`, so this is moot. If we add a workspace setting for it later, we'd need to fall back to remove-and-recreate when the primary mode is stale. Worth deciding before exposing `NetworkMode` in the workspace schema, not now.

2. **Should the proactive check also handle networks the container is attached to but the workspace no longer declares?** Proposal says no — removing attachments is a different operation with different semantics and not in scope for "network is stale, refresh it". Worth confirming.

3. **Counter / metric.** Not added in this proposal. If the failure is rare in production we can leave it at log-only; if it's frequent we want a counter so on-call can spot a pattern. Easy follow-up.