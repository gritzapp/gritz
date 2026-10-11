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

// fakeCloud is an in-memory Cloud for tests. All lifecycle verbs are recorded so
// tests can assert the runner (never the guest) drives suspend/resume/terminate.
type fakeCloud struct {
	mu      sync.Mutex
	next    int
	vms     map[string]*lambdamicrovms.GetMicrovmOutput
	lastRun *lambdamicrovms.RunMicrovmInput

	runErr error

	suspended []string // ids passed to SuspendMicrovm, in order
	resumed   []string // ids passed to ResumeMicrovm, in order
	tokens    []*lambdamicrovms.CreateMicrovmAuthTokenInput
}

func newFakeCloud() *fakeCloud {
	return &fakeCloud{vms: map[string]*lambdamicrovms.GetMicrovmOutput{}}
}

func notFound(id string) error {
	return &types.ResourceNotFoundException{Message: aws.String("microvm " + id + " not found")}
}

func (f *fakeCloud) RunMicrovm(_ context.Context, in *lambdamicrovms.RunMicrovmInput, _ ...func(*lambdamicrovms.Options)) (*lambdamicrovms.RunMicrovmOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runErr != nil {
		return nil, f.runErr
	}
	f.lastRun = in
	f.next++
	id := fmt.Sprintf("mvm-%d", f.next)
	endpoint := id + ".example.com"
	f.vms[id] = &lambdamicrovms.GetMicrovmOutput{MicrovmId: aws.String(id), State: types.MicrovmStateRunning, Endpoint: aws.String(endpoint)}
	return &lambdamicrovms.RunMicrovmOutput{MicrovmId: aws.String(id), State: types.MicrovmStatePending, Endpoint: aws.String(endpoint)}, nil
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
	return &out, nil
}

func (f *fakeCloud) TerminateMicrovm(_ context.Context, in *lambdamicrovms.TerminateMicrovmInput, _ ...func(*lambdamicrovms.Options)) (*lambdamicrovms.TerminateMicrovmOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if vm, ok := f.vms[aws.ToString(in.MicrovmIdentifier)]; ok {
		vm.State = types.MicrovmStateTerminated
	}
	return &lambdamicrovms.TerminateMicrovmOutput{}, nil
}

func (f *fakeCloud) SuspendMicrovm(_ context.Context, in *lambdamicrovms.SuspendMicrovmInput, _ ...func(*lambdamicrovms.Options)) (*lambdamicrovms.SuspendMicrovmOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := aws.ToString(in.MicrovmIdentifier)
	f.suspended = append(f.suspended, id)
	if vm, ok := f.vms[id]; ok {
		vm.State = types.MicrovmStateSuspended
	}
	return &lambdamicrovms.SuspendMicrovmOutput{}, nil
}

func (f *fakeCloud) ResumeMicrovm(_ context.Context, in *lambdamicrovms.ResumeMicrovmInput, _ ...func(*lambdamicrovms.Options)) (*lambdamicrovms.ResumeMicrovmOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := aws.ToString(in.MicrovmIdentifier)
	f.resumed = append(f.resumed, id)
	if vm, ok := f.vms[id]; ok {
		vm.State = types.MicrovmStateRunning
	}
	return &lambdamicrovms.ResumeMicrovmOutput{}, nil
}

func (f *fakeCloud) CreateMicrovmAuthToken(_ context.Context, in *lambdamicrovms.CreateMicrovmAuthTokenInput, _ ...func(*lambdamicrovms.Options)) (*lambdamicrovms.CreateMicrovmAuthTokenOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, in)
	return &lambdamicrovms.CreateMicrovmAuthTokenOutput{
		AuthToken: map[string]string{awsmicrovm.ProxyAuthHeader: "token-" + aws.ToString(in.MicrovmIdentifier)},
	}, nil
}

// add registers a VM with explicit id/state/endpoint.
func (f *fakeCloud) add(id string, state types.MicrovmState, endpoint string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vms[id] = &lambdamicrovms.GetMicrovmOutput{
		MicrovmId: aws.String(id),
		State:     state,
		Endpoint:  aws.String(endpoint),
	}
}

// setState moves a VM to state s, with the service's reason for it.
func (f *fakeCloud) setState(id string, s types.MicrovmState, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if vm, ok := f.vms[id]; ok {
		vm.State = s
		vm.StateReason = aws.String(reason)
	}
}

func (f *fakeCloud) state(id string) types.MicrovmState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if vm, ok := f.vms[id]; ok {
		return vm.State
	}
	return ""
}

func (f *fakeCloud) suspendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.suspended)
}

// fakeStager is an in-memory Stager.
type fakeStager struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeStager() *fakeStager { return &fakeStager{objects: map[string][]byte{}} }

func (f *fakeStager) Stage(_ context.Context, bucket, key string, data []byte, _ int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = data
	return "https://" + bucket + ".staging.example.com/" + key, nil
}

func (f *fakeStager) Remove(_ context.Context, _, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	return nil
}

func (f *fakeStager) get(key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.objects[key]
	return d, ok
}
