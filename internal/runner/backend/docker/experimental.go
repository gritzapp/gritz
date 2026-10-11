package docker

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/workspace"
)

// ExperimentalHandleType is the backend.Handle.Type the experimental Docker
// backend stamps on the handles it produces.
const ExperimentalHandleType = "experimental-docker"

// DriverPort is the port `gritz driver --serve` listens on inside a container
// with its own network namespace.
const DriverPort = 8080

// driverAddrLabel records the driver server's address on a container whose
// address is not derived from its IP: one sharing the host's network
// namespace, where :8080 would collide with the host and other containers.
const driverAddrLabel = "gritz.driver.addr"

const (
	// driverReadyTimeout bounds how long Launch waits for a started
	// container's driver server to answer Status.
	driverReadyTimeout = 30 * time.Second
	// driverRetryInterval is the pause between attempts to reach the driver.
	driverRetryInterval = 250 * time.Millisecond
)

// Experimental runs task sandboxes as containers whose main process is the
// driver server (`gritz driver --serve`), and starts each run over the
// driver.v1 protocol. This is phase A of proposals/draft/driver-server.md: it
// implements the same backend.Backend as Backend, the driver still reports to
// the gritz server itself, and the container is stopped after every run.
//
// A handle's Data carries the version of the run Launch started, which Wait,
// Probe and Signal address the driver with.
type Experimental struct {
	*Backend
	http *http.Client
}

var _ backend.Backend = (*Experimental)(nil)

// NewExperimental returns an experimental Docker backend.
func NewExperimental(opts Options) (*Experimental, error) {
	b, err := New(opts)
	if err != nil {
		return nil, err
	}
	return &Experimental{Backend: b, http: &http.Client{}}, nil
}

// handleData is the backend-defined part of an experimental handle.
type handleData struct {
	Version int64 `json:"version"`
}

func decodeHandle(h backend.Handle) (handleData, error) {
	var d handleData
	if len(h.Data) == 0 {
		return d, fmt.Errorf("handle %s has no run version", h.ID)
	}
	if err := json.Unmarshal(h.Data, &d); err != nil {
		return d, fmt.Errorf("failed to decode handle data: %w", err)
	}
	return d, nil
}

// Launch ensures the task's container exists and is running, waits for its
// driver server to answer, and starts the spec's run on it. The reuse
// semantics are Backend.Launch's: a reuse handle adopts the exact recorded
// container or returns backend.ErrGone. Starting a container that is already
// running is a no-op, so a container whose previous run has finished but which
// has not been stopped yet is reused as is.
func (e *Experimental) Launch(ctx context.Context, spec *backend.Spec, reuse *backend.Handle) (backend.Handle, error) {
	containerID, err := e.ensure(ctx, spec, reuse)
	if err != nil {
		return backend.Handle{}, err
	}
	if err := e.docker.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return backend.Handle{}, fmt.Errorf("failed to start container: %w", err)
	}
	client, err := e.dial(ctx, containerID)
	if err != nil {
		return backend.Handle{}, err
	}
	if err := e.waitReady(ctx, client); err != nil {
		return backend.Handle{}, err
	}
	runSpec, err := newRunSpec(spec)
	if err != nil {
		return backend.Handle{}, err
	}
	if err := e.start(ctx, client, spec.Version, runSpec); err != nil {
		return backend.Handle{}, err
	}
	data, err := json.Marshal(handleData{Version: spec.Version})
	if err != nil {
		return backend.Handle{}, err
	}
	return backend.Handle{Type: ExperimentalHandleType, ID: containerID, Data: data}, nil
}

// ensure resolves the container to start, like Backend.ensure, but creates it
// running the driver server instead of the one-shot driver. The container gets
// only the workspace's environment: the run's env, secrets and token arrive in
// the RunSpec, so a container reused for a later run never holds stale ones.
func (e *Experimental) ensure(ctx context.Context, spec *backend.Spec, reuse *backend.Handle) (string, error) {
	if reuse != nil && reuse.ID != "" {
		id, ok, err := e.inspectID(ctx, reuse.ID)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", backend.ErrGone
		}
		return e.adopt(ctx, spec, id)
	}
	wc := &spec.Workspace.Container
	cmd := []string{backend.BinaryPath, "driver", "--serve"}
	var labels map[string]string
	if container.NetworkMode(wc.NetworkMode).IsHost() {
		addr, err := freeLocalAddr()
		if err != nil {
			return "", err
		}
		cmd = append(cmd, "--addr", addr)
		labels = map[string]string{driverAddrLabel: addr}
	}
	return e.create(ctx, spec, cmd, wc.Environ(), labels)
}

// freeLocalAddr returns a loopback address with a port that is free now.
func freeLocalAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("failed to find a free port: %w", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		return "", err
	}
	return addr, nil
}

// errNotRunning means the container exists but is not running.
var errNotRunning = errors.New("container is not running")

// dial returns a client for the driver server of a running container. A
// missing container is reported as cerrdefs.ErrNotFound, a stopped one as
// errNotRunning.
func (e *Experimental) dial(ctx context.Context, containerID string) (driverv1connect.DriverServiceClient, error) {
	info, err := e.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container: %w", err)
	}
	if info.State == nil || !info.State.Running {
		return nil, errNotRunning
	}
	addr, err := driverAddr(info)
	if err != nil {
		return nil, err
	}
	return driverv1connect.NewDriverServiceClient(e.http, "http://"+addr), nil
}

// driverAddr returns the address of a running container's driver server: the
// label recorded at create, or else the container's IP on DriverPort. With
// several networks the first by name is used.
func driverAddr(info container.InspectResponse) (string, error) {
	if info.Config != nil {
		if addr := info.Config.Labels[driverAddrLabel]; addr != "" {
			return addr, nil
		}
	}
	if info.NetworkSettings != nil {
		for _, name := range slices.Sorted(maps.Keys(info.NetworkSettings.Networks)) {
			if ep := info.NetworkSettings.Networks[name]; ep != nil && ep.IPAddress != "" {
				return net.JoinHostPort(ep.IPAddress, strconv.Itoa(DriverPort)), nil
			}
		}
	}
	return "", fmt.Errorf("container %s has no IP address to reach its driver on", info.ID)
}

// waitReady polls Status until the driver server answers or
// driverReadyTimeout elapses.
func (e *Experimental) waitReady(ctx context.Context, client driverv1connect.DriverServiceClient) error {
	ctx, cancel := context.WithTimeout(ctx, driverReadyTimeout)
	defer cancel()
	for {
		_, err := client.Status(ctx, &driverv1.StatusRequest{})
		if err == nil {
			return nil
		}
		if err := sleep(ctx, driverRetryInterval); err != nil {
			return fmt.Errorf("driver server did not become ready: %w", err)
		}
	}
}

// start opens a Run stream for version with spec and returns once the driver
// has accepted it, closing the stream: the run keeps going without it. A Run
// that is rejected because another stream is open is retried.
func (e *Experimental) start(ctx context.Context, client driverv1connect.DriverServiceClient, version int64, spec *driverv1.RunSpec) error {
	for {
		err := accept(ctx, client, &driverv1.RunRequest{Version: version, Spec: spec})
		if err == nil {
			return nil
		}
		if connect.CodeOf(err) != connect.CodeUnavailable {
			return fmt.Errorf("failed to start run %d: %w", version, err)
		}
		if err := sleep(ctx, driverRetryInterval); err != nil {
			return fmt.Errorf("failed to start run %d: %w", version, err)
		}
	}
}

// accept opens a Run stream and closes it after its Accepted.
func accept(ctx context.Context, client driverv1connect.DriverServiceClient, req *driverv1.RunRequest) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := client.Run(ctx, req)
	if err != nil {
		return err
	}
	defer stream.Close()
	if !stream.Receive() {
		return cmp.Or(stream.Err(), errors.New("run stream ended before it was accepted"))
	}
	if stream.Msg().GetAccepted() == nil {
		return fmt.Errorf("unexpected first run response: %v", stream.Msg())
	}
	return nil
}

// Wait attaches to the handle's run and blocks until its Finished, then stops
// the container, so between runs it is stopped exactly as Backend's is. It
// returns 0 if the driver reported the run's outcome, else ExitLost. A dropped
// stream is reattached while the container is running; a container that is gone
// or stopped before Finished arrived is reported as lost.
func (e *Experimental) Wait(ctx context.Context, h backend.Handle) (backend.ExitCode, error) {
	d, err := decodeHandle(h)
	if err != nil {
		e.log.Error("wait: invalid handle", "container", h.ID, "err", err)
		return backend.ExitLost, nil
	}
	for {
		finished, err := e.attach(ctx, h.ID, d.Version)
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		switch {
		case finished != nil:
			e.stopAfter(ctx, h.ID, d.Version)
			if !finished.GetReported() {
				return backend.ExitLost, nil
			}
			return 0, nil
		case cerrdefs.IsNotFound(err) || errors.Is(err, errNotRunning):
			e.log.Error("container exited before its run finished", "container", h.ID, "version", d.Version, "err", err)
			return backend.ExitLost, nil
		case isTerminal(err):
			// The driver has no run with this version (or has moved past it),
			// so its Finished will never come.
			e.log.Error("driver has no such run", "container", h.ID, "version", d.Version, "err", err)
			return backend.ExitLost, nil
		}
		e.log.Warn("run stream dropped, reattaching", "container", h.ID, "version", d.Version, "err", err)
		if err := sleep(ctx, driverRetryInterval); err != nil {
			return 0, err
		}
	}
}

// attach opens a Run stream for version without a spec and reads it until the
// run's Finished, which it returns. Otherwise it returns why the stream ended.
func (e *Experimental) attach(ctx context.Context, containerID string, version int64) (*driverv1.Finished, error) {
	client, err := e.dial(ctx, containerID)
	if err != nil {
		return nil, err
	}
	stream, err := client.Run(ctx, &driverv1.RunRequest{Version: version})
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	for stream.Receive() {
		if finished := stream.Msg().GetFinished(); finished != nil {
			return finished, nil
		}
	}
	return nil, cmp.Or(stream.Err(), errors.New("run stream ended before Finished"))
}

// isTerminal reports whether a Run error means the run can never be attached
// to: NotFound (no such run), FailedPrecondition (stale version) or Aborted
// (another version is running).
func isTerminal(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeNotFound, connect.CodeFailedPrecondition, connect.CodeAborted:
		return true
	}
	return false
}

// stopAfter stops the container once the run with version has finished,
// unless a later run has started on it in the meantime. Failures are logged:
// the run's outcome is already known.
func (e *Experimental) stopAfter(ctx context.Context, containerID string, version int64) {
	if client, err := e.dial(ctx, containerID); err == nil {
		resp, err := client.Status(ctx, &driverv1.StatusRequest{})
		if err == nil && resp.GetRun().GetVersion() != version {
			return
		}
	}
	err := e.docker.ContainerStop(ctx, containerID, container.StopOptions{})
	if err != nil && !cerrdefs.IsNotFound(err) {
		e.log.Error("failed to stop container after its run", "container", containerID, "err", err)
	}
}

// Probe reports the liveness of the handle's run: a missing container is gone,
// a stopped one exited, and a running one is running only while the driver's
// run record shows the handle's version running.
func (e *Experimental) Probe(ctx context.Context, h backend.Handle) (backend.State, error) {
	d, err := decodeHandle(h)
	if err != nil {
		return backend.StateUnknown, err
	}
	client, err := e.dial(ctx, h.ID)
	switch {
	case cerrdefs.IsNotFound(err):
		return backend.StateGone, nil
	case errors.Is(err, errNotRunning):
		return backend.StateExited, nil
	case err != nil:
		return backend.StateUnknown, err
	}
	resp, err := client.Status(ctx, &driverv1.StatusRequest{})
	if err != nil {
		return backend.StateUnknown, fmt.Errorf("failed to get driver status: %w", err)
	}
	run := resp.GetRun()
	if run.GetVersion() == d.Version && run.GetState() == driverv1.RunRecord_STATE_RUNNING {
		return backend.StateRunning, nil
	}
	return backend.StateExited, nil
}

// Signal asks the driver to stop the handle's run, reporting whether it was
// running. The driver reports stopped and sends Finished, after which Wait
// stops the container.
func (e *Experimental) Signal(ctx context.Context, h backend.Handle) (bool, error) {
	d, err := decodeHandle(h)
	if err != nil {
		return false, err
	}
	e.log.Info("stopping run", "container", h.ID, "version", d.Version)
	client, err := e.dial(ctx, h.ID)
	if cerrdefs.IsNotFound(err) || errors.Is(err, errNotRunning) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	resp, err := client.Stop(ctx, &driverv1.StopRequest{Version: d.Version})
	if err != nil {
		return false, fmt.Errorf("failed to stop run: %w", err)
	}
	return resp.GetStopped(), nil
}

// ValidateWorkspace reports whether the workspace's container config is usable
// by the experimental backend: Backend's checks, plus a network the runner can
// reach the driver server on.
func (e *Experimental) ValidateWorkspace(ws *workspace.Workspace) error {
	if err := e.Backend.ValidateWorkspace(ws); err != nil {
		return err
	}
	mode := container.NetworkMode(ws.Container.NetworkMode)
	if mode.IsNone() || mode.IsContainer() {
		return fmt.Errorf("network_mode %q is not supported: the runner must reach the driver server", ws.Container.NetworkMode)
	}
	return nil
}

// newRunSpec builds the driver.v1 RunSpec for spec. The runner hands every
// backend the one-shot driver's environment (GRITZ_SERVER, GRITZ_TOKEN, the
// workspace secrets and GRITZ_SECRETS naming them); the server URL, token and
// secrets are lifted out of it into their own fields, and the rest is the
// run's env.
func newRunSpec(spec *backend.Spec) (*driverv1.RunSpec, error) {
	env := map[string]string{}
	for _, kv := range spec.Env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("invalid environment entry %q", kv)
		}
		env[k] = v
	}
	rs := &driverv1.RunSpec{
		TaskId:    spec.TaskID,
		ServerUrl: env["GRITZ_SERVER"],
		Token:     env["GRITZ_TOKEN"],
		Env:       env,
	}
	if names := env["GRITZ_SECRETS"]; names != "" {
		rs.Secrets = map[string]string{}
		for name := range strings.SplitSeq(names, ",") {
			if v, ok := env[name]; ok {
				rs.Secrets[name] = v
				delete(env, name)
			}
		}
	}
	delete(env, "GRITZ_SECRETS")
	return rs, nil
}

// sleep waits for d or until ctx is done, returning ctx's error in that case.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
