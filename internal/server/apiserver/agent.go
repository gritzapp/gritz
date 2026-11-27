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
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/taskvar"
)

// Agents are org configuration: reading one needs OpOrgRead and changing one
// OpOrgWrite.

func (s *Server) CreateAgent(ctx context.Context, req *gritzv1.CreateAgentRequest) (*gritzv1.CreateAgentResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write org"))
	}
	agent := model.AgentFromProto(caller.OrgID, req.GetAgent())
	agent.ID = 0
	now := time.Now()
	for _, sched := range agent.Schedules {
		sched.ID = 0
	}
	err := s.store.WithTx(ctx, nil, func(tx *sql.Tx) error {
		if err := s.validateAgent(ctx, tx, agent); err != nil {
			return err
		}
		for _, sched := range agent.Schedules {
			// Validated above, so Next can't fail.
			if err := sched.Reschedule(nil, now); err != nil {
				return err
			}
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
	s.publish(model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "created", Type: "agent", ID: agent.ID}},
		OrgID:     caller.OrgID,
		UserID:    caller.ID,
		ClientID:  caller.ClientID,
		Time:      time.Now(),
	})
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
	agents, err := s.store.ListAgents(ctx, nil, caller.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &gritzv1.ListAgentsResponse{Agents: model.ProtoMap(agents)}, nil
}

// UpdateAgent replaces the agent wholesale: the request carries its full
// desired state, validated as on create. Its schedules are matched to the
// stored ones by ID, keeping their run state; see Schedule.Reschedule.
func (s *Server) UpdateAgent(ctx context.Context, req *gritzv1.UpdateAgentRequest) (*gritzv1.UpdateAgentResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write org"))
	}
	agent := model.AgentFromProto(caller.OrgID, req.GetAgent())
	now := time.Now()
	err := s.store.WithTx(ctx, nil, func(tx *sql.Tx) error {
		if err := s.validateAgent(ctx, tx, agent); err != nil {
			return err
		}
		// The schedules' locks before the agent's, as the scheduler takes
		// them: a save waits for a fire in flight, and a tick skips the
		// schedules until the save commits.
		stored, err := s.store.LockAgentSchedules(ctx, tx, agent.OrgID, agent.ID)
		if err != nil {
			return err
		}
		seen := make(map[int64]bool, len(agent.Schedules))
		for _, sched := range agent.Schedules {
			var prev *model.Schedule
			if sched.ID != 0 {
				if prev = stored[sched.ID]; prev == nil {
					return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("schedule %d is not a schedule of agent %d", sched.ID, agent.ID))
				}
				if seen[sched.ID] {
					return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("schedule %d is listed twice", sched.ID))
				}
				seen[sched.ID] = true
			}
			// Validated above, so Next can't fail.
			if err := sched.Reschedule(prev, now); err != nil {
				return err
			}
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
	s.publish(model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "updated", Type: "agent", ID: agent.ID}},
		OrgID:     caller.OrgID,
		UserID:    caller.ID,
		ClientID:  caller.ClientID,
		Time:      time.Now(),
	})
	return &gritzv1.UpdateAgentResponse{Agent: agent.Proto()}, nil
}

func (s *Server) DeleteAgent(ctx context.Context, req *gritzv1.DeleteAgentRequest) (*gritzv1.DeleteAgentResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write org"))
	}
	err := s.store.WithTx(ctx, nil, func(tx *sql.Tx) error {
		if err := s.store.DeleteAgent(ctx, tx, caller.OrgID, req.Id); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("agent %d not found", req.Id))
		}
		if errors.Is(err, store.ErrAgentInUse) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("agent %d has unarchived tasks: archive them first", req.Id))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.log.InfoContext(ctx, "agent deleted", "id", req.Id)
	s.publish(model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "deleted", Type: "agent", ID: req.Id}},
		OrgID:     caller.OrgID,
		UserID:    caller.ID,
		ClientID:  caller.ClientID,
		Time:      time.Now(),
	})
	return &gritzv1.DeleteAgentResponse{}, nil
}

// validateAgent checks an agent before it is saved in tx, returning an
// InvalidArgument error when it is rejected: against the built-in and the
// org's custom event types, its template: a name, a workspace registered on
// its runner, and variables the workspace accepts, and its schedules' cron
// expressions and timezones.
func (s *Server) validateAgent(ctx context.Context, tx *sql.Tx, agent *model.Agent) error {
	if agent.Name == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("name is required"))
	}
	for i, sched := range agent.Schedules {
		if err := sched.Validate(); err != nil {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("schedule %d: %w", i+1, err))
		}
	}
	custom, err := s.store.GetOrgCustomEventTypes(ctx, tx, agent.OrgID)
	if err != nil {
		return err
	}
	if err := eventrouter.DefaultSchemaRegistry.With(custom...).ValidateAgent(agent); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	ws, err := s.store.GetWorkspace(ctx, tx, agent.Runner, agent.Workspace, agent.OrgID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workspace %q not found on runner %q", agent.Workspace, agent.Runner))
		}
		return err
	}
	if _, err := taskvar.Resolve(ws.Variables, agent.Variables); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workspace %q: %w", agent.Workspace, err))
	}
	if err := ws.CheckFeatures(agent.Features); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workspace %q: %w", agent.Workspace, err))
	}
	return nil
}
