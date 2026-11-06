// Package local implements the runner backend on the runner's own host, with
// no isolation: each sandbox is a directory Root/<task id>, and its driver an
// ordinary child process (`gritz driver --prefix DIR`) running there. It is for
// tests and development, never for running untrusted agents.
//
// Drivers outlive the Backend that started them, as containers outlive a Docker
// client: each runs in its own process group and is found again through the pid
// file it leaves in its sandbox directory.
package local

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/workspace"
)

// HandleType is the backend.Handle.Type of local sandboxes.
const HandleType = "local"

// Paths inside a sandbox directory.
const (
	stateDir   = "gritz"
	socketPath = "gritz/driver.sock"
	pidPath    = "gritz/driver.pid"
	outPath    = "gritz/driver.out"
	homeDir    = "home"
)

// maxSocketPath is the longest unix socket path the kernel accepts: sun_path
// is 108 bytes, including the terminating NUL.
const maxSocketPath = 107

// suspendTimeout is how long Suspend lets the driver shut down gracefully after
// SIGTERM before it kills the driver's process group.
const suspendTimeout = 30 * time.Second

// exitPoll is how often a signalled driver is checked for having exited.
const exitPoll = 20 * time.Millisecond

// Options configures a Backend.
type Options struct {
	// Root holds one directory per sandbox, named after its task id.
	Root string
	// Binary is the gritz binary each sandbox runs as its driver.
	Binary string
	// Environ is the base environment of every driver; HOME is set per sandbox.
	Environ []string
	Log     *slog.Logger
}

// Backend runs each sandbox as a directory and a driver process on this host,
// with no isolation.
type Backend struct {
	root    string
	binary  string
	environ []string
	log     *slog.Logger

	mu sync.Mutex
	// locks serialises the operations on each sandbox, by handle ID, so that
	// concurrent Boots start one driver.
	locks map[string]*sync.Mutex
}

var _ backend.Backend = (*Backend)(nil)

// New returns a Backend over opts.Root, rejecting a root too long for the
// drivers' socket paths. It creates Root if it is missing.
func New(opts Options) (*Backend, error) {
	if opts.Root == "" {
		panic("local: New called without Options.Root")
	}
	if opts.Binary == "" {
		panic("local: New called without Options.Binary")
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("local backend: %w", err)
	}
	binary, err := filepath.Abs(opts.Binary)
	if err != nil {
		return nil, fmt.Errorf("local backend: %w", err)
	}
	// The longest sandbox directory is named after the largest task id.
	longest := filepath.Join(root, strconv.FormatInt(1<<63-1, 10), socketPath)
	if len(longest) > maxSocketPath {
		return nil, fmt.Errorf("local backend: root %q is too long: socket paths under it reach %d bytes, over %d", root, len(longest), maxSocketPath)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("local backend: %w", err)
	}
	return &Backend{
		root:    root,
		binary:  binary,
		environ: opts.Environ,
		log:     cmp.Or(opts.Log, slog.Default()),
		locks:   map[string]*sync.Mutex{},
	}, nil
}

// ValidateWorkspace accepts every workspace: the local backend reads no runtime
// section.
func (b *Backend) ValidateWorkspace(*workspace.Workspace) error {
	return nil
}

// BinaryPath returns opts.Binary, which the injected MCP server runs.
func (b *Backend) BinaryPath() string {
	return b.binary
}

// Close does nothing; drivers outlive the backend.
func (b *Backend) Close() error {
	return nil
}

// Create makes the task's sandbox directory, with the driver's state directory
// and HOME in it. It fails if the directory exists, as a Docker name clash
// does. Nothing is copied: the driver binary is run in place.
func (b *Backend) Create(_ context.Context, spec *backend.Spec) (backend.Sandbox, error) {
	s := b.sandbox(strconv.FormatInt(spec.TaskID, 10))
	b.log.Info("creating sandbox", "task", spec.TaskID, "dir", s.dir)
	if err := os.Mkdir(s.dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create sandbox: %w", err)
	}
	for _, dir := range []string{stateDir, homeDir} {
		// Like Docker's /gritz, the state directory is open to any agent user.
		if err := os.Mkdir(filepath.Join(s.dir, dir), 0o777); err != nil {
			_ = os.RemoveAll(s.dir)
			return nil, fmt.Errorf("failed to create sandbox: %w", err)
		}
	}
	return s, nil
}

// Open returns the sandbox for a local handle, without I/O.
func (b *Backend) Open(h backend.Handle) (backend.Sandbox, error) {
	if h.Type != HandleType {
		return nil, fmt.Errorf("handle type %q is not %q", h.Type, HandleType)
	}
	if _, err := strconv.ParseInt(h.ID, 10, 64); err != nil {
		return nil, fmt.Errorf("handle id %q is not a task id", h.ID)
	}
	return b.sandbox(h.ID), nil
}

// Kill sends SIGKILL to the sandbox's driver process group and leaves its
// directory, so the sandbox reads as suspended. It returns once the driver has
// exited. A sandbox with no live driver is left as it is.
func (b *Backend) Kill(h backend.Handle) error {
	sb, err := b.Open(h)
	if err != nil {
		return err
	}
	s := sb.(*sandbox)
	unlock := s.lock()
	defer unlock()
	pid, ok := s.pid()
	if !ok {
		return nil
	}
	return s.kill(context.Background(), pid)
}

func (b *Backend) sandbox(id string) *sandbox {
	return &sandbox{b: b, id: id, dir: filepath.Join(b.root, id)}
}

// lock returns the mutex of the sandbox with handle ID id.
func (b *Backend) lock(id string) *sync.Mutex {
	b.mu.Lock()
	defer b.mu.Unlock()
	l, ok := b.locks[id]
	if !ok {
		l = new(sync.Mutex)
		b.locks[id] = l
	}
	return l
}

// alive reports whether pid is a live driver for the sandbox directory dir: a
// process whose command line carries `--prefix dir`. An exited process that
// isn't reaped yet has an empty command line, so a zombie never looks alive,
// and neither does an unrelated process that reused the pid.
func alive(pid int, dir string) bool {
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	args := bytes.Split(cmdline, []byte{0})
	for i := 0; i+1 < len(args); i++ {
		if string(args[i]) == "--prefix" && string(args[i+1]) == dir {
			return true
		}
	}
	return false
}

// exists reports whether dir exists.
func exists(dir string) (bool, error) {
	_, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// signalGroup sends sig to the process group led by pid. A group with no
// processes left is not an error.
func signalGroup(pid int, sig syscall.Signal) error {
	err := syscall.Kill(-pid, sig)
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("failed to signal driver: %w", err)
	}
	return nil
}

// startDriver starts the driver for the sandbox directory dir and writes its
// pid file, and returns once the driver reads as up. A goroutine reaps it when
// it exits. The pid file stays: it no longer names a live driver, and removing
// it would race with a Boot that has since written its successor's.
func (b *Backend) startDriver(ctx context.Context, dir string) error {
	out, err := os.OpenFile(filepath.Join(dir, outPath), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open driver output: %w", err)
	}
	defer func() { _ = out.Close() }()
	cmd := exec.Command(b.binary, "driver",
		"--addr", "unix://"+filepath.Join(dir, socketPath),
		"--prefix", dir)
	cmd.Dir = dir
	cmd.Env = append(append([]string(nil), b.environ...), "HOME="+filepath.Join(dir, homeDir))
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start driver: %w", err)
	}
	pid := cmd.Process.Pid
	pidFile := filepath.Join(dir, pidPath)
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		_ = signalGroup(pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return fmt.Errorf("failed to write driver pid file: %w", err)
	}
	b.log.Info("started driver", "dir", dir, "pid", pid)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		err := cmd.Wait()
		b.log.Info("driver exited", "dir", dir, "pid", pid, "err", err)
	}()
	// Start returns once the exec has closed the parent's pipe, which is
	// before the kernel sets the new command line that alive reads.
	t := time.NewTicker(time.Millisecond)
	defer t.Stop()
	for !alive(pid, dir) {
		select {
		case <-ctx.Done():
			return fmt.Errorf("driver %d did not start: %w", pid, context.Cause(ctx))
		case <-exited:
			return fmt.Errorf("driver %d exited on start; see %s", pid, filepath.Join(dir, outPath))
		case <-t.C:
		}
	}
	return nil
}
