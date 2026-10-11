package experimentaldocker

import (
	"cmp"
	"context"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/dockerx"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
)

func TestNewRunSpec(t *testing.T) {
	spec := &backend.Spec{
		TaskID:  7,
		Version: 3,
		Env: []string{
			"GRITZ_TASK_ID=7",
			"GRITZ_TOKEN=tok",
			"GRITZ_SERVER=http://server",
			"GH_TOKEN=ghp_secret",
			"GRITZ_SECRETS=GH_TOKEN",
		},
	}

	rs, err := newRunSpec(spec)

	// The server URL, token and secrets get their own fields; GRITZ_SECRETS
	// is dropped, since the secrets map is the mask.
	assert.NilError(t, err)
	assert.DeepEqual(t, rs, &driverv1.RunSpec{
		TaskId:    7,
		ServerUrl: "http://server",
		Token:     "tok",
		Env: map[string]string{
			"GRITZ_TASK_ID": "7",
			"GRITZ_TOKEN":   "tok",
			"GRITZ_SERVER":  "http://server",
		},
		Secrets: map[string]string{"GH_TOKEN": "ghp_secret"},
	}, protocmp.Transform())
}

func TestValidateWorkspace(t *testing.T) {
	be := &Backend{}
	for _, mode := range []string{"", "bridge", "gritz-net"} {
		t.Run("accepts "+cmp.Or(mode, "default"), func(t *testing.T) {
			ws := &workspace.Workspace{Container: workspace.Container{Image: "alpine:latest", NetworkMode: mode}}
			assert.NilError(t, be.ValidateWorkspace(ws))
		})
	}
	// The runner reaches the driver server at the container's own IP, which
	// these modes do not give it.
	for _, mode := range []string{"host", "none", "container:abc"} {
		t.Run("rejects "+mode, func(t *testing.T) {
			ws := &workspace.Workspace{Container: workspace.Container{Image: "alpine:latest", NetworkMode: mode}}
			assert.ErrorContains(t, be.ValidateWorkspace(ws), "not supported by the experimental docker backend")
		})
	}
}

func TestDriverAddr(t *testing.T) {
	tests := []struct {
		name string
		info container.InspectResponse
		want string
	}{
		{
			name: "first network by name",
			info: container.InspectResponse{
				Config: &container.Config{},
				NetworkSettings: &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{
					"zeta":  {IPAddress: "10.0.0.2"},
					"alpha": {IPAddress: "10.0.0.1"},
				}},
			},
			want: "10.0.0.1:8080",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, err := driverAddr(tt.info)
			assert.NilError(t, err)
			assert.Equal(t, addr, tt.want)
		})
	}
}

func TestDriverAddr_NoIP(t *testing.T) {
	_, err := driverAddr(container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{ID: "abc"},
		Config:            &container.Config{},
		NetworkSettings:   &container.NetworkSettings{},
	})
	assert.ErrorContains(t, err, "no IP address")
}

func TestLaunch_ReuseGoneReturnsErrGone(t *testing.T) {
	t.Parallel()
	be, err := New(Options{RunnerID: "test-runner"})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = be.Close() })

	_, err = be.Launch(t.Context(), &backend.Spec{TaskID: 1}, &backend.Handle{Type: HandleType, ID: "nonexistent-container-id"})
	assert.ErrorIs(t, err, backend.ErrGone)
}

func TestProbe_Gone(t *testing.T) {
	t.Parallel()
	be, err := New(Options{RunnerID: "test-runner"})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = be.Close() })

	st, err := be.Probe(t.Context(), backend.Handle{Type: HandleType, ID: "nonexistent-container-id", Data: []byte(`{"version":1}`)})
	assert.NilError(t, err)
	assert.Equal(t, st, backend.StateGone)
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

func TestProbe_States(t *testing.T) {
	t.Parallel()
	be, err := New(Options{RunnerID: "test-runner"})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = be.Close() })
	data := []byte(`{"version":1}`)

	// A running container is running whatever its driver reports — here there
	// is no driver at all, as after a run's Finished but before Wait stops the
	// container: the runner must not launch on it then (#1684).
	_, running := startContainer(t, []string{"sleep", "30"})
	st, err := be.Probe(t.Context(), backend.Handle{Type: HandleType, ID: running, Data: data})
	assert.NilError(t, err)
	assert.Equal(t, st, backend.StateRunning)

	// A stopped container is an exited husk.
	docker, exited := startContainer(t, []string{"true"})
	assert.NilError(t, dockerx.ContainerWait(t.Context(), docker, exited, container.WaitConditionNotRunning))
	st, err = be.Probe(t.Context(), backend.Handle{Type: HandleType, ID: exited, Data: data})
	assert.NilError(t, err)
	assert.Equal(t, st, backend.StateExited)
}

// A fresh Launch that fails after creating its container removes it: the
// runner gets no handle, so nothing else would (#1685).
func TestLaunch_FreshFailureRemovesContainer(t *testing.T) {
	prebuiltDir, err := filepath.Abs("../../../../prebuilt")
	assert.NilError(t, err)
	t.Setenv("GRITZ_PREBUILT_DIR", prebuiltDir)
	be, err := New(Options{RunnerID: "test-runner"})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = be.Close() })
	// The driver server cannot answer within a nanosecond.
	be.readyTimeout = time.Nanosecond
	taskID := rand.Int64N(1 << 40)
	name := fmt.Sprintf("gritz-%d", taskID)
	t.Cleanup(func() {
		_ = be.docker.ContainerRemove(context.Background(), name, container.RemoveOptions{Force: true})
	})

	_, err = be.Launch(t.Context(), &backend.Spec{
		TaskID:    taskID,
		Version:   1,
		Workspace: &workspace.Workspace{Container: workspace.Container{Image: "alpine:latest"}},
	}, nil)
	assert.ErrorContains(t, err, "did not become ready")

	_, err = be.docker.ContainerInspect(t.Context(), name)
	assert.Assert(t, cerrdefs.IsNotFound(err), "container %s was left behind: %v", name, err)
}

// A reuse Launch that fails after starting the container stops it but keeps
// it: Probe would otherwise report it running with no run on it, and the
// runner would never launch on it again (#1685).
func TestLaunch_ReuseFailureStopsContainer(t *testing.T) {
	t.Parallel()
	be, err := New(Options{RunnerID: "test-runner"})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = be.Close() })
	be.readyTimeout = time.Nanosecond

	// A stopped container with no driver server, which exits on SIGTERM.
	docker, id := startContainer(t, []string{"sh", "-c", "trap 'exit 0' TERM; while :; do sleep 0.1; done"})
	assert.NilError(t, docker.ContainerStop(t.Context(), id, container.StopOptions{}))
	h := backend.Handle{Type: HandleType, ID: id, Data: []byte(`{"version":1}`)}

	_, err = be.Launch(t.Context(), &backend.Spec{
		TaskID:    1,
		Version:   2,
		Workspace: &workspace.Workspace{},
	}, &h)
	assert.ErrorContains(t, err, "did not become ready")

	st, err := be.Probe(t.Context(), h)
	assert.NilError(t, err)
	assert.Equal(t, st, backend.StateExited)
}

// A Wait whose driver never answers gives up after lostTimeout: it stops the
// container and reports the run lost rather than retrying forever (#1687).
func TestWait_DriverUnreachableGivesUp(t *testing.T) {
	t.Parallel()
	be, err := New(Options{RunnerID: "test-runner"})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = be.Close() })
	be.lostTimeout = time.Second
	// A running container with nothing listening on the driver port.
	docker, id := startContainer(t, []string{"sh", "-c", "trap 'exit 0' TERM; sleep 300 & wait"})

	code, err := be.Wait(t.Context(), backend.Handle{Type: HandleType, ID: id, Data: []byte(`{"version":1}`)})

	assert.NilError(t, err)
	assert.Equal(t, code, backend.ExitLost)
	info, err := docker.ContainerInspect(t.Context(), id)
	assert.NilError(t, err)
	assert.Assert(t, !info.State.Running, "container %s is still running", id)
}
