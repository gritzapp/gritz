// Package docker implements the runner backend on the local Docker daemon
// with the driver server (`gritz driver`) as each container's main
// process, starting each run over the driver.v1 protocol. The driver still
// reports to the gritz server itself, and the container is stopped after
// every run.
//
// Containers are named gritz-{task-id} and labelled with the owning runner.
package docker

import (
	"archive/tar"
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/driverclient"
	"github.com/gritzapp/gritz/internal/runner/prebuilt"
	"github.com/gritzapp/gritz/internal/runner/runrecord"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/dockerx"
)

// HandleType is the backend.Handle.Type this backend stamps on the handles it
// produces.
const HandleType = "docker"

// DriverAddr is the unix socket `gritz driver` listens on inside a
// container. Each container has its own filesystem, so drivers sharing a
// network namespace never collide, and nothing outside the container can reach
// the driver over the network.
const DriverAddr = "unix:///gritz/driver.sock"

// Backend runs task sandboxes as containers on the local Docker daemon whose
// main process is the driver server.
type Backend struct {
	docker   *client.Client
	runnerID string
	log      *slog.Logger
	// idleTimeout is how long a Run stream may go without a message; tests
	// shorten it.
	idleTimeout time.Duration
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
		docker:      docker,
		runnerID:    opts.RunnerID,
		log:         cmp.Or(opts.Log, slog.Default()),
		idleTimeout: driverclient.DefaultIdleTimeout,
	}, nil
}

func (b *Backend) Close() error {
	return b.docker.Close()
}

// ValidateWorkspace reports whether the workspace's container config is
// usable. Any network mode is: the driver server listens on a unix socket in
// the container's filesystem, not on a port.
func (b *Backend) ValidateWorkspace(ws *workspace.Workspace) error {
	return ws.Container.Validate()
}

// discard force-removes a container create made and failed to provision: the
// runner has no record of it, so nothing else would. Failures are logged:
// create returns the error that caused them.
func (b *Backend) discard(ctx context.Context, containerID string) {
	b.log.Info("removing container after failed create", "container", containerID)
	err := b.docker.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		b.log.Error("failed to remove container after failed create", "container", containerID, "err", err)
	}
}

// create creates the task's gritz-{taskID} container running the driver server
// and copies the driver binary into it, along with the driver's state
// directory, which a non-root driver cannot create itself. spec.Files arrive in
// the RunSpec, which the driver provisions on the first run. The container gets
// only the workspace's environment: the run's env, secrets and token arrive in
// the RunSpec too, so a container reused for a later run never holds stale
// ones.
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
			Cmd:        []string{backend.BinaryPath, "driver", "--addr", DriverAddr},
			Env:        wc.Environ(),
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

	files := []backend.File{
		{Path: backend.BinaryPath, Data: binData, Mode: 0755},
		{Path: path.Dir(runrecord.Path), Mode: 0777, Dir: true},
	}
	if err := b.copyFiles(ctx, resp.ID, files); err != nil {
		b.discard(context.WithoutCancel(ctx), resp.ID)
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

// execConn opens a connection to the container's driver server through a
// Docker exec of `gritz tool proxy`: writes go to the exec's stdin, and reads
// come from its demultiplexed stdout.
func (b *Backend) execConn(ctx context.Context, containerID string) (net.Conn, error) {
	exec, err := b.docker.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		AttachStdin:  true,
		AttachStdout: true,
		Cmd:          []string{backend.BinaryPath, "tool", "proxy", "--addr", DriverAddr},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create driver proxy exec: %w", err)
	}
	resp, err := b.docker.ContainerExecAttach(ctx, exec.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to attach to driver proxy exec: %w", err)
	}
	pr, pw := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(pw, io.Discard, resp.Reader)
		pw.CloseWithError(err)
	}()
	return &execConn{Conn: resp.Conn, stdout: pr}, nil
}

// execConn is a hijacked exec attach connection whose reads are the exec's
// demultiplexed stdout.
type execConn struct {
	net.Conn
	stdout *io.PipeReader
}

func (c *execConn) Read(p []byte) (int, error) {
	return c.stdout.Read(p)
}

func (c *execConn) Close() error {
	_ = c.stdout.Close()
	return c.Conn.Close()
}
