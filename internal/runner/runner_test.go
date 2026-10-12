package runner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/cenkalti/backoff/v5"
	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/proto/gritz/v1/gritzv1connect"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/backend/docker"
	"github.com/gritzapp/gritz/internal/runner/driverserver"
	"github.com/gritzapp/gritz/internal/runner/taskstate"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/dockerx"
	"github.com/gritzapp/gritz/internal/x/outbox"
	"github.com/gritzapp/gritz/internal/x/testx"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
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

func TestRunnerStart(t *testing.T) {
	abs, err := filepath.Abs("../../prebuilt")
	assert.NilError(t, err)
	t.Setenv("GRITZ_PREBUILT_DIR", abs)

	task := &model.Task{
		ID:        1,
		Name:      "test-task",
		Runner:    "test-runner",
		Workspace: "test",
		Status:    model.TaskStatusPending,
		Command:   model.TaskCommandStart,
		Version:   1,
	}

	// Create mock client. The driver and the injected MCP server now connect to
	// this server directly (over the host network), so it must answer the
	// driver's started/stopped events, its first-run brief (ListLinks), and the
	// agent's get_my_task (GetTask + ListEventsByTask + ListLinks).
	mock := &gritzclient.ClientMock{
		CreateTaskTokenFunc: func(_ context.Context, req *gritzv1.CreateTaskTokenRequest) (*gritzv1.CreateTaskTokenResponse, error) {
			return &gritzv1.CreateTaskTokenResponse{Token: "test-token"}, nil
		},
		SubmitRunnerEventsFunc: func(_ context.Context, req *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
			return &gritzv1.SubmitRunnerEventsResponse{}, nil
		},
		// The driver reads its task at startup to fork on shell_session; this
		// task has none, so it takes the normal agent path.
		GetTaskFunc: func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
			return &gritzv1.GetTaskResponse{Task: task.Proto("")}, nil
		},
		// The driver drains the event stream before running the agent; an empty
		// page (more=false) makes the drain a single no-op call.
		ListEventsByTaskFunc: func(_ context.Context, req *gritzv1.ListEventsByTaskRequest) (*gritzv1.ListEventsByTaskResponse, error) {
			return &gritzv1.ListEventsByTaskResponse{}, nil
		},
		// The driver fetches the task's standing links for the first-run brief.
		ListLinksFunc: func(_ context.Context, req *gritzv1.ListLinksRequest) (*gritzv1.ListLinksResponse, error) {
			return &gritzv1.ListLinksResponse{}, nil
		},
		// The driver ships its log to the server.
		AppendLogChunkFunc: func(_ context.Context, _ *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
			return &gritzv1.AppendLogChunkResponse{}, nil
		},
	}

	dc, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	assert.NilError(t, err)
	defer dc.Close()

	// Create httptest server with the mock. The driver connects to the server
	// directly over the network, so the container shares the host network
	// namespace (NetworkMode "host") to reach it on 127.0.0.1, and ServerURL
	// is the same URL.
	_, handler := gritzv1connect.NewGritzServiceHandler(mock)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	client := gritzclient.New(gritzclient.Options{BaseURL: ts.URL})
	be, err := docker.New(docker.Options{RunnerID: "test-runner"})
	assert.NilError(t, err)
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   client,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	r, err := New(Options{
		Client:    client,
		Backend:   be,
		Store:     testStore(t),
		ServerURL: ts.URL,
		Queue:     queue,
		Workspaces: &workspace.Config{
			Workspaces: map[string]workspace.Workspace{
				"test": {
					Container: workspace.Container{
						Image:       "alpine:latest",
						NetworkMode: "host",
					},
					Agent: workspace.Agent{
						Type: "dummy",
						Dummy: &workspace.DummyConfig{
							Sleep: 1,
							ToolCalls: []agent.DummyToolCall{
								{Server: "gritz", Name: "get_my_task"},
							},
						},
					},
				},
			},
		},
		Concurrency: 1,
		RunnerID:    "test-runner",
	})
	assert.NilError(t, err)
	t.Cleanup(func() { r.Close() })

	// Remove any leftover container from a previous aborted run.
	_ = dc.ContainerRemove(t.Context(), "gritz-1", container.RemoveOptions{Force: true})

	// Start a task
	err = r.Start(t.Context(), task)
	assert.NilError(t, err)

	// The runner records the launched handle in the store.
	rec, ok, err := r.store.Read(task.ID)
	assert.NilError(t, err)
	assert.Equal(t, ok, true)
	assert.Equal(t, rec.Type, docker.HandleType)
	assert.Assert(t, rec.ID != "")

	// Wait for the container to exit
	err = dockerx.ContainerWait(t.Context(), dc, "gritz-1", container.WaitConditionNotRunning)
	assert.NilError(t, err)

	// Remove the container
	err = dc.ContainerRemove(t.Context(), "gritz-1", container.RemoveOptions{})
	assert.NilError(t, err)

	// Verify ListLinks was called twice — once by the driver's first-run brief
	// and once by the dummy agent's get_my_task tool call.
	assert.Assert(t, cmp.Len(mock.ListLinksCalls(), 2))

	// Verify the driver reported its own lifecycle: started, then stopped.
	assert.DeepEqual(t, testx.ExtractField(mock.SubmittedRunnerEvents(), "Event"), []string{"started", "stopped"})
}

// TestRunnerSpec_PreCreatesLogDir asserts the sandbox spec ships a directory
// entry for the driver's /gritz/log parent, created 0777 so a non-root driver
// can write the append-only log file there (the driver's MkdirAll is only a
// fallback).
func TestRunnerSpec_PreCreatesLogDir(t *testing.T) {
	t.Parallel()

	mock := &gritzclient.ClientMock{
		CreateTaskTokenFunc: func(_ context.Context, req *gritzv1.CreateTaskTokenRequest) (*gritzv1.CreateTaskTokenResponse, error) {
			return &gritzv1.CreateTaskTokenResponse{Token: "test-token"}, nil
		},
	}
	client := gritzclient.New(gritzclient.Options{BaseURL: "http://localhost"})
	be, err := docker.New(docker.Options{RunnerID: "test-runner"})
	assert.NilError(t, err)
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   client,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	r, err := New(Options{
		Client:    mock,
		Backend:   be,
		Store:     testStore(t),
		ServerURL: "http://localhost",
		Queue:     queue,
		Workspaces: &workspace.Config{
			Workspaces: map[string]workspace.Workspace{
				"test": {
					Container: workspace.Container{Image: "alpine:latest"},
					Agent:     workspace.Agent{Type: "dummy"},
				},
			},
		},
		Concurrency: 1,
		RunnerID:    "test-runner",
	})
	assert.NilError(t, err)
	t.Cleanup(func() { r.Close() })

	task := &model.Task{ID: 1, Runner: "test-runner", Workspace: "test", Version: 1}
	spec, err := r.spec(t.Context(), task)
	assert.NilError(t, err)

	logDir := filepath.Dir(agent.DefaultLogPath)
	var found bool
	for _, f := range spec.Files {
		if f.Path == logDir {
			found = true
			assert.Assert(t, f.Dir)
			assert.Equal(t, f.Mode, int64(0o777))
		}
	}
	assert.Assert(t, found, "spec should pre-create the log dir %q", logDir)
}

// TestRunnerSpec_Secrets asserts the runner hands the backend the server URL,
// the task token and the declared workspace secrets in their own fields, and
// gives the run the GRITZ_* environment the agent's tools read. The secrets stay
// out of Env: the driver adds them to the run's environment and masks them in
// the log it ships.
func TestRunnerSpec_Secrets(t *testing.T) {
	t.Parallel()

	mock := &gritzclient.ClientMock{
		CreateTaskTokenFunc: func(_ context.Context, req *gritzv1.CreateTaskTokenRequest) (*gritzv1.CreateTaskTokenResponse, error) {
			return &gritzv1.CreateTaskTokenResponse{Token: "test-token"}, nil
		},
	}
	client := gritzclient.New(gritzclient.Options{BaseURL: "http://localhost"})
	be, err := docker.New(docker.Options{RunnerID: "test-runner"})
	assert.NilError(t, err)
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   client,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	r, err := New(Options{
		Client:    mock,
		Backend:   be,
		Store:     testStore(t),
		ServerURL: "http://localhost",
		Queue:     queue,
		Workspaces: &workspace.Config{
			Workspaces: map[string]workspace.Workspace{
				"test": {
					Container: workspace.Container{Image: "alpine:latest"},
					Agent:     workspace.Agent{Type: "dummy"},
					Secrets: map[string]string{
						"NPM_TOKEN": "npm_secretvalue",
						"GH_TOKEN":  "gho_secretvalue",
					},
				},
			},
		},
		Concurrency: 1,
		RunnerID:    "test-runner",
	})
	assert.NilError(t, err)
	t.Cleanup(func() { r.Close() })

	task := &model.Task{ID: 1, Runner: "test-runner", Workspace: "test", Version: 1}
	spec, err := r.spec(t.Context(), task)
	assert.NilError(t, err)

	assert.Equal(t, spec.ServerURL, "http://localhost")
	assert.Equal(t, spec.Token, "test-token")
	assert.DeepEqual(t, spec.Env, map[string]string{
		"GRITZ_TASK_ID": "1",
		"GRITZ_TOKEN":   "test-token",
		"GRITZ_SERVER":  "http://localhost",
	})
	assert.DeepEqual(t, spec.Secrets, map[string]string{
		"GH_TOKEN":  "gho_secretvalue",
		"NPM_TOKEN": "npm_secretvalue",
	})
}

// runEnvBackend is a backend whose sandboxes have no environment of their own.
type runEnvBackend struct {
	*backend.BackendMock
	env map[string]string
}

func (b runEnvBackend) RunEnv(*workspace.Workspace) map[string]string { return b.env }

// TestRunnerRunSpec_RunEnv asserts a backend's RunEnv reaches the run's
// environment under the runner's own.
func TestRunnerRunSpec_RunEnv(t *testing.T) {
	t.Parallel()
	be := runEnvBackend{
		BackendMock: &backend.BackendMock{},
		env:         map[string]string{"FOO": "bar", "GRITZ_TASK_ID": "shadowed"},
	}
	r, err := New(Options{Backend: be, Store: testStore(t), RunnerID: "test-runner"})
	assert.NilError(t, err)

	rs := r.runSpec(&backend.Spec{TaskID: 1, Env: map[string]string{"GRITZ_TASK_ID": "1"}})

	assert.DeepEqual(t, rs.Env, map[string]string{"FOO": "bar", "GRITZ_TASK_ID": "1"})
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

// TestRunnerEventsSurviveRestart is the durability payoff of this layer: events
// persisted by one runner process are redelivered by the next, with no separate
// recovery path — the reopened outbox's first Run pass drains them in FIFO order.
func TestRunnerEventsSurviveRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// A runner persists two lifecycle events, then "crashes" before delivering
	// them: this outbox is never Run, so nothing reaches the server.
	crashed, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: dir,
		Client:   &gritzclient.ClientMock{},
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	assert.NilError(t, crashed.Enqueue(model.RunnerEvent{TaskID: 1, Event: model.RunnerEventStarted}))
	assert.NilError(t, crashed.Enqueue(model.RunnerEvent{TaskID: 2, Event: model.RunnerEventFailed, Version: 5, Reason: "boom"}))

	// The new process re-opens the same durable dir and delivers on startup.
	mock := &gritzclient.ClientMock{
		SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
			return &gritzv1.SubmitRunnerEventsResponse{}, nil
		},
	}
	restarted, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: dir,
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)

	// The events survived the restart and are redelivered in FIFO order, with
	// their TaskId/Version/Reason intact.
	events := submitted(t, mock, restarted)
	assert.DeepEqual(t, events, []*gritzv1.RunnerEvent{
		{Event: "started", TaskId: 1},
		{Event: "failed", TaskId: 2, Version: 5, Reason: "boom"},
	}, protocmp.Transform())
}

func TestRunnerPoll_StopWithoutSandbox(t *testing.T) {
	t.Parallel()
	// Arrange - task being cancelled with no tracked sandbox.
	task := &model.Task{
		ID:        7,
		Runner:    "test-runner",
		Workspace: "test",
		Status:    model.TaskStatusCancelling,
		Command:   model.TaskCommandStop,
		Version:   3,
	}
	mock := &gritzclient.ClientMock{
		ListRunnerTasksFunc: func(_ context.Context, _ *gritzv1.ListRunnerTasksRequest) (*gritzv1.ListRunnerTasksResponse, error) {
			return &gritzv1.ListRunnerTasksResponse{Tasks: []*gritzv1.Task{task.Proto("")}}, nil
		},
		SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
			return &gritzv1.SubmitRunnerEventsResponse{}, nil
		},
	}
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	r, err := New(Options{Client: mock, Backend: &backend.BackendMock{}, Store: testStore(t), RunnerID: "test-runner", Concurrency: 1, Queue: queue})
	assert.NilError(t, err)

	// Act
	err = r.Poll(t.Context())
	assert.NilError(t, err)

	// Assert - no record, so there is no driver to stop and the runner emits
	// "stopped" itself.
	events := submitted(t, mock, queue)
	assert.Equal(t, len(events), 1)
	assert.DeepEqual(t,
		events[0],
		&gritzv1.RunnerEvent{Event: "stopped", TaskId: 7, Version: 3},
		protocmp.Transform(),
	)
}

func TestRunnerPoll_StopSignalled(t *testing.T) {
	t.Parallel()
	// Arrange - task being cancelled with a tracked, running sandbox.
	task := &model.Task{
		ID:        7,
		Runner:    "test-runner",
		Workspace: "test",
		Status:    model.TaskStatusCancelling,
		Command:   model.TaskCommandStop,
		Version:   3,
	}
	mock := &gritzclient.ClientMock{
		ListRunnerTasksFunc: func(_ context.Context, _ *gritzv1.ListRunnerTasksRequest) (*gritzv1.ListRunnerTasksResponse, error) {
			return &gritzv1.ListRunnerTasksResponse{Tasks: []*gritzv1.Task{task.Proto("")}}, nil
		},
	}
	client, stream := testDriver(t, 7, 3, sleepForever)
	sb := &backend.SandboxMock{
		StatusFunc: func(context.Context) (backend.Status, error) { return backend.StatusUp, nil },
		DialFunc: func(context.Context, string) (driverv1connect.DriverServiceClient, error) {
			return client, nil
		},
	}
	be := &backend.BackendMock{
		OpenFunc: func(backend.Handle) (backend.Sandbox, error) { return sb, nil },
	}
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	r, err := New(Options{Client: mock, Backend: be, Store: testStore(t), RunnerID: "test-runner", Concurrency: 1, Queue: queue})
	assert.NilError(t, err)
	seed(t, r, taskstate.Record{TaskID: 7, Version: 3, Type: "docker", ID: "c7"})

	// Act
	err = r.Poll(t.Context())
	assert.NilError(t, err)

	// Assert - the driver stopped the run and owns the terminal report.
	waitFinished(t, stream)
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
}

// testSandbox returns a sandbox whose driver is client and which is up until it
// is parked. parked is closed by its first Park.
func testSandbox(client driverv1connect.DriverServiceClient) (sb *backend.SandboxMock, parked <-chan struct{}) {
	var once sync.Once
	ch := make(chan struct{})
	sb = &backend.SandboxMock{
		HandleFunc: func() backend.Handle { return backend.Handle{Type: "docker", ID: "c1"} },
		DialFunc: func(context.Context, string) (driverv1connect.DriverServiceClient, error) {
			return client, nil
		},
		StatusFunc: func(context.Context) (backend.Status, error) {
			select {
			case <-ch:
				return backend.StatusParked, nil
			default:
				return backend.StatusUp, nil
			}
		},
		ParkFunc: func(context.Context) error {
			once.Do(func() { close(ch) })
			return nil
		},
		DestroyFunc: func(context.Context) error { return nil },
	}
	return sb, ch
}

// testSupervisor returns a runner with the "test" workspace whose client mints
// task tokens, with reattach timings shortened for tests.
func testSupervisor(t *testing.T, be backend.Backend, store *taskstate.Store) (*Runner, *gritzclient.ClientMock, *outbox.Outbox[model.RunnerEvent]) {
	t.Helper()
	mock := &gritzclient.ClientMock{
		CreateTaskTokenFunc: func(_ context.Context, _ *gritzv1.CreateTaskTokenRequest) (*gritzv1.CreateTaskTokenResponse, error) {
			return &gritzv1.CreateTaskTokenResponse{Token: "t"}, nil
		},
		SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
			return &gritzv1.SubmitRunnerEventsResponse{}, nil
		},
	}
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	r, err := New(Options{Client: mock, Backend: be, Store: store, RunnerID: "test-runner", Concurrency: 1, Queue: queue, Workspaces: testWorkspaces()})
	assert.NilError(t, err)
	r.retryInterval = 10 * time.Millisecond
	r.retryMaxInterval = 50 * time.Millisecond
	r.lostTimeout = 10 * time.Second
	r.readyTimeout = 10 * time.Second
	return r, mock, queue
}

// supervise starts following run version of taskID on sb, as Start does once
// the record is written, and returns the task's entry.
func supervise(t *testing.T, ctx context.Context, r *Runner, taskID, version int64, sb backend.Sandbox) *taskEntry {
	t.Helper()
	assert.Assert(t, r.sem.TryAcquire(1)) // the slot supervise will release
	e := track(t, r, taskstate.Record{TaskID: taskID, Version: version, Type: "docker", ID: "c1"}, sb)
	e.mu.Lock()
	defer e.mu.Unlock()
	r.supervise(ctx, e, nil)
	return e
}

// track registers rec's task as Load does: rec in the store, and an entry for
// it holding sb.
func track(t *testing.T, r *Runner, rec taskstate.Record, sb backend.Sandbox) *taskEntry {
	t.Helper()
	assert.NilError(t, r.store.Write(rec))
	e := &taskEntry{rec: rec, sb: sb}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks[rec.TaskID] = e
	return e
}

// seed tracks recs, each with the sandbox the runner's backend opens for its
// handle, as Load does without probing the sandboxes.
func seed(t *testing.T, r *Runner, recs ...taskstate.Record) {
	t.Helper()
	for _, rec := range recs {
		sb, err := r.backend.Open(recordHandle(rec))
		assert.NilError(t, err)
		track(t, r, rec, sb)
	}
}

// entryOf returns the task's entry, or nil if it has none.
func entryOf(r *Runner, taskID int64) *taskEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tasks[taskID]
}

// assertTracked asserts that the task has a record and an entry holding that
// record, or, with want nil, that it has neither.
func assertTracked(t *testing.T, r *Runner, taskID int64, want *taskstate.Record) {
	t.Helper()
	rec, ok, err := r.store.Read(taskID)
	assert.NilError(t, err)
	e := entryOf(r, taskID)
	if want == nil {
		assert.Assert(t, !ok, "task %d has a record", taskID)
		assert.Assert(t, e == nil, "task %d has an entry", taskID)
		return
	}
	assert.Assert(t, ok, "no record for task %d", taskID)
	assert.DeepEqual(t, rec, *want)
	assert.Assert(t, e != nil, "no entry for task %d", taskID)
	e.mu.Lock()
	defer e.mu.Unlock()
	assert.DeepEqual(t, e.rec, *want)
	assert.Assert(t, e.sb != nil)
}

// waitSupervised waits until no supervise goroutine follows the task.
func waitSupervised(t *testing.T, r *Runner, taskID int64) {
	t.Helper()
	e := entryOf(r, taskID)
	assert.Assert(t, e != nil, "no entry for task %d", taskID)
	waitRun(t, e)
}

// waitRun waits until no supervise goroutine follows e's run.
func waitRun(t *testing.T, e *taskEntry) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		e.mu.Lock()
		run := e.run
		e.mu.Unlock()
		if run == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("supervise did not return")
		}
		time.Sleep(time.Millisecond)
	}
}

// waitDialed waits until the runner has dialed sb's driver at least min times.
func waitDialed(t *testing.T, sb *backend.SandboxMock, min int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(sb.DialCalls()) < min {
		if time.Now().After(deadline) {
			t.Fatalf("dialed %d times, want %d", len(sb.DialCalls()), min)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRunnerSupervise_Finished(t *testing.T) {
	t.Parallel()
	// Arrange - a run that finishes on its own; the test's own stream is
	// closed, which detaches it and leaves the run to supervise.
	client, stream := testDriver(t, 1, 3, &agent.Config{Type: agent.TypeDummy})
	assert.NilError(t, stream.Close())
	sb, parked := testSandbox(client)
	r, _, queue := testSupervisor(t, &backend.BackendMock{}, testStore(t))

	// Act
	supervise(t, t.Context(), r, 1, 3, sb)
	waitSupervised(t, r, 1)

	// Assert - the driver reported, so no event is owed; the sandbox is parked
	// after Finished and the slot is released.
	select {
	case <-parked:
	default:
		t.Fatal("sandbox was not parked after Finished")
	}
	assert.Assert(t, cmp.Len(sb.ParkCalls(), 1))
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
	assert.Assert(t, r.sem.TryAcquire(1))
}

func TestRunnerSupervise_Reattach(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going until it is stopped.
	hs, client, stream := serveDriver(t, 1, 3, sleepForever)
	assert.NilError(t, stream.Close())
	sb, parked := testSandbox(client)
	r, _, queue := testSupervisor(t, &backend.BackendMock{}, testStore(t))
	supervise(t, t.Context(), r, 1, 3, sb)
	waitDialed(t, sb, 1)

	// Act - drop every connection to the driver, the attached stream with
	// them, until supervise has dialed again; then stop the run.
	dials := len(sb.DialCalls())
	deadline := time.Now().Add(10 * time.Second)
	for len(sb.DialCalls()) == dials {
		if time.Now().After(deadline) {
			t.Fatal("supervise did not reattach")
		}
		hs.CloseClientConnections()
		time.Sleep(5 * time.Millisecond)
	}
	stopRun(t, client, 3)
	waitSupervised(t, r, 1)

	// Assert - the reattached stream saw Finished: the driver reported, the
	// sandbox is parked and the slot released, and no event is owed.
	<-parked
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
	assert.Assert(t, r.sem.TryAcquire(1))
}

func TestRunnerSupervise_DriverUnreachable(t *testing.T) {
	t.Parallel()
	// Arrange - a sandbox that stays up while its driver never answers.
	hs := httptest.NewServer(http.NotFoundHandler())
	hs.Close()
	sb, parked := testSandbox(driverv1connect.NewDriverServiceClient(hs.Client(), hs.URL))
	r, mock, queue := testSupervisor(t, &backend.BackendMock{}, testStore(t))
	r.lostTimeout = 100 * time.Millisecond

	// Act
	supervise(t, t.Context(), r, 2, 4, sb)
	waitSupervised(t, r, 2)

	// Assert - supervise gave up: the sandbox is parked, the slot released, and
	// the runner's fallback "failed" is scoped to the run's version.
	<-parked
	assert.DeepEqual(t, submitted(t, mock, queue), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 2, Version: 4, Reason: "driver unreachable"},
	}, protocmp.Transform())
	assert.Assert(t, r.sem.TryAcquire(1))
}

func TestRunnerSupervise_NoSuchRun(t *testing.T) {
	t.Parallel()
	// Arrange - a driver that has never run, so attaching to version 3 is
	// NotFound.
	client, _ := testDriver(t, 1, 3, nil)
	sb, parked := testSandbox(client)
	r, mock, queue := testSupervisor(t, &backend.BackendMock{}, testStore(t))

	// Act
	supervise(t, t.Context(), r, 1, 3, sb)
	waitSupervised(t, r, 1)

	// Assert - nothing runs on the sandbox, so it is parked and the run is
	// reported failed.
	<-parked
	assert.DeepEqual(t, submitted(t, mock, queue), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 1, Version: 3, Reason: "driver has no such run"},
	}, protocmp.Transform())
}

func TestRunnerSupervise_Shutdown(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going.
	client, stream := testDriver(t, 1, 3, sleepForever)
	assert.NilError(t, stream.Close())
	sb, _ := testSandbox(client)
	r, _, queue := testSupervisor(t, &backend.BackendMock{}, testStore(t))
	ctx, cancel := context.WithCancel(t.Context())
	supervise(t, ctx, r, 1, 3, sb)
	waitDialed(t, sb, 1)

	// Act - the runner shuts down.
	cancel()
	waitSupervised(t, r, 1)

	// Assert - the sandbox stays up for next-boot rehydration: no park, no
	// event, and the slot is still held.
	assert.Assert(t, cmp.Len(sb.ParkCalls(), 0))
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
	assert.Assert(t, !r.sem.TryAcquire(1))
}

func TestRunnerSupervise_Removed(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going, on a recorded sandbox.
	client, stream := testDriver(t, 1, 3, sleepForever)
	assert.NilError(t, stream.Close())
	sb, _ := testSandbox(client)
	r, mock, queue := testSupervisor(t, &backend.BackendMock{}, testStore(t))
	e := supervise(t, t.Context(), r, 1, 3, sb)
	waitDialed(t, sb, 1)

	// Act - the sandbox is removed under the run.
	assert.NilError(t, r.Remove(t.Context(), 1))
	waitRun(t, e)

	// Assert - Remove destroyed the sandbox supervise holds, the task's record
	// and entry are gone, and the run is lost.
	assert.Assert(t, cmp.Len(sb.DestroyCalls(), 1))
	assertTracked(t, r, 1, nil)
	assert.DeepEqual(t, submitted(t, mock, queue), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 1, Version: 3, Reason: "sandbox removed before the run finished"},
	}, protocmp.Transform())
	assert.Assert(t, r.sem.TryAcquire(1))
}

// TestRunnerSupervise_RestartReattaches covers a runner restart mid-run: the
// first runner process shuts down while it follows the run, and the next one
// reattaches to it from the record in Load.
func TestRunnerSupervise_RestartReattaches(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going, recorded at its version, followed by a
	// runner that then shuts down.
	client, stream := testDriver(t, 1, 3, sleepForever)
	assert.NilError(t, stream.Close())
	sb, parked := testSandbox(client)
	be := &backend.BackendMock{
		OpenFunc: func(backend.Handle) (backend.Sandbox, error) { return sb, nil },
	}
	store := testStore(t, taskstate.Record{TaskID: 1, Version: 3, Type: "docker", ID: "c1"})
	first, _, _ := testSupervisor(t, be, store)
	ctx, cancel := context.WithCancel(t.Context())
	assert.NilError(t, first.Load(ctx))
	waitDialed(t, sb, 1)
	cancel()
	waitSupervised(t, first, 1)
	assert.Assert(t, cmp.Len(sb.ParkCalls(), 0))

	// Act - the next runner process loads the record and reattaches; then the
	// run finishes.
	dials := len(sb.DialCalls())
	r, _, queue := testSupervisor(t, be, store)
	assert.NilError(t, r.Load(t.Context()))
	waitDialed(t, sb, dials+1)
	stopRun(t, client, 3)
	waitSupervised(t, r, 1)

	// Assert - the reattached runner saw Finished: the sandbox is parked, the
	// slot Load seeded is released, and no event is owed.
	<-parked
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
	assert.Assert(t, r.sem.TryAcquire(1))
}

// startTask starts task as Poll does, holding the concurrency slot the run's
// supervise goroutine releases.
func startTask(t *testing.T, r *Runner, task *model.Task) error {
	t.Helper()
	assert.Assert(t, r.sem.TryAcquire(1))
	err := r.Start(t.Context(), task)
	if err != nil {
		r.sem.Release(1)
	}
	return err
}

// assertRecord asserts the task's record.
func assertRecord(t *testing.T, store *taskstate.Store, want taskstate.Record) {
	t.Helper()
	rec, ok, err := store.Read(want.TaskID)
	assert.NilError(t, err)
	assert.Assert(t, ok, "no record for task %d", want.TaskID)
	assert.DeepEqual(t, rec, want)
}

// assertReported asserts that supervise is done, the run's driver reported its
// outcome, so no event is owed, the sandbox is parked and the slot released.
func assertReported(t *testing.T, r *Runner, taskID int64, queue *outbox.Outbox[model.RunnerEvent], parked <-chan struct{}) {
	t.Helper()
	waitSupervised(t, r, taskID)
	<-parked
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
	assert.Assert(t, r.sem.TryAcquire(1))
}

func TestRunnerStart_Create(t *testing.T) {
	t.Parallel()
	// Arrange - a task with no record, whose created sandbox's driver runs the
	// workspace's agent to completion.
	task := &model.Task{ID: 1, Runner: "test-runner", Workspace: "test", Version: 1}
	_, client, _ := newDriver(t, 1, nil)
	sb, parked := testSandbox(client)
	store := testStore(t)
	var r *Runner
	sb.StartFunc = func(context.Context) error {
		// The handle is recorded, and the task's entry registered, before the
		// sandbox is started.
		assertRecord(t, store, taskstate.Record{TaskID: 1, Version: 1, Type: "docker", ID: "c1"})
		assert.Assert(t, entryOf(r, 1) != nil)
		return nil
	}
	be := &backend.BackendMock{
		ValidateWorkspaceFunc: func(*workspace.Workspace) error { return nil },
		CreateFunc:            func(context.Context, *backend.Spec) (backend.Sandbox, error) { return sb, nil },
	}
	r, _, queue := testSupervisor(t, be, store)

	// Act
	err := startTask(t, r, task)

	// Assert - the sandbox was created for the task's run, which the driver
	// ran and reported.
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(be.CreateCalls(), 1))
	spec := be.CreateCalls()[0].Spec
	assert.Equal(t, spec.TaskID, int64(1))
	assert.Equal(t, spec.Version, int64(1))
	assert.Assert(t, cmp.Len(sb.StartCalls(), 1))
	assertReported(t, r, 1, queue, parked)
	assertTracked(t, r, 1, &taskstate.Record{TaskID: 1, Version: 1, Type: "docker", ID: "c1"})
}

// TestRunnerStart_CreateFails covers a sandbox that cannot be created: the task
// gets neither a record nor an entry.
func TestRunnerStart_CreateFails(t *testing.T) {
	t.Parallel()
	// Arrange
	task := &model.Task{ID: 1, Runner: "test-runner", Workspace: "test", Version: 1}
	boom := errors.New("boom")
	be := &backend.BackendMock{
		ValidateWorkspaceFunc: func(*workspace.Workspace) error { return nil },
		CreateFunc:            func(context.Context, *backend.Spec) (backend.Sandbox, error) { return nil, boom },
	}
	r, _, _ := testSupervisor(t, be, testStore(t))

	// Act
	err := startTask(t, r, task)

	// Assert
	assert.ErrorIs(t, err, boom)
	assertTracked(t, r, 1, nil)
}

// TestRunnerStart_Restart covers a task restarted on its recorded sandbox,
// whether an older runner or this one recorded it: the sandbox is reused, and
// the record moves to the sandbox's current handle at the new version.
func TestRunnerStart_Restart(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		record taskstate.Record
	}{
		{name: "legacy handle", record: legacyRecord(1, "c1")},
		{name: "current handle", record: taskstate.Record{TaskID: 1, Version: 3, Type: "docker", ID: "c1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - the sandbox's driver has never run version 4.
			task := &model.Task{ID: 1, Runner: "test-runner", Workspace: "test", Version: 4}
			_, client, _ := newDriver(t, 4, nil)
			sb, parked := testSandbox(client)
			sb.StartFunc = func(context.Context) error { return nil }
			// Create is unmocked: Start must not create a second sandbox.
			be := &backend.BackendMock{
				ValidateWorkspaceFunc: func(*workspace.Workspace) error { return nil },
				OpenFunc:              func(backend.Handle) (backend.Sandbox, error) { return sb, nil },
			}
			r, _, queue := testSupervisor(t, be, testStore(t))
			seed(t, r, tt.record)

			// Act
			err := startTask(t, r, task)

			// Assert - the recorded sandbox was opened, started and ran the
			// new version.
			assert.NilError(t, err)
			assert.Assert(t, cmp.Len(be.OpenCalls(), 1))
			assert.DeepEqual(t, be.OpenCalls()[0].H, recordHandle(tt.record))
			assert.Assert(t, cmp.Len(sb.StartCalls(), 1))
			assertReported(t, r, 1, queue, parked)
			assertTracked(t, r, 1, &taskstate.Record{TaskID: 1, Version: 4, Type: "docker", ID: "c1"})
		})
	}
}

// TestRunnerPoll_SlowDriver covers a driver that is slow to come up: Poll
// returns once the sandbox is started, without waiting for it, and the run is
// started once the driver answers.
func TestRunnerPoll_SlowDriver(t *testing.T) {
	t.Parallel()
	// Arrange - a pending task with no record, whose driver fails every call
	// until ready is closed.
	task := &model.Task{
		ID:        1,
		Runner:    "test-runner",
		Workspace: "test",
		Status:    model.TaskStatusPending,
		Command:   model.TaskCommandStart,
		Version:   1,
	}
	ready := make(chan struct{})
	_, client, _ := newDriver(t, 1, ready)
	sb, parked := testSandbox(client)
	sb.StartFunc = func(context.Context) error { return nil }
	be := &backend.BackendMock{
		ValidateWorkspaceFunc: func(*workspace.Workspace) error { return nil },
		CreateFunc:            func(context.Context, *backend.Spec) (backend.Sandbox, error) { return sb, nil },
	}
	r, mock, queue := testSupervisor(t, be, testStore(t))
	mock.ListRunnerTasksFunc = func(_ context.Context, _ *gritzv1.ListRunnerTasksRequest) (*gritzv1.ListRunnerTasksResponse, error) {
		return &gritzv1.ListRunnerTasksResponse{Tasks: []*gritzv1.Task{task.Proto("")}}, nil
	}

	// Act - poll while the driver is not up, then bring it up.
	assert.NilError(t, r.Poll(t.Context()))
	waitDialed(t, sb, 1)
	close(ready)

	// Assert - Poll returned with the sandbox started and the run pending; the
	// run then started, and the driver reported it.
	assert.Assert(t, cmp.Len(sb.StartCalls(), 1))
	assertReported(t, r, 1, queue, parked)
}

// TestRunnerSupervise_NotReady covers a driver that never comes up: the run
// is reported failed, the sandbox parked, and the record kept.
func TestRunnerSupervise_NotReady(t *testing.T) {
	t.Parallel()
	// Arrange
	task := &model.Task{ID: 1, Runner: "test-runner", Workspace: "test", Version: 1}
	_, client, _ := newDriver(t, 1, make(chan struct{}))
	sb, parked := testSandbox(client)
	sb.StartFunc = func(context.Context) error { return nil }
	be := &backend.BackendMock{
		ValidateWorkspaceFunc: func(*workspace.Workspace) error { return nil },
		CreateFunc:            func(context.Context, *backend.Spec) (backend.Sandbox, error) { return sb, nil },
	}
	r, mock, queue := testSupervisor(t, be, testStore(t))
	r.readyTimeout = 100 * time.Millisecond

	// Act
	assert.NilError(t, startTask(t, r, task))
	waitSupervised(t, r, 1)

	// Assert
	<-parked
	events := submitted(t, mock, queue)
	assert.Assert(t, cmp.Len(events, 1))
	assert.Equal(t, events[0].Event, "failed")
	assert.Equal(t, events[0].Version, int64(1))
	assert.Assert(t, cmp.Contains(events[0].Reason, "driver server did not become ready"))
	assert.Assert(t, r.sem.TryAcquire(1))
	assertTracked(t, r, 1, &taskstate.Record{TaskID: 1, Version: 1, Type: "docker", ID: "c1"})
}

// TestRunnerStart_StartFails covers a sandbox that cannot be started after
// Create: the record is kept, and Remove destroys the sandbox.
func TestRunnerStart_StartFails(t *testing.T) {
	t.Parallel()
	// Arrange
	task := &model.Task{ID: 1, Runner: "test-runner", Workspace: "test", Version: 1}
	boom := errors.New("boom")
	sb, _ := testSandbox(nil)
	sb.StartFunc = func(context.Context) error { return boom }
	// Open is unmocked: Remove destroys the sandbox Start created.
	be := &backend.BackendMock{
		ValidateWorkspaceFunc: func(*workspace.Workspace) error { return nil },
		CreateFunc:            func(context.Context, *backend.Spec) (backend.Sandbox, error) { return sb, nil },
	}
	r, _, _ := testSupervisor(t, be, testStore(t))

	// Act
	err := startTask(t, r, task)

	// Assert - Start failed with the record and entry kept and nothing
	// supervised.
	assert.ErrorIs(t, err, boom)
	assertTracked(t, r, 1, &taskstate.Record{TaskID: 1, Version: 1, Type: "docker", ID: "c1"})
	assert.Assert(t, cmp.Len(sb.DialCalls(), 0))

	// Act - the task's sandbox is removed.
	assert.NilError(t, r.Remove(t.Context(), 1))

	// Assert
	assert.Assert(t, cmp.Len(sb.DestroyCalls(), 1))
	assertTracked(t, r, 1, nil)
}

func TestRunnerStart_Gone(t *testing.T) {
	t.Parallel()
	// Arrange - a task whose recorded sandbox has vanished. Start never
	// creates a fresh sandbox for a recorded task.
	task := &model.Task{ID: 8, Runner: "test-runner", Workspace: "test", Version: 2}
	sb, _ := testSandbox(nil)
	sb.StartFunc = func(context.Context) error { return backend.ErrGone }
	be := &backend.BackendMock{
		ValidateWorkspaceFunc: func(*workspace.Workspace) error { return nil },
		OpenFunc:              func(backend.Handle) (backend.Sandbox, error) { return sb, nil },
	}
	r, _, _ := testSupervisor(t, be, testStore(t))
	seed(t, r, taskstate.Record{TaskID: 8, Version: 1, Type: "docker", ID: "old-id"})
	e := entryOf(r, 8)

	// Act
	err := startTask(t, r, task)

	// Assert - ErrGone surfaced (the caller emits failed and releases the
	// slot), the dangling record and the entry were removed, the entry marked
	// with why, and nothing is supervised.
	assert.ErrorIs(t, err, backend.ErrGone)
	assertTracked(t, r, 8, nil)
	assert.ErrorIs(t, e.removed, backend.ErrGone)
	assert.Assert(t, cmp.Len(sb.DialCalls(), 0))
}

// TestRunnerPoll_StartStillRunning covers a start command for a task whose
// previous run is still supervised while Running reports its sandbox parked
// (a Lambda VM suspending after its run): nothing is started, and the
// concurrency slot is given back.
func TestRunnerPoll_StartStillRunning(t *testing.T) {
	t.Parallel()
	// Arrange
	task := &model.Task{
		ID:        1,
		Runner:    "test-runner",
		Workspace: "test",
		Status:    model.TaskStatusCompleted,
		Command:   model.TaskCommandStart,
		Version:   4,
	}
	be, _ := testBackend(map[string]backend.Status{"c1": backend.StatusParked}, nil)
	r, mock, queue := testSupervisor(t, be, testStore(t))
	mock.ListRunnerTasksFunc = func(_ context.Context, _ *gritzv1.ListRunnerTasksRequest) (*gritzv1.ListRunnerTasksResponse, error) {
		return &gritzv1.ListRunnerTasksResponse{Tasks: []*gritzv1.Task{task.Proto("")}}, nil
	}
	seed(t, r, legacyRecord(1, "c1"))
	entryOf(r, 1).run = &taskRun{cancel: func(error) {}}

	// Act
	err := r.Poll(t.Context())
	assert.NilError(t, err)

	// Assert - no sandbox started, no token minted, no event, and the slot is
	// free again.
	assert.Assert(t, cmp.Len(mock.CreateTaskTokenCalls(), 0))
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
	assert.Assert(t, r.sem.TryAcquire(1))
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

// legacyRecord is a record written by an older runner: its Data carries the run
// version.
func legacyRecord(taskID int64, id string) taskstate.Record {
	return taskstate.Record{TaskID: taskID, Version: 3, Type: "docker", ID: id, Data: json.RawMessage(`{"version":3}`)}
}

// testBackend returns a Backend mock whose opened sandboxes report the
// status (or error) given for their ID. Destroy records the IDs it destroyed.
func testBackend(status map[string]backend.Status, statusErr map[string]error) (*backend.BackendMock, *[]string) {
	var mu sync.Mutex
	var destroyed []string
	return &backend.BackendMock{
		OpenFunc: func(h backend.Handle) (backend.Sandbox, error) {
			return &backend.SandboxMock{
				StatusFunc: func(context.Context) (backend.Status, error) {
					if err := statusErr[h.ID]; err != nil {
						return backend.StatusUnknown, err
					}
					return status[h.ID], nil
				},
				DestroyFunc: func(context.Context) error {
					mu.Lock()
					defer mu.Unlock()
					destroyed = append(destroyed, h.ID)
					return nil
				},
			}, nil
		},
	}, &destroyed
}

func TestRunnerLoad(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		record     taskstate.Record
		openErr    error
		status     backend.Status
		statusErr  error
		wantEvents []*gritzv1.RunnerEvent
		// wantRecord is whether the record is kept, and the task has an entry.
		wantRecord bool
		wantAttach bool // supervised: the runner dialed the driver to reattach
	}{
		{
			name:       "up",
			record:     legacyRecord(1, "c1"),
			status:     backend.StatusUp,
			wantRecord: true,
			wantAttach: true,
		},
		{
			// A parked husk is a lost-report backstop; its record is kept.
			name:       "parked",
			record:     legacyRecord(1, "c1"),
			status:     backend.StatusParked,
			wantEvents: []*gritzv1.RunnerEvent{{Event: "failed", TaskId: 1, Version: 3, Reason: "sandbox exited"}},
			wantRecord: true,
		},
		{
			// A gone sandbox is a lost-report backstop; its record is dropped.
			name:       "gone",
			record:     legacyRecord(1, "c1"),
			status:     backend.StatusGone,
			wantEvents: []*gritzv1.RunnerEvent{{Event: "failed", TaskId: 1, Version: 3, Reason: "sandbox exited"}},
		},
		{
			// A failed status check leaves the record alone.
			name:       "status error",
			record:     legacyRecord(1, "c1"),
			statusErr:  errors.New("boom"),
			wantRecord: true,
		},
		{
			// A record whose handle cannot be opened (a malformed handle) has
			// no sandbox, so it is dropped.
			name:    "open fails",
			record:  legacyRecord(1, "c1"),
			openErr: errors.New("malformed handle"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - the record's task is still RUNNING on the server.
			mock := &gritzclient.ClientMock{
				GetTaskFunc: func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
					return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Id: req.Id, Status: gritzv1.TaskStatus_RUNNING}}, nil
				},
				SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
					return &gritzv1.SubmitRunnerEventsResponse{}, nil
				},
			}
			be, destroyed := testBackend(
				map[string]backend.Status{"c1": tt.status},
				map[string]error{"c1": tt.statusErr},
			)
			dialed := make(chan backend.Handle, 1)
			open := be.OpenFunc
			be.OpenFunc = func(h backend.Handle) (backend.Sandbox, error) {
				if tt.openErr != nil {
					return nil, tt.openErr
				}
				sb, err := open(h)
				sb.(*backend.SandboxMock).DialFunc = func(ctx context.Context, _ string) (driverv1connect.DriverServiceClient, error) {
					dialed <- h // the up sandbox got a supervise goroutine
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return sb, err
			}
			store := testStore(t, tt.record)
			queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
				StoreDir: t.TempDir(),
				Client:   mock,
				Backoff:  backoff.NewConstantBackOff(0),
				Log:      slog.Default(),
			})
			assert.NilError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r, err := New(Options{Client: mock, Backend: be, Store: store, RunnerID: "test-runner", Concurrency: 5, Queue: queue})
			assert.NilError(t, err)

			// Act
			err = r.Load(ctx)

			// Assert
			assert.NilError(t, err)
			assert.DeepEqual(t, submitted(t, mock, queue), tt.wantEvents, protocmp.Transform())
			if tt.wantRecord {
				assertTracked(t, r, 1, &tt.record)
			} else {
				assertTracked(t, r, 1, nil)
			}
			assert.Assert(t, cmp.Len(*destroyed, 0)) // Load never destroys
			var running int64
			if tt.wantAttach {
				running = 1
				// Reattached over the sandbox opened from the recorded handle.
				select {
				case h := <-dialed:
					assert.DeepEqual(t, h, backend.Handle{Type: "docker", ID: "c1", Data: tt.record.Data})
				case <-time.After(3 * time.Second):
					t.Fatal("up sandbox was not supervised")
				}
			} else {
				assert.Equal(t, len(dialed), 0)
			}
			// The semaphore was seeded with the up count.
			assert.Assert(t, r.sem.TryAcquire(5-running))
			assert.Assert(t, !r.sem.TryAcquire(1))
		})
	}
}

// TestRunnerLoad_VersionScopedBackstop checks that the boot-time backstop stamps
// the exited record's version onto its "failed" so ApplyRunnerEvent drops it for
// a superseded run, applies it for the current run, and bypasses for a legacy
// version-0 record.
func TestRunnerLoad_VersionScopedBackstop(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		recVersion int64 // version stamped in the seeded exited record
		taskNow    int64 // the task's current version when the event is applied
		wantApply  bool
	}{
		{name: "superseded run dropped", recVersion: 4, taskNow: 5, wantApply: false},
		{name: "current run applies", recVersion: 4, taskNow: 4, wantApply: true},
		{name: "legacy record bypasses", recVersion: 0, taskNow: 5, wantApply: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - one parked husk whose task is still RUNNING on the server,
			// so the boot-time backstop fires a "failed".
			mock := &gritzclient.ClientMock{
				GetTaskFunc: func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
					return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Id: req.Id, Status: gritzv1.TaskStatus_RUNNING}}, nil
				},
				SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
					return &gritzv1.SubmitRunnerEventsResponse{}, nil
				},
			}
			be, _ := testBackend(map[string]backend.Status{"c1": backend.StatusParked}, nil)
			store := testStore(t, taskstate.Record{TaskID: 1, Version: tt.recVersion, Type: "docker", ID: "c1"})
			queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
				StoreDir: t.TempDir(),
				Client:   mock,
				Backoff:  backoff.NewConstantBackOff(0),
				Log:      slog.Default(),
			})
			assert.NilError(t, err)
			r, err := New(Options{Client: mock, Backend: be, Store: store, RunnerID: "test-runner", Concurrency: 1, Queue: queue})
			assert.NilError(t, err)

			// Act - Load drives the parked husk through failIfTaskRunning, which
			// emits a "failed" stamped with the record's version.
			assert.NilError(t, r.Load(t.Context()))
			events := submitted(t, mock, queue)
			assert.Equal(t, len(events), 1)
			assert.Equal(t, events[0].Version, tt.recVersion) // stamped from the record

			// The existing guard decides: apply the backstop against a task at its
			// current version.
			task := &model.Task{ID: 1, Status: model.TaskStatusRunning, Version: tt.taskNow}
			ev := model.RunnerEventFromProto(events[0])
			assert.Equal(t, task.ApplyRunnerEvent(&ev), tt.wantApply)
		})
	}
}

func TestRunnerPrune(t *testing.T) {
	t.Parallel()
	// Arrange - every record but the deleted task's (4) belongs to an archived
	// task, except 2's.
	mock := &gritzclient.ClientMock{
		GetTaskFunc: func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
			if req.Id == 4 {
				return nil, connect.NewError(connect.CodeNotFound, errors.New("task not found"))
			}
			return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Id: req.Id, Archived: req.Id != 2}}, nil
		},
	}
	be, destroyed := testBackend(
		map[string]backend.Status{
			"c1": backend.StatusParked, // archived -> removed
			"c2": backend.StatusParked, // not archived -> kept
			"c3": backend.StatusUp,     // up -> not considered
			"c4": backend.StatusParked, // deleted -> removed
			"c5": backend.StatusGone,   // archived -> removed
		},
		map[string]error{
			"c7": errors.New("boom"), // status unknown -> not considered
		},
	)
	store := testStore(t)
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	r, err := New(Options{Client: mock, Backend: be, Store: store, RunnerID: "test-runner", Concurrency: 1, Queue: queue})
	assert.NilError(t, err)
	seed(t, r,
		legacyRecord(1, "c1"),
		legacyRecord(2, "c2"),
		legacyRecord(3, "c3"),
		legacyRecord(4, "c4"),
		legacyRecord(5, "c5"),
		legacyRecord(7, "c7"),
	)

	// Act
	err = r.Prune(t.Context())
	assert.NilError(t, err)

	// Assert - only sandboxes that are not up had their task looked up.
	var looked []int64
	for _, call := range mock.GetTaskCalls() {
		looked = append(looked, call.GetTaskRequest.Id)
	}
	slices.Sort(looked)
	assert.DeepEqual(t, looked, []int64{1, 2, 4, 5})
	// Archived/deleted tasks' sandboxes are destroyed.
	slices.Sort(*destroyed)
	assert.DeepEqual(t, *destroyed, []string{"c1", "c4", "c5"})
	recs, err := store.List()
	assert.NilError(t, err)
	var kept []int64
	for _, rec := range recs {
		kept = append(kept, rec.TaskID)
	}
	slices.Sort(kept)
	assert.DeepEqual(t, kept, []int64{2, 3, 7})
	// The registry dropped exactly the removed tasks' entries.
	r.mu.Lock()
	entries := slices.Sorted(maps.Keys(r.tasks))
	r.mu.Unlock()
	assert.DeepEqual(t, entries, kept)
}

func TestRunnerRunning(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		records []taskstate.Record
		status  backend.Status
		err     error
		want    bool
	}{
		{name: "up", records: []taskstate.Record{legacyRecord(1, "c1")}, status: backend.StatusUp, want: true},
		{name: "parked", records: []taskstate.Record{legacyRecord(1, "c1")}, status: backend.StatusParked},
		{name: "gone", records: []taskstate.Record{legacyRecord(1, "c1")}, status: backend.StatusGone},
		{name: "no record"},
		{name: "status error", records: []taskstate.Record{legacyRecord(1, "c1")}, err: errors.New("boom")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			be, _ := testBackend(
				map[string]backend.Status{"c1": tt.status},
				map[string]error{"c1": tt.err},
			)
			r, err := New(Options{Backend: be, Store: testStore(t), RunnerID: "test-runner"})
			assert.NilError(t, err)
			seed(t, r, tt.records...)

			// Act
			running, err := r.Running(t.Context(), 1)

			// Assert
			if tt.err != nil {
				assert.ErrorIs(t, err, tt.err)
			} else {
				assert.NilError(t, err)
			}
			assert.Equal(t, running, tt.want)
		})
	}
}

func TestRunnerRemove(t *testing.T) {
	t.Parallel()
	// A record written by an older runner carries the run version in Data.
	legacy := taskstate.Record{TaskID: 1, Version: 3, Type: "docker", ID: "c1", Data: json.RawMessage(`{"version":3}`)}
	tests := []struct {
		name        string
		records     []taskstate.Record
		destroyErr  error
		wantErr     bool
		wantOpened  []backend.Handle
		wantRecords bool
	}{
		{
			name:       "legacy handle",
			records:    []taskstate.Record{legacy},
			wantOpened: []backend.Handle{{Type: "docker", ID: "c1", Data: legacy.Data}},
		},
		{
			// A gone sandbox's Destroy returns nil, so the record is dropped.
			name:       "sandbox gone",
			records:    []taskstate.Record{{TaskID: 1, Type: "docker", ID: "c1"}},
			wantOpened: []backend.Handle{{Type: "docker", ID: "c1"}},
		},
		{
			// A failed destroy keeps the record so the removal is retried.
			name:        "destroy fails",
			records:     []taskstate.Record{legacy},
			destroyErr:  errors.New("boom"),
			wantErr:     true,
			wantOpened:  []backend.Handle{{Type: "docker", ID: "c1", Data: legacy.Data}},
			wantRecords: true,
		},
		{
			name: "no record",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			sb := &backend.SandboxMock{
				DestroyFunc: func(context.Context) error { return tt.destroyErr },
			}
			be := &backend.BackendMock{
				OpenFunc: func(backend.Handle) (backend.Sandbox, error) { return sb, nil },
			}
			r, err := New(Options{Backend: be, Store: testStore(t), RunnerID: "test-runner"})
			assert.NilError(t, err)
			seed(t, r, tt.records...)

			// Act
			err = r.Remove(t.Context(), 1)

			// Assert
			if tt.wantErr {
				assert.ErrorIs(t, err, tt.destroyErr)
			} else {
				assert.NilError(t, err)
			}
			var opened []backend.Handle
			for _, call := range be.OpenCalls() {
				opened = append(opened, call.H)
			}
			assert.DeepEqual(t, opened, tt.wantOpened)
			assert.Equal(t, len(sb.DestroyCalls()), len(tt.wantOpened))
			// The record and the entry are removed together.
			if tt.wantRecords {
				assertTracked(t, r, 1, &tt.records[0])
			} else {
				assertTracked(t, r, 1, nil)
			}
		})
	}
}

// TestRunnerRemovedEntry covers an operation that looked up a task's entry
// just before the entry was removed: once it has the entry's lock it returns
// why the entry was removed, without touching the sandbox or creating a new
// one.
func TestRunnerRemovedEntry(t *testing.T) {
	t.Parallel()
	ops := []struct {
		name string
		op   func(r *Runner) error
	}{
		{name: "start", op: func(r *Runner) error {
			return r.Start(t.Context(), &model.Task{ID: 1, Workspace: "test", Version: 4})
		}},
		{name: "kill", op: func(r *Runner) error {
			_, err := r.Kill(t.Context(), &model.Task{ID: 1, Version: 4})
			return err
		}},
		{name: "running", op: func(r *Runner) error {
			_, err := r.Running(t.Context(), 1)
			return err
		}},
		{name: "remove", op: func(r *Runner) error { return r.Remove(t.Context(), 1) }},
		{name: "version", op: func(r *Runner) error {
			_, err := r.version(1)
			return err
		}},
	}
	for _, cause := range []error{errRemoved, backend.ErrGone} {
		for _, tt := range ops {
			t.Run(tt.name+"/"+cause.Error(), func(t *testing.T) {
				t.Parallel()
				// Arrange - the entry is still in the registry, as it was when
				// the operation looked it up, but has since been removed.
				// Every backend call is unmocked, and so must not be made.
				sb := &backend.SandboxMock{}
				be := &backend.BackendMock{
					OpenFunc: func(backend.Handle) (backend.Sandbox, error) { return sb, nil },
				}
				r, mock, _ := testSupervisor(t, be, testStore(t))
				seed(t, r, legacyRecord(1, "c1"))
				entryOf(r, 1).removed = cause

				// Act
				err := tt.op(r)

				// Assert
				assert.ErrorIs(t, err, cause)
				assert.Assert(t, cmp.Len(mock.CreateTaskTokenCalls(), 0))
			})
		}
	}
}

// sleepForever is an agent config whose dummy agent runs until it is stopped.
var sleepForever = &agent.Config{Type: agent.TypeDummy, Dummy: &agent.DummyOptions{Sleep: -1}}

// testDriver serves a driver server over an in-memory Connect client. With cfg
// set, it starts run version of taskID with that agent config and returns the
// run's stream; with cfg nil the driver has never run.
func testDriver(t *testing.T, taskID, version int64, cfg *agent.Config) (driverv1connect.DriverServiceClient, *connect.ServerStreamForClient[driverv1.RunResponse]) {
	t.Helper()
	_, client, stream := serveDriver(t, taskID, version, cfg)
	return client, stream
}

// serveDriver is testDriver, also returning the server the driver is served
// by.
func serveDriver(t *testing.T, taskID, version int64, cfg *agent.Config) (*httptest.Server, driverv1connect.DriverServiceClient, *connect.ServerStreamForClient[driverv1.RunResponse]) {
	t.Helper()
	hs, client, configs := newDriver(t, version, nil)
	if cfg == nil {
		return hs, client, nil
	}
	assert.NilError(t, configs.Save(taskID, cfg))
	stream, err := client.Run(t.Context(), &driverv1.RunRequest{
		Version: version,
		Spec:    &driverv1.RunSpec{TaskId: taskID, ServerUrl: "http://gritz.test", Token: "token"},
	})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	assert.Assert(t, stream.Receive(), "Run: %v", stream.Err())
	assert.Assert(t, stream.Msg().GetAccepted() != nil, "first message is %v, want accepted", stream.Msg())
	return hs, client, stream
}

// newDriver serves a driver server that has never run, for tasks on version.
// It reads the agent config from where the runner's spec provisions it, so a
// Run with that spec runs the workspace's agent; configs is that store. Until
// ready is closed every call fails Unavailable, as from a driver that is not
// up yet; a nil ready is open.
func newDriver(t *testing.T, version int64, ready <-chan struct{}) (*httptest.Server, driverv1connect.DriverServiceClient, agent.ConfigStore) {
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
	mux := http.NewServeMux()
	mux.Handle(srv.Handler())
	var handler http.Handler = mux
	if ready != nil {
		handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			select {
			case <-ready:
				mux.ServeHTTP(w, req)
			default:
				http.Error(w, "driver is starting", http.StatusServiceUnavailable)
			}
		})
	}
	hs := httptest.NewServer(handler)
	t.Cleanup(hs.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		assert.NilError(t, srv.Shutdown(ctx))
	})
	return hs, driverv1connect.NewDriverServiceClient(hs.Client(), hs.URL), configs
}

// waitFinished reads a run stream until the driver sends Finished.
func waitFinished(t *testing.T, stream *connect.ServerStreamForClient[driverv1.RunResponse]) {
	t.Helper()
	for stream.Receive() {
		if stream.Msg().GetFinished() != nil {
			return
		}
	}
	t.Fatalf("stream ended before finished: %v", stream.Err())
}

func TestRunnerKill(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// record, when set, is the task's record (version 3).
		record bool
		status backend.Status
		// agent, when set, starts run version 3 with it; finish waits for that
		// run to finish before Kill.
		agent        *agent.Config
		finish       bool
		statusErr    error
		wantSignal   bool
		wantErr      bool
		wantDialed   bool
		wantFinished bool
	}{
		{
			name:         "running",
			record:       true,
			status:       backend.StatusUp,
			agent:        sleepForever,
			wantSignal:   true,
			wantDialed:   true,
			wantFinished: true,
		},
		{
			name:       "finished",
			record:     true,
			status:     backend.StatusUp,
			agent:      &agent.Config{Type: agent.TypeDummy},
			finish:     true,
			wantDialed: true,
		},
		{
			name:       "never run",
			record:     true,
			status:     backend.StatusUp,
			wantDialed: true,
		},
		{
			name:   "parked",
			record: true,
			status: backend.StatusParked,
		},
		{
			name:   "gone",
			record: true,
			status: backend.StatusGone,
		},
		{
			name:      "status fails",
			record:    true,
			statusErr: errors.New("boom"),
			wantErr:   true,
		},
		{
			name: "no record",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			client, stream := testDriver(t, 1, 3, tt.agent)
			if tt.finish {
				waitFinished(t, stream)
			}
			sb := &backend.SandboxMock{
				StatusFunc: func(context.Context) (backend.Status, error) {
					if tt.statusErr != nil {
						return backend.StatusUnknown, tt.statusErr
					}
					return tt.status, nil
				},
				DialFunc: func(context.Context, string) (driverv1connect.DriverServiceClient, error) {
					return client, nil
				},
			}
			be := &backend.BackendMock{
				OpenFunc: func(backend.Handle) (backend.Sandbox, error) { return sb, nil },
			}
			var recs []taskstate.Record
			if tt.record {
				recs = append(recs, legacyRecord(1, "c1"))
			}
			r, err := New(Options{Backend: be, Store: testStore(t), RunnerID: "test-runner"})
			assert.NilError(t, err)
			seed(t, r, recs...)

			// Act
			signalled, err := r.Kill(t.Context(), &model.Task{ID: 1, Version: 3})

			// Assert
			if tt.wantErr {
				assert.ErrorIs(t, err, tt.statusErr)
			} else {
				assert.NilError(t, err)
			}
			assert.Equal(t, signalled, tt.wantSignal)
			assert.Equal(t, len(sb.DialCalls()) == 1, tt.wantDialed)
			if tt.wantFinished {
				// The stopped run reports and finishes on its own stream.
				waitFinished(t, stream)
			}
			if tt.record {
				assert.DeepEqual(t, be.OpenCalls()[0].H, backend.Handle{Type: "docker", ID: "c1", Data: recs[0].Data})
			} else {
				assert.Assert(t, cmp.Len(be.OpenCalls(), 0))
			}
		})
	}
}

// TestRunnerDie_StopWithoutSandbox is the sharpest unrecoverable case from #1209:
// a stop command with no running sandbox has no reconciliation path on restart
// (Load has nothing to probe and cannot re-derive "stopped"), so a dropped persist
// would leave the task stuck in cancelling forever. When the durable outbox write
// fails there, the runner must crash — die cancels the root ctx with a
// FatalError cause — instead of logging and continuing.
func TestRunnerDie_StopWithoutSandbox(t *testing.T) {
	t.Parallel()
	task := &model.Task{
		ID:        7,
		Runner:    "test-runner",
		Workspace: "test",
		Status:    model.TaskStatusCancelling,
		Command:   model.TaskCommandStop,
		Version:   3,
	}
	mock := &gritzclient.ClientMock{
		ListRunnerTasksFunc: func(_ context.Context, _ *gritzv1.ListRunnerTasksRequest) (*gritzv1.ListRunnerTasksResponse, error) {
			return &gritzv1.ListRunnerTasksResponse{Tasks: []*gritzv1.Task{task.Proto("")}}, nil
		},
	}
	// No record, so there is no run to stop → the runner emits "stopped".
	// Back the outbox with a store whose durable Append fails. The die path is
	// reached through Enqueue → Append alone, so Peek/Drop/Len are never called
	// (an unstubbed moq method would panic if they were).
	store := &outbox.StoreMock{
		AppendFunc: func(json.RawMessage) error { return errors.New("no space left on device") },
	}
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		Store:   store,
		Client:  mock,
		Backoff: backoff.NewConstantBackOff(0),
		Log:     slog.Default(),
	})
	assert.NilError(t, err)

	ctx, cancelCause := context.WithCancelCause(t.Context())
	defer cancelCause(nil)
	r, err := New(Options{Client: mock, Backend: &backend.BackendMock{}, Store: testStore(t), RunnerID: "test-runner", Concurrency: 1, Queue: queue, Fatal: cancelCause})
	assert.NilError(t, err)

	// Act - the "stopped" enqueue fails durably, so die fires.
	assert.NilError(t, r.Poll(ctx))

	// Assert - the root ctx is cancelled with a FatalError cause, which the
	// command layer maps to a non-zero exit.
	var fatal FatalError
	assert.Assert(t, errors.As(context.Cause(ctx), &fatal))
}

// TestRunnerGracefulShutdown_NoFatalCause is the complement: on the happy path the
// durable write succeeds, die never fires, and a plain ctx cancel (the signal
// shutdown) leaves no FatalError cause — so the command layer's exit mapping
// returns nil (exit 0) rather than crashing.
func TestRunnerGracefulShutdown_NoFatalCause(t *testing.T) {
	t.Parallel()
	task := &model.Task{
		ID:        7,
		Runner:    "test-runner",
		Workspace: "test",
		Status:    model.TaskStatusCancelling,
		Command:   model.TaskCommandStop,
		Version:   3,
	}
	mock := &gritzclient.ClientMock{
		ListRunnerTasksFunc: func(_ context.Context, _ *gritzv1.ListRunnerTasksRequest) (*gritzv1.ListRunnerTasksResponse, error) {
			return &gritzv1.ListRunnerTasksResponse{Tasks: []*gritzv1.Task{task.Proto("")}}, nil
		},
		SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
			return &gritzv1.SubmitRunnerEventsResponse{}, nil
		},
	}
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)

	ctx, cancelCause := context.WithCancelCause(context.Background())
	r, err := New(Options{Client: mock, Backend: &backend.BackendMock{}, Store: testStore(t), RunnerID: "test-runner", Concurrency: 1, Queue: queue, Fatal: cancelCause})
	assert.NilError(t, err)

	// The durable enqueue succeeds, so die never fires.
	assert.NilError(t, r.Poll(ctx))

	// A graceful signal shutdown cancels ctx with no FatalError cause.
	cancelCause(nil)
	var fatal FatalError
	assert.Assert(t, !errors.As(context.Cause(ctx), &fatal))
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
