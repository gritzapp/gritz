// Package docker implements the runner backend on the local Docker daemon.
// Containers are named gritz-{task-id} and labelled with the owning runner
// so multiple runners can share a daemon.
package docker

import (
	"archive/tar"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/prebuilt"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/dockerx"
)

// HandleType is the backend.Handle.Type the Docker backend stamps on the
// handles it produces (informational metadata persisted in the task record).
const HandleType = "docker"

// Backend runs task sandboxes as containers on the local Docker daemon.
type Backend struct {
	docker   *client.Client
	runnerID string
	log      *slog.Logger
}

type Options struct {
	RunnerID string
	Log      *slog.Logger
}

func New(opts Options) (*Backend, error) {
	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}
	return &Backend{
		docker:   docker,
		runnerID: opts.RunnerID,
		log:      cmp.Or(opts.Log, slog.Default()),
	}, nil
}

func (b *Backend) Close() error {
	return b.docker.Close()
}

// ValidateWorkspace reports whether the workspace's container config is usable
// by the Docker backend.
func (b *Backend) ValidateWorkspace(ws *workspace.Workspace) error {
	return ws.Container.Validate()
}

// inspectID returns the container's id and whether it exists. A missing
// container is ("", false, nil); any other failure is an error.
func (b *Backend) inspectID(ctx context.Context, ref string) (string, bool, error) {
	info, err := b.docker.ContainerInspect(ctx, ref)
	if cerrdefs.IsNotFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("failed to inspect container: %w", err)
	}
	return info.ID, true, nil
}

// Launch ensures the task's container exists and starts it, returning the
// handle the runner persists (the container id). With a reuse handle the exact
// recorded container is adopted in place so its filesystem persists across
// restarts; if that container is gone Launch returns backend.ErrGone rather than
// creating a fresh one. Without a reuse handle a fresh gritz-{taskID} container
// is created (a task's first start).
func (b *Backend) Launch(ctx context.Context, spec *backend.Spec, reuse *backend.Handle) (backend.Handle, error) {
	containerID, err := b.ensure(ctx, spec, reuse)
	if err != nil {
		return backend.Handle{}, err
	}
	if err := b.docker.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return backend.Handle{}, fmt.Errorf("failed to start container: %w", err)
	}
	return backend.Handle{Type: HandleType, ID: containerID}, nil
}

// ensure resolves the container to start, enforcing the one-sandbox-per-task
// invariant: with a reuse handle it adopts the exact recorded container, or
// returns backend.ErrGone if that container has vanished — it never falls
// through to create. A fresh container is created only when reuse is nil (a
// task's first start).
func (b *Backend) ensure(ctx context.Context, spec *backend.Spec, reuse *backend.Handle) (string, error) {
	// Reuse path: adopt the exact recorded container, or surface it as gone.
	if reuse != nil && reuse.ID != "" {
		id, ok, err := b.inspectID(ctx, reuse.ID)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", backend.ErrGone
		}
		return b.adopt(ctx, spec, id)
	}

	return b.create(ctx, spec)
}

// adopt reuses an existing container, repairing any network attachments whose
// endpoint ID has drifted from the live network — e.g. after
// `docker compose down && up`.
func (b *Backend) adopt(ctx context.Context, spec *backend.Spec, containerID string) (string, error) {
	b.log.Info("adopting existing container", "task", spec.TaskID, "container", containerID)
	repaired, err := dockerx.RepairNetworks(ctx, b.docker, containerID, spec.Workspace.Container.Networks)
	if err != nil {
		return "", fmt.Errorf("failed to repair network attachment: %w", err)
	}
	if len(repaired) > 0 {
		b.log.Warn("repaired stale network attachments",
			"task", spec.TaskID, "container", containerID, "networks", repaired)
	}
	return containerID, nil
}

func (b *Backend) create(ctx context.Context, spec *backend.Spec) (string, error) {
	wc := &spec.Workspace.Container

	b.log.Info("creating container", "task", spec.TaskID, "image", wc.Image)

	// Ensure the image is available locally (pulls if needed).
	info, err := dockerx.ImageEnsure(ctx, b.docker, dockerx.ImageEnsureOptions{
		Ref:         wc.Image,
		PullOptions: image.PullOptions{RegistryAuth: dockerx.ResolveRegistryAuth(wc.Image)},
		PullProgress: func(p dockerx.PullProgress) {
			if p.Status != "" && p.Progress == "" {
				b.log.Info("pull", "image", wc.Image, "status", p.Status)
			}
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to ensure image: %w", err)
	}

	// The driver binary is provisioned by the backend because selecting it
	// requires the image architecture, which only the runtime knows.
	binData, err := prebuilt.ReadBinary(info.Architecture)
	if err != nil {
		return "", err
	}

	resp, err := b.docker.ContainerCreate(ctx,
		&container.Config{
			Image: wc.Image,
			User:  wc.User,
			Labels: map[string]string{
				"gritz":        "true",
				"gritz.task":   fmt.Sprint(spec.TaskID),
				"gritz.runner": b.runnerID,
			},
			Cmd:        spec.Cmd,
			Env:        append(wc.Environ(), spec.Env...),
			WorkingDir: wc.WorkingDir,
		},
		&container.HostConfig{
			Binds:       wc.Volumes,
			GroupAdd:    wc.GroupAdd,
			Runtime:     wc.Runtime,
			Privileged:  wc.Privileged,
			NetworkMode: container.NetworkMode(wc.NetworkMode),
		},
		wc.NetworkingConfig(),
		nil,
		fmt.Sprintf("gritz-%d", spec.TaskID),
	)
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	files := append([]backend.File{
		{Path: backend.BinaryPath, Data: binData, Mode: 0755},
	}, spec.Files...)
	if err := b.copyFiles(ctx, resp.ID, files); err != nil {
		return "", fmt.Errorf("failed to copy files: %w", err)
	}

	return resp.ID, nil
}

func (b *Backend) copyFiles(ctx context.Context, containerID string, files []backend.File) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		name := strings.TrimPrefix(f.Path, "/")
		if f.Dir {
			if err := tw.WriteHeader(&tar.Header{
				Name:     name + "/",
				Mode:     f.Mode,
				Typeflag: tar.TypeDir,
			}); err != nil {
				return err
			}
			continue
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: f.Mode,
			Size: int64(len(f.Data)),
		}); err != nil {
			return err
		}
		if _, err := tw.Write(f.Data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return b.docker.CopyToContainer(ctx, containerID, "/", &buf, container.CopyToContainerOptions{})
}

// Probe reports the liveness of a single handle by inspecting its container.
// A missing container is gone (removed); a stopped one is an exited husk.
func (b *Backend) Probe(ctx context.Context, h backend.Handle) (backend.State, error) {
	info, err := b.docker.ContainerInspect(ctx, h.ID)
	if cerrdefs.IsNotFound(err) {
		return backend.StateGone, nil
	}
	if err != nil {
		return backend.StateUnknown, fmt.Errorf("failed to inspect container: %w", err)
	}
	return sandboxState(info.State.Status), nil
}

func sandboxState(state string) backend.State {
	switch state {
	case "running":
		return backend.StateRunning
	case "exited", "dead":
		return backend.StateExited
	default:
		return backend.StateUnknown
	}
}

// Signal gracefully stops the handle's container if it is running: SIGTERM
// first, then SIGKILL after a 30s grace period. It reports whether a running
// container was signalled — in that case the driver owns the terminal event
// report (see the driver-owned-events proposal).
func (b *Backend) Signal(ctx context.Context, h backend.Handle) (bool, error) {
	b.log.Info("killing container", "container", h.ID)

	// Try SIGTERM first with a timeout
	killCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := dockerx.ContainerKill(killCtx, b.docker, h.ID, "SIGTERM")
	if err == nil {
		return true, nil
	}
	if errors.Is(err, dockerx.ErrNotRunning) || cerrdefs.IsNotFound(err) {
		// The container exited (or was removed) before the kill, so nothing
		// was signalled.
		return false, nil
	}

	// If SIGTERM timed out, send SIGKILL
	if errors.Is(err, context.DeadlineExceeded) {
		b.log.Warn("SIGTERM timed out, sending SIGKILL", "container", h.ID)
		if err := dockerx.ContainerKill(ctx, b.docker, h.ID, "SIGKILL"); err != nil && !errors.Is(err, dockerx.ErrNotRunning) && !cerrdefs.IsNotFound(err) {
			return true, err
		}
		return true, nil
	}

	return true, err
}

// Destroy removes the handle's container. A missing container is not an error.
func (b *Backend) Destroy(ctx context.Context, h backend.Handle) error {
	err := b.docker.ContainerRemove(ctx, h.ID, container.RemoveOptions{Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("failed to remove container: %w", err)
	}
	return nil
}

// Wait blocks until the handle's container stops, returning its exit code. It
// waits on WaitConditionNotRunning, which is level-triggered: the daemon returns
// an already-stopped container's stored exit code immediately, closing the
// launch→persist race and the boot Probe→Wait TOCTOU without a manual inspect
// (WaitConditionNextExit is edge-triggered and would block forever on a
// container that already exited before this call). A removed container (NotFound)
// or any other wait error has no recoverable code, so it is reported as lost.
func (b *Backend) Wait(ctx context.Context, h backend.Handle) (backend.ExitCode, error) {
	okCh, errCh := b.docker.ContainerWait(ctx, h.ID, container.WaitConditionNotRunning)
	// The single select must always drain one channel: the SDK's result channel
	// is unbuffered and its send is not ctx-aware, so returning without receiving
	// a ready result would leak the SDK's wait goroutine.
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case err := <-errCh:
		// A canceled ctx aborts the SDK's request, surfacing here as an error
		// when the select races ctx.Done(). That is cancellation, not an exit.
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		// Removed (NotFound) or a wait error: no code to recover → report lost.
		b.log.Error("container wait failed", "container", h.ID, "err", err)
		return backend.ExitLost, nil
	case res := <-okCh:
		return backend.ExitCode(res.StatusCode), nil
	}
}
