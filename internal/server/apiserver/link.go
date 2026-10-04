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

func (s *Server) CreateLink(ctx context.Context, req *gritzv1.CreateLinkRequest) (*gritzv1.CreateLinkResponse, error) {
	caller := apiauth.MustCaller(ctx)
	// Coarse, fail-fast capability gate before the DB read (AllowOp ignores
	// predicates); the instance check happens after the row is loaded.
	if !caller.Scopes.AllowOp(authscope.OpTaskWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write task"))
	}
	// Load the row (org-scoped for tenancy) so the instance check can see the
	// task's id/parent/archived attributes.
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
	link := &model.Link{
		TaskID:     req.TaskId,
		Relevance:  req.Relevance,
		URL:        req.Url,
		RoutingKey: model.RoutingKey(req.Url),
		Title:      req.Title,
		Subscribe:  req.Subscribe,
		CreatedAt:  time.Now(),
	}
	// task_links is the subscription/list projection; the link event is the
	// timeline source of truth. Upsert the projection and append the event in one
	// transaction so they can't drift. link_id points back at the task_links row.
	err = s.store.WithTx(ctx, nil, func(tx *sql.Tx) error {
		if err := s.store.CreateLink(ctx, tx, link); err != nil {
			return err
		}
		if err := s.store.CreateEvent(ctx, tx, &model.Event{
			TaskID:  task.ID,
			OrgID:   task.OrgID,
			Payload: link.EventPayload(),
		}); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.InfoContext(ctx, "link created", "relevance", req.Relevance, "url", req.Url)
	s.publish(model.Notification{
		Type: "change",
		Resources: []model.NotificationResource{
			{Action: "created", Type: "task_links", ID: req.TaskId},
			{Action: "created", Type: "link", ID: link.ID},
		},
		OrgID:    caller.OrgID,
		UserID:   caller.ID,
		ClientID: caller.ClientID,
		Time:     time.Now(),
	})
	return &gritzv1.CreateLinkResponse{
		Link: link.Proto(),
	}, nil
}

func (s *Server) ListLinks(ctx context.Context, req *gritzv1.ListLinksRequest) (*gritzv1.ListLinksResponse, error) {
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
	links, err := s.store.ListLinksByTask(ctx, nil, req.TaskId, caller.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &gritzv1.ListLinksResponse{
		Links: model.ProtoMap(links),
	}, nil
}
