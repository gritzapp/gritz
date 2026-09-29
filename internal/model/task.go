package model

import (
	"cmp"
	"log/slog"
	"time"

	"github.com/icholy/gritz/internal/auth/authscope"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

//go:generate stringer -type=TaskStatus -trimprefix=TaskStatus

// TaskStatus represents the current state of a task.
type TaskStatus int32

const (
	TaskStatusUnspecified TaskStatus = TaskStatus(gritzv1.TaskStatus_UNSPECIFIED)
	TaskStatusPending     TaskStatus = TaskStatus(gritzv1.TaskStatus_PENDING)
	TaskStatusRunning     TaskStatus = TaskStatus(gritzv1.TaskStatus_RUNNING)
	TaskStatusRestarting  TaskStatus = TaskStatus(gritzv1.TaskStatus_RESTARTING)
	TaskStatusCancelling  TaskStatus = TaskStatus(gritzv1.TaskStatus_CANCELLING)
	TaskStatusCompleted   TaskStatus = TaskStatus(gritzv1.TaskStatus_COMPLETED)
	TaskStatusFailed      TaskStatus = TaskStatus(gritzv1.TaskStatus_FAILED)
	TaskStatusCancelled   TaskStatus = TaskStatus(gritzv1.TaskStatus_CANCELLED)
	TaskStatusDraft       TaskStatus = TaskStatus(gritzv1.TaskStatus_DRAFT)
)

// Label renders a TaskStatus for a lifecycle payload, mapping the zero
// (unspecified) status to the empty string — e.g. a freshly created task has no
// prior status.
func (s TaskStatus) Label() string {
	if s == TaskStatusUnspecified {
		return ""
	}
	return s.String()
}

// IsTerminal reports whether the status is a finished run state:
// completed, failed, or cancelled. Draft is not terminal — a draft has never
// run, so nothing has finished.
func (s TaskStatus) IsTerminal() bool {
	return s == TaskStatusCompleted ||
		s == TaskStatusFailed ||
		s == TaskStatusCancelled
}

//go:generate stringer -type=TaskCommand -trimprefix=TaskCommand

// TaskCommand represents a command to be executed by the runner.
type TaskCommand int32

const (
	TaskCommandNone    TaskCommand = TaskCommand(gritzv1.TaskCommand_NONE)
	TaskCommandRestart TaskCommand = TaskCommand(gritzv1.TaskCommand_RESTART)
	TaskCommandStop    TaskCommand = TaskCommand(gritzv1.TaskCommand_STOP)
	TaskCommandStart   TaskCommand = TaskCommand(gritzv1.TaskCommand_START)
)

// Task represents a task in the system. Instructions are no longer a Task field —
// they are instruction events in the task's stream (see InstructionPayload).
type Task struct {
	ID        int64       `json:"id"`
	Name      string      `json:"name"`
	Runner    string      `json:"runner"`
	Workspace string      `json:"workspace"`
	Status    TaskStatus  `json:"status"`
	Command   TaskCommand `json:"command"`
	Version   int64       `json:"version"`
	OrgID     int64       `json:"org_id"`
	Archived  bool        `json:"archived"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
	// Namespace partitions subscription matching in the event router. Empty is
	// the default namespace — the behavior every existing task already has. It is
	// set at creation and read-only thereafter (see
	// proposals/draft/task-namespaces.md).
	Namespace string `json:"namespace,omitempty"`
	// AutoArchive controls auto-archive after the task reaches a terminal
	// status. 0 = never (default); <0 = archive immediately; >0 = delay.
	AutoArchive time.Duration `json:"auto_archive,omitempty"`
	// ShellSession is the rendezvous session id for a driver reverse-shell run.
	// Non-empty => this sandbox run is a shell for that session; empty => normal
	// agent run. Persistent and server-owned (see
	// proposals/draft/driver-reverse-shell.md). Inert for now — no consumers yet.
	ShellSession string `json:"shell_session,omitempty"`
}

// Clone returns an independent copy of the task. Every field is value-typed,
// so a shallow struct copy is a complete snapshot — callers can retain it
// across in-place mutations (e.g. the ApplyRunnerEvent fold).
func (t *Task) Clone() *Task {
	c := *t
	return &c
}

// LogValue implements slog.LogValuer, rendering the task's core state
// (id, status, command, version) as a grouped attribute. This lets a task be
// logged as a single structured value — e.g. "original"/"updated" snapshots
// around the ApplyRunnerEvent fold, where the pair reads as a transition.
func (t *Task) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int64("id", t.ID),
		slog.Any("status", t.Status),
		slog.Any("command", t.Command),
		slog.Int64("version", t.Version),
	)
}

// ScopeAttr returns the authscope attributes describing this task, for the
// post-load Allow check in the apiserver task handlers. Centralizing the set
// here keeps a handler from forgetting one (especially task.archived): own-task
// matching rides on task.id and archive-based revocation on task.archived. See
// proposals/implemented/eliminate-runner-socket-proxy.md §5.
func (t *Task) ScopeAttr() []authscope.Attr {
	return []authscope.Attr{
		authscope.WithTaskID(t.ID),
		authscope.WithTaskArchived(t.Archived),
	}
}

// Proto converts a Task to its protobuf representation.
func (t *Task) Proto(baseURL string) *gritzv1.Task {
	return &gritzv1.Task{
		Id:           t.ID,
		Name:         t.Name,
		Runner:       t.Runner,
		Workspace:    t.Workspace,
		Status:       gritzv1.TaskStatus(t.Status),
		Command:      gritzv1.TaskCommand(t.Command),
		Version:      t.Version,
		Archived:     t.Archived,
		Url:          TaskURL(baseURL, t.ID, t.OrgID),
		CreatedAt:    timestamppb.New(t.CreatedAt),
		UpdatedAt:    timestamppb.New(t.UpdatedAt),
		AutoArchive:  durationpb.New(t.AutoArchive),
		ShellSession: t.ShellSession,
		Namespace:    t.Namespace,
		Actions: &gritzv1.TaskActions{
			Archive:   t.CanArchive(),
			Unarchive: t.CanUnarchive(),
			Cancel:    t.CanCancel(),
			Restart:   t.CanRestart(),
			Start:     t.CanStart(),
		},
	}
}

// TaskFromProto converts a protobuf Task to a model Task.
func TaskFromProto(pb *gritzv1.Task) *Task {
	var createdAt, updatedAt time.Time
	if pb.CreatedAt != nil {
		createdAt = pb.CreatedAt.AsTime()
	}
	if pb.UpdatedAt != nil {
		updatedAt = pb.UpdatedAt.AsTime()
	}
	return &Task{
		ID:           pb.Id,
		Name:         pb.Name,
		Runner:       pb.Runner,
		Workspace:    pb.Workspace,
		Status:       TaskStatus(pb.Status),
		Command:      TaskCommand(pb.Command),
		Version:      pb.Version,
		Archived:     pb.Archived,
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
		AutoArchive:  pb.AutoArchive.AsDuration(),
		ShellSession: pb.ShellSession,
		Namespace:    pb.Namespace,
	}
}

// RunnerEventType represents the type of event reported by the runner.
type RunnerEventType string

const (
	RunnerEventStarted RunnerEventType = "started"
	RunnerEventStopped RunnerEventType = "stopped"
	RunnerEventFailed  RunnerEventType = "failed"
)

// RunnerEvent represents an event from the runner about a task's container.
type RunnerEvent struct {
	TaskID    int64
	Event     RunnerEventType
	Version   int64
	Reconcile bool
	// Reason is an optional human-readable detail (e.g. the failure reason).
	// It is currently populated only for "failed" events and is empty otherwise.
	Reason string
}

// Proto converts a RunnerEvent to its protobuf representation.
func (r *RunnerEvent) Proto() *gritzv1.RunnerEvent {
	return &gritzv1.RunnerEvent{
		TaskId:    r.TaskID,
		Event:     string(r.Event),
		Version:   r.Version,
		Reconcile: r.Reconcile,
		Reason:    r.Reason,
	}
}

// RunnerEventFromProto converts a protobuf RunnerEvent to a model RunnerEvent.
func RunnerEventFromProto(pb *gritzv1.RunnerEvent) RunnerEvent {
	return RunnerEvent{
		TaskID:    pb.TaskId,
		Event:     RunnerEventType(pb.Event),
		Version:   pb.Version,
		Reconcile: pb.Reconcile,
		Reason:    pb.Reason,
	}
}

// ApplyRunnerEvent applies a runner event to the task, updating its status
// and command fields according to the state machine rules defined in RFC #149.
// Returns true if the task was updated, false otherwise.
func (t *Task) ApplyRunnerEvent(e *RunnerEvent) bool {
	// TODO: Reconciliation events require special handling
	if e.Reconcile {
		return false
	}

	// The version is the run counter (see proposals/draft/task-run-versions.md):
	// N >= 1 is scoped to run N, so the event applies only when it matches the
	// task's current run; 0 is the unscoped bypass, applying regardless of the
	// task's version. The guard logic is unchanged — only its meaning is now
	// normative.
	if e.Version != 0 && e.Version != t.Version {
		return false
	}

	switch e.Event {
	case RunnerEventStarted:
		return t.applyRunnerEventStarted()
	case RunnerEventStopped:
		return t.applyRunnerEventStopped()
	case RunnerEventFailed:
		return t.applyRunnerEventFailed()
	default:
		return false
	}
}

func (t *Task) applyRunnerEventStarted() bool {
	// If an archived task's container starts, cancel it
	if t.Archived {
		t.Status = TaskStatusCancelling
		t.Command = TaskCommandStop
		return true
	}
	switch t.Status {
	case TaskStatusPending, TaskStatusRestarting, TaskStatusRunning:
		if t.Command == TaskCommandRestart || t.Command == TaskCommandStart {
			t.Status = TaskStatusRunning
			t.Command = TaskCommandNone
			return true
		}
		return false
	case TaskStatusCancelled:
		t.Status = TaskStatusCancelling
		t.Command = TaskCommandStop
		return true
	default:
		return false
	}
}

func (t *Task) applyRunnerEventStopped() bool {
	switch t.Status {
	case TaskStatusRunning:
		if t.Command == TaskCommandStop {
			t.Status = TaskStatusCancelled
			t.Command = TaskCommandNone
			return true
		}
		if t.Command == TaskCommandStart {
			// Container finished, but start command pending. This is the run
			// boundary: run N's exit provisions run N+1. Go back to pending so
			// the runner picks it up and starts a new container, keep the start
			// command for it, and bump the version to N+1 (see
			// proposals/draft/task-run-versions.md).
			t.Status = TaskStatusPending
			t.Version++
			return true
		}
		if t.Command == TaskCommandNone {
			t.Status = TaskStatusCompleted
			return true
		}
		return false
	case TaskStatusCancelling:
		if t.Command == TaskCommandStop {
			t.Status = TaskStatusCancelled
			t.Command = TaskCommandNone
			return true
		}
		return false
	default:
		return false
	}
}

func (t *Task) applyRunnerEventFailed() bool {
	switch t.Status {
	case TaskStatusPending, TaskStatusRestarting, TaskStatusRunning, TaskStatusCancelling:
		t.Status = TaskStatusFailed
		t.Command = TaskCommandNone
		return true
	default:
		return false
	}
}

// LifecycleEvent maps the runner event to its sandbox lifecycle event. task
// carries the post-fold status and from is the status before the fold, so the
// payload records the transition (e.g. RUNNING -> COMPLETED). The container
// failure detail rides in the SANDBOX_FAILED message field (the old `error` log
// content). Returns false for runner events with no lifecycle home.
func (e RunnerEvent) LifecycleEvent(task *Task, from TaskStatus) (*Event, bool) {
	lifecycle := func(kind LifecycleKind, message string) *Event {
		return &Event{
			TaskID: task.ID,
			OrgID:  task.OrgID,
			Payload: &LifecyclePayload{
				Kind:       kind,
				Actor:      RunnerActor,
				FromStatus: from.Label(),
				ToStatus:   task.Status.Label(),
				Message:    message,
			},
		}
	}
	switch e.Event {
	case RunnerEventStarted:
		return lifecycle(LifecycleKindSandboxStarted, ""), true
	case RunnerEventStopped:
		return lifecycle(LifecycleKindSandboxExited, ""), true
	case RunnerEventFailed:
		// The reason is passed through as-is; fall back to the legacy constant
		// only when a producer left it empty (old runners/drivers).
		return lifecycle(LifecycleKindSandboxFailed, cmp.Or(e.Reason, "container failed")), true
	default:
		return nil, false
	}
}

// IsDone reports whether the task has finished its run: completed, failed,
// or cancelled. A done task can still be restarted via Start/Restart, so
// this isn't an absorbing state — just a snapshot that the current run
// reached an end.
func (t *Task) IsDone() bool {
	return t.Status.IsTerminal()
}

// CanArchive returns true if the task can be archived. A draft is archivable
// even though it is not done: it holds no sandbox, so archiving is how an
// abandoned one is disposed of.
func (t *Task) CanArchive() bool {
	if t.Archived || t.Command != TaskCommandNone {
		return false
	}
	return t.IsDone() || t.Status == TaskStatusDraft
}

// Archive marks the task as archived.
// Returns true if the transition is valid and was applied.
// Only valid from completed, failed, or cancelled status.
func (t *Task) Archive() bool {
	if !t.CanArchive() {
		return false
	}
	t.Archived = true
	return true
}

// CanUnarchive returns true if the task can be unarchived.
func (t *Task) CanUnarchive() bool {
	return t.Archived
}

// Unarchive marks the task as no longer archived. Also clears any
// auto_archive timeout (sets it to 0 = never) so the archiver worker doesn't
// immediately re-archive the task on its next tick.
// Returns true if the task was archived and is now unarchived.
func (t *Task) Unarchive() bool {
	if !t.CanUnarchive() {
		return false
	}
	t.Archived = false
	t.AutoArchive = 0
	return true
}

// CanCancel returns true if the task can be cancelled.
func (t *Task) CanCancel() bool {
	if t.Archived {
		return false
	}
	switch t.Status {
	case TaskStatusRunning, TaskStatusRestarting, TaskStatusPending:
		return true
	default:
		return false
	}
}

// Cancel transitions the task to cancelling/cancelled status and sets the stop command.
// Returns true if the transition is valid and was applied.
// For running or restarting tasks: sets status to cancelling, command to stop. The
// version stays with the live run so its SIGTERM-induced stopped (scoped to that
// version) still applies and lands the task in cancelled.
// For pending tasks: sets status to cancelled directly (no runner action needed).
func (t *Task) Cancel() bool {
	if !t.CanCancel() {
		return false
	}
	switch t.Status {
	case TaskStatusRunning, TaskStatusRestarting:
		t.Status = TaskStatusCancelling
		t.Command = TaskCommandStop
	case TaskStatusPending:
		t.Status = TaskStatusCancelled
		t.Command = TaskCommandNone
	}
	return true
}

// CanRestart returns true if the task can be restarted.
func (t *Task) CanRestart() bool {
	if t.Archived {
		return false
	}
	switch t.Status {
	case TaskStatusRunning, TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled:
		return true
	default:
		return false
	}
}

// Restart transitions the task to pending/restarting status and sets the restart command.
// Returns true if the transition is valid and was applied.
// For running tasks: sets status to restarting, command to restart, increments version.
// For completed, failed, or cancelled tasks: sets status to pending, command to restart, increments version.
func (t *Task) Restart() bool {
	if !t.CanRestart() {
		return false
	}
	switch t.Status {
	case TaskStatusRunning:
		t.Status = TaskStatusRestarting
	default:
		t.Status = TaskStatusPending
	}
	t.Command = TaskCommandRestart
	t.Version++
	return true
}

// CanStart returns true if the task can be started.
func (t *Task) CanStart() bool {
	if t.Archived {
		return false
	}
	switch t.Status {
	case TaskStatusRunning, TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled, TaskStatusDraft:
		return true
	default:
		return false
	}
}

// Start sets the start command without interrupting a running task.
// Returns true if the transition is valid and was applied.
// For running tasks: sets command to start (container continues, will restart
// after exit). Nothing is provisioned yet — the wake is queued and the version
// stays with the live run; the bump happens at the run boundary when the exit
// stopped folds Running+start back to Pending (see applyRunnerEventStopped).
// For draft, completed, failed, or cancelled tasks: sets status to pending,
// command to start, increments version — this provisions the next run now. A
// draft sits at version 0 ("never provisioned"), so its first run is run 1.
func (t *Task) Start() bool {
	if !t.CanStart() {
		return false
	}
	if t.Status != TaskStatusRunning {
		t.Status = TaskStatusPending
		t.Version++
	}
	t.Command = TaskCommandStart
	return true
}

// PendingRunner returns the runner that has pending work for this task, or ""
// if no runner action is needed. A runner has work when the task has a command
// and is not archived — the same condition as ListTasksForRunner.
func (t *Task) PendingRunner() string {
	if t.Command == TaskCommandNone || t.Archived {
		return ""
	}
	return t.Runner
}
