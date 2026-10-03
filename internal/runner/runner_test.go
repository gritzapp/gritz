package runner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/cenkalti/backoff/v5"
	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/icholy/gritz/internal/gritzclient"
	"github.com/icholy/gritz/internal/model"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"github.com/icholy/gritz/internal/proto/gritz/v1/gritzv1connect"
	"github.com/icholy/gritz/internal/runner/agent"
	"github.com/icholy/gritz/internal/runner/backend"
	dockerbackend "github.com/icholy/gritz/internal/runner/backend/docker"
	"github.com/icholy/gritz/internal/runner/taskstate"
	"github.com/icholy/gritz/internal/runner/workspace"
	"github.com/icholy/gritz/internal/x/dockerx"
	"github.com/icholy/gritz/internal/x/outbox"
	"github.com/icholy/gritz/internal/x/testx"
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
	}

	// Create httptest server with the mock
	_, handler := gritzv1connect.NewGritzServiceHandler(mock)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	// Create runner. The agent connects to the server directly over the network, so
	// the container shares the host network namespace (NetworkMode "host") to
	// reach the httptest server on 127.0.0.1, and ServerURL is the same URL.
	client := gritzclient.New(gritzclient.Options{BaseURL: ts.URL})
	be, err := dockerbackend.New(dockerbackend.Options{RunnerID: "test-runner"})
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

	docker, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	assert.NilError(t, err)
	defer docker.Close()

	// Remove any leftover container from a previous aborted run.
	_ = docker.ContainerRemove(t.Context(), "gritz-1", container.RemoveOptions{Force: true})

	// Start a task
	err = r.Start(t.Context(), task)
	assert.NilError(t, err)

	// The runner records the launched handle in the store.
	rec, ok, err := r.store.Read(task.ID)
	assert.NilError(t, err)
	assert.Equal(t, ok, true)
	assert.Equal(t, rec.Type, "docker")
	assert.Assert(t, rec.ID != "")

	// Wait for the container to exit
	err = dockerx.ContainerWait(t.Context(), docker, "gritz-1", container.WaitConditionNotRunning)
	assert.NilError(t, err)

	// Remove the container
	err = docker.ContainerRemove(t.Context(), "gritz-1", container.RemoveOptions{})
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
	be, err := dockerbackend.New(dockerbackend.Options{RunnerID: "test-runner"})
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

// TestRunnerSpec_Secrets asserts the runner injects each declared workspace
// secret into the sandbox as a plain environment variable and names them all in
// GRITZ_SECRETS, which is how the driver learns which values to mask in the log
// it ships (nothing consumes the list yet).
func TestRunnerSpec_Secrets(t *testing.T) {
	t.Parallel()

	mock := &gritzclient.ClientMock{
		CreateTaskTokenFunc: func(_ context.Context, req *gritzv1.CreateTaskTokenRequest) (*gritzv1.CreateTaskTokenResponse, error) {
			return &gritzv1.CreateTaskTokenResponse{Token: "test-token"}, nil
		},
	}
	client := gritzclient.New(gritzclient.Options{BaseURL: "http://localhost"})
	be, err := dockerbackend.New(dockerbackend.Options{RunnerID: "test-runner"})
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

	assert.DeepEqual(t, spec.Env, []string{
		"GRITZ_TASK_ID=1",
		"GRITZ_TOKEN=test-token",
		"GRITZ_SERVER=http://localhost",
		"GH_TOKEN=gho_secretvalue",
		"NPM_TOKEN=npm_secretvalue",
		"GRITZ_SECRETS=GH_TOKEN,NPM_TOKEN",
	})
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

func TestRunnerStart_Idempotent(t *testing.T) {
	t.Parallel()
	// Arrange - a task with an already-running tracked sandbox.
	task := &model.Task{ID: 5, Runner: "test-runner", Workspace: "test", Version: 1}
	store := testStore(t, taskstate.Record{TaskID: 5, Type: "docker", ID: "c5"})
	be := &backend.BackendMock{
		ProbeFunc: func(_ context.Context, h backend.Handle) (backend.State, error) {
			assert.Equal(t, h.ID, "c5")
			return backend.StateRunning, nil
		},
	}
	mock := &gritzclient.ClientMock{}
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	r, err := New(Options{Client: mock, Backend: be, Store: store, RunnerID: "test-runner", Concurrency: 1, Queue: queue})
	assert.NilError(t, err)

	// Act
	err = r.Start(t.Context(), task)

	// Assert - the running sandbox is left alone: no Launch, no token minted.
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(be.LaunchCalls(), 0))
	assert.Assert(t, cmp.Len(mock.CreateTaskTokenCalls(), 0))
}

func TestRunnerStart_AdoptReuse(t *testing.T) {
	t.Parallel()
	// Arrange - a task whose tracked sandbox has exited; Start must pass the
	// prior handle to Launch as reuse and persist the returned handle.
	task := &model.Task{ID: 6, Runner: "test-runner", Workspace: "test", Version: 1}
	store := testStore(t, taskstate.Record{TaskID: 6, Type: "docker", ID: "old-id"})
	be := &backend.BackendMock{
		ValidateWorkspaceFunc: func(_ *workspace.Workspace) error { return nil },
		ProbeFunc: func(_ context.Context, h backend.Handle) (backend.State, error) {
			return backend.StateExited, nil
		},
		LaunchFunc: func(_ context.Context, _ *backend.Spec, reuse *backend.Handle) (backend.Handle, error) {
			assert.Assert(t, reuse != nil)
			assert.Equal(t, reuse.ID, "old-id")
			return backend.Handle{ID: "new-id"}, nil
		},
		// Start spawns a supervise goroutine; park it until the test's ctx ends.
		WaitFunc: func(ctx context.Context, _ backend.Handle) (backend.ExitCode, error) {
			<-ctx.Done()
			return 0, ctx.Err()
		},
	}
	mock := &gritzclient.ClientMock{
		CreateTaskTokenFunc: func(_ context.Context, _ *gritzv1.CreateTaskTokenRequest) (*gritzv1.CreateTaskTokenResponse, error) {
			return &gritzv1.CreateTaskTokenResponse{Token: "t"}, nil
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

	// Act
	err = r.Start(t.Context(), task)

	// Assert - the new handle replaced the old record.
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(be.LaunchCalls(), 1))
	rec, ok, err := store.Read(6)
	assert.NilError(t, err)
	assert.Equal(t, ok, true)
	assert.Equal(t, rec.ID, "new-id")
}

func TestRunnerList(t *testing.T) {
	t.Parallel()
	// Arrange - two tracked records; List maps each to a Sandbox via Probe.
	store := testStore(t,
		taskstate.Record{TaskID: 1, Type: "docker", ID: "c1"},
		taskstate.Record{TaskID: 2, Type: "docker", ID: "c2"},
	)
	state := map[string]backend.State{"c1": backend.StateRunning, "c2": backend.StateExited}
	be := &backend.BackendMock{
		ProbeFunc: func(_ context.Context, h backend.Handle) (backend.State, error) {
			return state[h.ID], nil
		},
	}
	mock := &gritzclient.ClientMock{}
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	r, err := New(Options{Client: mock, Backend: be, Store: store, RunnerID: "test-runner", Concurrency: 1, Queue: queue})
	assert.NilError(t, err)

	// Act
	sandboxes, err := r.List(t.Context())

	// Assert
	assert.NilError(t, err)
	got := map[int64]backend.State{}
	for _, sb := range sandboxes {
		got[sb.TaskID] = sb.State
	}
	assert.DeepEqual(t, got, map[int64]backend.State{1: backend.StateRunning, 2: backend.StateExited})
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
	be := &backend.BackendMock{
		SignalFunc: func(_ context.Context, _ backend.Handle) (bool, error) {
			return false, nil
		},
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

	// Act
	err = r.Poll(t.Context())
	assert.NilError(t, err)

	// Assert - no record, so the backend is never signalled and the runner
	// emits "stopped" itself.
	assert.Assert(t, cmp.Len(be.SignalCalls(), 0))
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
	be := &backend.BackendMock{
		SignalFunc: func(_ context.Context, h backend.Handle) (bool, error) {
			assert.Equal(t, h.ID, "c7")
			return true, nil
		},
	}
	store := testStore(t, taskstate.Record{TaskID: 7, Type: "docker", ID: "c7"})
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	r, err := New(Options{Client: mock, Backend: be, Store: store, RunnerID: "test-runner", Concurrency: 1, Queue: queue})
	assert.NilError(t, err)

	// Act
	err = r.Poll(t.Context())
	assert.NilError(t, err)

	// Assert - the driver was signalled and owns the terminal report.
	assert.Assert(t, cmp.Len(be.SignalCalls(), 1))
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
}

func TestRunnerSupervise_ReportLost(t *testing.T) {
	t.Parallel()
	// Arrange - Wait reports the exit as lost (non-zero code): the driver's
	// terminal report never reached the server, so the runner emits "failed".
	mock := &gritzclient.ClientMock{
		SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
			return &gritzv1.SubmitRunnerEventsResponse{}, nil
		},
	}
	be := &backend.BackendMock{
		WaitFunc: func(_ context.Context, h backend.Handle) (backend.ExitCode, error) {
			assert.Equal(t, h.ID, "c2")
			return backend.ExitLost, nil
		},
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
	assert.Assert(t, r.sem.TryAcquire(1)) // the slot supervise will release

	// Act - supervise the run at version 4; the emitted failed is scoped to it.
	r.supervise(t.Context(), 2, 4, backend.Handle{ID: "c2"})

	// Assert - a failed event is emitted, scoped to the run's version, and the
	// concurrency slot is released.
	events := submitted(t, mock, queue)
	assert.Equal(t, len(events), 1)
	assert.DeepEqual(t,
		events[0],
		&gritzv1.RunnerEvent{Event: "failed", TaskId: 2, Version: 4, Reason: "sandbox exited with status code -1"},
		protocmp.Transform(),
	)
	assert.Assert(t, r.sem.TryAcquire(1)) // the slot was released
}

func TestRunnerSupervise_CleanExit(t *testing.T) {
	t.Parallel()
	// Arrange - Wait returns a clean exit (0): the driver already reported, so
	// no event is owed, but the slot is still released.
	mock := &gritzclient.ClientMock{}
	be := &backend.BackendMock{
		WaitFunc: func(_ context.Context, _ backend.Handle) (backend.ExitCode, error) {
			return 0, nil
		},
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
	assert.Assert(t, r.sem.TryAcquire(1))

	// Act
	r.supervise(t.Context(), 3, 1, backend.Handle{ID: "c3"})

	// Assert - no event, slot released.
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
	assert.Assert(t, r.sem.TryAcquire(1))
}

func TestRunnerSupervise_Shutdown(t *testing.T) {
	t.Parallel()
	// Arrange - Wait returns context.Canceled: the runner is shutting down, the
	// sandbox stays alive for next-boot rehydration. No event, slot NOT released.
	mock := &gritzclient.ClientMock{}
	be := &backend.BackendMock{
		WaitFunc: func(_ context.Context, _ backend.Handle) (backend.ExitCode, error) {
			return 0, context.Canceled
		},
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
	assert.Assert(t, r.sem.TryAcquire(1))

	// Act
	r.supervise(t.Context(), 4, 1, backend.Handle{ID: "c4"})

	// Assert - no event, and the slot is still held (not released on shutdown).
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
	assert.Assert(t, !r.sem.TryAcquire(1))
}

func TestRunnerLoad(t *testing.T) {
	t.Parallel()
	// Arrange - a running sandbox (re-attached), an exited husk whose task is
	// still running (lost-report backstop), and a gone sandbox whose record is
	// dropped.
	status := map[int64]gritzv1.TaskStatus{
		2: gritzv1.TaskStatus_RUNNING,
		3: gritzv1.TaskStatus_RUNNING,
	}
	mock := &gritzclient.ClientMock{
		GetTaskFunc: func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
			return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Id: req.Id, Status: status[req.Id]}}, nil
		},
		SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
			return &gritzv1.SubmitRunnerEventsResponse{}, nil
		},
	}
	state := map[string]backend.State{
		"c1": backend.StateRunning,
		"c2": backend.StateExited,
		"c3": backend.StateGone,
	}
	waited := make(chan string, 1)
	be := &backend.BackendMock{
		ProbeFunc: func(_ context.Context, h backend.Handle) (backend.State, error) {
			return state[h.ID], nil
		},
		WaitFunc: func(ctx context.Context, h backend.Handle) (backend.ExitCode, error) {
			waited <- h.ID // the running sandbox got a supervise goroutine
			<-ctx.Done()
			return 0, ctx.Err()
		},
	}
	store := testStore(t,
		taskstate.Record{TaskID: 1, Type: "docker", ID: "c1"},
		taskstate.Record{TaskID: 2, Type: "docker", ID: "c2"},
		taskstate.Record{TaskID: 3, Type: "docker", ID: "c3"},
	)
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
	assert.NilError(t, err)

	// Assert - the exited husk and gone sandbox (both still-running tasks) each
	// emit "failed"; the gone record is dropped, the others kept.
	events := submitted(t, mock, queue)
	assert.DeepEqual(t, events, []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 2, Reason: "sandbox exited"},
		{Event: "failed", TaskId: 3, Reason: "sandbox exited"},
	}, protocmp.Transform())

	_, ok, err := store.Read(3)
	assert.NilError(t, err)
	assert.Equal(t, ok, false) // gone record dropped
	_, ok, err = store.Read(1)
	assert.NilError(t, err)
	assert.Equal(t, ok, true) // running record kept

	// The running sandbox was re-attached via a supervise goroutine.
	select {
	case id := <-waited:
		assert.Equal(t, id, "c1")
	case <-time.After(3 * time.Second):
		t.Fatal("running sandbox was not supervised")
	}

	// The semaphore was seeded with the one running sandbox.
	assert.Assert(t, r.sem.TryAcquire(4))
	assert.Assert(t, !r.sem.TryAcquire(1))
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
			// Arrange - one exited husk whose task is still RUNNING on the server,
			// so the boot-time backstop fires a "failed".
			mock := &gritzclient.ClientMock{
				GetTaskFunc: func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
					return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Id: req.Id, Status: gritzv1.TaskStatus_RUNNING}}, nil
				},
				SubmitRunnerEventsFunc: func(_ context.Context, _ *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
					return &gritzv1.SubmitRunnerEventsResponse{}, nil
				},
			}
			be := &backend.BackendMock{
				ProbeFunc: func(_ context.Context, _ backend.Handle) (backend.State, error) {
					return backend.StateExited, nil
				},
			}
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

			// Act - Load drives the exited husk through failIfTaskRunning, which
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
	// Arrange
	archived := map[int64]bool{1: true, 2: false}
	mock := &gritzclient.ClientMock{
		GetTaskFunc: func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
			if req.Id == 4 {
				return nil, connect.NewError(connect.CodeNotFound, errors.New("task not found"))
			}
			return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Id: req.Id, Archived: archived[req.Id]}}, nil
		},
	}
	state := map[string]backend.State{
		"c1": backend.StateExited,  // archived -> removed
		"c2": backend.StateExited,  // not archived -> kept
		"c3": backend.StateRunning, // running -> not considered
		"c4": backend.StateExited,  // deleted -> removed
	}
	be := &backend.BackendMock{
		ProbeFunc: func(_ context.Context, h backend.Handle) (backend.State, error) {
			return state[h.ID], nil
		},
		DestroyFunc: func(_ context.Context, _ backend.Handle) error {
			return nil
		},
	}
	store := testStore(t,
		taskstate.Record{TaskID: 1, Type: "docker", ID: "c1"},
		taskstate.Record{TaskID: 2, Type: "docker", ID: "c2"},
		taskstate.Record{TaskID: 3, Type: "docker", ID: "c3"},
		taskstate.Record{TaskID: 4, Type: "docker", ID: "c4"},
	)
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	r, err := New(Options{Client: mock, Backend: be, Store: store, RunnerID: "test-runner", Concurrency: 1, Queue: queue})
	assert.NilError(t, err)

	// Act
	err = r.Prune(t.Context())
	assert.NilError(t, err)

	// Assert - archived/deleted tasks' sandboxes are destroyed and their
	// records removed.
	var destroyed []string
	for _, call := range be.DestroyCalls() {
		destroyed = append(destroyed, call.H.ID)
	}
	assert.DeepEqual(t, destroyed, []string{"c1", "c4"})
	for _, id := range []int64{1, 4} {
		_, ok, err := store.Read(id)
		assert.NilError(t, err)
		assert.Equal(t, ok, false)
	}
	// Kept records survive.
	_, ok, err := store.Read(2)
	assert.NilError(t, err)
	assert.Equal(t, ok, true)
}

func TestRunnerStart_Gone(t *testing.T) {
	t.Parallel()
	// Arrange - a task whose recorded sandbox has vanished. Launch returns
	// ErrGone (it never creates a fresh sandbox on the reuse path), so Start
	// drops the dangling record and propagates ErrGone to its caller.
	task := &model.Task{ID: 8, Runner: "test-runner", Workspace: "test", Version: 1}
	store := testStore(t, taskstate.Record{TaskID: 8, Type: "docker", ID: "old-id"})
	be := &backend.BackendMock{
		ValidateWorkspaceFunc: func(_ *workspace.Workspace) error { return nil },
		ProbeFunc: func(_ context.Context, _ backend.Handle) (backend.State, error) {
			// Probe races Launch; report exited so the reuse handle is passed on
			// and Launch's ErrGone guard is the authoritative one.
			return backend.StateExited, nil
		},
		LaunchFunc: func(_ context.Context, _ *backend.Spec, reuse *backend.Handle) (backend.Handle, error) {
			assert.Assert(t, reuse != nil)
			return backend.Handle{}, backend.ErrGone
		},
	}
	mock := &gritzclient.ClientMock{
		CreateTaskTokenFunc: func(_ context.Context, _ *gritzv1.CreateTaskTokenRequest) (*gritzv1.CreateTaskTokenResponse, error) {
			return &gritzv1.CreateTaskTokenResponse{Token: "t"}, nil
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

	// Act
	err = r.Start(t.Context(), task)

	// Assert - ErrGone surfaced (the caller emits failed + releases the slot) and
	// the dangling record was removed. No supervise goroutine was spawned.
	assert.Assert(t, errors.Is(err, backend.ErrGone))
	_, ok, err := store.Read(8)
	assert.NilError(t, err)
	assert.Equal(t, ok, false)
}

func TestRunnerStart_GoneViaProbe(t *testing.T) {
	t.Parallel()
	// Arrange - the pre-Launch Probe already reports the sandbox gone, so Start
	// short-circuits without minting a token or calling Launch.
	task := &model.Task{ID: 9, Runner: "test-runner", Workspace: "test", Version: 1}
	store := testStore(t, taskstate.Record{TaskID: 9, Type: "docker", ID: "old-id"})
	be := &backend.BackendMock{
		ProbeFunc: func(_ context.Context, _ backend.Handle) (backend.State, error) {
			return backend.StateGone, nil
		},
	}
	mock := &gritzclient.ClientMock{}
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	r, err := New(Options{Client: mock, Backend: be, Store: store, RunnerID: "test-runner", Concurrency: 1, Queue: queue})
	assert.NilError(t, err)

	// Act
	err = r.Start(t.Context(), task)

	// Assert - gone short-circuits: ErrGone, record dropped, no Launch, no token.
	assert.Assert(t, errors.Is(err, backend.ErrGone))
	assert.Assert(t, cmp.Len(be.LaunchCalls(), 0))
	assert.Assert(t, cmp.Len(mock.CreateTaskTokenCalls(), 0))
	_, ok, err := store.Read(9)
	assert.NilError(t, err)
	assert.Equal(t, ok, false)
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
	be := &backend.BackendMock{
		SignalFunc: func(_ context.Context, _ backend.Handle) (bool, error) {
			return false, nil // no running sandbox to signal → runner emits "stopped"
		},
	}
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
	r, err := New(Options{Client: mock, Backend: be, Store: testStore(t), RunnerID: "test-runner", Concurrency: 1, Queue: queue, Fatal: cancelCause})
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
	be := &backend.BackendMock{
		SignalFunc: func(_ context.Context, _ backend.Handle) (bool, error) {
			return false, nil
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
	r, err := New(Options{Client: mock, Backend: be, Store: testStore(t), RunnerID: "test-runner", Concurrency: 1, Queue: queue, Fatal: cancelCause})
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
