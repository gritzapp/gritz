package lambdamicrovm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/awsmicrovm"
	"github.com/gritzapp/gritz/internal/x/sse"
	"gotest.tools/v3/assert"
)

func newTestBackend(t *testing.T) (*Backend, *fakeCloud, *fakeStager) {
	t.Helper()
	cloud := newFakeCloud()
	stager := newFakeStager()
	be, err := New(Options{
		Cloud:    cloud,
		Stager:   stager,
		RunnerID: "runner-1",
	})
	assert.NilError(t, err)
	return be, cloud, stager
}

func testWorkspace() *workspace.Workspace {
	return &workspace.Workspace{
		LambdaMicroVM: &workspace.LambdaMicroVM{
			ImageIdentifier: "arn:aws:lambda:us-east-1:123:microvm-image/x",
			ExecutionRole:   "arn:aws:iam::123:role/x",
			EgressConnector: "arn:aws:lambda:us-east-1:aws:network-connector:aws-network-connector:INTERNET_EGRESS",
			StagingBucket:   "bucket",
			Environment:     map[string]string{"FOO": "bar"},
		},
	}
}

func testSpec(taskID int64) *backend.Spec {
	return &backend.Spec{
		TaskID:    taskID,
		Workspace: testWorkspace(),
		Cmd:       []string{backend.BinaryPath, "driver", "--task", "1"},
		Env:       []string{"GRITZ_TASK_ID=1"},
		Files: []backend.File{
			{Path: "/tmp/gritz", Mode: 0777, Dir: true},
			{Path: "/tmp/gritz/1.json", Data: []byte(`{"type":"claude"}`), Mode: 0666},
		},
	}
}

func TestValidateWorkspace(t *testing.T) {
	be, _, _ := newTestBackend(t)
	assert.NilError(t, be.ValidateWorkspace(testWorkspace()))

	cases := map[string]func(*workspace.LambdaMicroVM){
		"missing image":    func(c *workspace.LambdaMicroVM) { c.ImageIdentifier = "" },
		"missing role":     func(c *workspace.LambdaMicroVM) { c.ExecutionRole = "" },
		"missing egress":   func(c *workspace.LambdaMicroVM) { c.EgressConnector = "" },
		"missing bucket":   func(c *workspace.LambdaMicroVM) { c.StagingBucket = "" },
		"duration too big": func(c *workspace.LambdaMicroVM) { c.MaxDurationSeconds = 99999 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			ws := testWorkspace()
			mutate(ws.LambdaMicroVM)
			assert.Assert(t, be.ValidateWorkspace(ws) != nil, "expected error for %s", name)
		})
	}

	t.Run("nil config", func(t *testing.T) {
		assert.Assert(t, be.ValidateWorkspace(&workspace.Workspace{}) != nil)
	})
}

func TestLaunchFresh(t *testing.T) {
	be, cloud, stager := newTestBackend(t)

	h, err := be.Launch(context.Background(), testSpec(7), nil)
	assert.NilError(t, err)

	// Handle: id is the microvmId, Data carries the endpoint + staging.
	assert.Equal(t, h.Type, HandleType)
	assert.Equal(t, h.ID, "mvm-1")
	hd, ok := decodeData(h.Data)
	assert.Assert(t, ok)
	assert.DeepEqual(t, hd, handleData{
		Endpoint:    "mvm-1.example.com",
		ImageARN:    "arn:aws:lambda:us-east-1:123:microvm-image/x",
		StageBucket: "bucket",
		StageKey:    "runner-1/7.json",
	})

	// run-microvm assembly: connectors, idle omitted, payload pointer, no tags.
	in := cloud.lastRun
	assert.Equal(t, in.ImageIdentifier, "arn:aws:lambda:us-east-1:123:microvm-image/x")
	assert.Equal(t, in.ExecutionRoleArn, "arn:aws:iam::123:role/x")
	assert.DeepEqual(t, in.EgressNetworkConnectors, []string{"arn:aws:lambda:us-east-1:aws:network-connector:aws-network-connector:INTERNET_EGRESS"})
	// An ingress connector is required to reach the shim over the managed proxy;
	// unset defaults to the managed ALL_INGRESS connector (bug 2 of #1088).
	assert.Assert(t, len(in.IngressNetworkConnectors) == 1 && strings.Contains(in.IngressNetworkConnectors[0], "ALL_INGRESS"))
	assert.Equal(t, in.MaximumDurationInSeconds, defaultMaxDuration)
	assert.Assert(t, in.IdlePolicy == nil) // omitted: the service cannot express "never auto-suspend"
	assert.Equal(t, in.RunHookPayload, "https://bucket.staging.example.com/runner-1/7.json")

	// Bundle round-trips cmd/env/files (env = workspace env + spec env).
	raw, ok := stager.get("runner-1/7.json")
	assert.Assert(t, ok)
	var b Bundle
	assert.NilError(t, json.Unmarshal(raw, &b))
	assert.DeepEqual(t, b.Cmd, testSpec(7).Cmd)
	assert.DeepEqual(t, b.Env, []string{"FOO=bar", "GRITZ_TASK_ID=1"})
	assert.Equal(t, len(b.Files), 2)
}

func TestLaunchFreshCustomIngressConnector(t *testing.T) {
	be, cloud, _ := newTestBackend(t)

	spec := testSpec(7)
	// An operator-supplied port-scoped ingress connector is passed through
	// verbatim rather than the ALL_INGRESS default.
	custom := "arn:aws:lambda:us-east-1:123:network-connector/port-8080"
	spec.Workspace.LambdaMicroVM.IngressConnector = custom

	_, err := be.Launch(context.Background(), spec, nil)
	assert.NilError(t, err)
	assert.DeepEqual(t, cloud.lastRun.IngressNetworkConnectors, []string{custom})
}

func TestLaunchReuseResumes(t *testing.T) {
	be, cloud, _ := newTestBackend(t)
	cloud.add("mvm-9", awsmicrovm.MicrovmStateSuspended, "mvm-9.example.com")
	reuse := &backend.Handle{Type: HandleType, ID: "mvm-9", Data: mustData(t, handleData{Endpoint: "mvm-9.example.com"})}

	h, err := be.Launch(context.Background(), testSpec(9), reuse)
	assert.NilError(t, err)

	// Resumed the existing VM; no new VM launched.
	assert.Equal(t, h.ID, "mvm-9")
	assert.DeepEqual(t, cloud.resumed, []string{"mvm-9"})
	assert.Assert(t, cloud.lastRun == nil, "should not RunMicrovm on reuse")
	assert.Equal(t, cloud.state("mvm-9"), awsmicrovm.MicrovmStateRunning)
}

func TestLaunchReuseGoneReturnsErrGone(t *testing.T) {
	be, cloud, _ := newTestBackend(t)
	cloud.add("mvm-dead", awsmicrovm.MicrovmStateTerminated, "")
	reuse := &backend.Handle{Type: HandleType, ID: "mvm-dead", Data: mustData(t, handleData{StageBucket: "bucket", StageKey: "runner-1/3.json"})}

	_, err := be.Launch(context.Background(), testSpec(3), reuse)

	// One sandbox per task: a terminated VM cannot resume, and Launch never
	// creates a fresh one on the reuse path — it surfaces ErrGone.
	assert.Assert(t, errors.Is(err, backend.ErrGone))
	assert.Equal(t, len(cloud.resumed), 0)
	assert.Assert(t, cloud.lastRun == nil, "should not launch fresh on a gone reuse handle")
}

func TestLaunchReuseNotFoundReturnsErrGone(t *testing.T) {
	be, cloud, _ := newTestBackend(t)
	// The reuse handle points at a VM the control plane no longer knows.
	reuse := &backend.Handle{Type: HandleType, ID: "mvm-missing", Data: mustData(t, handleData{StageBucket: "bucket", StageKey: "runner-1/3.json"})}

	_, err := be.Launch(context.Background(), testSpec(3), reuse)

	assert.Assert(t, errors.Is(err, backend.ErrGone))
	assert.Assert(t, cloud.lastRun == nil)
}

func TestProbe(t *testing.T) {
	be, cloud, _ := newTestBackend(t)
	cloud.add("run", awsmicrovm.MicrovmStateRunning, "e")
	cloud.add("susp", awsmicrovm.MicrovmStateSuspended, "e")
	cloud.add("term", awsmicrovm.MicrovmStateTerminated, "e")

	mustState := func(id string) backend.State {
		s, err := be.Probe(context.Background(), backend.Handle{ID: id})
		assert.NilError(t, err)
		return s
	}
	assert.Equal(t, mustState("run"), backend.StateRunning)
	assert.Equal(t, mustState("susp"), backend.StateExited) // suspended husk preserved
	assert.Equal(t, mustState("term"), backend.StateGone)   // terminated: nothing to resume
	assert.Equal(t, mustState("gone"), backend.StateGone)   // not found
}

func TestSignalPostsStop(t *testing.T) {
	be, cloud, _ := newTestBackend(t)
	cloud.add("mvm-1", awsmicrovm.MicrovmStateRunning, "")

	var gotPath, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get(awsmicrovm.ProxyAuthHeader)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := backend.Handle{ID: "mvm-1", Data: mustData(t, handleData{Endpoint: srv.URL})}
	signalled, err := be.Signal(context.Background(), h)
	assert.NilError(t, err)
	assert.Assert(t, signalled)
	assert.Equal(t, gotPath, stopPath)
	assert.Equal(t, gotToken, "token-mvm-1")
	assert.DeepEqual(t, cloud.tokens, []string{"mvm-1"})
}

func TestSignalNoEndpoint(t *testing.T) {
	be, _, _ := newTestBackend(t)
	signalled, err := be.Signal(context.Background(), backend.Handle{ID: "mvm-1"})
	assert.NilError(t, err)
	assert.Assert(t, !signalled)
}

func TestDestroyTerminatesAndCleansStaging(t *testing.T) {
	be, cloud, stager := newTestBackend(t)
	cloud.add("mvm-1", awsmicrovm.MicrovmStateSuspended, "e")
	stager.objects["runner-1/1.json"] = []byte("x")
	h := backend.Handle{ID: "mvm-1", Data: mustData(t, handleData{StageBucket: "bucket", StageKey: "runner-1/1.json"})}

	assert.NilError(t, be.Destroy(context.Background(), h))
	assert.Equal(t, cloud.state("mvm-1"), awsmicrovm.MicrovmStateTerminated)
	_, ok := stager.get("runner-1/1.json")
	assert.Assert(t, !ok)
}

func TestDestroyAbsentIsNoError(t *testing.T) {
	be, _, _ := newTestBackend(t)
	assert.NilError(t, be.Destroy(context.Background(), backend.Handle{ID: "gone"}))
}

// --- Wait ---

// sseTestServer serves /gritz/lifecycle, invoking handler with a per-connection
// counter so a test can drop the first connection and emit on the second.
func sseTestServer(t *testing.T, handler func(conn int, sw *sse.ServerWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	var n atomic.Int64
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != lifecyclePath {
			http.NotFound(w, r)
			return
		}
		sw, err := sse.NewServerWriter(w)
		if err != nil {
			t.Errorf("server writer: %v", err)
			return
		}
		handler(int(n.Add(1)), sw, r)
	}))
}

func driverExited(code int) sse.Event {
	data, _ := json.Marshal(DriverExited{Code: code})
	return sse.Event{Event: EventDriverExited, Data: data}
}

// waitResult is the outcome of a Wait call run in a goroutine.
type waitResult struct {
	code backend.ExitCode
	err  error
}

// runWait invokes Wait for a handle whose endpoint is set to the given URL, on a
// cancellable context, returning a channel that receives the single result.
func runWait(t *testing.T, be *Backend, id, endpoint string) (<-chan waitResult, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h := backend.Handle{Type: HandleType, ID: id, Data: mustData(t, handleData{Endpoint: endpoint})}
	res := make(chan waitResult, 1)
	go func() {
		code, err := be.Wait(ctx, h)
		res <- waitResult{code, err}
	}()
	return res, cancel
}

func awaitResult(t *testing.T, res <-chan waitResult) waitResult {
	t.Helper()
	select {
	case r := <-res:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Wait to return")
		return waitResult{}
	}
}

func assertNoResult(t *testing.T, res <-chan waitResult, d time.Duration) {
	t.Helper()
	select {
	case r := <-res:
		t.Fatalf("unexpected Wait result: %+v", r)
	case <-time.After(d):
	}
}

func TestWaitCleanExitSuspends(t *testing.T) {
	be, cloud, _ := newTestBackend(t)
	srv := sseTestServer(t, func(_ int, sw *sse.ServerWriter, r *http.Request) {
		_ = sw.Write(driverExited(7))
		<-r.Context().Done()
	})
	defer srv.Close()
	cloud.add("mvm-1", awsmicrovm.MicrovmStateRunning, srv.URL)

	res, cancel := runWait(t, be, "mvm-1", srv.URL)
	defer cancel()

	r := awaitResult(t, res)
	assert.NilError(t, r.err)
	assert.Equal(t, r.code, backend.ExitCode(7))
	// The runner — not the guest — suspended the VM.
	assert.DeepEqual(t, cloud.suspended, []string{"mvm-1"})
	assert.Equal(t, cloud.state("mvm-1"), awsmicrovm.MicrovmStateSuspended)
}

func TestWaitDropWithRunningReconnects(t *testing.T) {
	be, cloud, _ := newTestBackend(t)
	srv := sseTestServer(t, func(conn int, sw *sse.ServerWriter, r *http.Request) {
		if conn == 1 {
			return // drop immediately, no event — VM stays RUNNING
		}
		_ = sw.Write(driverExited(3)) // the gap-exit, delivered on reconnect
		<-r.Context().Done()
	})
	defer srv.Close()
	cloud.add("mvm-1", awsmicrovm.MicrovmStateRunning, srv.URL)

	res, cancel := runWait(t, be, "mvm-1", srv.URL)
	defer cancel()

	r := awaitResult(t, res)
	assert.NilError(t, r.err)
	assert.Equal(t, r.code, backend.ExitCode(3)) // the reconnect's exit, not the drop
}

func TestWaitDropWithTerminatedReportsLost(t *testing.T) {
	be, cloud, _ := newTestBackend(t)
	srv := sseTestServer(t, func(_ int, _ *sse.ServerWriter, r *http.Request) {
		// Drop the connection, and the VM is terminal by the time we arbitrate.
		cloud.setState("mvm-1", awsmicrovm.MicrovmStateTerminated)
	})
	defer srv.Close()
	cloud.add("mvm-1", awsmicrovm.MicrovmStateRunning, srv.URL)

	res, cancel := runWait(t, be, "mvm-1", srv.URL)
	defer cancel()

	r := awaitResult(t, res)
	assert.NilError(t, r.err)
	assert.Equal(t, r.code, backend.ExitLost) // no SSE code was seen → report lost
	assert.Equal(t, cloud.suspendCount(), 0)
}

func TestWaitAlreadyGoneReportsLost(t *testing.T) {
	be, _, _ := newTestBackend(t)
	// A rehydrated-already-dead sandbox: the stream drops and the control plane
	// has no record of the VM (GetMicrovm 404s), so Wait reports lost.
	srv := sseTestServer(t, func(_ int, _ *sse.ServerWriter, _ *http.Request) {
		// Return immediately: the stream ends with no event.
	})
	defer srv.Close()
	// Note: the VM is intentionally NOT registered in the cloud fake.

	res, cancel := runWait(t, be, "mvm-missing", srv.URL)
	defer cancel()

	r := awaitResult(t, res)
	assert.NilError(t, r.err)
	assert.Equal(t, r.code, backend.ExitLost)
}

func TestWaitShutdownReturnsCanceled(t *testing.T) {
	be, cloud, _ := newTestBackend(t)
	// A stream that stays open with no event: Wait is parked until ctx cancel.
	srv := sseTestServer(t, func(_ int, _ *sse.ServerWriter, r *http.Request) {
		<-r.Context().Done()
	})
	defer srv.Close()
	cloud.add("mvm-1", awsmicrovm.MicrovmStateRunning, srv.URL)

	res, cancel := runWait(t, be, "mvm-1", srv.URL)
	assertNoResult(t, res, 100*time.Millisecond)

	// Act - runner shutdown.
	cancel()

	// Assert - Wait returns a cancellation error and does NOT suspend the VM (it
	// stays alive for next-boot rehydration).
	r := awaitResult(t, res)
	assert.Assert(t, errors.Is(r.err, context.Canceled))
	assert.Equal(t, cloud.suspendCount(), 0)
}

func mustData(t *testing.T, hd handleData) []byte {
	t.Helper()
	data, err := json.Marshal(hd)
	assert.NilError(t, err)
	return data
}
