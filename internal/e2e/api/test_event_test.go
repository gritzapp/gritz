package api_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/e2e"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/cmpx"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
)

// TestTestEvent is a sanity check on the dry-run handler: it composes a
// synthetic event, runs the real matcher, reports the matched rule straight
// through, and writes nothing.
func TestTestEvent(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	h.Org.SetRoutingRules(&gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{
			{
				Source: "github",
				Type:   "issue_comment",
				Wakeup: true,
				Conditions: []*gritzv1.RuleCondition{
					{Attr: "body", Op: "prefix", Value: "gritz:"},
				},
			},
		},
	})

	// Act
	resp, err := h.Org.Client.TestEvent(t.Context(), &gritzv1.TestEventRequest{
		Source: "github",
		Type:   "issue_comment",
		Attrs:  map[string]*gritzv1.AttrValues{"body": {Values: []string{"gritz: do it"}}, "url": {Values: []string{"https://github.com/o/r/issues/1"}}},
	})

	// Assert: the matched rule is reported straight through, and the dry run
	// never fires.
	assert.NilError(t, err)
	assert.DeepEqual(t,
		resp,
		&gritzv1.TestEventResponse{
			Matches: []*gritzv1.TestEventMatch{
				{OrgId: h.Org.ID, RuleIndex: 0, WouldWake: true},
			},
		},
		protocmp.Transform(),
	)

	// Assert: the dry run persists nothing.
	assert.Equal(t, len(h.Org.ListTasks(&gritzv1.ListTasksRequest{Archived: true})), 0)
	assert.Equal(t, len(h.Org.ListExternalEvents(&gritzv1.ListExternalEventsRequest{})), 0)
}

// TestTestEventFireCreatesTask fires a synthetic event against a create rule and
// asserts it routes for real: a task and its external event are written, the
// composed Details land on the persisted ExternalPayload, and the response
// reports the created task and event ids.
func TestTestEventFireCreatesTask(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	h.Org.SetRoutingRules(&gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{
			{
				Source: "github",
				Type:   "issue_comment",
				Conditions: []*gritzv1.RuleCondition{
					{Attr: "body", Op: "prefix", Value: "gritz:"},
				},
				Create: &gritzv1.CreateTaskAction{
					Workspace: e2e.DefaultWorkspace,
					Runner:    e2e.DefaultRunner,
					Prompt:    "Do it.",
				},
			},
		},
	})
	url := "https://github.com/o/r/issues/1"
	details := map[string]string{"path": "main.go", "line": "42"}

	// Act
	resp, err := h.Org.Client.TestEvent(t.Context(), &gritzv1.TestEventRequest{
		Source:      "github",
		Type:        "issue_comment",
		Description: "alice commented",
		Attrs:       map[string]*gritzv1.AttrValues{"body": {Values: []string{"gritz: do it"}}, "url": {Values: []string{url}}},
		Details:     details,
		Fire:        true,
	})

	assert.NilError(t, err)

	// Assert: a real task exists.
	tasks := h.Org.ListTasks(&gritzv1.ListTasksRequest{})
	assert.DeepEqual(t,
		tasks,
		[]*gritzv1.Task{{
			Id:      resp.Matches[0].CreatedTaskIds[0],
			Status:  gritzv1.TaskStatus_PENDING,
			Command: gritzv1.TaskCommand_START,
		}},
		protocmp.Transform(),
		cmpx.OnlyProtoFields(&gritzv1.Task{}, "id", "status", "command"),
	)

	// Assert: the external event carries the composed Details on its
	// ExternalPayload verbatim.
	events := h.Org.ListExternalEvents(&gritzv1.ListExternalEventsRequest{})
	assert.DeepEqual(t,
		events,
		[]*gritzv1.Event{{
			TaskId: tasks[0].Id,
			Payload: &gritzv1.Event_External{External: &gritzv1.ExternalPayload{
				Description: "alice commented",
				Url:         url,
				Data:        "gritz: do it",
				Details:     details,
				Source:      "github",
				Type:        "issue_comment",
			}},
		}},
		protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.Event{}, "id", "created_at"),
	)

	// Assert: the response reports the fire and the rows written: the created
	// task and the external event.
	assert.DeepEqual(t,
		resp,
		&gritzv1.TestEventResponse{
			Matches: []*gritzv1.TestEventMatch{{
				OrgId:          h.Org.ID,
				RuleIndex:      0,
				WouldCreate:    true,
				CreatedTaskIds: []int64{tasks[0].Id},
				EventIds:       []int64{events[0].Id},
			}},
			Fired: true,
		},
		protocmp.Transform(),
	)
}

// TestTestEventFireWakesSubscribedTask fires a synthetic event that matches a
// wake rule against an existing subscribed task and asserts the task is woken
// (restarted) with the event — carrying its Details — attached, rather than a
// new task created.
func TestTestEventFireWakesSubscribedTask(t *testing.T) {
	t.Parallel()
	// Arrange: a completed task subscribed to the issue.
	h := e2e.New(t, nil)
	url := "https://github.com/o/r/issues/1"
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{Draft: true})
	h.Org.CreateLink(&gritzv1.CreateLinkRequest{TaskId: task.ID, Url: url, Subscribe: true})
	h.Org.UpdateTask(&gritzv1.UpdateTaskRequest{Id: task.ID, Start: true})
	h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: task.ID, Event: string(model.RunnerEventStarted)},
			{TaskId: task.ID, Event: string(model.RunnerEventStopped)},
		},
	})
	assert.Equal(t, h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID}).Status, gritzv1.TaskStatus_COMPLETED)
	h.Org.SetRoutingRules(&gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{
			{
				Source: "github",
				Type:   "issue_comment",
				Wakeup: true,
				Conditions: []*gritzv1.RuleCondition{
					{Attr: "body", Op: "prefix", Value: "gritz:"},
				},
			},
		},
	})
	details := map[string]string{"path": "main.go"}

	// Act
	resp, err := h.Org.Client.TestEvent(t.Context(), &gritzv1.TestEventRequest{
		Source:      "github",
		Type:        "issue_comment",
		Description: "follow-up comment",
		Attrs:       map[string]*gritzv1.AttrValues{"body": {Values: []string{"gritz: again"}}, "url": {Values: []string{url}}},
		Details:     details,
		Fire:        true,
	})

	assert.NilError(t, err)

	// Assert: no new task was created — the existing one is the only one, and
	// it was restarted.
	assert.DeepEqual(t,
		h.Org.ListTasks(&gritzv1.ListTasksRequest{Archived: true}),
		[]*gritzv1.Task{{
			Id:      task.ID,
			Status:  gritzv1.TaskStatus_PENDING,
			Version: 2,
		}},
		protocmp.Transform(),
		cmpx.OnlyProtoFields(&gritzv1.Task{}, "id", "status", "version"),
	)

	// Assert: the attached event carries the composed Details.
	events := h.Org.ListExternalEvents(&gritzv1.ListExternalEventsRequest{})
	assert.DeepEqual(t,
		events,
		[]*gritzv1.Event{{
			TaskId: task.ID,
			Wake:   true,
			Payload: &gritzv1.Event_External{External: &gritzv1.ExternalPayload{
				Description: "follow-up comment",
				Url:         url,
				Data:        "gritz: again",
				Details:     details,
				Source:      "github",
				Type:        "issue_comment",
			}},
		}},
		protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.Event{}, "id", "created_at"),
	)

	// Assert: the wake path attaches to the existing task: no task is created,
	// one event is written.
	assert.DeepEqual(t,
		resp,
		&gritzv1.TestEventResponse{
			Matches: []*gritzv1.TestEventMatch{{
				OrgId:     h.Org.ID,
				RuleIndex: 0,
				WouldWake: true,
				EventIds:  []int64{events[0].Id},
			}},
			Fired: true,
		},
		protocmp.Transform(),
	)
}

// TestTestEventFireRequiresOrgWrite asserts fire mode is gated on OpOrgWrite: a
// read-only caller can dry-run but not fire.
func TestTestEventFireRequiresOrgWrite(t *testing.T) {
	t.Parallel()
	// Arrange: a read-only caller, OpOrgRead only, no write.
	h := e2e.New(t, nil)
	key := h.Org.CreateScopedKey(authscope.Scopes{
		authscope.New(
			authscope.OpOrgRead,
		),
	})
	req := &gritzv1.TestEventRequest{
		Source: "github",
		Type:   "issue_comment",
		Attrs:  map[string]*gritzv1.AttrValues{"body": {Values: []string{"gritz: go"}}, "url": {Values: []string{"https://github.com/o/r/issues/1"}}},
	}

	// Act & Assert - dry run is allowed.
	_, err := key.Client.TestEvent(t.Context(), req)
	assert.NilError(t, err)

	// Act & Assert - fire is denied.
	req.Fire = true
	_, err = key.Client.TestEvent(t.Context(), req)
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
}

// setProblemEventType declares the custom "problem" event type with a
// severity attr.
func setProblemEventType(t *testing.T, h *e2e.Harness) {
	t.Helper()
	_, err := h.Org.Client.SetCustomEventTypes(t.Context(), &gritzv1.SetCustomEventTypesRequest{
		EventTypes: []*gritzv1.EventTypeDef{
			{
				Source: "custom",
				Type:   "problem",
				Label:  "Dynatrace Problem",
				Attrs: []*gritzv1.AttrDef{
					{Key: "body"},
					{Key: "url"},
					{Key: "severity"},
				},
			},
		},
	})
	assert.NilError(t, err)
}

// TestTestEventFireCustomRoutesWithoutActor fires a custom test event and
// asserts it routes like one posted to the webhook: with no actor, so a
// member-only rule is skipped and the public rule after it creates the task,
// and with the type's label as its description.
func TestTestEventFireCustomRoutesWithoutActor(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	setProblemEventType(t, h)
	create := &model.CreateTaskAction{Workspace: e2e.DefaultWorkspace, Runner: e2e.DefaultRunner}
	// SetRoutingRules rejects a member-only custom rule, so it is stored
	// directly: the test needs one to show the caller's membership is ignored.
	err := h.Server.Store.SetOrgRoutingRules(t.Context(), nil, h.Org.ID, []model.RoutingRule{
		{Source: "custom", Type: "problem", Create: create},
		{Source: "custom", Type: "problem", Create: create, Public: true},
	})
	assert.NilError(t, err)
	url := "https://abc123.live.dynatrace.com/#problems/problemdetails;pid=1"

	// Act
	resp, err := h.Org.Client.TestEvent(t.Context(), &gritzv1.TestEventRequest{
		Source: "custom",
		Type:   "problem",
		Attrs: map[string]*gritzv1.AttrValues{
			"body":     {Values: []string{"CPU saturation"}},
			"url":      {Values: []string{url}},
			"severity": {Values: []string{"critical"}},
		},
		Fire: true,
	})

	// Assert: the public rule matched, not the member-only one before it.
	assert.NilError(t, err)
	tasks := h.Org.ListTasks(&gritzv1.ListTasksRequest{})
	assert.Equal(t, len(tasks), 1)
	events := h.Org.ListExternalEvents(&gritzv1.ListExternalEventsRequest{})
	assert.DeepEqual(t,
		resp,
		&gritzv1.TestEventResponse{
			Matches: []*gritzv1.TestEventMatch{{
				OrgId:          h.Org.ID,
				RuleIndex:      1,
				WouldCreate:    true,
				CreatedTaskIds: []int64{tasks[0].Id},
				EventIds:       []int64{events[0].Id},
			}},
			Fired: true,
		},
		protocmp.Transform(),
	)

	// Assert: the event's description defaults to the type's label.
	assert.DeepEqual(t,
		events,
		[]*gritzv1.Event{{
			TaskId: tasks[0].Id,
			Payload: &gritzv1.Event_External{External: &gritzv1.ExternalPayload{
				Description: "Dynatrace Problem",
				Url:         url,
				Data:        "CPU saturation",
				Source:      "custom",
				Type:        "problem",
			}},
		}},
		protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.Event{}, "id", "created_at"),
	)
}

// TestTestEventCustomMultiValuedAttr asserts a rule condition matches a custom
// event when any of a multi-valued attr's values satisfies it.
func TestTestEventCustomMultiValuedAttr(t *testing.T) {
	t.Parallel()
	// Arrange
	h := e2e.New(t, nil)
	setProblemEventType(t, h)
	h.Org.SetRoutingRules(&gritzv1.SetRoutingRulesRequest{
		Rules: []*gritzv1.RoutingRule{
			{
				Source:     "custom",
				Type:       "problem",
				Wakeup:     true,
				Conditions: []*gritzv1.RuleCondition{{Attr: "severity", Op: "equals", Value: "critical"}},
				Public:     true,
			},
		},
	})

	// Act
	resp, err := h.Org.Client.TestEvent(t.Context(), &gritzv1.TestEventRequest{
		Source: "custom",
		Type:   "problem",
		Attrs: map[string]*gritzv1.AttrValues{
			"url":      {Values: []string{"https://example.com/problems/1"}},
			"severity": {Values: []string{"warning", "critical"}},
		},
	})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t,
		resp,
		&gritzv1.TestEventResponse{
			Matches: []*gritzv1.TestEventMatch{{OrgId: h.Org.ID, RuleIndex: 0, WouldWake: true}},
		},
		protocmp.Transform(),
	)
}

// TestTestEventCustomInvalid asserts a custom test event is rejected wherever
// the custom event webhook would reject it.
func TestTestEventCustomInvalid(t *testing.T) {
	t.Parallel()
	h := e2e.New(t, nil)
	setProblemEventType(t, h)
	values := func(v ...string) *gritzv1.AttrValues { return &gritzv1.AttrValues{Values: v} }
	tests := []struct {
		name  string
		typ   string
		attrs map[string]*gritzv1.AttrValues
	}{
		{
			name:  "unknown type",
			typ:   "incident",
			attrs: map[string]*gritzv1.AttrValues{"url": values("https://example.com/1")},
		},
		{
			name: "missing url",
			typ:  "problem",
		},
		{
			name:  "relative url",
			typ:   "problem",
			attrs: map[string]*gritzv1.AttrValues{"url": values("/problems/1")},
		},
		{
			name:  "non-http url",
			typ:   "problem",
			attrs: map[string]*gritzv1.AttrValues{"url": values("ftp://example.com/1")},
		},
		{
			name: "undeclared attr",
			typ:  "problem",
			attrs: map[string]*gritzv1.AttrValues{
				"url":   values("https://example.com/1"),
				"owner": values("alice"),
			},
		},
		{
			name: "two urls",
			typ:  "problem",
			attrs: map[string]*gritzv1.AttrValues{
				"url": values("https://example.com/1", "https://example.com/2"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			_, err := h.Org.Client.TestEvent(t.Context(), &gritzv1.TestEventRequest{
				Source: "custom",
				Type:   tt.typ,
				Attrs:  tt.attrs,
				Fire:   true,
			})

			// Assert
			assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
		})
	}

	// Assert: nothing was persisted.
	assert.Equal(t, len(h.Org.ListExternalEvents(&gritzv1.ListExternalEventsRequest{})), 0)
}
