package driverserver

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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
	// Dir, when set, is the sandbox state directory, shared with an earlier
	// setup; defaults to a fresh one.
	Dir string
}

// setup saves cfg for the test task and serves a driver server over an
// in-memory Connect client, with a fake gritz server that acks everything.
func setup(t *testing.T, cfg *agent.Config, opts testOptions) *testEnv {
	t.Helper()
	te := &testEnv{dir: opts.Dir}
	if te.dir == "" {
		te.dir = t.TempDir()
	}
	store := agent.ConfigStore(filepath.Join(te.dir, "config"))
	assert.NilError(t, store.Save(testTaskID, cfg))
	recordPath := filepath.Join(te.dir, "run.json")
	if opts.Record != nil {
		assert.NilError(t, runrecord.Write(recordPath, *opts.Record))
	}
	te.gritz = &gritzclient.ClientMock{
		GetTaskFunc: func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
			return &gritzv1.GetTaskResponse{
				Task: &gritzv1.Task{Id: req.Id, Version: testVersion},
			}, nil
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
		EventsPath: filepath.Join(te.dir, "events.jsonl"),
		LogPath:    filepath.Join(te.dir, "log"),
		Config:     store,
		FileRoot:   filepath.Join(te.dir, "root"),
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
	// Run is a bidi stream, which needs HTTP/2.
	hs := httptest.NewUnstartedServer(mux)
	hs.EnableHTTP2 = true
	hs.StartTLS()
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

// runStream is the runner's end of a Run stream.
type runStream = connect.BidiStreamForClientSimple[driverv1.RunRequest, driverv1.RunResponse]

// startReq is the first message of a stream that starts run version with spec.
func startReq(version int64, spec *driverv1.RunSpec) *driverv1.RunRequest {
	return &driverv1.RunRequest{Version: version, Msg: &driverv1.RunRequest_Spec{Spec: spec}}
}

// attachReq is the first message of a stream that attaches to run version.
func attachReq(version int64) *driverv1.RunRequest {
	return &driverv1.RunRequest{Version: version}
}

// call opens a Run stream, sends first, and returns the stream with its first
// response, or the error it ended with before one.
func (te *testEnv) call(t *testing.T, first *driverv1.RunRequest) (*runStream, *driverv1.RunResponse, error) {
	t.Helper()
	stream, err := te.client.Run(t.Context())
	assert.NilError(t, err)
	err = stream.Send(first)
	assert.Assert(t, err == nil || errors.Is(err, io.EOF), "Send: %v", err)
	msg, err := stream.Receive()
	if err != nil {
		_ = stream.CloseResponse()
		return nil, nil, err
	}
	t.Cleanup(func() { _ = stream.CloseResponse() })
	return stream, msg, nil
}

// open opens a Run stream with first, retrying while the previous stream is
// still being released, and reads the Hello every stream starts with.
func (te *testEnv) open(t *testing.T, first *driverv1.RunRequest) (*runStream, *driverv1.Hello) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		stream, msg, err := te.call(t, first)
		if err == nil {
			hello := msg.GetHello()
			assert.Assert(t, hello != nil, "first message is %v, want hello", msg)
			return stream, hello
		}
		if connect.CodeOf(err) != connect.CodeUnavailable || time.Now().After(deadline) {
			t.Fatalf("Run: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// runErr opens a Run stream with first and returns the error it ends with
// before any message, retrying while the previous stream is still being
// released.
func (te *testEnv) runErr(t *testing.T, first *driverv1.RunRequest) error {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, msg, err := te.call(t, first)
		assert.Assert(t, err != nil, "Run answered %v, want an error", msg)
		if connect.CodeOf(err) != connect.CodeUnavailable || time.Now().After(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// idle reads the stream until Idle, skipping keep-alives, and checks that the
// stream ends there.
func idle(t *testing.T, stream *runStream) *driverv1.Idle {
	t.Helper()
	for {
		msg, err := stream.Receive()
		if err != nil {
			t.Fatalf("stream ended before idle: %v", err)
		}
		if idle := msg.GetIdle(); idle != nil {
			msg, err := stream.Receive()
			assert.Assert(t, errors.Is(err, io.EOF), "message after idle: %v, %v", msg, err)
			return idle
		}
		assert.Assert(t, msg.GetKeepAlive() != nil, "unexpected message: %v", msg)
	}
}

// send sends events on stream as one batch.
func send(t *testing.T, stream *runStream, events ...*gritzv1.Event) {
	t.Helper()
	assert.NilError(t, stream.Send(&driverv1.RunRequest{Msg: &driverv1.RunRequest_Events{
		Events: &driverv1.TaskEvents{Events: events},
	}}))
}

// event returns event id of the test task, a start command.
func event(id int64) *gritzv1.Event {
	return &gritzv1.Event{
		Id:     id,
		TaskId: testTaskID,
		Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{
			Command: gritzv1.TaskCommand_START,
			Version: testVersion,
		}},
	}
}

// ids returns the id of each event.
func ids(events []*gritzv1.Event) []int64 {
	var out []int64
	for _, ev := range events {
		out = append(out, ev.GetId())
	}
	return out
}

// waitStored waits until the event file holds the events with ids want, in
// that order, and nothing else.
func (te *testEnv) waitStored(t *testing.T, want ...int64) {
	t.Helper()
	var got []int64
	ok := poll(func() bool {
		events, err := te.srv.events.After(0)
		assert.NilError(t, err)
		got = ids(events)
		return slices.Equal(got, want)
	})
	assert.Assert(t, ok, "stored %v, want %v", got, want)
}

// record reads the run record from disk.
func (te *testEnv) record(t *testing.T) (runrecord.Record, bool) {
	t.Helper()
	rec, ok, err := runrecord.Read(filepath.Join(te.dir, "run.json"))
	assert.NilError(t, err)
	return rec, ok
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
		out = append(out, &gritzv1.RunnerEvent{
			TaskId:  testTaskID,
			Version: testVersion,
			Event:   event,
		})
	}
	return out
}

func TestRun(t *testing.T) {
	t.Parallel()
	// Arrange
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})
	_, ok := te.record(t)
	assert.Assert(t, !ok)

	// Act
	stream, hello := te.open(t, startReq(testVersion, testSpec()))

	// Assert - the run started, reported both events itself, and Idle says
	// the report was acknowledged
	assert.Equal(t, hello.GetLastEventId(), int64(0))
	assert.DeepEqual(t, idle(t, stream), &driverv1.Idle{
		Version:  testVersion,
		Reported: true,
	}, protocmp.Transform())
	assert.DeepEqual(t, te.gritz.SubmittedRunnerEvents(), runnerEvents("started", "stopped"), protocmp.Transform())
	assert.DeepEqual(t, te.dials(), []string{testServerURL + " " + testToken})
	rec, ok := te.record(t)
	assert.Assert(t, ok)
	assert.Equal(t, rec.Version, int64(testVersion))
	assert.Equal(t, rec.State, runrecord.Finished)
	assert.Assert(t, !rec.FinishedAt.IsZero())
	assert.Equal(t, rec.Reported, true)
}

// root returns the path of a sandbox path under the test's file root.
func (te *testEnv) root(path string) string {
	return filepath.Join(te.dir, "root", path)
}

func TestRun_ProvisionsFiles(t *testing.T) {
	t.Parallel()
	// Arrange
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})
	spec := testSpec()
	spec.Files = []*driverv1.File{
		{Path: "/gritz", Mode: 0o777, Dir: true},
		{Path: "/tmp/gritz/1.json", Data: []byte("config"), Mode: 0o640},
	}

	// Act
	stream, _ := te.open(t, startReq(testVersion, spec))
	idle(t, stream)

	// Assert - the files were written with their modes, regardless of the
	// umask, and the sandbox is marked provisioned
	info, err := os.Stat(te.root("/gritz"))
	assert.NilError(t, err)
	assert.Assert(t, info.IsDir())
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o777))
	data, err := os.ReadFile(te.root("/tmp/gritz/1.json"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), "config")
	info, err = os.Stat(te.root("/tmp/gritz/1.json"))
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o640))
	_, err = os.Stat(te.root(ProvisionedPath))
	assert.NilError(t, err)
}

func TestRun_ProvisionsOnce(t *testing.T) {
	t.Parallel()
	// Arrange - a provisioned sandbox whose agent changed a file
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})
	spec := testSpec()
	spec.Files = []*driverv1.File{
		{Path: "/tmp/gritz/1.json", Data: []byte("config"), Mode: 0o640},
	}
	stream, _ := te.open(t, startReq(testVersion, spec))
	idle(t, stream)
	assert.NilError(t, os.WriteFile(te.root("/tmp/gritz/1.json"), []byte("changed"), 0o640))

	// Act - a later run with different files
	spec = testSpec()
	spec.Files = []*driverv1.File{
		{Path: "/tmp/gritz/1.json", Data: []byte("new config"), Mode: 0o640},
	}
	stream, _ = te.open(t, startReq(testVersion+1, spec))
	idle(t, stream)

	// Assert - the run started, and the file was not rewritten
	assert.Equal(t, len(te.dials()), 2)
	data, err := os.ReadFile(te.root("/tmp/gritz/1.json"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), "changed")
}

func TestRun_AttachDoesNotProvision(t *testing.T) {
	t.Parallel()
	// Arrange - a run that finished before this process started
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{
		Record: &runrecord.Record{Version: testVersion, State: runrecord.Finished, Reported: true},
	})
	spec := testSpec()
	spec.Files = []*driverv1.File{
		{Path: "/tmp/gritz/1.json", Data: []byte("config"), Mode: 0o640},
	}

	// Act - the same version, spec and all
	stream, _ := te.open(t, startReq(testVersion, spec))
	idle(t, stream)

	// Assert - attached, and nothing was written
	assert.Equal(t, len(te.dials()), 0)
	_, err := os.Stat(te.root("/tmp/gritz/1.json"))
	assert.Assert(t, errors.Is(err, os.ErrNotExist), err)
	_, err = os.Stat(te.root(ProvisionedPath))
	assert.Assert(t, errors.Is(err, os.ErrNotExist), err)
}

func TestRun_ProvisionFailure(t *testing.T) {
	t.Parallel()
	// Arrange - a file whose parent is a regular file
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})
	spec := testSpec()
	spec.Files = []*driverv1.File{
		{Path: "/etc", Data: []byte("not a dir"), Mode: 0o644},
		{Path: "/etc/config.json", Data: []byte("config"), Mode: 0o644},
	}

	// Act
	err := te.runErr(t, startReq(testVersion, spec))

	// Assert - the start failed before the run record, and the sandbox is
	// not marked provisioned
	assert.Equal(t, connect.CodeOf(err), connect.CodeInternal)
	_, ok := te.record(t)
	assert.Assert(t, !ok)
	assert.Equal(t, len(te.dials()), 0)
	_, err = os.Stat(te.root(ProvisionedPath))
	assert.Assert(t, errors.Is(err, os.ErrNotExist), err)
}

func TestRun_Idempotent(t *testing.T) {
	t.Parallel()
	// Arrange - a run that has finished
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})
	stream, _ := te.open(t, startReq(testVersion, testSpec()))
	idle(t, stream)

	// Act - the same version again, spec and all
	stream, _ = te.open(t, startReq(testVersion, testSpec()))

	// Assert - attached to the finished run; the agent did not run again
	assert.DeepEqual(t, idle(t, stream), &driverv1.Idle{
		Version:  testVersion,
		Reported: true,
	}, protocmp.Transform())
	assert.DeepEqual(t, te.gritz.SubmittedRunnerEvents(), runnerEvents("started", "stopped"), protocmp.Transform())
	assert.Equal(t, len(te.dials()), 1)
}

func TestRun_ReattachAfterDrop(t *testing.T) {
	t.Parallel()
	// Arrange - a running run whose stream is dropped
	te := setup(t, sleepForever, testOptions{})
	stream, _ := te.open(t, startReq(testVersion, testSpec()))
	assert.NilError(t, stream.CloseResponse())

	// Act - attach without a spec
	stream, _ = te.open(t, attachReq(testVersion))

	// Assert - the run kept going through the drop
	rec, _ := te.record(t)
	assert.Equal(t, rec.Version, int64(testVersion))
	assert.Equal(t, rec.State, runrecord.Running)

	// Act - stop it
	assert.Equal(t, te.stop(t, testVersion), true)

	// Assert - the attached stream gets Idle, and the stop was reported
	assert.DeepEqual(t, idle(t, stream), &driverv1.Idle{
		Version:  testVersion,
		Reported: true,
	}, protocmp.Transform())
	assert.DeepEqual(t, te.gritz.SubmittedRunnerEvents(), runnerEvents("started", "stopped"), protocmp.Transform())
	rec, _ = te.record(t)
	assert.Equal(t, rec.State, runrecord.Finished)
}

func TestRun_StoresEvents(t *testing.T) {
	t.Parallel()
	// Arrange - a running run with its stream open
	te := setup(t, sleepForever, testOptions{})
	stream, _ := te.open(t, startReq(testVersion, testSpec()))

	// Act - two batches, in log order, which is not id order
	send(t, stream, event(5), event(3))
	send(t, stream, event(8))

	// Assert - the events are stored in the order they were sent
	te.waitStored(t, 5, 3, 8)
	assert.Equal(t, te.srv.events.LastID(), int64(8))
}

func TestRun_EventsSurviveRestart(t *testing.T) {
	t.Parallel()
	// Arrange - a driver that stored events, then went away
	te := setup(t, sleepForever, testOptions{})
	stream, _ := te.open(t, startReq(testVersion, testSpec()))
	send(t, stream, event(5), event(3))
	te.waitStored(t, 5, 3)
	assert.NilError(t, te.srv.Shutdown(t.Context()))
	idle(t, stream)

	// Act - a new driver process on the same sandbox
	restarted := setup(t, sleepForever, testOptions{Dir: te.dir})
	_, hello := restarted.open(t, attachReq(testVersion))

	// Assert - it kept the events, and says where they end
	assert.Equal(t, hello.GetLastEventId(), int64(3))
	restarted.waitStored(t, 5, 3)
}

func TestRun_ReopenedStream(t *testing.T) {
	t.Parallel()
	// Arrange - a running run that stored events on a stream that dropped
	te := setup(t, sleepForever, testOptions{})
	stream, _ := te.open(t, startReq(testVersion, testSpec()))
	send(t, stream, event(5), event(3))
	te.waitStored(t, 5, 3)
	assert.NilError(t, stream.CloseResponse())

	// Act - reattach, and resend from before the driver's position
	stream, hello := te.open(t, attachReq(testVersion))
	send(t, stream, event(3), event(9))

	// Assert - Hello says what the file has, and only what it lacked is
	// stored
	assert.Equal(t, hello.GetLastEventId(), int64(3))
	te.waitStored(t, 5, 3, 9)
}

func TestRun_KeepAlive(t *testing.T) {
	t.Parallel()
	// Arrange
	te := setup(t, sleepForever, testOptions{})
	stream, _ := te.open(t, startReq(testVersion, testSpec()))

	// Act
	msg, err := stream.Receive()

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, msg.GetKeepAlive() != nil, "got %v, want keep_alive", msg)
}

func TestRun_SecondStream(t *testing.T) {
	t.Parallel()
	// Arrange - a run with its stream still open
	te := setup(t, sleepForever, testOptions{})
	te.open(t, startReq(testVersion, testSpec()))

	// Act
	_, _, err := te.call(t, attachReq(testVersion))

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnavailable)
}

func TestRun_Stale(t *testing.T) {
	t.Parallel()
	// Arrange
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})
	stream, _ := te.open(t, startReq(testVersion, testSpec()))
	idle(t, stream)

	// Act
	err := te.runErr(t, startReq(testVersion-1, testSpec()))

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeFailedPrecondition)
}

func TestRun_Busy(t *testing.T) {
	t.Parallel()
	// Arrange - a running run, detached
	te := setup(t, sleepForever, testOptions{})
	stream, _ := te.open(t, startReq(testVersion, testSpec()))
	assert.NilError(t, stream.CloseResponse())

	// Act
	err := te.runErr(t, startReq(testVersion+1, testSpec()))

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
		{
			"older record",
			&runrecord.Record{
				Version:    testVersion - 1,
				State:      runrecord.Finished,
				FinishedAt: time.Now(),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{Record: tt.record})

			err := te.runErr(t, attachReq(testVersion))

			assert.Equal(t, connect.CodeOf(err), connect.CodeNotFound)
		})
	}
}

func TestRun_NextVersion(t *testing.T) {
	t.Parallel()
	// Arrange - a finished run
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})
	stream, _ := te.open(t, startReq(testVersion, testSpec()))
	idle(t, stream)

	// Act
	stream, _ = te.open(t, startReq(testVersion+1, testSpec()))

	// Assert - a new run started in the same process
	assert.DeepEqual(t, idle(t, stream), &driverv1.Idle{
		Version:  testVersion + 1,
		Reported: true,
	}, protocmp.Transform())
	rec, _ := te.record(t)
	assert.Equal(t, rec.Version, int64(testVersion+1))
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
	stream, _ := te.open(t, startReq(testVersion, testSpec()))

	// Assert
	assert.DeepEqual(t, idle(t, stream), &driverv1.Idle{
		Version:  testVersion,
		Reported: false,
	}, protocmp.Transform())
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
			stream, _ := te.open(t, startReq(testVersion, testSpec()))
			idle(t, stream)

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
	rec, _ := te.record(t)
	stream, _ := te.open(t, startReq(testVersion, testSpec()))

	// Assert - the run is finished and unreported, and is not run again
	assert.Equal(t, rec.Version, int64(testVersion))
	assert.Equal(t, rec.State, runrecord.Finished)
	assert.DeepEqual(t, idle(t, stream), &driverv1.Idle{
		Version:  testVersion,
		Reported: false,
	}, protocmp.Transform())
	assert.Equal(t, len(te.dials()), 0)
}

func TestShutdown(t *testing.T) {
	t.Parallel()
	// Arrange - a running run with its stream open
	te := setup(t, sleepForever, testOptions{})
	stream, _ := te.open(t, startReq(testVersion, testSpec()))

	// Act
	assert.NilError(t, te.srv.Shutdown(t.Context()))

	// Assert - the run was stopped and reported before Shutdown returned
	assert.DeepEqual(t, te.gritz.SubmittedRunnerEvents(), runnerEvents("started", "stopped"), protocmp.Transform())
	assert.DeepEqual(t, idle(t, stream), &driverv1.Idle{
		Version:  testVersion,
		Reported: true,
	}, protocmp.Transform())

	// Assert - no run starts afterwards
	_, _, err := te.call(t, startReq(testVersion+1, testSpec()))
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnavailable)
	assert.ErrorContains(t, err, "shutting down")
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
	stream, _ := te.open(t, startReq(testVersion, spec))
	idle(t, stream)

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
	stream, _ := te.open(t, startReq(testVersion, spec))
	idle(t, stream)

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

func TestListenAndServe(t *testing.T) {
	t.Parallel()
	// Arrange - serving on a free port
	te := setup(t, sleepForever, testOptions{})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NilError(t, err)
	addr := l.Addr().String()
	assert.NilError(t, l.Close())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- te.srv.ListenAndServe(ctx, addr) }()
	// Unencrypted HTTP/2, which Run's bidi stream needs.
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	te.client = driverv1connect.NewDriverServiceClient(&http.Client{Transport: &http.Transport{Protocols: protocols}}, "http://"+addr)
	assert.Assert(t, poll(func() bool {
		_, err := te.client.Stop(t.Context(), &driverv1.StopRequest{Version: testVersion})
		return err == nil
	}), "server never came up")
	stream, _ := te.open(t, startReq(testVersion, testSpec()))

	// Act
	cancel()

	// Assert - the run was stopped and reported, its stream got Idle,
	// and the server shut down cleanly
	assert.DeepEqual(t, idle(t, stream), &driverv1.Idle{
		Version:  testVersion,
		Reported: true,
	}, protocmp.Transform())
	assert.NilError(t, <-errCh)
	assert.DeepEqual(t, te.gritz.SubmittedRunnerEvents(), runnerEvents("started", "stopped"), protocmp.Transform())
}

// On a unix:///path address, ListenAndServe replaces a stale socket file and
// removes the socket on shutdown.
func TestListenAndServe_Unix(t *testing.T) {
	t.Parallel()
	// Arrange - a file left behind at the socket path
	te := setup(t, sleepForever, testOptions{})
	path := filepath.Join(t.TempDir(), "driver.sock")
	assert.NilError(t, os.WriteFile(path, nil, 0o600))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- te.srv.ListenAndServe(ctx, "unix://"+path) }()
	client := driverv1connect.NewDriverServiceClient(&http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}, "http://driver")

	// Act
	ok := poll(func() bool {
		_, err := client.Stop(t.Context(), &driverv1.StopRequest{Version: testVersion})
		return err == nil
	})

	// Assert - the server answered on the socket, and removed it on shutdown
	assert.Assert(t, ok, "server never came up")
	cancel()
	assert.NilError(t, <-errCh)
	_, err := os.Stat(path)
	assert.Assert(t, errors.Is(err, fs.ErrNotExist), "socket file not removed: %v", err)
}

// poll reports whether cond became true within five seconds.
func poll(cond func() bool) bool {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return false
}
