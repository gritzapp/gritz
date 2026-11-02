package runner_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/backend/fake"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/testrunner"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
	"gotest.tools/v3/poll"
)

// taskRunning is a GetTask that reports every task RUNNING on the server.
func taskRunning(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
	return &gritzv1.GetTaskResponse{
		Task: &gritzv1.Task{Id: req.Id, Status: gritzv1.TaskStatus_RUNNING},
	}, nil
}

// TestTaskManagerLoad_RestartReattaches covers a runner restart mid-run: the
// first runner process shuts down while it follows the run, and the next one
// reattaches to it from the record in Load.
func TestTaskManagerLoad_RestartReattaches(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going on an up sandbox, recorded at its
	// version, followed by a runner that then shuts down.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{
		Version: 3,
		Agent:   testrunner.SleepForever,
	})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	be.Add("c1", fake.Up)
	store := testrunner.NewRunnerStore(t, runnerstore.SandboxRecord{TaskID: 1, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c1"}})
	server := testrunner.NewServer(testrunner.ServerOptions{})
	server.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 3))
	first := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be, Server: server, RunnerStore: store})
	ctx, cancel := context.WithCancel(t.Context())
	assert.NilError(t, first.Load(ctx))
	testrunner.WaitRuns(t, be, "c1", 1)
	cancel()
	// The sandbox stays up for next-boot rehydration.
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Up)
	assert.Equal(t, sbx.Suspends, 0)

	// Act - the next runner process loads the record and goes straight to
	// drive, over its own Open of the same sandbox; then the run is stopped.
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be, Server: server, RunnerStore: store})
	assert.NilError(t, m.Load(t.Context()))
	testrunner.WaitRuns(t, be, "c1", sbx.Runs+1)
	m.Run(t)
	m.Add(testrunner.CommandEvent(2, 1, gritzv1.TaskCommand_STOP, 3))

	// Assert - the reattached runner saw Idle: the sandbox is suspended, it
	// was never booted, and no event is owed; the driver ran run 3 once.
	sbx = testrunner.WaitSuspended(t, be, "c1")
	assert.Equal(t, sbx.Boots, 0)
	assert.Equal(t, m.Queued(t), 0)
	assert.DeepEqual(t, client.Reported(), []string{"started", "stopped"})
}

func TestTaskManagerLoad(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		record     runnerstore.SandboxRecord
		state      fake.State
		openErr    error
		statusErr  error
		wantEvents []*gritzv1.RunnerEvent
		// wantRecord is whether the record is kept.
		wantRecord bool
		// wantAttach is whether RunLoop reattached to the sandbox's run,
		// holding the manager's one concurrency slot.
		wantAttach bool
	}{
		{
			name:       "up",
			record:     runnerstore.SandboxRecord{TaskID: 1, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c1"}},
			state:      fake.Up,
			wantRecord: true,
			wantAttach: true,
		},
		{
			// A suspended husk is a lost-report backstop; its record is kept.
			name:   "suspended",
			record: runnerstore.SandboxRecord{TaskID: 1, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c1"}},
			state:  fake.Suspended,
			wantEvents: []*gritzv1.RunnerEvent{
				{Event: "failed", TaskId: 1, Version: 3, Reason: "sandbox exited"},
			},
			wantRecord: true,
		},
		{
			// A gone sandbox is a lost-report backstop; its record is dropped.
			name:   "gone",
			record: runnerstore.SandboxRecord{TaskID: 1, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c1"}},
			state:  fake.Gone,
			wantEvents: []*gritzv1.RunnerEvent{
				{Event: "failed", TaskId: 1, Version: 3, Reason: "sandbox exited"},
			},
		},
		{
			// A failed status check leaves the record alone.
			name:       "status error",
			record:     runnerstore.SandboxRecord{TaskID: 1, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c1"}},
			state:      fake.Up,
			statusErr:  errors.New("boom"),
			wantRecord: true,
		},
		{
			// A record whose handle cannot be opened (a malformed handle) has
			// no sandbox, so it is dropped.
			name:    "open fails",
			record:  runnerstore.SandboxRecord{TaskID: 1, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c1"}},
			state:   fake.Up,
			openErr: errors.New("malformed handle"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - the record's task is still RUNNING on the server, with
			// a run that keeps going on its sandbox's driver. Task 2 is
			// pending, and every other sandbox's driver runs its agent to
			// completion.
			client := testrunner.NewDriver(t, &testrunner.DriverOptions{
				Version: 3,
				Agent:   testrunner.SleepForever,
			})
			other := testrunner.NewDriver(t, nil)
			be := testrunner.NewBackend(t, &testrunner.BackendOptions{
				Driver:  other,
				Drivers: map[string]driverv1connect.DriverServiceClient{"c1": client},
			})
			be.Add("c1", tt.state)
			if tt.openErr != nil {
				be.FailNext(fake.OpOpen, "c1", tt.openErr)
			}
			if tt.statusErr != nil {
				be.FailNext(fake.OpStatus, "c1", tt.statusErr)
			}
			m := testrunner.NewManager(t, &testrunner.ManagerOptions{
				Backend:     be,
				RunnerStore: testrunner.NewRunnerStore(t, tt.record),
			})
			m.Client.GetTaskFunc = taskRunning

			// Act
			err := m.Load(t.Context())

			// Assert - an up sandbox was reattached, and its busy period holds
			// the only slot: task 2's start waits until the run finishes.
			assert.NilError(t, err)
			m.Run(t)
			if tt.wantAttach {
				m.Add(testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 3))
			}
			m.Add(testrunner.CommandEvent(2, 2, gritzv1.TaskCommand_START, 1))
			if tt.wantAttach {
				testrunner.WaitRuns(t, be, "c1", 1)
				assert.Assert(t, cmp.Len(be.Snapshot().Specs, 0), "the slot Load seeded is free")
				m.Add(testrunner.CommandEvent(3, 1, gritzv1.TaskCommand_STOP, 3))
			} else {
				sbx, _ := be.Snapshot().Sandbox("c1")
				assert.Equal(t, sbx.Runs, 0)
			}
			// Assert - the slot is free, so task 2 gets a sandbox.
			testrunner.WaitSuspended(t, be, "c2")
			snap := be.Snapshot()
			assert.Assert(t, cmp.Len(snap.Specs, 1))
			assert.Equal(t, snap.Specs[0].TaskID, int64(2))
			// Assert - the backstop's events, and Load never destroys.
			assert.DeepEqual(t, m.Submitted(t), tt.wantEvents, protocmp.Transform())
			assert.Assert(t, cmp.Len(snap.Destroyed, 0))
			if tt.wantRecord {
				m.AssertRecord(t, 1, &tt.record)
			} else {
				m.AssertRecord(t, 1, nil)
			}
		})
	}
}

// TestTaskManagerLoad_VersionScopedBackstop checks that the boot-time backstop stamps
// the exited record's version onto its "failed" so ApplyRunnerEvent drops it for
// a superseded run and applies it for the current run.
func TestTaskManagerLoad_VersionScopedBackstop(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		recVersion int64 // version stamped in the seeded exited record
		taskNow    int64 // the task's current version when the event is applied
		wantApply  bool
	}{
		{name: "superseded run dropped", recVersion: 4, taskNow: 5, wantApply: false},
		{name: "current run applies", recVersion: 4, taskNow: 4, wantApply: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - one suspended husk whose task is still RUNNING on the server,
			// so the boot-time backstop fires a "failed".
			be := testrunner.NewBackend(t, nil)
			be.Add("c1", fake.Suspended)
			m := testrunner.NewManager(t, &testrunner.ManagerOptions{
				Backend: be,
				RunnerStore: testrunner.NewRunnerStore(t, runnerstore.SandboxRecord{
					TaskID:  1,
					Version: tt.recVersion,
					Handle:  backend.Handle{Type: "fake", ID: "c1"},
				}),
			})
			m.Client.GetTaskFunc = taskRunning

			// Act - Load drives the suspended husk through the backstop, which
			// emits a "failed" stamped with the record's version.
			assert.NilError(t, m.Load(t.Context()))
			events := m.Submitted(t)
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

// TestTaskManagerLoad_Archived covers a task archived before a runner
// restart: Load hands its TaskRunner the stored state, archived included, so
// the sandbox it recorded is destroyed, a failed destroy being retried, and
// the record removed.
func TestTaskManagerLoad_Archived(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		destroyErr error
	}{
		{name: "destroyed"},
		{name: "destroy fails once", destroyErr: errors.New("boom")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - task 1 ran on c1, now suspended, and was archived.
			be := testrunner.NewBackend(t, nil)
			be.Add("c1", fake.Suspended)
			if tt.destroyErr != nil {
				be.FailNext(fake.OpDestroy, "c1", tt.destroyErr)
			}
			m := testrunner.NewManager(t, &testrunner.ManagerOptions{
				Backend:     be,
				RunnerStore: testrunner.NewRunnerStore(t, runnerstore.SandboxRecord{TaskID: 1, Version: 3, Handle: backend.Handle{Type: "fake", ID: "c1"}}),
			})
			m.Replicated(t,
				testrunner.CommandEvent(1, 1, gritzv1.TaskCommand_START, 3),
				testrunner.ArchiveEvent(2, 1),
			)

			// Act
			assert.NilError(t, m.Load(t.Context()))

			// Assert - c1 is destroyed, and its record removed.
			testrunner.WaitSandbox(t, be, "c1", func(sbx fake.Info) bool { return sbx.Destroys == 1 })
			poll.WaitOn(t, func(poll.LogT) poll.Result {
				_, ok, err := m.RunnerStore.Sandbox(t.Context(), 1)
				if err != nil {
					return poll.Error(err)
				}
				if ok {
					return poll.Continue("task 1 has a record")
				}
				return poll.Success()
			}, poll.WithTimeout(3*time.Second))
		})
	}
}

// TestTaskManagerRun_Fatal covers a durable write failure in a RunLoop: Run
// returns the FatalError cause it terminated the runner with.
func TestTaskManagerRun_Fatal(t *testing.T) {
	t.Parallel()
	// Arrange - the outbox's store is closed, so the first event the runner
	// persists fails and terminates it.
	ctx, cancelCause := context.WithCancelCause(t.Context())
	defer cancelCause(nil)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Fatal:      cancelCause,
		QueueStore: closedStore(t),
	})
	done := make(chan error, 1)
	go func() { done <- m.TaskManager.Run(ctx) }()

	// Act - a stop command for a task with no sandbox, whose "stopped" cannot
	// be persisted.
	m.Add(testrunner.CommandEvent(1, 7, gritzv1.TaskCommand_STOP, 3))

	// Assert - Run returns the FatalError cause.
	select {
	case err := <-done:
		var fatal runner.FatalError
		assert.Assert(t, errors.As(err, &fatal), "got %v", err)
		assert.ErrorContains(t, err, "database is closed")
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}
}

// TestTaskManagerDie_StopWithoutSandbox is the sharpest unrecoverable case from #1209:
// a stop command with no running sandbox has no reconciliation path on restart
// (Load has nothing to probe and cannot re-derive "stopped"), so a dropped persist
// would leave the task stuck in cancelling forever. When the durable outbox write
// fails there, the runner must crash — die cancels the root ctx with a
// FatalError cause — instead of logging and continuing.
func TestTaskManagerDie_StopWithoutSandbox(t *testing.T) {
	t.Parallel()
	// No record, so there is no run to stop → the TaskManager emits "stopped".
	// Back the outbox with a closed store, so its durable write fails.
	ctx, cancelCause := context.WithCancelCause(t.Context())
	defer cancelCause(nil)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Fatal:      cancelCause,
		QueueStore: closedStore(t),
	})
	m.RunContext(t, ctx)

	// Act - the "stopped" enqueue fails durably, so die fires.
	m.Add(testrunner.CommandEvent(1, 7, gritzv1.TaskCommand_STOP, 3))

	// Assert - the root ctx is cancelled with a FatalError cause, which the
	// command layer maps to a non-zero exit.
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("runner was not terminated")
	}
	var fatal runner.FatalError
	assert.Assert(t, errors.As(context.Cause(ctx), &fatal))
}

// TestTaskManagerGracefulShutdown_NoFatalCause is the complement: on the happy path the
// durable write succeeds, die never fires, and a plain ctx cancel (the signal
// shutdown) leaves no FatalError cause — so the command layer's exit mapping
// returns nil (exit 0) rather than crashing.
func TestTaskManagerGracefulShutdown_NoFatalCause(t *testing.T) {
	t.Parallel()
	ctx, cancelCause := context.WithCancelCause(context.Background())
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Fatal: cancelCause})
	m.RunContext(t, ctx)

	// The durable enqueue succeeds, so die never fires.
	m.Add(testrunner.CommandEvent(1, 7, gritzv1.TaskCommand_STOP, 3))
	m.WaitQueued(t, 1)

	// A graceful signal shutdown cancels ctx with no FatalError cause.
	cancelCause(nil)
	var fatal runner.FatalError
	assert.Assert(t, !errors.As(context.Cause(ctx), &fatal))
}

// TestTaskManagerLoad_SandboxGone covers a sandbox that vanishes under its
// driver's stream: drive reports the run failed, and the record is dropped.
func TestTaskManagerLoad_SandboxGone(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going on an up sandbox, driven by the
	// RunLoop Load starts for it.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{
		Version: 3,
		Agent:   testrunner.SleepForever,
	})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	be.Add("c1", fake.Up)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Backend: be,
		RunnerStore: testrunner.NewRunnerStore(t, runnerstore.SandboxRecord{
			TaskID:  1,
			Version: 3,
			Handle:  backend.Handle{Type: "fake", ID: "c1"},
		}),
	})
	assert.NilError(t, m.Load(t.Context()))
	testrunner.WaitRuns(t, be, "c1", 1)

	// Act - the sandbox vanishes under the run.
	be.Set("c1", fake.Gone)

	// Assert
	m.WaitQueued(t, 1)
	assert.DeepEqual(t, m.Submitted(t), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 1, Version: 3, Reason: "sandbox gone"},
	}, protocmp.Transform())
	m.AssertRecord(t, 1, nil)
}

// closedStore returns a runner store that is already closed, so every write
// to it fails.
func closedStore(t *testing.T) *runnerstore.Store {
	t.Helper()
	s, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	assert.NilError(t, s.Close())
	return s
}
