package runner

import (
	"context"
	"encoding/json"
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
		BackendV2: be,
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
		BackendV2: be,
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
		BackendV2: be,
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
	r, err := New(Options{Client: mock, Backend: be, BackendV2: &backend.BackendV2Mock{}, Store: store, RunnerID: "test-runner", Concurrency: 1, Queue: queue})
	assert.NilError(t, err)

	// Act
	err = r.Start(t.Context(), task)

	// Assert - the running sandbox is left alone: no Launch, no token minted.
	assert.ErrorIs(t, err, errRunning)
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
	r, err := New(Options{Client: mock, Backend: be, BackendV2: &backend.BackendV2Mock{}, Store: store, RunnerID: "test-runner", Concurrency: 1, Queue: queue, Workspaces: testWorkspaces()})
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
	be := &backend.BackendMock{}
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	r, err := New(Options{Client: mock, Backend: be, BackendV2: &backend.BackendV2Mock{}, Store: testStore(t), RunnerID: "test-runner", Concurrency: 1, Queue: queue})
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
	sb := &backend.SandboxV2Mock{
		StatusFunc: func(context.Context) (backend.Status, error) { return backend.StatusUp, nil },
		DialFunc: func(context.Context, string) (driverv1connect.DriverServiceClient, error) {
			return client, nil
		},
	}
	bev2 := &backend.BackendV2Mock{
		OpenFunc: func(backend.Handle) (backend.SandboxV2, error) { return sb, nil },
	}
	store := testStore(t, taskstate.Record{TaskID: 7, Version: 3, Type: "docker", ID: "c7"})
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	r, err := New(Options{Client: mock, Backend: &backend.BackendMock{}, BackendV2: bev2, Store: store, RunnerID: "test-runner", Concurrency: 1, Queue: queue})
	assert.NilError(t, err)

	// Act
	err = r.Poll(t.Context())
	assert.NilError(t, err)

	// Assert - the driver stopped the run and owns the terminal report.
	waitFinished(t, stream)
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
	r, err := New(Options{Client: mock, Backend: be, BackendV2: &backend.BackendV2Mock{}, Store: testStore(t), RunnerID: "test-runner", Concurrency: 1, Queue: queue})
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
	r, err := New(Options{Client: mock, Backend: be, BackendV2: &backend.BackendV2Mock{}, Store: testStore(t), RunnerID: "test-runner", Concurrency: 1, Queue: queue})
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
	r, err := New(Options{Client: mock, Backend: be, BackendV2: &backend.BackendV2Mock{}, Store: testStore(t), RunnerID: "test-runner", Concurrency: 1, Queue: queue})
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

// v1Record is a record written by v1 Launch: its Data carries the run version.
func v1Record(taskID int64, id string) taskstate.Record {
	return taskstate.Record{TaskID: taskID, Version: 3, Type: "docker", ID: id, Data: json.RawMessage(`{"version":3}`)}
}

// testBackendV2 returns a BackendV2 mock whose opened sandboxes report the
// status (or error) given for their ID. Destroy records the IDs it destroyed.
func testBackendV2(status map[string]backend.Status, statusErr map[string]error) (*backend.BackendV2Mock, *[]string) {
	var mu sync.Mutex
	var destroyed []string
	return &backend.BackendV2Mock{
		OpenFunc: func(h backend.Handle) (backend.SandboxV2, error) {
			return &backend.SandboxV2Mock{
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
		status     backend.Status
		statusErr  error
		wantEvents []*gritzv1.RunnerEvent
		wantRecord bool
		wantWait   bool // re-attached through v1 Wait
	}{
		{
			name:       "up",
			record:     v1Record(1, "c1"),
			status:     backend.StatusUp,
			wantRecord: true,
			wantWait:   true,
		},
		{
			// A parked husk is a lost-report backstop; its record is kept.
			name:       "parked",
			record:     v1Record(1, "c1"),
			status:     backend.StatusParked,
			wantEvents: []*gritzv1.RunnerEvent{{Event: "failed", TaskId: 1, Version: 3, Reason: "sandbox exited"}},
			wantRecord: true,
		},
		{
			// A gone sandbox is a lost-report backstop; its record is dropped.
			name:       "gone",
			record:     v1Record(1, "c1"),
			status:     backend.StatusGone,
			wantEvents: []*gritzv1.RunnerEvent{{Event: "failed", TaskId: 1, Version: 3, Reason: "sandbox exited"}},
		},
		{
			// A failed status check leaves the record alone.
			name:       "status error",
			record:     v1Record(1, "c1"),
			statusErr:  errors.New("boom"),
			wantRecord: true,
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
			waited := make(chan backend.Handle, 1)
			be := &backend.BackendMock{
				WaitFunc: func(ctx context.Context, h backend.Handle) (backend.ExitCode, error) {
					waited <- h // the up sandbox got a supervise goroutine
					<-ctx.Done()
					return 0, ctx.Err()
				},
			}
			bev2, destroyed := testBackendV2(
				map[string]backend.Status{"c1": tt.status},
				map[string]error{"c1": tt.statusErr},
			)
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
			r, err := New(Options{Client: mock, Backend: be, BackendV2: bev2, Store: store, RunnerID: "test-runner", Concurrency: 5, Queue: queue})
			assert.NilError(t, err)

			// Act
			err = r.Load(ctx)

			// Assert
			assert.NilError(t, err)
			assert.DeepEqual(t, submitted(t, mock, queue), tt.wantEvents, protocmp.Transform())
			_, ok, err := store.Read(1)
			assert.NilError(t, err)
			assert.Equal(t, ok, tt.wantRecord)
			assert.Assert(t, cmp.Len(*destroyed, 0)) // Load never destroys
			var running int64
			if tt.wantWait {
				running = 1
				// Re-attached through v1 Wait, with the recorded v1 handle.
				select {
				case h := <-waited:
					assert.DeepEqual(t, h, backend.Handle{Type: "docker", ID: "c1", Data: tt.record.Data})
				case <-time.After(3 * time.Second):
					t.Fatal("up sandbox was not supervised")
				}
			} else {
				assert.Assert(t, cmp.Len(be.WaitCalls(), 0))
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
			bev2, _ := testBackendV2(map[string]backend.Status{"c1": backend.StatusParked}, nil)
			store := testStore(t, taskstate.Record{TaskID: 1, Version: tt.recVersion, Type: "docker", ID: "c1"})
			queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
				StoreDir: t.TempDir(),
				Client:   mock,
				Backoff:  backoff.NewConstantBackOff(0),
				Log:      slog.Default(),
			})
			assert.NilError(t, err)
			r, err := New(Options{Client: mock, Backend: &backend.BackendMock{}, BackendV2: bev2, Store: store, RunnerID: "test-runner", Concurrency: 1, Queue: queue})
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
	bev2, destroyed := testBackendV2(
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
	store := testStore(t,
		v1Record(1, "c1"),
		v1Record(2, "c2"),
		v1Record(3, "c3"),
		v1Record(4, "c4"),
		v1Record(5, "c5"),
		v1Record(7, "c7"),
	)
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	r, err := New(Options{Client: mock, Backend: &backend.BackendMock{}, BackendV2: bev2, Store: store, RunnerID: "test-runner", Concurrency: 1, Queue: queue})
	assert.NilError(t, err)

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
		{name: "up", records: []taskstate.Record{v1Record(1, "c1")}, status: backend.StatusUp, want: true},
		{name: "parked", records: []taskstate.Record{v1Record(1, "c1")}, status: backend.StatusParked},
		{name: "gone", records: []taskstate.Record{v1Record(1, "c1")}, status: backend.StatusGone},
		{name: "no record"},
		{name: "status error", records: []taskstate.Record{v1Record(1, "c1")}, err: errors.New("boom")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			bev2, _ := testBackendV2(
				map[string]backend.Status{"c1": tt.status},
				map[string]error{"c1": tt.err},
			)
			r, err := New(Options{Backend: &backend.BackendMock{}, BackendV2: bev2, Store: testStore(t, tt.records...), RunnerID: "test-runner"})
			assert.NilError(t, err)

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

// TestRunnerPoll_StartStillRunning covers a sandbox Running reports parked but
// Start's Probe reports running (a Lambda VM still suspending after its run):
// nothing is started, and the concurrency slot is given back.
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
	mock := &gritzclient.ClientMock{
		ListRunnerTasksFunc: func(_ context.Context, _ *gritzv1.ListRunnerTasksRequest) (*gritzv1.ListRunnerTasksResponse, error) {
			return &gritzv1.ListRunnerTasksResponse{Tasks: []*gritzv1.Task{task.Proto("")}}, nil
		},
	}
	be := &backend.BackendMock{
		ProbeFunc: func(_ context.Context, _ backend.Handle) (backend.State, error) {
			return backend.StateRunning, nil
		},
	}
	bev2, _ := testBackendV2(map[string]backend.Status{"c1": backend.StatusParked}, nil)
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	r, err := New(Options{Client: mock, Backend: be, BackendV2: bev2, Store: testStore(t, v1Record(1, "c1")), RunnerID: "test-runner", Concurrency: 1, Queue: queue})
	assert.NilError(t, err)

	// Act
	err = r.Poll(t.Context())
	assert.NilError(t, err)

	// Assert - no launch, no event, and the slot is free again.
	assert.Assert(t, cmp.Len(be.LaunchCalls(), 0))
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
	assert.Assert(t, r.sem.TryAcquire(1))
}

func TestRunnerRemove(t *testing.T) {
	t.Parallel()
	// A record written by v1 Launch carries the run version in Data.
	v1 := taskstate.Record{TaskID: 1, Version: 3, Type: "docker", ID: "c1", Data: json.RawMessage(`{"version":3}`)}
	tests := []struct {
		name        string
		records     []taskstate.Record
		destroyErr  error
		wantErr     bool
		wantOpened  []backend.Handle
		wantRecords bool
	}{
		{
			name:       "v1 handle",
			records:    []taskstate.Record{v1},
			wantOpened: []backend.Handle{{Type: "docker", ID: "c1", Data: v1.Data}},
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
			records:     []taskstate.Record{v1},
			destroyErr:  errors.New("boom"),
			wantErr:     true,
			wantOpened:  []backend.Handle{{Type: "docker", ID: "c1", Data: v1.Data}},
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
			sb := &backend.SandboxV2Mock{
				DestroyFunc: func(context.Context) error { return tt.destroyErr },
			}
			bev2 := &backend.BackendV2Mock{
				OpenFunc: func(backend.Handle) (backend.SandboxV2, error) { return sb, nil },
			}
			store := testStore(t, tt.records...)
			r, err := New(Options{Backend: &backend.BackendMock{}, BackendV2: bev2, Store: store, RunnerID: "test-runner"})
			assert.NilError(t, err)

			// Act
			err = r.Remove(t.Context(), 1)

			// Assert
			if tt.wantErr {
				assert.ErrorIs(t, err, tt.destroyErr)
			} else {
				assert.NilError(t, err)
			}
			var opened []backend.Handle
			for _, call := range bev2.OpenCalls() {
				opened = append(opened, call.H)
			}
			assert.DeepEqual(t, opened, tt.wantOpened)
			assert.Equal(t, len(sb.DestroyCalls()), len(tt.wantOpened))
			_, ok, err := store.Read(1)
			assert.NilError(t, err)
			assert.Equal(t, ok, tt.wantRecords)
		})
	}
}

// sleepForever is an agent config whose dummy agent runs until it is stopped.
var sleepForever = &agent.Config{Type: agent.TypeDummy, Dummy: &agent.DummyOptions{Sleep: -1}}

// testDriver serves a driver server over an in-memory Connect client. With cfg
// set, it starts run version of taskID with that agent config and returns the
// run's stream; with cfg nil the driver has never run.
func testDriver(t *testing.T, taskID, version int64, cfg *agent.Config) (driverv1connect.DriverServiceClient, *connect.ServerStreamForClient[driverv1.RunResponse]) {
	t.Helper()
	dir := t.TempDir()
	configs := agent.ConfigStore(filepath.Join(dir, "config"))
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
		FileRoot:   filepath.Join(dir, "root"),
		NewClient:  func(_, _ string) gritzclient.Client { return gritz },
		KeepAlive:  20 * time.Millisecond,
	})
	assert.NilError(t, err)
	mux := http.NewServeMux()
	mux.Handle(srv.Handler())
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		assert.NilError(t, srv.Shutdown(ctx))
	})
	client := driverv1connect.NewDriverServiceClient(hs.Client(), hs.URL)
	if cfg == nil {
		return client, nil
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
	return client, stream
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
			sb := &backend.SandboxV2Mock{
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
			bev2 := &backend.BackendV2Mock{
				OpenFunc: func(backend.Handle) (backend.SandboxV2, error) { return sb, nil },
			}
			var recs []taskstate.Record
			if tt.record {
				recs = append(recs, v1Record(1, "c1"))
			}
			// The v1 backend is unmocked: Kill must not call Signal.
			r, err := New(Options{Backend: &backend.BackendMock{}, BackendV2: bev2, Store: testStore(t, recs...), RunnerID: "test-runner"})
			assert.NilError(t, err)

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
				assert.DeepEqual(t, bev2.OpenCalls()[0].H, backend.Handle{Type: "docker", ID: "c1", Data: recs[0].Data})
			} else {
				assert.Assert(t, cmp.Len(bev2.OpenCalls(), 0))
			}
		})
	}
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
	r, err := New(Options{Client: mock, Backend: be, BackendV2: &backend.BackendV2Mock{}, Store: store, RunnerID: "test-runner", Concurrency: 1, Queue: queue, Workspaces: testWorkspaces()})
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
	r, err := New(Options{Client: mock, Backend: be, BackendV2: &backend.BackendV2Mock{}, Store: store, RunnerID: "test-runner", Concurrency: 1, Queue: queue})
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
	// No record, so there is no run to stop → the runner emits "stopped".
	be := &backend.BackendMock{}
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
	r, err := New(Options{Client: mock, Backend: be, BackendV2: &backend.BackendV2Mock{}, Store: testStore(t), RunnerID: "test-runner", Concurrency: 1, Queue: queue, Fatal: cancelCause})
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
	be := &backend.BackendMock{}
	queue, err := NewRunnerEventOutbox(RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   mock,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)

	ctx, cancelCause := context.WithCancelCause(context.Background())
	r, err := New(Options{Client: mock, Backend: be, BackendV2: &backend.BackendV2Mock{}, Store: testStore(t), RunnerID: "test-runner", Concurrency: 1, Queue: queue, Fatal: cancelCause})
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
