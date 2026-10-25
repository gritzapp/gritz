package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/proto/gritz/v1/gritzv1connect"
	"github.com/gritzapp/gritz/internal/runner"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/backend/docker"
	"github.com/gritzapp/gritz/internal/runner/backend/fake"
	"github.com/gritzapp/gritz/internal/runner/taskstate"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/dockerx"
	"github.com/gritzapp/gritz/internal/x/testx"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestTaskManagerStart(t *testing.T) {
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
	queue, err := runner.NewRunnerEventOutbox(runner.RunnerEventOutboxOptions{
		StoreDir: t.TempDir(),
		Client:   client,
		Backoff:  backoff.NewConstantBackOff(0),
		Log:      slog.Default(),
	})
	assert.NilError(t, err)
	store := testStore(t)
	m, err := runner.NewTaskManager(runner.TaskManagerOptions{
		Client:    client,
		Backend:   be,
		Store:     store,
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
	t.Cleanup(func() { m.Close() })

	// Remove any leftover container from a previous aborted run.
	_ = dc.ContainerRemove(t.Context(), "gritz-1", container.RemoveOptions{Force: true})

	// Start a task
	err = m.Start(t.Context(), task)
	assert.NilError(t, err)

	// The TaskManager records the launched handle in the store.
	rec, ok, err := store.Read(task.ID)
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

// TestTaskManagerSpec_PreCreatesLogDir asserts the sandbox spec ships a directory
// entry for the driver's /gritz/log parent, created 0777 so a non-root driver
// can write the append-only log file there (the driver's MkdirAll is only a
// fallback).
func TestTaskManagerSpec_PreCreatesLogDir(t *testing.T) {
	t.Parallel()
	// Arrange
	be := newFake(t, driverHandler(t, 1, nil))
	m, _, _ := newTestManager(t, runner.TaskManagerOptions{
		Backend:   be,
		Store:     testStore(t),
		ServerURL: "http://localhost",
		Workspaces: &workspace.Config{
			Workspaces: map[string]workspace.Workspace{
				"test": {
					Container: workspace.Container{Image: "alpine:latest"},
					Agent:     workspace.Agent{Type: "dummy"},
				},
			},
		},
	})
	task := &model.Task{ID: 1, Runner: "test-runner", Workspace: "test", Version: 1}

	// Act
	assert.NilError(t, m.Start(t.Context(), task))
	assert.NilError(t, m.Wait(t.Context()))

	// Assert - the spec the sandbox was created from has the log dir.
	specs := be.Snapshot().Specs
	assert.Assert(t, cmp.Len(specs, 1))
	logDir := filepath.Dir(agent.DefaultLogPath)
	var found bool
	for _, f := range specs[0].Files {
		if f.Path == logDir {
			found = true
			assert.Assert(t, f.Dir)
			assert.Equal(t, f.Mode, int64(0o777))
		}
	}
	assert.Assert(t, found, "spec should pre-create the log dir %q", logDir)
}

// TestTaskManagerSpec_Secrets asserts the TaskManager hands the backend the server URL,
// the task token and the declared workspace secrets in their own fields, and
// gives the run the GRITZ_* environment the agent's tools read. The secrets stay
// out of Env: the driver adds them to the run's environment and masks them in
// the log it ships.
func TestTaskManagerSpec_Secrets(t *testing.T) {
	t.Parallel()
	// Arrange
	be := newFake(t, driverHandler(t, 1, nil))
	m, _, _ := newTestManager(t, runner.TaskManagerOptions{
		Backend:   be,
		Store:     testStore(t),
		ServerURL: "http://localhost",
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
	})
	task := &model.Task{ID: 1, Runner: "test-runner", Workspace: "test", Version: 1}

	// Act
	assert.NilError(t, m.Start(t.Context(), task))
	assert.NilError(t, m.Wait(t.Context()))

	// Assert
	specs := be.Snapshot().Specs
	assert.Assert(t, cmp.Len(specs, 1))
	spec := specs[0]
	assert.Equal(t, spec.ServerURL, "http://localhost")
	assert.Equal(t, spec.Token, "t")
	assert.DeepEqual(t, spec.Env, map[string]string{
		"GRITZ_TASK_ID": "1",
		"GRITZ_TOKEN":   "t",
		"GRITZ_SERVER":  "http://localhost",
	})
	assert.DeepEqual(t, spec.Secrets, map[string]string{
		"GH_TOKEN":  "gho_secretvalue",
		"NPM_TOKEN": "npm_secretvalue",
	})
}

// TestTaskManagerRunSpec_RunEnv asserts a backend's RunEnv reaches the run's
// environment under the TaskManager's own.
func TestTaskManagerRunSpec_RunEnv(t *testing.T) {
	t.Parallel()
	// Arrange - a backend whose runs get an environment of their own.
	be := fake.New(fake.Options{
		Driver: drivers(driverHandler(t, 1, nil)),
		RunEnv: func(*workspace.Workspace) map[string]string {
			return map[string]string{"FOO": "bar", "GRITZ_TASK_ID": "shadowed"}
		},
	})
	t.Cleanup(func() { assert.NilError(t, be.Close()) })
	m, _, _ := testManager(t, be, testStore(t))
	task := &model.Task{ID: 1, Runner: "test-runner", Workspace: "test", Version: 1}

	// Act
	assert.NilError(t, m.Start(t.Context(), task))
	assert.NilError(t, m.Wait(t.Context()))

	// Assert - the environment of the run the driver was asked to start is
	// RunEnv's, overridden by the TaskManager's.
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Assert(t, cmp.Len(sbx.RunSpecs, 1))
	assert.DeepEqual(t, sbx.RunSpecs[0].GetEnv(), map[string]string{
		"FOO":           "bar",
		"GRITZ_TASK_ID": "1",
		"GRITZ_TOKEN":   "t",
		"GRITZ_SERVER":  "",
	})
}

func TestTaskManagerPoll_StopWithoutSandbox(t *testing.T) {
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
	be := fake.New(fake.Options{})
	m, mock, queue := testManager(t, be, testStore(t))
	mock.ListRunnerTasksFunc = func(_ context.Context, _ *gritzv1.ListRunnerTasksRequest) (*gritzv1.ListRunnerTasksResponse, error) {
		return &gritzv1.ListRunnerTasksResponse{Tasks: []*gritzv1.Task{task.Proto("")}}, nil
	}

	// Act
	err := m.Poll(t.Context())
	assert.NilError(t, err)

	// Assert - no record, so there is no driver to stop and the TaskManager emits
	// "stopped" itself.
	waitQueued(t, queue, 1)
	assert.DeepEqual(t, submitted(t, mock, queue), []*gritzv1.RunnerEvent{
		{Event: "stopped", TaskId: 7, Version: 3},
	}, protocmp.Transform())
}

func TestTaskManagerPoll_StopSignalled(t *testing.T) {
	t.Parallel()
	// Arrange - task being cancelled with a tracked sandbox, up and running
	// the task's run.
	task := &model.Task{
		ID:        7,
		Runner:    "test-runner",
		Workspace: "test",
		Status:    model.TaskStatusCancelling,
		Command:   model.TaskCommandStop,
		Version:   3,
	}
	h, client := serveDriver(t, 7, 3, sleepForever)
	be := newFake(t, h)
	be.Add("c7", fake.Up)
	rec := taskstate.Record{TaskID: 7, Version: 3, Type: "fake", ID: "c7"}
	m, mock, queue := testManager(t, be, testStore(t, rec))
	mock.ListRunnerTasksFunc = func(_ context.Context, _ *gritzv1.ListRunnerTasksRequest) (*gritzv1.ListRunnerTasksResponse, error) {
		return &gritzv1.ListRunnerTasksResponse{Tasks: []*gritzv1.Task{task.Proto("")}}, nil
	}
	_, err := m.Runner(7).Restore(t.Context(), rec)
	assert.NilError(t, err)

	// Act
	err = m.Poll(t.Context())
	assert.NilError(t, err)

	// Assert - the driver stopped the run and owns the terminal report.
	waitFinished(t, client, 3)
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
}

// TestTaskManagerPoll_SlowDriver covers a driver that is slow to come up: Poll
// returns once the sandbox is started, without waiting for it, and the run is
// started once the driver answers.
func TestTaskManagerPoll_SlowDriver(t *testing.T) {
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
	be := newFake(t, driverHandler(t, 1, ready))
	m, mock, queue := testManager(t, be, testStore(t))
	mock.ListRunnerTasksFunc = func(_ context.Context, _ *gritzv1.ListRunnerTasksRequest) (*gritzv1.ListRunnerTasksResponse, error) {
		return &gritzv1.ListRunnerTasksResponse{Tasks: []*gritzv1.Task{task.Proto("")}}, nil
	}

	// Act - poll while the driver is not up, then bring it up.
	assert.NilError(t, m.Poll(t.Context()))
	waitDialed(t, be, "c1", 1)
	close(ready)
	assert.NilError(t, m.Wait(t.Context()))

	// Assert - Poll returned with the sandbox started and the run pending; the
	// run then started, and the driver reported it, so no event is owed and
	// the sandbox is parked.
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.Starts, 1)
	assert.Equal(t, sbx.State, fake.Parked)
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
}

// TestTaskManagerPoll_StartStillRunning covers a start command for a task whose
// previous run is still followed by Run while Running reports its sandbox parked
// (a Lambda VM suspending after its run): nothing is started, and the
// concurrency slot is given back.
func TestTaskManagerPoll_StartStillRunning(t *testing.T) {
	t.Parallel()
	// Arrange - task 1's TaskRunner is claimed, and its sandbox parked. The
	// manager has one slot.
	task := &model.Task{
		ID:        1,
		Runner:    "test-runner",
		Workspace: "test",
		Status:    model.TaskStatusCompleted,
		Command:   model.TaskCommandStart,
		Version:   4,
	}
	be := newFake(t, driverHandler(t, 1, nil))
	be.Add("c1", fake.Up)
	rec := taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"}
	m, mock, queue := testManager(t, be, testStore(t, rec))
	_, err := m.Runner(1).Restore(t.Context(), rec)
	assert.NilError(t, err)
	be.Set("c1", fake.Parked)
	mock.ListRunnerTasksFunc = func(_ context.Context, _ *gritzv1.ListRunnerTasksRequest) (*gritzv1.ListRunnerTasksResponse, error) {
		return &gritzv1.ListRunnerTasksResponse{Tasks: []*gritzv1.Task{task.Proto("")}}, nil
	}

	// Act
	err = m.Poll(t.Context())
	assert.NilError(t, err)

	// Assert - no sandbox started or created, and no event.
	snap := be.Snapshot()
	assert.Assert(t, cmp.Len(snap.Specs, 0))
	sbx, _ := snap.Sandbox("c1")
	assert.Equal(t, sbx.Starts, 0)
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)

	// Act - a start command for another task.
	other := &model.Task{
		ID:        2,
		Runner:    "test-runner",
		Workspace: "test",
		Status:    model.TaskStatusPending,
		Command:   model.TaskCommandStart,
		Version:   1,
	}
	mock.ListRunnerTasksFunc = func(_ context.Context, _ *gritzv1.ListRunnerTasksRequest) (*gritzv1.ListRunnerTasksResponse, error) {
		return &gritzv1.ListRunnerTasksResponse{Tasks: []*gritzv1.Task{other.Proto("")}}, nil
	}
	assert.NilError(t, m.Poll(t.Context()))
	assert.NilError(t, m.Wait(t.Context()))

	// Assert - the slot was free again, so task 2 got a sandbox.
	specs := be.Snapshot().Specs
	assert.Assert(t, cmp.Len(specs, 1))
	assert.Equal(t, specs[0].TaskID, int64(2))
}

func TestTaskManagerStart_Create(t *testing.T) {
	t.Parallel()
	// Arrange - a task with no record, whose created sandbox's driver runs the
	// workspace's agent to completion.
	task := &model.Task{ID: 1, Runner: "test-runner", Workspace: "test", Version: 1}
	be := newFake(t, driverHandler(t, 1, nil))
	store := testStore(t)
	m, _, queue := testManager(t, be, store)

	// Act
	err := m.Start(t.Context(), task)

	// Assert - the sandbox was created for the task's run and started.
	assert.NilError(t, err)
	snap := be.Snapshot()
	assert.Assert(t, cmp.Len(snap.Specs, 1))
	assert.Equal(t, snap.Specs[0].TaskID, int64(1))
	assert.Equal(t, snap.Specs[0].Version, int64(1))
	sbx, _ := snap.Sandbox("c1")
	assert.Equal(t, sbx.Starts, 1)

	// Assert - the driver ran the run and reported it, so no event is owed,
	// and the sandbox is parked.
	assert.NilError(t, m.Wait(t.Context()))
	sbx, _ = be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Parked)
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
	assertTracked(t, m, be, store, 1, &taskstate.Record{TaskID: 1, Version: 1, Type: "fake", ID: "c1"})
}

// TestTaskManagerStart_CreateFails covers a sandbox that cannot be created: the task
// gets no record, and its TaskRunner no sandbox.
func TestTaskManagerStart_CreateFails(t *testing.T) {
	t.Parallel()
	// Arrange
	task := &model.Task{ID: 1, Runner: "test-runner", Workspace: "test", Version: 1}
	boom := errors.New("boom")
	be := fake.New(fake.Options{})
	be.FailNext(fake.OpCreate, "", boom)
	store := testStore(t)
	m, _, _ := testManager(t, be, store)

	// Act
	err := m.Start(t.Context(), task)

	// Assert
	assert.ErrorIs(t, err, boom)
	assert.Assert(t, cmp.Len(be.Snapshot().Sandboxes, 0))
	assertTracked(t, m, be, store, 1, nil)
}

// TestTaskManagerStart_Restart covers a task restarted on its recorded sandbox,
// whether an older runner or this one recorded it: the sandbox is reused, and
// the record moves to the sandbox's current handle at the new version.
func TestTaskManagerStart_Restart(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		record taskstate.Record
	}{
		// A record written by an older runner carries the run version in Data.
		{name: "legacy handle", record: taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1", Data: json.RawMessage(`{"version":3}`)}},
		{name: "current handle", record: taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - the recorded sandbox is parked, and its driver has
			// never run version 4.
			task := &model.Task{ID: 1, Runner: "test-runner", Workspace: "test", Version: 4}
			be := newFake(t, driverHandler(t, 4, nil))
			be.Add("c1", fake.Parked)
			store := testStore(t, tt.record)
			m, _, queue := testManager(t, be, store)
			_, err := m.Runner(1).Restore(t.Context(), tt.record)
			assert.NilError(t, err)

			// Act
			err = m.Start(t.Context(), task)
			assert.NilError(t, m.Wait(t.Context()))

			// Assert - no second sandbox was created: the recorded one was
			// started and ran the new version, which the driver reported.
			assert.NilError(t, err)
			snap := be.Snapshot()
			assert.Assert(t, cmp.Len(snap.Specs, 0))
			sbx, _ := snap.Sandbox("c1")
			assert.Equal(t, sbx.Starts, 1)
			assert.Equal(t, sbx.State, fake.Parked)
			n, err := queue.Len()
			assert.NilError(t, err)
			assert.Equal(t, n, 0)
			assertTracked(t, m, be, store, 1, &taskstate.Record{TaskID: 1, Version: 4, Type: "fake", ID: "c1"})
		})
	}
}

// TestTaskManagerWait covers Wait after a run: it returns once the run's
// goroutine has, with the sandbox parked.
func TestTaskManagerWait(t *testing.T) {
	t.Parallel()
	// Arrange
	task := &model.Task{ID: 1, Runner: "test-runner", Workspace: "test", Version: 1}
	be := newFake(t, driverHandler(t, 1, nil))
	m, _, _ := testManager(t, be, testStore(t))
	assert.NilError(t, m.Start(t.Context(), task))

	// Act
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	err := m.Wait(ctx)

	// Assert - the run is done: its sandbox is parked.
	assert.NilError(t, err)
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Parked)
}

// TestTaskManagerWait_Done covers Wait on a done ctx while a run is still
// followed: it returns ctx's cause without waiting for the run.
func TestTaskManagerWait_Done(t *testing.T) {
	t.Parallel()
	// Arrange - a run whose driver fails every call until ready is closed.
	task := &model.Task{ID: 1, Runner: "test-runner", Workspace: "test", Version: 1}
	ready := make(chan struct{})
	be := newFake(t, driverHandler(t, 1, ready))
	m, _, _ := testManager(t, be, testStore(t))
	assert.NilError(t, m.Start(t.Context(), task))
	boom := errors.New("boom")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(boom)

	// Act
	err := m.Wait(ctx)

	// Assert - Wait returned ctx's cause; once the driver comes up, the run
	// finishes and Wait returns nil.
	assert.ErrorIs(t, err, boom)
	close(ready)
	ctx, cancelTimeout := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelTimeout()
	assert.NilError(t, m.Wait(ctx))
}

// TestTaskManagerStart_NotReady covers a driver that never comes up: the run
// is reported failed, the sandbox parked, and the record kept.
func TestTaskManagerStart_NotReady(t *testing.T) {
	t.Parallel()
	// Arrange
	task := &model.Task{ID: 1, Runner: "test-runner", Workspace: "test", Version: 1}
	be := newFake(t, driverHandler(t, 1, make(chan struct{})))
	store := testStore(t)
	m, mock, queue := testManager(t, be, store, func(opts *runner.TaskRunnerOptions) {
		opts.ReadyTimeout = 100 * time.Millisecond
	})

	// Act
	assert.NilError(t, m.Start(t.Context(), task))
	assert.NilError(t, m.Wait(t.Context()))

	// Assert
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Parked)
	waitQueued(t, queue, 1)
	events := submitted(t, mock, queue)
	assert.Assert(t, cmp.Len(events, 1))
	assert.Equal(t, events[0].Event, "failed")
	assert.Equal(t, events[0].Version, int64(1))
	assert.Assert(t, cmp.Contains(events[0].Reason, "driver server did not become ready"))
	assertTracked(t, m, be, store, 1, &taskstate.Record{TaskID: 1, Version: 1, Type: "fake", ID: "c1"})
}

// TestTaskManagerStart_StartFails covers a sandbox that cannot be started after
// Create: the record is kept, and Destroy destroys the sandbox.
func TestTaskManagerStart_StartFails(t *testing.T) {
	t.Parallel()
	// Arrange - the sandbox Start creates refuses to start.
	task := &model.Task{ID: 1, Runner: "test-runner", Workspace: "test", Version: 1}
	boom := errors.New("boom")
	be := fake.New(fake.Options{})
	be.FailNext(fake.OpStart, "", boom)
	store := testStore(t)
	m, _, _ := testManager(t, be, store)

	// Act
	err := m.Start(t.Context(), task)

	// Assert - Start failed with the record and sandbox kept and nothing run.
	// The record surviving the failed start also shows it was written before
	// the sandbox was started.
	assert.ErrorIs(t, err, boom)
	assertRecord(t, store, 1, &taskstate.Record{TaskID: 1, Version: 1, Type: "fake", ID: "c1"})
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Created)
	assert.Equal(t, sbx.Dials, 0)

	// Act - the task's sandbox is removed.
	assert.NilError(t, m.Runner(1).Destroy(t.Context()))

	// Assert - Destroy found c1, so the TaskRunner still held it; now it holds
	// nothing, and the record is gone.
	assert.DeepEqual(t, be.Snapshot().Destroyed, []string{"c1"})
	assertTracked(t, m, be, store, 1, nil)
}

// TestTaskManagerStart_Gone covers a task whose recorded sandbox has vanished:
// Start fails with ErrGone and leaves the TaskRunner with no sandbox, and the
// next Start creates a fresh one.
func TestTaskManagerStart_Gone(t *testing.T) {
	t.Parallel()
	// Arrange - task 8's recorded sandbox, restored parked, is then removed
	// behind the runner's back.
	rec := taskstate.Record{TaskID: 8, Version: 1, Type: "fake", ID: "c1"}
	be := newFake(t, driverHandler(t, 3, nil))
	be.Add("c1", fake.Parked)
	store := testStore(t, rec)
	m, _, queue := testManager(t, be, store)
	tr := m.Runner(8)
	_, err := tr.Restore(t.Context(), rec)
	assert.NilError(t, err)
	be.Set("c1", fake.Gone)
	task := &model.Task{ID: 8, Runner: "test-runner", Workspace: "test", Version: 2}

	// Act
	err = m.Start(t.Context(), task)

	// Assert - ErrGone surfaced (the caller emits failed and releases the
	// slot), no fresh sandbox was created, the dangling record was removed,
	// and the TaskRunner was kept with no sandbox.
	assert.ErrorIs(t, err, backend.ErrGone)
	assert.Assert(t, cmp.Len(be.Snapshot().Specs, 0))
	assertRecord(t, store, 8, nil)
	assert.Equal(t, m.Runner(8), tr)

	// Act - the task is started again. This Start is also the probe for the
	// first one: a Create means it left the TaskRunner with no sandbox.
	task.Version = 3
	assert.NilError(t, m.Start(t.Context(), task))
	assert.NilError(t, m.Wait(t.Context()))

	// Assert - the same TaskRunner created one fresh sandbox and ran the
	// workspace's agent on it; the driver reported, and the sandbox is parked.
	snap := be.Snapshot()
	assert.Assert(t, cmp.Len(snap.Specs, 1))
	assert.Equal(t, snap.Specs[0].Version, int64(3))
	fresh, _ := snap.Sandbox("c2")
	assert.Equal(t, fresh.State, fake.Parked)
	assert.Equal(t, m.Runner(8), tr)
	n, err := queue.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
	// The runner never destroyed the vanished sandbox; it only dropped it.
	assert.Assert(t, cmp.Len(snap.Destroyed, 0))
	assertTracked(t, m, be, store, 8, &taskstate.Record{TaskID: 8, Version: 3, Type: "fake", ID: "c2"})
}

func TestTaskManagerKill(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// state, when set, is the state of the task's recorded sandbox, c1
		// (version 3).
		state fake.State
		// vanish moves c1 to gone once it is restored.
		vanish bool
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
			state:        fake.Up,
			agent:        sleepForever,
			wantSignal:   true,
			wantDialed:   true,
			wantFinished: true,
		},
		{
			name:       "finished",
			state:      fake.Up,
			agent:      &agent.Config{Type: agent.TypeDummy},
			finish:     true,
			wantDialed: true,
		},
		{
			name:       "never run",
			state:      fake.Up,
			wantDialed: true,
		},
		{
			name:  "parked",
			state: fake.Parked,
		},
		{
			name:   "gone",
			state:  fake.Parked,
			vanish: true,
		},
		{
			name:      "status fails",
			state:     fake.Up,
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
			h, client := serveDriver(t, 1, 3, tt.agent)
			if tt.finish {
				waitFinished(t, client, 3)
			}
			be := newFake(t, h)
			m, _, _ := testManager(t, be, testStore(t))
			if tt.state != 0 {
				rec := taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"}
				be.Add("c1", tt.state)
				_, err := m.Runner(1).Restore(t.Context(), rec)
				assert.NilError(t, err)
			}
			if tt.vanish {
				be.Set("c1", fake.Gone)
			}
			if tt.statusErr != nil {
				be.FailNext(fake.OpStatus, "c1", tt.statusErr)
			}

			// Act
			signalled, err := m.Runner(1).Kill(t.Context())

			// Assert
			if tt.wantErr {
				assert.ErrorIs(t, err, tt.statusErr)
			} else {
				assert.NilError(t, err)
			}
			assert.Equal(t, signalled, tt.wantSignal)
			sbx, _ := be.Snapshot().Sandbox("c1")
			assert.Equal(t, sbx.Dials == 1, tt.wantDialed)
			if tt.wantFinished {
				// The stopped run reports and finishes.
				waitFinished(t, client, 3)
			}
		})
	}
}

func TestTaskManagerRemove(t *testing.T) {
	t.Parallel()
	// A record written by an older runner carries the run version in Data.
	legacy := taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1", Data: json.RawMessage(`{"version":3}`)}
	tests := []struct {
		name string
		// record, when set, is the task's record, of parked sandbox c1.
		record *taskstate.Record
		// vanish moves c1 to gone once it is restored.
		vanish      bool
		destroyErr  error
		wantErr     bool
		wantDestroy int // the Destroy calls on c1 that succeeded
		wantRecord  bool
	}{
		{
			name:        "legacy handle",
			record:      &legacy,
			wantDestroy: 1,
		},
		{
			// A gone sandbox's Destroy returns nil, so the record is dropped.
			name:        "sandbox gone",
			record:      &taskstate.Record{TaskID: 1, Type: "fake", ID: "c1"},
			vanish:      true,
			wantDestroy: 1,
		},
		{
			// A failed destroy keeps the record so the removal is retried.
			name:       "destroy fails",
			record:     &legacy,
			destroyErr: errors.New("boom"),
			wantErr:    true,
			wantRecord: true,
		},
		{
			name: "no record",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			be := fake.New(fake.Options{})
			store := testStore(t)
			m, _, _ := testManager(t, be, store)
			if tt.record != nil {
				assert.NilError(t, store.Write(*tt.record))
				be.Add("c1", fake.Parked)
				_, err := m.Runner(1).Restore(t.Context(), *tt.record)
				assert.NilError(t, err)
			}
			if tt.vanish {
				be.Set("c1", fake.Gone)
			}
			if tt.destroyErr != nil {
				be.FailNext(fake.OpDestroy, "c1", tt.destroyErr)
			}

			// Act
			err := m.Runner(1).Destroy(t.Context())

			// Assert
			if tt.wantErr {
				assert.ErrorIs(t, err, tt.destroyErr)
			} else {
				assert.NilError(t, err)
			}
			sbx, _ := be.Snapshot().Sandbox("c1")
			assert.Equal(t, sbx.Destroys, tt.wantDestroy)
			// The record is removed only with the sandbox.
			if tt.wantRecord {
				assertTracked(t, m, be, store, 1, tt.record)
			} else {
				assertTracked(t, m, be, store, 1, nil)
			}
		})
	}
}

// TestTaskManagerRemove_Running covers a task removed while its run is
// followed: the run is lost, and its Run goroutine returns.
func TestTaskManagerRemove_Running(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going on an up sandbox, recorded, and
	// followed by the Run goroutine Load starts for it.
	h, _ := serveDriver(t, 1, 3, sleepForever)
	be := newFake(t, h)
	be.Add("c1", fake.Up)
	store := testStore(t, taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"})
	m, mock, queue := testManager(t, be, store)
	assert.NilError(t, m.Load(t.Context()))
	waitDialed(t, be, "c1", 1)

	// Act - the sandbox is removed under the run.
	assert.NilError(t, m.Runner(1).Destroy(t.Context()))
	assert.NilError(t, m.Wait(t.Context()))

	// Assert - Destroy destroyed the sandbox Run holds, and the run is lost.
	assert.DeepEqual(t, be.Snapshot().Destroyed, []string{"c1"})
	waitQueued(t, queue, 1)
	assert.DeepEqual(t, submitted(t, mock, queue), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 1, Version: 3, Reason: "sandbox removed before the run finished"},
	}, protocmp.Transform())
	// The task's record is gone and its TaskRunner has no sandbox.
	assertTracked(t, m, be, store, 1, nil)
}

// TestTaskManagerAfterDestroy covers the operations on a task whose sandbox
// was destroyed: its TaskRunner stays registered, and they see no sandbox.
func TestTaskManagerAfterDestroy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		op   func(t *testing.T, m *runner.TaskManager)
	}{
		{name: "kill", op: func(t *testing.T, m *runner.TaskManager) {
			stopped, err := m.Runner(1).Kill(t.Context())
			assert.NilError(t, err)
			assert.Assert(t, !stopped)
		}},
		{name: "running", op: func(t *testing.T, m *runner.TaskManager) {
			running, err := m.Runner(1).Running(t.Context())
			assert.NilError(t, err)
			assert.Assert(t, !running)
		}},
		{name: "remove", op: func(t *testing.T, m *runner.TaskManager) {
			assert.NilError(t, m.Runner(1).Destroy(t.Context()))
		}},
		{name: "version", op: func(t *testing.T, m *runner.TaskManager) {
			assert.Equal(t, m.Runner(1).Version(), int64(0))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - the task's parked sandbox is destroyed.
			rec := taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"}
			be := fake.New(fake.Options{})
			be.Add("c1", fake.Parked)
			store := testStore(t, rec)
			m, _, _ := testManager(t, be, store)
			tr := m.Runner(1)
			_, err := tr.Restore(t.Context(), rec)
			assert.NilError(t, err)
			assert.NilError(t, tr.Destroy(t.Context()))

			// Act
			tt.op(t, m)

			// Assert - the sandbox was destroyed once and nothing else was
			// done to it, and the task keeps its TaskRunner with no sandbox.
			sbx, _ := be.Snapshot().Sandbox("c1")
			assert.DeepEqual(t, sbx, fake.Info{Handle: backend.Handle{Type: "fake", ID: "c1"}, State: fake.Gone, Destroys: 1})
			assert.Equal(t, m.Runner(1), tr)
			assertTracked(t, m, be, store, 1, nil)
		})
	}
}
