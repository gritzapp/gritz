package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/driverclient"
)

// parkTimeout is how long Park lets the driver server shut down gracefully
// after SIGTERM before Docker kills the container.
const parkTimeout = 30 * time.Second

var _ backend.BackendV2 = (*Backend)(nil)

// Create creates the task's container, running the driver server, without
// starting it. A container whose binary copy fails is removed before Create
// returns. The handle is the container's ID.
func (b *Backend) Create(ctx context.Context, spec *backend.Spec) (backend.SandboxV2, error) {
	id, err := b.create(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &sandbox{b: b, id: id}, nil
}

// Open returns the sandbox for a handle this backend produced. It does no I/O.
// A handle's Data is not read: a handle Launch produced, carrying its run's
// version, opens the same container.
func (b *Backend) Open(h backend.Handle) (backend.SandboxV2, error) {
	if foreign(h) {
		return nil, fmt.Errorf("handle type %q is not %q: %w", h.Type, HandleType, backend.ErrForeign)
	}
	if h.ID == "" {
		return nil, errors.New("handle has no container id")
	}
	return &sandbox{b: b, id: h.ID}, nil
}

// sandbox is one task container. It holds nothing but the container's ID, so
// it is safe for concurrent use.
type sandbox struct {
	b  *Backend
	id string
}

func (s *sandbox) Handle() backend.Handle {
	return backend.Handle{Type: HandleType, ID: s.id}
}

// Start starts the container. Starting a running container is a no-op.
func (s *sandbox) Start(ctx context.Context) error {
	err := s.b.docker.ContainerStart(ctx, s.id, container.StartOptions{})
	if cerrdefs.IsNotFound(err) {
		return backend.ErrGone
	}
	if err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}
	return nil
}

// Dial returns a client for the container's driver server whose every
// connection is tunnelled through a Docker exec of `gritz tool proxy` to the
// driver's socket, so the runner needs only Docker API access to reach it.
// Keep-alives are off, so no idle connection holds an exec open. Dial does no
// I/O: a container that is gone or not running fails the client's calls.
func (s *sandbox) Dial(_ context.Context, jwt string) (driverv1connect.DriverServiceClient, error) {
	httpClient := &http.Client{Transport: &bearerTransport{
		token: jwt,
		base: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return s.b.execConn(ctx, s.id)
			},
			DisableKeepAlives: true,
		},
	}}
	return driverclient.New(httpClient, "http://driver", driverclient.Options{IdleTimeout: s.b.idleTimeout}), nil
}

// Status reports a running container as up, a missing one as gone, and any
// other — created, exited or paused — as parked.
func (s *sandbox) Status(ctx context.Context) (backend.Status, error) {
	info, err := s.b.docker.ContainerInspect(ctx, s.id)
	if cerrdefs.IsNotFound(err) {
		return backend.StatusGone, nil
	}
	if err != nil {
		return backend.StatusUnknown, fmt.Errorf("failed to inspect container: %w", err)
	}
	if info.State != nil && info.State.Running && !info.State.Paused {
		return backend.StatusUp, nil
	}
	return backend.StatusParked, nil
}

// Park stops the container, giving the driver server parkTimeout to shut down
// before it is killed. Stopping a stopped container is a no-op.
func (s *sandbox) Park(ctx context.Context) error {
	timeout := int(parkTimeout / time.Second)
	err := s.b.docker.ContainerStop(ctx, s.id, container.StopOptions{Timeout: &timeout})
	if cerrdefs.IsNotFound(err) {
		return backend.ErrGone
	}
	if err != nil {
		return fmt.Errorf("failed to stop container: %w", err)
	}
	return nil
}

// Destroy force-removes the container. A missing container is not an error.
func (s *sandbox) Destroy(ctx context.Context) error {
	err := s.b.docker.ContainerRemove(ctx, s.id, container.RemoveOptions{Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("failed to remove container: %w", err)
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
