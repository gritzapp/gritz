package experimentaldocker

import (
	"cmp"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"

	"github.com/docker/docker/api/types/container"
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
	// The driver server is reached over its socket, whatever the network.
	for _, mode := range []string{"", "bridge", "gritz-net", "host"} {
		t.Run("accepts "+cmp.Or(mode, "default"), func(t *testing.T) {
			ws := &workspace.Workspace{Container: workspace.Container{Image: "alpine:latest", NetworkMode: mode}}
			assert.NilError(t, be.ValidateWorkspace(ws))
		})
	}
	for _, mode := range []string{"none", "container:abc"} {
		t.Run("rejects "+mode, func(t *testing.T) {
			ws := &workspace.Workspace{Container: workspace.Container{Image: "alpine:latest", NetworkMode: mode}}
			assert.ErrorContains(t, be.ValidateWorkspace(ws), "not supported by the experimental docker backend")
		})
	}
}

func TestLaunch_ReuseGoneReturnsErrGone(t *testing.T) {
	t.Parallel()
	be := newBackend(t)

	_, err := be.Launch(t.Context(), &backend.Spec{TaskID: 1}, &backend.Handle{Type: HandleType, ID: "nonexistent-container-id"})
	assert.ErrorIs(t, err, backend.ErrGone)
}

func TestProbe_Gone(t *testing.T) {
	t.Parallel()
	be := newBackend(t)

	st, err := be.Probe(t.Context(), backend.Handle{Type: HandleType, ID: "nonexistent-container-id", Data: []byte(`{"version":1}`)})
	assert.NilError(t, err)
	assert.Equal(t, st, backend.StateGone)
}

// newBackend returns a backend under a runner ID of its own, whose directory is
// removed on cleanup.
func newBackend(t *testing.T) *Backend {
	t.Helper()
	be, err := New(Options{RunnerID: fmt.Sprintf("test-runner-%d", rand.Int64N(1<<40))})
	assert.NilError(t, err)
	t.Cleanup(func() {
		_ = be.Close()
		_ = os.RemoveAll(be.runDir)
	})
	return be
}

// startContainer creates and starts an alpine container running cmd, labelled
// with a task ID, returning a raw client and its id. It is removed on cleanup.
func startContainer(t *testing.T, cmd []string) (*dockerclient.Client, string) {
	t.Helper()
	docker, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	assert.NilError(t, err)
	t.Cleanup(func() { _ = docker.Close() })
	_, err = dockerx.ImageEnsure(t.Context(), docker, dockerx.ImageEnsureOptions{Ref: "alpine:latest"})
	assert.NilError(t, err)
	resp, err := docker.ContainerCreate(t.Context(),
		&container.Config{
			Image:  "alpine:latest",
			Cmd:    cmd,
			Labels: map[string]string{"gritz.task": fmt.Sprint(rand.Int64N(1 << 40))},
		},
		&container.HostConfig{}, nil, nil, "")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = docker.ContainerRemove(t.Context(), resp.ID, container.RemoveOptions{Force: true}) })
	assert.NilError(t, docker.ContainerStart(t.Context(), resp.ID, container.StartOptions{}))
	return docker, resp.ID
}

func TestProbe_States(t *testing.T) {
	t.Parallel()
	be := newBackend(t)
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
	be := newCreateBackend(t)
	// The driver server cannot answer within a nanosecond.
	be.readyTimeout = time.Nanosecond
	taskID := rand.Int64N(1 << 40)
	name := fmt.Sprintf("gritz-%d", taskID)
	t.Cleanup(func() {
		_ = be.docker.ContainerRemove(context.Background(), name, container.RemoveOptions{Force: true})
	})

	_, err := be.Launch(t.Context(), &backend.Spec{
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
	be := newBackend(t)
	be.readyTimeout = time.Nanosecond

	// A stopped container with no driver server, which exits on SIGTERM.
	docker, id := startContainer(t, []string{"sh", "-c", "trap 'exit 0' TERM; while :; do sleep 0.1; done"})
	assert.NilError(t, docker.ContainerStop(t.Context(), id, container.StopOptions{}))
	h := backend.Handle{Type: HandleType, ID: id, Data: []byte(`{"version":1}`)}

	_, err := be.Launch(t.Context(), &backend.Spec{
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
	be := newBackend(t)
	be.lostTimeout = time.Second
	// A running container with no driver server.
	docker, id := startContainer(t, []string{"sh", "-c", "trap 'exit 0' TERM; sleep 300 & wait"})

	code, err := be.Wait(t.Context(), backend.Handle{Type: HandleType, ID: id, Data: []byte(`{"version":1}`)})

	assert.NilError(t, err)
	assert.Equal(t, code, backend.ExitLost)
	info, err := docker.ContainerInspect(t.Context(), id)
	assert.NilError(t, err)
	assert.Assert(t, !info.State.Running, "container %s is still running", id)
}

// newCreateBackend returns a newBackend that can create containers.
func newCreateBackend(t *testing.T) *Backend {
	t.Helper()
	prebuiltDir, err := filepath.Abs("../../../../prebuilt")
	assert.NilError(t, err)
	t.Setenv("GRITZ_PREBUILT_DIR", prebuiltDir)
	return newBackend(t)
}

// createContainer creates a task's container with be, removing it on cleanup.
func createContainer(t *testing.T, be *Backend, taskID int64) string {
	t.Helper()
	id, err := be.create(t.Context(), &backend.Spec{
		TaskID:    taskID,
		Workspace: &workspace.Workspace{Container: workspace.Container{Image: "alpine:latest"}},
	})
	assert.NilError(t, err)
	t.Cleanup(func() {
		_ = be.docker.ContainerRemove(context.Background(), id, container.RemoveOptions{Force: true})
	})
	return id
}

// A container from create runs the driver server on the socket in its task's
// directory, which the runner dials (#1697).
func TestCreate_DriverAnswersOverSocket(t *testing.T) {
	be := newCreateBackend(t)
	taskID := rand.Int64N(1 << 40)
	id := createContainer(t, be, taskID)
	assert.NilError(t, be.docker.ContainerStart(t.Context(), id, container.StartOptions{}))

	client, err := be.dial(t.Context(), id)
	assert.NilError(t, err)
	err = be.waitReady(t.Context(), client)

	assert.NilError(t, err)
	fi, err := os.Stat(be.runDir)
	assert.NilError(t, err)
	assert.Equal(t, fi.Mode().Perm(), os.FileMode(0700))
	fi, err = os.Stat(be.taskDir(taskID))
	assert.NilError(t, err)
	assert.Equal(t, fi.Mode().Perm(), os.FileMode(0777))
	fi, err = os.Stat(filepath.Join(be.taskDir(taskID), DriverSocket))
	assert.NilError(t, err)
	assert.Equal(t, fi.Mode().Perm(), os.FileMode(0666))
}

func TestDestroy_RemovesTaskDir(t *testing.T) {
	be := newCreateBackend(t)
	taskID := rand.Int64N(1 << 40)
	id := createContainer(t, be, taskID)

	err := be.Destroy(t.Context(), backend.Handle{Type: HandleType, ID: id})

	assert.NilError(t, err)
	_, err = be.docker.ContainerInspect(t.Context(), id)
	assert.Assert(t, cerrdefs.IsNotFound(err), "container %s was left behind: %v", id, err)
	_, err = os.Stat(be.taskDir(taskID))
	assert.Assert(t, os.IsNotExist(err), "task directory was left behind: %v", err)
}
