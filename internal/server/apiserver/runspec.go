package apiserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
)

// GetRunSpecInputs returns the task token and the task's variables for a run's
// spec. The token is minted by CreateTaskToken, which also checks that the task
// belongs to the caller's org.
func (s *Server) GetRunSpecInputs(ctx context.Context, req *gritzv1.GetRunSpecInputsRequest) (*gritzv1.GetRunSpecInputsResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpTaskTokenCreate) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot get run spec inputs"))
	}
	tokenResp, err := s.CreateTaskToken(ctx, &gritzv1.CreateTaskTokenRequest{
		TaskId:       req.TaskId,
		Capabilities: req.Capabilities,
	})
	if err != nil {
		return nil, err
	}
	task, err := s.store.GetTask(ctx, nil, req.TaskId, caller.OrgID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("task %d not found", req.TaskId))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &gritzv1.GetRunSpecInputsResponse{
		Token:     tokenResp.Token,
		Variables: task.Variables,
	}, nil
}
