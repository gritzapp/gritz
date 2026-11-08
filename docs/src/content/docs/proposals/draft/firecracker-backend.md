---
title: "Firecracker Backend for the Runner"
description: "Docker is the only task sandbox, so every task shares the runner host's kernel and a container escape exposes the host, its credentials, and all other tasks."
---

Issue: https://github.com/icholy/gritz/issues/920

## Problem

Tasks run autonomous coding agents that execute arbitrary shell commands. Today the only sandbox is a Docker container: every task shares the runner host's kernel, and a container escape compromises the host, its credentials (the `xat_` runner key, registry auth, secrets expanded into `workspaces.yaml`), and every other task on the machine. Workspaces that set `privileged: true`, custom runtimes, or host volume mounts widen the blast radius further.

The runner's sandbox runtime is abstracted behind `backend.Backend` (proposals/implemented/runner-backend-interface.md), which names Firecracker microVMs as a target runtime — but Docker remains the only implementation. This proposal adds a `firecracker` backend that runs each task in its own KVM microVM: hardware-virtualized isolation per task, on a single host, with no cluster or external scheduler.

## Design

### Overview

A new package `internal/runner/backend/firecracker` implements `backend.Backend` by supervising one `firecracker` process per task. Selection follows the existing seam:

```
gritz runner --backend firecracker
```

The backend's job per task: turn the workspace's OCI image into an ext4 root filesystem, boot a microVM from it with a guest kernel, run `spec.Cmd` (the driver) inside via a minimal PID-1 init, and report the driver's exit code back through `backend.Exit`. The orchestrator (`runner.Runner`), driver, server API, and database are untouched — the driver already connects directly to the server with its task token and neither knows nor cares what runtime launched it.

### Host requirements

- Linux with `/dev/kvm` (bare metal or nested virtualization)
- root (TAP device creation, NAT rules, preserving uid/gid when unpacking image filesystems)
- `firecracker` binary and a guest kernel (`vmlinux`) — fetched by `gritz download --firecracker`, see CLI below
- `e2fsprogs` (`mkfs.ext4 -d`, `resize2fs` — both operate on image files without mounting)
- `iproute2` and nftables (TAP, bridge, masquerade)

Guest architecture always equals host architecture — KVM does not emulate — so the backend injects the driver binary via `prebuilt.ReadBinary(runtime.GOARCH)` with no inspection step.

### Workspace config

Per the backend-interface proposal, backends get sibling config sections. `workspace.Workspace` gains a `firecracker:` section next to `container:`:

```yaml
workspaces:
  pets-workshop:
    firecracker:
      image: ghcr.io/icholy/gritz-workspace-debian:latest
      vcpus: 2            # default 2
      memory_mib: 2048    # default 2048
      disk_size_mib: 8192 # rootfs size incl. free space, default 8192
      working_dir: /root
      user: ""            # default root
      environment:
        CLAUDE_CODE_OAUTH_TOKEN: ${env:CLAUDE_CODE_OAUTH_TOKEN}
    agent:
      type: claude
      ...
```

```go
type Firecracker struct {
	Image       string            `yaml:"image"`
	Vcpus       int64             `yaml:"vcpus"`
	MemoryMib   int64             `yaml:"memory_mib"`
	DiskSizeMib int64             `yaml:"disk_size_mib"`
	WorkingDir  string            `yaml:"working_dir"`
	User        string            `yaml:"user"`
	Environment map[string]string `yaml:"environment"`
}
```

`image` is the same kind of OCI reference the Docker backend uses — existing workspace images work unmodified, since the agent toolchain they carry (claude CLI, git, language runtimes) is exactly what the microVM needs. A workspace may configure both `container:` and `firecracker:` so the same `workspaces.yaml` serves runners with different backends.

This resolves the backend-interface proposal's first open question: `Backend` grows a validation method, and the `container.image is required` check moves out of `workspace.Container.Validate` into the Docker backend:

```go
type Backend interface {
	// ValidateWorkspace checks the workspace's config section for this
	// backend. The runner validates at startup and registers only the
	// workspaces its backend accepts; Start re-validates.
	ValidateWorkspace(ws *workspace.Workspace) error
	// ... existing methods unchanged
}
```

`RegisterWorkspaces` skips (with a warning) workspaces that fail validation, so a shared `workspaces.yaml` across a heterogeneous runner fleet advertises each workspace only from runners that can actually run it.

### State directory

All backend state lives under a per-runner directory (default `/var/lib/gritz/firecracker/<runner-id>`):

```
<state-dir>/
├── images/
│   └── <image-digest>-<gritz-version>.ext4   # cached base rootfs images
└── tasks/<task-id>/
    ├── rootfs.ext4       # per-task root filesystem (persists across restarts)
    ├── config.tar        # boot manifest + spec.Files, rebuilt every Start
    ├── status.img        # 4 KiB raw block device for the exit code
    ├── ip                # allocated guest IP, stable across restarts
    ├── firecracker.pid
    ├── api.sock          # firecracker API socket (while running)
    └── console.log       # guest serial console, for debugging
```

The `tasks/<task-id>` directory is the sandbox for the purposes of the `Backend` contract: `List` scans it, `Remove` deletes it, and `Start` reuses it when present.

### Rootfs from the OCI image

`Start` ensures a cached base image for the workspace's image reference:

1. Pull and flatten the image with `go-containerregistry` (already in the module graph), using `authn.DefaultKeychain` — the same `~/.docker/config.json` credentials `dockerx.ResolveRegistryAuth` resolves today.
2. Unpack the flattened tar to a staging directory, preserving uid/gid and modes (this is the root requirement).
3. Write the host-arch driver binary to `staging/usr/local/bin/gritz` (`backend.BinaryPath`) — baked into the base image because the kernel must be able to exec it as init before any other provisioning runs.
4. `mkfs.ext4 -d staging base.ext4` — builds the filesystem from the directory without mounting anything.
5. Cache at `images/<digest>-<gritz-version>.ext4`. The cache key includes the gritz version because the image embeds the driver binary.

A fresh task copies the base (`cp --reflink=auto`, sparse fallback) to `tasks/<id>/rootfs.ext4`, then grows it to `disk_size_mib` with `truncate` + `resize2fs`. A restarted task reuses its existing `rootfs.ext4` — the same filesystem-reuse semantics as the Docker backend reusing a container.

### Guest boot

Each `Start` configures a fresh firecracker process through its API socket — machine config (`vcpus`, `memory_mib`, SMT off), boot source, three drives, one network interface, MMDS — then calls `InstanceStart`. Boot args:

```
console=ttyS0 reboot=k panic=1 pci=off
ip=<guest-ip>::<gateway>:<netmask>::eth0:off
init=/usr/local/bin/gritz -- tool vm-init
```

The kernel configures eth0 itself (`ip=` / `CONFIG_IP_PNP`, enabled in the Firecracker project's CI kernels) and execs the gritz binary as PID 1. There is no dependence on the image's own init system, and no separate init artifact: the binary that is already required to be at `backend.BinaryPath` plays both roles.

The three drives:

| Device | Contents | Lifetime |
|---|---|---|
| `/dev/vda` | `rootfs.ext4`, read-write root device | persists across restarts |
| `/dev/vdb` | `config.tar` — a raw tar stream, no filesystem | rebuilt every Start |
| `/dev/vdc` | `status.img` — 4 KiB raw, zeroed every Start | rebuilt every Start |

`config.tar` is the firecracker equivalent of the Docker backend's `CopyToContainer` tar: its first entry is a boot manifest, followed by `spec.Files` verbatim (directory entries included):

```go
type bootManifest struct {
	Cmd         []string `json:"cmd"`          // spec.Cmd
	Env         []string `json:"env"`          // ws.Firecracker environment + spec.Env
	WorkingDir  string   `json:"working_dir,omitempty"`
	User        string   `json:"user,omitempty"`
	Nameservers []string `json:"nameservers,omitempty"`
}
```

### vm-init

`gritz tool vm-init` (a hidden subcommand beside `tool agent-mcp`) is the guest PID 1:

1. Mount `/proc`, `/sys`, and devtmpfs on `/dev`. Deliberately do **not** mount tmpfs on `/tmp`: the agent config (`/tmp/gritz/<task-id>.json`) carries the driver's `SetupCommandsCompleted` and `Started` markers and must persist across restarts, exactly as it does in a reused container.
2. Set hostname `gritz-<task-id>`, add the link-local route to the MMDS address (169.254.169.254), write `/etc/resolv.conf` from the manifest's nameservers.
3. Read the tar stream from `/dev/vdb`. The manifest entry is consumed on every boot; the remaining file entries are extracted only if `/gritz/.provisioned` is absent, which is then created. This reproduces the Docker backend's provision-at-create-only semantics, so a restart never clobbers agent-managed state.
4. Spawn the driver: `manifest.Cmd` with `manifest.Env`, working directory, and optional setuid to `manifest.User`. As PID 1, reap orphaned children.
5. Poll the MMDS stop flag (below) once per second; on stop, SIGTERM the driver — the in-guest mirror of the Docker backend's SIGTERM.
6. When the driver exits, write `gritz-exit:<code>\n` to `/dev/vdc`, sync, and power off via `reboot(RB_POWER_OFF)`.

One deliberate difference from Docker reuse: because cmd/env are delivered fresh on every boot, a restarted task's driver runs with the newly minted task token instead of the original one. The orchestrator already mints a token on every `Start`, so this only tightens the existing behavior.

### Networking

- One bridge per runner (`gritz0`, gateway at the subnet's first address), subnet from `--firecracker-subnet` (default `172.30.0.0/16`).
- Per VM: a TAP device (`xat<task-id>`) attached to the bridge, a deterministic MAC derived from the task ID, and a guest IP allocated sequentially and persisted at `tasks/<id>/ip` so it is stable across restarts.
- Egress via nftables masquerade of the subnet plus `ip_forward=1` — the same posture as Docker's default bridge.
- Reachability constraint is unchanged from Docker bridge networking: the runner's `--server` URL must be reachable from the VM network. A localhost server must be addressed via the bridge gateway IP.

### Backend method mapping

| Method | Implementation |
|---|---|
| `Start` | Ensure base image; create or reuse `tasks/<id>` (rootfs copy + resize on create); allocate or reload IP, ensure TAP; rebuild `config.tar`, zero `status.img`; spawn `firecracker` detached (own session, survives runner restarts, like containers do); configure via API socket; `InstanceStart`; write pidfile. |
| `Stop` | Not running → `(false, nil)`. Otherwise `PATCH /mmds {"stop":"true"}` and wait up to 30s for the process to exit; on timeout, SIGKILL the firecracker process. Returns `(true, nil)` — the driver owns the terminal report, same contract as Docker's SIGTERM→SIGKILL. |
| `Running` | Pidfile + `/proc/<pid>` liveness, with the cmdline checked against this task's `api.sock` path to guard against PID reuse. |
| `List` | Scan `tasks/`; `StateRunning` per the liveness check, otherwise `StateExited`. |
| `Remove` | Kill the process if alive; delete the TAP device and the task directory. |
| `Watch` | One watcher per known VM (spawned and adopted alike) using `pidfd_open` + poll, which works for non-children — so VMs from a previous runner process are observed identically. On exit, parse `status.img`; emit `Exit{TaskID, ExitCode}`. |
| `Close` | Stops watchers; leaves VMs running, exactly as containers outlive the runner today. |

Exit-code fidelity follows the backend-interface contract: a missing or garbled status record is reported as `-1`. By the driver-owned-events invariant that means "report lost"; if the driver did report before dying, the status guard rejects the spurious `failed`.

### CLI

```
gritz runner --backend firecracker \
  [--firecracker-state-dir /var/lib/gritz/firecracker] \
  [--firecracker-kernel <state-dir>/vmlinux] \
  [--firecracker-bin firecracker] \
  [--firecracker-subnet 172.30.0.0/16]
```

All flags have `GRITZ_FIRECRACKER_*` env sources. `internal/command/runner.go`'s backend switch gains a `firecracker` case.

`gritz download` gains a `--firecracker` flag that fetches two pinned artifacts into the state directory: the static `firecracker` release binary for the host arch (from the firecracker-microvm GitHub releases) and a known-good guest `vmlinux` (the Firecracker project's published CI kernels, which carry the required virtio-blk/net, ext4, and `CONFIG_IP_PNP` config). Versions are pinned as constants in the backend package so the kernel and firecracker are upgraded deliberately.

### Testing

- Unit tests (no KVM): IP allocation and persistence, `config.tar` construction (manifest + `spec.Files`, directory entries), status-record parsing, boot-args assembly, `ValidateWorkspace`.
- Integration tests in `backend/firecracker`, skipped unless `/dev/kvm` exists and the test runs as root — mirroring how the Docker e2e tests require a daemon. They cover boot, provision-once-across-restart, graceful stop, exit-code propagation, and adoption after a simulated runner restart.
- The orchestrator needs no new tests: it already runs against `BackendMock`.

### What doesn't change

The orchestrator (`runner.go`), `EventQueue`, proto definitions, database schema, driver, and task state machine are untouched. The Docker backend changes only by gaining `ValidateWorkspace` (absorbing the `image is required` check). `prebuilt` is reused as-is for the driver binary.

## Trade-offs

**OCI images as the rootfs source vs. a dedicated rootfs artifact.** Requiring workspaces to supply prebuilt ext4 images would remove the conversion step but fork the artifact pipeline: every workspace image would need a second build target, and registry auth, versioning, and distribution would be reinvented. Converting the existing images keeps `workspaces.yaml` portable across backends — the cost is a one-time conversion per image digest, amortized by the cache.

**gritz as guest init vs. a separate init.** A dedicated minimal init binary (or relying on the image's systemd) would either add a release artifact or depend on what each image ships. The gritz binary is already required inside the sandbox at a fixed path; booting it as PID 1 with a `vm-init` subcommand adds no artifact and keeps guest behavior identical across images. The cost is PID-1 duties (reaping, mounts) in our code, which are small and testable.

**Raw tar config disk + raw status disk vs. vsock.** A vsock channel could deliver files, stop signals, and exit codes over one transport, but requires `CONFIG_VIRTIO_VSOCKETS` in the guest kernel, an AF_VSOCK dependency in the guest, and host-side socket multiplexing. Two raw block devices and a tar stream use nothing but `archive/tar` and preserve an exact parallel with the Docker backend's tar copy. The stop signal then needs its own channel, hence MMDS.

**MMDS-polled stop vs. `SendCtrlAltDel`.** Firecracker's `SendCtrlAltDel` action is x86_64-only (it emulates the i8042 controller), so relying on it would make graceful stop architecture-dependent. Updating MMDS and having vm-init poll once per second works identically on x86_64 and aarch64; the worst-case extra second of latency is negligible against the 30s kill grace period.

**Full per-task rootfs copy vs. snapshot layers.** firecracker-containerd solves rootfs duplication with devicemapper thin pools; overlayfs over virtio would need kernel and image cooperation. Both buy storage efficiency at a large complexity cost. A reflink-aware sparse copy is one syscall-deep, and on XFS/btrfs costs near-zero; on ext4 hosts it degrades to a sparse copy of mostly-empty filesystems. Acceptable until proven otherwise.

**Host-arch only.** The Docker backend can run foreign-arch images via qemu emulation; KVM cannot. A workspace image must be available for the runner's architecture. This matches the prebuilt binary set (linux amd64/arm64) and is a documented constraint, not a design gap.

**No volumes.** `container.volumes` has no firecracker equivalent in this proposal — Firecracker has no virtio-fs, and host bind mounts are precisely the isolation hole this backend exists to close. Workspaces that need shared caches or secrets must bake them into the image or fetch them in setup commands (see Open Questions).

## Open Questions

1. **Jailer.** Production Firecracker deployments run under the `jailer` binary (chroot, cgroups, seccomp, dedicated uid per VM). Adopt it from the start, or land the backend first and harden in a follow-up?
2. **Persistent caches across tasks.** Without volumes, every fresh task re-downloads dependencies. Is an optional extra block device (a per-workspace cache disk, attached read-write to one VM at a time) worth the contention rules it would need?
3. **Kernel provenance.** Pinning the Firecracker project's CI kernels is convenient but depends on their artifact bucket. Should the release pipeline eventually build and publish a vmlinux alongside the prebuilt binaries?
4. **Base image GC.** `images/*.ext4` accumulates one file per (digest, gritz version). Prune by LRU when unreferenced by any task directory, or leave it to the operator?
5. **Snapshot/restore.** Firecracker snapshots could cut boot+setup latency dramatically for hot workspaces. Out of scope here, but does the state layout need anything now to keep that door open?
