package runner_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/cenkalti/backoff/v5"
	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/backend/fake"
	"github.com/gritzapp/gritz/internal/runner/driverserver"
	"github.com/gritzapp/gritz/internal/runner/taskstate"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/outbox"
	"gotest.tools/v3/assert"
)

// testStore opens a fresh taskstate store in a temp dir, optionally seeded with
// records.
func testStore(t *testing.T, recs ...taskstate.Record) *taskstate.Store {
	t.Helper()
	s, err := taskstate.Open(t.TempDir())
	assert.NilError(t, err)
	for _, rec := range recs {
		assert.NilError(t, s.Write(rec))
	}
	return s
}

// testWorkspaces returns a minimal workspace config with a "test" workspace.
func testWorkspaces() *workspace.Config {
	return &workspace.Config{
		Workspaces: map[string]workspace.Workspace{
			"test": {
				Container: workspace.Container{Image: "alpine:latest"},
				Agent:     workspace.Agent{Type: "dummy", Dummy: &workspace.DummyConfig{}},
			},
		},
	}
}

// testManager returns a manager with the "test" workspace whose client mints
// task tokens, with reattach timings shortened for tests, and its HandleEvents
// running until the test ends. Each tune then adjusts the options of the
// manager's TaskRunners before they are built.
func testManager(t *testing.T, be backend.Backend, store *taskstate.Store, tune ...func(*runner.TaskRunnerOptions)) (*runner.TaskManager, *gritzclient.ClientMock, *outbox.Outbox[model.RunnerEvent]) {
	t.Helper()
	return newTestManager(t, runner.TaskManagerOptions{Backend: be, Store: store}, tune...)
}

// newTestManager is testManager with opts, whose Client, Queue, RunnerID,
// Concurrency, Workspaces and NewTaskRunner it sets: Workspaces only if opts
// has none.
func newTestManager(t *testing.T, opts runner.TaskManagerOptions, tune ...func(*runner.TaskRunnerOptions)) (*runner.TaskManager, *gritzclient.ClientMock, *outbox.Outbox[model.RunnerEvent]) {
	t.Helper()
	mock := &gritzclient.ClientMock{
		CreateTaskTokenFunc: func(_ context.Context, _ *gritzv1.CreateTaskTokenRequest) (*gritzv1.CreateTaskTokenResponse, error) {
			return &gritzv1.CreateTaskTokenResponse{Token: "t"}, nil
		},
		SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
			return &gritzv1.SubmitRunnerEventsResponse{}, nil
		},
	}
	queue, err := runner.NewRunnerEventOutbox(runner.RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	opts.Client = mock
	opts.Queue = queue
	opts.RunnerID = "test-runner"
	opts.Concurrency = 1
	if opts.Workspaces == nil {
		opts.Workspaces = testWorkspaces()
	}
	opts.NewTaskRunner = func(opts runner.TaskRunnerOptions) *runner.TaskRunner {
		opts.RetryInterval = 10 * time.Millisecond
		opts.RetryMaxInterval = 50 * time.Millisecond
		opts.LostTimeout = 10 * time.Second
		opts.ReadyTimeout = 10 * time.Second
		for _, f := range tune {
			f(&opts)
		}
		return runner.NewTaskRunner(opts)
	}
	m, err := runner.NewTaskManager(opts)
	assert.NilError(t, err)
	go m.HandleEvents(t.Context())
	return m, mock, queue
}

// submitted drives the outbox until it drains the persisted events, then returns
// the events it delivered through the mock client. The outbox's first Run pass
// delivers everything already persisted, so this exercises the same durable path
// production uses.
func submitted(t *testing.T, mock *gritzclient.ClientMock, queue *outbox.Outbox[model.RunnerEvent]) []*gritzv1.RunnerEvent {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go queue.Run(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for {
		n, err := queue.Len()
		assert.NilError(t, err)
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("outbox did not drain")
		}
		time.Sleep(time.Millisecond)
	}
	return mock.SubmittedRunnerEvents()
}

// waitQueued waits until the outbox holds n events. A TaskRunner's send
// returns once HandleEvents has received the event, which it enqueues only
// after.
func waitQueued(t *testing.T, queue *outbox.Outbox[model.RunnerEvent], n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, err := queue.Len()
		assert.NilError(t, err)
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("outbox holds %d events, want %d", got, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// assertRecord asserts the task's record in store: *want, or none with want
// nil.
func assertRecord(t *testing.T, store *taskstate.Store, taskID int64, want *taskstate.Record) {
	t.Helper()
	rec, ok, err := store.Read(taskID)
	assert.NilError(t, err)
	if want == nil {
		assert.Assert(t, !ok, "task %d has a record", taskID)
		return
	}
	assert.Assert(t, ok, "no record for task %d", taskID)
	assert.DeepEqual(t, rec, *want)
}

// probeVersion is the version assertTracked's probe Start runs.
const probeVersion = 1000

// assertTracked asserts the task's record, as assertRecord does, and that
// its TaskRunner holds sandbox want.ID, or, with want nil, no sandbox. It
// finds out by starting the TaskRunner, which changes its state and the
// record, so it is the last thing a test does with the task.
func assertTracked(t *testing.T, m *runner.TaskManager, be *fake.Backend, store *taskstate.Store, taskID int64, want *taskstate.Record) {
	t.Helper()
	assertRecord(t, store, taskID, want)
	before := be.Snapshot()
	err := m.Runner(taskID).Start(t.Context(), backend.Spec{TaskID: taskID, Version: probeVersion})
	after := be.Snapshot()
	assert.Assert(t, !errors.Is(err, runner.ErrRunning), "task %d's TaskRunner is claimed, so it cannot be probed", taskID)
	if want == nil {
		// A TaskRunner with no sandbox creates one.
		assert.Equal(t, len(after.Specs), len(before.Specs)+1, "task %d's TaskRunner holds a sandbox", taskID)
		return
	}
	// A TaskRunner holding want.ID starts it, and creates nothing.
	assert.NilError(t, err)
	assert.Equal(t, len(after.Specs), len(before.Specs), "task %d's TaskRunner held no sandbox", taskID)
	was, _ := before.Sandbox(want.ID)
	now, _ := after.Sandbox(want.ID)
	assert.Equal(t, now.Starts, was.Starts+1, "task %d's TaskRunner does not hold %s", taskID, want.ID)
}

// waitDialed waits until sandbox id's driver has been dialed at least n times.
func waitDialed(t *testing.T, be *fake.Backend, id string, n int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err := be.Wait(ctx, func(s fake.Snapshot) bool {
		sbx, _ := s.Sandbox(id)
		return sbx.Dials >= n
	})
	assert.NilError(t, err, "sandbox %s was not dialed %d times", id, n)
}

// drivers returns a fake.Options.Driver that gives every sandbox driver h.
func drivers(h http.Handler) func(backend.Handle) http.Handler {
	return func(backend.Handle) http.Handler { return h }
}

// newFake returns a fake backend whose every sandbox has driver h, closed
// when the test ends so its driver servers do not leak.
func newFake(t *testing.T, h http.Handler) *fake.Backend {
	t.Helper()
	be := fake.New(fake.Options{Driver: drivers(h)})
	t.Cleanup(func() { assert.NilError(t, be.Close()) })
	return be
}

// sleepForever is an agent config whose dummy agent runs until it is stopped.
var sleepForever = &agent.Config{Type: agent.TypeDummy, Dummy: &agent.DummyOptions{Sleep: -1}}

// serveDriver returns the handler of a driver server, and the test's own
// client to it over its own server. With cfg set, it starts run version of
// taskID with that agent config and closes the run's stream once the run is
// accepted, which leaves the run going; with cfg nil the driver has never
// run.
func serveDriver(t *testing.T, taskID, version int64, cfg *agent.Config) (http.Handler, driverv1connect.DriverServiceClient) {
	t.Helper()
	h, configs := newDriver(t, version, nil)
	hs := httptest.NewServer(h)
	t.Cleanup(hs.Close)
	client := driverv1connect.NewDriverServiceClient(hs.Client(), hs.URL)
	if cfg == nil {
		return h, client
	}
	assert.NilError(t, configs.Save(taskID, cfg))
	stream, err := client.Run(t.Context(), &driverv1.RunRequest{
		Version: version,
		Spec:    &driverv1.RunSpec{TaskId: taskID, ServerUrl: "http://gritz.test", Token: "token"},
	})
	assert.NilError(t, err)
	defer func() { assert.NilError(t, stream.Close()) }()
	assert.Assert(t, stream.Receive(), "Run: %v", stream.Err())
	assert.Assert(t, stream.Msg().GetAccepted() != nil, "first message is %v, want accepted", stream.Msg())
	return h, client
}

// driverHandler returns the handler of a driver server that has never run,
// for tasks on version. It reads the agent config from where the TaskManager's
// spec provisions it, so a Run with that spec runs the workspace's agent.
// Until ready is closed every call fails Unavailable, as from a driver that is
// not up yet; a nil ready is open. opts configure the driver.v1 handler, as
// an interceptor recording the requests the driver receives.
func driverHandler(t *testing.T, version int64, ready <-chan struct{}, opts ...connect.HandlerOption) http.Handler {
	t.Helper()
	h, _ := newDriver(t, version, ready, opts...)
	return h
}

// newDriver is driverHandler, also returning the store the driver reads agent
// configs from.
func newDriver(t *testing.T, version int64, ready <-chan struct{}, opts ...connect.HandlerOption) (http.Handler, agent.ConfigStore) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	configs := agent.ConfigStore(filepath.Join(root, string(agent.DefaultConfigStore)))
	gritz := &gritzclient.ClientMock{
		GetTaskFunc: func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
			return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Id: req.Id, Version: version}}, nil
		},
		SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
			return &gritzv1.SubmitRunnerEventsResponse{}, nil
		},
		ListEventsByTaskFunc: func(_ context.Context, _ *gritzv1.ListEventsByTaskRequest) (*gritzv1.ListEventsByTaskResponse, error) {
			return &gritzv1.ListEventsByTaskResponse{}, nil
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
		Config:     configs,
		FileRoot:   root,
		NewClient:  func(_, _ string) gritzclient.Client { return gritz },
		KeepAlive:  20 * time.Millisecond,
	})
	assert.NilError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		assert.NilError(t, srv.Shutdown(ctx))
	})
	mux := http.NewServeMux()
	mux.Handle(driverv1connect.NewDriverServiceHandler(srv, opts...))
	if ready == nil {
		return mux, configs
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		select {
		case <-ready:
			mux.ServeHTTP(w, req)
		default:
			http.Error(w, "driver is starting", http.StatusServiceUnavailable)
		}
	}), configs
}

// stopRun stops the running run version, retrying until the driver has it
// running.
func stopRun(t *testing.T, client driverv1connect.DriverServiceClient, version int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := client.Stop(t.Context(), &driverv1.StopRequest{Version: version})
		assert.NilError(t, err)
		if resp.GetStopped() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("run was never running")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitFinished waits until run version has finished, attaching to it over
// the test's client to the driver, and retrying while another Run stream is
// still open.
func waitFinished(t *testing.T, client driverv1connect.DriverServiceClient, version int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for {
		stream, err := client.Run(ctx, &driverv1.RunRequest{Version: version})
		assert.NilError(t, err)
		for stream.Receive() {
			if stream.Msg().GetFinished() != nil {
				assert.NilError(t, stream.Close())
				return
			}
		}
		err = stream.Err()
		assert.NilError(t, stream.Close())
		assert.Equal(t, connect.CodeOf(err), connect.CodeUnavailable, "stream ended before finished: %v", err)
		time.Sleep(5 * time.Millisecond)
	}
}
