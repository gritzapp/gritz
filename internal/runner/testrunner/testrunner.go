// Package testrunner provides helpers for testing the runner: a TaskManager
// on a fake backend with a mock server, drivers serving real runs, and the
// waits and assertions the runner's tests share.
package testrunner

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
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

const (
	// RunnerID is the runner a Manager runs as, and a Task is assigned to.
	RunnerID = "test-runner"
	// Workspace is the workspace a Manager has by default, and a Task uses.
	Workspace = "test"
	// ProbeVersion is the version AssertTracked's probe Start runs.
	ProbeVersion = 1000
)

// SleepForever is an agent config whose dummy agent runs until it is stopped.
var SleepForever = &agent.Config{Type: agent.TypeDummy, Dummy: &agent.DummyOptions{Sleep: -1}}

// NewStore opens a fresh taskstate store in a temp dir, seeded with recs.
func NewStore(t *testing.T, recs ...taskstate.Record) *taskstate.Store {
	t.Helper()
	s, err := taskstate.Open(t.TempDir())
	assert.NilError(t, err)
	for _, rec := range recs {
		assert.NilError(t, s.Write(rec))
	}
	return s
}

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
}

// NewDriver serves a driver server and returns the test's client to it. The
// driver reads the agent config from where the TaskManager's spec provisions
// it, so a Run with that spec runs the workspace's agent. The server is shut
// down when the test ends.
func NewDriver(t *testing.T, opts *DriverOptions) driverv1connect.DriverServiceClient {
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
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	client := driverv1connect.NewDriverServiceClient(hs.Client(), hs.URL)
	if opts.Agent == nil {
		return client
	}
	// Start the run, and close its stream once it is accepted, which leaves
	// the run going.
	assert.NilError(t, configs.Save(taskID, opts.Agent))
	stream, err := client.Run(t.Context(), &driverv1.RunRequest{
		Version: version,
		Spec:    &driverv1.RunSpec{TaskId: taskID, ServerUrl: "http://gritz.test", Token: "token"},
	})
	assert.NilError(t, err)
	defer func() { assert.NilError(t, stream.Close()) }()
	assert.Assert(t, stream.Receive(), "Run: %v", stream.Err())
	assert.Assert(t, stream.Msg().GetAccepted() != nil, "first message is %v, want accepted", stream.Msg())
	return client
}

// NewStubDriver serves h as a driver and returns the test's client to it, so
// a test can script what a driver sends, including what the real one never
// does. h embeds driverv1connect.UnimplementedDriverServiceHandler for the
// calls it does not script. The server is shut down when the test ends.
func NewStubDriver(t *testing.T, h driverv1connect.DriverServiceHandler) driverv1connect.DriverServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(driverv1connect.NewDriverServiceHandler(h))
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	return driverv1connect.NewDriverServiceClient(hs.Client(), hs.URL)
}

// TaskOptions configures Task. Zero values are replaced with sensible
// defaults.
type TaskOptions struct {
	// ID defaults to 1.
	ID int64
	// Version defaults to 1.
	Version int64
	Name    string
	// Status defaults to pending.
	Status model.TaskStatus
	// Command defaults to start.
	Command model.TaskCommand
}

// Task returns a task of the Workspace workspace, assigned to RunnerID.
func Task(opts *TaskOptions) *model.Task {
	if opts == nil {
		opts = &TaskOptions{}
	}
	return &model.Task{
		ID:        cmp.Or(opts.ID, 1),
		Name:      opts.Name,
		Runner:    RunnerID,
		Workspace: Workspace,
		Status:    cmp.Or(opts.Status, model.TaskStatusPending),
		Command:   cmp.Or(opts.Command, model.TaskCommandStart),
		Version:   cmp.Or(opts.Version, 1),
	}
}

// ManagerOptions configures NewManager. Zero values are replaced with
// sensible defaults.
type ManagerOptions struct {
	// Backend defaults to a NewBackend whose drivers never answer.
	Backend *fake.Backend
	// Store defaults to a NewStore with no records.
	Store *taskstate.Store
	// Workspaces defaults to a config with the Workspace workspace, whose
	// agent is a dummy.
	Workspaces *workspace.Config
	ServerURL  string
	// Concurrency defaults to 1.
	Concurrency int
	// PollInterval defaults to the TaskManager's default.
	PollInterval time.Duration
	// Fatal is runner.TaskManagerOptions.Fatal.
	Fatal context.CancelCauseFunc
	// QueueStore is the store behind the Manager's Queue; defaults to a file
	// store in a temp dir.
	QueueStore outbox.Store
	// TaskRunner, when set, adjusts the options of the manager's TaskRunners
	// before they are built, after their reattach timings are shortened.
	TaskRunner func(*runner.TaskRunnerOptions)
}

// Manager is a TaskManager with what a test needs to drive and observe it.
type Manager struct {
	*runner.TaskManager
	Backend *fake.Backend
	Store   *taskstate.Store
	// Client is the server. It mints task tokens, accepts runner events, and
	// lists the tasks SetTasks sets.
	Client *gritzclient.ClientMock
	// Queue is the manager's outbox, delivering to Client. Nothing runs it
	// until Submitted.
	Queue *outbox.Outbox[model.RunnerEvent]

	mu    sync.Mutex
	tasks []*model.Task
}

// NewManager returns a Manager running as RunnerID, with its HandleEvents
// running until the test ends.
func NewManager(t *testing.T, opts *ManagerOptions) *Manager {
	t.Helper()
	if opts == nil {
		opts = &ManagerOptions{}
	}
	m := &Manager{
		Backend: opts.Backend,
		Store:   opts.Store,
	}
	if m.Backend == nil {
		m.Backend = NewBackend(t, nil)
	}
	if m.Store == nil {
		m.Store = NewStore(t)
	}
	m.Client = &gritzclient.ClientMock{
		CreateTaskTokenFunc: func(_ context.Context, _ *gritzv1.CreateTaskTokenRequest) (*gritzv1.CreateTaskTokenResponse, error) {
			return &gritzv1.CreateTaskTokenResponse{Token: "t"}, nil
		},
		SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
			return &gritzv1.SubmitRunnerEventsResponse{}, nil
		},
		ListRunnerTasksFunc: func(context.Context, *gritzv1.ListRunnerTasksRequest) (*gritzv1.ListRunnerTasksResponse, error) {
			m.mu.Lock()
			defer m.mu.Unlock()
			resp := &gritzv1.ListRunnerTasksResponse{}
			for _, task := range m.tasks {
				resp.Tasks = append(resp.Tasks, task.Proto(""))
			}
			return resp, nil
		},
	}
	var err error
	m.Queue, err = runner.NewRunnerEventOutbox(runner.RunnerEventOutboxOptions{
		Store:    opts.QueueStore,
		StoreDir: t.TempDir(),
		Client:   m.Client,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
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
	m.TaskManager, err = runner.NewTaskManager(runner.TaskManagerOptions{
		Client:       m.Client,
		Backend:      m.Backend,
		Store:        m.Store,
		ServerURL:    opts.ServerURL,
		Workspaces:   workspaces,
		Concurrency:  cmp.Or(opts.Concurrency, 1),
		RunnerID:     RunnerID,
		Queue:        m.Queue,
		PollInterval: opts.PollInterval,
		Fatal:        opts.Fatal,
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
	go m.HandleEvents(t.Context())
	return m
}

// SetTasks sets the tasks the server lists for the runner, which Poll acts
// on. It may be called while the manager polls.
func (m *Manager) SetTasks(tasks ...*model.Task) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasks = slices.Clone(tasks)
}

// Queued returns the number of events in the manager's outbox.
func (m *Manager) Queued(t *testing.T) int {
	t.Helper()
	n, err := m.Queue.Len()
	assert.NilError(t, err)
	return n
}

// WaitQueued waits until the manager's outbox holds n events. A TaskRunner's
// send returns once HandleEvents has received the event, which it enqueues
// only after.
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
func Drain(t *testing.T, queue *outbox.Outbox[model.RunnerEvent]) {
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
func AssertRecord(t *testing.T, store *taskstate.Store, taskID int64, want *taskstate.Record) {
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

// AssertRecord asserts the task's record in the manager's store, as the
// package's AssertRecord does.
func (m *Manager) AssertRecord(t *testing.T, taskID int64, want *taskstate.Record) {
	t.Helper()
	AssertRecord(t, m.Store, taskID, want)
}

// AssertTracked asserts the task's record, as AssertRecord does, and that
// its TaskRunner holds sandbox want.ID, or, with want nil, no sandbox. It
// finds out by starting the TaskRunner at ProbeVersion, which changes its
// state and the record, so it is the last thing a test does with the task.
func (m *Manager) AssertTracked(t *testing.T, taskID int64, want *taskstate.Record) {
	t.Helper()
	m.AssertRecord(t, taskID, want)
	before := m.Backend.Snapshot()
	err := m.Runner(taskID).Start(t.Context(), backend.Spec{TaskID: taskID, Version: ProbeVersion})
	after := m.Backend.Snapshot()
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

// WaitRuns waits until the runner has made at least n Run calls on sandbox
// id's driver that went through. The fake counts a call once it can cut it,
// so the n-th stream can then be cut with Drop, Set or Park.
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

// StopRun stops the running run version over the test's client to the
// driver, retrying until the driver has it running.
func StopRun(t *testing.T, client driverv1connect.DriverServiceClient, version int64) {
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

// WaitFinished waits until run version has finished, attaching to it over
// the test's client to the driver, and retrying while another Run stream is
// still open.
func WaitFinished(t *testing.T, client driverv1connect.DriverServiceClient, version int64) {
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
