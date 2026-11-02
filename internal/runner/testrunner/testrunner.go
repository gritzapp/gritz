// Package testrunner provides helpers for testing the runner: a TaskManager
// on a fake backend with a mock server, drivers serving real runs, an
// in-memory runner event log (Server) and the events tests add to it, and the
// waits and assertions the runner's tests share.
package testrunner

import (
	"cmp"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/cenkalti/backoff/v5"
	"github.com/gritzapp/gritz/internal/gritzclient"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/backend/fake"
	"github.com/gritzapp/gritz/internal/runner/driverserver"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/runrecord"
	"github.com/gritzapp/gritz/internal/runner/taskevents"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"gotest.tools/v3/assert"
)

const (
	// RunnerID is the runner a Manager runs as, and a CommandEvent is for.
	RunnerID = "test-runner"
	// Workspace is the workspace a Manager has by default, and a
	// CommandEvent's command runs in.
	Workspace = "test"
)

// SleepForever is an agent config whose dummy agent runs until it is stopped.
var SleepForever = &agent.Config{Type: agent.TypeDummy, Dummy: &agent.DummyOptions{Sleep: -1}}

// BackendOptions configures NewBackend. Zero values are replaced with
// sensible defaults.
type BackendOptions struct {
	// Driver is the test's client to every sandbox's driver; nil means the
	// drivers never answer.
	Driver driverv1connect.DriverServiceClient
	// Drivers overrides Driver for the sandboxes with these IDs.
	Drivers map[string]driverv1connect.DriverServiceClient
	// DriverNotReady is fake.Options.DriverNotReady.
	DriverNotReady bool
	// RunEnv is fake.Options.RunEnv.
	RunEnv func(*workspace.Workspace) map[string]string
}

// NewBackend returns a fake backend with no sandboxes. It is closed when the
// test ends, so the runner's open Run calls to its drivers are cut.
func NewBackend(t *testing.T, opts *BackendOptions) *fake.Backend {
	t.Helper()
	if opts == nil {
		opts = &BackendOptions{}
	}
	be := fake.New(fake.Options{
		Dial: func(h backend.Handle) driverv1connect.DriverServiceClient {
			if client, ok := opts.Drivers[h.ID]; ok {
				return client
			}
			return opts.Driver
		},
		RunEnv:         opts.RunEnv,
		DriverNotReady: opts.DriverNotReady,
	})
	t.Cleanup(func() { assert.NilError(t, be.Close()) })
	return be
}

// DriverOptions configures NewDriver. Zero values are replaced with sensible
// defaults.
type DriverOptions struct {
	// TaskID is the task whose run Agent starts; defaults to 1.
	TaskID int64
	// Version is the version the driver's server reports for every task, and
	// of the run Agent starts; defaults to 1.
	Version int64
	// Agent, when set, starts run Version of TaskID with this agent config
	// and leaves it going; when nil the driver has never run.
	Agent *agent.Config
	// EventID is the id of the start event Agent's run is started with,
	// which the driver stores; defaults to 1. A runner forwards the events
	// after it, so a test whose replica has the task's events gives that
	// start this id there too.
	EventID int64
}

// Driver is a driver server, reached through its embedded client.
type Driver struct {
	driverv1connect.DriverServiceClient
	// gritz is the server the driver's runs report to.
	gritz *gritzclient.ClientMock
}

// Reported returns the runner events the driver's runs reported, by name.
func (d *Driver) Reported() []string {
	var names []string
	for _, ev := range d.gritz.SubmittedRunnerEvents() {
		names = append(names, ev.GetEvent())
	}
	return names
}

// WaitStarted waits until the driver's runs have reported n starts.
func (d *Driver) WaitStarted(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		started := 0
		for _, name := range d.Reported() {
			if name == "started" {
				started++
			}
		}
		if started >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("driver reported %d runs started, want %d", started, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// NewDriver serves a driver server and returns it. The driver reads the
// agent config from where the TaskManager's spec provisions it, so a run it
// starts with that spec runs the workspace's agent. The server is shut down
// when the test ends.
func NewDriver(t *testing.T, opts *DriverOptions) *Driver {
	t.Helper()
	if opts == nil {
		opts = &DriverOptions{}
	}
	taskID := cmp.Or(opts.TaskID, 1)
	version := cmp.Or(opts.Version, 1)
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
		EventsPath: filepath.Join(dir, "events.jsonl"),
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
	mux.Handle(srv.Handler())
	d := &Driver{DriverServiceClient: serve(t, mux), gritz: gritz}
	if opts.Agent == nil {
		return d
	}
	// Start the run with a start event, and close its stream once the run
	// record says it is going, which leaves it going.
	assert.NilError(t, configs.Save(taskID, opts.Agent))
	stream := OpenRun(t, t.Context(), d, &driverv1.RunRequest{Msg: &driverv1.RunRequest_Spec{
		Spec: &driverv1.RunSpec{TaskId: taskID, ServerUrl: "http://gritz.test", Token: "token"},
	}})
	assert.NilError(t, stream.Send(&driverv1.RunRequest{Msg: &driverv1.RunRequest_Events{Events: &driverv1.TaskEvents{
		Events: []*gritzv1.Event{CommandEvent(cmp.Or(opts.EventID, 1), taskID, gritzv1.TaskCommand_START, version)},
	}}}))
	deadline := time.Now().Add(10 * time.Second)
	for {
		rec, _, err := runrecord.Read(filepath.Join(dir, "run.json"))
		assert.NilError(t, err)
		if rec.Version == version && rec.State == runrecord.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %d did not start", version)
		}
		time.Sleep(5 * time.Millisecond)
	}
	assert.NilError(t, stream.CloseResponse())
	return d
}

// serve serves h over HTTP/2, which the driver's bidi Run stream needs, until
// the test ends, and returns the test's client to it.
func serve(t *testing.T, h http.Handler) driverv1connect.DriverServiceClient {
	t.Helper()
	hs := httptest.NewUnstartedServer(h)
	hs.EnableHTTP2 = true
	hs.StartTLS()
	t.Cleanup(hs.Close)
	return driverv1connect.NewDriverServiceClient(hs.Client(), hs.URL)
}

// RunStream is the runner's end of a driver Run stream.
type RunStream = connect.BidiStreamForClientSimple[driverv1.RunRequest, driverv1.RunResponse]

// OpenRun opens a Run stream on client, sends first, and reads the driver's
// Hello. The stream is closed when the test ends.
func OpenRun(t *testing.T, ctx context.Context, client driverv1connect.DriverServiceClient, first *driverv1.RunRequest) *RunStream {
	t.Helper()
	stream, err := client.Run(ctx)
	assert.NilError(t, err)
	t.Cleanup(func() { _ = stream.CloseResponse() })
	err = stream.Send(first)
	assert.Assert(t, err == nil || errors.Is(err, io.EOF), "Send: %v", err)
	msg, err := stream.Receive()
	assert.NilError(t, err)
	assert.Assert(t, msg.GetHello() != nil, "first message is %v, want hello", msg)
	return stream
}

// NewStubDriver serves h as a driver and returns the test's client to it, so
// a test can script what a driver sends, including what the real one never
// does. h embeds driverv1connect.UnimplementedDriverServiceHandler for the
// calls it does not script. The server is shut down when the test ends.
func NewStubDriver(t *testing.T, h driverv1connect.DriverServiceHandler) driverv1connect.DriverServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(driverv1connect.NewDriverServiceHandler(h))
	return serve(t, mux)
}

// CommandEvent returns event id of task taskID, the command command at
// version, in the Workspace workspace.
func CommandEvent(id, taskID int64, command gritzv1.TaskCommand, version int64) *gritzv1.Event {
	return &gritzv1.Event{
		Id:     id,
		TaskId: taskID,
		Payload: &gritzv1.Event_Command{Command: &gritzv1.CommandPayload{
			Command:   command,
			Version:   version,
			Runner:    RunnerID,
			Workspace: Workspace,
		}},
	}
}

// ArchiveEvent returns event id of task taskID, which archives it.
func ArchiveEvent(id, taskID int64) *gritzv1.Event {
	return LifecycleEvent(id, taskID, gritzv1.LifecycleKind_LIFECYCLE_KIND_ARCHIVED)
}

// LifecycleEvent returns event id of task taskID, a lifecycle event of kind.
func LifecycleEvent(id, taskID int64, kind gritzv1.LifecycleKind) *gritzv1.Event {
	return &gritzv1.Event{
		Id:      id,
		TaskId:  taskID,
		Payload: &gritzv1.Event_Lifecycle{Lifecycle: &gritzv1.LifecyclePayload{Kind: kind}},
	}
}

// NewRunnerStore opens a fresh runnerstore in a temp dir, seeded with the
// sandbox records recs, and closed when the test ends.
func NewRunnerStore(t *testing.T, recs ...runnerstore.SandboxRecord) *runnerstore.Store {
	t.Helper()
	s, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	t.Cleanup(func() { assert.NilError(t, s.Close()) })
	for _, rec := range recs {
		assert.NilError(t, s.WriteSandbox(t.Context(), rec))
	}
	return s
}

// ManagerOptions configures NewManager. Zero values are replaced with
// sensible defaults.
type ManagerOptions struct {
	// Backend defaults to a NewBackend whose drivers never answer.
	Backend *fake.Backend
	// Server is the runner event log the manager replicates; defaults to an
	// empty one.
	Server *Server
	// RunnerStore holds the manager's replica of Server and its sandbox
	// records; defaults to a NewRunnerStore with no records.
	RunnerStore *runnerstore.Store
	// Workspaces defaults to a config with the Workspace workspace, whose
	// agent is a dummy.
	Workspaces *workspace.Config
	ServerURL  string
	// Concurrency defaults to 1.
	Concurrency int
	// PollInterval is the event log's; defaults to its own.
	PollInterval time.Duration
	// Fatal is runner.TaskManagerOptions.Fatal.
	Fatal context.CancelCauseFunc
	// QueueStore is the store behind the Manager's Queue; defaults to
	// RunnerStore.
	QueueStore *runnerstore.Store
	// TaskRunner, when set, adjusts the options of the manager's TaskRunners
	// before they are built, after their reattach timings are shortened.
	TaskRunner func(*runner.TaskRunnerOptions)
}

// Manager is a TaskManager with what a test needs to drive and observe it.
type Manager struct {
	*runner.TaskManager
	Backend *fake.Backend
	// Server is the runner event log the manager replicates once it runs; Add
	// appends to it.
	Server *Server
	// RunnerStore holds the manager's replica of Server and its sandbox
	// records.
	RunnerStore *runnerstore.Store
	// Client is the server. It serves Server's events, mints task tokens,
	// accepts runner events, and reports every task completed.
	Client *gritzclient.ClientMock
	// Queue is the manager's outbox, delivering to Client. Nothing runs it
	// until Submitted.
	Queue *runner.RunnerEventOutbox
}

// NewManager returns a Manager running as RunnerID.
func NewManager(t *testing.T, opts *ManagerOptions) *Manager {
	t.Helper()
	if opts == nil {
		opts = &ManagerOptions{}
	}
	m := &Manager{
		Backend:     opts.Backend,
		Server:      opts.Server,
		RunnerStore: opts.RunnerStore,
	}
	if m.Backend == nil {
		m.Backend = NewBackend(t, nil)
	}
	if m.Server == nil {
		m.Server = NewServer(ServerOptions{})
	}
	if m.RunnerStore == nil {
		m.RunnerStore = NewRunnerStore(t)
	}
	m.Client = m.Server.Client()
	m.Client.CreateTaskTokenFunc = func(_ context.Context, _ *gritzv1.CreateTaskTokenRequest) (*gritzv1.CreateTaskTokenResponse, error) {
		return &gritzv1.CreateTaskTokenResponse{Token: "t"}, nil
	}
	m.Client.SubmitRunnerEventsFunc = func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
		return &gritzv1.SubmitRunnerEventsResponse{}, nil
	}
	// Load's backstop finds every task not running.
	m.Client.GetTaskFunc = func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
		return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Id: req.Id, Status: gritzv1.TaskStatus_COMPLETED}}, nil
	}
	m.Queue = runner.NewRunnerEventOutbox(runner.RunnerEventOutboxOptions{
		Store:   cmp.Or(opts.QueueStore, m.RunnerStore),
		Client:  m.Client,
		Backoff: backoff.NewConstantBackOff(0),
		Log:     slog.Default(),
	})
	workspaces := opts.Workspaces
	if workspaces == nil {
		workspaces = &workspace.Config{
			Workspaces: map[string]workspace.Workspace{
				Workspace: {
					Container: workspace.Container{Image: "alpine:latest"},
					Agent:     workspace.Agent{Type: "dummy", Dummy: &workspace.DummyConfig{}},
				},
			},
		}
	}
	var err error
	m.TaskManager, err = runner.NewTaskManager(runner.TaskManagerOptions{
		Client:      m.Client,
		Backend:     m.Backend,
		ServerURL:   opts.ServerURL,
		Workspaces:  workspaces,
		Concurrency: cmp.Or(opts.Concurrency, 1),
		RunnerID:    RunnerID,
		Queue:       m.Queue,
		TaskLog: taskevents.New(taskevents.Options{
			Store:        m.RunnerStore,
			Client:       m.Client,
			RunnerID:     RunnerID,
			PollInterval: opts.PollInterval,
		}),
		RunnerStore: m.RunnerStore,
		Fatal:       opts.Fatal,
		NewTaskRunner: func(o runner.TaskRunnerOptions) *runner.TaskRunner {
			o.RetryInterval = 10 * time.Millisecond
			o.RetryMaxInterval = 50 * time.Millisecond
			o.LostTimeout = 10 * time.Second
			o.ReadyTimeout = 10 * time.Second
			if opts.TaskRunner != nil {
				opts.TaskRunner(&o)
			}
			return runner.NewTaskRunner(o)
		},
	})
	assert.NilError(t, err)
	return m
}

// Add appends events to the manager's Server, and has its event log fetch
// them. It may be called while the manager runs.
func (m *Manager) Add(events ...*gritzv1.Event) {
	m.Server.Add(events...)
	m.Wake()
}

// Run runs the manager, as TaskManager.Run does, until the test ends. Load
// must have returned first, if the test calls it.
func (m *Manager) Run(t *testing.T) {
	t.Helper()
	m.RunContext(t, t.Context())
}

// RunContext runs the manager until ctx is done, and waits for it to stop
// when the test ends.
func (m *Manager) RunContext(t *testing.T, ctx context.Context) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = m.TaskManager.Run(ctx)
	}()
	t.Cleanup(func() { <-done })
}

// Replicated stores events in the manager's replica as replication would,
// for a test that doesn't run the manager: a Run stream forwards them, and
// Load hands the tasks' stored state to their TaskRunners, but nothing
// reports them on the event log's Tasks.
func (m *Manager) Replicated(t *testing.T, events ...*gritzv1.Event) {
	t.Helper()
	_, err := m.RunnerStore.AppendEvents(t.Context(), events, "replicated")
	assert.NilError(t, err)
}

// Queued returns the number of events in the manager's outbox.
func (m *Manager) Queued(t *testing.T) int {
	t.Helper()
	n, err := m.Queue.Len()
	assert.NilError(t, err)
	return n
}

// WaitQueued waits until the manager's outbox holds n events.
func (m *Manager) WaitQueued(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := m.Queued(t)
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("outbox holds %d events, want %d", got, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// Submitted drains the manager's outbox, as Drain does, and returns every
// event the server was sent.
func (m *Manager) Submitted(t *testing.T) []*gritzv1.RunnerEvent {
	t.Helper()
	Drain(t, m.Queue)
	return m.Client.SubmittedRunnerEvents()
}

// Drain runs queue until it has delivered the events it holds. The outbox's
// first Run pass delivers everything already persisted, so this exercises the
// same durable path production uses.
func Drain(t *testing.T, queue *runner.RunnerEventOutbox) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go queue.Run(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for {
		n, err := queue.Len()
		assert.NilError(t, err)
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("outbox did not drain")
		}
		time.Sleep(time.Millisecond)
	}
}

// AssertRecord asserts the task's record in store: *want, or none with want
// nil.
func AssertRecord(t *testing.T, store *runnerstore.Store, taskID int64, want *runnerstore.SandboxRecord) {
	t.Helper()
	rec, ok, err := store.Sandbox(t.Context(), taskID)
	assert.NilError(t, err)
	if want == nil {
		assert.Assert(t, !ok, "task %d has a record", taskID)
		return
	}
	assert.Assert(t, ok, "no record for task %d", taskID)
	assert.DeepEqual(t, rec, *want)
}

// AssertRecord asserts the task's record in the manager's store, as the
// package's AssertRecord does.
func (m *Manager) AssertRecord(t *testing.T, taskID int64, want *runnerstore.SandboxRecord) {
	t.Helper()
	AssertRecord(t, m.RunnerStore, taskID, want)
}

// WaitRuns waits until the runner has made at least n Run calls on sandbox
// id's driver that went through. The fake counts a call once it can cut it,
// so the n-th stream can then be cut with Drop, Set or Suspend.
func WaitRuns(t *testing.T, be *fake.Backend, id string, n int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err := be.Wait(ctx, func(s fake.Snapshot) bool {
		sbx, _ := s.Sandbox(id)
		return sbx.Runs >= n
	})
	assert.NilError(t, err, "sandbox %s did not get %d Run calls", id, n)
}

// WaitSandbox waits until sandbox id in be satisfies fn, and returns it.
func WaitSandbox(t *testing.T, be *fake.Backend, id string, fn func(fake.Info) bool) fake.Info {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	snap, err := be.Wait(ctx, func(s fake.Snapshot) bool {
		sbx, ok := s.Sandbox(id)
		return ok && fn(sbx)
	})
	assert.NilError(t, err, "sandbox %s did not reach the state waited for", id)
	sbx, _ := snap.Sandbox(id)
	return sbx
}

// WaitSuspended waits until sandbox id in be is suspended, as after its busy
// period, and returns it.
func WaitSuspended(t *testing.T, be *fake.Backend, id string) fake.Info {
	t.Helper()
	return WaitSandbox(t, be, id, func(sbx fake.Info) bool { return sbx.State == fake.Suspended })
}
