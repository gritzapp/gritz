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
)

func (s *Server) SubmitRunnerEvents(ctx context.Context, req *gritzv1.SubmitRunnerEventsRequest) (*gritzv1.SubmitRunnerEventsResponse, error) {
	caller := apiauth.MustCaller(ctx)
	// Coarse, fail-fast capability gate (AllowOp ignores predicates); each event
	// is authorized per-instance inside its transaction against the row it loads.
	if !caller.Scopes.AllowOp(authscope.OpTaskWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot submit runner events"))
	}
	// Authorize per-event: a partial-batch failure is acceptable (this RPC is
	// runner-facing, and coarse/admin callers pass every instance check anyway).
	for _, pbEvent := range req.Events {
		event := model.RunnerEventFromProto(pbEvent)
		notification := model.Notification{
			Type: "change",
			Resources: []model.NotificationResource{
				{Action: "updated", Type: "task", ID: event.TaskID},
				{Action: "appended", Type: "task_events", ID: event.TaskID},
			},
			OrgID:    caller.OrgID,
			UserID:   caller.ID,
			ClientID: caller.ClientID,
			Time:     time.Now(),
		}
		err := s.store.WithTx(ctx, nil, func(tx *sql.Tx) error {
			task, err := s.store.GetTaskForUpdate(ctx, tx, event.TaskID, caller.OrgID)
			if err != nil {
				return err
			}
			if !caller.Scopes.Allow(authscope.OpTaskWrite, task.ScopeAttr()...) {
				return connect.NewError(connect.CodePermissionDenied, errors.New("cannot submit runner events"))
			}
			// Snapshot the task before the fold: ApplyRunnerEvent mutates it in
			// place (it can bump the version and clear the command), so the
			// original and updated tasks are logged side by side to diagnose
			// stale-vs-state rejections and read applied events as a transition.
			original := task.Clone()
			applied := task.ApplyRunnerEvent(&event)
			s.log.InfoContext(ctx, "runner event received",
				"task_id", event.TaskID,
				"event", event.Event,
				"version", event.Version,
				"reason", event.Reason,
				"applied", applied,
				"original", original,
				"updated", task,
			)
			if !applied {
				notification.Ignore = true
				return nil
			}
			if err := s.store.UpdateTask(ctx, tx, task); err != nil {
				return err
			}
			// Append the sandbox lifecycle event beside the status fold (status is
			// the materialized projection; the fold logic is unchanged). original
			// carries the pre-fold status; the task carries the post-fold status.
			if ev, ok := event.LifecycleEvent(task, original.Status); ok {
				if err := s.store.CreateEvent(ctx, tx, ev); err != nil {
					return err
				}
			}
			notification.Runner = task.PendingRunner()
			// Only terminal statuses produce a channel message. Completed,
			// Failed, and Cancelled are unambiguous and agent-actionable; the
			// non-terminal runner transitions (running, restarting, pending
			// re-queue) don't carry enough context to say anything useful
			// without re-deriving why, so we stay silent and let the
			// eventual terminal event speak. The terminal status is also
			// stamped on the notification so subscribers like `gritz notify`
			// can filter to task outcomes that need attention.
			switch task.Status {
			case model.TaskStatusCompleted:
				notification.TaskStatus = task.Status
				notification.ChannelMessage = fmt.Sprintf("Task %d completed.", task.ID)
			case model.TaskStatusFailed:
				notification.TaskStatus = task.Status
				notification.ChannelMessage = fmt.Sprintf("Task %d failed.", task.ID)
			case model.TaskStatusCancelled:
				notification.TaskStatus = task.Status
				notification.ChannelMessage = fmt.Sprintf("Task %d cancelled.", task.ID)
			}
			return tx.Commit()
		})
		if err != nil {
			// The in-tx checks return typed connect errors; surface any of them
			// as-is rather than re-wrapping them as Internal.
			if connect.CodeOf(err) != connect.CodeUnknown {
				return nil, err
			}
			if errors.Is(err, sql.ErrNoRows) {
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("task %d not found", event.TaskID))
			}
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		s.publish(notification)
	}
	return &gritzv1.SubmitRunnerEventsResponse{}, nil
}
