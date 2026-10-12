package docker

import (
	"context"
	"math/rand/v2"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/poll"
)

// newTestBackend returns a backend on the local daemon that can provision the
// prebuilt driver binary.
func newTestBackend(t *testing.T) *Backend {
	t.Helper()
	prebuiltDir, err := filepath.Abs("../../../../prebuilt")
	assert.NilError(t, err)
	t.Setenv("GRITZ_PREBUILT_DIR", prebuiltDir)
	be, err := New(Options{RunnerID: "test-runner"})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = be.Close() })
	return be
}

// create creates a sandbox for a random task, removed on cleanup.
func create(t *testing.T, be *Backend) backend.SandboxV2 {
	t.Helper()
	sb, err := be.Create(t.Context(), &backend.Spec{
		TaskID:    rand.Int64N(1 << 40),
		Workspace: &workspace.Workspace{Container: workspace.Container{Image: "alpine:latest"}},
	})
	assert.NilError(t, err)
	t.Cleanup(func() {
		_ = be.docker.ContainerRemove(context.Background(), sb.Handle().ID, container.RemoveOptions{Force: true})
	})
	return sb
}

func assertStatus(t *testing.T, sb backend.SandboxV2, want backend.Status) {
	t.Helper()
	st, err := sb.Status(t.Context())
	assert.NilError(t, err)
	assert.Equal(t, st, want)
}

// dialReady dials the sandbox's driver and waits for it to answer.
func dialReady(t *testing.T, sb backend.SandboxV2) {
	t.Helper()
	client, err := sb.Dial(t.Context(), "jwt")
	assert.NilError(t, err)
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		if _, err := client.Status(t.Context(), &driverv1.StatusRequest{}); err != nil {
			return poll.Continue("driver not ready: %v", err)
		}
		return poll.Success()
	}, poll.WithTimeout(30*time.Second), poll.WithDelay(250*time.Millisecond))
}

// A sandbox goes through its whole life: created, started, dialed, parked,
// restarted and destroyed, after which it is gone.
func TestSandbox_Lifecycle(t *testing.T) {
	be := newTestBackend(t)

	sb := create(t, be)

	// Create does not start the container: a created container is parked.
	h := sb.Handle()
	assert.Equal(t, h.Type, HandleType)
	info, err := be.docker.ContainerInspect(t.Context(), h.ID)
	assert.NilError(t, err)
	assert.Equal(t, info.ID, h.ID)
	assert.Equal(t, info.State.Status, "created")
	assertStatus(t, sb, backend.StatusParked)

	assert.NilError(t, sb.Start(t.Context()))
	assertStatus(t, sb, backend.StatusUp)
	// Starting a running container is a no-op.
	assert.NilError(t, sb.Start(t.Context()))
	dialReady(t, sb)

	assert.NilError(t, sb.Park(t.Context()))
	assertStatus(t, sb, backend.StatusParked)
	// Parking a stopped container is a no-op.
	assert.NilError(t, sb.Park(t.Context()))
	// A stopped container's driver cannot be reached.
	client, err := sb.Dial(t.Context(), "jwt")
	assert.NilError(t, err)
	_, err = client.Status(t.Context(), &driverv1.StatusRequest{})
	assert.Assert(t, err != nil)

	assert.NilError(t, sb.Start(t.Context()))
	assertStatus(t, sb, backend.StatusUp)
	dialReady(t, sb)

	assert.NilError(t, sb.Destroy(t.Context()))
	assertStatus(t, sb, backend.StatusGone)
	assert.ErrorIs(t, sb.Start(t.Context()), backend.ErrGone)
	assert.ErrorIs(t, sb.Park(t.Context()), backend.ErrGone)
	assert.NilError(t, sb.Destroy(t.Context()))
}

// A reopened handle reaches the same container.
func TestSandbox_Reopen(t *testing.T) {
	be := newTestBackend(t)
	sb := create(t, be)
	assert.NilError(t, sb.Start(t.Context()))

	reopened, err := be.Open(sb.Handle())
	assert.NilError(t, err)

	assert.DeepEqual(t, reopened.Handle(), sb.Handle())
	assertStatus(t, reopened, backend.StatusUp)
	dialReady(t, reopened)
}

func TestOpen(t *testing.T) {
	be := &Backend{}

	// Open does not check that the container exists.
	sb, err := be.Open(backend.Handle{Type: HandleType, ID: "nonexistent-container-id"})
	assert.NilError(t, err)
	assert.DeepEqual(t, sb.Handle(), backend.Handle{Type: HandleType, ID: "nonexistent-container-id"})
	// A handle the v1 Backend's Launch produced, which also carries its run's
	// version, opens the same container.
	sb, err = be.Open(backend.Handle{Type: HandleType, ID: "c1", Data: []byte(`{"version":1}`)})
	assert.NilError(t, err)
	assert.Equal(t, sb.Handle().ID, "c1")

	_, err = be.Open(backend.Handle{Type: "lambda-microvm", ID: "c1"})
	assert.ErrorContains(t, err, "is not")
	_, err = be.Open(backend.Handle{Type: HandleType})
	assert.ErrorContains(t, err, "no container id")
}

// A container that is not running — exited or paused — is parked.
func TestSandbox_Status(t *testing.T) {
	t.Parallel()
	be, err := New(Options{RunnerID: "test-runner"})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = be.Close() })
	open := func(id string) backend.SandboxV2 {
		sb, err := be.Open(backend.Handle{Type: HandleType, ID: id})
		assert.NilError(t, err)
		return sb
	}

	docker, running := startContainer(t, []string{"sleep", "30"})
	assertStatus(t, open(running), backend.StatusUp)

	_, paused := startContainer(t, []string{"sleep", "30"})
	assert.NilError(t, docker.ContainerPause(t.Context(), paused))
	assertStatus(t, open(paused), backend.StatusParked)
	assert.Assert(t, open(paused).Start(t.Context()) != nil)

	_, exited := startContainer(t, []string{"sh", "-c", "trap 'exit 0' TERM; sleep 300 & wait"})
	assert.NilError(t, docker.ContainerStop(t.Context(), exited, container.StopOptions{}))
	assertStatus(t, open(exited), backend.StatusParked)

	assertStatus(t, open("nonexistent-container-id"), backend.StatusGone)
}

// A sandbox's methods are safe for concurrent use, as the runner's supervisor,
// Kill and idle timer make them; run with -race.
func TestSandbox_Concurrent(t *testing.T) {
	be := newTestBackend(t)
	sb := create(t, be)
	assert.NilError(t, sb.Start(t.Context()))
	dialReady(t, sb)

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			assert.Check(t, sb.Start(t.Context()))
			st, err := sb.Status(t.Context())
			assert.Check(t, err)
			assert.Check(t, st == backend.StatusUp)
			client, err := sb.Dial(t.Context(), "jwt")
			if !assert.Check(t, err) {
				return
			}
			_, err = client.Status(t.Context(), &driverv1.StatusRequest{})
			assert.Check(t, err)
		})
	}
	wg.Wait()
}
