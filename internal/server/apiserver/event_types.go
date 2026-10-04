package apiserver

import (
	"context"
	"errors"
	"slices"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/server/eventrouter"
)

// GetEventTypes returns the eventrouter event-type registry so clients (the
// routing-rule editor UI) can discover the valid (source, type) event kinds and
// the attributes a rule may condition on for each. The registry is global —
// populated by the producer packages' init (githubserver, atlassianserver) — so
// the response is not org-scoped.
func (s *Server) GetEventTypes(ctx context.Context, req *gritzv1.GetEventTypesRequest) (*gritzv1.GetEventTypesResponse, error) {
	caller := apiauth.MustCaller(ctx)
	if !caller.Scopes.Allow(authscope.OpOrgRead) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot read org"))
	}
	defs := eventrouter.DefaultSchemaRegistry.EventTypes()
	pb := make([]*gritzv1.EventTypeDef, len(defs))
	for i, def := range defs {
		attrs := make([]*gritzv1.AttrDef, len(def.Attrs))
		for j, attr := range def.Attrs {
			attrs[j] = &gritzv1.AttrDef{
				Key:         attr.Key,
				Label:       attr.Label,
				Help:        attr.Help,
				Placeholder: attr.Placeholder,
			}
		}
		pb[i] = &gritzv1.EventTypeDef{
			Source: def.Source,
			Type:   def.Type,
			Label:  def.Label,
			Attrs:  attrs,
		}
	}
	return &gritzv1.GetEventTypesResponse{EventTypes: pb}, nil
}

// TestEvent feeds a hand-composed synthetic event into the real routing code
// scoped to the caller's org (see proposals/draft/test-event-injection.md). It
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
// The request is not validated against the schema registry: attrs pass through
// as-is, and an unknown source/type/attr simply yields no match rather than an
// error. For a dry run, task/link resolution is skipped — the report is derived
// from the matched rule alone; firing resolves links so it can wake existing
// subscribed tasks.
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

	// Compose the synthetic InputEvent. The "body" and "url" attrs are the derived
	// views over Data/URL (InputEvent.Attr); every other attr is wrapped into the
	// one-element slice the matcher expects. Details pass through verbatim — the
	// router does not interpret them and they never affect matching. Orgs is scoped
	// to the caller's org; Meta stays nil (only OnRouteOutcome reads it, which the
	// test path never invokes).
	input := eventrouter.InputEvent{
		Source:      req.Source,
		Type:        req.Type,
		Description: req.Description,
		UserID:      caller.ID,
		Orgs:        []int64{caller.OrgID},
		Attrs:       eventrouter.Attrs{},
		Details:     req.Details,
	}
	for key, value := range req.Attrs {
		switch key {
		case "body":
			input.Data = value
		case "url":
			input.URL = value
		default:
			input.Attrs[key] = []string{value}
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
