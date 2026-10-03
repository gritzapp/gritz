package apiserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/icholy/gritz/internal/auth/apiauth"
	"github.com/icholy/gritz/internal/auth/authscope"
	"github.com/icholy/gritz/internal/model"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"github.com/icholy/gritz/internal/store"
	"github.com/icholy/gritz/internal/x/pagination"
)

// reportLogType is the legacy logs.type value the agent's report tool uploads.
// The UploadLogs handler re-points it onto the event stream as a report event;
// the wire (UploadLogs) is unchanged until the agent surface lands.
const reportLogType = "llm"

func (s *Server) UploadLogs(ctx context.Context, req *gritzv1.UploadLogsRequest) (*gritzv1.UploadLogsResponse, error) {
	caller := apiauth.MustCaller(ctx)
	// Coarse, fail-fast capability gate before the DB read (AllowOp ignores
	// predicates); the instance check happens after the row is loaded.
	if !caller.Scopes.AllowOp(authscope.OpTaskWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write task"))
	}
	task, err := s.store.GetTask(ctx, nil, req.TaskId, caller.OrgID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("task %d not found", req.TaskId))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !caller.Scopes.Allow(authscope.OpTaskWrite, task.ScopeAttr()...) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write task"))
	}
	for _, entry := range req.Entries {
		// The logs table is gone: the only log channel left on the wire is the
		// agent's report tool (`llm`), which becomes a from-agent `report` event.
		// Reports are from-agent, so they do not wake the task. Other log types no
		// longer have a writer (audit/info/error became lifecycle events, mcp
		// breadcrumbs were dropped), so any non-report entry is ignored.
		if entry.Type != reportLogType {
			continue
		}
		if err := s.store.CreateEvent(ctx, nil, &model.Event{
			TaskID:  task.ID,
			OrgID:   task.OrgID,
			Payload: &model.ReportPayload{Content: entry.Content},
		}); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	s.publish(model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "appended", Type: "task_events", ID: req.TaskId}},
		OrgID:     caller.OrgID,
		UserID:    caller.ID,
		ClientID:  caller.ClientID,
		Time:      time.Now(),
	})
	return &gritzv1.UploadLogsResponse{}, nil
}

// maxLogChunkSize caps a single AppendLogChunk payload. The driver cuts chunks
// at 32 KiB, so this is an abuse ceiling rather than a tuning knob.
const maxLogChunkSize = 1 << 20 // 1 MiB

// AppendLogChunk stores one opaque run of driver log bytes. Auth mirrors
// UploadLogs: the driver's narrow task JWT already carries OpTaskWrite bound to
// its task, so no scope changes are needed. Nothing is published — UploadLogs's
// notifications were already silenced as log spam
// (proposals/implemented/summary-gated-channel-notifications.md) and raw chunks
// are strictly noisier.
func (s *Server) AppendLogChunk(ctx context.Context, req *gritzv1.AppendLogChunkRequest) (*gritzv1.AppendLogChunkResponse, error) {
	caller := apiauth.MustCaller(ctx)
	// Coarse, fail-fast capability gate before the DB read (AllowOp ignores
	// predicates); the instance check happens after the row is loaded.
	if !caller.Scopes.AllowOp(authscope.OpTaskWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write task"))
	}
	if len(req.Data) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("data is required"))
	}
	if len(req.Data) > maxLogChunkSize {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("data must be at most %d bytes", maxLogChunkSize))
	}
	task, err := s.store.GetTask(ctx, nil, req.TaskId, caller.OrgID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("task %d not found", req.TaskId))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !caller.Scopes.Allow(authscope.OpTaskWrite, task.ScopeAttr()...) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write task"))
	}
	if err := s.store.CreateLogChunk(ctx, nil, &model.LogChunk{
		TaskID:  task.ID,
		OrgID:   task.OrgID,
		Version: req.Version,
		Data:    req.Data,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &gritzv1.AppendLogChunkResponse{}, nil
}

// ListLogChunksByTask returns a bidirectional keyset page of a task's log
// chunks, always oldest-first. An empty page token opens at the tail — the end
// of the log, which is what a post-mortem reader wants first.
func (s *Server) ListLogChunksByTask(ctx context.Context, req *gritzv1.ListLogChunksByTaskRequest) (*gritzv1.ListLogChunksByTaskResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.AllowOp(authscope.OpTaskRead) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot read task"))
	}
	// A blanket task.read (admin/coarse) is authorized without inspecting the row,
	// and the list query is already org-scoped. Only a predicated caller needs the
	// row loaded to check task.id/parent/archived.
	if !caller.Scopes.Allow(authscope.OpTaskRead) {
		task, err := s.store.GetTask(ctx, nil, req.TaskId, caller.OrgID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("task %d not found", req.TaskId))
			}
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if !caller.Scopes.Allow(authscope.OpTaskRead, task.ScopeAttr()...) {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot read task"))
		}
	}
	page, err := s.store.ListLogChunksByTaskPage(ctx, nil, store.ListLogChunksByTaskPageParams{
		TaskID:    req.TaskId,
		OrgID:     caller.OrgID,
		PageSize:  req.PageSize,
		PageToken: req.PageToken,
	})
	if err != nil {
		code := connect.CodeInternal
		if errors.Is(err, pagination.ErrInvalidRequest) {
			code = connect.CodeInvalidArgument
		}
		return nil, connect.NewError(code, err)
	}
	// The primary (forward) walk goes toward older rows, so the store's NextToken
	// is the transcript's "previous" (scroll-back) page; the reverse (backward)
	// walk is the newer/live-follow "next".
	return &gritzv1.ListLogChunksByTaskResponse{
		Chunks:        model.ProtoMap(page.Items),
		PrevPageToken: page.NextToken,
		NextPageToken: page.PrevToken,
		More:          page.More,
	}, nil
}
