package runner_test

import (
	"context"
	"errors"
	"log/slog"
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
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/proto/gritz/v1/gritzv1connect"
	"github.com/gritzapp/gritz/internal/runner"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/backend/docker"
	"github.com/gritzapp/gritz/internal/runner/backend/fake"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/taskevents"
	"github.com/gritzapp/gritz/internal/runner/testrunner"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/gritzapp/gritz/internal/x/dockerx"
	"github.com/gritzapp/gritz/internal/x/testx"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
	"gotest.tools/v3/poll"
)

func TestTaskManagerStart(t *testing.T) {
	abs, err := filepath.Abs("../../prebuilt")
	assert.NilError(t, err)
	t.Setenv("GRITZ_PREBUILT_DIR", abs)

	task := runnerstore.TaskChange{ID: 1, Command: gritzv1.TaskCommand_START, Version: 1, Workspace: "test"}

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
			return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{
				Id:        task.ID,
				Name:      "test-task",
				Runner:    testrunner.RunnerID,
				Workspace: task.Workspace,
				Status:    gritzv1.TaskStatus_PENDING,
				Command:   task.Command,
				Version:   task.Version,
			}}, nil
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
		// The runner's event log has nothing past what the test stores.
		ListRunnerEventsFunc: func(_ context.Context, req *gritzv1.ListRunnerEventsRequest) (*gritzv1.ListRunnerEventsResponse, error) {
			return &gritzv1.ListRunnerEventsResponse{NextPageToken: req.PageToken}, nil
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
	rstore := testrunner.NewRunnerStore(t)
	queue := runner.NewRunnerEventOutbox(runner.RunnerEventOutboxOptions{
		Store:   rstore,
		Client:  client,
		Backoff: backoff.NewConstantBackOff(0),
		Log:     slog.Default(),
	})
	m, err := runner.NewTaskManager(runner.TaskManagerOptions{
		Client:      client,
		Backend:     be,
		ServerURL:   ts.URL,
		Queue:       queue,
		TaskLog:     taskevents.New(taskevents.Options{Store: rstore, Client: client, RunnerID: "test-runner"}),
		RunnerStore: rstore,
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

	// Start a task: its start is in the replica, which the event log reports
	// at boot, and the runner forwards to the driver.
	_, err = rstore.AppendEvents(t.Context(), []*gritzv1.Event{
		testrunner.CommandEvent(1, task.ID, task.Command, task.Version),
	}, "")
	assert.NilError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = m.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// The TaskManager records the launched handle in the store.
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		_, ok, err := rstore.Sandbox(t.Context(), task.ID)
		if err != nil {
			return poll.Error(err)
		}
		if !ok {
			return poll.Continue("no record for task %d", task.ID)
		}
		return poll.Success()
	}, poll.WithTimeout(time.Minute))
	rec, _, err := rstore.Sandbox(t.Context(), task.ID)
	assert.NilError(t, err)
	assert.Equal(t, rec.Handle.Type, docker.HandleType)
	assert.Assert(t, rec.Handle.ID != "")

	// Wait for the container to be stopped once the driver is idle.
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
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewDriver(t, nil)})
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Backend:   be,
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
	m.Run(t)

	// Act
	m.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 1))
	testrunner.WaitSuspended(t, be, "c1")

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
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewDriver(t, nil)})
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Backend:   be,
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
	m.Run(t)

	// Act
	m.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 1))
	testrunner.WaitSuspended(t, be, "c1")

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
	drv := &specDriver{}
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{
		Driver: testrunner.NewStubDriver(t, drv),
		RunEnv: func(*workspace.Workspace) map[string]string {
			return map[string]string{"FOO": "bar", "GRITZ_TASK_ID": "shadowed"}
		},
	})
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})
	m.Run(t)

	// Act
	m.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 1))
	testrunner.WaitSuspended(t, be, "c1")

	// Assert - the environment of the run the driver was asked to start is
	// RunEnv's, overridden by the TaskManager's.
	specs := drv.started()
	assert.Assert(t, cmp.Len(specs, 1))
	assert.DeepEqual(t, specs[0].GetEnv(), map[string]string{
		"FOO":           "bar",
		"GRITZ_TASK_ID": "1",
		"GRITZ_TOKEN":   "t",
		"GRITZ_SERVER":  "",
	})
}

// specDriver is a driver that records the spec of every stream it is opened
// with, and goes idle at once, as after a run of version 1 it reported.
type specDriver struct {
	driverv1connect.UnimplementedDriverServiceHandler

	mu    sync.Mutex
	specs []*driverv1.RunSpec
}

func (d *specDriver) Run(_ context.Context, stream *connect.BidiStream[driverv1.RunRequest, driverv1.RunResponse]) error {
	req, err := stream.Receive()
	if err != nil {
		return err
	}
	if spec := req.GetSpec(); spec != nil {
		d.mu.Lock()
		d.specs = append(d.specs, spec)
		d.mu.Unlock()
	}
	if err := stream.Send(&driverv1.RunResponse{Msg: &driverv1.RunResponse_Hello{Hello: &driverv1.Hello{}}}); err != nil {
		return err
	}
	return stream.Send(&driverv1.RunResponse{Msg: &driverv1.RunResponse_Idle{
		Idle: &driverv1.Idle{Version: 1, Reported: true},
	}})
}

// started returns the spec of every stream d was opened with.
func (d *specDriver) started() []*driverv1.RunSpec {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.specs)
}

func TestTaskManagerRunLoop_StopWithoutSandbox(t *testing.T) {
	t.Parallel()
	// Arrange - task being cancelled with no tracked sandbox.
	be := testrunner.NewBackend(t, nil)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})
	m.Run(t)

	// Act
	m.Add(testrunner.CommandEvent(1, 7, gritzv1.TaskCommand_STOP, 3))

	// Assert - no Run is open, so there is no driver to stop and the
	// TaskManager emits "stopped" itself.
	m.WaitQueued(t, 1)
	assert.DeepEqual(t, m.Submitted(t), []*gritzv1.RunnerEvent{
		{Event: "stopped", TaskId: 7, Version: 3},
	}, protocmp.Transform())
}

// TestTaskManagerRunLoop_StopRunOpen covers a stop while the driver's stream
// is open: the stop reaches the driver on it, which stops the run and
// reports it, so the runner reports nothing.
func TestTaskManagerRunLoop_StopRunOpen(t *testing.T) {
	t.Parallel()
	// Arrange - run 3 of task 7 keeps going on c7, and Load reattaches to it.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{
		TaskID:  7,
		Version: 3,
		Agent:   testrunner.SleepForever,
	})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	be.Add("c7", fake.Up)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Backend:     be,
		RunnerStore: testrunner.NewRunnerStore(t, runnerstore.SandboxRecord{TaskID: 7, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c7"}}),
	})
	m.Add(testrunner.CommandEvent(1, 7, gritzv1.TaskCommand_START, 3))
	assert.NilError(t, m.Load(t.Context()))
	testrunner.WaitRuns(t, be, "c7", 1)
	m.Run(t)

	// Act
	m.Add(testrunner.CommandEvent(2, 7, gritzv1.TaskCommand_STOP, 3))

	// Assert - the driver stopped the run and reported it; the stream went
	// idle, and the sandbox is suspended.
	testrunner.WaitSuspended(t, be, "c7")
	assert.DeepEqual(t, client.Reported(), []string{"started", "stopped"})
	assert.Equal(t, m.Queued(t), 0)
}

// TestTaskManagerRunLoop_Start covers a start: RunLoop acts on the task's
// latest report, so a page whose start was withdrawn and issued again
// starts the task once, at the latest version.
func TestTaskManagerRunLoop_Start(t *testing.T) {
	t.Parallel()
	// Arrange
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{
		Driver: testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 2}),
	})
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})
	m.Add(
		testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 1),
		testrunner.CommandEvent(2, 1, gritzv1.TaskCommand_NONE, 1),
		testrunner.CommandEvent(3, 1, gritzv1.TaskCommand_START, 2),
	)

	// Act
	m.Run(t)

	// Assert - one sandbox, created for version 2 and booted once, which ran
	// the workspace's agent; the driver reported, and the sandbox is
	// suspended.
	sbx := testrunner.WaitSuspended(t, be, "c1")
	assert.Equal(t, sbx.Boots, 1)
	snap := be.Snapshot()
	assert.Assert(t, cmp.Len(snap.Specs, 1))
	assert.Equal(t, snap.Specs[0].Version, int64(2))
	assert.Equal(t, m.Queued(t), 0)
	m.AssertRecord(t, 1, &runnerstore.SandboxRecord{TaskID: 1, Version: 2, Handle: backend.Handle{Type: "fake", ID: "c1"}})
}

// TestTaskManagerRunLoop_Restart covers a restart of a running task: the
// restart reaches the driver on the stream Load reattached, which stops run 3
// and starts run 4 on the same stream, with the sandbox kept up.
func TestTaskManagerRunLoop_Restart(t *testing.T) {
	t.Parallel()
	// Arrange - run 3 keeps going on c1, and Load reattaches to it, which
	// holds the manager's one slot.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{
		Version: 3,
		Agent:   testrunner.SleepForever,
	})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	be.Add("c1", fake.Up)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Backend:     be,
		RunnerStore: testrunner.NewRunnerStore(t, runnerstore.SandboxRecord{TaskID: 1, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c1"}}),
	})
	m.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 3))
	assert.NilError(t, m.Load(t.Context()))
	testrunner.WaitRuns(t, be, "c1", 1)
	m.Run(t)

	// Act
	m.Add(testrunner.CommandEvent(2, 1, gritzv1.TaskCommand_RESTART, 4))
	client.WaitStarted(t, 2)

	// Assert - run 4 runs on c1, which was neither booted again nor
	// replaced, over the stream Load opened.
	snap := be.Snapshot()
	assert.Assert(t, cmp.Len(snap.Specs, 0))
	sbx, _ := snap.Sandbox("c1")
	assert.Equal(t, sbx.Boots, 0)
	assert.Equal(t, sbx.State, fake.Up)
	assert.Equal(t, sbx.Runs, 1)

	// Act - run 4 is stopped.
	m.Add(testrunner.CommandEvent(3, 1, gritzv1.TaskCommand_STOP, 4))

	// Assert - the driver reported both runs; the sandbox is suspended, and
	// the record moved to run 4.
	testrunner.WaitSuspended(t, be, "c1")
	assert.Equal(t, m.Queued(t), 0)
	assert.DeepEqual(t, client.Reported(), []string{"started", "stopped", "started", "stopped"})
	m.AssertRecord(t, 1, &runnerstore.SandboxRecord{TaskID: 1, Version: 4, Handle: backend.Handle{Type: "fake", ID: "c1"}})
}

// TestTaskManagerRunLoop_RestartCancelled covers a restart cancelled before
// the driver started it: the driver stops run 3 and never starts run 4, so
// the runner reports run 4 stopped, as no run will.
func TestTaskManagerRunLoop_RestartCancelled(t *testing.T) {
	t.Parallel()
	// Arrange - run 3 keeps going on c1, and Load reattaches to it.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{
		Version: 3,
		Agent:   testrunner.SleepForever,
	})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	be.Add("c1", fake.Up)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Backend:     be,
		RunnerStore: testrunner.NewRunnerStore(t, runnerstore.SandboxRecord{TaskID: 1, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c1"}}),
	})
	m.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 3))
	assert.NilError(t, m.Load(t.Context()))
	testrunner.WaitRuns(t, be, "c1", 1)
	m.Run(t)

	// Act - the task is restarted and cancelled in one page: the stop
	// carries the restart's version.
	m.Add(
		testrunner.CommandEvent(2, 1, gritzv1.TaskCommand_RESTART, 4),
		testrunner.CommandEvent(3, 1, gritzv1.TaskCommand_STOP, 4),
	)

	// Assert - run 4 never ran, and the runner reported it stopped.
	testrunner.WaitSuspended(t, be, "c1")
	m.WaitQueued(t, 1)
	assert.DeepEqual(t, client.Reported(), []string{"started", "stopped"})
	assert.DeepEqual(t, m.Submitted(t), []*gritzv1.RunnerEvent{
		{Event: "stopped", TaskId: 1, Version: 4},
	}, protocmp.Transform())
	m.AssertRecord(t, 1, &runnerstore.SandboxRecord{TaskID: 1, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c1"}})
}

// TestTaskManagerRunLoop_SlowDriver covers a driver that is slow to come up:
// the sandbox is booted, and the run is started once the driver answers.
func TestTaskManagerRunLoop_SlowDriver(t *testing.T) {
	t.Parallel()
	// Arrange - a pending task with no record, whose sandbox's driver is not
	// listening yet.
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{
		Driver:         testrunner.NewDriver(t, nil),
		DriverNotReady: true,
	})
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})
	m.Run(t)

	// Act - start while the driver is not up.
	m.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 1))

	// Assert - the sandbox is booted with the run pending.
	sbx := testrunner.WaitSandbox(t, be, "c1", func(sbx fake.Info) bool { return sbx.State == fake.Up })
	assert.Equal(t, sbx.Runs, 0)

	// Act - the driver comes up.
	be.SetDriverReady("c1", true)

	// Assert - the run started, and the driver reported it, so no event is
	// owed and the sandbox is suspended.
	sbx = testrunner.WaitSuspended(t, be, "c1")
	assert.Equal(t, sbx.Boots, 1)
	assert.Equal(t, m.Queued(t), 0)
}

// slotHeld returns a manager whose one concurrency slot is held by run 3 of
// task 1, reattached by Load and going on until the test stops it with
// stopSlot. Task 1's events have ids from 100. Every other sandbox's driver
// runs its agent to completion.
func slotHeld(t *testing.T) *testrunner.Manager {
	t.Helper()
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{
		Version: 3,
		Agent:   testrunner.SleepForever,
		EventID: 100,
	})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{
		Driver:  testrunner.NewDriver(t, nil),
		Drivers: map[string]driverv1connect.DriverServiceClient{"c1": client},
	})
	be.Add("c1", fake.Up)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Backend:     be,
		RunnerStore: testrunner.NewRunnerStore(t, runnerstore.SandboxRecord{TaskID: 1, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c1"}}),
	})
	m.Add(testrunner.CommandEvent(100, 1, gritzv1.TaskCommand_START, 3))
	assert.NilError(t, m.Load(t.Context()))
	testrunner.WaitRuns(t, be, "c1", 1)
	m.Run(t)
	return m
}

// stopSlot stops the run holding slotHeld's slot, and waits until its busy
// period has ended, giving back the slot.
func stopSlot(t *testing.T, m *testrunner.Manager) {
	t.Helper()
	m.Add(testrunner.CommandEvent(101, 1, gritzv1.TaskCommand_STOP, 3))
	testrunner.WaitSuspended(t, m.Backend, "c1")
}

// TestTaskManagerRunLoop_StartWaitsForSlot covers a start at the concurrency
// limit: it runs once a busy period gives back its slot.
func TestTaskManagerRunLoop_StartWaitsForSlot(t *testing.T) {
	t.Parallel()
	// Arrange
	m := slotHeld(t)

	// Act - task 2's start finds no slot.
	m.Add(testrunner.CommandEvent(1, 2, gritzv1.TaskCommand_START, 1))

	// Assert - nothing is started while the slot is held.
	assert.Assert(t, cmp.Len(m.Backend.Snapshot().Specs, 0))

	// Act - run 3 finishes, giving back the slot.
	stopSlot(t, m)

	// Assert - task 2 got a sandbox.
	testrunner.WaitSuspended(t, m.Backend, "c2")
	specs := m.Backend.Snapshot().Specs
	assert.Assert(t, cmp.Len(specs, 1))
	assert.Equal(t, specs[0].TaskID, int64(2))
}

// TestTaskManagerRunLoop_StopSupersedesWaitingStart covers a cancel landing
// while a start waits for a slot: the wait is preempted, and the start
// never runs.
func TestTaskManagerRunLoop_StopSupersedesWaitingStart(t *testing.T) {
	t.Parallel()
	// Arrange - task 2's start waits for a slot.
	m := slotHeld(t)
	m.Add(testrunner.CommandEvent(1, 2, gritzv1.TaskCommand_START, 1))

	// Act - task 2 is cancelled, then run 3 gives back the slot.
	m.Add(testrunner.CommandEvent(2, 2, gritzv1.TaskCommand_STOP, 1))
	m.WaitQueued(t, 1)
	stopSlot(t, m)

	// Assert - task 2 never got a sandbox, and with no Run open, the
	// TaskManager emitted its "stopped".
	assert.Assert(t, cmp.Len(m.Backend.Snapshot().Specs, 0))
	assert.DeepEqual(t, m.Submitted(t), []*gritzv1.RunnerEvent{
		{Event: "stopped", TaskId: 2, Version: 1},
	}, protocmp.Transform())
}

// TestTaskManagerRunLoop_Archive covers an archived task: its RunLoop
// destroys its suspended sandbox and ends, and an unarchive gives the task a
// fresh TaskRunner, which runs its next start.
func TestTaskManagerRunLoop_Archive(t *testing.T) {
	t.Parallel()
	// Arrange - task 1 ran on c1, which is suspended.
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewDriver(t, nil)})
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})
	m.Run(t)
	m.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 1))
	testrunner.WaitSuspended(t, be, "c1")

	// Act
	m.Add(testrunner.ArchiveEvent(2, 1))

	// Assert - c1 is destroyed and its record removed.
	testrunner.WaitSandbox(t, be, "c1", func(sbx fake.Info) bool { return sbx.Destroys == 1 })
	m.AssertRecord(t, 1, nil)

	// Act - the task is unarchived and started again.
	m.Add(
		testrunner.LifecycleEvent(3, 1, gritzv1.LifecycleKind_LIFECYCLE_KIND_UNARCHIVED),
		testrunner.CommandEvent(4, 1, gritzv1.TaskCommand_START, 2),
	)

	// Assert - the task got a fresh sandbox for the start.
	testrunner.WaitSuspended(t, be, "c2")
	snap := be.Snapshot()
	assert.Assert(t, cmp.Len(snap.Specs, 2))
	assert.Equal(t, snap.Specs[1].Version, int64(2))
	m.AssertRecord(t, 1, &runnerstore.SandboxRecord{TaskID: 1, Version: 2, Handle: backend.Handle{Type: "fake", ID: "c2"}})
}

// TestTaskManagerRunLoop_CreateFails covers a sandbox that cannot be
// created: the run is reported failed, and the task gets no record.
func TestTaskManagerRunLoop_CreateFails(t *testing.T) {
	t.Parallel()
	// Arrange
	be := testrunner.NewBackend(t, nil)
	be.FailNext(fake.OpCreate, "", errors.New("boom"))
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})
	m.Run(t)

	// Act
	m.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 1))

	// Assert
	m.WaitQueued(t, 1)
	assert.DeepEqual(t, m.Submitted(t), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 1, Version: 1, Reason: "boom"},
	}, protocmp.Transform())
	assert.Assert(t, cmp.Len(be.Snapshot().Sandboxes, 0))
	m.AssertRecord(t, 1, nil)
}

// TestTaskManagerRunLoop_RestartSuspended covers a task started again on its
// recorded, suspended sandbox: the sandbox is reused, and the record moves
// to the new version.
func TestTaskManagerRunLoop_RestartSuspended(t *testing.T) {
	t.Parallel()
	// Arrange - the recorded sandbox is suspended, and its driver has never
	// run version 4.
	rec := runnerstore.SandboxRecord{TaskID: 1, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c1"}}
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{
		Driver: testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 4}),
	})
	be.Add("c1", fake.Suspended)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Backend:     be,
		RunnerStore: testrunner.NewRunnerStore(t, rec),
	})
	assert.NilError(t, m.Load(t.Context()))
	m.Run(t)

	// Act
	m.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 4))

	// Assert - no second sandbox was created: the recorded one was booted and
	// ran the new version, which the driver reported.
	sbx := testrunner.WaitSandbox(t, be, "c1", func(sbx fake.Info) bool {
		return sbx.Boots == 1 && sbx.State == fake.Suspended
	})
	assert.Equal(t, sbx.Suspends, 1)
	assert.Assert(t, cmp.Len(be.Snapshot().Specs, 0))
	assert.Equal(t, m.Queued(t), 0)
	m.AssertRecord(t, 1, &runnerstore.SandboxRecord{TaskID: 1, Version: 4, Handle: backend.Handle{Type: "fake", ID: "c1"}})
}

// TestTaskManagerRunLoop_NotReady covers a driver that never comes up: the
// run is reported failed, the sandbox suspended, and the record kept.
func TestTaskManagerRunLoop_NotReady(t *testing.T) {
	t.Parallel()
	// Arrange
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{
		Driver:         testrunner.NewDriver(t, nil),
		DriverNotReady: true,
	})
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be, TaskRunner: func(opts *runner.TaskRunnerOptions) {
		opts.ReadyTimeout = 100 * time.Millisecond
	}})
	m.Run(t)

	// Act
	m.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 1))

	// Assert
	testrunner.WaitSuspended(t, be, "c1")
	m.WaitQueued(t, 1)
	events := m.Submitted(t)
	assert.Assert(t, cmp.Len(events, 1))
	assert.Equal(t, events[0].Event, "failed")
	assert.Equal(t, events[0].Version, int64(1))
	assert.Assert(t, cmp.Contains(events[0].Reason, "driver server did not become ready"))
	m.AssertRecord(t, 1, &runnerstore.SandboxRecord{TaskID: 1, Version: 1, Handle: backend.Handle{Type: "fake", ID: "c1"}})
}

// TestTaskManagerRunLoop_BootFails covers a sandbox that cannot be booted
// after it is created: the run is reported failed with the record kept, and
// archiving the task destroys the sandbox.
func TestTaskManagerRunLoop_BootFails(t *testing.T) {
	t.Parallel()
	// Arrange - the sandbox the start creates refuses to boot.
	be := testrunner.NewBackend(t, nil)
	be.FailNext(fake.OpBoot, "", errors.New("boom"))
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})
	m.Run(t)

	// Act
	m.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 1))

	// Assert - the run failed with the record and sandbox kept and nothing
	// run. The record surviving the failed boot also shows it was written
	// before the sandbox was booted.
	m.WaitQueued(t, 1)
	assert.DeepEqual(t, m.Submitted(t), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 1, Version: 1, Reason: "boom"},
	}, protocmp.Transform())
	m.AssertRecord(t, 1, &runnerstore.SandboxRecord{TaskID: 1, Version: 1, Handle: backend.Handle{Type: "fake", ID: "c1"}})
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Created)
	assert.Equal(t, sbx.Dials, 0)

	// Act - the task is archived.
	m.Add(testrunner.ArchiveEvent(2, 1))

	// Assert - the TaskRunner still held c1: it is destroyed, and the record
	// is gone.
	testrunner.WaitSandbox(t, be, "c1", func(sbx fake.Info) bool { return sbx.Destroys == 1 })
	m.AssertRecord(t, 1, nil)
}

// TestTaskManagerRunLoop_Gone covers a task whose recorded sandbox has
// vanished: the boot fails with the run reported failed and the record
// dropped, and the next start creates a fresh sandbox.
func TestTaskManagerRunLoop_Gone(t *testing.T) {
	t.Parallel()
	// Arrange - task 8's recorded sandbox, restored suspended, is then removed
	// behind the runner's back.
	rec := runnerstore.SandboxRecord{TaskID: 8, Version: 1, Handle: backend.Handle{Type: "fake", ID: "c1"}}
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{
		Driver: testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3}),
	})
	be.Add("c1", fake.Suspended)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Backend:     be,
		RunnerStore: testrunner.NewRunnerStore(t, rec),
	})
	assert.NilError(t, m.Load(t.Context()))
	be.Set("c1", fake.Gone)
	m.Run(t)

	// Act
	m.Add(testrunner.CommandEvent(1, 8, gritzv1.TaskCommand_START, 2))

	// Assert - the run failed, no fresh sandbox was created, and the
	// dangling record was removed.
	m.WaitQueued(t, 1)
	assert.DeepEqual(t, m.Submitted(t), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 8, Version: 2, Reason: backend.ErrGone.Error()},
	}, protocmp.Transform())
	assert.Assert(t, cmp.Len(be.Snapshot().Specs, 0))
	m.AssertRecord(t, 8, nil)

	// Act - the task is started again.
	m.Add(testrunner.CommandEvent(2, 8, gritzv1.TaskCommand_START, 3))

	// Assert - the task got one fresh sandbox, which ran the workspace's
	// agent; the driver reported, and the sandbox is suspended. The runner
	// never destroyed the vanished sandbox; it only dropped it.
	testrunner.WaitSuspended(t, be, "c2")
	snap := be.Snapshot()
	assert.Assert(t, cmp.Len(snap.Specs, 1))
	assert.Equal(t, snap.Specs[0].Version, int64(3))
	assert.Assert(t, cmp.Len(snap.Destroyed, 0))
	assert.Equal(t, m.Queued(t), 0)
	m.AssertRecord(t, 8, &runnerstore.SandboxRecord{TaskID: 8, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c2"}})
}
