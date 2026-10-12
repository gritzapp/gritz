// Package lambdamicrovm implements the runner backend on AWS Lambda MicroVMs
// with the driver server (`gritz driver --aws-lambda-hooks`) as each
// VM's main process, starting each run over the driver.v1 protocol through
// AWS's managed proxy. It is the Lambda counterpart of the docker backend: the
// driver still reports to the gritz server itself, and the VM is suspended
// after every run.
//
// It shares no code with the docker package on purpose. Agent files arrive in
// the RunSpec, so nothing is staged in S3. See README.md for building the VM
// image.
package lambdamicrovm

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambdamicrovms"
	"github.com/aws/aws-sdk-go-v2/service/lambdamicrovms/types"
	"github.com/google/uuid"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/driverclient"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/awsmicrovm"
)

// HandleType is the backend.Handle.Type this backend stamps on the handles it
// produces.
const HandleType = "lambda-microvm"

// DriverPort is the port `gritz driver` listens on inside the VM, and
// the only port the proxy tokens this backend mints may reach.
const DriverPort = 8080

// allIngressConnector is the Lambda-managed connector granting inbound
// connectivity, which the managed proxy needs. It is the default when a
// workspace does not pin a port-scoped connector; %s is the region.
const allIngressConnector = "arn:aws:lambda:%s:aws:network-connector:aws-network-connector:ALL_INGRESS"

// defaultMaxDuration is used when a workspace does not set
// max_duration_seconds. Lambda caps the value at 28800 (8h).
const defaultMaxDuration int32 = 14400

const (
	// tokenLifetime is the lifetime of a minted proxy token, and
	// tokenRefreshMargin how long before it expires a client mints another.
	tokenLifetime      = time.Hour
	tokenRefreshMargin = 5 * time.Minute
	// stateTimeout bounds how long the backend polls a VM to reach RUNNING or
	// SUSPENDED, and statePollInterval is the pause between polls.
	stateTimeout      = 5 * time.Minute
	statePollInterval = time.Second
)

// Cloud is the subset of the AWS Lambda MicroVMs control plane the backend
// uses. *lambdamicrovms.Client implements it; tests use a fake.
type Cloud interface {
	RunMicrovm(ctx context.Context, in *lambdamicrovms.RunMicrovmInput, optFns ...func(*lambdamicrovms.Options)) (*lambdamicrovms.RunMicrovmOutput, error)
	GetMicrovm(ctx context.Context, in *lambdamicrovms.GetMicrovmInput, optFns ...func(*lambdamicrovms.Options)) (*lambdamicrovms.GetMicrovmOutput, error)
	TerminateMicrovm(ctx context.Context, in *lambdamicrovms.TerminateMicrovmInput, optFns ...func(*lambdamicrovms.Options)) (*lambdamicrovms.TerminateMicrovmOutput, error)
	SuspendMicrovm(ctx context.Context, in *lambdamicrovms.SuspendMicrovmInput, optFns ...func(*lambdamicrovms.Options)) (*lambdamicrovms.SuspendMicrovmOutput, error)
	ResumeMicrovm(ctx context.Context, in *lambdamicrovms.ResumeMicrovmInput, optFns ...func(*lambdamicrovms.Options)) (*lambdamicrovms.ResumeMicrovmOutput, error)
	CreateMicrovmAuthToken(ctx context.Context, in *lambdamicrovms.CreateMicrovmAuthTokenInput, optFns ...func(*lambdamicrovms.Options)) (*lambdamicrovms.CreateMicrovmAuthTokenOutput, error)
}

// Backend runs task sandboxes as AWS Lambda MicroVMs whose main process is the
// driver server.
//
// A handle's ID is the MicroVM id, and its Data carries the VM's proxy
// endpoint.
type Backend struct {
	cloud     Cloud
	transport http.RoundTripper
	region    string
	log       *slog.Logger
	// The timeouts and intervals below are shortened by tests.
	idleTimeout   time.Duration
	stateTimeout  time.Duration
	stateInterval time.Duration
}

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
		return nil, errors.New("lambdamicrovm: Cloud is required")
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
		idleTimeout:   driverclient.DefaultIdleTimeout,
		stateTimeout:  stateTimeout,
		stateInterval: statePollInterval,
	}, nil
}

func (b *Backend) Close() error { return nil }

// ValidateWorkspace requires the lambda_microvm config a VM is launched with.
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

// runVM calls RunMicrovm for spec and returns as soon as the call does, with
// the VM still PENDING.
func (b *Backend) runVM(ctx context.Context, spec *backend.Spec) (*lambdamicrovms.RunMicrovmOutput, error) {
	cfg := spec.Workspace.LambdaMicroVM
	b.log.Info("running microvm", "task", spec.TaskID, "image", cfg.ImageIdentifier)
	out, err := b.cloud.RunMicrovm(ctx, &lambdamicrovms.RunMicrovmInput{
		// One token per launch, so a retried request cannot run a second VM.
		ClientToken:              aws.String(uuid.NewString()),
		ImageIdentifier:          aws.String(cfg.ImageIdentifier),
		ExecutionRoleArn:         aws.String(cfg.ExecutionRole),
		EgressNetworkConnectors:  []string{cfg.EgressConnector},
		IngressNetworkConnectors: []string{cmp.Or(cfg.IngressConnector, fmt.Sprintf(allIngressConnector, b.region))},
		MaximumDurationInSeconds: aws.Int32(cmp.Or(int32(cfg.MaxDurationSeconds), defaultMaxDuration)),
		// No idle policy: the VM is suspended explicitly.
	})
	if err != nil {
		return nil, fmt.Errorf("failed to run microvm: %w", err)
	}
	return out, nil
}

// boot brings the VM to RUNNING and returns it: a suspended VM is resumed, and
// a pending or running one is awaited as it is. A VM that is terminating,
// terminated or not found is backend.ErrGone, and one that is still suspending
// is a plain error.
func (b *Backend) boot(ctx context.Context, id string) (*lambdamicrovms.GetMicrovmOutput, error) {
	out, err := b.cloud.GetMicrovm(ctx, &lambdamicrovms.GetMicrovmInput{MicrovmIdentifier: aws.String(id)})
	if isNotFound(err) {
		return nil, backend.ErrGone
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get microvm: %w", err)
	}
	switch state := out.State; {
	case terminal(state):
		return nil, fmt.Errorf("microvm %s is %s: %w", id, describe(out), backend.ErrGone)
	case state == types.MicrovmStateSuspending:
		return nil, fmt.Errorf("microvm %s is still suspending: %s", id, describe(out))
	case state == types.MicrovmStateRunning:
		return out, nil
	case state == types.MicrovmStateSuspended:
		b.log.Info("resuming microvm", "microvm", id)
		if _, err := b.cloud.ResumeMicrovm(ctx, &lambdamicrovms.ResumeMicrovmInput{MicrovmIdentifier: aws.String(id)}); err != nil {
			if isNotFound(err) {
				return nil, backend.ErrGone
			}
			return nil, fmt.Errorf("failed to resume microvm: %w", err)
		}
	default:
		b.log.Info("waiting for microvm", "microvm", id, "state", state)
	}
	return b.await(ctx, id, types.MicrovmStateRunning)
}

// await polls the VM until it is in state want and returns it. A VM that is
// terminating, terminated or not found is backend.ErrGone; one that is not in
// want within the backend's stateTimeout is an error. Both carry the state the
// VM was last seen in, with the service's reason for it.
func (b *Backend) await(ctx context.Context, id string, want types.MicrovmState) (*lambdamicrovms.GetMicrovmOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, b.stateTimeout)
	defer cancel()
	last := "unknown"
	for {
		out, err := b.cloud.GetMicrovm(ctx, &lambdamicrovms.GetMicrovmInput{MicrovmIdentifier: aws.String(id)})
		switch {
		case isNotFound(err):
			return nil, backend.ErrGone
		case err != nil:
			b.log.Warn("failed to get microvm", "microvm", id, "err", err)
		case out.State == want:
			return out, nil
		case terminal(out.State):
			return nil, fmt.Errorf("microvm %s is %s: %w", id, describe(out), backend.ErrGone)
		default:
			last = describe(out)
		}
		if err := sleep(ctx, b.stateInterval); err != nil {
			return nil, fmt.Errorf("microvm %s did not become %s (last %s): %w", id, want, last, err)
		}
	}
}

// driverURL returns the base URL of the driver server behind a VM's proxy
// endpoint, which the control plane may report without a scheme.
func driverURL(endpoint string) string {
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	return strings.TrimRight(endpoint, "/")
}

// newTokenTransport returns a transport that adds a proxy token for the VM,
// scoped to DriverPort, to every request.
func (b *Backend) newTokenTransport(id string) *tokenTransport {
	return &tokenTransport{
		base: b.transport,
		mint: func(ctx context.Context) (string, error) {
			out, err := b.cloud.CreateMicrovmAuthToken(ctx, &lambdamicrovms.CreateMicrovmAuthTokenInput{
				MicrovmIdentifier:   aws.String(id),
				ExpirationInMinutes: aws.Int32(int32(tokenLifetime / time.Minute)),
				AllowedPorts:        []types.PortSpecification{&types.PortSpecificationMemberPort{Value: DriverPort}},
			})
			if err != nil {
				return "", fmt.Errorf("failed to mint proxy token: %w", err)
			}
			return out.AuthToken[awsmicrovm.ProxyAuthHeader], nil
		},
	}
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

// get returns the VM, or nil if it is not found.
func (b *Backend) get(ctx context.Context, id string) (*lambdamicrovms.GetMicrovmOutput, error) {
	out, err := b.cloud.GetMicrovm(ctx, &lambdamicrovms.GetMicrovmInput{MicrovmIdentifier: aws.String(id)})
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get microvm: %w", err)
	}
	return out, nil
}

// terminal reports whether a VM in state is being torn down or is gone, and
// will not run again.
func terminal(state types.MicrovmState) bool {
	return state == types.MicrovmStateTerminating || state == types.MicrovmStateTerminated
}

// describe renders vm's state with the service's reason for it, if any. A nil
// vm is one that is not found.
func describe(vm *lambdamicrovms.GetMicrovmOutput) string {
	if vm == nil {
		return "not found"
	}
	if reason := aws.ToString(vm.StateReason); reason != "" {
		return fmt.Sprintf("%s (%s)", vm.State, reason)
	}
	return string(vm.State)
}

// isNotFound reports whether err is the control plane's not-found error.
func isNotFound(err error) bool {
	var nf *types.ResourceNotFoundException
	return errors.As(err, &nf)
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
