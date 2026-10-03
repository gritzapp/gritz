package microvmshim

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/icholy/gritz/internal/runner/backend/lambdamicrovm"
	"github.com/icholy/gritz/internal/x/awsmicrovm"
	"github.com/icholy/gritz/internal/x/sse"
	"gotest.tools/v3/assert"
)

// fakeProcess is a controllable Process. Wait blocks until the process is
// released (by exiting, a SIGTERM it honors, or a Kill).
type fakeProcess struct {
	exitCode  int
	honorTerm bool // if true, SIGTERM releases Wait

	mu      sync.Mutex
	signals []os.Signal
	killed  bool
	done    chan struct{}
}

func newFakeProcess(exitCode int, honorTerm bool) *fakeProcess {
	return &fakeProcess{exitCode: exitCode, honorTerm: honorTerm, done: make(chan struct{})}
}

func (p *fakeProcess) Wait() (int, error) {
	<-p.done
	return p.exitCode, nil
}

func (p *fakeProcess) Signal(s os.Signal) error {
	p.mu.Lock()
	p.signals = append(p.signals, s)
	honor := p.honorTerm
	p.mu.Unlock()
	if honor && s == syscall.SIGTERM {
		p.release()
	}
	return nil
}

func (p *fakeProcess) Kill() error {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	p.release()
	return nil
}

func (p *fakeProcess) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done:
	default:
		close(p.done)
	}
}

func (p *fakeProcess) exit() { p.release() }

func (p *fakeProcess) wasKilled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.killed
}

func (p *fakeProcess) gotSignals() []os.Signal {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]os.Signal(nil), p.signals...)
}

// postHook POSTs an AWS lifecycle hook with the given microvmId/payload.
func postHook(t *testing.T, base, path, microvmID, payload string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"microvmId": microvmID, "runHookPayload": payload})
	resp, err := http.Post(base+path, "application/json", bytes.NewReader(body))
	assert.NilError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, resp.StatusCode, http.StatusOK)
}

func bundleJSON(cmd ...string) []byte {
	data, _ := json.Marshal(lambdamicrovm.Bundle{Cmd: cmd})
	return data
}

func TestRunProvisionsOnceAndSpawns(t *testing.T) {
	var provisions, spawns atomic.Int64
	proc := newFakeProcess(0, false)
	srv := &Server{
		Fetch:     func(context.Context, string) ([]byte, error) { return bundleJSON("driver"), nil },
		Provision: func(lambdamicrovm.Bundle) error { provisions.Add(1); return nil },
		Start: func(context.Context, lambdamicrovm.Bundle) (Process, error) {
			spawns.Add(1)
			return proc, nil
		},
	}
	ts := httptest.NewServer(srv.HooksHandler())
	defer ts.Close()

	postHook(t, ts.URL, awsmicrovm.HookRun, "vm1", "http://staged")
	assert.Equal(t, provisions.Load(), int64(1))
	assert.Equal(t, spawns.Load(), int64(1))

	// A repeated /run is a no-op (already started).
	postHook(t, ts.URL, awsmicrovm.HookRun, "vm1", "http://staged")
	assert.Equal(t, provisions.Load(), int64(1))
	assert.Equal(t, spawns.Load(), int64(1))
}

func TestResumeRespawnsWithoutReprovision(t *testing.T) {
	var provisions, spawns atomic.Int64
	procs := make(chan *fakeProcess, 4)
	srv := &Server{
		Fetch:     func(context.Context, string) ([]byte, error) { return bundleJSON("driver"), nil },
		Provision: func(lambdamicrovm.Bundle) error { provisions.Add(1); return nil },
		Start: func(context.Context, lambdamicrovm.Bundle) (Process, error) {
			spawns.Add(1)
			p := newFakeProcess(0, false)
			procs <- p
			return p, nil
		},
	}
	ts := httptest.NewServer(srv.HooksHandler())
	defer ts.Close()

	postHook(t, ts.URL, awsmicrovm.HookRun, "vm1", "http://staged")
	(<-procs).exit() // first driver finishes → VM would suspend

	postHook(t, ts.URL, awsmicrovm.HookResume, "vm1", "")
	assert.Equal(t, provisions.Load(), int64(1)) // NOT re-provisioned
	assert.Equal(t, spawns.Load(), int64(2))     // driver re-spawned
}

func TestStopGracefulSIGTERM(t *testing.T) {
	proc := newFakeProcess(0, true) // honors SIGTERM
	srv := newSpawnedServer(t, proc)
	ts := httptest.NewServer(srv.ControlHandler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+lambdamicrovmStopPath, "", nil)
	assert.NilError(t, err)
	resp.Body.Close()
	assert.Equal(t, resp.StatusCode, http.StatusOK)

	assert.DeepEqual(t, signalKinds(proc.gotSignals()), []string{"terminated"})
	assert.Assert(t, !proc.wasKilled())
}

func TestStopEscalatesToSIGKILL(t *testing.T) {
	proc := newFakeProcess(0, false) // ignores SIGTERM
	srv := newSpawnedServer(t, proc)
	srv.Grace = 30 * time.Millisecond
	ts := httptest.NewServer(srv.ControlHandler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+lambdamicrovmStopPath, "", nil)
	assert.NilError(t, err)
	resp.Body.Close()

	assert.Assert(t, proc.wasKilled())
}

func TestTerminateHookSignalsDriver(t *testing.T) {
	proc := newFakeProcess(0, true)
	srv := newSpawnedServer(t, proc)
	ts := httptest.NewServer(srv.HooksHandler())
	defer ts.Close()

	postHook(t, ts.URL, awsmicrovm.HookTerminate, "vm1", "")
	assert.DeepEqual(t, signalKinds(proc.gotSignals()), []string{"terminated"})
}

func TestLifecycleStreamsDriverExited(t *testing.T) {
	proc := newFakeProcess(5, false)
	srv := newSpawnedServer(t, proc)
	ts := httptest.NewServer(srv.ControlHandler())
	defer ts.Close()

	// Open the stream, then exit the driver.
	body := openStream(t, ts.URL)
	defer body.Close()
	proc.exit()

	ev := readDriverExited(t, body)
	assert.Equal(t, ev.Code, 5)
}

func TestLifecycleStickyReplayToLateConnection(t *testing.T) {
	proc := newFakeProcess(9, false)
	srv := newSpawnedServer(t, proc)
	ts := httptest.NewServer(srv.ControlHandler())
	defer ts.Close()

	// Driver exits BEFORE anyone connects.
	proc.exit()
	waitSticky(t, srv)

	// A connection attaching after the exit still receives it (sticky replay).
	body := openStream(t, ts.URL)
	defer body.Close()
	ev := readDriverExited(t, body)
	assert.Equal(t, ev.Code, 9)
}

// TestNoControlPlaneCredentials drives the full lifecycle with NO AWS client
// configured anywhere: the shim makes no control-plane call, by construction
// (the Server type exposes no cloud hook). This is the in-guest-credential-free
// guarantee.
func TestNoControlPlaneCredentials(t *testing.T) {
	procs := make(chan *fakeProcess, 4)
	srv := &Server{
		Fetch:     func(context.Context, string) ([]byte, error) { return bundleJSON("driver"), nil },
		Provision: func(lambdamicrovm.Bundle) error { return nil },
		Start: func(context.Context, lambdamicrovm.Bundle) (Process, error) {
			p := newFakeProcess(0, true)
			procs <- p
			return p, nil
		},
	}
	hooks := httptest.NewServer(srv.HooksHandler())
	defer hooks.Close()
	control := httptest.NewServer(srv.ControlHandler())
	defer control.Close()

	// run → exit → suspend hook → resume → stop. No creds, no panics. The AWS
	// hooks and the gritz control surface are served on separate ports.
	postHook(t, hooks.URL, awsmicrovm.HookRun, "vm1", "http://staged")
	(<-procs).exit()
	postHook(t, hooks.URL, awsmicrovm.HookSuspend, "vm1", "")
	postHook(t, hooks.URL, awsmicrovm.HookResume, "vm1", "")
	resp, err := http.Post(control.URL+lambdamicrovmStopPath, "", nil)
	assert.NilError(t, err)
	resp.Body.Close()
	(<-procs).exit()
}

// TestBuildHooksReturn200 confirms the /ready and /validate build hooks (which
// gate the image snapshot) answer 200 on the hook handler even before any driver
// has been spawned — the shim serving means the app is up.
func TestBuildHooksReturn200(t *testing.T) {
	srv := &Server{}
	hooks := httptest.NewServer(srv.HooksHandler())
	defer hooks.Close()

	for _, path := range []string{awsmicrovm.HookReady, awsmicrovm.HookValidate} {
		resp, err := http.Post(hooks.URL+path, "application/json", bytes.NewReader(nil))
		assert.NilError(t, err)
		resp.Body.Close()
		assert.Equal(t, resp.StatusCode, http.StatusOK, "path %s", path)
	}

	// The build hooks are NOT on the gritz control (ingress) surface.
	control := httptest.NewServer(srv.ControlHandler())
	defer control.Close()
	resp, err := http.Post(control.URL+awsmicrovm.HookReady, "application/json", bytes.NewReader(nil))
	assert.NilError(t, err)
	resp.Body.Close()
	assert.Equal(t, resp.StatusCode, http.StatusNotFound)
}

// TestSurfacesAreSeparate confirms the two handlers are disjoint: the AWS hooks
// are not reachable on the control handler (the ingress port) and the gritz
// control routes are not reachable on the hooks handler (the hook port).
func TestSurfacesAreSeparate(t *testing.T) {
	srv := &Server{}
	control := httptest.NewServer(srv.ControlHandler())
	defer control.Close()
	hooks := httptest.NewServer(srv.HooksHandler())
	defer hooks.Close()

	// AWS hooks are NOT on the control (ingress) handler.
	resp, err := http.Post(control.URL+awsmicrovm.HookRun, "application/json", bytes.NewReader(nil))
	assert.NilError(t, err)
	resp.Body.Close()
	assert.Equal(t, resp.StatusCode, http.StatusNotFound)

	// The gritz control surface is NOT on the hooks handler.
	resp, err = http.Post(hooks.URL+lambdamicrovmStopPath, "", nil)
	assert.NilError(t, err)
	resp.Body.Close()
	assert.Equal(t, resp.StatusCode, http.StatusNotFound)
}

// --- helpers ---

// newSpawnedServer returns a Server whose driver has already been spawned (via a
// /run hook) using proc.
func newSpawnedServer(t *testing.T, proc Process) *Server {
	t.Helper()
	srv := &Server{
		Fetch:     func(context.Context, string) ([]byte, error) { return bundleJSON("driver"), nil },
		Provision: func(lambdamicrovm.Bundle) error { return nil },
		Start:     func(context.Context, lambdamicrovm.Bundle) (Process, error) { return proc, nil },
	}
	assert.NilError(t, srv.runHook(context.Background(), awsmicrovm.RunHookRequest{MicrovmID: "vm1", Payload: "http://staged"}))
	return srv
}

func signalKinds(sigs []os.Signal) []string {
	out := make([]string, 0, len(sigs))
	for _, s := range sigs {
		out = append(out, s.String())
	}
	return out
}

func openStream(t *testing.T, base string) io.ReadCloser {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+lambdamicrovmLifecyclePath, nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	assert.NilError(t, err)
	assert.Equal(t, resp.StatusCode, http.StatusOK)
	return resp.Body
}

func readDriverExited(t *testing.T, body io.Reader) lambdamicrovm.DriverExited {
	t.Helper()
	r := sse.NewReader(body)
	deadline := time.After(3 * time.Second)
	type result struct {
		ev  lambdamicrovm.DriverExited
		err error
	}
	ch := make(chan result, 1)
	go func() {
		for {
			ev, err := r.Read()
			if err != nil {
				ch <- result{err: err}
				return
			}
			if ev.Event == lambdamicrovm.EventDriverExited {
				var de lambdamicrovm.DriverExited
				_ = json.Unmarshal(ev.Data, &de)
				ch <- result{ev: de}
				return
			}
		}
	}()
	select {
	case res := <-ch:
		assert.NilError(t, res.err)
		return res.ev
	case <-deadline:
		t.Fatal("timed out reading driver-exited")
		return lambdamicrovm.DriverExited{}
	}
}

// waitSticky waits until the server has recorded the sticky driver-exited.
func waitSticky(t *testing.T, srv *Server) {
	t.Helper()
	for i := 0; i < 300; i++ {
		_, sticky, unsub := srv.lc.subscribe()
		unsub()
		if sticky != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("sticky driver-exited never recorded")
}
