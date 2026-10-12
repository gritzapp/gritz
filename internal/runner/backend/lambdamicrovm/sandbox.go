package lambdamicrovm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambdamicrovms"
	"github.com/aws/aws-sdk-go-v2/service/lambdamicrovms/types"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/driverclient"
	"github.com/gritzapp/gritz/internal/runner/workspace"
)

var (
	_ backend.Backend  = (*Backend)(nil)
	_ backend.RunEnver = (*Backend)(nil)
)

// RunEnv returns the workspace's lambda_microvm environment: a VM is run with
// no environment of its own, so it reaches the run through its RunSpec.
func (b *Backend) RunEnv(ws *workspace.Workspace) map[string]string {
	if cfg := ws.LambdaMicroVM; cfg != nil {
		return cfg.Environment
	}
	return nil
}

// sandboxData is the backend-defined part of a Sandbox handle.
type sandboxData struct {
	Endpoint string `json:"endpoint"`
}

// Create runs a VM for spec and returns as soon as RunMicrovm does, with the VM
// still PENDING. The handle is the VM's id and the endpoint RunMicrovm
// returned.
func (b *Backend) Create(ctx context.Context, spec *backend.Spec) (backend.Sandbox, error) {
	if err := b.ValidateWorkspace(spec.Workspace); err != nil {
		return nil, err
	}
	out, err := b.runVM(ctx, spec)
	if err != nil {
		return nil, err
	}
	id := aws.ToString(out.MicrovmId)
	endpoint := aws.ToString(out.Endpoint)
	data, _ := json.Marshal(sandboxData{Endpoint: endpoint}) // a string field always marshals
	return b.newSandbox(backend.Handle{Type: HandleType, ID: id, Data: data}, endpoint), nil
}

// Open returns the sandbox for a handle this backend produced. It does no I/O:
// the proxy token is minted on the first call through a client Dial returns.
// A handle recorded by an older runner, which also carries its run's
// version, opens the same VM.
func (b *Backend) Open(h backend.Handle) (backend.Sandbox, error) {
	if h.Type != HandleType {
		return nil, fmt.Errorf("handle type %q is not %q", h.Type, HandleType)
	}
	if h.ID == "" {
		return nil, errors.New("handle has no microvm id")
	}
	var d sandboxData
	if len(h.Data) > 0 {
		if err := json.Unmarshal(h.Data, &d); err != nil {
			return nil, fmt.Errorf("failed to decode handle data: %w", err)
		}
	}
	return b.newSandbox(h, d.Endpoint), nil
}

func (b *Backend) newSandbox(h backend.Handle, endpoint string) *sandbox {
	return &sandbox{
		b:        b,
		handle:   h,
		tokens:   b.newTokenTransport(h.ID),
		endpoint: endpoint,
	}
}

// sandbox is one task VM. Its proxy token is minted once and shared by every
// client Dial returns; tokenTransport synchronizes it. Its endpoint starts as
// the handle's and follows the one the control plane reports, under mu.
type sandbox struct {
	b      *Backend
	handle backend.Handle
	tokens *tokenTransport

	mu       sync.Mutex
	endpoint string
}

func (s *sandbox) Handle() backend.Handle {
	return s.handle
}

func (s *sandbox) id() string {
	return s.handle.ID
}

// observe records the endpoint the control plane reported for the VM, if it
// reported one.
func (s *sandbox) observe(vm *lambdamicrovms.GetMicrovmOutput) {
	if vm == nil || aws.ToString(vm.Endpoint) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endpoint = aws.ToString(vm.Endpoint)
}

// Start waits for a PENDING VM to be RUNNING, or resumes a SUSPENDED one and
// waits. A RUNNING VM is left as it is. A SUSPENDING VM is a plain error, and a
// terminating, terminated or missing one backend.ErrGone.
func (s *sandbox) Start(ctx context.Context) error {
	vm, err := s.b.boot(ctx, s.id())
	if err != nil {
		return err
	}
	s.observe(vm)
	return nil
}

// Dial returns a client for the VM's driver server through the managed proxy,
// over HTTP/2. Every request carries the sandbox's cached proxy token and jwt.
// It fails only if no endpoint is known for the VM yet.
func (s *sandbox) Dial(_ context.Context, jwt string) (driverv1connect.DriverServiceClient, error) {
	s.mu.Lock()
	endpoint := s.endpoint
	s.mu.Unlock()
	if endpoint == "" {
		return nil, fmt.Errorf("microvm %s has no endpoint", s.id())
	}
	httpClient := &http.Client{Transport: &bearerTransport{base: s.tokens, token: jwt}}
	return driverclient.New(httpClient, driverURL(endpoint), driverclient.Options{IdleTimeout: s.b.idleTimeout}), nil
}

// Status reports a RUNNING VM as up; a PENDING (created, not yet booted),
// SUSPENDING or SUSPENDED one as parked; and a terminating, terminated or
// missing one as gone.
func (s *sandbox) Status(ctx context.Context) (backend.Status, error) {
	vm, err := s.b.get(ctx, s.id())
	if err != nil {
		return backend.StatusUnknown, err
	}
	if vm == nil {
		return backend.StatusGone, nil
	}
	s.observe(vm)
	switch state := vm.State; {
	case state == types.MicrovmStateRunning:
		return backend.StatusUp, nil
	case state == types.MicrovmStatePending, state == types.MicrovmStateSuspending, state == types.MicrovmStateSuspended:
		return backend.StatusParked, nil
	case terminal(state):
		return backend.StatusGone, nil
	default:
		return backend.StatusUnknown, fmt.Errorf("microvm %s is in unknown state %s", s.id(), describe(vm))
	}
}

// Park suspends the VM and polls until it is SUSPENDED. A SUSPENDED VM is left
// as it is, a SUSPENDING one is waited for, and a PENDING one is waited for to
// be RUNNING before it is suspended. A terminating, terminated or missing VM is
// backend.ErrGone.
func (s *sandbox) Park(ctx context.Context) error {
	id := s.id()
	vm, err := s.b.get(ctx, id)
	if err != nil {
		return err
	}
	if vm == nil {
		return backend.ErrGone
	}
	switch state := vm.State; {
	case terminal(state):
		return fmt.Errorf("microvm %s is %s: %w", id, describe(vm), backend.ErrGone)
	case state == types.MicrovmStateSuspended:
		return nil
	case state == types.MicrovmStateSuspending:
		_, err := s.b.await(ctx, id, types.MicrovmStateSuspended)
		return err
	case state == types.MicrovmStatePending:
		if _, err := s.b.await(ctx, id, types.MicrovmStateRunning); err != nil {
			return err
		}
	}
	s.b.log.Info("suspending microvm", "microvm", id)
	_, err = s.b.cloud.SuspendMicrovm(ctx, &lambdamicrovms.SuspendMicrovmInput{MicrovmIdentifier: aws.String(id)})
	if isNotFound(err) {
		return backend.ErrGone
	}
	if err != nil {
		return fmt.Errorf("failed to suspend microvm: %w", err)
	}
	_, err = s.b.await(ctx, id, types.MicrovmStateSuspended)
	return err
}

// Destroy terminates the VM. A VM that is missing, or already terminating or
// terminated, is not an error.
func (s *sandbox) Destroy(ctx context.Context) error {
	id := s.id()
	s.b.log.Info("terminating microvm", "microvm", id)
	_, err := s.b.cloud.TerminateMicrovm(ctx, &lambdamicrovms.TerminateMicrovmInput{MicrovmIdentifier: aws.String(id)})
	if err == nil || isNotFound(err) {
		return nil
	}
	// The control plane may refuse to terminate a VM that is already going.
	if vm, getErr := s.b.get(ctx, id); getErr == nil && (vm == nil || terminal(vm.State)) {
		return nil
	}
	return fmt.Errorf("failed to terminate microvm: %w", err)
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
