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
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/x/pagination"
)

func (s *Server) ListExternalEvents(ctx context.Context, req *gritzv1.ListExternalEventsRequest) (*gritzv1.ListExternalEventsResponse, error) {
	const maxLimit = 100
	limit := cmp.Or(int(req.Limit), maxLimit)
	if limit < 0 || limit > maxLimit {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("limit must be at most %d", maxLimit))
	}
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpEventRead) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot list events"))
	}
	// The org feed is external-only; narrow the parameterized store query to the
	// external arm.
	events, err := s.store.ListEvents(ctx, nil, limit, caller.OrgID, []string{model.EventTypeExternal})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &gritzv1.ListExternalEventsResponse{
		Events: model.ProtoMap(events),
	}, nil
}

func (s *Server) GetEvent(ctx context.Context, req *gritzv1.GetEventRequest) (*gritzv1.GetEventResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpEventRead) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot read event"))
	}
	event, err := s.store.GetEvent(ctx, nil, req.Id, caller.OrgID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("event %d not found", req.Id))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &gritzv1.GetEventResponse{
		Event: event.Proto(),
	}, nil
}

func (s *Server) DeleteEvent(ctx context.Context, req *gritzv1.DeleteEventRequest) (*gritzv1.DeleteEventResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpEventWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write event"))
	}
	if err := s.store.DeleteEvent(ctx, nil, req.Id, caller.OrgID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.InfoContext(ctx, "event deleted", "id", req.Id)
	s.publish(model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "deleted", Type: "event", ID: req.Id}},
		OrgID:     caller.OrgID,
		UserID:    caller.ID,
		ClientID:  caller.ClientID,
		Time:      time.Now(),
	})
	return &gritzv1.DeleteEventResponse{}, nil
}

func (s *Server) ListEventsByTask(ctx context.Context, req *gritzv1.ListEventsByTaskRequest) (*gritzv1.ListEventsByTaskResponse, error) {
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
	// Legacy unpaged path: no pagination fields → all events, ascending. nil types
	// → all event types. The store returns ascending (chronological) stream order
	// (ORDER BY id), which is what the only order-sensitive consumer — the activity
	// timeline — wants, so pass it through as-is.
	if req.PageSize == 0 && req.PageToken == "" {
		events, err := s.store.ListEventsByTask(ctx, nil, req.TaskId, caller.OrgID, nil)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return &gritzv1.ListEventsByTaskResponse{
			Events: model.ProtoMap(events),
		}, nil
	}
	// Paged path: bidirectional keyset page (empty token → newest page).
	page, err := s.store.ListEventsByTaskPage(ctx, nil, store.ListEventsByTaskPageParams{
		TaskID:    req.TaskId,
		OrgID:     caller.OrgID,
		PageSize:  req.PageSize,
		PageToken: req.PageToken,
		Types:     req.Types,
	})
	if err != nil {
		code := connect.CodeInternal
		if errors.Is(err, pagination.ErrInvalidRequest) {
			code = connect.CodeInvalidArgument
		}
		return nil, connect.NewError(code, err)
	}
	// The primary (forward) walk goes toward older rows, so the store's NextToken
	// is the timeline's "previous" (scroll-back) page; the reverse (backward) walk
	// is the newer/live-follow "next".
	return &gritzv1.ListEventsByTaskResponse{
		Events:        model.ProtoMap(page.Items),
		PrevPageToken: page.NextToken,
		NextPageToken: page.PrevToken,
		More:          page.More,
	}, nil
}
