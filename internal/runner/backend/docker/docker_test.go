package docker

import (
	"testing"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/x/dockerx"
	"gotest.tools/v3/assert"
)

// testBackend returns a Docker backend and a raw client for test setup.
func testBackend(t *testing.T) (*Backend, *dockerclient.Client) {
	t.Helper()
	be, err := New(Options{RunnerID: "test-runner"})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = be.Close() })

	docker, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	assert.NilError(t, err)
	t.Cleanup(func() { _ = docker.Close() })

	// Ensure alpine is available for the short-lived test containers.
	_, err = dockerx.ImageEnsure(t.Context(), docker, dockerx.ImageEnsureOptions{Ref: "alpine:latest"})
	assert.NilError(t, err)
	return be, docker
}

// startContainer creates and starts an alpine container running cmd, returning
// its id. It is removed on cleanup.
func startContainer(t *testing.T, docker *dockerclient.Client, cmd []string) string {
	t.Helper()
	resp, err := docker.ContainerCreate(t.Context(),
		&container.Config{Image: "alpine:latest", Cmd: cmd},
		&container.HostConfig{}, nil, nil, "")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = docker.ContainerRemove(t.Context(), resp.ID, container.RemoveOptions{Force: true}) })
	assert.NilError(t, docker.ContainerStart(t.Context(), resp.ID, container.StartOptions{}))
	return resp.ID
}

func TestWait_ExitCode(t *testing.T) {
	t.Parallel()
	be, docker := testBackend(t)
	id := startContainer(t, docker, []string{"sh", "-c", "exit 7"})

	// Wait returns the container's real exit code.
	code, err := be.Wait(t.Context(), backend.Handle{Type: HandleType, ID: id})
	assert.NilError(t, err)
	assert.Equal(t, code, backend.ExitCode(7))
}

func TestWait_AlreadyExited(t *testing.T) {
	t.Parallel()
	be, docker := testBackend(t)
	id := startContainer(t, docker, []string{"sh", "-c", "exit 3"})

	// Let the container fully stop before waiting: WaitConditionNotRunning is
	// level-triggered, so the stored exit code is returned immediately (this is
	// what closes the launch→persist race and the boot Probe→Wait TOCTOU).
	assert.NilError(t, dockerx.ContainerWait(t.Context(), docker, id, container.WaitConditionNotRunning))

	code, err := be.Wait(t.Context(), backend.Handle{Type: HandleType, ID: id})
	assert.NilError(t, err)
	assert.Equal(t, code, backend.ExitCode(3))
}

func TestWait_RemovedReportsLost(t *testing.T) {
	t.Parallel()
	be, docker := testBackend(t)
	id := startContainer(t, docker, []string{"sh", "-c", "exit 0"})
	assert.NilError(t, dockerx.ContainerWait(t.Context(), docker, id, container.WaitConditionNotRunning))
	assert.NilError(t, docker.ContainerRemove(t.Context(), id, container.RemoveOptions{Force: true}))

	// A removed container has no code to recover → report lost.
	code, err := be.Wait(t.Context(), backend.Handle{Type: HandleType, ID: id})
	assert.NilError(t, err)
	assert.Equal(t, code, backend.ExitLost)
}

func TestProbe_States(t *testing.T) {
	t.Parallel()
	be, docker := testBackend(t)

	// Running container.
	running := startContainer(t, docker, []string{"sleep", "30"})
	st, err := be.Probe(t.Context(), backend.Handle{ID: running})
	assert.NilError(t, err)
	assert.Equal(t, st, backend.StateRunning)

	// Stopped-but-present container is an exited husk.
	exited := startContainer(t, docker, []string{"sh", "-c", "exit 0"})
	assert.NilError(t, dockerx.ContainerWait(t.Context(), docker, exited, container.WaitConditionNotRunning))
	st, err = be.Probe(t.Context(), backend.Handle{ID: exited})
	assert.NilError(t, err)
	assert.Equal(t, st, backend.StateExited)

	// A missing container is gone.
	st, err = be.Probe(t.Context(), backend.Handle{ID: "nonexistent-container-id"})
	assert.NilError(t, err)
	assert.Equal(t, st, backend.StateGone)
}

func TestLaunch_ReuseGoneReturnsErrGone(t *testing.T) {
	t.Parallel()
	be, _ := testBackend(t)

	// A reuse handle whose container no longer exists must surface ErrGone rather
	// than creating a fresh sandbox (one-sandbox-per-task invariant).
	_, err := be.Launch(t.Context(), &backend.Spec{TaskID: 1}, &backend.Handle{Type: HandleType, ID: "nonexistent-container-id"})
	assert.ErrorIs(t, err, backend.ErrGone)
}
