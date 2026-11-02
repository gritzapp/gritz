package driverserver

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/gritzclient"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/eventfile"
	"github.com/gritzapp/gritz/internal/runner/runrecord"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
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
	store := agent.ConfigStore(te.root(string(agent.DefaultConfigStore)))
	assert.NilError(t, store.Save(testTaskID, cfg))
	// The backend creates the sandbox's /gritz directory.
	assert.NilError(t, os.MkdirAll(te.root("/gritz"), 0o777))
	if opts.Record != nil {
		assert.NilError(t, runrecord.Write(te.root(runrecord.Path), *opts.Record))
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
		ListLinksFunc: func(_ context.Context, _ *gritzv1.ListLinksRequest) (*gritzv1.ListLinksResponse, error) {
			return &gritzv1.ListLinksResponse{}, nil
		},
		AppendLogChunkFunc: func(_ context.Context, _ *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}
	srv, err := New(Options{
		PrefixPath: te.dir,
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

// specReq is the first message of a stream that brings spec.
func specReq(spec *driverv1.RunSpec) *driverv1.RunRequest {
	return &driverv1.RunRequest{Msg: &driverv1.RunRequest_Spec{Spec: spec}}
}

// attachReq is the first message of a stream that brings no spec.
func attachReq() *driverv1.RunRequest {
	return &driverv1.RunRequest{}
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

// command returns event id of the test task, the command cmd at version.
func command(id int64, cmd gritzv1.TaskCommand, version int64) *gritzv1.Event {
	return &gritzv1.Event{
		Id:     id,
		TaskId: testTaskID,
		Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{
			Command: cmd,
			Version: version,
		}},
	}
}

// start, restart, stop and none return event id of the test task, that
// command at version.
func start(id, version int64) *gritzv1.Event {
	return command(id, gritzv1.TaskCommand_START, version)
}

func restart(id, version int64) *gritzv1.Event {
	return command(id, gritzv1.TaskCommand_RESTART, version)
}

func stop(id, version int64) *gritzv1.Event {
	return command(id, gritzv1.TaskCommand_STOP, version)
}

func none(id, version int64) *gritzv1.Event {
	return command(id, gritzv1.TaskCommand_NONE, version)
}

// event returns event id of the test task, an instruction, which is no
// command.
func event(id int64) *gritzv1.Event {
	return &gritzv1.Event{
		Id:      id,
		TaskId:  testTaskID,
		Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{Text: "hi"}},
	}
}

// run opens a Run stream with the test spec and sends events as its first
// batch.
func (te *testEnv) run(t *testing.T, events ...*gritzv1.Event) *runStream {
	t.Helper()
	stream, _ := te.open(t, specReq(testSpec()))
	send(t, stream, events...)
	return stream
}

// waitRecord waits until the run record is want.
func (te *testEnv) waitRecord(t *testing.T, version int64, state runrecord.State) {
	t.Helper()
	var rec runrecord.Record
	ok := poll(func() bool {
		rec, _ = te.record(t)
		return rec.Version == version && rec.State == state
	})
	assert.Assert(t, ok, "record is %+v, want version %d %v", rec, version, state)
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
	rec, ok, err := runrecord.Read(te.root(runrecord.Path))
	assert.NilError(t, err)
	return rec, ok
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

func TestRun_NothingToDo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		events []*gritzv1.Event
	}{
		{"no events", nil},
		{"no command", []*gritzv1.Event{event(1)}},
		{"stop", []*gritzv1.Event{stop(1, testVersion)}},
		{"withdrawn start", []*gritzv1.Event{start(1, testVersion), none(2, testVersion)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})

			stream := te.run(t, tt.events...)

			// Idle at once: the driver has never run
			assert.DeepEqual(t, idle(t, stream), &driverv1.Idle{}, protocmp.Transform())
			assert.Equal(t, len(te.dials()), 0)
		})
	}
}

// root returns the path of a sandbox path under the test's prefix.
func (te *testEnv) root(path string) string {
	return filepath.Join(te.dir, path)
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
	stream, _ := te.open(t, specReq(spec))
	send(t, stream, start(1, testVersion))

	// Assert - the run finished unreported without reaching the agent, and
	// the sandbox is not marked provisioned
	assert.DeepEqual(t, idle(t, stream), &driverv1.Idle{
		Version:  testVersion,
		Reported: false,
	}, protocmp.Transform())
	assert.Equal(t, len(te.dials()), 0)
	_, err := os.Stat(te.root(ProvisionedPath))
	assert.Assert(t, errors.Is(err, os.ErrNotExist), err)
}

func TestRun_PendingStart(t *testing.T) {
	t.Parallel()
	// Arrange - run 5, which takes a second, on a stream
	te := setup(t, &agent.Config{Type: agent.TypeDummy, Dummy: &agent.DummyOptions{Sleep: 1}}, testOptions{})
	stream := te.run(t, start(1, 5))
	te.waitRecord(t, 5, runrecord.Running)

	// Act - a start of run 6 while run 5 is in progress
	send(t, stream, none(2, 5), start(3, 6))

	// Assert - run 6 started once run 5 finished, on the same stream, which
	// went idle only after it
	assert.DeepEqual(t, idle(t, stream), &driverv1.Idle{
		Version:  6,
		Reported: true,
	}, protocmp.Transform())
	assert.Equal(t, len(te.dials()), 2)
	assert.Equal(t, len(te.gritz.SubmittedRunnerEvents()), 4)
}

func TestRun_StopSupersedesPendingStart(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pending *gritzv1.Event
	}{
		{"start", start(2, 6)},
		{"restart", restart(2, 6)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - run 5 in progress, and run 6 pending
			te := setup(t, sleepForever, testOptions{})
			stream := te.run(t, start(1, 5))
			te.waitRecord(t, 5, runrecord.Running)

			// Act - the stop that cancels the task: it carries the task's
			// version, 6
			send(t, stream, tt.pending, stop(3, 6))

			// Assert - run 5 was stopped, and run 6 never started
			assert.DeepEqual(t, idle(t, stream), &driverv1.Idle{
				Version:  5,
				Reported: true,
			}, protocmp.Transform())
			assert.Equal(t, len(te.dials()), 1)
		})
	}
}

func TestRun_PendingAfterDrop(t *testing.T) {
	t.Parallel()
	// Arrange - run 5, which takes a second, with run 6 pending, on a
	// stream that drops
	te := setup(t, &agent.Config{Type: agent.TypeDummy, Dummy: &agent.DummyOptions{Sleep: 1}}, testOptions{})
	stream := te.run(t, start(1, 5))
	te.waitRecord(t, 5, runrecord.Running)
	send(t, stream, start(2, 6))
	te.waitStored(t, 1, 2)
	assert.NilError(t, stream.CloseResponse())

	// Act - wait out run 5 with no stream open
	te.waitRecord(t, 6, runrecord.Running)

	// Assert - run 6 started with the spec of the dropped stream, and a
	// reopened stream sees it through
	stream, _ = te.open(t, attachReq())
	send(t, stream)
	assert.DeepEqual(t, idle(t, stream), &driverv1.Idle{
		Version:  6,
		Reported: true,
	}, protocmp.Transform())
	assert.DeepEqual(t, te.dials(), []string{testServerURL + " " + testToken, testServerURL + " " + testToken})
}

func TestRun_NoSpec(t *testing.T) {
	t.Parallel()
	// Arrange - a driver that has never had a spec
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})

	// Act - a start, on a stream that brings none
	stream, _ := te.open(t, attachReq())
	send(t, stream, start(1, testVersion))

	// Assert - Idle at once, so the runner can bring one
	assert.DeepEqual(t, idle(t, stream), &driverv1.Idle{}, protocmp.Transform())

	// Act - a stream with a spec
	stream = te.run(t)

	// Assert - the stored start ran
	assert.DeepEqual(t, idle(t, stream), &driverv1.Idle{
		Version:  testVersion,
		Reported: true,
	}, protocmp.Transform())
}

func TestRun_WaitsForFirstBatch(t *testing.T) {
	t.Parallel()
	// Arrange - a driver with nothing to do
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{})
	stream, _ := te.open(t, specReq(testSpec()))

	// Act - no batch yet
	msg, err := stream.Receive()

	// Assert - only keep-alives until the first batch
	assert.NilError(t, err)
	assert.Assert(t, msg.GetKeepAlive() != nil, "got %v, want keep_alive", msg)
	send(t, stream)
	assert.DeepEqual(t, idle(t, stream), &driverv1.Idle{}, protocmp.Transform())
}

func TestRun_EventsSurviveRestart(t *testing.T) {
	t.Parallel()
	// Arrange - a driver that stored events, then went away
	te := setup(t, sleepForever, testOptions{})
	stream := te.run(t, start(1, testVersion), event(5), event(3))
	te.waitStored(t, 1, 5, 3)
	assert.NilError(t, te.srv.Shutdown(t.Context()))
	idle(t, stream)

	// Act - a new driver process on the same sandbox
	restarted := setup(t, sleepForever, testOptions{Dir: te.dir})
	_, hello := restarted.open(t, specReq(testSpec()))

	// Assert - it kept the events, and says where they end
	assert.Equal(t, hello.GetLastEventId(), int64(3))
	restarted.waitStored(t, 1, 5, 3)
}

func TestRun_KeepAlive(t *testing.T) {
	t.Parallel()
	// Arrange
	te := setup(t, sleepForever, testOptions{})
	stream := te.run(t, start(1, testVersion))

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
	te.run(t, start(1, testVersion))

	// Act
	_, _, err := te.call(t, attachReq())

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnavailable)
}

func TestNew_PendingAfterRestart(t *testing.T) {
	t.Parallel()
	// Arrange - the previous process stored a start and died before it
	// acted on it
	dir := t.TempDir()
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, "gritz"), 0o777))
	events, err := eventfile.Open(filepath.Join(dir, eventfile.Path))
	assert.NilError(t, err)
	_, err = events.Append([]*gritzv1.Event{start(1, testVersion)})
	assert.NilError(t, err)
	te := setup(t, &agent.Config{Type: agent.TypeDummy}, testOptions{Dir: dir})

	// Act - a stream with nothing new
	stream := te.run(t)

	// Assert - the stored start ran
	assert.DeepEqual(t, idle(t, stream), &driverv1.Idle{
		Version:  testVersion,
		Reported: true,
	}, protocmp.Transform())
	assert.Equal(t, len(te.dials()), 1)
}

func TestShutdown(t *testing.T) {
	t.Parallel()
	// Arrange - a running run with its stream open
	te := setup(t, sleepForever, testOptions{})
	stream := te.run(t, start(1, testVersion))
	te.waitRecord(t, testVersion, runrecord.Running)

	// Act
	assert.NilError(t, te.srv.Shutdown(t.Context()))

	// Assert - the run was stopped and reported before Shutdown returned
	assert.DeepEqual(t, te.gritz.SubmittedRunnerEvents(), runnerEvents("started", "stopped"), protocmp.Transform())
	assert.DeepEqual(t, idle(t, stream), &driverv1.Idle{
		Version:  testVersion,
		Reported: true,
	}, protocmp.Transform())

	// Assert - no stream opens afterwards
	_, _, err := te.call(t, specReq(testSpec()))
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnavailable)
	assert.ErrorContains(t, err, "shutting down")
}

func TestShutdown_Pending(t *testing.T) {
	t.Parallel()
	// Arrange - run 5 in progress with run 6 pending
	te := setup(t, sleepForever, testOptions{})
	stream := te.run(t, start(1, 5))
	te.waitRecord(t, 5, runrecord.Running)
	send(t, stream, start(2, 6))
	te.waitStored(t, 1, 2)

	// Act
	assert.NilError(t, te.srv.Shutdown(t.Context()))

	// Assert - run 6 never starts, and the stream goes idle
	assert.DeepEqual(t, idle(t, stream), &driverv1.Idle{
		Version:  5,
		Reported: true,
	}, protocmp.Transform())
	assert.Equal(t, len(te.dials()), 1)
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

// poll reports whether cond became true within five seconds.
func poll(cond func() bool) bool {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return false
}
