package lambdamicrovm

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"

	"github.com/gritzapp/gritz/internal/x/awsmicrovm"
)

// fakeCloud is an in-memory Cloud for tests. All lifecycle verbs are recorded so
// tests can assert the runner (never the guest) drives suspend/resume/terminate.
type fakeCloud struct {
	mu      sync.Mutex
	next    int
	vms     map[string]*awsmicrovm.Microvm
	lastRun *awsmicrovm.RunMicrovmInput

	runErr   error
	listErr  error // if set, ListMicrovms returns it
	pageSize int   // >0 paginates ListMicrovms (NextToken is the start index)

	suspended []string // ids passed to SuspendMicrovm, in order
	resumed   []string // ids passed to ResumeMicrovm, in order
	tokens    []string // ids passed to CreateMicrovmAuthToken, in order
}

func newFakeCloud() *fakeCloud { return &fakeCloud{vms: map[string]*awsmicrovm.Microvm{}} }

func (f *fakeCloud) RunMicrovm(_ context.Context, in *awsmicrovm.RunMicrovmInput) (*awsmicrovm.RunMicrovmOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runErr != nil {
		return nil, f.runErr
	}
	f.lastRun = in
	f.next++
	id := fmt.Sprintf("mvm-%d", f.next)
	endpoint := id + ".example.com"
	// run-microvm does not tag the VM (the service has no tags input and a running
	// MicroVM is not taggable); ownership for the Watch tests is set via add().
	f.vms[id] = &awsmicrovm.Microvm{MicrovmID: id, State: awsmicrovm.MicrovmStateRunning, Endpoint: endpoint}
	return &awsmicrovm.RunMicrovmOutput{MicrovmID: id, Endpoint: endpoint}, nil
}

func (f *fakeCloud) GetMicrovm(_ context.Context, in *awsmicrovm.GetMicrovmInput) (*awsmicrovm.GetMicrovmOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	vm, ok := f.vms[in.MicrovmID]
	if !ok {
		return nil, &awsmicrovm.APIError{Op: "GetMicrovm", StatusCode: 404}
	}
	return &awsmicrovm.GetMicrovmOutput{Microvm: *vm}, nil
}

func (f *fakeCloud) TerminateMicrovm(_ context.Context, in *awsmicrovm.TerminateMicrovmInput) (*awsmicrovm.TerminateMicrovmOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if vm, ok := f.vms[in.MicrovmID]; ok {
		vm.State = awsmicrovm.MicrovmStateTerminated
	}
	return &awsmicrovm.TerminateMicrovmOutput{}, nil
}

func (f *fakeCloud) SuspendMicrovm(_ context.Context, in *awsmicrovm.SuspendMicrovmInput) (*awsmicrovm.SuspendMicrovmOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.suspended = append(f.suspended, in.MicrovmID)
	if vm, ok := f.vms[in.MicrovmID]; ok {
		vm.State = awsmicrovm.MicrovmStateSuspended
	}
	return &awsmicrovm.SuspendMicrovmOutput{}, nil
}

func (f *fakeCloud) ResumeMicrovm(_ context.Context, in *awsmicrovm.ResumeMicrovmInput) (*awsmicrovm.ResumeMicrovmOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumed = append(f.resumed, in.MicrovmID)
	if vm, ok := f.vms[in.MicrovmID]; ok {
		vm.State = awsmicrovm.MicrovmStateRunning
	}
	return &awsmicrovm.ResumeMicrovmOutput{}, nil
}

func (f *fakeCloud) CreateMicrovmAuthToken(_ context.Context, in *awsmicrovm.CreateMicrovmAuthTokenInput) (*awsmicrovm.CreateMicrovmAuthTokenOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, in.MicrovmID)
	return &awsmicrovm.CreateMicrovmAuthTokenOutput{Token: "token-" + in.MicrovmID}, nil
}

func (f *fakeCloud) ListMicrovms(_ context.Context, in *awsmicrovm.ListMicrovmsInput) (*awsmicrovm.ListMicrovmsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	// Stable order so NextToken (a start index) paginates deterministically.
	ids := make([]string, 0, len(f.vms))
	for id := range f.vms {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	start := 0
	if in != nil && in.NextToken != "" {
		start, _ = strconv.Atoi(in.NextToken)
	}
	size := f.pageSize
	if size <= 0 {
		size = len(ids)
	}
	end := start + size
	if end > len(ids) {
		end = len(ids)
	}
	out := &awsmicrovm.ListMicrovmsOutput{}
	for _, id := range ids[start:end] {
		out.Microvms = append(out.Microvms, *f.vms[id])
	}
	if end < len(ids) {
		out.NextToken = strconv.Itoa(end)
	}
	return out, nil
}

// add registers a VM with explicit id/state/endpoint.
func (f *fakeCloud) add(id string, state awsmicrovm.MicrovmState, endpoint string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vms[id] = &awsmicrovm.Microvm{
		MicrovmID: id,
		State:     state,
		Endpoint:  endpoint,
	}
}

func (f *fakeCloud) setState(id string, s awsmicrovm.MicrovmState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if vm, ok := f.vms[id]; ok {
		vm.State = s
	}
}

func (f *fakeCloud) state(id string) awsmicrovm.MicrovmState {
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
