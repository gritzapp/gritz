package runner_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

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
)

// taskRunning is a GetTask that reports every task RUNNING on the server.
func taskRunning(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
	return &gritzv1.GetTaskResponse{
		Task: &gritzv1.Task{Id: req.Id, Status: gritzv1.TaskStatus_RUNNING},
	}, nil
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

// closedStore returns a runner store that is already closed, so every write
// to it fails.
func closedStore(t *testing.T) *runnerstore.Store {
	t.Helper()
	s, err := runnerstore.Open(filepath.Join(t.TempDir(), "runner.db"))
	assert.NilError(t, err)
	assert.NilError(t, s.Close())
	return s
}
