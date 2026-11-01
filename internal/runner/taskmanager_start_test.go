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
)

func TestTaskManagerStart(t *testing.T) {
	abs, err := filepath.Abs("../../prebuilt")
	assert.NilError(t, err)
	t.Setenv("GRITZ_PREBUILT_DIR", abs)

	task := testrunner.StartChange(1, 1)

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

	// Start a task
	err = m.Runner(task.ID).Start(t.Context(), task)
	assert.NilError(t, err)

	// The TaskManager records the launched handle in the store.
	rec, ok, err := rstore.Sandbox(t.Context(), task.ID)
	assert.NilError(t, err)
	assert.Equal(t, ok, true)
	assert.Equal(t, rec.Handle.Type, docker.HandleType)
	assert.Assert(t, rec.Handle.ID != "")

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
	task := testrunner.StartChange(1, 1)

	// Act
	assert.NilError(t, m.Runner(task.ID).Start(t.Context(), task))
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
	task := testrunner.StartChange(1, 1)

	// Act
	assert.NilError(t, m.Runner(task.ID).Start(t.Context(), task))
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
	drv := &specDriver{}
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{
		Driver: testrunner.NewStubDriver(t, drv),
		RunEnv: func(*workspace.Workspace) map[string]string {
			return map[string]string{"FOO": "bar", "GRITZ_TASK_ID": "shadowed"}
		},
	})
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})
	task := testrunner.StartChange(1, 1)

	// Act
	assert.NilError(t, m.Runner(task.ID).Start(t.Context(), task))
	assert.NilError(t, m.Wait(t.Context()))

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

// specDriver is a driver that records the spec of every run it is asked to
// start, and ends it at once, reported.
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
		Idle: &driverv1.Idle{Version: req.GetVersion(), Reported: true},
	}})
}

// started returns the spec of every run d was asked to start.
func (d *specDriver) started() []*driverv1.RunSpec {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.specs)
}

func TestTaskManagerFollow_StopWithoutSandbox(t *testing.T) {
	t.Parallel()
	// Arrange - task being cancelled with no tracked sandbox.
	be := testrunner.NewBackend(t, nil)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})
	m.Run(t)

	// Act
	m.Add(testrunner.CommandEvent(1, 7, gritzv1.TaskCommand_STOP, 3))

	// Assert - no record, so there is no driver to stop and the TaskManager emits
	// "stopped" itself.
	m.WaitHandled(t, 7, 1)
	m.WaitQueued(t, 1)
	assert.DeepEqual(t, m.Submitted(t), []*gritzv1.RunnerEvent{
		{Event: "stopped", TaskId: 7, Version: 3},
	}, protocmp.Transform())
}

func TestTaskManagerFollow_StopSignalled(t *testing.T) {
	t.Parallel()
	// Arrange - task being cancelled with a tracked sandbox, up and running
	// the task's run.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{
		TaskID:  7,
		Version: 3,
		Agent:   testrunner.SleepForever,
	})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	be.Add("c7", fake.Up)
	rec := runnerstore.SandboxRecord{TaskID: 7, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c7"}}
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Backend:     be,
		RunnerStore: testrunner.NewRunnerStore(t, rec),
	})
	_, err := m.Runner(7).Restore(t.Context(), rec)
	assert.NilError(t, err)
	m.Run(t)

	// Act
	m.Add(testrunner.CommandEvent(1, 7, gritzv1.TaskCommand_STOP, 3))

	// Assert - the driver stopped the run and owns the terminal report.
	m.WaitHandled(t, 7, 1)
	testrunner.WaitFinished(t, client, 3)
	assert.Equal(t, m.Queued(t), 0)
}

// TestTaskManagerFollow_Start covers a start: the follower acts on the
// task's latest report, so a page whose start was withdrawn and issued again
// starts the task once, at the latest version.
func TestTaskManagerFollow_Start(t *testing.T) {
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

	// Assert - one sandbox, created for version 2, which ran the
	// workspace's agent; the driver reported, and the sandbox is suspended.
	m.WaitHandled(t, 1, 3)
	assert.NilError(t, m.Wait(t.Context()))
	snap := be.Snapshot()
	assert.Assert(t, cmp.Len(snap.Specs, 1))
	assert.Equal(t, snap.Specs[0].Version, int64(2))
	sbx, _ := snap.Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Suspended)
	assert.Equal(t, m.Queued(t), 0)
	m.AssertRecord(t, 1, &runnerstore.SandboxRecord{TaskID: 1, Version: 2, Handle: backend.Handle{Type: "fake", ID: "c1"}})
}

// TestTaskManagerFollow_Restart covers a restart of a running task: the
// follower stops the run, waits for its Run to give back the only slot, and
// starts the new version on the same sandbox.
func TestTaskManagerFollow_Restart(t *testing.T) {
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
	assert.NilError(t, m.Load(t.Context()))
	testrunner.WaitRuns(t, be, "c1", 1)
	m.Run(t)

	// Act
	m.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_RESTART, 4))

	// Assert - run 3 was stopped, as the driver started run 4, and c1
	// booted again for it, with no new sandbox. The driver runs the agent it
	// was given, which runs until it is stopped; it reported both runs.
	m.WaitHandled(t, 1, 1)
	testrunner.StopRun(t, client, 4)
	assert.NilError(t, m.Wait(t.Context()))
	snap := be.Snapshot()
	assert.Assert(t, cmp.Len(snap.Specs, 0))
	sbx, _ := snap.Sandbox("c1")
	assert.Equal(t, sbx.Boots, 1)
	assert.Equal(t, sbx.State, fake.Suspended)
	assert.Equal(t, m.Queued(t), 0)
	m.AssertRecord(t, 1, &runnerstore.SandboxRecord{TaskID: 1, Version: 4, Handle: backend.Handle{Type: "fake", ID: "c1"}})
}

// TestTaskManagerFollow_SlowDriver covers a driver that is slow to come up:
// the follower handles the start once the sandbox is booted, without waiting
// for it, and the run is started once the driver answers.
func TestTaskManagerFollow_SlowDriver(t *testing.T) {
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

	// Assert - the start is handled with the sandbox booted and the run
	// pending.
	m.WaitHandled(t, 1, 1)
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Up)
	assert.Equal(t, sbx.Runs, 0)

	// Act - the driver comes up.
	be.SetDriverReady("c1", true)
	assert.NilError(t, m.Wait(t.Context()))

	// Assert - the run started, and the driver reported it, so no event is
	// owed and the sandbox is suspended.
	sbx, _ = be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.Boots, 1)
	assert.Equal(t, sbx.State, fake.Suspended)
	assert.Equal(t, m.Queued(t), 0)
}

// TestTaskManagerFollow_StartStillRunning covers a start for a task whose
// previous run is still followed by Run while Running reports its sandbox
// suspended (a Lambda VM suspending after its run): nothing is started, the
// concurrency slot is given back, and the start waits.
func TestTaskManagerFollow_StartStillRunning(t *testing.T) {
	t.Parallel()
	// Arrange - task 1's TaskRunner is claimed, and its sandbox suspended. The
	// manager has one slot.
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewDriver(t, nil)})
	be.Add("c1", fake.Up)
	rec := runnerstore.SandboxRecord{TaskID: 1, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c1"}}
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Backend:     be,
		RunnerStore: testrunner.NewRunnerStore(t, rec),
	})
	_, err := m.Runner(1).Restore(t.Context(), rec)
	assert.NilError(t, err)
	be.Set("c1", fake.Suspended)
	m.Run(t)

	// Act - a start for task 1, then one for task 2.
	m.Add(
		testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 4),
		testrunner.CommandEvent(2, 2, gritzv1.TaskCommand_START, 1),
	)

	// Assert - the slot was free again, so task 2 got a sandbox.
	m.WaitHandled(t, 2, 2)
	assert.NilError(t, m.Wait(t.Context()))
	snap := be.Snapshot()
	assert.Assert(t, cmp.Len(snap.Specs, 1))
	assert.Equal(t, snap.Specs[0].TaskID, int64(2))
	// Assert - task 1's start still waits: c1 was not booted, and no event
	// is owed.
	assert.Equal(t, m.Handled(t, 1), int64(0))
	sbx, _ := snap.Sandbox("c1")
	assert.Equal(t, sbx.Boots, 0)
	assert.Equal(t, m.Queued(t), 0)
}

// slotHeld returns a manager whose one concurrency slot is held by run 3 of
// task 1, reattached by Load and going on until the test stops it over the
// returned client. Every other sandbox's driver runs its agent to completion.
// built is closed once the manager has built task 2's TaskRunner, which the
// follower does as it handles task 2's first command.
func slotHeld(t *testing.T) (m *testrunner.Manager, client driverv1connect.DriverServiceClient, built <-chan struct{}) {
	t.Helper()
	client = testrunner.NewDriver(t, &testrunner.DriverOptions{
		Version: 3,
		Agent:   testrunner.SleepForever,
	})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{
		Driver:  testrunner.NewDriver(t, nil),
		Drivers: map[string]driverv1connect.DriverServiceClient{"c1": client},
	})
	be.Add("c1", fake.Up)
	ch := make(chan struct{})
	m = testrunner.NewManager(t, &testrunner.ManagerOptions{
		Backend:     be,
		RunnerStore: testrunner.NewRunnerStore(t, runnerstore.SandboxRecord{TaskID: 1, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c1"}}),
		TaskRunner: func(opts *runner.TaskRunnerOptions) {
			if opts.TaskID == 2 {
				close(ch)
			}
		},
	})
	assert.NilError(t, m.Load(t.Context()))
	testrunner.WaitRuns(t, be, "c1", 1)
	m.Run(t)
	return m, client, ch
}

// TestTaskManagerFollow_StartWaitsForSlot covers a start at the concurrency
// limit: it runs once a run gives back its slot.
func TestTaskManagerFollow_StartWaitsForSlot(t *testing.T) {
	t.Parallel()
	// Arrange
	m, client, built := slotHeld(t)

	// Act - task 2's start finds no slot.
	m.Add(testrunner.CommandEvent(1, 2, gritzv1.TaskCommand_START, 1))
	<-built

	// Assert - nothing is started while the slot is held.
	assert.Equal(t, m.Handled(t, 2), int64(0))
	assert.Assert(t, cmp.Len(m.Backend.Snapshot().Specs, 0))

	// Act - run 3 finishes, giving back the slot.
	testrunner.StopRun(t, client, 3)

	// Assert - task 2 got a sandbox.
	m.WaitHandled(t, 2, 1)
	assert.NilError(t, m.Wait(t.Context()))
	specs := m.Backend.Snapshot().Specs
	assert.Assert(t, cmp.Len(specs, 1))
	assert.Equal(t, specs[0].TaskID, int64(2))
}

// TestTaskManagerFollow_StopSupersedesWaitingStart covers a cancel landing
// while a start waits for a slot: the follower acts on the stop instead, and
// the start never runs.
func TestTaskManagerFollow_StopSupersedesWaitingStart(t *testing.T) {
	t.Parallel()
	// Arrange - task 2's start waits for a slot.
	m, client, built := slotHeld(t)
	m.Add(testrunner.CommandEvent(1, 2, gritzv1.TaskCommand_START, 1))
	<-built

	// Act - task 2 is cancelled, then run 3 gives back the slot.
	m.Add(testrunner.CommandEvent(2, 2, gritzv1.TaskCommand_STOP, 1))
	m.WaitHandled(t, 2, 2)
	testrunner.StopRun(t, client, 3)
	assert.NilError(t, m.Wait(t.Context()))

	// Assert - task 2 never got a sandbox, and with none to stop, the
	// TaskManager emitted its "stopped".
	assert.Assert(t, cmp.Len(m.Backend.Snapshot().Specs, 0))
	m.WaitQueued(t, 1)
	assert.DeepEqual(t, m.Submitted(t), []*gritzv1.RunnerEvent{
		{Event: "stopped", TaskId: 2, Version: 1},
	}, protocmp.Transform())
}

// TestTaskManagerFollow_Archive covers an archived task: its follower stops,
// and its TaskRunner is retired, destroying its suspended sandbox.
func TestTaskManagerFollow_Archive(t *testing.T) {
	t.Parallel()
	// Arrange - task 1 ran on c1, which is suspended.
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewDriver(t, nil)})
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})
	m.Run(t)
	m.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 1))
	m.WaitHandled(t, 1, 1)
	assert.NilError(t, m.Wait(t.Context()))

	// Act
	m.Add(testrunner.ArchiveEvent(2, 1))

	// Assert - c1 is destroyed and its record removed.
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, err := be.Wait(ctx, func(s fake.Snapshot) bool { return len(s.Destroyed) > 0 })
	assert.NilError(t, err, "sandbox was not retired")
	assert.DeepEqual(t, be.Snapshot().Destroyed, []string{"c1"})
	m.AssertRecord(t, 1, nil)
}

// TestTaskManagerFollow_ResumesFromHandled covers a runner restart: the next
// runner's follower starts from the stored handled position, so the boot
// report of the command the first runner already carried out does nothing.
func TestTaskManagerFollow_ResumesFromHandled(t *testing.T) {
	t.Parallel()
	// Arrange - a first runner starts task 1, whose run finishes on c1, and
	// shuts down.
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewDriver(t, nil)})
	server := testrunner.NewServer(testrunner.ServerOptions{})
	opts := &testrunner.ManagerOptions{
		Backend:     be,
		Server:      server,
		RunnerStore: testrunner.NewRunnerStore(t),
	}
	first := testrunner.NewManager(t, opts)
	ctx, cancel := context.WithCancel(t.Context())
	first.RunContext(t, ctx)
	first.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 1))
	first.WaitHandled(t, 1, 1)
	assert.NilError(t, first.Wait(t.Context()))
	cancel()
	before, _ := be.Snapshot().Sandbox("c1")

	// Act - the next runner boots on the same state, which reports the
	// start again, and the task's command is then withdrawn.
	m := testrunner.NewManager(t, opts)
	assert.NilError(t, m.Load(t.Context()))
	m.Run(t)
	m.Add(testrunner.CommandEvent(2, 1, gritzv1.TaskCommand_NONE, 1))

	// Assert - the withdrawal is handled, and the start wasn't acted on
	// again: c1 wasn't booted, and no sandbox was created.
	m.WaitHandled(t, 1, 2)
	after, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, after.Boots, before.Boots)
	assert.Assert(t, cmp.Len(be.Snapshot().Specs, 1))
	assert.Equal(t, m.Queued(t), 0)
}

func TestTaskManagerStart_Create(t *testing.T) {
	t.Parallel()
	// Arrange - a task with no record, whose created sandbox's driver runs the
	// workspace's agent to completion.
	task := testrunner.StartChange(1, 1)
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewDriver(t, nil)})
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})

	// Act
	err := m.Runner(task.ID).Start(t.Context(), task)

	// Assert - the sandbox was created for the task's run and booted.
	assert.NilError(t, err)
	snap := be.Snapshot()
	assert.Assert(t, cmp.Len(snap.Specs, 1))
	assert.Equal(t, snap.Specs[0].TaskID, int64(1))
	assert.Equal(t, snap.Specs[0].Version, int64(1))
	sbx, _ := snap.Sandbox("c1")
	assert.Equal(t, sbx.Boots, 1)

	// Assert - the driver ran the run and reported it, so no event is owed,
	// and the sandbox is suspended.
	assert.NilError(t, m.Wait(t.Context()))
	sbx, _ = be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Suspended)
	assert.Equal(t, m.Queued(t), 0)
	m.AssertTracked(t, 1, &runnerstore.SandboxRecord{TaskID: 1, Version: 1, Handle: backend.Handle{Type: "fake", ID: "c1"}})
}

// TestTaskManagerStart_CreateFails covers a sandbox that cannot be created: the task
// gets no record, and its TaskRunner no sandbox.
func TestTaskManagerStart_CreateFails(t *testing.T) {
	t.Parallel()
	// Arrange
	task := testrunner.StartChange(1, 1)
	boom := errors.New("boom")
	be := testrunner.NewBackend(t, nil)
	be.FailNext(fake.OpCreate, "", boom)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})

	// Act
	err := m.Runner(task.ID).Start(t.Context(), task)

	// Assert
	assert.ErrorIs(t, err, boom)
	assert.Assert(t, cmp.Len(be.Snapshot().Sandboxes, 0))
	m.AssertTracked(t, 1, nil)
}

// TestTaskManagerStart_Restart covers a task restarted on its recorded
// sandbox: the sandbox is reused, and the record moves to the new version.
func TestTaskManagerStart_Restart(t *testing.T) {
	t.Parallel()
	// Arrange - the recorded sandbox is suspended, and its driver has never run
	// version 4.
	rec := runnerstore.SandboxRecord{TaskID: 1, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c1"}}
	task := testrunner.StartChange(1, 4)
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{
		Driver: testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 4}),
	})
	be.Add("c1", fake.Suspended)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Backend:     be,
		RunnerStore: testrunner.NewRunnerStore(t, rec),
	})
	_, err := m.Runner(1).Restore(t.Context(), rec)
	assert.NilError(t, err)

	// Act
	err = m.Runner(task.ID).Start(t.Context(), task)
	assert.NilError(t, m.Wait(t.Context()))

	// Assert - no second sandbox was created: the recorded one was booted and
	// ran the new version, which the driver reported.
	assert.NilError(t, err)
	snap := be.Snapshot()
	assert.Assert(t, cmp.Len(snap.Specs, 0))
	sbx, _ := snap.Sandbox("c1")
	assert.Equal(t, sbx.Boots, 1)
	assert.Equal(t, sbx.State, fake.Suspended)
	assert.Equal(t, m.Queued(t), 0)
	m.AssertTracked(t, 1, &runnerstore.SandboxRecord{TaskID: 1, Version: 4, Handle: backend.Handle{Type: "fake", ID: "c1"}})
}

// TestTaskManagerWait covers Wait after a run: it returns once the run's
// goroutine has, with the sandbox suspended.
func TestTaskManagerWait(t *testing.T) {
	t.Parallel()
	// Arrange
	task := testrunner.StartChange(1, 1)
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewDriver(t, nil)})
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})
	assert.NilError(t, m.Runner(task.ID).Start(t.Context(), task))

	// Act
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	err := m.Wait(ctx)

	// Assert - the run is done: its sandbox is suspended.
	assert.NilError(t, err)
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Suspended)
}

// TestTaskManagerWait_Done covers Wait on a done ctx while a run is still
// followed: it returns ctx's cause without waiting for the run.
func TestTaskManagerWait_Done(t *testing.T) {
	t.Parallel()
	// Arrange - a run whose sandbox's driver is not listening yet.
	task := testrunner.StartChange(1, 1)
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{
		Driver:         testrunner.NewDriver(t, nil),
		DriverNotReady: true,
	})
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})
	assert.NilError(t, m.Runner(task.ID).Start(t.Context(), task))
	boom := errors.New("boom")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(boom)

	// Act
	err := m.Wait(ctx)

	// Assert - Wait returned ctx's cause; once the driver comes up, the run
	// finishes and Wait returns nil.
	assert.ErrorIs(t, err, boom)
	be.SetDriverReady("c1", true)
	ctx, cancelTimeout := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelTimeout()
	assert.NilError(t, m.Wait(ctx))
}

// TestTaskManagerStart_NotReady covers a driver that never comes up: the run
// is reported failed, the sandbox suspended, and the record kept.
func TestTaskManagerStart_NotReady(t *testing.T) {
	t.Parallel()
	// Arrange
	task := testrunner.StartChange(1, 1)
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{
		Driver:         testrunner.NewDriver(t, nil),
		DriverNotReady: true,
	})
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be, TaskRunner: func(opts *runner.TaskRunnerOptions) {
		opts.ReadyTimeout = 100 * time.Millisecond
	}})

	// Act
	assert.NilError(t, m.Runner(task.ID).Start(t.Context(), task))
	assert.NilError(t, m.Wait(t.Context()))

	// Assert
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Suspended)
	m.WaitQueued(t, 1)
	events := m.Submitted(t)
	assert.Assert(t, cmp.Len(events, 1))
	assert.Equal(t, events[0].Event, "failed")
	assert.Equal(t, events[0].Version, int64(1))
	assert.Assert(t, cmp.Contains(events[0].Reason, "driver server did not become ready"))
	m.AssertTracked(t, 1, &runnerstore.SandboxRecord{TaskID: 1, Version: 1, Handle: backend.Handle{Type: "fake", ID: "c1"}})
}

// TestTaskManagerStart_BootFails covers a sandbox that cannot be booted after
// Create: the record is kept, and Destroy destroys the sandbox.
func TestTaskManagerStart_BootFails(t *testing.T) {
	t.Parallel()
	// Arrange - the sandbox Start creates refuses to boot.
	task := testrunner.StartChange(1, 1)
	boom := errors.New("boom")
	be := testrunner.NewBackend(t, nil)
	be.FailNext(fake.OpBoot, "", boom)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})

	// Act
	err := m.Runner(task.ID).Start(t.Context(), task)

	// Assert - Start failed with the record and sandbox kept and nothing run.
	// The record surviving the failed boot also shows it was written before
	// the sandbox was booted.
	assert.ErrorIs(t, err, boom)
	m.AssertRecord(t, 1, &runnerstore.SandboxRecord{TaskID: 1, Version: 1, Handle: backend.Handle{Type: "fake", ID: "c1"}})
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Created)
	assert.Equal(t, sbx.Dials, 0)

	// Act - the task's sandbox is removed.
	assert.NilError(t, m.Runner(1).Destroy(t.Context()))

	// Assert - Destroy found c1, so the TaskRunner still held it; now it holds
	// nothing, and the record is gone.
	assert.DeepEqual(t, be.Snapshot().Destroyed, []string{"c1"})
	m.AssertTracked(t, 1, nil)
}

// TestTaskManagerStart_Gone covers a task whose recorded sandbox has vanished:
// Start fails with ErrGone and leaves the TaskRunner with no sandbox, and the
// next Start creates a fresh one.
func TestTaskManagerStart_Gone(t *testing.T) {
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
	_, err := m.Runner(8).Restore(t.Context(), rec)
	assert.NilError(t, err)
	be.Set("c1", fake.Gone)
	task := testrunner.StartChange(8, 2)

	// Act
	err = m.Runner(task.ID).Start(t.Context(), task)

	// Assert - ErrGone surfaced (the caller emits failed and releases the
	// slot), no fresh sandbox was created, and the dangling record was
	// removed.
	assert.ErrorIs(t, err, backend.ErrGone)
	assert.Assert(t, cmp.Len(be.Snapshot().Specs, 0))
	m.AssertRecord(t, 8, nil)

	// Act - the task is started again. This Start is also the probe for the
	// first one: a Create means it left the TaskRunner with no sandbox.
	task.Version = 3
	assert.NilError(t, m.Runner(task.ID).Start(t.Context(), task))
	assert.NilError(t, m.Wait(t.Context()))

	// Assert - the task got one fresh sandbox, which ran the
	// workspace's agent on it; the driver reported, and the sandbox is suspended.
	snap := be.Snapshot()
	assert.Assert(t, cmp.Len(snap.Specs, 1))
	assert.Equal(t, snap.Specs[0].Version, int64(3))
	fresh, _ := snap.Sandbox("c2")
	assert.Equal(t, fresh.State, fake.Suspended)
	assert.Equal(t, m.Queued(t), 0)
	// The runner never destroyed the vanished sandbox; it only dropped it.
	assert.Assert(t, cmp.Len(snap.Destroyed, 0))
	m.AssertTracked(t, 8, &runnerstore.SandboxRecord{TaskID: 8, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c2"}})
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
			agent:        testrunner.SleepForever,
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
			name:  "suspended",
			state: fake.Suspended,
		},
		{
			name:   "gone",
			state:  fake.Suspended,
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
			client := testrunner.NewDriver(t, &testrunner.DriverOptions{
				Version: 3,
				Agent:   tt.agent,
			})
			if tt.finish {
				testrunner.WaitFinished(t, client, 3)
			}
			be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
			m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})
			if tt.state != 0 {
				rec := runnerstore.SandboxRecord{TaskID: 1, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c1"}}
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
				testrunner.WaitFinished(t, client, 3)
			}
		})
	}
}

func TestTaskManagerRemove(t *testing.T) {
	t.Parallel()
	rec := runnerstore.SandboxRecord{TaskID: 1, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c1"}}
	tests := []struct {
		name string
		// record, when set, is the task's record, of suspended sandbox c1.
		record *runnerstore.SandboxRecord
		// vanish moves c1 to gone once it is restored.
		vanish      bool
		destroyErr  error
		wantErr     bool
		wantDestroy int // the Destroy calls on c1 that succeeded
		wantRecord  bool
	}{
		{
			name:        "suspended",
			record:      &rec,
			wantDestroy: 1,
		},
		{
			// A gone sandbox's Destroy returns nil, so the record is dropped.
			name:        "sandbox gone",
			record:      &runnerstore.SandboxRecord{TaskID: 1, Handle: backend.Handle{Type: "fake", ID: "c1"}},
			vanish:      true,
			wantDestroy: 1,
		},
		{
			// A failed destroy keeps the record so the removal is retried.
			name:       "destroy fails",
			record:     &rec,
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
			be := testrunner.NewBackend(t, nil)
			m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})
			if tt.record != nil {
				assert.NilError(t, m.RunnerStore.WriteSandbox(t.Context(), *tt.record))
				be.Add("c1", fake.Suspended)
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
				m.AssertTracked(t, 1, tt.record)
			} else {
				m.AssertTracked(t, 1, nil)
			}
		})
	}
}

// TestTaskManagerAfterDestroy covers the operations on a task whose sandbox
// was destroyed: they see no sandbox.
func TestTaskManagerAfterDestroy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		op   func(t *testing.T, m *testrunner.Manager)
	}{
		{name: "kill", op: func(t *testing.T, m *testrunner.Manager) {
			stopped, err := m.Runner(1).Kill(t.Context())
			assert.NilError(t, err)
			assert.Assert(t, !stopped)
		}},
		{name: "running", op: func(t *testing.T, m *testrunner.Manager) {
			running, err := m.Runner(1).Running(t.Context())
			assert.NilError(t, err)
			assert.Assert(t, !running)
		}},
		{name: "remove", op: func(t *testing.T, m *testrunner.Manager) {
			assert.NilError(t, m.Runner(1).Destroy(t.Context()))
		}},
		{name: "version", op: func(t *testing.T, m *testrunner.Manager) {
			assert.Equal(t, m.Runner(1).Version(), int64(0))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - the task's suspended sandbox is destroyed.
			rec := runnerstore.SandboxRecord{TaskID: 1, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c1"}}
			be := testrunner.NewBackend(t, nil)
			be.Add("c1", fake.Suspended)
			m := testrunner.NewManager(t, &testrunner.ManagerOptions{
				Backend:     be,
				RunnerStore: testrunner.NewRunnerStore(t, rec),
			})
			tr := m.Runner(1)
			_, err := tr.Restore(t.Context(), rec)
			assert.NilError(t, err)
			assert.NilError(t, tr.Destroy(t.Context()))

			// Act
			tt.op(t, m)

			// Assert - the sandbox was destroyed once and nothing else was
			// done to it, and the task has no sandbox.
			sbx, _ := be.Snapshot().Sandbox("c1")
			assert.DeepEqual(t, sbx, fake.Info{
				Handle:   backend.Handle{Type: "fake", ID: "c1"},
				State:    fake.Gone,
				Destroys: 1,
			})
			m.AssertTracked(t, 1, nil)
		})
	}
}
