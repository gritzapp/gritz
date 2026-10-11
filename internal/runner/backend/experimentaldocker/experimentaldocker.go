// Package experimentaldocker implements the runner backend on the local Docker
// daemon with the driver server (`gritz driver --serve`) as each container's
// main process, starting each run over the driver.v1 protocol. This is phase A
// of proposals/draft/driver-server.md: it implements backend.Backend, the
// driver still reports to the gritz server itself, and the container is
// stopped after every run.
//
// Containers are named gritz-{task-id} and labelled with the owning runner.
package experimentaldocker

import (
	"archive/tar"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/driverclient"
	"github.com/gritzapp/gritz/internal/runner/prebuilt"
	"github.com/gritzapp/gritz/internal/runner/runrecord"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/dockerx"
)

// HandleType is the backend.Handle.Type this backend stamps on the handles it
// produces.
const HandleType = "experimental-docker"

// DriverPort is the port `gritz driver --serve` listens on inside a container.
// Every container has its own network namespace, so it never collides.
const DriverPort = 8080

const (
	// driverReadyTimeout bounds how long Launch waits for a started
	// container's driver server to answer Status.
	driverReadyTimeout = 30 * time.Second
	// driverRetryInterval is the pause between attempts to reach the driver,
	// and the first pause between attempts to reattach to a run.
	driverRetryInterval = 250 * time.Millisecond
	// reattachMaxInterval caps the backoff between attempts to reattach.
	reattachMaxInterval = 10 * time.Second
	// driverLostTimeout is how long Wait keeps reattaching without a good
	// message from the driver before it stops the container and reports the
	// run lost.
	driverLostTimeout = 2 * time.Minute
)

// Backend runs task sandboxes as containers on the local Docker daemon whose
// main process is the driver server.
//
// A handle's Data carries the version of the run Launch started, which Wait
// and Signal address the driver with.
type Backend struct {
	docker   *client.Client
	runnerID string
	log      *slog.Logger
	// readyTimeout bounds how long Launch waits for the driver server; tests
	// shorten it.
	readyTimeout time.Duration
	// idleTimeout is how long a Run stream may go without a message, and
	// lostTimeout how long Wait reattaches without a good message; tests
	// shorten them.
	idleTimeout time.Duration
	lostTimeout time.Duration
}

var _ backend.Backend = (*Backend)(nil)

type Options struct {
	RunnerID string
	Log      *slog.Logger
}

func New(opts Options) (*Backend, error) {
	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}
	return &Backend{
		docker:       docker,
		runnerID:     opts.RunnerID,
		log:          cmp.Or(opts.Log, slog.Default()),
		readyTimeout: driverReadyTimeout,
		idleTimeout:  driverclient.DefaultIdleTimeout,
		lostTimeout:  driverLostTimeout,
	}, nil
}

func (b *Backend) Close() error {
	return b.docker.Close()
}

// ValidateWorkspace reports whether the workspace's container config is usable:
// it needs an image, and a network namespace of its own for the driver server
// to listen on DriverPort in. Host and container: modes share a namespace in
// which several drivers would collide; none is rejected as well.
func (b *Backend) ValidateWorkspace(ws *workspace.Workspace) error {
	if err := ws.Container.Validate(); err != nil {
		return err
	}
	mode := container.NetworkMode(ws.Container.NetworkMode)
	if mode.IsNone() || mode.IsContainer() || mode.IsHost() {
		return fmt.Errorf("network_mode %q is not supported by the experimental docker backend: the driver server needs a network namespace of its own", ws.Container.NetworkMode)
	}
	return nil
}

// handleData is the backend-defined part of an experimental handle.
type handleData struct {
	Version int64 `json:"version"`
}

// foreign reports whether h was produced by another backend. This backend
// replaces docker rather than running beside it, so it treats such a handle's
// sandbox as gone: Launch rejects it with backend.ErrGone, Probe answers
// StateGone, Wait reports the run lost, and Signal and Destroy leave it alone.
func foreign(h backend.Handle) bool {
	return h.Type != HandleType
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
// driver server to answer, and starts the spec's run on it. With a reuse
// handle it adopts the exact recorded container, or returns backend.ErrGone if
// that container is gone; it never creates a fresh one. The runner only
// launches on a container Probe does not report running, so between runs the
// container has been stopped by Wait and is started again here.
//
// A failed Launch cleans up after itself, since the runner gets no handle to do
// it with: a container it created is removed, and an adopted one is stopped —
// not removed, as the task's record still points at it — so Probe does not
// report it running with no run on it.
func (b *Backend) Launch(ctx context.Context, spec *backend.Spec, reuse *backend.Handle) (backend.Handle, error) {
	fresh := reuse == nil || reuse.ID == ""
	containerID, err := b.ensure(ctx, spec, reuse)
	if err != nil {
		return backend.Handle{}, err
	}
	h, err := b.launch(ctx, spec, containerID)
	if err != nil {
		// ctx being cancelled is one of the failures to clean up after.
		ctx := context.WithoutCancel(ctx)
		if fresh {
			b.discard(ctx, containerID)
		} else {
			b.stopFailed(ctx, containerID)
		}
		return backend.Handle{}, err
	}
	return h, nil
}

// launch starts the container, waits for its driver server and starts the
// spec's run on it.
func (b *Backend) launch(ctx context.Context, spec *backend.Spec, containerID string) (backend.Handle, error) {
	if err := b.docker.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return backend.Handle{}, fmt.Errorf("failed to start container: %w", err)
	}
	client, err := b.dial(ctx, containerID)
	if err != nil {
		return backend.Handle{}, err
	}
	if err := b.waitReady(ctx, client); err != nil {
		return backend.Handle{}, err
	}
	runSpec, err := newRunSpec(spec)
	if err != nil {
		return backend.Handle{}, err
	}
	if err := b.start(ctx, client, spec.Version, runSpec); err != nil {
		return backend.Handle{}, err
	}
	data, err := json.Marshal(handleData{Version: spec.Version})
	if err != nil {
		return backend.Handle{}, err
	}
	return backend.Handle{Type: HandleType, ID: containerID, Data: data}, nil
}

// discard force-removes a container this Launch created and failed to start a
// run on: the runner has no record of it, so nothing else would. Failures are
// logged: Launch returns the error that caused them.
func (b *Backend) discard(ctx context.Context, containerID string) {
	b.log.Info("removing container after failed launch", "container", containerID)
	err := b.docker.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		b.log.Error("failed to remove container after failed launch", "container", containerID, "err", err)
	}
}

// stopFailed stops an adopted container Launch failed to start a run on, so
// the next start launches on it again rather than finding it running.
// Failures are logged: Launch returns the error that caused them.
func (b *Backend) stopFailed(ctx context.Context, containerID string) {
	b.log.Info("stopping container after failed launch", "container", containerID)
	err := b.docker.ContainerStop(ctx, containerID, container.StopOptions{})
	if err != nil && !cerrdefs.IsNotFound(err) {
		b.log.Error("failed to stop container after failed launch", "container", containerID, "err", err)
	}
}

// ensure resolves the container to start, enforcing the one-sandbox-per-task
// invariant: with a reuse handle it adopts the exact recorded container, or
// returns backend.ErrGone if that container has vanished — it never falls
// through to create. A fresh container is created only when reuse is nil (a
// task's first start).
func (b *Backend) ensure(ctx context.Context, spec *backend.Spec, reuse *backend.Handle) (string, error) {
	if reuse != nil && reuse.ID != "" {
		if foreign(*reuse) {
			return "", backend.ErrGone
		}
		id, ok, err := b.inspectID(ctx, reuse.ID)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", backend.ErrGone
		}
		return b.adopt(ctx, spec, id)
	}
	return b.create(ctx, spec)
}

// inspectID returns the container's id and whether it exists. A missing
// container is ("", false, nil); any other failure is an error.
func (b *Backend) inspectID(ctx context.Context, ref string) (string, bool, error) {
	info, err := b.docker.ContainerInspect(ctx, ref)
	if cerrdefs.IsNotFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("failed to inspect container: %w", err)
	}
	return info.ID, true, nil
}

// adopt reuses an existing container, repairing any network attachments whose
// endpoint ID has drifted from the live network — e.g. after
// `docker compose down && up`.
func (b *Backend) adopt(ctx context.Context, spec *backend.Spec, containerID string) (string, error) {
	b.log.Info("adopting existing container", "task", spec.TaskID, "container", containerID)
	repaired, err := dockerx.RepairNetworks(ctx, b.docker, containerID, spec.Workspace.Container.Networks)
	if err != nil {
		return "", fmt.Errorf("failed to repair network attachment: %w", err)
	}
	if len(repaired) > 0 {
		b.log.Warn("repaired stale network attachments",
			"task", spec.TaskID, "container", containerID, "networks", repaired)
	}
	return containerID, nil
}

// create creates the task's gritz-{taskID} container running the driver server
// and copies the driver binary into it, along with the driver's state
// directory, which a non-root driver cannot create itself. spec.Files arrive in
// the RunSpec, which the driver provisions on the first run. The container gets
// only the workspace's environment: the run's env, secrets and token arrive in
// the RunSpec too, so a container reused for a later run never holds stale
// ones.
func (b *Backend) create(ctx context.Context, spec *backend.Spec) (string, error) {
	wc := &spec.Workspace.Container

	b.log.Info("creating container", "task", spec.TaskID, "image", wc.Image)

	// Ensure the image is available locally (pulls if needed).
	info, err := dockerx.ImageEnsure(ctx, b.docker, dockerx.ImageEnsureOptions{
		Ref:         wc.Image,
		PullOptions: image.PullOptions{RegistryAuth: dockerx.ResolveRegistryAuth(wc.Image)},
		PullProgress: func(p dockerx.PullProgress) {
			if p.Status != "" && p.Progress == "" {
				b.log.Info("pull", "image", wc.Image, "status", p.Status)
			}
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to ensure image: %w", err)
	}

	// The driver binary is provisioned by the backend because selecting it
	// requires the image architecture, which only the runtime knows.
	binData, err := prebuilt.ReadBinary(info.Architecture)
	if err != nil {
		return "", err
	}

	resp, err := b.docker.ContainerCreate(ctx,
		&container.Config{
			Image: wc.Image,
			User:  wc.User,
			Labels: map[string]string{
				"gritz":        "true",
				"gritz.task":   fmt.Sprint(spec.TaskID),
				"gritz.runner": b.runnerID,
			},
			Cmd:        []string{backend.BinaryPath, "driver", "--serve"},
			Env:        wc.Environ(),
			WorkingDir: wc.WorkingDir,
		},
		&container.HostConfig{
			Binds:       wc.Volumes,
			GroupAdd:    wc.GroupAdd,
			Runtime:     wc.Runtime,
			Privileged:  wc.Privileged,
			NetworkMode: container.NetworkMode(wc.NetworkMode),
		},
		wc.NetworkingConfig(),
		nil,
		fmt.Sprintf("gritz-%d", spec.TaskID),
	)
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	files := []backend.File{
		{Path: backend.BinaryPath, Data: binData, Mode: 0755},
		{Path: path.Dir(runrecord.Path), Mode: 0777, Dir: true},
	}
	if err := b.copyFiles(ctx, resp.ID, files); err != nil {
		b.discard(context.WithoutCancel(ctx), resp.ID)
		return "", fmt.Errorf("failed to copy files: %w", err)
	}

	return resp.ID, nil
}

func (b *Backend) copyFiles(ctx context.Context, containerID string, files []backend.File) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		name := strings.TrimPrefix(f.Path, "/")
		if f.Dir {
			if err := tw.WriteHeader(&tar.Header{
				Name:     name + "/",
				Mode:     f.Mode,
				Typeflag: tar.TypeDir,
			}); err != nil {
				return err
			}
			continue
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: f.Mode,
			Size: int64(len(f.Data)),
		}); err != nil {
			return err
		}
		if _, err := tw.Write(f.Data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return b.docker.CopyToContainer(ctx, containerID, "/", &buf, container.CopyToContainerOptions{})
}

// errNotRunning means the container exists but is not running.
var errNotRunning = errors.New("container is not running")

// dial returns a client for the driver server of a running container, whose
// Run streams fail with driverclient.ErrStreamIdle once the driver goes quiet.
// A missing container is reported as cerrdefs.ErrNotFound, a stopped one as
// errNotRunning.
//
// The client never connects to the container over the network: each
// connection is tunnelled through a Docker exec of `gritz tool proxy`, so the
// runner needs only Docker API access to reach the driver. Keep-alives are
// off, so no idle connection holds an exec open.
func (b *Backend) dial(ctx context.Context, containerID string) (driverv1connect.DriverServiceClient, error) {
	info, err := b.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container: %w", err)
	}
	if info.State == nil || !info.State.Running {
		return nil, errNotRunning
	}
	httpClient := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return b.execConn(ctx, containerID)
		},
		DisableKeepAlives: true,
	}}
	return driverclient.New(httpClient, "http://driver", driverclient.Options{IdleTimeout: b.idleTimeout}), nil
}

// execConn opens a connection to the container's driver server through a
// Docker exec of `gritz tool proxy`: writes go to the exec's stdin, and reads
// come from its demultiplexed stdout.
func (b *Backend) execConn(ctx context.Context, containerID string) (net.Conn, error) {
	exec, err := b.docker.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		AttachStdin:  true,
		AttachStdout: true,
		Cmd:          []string{backend.BinaryPath, "tool", "proxy", "--addr", net.JoinHostPort("127.0.0.1", strconv.Itoa(DriverPort))},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create driver proxy exec: %w", err)
	}
	resp, err := b.docker.ContainerExecAttach(ctx, exec.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to attach to driver proxy exec: %w", err)
	}
	pr, pw := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(pw, io.Discard, resp.Reader)
		pw.CloseWithError(err)
	}()
	return &execConn{Conn: resp.Conn, stdout: pr}, nil
}

// execConn is a hijacked exec attach connection whose reads are the exec's
// demultiplexed stdout.
type execConn struct {
	net.Conn
	stdout *io.PipeReader
}

func (c *execConn) Read(p []byte) (int, error) {
	return c.stdout.Read(p)
}

func (c *execConn) Close() error {
	_ = c.stdout.Close()
	return c.Conn.Close()
}

// waitReady polls Status until the driver server answers or
// the backend's readyTimeout elapses.
func (b *Backend) waitReady(ctx context.Context, client driverv1connect.DriverServiceClient) error {
	ctx, cancel := context.WithTimeout(ctx, b.readyTimeout)
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
func (b *Backend) start(ctx context.Context, client driverv1connect.DriverServiceClient, version int64, spec *driverv1.RunSpec) error {
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
// the container, so between runs it is stopped exactly as the docker backend's is. It
// returns 0 if the driver reported the run's outcome, else ExitLost. A dropped
// stream is reattached while the container is running, backing off
// exponentially; a container that is gone or stopped before Finished arrived is
// reported as lost.
//
// Reattaching gives up once the backend's lostTimeout has passed without a good
// message — an accepted stream — from the driver: Wait stops the container and
// reports the run lost. It returns only once the stop succeeded or the
// container is gone, so Probe never reports a container running that no Wait
// supervises; while dockerd cannot stop it, Wait keeps retrying.
func (b *Backend) Wait(ctx context.Context, h backend.Handle) (backend.ExitCode, error) {
	if foreign(h) {
		return backend.ExitLost, nil
	}
	d, err := decodeHandle(h)
	if err != nil {
		b.log.Error("wait: invalid handle", "container", h.ID, "err", err)
		return backend.ExitLost, nil
	}
	lastGood := time.Now()
	delay := driverRetryInterval
	var lastErr string
	for {
		finished, accepted, err := b.attach(ctx, h.ID, d.Version)
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		switch {
		case finished != nil:
			b.stopAfter(ctx, h.ID)
			if !finished.GetReported() {
				return backend.ExitLost, nil
			}
			return 0, nil
		case cerrdefs.IsNotFound(err) || errors.Is(err, errNotRunning):
			b.log.Error("container exited before its run finished", "container", h.ID, "version", d.Version, "err", err)
			return backend.ExitLost, nil
		case isTerminal(err):
			// The driver has no run with this version (or has moved past it),
			// so its Finished will never come.
			b.log.Error("driver has no such run", "container", h.ID, "version", d.Version, "err", err)
			return backend.ExitLost, nil
		}
		if accepted {
			// The stream was live until it dropped: the driver's idle watchdog
			// would have ended it otherwise.
			lastGood = time.Now()
			delay = driverRetryInterval
			lastErr = ""
		}
		if time.Since(lastGood) >= b.lostTimeout {
			// Give up on the driver; stop the container instead. While that
			// fails, keep retrying the attach too in case the driver is back.
			stopErr := b.docker.ContainerStop(ctx, h.ID, container.StopOptions{})
			if stopErr == nil || cerrdefs.IsNotFound(stopErr) {
				b.log.Error("run lost: driver unreachable, container stopped", "container", h.ID, "version", d.Version, "err", err)
				return backend.ExitLost, nil
			}
			err = fmt.Errorf("failed to stop container of lost run: %w (driver: %w)", stopErr, err)
		}
		if msg := err.Error(); msg != lastErr {
			b.log.Warn("run stream dropped, reattaching", "container", h.ID, "version", d.Version, "err", err)
			lastErr = msg
		}
		if err := sleep(ctx, delay); err != nil {
			return 0, err
		}
		delay = min(delay*2, reattachMaxInterval)
	}
}

// attach opens a Run stream for version without a spec and reads it until the
// run's Finished, which it returns. Otherwise it returns why the stream ended.
// accepted reports whether the driver accepted the stream before it ended.
func (b *Backend) attach(ctx context.Context, containerID string, version int64) (finished *driverv1.Finished, accepted bool, err error) {
	client, err := b.dial(ctx, containerID)
	if err != nil {
		return nil, false, err
	}
	stream, err := client.Run(ctx, &driverv1.RunRequest{Version: version})
	if err != nil {
		return nil, false, err
	}
	defer stream.Close()
	for stream.Receive() {
		msg := stream.Msg()
		if msg.GetAccepted() != nil {
			accepted = true
		}
		if finished := msg.GetFinished(); finished != nil {
			return finished, accepted, nil
		}
	}
	return nil, accepted, cmp.Or(stream.Err(), errors.New("run stream ended before Finished"))
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

// stopAfter stops the container once its run has finished. No later run can
// have started on it meanwhile: Probe reports a running container as running,
// so the runner does not launch on it until it is stopped. Failures are
// logged: the run's outcome is already known.
func (b *Backend) stopAfter(ctx context.Context, containerID string) {
	err := b.docker.ContainerStop(ctx, containerID, container.StopOptions{})
	if err != nil && !cerrdefs.IsNotFound(err) {
		b.log.Error("failed to stop container after its run", "container", containerID, "err", err)
	}
}

// Probe reports the liveness of the handle's container: a missing container is
// gone, a stopped one exited, and a running one running — even after its run
// has finished, until Wait stops it. It does not ask the driver: a running
// container is never launched on, so a new run cannot race the stop.
func (b *Backend) Probe(ctx context.Context, h backend.Handle) (backend.State, error) {
	if foreign(h) {
		return backend.StateGone, nil
	}
	info, err := b.docker.ContainerInspect(ctx, h.ID)
	if cerrdefs.IsNotFound(err) {
		return backend.StateGone, nil
	}
	if err != nil {
		return backend.StateUnknown, fmt.Errorf("failed to inspect container: %w", err)
	}
	if info.State != nil && info.State.Running {
		return backend.StateRunning, nil
	}
	return backend.StateExited, nil
}

// Signal asks the driver to stop the handle's run, reporting whether it was
// running. The driver reports stopped and sends Finished, after which Wait
// stops the container.
func (b *Backend) Signal(ctx context.Context, h backend.Handle) (bool, error) {
	if foreign(h) {
		return false, nil
	}
	d, err := decodeHandle(h)
	if err != nil {
		return false, err
	}
	b.log.Info("stopping run", "container", h.ID, "version", d.Version)
	client, err := b.dial(ctx, h.ID)
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

// Destroy removes the handle's container. A missing container is not an error.
func (b *Backend) Destroy(ctx context.Context, h backend.Handle) error {
	if foreign(h) {
		return nil
	}
	err := b.docker.ContainerRemove(ctx, h.ID, container.RemoveOptions{Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("failed to remove container: %w", err)
	}
	return nil
}

// newRunSpec builds the driver.v1 RunSpec for spec. The runner hands every
// backend the one-shot driver's environment (GRITZ_SERVER, GRITZ_TOKEN, the
// workspace secrets and GRITZ_SECRETS naming them); the server URL, token and
// secrets are lifted out of it into their own fields, and the rest is the
// run's env. spec.Files are carried as they are.
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
	for _, f := range spec.Files {
		rs.Files = append(rs.Files, &driverv1.File{
			Path: f.Path,
			Data: f.Data,
			Mode: f.Mode,
			Dir:  f.Dir,
		})
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
