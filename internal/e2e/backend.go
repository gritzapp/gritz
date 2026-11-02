package e2e

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/backend/local"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"gotest.tools/v3/poll"
)

// Backend is the harness's local backend, and the backend.Backend the runner
// is given. Its methods and its sandboxes' go through any installed mocks.
type Backend struct {
	t       *testing.T
	local   *local.Backend
	root    string
	timeout time.Duration
	mock    atomic.Pointer[backend.BackendMock]

	mu sync.Mutex
	// sandboxMocks are the installed sandbox mocks, by task id.
	sandboxMocks map[int64]*backend.SandboxMock
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
	b := &Backend{t: t, local: lb, root: root, timeout: timeout, sandboxMocks: map[int64]*backend.SandboxMock{}}
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
	if m := b.mock.Load(); m != nil && m.ValidateWorkspaceFunc != nil {
		return m.ValidateWorkspace(ws)
	}
	return b.local.ValidateWorkspace(ws)
}

// Create implements backend.Backend. The sandbox it returns goes through the
// task's sandbox mock.
func (b *Backend) Create(ctx context.Context, spec *backend.Spec) (backend.Sandbox, error) {
	if m := b.mock.Load(); m != nil && m.CreateFunc != nil {
		return m.Create(ctx, spec)
	}
	sb, err := b.local.Create(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &sandbox{b: b, taskID: spec.TaskID, local: sb}, nil
}

// Open implements backend.Backend. The sandbox it returns goes through the
// task's sandbox mock.
func (b *Backend) Open(h backend.Handle) (backend.Sandbox, error) {
	if m := b.mock.Load(); m != nil && m.OpenFunc != nil {
		return m.Open(h)
	}
	sb, err := b.local.Open(h)
	if err != nil {
		return nil, err
	}
	// The local backend accepts only handles named after a task id.
	taskID, err := strconv.ParseInt(h.ID, 10, 64)
	if err != nil {
		panic("e2e: local backend opened a handle not named after a task id: " + h.ID)
	}
	return &sandbox{b: b, taskID: taskID, local: sb}, nil
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

// Mock installs m, replacing any previous mock: the methods whose <Method>Func
// is set go to m, for every caller, and the others to the local backend.
// Mock(nil) removes it.
func (b *Backend) Mock(m *backend.BackendMock) {
	b.mock.Store(m)
}

// MockSandbox installs m for the task's sandbox, replacing any previous one:
// the sandbox methods whose <Method>Func is set go to m, and the others to the
// local sandbox. It applies to whichever sandbox the task has or gets, created
// or opened. MockSandbox(taskID, nil) removes it.
func (b *Backend) MockSandbox(taskID int64, m *backend.SandboxMock) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if m == nil {
		delete(b.sandboxMocks, taskID)
		return
	}
	b.sandboxMocks[taskID] = m
}

// unmock removes every installed mock.
func (b *Backend) unmock() {
	b.Mock(nil)
	b.mu.Lock()
	defer b.mu.Unlock()
	clear(b.sandboxMocks)
}

// sandboxMock returns the task's installed sandbox mock, or nil.
func (b *Backend) sandboxMock(taskID int64) *backend.SandboxMock {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sandboxMocks[taskID]
}

// Dir returns the task's sandbox directory.
func (b *Backend) Dir(taskID int64) string {
	return filepath.Join(b.root, strconv.FormatInt(taskID, 10))
}

// ReadFile returns the content of name in the task's sandbox directory, as the
// workspace's setup commands wrote it.
func (b *Backend) ReadFile(taskID int64, name string) string {
	b.t.Helper()
	data, err := os.ReadFile(filepath.Join(b.Dir(taskID), name))
	if err != nil {
		b.t.Fatal(err)
	}
	return string(data)
}

// KillDriver kills the task's driver with SIGKILL, leaving its sandbox, and
// returns once it has exited: a real lost run.
func (b *Backend) KillDriver(taskID int64) {
	b.t.Helper()
	if err := b.local.Kill(backend.Handle{Type: local.HandleType, ID: strconv.FormatInt(taskID, 10)}); err != nil {
		b.t.Fatalf("task %d: kill driver: %v", taskID, err)
	}
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

// sandbox is a local sandbox whose methods go through its task's sandbox mock.
type sandbox struct {
	b      *Backend
	taskID int64
	local  backend.Sandbox
}

func (s *sandbox) Handle() backend.Handle {
	if m := s.b.sandboxMock(s.taskID); m != nil && m.HandleFunc != nil {
		return m.Handle()
	}
	return s.local.Handle()
}

func (s *sandbox) Boot(ctx context.Context) error {
	if m := s.b.sandboxMock(s.taskID); m != nil && m.BootFunc != nil {
		return m.Boot(ctx)
	}
	return s.local.Boot(ctx)
}

func (s *sandbox) Dial(ctx context.Context, jwt string) (driverv1connect.DriverServiceClient, error) {
	if m := s.b.sandboxMock(s.taskID); m != nil && m.DialFunc != nil {
		return m.Dial(ctx, jwt)
	}
	return s.local.Dial(ctx, jwt)
}

func (s *sandbox) Status(ctx context.Context) (backend.Status, error) {
	if m := s.b.sandboxMock(s.taskID); m != nil && m.StatusFunc != nil {
		return m.Status(ctx)
	}
	return s.local.Status(ctx)
}

func (s *sandbox) Suspend(ctx context.Context) error {
	if m := s.b.sandboxMock(s.taskID); m != nil && m.SuspendFunc != nil {
		return m.Suspend(ctx)
	}
	return s.local.Suspend(ctx)
}

func (s *sandbox) Destroy(ctx context.Context) error {
	if m := s.b.sandboxMock(s.taskID); m != nil && m.DestroyFunc != nil {
		return m.Destroy(ctx)
	}
	return s.local.Destroy(ctx)
}
