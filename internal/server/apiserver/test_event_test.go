package apiserver

import (
	"context"
	"testing"

	"github.com/icholy/gritz/internal/auth/apiauth"
	"github.com/icholy/gritz/internal/auth/authscope"
	"github.com/icholy/gritz/internal/model"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"github.com/icholy/gritz/internal/server/eventrouter"
	"github.com/icholy/gritz/internal/store/teststore"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"

	// Blank-imported so its init registers the eventrouter schemas TestEvent
	// routes against (see event_types_test.go).
	_ "github.com/icholy/gritz/internal/server/githubserver"
)

// TestTestEvent is a sanity check on the dry-run handler: it composes a
// synthetic event, runs the real matcher through the injected router, reports
// the matched rule straight through, and writes nothing.
func TestTestEvent(t *testing.T) {
	t.Parallel()
	st := teststore.New(t)
	srv := New(Options{Store: st, Router: &eventrouter.Router{Store: st}})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)
	err := srv.store.SetOrgRoutingRules(ctx, nil, org.OrgID, []model.RoutingRule{
		{Source: "github", Type: "issue_comment", Wakeup: true,
			Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "gritz:"}}},
	})
	assert.NilError(t, err)

	resp, err := srv.TestEvent(ctx, &gritzv1.TestEventRequest{
		Source: "github",
		Type:   "issue_comment",
		Attrs:  map[string]string{"body": "gritz: do it", "url": "https://github.com/o/r/issues/1"},
	})
	assert.NilError(t, err)

	// The matched rule is reported straight through, and the dry run never fires.
	assert.Assert(t, !resp.Fired)
	assert.DeepEqual(t, resp.Matches, []*gritzv1.TestEventMatch{
		{OrgId: org.OrgID, RuleIndex: 0, WouldWake: true},
	}, protocmp.Transform())

	// Dry run persists nothing.
	tasks, err := srv.store.ListTasks(ctx, nil, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(tasks), 0)
	events, err := srv.store.ListEvents(ctx, nil, 1000, org.OrgID, nil)
	assert.NilError(t, err)
	assert.Equal(t, len(events), 0)
}

// TestTestEventFireCreatesTask fires a synthetic event against a create rule and
// asserts it routes for real: a task and its external event row are written,
// the composed Details land on the persisted ExternalPayload, and the response
// reports the created task and event ids.
func TestTestEventFireCreatesTask(t *testing.T) {
	t.Parallel()
	st := teststore.New(t)
	srv := New(Options{Store: st, Router: &eventrouter.Router{Store: st}})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)
	err := srv.store.SetOrgRoutingRules(ctx, nil, org.OrgID, []model.RoutingRule{
		{Source: "github", Type: "issue_comment",
			Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "gritz:"}},
			Create:     &model.CreateTaskAction{Workspace: "default", Runner: "r", Prompt: "Do it."}},
	})
	assert.NilError(t, err)

	url := "https://github.com/o/r/issues/1"
	details := map[string]string{"path": "main.go", "line": "42"}
	resp, err := srv.TestEvent(ctx, &gritzv1.TestEventRequest{
		Source:      "github",
		Type:        "issue_comment",
		Description: "alice commented",
		Attrs:       map[string]string{"body": "gritz: do it", "url": url},
		Details:     details,
		Fire:        true,
	})
	assert.NilError(t, err)

	// The response reports the fire and the rows written: one created task, one
	// external event.
	assert.Assert(t, resp.Fired)
	assert.Equal(t, len(resp.Matches), 1)
	match := resp.Matches[0]
	assert.Equal(t, match.OrgId, org.OrgID)
	assert.Equal(t, match.WouldCreate, true)
	assert.Equal(t, len(match.CreatedTaskIds), 1)
	assert.Equal(t, len(match.EventIds), 1)

	// A real task row exists.
	task, err := srv.store.GetTask(ctx, nil, match.CreatedTaskIds[0], org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, task.Status, model.TaskStatusPending)

	// The reported external event carries the composed Details on its
	// ExternalPayload verbatim.
	event, err := srv.store.GetEvent(ctx, nil, match.EventIds[0], org.OrgID)
	assert.NilError(t, err)
	assert.DeepEqual(t, event.Payload, &model.ExternalPayload{
		Description: "alice commented",
		URL:         url,
		Data:        "gritz: do it",
		Details:     details,
		Source:      "github",
		EventType:   "issue_comment",
	})
}

// TestTestEventFireWakesSubscribedTask fires a synthetic event that matches a
// wake rule against an existing subscribed task and asserts the task is woken
// (restarted) with the event — carrying its Details — attached, rather than a
// new task created.
func TestTestEventFireWakesSubscribedTask(t *testing.T) {
	t.Parallel()
	st := teststore.New(t)
	srv := New(Options{Store: st, Router: &eventrouter.Router{Store: st}})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)
	url := "https://github.com/o/r/issues/1"
	task := teststore.CreateTask(t, srv.store, org, &teststore.TaskOptions{
		Status: model.TaskStatusCompleted,
		Links:  []teststore.LinkOptions{{URL: url, Subscribe: true}},
	})
	err := srv.store.SetOrgRoutingRules(ctx, nil, org.OrgID, []model.RoutingRule{
		{Source: "github", Type: "issue_comment", Wakeup: true,
			Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "gritz:"}}},
	})
	assert.NilError(t, err)

	details := map[string]string{"path": "main.go"}
	resp, err := srv.TestEvent(ctx, &gritzv1.TestEventRequest{
		Source:      "github",
		Type:        "issue_comment",
		Description: "follow-up comment",
		Attrs:       map[string]string{"body": "gritz: again", "url": url},
		Details:     details,
		Fire:        true,
	})
	assert.NilError(t, err)

	// The wake path attaches to the existing task: no task is created, one event
	// row is written.
	assert.Assert(t, resp.Fired)
	assert.Equal(t, len(resp.Matches), 1)
	match := resp.Matches[0]
	assert.Equal(t, match.WouldWake, true)
	assert.Equal(t, match.WouldCreate, false)
	assert.Equal(t, len(match.CreatedTaskIds), 0)
	assert.Equal(t, len(match.EventIds), 1)

	// No new task was created — the existing one is the only one, and it was
	// restarted.
	tasks, err := srv.store.ListTasks(ctx, nil, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(tasks), 1)
	updated, err := srv.store.GetTask(ctx, nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, updated.Status, model.TaskStatusPending)

	// The attached event carries the composed Details.
	event, err := srv.store.GetEvent(ctx, nil, match.EventIds[0], org.OrgID)
	assert.NilError(t, err)
	assert.DeepEqual(t, event.Payload, &model.ExternalPayload{
		Description: "follow-up comment",
		URL:         url,
		Data:        "gritz: again",
		Details:     details,
		Source:      "github",
		EventType:   "issue_comment",
	})
}

// TestTestEventFireSkipsOutboundSideEffects asserts fire routes via Router.Apply,
// which never invokes OnRouteOutcome — so the GitHub reaction path (and any other
// outbound side effect wired through the callback) does not run even when the
// injected router carries one (§5).
func TestTestEventFireSkipsOutboundSideEffects(t *testing.T) {
	t.Parallel()
	st := teststore.New(t)
	var reacted bool
	router := &eventrouter.Router{
		Store: st,
		OnRouteOutcome: func(context.Context, eventrouter.RouteOutcome) {
			reacted = true
		},
	}
	srv := New(Options{Store: st, Router: router})
	org := teststore.CreateOrg(t, srv.store, nil)
	ctx := createCtx(t, org)
	err := srv.store.SetOrgRoutingRules(ctx, nil, org.OrgID, []model.RoutingRule{
		{Source: "github", Type: "issue_comment",
			Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "gritz:"}},
			Create:     &model.CreateTaskAction{Workspace: "default", Runner: "r"}},
	})
	assert.NilError(t, err)

	resp, err := srv.TestEvent(ctx, &gritzv1.TestEventRequest{
		Source: "github",
		Type:   "issue_comment",
		Attrs:  map[string]string{"body": "gritz: go", "url": "https://github.com/o/r/issues/1"},
		Fire:   true,
	})
	assert.NilError(t, err)

	// Apply ran (a task was created) but the reaction callback did not fire.
	assert.Assert(t, resp.Fired)
	assert.Equal(t, len(resp.Matches[0].CreatedTaskIds), 1)
	assert.Assert(t, !reacted)
}

// TestTestEventFireRequiresOrgWrite asserts fire mode is gated on OpOrgWrite: a
// read-only caller can dry-run but not fire.
func TestTestEventFireRequiresOrgWrite(t *testing.T) {
	t.Parallel()
	st := teststore.New(t)
	srv := New(Options{Store: st, Router: &eventrouter.Router{Store: st}})
	org := teststore.CreateOrg(t, srv.store, nil)
	// A read-only caller: OpOrgRead only, no write.
	ctx := apiauth.WithUser(t.Context(), &apiauth.UserInfo{
		ID:     org.UserID,
		OrgID:  org.OrgID,
		Scopes: authscope.Scopes{authscope.New(authscope.OpOrgRead)},
	})

	// Dry run is allowed.
	_, err := srv.TestEvent(ctx, &gritzv1.TestEventRequest{
		Source: "github",
		Type:   "issue_comment",
		Attrs:  map[string]string{"body": "gritz: go", "url": "https://github.com/o/r/issues/1"},
	})
	assert.NilError(t, err)

	// Fire is denied.
	_, err = srv.TestEvent(ctx, &gritzv1.TestEventRequest{
		Source: "github",
		Type:   "issue_comment",
		Attrs:  map[string]string{"body": "gritz: go", "url": "https://github.com/o/r/issues/1"},
		Fire:   true,
	})
	assert.Assert(t, err != nil)
}
