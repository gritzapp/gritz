package lambdamicrovm

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/lambdamicrovms/types"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// newTestBackend returns a backend on a fake cloud with no driver behind it,
// polling the control plane without pause.
func newTestBackend(t *testing.T) (*Backend, *fakeCloud) {
	t.Helper()
	cloud := newFakeCloud("vm.example.com")
	be, err := New(Options{Cloud: cloud, Region: "us-east-1"})
	assert.NilError(t, err)
	be.stateTimeout = 5 * time.Second
	be.stateInterval = time.Millisecond
	return be, cloud
}

// sandboxHandle returns a handle Create could have produced for the VM.
func sandboxHandle(id, endpoint string) backend.Handle {
	data, _ := json.Marshal(sandboxData{Endpoint: endpoint})
	return backend.Handle{Type: HandleType, ID: id, Data: data}
}

func open(t *testing.T, be *Backend, h backend.Handle) backend.SandboxV2 {
	t.Helper()
	sb, err := be.Open(h)
	assert.NilError(t, err)
	return sb
}

// A sandbox goes through its whole life: created, started, dialed, parked,
// resumed and destroyed, after which it is gone.
func TestSandbox_Lifecycle(t *testing.T) {
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})

	sb, err := te.be.Create(t.Context(), testSpec(1))
	assert.NilError(t, err)

	// Create returns with the VM still PENDING, which is parked.
	assert.DeepEqual(t, sb.Handle(), sandboxHandle("mvm-1", te.cloud.endpoint))
	assert.Equal(t, te.cloud.state("mvm-1"), types.MicrovmStatePending)
	assertStatus(t, sb, backend.StatusParked)

	assert.NilError(t, sb.Start(t.Context()))
	assert.Equal(t, te.cloud.state("mvm-1"), types.MicrovmStateRunning)
	assertStatus(t, sb, backend.StatusUp)
	// Starting a running VM is a no-op.
	assert.NilError(t, sb.Start(t.Context()))

	client, err := sb.Dial(t.Context(), "jwt")
	assert.NilError(t, err)
	_, err = client.Status(t.Context(), &driverv1.StatusRequest{})
	assert.NilError(t, err)

	assert.NilError(t, sb.Park(t.Context()))
	assert.Equal(t, te.cloud.state("mvm-1"), types.MicrovmStateSuspended)
	assertStatus(t, sb, backend.StatusParked)
	// Parking a suspended VM is a no-op.
	assert.NilError(t, sb.Park(t.Context()))

	assert.NilError(t, sb.Start(t.Context()))
	assert.Equal(t, te.cloud.state("mvm-1"), types.MicrovmStateRunning)

	assert.NilError(t, sb.Destroy(t.Context()))
	assertStatus(t, sb, backend.StatusGone)
	assert.ErrorIs(t, sb.Start(t.Context()), backend.ErrGone)
	assert.ErrorIs(t, sb.Park(t.Context()), backend.ErrGone)
	assert.NilError(t, sb.Destroy(t.Context()))

	suspended, resumed, terminated := te.cloud.calls()
	assert.DeepEqual(t, suspended, []string{"mvm-1"})
	assert.DeepEqual(t, resumed, []string{"mvm-1"})
	assert.DeepEqual(t, terminated, []string{"mvm-1", "mvm-1"})
}

func assertStatus(t *testing.T, sb backend.SandboxV2, want backend.Status) {
	t.Helper()
	st, err := sb.Status(t.Context())
	assert.NilError(t, err)
	assert.Equal(t, st, want)
}

func TestCreate_InvalidWorkspace(t *testing.T) {
	be, cloud := newTestBackend(t)
	spec := testSpec(1)
	spec.Workspace.LambdaMicroVM.ImageIdentifier = ""

	_, err := be.Create(t.Context(), spec)

	assert.ErrorContains(t, err, "image_identifier is required")
	assert.Assert(t, cloud.lastRun == nil)
}

func TestOpen(t *testing.T) {
	be, _ := newTestBackend(t)

	// Open does not check that the VM exists.
	h := sandboxHandle("mvm-404", "vm.example.com")
	assert.DeepEqual(t, open(t, be, h).Handle(), h)
	// A handle Launch produced opens the same VM.
	assert.Equal(t, open(t, be, handle("mvm-9", 3, "vm.example.com")).Handle().ID, "mvm-9")

	tests := map[string]backend.Handle{
		"foreign":   {Type: "docker", ID: "mvm-9"},
		"no id":     {Type: HandleType},
		"malformed": {Type: HandleType, ID: "mvm-9", Data: []byte(`{`)},
	}
	for name, h := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := be.Open(h)
			assert.Assert(t, err != nil)
		})
	}
}

func TestSandbox_Status(t *testing.T) {
	tests := []struct {
		state types.MicrovmState // "" for a VM that is not found
		want  backend.Status
	}{
		// Created but not yet booted.
		{state: types.MicrovmStatePending, want: backend.StatusParked},
		{state: types.MicrovmStateRunning, want: backend.StatusUp},
		{state: types.MicrovmStateSuspending, want: backend.StatusParked},
		{state: types.MicrovmStateSuspended, want: backend.StatusParked},
		{state: types.MicrovmStateTerminating, want: backend.StatusGone},
		{state: types.MicrovmStateTerminated, want: backend.StatusGone},
		{state: "", want: backend.StatusGone},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			be, cloud := newTestBackend(t)
			if tt.state != "" {
				cloud.add("mvm-9", tt.state, "")
			}
			sb := open(t, be, sandboxHandle("mvm-9", "vm.example.com"))

			assertStatus(t, sb, tt.want)
		})
	}
}

func TestSandbox_Start(t *testing.T) {
	tests := []struct {
		state   types.MicrovmState // "" for a VM that is not found
		resumed bool
		final   types.MicrovmState
		err     error
		errMsg  string
	}{
		{state: types.MicrovmStatePending, final: types.MicrovmStateRunning},
		{state: types.MicrovmStateRunning, final: types.MicrovmStateRunning},
		{state: types.MicrovmStateSuspended, resumed: true, final: types.MicrovmStateRunning},
		// Start's GetMicrovm settles the fake's suspend; it is not resumed.
		{state: types.MicrovmStateSuspending, final: types.MicrovmStateSuspended, errMsg: "still suspending"},
		{state: types.MicrovmStateTerminating, final: types.MicrovmStateTerminating, err: backend.ErrGone},
		{state: types.MicrovmStateTerminated, final: types.MicrovmStateTerminated, err: backend.ErrGone},
		{state: "", err: backend.ErrGone},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			be, cloud := newTestBackend(t)
			if tt.state != "" {
				cloud.add("mvm-9", tt.state, "")
			}
			sb := open(t, be, sandboxHandle("mvm-9", "vm.example.com"))

			err := sb.Start(t.Context())

			switch {
			case tt.err != nil:
				assert.ErrorIs(t, err, tt.err)
			case tt.errMsg != "":
				// A transitional state is a plain error, not gone.
				assert.ErrorContains(t, err, tt.errMsg)
				assert.Assert(t, !errors.Is(err, backend.ErrGone))
			default:
				assert.NilError(t, err)
			}
			assert.Equal(t, cloud.state("mvm-9"), tt.final)
			suspended, resumed, terminated := cloud.calls()
			if tt.resumed {
				assert.DeepEqual(t, resumed, []string{"mvm-9"})
			} else {
				assert.Assert(t, cmp.Len(resumed, 0))
			}
			assert.Assert(t, cmp.Len(suspended, 0))
			assert.Assert(t, cmp.Len(terminated, 0))
		})
	}
}

func TestSandbox_Park(t *testing.T) {
	tests := []struct {
		state     types.MicrovmState // "" for a VM that is not found
		suspended bool
		final     types.MicrovmState
		err       error
	}{
		{state: types.MicrovmStateRunning, suspended: true, final: types.MicrovmStateSuspended},
		// A VM that has not booted yet is suspended once it has.
		{state: types.MicrovmStatePending, suspended: true, final: types.MicrovmStateSuspended},
		// A suspend in progress is waited for, not repeated.
		{state: types.MicrovmStateSuspending, final: types.MicrovmStateSuspended},
		{state: types.MicrovmStateSuspended, final: types.MicrovmStateSuspended},
		{state: types.MicrovmStateTerminating, final: types.MicrovmStateTerminating, err: backend.ErrGone},
		{state: types.MicrovmStateTerminated, final: types.MicrovmStateTerminated, err: backend.ErrGone},
		{state: "", err: backend.ErrGone},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			be, cloud := newTestBackend(t)
			if tt.state != "" {
				cloud.add("mvm-9", tt.state, "")
			}
			sb := open(t, be, sandboxHandle("mvm-9", "vm.example.com"))

			err := sb.Park(t.Context())

			if tt.err != nil {
				assert.ErrorIs(t, err, tt.err)
			} else {
				assert.NilError(t, err)
			}
			assert.Equal(t, cloud.state("mvm-9"), tt.final)
			suspended, resumed, terminated := cloud.calls()
			if tt.suspended {
				assert.DeepEqual(t, suspended, []string{"mvm-9"})
			} else {
				assert.Assert(t, cmp.Len(suspended, 0))
			}
			assert.Assert(t, cmp.Len(resumed, 0))
			assert.Assert(t, cmp.Len(terminated, 0))
		})
	}
}

// Park polls until the VM is SUSPENDED, not just until the suspend is
// accepted.
func TestSandbox_ParkPollsUntilSuspended(t *testing.T) {
	be, cloud := newTestBackend(t)
	cloud.add("mvm-9", types.MicrovmStateRunning, "")
	sb := open(t, be, sandboxHandle("mvm-9", "vm.example.com"))

	assert.NilError(t, sb.Park(t.Context()))

	// SuspendMicrovm leaves the fake VM SUSPENDING; only a later GetMicrovm
	// settles it.
	assert.Equal(t, cloud.state("mvm-9"), types.MicrovmStateSuspended)
}

func TestSandbox_Destroy(t *testing.T) {
	be, cloud := newTestBackend(t)
	cloud.add("mvm-9", types.MicrovmStateSuspended, "")
	cloud.add("mvm-8", types.MicrovmStateTerminated, "")

	assert.NilError(t, open(t, be, sandboxHandle("mvm-9", "vm.example.com")).Destroy(t.Context()))
	// A missing or terminated VM is not an error.
	assert.NilError(t, open(t, be, sandboxHandle("mvm-404", "vm.example.com")).Destroy(t.Context()))
	assert.NilError(t, open(t, be, sandboxHandle("mvm-8", "vm.example.com")).Destroy(t.Context()))

	_, _, terminated := cloud.calls()
	assert.DeepEqual(t, terminated, []string{"mvm-9", "mvm-404", "mvm-8"})
	assert.Equal(t, cloud.state("mvm-9"), types.MicrovmStateTerminated)
}

// Every request a dialed client makes carries the JWT and a proxy token scoped
// to the driver port, which the sandbox mints once for all its clients.
func TestSandbox_Dial(t *testing.T) {
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})
	te.cloud.add("mvm-9", types.MicrovmStateRunning, "")
	sb := open(t, te.be, sandboxHandle("mvm-9", te.cloud.endpoint))

	for _, jwt := range []string{"jwt-1", "jwt-2"} {
		client, err := sb.Dial(t.Context(), jwt)
		assert.NilError(t, err)
		_, err = client.Status(t.Context(), &driverv1.StatusRequest{})
		assert.NilError(t, err)
	}

	assert.DeepEqual(t, te.tokens(), []string{"token-mvm-9"})
	assert.Assert(t, cmp.Len(te.cloud.mints(), 1))
	te.mu.Lock()
	defer te.mu.Unlock()
	assert.DeepEqual(t, te.bearer, []string{"Bearer jwt-1", "Bearer jwt-2"})
}

// A handle without an endpoint cannot be dialed until the control plane has
// reported one.
func TestSandbox_DialLearnsEndpoint(t *testing.T) {
	be, cloud := newTestBackend(t)
	cloud.add("mvm-9", types.MicrovmStateRunning, "")
	sb := open(t, be, backend.Handle{Type: HandleType, ID: "mvm-9"})

	_, err := sb.Dial(t.Context(), "jwt")
	assert.ErrorContains(t, err, "no endpoint")

	assertStatus(t, sb, backend.StatusUp)
	_, err = sb.Dial(t.Context(), "jwt")
	assert.NilError(t, err)
	// The handle is fixed, whatever the control plane reports.
	assert.DeepEqual(t, sb.Handle(), backend.Handle{Type: HandleType, ID: "mvm-9"})
}

// A sandbox's methods are safe for concurrent use, as the runner's supervisor,
// Kill and idle timer make them; run with -race.
func TestSandbox_Concurrent(t *testing.T) {
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})
	sb, err := te.be.Create(t.Context(), testSpec(1))
	assert.NilError(t, err)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			assert.Check(t, sb.Start(t.Context()))
			_, err := sb.Status(t.Context())
			assert.Check(t, err)
			client, err := sb.Dial(t.Context(), "jwt")
			if !assert.Check(t, err) {
				return
			}
			_, err = client.Status(t.Context(), &driverv1.StatusRequest{})
			assert.Check(t, err)
		})
	}
	wg.Wait()

	assert.Assert(t, cmp.Len(te.cloud.mints(), 1))
}
