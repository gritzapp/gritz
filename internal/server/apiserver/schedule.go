package apiserver

import (
	"cmp"
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
	"github.com/gritzapp/gritz/internal/server/scheduler"
)

// Schedules are org-owned objects; permissions gate the API surface, not the
// firing (see proposals/accepted/scheduled-tasks.md §"API surface"). Creating or
// updating a schedule is a deferred CreateTask, so it demands the same
// task-create scope on the target (workspace, runner). Deleting or toggling one
// is a mutation, gated on task-write like every other mutation in the codebase
// (there is no task-delete tier — write is it). Listing and getting only require
// org membership, expressed as the task-read capability every member holds.

func (s *Server) CreateSchedule(ctx context.Context, req *gritzv1.CreateScheduleRequest) (*gritzv1.CreateScheduleResponse, error) {
	caller := apiauth.MustCaller(ctx)
	// No row exists yet, so authorize directly on the request's target — the
	// narrow create scope (workspace/runner) a privileged caller holds, exactly
	// as CreateTask does. A schedule is never archived, so the literal
	// task.archived:"false" satisfies the minted scope's archived predicate.
	if !caller.Scopes.Allow(authscope.OpTaskCreate,
		authscope.WithTaskWorkspace(req.Workspace),
		authscope.WithTaskRunner(req.Runner),
		authscope.WithTaskArchived(false),
	) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot create schedule"))
	}
	ok, err := s.store.HasWorkspace(ctx, nil, req.Runner, req.Workspace, caller.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace %q not found on runner %q", req.Workspace, req.Runner))
	}
	sched := &model.Schedule{
		OrgID:        caller.OrgID,
		CreatedBy:    caller.ID,
		Name:         req.Name,
		Workspace:    req.Workspace,
		Runner:       req.Runner,
		Namespace:    req.Namespace,
		Instructions: model.ScheduleInstructionsFromProto(req.Instructions),
		CronExpr:     req.CronExpr,
		Timezone:     cmp.Or(req.Timezone, "UTC"),
		Enabled:      req.Enabled,
	}
	if req.AutoArchive != nil {
		sched.AutoArchive = req.AutoArchive.AsDuration()
	}
	// Validate cron/timezone before the write so an invalid schedule can never be
	// stored; a parse error surfaces as InvalidArgument.
	if err := sched.Validate(); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	// An enabled schedule needs its first fire time; a disabled one stays out of
	// the claim query with next_run_at = NULL.
	if sched.Enabled {
		next, err := sched.Next(time.Now())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		sched.NextRunAt = &next
	}
	if err := s.store.CreateSchedule(ctx, nil, sched); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.InfoContext(ctx, "schedule created", "id", sched.ID, "runner", sched.Runner, "workspace", sched.Workspace)
	// A schedule change only refreshes the Web UI; it never wakes a runner (the
	// scheduler worker, L4, creates the tasks), so no Runner is set.
	s.publish(model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "created", Type: "schedule", ID: sched.ID}},
		OrgID:     caller.OrgID,
		UserID:    caller.ID,
		ClientID:  caller.ClientID,
		Time:      time.Now(),
	})
	return &gritzv1.CreateScheduleResponse{Schedule: sched.Proto()}, nil
}

func (s *Server) GetSchedule(ctx context.Context, req *gritzv1.GetScheduleRequest) (*gritzv1.GetScheduleResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpTaskRead) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot read schedule"))
	}
	sched, err := s.store.GetSchedule(ctx, nil, req.Id, caller.OrgID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("schedule %d not found", req.Id))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &gritzv1.GetScheduleResponse{Schedule: sched.Proto()}, nil
}

func (s *Server) ListSchedules(ctx context.Context, req *gritzv1.ListSchedulesRequest) (*gritzv1.ListSchedulesResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpTaskRead) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot list schedules"))
	}
	scheds, err := s.store.ListSchedules(ctx, nil, caller.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &gritzv1.ListSchedulesResponse{Schedules: model.ProtoMap(scheds)}, nil
}

// UpdateSchedule replaces the mutable template + spec fields wholesale (the
// edit form sends the full desired state); enable/disable stays in
// SetScheduleEnabled. Authorizing already forces the caller to supply the target
// (workspace, runner), so the request is inherently a full specification.
func (s *Server) UpdateSchedule(ctx context.Context, req *gritzv1.UpdateScheduleRequest) (*gritzv1.UpdateScheduleResponse, error) {
	caller := apiauth.MustCaller(ctx)
	// Updating retargets a deferred CreateTask, so authorize the create scope on
	// the new (workspace, runner) up front. This also denies an empty-scopes
	// caller before any store access, keeping the row read out of that path.
	if !caller.Scopes.Allow(authscope.OpTaskCreate,
		authscope.WithTaskWorkspace(req.Workspace),
		authscope.WithTaskRunner(req.Runner),
		authscope.WithTaskArchived(false),
	) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot update schedule"))
	}
	ok, err := s.store.HasWorkspace(ctx, nil, req.Runner, req.Workspace, caller.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace %q not found on runner %q", req.Workspace, req.Runner))
	}
	timezone := cmp.Or(req.Timezone, "UTC")
	var sched *model.Schedule
	err = s.store.WithTx(ctx, nil, func(tx *sql.Tx) error {
		existing, err := s.store.GetScheduleForUpdate(ctx, tx, req.Id, caller.OrgID)
		if err != nil {
			return err
		}
		existing.Name = req.Name
		existing.Workspace = req.Workspace
		existing.Runner = req.Runner
		existing.Namespace = req.Namespace
		existing.Instructions = model.ScheduleInstructionsFromProto(req.Instructions)
		existing.CronExpr = req.CronExpr
		existing.Timezone = timezone
		existing.AutoArchive = req.AutoArchive.AsDuration()
		if err := existing.Validate(); err != nil {
			return connect.NewError(connect.CodeInvalidArgument, err)
		}
		// An enabled schedule re-aligns to its cron grid from now on every update,
		// so a spec edit takes effect immediately; a disabled one keeps
		// next_run_at = NULL (enable/disable is SetScheduleEnabled's job).
		if existing.Enabled {
			next, err := existing.Next(time.Now())
			if err != nil {
				return connect.NewError(connect.CodeInvalidArgument, err)
			}
			existing.NextRunAt = &next
		}
		if err := s.store.UpdateSchedule(ctx, tx, existing); err != nil {
			return err
		}
		sched = existing
		return tx.Commit()
	})
	if err != nil {
		// The in-tx checks return typed connect errors; surface any of them as-is
		// rather than re-wrapping them as Internal.
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("schedule %d not found", req.Id))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.InfoContext(ctx, "schedule updated", "id", sched.ID, "runner", sched.Runner, "workspace", sched.Workspace)
	s.publish(model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "updated", Type: "schedule", ID: sched.ID}},
		OrgID:     caller.OrgID,
		UserID:    caller.ID,
		ClientID:  caller.ClientID,
		Time:      time.Now(),
	})
	return &gritzv1.UpdateScheduleResponse{Schedule: sched.Proto()}, nil
}

func (s *Server) DeleteSchedule(ctx context.Context, req *gritzv1.DeleteScheduleRequest) (*gritzv1.DeleteScheduleResponse, error) {
	caller := apiauth.MustCaller(ctx)
	// Deleting is a mutation, gated on task-write like ArchiveTask/DeleteEvent —
	// a read-only caller must not be able to remove an org schedule.
	if !caller.Scopes.Allow(authscope.OpTaskWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot delete schedule"))
	}
	// DeleteSchedule is org-scoped, so a missing (or other-org) id is a no-op,
	// matching DeleteEvent.
	if err := s.store.DeleteSchedule(ctx, nil, req.Id, caller.OrgID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.InfoContext(ctx, "schedule deleted", "id", req.Id)
	s.publish(model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "deleted", Type: "schedule", ID: req.Id}},
		OrgID:     caller.OrgID,
		UserID:    caller.ID,
		ClientID:  caller.ClientID,
		Time:      time.Now(),
	})
	return &gritzv1.DeleteScheduleResponse{}, nil
}

func (s *Server) SetScheduleEnabled(ctx context.Context, req *gritzv1.SetScheduleEnabledRequest) (*gritzv1.SetScheduleEnabledResponse, error) {
	caller := apiauth.MustCaller(ctx)
	// Toggling a schedule is a mutation — and enabling resumes firing on a target
	// the caller may not hold create scope for — so it is gated on task-write, the
	// mutation tier, not on read/membership.
	if !caller.Scopes.Allow(authscope.OpTaskWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot update schedule"))
	}
	var sched *model.Schedule
	err := s.store.WithTx(ctx, nil, func(tx *sql.Tx) error {
		existing, err := s.store.GetScheduleForUpdate(ctx, tx, req.Id, caller.OrgID)
		if err != nil {
			return err
		}
		existing.Enabled = req.Enabled
		if req.Enabled {
			// Enabling realigns to the grid from now; a stored cron/timezone that has
			// since become invalid surfaces as InvalidArgument.
			next, err := existing.Next(time.Now())
			if err != nil {
				return connect.NewError(connect.CodeInvalidArgument, err)
			}
			existing.NextRunAt = &next
		} else {
			// Disabling drops it from the claim query.
			existing.NextRunAt = nil
		}
		if err := s.store.UpdateSchedule(ctx, tx, existing); err != nil {
			return err
		}
		sched = existing
		return tx.Commit()
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("schedule %d not found", req.Id))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.InfoContext(ctx, "schedule enabled set", "id", sched.ID, "enabled", sched.Enabled)
	s.publish(model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "updated", Type: "schedule", ID: sched.ID}},
		OrgID:     caller.OrgID,
		UserID:    caller.ID,
		ClientID:  caller.ClientID,
		Time:      time.Now(),
	})
	return &gritzv1.SetScheduleEnabledResponse{Schedule: sched.Proto()}, nil
}

// RunSchedule fires a schedule immediately as a one-off, in addition to its cron
// cadence. It materializes the exact same task the scheduler worker would (via
// scheduler.Fire) and records NOTHING on the schedule row — next_run_at,
// last_run_at, and last_task_id stay untouched, so the extra run never disturbs
// the cadence. It works even on a disabled schedule (a disabled schedule only
// means "don't fire automatically"), which is the primary "test right after
// creating" use case.
func (s *Server) RunSchedule(ctx context.Context, req *gritzv1.RunScheduleRequest) (*gritzv1.RunScheduleResponse, error) {
	caller := apiauth.MustCaller(ctx)
	// Coarse, fail-fast capability gate (AllowOp ignores predicates) so an
	// empty-scopes caller is denied before any store access; the concrete
	// per-target check happens after the row is loaded, below.
	if !caller.Scopes.AllowOp(authscope.OpTaskCreate) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot run schedule"))
	}
	var (
		task *model.Task
		note model.Notification
	)
	err := s.store.WithTx(ctx, nil, func(tx *sql.Tx) error {
		// No FOR UPDATE: nothing is written back to the schedule row, so a plain
		// org-scoped read suffices.
		sched, err := s.store.GetSchedule(ctx, tx, req.Id, caller.OrgID)
		if err != nil {
			return err
		}
		// A manual run materializes a real task on the schedule's target, so it
		// demands the same task-create scope CreateSchedule/UpdateSchedule require —
		// stronger than the task-write tier used for enable/disable. The target is
		// read from the row, not the request, so it can't be spoofed.
		if !caller.Scopes.Allow(authscope.OpTaskCreate,
			authscope.WithTaskWorkspace(sched.Workspace),
			authscope.WithTaskRunner(sched.Runner),
			authscope.WithTaskArchived(false),
		) {
			return connect.NewError(connect.CodePermissionDenied, errors.New("cannot run schedule"))
		}
		// Same occurrence the scheduler worker fires — identical task and events —
		// and nothing else. The schedule row is untouched, so a manual run works
		// even on a disabled schedule or one whose cron/timezone later became invalid
		// (this path never computes Next()).
		task, note, err = scheduler.Fire(ctx, tx, s.store, sched)
		if err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("schedule %d not found", req.Id))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.InfoContext(ctx, "schedule run manually", "id", req.Id, "task", task.ID)
	// Publish the same task-created notification the scheduler and CreateTask emit,
	// AFTER commit, so the runner wake channel and Web UI never see a rolled-back
	// task. Fire built it with Runner set; stamp in the acting user for the SSE fan-out.
	note.UserID, note.ClientID = caller.ID, caller.ClientID
	s.publish(note)
	return &gritzv1.RunScheduleResponse{Task: task.Proto(s.baseURL)}, nil
}
