// Package experimentallambdamicrovm implements the runner backend on AWS
// Lambda MicroVMs with the driver server (`gritz driver --serve
// --aws-lambda-hooks`) as each VM's main process, starting each run over the
// driver.v1 protocol through AWS's managed proxy. It is the Lambda counterpart
// of experimentaldocker, and like it is phase A of
// proposals/draft/driver-server.md: it implements the same backend.Backend as
// the lambdamicrovm package, the driver still reports to the gritz server
// itself, and the VM is suspended after every run.
//
// It shares no code with the lambdamicrovm or experimentaldocker packages on
// purpose: lambdamicrovm and its in-VM shim are removed once this one replaces
// them, and the backends evolve independently until then. Agent files arrive
// in the RunSpec, so nothing is staged in S3. See README.md for building the
// VM image.
package experimentallambdamicrovm

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/driverclient"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/awsmicrovm"
)

// HandleType is the backend.Handle.Type this backend stamps on the handles it
// produces.
const HandleType = "experimental-lambda-microvm"

// DriverPort is the port `gritz driver --serve` listens on inside the VM, and
// the only port the proxy tokens this backend mints may reach.
const DriverPort = 8080

// allIngressConnector is the Lambda-managed connector granting inbound
// connectivity, which the managed proxy needs. It is the default when a
// workspace does not pin a port-scoped connector; %s is the region.
const allIngressConnector = "arn:aws:lambda:%s:aws:network-connector:aws-network-connector:ALL_INGRESS"

// defaultMaxDuration is used when a workspace does not set
// max_duration_seconds. Lambda caps the value at 28800 (8h).
const defaultMaxDuration int64 = 14400

const (
	// tokenLifetime is the lifetime of a minted proxy token, and
	// tokenRefreshMargin how long before it expires a client mints another.
	tokenLifetime      = time.Hour
	tokenRefreshMargin = 5 * time.Minute
	// driverReadyTimeout bounds how long Launch waits for a running VM's
	// driver server to answer Status.
	driverReadyTimeout = time.Minute
	// driverRetryInterval is the pause between attempts to reach the driver,
	// and the first pause between attempts to reattach to a run.
	driverRetryInterval = 250 * time.Millisecond
	// reattachMaxInterval caps the backoff between attempts to reattach.
	reattachMaxInterval = 10 * time.Second
	// driverLostTimeout is how long Wait keeps reattaching without a good
	// message from the driver before it suspends the VM and reports the run
	// lost.
	driverLostTimeout = 2 * time.Minute
	// stateTimeout bounds how long the backend polls a VM to reach RUNNING or
	// SUSPENDED, and statePollInterval is the pause between polls.
	stateTimeout      = 5 * time.Minute
	statePollInterval = time.Second
)

// Cloud is the subset of the AWS Lambda MicroVMs control plane the backend
// uses. *awsmicrovm.Client implements it; tests use a fake.
type Cloud interface {
	RunMicrovm(ctx context.Context, in *awsmicrovm.RunMicrovmInput) (*awsmicrovm.RunMicrovmOutput, error)
	GetMicrovm(ctx context.Context, in *awsmicrovm.GetMicrovmInput) (*awsmicrovm.GetMicrovmOutput, error)
	TerminateMicrovm(ctx context.Context, in *awsmicrovm.TerminateMicrovmInput) (*awsmicrovm.TerminateMicrovmOutput, error)
	SuspendMicrovm(ctx context.Context, in *awsmicrovm.SuspendMicrovmInput) (*awsmicrovm.SuspendMicrovmOutput, error)
	ResumeMicrovm(ctx context.Context, in *awsmicrovm.ResumeMicrovmInput) (*awsmicrovm.ResumeMicrovmOutput, error)
	CreateMicrovmAuthToken(ctx context.Context, in *awsmicrovm.CreateMicrovmAuthTokenInput) (*awsmicrovm.CreateMicrovmAuthTokenOutput, error)
}

// Backend runs task sandboxes as AWS Lambda MicroVMs whose main process is the
// driver server.
//
// A handle's ID is the MicroVM id, and its Data carries the VM's proxy
// endpoint and the version of the run Launch started, which Wait and Signal
// address the driver with.
type Backend struct {
	cloud     Cloud
	transport http.RoundTripper
	region    string
	log       *slog.Logger
	// The timeouts and intervals below are shortened by tests.
	readyTimeout  time.Duration
	idleTimeout   time.Duration
	lostTimeout   time.Duration
	stateTimeout  time.Duration
	stateInterval time.Duration
}

var _ backend.Backend = (*Backend)(nil)

type Options struct {
	Cloud Cloud
	// Transport carries requests to the VMs' proxy endpoints, under the proxy
	// token. Defaults to a clone of http.DefaultTransport.
	Transport http.RoundTripper
	// Region is the single AWS region this runner's MicroVMs run in.
	Region string
	Log    *slog.Logger
}

func New(opts Options) (*Backend, error) {
	if opts.Cloud == nil {
		return nil, errors.New("experimentallambdamicrovm: Cloud is required")
	}
	transport := opts.Transport
	if transport == nil {
		transport = http.DefaultTransport.(*http.Transport).Clone()
	}
	return &Backend{
		cloud:         opts.Cloud,
		transport:     transport,
		region:        opts.Region,
		log:           cmp.Or(opts.Log, slog.Default()),
		readyTimeout:  driverReadyTimeout,
		idleTimeout:   driverclient.DefaultIdleTimeout,
		lostTimeout:   driverLostTimeout,
		stateTimeout:  stateTimeout,
		stateInterval: statePollInterval,
	}, nil
}

func (b *Backend) Close() error { return nil }

// ValidateWorkspace requires the lambda_microvm config a VM is launched with.
// staging_bucket is unused: agent files arrive in the RunSpec.
func (b *Backend) ValidateWorkspace(ws *workspace.Workspace) error {
	cfg := ws.LambdaMicroVM
	if cfg == nil {
		return errors.New("lambda_microvm config is required")
	}
	if cfg.ImageIdentifier == "" {
		return errors.New("lambda_microvm.image_identifier is required")
	}
	if cfg.ExecutionRole == "" {
		return errors.New("lambda_microvm.execution_role is required")
	}
	if cfg.EgressConnector == "" {
		return errors.New("lambda_microvm.egress_connector is required")
	}
	if cfg.MaxDurationSeconds < 0 || cfg.MaxDurationSeconds > 28800 {
		return errors.New("lambda_microvm.max_duration_seconds must be in (0, 28800]")
	}
	if cfg.Region != "" && b.region != "" && cfg.Region != b.region {
		return fmt.Errorf("lambda_microvm.region %q does not match runner region %q (multi-region is unsupported)", cfg.Region, b.region)
	}
	return nil
}

// handleData is the backend-defined part of an experimental handle.
type handleData struct {
	Endpoint string `json:"endpoint"`
	Version  int64  `json:"version"`
}

// foreign reports whether h was produced by another backend. This backend
// replaces lambdamicrovm rather than running beside it, so it treats such a
// handle's sandbox as gone: Launch rejects it with backend.ErrGone, Probe
// answers StateGone, Wait reports the run lost, and Signal and Destroy leave
// it alone.
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
	if d.Endpoint == "" {
		return d, fmt.Errorf("handle %s has no endpoint", h.ID)
	}
	return d, nil
}

// Launch ensures the task's VM exists and is running, waits for its driver
// server to answer, and starts the spec's run on it. Without a reuse handle it
// runs a fresh VM. With one it adopts the exact recorded VM, resuming it if it
// is suspended; it returns backend.ErrGone if that VM is terminated or not
// found, and an error if it is still suspending. It never runs a fresh VM in
// place of a recorded one.
//
// A failed Launch cleans up after itself, since the runner gets no handle to do
// it with: a VM it ran is terminated, and an adopted one is suspended — not
// terminated, as the task's record still points at it — so Probe does not
// report it running with no run on it.
func (b *Backend) Launch(ctx context.Context, spec *backend.Spec, reuse *backend.Handle) (backend.Handle, error) {
	if err := b.ValidateWorkspace(spec.Workspace); err != nil {
		return backend.Handle{}, err
	}
	fresh := reuse == nil || reuse.ID == ""
	var (
		id       string
		endpoint string
		err      error
	)
	if fresh {
		id, endpoint, err = b.run(ctx, spec)
	} else {
		id, endpoint, err = b.adopt(ctx, reuse)
	}
	if err != nil {
		return backend.Handle{}, err
	}
	h, err := b.launch(ctx, spec, id, endpoint)
	if err != nil {
		// ctx being cancelled is one of the failures to clean up after.
		ctx := context.WithoutCancel(ctx)
		if fresh {
			b.discard(ctx, id)
		} else {
			b.suspendFailed(ctx, id)
		}
		return backend.Handle{}, err
	}
	return h, nil
}

// run runs a fresh VM for spec and returns its id and endpoint. The VM gets no
// run-hook payload: its run arrives over driver.v1. A VM that does not reach
// RUNNING is terminated.
func (b *Backend) run(ctx context.Context, spec *backend.Spec) (string, string, error) {
	cfg := spec.Workspace.LambdaMicroVM
	b.log.Info("running microvm", "task", spec.TaskID, "image", cfg.ImageIdentifier)
	out, err := b.cloud.RunMicrovm(ctx, &awsmicrovm.RunMicrovmInput{
		ImageIdentifier:          cfg.ImageIdentifier,
		ExecutionRoleArn:         cfg.ExecutionRole,
		EgressNetworkConnectors:  []string{cfg.EgressConnector},
		IngressNetworkConnectors: []string{cmp.Or(cfg.IngressConnector, fmt.Sprintf(allIngressConnector, b.region))},
		MaximumDurationInSeconds: cmp.Or(cfg.MaxDurationSeconds, defaultMaxDuration),
		// No idle policy: Wait suspends the VM after every run.
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to run microvm: %w", err)
	}
	vm, err := b.await(ctx, out.MicrovmID, awsmicrovm.MicrovmStateRunning)
	if err != nil {
		b.discard(context.WithoutCancel(ctx), out.MicrovmID)
		return "", "", err
	}
	return out.MicrovmID, cmp.Or(vm.Endpoint, out.Endpoint), nil
}

// adopt resolves the reuse handle's VM and brings it to RUNNING, returning its
// id and endpoint: a suspended VM is resumed, and a pending or running one is
// adopted as it is.
func (b *Backend) adopt(ctx context.Context, reuse *backend.Handle) (string, string, error) {
	if foreign(*reuse) {
		return "", "", backend.ErrGone
	}
	out, err := b.cloud.GetMicrovm(ctx, &awsmicrovm.GetMicrovmInput{MicrovmID: reuse.ID})
	if awsmicrovm.IsNotFound(err) {
		return "", "", backend.ErrGone
	}
	if err != nil {
		return "", "", fmt.Errorf("failed to get microvm: %w", err)
	}
	switch state := out.Microvm.State; {
	case state.Terminal():
		return "", "", backend.ErrGone
	case state == awsmicrovm.MicrovmStateSuspending:
		return "", "", fmt.Errorf("microvm %s is still suspending", reuse.ID)
	case state == awsmicrovm.MicrovmStateSuspended:
		b.log.Info("resuming microvm", "microvm", reuse.ID)
		if _, err := b.cloud.ResumeMicrovm(ctx, &awsmicrovm.ResumeMicrovmInput{MicrovmID: reuse.ID}); err != nil {
			if awsmicrovm.IsNotFound(err) {
				return "", "", backend.ErrGone
			}
			return "", "", fmt.Errorf("failed to resume microvm: %w", err)
		}
	default:
		b.log.Info("adopting running microvm", "microvm", reuse.ID, "state", state)
	}
	vm, err := b.await(ctx, reuse.ID, awsmicrovm.MicrovmStateRunning)
	if err != nil {
		return "", "", err
	}
	endpoint := vm.Endpoint
	if endpoint == "" {
		// The control plane did not repeat it; fall back to the recorded one.
		var d handleData
		_ = json.Unmarshal(reuse.Data, &d)
		endpoint = d.Endpoint
	}
	if endpoint == "" {
		return "", "", fmt.Errorf("microvm %s has no endpoint", reuse.ID)
	}
	return reuse.ID, endpoint, nil
}

// launch waits for the VM's driver server and starts the spec's run on it.
func (b *Backend) launch(ctx context.Context, spec *backend.Spec, id, endpoint string) (backend.Handle, error) {
	client := b.dial(id, endpoint)
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
	data, err := json.Marshal(handleData{Endpoint: endpoint, Version: spec.Version})
	if err != nil {
		return backend.Handle{}, err
	}
	return backend.Handle{Type: HandleType, ID: id, Data: data}, nil
}

// discard terminates a VM this Launch ran and failed to start a run on: the
// runner has no record of it, so nothing else would. Failures are logged:
// Launch returns the error that caused them.
func (b *Backend) discard(ctx context.Context, id string) {
	b.log.Info("terminating microvm after failed launch", "microvm", id)
	_, err := b.cloud.TerminateMicrovm(ctx, &awsmicrovm.TerminateMicrovmInput{MicrovmID: id})
	if err != nil && !awsmicrovm.IsNotFound(err) {
		b.log.Error("failed to terminate microvm after failed launch", "microvm", id, "err", err)
	}
}

// suspendFailed suspends an adopted VM Launch failed to start a run on, so the
// next start launches on it again rather than finding it running. Failures
// are logged: Launch returns the error that caused them.
func (b *Backend) suspendFailed(ctx context.Context, id string) {
	b.log.Info("suspending microvm after failed launch", "microvm", id)
	if err := b.suspend(ctx, id); err != nil {
		b.log.Error("failed to suspend microvm after failed launch", "microvm", id, "err", err)
	}
}

// suspend suspends the VM and polls until it is SUSPENDED. A VM that is gone
// is not an error.
func (b *Backend) suspend(ctx context.Context, id string) error {
	_, err := b.cloud.SuspendMicrovm(ctx, &awsmicrovm.SuspendMicrovmInput{MicrovmID: id})
	if awsmicrovm.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to suspend microvm: %w", err)
	}
	_, err = b.await(ctx, id, awsmicrovm.MicrovmStateSuspended)
	if errors.Is(err, backend.ErrGone) {
		return nil
	}
	return err
}

// await polls the VM until it is in state want and returns it. A VM that is
// terminated or not found is backend.ErrGone; one that is not in want within
// the backend's stateTimeout is an error.
func (b *Backend) await(ctx context.Context, id string, want awsmicrovm.MicrovmState) (awsmicrovm.Microvm, error) {
	ctx, cancel := context.WithTimeout(ctx, b.stateTimeout)
	defer cancel()
	for {
		out, err := b.cloud.GetMicrovm(ctx, &awsmicrovm.GetMicrovmInput{MicrovmID: id})
		switch {
		case awsmicrovm.IsNotFound(err):
			return awsmicrovm.Microvm{}, backend.ErrGone
		case err != nil:
			b.log.Warn("failed to get microvm", "microvm", id, "err", err)
		case out.Microvm.State == want:
			return out.Microvm, nil
		case out.Microvm.State.Terminal():
			return awsmicrovm.Microvm{}, backend.ErrGone
		}
		if err := sleep(ctx, b.stateInterval); err != nil {
			return awsmicrovm.Microvm{}, fmt.Errorf("microvm %s did not become %s: %w", id, want, err)
		}
	}
}

// dial returns a client for the driver server of the VM at endpoint, through
// the managed proxy, whose Run streams fail with driverclient.ErrStreamIdle
// once the driver goes quiet. The proxy speaks HTTP/2 and the Connect
// protocol, connect-go's default.
func (b *Backend) dial(id, endpoint string) driverv1connect.DriverServiceClient {
	baseURL := endpoint
	if !strings.Contains(baseURL, "://") {
		baseURL = "https://" + baseURL
	}
	httpClient := &http.Client{Transport: &tokenTransport{
		base: b.transport,
		mint: func(ctx context.Context) (string, error) {
			out, err := b.cloud.CreateMicrovmAuthToken(ctx, &awsmicrovm.CreateMicrovmAuthTokenInput{
				MicrovmID:         id,
				ExpirationMinutes: int(tokenLifetime / time.Minute),
				AllowedPorts:      []awsmicrovm.AllowedPort{{Port: DriverPort}},
			})
			if err != nil {
				return "", fmt.Errorf("failed to mint proxy token: %w", err)
			}
			return out.Token, nil
		},
	}}
	return driverclient.New(httpClient, strings.TrimRight(baseURL, "/"), driverclient.Options{IdleTimeout: b.idleTimeout})
}

// tokenTransport adds a proxy token to every request as
// awsmicrovm.ProxyAuthHeader. It mints the token on first use and caches it
// until tokenRefreshMargin before it expires.
type tokenTransport struct {
	base http.RoundTripper
	mint func(ctx context.Context) (string, error)

	mu      sync.Mutex
	token   string
	expires time.Time
}

func (t *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.get(req.Context())
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	req = req.Clone(req.Context())
	req.Header.Set(awsmicrovm.ProxyAuthHeader, token)
	return t.base.RoundTrip(req)
}

func (t *tokenTransport) get(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Until(t.expires) > tokenRefreshMargin {
		return t.token, nil
	}
	now := time.Now()
	token, err := t.mint(ctx)
	if err != nil {
		return "", err
	}
	t.token, t.expires = token, now.Add(tokenLifetime)
	return token, nil
}

// waitReady polls Status until the driver server answers or the backend's
// readyTimeout elapses.
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

// Wait attaches to the handle's run and blocks until its Finished, then
// suspends the VM and polls until it is SUSPENDED, so between runs the VM costs
// no compute. It returns 0 if the driver reported the run's outcome, else
// ExitLost. A dropped stream is reattached while the VM is pending or running,
// backing off exponentially; a VM that is gone or no longer running before
// Finished arrived is reported as lost.
//
// Reattaching gives up once the backend's lostTimeout has passed without a good
// message — an accepted stream — from the driver: Wait suspends the VM and
// reports the run lost. It returns only once the suspend succeeded or the VM
// is gone, so Probe never reports a VM running that no Wait supervises; while
// the control plane cannot suspend it, Wait keeps retrying.
func (b *Backend) Wait(ctx context.Context, h backend.Handle) (backend.ExitCode, error) {
	if foreign(h) {
		return backend.ExitLost, nil
	}
	d, err := decodeHandle(h)
	if err != nil {
		b.log.Error("wait: invalid handle", "microvm", h.ID, "err", err)
		return backend.ExitLost, nil
	}
	client := b.dial(h.ID, d.Endpoint)
	lastGood := time.Now()
	delay := driverRetryInterval
	var lastErr string
	for {
		finished, accepted, err := attach(ctx, client, d.Version)
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if finished != nil {
			b.suspendAfter(ctx, h.ID)
			if !finished.GetReported() {
				return backend.ExitLost, nil
			}
			return 0, nil
		}
		if isTerminal(err) {
			// The driver has no run with this version (or has moved past it),
			// so its Finished will never come. Nothing runs on the VM for
			// this handle any more.
			b.log.Error("driver has no such run", "microvm", h.ID, "version", d.Version, "err", err)
			b.suspendAfter(ctx, h.ID)
			return backend.ExitLost, nil
		}
		if state, stateErr := b.state(ctx, h.ID); stateErr != nil {
			err = fmt.Errorf("%w (control plane: %w)", err, stateErr)
		} else if !alive(state) {
			b.log.Error("microvm stopped running before its run finished", "microvm", h.ID, "state", state, "version", d.Version, "err", err)
			if state == awsmicrovm.MicrovmStateSuspending {
				// Let the suspend complete, so Probe does not report the VM
				// running once Wait has returned.
				if _, err := b.await(ctx, h.ID, awsmicrovm.MicrovmStateSuspended); err != nil && !errors.Is(err, backend.ErrGone) {
					b.log.Error("microvm did not finish suspending", "microvm", h.ID, "err", err)
				}
			}
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
			// Give up on the driver; suspend the VM instead. While that
			// fails, keep retrying the attach too in case the driver is back.
			suspendErr := b.suspend(ctx, h.ID)
			if suspendErr == nil {
				b.log.Error("run lost: driver unreachable, microvm suspended", "microvm", h.ID, "version", d.Version, "err", err)
				return backend.ExitLost, nil
			}
			err = fmt.Errorf("failed to suspend microvm of lost run: %w (driver: %w)", suspendErr, err)
		}
		if msg := err.Error(); msg != lastErr {
			b.log.Warn("run stream dropped, reattaching", "microvm", h.ID, "version", d.Version, "err", err)
			lastErr = msg
		}
		if err := sleep(ctx, delay); err != nil {
			return 0, err
		}
		delay = min(delay*2, reattachMaxInterval)
	}
}

// state returns the VM's state, or "" if it is not found.
func (b *Backend) state(ctx context.Context, id string) (awsmicrovm.MicrovmState, error) {
	out, err := b.cloud.GetMicrovm(ctx, &awsmicrovm.GetMicrovmInput{MicrovmID: id})
	if awsmicrovm.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get microvm: %w", err)
	}
	return out.Microvm.State, nil
}

// alive reports whether a VM in state is pending or running, so its driver may
// yet answer.
func alive(state awsmicrovm.MicrovmState) bool {
	return state == awsmicrovm.MicrovmStatePending || state == awsmicrovm.MicrovmStateRunning
}

// attach opens a Run stream for version without a spec and reads it until the
// run's Finished, which it returns. Otherwise it returns why the stream ended.
// accepted reports whether the driver accepted the stream before it ended.
func attach(ctx context.Context, client driverv1connect.DriverServiceClient, version int64) (finished *driverv1.Finished, accepted bool, err error) {
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

// suspendAfter suspends the VM once its run has finished. No later run can
// have started on it meanwhile: Probe reports a running or suspending VM as
// running, so the runner does not launch on it until it is suspended. Failures
// are logged: the run's outcome is already known.
func (b *Backend) suspendAfter(ctx context.Context, id string) {
	if err := b.suspend(ctx, id); err != nil {
		b.log.Error("failed to suspend microvm after its run", "microvm", id, "err", err)
	}
}

// Probe reports the liveness of the handle's VM from the control plane. A
// pending, running or suspending VM is running — even after its run has
// finished, until Wait's suspend completes — so the runner never launches on a
// VM that is still finishing the previous run. A suspended VM is exited, and a
// terminated or missing one gone.
func (b *Backend) Probe(ctx context.Context, h backend.Handle) (backend.State, error) {
	if foreign(h) {
		return backend.StateGone, nil
	}
	out, err := b.cloud.GetMicrovm(ctx, &awsmicrovm.GetMicrovmInput{MicrovmID: h.ID})
	if awsmicrovm.IsNotFound(err) {
		return backend.StateGone, nil
	}
	if err != nil {
		return backend.StateUnknown, fmt.Errorf("failed to get microvm: %w", err)
	}
	switch state := out.Microvm.State; {
	case state == awsmicrovm.MicrovmStatePending, state == awsmicrovm.MicrovmStateRunning, state == awsmicrovm.MicrovmStateSuspending:
		return backend.StateRunning, nil
	case state == awsmicrovm.MicrovmStateSuspended:
		return backend.StateExited, nil
	case state.Terminal():
		return backend.StateGone, nil
	default:
		return backend.StateUnknown, fmt.Errorf("microvm %s is in unknown state %q", h.ID, state)
	}
}

// Signal asks the driver to stop the handle's run, reporting whether it was
// running. The driver reports stopped and sends Finished, after which Wait
// suspends the VM.
func (b *Backend) Signal(ctx context.Context, h backend.Handle) (bool, error) {
	if foreign(h) {
		return false, nil
	}
	d, err := decodeHandle(h)
	if err != nil {
		return false, err
	}
	state, err := b.state(ctx, h.ID)
	if err != nil {
		return false, err
	}
	if !alive(state) {
		return false, nil
	}
	b.log.Info("stopping run", "microvm", h.ID, "version", d.Version)
	resp, err := b.dial(h.ID, d.Endpoint).Stop(ctx, &driverv1.StopRequest{Version: d.Version})
	if err != nil {
		return false, fmt.Errorf("failed to stop run: %w", err)
	}
	return resp.GetStopped(), nil
}

// Destroy terminates the handle's VM. A missing VM is not an error.
func (b *Backend) Destroy(ctx context.Context, h backend.Handle) error {
	if foreign(h) {
		return nil
	}
	b.log.Info("terminating microvm", "microvm", h.ID)
	_, err := b.cloud.TerminateMicrovm(ctx, &awsmicrovm.TerminateMicrovmInput{MicrovmID: h.ID})
	if err != nil && !awsmicrovm.IsNotFound(err) {
		return fmt.Errorf("failed to terminate microvm: %w", err)
	}
	return nil
}

// newRunSpec builds the driver.v1 RunSpec for spec. A VM has no environment of
// its own to carry the workspace's, so the run's env is the workspace's
// lambda_microvm environment overlaid with the one-shot driver's environment
// the runner hands every backend (GRITZ_SERVER, GRITZ_TOKEN, the workspace
// secrets and GRITZ_SECRETS naming them). The server URL, token and secrets
// are lifted out of it into their own fields. spec.Files are carried as they
// are.
func newRunSpec(spec *backend.Spec) (*driverv1.RunSpec, error) {
	env := map[string]string{}
	if cfg := spec.Workspace.LambdaMicroVM; cfg != nil {
		for k, v := range cfg.Environment {
			env[k] = v
		}
	}
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
