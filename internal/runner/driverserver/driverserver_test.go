package driverserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/gritzclient"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/runrecord"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

const (
	testTaskID    = 1
	testVersion   = 7
	testServerURL = "http://gritz.test"
	testToken     = "eyJhbGciOiJIUzI1NiJ9.dGFzay10b2tlbg.c2lnbmF0dXJl"
)

// sleepForever is a config whose dummy agent runs until it is stopped.
var sleepForever = &agent.Config{Type: agent.TypeDummy, Dummy: &agent.DummyOptions{Sleep: -1}}

type testEnv struct {
	dir    string
	srv    *Server
	client driverv1connect.DriverServiceClient
	// gritz is the fake gritz server every run's client talks to.
	gritz *gritzclient.ClientMock
	mu    sync.Mutex
	// dialed records the server URL and token each run built its client with.
	dialed []string
}

type testOptions struct {
	// Record, when set, is written as the run record before the server boots.
	Record  *runrecord.Record
	Environ []string
}

// setup saves cfg for the test task and serves a driver server over an
// in-memory Connect client, with a fake gritz server that acks everything.
func setup(t *testing.T, cfg *agent.Config, opts testOptions) *testEnv {
	t.Helper()
	te := &testEnv{dir: t.TempDir()}
	store := agent.ConfigStore(filepath.Join(te.dir, "config"))
	assert.NilError(t, store.Save(testTaskID, cfg))
	recordPath := filepath.Join(te.dir, "run.json")
	if opts.Record != nil {
		assert.NilError(t, runrecord.Write(recordPath, *opts.Record))
	}
	te.gritz = &gritzclient.ClientMock{
		GetTaskFunc: func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
			return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Id: req.Id, Version: testVersion}}, nil
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
	srv, err := New(Options{
		RecordPath: recordPath,
		LogPath:    filepath.Join(te.dir, "log"),
		Config:     store,
		Environ:    opts.Environ,
		NewClient: func(serverURL, token string) gritzclient.Client {
			te.mu.Lock()
			defer te.mu.Unlock()
			te.dialed = append(te.dialed, serverURL+" "+token)
			return te.gritz
		},
		KeepAlive: 20 * time.Millisecond,
	})
	assert.NilError(t, err)
	te.srv = srv
	mux := http.NewServeMux()
	mux.Handle(srv.Handler())
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		assert.NilError(t, srv.Shutdown(ctx))
	})
	te.client = driverv1connect.NewDriverServiceClient(hs.Client(), hs.URL)
	return te
}

func testSpec() *driverv1.RunSpec {
	return &driverv1.RunSpec{TaskId: testTaskID, ServerUrl: testServerURL, Token: testToken}
}

// open calls Run, retrying while the previous stream is still being released,
// and reads the Accepted every stream starts with.
func (te *testEnv) open(t *testing.T, req *driverv1.RunRequest) (*connect.ServerStreamForClient[driverv1.RunResponse], *driverv1.Accepted) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		stream, err := te.client.Run(t.Context(), req)
		assert.NilError(t, err)
		if stream.Receive() {
			accepted := stream.Msg().GetAccepted()
			assert.Assert(t, accepted != nil, "first message is %v, want accepted", stream.Msg())
			t.Cleanup(func() { _ = stream.Close() })
			return stream, accepted
		}
		err = stream.Err()
		_ = stream.Close()
		if connect.CodeOf(err) != connect.CodeUnavailable || time.Now().After(deadline) {
			t.Fatalf("Run: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// runErr calls Run and returns the error the stream ends with before any
// message, retrying while the previous stream is still being released.
func (te *testEnv) runErr(t *testing.T, req *driverv1.RunRequest) error {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		stream, err := te.client.Run(t.Context(), req)
		assert.NilError(t, err)
		ok := stream.Receive()
		err = stream.Err()
		_ = stream.Close()
		assert.Assert(t, !ok, "Run accepted, want an error")
		if connect.CodeOf(err) != connect.CodeUnavailable || time.Now().After(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// finished reads the stream until Finished, skipping keep-alives, and checks
// that the stream ends there.
func finished(t *testing.T, stream *connect.ServerStreamForClient[driverv1.RunResponse]) *driverv1.Finished {
	t.Helper()
	for stream.Receive() {
		if f := stream.Msg().GetFinished(); f != nil {
			assert.Assert(t, !stream.Receive(), "message after finished: %v", stream.Msg())
			assert.NilError(t, stream.Err())
			return f
		}
		assert.Assert(t, stream.Msg().GetKeepAlive() != nil, "unexpected message: %v", stream.Msg())
	}
	t.Fatalf("stream ended before finished: %v", stream.Err())
	return nil
}

func (te *testEnv) status(t *testing.T) *driverv1.RunRecord {
	t.Helper()
	resp, err := te.client.Status(t.Context(), &driverv1.StatusRequest{})
	assert.NilError(t, err)
	return resp.GetRun()
}

func (te *testEnv) stop(t *testing.T, version int64) bool {
	t.Helper()
	resp, err := te.client.Stop(t.Context(), &driverv1.StopRequest{Version: version})
	assert.NilError(t, err)
	return resp.GetStopped()
}

func (te *testEnv) dials() []string {
	te.mu.Lock()
	defer te.mu.Unlock()
	return te.dialed
}

func runnerEvents(events ...string) []*gritzv1.RunnerEvent {
	var out []*gritzv1.RunnerEvent
	for _, event := range events {
		out = append(out, &gritzv1.RunnerEvent{TaskId: testTaskID, Version: testVersion, Event: event})
	}
	return out
}

func TestRun(t *testing.T) {
	t.Parallel()
	// Arrange
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})
	assert.Assert(t, te.status(t) == nil)

	// Act
	stream, accepted := te.open(t, &driverv1.RunRequest{Version: testVersion, Spec: testSpec()})

	// Assert - the run started, reported both events itself, and Finished
	// says the report was acknowledged
	assert.Equal(t, accepted.GetAttached(), false)
	assert.DeepEqual(t, finished(t, stream), &driverv1.Finished{Version: testVersion, Reported: true}, protocmp.Transform())
	assert.DeepEqual(t, te.gritz.SubmittedRunnerEvents(), runnerEvents("started", "stopped"), protocmp.Transform())
	assert.DeepEqual(t, te.dials(), []string{testServerURL + " " + testToken})
	rec := te.status(t)
	assert.Equal(t, rec.GetVersion(), int64(testVersion))
	assert.Equal(t, rec.GetState(), driverv1.RunRecord_STATE_FINISHED)
	assert.Assert(t, rec.GetFinishedAt() != nil)
	disk, ok, err := runrecord.Read(filepath.Join(te.dir, "run.json"))
	assert.NilError(t, err)
	assert.Assert(t, ok)
	assert.Equal(t, disk.Reported, true)
}

func TestRun_Idempotent(t *testing.T) {
	t.Parallel()
	// Arrange - a run that has finished
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})
	stream, _ := te.open(t, &driverv1.RunRequest{Version: testVersion, Spec: testSpec()})
	finished(t, stream)

	// Act - the same version again, spec and all
	stream, accepted := te.open(t, &driverv1.RunRequest{Version: testVersion, Spec: testSpec()})

	// Assert - attached to the finished run; the agent did not run again
	assert.Equal(t, accepted.GetAttached(), true)
	assert.DeepEqual(t, finished(t, stream), &driverv1.Finished{Version: testVersion, Reported: true}, protocmp.Transform())
	assert.DeepEqual(t, te.gritz.SubmittedRunnerEvents(), runnerEvents("started", "stopped"), protocmp.Transform())
	assert.Equal(t, len(te.dials()), 1)
}

func TestRun_ReattachAfterDrop(t *testing.T) {
	t.Parallel()
	// Arrange - a running run whose stream is dropped
	te := setup(t, sleepForever, testOptions{})
	stream, _ := te.open(t, &driverv1.RunRequest{Version: testVersion, Spec: testSpec()})
	assert.NilError(t, stream.Close())

	// Act - attach without a spec
	stream, accepted := te.open(t, &driverv1.RunRequest{Version: testVersion})

	// Assert - the run kept going through the drop
	assert.Equal(t, accepted.GetAttached(), true)
	rec := te.status(t)
	assert.Equal(t, rec.GetVersion(), int64(testVersion))
	assert.Equal(t, rec.GetState(), driverv1.RunRecord_STATE_RUNNING)

	// Act - stop it
	assert.Equal(t, te.stop(t, testVersion), true)

	// Assert - the attached stream gets Finished, and the stop was reported
	assert.DeepEqual(t, finished(t, stream), &driverv1.Finished{Version: testVersion, Reported: true}, protocmp.Transform())
	assert.DeepEqual(t, te.gritz.SubmittedRunnerEvents(), runnerEvents("started", "stopped"), protocmp.Transform())
	assert.Equal(t, te.status(t).GetState(), driverv1.RunRecord_STATE_FINISHED)
}

func TestRun_KeepAlive(t *testing.T) {
	t.Parallel()
	// Arrange
	te := setup(t, sleepForever, testOptions{})
	stream, _ := te.open(t, &driverv1.RunRequest{Version: testVersion, Spec: testSpec()})

	// Act
	ok := stream.Receive()

	// Assert
	assert.Assert(t, ok, stream.Err())
	assert.Assert(t, stream.Msg().GetKeepAlive() != nil, "got %v, want keep_alive", stream.Msg())
}

func TestRun_SecondStream(t *testing.T) {
	t.Parallel()
	// Arrange - a run with its stream still open
	te := setup(t, sleepForever, testOptions{})
	te.open(t, &driverv1.RunRequest{Version: testVersion, Spec: testSpec()})

	// Act
	stream, err := te.client.Run(t.Context(), &driverv1.RunRequest{Version: testVersion})
	assert.NilError(t, err)
	defer stream.Close()
	ok := stream.Receive()

	// Assert
	assert.Assert(t, !ok)
	assert.Equal(t, connect.CodeOf(stream.Err()), connect.CodeUnavailable)
}

func TestRun_Stale(t *testing.T) {
	t.Parallel()
	// Arrange
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})
	stream, _ := te.open(t, &driverv1.RunRequest{Version: testVersion, Spec: testSpec()})
	finished(t, stream)

	// Act
	err := te.runErr(t, &driverv1.RunRequest{Version: testVersion - 1, Spec: testSpec()})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeFailedPrecondition)
}

func TestRun_Busy(t *testing.T) {
	t.Parallel()
	// Arrange - a running run, detached
	te := setup(t, sleepForever, testOptions{})
	stream, _ := te.open(t, &driverv1.RunRequest{Version: testVersion, Spec: testSpec()})
	assert.NilError(t, stream.Close())

	// Act
	err := te.runErr(t, &driverv1.RunRequest{Version: testVersion + 1, Spec: testSpec()})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeAborted)
}

func TestRun_NotFound(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		record *runrecord.Record
	}{
		{"no record", nil},
		{"older record", &runrecord.Record{Version: testVersion - 1, State: runrecord.Finished, FinishedAt: time.Now()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{Record: tt.record})

			err := te.runErr(t, &driverv1.RunRequest{Version: testVersion})

			assert.Equal(t, connect.CodeOf(err), connect.CodeNotFound)
		})
	}
}

func TestRun_NextVersion(t *testing.T) {
	t.Parallel()
	// Arrange - a finished run
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})
	stream, _ := te.open(t, &driverv1.RunRequest{Version: testVersion, Spec: testSpec()})
	finished(t, stream)

	// Act
	stream, accepted := te.open(t, &driverv1.RunRequest{Version: testVersion + 1, Spec: testSpec()})

	// Assert - a new run started in the same process
	assert.Equal(t, accepted.GetAttached(), false)
	assert.DeepEqual(t, finished(t, stream), &driverv1.Finished{Version: testVersion + 1, Reported: true}, protocmp.Transform())
	assert.Equal(t, te.status(t).GetVersion(), int64(testVersion+1))
	assert.Equal(t, len(te.gritz.SubmittedRunnerEvents()), 4)
}

func TestRun_NotReported(t *testing.T) {
	t.Parallel()
	// Arrange - the gritz server rejects the terminal event
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})
	te.gritz.SubmitRunnerEventsFunc = func(_ context.Context, req *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
		if req.Events[0].Event == "stopped" {
			return nil, errors.New("server unavailable")
		}
		return &gritzv1.SubmitRunnerEventsResponse{}, nil
	}

	// Act
	stream, _ := te.open(t, &driverv1.RunRequest{Version: testVersion, Spec: testSpec()})

	// Assert
	assert.DeepEqual(t, finished(t, stream), &driverv1.Finished{Version: testVersion, Reported: false}, protocmp.Transform())
	disk, _, err := runrecord.Read(filepath.Join(te.dir, "run.json"))
	assert.NilError(t, err)
	assert.Equal(t, disk.State, runrecord.Finished)
	assert.Equal(t, disk.Reported, false)
}

func TestStop_NotRunning(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		version int64
	}{
		{"active version, finished", testVersion},
		{"other version", testVersion + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})
			stream, _ := te.open(t, &driverv1.RunRequest{Version: testVersion, Spec: testSpec()})
			finished(t, stream)

			stopped := te.stop(t, tt.version)

			assert.Equal(t, stopped, false)
		})
	}
}

func TestStop_NoRun(t *testing.T) {
	t.Parallel()
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})

	stopped := te.stop(t, testVersion)

	assert.Equal(t, stopped, false)
}

func TestNew_Reconcile(t *testing.T) {
	t.Parallel()
	// Arrange - the previous process died mid-run
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{
		Record: &runrecord.Record{Version: testVersion, State: runrecord.Running},
	})

	// Act
	rec := te.status(t)
	stream, accepted := te.open(t, &driverv1.RunRequest{Version: testVersion, Spec: testSpec()})

	// Assert - the run is finished and unreported, and is not run again
	assert.Equal(t, rec.GetVersion(), int64(testVersion))
	assert.Equal(t, rec.GetState(), driverv1.RunRecord_STATE_FINISHED)
	assert.Equal(t, accepted.GetAttached(), true)
	assert.DeepEqual(t, finished(t, stream), &driverv1.Finished{Version: testVersion, Reported: false}, protocmp.Transform())
	assert.Equal(t, len(te.dials()), 0)
}

func TestShutdown(t *testing.T) {
	t.Parallel()
	// Arrange - a running run with its stream open
	te := setup(t, sleepForever, testOptions{})
	stream, _ := te.open(t, &driverv1.RunRequest{Version: testVersion, Spec: testSpec()})

	// Act
	assert.NilError(t, te.srv.Shutdown(t.Context()))

	// Assert - the run was stopped and reported before Shutdown returned
	assert.DeepEqual(t, te.gritz.SubmittedRunnerEvents(), runnerEvents("started", "stopped"), protocmp.Transform())
	assert.DeepEqual(t, finished(t, stream), &driverv1.Finished{Version: testVersion, Reported: true}, protocmp.Transform())

	// Assert - no run starts afterwards
	stream, err := te.client.Run(t.Context(), &driverv1.RunRequest{Version: testVersion + 1, Spec: testSpec()})
	assert.NilError(t, err)
	defer stream.Close()
	assert.Assert(t, !stream.Receive())
	assert.Equal(t, connect.CodeOf(stream.Err()), connect.CodeUnavailable)
	assert.ErrorContains(t, stream.Err(), "shutting down")
}

func TestRun_Env(t *testing.T) {
	t.Parallel()
	// Arrange - a setup command printing variables set at each layer
	te := setup(t, &agent.Config{
		Type:     agent.TypeDummy,
		Commands: []string{`echo "$BASE $FOO $BAR" > "$OUT"`},
	}, testOptions{Environ: []string{"BASE=base", "FOO=environ", "BAR=environ"}})
	out := filepath.Join(te.dir, "out")
	spec := testSpec()
	spec.Env = map[string]string{"FOO": "env", "BAR": "env", "OUT": out}
	spec.Secrets = map[string]string{"BAR": "secret"}

	// Act
	stream, _ := te.open(t, &driverv1.RunRequest{Version: testVersion, Spec: spec})
	finished(t, stream)

	// Assert - the run's env wins over the driver's, and its secrets over both
	got, err := os.ReadFile(out)
	assert.NilError(t, err)
	assert.Equal(t, string(got), "base env secret\n")
}

func TestRun_MasksSecretsInShippedLog(t *testing.T) {
	t.Parallel()
	// Arrange - a setup command echoing both a secret and the task token
	const secret = "ghp_S3CR3TV4LU3"
	te := setup(t, &agent.Config{
		Type:     agent.TypeDummy,
		Commands: []string{"echo cloning with $GH_TOKEN as " + testToken},
	}, testOptions{})
	spec := testSpec()
	spec.Secrets = map[string]string{"GH_TOKEN": secret}

	// Act
	stream, _ := te.open(t, &driverv1.RunRequest{Version: testVersion, Spec: spec})
	finished(t, stream)

	// Assert - /gritz/log is raw, the shipped copy is masked
	file, err := os.ReadFile(filepath.Join(te.dir, "log"))
	assert.NilError(t, err)
	assert.Assert(t, cmp.Contains(string(file), "cloning with "+secret))
	shipped := te.gritz.ShippedLog()
	assert.Assert(t, cmp.Contains(shipped, "cloning with [gritz:masked GH_TOKEN] as [gritz:masked token]"))
	assert.Assert(t, !strings.Contains(shipped, secret), "shipped log leaked the secret")
	assert.Assert(t, !strings.Contains(shipped, testToken), "shipped log leaked the token")
}

func TestEnv(t *testing.T) {
	spec := &driverv1.RunSpec{
		Env:     map[string]string{"B": "env", "A": "env"},
		Secrets: map[string]string{"B": "secret"},
	}

	env := Env([]string{"PATH=/bin", "A=environ"}, spec)

	assert.DeepEqual(t, env, []string{"PATH=/bin", "A=environ", "A=env", "B=env", "B=secret"})
}

func TestSecrets(t *testing.T) {
	spec := &driverv1.RunSpec{
		Secrets: map[string]string{"GH_TOKEN": "ghp", "token": "workspace"},
		Token:   "jwt",
	}

	secrets := Secrets(spec)

	assert.DeepEqual(t, secrets, map[string]string{"GH_TOKEN": "ghp", "token": "jwt"})
}
