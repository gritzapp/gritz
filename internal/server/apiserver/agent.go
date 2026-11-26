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
	"github.com/gritzapp/gritz/internal/server/eventrouter"
	"github.com/gritzapp/gritz/internal/taskvar"
)

// Agents are org configuration, like the routing rules they replace: reading
// one needs OpOrgRead and changing one OpOrgWrite.

func (s *Server) CreateAgent(ctx context.Context, req *gritzv1.CreateAgentRequest) (*gritzv1.CreateAgentResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write org"))
	}
	agent := model.AgentFromProto(caller.OrgID, req.GetAgent())
	agent.ID = 0
	err := s.store.WithTx(ctx, nil, func(tx *sql.Tx) error {
		if err := s.validateAgent(ctx, tx, agent); err != nil {
			return err
		}
		if err := s.store.CreateAgent(ctx, tx, agent); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.InfoContext(ctx, "agent created", "id", agent.ID, "name", agent.Name)
	s.publishAgentChange(caller, "created", agent.ID)
	return &gritzv1.CreateAgentResponse{Agent: agent.Proto()}, nil
}

func (s *Server) GetAgent(ctx context.Context, req *gritzv1.GetAgentRequest) (*gritzv1.GetAgentResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgRead) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot read org"))
	}
	agent, err := s.store.GetAgent(ctx, nil, caller.OrgID, req.Id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("agent %d not found", req.Id))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &gritzv1.GetAgentResponse{Agent: agent.Proto()}, nil
}

func (s *Server) ListAgents(ctx context.Context, req *gritzv1.ListAgentsRequest) (*gritzv1.ListAgentsResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgRead) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot read org"))
	}
	agents, err := s.store.ListAgentsForOrgs(ctx, nil, []int64{caller.OrgID})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &gritzv1.ListAgentsResponse{Agents: model.ProtoMap(agents)}, nil
}

// UpdateAgent replaces the agent wholesale: the request carries its full
// desired state, validated as on create.
func (s *Server) UpdateAgent(ctx context.Context, req *gritzv1.UpdateAgentRequest) (*gritzv1.UpdateAgentResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write org"))
	}
	agent := model.AgentFromProto(caller.OrgID, req.GetAgent())
	err := s.store.WithTx(ctx, nil, func(tx *sql.Tx) error {
		if err := s.validateAgent(ctx, tx, agent); err != nil {
			return err
		}
		if err := s.store.UpdateAgent(ctx, tx, agent); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("agent %d not found", agent.ID))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.InfoContext(ctx, "agent updated", "id", agent.ID, "name", agent.Name)
	s.publishAgentChange(caller, "updated", agent.ID)
	return &gritzv1.UpdateAgentResponse{Agent: agent.Proto()}, nil
}

func (s *Server) DeleteAgent(ctx context.Context, req *gritzv1.DeleteAgentRequest) (*gritzv1.DeleteAgentResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write org"))
	}
	if err := s.store.DeleteAgent(ctx, nil, caller.OrgID, req.Id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("agent %d not found", req.Id))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.InfoContext(ctx, "agent deleted", "id", req.Id)
	s.publishAgentChange(caller, "deleted", req.Id)
	return &gritzv1.DeleteAgentResponse{}, nil
}

// validateAgent checks an agent before it is saved in tx, returning an
// InvalidArgument error when it is rejected. It takes the org lock, so the
// agent is checked against custom event types a concurrent SetCustomEventTypes
// can't be changing, and checks the agent's template: a name, a workspace
// registered on its runner, and variables the workspace accepts.
func (s *Server) validateAgent(ctx context.Context, tx *sql.Tx, agent *model.Agent) error {
	if agent.Name == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("name is required"))
	}
	if err := s.store.LockOrg(ctx, tx, agent.OrgID); err != nil {
		return err
	}
	custom, err := s.store.GetOrgCustomEventTypes(ctx, tx, agent.OrgID)
	if err != nil {
		return err
	}
	if err := eventrouter.DefaultSchemaRegistry.With(custom...).ValidateAgent(agent); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	decls, err := s.store.GetWorkspaceVariables(ctx, tx, agent.Runner, agent.Workspace, agent.OrgID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workspace %q not found on runner %q", agent.Workspace, agent.Runner))
		}
		return err
	}
	if _, err := taskvar.Resolve(decls, agent.Variables); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workspace %q: %w", agent.Workspace, err))
	}
	return nil
}

// publishAgentChange refreshes the Web UI after an agent changes.
func (s *Server) publishAgentChange(caller *apiauth.UserInfo, action string, id int64) {
	s.publish(model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: action, Type: "agent", ID: id}},
		OrgID:     caller.OrgID,
		UserID:    caller.ID,
		ClientID:  caller.ClientID,
		Time:      time.Now(),
	})
}
