package apiserver

import (
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
	"github.com/gritzapp/gritz/internal/server/customserver"
	"github.com/gritzapp/gritz/internal/server/eventrouter"
)

// GetEventTypes returns the event types the caller's org can route on, so
// clients (the routing-rule editor UI) can discover the valid (source, type)
// event kinds and the attributes a rule may condition on for each: the global
// registry populated by the producer packages' init (githubserver,
// atlassianserver) plus the org's custom event types.
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
// stored routing rule of the org is re-validated against the new set under the
// org lock, so deleting a type or removing an attr that a rule still uses is
// rejected rather than leaving a rule that can never match.
func (s *Server) SetCustomEventTypes(ctx context.Context, req *gritzv1.SetCustomEventTypesRequest) (*gritzv1.SetCustomEventTypesResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgWrite) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot write org"))
	}
	defs := make([]model.EventTypeDef, len(req.EventTypes))
	for i, pb := range req.EventTypes {
		defs[i] = model.EventTypeDefFromProto(pb)
	}
	if err := eventrouter.ValidateCustomEventTypes(defs); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	err := s.store.WithTx(ctx, nil, func(tx *sql.Tx) error {
		if err := s.store.LockOrg(ctx, tx, caller.OrgID); err != nil {
			return err
		}
		rules, err := s.store.GetOrgRoutingRules(ctx, tx, caller.OrgID)
		if err != nil {
			return err
		}
		reg := eventrouter.DefaultSchemaRegistry.With(defs...)
		for i, rule := range rules {
			if err := reg.Validate(rule); err != nil {
				return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("routing rule %d: %w", i+1, err))
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
//     runs the shared matcher (Router.Plan), and reports which rule matched and
//     whether it would wake or create — without writing any events or tasks rows.
//   - Fire (fire=true, OpOrgWrite): applies the plan for real via the shared
//     write path (Router.Apply), waking/creating real tasks and persisting real
//     events — no different from a webhook-born event (§3). GitHub reactions and
//     other outbound side effects are deliberately not fired: the injected router
//     carries no OnRouteOutcome (§5).
//
// An event of a built-in source is not validated against the schema registry:
// attrs pass through as-is, and an unknown type/attr simply yields no match
// rather than an error. It routes as the caller. A custom event is validated
// and composed by the custom webhook's own code, so it is rejected with
// InvalidArgument wherever the webhook would reject it, and it routes with no
// actor, so only public rules match it. For a dry run, task/link resolution is
// skipped — the report is derived from the matched rule alone; firing resolves
// links so it can wake existing subscribed tasks.
func (s *Server) TestEvent(ctx context.Context, req *gritzv1.TestEventRequest) (*gritzv1.TestEventResponse, error) {
	caller := apiauth.MustCaller(ctx)
	// Fire mutates (wakes/creates tasks), so it needs OpOrgWrite — the same scope
	// as editing rules; a dry run is a read-only simulation and needs only
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

	// Compose the synthetic InputEvent. Details pass through verbatim — the
	// router does not interpret them and they never affect matching. Orgs is
	// scoped to the caller's org; Meta stays nil (only OnRouteOutcome reads it,
	// which the test path never invokes).
	attrs := eventrouter.Attrs{}
	for key, attr := range req.Attrs {
		attrs[key] = attr.GetValues()
	}
	input := eventrouter.InputEvent{
		Source:      req.Source,
		Type:        req.Type,
		Description: req.Description,
		Data:        req.Body,
		URL:         req.Url,
		UserID:      caller.ID,
		Orgs:        []int64{caller.OrgID},
		Attrs:       attrs,
		Details:     req.Details,
	}
	if req.Source == eventrouter.SourceCustom {
		custom, err := s.store.GetOrgCustomEventTypes(ctx, nil, caller.OrgID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		reg := eventrouter.DefaultSchemaRegistry.With(custom...)
		input, err = customserver.ToInputEvent(customserver.Envelope{
			Type:        req.Type,
			Description: req.Description,
			Body:        req.Body,
			URL:         req.Url,
			Attrs:       attrs,
			Details:     req.Details,
		}, reg, caller.OrgID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}

	// Plan is the shared read-only matcher — running it (not a parallel simulator)
	// is what guarantees the dry-run report agrees with what firing would do.
	matches, err := s.router.Plan(ctx, input)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Scope to the caller's org (§4): Plan evaluates every org the caller is a
	// member of, but a test event is explicitly about the one org the operator is
	// looking at, and firing must only touch that org's tasks.
	matches = slices.DeleteFunc(matches, func(m eventrouter.RouteMatch) bool {
		return m.OrgID != caller.OrgID
	})

	resp := &gritzv1.TestEventResponse{
		Fired:   req.Fire,
		Matches: model.ProtoMap(matches),
	}
	if !req.Fire {
		return resp, nil
	}

	// Fire: apply the scoped matches for real through the shared write path. The
	// injected router carries no OnRouteOutcome, so this wakes/creates tasks and
	// persists events (with the composed Details on each ExternalPayload) but
	// fires no GitHub reaction or other outbound side effect (§5). A fired event
	// is an ordinary event — nothing marks it as synthetic (§3).
	outcomes, err := s.router.Apply(ctx, input, matches)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	for i, outcome := range outcomes {
		// outcomes align 1:1 with scoped/resp.Matches. Report the rows written:
		// created task ids (create path only) and every external event id (both
		// wake and create paths).
		if outcome.Created {
			resp.Matches[i].CreatedTaskIds = outcome.TaskIDs
		}
		resp.Matches[i].EventIds = outcome.EventIDs
	}

	return resp, nil
}
