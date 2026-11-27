package api_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/e2e/harness"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/cmpx"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
)

// TestTestEvent is a sanity check on the dry-run handler: it composes a
// synthetic event, runs the real planner, reports per agent what the event
// would do, and writes nothing.
func TestTestEvent(t *testing.T) {
	t.Parallel()
	// Arrange - a watcher's task subscribed to the issue, a responder whose
	// second trigger matches, and an agent whose trigger doesn't.
	h := harness.New(t, nil)
	url := "https://github.com/o/r/issues/1"
	watcher := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "watcher",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Links:     []*gritzv1.AgentLink{{Type: "github.issue", Events: []*gritzv1.AgentLinkEvent{{Type: "issue_comment", Wake: true}}}},
	}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{AgentId: watcher.Id, Draft: true})
	h.Org.CreateLink(&gritzv1.CreateLinkRequest{TaskId: task.ID, Url: url, Kind: "github.issue"})
	responder := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "responder",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Triggers: []*gritzv1.AgentTrigger{
			{Source: "github", Type: "pull_request_opened"},
			{
				Source:     "github",
				Type:       "issue_comment",
				Conditions: []*gritzv1.RuleCondition{{Attr: "body", Op: "prefix", Value: "gritz:"}},
			},
		},
	}})
	h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "reviewer",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Triggers:  []*gritzv1.AgentTrigger{{Source: "github", Type: "pull_request_opened"}},
	}})

	// Act
	resp, err := h.Org.Client.TestEvent(t.Context(), &gritzv1.TestEventRequest{
		Source: "github",
		Type:   "issue_comment",
		Body:   "gritz: do it",
		Url:    url + "#issuecomment-9",
	})

	// Assert: per agent, in ListAgents order; the dry run never fires.
	assert.NilError(t, err)
	assert.DeepEqual(t,
		resp,
		&gritzv1.TestEventResponse{
			Agents: []*gritzv1.TestEventAgent{
				{AgentId: responder.Id, TriggerIndex: 1},
				{
					AgentId:      watcher.Id,
					TriggerIndex: -1,
					Deliveries:   []*gritzv1.TestEventDelivery{{TaskId: task.ID, Kind: "github.issue", Wake: true}},
				},
			},
		},
		protocmp.Transform(),
	)

	// Assert: the dry run persists nothing.
	assert.Equal(t, len(h.Org.ListTasks(&gritzv1.ListTasksRequest{Archived: true})), 1)
	assert.Equal(t, len(h.Org.ListExternalEvents(&gritzv1.ListExternalEventsRequest{})), 0)
}

// TestTestEventLinkConditions asserts the report delivers an event through a
// link kind only when the kind's conditions on the event type hold.
func TestTestEventLinkConditions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		body      string
		delivered bool
	}{
		{name: "conditions hold", body: "LGTM, ship it", delivered: true},
		{name: "conditions don't hold", body: "needs work"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - a watcher's task subscribed to the PR, through a kind
			// that delivers only approving reviews.
			h := harness.New(t, nil)
			url := "https://github.com/o/r/pull/1"
			watcher := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
				Name:      "watcher",
				Workspace: harness.DefaultWorkspace,
				Runner:    harness.DefaultRunner,
				Links: []*gritzv1.AgentLink{{
					Type: "github.pr",
					Events: []*gritzv1.AgentLinkEvent{{
						Type:       "pull_request_review",
						Conditions: []*gritzv1.RuleCondition{{Attr: "body", Op: "prefix", Value: "LGTM"}},
					}},
				}},
			}})
			task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{AgentId: watcher.Id, Draft: true})
			h.Org.CreateLink(&gritzv1.CreateLinkRequest{TaskId: task.ID, Url: url, Kind: "github.pr"})

			// Act
			resp, err := h.Org.Client.TestEvent(t.Context(), &gritzv1.TestEventRequest{
				Source: "github",
				Type:   "pull_request_review",
				Body:   tt.body,
				Url:    url + "#pullrequestreview-9",
			})

			// Assert
			assert.NilError(t, err)
			want := &gritzv1.TestEventResponse{}
			if tt.delivered {
				want.Agents = []*gritzv1.TestEventAgent{{
					AgentId:      watcher.Id,
					TriggerIndex: -1,
					Deliveries:   []*gritzv1.TestEventDelivery{{TaskId: task.ID, Kind: "github.pr"}},
				}}
			}
			assert.DeepEqual(t, resp, want, protocmp.Transform())
		})
	}
}

// TestTestEventYield asserts a trigger yields to a task holding one of its
// yield_to claims on the event's resource, and the report names the holder.
func TestTestEventYield(t *testing.T) {
	t.Parallel()
	// Arrange - an implementer's task owns the PR.
	h := harness.New(t, nil)
	implementer := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "implementer",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Links: []*gritzv1.AgentLink{{
			Type:   "github.pr",
			Kind:   "owned-pr",
			Claim:  "pr-owner",
			Events: []*gritzv1.AgentLinkEvent{{Type: "pull_request_review"}},
		}},
	}})
	owner := h.Org.CreateTask(&gritzv1.CreateTaskRequest{AgentId: implementer.Id, Draft: true})
	h.Org.CreateLink(&gritzv1.CreateLinkRequest{TaskId: owner.ID, Url: "https://github.com/o/r/pull/1", Kind: "owned-pr"})
	responder := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "responder",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Triggers: []*gritzv1.AgentTrigger{{
			Source:  "github",
			Type:    "issue_comment",
			YieldTo: []string{"pr-owner"},
		}},
	}})

	// Act
	resp, err := h.Org.Client.TestEvent(t.Context(), &gritzv1.TestEventRequest{
		Source: "github",
		Type:   "issue_comment",
		Url:    "https://github.com/o/r/pull/1#issuecomment-9",
		Fire:   true,
	})

	// Assert: the responder yielded and created nothing; the owner's kind
	// doesn't list comments, so it isn't delivered the event either.
	assert.NilError(t, err)
	assert.DeepEqual(t,
		resp,
		&gritzv1.TestEventResponse{
			Fired: true,
			Agents: []*gritzv1.TestEventAgent{
				{AgentId: responder.Id, TriggerIndex: 0, YieldedTo: []int64{owner.ID}},
			},
		},
		protocmp.Transform(),
	)
	assert.Equal(t, len(h.Org.ListTasks(&gritzv1.ListTasksRequest{})), 1)
}

// TestTestEventFireCreatesTask fires a synthetic event against a trigger and
// asserts it routes for real: a task of the agent and its external event are
// written, the composed Details land on the persisted ExternalPayload, and the
// response reports the created task and event ids.
func TestTestEventFireCreatesTask(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	agent := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "responder",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Prompt:    "Do it.",
		Triggers: []*gritzv1.AgentTrigger{{
			Source:     "github",
			Type:       "issue_comment",
			Conditions: []*gritzv1.RuleCondition{{Attr: "body", Op: "prefix", Value: "gritz:"}},
		}},
	}})
	url := "https://github.com/o/r/issues/1"
	details := map[string]string{"path": "main.go", "line": "42"}

	// Act
	resp, err := h.Org.Client.TestEvent(t.Context(), &gritzv1.TestEventRequest{
		Source:      "github",
		Type:        "issue_comment",
		Description: "alice commented",
		Body:        "gritz: do it",
		Url:         url,
		Details:     details,
		Fire:        true,
	})

	assert.NilError(t, err)

	// Assert: a real task of the agent exists.
	tasks := h.Org.ListTasks(&gritzv1.ListTasksRequest{})
	assert.DeepEqual(t,
		tasks,
		[]*gritzv1.Task{{
			Id:      resp.Agents[0].CreatedTaskId,
			Status:  gritzv1.TaskStatus_PENDING,
			Command: gritzv1.TaskCommand_START,
			AgentId: agent.Id,
		}},
		protocmp.Transform(),
		cmpx.OnlyProtoFields(&gritzv1.Task{}, "id", "status", "command", "agent_id"),
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
			Agents: []*gritzv1.TestEventAgent{{
				AgentId:        agent.Id,
				TriggerIndex:   0,
				CreatedTaskId:  tasks[0].Id,
				CreatedEventId: events[0].Id,
			}},
			Fired: true,
		},
		protocmp.Transform(),
	)
}

// TestTestEventFireWakesThroughTriggerLink fires an event that creates a task
// through a trigger with a link, then one on the same resource, and asserts
// the second is delivered to the created task through that link, waking it.
func TestTestEventFireWakesThroughTriggerLink(t *testing.T) {
	t.Parallel()
	// Arrange - a reviewer created for each opened PR, linked to it, and woken
	// by its reviews.
	h := harness.New(t, nil)
	agent := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "reviewer",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Triggers:  []*gritzv1.AgentTrigger{{Source: "github", Type: "pull_request_opened", Link: "github.pr"}},
		Links:     []*gritzv1.AgentLink{{Type: "github.pr", Events: []*gritzv1.AgentLinkEvent{{Type: "pull_request_review", Wake: true}}}},
	}})
	created := h.Org.TestEvent(&gritzv1.TestEventRequest{
		Source: "github",
		Type:   "pull_request_opened",
		Url:    "https://github.com/o/r/pull/1",
		Fire:   true,
	})
	taskID := created.Agents[0].CreatedTaskId
	assert.DeepEqual(t,
		h.Org.ListLinks(&gritzv1.ListLinksRequest{TaskId: taskID}),
		[]*gritzv1.TaskLink{{
			TaskId:    taskID,
			Relevance: "trigger",
			Url:       "https://github.com/o/r/pull/1",
			Kind:      "github.pr",
		}},
		protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.TaskLink{}, "id", "created_at"),
	)
	// The task finishes its run.
	h.Org.SubmitRunnerEvents(&gritzv1.SubmitRunnerEventsRequest{
		Events: []*gritzv1.RunnerEvent{
			{TaskId: taskID, Event: string(model.RunnerEventStarted)},
			{TaskId: taskID, Event: string(model.RunnerEventStopped)},
		},
	})

	// Act - a review on the PR.
	resp, err := h.Org.Client.TestEvent(t.Context(), &gritzv1.TestEventRequest{
		Source: "github",
		Type:   "pull_request_review",
		Url:    "https://github.com/o/r/pull/1#pullrequestreview-5",
		Fire:   true,
	})

	// Assert - delivered through the trigger's link, waking the task.
	assert.NilError(t, err)
	assert.DeepEqual(t,
		resp,
		&gritzv1.TestEventResponse{
			Fired: true,
			Agents: []*gritzv1.TestEventAgent{{
				AgentId:      agent.Id,
				TriggerIndex: -1,
				Deliveries:   []*gritzv1.TestEventDelivery{{TaskId: taskID, Kind: "github.pr", Wake: true}},
			}},
		},
		protocmp.Transform(),
		protocmp.IgnoreFields(&gritzv1.TestEventDelivery{}, "event_id"),
	)
	assert.DeepEqual(t,
		h.Org.GetTask(&gritzv1.GetTaskRequest{Id: taskID}),
		&gritzv1.Task{Id: taskID, Status: gritzv1.TaskStatus_PENDING, Version: 2},
		protocmp.Transform(),
		cmpx.OnlyProtoFields(&gritzv1.Task{}, "id", "status", "version"),
	)
}

// TestTestEventFireRequiresOrgWrite asserts fire mode is gated on OpOrgWrite: a
// read-only caller can dry-run but not fire.
func TestTestEventFireRequiresOrgWrite(t *testing.T) {
	t.Parallel()
	// Arrange: a read-only caller, OpOrgRead only, no write.
	h := harness.New(t, nil)
	key := h.Org.CreateScopedKey(authscope.Scopes{
		authscope.New(
			authscope.OpOrgRead,
		),
	})
	req := &gritzv1.TestEventRequest{
		Source: "github",
		Type:   "issue_comment",
		Body:   "gritz: go",
		Url:    "https://github.com/o/r/issues/1",
	}

	// Act & Assert - dry run is allowed.
	_, err := key.Client.TestEvent(t.Context(), req)
	assert.NilError(t, err)

	// Act & Assert - fire is denied.
	req.Fire = true
	_, err = key.Client.TestEvent(t.Context(), req)
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
}

// TestTestEventFireCustomRoutesWithoutActor fires a custom test event and
// asserts it routes like one posted to the webhook: with no actor, so a
// member-only trigger is skipped and the public trigger after it creates the task,
// and with the type's label as its description.
func TestTestEventFireCustomRoutesWithoutActor(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
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
	// CreateAgent rejects a member-only custom trigger, so the agent is stored
	// directly: the test needs one to show the caller's membership is ignored.
	agent := &model.Agent{
		OrgID:     h.Org.ID,
		Name:      "investigator",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Triggers: []model.AgentTrigger{
			{Source: "custom", Type: "problem"},
			{Source: "custom", Type: "problem", Public: true},
		},
	}
	err = h.Server.Store.CreateAgent(t.Context(), nil, agent)
	assert.NilError(t, err)
	url := "https://abc123.live.dynatrace.com/#problems/problemdetails;pid=1"

	// Act
	resp, err := h.Org.Client.TestEvent(t.Context(), &gritzv1.TestEventRequest{
		Source: "custom",
		Type:   "problem",
		Body:   "CPU saturation",
		Url:    url,
		Attrs:  map[string]*gritzv1.AttrValues{"severity": {Values: []string{"critical"}}},
		Fire:   true,
	})

	// Assert: the public trigger matched, not the member-only one before it.
	assert.NilError(t, err)
	tasks := h.Org.ListTasks(&gritzv1.ListTasksRequest{})
	assert.Equal(t, len(tasks), 1)
	events := h.Org.ListExternalEvents(&gritzv1.ListExternalEventsRequest{})
	assert.DeepEqual(t,
		resp,
		&gritzv1.TestEventResponse{
			Agents: []*gritzv1.TestEventAgent{{
				AgentId:        agent.ID,
				TriggerIndex:   1,
				CreatedTaskId:  tasks[0].Id,
				CreatedEventId: events[0].Id,
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

// TestTestEventCustomMultiValuedAttr asserts a trigger condition matches a custom
// event when any of a multi-valued attr's values satisfies it.
func TestTestEventCustomMultiValuedAttr(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
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
	agent := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "investigator",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Triggers: []*gritzv1.AgentTrigger{{
			Source:     "custom",
			Type:       "problem",
			Conditions: []*gritzv1.RuleCondition{{Attr: "severity", Op: "equals", Value: "critical"}},
			Public:     true,
		}},
	}})

	// Act
	resp, err := h.Org.Client.TestEvent(t.Context(), &gritzv1.TestEventRequest{
		Source: "custom",
		Type:   "problem",
		Url:    "https://example.com/problems/1",
		Attrs:  map[string]*gritzv1.AttrValues{"severity": {Values: []string{"warning", "critical"}}},
	})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t,
		resp,
		&gritzv1.TestEventResponse{
			Agents: []*gritzv1.TestEventAgent{{AgentId: agent.Id, TriggerIndex: 0}},
		},
		protocmp.Transform(),
	)
}

// TestTestEventCustomInvalid asserts a custom test event is rejected wherever
// the custom event webhook would reject it.
func TestTestEventCustomInvalid(t *testing.T) {
	t.Parallel()
	h := harness.New(t, nil)
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
	tests := []struct {
		name  string
		typ   string
		url   string
		attrs map[string]*gritzv1.AttrValues
	}{
		{
			name: "unknown type",
			typ:  "incident",
			url:  "https://example.com/1",
		},
		{
			name: "missing url",
			typ:  "problem",
		},
		{
			name: "relative url",
			typ:  "problem",
			url:  "/problems/1",
		},
		{
			name: "non-http url",
			typ:  "problem",
			url:  "ftp://example.com/1",
		},
		{
			name:  "undeclared attr",
			typ:   "problem",
			url:   "https://example.com/1",
			attrs: map[string]*gritzv1.AttrValues{"owner": {Values: []string{"alice"}}},
		},
		{
			name:  "url sent as an attr",
			typ:   "problem",
			url:   "https://example.com/1",
			attrs: map[string]*gritzv1.AttrValues{"url": {Values: []string{"https://example.com/1"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			_, err := h.Org.Client.TestEvent(t.Context(), &gritzv1.TestEventRequest{
				Source: "custom",
				Type:   tt.typ,
				Url:    tt.url,
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

// TestTestEventBuiltinInvalid asserts a built-in test event is validated
// against its type like a custom one.
func TestTestEventBuiltinInvalid(t *testing.T) {
	t.Parallel()
	h := harness.New(t, nil)
	tests := []struct {
		name   string
		source string
		typ    string
		url    string
		attrs  map[string]*gritzv1.AttrValues
	}{
		{
			name:   "unknown source",
			source: "gitlab",
			typ:    "issue_comment",
			url:    "https://github.com/o/r/issues/1",
		},
		{
			name:   "unknown type",
			source: "github",
			typ:    "not_a_type",
			url:    "https://github.com/o/r/issues/1",
		},
		{
			name:   "missing url",
			source: "github",
			typ:    "issue_comment",
		},
		{
			name:   "undeclared attr",
			source: "github",
			typ:    "issue_comment",
			url:    "https://github.com/o/r/issues/1",
			attrs:  map[string]*gritzv1.AttrValues{"severity": {Values: []string{"critical"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			_, err := h.Org.Client.TestEvent(t.Context(), &gritzv1.TestEventRequest{
				Source: tt.source,
				Type:   tt.typ,
				Url:    tt.url,
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
