package docker

import (
	"cmp"
	"context"
	"math/rand/v2"
	"testing"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/dockerx"
	"gotest.tools/v3/assert"
)

func TestValidateWorkspace(t *testing.T) {
	be := &Backend{}
	// The driver server listens on a unix socket, so any network mode works.
	for _, mode := range []string{"", "bridge", "gritz-net", "host", "none", "container:abc"} {
		t.Run("accepts "+cmp.Or(mode, "default"), func(t *testing.T) {
			ws := &workspace.Workspace{Container: workspace.Container{Image: "alpine:latest", NetworkMode: mode}}
			assert.NilError(t, be.ValidateWorkspace(ws))
		})
	}
	t.Run("rejects missing image", func(t *testing.T) {
		ws := &workspace.Workspace{Container: workspace.Container{NetworkMode: "host"}}
		assert.ErrorContains(t, be.ValidateWorkspace(ws), "image is required")
	})
}

// A sandbox answers Status through the exec tunnel to the driver's socket,
// whatever its network mode — including two sharing the host's network
// namespace.
func TestDial_TunnelsThroughExec(t *testing.T) {
	be := newTestBackend(t)
	for _, mode := range []string{"", "none", "host", "host"} {
		sb, err := be.Create(t.Context(), &backend.Spec{
			TaskID:    rand.Int64N(1 << 40),
			Workspace: &workspace.Workspace{Container: workspace.Container{Image: "alpine:latest", NetworkMode: mode}},
		})
		assert.NilError(t, err)
		t.Cleanup(func() {
			_ = be.docker.ContainerRemove(context.Background(), sb.Handle().ID, container.RemoveOptions{Force: true})
		})
		assert.NilError(t, sb.Boot(t.Context()))

		dialReady(t, sb)
	}
}

// startContainer creates and starts an alpine container running cmd, returning
// a raw client and its id. It is removed on cleanup.
func startContainer(t *testing.T, cmd []string) (*dockerclient.Client, string) {
	t.Helper()
	docker, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	assert.NilError(t, err)
	t.Cleanup(func() { _ = docker.Close() })
	_, err = dockerx.ImageEnsure(t.Context(), docker, dockerx.ImageEnsureOptions{Ref: "alpine:latest"})
	assert.NilError(t, err)
	resp, err := docker.ContainerCreate(t.Context(),
		&container.Config{Image: "alpine:latest", Cmd: cmd},
		&container.HostConfig{}, nil, nil, "")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = docker.ContainerRemove(t.Context(), resp.ID, container.RemoveOptions{Force: true}) })
	assert.NilError(t, docker.ContainerStart(t.Context(), resp.ID, container.StartOptions{}))
	return docker, resp.ID
}
