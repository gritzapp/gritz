package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/backend/fake"
	"github.com/gritzapp/gritz/internal/runner/taskstate"
	"github.com/gritzapp/gritz/internal/runner/testrunner"
	"github.com/gritzapp/gritz/internal/x/outbox"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// legacyRecord is a record written by an older runner: its Data carries the run
// version.
func legacyRecord(taskID int64, id string) taskstate.Record {
	return taskstate.Record{TaskID: taskID, Version: 3, Type: "fake", ID: id, Data: json.RawMessage(`{"version":3}`)}
}

// taskRunning is a GetTask that reports every task RUNNING on the server.
func taskRunning(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
	return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Id: req.Id, Status: gritzv1.TaskStatus_RUNNING}}, nil
}

// TestTaskManagerLoad_RestartReattaches covers a runner restart mid-run: the
// first runner process shuts down while it follows the run, and the next one
// reattaches to it from the record in Load.
func TestTaskManagerLoad_RestartReattaches(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going on an up sandbox, recorded at its
	// version, followed by a runner that then shuts down.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3, Agent: testrunner.SleepForever})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	be.Add("c1", fake.Up)
	store := testrunner.NewStore(t, taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"})
	first := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be, Store: store})
	ctx, cancel := context.WithCancel(t.Context())
	assert.NilError(t, first.Load(ctx))
	testrunner.WaitRuns(t, be, "c1", 1)
	cancel()
	assert.NilError(t, first.Wait(t.Context()))
	// The sandbox stays up for next-boot rehydration.
	sbx, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Up)
	assert.Equal(t, sbx.Parks, 0)

	// Act - the next runner process loads the record and reattaches, over its
	// own Open of the same sandbox; then the run finishes.
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be, Store: store})
	assert.NilError(t, m.Load(t.Context()))
	testrunner.WaitRuns(t, be, "c1", sbx.Runs+1)
	testrunner.StopRun(t, client, 3)
	assert.NilError(t, m.Wait(t.Context()))

	// Assert - the reattached runner saw Finished: the sandbox is parked and
	// no event is owed.
	sbx, _ = be.Snapshot().Sandbox("c1")
	assert.Equal(t, sbx.State, fake.Parked)
	assert.Equal(t, m.Queued(t), 0)
}

func TestTaskManagerLoad(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		record     taskstate.Record
		state      fake.State
		openErr    error
		statusErr  error
		wantEvents []*gritzv1.RunnerEvent
		// wantRecord is whether the record is kept, and the task's TaskRunner
		// holds its sandbox.
		wantRecord bool
		// wantAttach is whether a Run reattached to the sandbox's run, holding
		// the manager's one concurrency slot.
		wantAttach bool
	}{
		{
			name:       "up",
			record:     legacyRecord(1, "c1"),
			state:      fake.Up,
			wantRecord: true,
			wantAttach: true,
		},
		{
			// A parked husk is a lost-report backstop; its record is kept.
			name:       "parked",
			record:     legacyRecord(1, "c1"),
			state:      fake.Parked,
			wantEvents: []*gritzv1.RunnerEvent{{Event: "failed", TaskId: 1, Version: 3, Reason: "sandbox exited"}},
			wantRecord: true,
		},
		{
			// A gone sandbox is a lost-report backstop; its record is dropped.
			name:       "gone",
			record:     legacyRecord(1, "c1"),
			state:      fake.Gone,
			wantEvents: []*gritzv1.RunnerEvent{{Event: "failed", TaskId: 1, Version: 3, Reason: "sandbox exited"}},
		},
		{
			// A failed status check leaves the record alone.
			name:       "status error",
			record:     legacyRecord(1, "c1"),
			state:      fake.Up,
			statusErr:  errors.New("boom"),
			wantRecord: true,
		},
		{
			// A record whose handle cannot be opened (a malformed handle) has
			// no sandbox, so it is dropped.
			name:    "open fails",
			record:  legacyRecord(1, "c1"),
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
			client := testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3, Agent: testrunner.SleepForever})
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
			m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be, Store: testrunner.NewStore(t, tt.record)})
			m.Client.GetTaskFunc = taskRunning
			m.SetTasks(testrunner.Task(&testrunner.TaskOptions{ID: 2}))

			// Act
			err := m.Load(t.Context())

			// Assert - an up sandbox was reattached, and its Run holds the
			// only slot: task 2 gets no sandbox until the run finishes.
			assert.NilError(t, err)
			if tt.wantAttach {
				testrunner.WaitRuns(t, be, "c1", 1)
				assert.NilError(t, m.Poll(t.Context()))
				assert.Assert(t, cmp.Len(be.Snapshot().Specs, 0), "the slot Load seeded is free")
				testrunner.StopRun(t, client, 3)
				assert.NilError(t, m.Wait(t.Context()))
			} else {
				sbx, _ := be.Snapshot().Sandbox("c1")
				assert.Equal(t, sbx.Runs, 0)
			}
			// Assert - the slot is free, so task 2 gets a sandbox.
			assert.NilError(t, m.Poll(t.Context()))
			assert.NilError(t, m.Wait(t.Context()))
			snap := be.Snapshot()
			assert.Assert(t, cmp.Len(snap.Specs, 1))
			assert.Equal(t, snap.Specs[0].TaskID, int64(2))
			// Assert - the backstop's events, and Load never destroys.
			assert.DeepEqual(t, m.Submitted(t), tt.wantEvents, protocmp.Transform())
			assert.Assert(t, cmp.Len(snap.Destroyed, 0))
			if tt.wantRecord {
				m.AssertTracked(t, 1, &tt.record)
			} else {
				m.AssertTracked(t, 1, nil)
			}
		})
	}
}

// TestTaskManagerLoad_VersionScopedBackstop checks that the boot-time backstop stamps
// the exited record's version onto its "failed" so ApplyRunnerEvent drops it for
// a superseded run, applies it for the current run, and bypasses for a legacy
// version-0 record.
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
		{name: "legacy record bypasses", recVersion: 0, taskNow: 5, wantApply: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - one parked husk whose task is still RUNNING on the server,
			// so the boot-time backstop fires a "failed".
			be := testrunner.NewBackend(t, nil)
			be.Add("c1", fake.Parked)
			m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be, Store: testrunner.NewStore(t, taskstate.Record{TaskID: 1, Version: tt.recVersion, Type: "fake", ID: "c1"})})
			m.Client.GetTaskFunc = taskRunning

			// Act - Load drives the parked husk through the backstop, which
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

func TestTaskManagerPrune(t *testing.T) {
	t.Parallel()
	// Arrange - every record but the deleted task's (4) belongs to an archived
	// task, except 2's. Each is restored parked, but task 8's, restored up.
	recs := []taskstate.Record{
		legacyRecord(1, "c1"), // archived -> removed
		legacyRecord(2, "c2"), // not archived -> kept
		legacyRecord(3, "c3"), // up -> kept
		legacyRecord(4, "c4"), // deleted -> removed
		legacyRecord(5, "c5"), // archived and gone -> removed
		legacyRecord(7, "c7"), // status unknown -> kept
		legacyRecord(8, "c8"), // claimed -> kept
	}
	be := testrunner.NewBackend(t, nil)
	for _, rec := range recs {
		be.Add(rec.ID, fake.Parked)
	}
	be.Set("c8", fake.Up)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be, Store: testrunner.NewStore(t, recs...)})
	m.Client.GetTaskFunc = func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
		if req.Id == 4 {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("task not found"))
		}
		return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Id: req.Id, Archived: req.Id != 2}}, nil
	}
	for _, rec := range recs {
		_, err := m.Runner(rec.TaskID).Restore(t.Context(), rec)
		assert.NilError(t, err)
	}
	// Behind the runner's back, c3 is started and c5 vanishes; c7's status
	// check will fail.
	be.Set("c3", fake.Up)
	be.Set("c5", fake.Gone)
	be.FailNext(fake.OpStatus, "c7", errors.New("boom"))
	// Task 8's run is claimed (restored up), and its sandbox has stopped since.
	be.Set("c8", fake.Parked)

	// Act
	err := m.Prune(t.Context())
	assert.NilError(t, err)

	// Assert - archived/deleted tasks' sandboxes are destroyed, including the one
	// that had already vanished, and the others are left as they were.
	snap := be.Snapshot()
	destroyed := slices.Sorted(slices.Values(snap.Destroyed))
	assert.DeepEqual(t, destroyed, []string{"c1", "c4"})
	for id, want := range map[string]struct {
		state    fake.State
		destroys int
	}{
		"c1": {fake.Gone, 1},
		"c2": {fake.Parked, 0},
		"c3": {fake.Up, 0},
		"c4": {fake.Gone, 1},
		"c5": {fake.Gone, 1},
		"c7": {fake.Parked, 0},
		"c8": {fake.Parked, 0},
	} {
		sbx, _ := snap.Sandbox(id)
		assert.Equal(t, sbx.State, want.state, "sandbox %s", id)
		assert.Equal(t, sbx.Destroys, want.destroys, "sandbox %s", id)
	}
	// The retired tasks' records are removed.
	stored, err := m.Store.List()
	assert.NilError(t, err)
	var kept []int64
	for _, rec := range stored {
		kept = append(kept, rec.TaskID)
	}
	slices.Sort(kept)
	assert.DeepEqual(t, kept, []int64{2, 3, 7, 8})
}

// TestTaskManagerRun covers the poll loop: between Poll passes it prunes an
// archived task's parked sandbox, and once a durable write fails it returns
// the FatalError cause.
func TestTaskManagerRun(t *testing.T) {
	t.Parallel()
	// Arrange - task 1 is archived and its sandbox is parked. The server has
	// no commands until SetTasks gives it a stop. The outbox's durable write fails, so the
	// first event the runner persists terminates it.
	rec := taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"}
	be := testrunner.NewBackend(t, nil)
	be.Add("c1", fake.Parked)
	ctx, cancelCause := context.WithCancelCause(t.Context())
	defer cancelCause(nil)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Backend:      be,
		Store:        testrunner.NewStore(t, rec),
		PollInterval: 10 * time.Millisecond,
		Fatal:        cancelCause,
		// The die path is reached through Enqueue → Append alone, so the other
		// store methods are never called.
		QueueStore: &outbox.StoreMock{AppendFunc: func(json.RawMessage) error { return errors.New("no space left on device") }},
	})
	m.Client.GetTaskFunc = func(_ context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
		return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Id: req.Id, Archived: true}}, nil
	}
	_, err := m.Runner(1).Restore(t.Context(), rec)
	assert.NilError(t, err)

	// Act
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()

	// Assert - the archived task's sandbox is pruned while Run keeps polling.
	waitCtx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, err = be.Wait(waitCtx, func(s fake.Snapshot) bool { return len(s.Destroyed) > 0 })
	assert.NilError(t, err, "sandbox was not pruned")
	assert.DeepEqual(t, be.Snapshot().Destroyed, []string{"c1"})
	m.AssertRecord(t, 1, nil)

	// Act - a stop command for a task with no sandbox, whose "stopped" cannot
	// be persisted.
	m.SetTasks(testrunner.Task(&testrunner.TaskOptions{ID: 7, Version: 3, Status: model.TaskStatusCancelling, Command: model.TaskCommandStop}))

	// Assert - Run returns the FatalError cause.
	select {
	case err := <-done:
		var fatal runner.FatalError
		assert.Assert(t, errors.As(err, &fatal), "got %v", err)
		assert.ErrorContains(t, err, "no space left on device")
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestTaskManagerRunning(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// state is the state c1 moves to after it is restored parked; 0 means
		// the task has no record.
		state fake.State
		err   error
		want  bool
	}{
		{name: "up", state: fake.Up, want: true},
		{name: "parked", state: fake.Parked},
		{name: "gone", state: fake.Gone},
		{name: "no record"},
		{name: "status error", state: fake.Up, err: errors.New("boom")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - the task's TaskRunner holds c1, which then moves
			// behind the runner's back.
			be := testrunner.NewBackend(t, nil)
			m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be})
			if tt.state != 0 {
				rec := legacyRecord(1, "c1")
				assert.NilError(t, m.Store.Write(rec))
				be.Add("c1", fake.Parked)
				_, err := m.Runner(1).Restore(t.Context(), rec)
				assert.NilError(t, err)
				if tt.state != fake.Parked {
					be.Set("c1", tt.state)
				}
			}
			if tt.err != nil {
				be.FailNext(fake.OpStatus, "c1", tt.err)
			}

			// Act
			running, err := m.Runner(1).Running(t.Context())

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

// TestTaskManagerDie_StopWithoutSandbox is the sharpest unrecoverable case from #1209:
// a stop command with no running sandbox has no reconciliation path on restart
// (Load has nothing to probe and cannot re-derive "stopped"), so a dropped persist
// would leave the task stuck in cancelling forever. When the durable outbox write
// fails there, the runner must crash — die cancels the root ctx with a
// FatalError cause — instead of logging and continuing.
func TestTaskManagerDie_StopWithoutSandbox(t *testing.T) {
	t.Parallel()
	// No record, so there is no run to stop → the TaskManager emits "stopped".
	// Back the outbox with a store whose durable Append fails. The die path is
	// reached through Enqueue → Append alone, so Peek/Drop/Len are never called
	// (an unstubbed moq method would panic if they were).
	ctx, cancelCause := context.WithCancelCause(t.Context())
	defer cancelCause(nil)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{
		Fatal:      cancelCause,
		QueueStore: &outbox.StoreMock{AppendFunc: func(json.RawMessage) error { return errors.New("no space left on device") }},
	})
	m.SetTasks(testrunner.Task(&testrunner.TaskOptions{ID: 7, Version: 3, Status: model.TaskStatusCancelling, Command: model.TaskCommandStop}))

	// Act - the "stopped" enqueue fails durably, so die fires.
	assert.NilError(t, m.Poll(ctx))

	// Assert - the root ctx is cancelled with a FatalError cause, which the
	// command layer maps to a non-zero exit.
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
	m.SetTasks(testrunner.Task(&testrunner.TaskOptions{ID: 7, Version: 3, Status: model.TaskStatusCancelling, Command: model.TaskCommandStop}))

	// The durable enqueue succeeds, so die never fires.
	assert.NilError(t, m.Poll(ctx))

	// A graceful signal shutdown cancels ctx with no FatalError cause.
	cancelCause(nil)
	var fatal runner.FatalError
	assert.Assert(t, !errors.As(context.Cause(ctx), &fatal))
}

// TestTaskManagerHandleEvents_Lost covers a lost run reported through the
// events channel: HandleEvents turns it into the run's "failed" in the outbox.
func TestTaskManagerHandleEvents_Lost(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going on an up sandbox, followed by the Run
	// goroutine Load starts for it.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3, Agent: testrunner.SleepForever})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	be.Add("c1", fake.Up)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be, Store: testrunner.NewStore(t, taskstate.Record{TaskID: 1, Version: 3, Type: "fake", ID: "c1"})})
	assert.NilError(t, m.Load(t.Context()))
	testrunner.WaitRuns(t, be, "c1", 1)

	// Act - the sandbox vanishes under the run, which is lost.
	be.Set("c1", fake.Gone)
	assert.NilError(t, m.Wait(t.Context()))

	// Assert
	m.WaitQueued(t, 1)
	assert.DeepEqual(t, m.Submitted(t), []*gritzv1.RunnerEvent{
		{Event: "failed", TaskId: 1, Version: 3, Reason: "sandbox stopped before the run finished"},
	}, protocmp.Transform())
}

// TestTaskManagerHandleEvents_Fatal covers a failed record write: the
// TaskRunner sends a TaskFatalEvent, and HandleEvents terminates the runner
// with a FatalError cause.
func TestTaskManagerHandleEvents_Fatal(t *testing.T) {
	t.Parallel()
	// Arrange - a store whose directory is gone, so every write fails.
	dir := t.TempDir()
	store, err := taskstate.Open(dir)
	assert.NilError(t, err)
	assert.NilError(t, os.RemoveAll(dir))
	be := testrunner.NewBackend(t, nil)
	ctx, cancelCause := context.WithCancelCause(t.Context())
	defer cancelCause(nil)
	m := testrunner.NewManager(t, &testrunner.ManagerOptions{Backend: be, Store: store, Fatal: cancelCause})

	// Act
	err = m.Start(t.Context(), testrunner.Task(nil))

	// Assert - Start failed, the unrecorded sandbox was destroyed without
	// being started, and the runner's root ctx is cancelled with a FatalError
	// cause.
	assert.ErrorContains(t, err, "failed to record task handle")
	snap := be.Snapshot()
	assert.DeepEqual(t, snap.Destroyed, []string{"c1"})
	c1, _ := snap.Sandbox("c1")
	assert.Equal(t, c1.Starts, 0)
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("runner was not terminated")
	}
	var fatal runner.FatalError
	assert.Assert(t, errors.As(context.Cause(ctx), &fatal))

	// Probe, last - a Start creates again, so the failed write left the
	// TaskRunner with no sandbox. Its own write fails as well.
	err = m.Runner(1).Start(t.Context(), backend.Spec{TaskID: 1, Version: testrunner.ProbeVersion})
	assert.ErrorContains(t, err, "failed to record task handle")
	assert.Assert(t, cmp.Len(be.Snapshot().Specs, 2))
}
