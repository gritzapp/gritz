// Package experimentaldocker implements the runner backend on the local Docker
// daemon with the driver server (`gritz driver --serve`) as each container's
// main process, starting each run over the driver.v1 protocol. This is phase A
// of proposals/draft/driver-server.md: it implements the same backend.Backend
// as the docker package, the driver still reports to the gritz server itself,
// and the container is stopped after every run.
//
// It shares no code with the docker package on purpose: that backend is
// removed in phase E, and the two evolve independently until then.
// Containers are named gritz-{task-id} and labelled with the owning runner, as
// the docker package's are.
package experimentaldocker

import (
	"archive/tar"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/prebuilt"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/dockerx"
)

// HandleType is the backend.Handle.Type this backend stamps on the handles it
// produces.
const HandleType = "experimental-docker"

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

// Backend runs task sandboxes as containers on the local Docker daemon whose
// main process is the driver server.
//
// A handle's Data carries the version of the run Launch started, which Wait,
// Probe and Signal address the driver with.
type Backend struct {
	docker   *client.Client
	http     *http.Client
	runnerID string
	log      *slog.Logger
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
		docker:   docker,
		http:     &http.Client{},
		runnerID: opts.RunnerID,
		log:      cmp.Or(opts.Log, slog.Default()),
	}, nil
}

func (b *Backend) Close() error {
	return b.docker.Close()
}

// ValidateWorkspace reports whether the workspace's container config is usable:
// it needs an image, and a network the runner can reach the driver server on.
func (b *Backend) ValidateWorkspace(ws *workspace.Workspace) error {
	if err := ws.Container.Validate(); err != nil {
		return err
	}
	mode := container.NetworkMode(ws.Container.NetworkMode)
	if mode.IsNone() || mode.IsContainer() {
		return fmt.Errorf("network_mode %q is not supported: the runner must reach the driver server", ws.Container.NetworkMode)
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
// that container is gone; it never creates a fresh one. Starting a container that is already
// running is a no-op, so a container whose previous run has finished but which
// has not been stopped yet is reused as is.
func (b *Backend) Launch(ctx context.Context, spec *backend.Spec, reuse *backend.Handle) (backend.Handle, error) {
	containerID, err := b.ensure(ctx, spec, reuse)
	if err != nil {
		return backend.Handle{}, err
	}
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
// and copies the driver binary and spec.Files into it. The container gets only
// the workspace's environment: the run's env, secrets and token arrive in the
// RunSpec, so a container reused for a later run never holds stale ones.
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

	labels := map[string]string{
		"gritz":        "true",
		"gritz.task":   fmt.Sprint(spec.TaskID),
		"gritz.runner": b.runnerID,
	}
	cmd := []string{backend.BinaryPath, "driver", "--serve"}
	if container.NetworkMode(wc.NetworkMode).IsHost() {
		addr, err := freeLocalAddr()
		if err != nil {
			return "", err
		}
		cmd = append(cmd, "--addr", addr)
		labels[driverAddrLabel] = addr
	}

	resp, err := b.docker.ContainerCreate(ctx,
		&container.Config{
			Image:      wc.Image,
			User:       wc.User,
			Labels:     labels,
			Cmd:        cmd,
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

	files := append([]backend.File{
		{Path: backend.BinaryPath, Data: binData, Mode: 0755},
	}, spec.Files...)
	if err := b.copyFiles(ctx, resp.ID, files); err != nil {
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
func (b *Backend) dial(ctx context.Context, containerID string) (driverv1connect.DriverServiceClient, error) {
	info, err := b.docker.ContainerInspect(ctx, containerID)
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
	return driverv1connect.NewDriverServiceClient(b.http, "http://"+addr), nil
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
func (b *Backend) waitReady(ctx context.Context, client driverv1connect.DriverServiceClient) error {
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
// stream is reattached while the container is running; a container that is gone
// or stopped before Finished arrived is reported as lost.
func (b *Backend) Wait(ctx context.Context, h backend.Handle) (backend.ExitCode, error) {
	if foreign(h) {
		return backend.ExitLost, nil
	}
	d, err := decodeHandle(h)
	if err != nil {
		b.log.Error("wait: invalid handle", "container", h.ID, "err", err)
		return backend.ExitLost, nil
	}
	for {
		finished, err := b.attach(ctx, h.ID, d.Version)
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		switch {
		case finished != nil:
			b.stopAfter(ctx, h.ID, d.Version)
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
		b.log.Warn("run stream dropped, reattaching", "container", h.ID, "version", d.Version, "err", err)
		if err := sleep(ctx, driverRetryInterval); err != nil {
			return 0, err
		}
	}
}

// attach opens a Run stream for version without a spec and reads it until the
// run's Finished, which it returns. Otherwise it returns why the stream ended.
func (b *Backend) attach(ctx context.Context, containerID string, version int64) (*driverv1.Finished, error) {
	client, err := b.dial(ctx, containerID)
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
func (b *Backend) stopAfter(ctx context.Context, containerID string, version int64) {
	if client, err := b.dial(ctx, containerID); err == nil {
		resp, err := client.Status(ctx, &driverv1.StatusRequest{})
		if err == nil && resp.GetRun().GetVersion() != version {
			return
		}
	}
	err := b.docker.ContainerStop(ctx, containerID, container.StopOptions{})
	if err != nil && !cerrdefs.IsNotFound(err) {
		b.log.Error("failed to stop container after its run", "container", containerID, "err", err)
	}
}

// Probe reports the liveness of the handle's run: a missing container is gone,
// a stopped one exited, and a running one is running only while the driver's
// run record shows the handle's version running.
func (b *Backend) Probe(ctx context.Context, h backend.Handle) (backend.State, error) {
	if foreign(h) {
		return backend.StateGone, nil
	}
	d, err := decodeHandle(h)
	if err != nil {
		return backend.StateUnknown, err
	}
	client, err := b.dial(ctx, h.ID)
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
