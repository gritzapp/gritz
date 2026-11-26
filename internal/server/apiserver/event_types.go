package apiserver

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/server/eventrouter"
)

// GetEventTypes returns the event types the caller's org can route on, so
// clients (the trigger editor UI) can discover the valid (source, type)
// event kinds and the attributes a trigger may condition on for each: the global
// registry populated by the producer packages' init (githubserver,
// atlassianserver) plus the org's custom event types. It also returns the
// registered link types, which the event types and agent link kinds name.
func (s *Server) GetEventTypes(ctx context.Context, req *gritzv1.GetEventTypesRequest) (*gritzv1.GetEventTypesResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgRead) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot read org"))
	}
	custom, err := s.store.GetOrgCustomEventTypes(ctx, nil, caller.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	reg := eventrouter.DefaultSchemaRegistry.With(custom...)
	return &gritzv1.GetEventTypesResponse{
		EventTypes: model.ProtoMap(reg.EventTypes()),
		LinkTypes:  model.ProtoMap(reg.LinkTypes()),
	}, nil
}

// GetCustomEventTypes returns the caller's org's custom event types and the
// URL custom events are POSTed to.
func (s *Server) GetCustomEventTypes(ctx context.Context, req *gritzv1.GetCustomEventTypesRequest) (*gritzv1.GetCustomEventTypesResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgRead) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot read org"))
	}
	defs, err := s.store.GetOrgCustomEventTypes(ctx, nil, caller.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &gritzv1.GetCustomEventTypesResponse{EventTypes: model.ProtoMap(defs)}, nil
}

// SetCustomEventTypes replaces the caller's org's custom event types. Every
// agent of the org is re-validated against the new set under the org lock, so
// deleting a type, or removing an attr or link type that an agent still uses,
// is rejected rather than leaving a trigger that can never match.
func (s *Server) SetCustomEventTypes(ctx context.Context, req *gritzv1.SetCustomEventTypesRequest) (*gritzv1.SetCustomEventTypesResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write org"))
	}
	defs := make([]model.EventTypeDef, len(req.EventTypes))
	for i, pb := range req.EventTypes {
		defs[i] = model.EventTypeDefFromProto(pb)
	}
	if err := eventrouter.DefaultSchemaRegistry.ValidateCustomEventTypes(defs); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	err := s.store.WithTx(ctx, nil, func(tx *sql.Tx) error {
		if err := s.store.LockOrg(ctx, tx, caller.OrgID); err != nil {
			return err
		}
		reg := eventrouter.DefaultSchemaRegistry.With(defs...)
		agents, err := s.store.ListAgentsForOrgs(ctx, tx, []int64{caller.OrgID})
		if err != nil {
			return err
		}
		for _, agent := range agents {
			if err := reg.ValidateAgent(agent); err != nil {
				return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("agent %d (%s): %w", agent.ID, agent.Name, err))
			}
		}
		if err := s.store.SetOrgCustomEventTypes(ctx, tx, caller.OrgID, defs); err != nil {
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
	return &gritzv1.SetCustomEventTypesResponse{EventTypes: model.ProtoMap(defs)}, nil
}

// TestEvent feeds a hand-composed synthetic event into the real routing code
// scoped to the caller's org (see
// docs/src/content/docs/proposals/implemented/test-event-injection.md). It
// supports two modes:
//
//   - Dry run (fire=false, OpOrgRead): composes an InputEvent from the request,
//     runs the shared planner (Router.Plan), and reports, per agent, the
//     trigger that matched, whether it would create a task or yield, and the
//     tasks the event would be delivered to — without writing any events or
//     tasks rows.
//   - Fire (fire=true, OpOrgWrite): applies the plan for real via the shared
//     write path (Router.Apply), waking/creating real tasks and persisting real
//     events — no different from a webhook-born event (§3). GitHub reactions and
//     other outbound side effects are deliberately not fired: the injected router
//     carries no OnRouteOutcome (§5).
//
// Every event is validated against its type's schema, as the custom webhook
// validates a custom event, and rejected with InvalidArgument for an unknown
// source or type, a missing or non-absolute http(s) url, or an undeclared attr.
// An event of a built-in source routes as the caller; a custom event routes
// with no actor, as from the webhook, so only public triggers and link kinds
// admit it.
func (s *Server) TestEvent(ctx context.Context, req *gritzv1.TestEventRequest) (*gritzv1.TestEventResponse, error) {
	caller := apiauth.MustCaller(ctx)
	// Fire mutates (wakes/creates tasks), so it needs OpOrgWrite — the same scope
	// as editing agents; a dry run is a read-only simulation and needs only
	// OpOrgRead (§4).
	op := authscope.OpOrgRead
	if req.Fire {
		op = authscope.OpOrgWrite
	}
	if !caller.Scopes.Allow(op) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot access org"))
	}
	if s.router == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("event router not configured"))
	}

	attrs := eventrouter.Attrs{}
	for key, attr := range req.Attrs {
		attrs[key] = attr.GetValues()
	}
	custom, err := s.store.GetOrgCustomEventTypes(ctx, nil, caller.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	reg := eventrouter.DefaultSchemaRegistry.With(custom...)
	def, ok := reg.EventTypeFor(req.Source, req.Type)
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown event type %q/%q", req.Source, req.Type))
	}
	if err := def.ValidateEvent(req.Url, attrs); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Compose the synthetic InputEvent. Details pass through verbatim — the
	// router does not interpret them and they never affect matching. Orgs is
	// scoped to the caller's org; Meta stays nil (only OnRouteOutcome reads it,
	// which the test path never invokes).
	input := eventrouter.InputEvent{
		Source:      req.Source,
		Type:        req.Type,
		Description: cmp.Or(req.Description, def.Label),
		Data:        req.Body,
		URL:         req.Url,
		UserID:      caller.ID,
		Orgs:        []int64{caller.OrgID},
		Attrs:       attrs,
		Details:     req.Details,
	}
	if req.Source == eventrouter.SourceCustom {
		// A custom event has no actor, as from the webhook: only public triggers
		// and link kinds admit it.
		input.UserID = ""
	}

	// Plan is the shared read-only planner — running it (not a parallel
	// simulator) is what guarantees the dry-run report agrees with what firing
	// would do.
	plan, err := s.router.Plan(ctx, input)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Scope to the caller's org (§4): Plan evaluates every org the caller is a
	// member of, but a test event is explicitly about the one org the operator is
	// looking at, and firing must only touch that org's tasks.
	plan.Deliveries = slices.DeleteFunc(plan.Deliveries, func(d eventrouter.Delivery) bool {
		return d.OrgID != caller.OrgID
	})
	plan.Triggers = slices.DeleteFunc(plan.Triggers, func(m eventrouter.TriggerMatch) bool {
		return m.OrgID != caller.OrgID
	})

	// Fire: apply the scoped plan for real through the shared write path. The
	// injected router carries no OnRouteOutcome, so this wakes/creates tasks and
	// persists events (with the composed Details on each ExternalPayload) but
	// fires no GitHub reaction or other outbound side effect (§5). A fired event
	// is an ordinary event — nothing marks it as synthetic (§3). The outcome
	// carries the plan with the written rows' IDs set.
	if req.Fire {
		applied := &eventrouter.Plan{}
		for _, outcome := range s.router.Apply(ctx, input, plan) {
			applied.Deliveries = append(applied.Deliveries, outcome.Deliveries...)
			applied.Triggers = append(applied.Triggers, outcome.Triggers...)
		}
		plan = applied
	}

	agents, err := s.store.ListAgentsForOrgs(ctx, nil, []int64{caller.OrgID})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return &gritzv1.TestEventResponse{
		Fired:  req.Fire,
		Agents: testEventAgents(agents, plan),
	}, nil
}

// testEventAgents reports plan per agent, in the order of agents, leaving out
// the agents it neither delivers to nor has a trigger match of.
func testEventAgents(agents []*model.Agent, plan *eventrouter.Plan) []*gritzv1.TestEventAgent {
	var report []*gritzv1.TestEventAgent
	for _, agent := range agents {
		pb := &gritzv1.TestEventAgent{AgentId: agent.ID, TriggerIndex: -1}
		for _, m := range plan.Triggers {
			if m.Agent.ID == agent.ID {
				pb.TriggerIndex = int32(m.TriggerIndex)
				pb.YieldedTo = m.YieldedTo
				pb.CreatedTaskId = m.TaskID
				pb.CreatedEventId = m.EventID
			}
		}
		for _, d := range plan.Deliveries {
			if d.AgentID == agent.ID {
				pb.Deliveries = append(pb.Deliveries, &gritzv1.TestEventDelivery{
					TaskId:  d.TaskID,
					Kind:    d.Kind,
					EventId: d.EventID,
				})
			}
		}
		if pb.TriggerIndex >= 0 || len(pb.Deliveries) > 0 {
			report = append(report, pb)
		}
	}
	return report
}
