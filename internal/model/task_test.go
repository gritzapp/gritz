package model

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestTaskStatus_Label(t *testing.T) {
	t.Parallel()
	// The unspecified (zero) status renders empty — e.g. a freshly created task
	// has no prior status.
	assert.Equal(t, TaskStatusUnspecified.Label(), "")
	assert.Equal(t, TaskStatusPending.Label(), "Pending")
	assert.Equal(t, TaskStatusCancelled.Label(), "Cancelled")
}

func TestTask_ApplyRunnerEvent(t *testing.T) {
	tests := []struct {
		name    string
		before  Task
		after   Task
		event   RunnerEvent
		changed bool
	}{
		// Version handling
		{
			name: "version mismatch returns false",
			before: Task{
				Status:  TaskStatusPending,
				Command: TaskCommandRestart,
				Version: 2,
			},
			event: RunnerEvent{
				Event:   RunnerEventStarted,
				Version: 1,
			},
			changed: false,
		},
		{
			name: "version 0 bypasses check",
			before: Task{
				Status:  TaskStatusRunning,
				Version: 5,
			},
			after: Task{
				Status:  TaskStatusFailed,
				Version: 5,
			},
			event: RunnerEvent{
				Event:   RunnerEventFailed,
				Version: 0,
			},
			changed: true,
		},

		// Started events
		{
			name: "started: pending with restart -> running",
			before: Task{
				Status:  TaskStatusPending,
				Command: TaskCommandRestart,
			},
			after: Task{
				Status: TaskStatusRunning,
			},
			event: RunnerEvent{
				Event: RunnerEventStarted,
			},
			changed: true,
		},
		{
			name: "started: restarting with restart -> running",
			before: Task{
				Status:  TaskStatusRestarting,
				Command: TaskCommandRestart,
			},
			after: Task{
				Status: TaskStatusRunning,
			},
			event: RunnerEvent{
				Event: RunnerEventStarted,
			},
			changed: true,
		},
		{
			name: "started: running with restart -> running (SIGHUP case)",
			before: Task{
				Status:  TaskStatusRunning,
				Command: TaskCommandRestart,
			},
			after: Task{
				Status: TaskStatusRunning,
			},
			event: RunnerEvent{
				Event: RunnerEventStarted,
			},
			changed: true,
		},
		{
			name: "started: pending without restart command returns false",
			before: Task{
				Status: TaskStatusPending,
			},
			event: RunnerEvent{
				Event: RunnerEventStarted,
			},
			changed: false,
		},
		{
			name: "started: cancelled -> cancelling with stop (no bump)",
			before: Task{
				Status:  TaskStatusCancelled,
				Version: 3,
			},
			after: Task{
				Status:  TaskStatusCancelling,
				Command: TaskCommandStop,
				Version: 3,
			},
			event: RunnerEvent{
				Event: RunnerEventStarted,
			},
			changed: true,
		},
		{
			name: "started: archived completed -> cancelling with stop (no bump)",
			before: Task{
				Status:   TaskStatusCompleted,
				Archived: true,
				Version:  3,
			},
			after: Task{
				Status:   TaskStatusCancelling,
				Command:  TaskCommandStop,
				Version:  3,
				Archived: true,
			},
			event: RunnerEvent{
				Event: RunnerEventStarted,
			},
			changed: true,
		},
		{
			name: "started: completed status returns false",
			before: Task{
				Status: TaskStatusCompleted,
			},
			event: RunnerEvent{
				Event: RunnerEventStarted,
			},
			changed: false,
		},

		// Started events with start command
		{
			name: "started: pending with start -> running",
			before: Task{
				Status:  TaskStatusPending,
				Command: TaskCommandStart,
			},
			after: Task{
				Status: TaskStatusRunning,
			},
			event: RunnerEvent{
				Event: RunnerEventStarted,
			},
			changed: true,
		},
		{
			name: "started: running with start -> running",
			before: Task{
				Status:  TaskStatusRunning,
				Command: TaskCommandStart,
			},
			after: Task{
				Status: TaskStatusRunning,
			},
			event: RunnerEvent{
				Event: RunnerEventStarted,
			},
			changed: true,
		},

		// Stopped events
		{
			name: "stopped: running with no command -> completed",
			before: Task{
				Status: TaskStatusRunning,
			},
			after: Task{
				Status: TaskStatusCompleted,
			},
			event: RunnerEvent{
				Event: RunnerEventStopped,
			},
			changed: true,
		},
		{
			name: "stopped: running with stop command -> cancelled",
			before: Task{
				Status:  TaskStatusRunning,
				Command: TaskCommandStop,
			},
			after: Task{
				Status: TaskStatusCancelled,
			},
			event: RunnerEvent{
				Event: RunnerEventStopped,
			},
			changed: true,
		},
		{
			name: "stopped: cancelling with stop command -> cancelled",
			before: Task{
				Status:  TaskStatusCancelling,
				Command: TaskCommandStop,
			},
			after: Task{
				Status: TaskStatusCancelled,
			},
			event: RunnerEvent{
				Event: RunnerEventStopped,
			},
			changed: true,
		},
		{
			name: "stopped: running with restart command returns false",
			before: Task{
				Status:  TaskStatusRunning,
				Command: TaskCommandRestart,
			},
			event: RunnerEvent{
				Event: RunnerEventStopped,
			},
			changed: false,
		},
		{
			name: "stopped: running with start command -> pending, bumps to next run",
			before: Task{
				Status:  TaskStatusRunning,
				Command: TaskCommandStart,
				Version: 3,
			},
			after: Task{
				Status:  TaskStatusPending,
				Command: TaskCommandStart,
				Version: 4,
			},
			event: RunnerEvent{
				Event:   RunnerEventStopped,
				Version: 3,
			},
			changed: true,
		},
		{
			name: "stopped: pending status returns false",
			before: Task{
				Status: TaskStatusPending,
			},
			event: RunnerEvent{
				Event: RunnerEventStopped,
			},
			changed: false,
		},

		// Failed events
		{
			name: "failed: pending -> failed",
			before: Task{
				Status:  TaskStatusPending,
				Command: TaskCommandRestart,
			},
			after: Task{
				Status: TaskStatusFailed,
			},
			event: RunnerEvent{
				Event: RunnerEventFailed,
			},
			changed: true,
		},
		{
			name: "failed: restarting -> failed",
			before: Task{
				Status:  TaskStatusRestarting,
				Command: TaskCommandRestart,
			},
			after: Task{
				Status: TaskStatusFailed,
			},
			event: RunnerEvent{
				Event: RunnerEventFailed,
			},
			changed: true,
		},
		{
			name: "failed: running -> failed",
			before: Task{
				Status: TaskStatusRunning,
			},
			after: Task{
				Status: TaskStatusFailed,
			},
			event: RunnerEvent{
				Event: RunnerEventFailed,
			},
			changed: true,
		},
		{
			name: "failed: running with stop command -> failed",
			before: Task{
				Status:  TaskStatusRunning,
				Command: TaskCommandStop,
			},
			after: Task{
				Status: TaskStatusFailed,
			},
			event: RunnerEvent{
				Event: RunnerEventFailed,
			},
			changed: true,
		},
		{
			name: "failed: cancelling -> failed",
			before: Task{
				Status:  TaskStatusCancelling,
				Command: TaskCommandStop,
			},
			after: Task{
				Status: TaskStatusFailed,
			},
			event: RunnerEvent{
				Event: RunnerEventFailed,
			},
			changed: true,
		},
		{
			name: "failed: already failed returns false",
			before: Task{
				Status: TaskStatusFailed,
			},
			event: RunnerEvent{
				Event: RunnerEventFailed,
			},
			changed: false,
		},
		{
			name: "failed: completed returns false",
			before: Task{
				Status: TaskStatusCompleted,
			},
			event: RunnerEvent{
				Event: RunnerEventFailed,
			},
			changed: false,
		},
		{
			name: "failed: cancelled returns false",
			before: Task{
				Status: TaskStatusCancelled,
			},
			event: RunnerEvent{
				Event: RunnerEventFailed,
			},
			changed: false,
		},

		// Unknown event type
		{
			name: "unknown event type returns false",
			before: Task{
				Status: TaskStatusRunning,
			},
			event: RunnerEvent{
				Event: RunnerEventType("unknown"),
			},
			changed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := tt.before
			got := task.ApplyRunnerEvent(&tt.event)
			assert.Equal(t, got, tt.changed)
			if tt.changed {
				assert.DeepEqual(t, task, tt.after)
			}
		})
	}
}

// The tests below walk multi-step sequences where the version acts as the run
// counter (proposals/draft/task-run-versions.md): the counter moves exactly at
// run boundaries and never under a live run unless a restart deliberately
// disowns it.

// Back-to-back wakes coalesce into one bump at the exit fold.
func TestTask_RunVersion_WakeCoalescing(t *testing.T) {
	t.Parallel()
	// Run 1 is live; three wakes arrive before the runner reacts.
	task := Task{Status: TaskStatusRunning, Version: 1}
	for range 3 {
		assert.Assert(t, task.Start())
	}
	// Each fold set command=start idempotently; the version stayed with run 1.
	assert.DeepEqual(t, task, Task{
		Status:  TaskStatusRunning,
		Command: TaskCommandStart,
		Version: 1,
	})
	// Run 1 exits: its stopped (scoped to 1) applies, folds Running+start ->
	// Pending, and bumps to run 2 with the command kept for the runner.
	assert.Assert(t, task.ApplyRunnerEvent(&RunnerEvent{Event: RunnerEventStopped, Version: 1}))
	assert.DeepEqual(t, task, Task{
		Status:  TaskStatusPending,
		Command: TaskCommandStart,
		Version: 2,
	})
	// Run 2 starts and sees all three instructions — one physical follow-up run.
	assert.Assert(t, task.ApplyRunnerEvent(&RunnerEvent{Event: RunnerEventStarted, Version: 2}))
	assert.DeepEqual(t, task, Task{Status: TaskStatusRunning, Version: 2})
}

// A versioned cancel round-trip lands the task in cancelled.
func TestTask_RunVersion_CancelRoundTrip(t *testing.T) {
	t.Parallel()
	// Run 3 is live; cancel does not bump, so the live driver's stopped
	// (scoped to 3) still matches and cancellation does not wedge.
	task := Task{Status: TaskStatusRunning, Version: 3}
	assert.Assert(t, task.Cancel())
	assert.DeepEqual(t, task, Task{
		Status:  TaskStatusCancelling,
		Command: TaskCommandStop,
		Version: 3,
	})
	assert.Assert(t, task.ApplyRunnerEvent(&RunnerEvent{Event: RunnerEventStopped, Version: 3}))
	assert.DeepEqual(t, task, Task{Status: TaskStatusCancelled, Version: 3})
}

// Restart disowns the killed run: its terminal event is stale, the new run's
// started consumes the restart command.
func TestTask_RunVersion_RestartDisown(t *testing.T) {
	t.Parallel()
	// Run 1 is live; restart provisions run 2 and disowns run 1.
	task := Task{Status: TaskStatusRunning, Version: 1}
	assert.Assert(t, task.Restart())
	assert.DeepEqual(t, task, Task{
		Status:  TaskStatusRestarting,
		Command: TaskCommandRestart,
		Version: 2,
	})
	// The killed run 1's terminal stopped is stale and rejected.
	assert.Assert(t, !task.ApplyRunnerEvent(&RunnerEvent{Event: RunnerEventStopped, Version: 1}))
	assert.DeepEqual(t, task, Task{
		Status:  TaskStatusRestarting,
		Command: TaskCommandRestart,
		Version: 2,
	})
	// Run 2's started consumes the restart command.
	assert.Assert(t, task.ApplyRunnerEvent(&RunnerEvent{Event: RunnerEventStarted, Version: 2}))
	assert.DeepEqual(t, task, Task{Status: TaskStatusRunning, Version: 2})
}

// Version 0 bypasses the run scope: a spontaneous failure from a dead run
// stamps version 0 and applies regardless of the task's current run.
func TestTask_RunVersion_ZeroBypasses(t *testing.T) {
	t.Parallel()
	task := Task{Status: TaskStatusRunning, Version: 7}
	assert.Assert(t, task.ApplyRunnerEvent(&RunnerEvent{Event: RunnerEventFailed, Version: 0}))
	assert.DeepEqual(t, task, Task{Status: TaskStatusFailed, Version: 7})
}

// An event scoped to a different run is rejected.
func TestTask_RunVersion_MismatchRejected(t *testing.T) {
	t.Parallel()
	task := Task{Status: TaskStatusRunning, Version: 7}
	assert.Assert(t, !task.ApplyRunnerEvent(&RunnerEvent{Event: RunnerEventStopped, Version: 6}))
	assert.DeepEqual(t, task, Task{Status: TaskStatusRunning, Version: 7})
}

func TestRunnerEvent_LifecycleEvent_FailedReason(t *testing.T) {
	t.Parallel()
	task := &Task{ID: 1, Status: TaskStatusFailed}

	tests := []struct {
		name   string
		reason string
		want   string
	}{
		{"empty falls back to constant", "", "container failed"},
		{"reason passed through as-is", "setup command 0 failed: exit status 1", "setup command 0 failed: exit status 1"},
		{"multiline reason preserved", "wrapper failed:\ncause here\nmore", "wrapper failed:\ncause here\nmore"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := RunnerEvent{Event: RunnerEventFailed, Reason: tt.reason}
			ev, ok := e.LifecycleEvent(task, TaskStatusRunning)
			assert.Assert(t, ok)
			lp, ok := ev.Payload.(*LifecyclePayload)
			assert.Assert(t, ok)
			assert.Equal(t, lp.Message, tt.want)
		})
	}
}

func TestTask_IsDone(t *testing.T) {
	tests := []struct {
		status TaskStatus
		want   bool
	}{
		{TaskStatusPending, false},
		{TaskStatusRunning, false},
		{TaskStatusRestarting, false},
		{TaskStatusCancelling, false},
		{TaskStatusCompleted, true},
		{TaskStatusFailed, true},
		{TaskStatusCancelled, true},
		{TaskStatusDraft, false},
	}
	for _, tt := range tests {
		t.Run(tt.status.String(), func(t *testing.T) {
			task := Task{Status: tt.status}
			assert.Equal(t, task.IsDone(), tt.want)
		})
	}
}

func TestTaskStatus_IsTerminal(t *testing.T) {
	tests := []struct {
		status TaskStatus
		want   bool
	}{
		{TaskStatusUnspecified, false},
		{TaskStatusPending, false},
		{TaskStatusRunning, false},
		{TaskStatusRestarting, false},
		{TaskStatusCancelling, false},
		{TaskStatusCompleted, true},
		{TaskStatusFailed, true},
		{TaskStatusCancelled, true},
		{TaskStatusDraft, false},
	}
	for _, tt := range tests {
		t.Run(tt.status.String(), func(t *testing.T) {
			assert.Equal(t, tt.status.IsTerminal(), tt.want)
		})
	}
}

func TestTask_Archive(t *testing.T) {
	tests := []struct {
		name   string
		before Task
		after  Task
		want   bool
	}{
		{
			name:   "from draft succeeds",
			before: Task{Status: TaskStatusDraft},
			after:  Task{Status: TaskStatusDraft, Archived: true},
			want:   true,
		},
		{
			name:   "from completed succeeds",
			before: Task{Status: TaskStatusCompleted},
			after:  Task{Status: TaskStatusCompleted, Archived: true},
			want:   true,
		},
		{
			name:   "from failed succeeds",
			before: Task{Status: TaskStatusFailed},
			after:  Task{Status: TaskStatusFailed, Archived: true},
			want:   true,
		},
		{
			name:   "from pending fails",
			before: Task{Status: TaskStatusPending},
			after:  Task{Status: TaskStatusPending},
			want:   false,
		},
		{
			name:   "from running fails",
			before: Task{Status: TaskStatusRunning},
			after:  Task{Status: TaskStatusRunning},
			want:   false,
		},
		{
			name:   "from restarting fails",
			before: Task{Status: TaskStatusRestarting},
			after:  Task{Status: TaskStatusRestarting},
			want:   false,
		},
		{
			name:   "from cancelling fails",
			before: Task{Status: TaskStatusCancelling},
			after:  Task{Status: TaskStatusCancelling},
			want:   false,
		},
		{
			name:   "from cancelled succeeds",
			before: Task{Status: TaskStatusCancelled},
			after:  Task{Status: TaskStatusCancelled, Archived: true},
			want:   true,
		},
		{
			name:   "already archived fails",
			before: Task{Status: TaskStatusCompleted, Archived: true},
			after:  Task{Status: TaskStatusCompleted, Archived: true},
			want:   false,
		},
		{
			name:   "with command fails",
			before: Task{Status: TaskStatusCompleted, Command: TaskCommandStart},
			after:  Task{Status: TaskStatusCompleted, Command: TaskCommandStart},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := tt.before
			assert.Equal(t, task.CanArchive(), tt.want)
			got := task.Archive()
			assert.Equal(t, got, tt.want)
			assert.DeepEqual(t, task, tt.after)
		})
	}
}

func TestTask_Cancel(t *testing.T) {
	tests := []struct {
		name   string
		before Task
		after  Task
		want   bool
	}{
		{
			name:   "from running succeeds without bumping (version stays with live run)",
			before: Task{Status: TaskStatusRunning, Version: 3},
			after:  Task{Status: TaskStatusCancelling, Command: TaskCommandStop, Version: 3},
			want:   true,
		},
		{
			name:   "from pending succeeds with cancelled status",
			before: Task{Status: TaskStatusPending},
			after:  Task{Status: TaskStatusCancelled},
			want:   true,
		},
		{
			name:   "from pending with command clears command",
			before: Task{Status: TaskStatusPending, Command: TaskCommandStart, Version: 1},
			after:  Task{Status: TaskStatusCancelled, Version: 1},
			want:   true,
		},
		{
			name:   "from completed fails",
			before: Task{Status: TaskStatusCompleted},
			after:  Task{Status: TaskStatusCompleted},
			want:   false,
		},
		{
			name:   "from failed fails",
			before: Task{Status: TaskStatusFailed},
			after:  Task{Status: TaskStatusFailed},
			want:   false,
		},
		{
			name:   "from restarting succeeds without bumping",
			before: Task{Status: TaskStatusRestarting, Version: 3},
			after:  Task{Status: TaskStatusCancelling, Command: TaskCommandStop, Version: 3},
			want:   true,
		},
		{
			name:   "from cancelling fails",
			before: Task{Status: TaskStatusCancelling},
			after:  Task{Status: TaskStatusCancelling},
			want:   false,
		},
		{
			name:   "from cancelled fails",
			before: Task{Status: TaskStatusCancelled},
			after:  Task{Status: TaskStatusCancelled},
			want:   false,
		},
		{
			name:   "from draft fails",
			before: Task{Status: TaskStatusDraft},
			after:  Task{Status: TaskStatusDraft},
			want:   false,
		},
		{
			name:   "archived fails",
			before: Task{Status: TaskStatusRunning, Archived: true},
			after:  Task{Status: TaskStatusRunning, Archived: true},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := tt.before
			assert.Equal(t, task.CanCancel(), tt.want)
			got := task.Cancel()
			assert.Equal(t, got, tt.want)
			assert.DeepEqual(t, task, tt.after)
		})
	}
}

func TestTask_Restart(t *testing.T) {
	tests := []struct {
		name   string
		before Task
		after  Task
		want   bool
	}{
		{
			name:   "from running succeeds",
			before: Task{Status: TaskStatusRunning},
			after:  Task{Status: TaskStatusRestarting, Command: TaskCommandRestart, Version: 1},
			want:   true,
		},
		{
			name:   "from completed succeeds",
			before: Task{Status: TaskStatusCompleted},
			after:  Task{Status: TaskStatusPending, Command: TaskCommandRestart, Version: 1},
			want:   true,
		},
		{
			name:   "from failed succeeds",
			before: Task{Status: TaskStatusFailed},
			after:  Task{Status: TaskStatusPending, Command: TaskCommandRestart, Version: 1},
			want:   true,
		},
		{
			name:   "from pending fails",
			before: Task{Status: TaskStatusPending},
			after:  Task{Status: TaskStatusPending},
			want:   false,
		},
		{
			name:   "from restarting fails",
			before: Task{Status: TaskStatusRestarting},
			after:  Task{Status: TaskStatusRestarting},
			want:   false,
		},
		{
			name:   "from cancelling fails",
			before: Task{Status: TaskStatusCancelling},
			after:  Task{Status: TaskStatusCancelling},
			want:   false,
		},
		{
			name:   "from cancelled succeeds",
			before: Task{Status: TaskStatusCancelled},
			after:  Task{Status: TaskStatusPending, Command: TaskCommandRestart, Version: 1},
			want:   true,
		},
		{
			name:   "archived fails",
			before: Task{Status: TaskStatusCompleted, Archived: true},
			after:  Task{Status: TaskStatusCompleted, Archived: true},
			want:   false,
		},
		{
			name:   "from draft fails",
			before: Task{Status: TaskStatusDraft},
			after:  Task{Status: TaskStatusDraft},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := tt.before
			assert.Equal(t, task.CanRestart(), tt.want)
			got := task.Restart()
			assert.Equal(t, got, tt.want)
			assert.DeepEqual(t, task, tt.after)
		})
	}
}

func TestTask_Start(t *testing.T) {
	tests := []struct {
		name   string
		before Task
		after  Task
		want   bool
	}{
		{
			name:   "from running succeeds without changing status or version",
			before: Task{Status: TaskStatusRunning, Version: 3},
			after:  Task{Status: TaskStatusRunning, Command: TaskCommandStart, Version: 3},
			want:   true,
		},
		{
			name:   "from completed succeeds",
			before: Task{Status: TaskStatusCompleted},
			after:  Task{Status: TaskStatusPending, Command: TaskCommandStart, Version: 1},
			want:   true,
		},
		{
			name:   "from failed succeeds",
			before: Task{Status: TaskStatusFailed},
			after:  Task{Status: TaskStatusPending, Command: TaskCommandStart, Version: 1},
			want:   true,
		},
		{
			name:   "from pending fails",
			before: Task{Status: TaskStatusPending},
			after:  Task{Status: TaskStatusPending},
			want:   false,
		},
		{
			name:   "from restarting fails",
			before: Task{Status: TaskStatusRestarting},
			after:  Task{Status: TaskStatusRestarting},
			want:   false,
		},
		{
			name:   "from cancelling fails",
			before: Task{Status: TaskStatusCancelling},
			after:  Task{Status: TaskStatusCancelling},
			want:   false,
		},
		{
			name:   "from cancelled succeeds",
			before: Task{Status: TaskStatusCancelled},
			after:  Task{Status: TaskStatusPending, Command: TaskCommandStart, Version: 1},
			want:   true,
		},
		{
			name:   "from draft succeeds and provisions run 1",
			before: Task{Status: TaskStatusDraft, Version: 0},
			after:  Task{Status: TaskStatusPending, Command: TaskCommandStart, Version: 1},
			want:   true,
		},
		{
			name:   "archived draft fails",
			before: Task{Status: TaskStatusDraft, Archived: true},
			after:  Task{Status: TaskStatusDraft, Archived: true},
			want:   false,
		},
		{
			name:   "archived fails",
			before: Task{Status: TaskStatusCompleted, Archived: true},
			after:  Task{Status: TaskStatusCompleted, Archived: true},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := tt.before
			assert.Equal(t, task.CanStart(), tt.want)
			got := task.Start()
			assert.Equal(t, got, tt.want)
			assert.DeepEqual(t, task, tt.after)
		})
	}
}
