package lambdamicrovm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambdamicrovms"
	"github.com/aws/aws-sdk-go-v2/service/lambdamicrovms/types"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/gritzapp/gritz/internal/gritzclient"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/driverserver"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/awsmicrovm"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

const testTaskID = 1

// sleepForever is a config whose dummy agent runs until it is stopped.
var sleepForever = &agent.Config{Type: agent.TypeDummy, Dummy: &agent.DummyOptions{Sleep: -1}}

// testEnv is a backend whose VMs all reach the same in-process driver server,
// served over TLS and HTTP/2 like the managed proxy, which rejects requests
// without a proxy token.
type testEnv struct {
	be    *Backend
	cloud *fakeCloud
	srv   *driverserver.Server

	mu sync.Mutex
	// auth records the proxy token header of every request the driver got,
	// and bearer its Authorization header.
	auth   []string
	bearer []string
}

type testOptions struct {
	// Down serves 503 instead of the driver server, as the proxy does for a
	// VM whose driver is not listening.
	Down bool
}

func setup(t *testing.T, cfg *agent.Config, opts testOptions) *testEnv {
	t.Helper()
	te := &testEnv{}
	dir := t.TempDir()
	store := agent.ConfigStore(filepath.Join(dir, "config"))
	assert.NilError(t, store.Save(testTaskID, cfg))
	gritz := &gritzclient.ClientMock{
		GetTaskFunc: func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
			return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Id: req.Id, Version: 1}}, nil
		},
		SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
			return &gritzv1.SubmitRunnerEventsResponse{}, nil
		},
		ListEventsByTaskFunc: func(_ context.Context, _ *gritzv1.ListEventsByTaskRequest) (*gritzv1.ListEventsByTaskResponse, error) {
			return &gritzv1.ListEventsByTaskResponse{NextPageToken: "tail"}, nil
		},
		ListLinksFunc: func(_ context.Context, _ *gritzv1.ListLinksRequest) (*gritzv1.ListLinksResponse, error) {
			return &gritzv1.ListLinksResponse{}, nil
		},
		AppendLogChunkFunc: func(_ context.Context, _ *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	srv, err := driverserver.New(driverserver.Options{
		RecordPath: filepath.Join(dir, "run.json"),
		LogPath:    filepath.Join(dir, "log"),
		Config:     store,
		FileRoot:   filepath.Join(dir, "root"),
		NewClient:  func(_, _ string) gritzclient.Client { return gritz },
		KeepAlive:  20 * time.Millisecond,
	})
	assert.NilError(t, err)
	te.srv = srv
	mux := http.NewServeMux()
	mux.Handle(srv.Handler())
	hs := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get(awsmicrovm.ProxyAuthHeader)
		te.mu.Lock()
		te.auth = append(te.auth, token)
		te.bearer = append(te.bearer, r.Header.Get("Authorization"))
		te.mu.Unlock()
		switch {
		case token == "":
			http.Error(w, "missing proxy token", http.StatusUnauthorized)
		case opts.Down:
			http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
		default:
			mux.ServeHTTP(w, r)
		}
	}))
	hs.EnableHTTP2 = true
	hs.StartTLS()
	t.Cleanup(hs.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		assert.NilError(t, srv.Shutdown(ctx))
	})

	te.cloud = newFakeCloud(hs.URL)
	te.be, err = New(Options{Cloud: te.cloud, Transport: hs.Client().Transport, Region: "us-east-1"})
	assert.NilError(t, err)
	te.be.readyTimeout = 5 * time.Second
	te.be.idleTimeout = time.Second
	te.be.lostTimeout = 5 * time.Second
	te.be.stateTimeout = 5 * time.Second
	te.be.stateInterval = time.Millisecond
	return te
}

// tokens returns the distinct proxy tokens the driver was reached with.
func (te *testEnv) tokens() []string {
	te.mu.Lock()
	defer te.mu.Unlock()
	seen := map[string]bool{}
	var tokens []string
	for _, tok := range te.auth {
		if !seen[tok] {
			seen[tok] = true
			tokens = append(tokens, tok)
		}
	}
	return tokens
}

func testWorkspace() *workspace.Workspace {
	return &workspace.Workspace{
		LambdaMicroVM: &workspace.LambdaMicroVM{
			ImageIdentifier: "arn:aws:lambda:us-east-1:123:microvm-image/x",
			ExecutionRole:   "arn:aws:iam::123:role/x",
			EgressConnector: "arn:aws:lambda:us-east-1:aws:network-connector:aws-network-connector:INTERNET_EGRESS",
		},
	}
}

func testSpec(version int64) *backend.Spec {
	return &backend.Spec{
		TaskID:    testTaskID,
		Version:   version,
		Workspace: testWorkspace(),
		ServerURL: "http://gritz.test",
		Token:     "tok",
	}
}

// handle returns a handle this backend could have produced for the VM.
func handle(id string, version int64, endpoint string) backend.Handle {
	data, _ := json.Marshal(handleData{Endpoint: endpoint, Version: version})
	return backend.Handle{Type: HandleType, ID: id, Data: data}
}

func TestLaunch_Fresh(t *testing.T) {
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})

	h, err := te.be.Launch(t.Context(), testSpec(1), nil)
	assert.NilError(t, err)
	code, err := te.be.Wait(t.Context(), h)

	assert.NilError(t, err)
	assert.Equal(t, code, backend.ExitCode(0))
	assert.Equal(t, h.Type, HandleType)
	assert.Equal(t, h.ID, "mvm-1")
	d, err := decodeHandle(h)
	assert.NilError(t, err)
	assert.DeepEqual(t, d, handleData{Endpoint: te.cloud.endpoint, Version: 1})
	// The VM is run with no run-hook payload and the default ingress
	// connector, and suspended once its run has finished.
	// Each launch carries its own client token, so a retry cannot run a
	// second VM.
	assert.Assert(t, aws.ToString(te.cloud.lastRun.ClientToken) != "")
	assert.DeepEqual(t, te.cloud.lastRun, &lambdamicrovms.RunMicrovmInput{
		ImageIdentifier:          aws.String("arn:aws:lambda:us-east-1:123:microvm-image/x"),
		ExecutionRoleArn:         aws.String("arn:aws:iam::123:role/x"),
		EgressNetworkConnectors:  []string{"arn:aws:lambda:us-east-1:aws:network-connector:aws-network-connector:INTERNET_EGRESS"},
		IngressNetworkConnectors: []string{"arn:aws:lambda:us-east-1:aws:network-connector:aws-network-connector:ALL_INGRESS"},
		MaximumDurationInSeconds: aws.Int32(defaultMaxDuration),
	}, cmpopts.IgnoreUnexported(lambdamicrovms.RunMicrovmInput{}), cmpopts.IgnoreFields(lambdamicrovms.RunMicrovmInput{}, "ClientToken"))
	suspended, resumed, terminated := te.cloud.calls()
	assert.DeepEqual(t, suspended, []string{"mvm-1"})
	assert.Assert(t, cmp.Len(resumed, 0))
	assert.Assert(t, cmp.Len(terminated, 0))
	assert.Equal(t, te.cloud.state("mvm-1"), types.MicrovmStateSuspended)
}

// Every request reaches the driver with a proxy token scoped to the driver
// port, and a client mints one token rather than one per request.
func TestLaunch_ProxyToken(t *testing.T) {
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})

	_, err := te.be.Launch(t.Context(), testSpec(1), nil)

	assert.NilError(t, err)
	assert.DeepEqual(t, te.tokens(), []string{"token-mvm-1"})
	assert.DeepEqual(t, te.cloud.mints(), []*lambdamicrovms.CreateMicrovmAuthTokenInput{{
		MicrovmIdentifier:   aws.String("mvm-1"),
		ExpirationInMinutes: aws.Int32(60),
		AllowedPorts:        []types.PortSpecification{&types.PortSpecificationMemberPort{Value: DriverPort}},
	}}, cmpopts.IgnoreUnexported(lambdamicrovms.CreateMicrovmAuthTokenInput{}, types.PortSpecificationMemberPort{}))
}

// A suspended VM is resumed and the next run started on it.
func TestLaunch_Resume(t *testing.T) {
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})
	te.cloud.add("mvm-9", types.MicrovmStateSuspended, "")
	reuse := handle("mvm-9", 1, te.cloud.endpoint)

	h, err := te.be.Launch(t.Context(), testSpec(2), &reuse)
	assert.NilError(t, err)
	code, err := te.be.Wait(t.Context(), h)

	assert.NilError(t, err)
	assert.Equal(t, code, backend.ExitCode(0))
	assert.Equal(t, h.ID, "mvm-9")
	d, err := decodeHandle(h)
	assert.NilError(t, err)
	assert.Equal(t, d.Version, int64(2))
	suspended, resumed, _ := te.cloud.calls()
	assert.DeepEqual(t, resumed, []string{"mvm-9"})
	assert.DeepEqual(t, suspended, []string{"mvm-9"})
	assert.Equal(t, te.cloud.state("mvm-9"), types.MicrovmStateSuspended)
}

func TestLaunch_ReuseStates(t *testing.T) {
	tests := []struct {
		name    string
		state   types.MicrovmState // "" for a VM that is not found
		reason  string
		foreign bool
		err     error
		errMsg  string
	}{
		{name: "suspending", state: types.MicrovmStateSuspending, errMsg: "still suspending"},
		{name: "terminating", state: types.MicrovmStateTerminating, err: backend.ErrGone},
		{name: "terminated", state: types.MicrovmStateTerminated, reason: "maximum duration reached", err: backend.ErrGone, errMsg: "TERMINATED (maximum duration reached)"},
		{name: "not found", err: backend.ErrGone},
		{name: "foreign", state: types.MicrovmStateSuspended, foreign: true, err: backend.ErrGone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloud := newFakeCloud("vm.example.com")
			be, err := New(Options{Cloud: cloud})
			assert.NilError(t, err)
			if tt.state != "" {
				cloud.add("mvm-9", tt.state, tt.reason)
			}
			reuse := handle("mvm-9", 1, "vm.example.com")
			if tt.foreign {
				reuse.Type = "experimental-lambda-microvm"
			}

			_, err = be.Launch(t.Context(), testSpec(2), &reuse)

			if tt.err != nil {
				assert.ErrorIs(t, err, tt.err)
			} else {
				assert.Assert(t, !errors.Is(err, backend.ErrGone))
			}
			if tt.errMsg != "" {
				assert.ErrorContains(t, err, tt.errMsg)
			}
			// Nothing is resumed, run, suspended or terminated.
			suspended, resumed, terminated := cloud.calls()
			assert.Assert(t, cmp.Len(suspended, 0))
			assert.Assert(t, cmp.Len(resumed, 0))
			assert.Assert(t, cmp.Len(terminated, 0))
			assert.Assert(t, cloud.lastRun == nil)
		})
	}
}

// A fresh Launch that fails after running its VM terminates it: the runner
// gets no handle, so nothing else would.
func TestLaunch_FreshFailureTerminates(t *testing.T) {
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{Down: true})
	te.be.readyTimeout = 100 * time.Millisecond

	_, err := te.be.Launch(t.Context(), testSpec(1), nil)

	assert.ErrorContains(t, err, "did not become ready")
	suspended, _, terminated := te.cloud.calls()
	assert.DeepEqual(t, terminated, []string{"mvm-1"})
	assert.Assert(t, cmp.Len(suspended, 0))
	assert.Equal(t, te.cloud.state("mvm-1"), types.MicrovmStateTerminated)
}

// A reuse Launch that fails after resuming the VM suspends it but keeps it:
// Probe would otherwise report it running with no run on it.
func TestLaunch_ResumeFailureSuspends(t *testing.T) {
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{Down: true})
	te.be.readyTimeout = 100 * time.Millisecond
	te.cloud.add("mvm-9", types.MicrovmStateSuspended, "")
	reuse := handle("mvm-9", 1, te.cloud.endpoint)

	_, err := te.be.Launch(t.Context(), testSpec(2), &reuse)

	assert.ErrorContains(t, err, "did not become ready")
	suspended, resumed, terminated := te.cloud.calls()
	assert.DeepEqual(t, resumed, []string{"mvm-9"})
	assert.DeepEqual(t, suspended, []string{"mvm-9"})
	assert.Assert(t, cmp.Len(terminated, 0))
	st, err := te.be.Probe(t.Context(), reuse)
	assert.NilError(t, err)
	assert.Equal(t, st, backend.StateExited)
}

func TestProbe(t *testing.T) {
	tests := []struct {
		state   types.MicrovmState // "" for a VM that is not found
		foreign bool
		want    backend.State
	}{
		{state: types.MicrovmStatePending, want: backend.StateRunning},
		{state: types.MicrovmStateRunning, want: backend.StateRunning},
		// A VM still suspending after its run is not launched on (#1684).
		{state: types.MicrovmStateSuspending, want: backend.StateRunning},
		{state: types.MicrovmStateSuspended, want: backend.StateExited},
		{state: types.MicrovmStateTerminating, want: backend.StateGone},
		{state: types.MicrovmStateTerminated, want: backend.StateGone},
		{state: "", want: backend.StateGone},
		{state: types.MicrovmStateRunning, foreign: true, want: backend.StateGone},
	}
	for _, tt := range tests {
		name := string(tt.state)
		if tt.foreign {
			name += " foreign"
		}
		t.Run(name, func(t *testing.T) {
			cloud := newFakeCloud("vm.example.com")
			be, err := New(Options{Cloud: cloud})
			assert.NilError(t, err)
			if tt.state != "" {
				cloud.add("mvm-9", tt.state, "")
			}
			h := handle("mvm-9", 1, "vm.example.com")
			if tt.foreign {
				h.Type = "experimental-lambda-microvm"
			}

			st, err := be.Probe(t.Context(), h)

			assert.NilError(t, err)
			assert.Equal(t, st, tt.want)
		})
	}
}

// Signal stops the run through the driver; Wait then gets its Finished and
// suspends the VM.
func TestSignal(t *testing.T) {
	te := setup(t, sleepForever, testOptions{})
	h, err := te.be.Launch(t.Context(), testSpec(1), nil)
	assert.NilError(t, err)

	stopped, err := te.be.Signal(t.Context(), h)
	assert.NilError(t, err)
	code, err := te.be.Wait(t.Context(), h)

	assert.NilError(t, err)
	assert.Assert(t, stopped)
	assert.Equal(t, code, backend.ExitCode(0))
	assert.Equal(t, te.cloud.state("mvm-1"), types.MicrovmStateSuspended)
}

// A VM that is not running has no run to stop, and is not dialed.
func TestSignal_NotRunning(t *testing.T) {
	te := setup(t, sleepForever, testOptions{})
	te.cloud.add("mvm-9", types.MicrovmStateSuspended, "")

	stopped, err := te.be.Signal(t.Context(), handle("mvm-9", 1, te.cloud.endpoint))

	assert.NilError(t, err)
	assert.Assert(t, !stopped)
	assert.Assert(t, cmp.Len(te.cloud.mints(), 0))
}

// A VM that is suspended before its run's Finished arrived is reported lost.
func TestWait_SuspendedBeforeFinished(t *testing.T) {
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{Down: true})
	te.cloud.add("mvm-9", types.MicrovmStateSuspending, "")

	code, err := te.be.Wait(t.Context(), handle("mvm-9", 1, te.cloud.endpoint))

	assert.NilError(t, err)
	assert.Equal(t, code, backend.ExitLost)
	// Wait lets the suspend complete rather than leave Probe reporting the VM
	// running.
	assert.Equal(t, te.cloud.state("mvm-9"), types.MicrovmStateSuspended)
}

// A VM that is terminated while its run is going is reported lost.
func TestWait_Gone(t *testing.T) {
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{Down: true})
	te.cloud.add("mvm-9", types.MicrovmStateTerminated, "")

	code, err := te.be.Wait(t.Context(), handle("mvm-9", 1, te.cloud.endpoint))

	assert.NilError(t, err)
	assert.Equal(t, code, backend.ExitLost)
}

// A Wait whose driver never answers gives up after lostTimeout: it suspends
// the VM and reports the run lost rather than retrying forever.
func TestWait_DriverUnreachableGivesUp(t *testing.T) {
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{Down: true})
	te.be.lostTimeout = 500 * time.Millisecond
	te.cloud.add("mvm-9", types.MicrovmStateRunning, "")

	code, err := te.be.Wait(t.Context(), handle("mvm-9", 1, te.cloud.endpoint))

	assert.NilError(t, err)
	assert.Equal(t, code, backend.ExitLost)
	assert.Equal(t, te.cloud.state("mvm-9"), types.MicrovmStateSuspended)
}

// A Wait whose driver has no such run reports it lost and suspends the VM.
func TestWait_NoSuchRun(t *testing.T) {
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})
	te.cloud.add("mvm-9", types.MicrovmStateRunning, "")

	code, err := te.be.Wait(t.Context(), handle("mvm-9", 1, te.cloud.endpoint))

	assert.NilError(t, err)
	assert.Equal(t, code, backend.ExitLost)
	assert.Equal(t, te.cloud.state("mvm-9"), types.MicrovmStateSuspended)
}

// A Wait for a stale version reports it lost but leaves the VM running: a
// newer run is active on it, which a suspend would kill.
func TestWait_StaleVersion(t *testing.T) {
	te := setup(t, sleepForever, testOptions{})
	h, err := te.be.Launch(t.Context(), testSpec(2), nil)
	assert.NilError(t, err)

	code, err := te.be.Wait(t.Context(), handle(h.ID, 1, te.cloud.endpoint))

	assert.NilError(t, err)
	assert.Equal(t, code, backend.ExitLost)
	suspended, _, _ := te.cloud.calls()
	assert.Assert(t, cmp.Len(suspended, 0))
	assert.Equal(t, te.cloud.state(h.ID), types.MicrovmStateRunning)
}

func TestWait_Foreign(t *testing.T) {
	cloud := newFakeCloud("vm.example.com")
	be, err := New(Options{Cloud: cloud})
	assert.NilError(t, err)

	code, err := be.Wait(t.Context(), backend.Handle{Type: "experimental-lambda-microvm", ID: "mvm-9"})

	assert.NilError(t, err)
	assert.Equal(t, code, backend.ExitLost)
}

func TestDestroy(t *testing.T) {
	cloud := newFakeCloud("vm.example.com")
	be, err := New(Options{Cloud: cloud})
	assert.NilError(t, err)
	cloud.add("mvm-9", types.MicrovmStateSuspended, "")

	assert.NilError(t, be.Destroy(t.Context(), handle("mvm-9", 1, "vm.example.com")))
	// A missing VM and a foreign handle are not errors.
	assert.NilError(t, be.Destroy(t.Context(), handle("mvm-404", 1, "vm.example.com")))
	assert.NilError(t, be.Destroy(t.Context(), backend.Handle{Type: "experimental-lambda-microvm", ID: "mvm-7"}))

	_, _, terminated := cloud.calls()
	assert.DeepEqual(t, terminated, []string{"mvm-9", "mvm-404"})
	assert.Equal(t, cloud.state("mvm-9"), types.MicrovmStateTerminated)
}

// A cached token is reused until shortly before it expires, then re-minted.
func TestTokenTransport_Refresh(t *testing.T) {
	var minted int
	tt := &tokenTransport{
		base: http.DefaultTransport,
		mint: func(context.Context) (string, error) {
			minted++
			return "token", nil
		},
	}

	_, err := tt.get(t.Context())
	assert.NilError(t, err)
	_, err = tt.get(t.Context())
	assert.NilError(t, err)
	assert.Equal(t, minted, 1)

	tt.expires = time.Now().Add(tokenRefreshMargin - time.Second)
	_, err = tt.get(t.Context())
	assert.NilError(t, err)
	assert.Equal(t, minted, 2)
}

func TestValidateWorkspace(t *testing.T) {
	be, err := New(Options{Cloud: newFakeCloud(""), Region: "us-east-1"})
	assert.NilError(t, err)
	assert.NilError(t, be.ValidateWorkspace(testWorkspace()))

	tests := map[string]func(*workspace.LambdaMicroVM){
		"missing image":    func(c *workspace.LambdaMicroVM) { c.ImageIdentifier = "" },
		"missing role":     func(c *workspace.LambdaMicroVM) { c.ExecutionRole = "" },
		"missing egress":   func(c *workspace.LambdaMicroVM) { c.EgressConnector = "" },
		"duration too big": func(c *workspace.LambdaMicroVM) { c.MaxDurationSeconds = 99999 },
		"other region":     func(c *workspace.LambdaMicroVM) { c.Region = "eu-west-1" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			ws := testWorkspace()
			mutate(ws.LambdaMicroVM)
			assert.Assert(t, be.ValidateWorkspace(ws) != nil)
		})
	}
}

func TestNewRunSpec(t *testing.T) {
	ws := testWorkspace()
	ws.LambdaMicroVM.Environment = map[string]string{"FOO": "workspace", "BAR": "bar"}
	spec := &backend.Spec{
		TaskID:    7,
		Version:   3,
		Workspace: ws,
		ServerURL: "http://server",
		Token:     "tok",
		Env: map[string]string{
			"FOO":          "run",
			"GRITZ_TOKEN":  "tok",
			"GRITZ_SERVER": "http://server",
		},
		Secrets: map[string]string{"GH_TOKEN": "ghp_secret"},
		Files:   []backend.File{{Path: "/tmp/gritz/7.json", Data: []byte(`{}`), Mode: 0666}},
	}

	rs := newRunSpec(spec)

	// The workspace environment is layered under the run's.
	assert.DeepEqual(t, rs, &driverv1.RunSpec{
		TaskId:    7,
		ServerUrl: "http://server",
		Token:     "tok",
		Env: map[string]string{
			"FOO":          "run",
			"BAR":          "bar",
			"GRITZ_TOKEN":  "tok",
			"GRITZ_SERVER": "http://server",
		},
		Secrets: map[string]string{"GH_TOKEN": "ghp_secret"},
		Files:   []*driverv1.File{{Path: "/tmp/gritz/7.json", Data: []byte(`{}`), Mode: 0666}},
	}, protocmp.Transform())
}
