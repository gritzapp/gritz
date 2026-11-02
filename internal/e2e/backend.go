package e2e

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/backend/local"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"gotest.tools/v3/poll"
)

// Backend is the harness's local backend, and the backend.Backend the runner
// is given.
type Backend struct {
	t       *testing.T
	local   *local.Backend
	root    string
	timeout time.Duration
}

var _ backend.Backend = (*Backend)(nil)

// newBackend returns a local backend running the prebuilt driver on a fresh
// root. The root is under os.MkdirTemp rather than t.TempDir, whose long paths
// would exceed the drivers' socket path limit. Cleanup destroys every sandbox,
// which kills its driver, and removes the root.
func newBackend(t *testing.T, timeout time.Duration, log *slog.Logger) *Backend {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	binary := filepath.Join(filepath.Dir(file), "..", "..", "prebuilt", "gritz-linux-"+runtime.GOARCH)
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("prebuilt driver: %v; run mise run build", err)
	}
	root, err := os.MkdirTemp("", "gritz-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	lb, err := local.New(local.Options{Root: root, Binary: binary, Environ: os.Environ(), Log: log})
	if err != nil {
		t.Fatal(err)
	}
	b := &Backend{t: t, local: lb, root: root, timeout: timeout}
	t.Cleanup(b.destroy)
	return b
}

// destroy destroys every sandbox under the root, logging its driver's output
// if the test failed, and removes the root.
func (b *Backend) destroy() {
	entries, err := os.ReadDir(b.root)
	if err != nil {
		b.t.Error(err)
	}
	for _, e := range entries {
		if b.t.Failed() {
			out, _ := os.ReadFile(filepath.Join(b.root, e.Name(), "gritz", "driver.out"))
			b.t.Logf("task %s: driver output:\n%s", e.Name(), out)
		}
		sb, err := b.local.Open(backend.Handle{Type: local.HandleType, ID: e.Name()})
		if err != nil {
			b.t.Error(err)
			continue
		}
		if err := sb.Destroy(context.Background()); err != nil {
			b.t.Errorf("task %s: destroy sandbox: %v", e.Name(), err)
		}
	}
	if err := os.RemoveAll(b.root); err != nil {
		b.t.Error(err)
	}
}

// ValidateWorkspace implements backend.Backend.
func (b *Backend) ValidateWorkspace(ws *workspace.Workspace) error {
	return b.local.ValidateWorkspace(ws)
}

// Create implements backend.Backend.
func (b *Backend) Create(ctx context.Context, spec *backend.Spec) (backend.Sandbox, error) {
	return b.local.Create(ctx, spec)
}

// Open implements backend.Backend.
func (b *Backend) Open(h backend.Handle) (backend.Sandbox, error) {
	return b.local.Open(h)
}

// BinaryPath implements backend.Backend.
func (b *Backend) BinaryPath() string {
	return b.local.BinaryPath()
}

// Close implements backend.Backend. The local backend's does nothing, so the
// runner closing it on shutdown leaves it usable by the next runner.
func (b *Backend) Close() error {
	return b.local.Close()
}

// WaitSandbox waits until the task's sandbox has status want. A task that never
// had a sandbox reports gone.
func (b *Backend) WaitSandbox(taskID int64, want backend.Status) {
	t := b.t
	t.Helper()
	sb, err := b.Open(backend.Handle{Type: local.HandleType, ID: strconv.FormatInt(taskID, 10)})
	if err != nil {
		t.Fatal(err)
	}
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		got, err := sb.Status(t.Context())
		if err != nil {
			return poll.Error(fmt.Errorf("task %d: sandbox status: %w", taskID, err))
		}
		if got != want {
			return poll.Continue("task %d: sandbox %s, want %s", taskID, got, want)
		}
		return poll.Success()
	}, poll.WithTimeout(b.timeout), poll.WithDelay(20*time.Millisecond))
}
