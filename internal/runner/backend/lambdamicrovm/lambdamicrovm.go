// Package lambdamicrovm implements the runner backend on AWS Lambda MicroVMs:
// AWS-managed Firecracker microVMs with no host hypervisor, kernel, or
// networking to operate. Each task runs in its own MicroVM launched from a
// pre-built image; the in-VM `gritz tool microvm-shim` receives the lifecycle
// hooks, fetches the task's spec bundle, runs the driver, and notifies the
// runner of the driver's exit over an SSE stream through AWS's managed proxy.
//
// See proposals/draft/lambda-microvm-backend.md. The lifecycle is symmetric
// with Docker: the driver exiting suspends the VM (state preserved, no compute),
// the next run resumes it (driver re-spawned against the preserved disk), and
// the VM is terminated only when the task is archived. All three control-plane
// verbs (suspend, resume, terminate) are the runner's — the guest holds no AWS
// credentials. The control plane is reached through the Cloud and Stager
// interfaces so the orchestration here is unit-tested against fakes.
package lambdamicrovm

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/awsmicrovm"
	"github.com/gritzapp/gritz/internal/x/sse"
)

// HandleType is the backend.Handle.Type the backend stamps on its handles
// (informational metadata persisted in the task record).
const HandleType = "lambda-microvm"

// shimPort is the port the in-VM shim's HTTP server (hooks + gritz control
// surface) listens on; the proxy auth token is scoped to it.
const shimPort = 8080

// gritz control-surface paths the runner reaches over the managed proxy.
const (
	lifecyclePath = "/gritz/lifecycle"
	stopPath      = "/gritz/stop"
)

// EventDriverExited is the SSE event type the shim emits when the supervised
// driver process exits. Its data is a JSON DriverExited. It is the only
// load-bearing event on the /gritz/lifecycle stream; the runner ignores all
// others (keep-alives, etc.).
const EventDriverExited = "driver-exited"

// DriverExited is the payload of an EventDriverExited SSE event: the driver's
// real process exit code, observed by the shim via proc.Wait().
type DriverExited struct {
	Code int `json:"code"`
}

// allIngressConnector is the Lambda-managed connector granting inbound
// connectivity. Reaching the shim endpoint over AWS's managed auth-token proxy
// (where the runner consumes the SSE lifecycle stream and POSTs /gritz/stop)
// requires an ingress connector — the managed proxy is not a path separate from
// ingress connectors. This is the default when a workspace does not pin a
// port-scoped connector; %s is the region. The proxy token is already
// port-scoped to shimPort (see mintToken), so the token layer bounds reachable
// ports even with all-ingress. Validated against real AWS in #1088.
const allIngressConnector = "arn:aws:lambda:%s:aws:network-connector:aws-network-connector:ALL_INGRESS"

// defaultMaxDuration is used when a workspace does not set max_duration_seconds.
// Lambda caps the value at 28800 (8h).
const defaultMaxDuration int64 = 14400

// tokenExpirationMinutes is the lifetime of a minted proxy auth token. The
// stream re-mints on expiry and reconnects, so a short lifetime bounds the blast
// radius of a leaked token without failing healthy tasks.
const tokenExpirationMinutes = 60

// Bundle is the task spec the backend stages and the in-VM shim fetches. It
// carries exactly what backend.Spec holds; File.Data is base64-encoded by the
// JSON marshaller.
type Bundle struct {
	Cmd        []string       `json:"cmd"`
	Env        []string       `json:"env"`
	Files      []backend.File `json:"files"`
	WorkingDir string         `json:"working_dir,omitempty"`
	User       string         `json:"user,omitempty"`
}

// handleData is the backend-defined Handle.Data: everything the backend needs
// to reach and clean up a sandbox, but not for identity (the MicroVM id is the
// Handle.ID). Most importantly it carries the VM Endpoint so the runner can mint
// a token, open the SSE stream, POST /gritz/stop, and resume — without
// re-listing first. The runner persists it opaquely and hands it back on
// Destroy / reuse.
type handleData struct {
	Endpoint    string `json:"endpoint"` // VM proxy endpoint, for SSE + /gritz/stop
	ImageARN    string `json:"image_arn"`
	StageBucket string `json:"stage_bucket"` // staged spec bundle, cleaned on Destroy
	StageKey    string `json:"stage_key"`
}

// Backend runs task sandboxes as AWS Lambda MicroVMs.
type Backend struct {
	cloud    Cloud
	stager   Stager
	http     *http.Client
	runnerID string
	region   string
	log      *slog.Logger
}

type Options struct {
	Cloud  Cloud
	Stager Stager
	// HTTPClient makes the proxy requests (SSE stream, /gritz/stop). It must
	// not impose a timeout that would cut the long-lived SSE stream; defaults to
	// a timeout-free client.
	HTTPClient *http.Client
	RunnerID   string
	// Region is the single AWS region this runner's MicroVMs run in. A workspace
	// may not pin a different region (open question: multi-region).
	Region string
	Log    *slog.Logger
}

func New(opts Options) (*Backend, error) {
	if opts.Cloud == nil || opts.Stager == nil {
		return nil, fmt.Errorf("lambdamicrovm: Cloud and Stager are required")
	}
	return &Backend{
		cloud:    opts.Cloud,
		stager:   opts.Stager,
		http:     cmp.Or(opts.HTTPClient, &http.Client{}),
		runnerID: opts.RunnerID,
		region:   opts.Region,
		log:      cmp.Or(opts.Log, slog.Default()),
	}, nil
}

func (b *Backend) Close() error { return nil }

// ValidateWorkspace requires the Lambda MicroVMs config a workspace needs to
// launch a sandbox. image_source build-on-demand is a documented follow-up, so
// a pre-built image_identifier is required for now.
func (b *Backend) ValidateWorkspace(ws *workspace.Workspace) error {
	cfg := ws.LambdaMicroVM
	if cfg == nil {
		return fmt.Errorf("lambda_microvm config is required")
	}
	if cfg.ImageIdentifier == "" {
		return fmt.Errorf("lambda_microvm.image_identifier is required")
	}
	if cfg.ExecutionRole == "" {
		return fmt.Errorf("lambda_microvm.execution_role is required")
	}
	if cfg.EgressConnector == "" {
		return fmt.Errorf("lambda_microvm.egress_connector is required")
	}
	if cfg.StagingBucket == "" {
		return fmt.Errorf("lambda_microvm.staging_bucket is required")
	}
	if cfg.MaxDurationSeconds < 0 || cfg.MaxDurationSeconds > 28800 {
		return fmt.Errorf("lambda_microvm.max_duration_seconds must be in (0, 28800]")
	}
	if cfg.Region != "" && b.region != "" && cfg.Region != b.region {
		return fmt.Errorf("lambda_microvm.region %q does not match runner region %q (multi-region is unsupported)", cfg.Region, b.region)
	}
	return nil
}

// Launch runs the task's sandbox. With a reuse handle identifying a suspended
// VM it resumes it (the /resume hook re-spawns the driver against the preserved
// disk — the MicroVM analog of reusing an exited Docker container); if that VM
// is gone (terminated / max_duration reaped) it returns backend.ErrGone rather
// than creating a fresh one, since the task is bound 1:1 to its sandbox. Without
// a reuse handle it stages the spec bundle and runs a fresh MicroVM (a task's
// first start). The returned handle's id is the MicroVM id and its Data carries
// the endpoint + staging location.
func (b *Backend) Launch(ctx context.Context, spec *backend.Spec, reuse *backend.Handle) (backend.Handle, error) {
	if err := b.ValidateWorkspace(spec.Workspace); err != nil {
		return backend.Handle{}, err
	}

	// Reuse path: resume the exact recorded VM if it is still alive. If it is
	// gone, surface ErrGone — the runner will fail the task and drop the record;
	// a later start with no handle is a legitimate first-start-fresh.
	if reuse != nil && reuse.ID != "" {
		resumed, h, err := b.tryResume(ctx, reuse)
		if err != nil {
			return backend.Handle{}, err
		}
		if !resumed {
			return backend.Handle{}, backend.ErrGone
		}
		return h, nil
	}

	return b.launchFresh(ctx, spec)
}

// tryResume resumes the reuse handle's MicroVM if it is alive. It returns
// resumed=false (with no error) when the VM is gone or terminal, signalling a
// fresh launch. A running VM is adopted as-is; a suspended one is resumed.
func (b *Backend) tryResume(ctx context.Context, reuse *backend.Handle) (bool, backend.Handle, error) {
	out, err := b.cloud.GetMicrovm(ctx, &awsmicrovm.GetMicrovmInput{MicrovmID: reuse.ID})
	if awsmicrovm.IsNotFound(err) {
		return false, backend.Handle{}, nil
	}
	if err != nil {
		return false, backend.Handle{}, fmt.Errorf("get microvm for resume: %w", err)
	}
	mvm := out.Microvm
	switch mvm.State {
	case awsmicrovm.MicrovmStateRunning, awsmicrovm.MicrovmStatePending:
		// Already running or coming up (e.g. a restart that beat the suspend):
		// adopt it.
	case awsmicrovm.MicrovmStateSuspended:
		b.log.Info("resuming microvm", "microvm", reuse.ID)
		if _, err := b.cloud.ResumeMicrovm(ctx, &awsmicrovm.ResumeMicrovmInput{MicrovmID: reuse.ID}); err != nil {
			return false, backend.Handle{}, fmt.Errorf("resume microvm: %w", err)
		}
	default: // SUSPENDING / TERMINATING / TERMINATED
		return false, backend.Handle{}, nil
	}

	hd, _ := decodeData(reuse.Data)
	if mvm.Endpoint != "" {
		hd.Endpoint = mvm.Endpoint
	}
	data, err := json.Marshal(hd)
	if err != nil {
		return false, backend.Handle{}, fmt.Errorf("marshal handle data: %w", err)
	}
	return true, backend.Handle{Type: HandleType, ID: reuse.ID, Data: data}, nil
}

// launchFresh stages the spec bundle and runs a brand-new MicroVM.
func (b *Backend) launchFresh(ctx context.Context, spec *backend.Spec) (backend.Handle, error) {
	cfg := spec.Workspace.LambdaMicroVM

	bundle := Bundle{
		Cmd:   spec.Cmd,
		Env:   append(cfg.Environ(), spec.Env...),
		Files: spec.Files,
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		return backend.Handle{}, fmt.Errorf("marshal bundle: %w", err)
	}

	maxDuration := cmp.Or(cfg.MaxDurationSeconds, defaultMaxDuration)
	stageKey := fmt.Sprintf("%s/%d.json", b.runnerID, spec.TaskID)
	url, err := b.stager.Stage(ctx, cfg.StagingBucket, stageKey, data, maxDuration)
	if err != nil {
		return backend.Handle{}, fmt.Errorf("stage bundle: %w", err)
	}

	// The shim endpoint is reached over the managed proxy, which requires an
	// ingress connector. Default to the managed all-ingress connector; a
	// workspace may pin a port-scoped one for tighter, defense-in-depth security.
	ingressConnector := cmp.Or(cfg.IngressConnector, fmt.Sprintf(allIngressConnector, b.region))

	b.log.Info("running microvm", "task", spec.TaskID, "image", cfg.ImageIdentifier)
	res, err := b.cloud.RunMicrovm(ctx, &awsmicrovm.RunMicrovmInput{
		ImageIdentifier:          cfg.ImageIdentifier,
		ExecutionRoleArn:         cfg.ExecutionRole,
		EgressNetworkConnectors:  []string{cfg.EgressConnector},
		IngressNetworkConnectors: []string{ingressConnector},
		MaximumDurationInSeconds: maxDuration,
		RunHookPayload:           url,
		// No idle policy: the runner drives suspend explicitly off the driver-exit
		// signal (see Wait). The service's idlePolicy cannot express "never
		// auto-suspend" (maxIdleDurationSeconds has a 600s floor), so it is omitted
		// rather than set to a partial/invalid value.
	})
	if err != nil {
		_ = b.stager.Remove(ctx, cfg.StagingBucket, stageKey)
		return backend.Handle{}, fmt.Errorf("run microvm: %w", err)
	}

	hd, err := json.Marshal(handleData{
		Endpoint:    res.Endpoint,
		ImageARN:    cfg.ImageIdentifier,
		StageBucket: cfg.StagingBucket,
		StageKey:    stageKey,
	})
	if err != nil {
		return backend.Handle{}, fmt.Errorf("marshal handle data: %w", err)
	}
	return backend.Handle{Type: HandleType, ID: res.MicrovmID, Data: hd}, nil
}

// Probe reports the liveness of a handle from the control plane. A SUSPENDED VM
// (the normal post-completion state) reports StateExited so it looks like an
// exited-but-preserved container: the orchestrator then drives Start → Probe
// StateExited → Launch(reuse) → resume, and Prune-on-archive → Destroy →
// terminate, unchanged. A TERMINATED or absent VM is gone (nothing to resume or
// destroy).
func (b *Backend) Probe(ctx context.Context, h backend.Handle) (backend.State, error) {
	out, err := b.cloud.GetMicrovm(ctx, &awsmicrovm.GetMicrovmInput{MicrovmID: h.ID})
	if awsmicrovm.IsNotFound(err) {
		return backend.StateGone, nil
	}
	if err != nil {
		return backend.StateUnknown, fmt.Errorf("get microvm: %w", err)
	}
	switch out.Microvm.State {
	case awsmicrovm.MicrovmStateRunning, awsmicrovm.MicrovmStatePending:
		return backend.StateRunning, nil // up, or coming up
	case awsmicrovm.MicrovmStateTerminating, awsmicrovm.MicrovmStateTerminated:
		return backend.StateGone, nil // gone: nothing to resume or destroy
	default:
		return backend.StateExited, nil // SUSPENDING / SUSPENDED (husk preserved)
	}
}

// Signal gracefully stops the driver: over the managed proxy it POSTs the shim's
// /gritz/stop endpoint (SIGTERM → grace → SIGKILL). The driver catches SIGTERM
// and owns its terminal report to the server; its exit then drives the suspend like
// any other completion (Wait). It reports signalled=true if a running VM was
// reached, and does NOT terminate or suspend — that is Destroy's / Wait's job.
func (b *Backend) Signal(ctx context.Context, h backend.Handle) (bool, error) {
	hd, ok := decodeData(h.Data)
	if !ok || hd.Endpoint == "" {
		// No endpoint to reach: the driver already exited and the VM is
		// suspended/gone, so nothing is running to signal.
		return false, nil
	}
	token, err := b.mintToken(ctx, h.ID)
	if err != nil {
		if awsmicrovm.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("mint auth token: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := awsmicrovm.NewProxyRequest(reqCtx, hd.Endpoint, token, http.MethodPost, stopPath, nil)
	if err != nil {
		return false, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		// The VM could not be reached (suspended / network): the driver is not
		// running, so nothing was signalled.
		b.log.Warn("signal: stop request failed", "microvm", h.ID, "err", err)
		return false, nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 == 2 {
		return true, nil
	}
	return false, fmt.Errorf("stop returned status %d", resp.StatusCode)
}

// Destroy is the only terminate path, reached via Prune on task archive/delete:
// terminate the MicroVM (idempotent — a 404 means it is already gone) and delete
// the staged S3 object. A non-404 terminate failure is returned so Prune retries
// rather than leaking a billed VM.
func (b *Backend) Destroy(ctx context.Context, h backend.Handle) error {
	b.log.Info("terminating microvm", "microvm", h.ID)
	if _, err := b.cloud.TerminateMicrovm(ctx, &awsmicrovm.TerminateMicrovmInput{MicrovmID: h.ID}); err != nil && !awsmicrovm.IsNotFound(err) {
		return fmt.Errorf("terminate microvm: %w", err)
	}
	if hd, ok := decodeData(h.Data); ok {
		if err := b.stager.Remove(ctx, hd.StageBucket, hd.StageKey); err != nil {
			b.log.Warn("remove staged bundle", "microvm", h.ID, "err", err)
		}
	}
	return nil
}

// mintToken mints a short-lived proxy auth token scoped to the shim's port.
func (b *Backend) mintToken(ctx context.Context, microvmID string) (string, error) {
	out, err := b.cloud.CreateMicrovmAuthToken(ctx, &awsmicrovm.CreateMicrovmAuthTokenInput{
		MicrovmID:         microvmID,
		ExpirationMinutes: tokenExpirationMinutes,
		AllowedPorts:      []awsmicrovm.AllowedPort{{Port: shimPort}},
	})
	if err != nil {
		return "", err
	}
	return out.Token, nil
}

func decodeData(raw []byte) (handleData, bool) {
	if len(raw) == 0 {
		return handleData{}, false
	}
	var hd handleData
	if err := json.Unmarshal(raw, &hd); err != nil {
		return handleData{}, false
	}
	return hd, true
}

// Wait blocks until the handle's MicroVM reaches a terminal outcome, returning
// exactly once. It mints a proxy token and reads the /gritz/lifecycle SSE
// stream over the managed proxy: a clean driver-exited{code} event suspends the
// VM (preserve state, stop compute) and returns (code, nil), so the orchestrator
// sees an exited sandbox Docker-identically. A stream drop is arbitrated via
// GetMicrovm — a RUNNING/PENDING VM reconnects (a sticky driver-exited covers a
// gap-exit), a non-running/gone VM returns (ExitLost, nil). ctx cancellation
// (runner shutdown) returns ctx.Err() without suspending, so the VM persists for
// next-boot rehydration. Wait tolerates attaching to a PENDING (not-yet-ready)
// VM and re-attaching to a VM this process did not start.
func (b *Backend) Wait(ctx context.Context, h backend.Handle) (backend.ExitCode, error) {
	id := h.ID
	var endpoint string
	if hd, ok := decodeData(h.Data); ok {
		endpoint = hd.Endpoint
	}

	backoff := 500 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		token, err := b.mintToken(ctx, id)
		if err != nil {
			if awsmicrovm.IsNotFound(err) {
				return backend.ExitLost, nil
			}
			b.log.Warn("wait: mint token", "microvm", id, "err", err)
			if code, done := b.arbitrate(ctx, id, &endpoint); done {
				return code, nil
			}
			if !sleep(ctx, &backoff) {
				return 0, ctx.Err()
			}
			continue
		}

		exited, code := b.readStream(ctx, id, endpoint, token)
		if exited {
			// Clean completion: suspend (preserve state, stop compute) and return
			// the true exit code.
			if _, err := b.cloud.SuspendMicrovm(ctx, &awsmicrovm.SuspendMicrovmInput{MicrovmID: id}); err != nil {
				b.log.Warn("wait: suspend after driver exit", "microvm", id, "err", err)
			}
			return backend.ExitCode(code), nil
		}

		// Bare drop: the control plane is the liveness authority.
		if code, done := b.arbitrate(ctx, id, &endpoint); done {
			return code, nil
		}
		backoff = 500 * time.Millisecond // a confirmed-running reconnect resets backoff
		if !sleep(ctx, &backoff) {
			return 0, ctx.Err()
		}
	}
}

// readStream opens the SSE stream and reads until a driver-exited event (returns
// exited=true with the code) or the stream ends/errors (returns exited=false).
func (b *Backend) readStream(ctx context.Context, id, endpoint, token string) (bool, int) {
	req, err := awsmicrovm.NewProxyRequest(ctx, endpoint, token, http.MethodGet, lifecyclePath, nil)
	if err != nil {
		b.log.Warn("wait: build proxy request", "microvm", id, "err", err)
		return false, 0
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := b.http.Do(req)
	if err != nil {
		return false, 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return false, 0
	}
	r := sse.NewReader(resp.Body)
	for {
		ev, err := r.Read()
		if err != nil {
			return false, 0
		}
		if ev.Event == "" && len(ev.Data) == 0 {
			return false, 0 // stream ended
		}
		if ev.Event == EventDriverExited {
			var de DriverExited
			if err := json.Unmarshal(ev.Data, &de); err != nil {
				b.log.Warn("wait: decode driver-exited", "microvm", id, "err", err)
				return true, -1
			}
			return true, de.Code
		}
		// Ignore keep-alives and unknown event types.
	}
}

// arbitrate consults the control plane after a stream drop or token failure. It
// returns done=true with the report-lost outcome (ExitLost) when the VM is
// non-running or gone, and done=false when the VM is still RUNNING/PENDING (the
// caller reconnects; a sticky driver-exited covers an exit during the gap). A
// transient control-plane error is treated as "keep trying" (done=false).
func (b *Backend) arbitrate(ctx context.Context, id string, endpoint *string) (backend.ExitCode, bool) {
	out, err := b.cloud.GetMicrovm(ctx, &awsmicrovm.GetMicrovmInput{MicrovmID: id})
	if awsmicrovm.IsNotFound(err) {
		return backend.ExitLost, true
	}
	if err != nil {
		b.log.Warn("wait: arbitrate get microvm", "microvm", id, "err", err)
		return 0, false
	}
	switch out.Microvm.State {
	case awsmicrovm.MicrovmStateRunning, awsmicrovm.MicrovmStatePending:
		if out.Microvm.Endpoint != "" {
			*endpoint = out.Microvm.Endpoint
		}
		return 0, false
	default:
		return backend.ExitLost, true
	}
}

// sleep waits for the backoff to elapse (or ctx to cancel) and doubles it,
// capped. It returns false if ctx was cancelled.
func sleep(ctx context.Context, backoff *time.Duration) bool {
	t := time.NewTimer(*backoff)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
	}
	*backoff *= 2
	if *backoff > 30*time.Second {
		*backoff = 30 * time.Second
	}
	return true
}
