package experimentaldocker

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/dockerx"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/poll"
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
	// The runner reaches the driver server through a port published from the
	// container's own network namespace, which these modes do not give it.
	for _, mode := range []string{"host", "none", "container:abc"} {
		t.Run("rejects "+mode, func(t *testing.T) {
			ws := &workspace.Workspace{Container: workspace.Container{Image: "alpine:latest", NetworkMode: mode}}
			assert.ErrorContains(t, be.ValidateWorkspace(ws), "not supported by the experimental docker backend")
		})
	}
}

func TestPublishedPort(t *testing.T) {
	tests := []struct {
		name  string
		ports nat.PortMap
		want  string
	}{
		{
			name:  "bound port",
			ports: nat.PortMap{"8080/tcp": {{HostIP: "127.0.0.1", HostPort: "49153"}}},
			want:  "49153",
		},
		{
			// An unspecified publish IP binds IPv4 and IPv6 on the same port.
			name: "several bindings",
			ports: nat.PortMap{"8080/tcp": {
				{HostIP: "0.0.0.0", HostPort: "49153"},
				{HostIP: "::", HostPort: "49153"},
			}},
			want: "49153",
		},
		{
			name: "other ports ignored",
			ports: nat.PortMap{
				"9090/tcp": {{HostIP: "127.0.0.1", HostPort: "49999"}},
				"8080/tcp": {{HostIP: "127.0.0.1", HostPort: "49153"}},
			},
			want: "49153",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port, err := publishedPort(container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{ID: "abc"},
				NetworkSettings:   networkSettings(tt.ports),
			})
			assert.NilError(t, err)
			assert.Equal(t, port, tt.want)
		})
	}
}

// networkSettings returns NetworkSettings with ports, without naming the
// deprecated NetworkSettingsBase that holds them.
func networkSettings(ports nat.PortMap) *container.NetworkSettings {
	ns := &container.NetworkSettings{}
	ns.Ports = ports
	return ns
}

func TestPublishedPort_Unbound(t *testing.T) {
	tests := []struct {
		name     string
		settings *container.NetworkSettings
	}{
		{name: "no network settings"},
		{name: "no ports", settings: &container.NetworkSettings{}},
		{
			// Exposed but not published.
			name:     "exposed only",
			settings: networkSettings(nat.PortMap{"8080/tcp": nil}),
		},
		{
			name:     "other port",
			settings: networkSettings(nat.PortMap{"9090/tcp": {{HostIP: "127.0.0.1", HostPort: "49153"}}}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := publishedPort(container.InspectResponse{
				ContainerJSONBase: &container.ContainerJSONBase{ID: "abc"},
				NetworkSettings:   tt.settings,
			})
			assert.ErrorContains(t, err, "no host port published")
		})
	}
}

// A started container's published driver port is found and reaches the
// container.
func TestDriverAddr_PublishedPort(t *testing.T) {
	t.Parallel()
	be, err := New(Options{RunnerID: "test-runner"})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = be.Close() })
	_, id := startContainer(t, []string{"sh", "-c", "while :; do echo hello | nc -l -p 8080; done"})

	addr, err := be.driverAddr(t.Context(), id)
	assert.NilError(t, err)
	host, _, err := net.SplitHostPort(addr)
	assert.NilError(t, err)
	assert.Equal(t, host, loopback)

	// nc may not be listening yet; docker-proxy accepts and closes until it is.
	var got string
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			return poll.Continue("dial: %v", err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		b, _ := io.ReadAll(conn)
		if got = strings.TrimSpace(string(b)); got == "" {
			return poll.Continue("no data from container yet")
		}
		return poll.Success()
	}, poll.WithTimeout(10*time.Second), poll.WithDelay(100*time.Millisecond))
	assert.Equal(t, got, "hello")
}

// A container create made has its driver server reachable through the
// published port, including after a restart that may bind a different one.
func TestCreate_DriverReachable(t *testing.T) {
	prebuiltDir, err := filepath.Abs("../../../../prebuilt")
	assert.NilError(t, err)
	t.Setenv("GRITZ_PREBUILT_DIR", prebuiltDir)
	be, err := New(Options{RunnerID: "test-runner"})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = be.Close() })
	taskID := rand.Int64N(1 << 40)

	id, err := be.create(t.Context(), &backend.Spec{
		TaskID:    taskID,
		Workspace: &workspace.Workspace{Container: workspace.Container{Image: "alpine:latest"}},
	})
	assert.NilError(t, err)
	t.Cleanup(func() {
		_ = be.docker.ContainerRemove(context.Background(), id, container.RemoveOptions{Force: true})
	})

	for range 2 {
		assert.NilError(t, be.docker.ContainerStart(t.Context(), id, container.StartOptions{}))
		client, err := be.dial(t.Context(), id)
		assert.NilError(t, err)
		assert.NilError(t, be.waitReady(t.Context(), client))
		assert.NilError(t, be.docker.ContainerStop(t.Context(), id, container.StopOptions{}))
	}
}

// A running container with no published driver port cannot be dialed.
func TestDriverAddr_Unpublished(t *testing.T) {
	t.Parallel()
	be, err := New(Options{RunnerID: "test-runner"})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = be.Close() })
	_, id := startContainerWith(t,
		&container.Config{Image: "alpine:latest", Cmd: []string{"sleep", "30"}},
		&container.HostConfig{})

	_, err = be.driverAddr(t.Context(), id)
	assert.ErrorContains(t, err, "no host port published")
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

// startContainer creates and starts an alpine container running cmd with its
// driver port published as create publishes it, returning a raw client and its
// id. It is removed on cleanup.
func startContainer(t *testing.T, cmd []string) (*dockerclient.Client, string) {
	t.Helper()
	return startContainerWith(t,
		&container.Config{Image: "alpine:latest", Cmd: cmd, ExposedPorts: nat.PortSet{driverPort: {}}},
		&container.HostConfig{PortBindings: nat.PortMap{driverPort: {{HostIP: loopback, HostPort: "0"}}}})
}

// startContainerWith creates and starts an alpine container with cfg and
// hostCfg, returning a raw client and its id. It is removed on cleanup.
func startContainerWith(t *testing.T, cfg *container.Config, hostCfg *container.HostConfig) (*dockerclient.Client, string) {
	t.Helper()
	docker, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	assert.NilError(t, err)
	t.Cleanup(func() { _ = docker.Close() })
	_, err = dockerx.ImageEnsure(t.Context(), docker, dockerx.ImageEnsureOptions{Ref: cfg.Image})
	assert.NilError(t, err)
	resp, err := docker.ContainerCreate(t.Context(), cfg, hostCfg, nil, nil, "")
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
