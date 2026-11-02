package local

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/driverclient"
)

// sandbox is one task's sandbox directory. Its operations are serialised by
// its backend's per-sandbox lock, so it is safe for concurrent use.
type sandbox struct {
	b   *Backend
	id  string
	dir string
}

func (s *sandbox) Handle() backend.Handle {
	return backend.Handle{Type: HandleType, ID: s.id}
}

func (s *sandbox) lock() (unlock func()) {
	l := s.b.lock(s.id)
	l.Lock()
	return l.Unlock
}

// driver returns the pid in the sandbox's pid file, and whether it is a live
// driver for this sandbox.
func (s *sandbox) driver() (int, bool) {
	data, err := os.ReadFile(filepath.Join(s.dir, pidPath))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	return pid, alive(pid, s.dir)
}

// Boot starts the sandbox's driver. Booting a sandbox whose driver is up is a
// no-op. It returns once the process has started: driver readiness is the
// runner's job.
func (s *sandbox) Boot(context.Context) error {
	unlock := s.lock()
	defer unlock()
	ok, err := exists(s.dir)
	if err != nil {
		return fmt.Errorf("failed to stat sandbox: %w", err)
	}
	if !ok {
		return backend.ErrGone
	}
	if _, up := s.driver(); up {
		return nil
	}
	return s.b.startDriver(s.dir)
}

// Dial returns a client for the sandbox's driver over its unix socket. It
// speaks unencrypted HTTP/2 with prior knowledge, which bidirectional streams
// need. Dial does no I/O: a sandbox whose driver is down fails the client's
// calls.
func (s *sandbox) Dial(_ context.Context, jwt string) (driverv1connect.DriverServiceClient, error) {
	sock := filepath.Join(s.dir, socketPath)
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	httpClient := &http.Client{Transport: &bearerTransport{
		token: jwt,
		base: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
			DisableKeepAlives: true,
			Protocols:         protocols,
		},
	}}
	return driverclient.New(httpClient, "http://driver", driverclient.Options{}), nil
}

// Status reports a missing directory as gone, a sandbox whose pid file names a
// live driver as up, and any other as suspended.
func (s *sandbox) Status(context.Context) (backend.Status, error) {
	ok, err := exists(s.dir)
	if err != nil {
		return backend.StatusUnknown, fmt.Errorf("failed to stat sandbox: %w", err)
	}
	if !ok {
		return backend.StatusGone, nil
	}
	if _, up := s.driver(); up {
		return backend.StatusUp, nil
	}
	return backend.StatusSuspended, nil
}

// Suspend sends SIGTERM to the driver, which stops its run and reports it, as
// `docker stop` does, and waits for it to exit. A driver still running after
// suspendTimeout has its process group killed. The directory stays.
// Suspending a sandbox whose driver is down is a no-op.
func (s *sandbox) Suspend(ctx context.Context) error {
	unlock := s.lock()
	defer unlock()
	ok, err := exists(s.dir)
	if err != nil {
		return fmt.Errorf("failed to stat sandbox: %w", err)
	}
	if !ok {
		return backend.ErrGone
	}
	pid, up := s.driver()
	if !up {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("failed to stop driver: %w", err)
	}
	termCtx, cancel := context.WithTimeout(ctx, suspendTimeout)
	defer cancel()
	if err := s.waitExit(termCtx, pid); err != nil && ctx.Err() != nil {
		return err
	}
	// Kill whatever the driver left in its group, or the driver itself if it
	// outlived suspendTimeout.
	return s.kill(ctx, pid)
}

// Destroy kills the driver's process group and removes the directory. A
// missing directory is not an error.
func (s *sandbox) Destroy(ctx context.Context) error {
	unlock := s.lock()
	defer unlock()
	if pid, up := s.driver(); up {
		if err := s.kill(ctx, pid); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(s.dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("failed to remove sandbox: %w", err)
	}
	return nil
}

// kill sends SIGKILL to the process group of the driver pid and waits for the
// driver to exit.
func (s *sandbox) kill(ctx context.Context, pid int) error {
	if err := signalGroup(pid, syscall.SIGKILL); err != nil {
		return err
	}
	return s.waitExit(ctx, pid)
}

// waitExit waits until pid is no longer a live driver for the sandbox.
func (s *sandbox) waitExit(ctx context.Context, pid int) error {
	t := time.NewTicker(exitPoll)
	defer t.Stop()
	for alive(pid, s.dir) {
		select {
		case <-ctx.Done():
			return fmt.Errorf("driver %d did not exit: %w", pid, context.Cause(ctx))
		case <-t.C:
		}
	}
	return nil
}

// bearerTransport adds token to every request as a bearer token, unless it is
// empty.
type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.token == "" {
		return t.base.RoundTrip(req)
	}
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
}
