package apiserver

import (
	"context"
	"testing"

	"github.com/gritzapp/gritz/internal/auth/apiauth"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/server/eventrouter"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"

	// Blank-imported so its init registers the eventrouter schemas TestEvent
	// routes against.
	_ "github.com/gritzapp/gritz/internal/server/githubserver"
)

// TestTestEventFireSkipsOutboundSideEffects asserts fire routes via Router.Apply,
// which never invokes OnRouteOutcome — so the GitHub reaction path (and any other
// outbound side effect wired through the callback) does not run even when the
// injected router carries one (§5).
func TestTestEventFireSkipsOutboundSideEffects(t *testing.T) {
	t.Parallel()
	st := teststore.New(t)
	var reacted bool
	router := &eventrouter.Router{
		Store:    st,
		Registry: eventrouter.DefaultSchemaRegistry,
		OnRouteOutcome: func(context.Context, eventrouter.RouteOutcome) {
			reacted = true
		},
	}
	srv := New(Options{Store: st, Router: router})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := apiauth.WithUser(t.Context(), &apiauth.UserInfo{
		ID:     org.UserID,
		OrgID:  org.OrgID,
		Scopes: authscope.Admin(),
	})
	teststore.CreateAgent(t, st, org, &model.Agent{
		Triggers: []model.AgentTrigger{{
			Source:     "github",
			Type:       "issue_comment",
			Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "gritz:"}},
		}},
	})

	resp, err := srv.TestEvent(ctx, &gritzv1.TestEventRequest{
		Source: "github",
		Type:   "issue_comment",
		Body:   "gritz: go",
		Url:    "https://github.com/o/r/issues/1",
		Fire:   true,
	})
	assert.NilError(t, err)

	// Apply ran (a task was created) but the reaction callback did not fire.
	assert.Assert(t, resp.Fired)
	assert.Assert(t, resp.Agents[0].CreatedTaskId != 0)
	assert.Assert(t, !reacted)
}
