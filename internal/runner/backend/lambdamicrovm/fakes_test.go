package lambdamicrovm

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambdamicrovms"
	"github.com/aws/aws-sdk-go-v2/service/lambdamicrovms/types"
	"github.com/gritzapp/gritz/internal/x/awsmicrovm"
)

// fakeCloud is an in-memory Cloud for tests. Every VM it runs gets the same
// endpoint, the test's in-process driver server. Transitional states settle
// one GetMicrovm later — PENDING into RUNNING, SUSPENDING into SUSPENDED — so
// the backend has to poll for them.
type fakeCloud struct {
	mu       sync.Mutex
	endpoint string
	next     int
	vms      map[string]*lambdamicrovms.GetMicrovmOutput
	lastRun  *lambdamicrovms.RunMicrovmInput

	suspendErr error // if set, SuspendMicrovm returns it

	suspended  []string // ids passed to SuspendMicrovm, in order
	resumed    []string // ids passed to ResumeMicrovm, in order
	terminated []string // ids passed to TerminateMicrovm, in order
	tokens     []*lambdamicrovms.CreateMicrovmAuthTokenInput
}

func newFakeCloud(endpoint string) *fakeCloud {
	return &fakeCloud{endpoint: endpoint, vms: map[string]*lambdamicrovms.GetMicrovmOutput{}}
}

func notFound(id string) error {
	return &types.ResourceNotFoundException{Message: aws.String("microvm " + id + " not found")}
}

func (f *fakeCloud) RunMicrovm(_ context.Context, in *lambdamicrovms.RunMicrovmInput, _ ...func(*lambdamicrovms.Options)) (*lambdamicrovms.RunMicrovmOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastRun = in
	f.next++
	id := fmt.Sprintf("mvm-%d", f.next)
	f.vms[id] = &lambdamicrovms.GetMicrovmOutput{MicrovmId: aws.String(id), State: types.MicrovmStatePending, Endpoint: aws.String(f.endpoint)}
	return &lambdamicrovms.RunMicrovmOutput{MicrovmId: aws.String(id), State: types.MicrovmStatePending, Endpoint: aws.String(f.endpoint)}, nil
}

func (f *fakeCloud) GetMicrovm(_ context.Context, in *lambdamicrovms.GetMicrovmInput, _ ...func(*lambdamicrovms.Options)) (*lambdamicrovms.GetMicrovmOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := aws.ToString(in.MicrovmIdentifier)
	vm, ok := f.vms[id]
	if !ok {
		return nil, notFound(id)
	}
	out := *vm
	switch vm.State {
	case types.MicrovmStatePending:
		vm.State = types.MicrovmStateRunning
	case types.MicrovmStateSuspending:
		vm.State = types.MicrovmStateSuspended
	}
	return &out, nil
}

func (f *fakeCloud) TerminateMicrovm(_ context.Context, in *lambdamicrovms.TerminateMicrovmInput, _ ...func(*lambdamicrovms.Options)) (*lambdamicrovms.TerminateMicrovmOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := aws.ToString(in.MicrovmIdentifier)
	f.terminated = append(f.terminated, id)
	vm, ok := f.vms[id]
	if !ok {
		return nil, notFound(id)
	}
	vm.State = types.MicrovmStateTerminated
	return &lambdamicrovms.TerminateMicrovmOutput{}, nil
}

func (f *fakeCloud) SuspendMicrovm(_ context.Context, in *lambdamicrovms.SuspendMicrovmInput, _ ...func(*lambdamicrovms.Options)) (*lambdamicrovms.SuspendMicrovmOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := aws.ToString(in.MicrovmIdentifier)
	f.suspended = append(f.suspended, id)
	if f.suspendErr != nil {
		return nil, f.suspendErr
	}
	vm, ok := f.vms[id]
	if !ok {
		return nil, notFound(id)
	}
	vm.State = types.MicrovmStateSuspending
	return &lambdamicrovms.SuspendMicrovmOutput{}, nil
}

func (f *fakeCloud) ResumeMicrovm(_ context.Context, in *lambdamicrovms.ResumeMicrovmInput, _ ...func(*lambdamicrovms.Options)) (*lambdamicrovms.ResumeMicrovmOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := aws.ToString(in.MicrovmIdentifier)
	f.resumed = append(f.resumed, id)
	vm, ok := f.vms[id]
	if !ok {
		return nil, notFound(id)
	}
	vm.State = types.MicrovmStatePending
	return &lambdamicrovms.ResumeMicrovmOutput{}, nil
}

func (f *fakeCloud) CreateMicrovmAuthToken(_ context.Context, in *lambdamicrovms.CreateMicrovmAuthTokenInput, _ ...func(*lambdamicrovms.Options)) (*lambdamicrovms.CreateMicrovmAuthTokenOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, in)
	id := aws.ToString(in.MicrovmIdentifier)
	if _, ok := f.vms[id]; !ok {
		return nil, notFound(id)
	}
	return &lambdamicrovms.CreateMicrovmAuthTokenOutput{
		AuthToken: map[string]string{awsmicrovm.ProxyAuthHeader: "token-" + id},
	}, nil
}

// add registers a VM with an explicit id and state, and the service's reason
// for that state, if any.
func (f *fakeCloud) add(id string, state types.MicrovmState, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	vm := &lambdamicrovms.GetMicrovmOutput{MicrovmId: aws.String(id), State: state, Endpoint: aws.String(f.endpoint)}
	if reason != "" {
		vm.StateReason = aws.String(reason)
	}
	f.vms[id] = vm
}

// state returns the VM's state without settling it.
func (f *fakeCloud) state(id string) types.MicrovmState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if vm, ok := f.vms[id]; ok {
		return vm.State
	}
	return ""
}

func (f *fakeCloud) calls() (suspended, resumed, terminated []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.suspended...), append([]string(nil), f.resumed...), append([]string(nil), f.terminated...)
}

func (f *fakeCloud) mints() []*lambdamicrovms.CreateMicrovmAuthTokenInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*lambdamicrovms.CreateMicrovmAuthTokenInput(nil), f.tokens...)
}
