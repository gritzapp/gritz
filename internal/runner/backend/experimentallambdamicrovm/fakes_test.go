package experimentallambdamicrovm

import (
	"context"
	"fmt"
	"sync"

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
	vms      map[string]*awsmicrovm.Microvm
	lastRun  *awsmicrovm.RunMicrovmInput

	suspendErr error // if set, SuspendMicrovm returns it

	suspended  []string // ids passed to SuspendMicrovm, in order
	resumed    []string // ids passed to ResumeMicrovm, in order
	terminated []string // ids passed to TerminateMicrovm, in order
	tokens     []*awsmicrovm.CreateMicrovmAuthTokenInput
}

func newFakeCloud(endpoint string) *fakeCloud {
	return &fakeCloud{endpoint: endpoint, vms: map[string]*awsmicrovm.Microvm{}}
}

func notFound(op string) error { return &awsmicrovm.APIError{Op: op, StatusCode: 404} }

func (f *fakeCloud) RunMicrovm(_ context.Context, in *awsmicrovm.RunMicrovmInput) (*awsmicrovm.RunMicrovmOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastRun = in
	f.next++
	id := fmt.Sprintf("mvm-%d", f.next)
	f.vms[id] = &awsmicrovm.Microvm{MicrovmID: id, State: awsmicrovm.MicrovmStatePending, Endpoint: f.endpoint}
	return &awsmicrovm.RunMicrovmOutput{MicrovmID: id, Endpoint: f.endpoint}, nil
}

func (f *fakeCloud) GetMicrovm(_ context.Context, in *awsmicrovm.GetMicrovmInput) (*awsmicrovm.GetMicrovmOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	vm, ok := f.vms[in.MicrovmID]
	if !ok {
		return nil, notFound("GetMicrovm")
	}
	out := &awsmicrovm.GetMicrovmOutput{Microvm: *vm}
	switch vm.State {
	case awsmicrovm.MicrovmStatePending:
		vm.State = awsmicrovm.MicrovmStateRunning
	case awsmicrovm.MicrovmStateSuspending:
		vm.State = awsmicrovm.MicrovmStateSuspended
	}
	return out, nil
}

func (f *fakeCloud) TerminateMicrovm(_ context.Context, in *awsmicrovm.TerminateMicrovmInput) (*awsmicrovm.TerminateMicrovmOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminated = append(f.terminated, in.MicrovmID)
	vm, ok := f.vms[in.MicrovmID]
	if !ok {
		return nil, notFound("TerminateMicrovm")
	}
	vm.State = awsmicrovm.MicrovmStateTerminated
	return &awsmicrovm.TerminateMicrovmOutput{}, nil
}

func (f *fakeCloud) SuspendMicrovm(_ context.Context, in *awsmicrovm.SuspendMicrovmInput) (*awsmicrovm.SuspendMicrovmOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.suspended = append(f.suspended, in.MicrovmID)
	if f.suspendErr != nil {
		return nil, f.suspendErr
	}
	vm, ok := f.vms[in.MicrovmID]
	if !ok {
		return nil, notFound("SuspendMicrovm")
	}
	vm.State = awsmicrovm.MicrovmStateSuspending
	return &awsmicrovm.SuspendMicrovmOutput{}, nil
}

func (f *fakeCloud) ResumeMicrovm(_ context.Context, in *awsmicrovm.ResumeMicrovmInput) (*awsmicrovm.ResumeMicrovmOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumed = append(f.resumed, in.MicrovmID)
	vm, ok := f.vms[in.MicrovmID]
	if !ok {
		return nil, notFound("ResumeMicrovm")
	}
	vm.State = awsmicrovm.MicrovmStatePending
	return &awsmicrovm.ResumeMicrovmOutput{}, nil
}

func (f *fakeCloud) CreateMicrovmAuthToken(_ context.Context, in *awsmicrovm.CreateMicrovmAuthTokenInput) (*awsmicrovm.CreateMicrovmAuthTokenOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, in)
	if _, ok := f.vms[in.MicrovmID]; !ok {
		return nil, notFound("CreateMicrovmAuthToken")
	}
	return &awsmicrovm.CreateMicrovmAuthTokenOutput{Token: "token-" + in.MicrovmID}, nil
}

// add registers a VM with an explicit id and state.
func (f *fakeCloud) add(id string, state awsmicrovm.MicrovmState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vms[id] = &awsmicrovm.Microvm{MicrovmID: id, State: state, Endpoint: f.endpoint}
}

// state returns the VM's state without settling it.
func (f *fakeCloud) state(id string) awsmicrovm.MicrovmState {
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

func (f *fakeCloud) mints() []*awsmicrovm.CreateMicrovmAuthTokenInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*awsmicrovm.CreateMicrovmAuthTokenInput(nil), f.tokens...)
}
