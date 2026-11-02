package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/poll"
)

// newRoot returns a fresh sandbox root, short enough for the drivers' socket
// paths, removed on cleanup.
func newRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "gritz-local-")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

// newTestBackend returns a backend on root running the prebuilt driver binary.
func newTestBackend(t *testing.T, root string) *Backend {
	t.Helper()
	binary, err := filepath.Abs("../../../../prebuilt/gritz-linux-" + runtime.GOARCH)
	assert.NilError(t, err)
	be, err := New(Options{Root: root, Binary: binary, Environ: os.Environ()})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = be.Close() })
	return be
}

// create creates a sandbox for a random task, destroyed on cleanup.
func create(t *testing.T, be *Backend) backend.Sandbox {
	t.Helper()
	sb, err := be.Create(t.Context(), &backend.Spec{TaskID: rand.Int64N(1 << 40)})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = sb.Destroy(context.Background()) })
	return sb
}

func assertStatus(t *testing.T, sb backend.Sandbox, want backend.Status) {
	t.Helper()
	st, err := sb.Status(t.Context())
	assert.NilError(t, err)
	assert.Equal(t, st, want)
}

// dialReady dials the sandbox's driver and waits for it to answer.
func dialReady(t *testing.T, sb backend.Sandbox) {
	t.Helper()
	client, err := sb.Dial(t.Context(), "jwt")
	assert.NilError(t, err)
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		if err := reach(t.Context(), client); err != nil {
			return poll.Continue("driver not ready: %v", err)
		}
		return poll.Success()
	}, poll.WithTimeout(30*time.Second), poll.WithDelay(50*time.Millisecond))
}

// A sandbox goes through its whole life: created, booted, dialed, suspended
// with its files kept, booted again and destroyed, after which it is gone.
func TestSandbox_Lifecycle(t *testing.T) {
	t.Parallel()
	be := newTestBackend(t, newRoot(t))

	sb := create(t, be)
	h := sb.Handle()
	assert.Equal(t, h.Type, HandleType)
	dir := filepath.Join(be.root, h.ID)

	// Create makes the directory and the driver's state directory, and starts
	// nothing: a created sandbox is suspended.
	info, err := os.Stat(filepath.Join(dir, "gritz"))
	assert.NilError(t, err)
	assert.Assert(t, info.IsDir())
	assertStatus(t, sb, backend.StatusSuspended)

	assert.NilError(t, sb.Boot(t.Context()))
	assertStatus(t, sb, backend.StatusUp)
	// Booting a running sandbox is a no-op.
	assert.NilError(t, sb.Boot(t.Context()))
	dialReady(t, sb)
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "kept"), []byte("kept"), 0o644))

	assert.NilError(t, sb.Suspend(t.Context()))
	assertStatus(t, sb, backend.StatusSuspended)
	// Suspending a suspended sandbox is a no-op.
	assert.NilError(t, sb.Suspend(t.Context()))
	// A suspended sandbox's driver cannot be reached.
	client, err := sb.Dial(t.Context(), "jwt")
	assert.NilError(t, err)
	assert.Assert(t, reach(t.Context(), client) != nil)
	// Its files stay.
	data, err := os.ReadFile(filepath.Join(dir, "kept"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), "kept")

	assert.NilError(t, sb.Boot(t.Context()))
	assertStatus(t, sb, backend.StatusUp)
	dialReady(t, sb)

	assert.NilError(t, sb.Destroy(t.Context()))
	assertStatus(t, sb, backend.StatusGone)
	_, err = os.Stat(dir)
	assert.Assert(t, errors.Is(err, os.ErrNotExist))
	assert.ErrorIs(t, sb.Boot(t.Context()), backend.ErrGone)
	assert.ErrorIs(t, sb.Suspend(t.Context()), backend.ErrGone)
	assert.NilError(t, sb.Destroy(t.Context()))
}

// Creating a sandbox whose directory exists fails, as a Docker name clash does.
func TestBackend_CreateExists(t *testing.T) {
	t.Parallel()
	be := newTestBackend(t, newRoot(t))
	sb := create(t, be)
	id := sb.Handle().ID

	var taskID int64
	_, err := fmt.Sscan(id, &taskID)
	assert.NilError(t, err)
	_, err = be.Create(t.Context(), &backend.Spec{TaskID: taskID})
	assert.ErrorContains(t, err, "file exists")
}

// A killed driver leaves its sandbox suspended, with its files, and the sandbox
// boots again.
func TestBackend_Kill(t *testing.T) {
	t.Parallel()
	be := newTestBackend(t, newRoot(t))
	sb := create(t, be)
	assert.NilError(t, sb.Boot(t.Context()))
	dialReady(t, sb)

	assert.NilError(t, be.Kill(sb.Handle()))
	assertStatus(t, sb, backend.StatusSuspended)
	// Killing a sandbox with no driver is a no-op.
	assert.NilError(t, be.Kill(sb.Handle()))

	assert.NilError(t, sb.Boot(t.Context()))
	assertStatus(t, sb, backend.StatusUp)
	dialReady(t, sb)
}

// A second backend on the same root, as a restarted runner has, sees a sandbox
// the first booted and suspends it.
func TestBackend_SecondBackend(t *testing.T) {
	t.Parallel()
	root := newRoot(t)
	first := newTestBackend(t, root)
	sb := create(t, first)
	assert.NilError(t, sb.Boot(t.Context()))

	second := newTestBackend(t, root)
	reopened, err := second.Open(sb.Handle())
	assert.NilError(t, err)
	assert.DeepEqual(t, reopened.Handle(), sb.Handle())
	assertStatus(t, reopened, backend.StatusUp)
	dialReady(t, reopened)

	assert.NilError(t, reopened.Suspend(t.Context()))
	assertStatus(t, reopened, backend.StatusSuspended)
	assertStatus(t, sb, backend.StatusSuspended)
}

// Two sandboxes run side by side, each with its own driver.
func TestBackend_SideBySide(t *testing.T) {
	t.Parallel()
	be := newTestBackend(t, newRoot(t))
	a := create(t, be)
	b := create(t, be)
	assert.NilError(t, a.Boot(t.Context()))
	assert.NilError(t, b.Boot(t.Context()))
	dialReady(t, a)
	dialReady(t, b)

	assert.NilError(t, a.Suspend(t.Context()))
	assertStatus(t, a, backend.StatusSuspended)
	assertStatus(t, b, backend.StatusUp)
	dialReady(t, b)
}

func TestNew(t *testing.T) {
	t.Parallel()
	_, err := New(Options{Root: "/" + strings.Repeat("a", 80), Binary: "/gritz"})
	assert.ErrorContains(t, err, "too long")
}

func TestOpen(t *testing.T) {
	t.Parallel()
	be := newTestBackend(t, newRoot(t))

	// Open does not check that the directory exists.
	sb, err := be.Open(backend.Handle{Type: HandleType, ID: "12345"})
	assert.NilError(t, err)
	assert.DeepEqual(t, sb.Handle(), backend.Handle{Type: HandleType, ID: "12345"})
	assertStatus(t, sb, backend.StatusGone)

	_, err = be.Open(backend.Handle{Type: "docker", ID: "12345"})
	assert.ErrorContains(t, err, "is not")
	_, err = be.Open(backend.Handle{Type: HandleType, ID: "../etc"})
	assert.ErrorContains(t, err, "not a task id")
}

// reach opens a Run stream on client, which proves the driver answers a bidi
// stream over the transport: it answers Hello, or, while an earlier reach's
// stream is still closing, refuses this one. It returns any other outcome as an
// error.
func reach(ctx context.Context, client driverv1connect.DriverServiceClient) error {
	stream, err := client.Run(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = stream.CloseResponse() }()
	if err := stream.Send(&driverv1.RunRequest{}); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	msg, err := stream.Receive()
	if connect.CodeOf(err) == connect.CodeUnavailable && strings.Contains(err.Error(), "another Run stream is open") {
		return nil
	}
	if err != nil {
		return err
	}
	if msg.GetHello() == nil {
		return fmt.Errorf("unexpected run response: %v", msg)
	}
	return nil
}
