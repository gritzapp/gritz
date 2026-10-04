package apiserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/shell"
)

// OpenShell opens an interactive debug shell into a finished task's sandbox. It
// mints a rendezvous session, records it on the task's shell_session field, and
// issues a start so the runner relaunches the sandbox against the preserved disk
// — the re-spawned driver reads shell_session and serves the shell instead of
// running the agent. The operator then connects to GET /shell/{session_id}/attach
// and the relay bridges the two.
//
// Authorization mirrors RestartTask: opening a root shell into a sandbox is at
// least as powerful as restarting it, so it is gated on task-write.
func (s *Server) OpenShell(ctx context.Context, req *gritzv1.OpenShellRequest) (*gritzv1.OpenShellResponse, error) {
	caller := apiauth.MustCaller(ctx)
	// Coarse, fail-fast capability gate before entering the transaction (AllowOp
	// ignores predicates); the per-instance check runs inside the tx against the
	// row it already loads.
	if !caller.Scopes.AllowOp(authscope.OpTaskWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write task"))
	}
	if s.shells == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("shell sessions are not configured"))
	}
	sessionID, err := shell.CreateSessionID()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	notification := model.Notification{
		Type:     "change",
		OrgID:    caller.OrgID,
		UserID:   caller.ID,
		ClientID: caller.ClientID,
		Time:     time.Now(),
	}
	err = s.store.WithTx(ctx, nil, func(tx *sql.Tx) error {
		task, err := s.store.GetTaskForUpdate(ctx, tx, req.TaskId, caller.OrgID)
		if err != nil {
			return err
		}
		if !caller.Scopes.Allow(authscope.OpTaskWrite, task.ScopeAttr()...) {
			return connect.NewError(connect.CodePermissionDenied, errors.New("cannot write task"))
		}
		// A debug shell is only for inspecting a finished task's preserved
		// filesystem — never for interrupting a live run. Require a terminal
		// status: opening a shell must not kill or displace a running sandbox
		// (which is what issuing a command to a non-terminal task would do).
		if !task.Status.IsTerminal() {
			return connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("cannot open shell for task with status %s: task must be in a terminal state", task.Status))
		}
		from := task.Status
		// Start relaunches the sandbox against the preserved disk. The task is
		// terminal, so this brings up a fresh (shell) run without killing
		// anything. Set shell_session in the same transaction so the field is
		// durable before the command becomes visible to the runner — the driver
		// reads it once.
		if !task.Start() {
			return connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("cannot open shell for task with status %s", task.Status))
		}
		task.ShellSession = sessionID
		if err := s.store.UpdateTask(ctx, tx, task); err != nil {
			return err
		}
		// Recorded as a restart in the timeline: bringing a finished task back
		// up is the same transition, and v1 deliberately leaves shell runs
		// indistinguishable from agent runs (see the design doc).
		if err := s.store.CreateEvent(ctx, tx, &model.Event{
			TaskID: task.ID,
			OrgID:  task.OrgID,
			Payload: &model.LifecyclePayload{
				Kind:       model.LifecycleKindRestarted,
				Actor:      model.UserActor(caller.AuditName()),
				FromStatus: from.Label(),
				ToStatus:   task.Status.Label(),
			},
		}); err != nil {
			return err
		}
		notification.Runner = task.PendingRunner()
		notification.Resources = []model.NotificationResource{
			{Action: "restarted", Type: "task", ID: task.ID},
			{Action: "appended", Type: "task_events", ID: task.ID},
		}
		notification.ChannelMessage = fmt.Sprintf("Shell session opened for task %d.", task.ID)
		return tx.Commit()
	})
	if err != nil {
		// The in-tx checks return typed connect errors; surface any of them
		// as-is rather than re-wrapping them as Internal.
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("task %d not found", req.TaskId))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// Register the rendezvous only after the task update commits, so a failed
	// transaction can't leave a session dangling. The sandbox takes seconds to
	// boot, so the session is ready well before the driver dials.
	if err := s.shells.Seed(sessionID, caller.OrgID, req.TaskId); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("register shell session: %w", err))
	}
	s.log.InfoContext(ctx, "shell session opened", "session", sessionID)
	s.publish(notification)
	return &gritzv1.OpenShellResponse{SessionId: sessionID}, nil
}
