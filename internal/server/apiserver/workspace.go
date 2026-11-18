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

func (s *Server) RegisterWorkspaces(ctx context.Context, req *gritzv1.RegisterWorkspacesRequest) (*gritzv1.RegisterWorkspacesResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpWorkspaceWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot register workspaces"))
	}
	// Resolving a task's variables relies on its workspace's declarations
	// being valid.
	for _, ws := range req.Workspaces {
		for _, d := range model.VariableDeclsFromProto(ws.Variables) {
			if err := d.Validate(); err != nil {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workspace %q: %w", ws.Name, err))
			}
		}
	}
	err := s.store.WithTx(ctx, nil, func(tx *sql.Tx) error {
		if err := s.store.DeleteWorkspacesByRunner(ctx, tx, req.RunnerId, caller.OrgID); err != nil {
			return err
		}
		for _, ws := range req.Workspaces {
			w := model.WorkspaceFromProto(ws)
			w.RunnerID = req.RunnerId
			w.OrgID = caller.OrgID
			if err := s.store.CreateWorkspace(ctx, tx, w); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.InfoContext(ctx, "workspaces registered", "runner_id", req.RunnerId, "count", len(req.Workspaces))
	s.publish(model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "registered", Type: "workspaces"}},
		OrgID:     caller.OrgID,
		UserID:    caller.ID,
		ClientID:  caller.ClientID,
		Time:      time.Now(),
	})
	return &gritzv1.RegisterWorkspacesResponse{}, nil
}

func (s *Server) ListWorkspaces(ctx context.Context, req *gritzv1.ListWorkspacesRequest) (*gritzv1.ListWorkspacesResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpWorkspaceRead) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot list workspaces"))
	}
	workspaces, err := s.store.ListWorkspaces(ctx, nil, caller.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &gritzv1.ListWorkspacesResponse{Workspaces: model.ProtoMap(workspaces)}, nil
}

func (s *Server) ClearWorkspaces(ctx context.Context, req *gritzv1.ClearWorkspacesRequest) (*gritzv1.ClearWorkspacesResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpWorkspaceWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot clear workspaces"))
	}
	if req.RunnerId != "" {
		if err := s.store.DeleteWorkspacesByRunner(ctx, nil, req.RunnerId, caller.OrgID); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		s.log.InfoContext(ctx, "workspaces cleared", "runner", req.RunnerId)
	} else {
		if err := s.store.ClearWorkspaces(ctx, nil, caller.OrgID); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		s.log.InfoContext(ctx, "workspaces cleared")
	}
	s.publish(model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "cleared", Type: "workspaces"}},
		OrgID:     caller.OrgID,
		UserID:    caller.ID,
		ClientID:  caller.ClientID,
		Time:      time.Now(),
	})
	return &gritzv1.ClearWorkspacesResponse{}, nil
}
