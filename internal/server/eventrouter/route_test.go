package eventrouter_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/server/customserver"
	"github.com/gritzapp/gritz/internal/server/eventrouter"
	"github.com/gritzapp/gritz/internal/server/githubserver"
	"github.com/gritzapp/gritz/internal/server/pubsub"
	"github.com/gritzapp/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// registry holds the real producer schemas, registered on an isolated
// registry.
var registry = func() *eventrouter.SchemaRegistry {
	reg := eventrouter.NewSchemaRegistry()
	githubserver.RegisterSchemas(reg)
	customserver.RegisterSchemas(reg)
	return reg
}()

func TestPlanWritesNothing(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	agent := teststore.CreateAgent(t, s, org, &model.Agent{
		Triggers: []model.AgentTrigger{{Source: "github", Type: "issue_comment"}},
	})
	r := &eventrouter.Router{Log: slog.Default(), Store: s, Registry: registry}

	// Act
	plan, err := r.Plan(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		URL:    "https://github.com/o/r/issues/1",
		UserID: org.UserID,
	})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, plan, &eventrouter.Plan{
		Triggers: []eventrouter.TriggerMatch{{OrgID: org.OrgID, Agent: agent, TriggerIndex: 0}},
	})
	tasks, err := s.ListTasks(t.Context(), nil, org.OrgID)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(tasks, 0))
}

func TestRouteDeliversByKind(t *testing.T) {
	t.Parallel()
	const url = "https://github.com/o/r/pull/1"
	tests := []struct {
		name      string
		agentLink model.AgentLink
		taskLink  teststore.LinkOptions
		delivered bool
	}{
		{
			name:      "kind lists the event type",
			agentLink: model.AgentLink{Type: "github.pr", Events: []model.AgentLinkEvent{{Type: "issue_comment"}}},
			taskLink:  teststore.LinkOptions{URL: url, Kind: "github.pr"},
			delivered: true,
		},
		{
			name: "kind's conditions on the event type hold",
			agentLink: model.AgentLink{Type: "github.pr", Kind: "owned-pr", Events: []model.AgentLinkEvent{{
				Type:       "issue_comment",
				Conditions: []model.Condition{{Attr: "body", Op: "contains", Value: "good"}},
			}}},
			taskLink:  teststore.LinkOptions{URL: url, Kind: "owned-pr"},
			delivered: true,
		},
		{
			name: "kind's conditions on the event type don't hold",
			agentLink: model.AgentLink{Type: "github.pr", Kind: "owned-pr", Events: []model.AgentLinkEvent{{
				Type:       "issue_comment",
				Conditions: []model.Condition{{Attr: "body", Op: "contains", Value: "bad"}},
			}}},
			taskLink: teststore.LinkOptions{URL: url, Kind: "owned-pr"},
		},
		{
			name:      "kind lists no event types",
			agentLink: model.AgentLink{Type: "github.pr", Kind: "owned-pr"},
			taskLink:  teststore.LinkOptions{URL: url, Kind: "owned-pr"},
		},
		{
			name:      "kind doesn't list the event type",
			agentLink: model.AgentLink{Type: "github.pr", Events: []model.AgentLinkEvent{{Type: "pull_request_closed"}}},
			taskLink:  teststore.LinkOptions{URL: url, Kind: "github.pr"},
		},
		{
			name:      "plain reference link",
			agentLink: model.AgentLink{Type: "github.pr"},
			taskLink:  teststore.LinkOptions{URL: url},
		},
		{
			name:      "kind the agent no longer has",
			agentLink: model.AgentLink{Type: "github.pr"},
			taskLink:  teststore.LinkOptions{URL: url, Kind: "owned-pr"},
		},
		{
			name:      "link to another resource",
			agentLink: model.AgentLink{Type: "github.pr"},
			taskLink:  teststore.LinkOptions{URL: "https://github.com/o/r/pull/2", Kind: "github.pr"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			s := teststore.New(t)
			org := teststore.CreateOrg(t, s, nil)
			agent := teststore.CreateAgent(t, s, org, &model.Agent{Links: []model.AgentLink{tt.agentLink}})
			task := teststore.CreateTask(t, s, org, &teststore.TaskOptions{
				AgentID: agent.ID,
				Status:  model.TaskStatusCompleted,
				Links:   []teststore.LinkOptions{tt.taskLink},
			})
			r := &eventrouter.Router{Log: slog.Default(), Store: s, Registry: registry}

			// Act - a comment's URL names the PR it is on.
			n, err := r.Route(t.Context(), eventrouter.InputEvent{
				Source: "github",
				Type:   "issue_comment",
				Data:   "looks good",
				URL:    url + "#issuecomment-9",
				UserID: org.UserID,
			})

			// Assert
			assert.NilError(t, err)
			got, err := s.GetTask(t.Context(), nil, task.ID, org.OrgID)
			assert.NilError(t, err)
			if tt.delivered {
				assert.Equal(t, n, 1)
				assert.Equal(t, got.Status, model.TaskStatusPending)
			} else {
				assert.Equal(t, n, 0)
				assert.Equal(t, got.Status, model.TaskStatusCompleted)
			}
		})
	}
}

func TestRouteDeliversOncePerTask(t *testing.T) {
	t.Parallel()
	// Arrange - a task with two links of different kinds on the PR.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	const url = "https://github.com/o/r/pull/1"
	agent := teststore.CreateAgent(t, s, org, &model.Agent{Links: []model.AgentLink{
		{Type: "github.pr", Events: []model.AgentLinkEvent{{Type: "pull_request_closed"}}},
		{Type: "github.pr", Kind: "owned-pr", Events: []model.AgentLinkEvent{{Type: "pull_request_closed"}}},
	}})
	task := teststore.CreateTask(t, s, org, &teststore.TaskOptions{
		AgentID: agent.ID,
		Links: []teststore.LinkOptions{
			{URL: url, Kind: "github.pr"},
			{URL: url, Kind: "owned-pr"},
		},
	})
	r := &eventrouter.Router{Log: slog.Default(), Store: s, Registry: registry}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "pull_request_closed",
		URL:    url,
		UserID: org.UserID,
	})

	// Assert - one event, through the first link.
	assert.NilError(t, err)
	assert.Equal(t, n, 1)
	events, err := s.ListEventsByTask(t.Context(), nil, task.ID, org.OrgID, []string{model.EventTypeExternal})
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(events, 1))
	assert.Equal(t, events[0].Wake, true)
}

func TestPlanDeliveryAdmission(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		eventType string
		delivered bool
	}{
		{name: "public event", eventType: "pull_request_review", delivered: true},
		{name: "member-only event", eventType: "issue_comment"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			s := teststore.New(t)
			org := teststore.CreateOrg(t, s, nil)
			const url = "https://github.com/o/r/pull/1"
			agent := teststore.CreateAgent(t, s, org, &model.Agent{Links: []model.AgentLink{
				{Type: "github.pr", Events: []model.AgentLinkEvent{{Type: "issue_comment"}, {Type: "pull_request_review", Public: true}}},
			}})
			task := teststore.CreateTask(t, s, org, &teststore.TaskOptions{
				AgentID: agent.ID,
				Links:   []teststore.LinkOptions{{URL: url, Kind: "github.pr"}},
			})
			r := &eventrouter.Router{Log: slog.Default(), Store: s, Registry: registry}

			// Act - an actor who isn't a member of the event's org.
			plan, err := r.Plan(t.Context(), eventrouter.InputEvent{
				Source: "github",
				Type:   tt.eventType,
				URL:    url,
				Orgs:   []int64{org.OrgID},
			})

			// Assert
			assert.NilError(t, err)
			var want []eventrouter.Delivery
			if tt.delivered {
				want = []eventrouter.Delivery{{OrgID: org.OrgID, AgentID: agent.ID, TaskID: task.ID, Kind: "github.pr"}}
			}
			assert.DeepEqual(t, plan.Deliveries, want)
		})
	}
}

func TestRouteDeliveryWakeMessage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		status  model.TaskStatus
		message string
	}{
		{
			name:    "finished task",
			status:  model.TaskStatusCompleted,
			message: "woken by event",
		},
		{
			// The notification still publishes so the FE invalidates, but the
			// agent channel stays silent for a task that is already running.
			name:   "running task",
			status: model.TaskStatusRunning,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			s := teststore.New(t)
			org := teststore.CreateOrg(t, s, nil)
			const url = "https://github.com/o/r/pull/1"
			agent := teststore.CreateAgent(t, s, org, &model.Agent{Links: []model.AgentLink{{Type: "github.pr", Events: []model.AgentLinkEvent{{Type: "issue_comment"}}}}})
			task := teststore.CreateTask(t, s, org, &teststore.TaskOptions{
				AgentID: agent.ID,
				Status:  tt.status,
				Links:   []teststore.LinkOptions{{URL: url, Kind: "github.pr"}},
			})
			pub := &pubsub.PublisherMock{
				PublishFunc: func(_ context.Context, _ model.Notification) error { return nil },
			}
			r := &eventrouter.Router{Log: slog.Default(), Store: s, Registry: registry, Publisher: pub}

			// Act
			_, err := r.Route(t.Context(), eventrouter.InputEvent{
				Source:      "github",
				Type:        "issue_comment",
				Description: "PR comment from alice",
				URL:         url,
				UserID:      org.UserID,
			})

			// Assert
			assert.NilError(t, err)
			notifications := pub.PublishedNotifications()
			assert.Assert(t, cmp.Len(notifications, 1))
			assert.Assert(t, cmp.Len(notifications[0].Resources, 3))
			assert.Equal(t, notifications[0].Resources[0].ID, task.ID)
			if tt.message == "" {
				assert.Equal(t, notifications[0].ChannelMessage, "")
			} else {
				assert.Assert(t, cmp.Contains(notifications[0].ChannelMessage, tt.message))
				assert.Assert(t, cmp.Contains(notifications[0].ChannelMessage, "PR comment from alice"))
			}
		})
	}
}

func TestRouteTriggerCreatesTask(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	agent := teststore.CreateAgent(t, s, org, &model.Agent{
		Workspace:   "w",
		Runner:      "r",
		Prompt:      "Review this PR.",
		AutoArchive: time.Hour,
		Variables:   map[string]string{"BRANCH": "${env:BRANCH}"},
		Features:    []string{"jira", "codex"},
		Triggers: []model.AgentTrigger{{
			Source:     "github",
			Type:       "issue_comment",
			Conditions: []model.Condition{{Attr: "mention", Op: "equals", Value: "gritz"}},
			Link:       "owned-pr",
			Prompt:     "Reply on the PR.",
		}},
		Links: []model.AgentLink{{Type: "github.pr", Kind: "owned-pr", Claim: "pr-owner"}},
	})
	r := &eventrouter.Router{Log: slog.Default(), Store: s, Registry: registry}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source:      "github",
		Type:        "issue_comment",
		Description: "alice commented on PR #1",
		Data:        "@gritz please review",
		URL:         "https://github.com/o/r/pull/1#issuecomment-9",
		UserID:      org.UserID,
		Attrs:       eventrouter.Attrs{"mention": {"gritz"}},
	})

	// Assert - a task of the agent, from its template; variables and features
	// are copied as saved, for the runner to resolve and apply.
	assert.NilError(t, err)
	assert.Equal(t, n, 1)
	tasks, err := s.ListTasks(t.Context(), nil, org.OrgID)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(tasks, 1))
	task := tasks[0]
	assert.DeepEqual(t, task, &model.Task{
		ID:          task.ID,
		Runner:      "r",
		Workspace:   "w",
		Status:      model.TaskStatusPending,
		Command:     model.TaskCommandStart,
		Version:     1,
		OrgID:       org.OrgID,
		AgentID:     agent.ID,
		AutoArchive: time.Hour,
		Variables:   map[string]string{"BRANCH": "${env:BRANCH}"},
		Features:    []string{"jira", "codex"},
	}, cmpopts.IgnoreFields(model.Task{}, "CreatedAt", "UpdatedAt"))

	// The trigger's link: the kind and its claim, on the PR's canonical URL.
	links, err := s.ListLinksByTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.DeepEqual(t, links, []*model.Link{{
		ID:        links[0].ID,
		TaskID:    task.ID,
		Relevance: "trigger",
		URL:       "https://github.com/o/r/pull/1",
		Kind:      "owned-pr",
		Claim:     "pr-owner",
	}}, cmpopts.IgnoreFields(model.Link{}, "CreatedAt"))

	// The timeline reads External -> Created -> Command -> Link -> the agent's
	// instruction -> the trigger's instruction.
	events, err := s.ListEventsByTask(t.Context(), nil, task.ID, org.OrgID, nil)
	assert.NilError(t, err)
	var payloads []model.EventPayload
	for _, e := range events {
		payloads = append(payloads, e.Payload)
	}
	assert.DeepEqual(t, payloads, []model.EventPayload{
		&model.ExternalPayload{
			Description: "alice commented on PR #1",
			URL:         "https://github.com/o/r/pull/1#issuecomment-9",
			Data:        "@gritz please review",
			Source:      "github",
			EventType:   "issue_comment",
		},
		&model.LifecyclePayload{
			Kind:     model.LifecycleKindCreated,
			Actor:    model.RouterActor,
			ToStatus: model.TaskStatusPending.Label(),
		},
		&model.CommandPayload{
			Command:   model.TaskCommandStart,
			Version:   1,
			Runner:    "r",
			Workspace: "w",
		},
		&model.LinkPayload{
			LinkID:    links[0].ID,
			Relevance: "trigger",
			URL:       "https://github.com/o/r/pull/1",
			Kind:      "owned-pr",
			Claim:     "pr-owner",
		},
		&model.InstructionPayload{Text: "Review this PR."},
		&model.InstructionPayload{Text: "Reply on the PR."},
	})
}

func TestRouteTriggerLink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		link  string
		url   string
		links int
	}{
		{name: "no link", url: "https://github.com/o/r/pull/1"},
		{name: "link kind accepts the URL", link: "github.pr", url: "https://github.com/o/r/pull/1", links: 1},
		// A comment on an issue names no PR: the task is still created.
		{name: "link kind doesn't accept the URL", link: "github.pr", url: "https://github.com/o/r/issues/1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			s := teststore.New(t)
			org := teststore.CreateOrg(t, s, nil)
			teststore.CreateAgent(t, s, org, &model.Agent{
				Triggers: []model.AgentTrigger{{Source: "github", Type: "issue_comment", Link: tt.link}},
				Links:    []model.AgentLink{{Type: "github.pr"}},
			})
			r := &eventrouter.Router{Log: slog.Default(), Store: s, Registry: registry}

			// Act
			n, err := r.Route(t.Context(), eventrouter.InputEvent{
				Source: "github",
				Type:   "issue_comment",
				URL:    tt.url,
				UserID: org.UserID,
			})

			// Assert
			assert.NilError(t, err)
			assert.Equal(t, n, 1)
			tasks, err := s.ListTasks(t.Context(), nil, org.OrgID)
			assert.NilError(t, err)
			assert.Assert(t, cmp.Len(tasks, 1))
			links, err := s.ListLinksByTask(t.Context(), nil, tasks[0].ID, org.OrgID)
			assert.NilError(t, err)
			assert.Assert(t, cmp.Len(links, tt.links))
		})
	}
}

func TestRouteTriggerPrompt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		agentPrompt   string
		triggerPrompt string
		want          []string
	}{
		{name: "neither"},
		{name: "agent only", agentPrompt: "Answer the comment.", want: []string{"Answer the comment."}},
		{name: "trigger only", triggerPrompt: "Triage the issue.", want: []string{"Triage the issue."}},
		{
			name:          "both",
			agentPrompt:   "Answer the comment.",
			triggerPrompt: "Triage the issue.",
			want:          []string{"Answer the comment.", "Triage the issue."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			s := teststore.New(t)
			org := teststore.CreateOrg(t, s, nil)
			teststore.CreateAgent(t, s, org, &model.Agent{
				Prompt:   tt.agentPrompt,
				Triggers: []model.AgentTrigger{{Source: "github", Type: "issue_comment", Prompt: tt.triggerPrompt}},
			})
			r := &eventrouter.Router{Log: slog.Default(), Store: s, Registry: registry}

			// Act
			_, err := r.Route(t.Context(), eventrouter.InputEvent{
				Source: "github",
				Type:   "issue_comment",
				URL:    "https://github.com/o/r/issues/1",
				UserID: org.UserID,
			})

			// Assert - one waking instruction per prompt, in order; none
			// without one.
			assert.NilError(t, err)
			tasks, err := s.ListTasks(t.Context(), nil, org.OrgID)
			assert.NilError(t, err)
			assert.Assert(t, cmp.Len(tasks, 1))
			events, err := s.ListEventsByTask(t.Context(), nil, tasks[0].ID, org.OrgID, []string{model.EventTypeInstruction})
			assert.NilError(t, err)
			var want []*model.Event
			for _, text := range tt.want {
				want = append(want, &model.Event{
					TaskID:  tasks[0].ID,
					OrgID:   org.OrgID,
					Wake:    true,
					Payload: &model.InstructionPayload{Text: text},
				})
			}
			assert.DeepEqual(t,
				events,
				want,
				cmpopts.IgnoreFields(model.Event{}, "ID", "CreatedAt", "XID"),
				cmpopts.EquateEmpty(),
			)
		})
	}
}

func TestPlanFirstTriggerWins(t *testing.T) {
	t.Parallel()
	// Arrange - the first trigger doesn't match; the second and third do.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	agent := teststore.CreateAgent(t, s, org, &model.Agent{
		Triggers: []model.AgentTrigger{
			{Source: "github", Type: "pull_request_closed"},
			{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "gritz:"}}},
			{Source: "github", Type: "issue_comment"},
		},
	})
	r := &eventrouter.Router{Log: slog.Default(), Store: s, Registry: registry}

	// Act
	plan, err := r.Plan(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		Data:   "gritz: fix it",
		URL:    "https://github.com/o/r/issues/1",
		UserID: org.UserID,
	})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, plan.Triggers, []eventrouter.TriggerMatch{{OrgID: org.OrgID, Agent: agent, TriggerIndex: 1}})
}

func TestRouteTwoAgentsOnOneEvent(t *testing.T) {
	t.Parallel()
	// Arrange - an implementer and a reviewer both trigger on the same event.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	implementer := teststore.CreateAgent(t, s, org, &model.Agent{
		Name:     "implementer",
		Triggers: []model.AgentTrigger{{Source: "github", Type: "pull_request_opened"}},
	})
	reviewer := teststore.CreateAgent(t, s, org, &model.Agent{
		Name:     "reviewer",
		Triggers: []model.AgentTrigger{{Source: "github", Type: "pull_request_opened"}},
	})
	r := &eventrouter.Router{Log: slog.Default(), Store: s, Registry: registry}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "pull_request_opened",
		URL:    "https://github.com/o/r/pull/1",
		UserID: org.UserID,
	})

	// Assert - a task of each.
	assert.NilError(t, err)
	assert.Equal(t, n, 2)
	tasks, err := s.ListTasks(t.Context(), nil, org.OrgID)
	assert.NilError(t, err)
	var agents []int64
	for _, task := range tasks {
		agents = append(agents, task.AgentID)
	}
	assert.DeepEqual(t, agents, []int64{implementer.ID, reviewer.ID}, cmpopts.SortSlices(func(a, b int64) bool { return a < b }))
}

func TestRouteTriggerYields(t *testing.T) {
	t.Parallel()
	const url = "https://github.com/o/r/pull/1"
	tests := []struct {
		name     string
		claim    string
		archived bool
		yields   bool
	}{
		{name: "unarchived holder", claim: "pr-owner", yields: true},
		{name: "archived holder", claim: "pr-owner", archived: true},
		{name: "link without the claim"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - an implementer's task owns the PR; a responder yields
			// to its owner.
			s := teststore.New(t)
			org := teststore.CreateOrg(t, s, nil)
			implementer := teststore.CreateAgent(t, s, org, &model.Agent{
				Name:  "implementer",
				Links: []model.AgentLink{{Type: "github.pr", Kind: "owned-pr", Claim: tt.claim, Events: []model.AgentLinkEvent{{Type: "pull_request_review"}}}},
			})
			holder := teststore.CreateTask(t, s, org, &teststore.TaskOptions{
				AgentID: implementer.ID,
				Status:  model.TaskStatusCompleted,
				Links:   []teststore.LinkOptions{{URL: url, Kind: "owned-pr", Claim: tt.claim}},
			})
			if tt.archived {
				holder.Archived = true
				assert.NilError(t, s.UpdateTask(t.Context(), nil, holder))
			}
			responder := teststore.CreateAgent(t, s, org, &model.Agent{
				Triggers: []model.AgentTrigger{{Source: "github", Type: "issue_comment", YieldTo: []string{"pr-owner"}}},
			})
			r := &eventrouter.Router{Log: slog.Default(), Store: s, Registry: registry}
			input := eventrouter.InputEvent{
				Source: "github",
				Type:   "issue_comment",
				URL:    url + "#issuecomment-9",
				UserID: org.UserID,
			}

			// Act
			plan, err := r.Plan(t.Context(), input)
			assert.NilError(t, err)
			outcomes := r.Apply(t.Context(), input, plan)

			// Assert - a yield creates nothing, and doesn't forward the event
			// to the holder, whose kind doesn't list it.
			assert.Assert(t, cmp.Len(plan.Deliveries, 0))
			match := eventrouter.TriggerMatch{OrgID: org.OrgID, Agent: responder, TriggerIndex: 0}
			if tt.yields {
				match.YieldedTo = []int64{holder.ID}
			}
			assert.DeepEqual(t, plan.Triggers, []eventrouter.TriggerMatch{match}, cmpopts.EquateEmpty())
			assert.Assert(t, cmp.Len(outcomes, 1))
			assert.Equal(t, outcomes[0].Created(), !tt.yields)
		})
	}
}

func TestRouteTriggerAndDeliveryAreIndependent(t *testing.T) {
	t.Parallel()
	// Arrange - a watcher's task subscribed to the PR, and a responder whose
	// trigger matches the same event.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	const url = "https://github.com/o/r/pull/1"
	watcher := teststore.CreateAgent(t, s, org, &model.Agent{
		Name:  "watcher",
		Links: []model.AgentLink{{Type: "github.pr", Events: []model.AgentLinkEvent{{Type: "issue_comment"}}}},
	})
	watching := teststore.CreateTask(t, s, org, &teststore.TaskOptions{
		AgentID: watcher.ID,
		Links:   []teststore.LinkOptions{{URL: url, Kind: "github.pr"}},
	})
	responder := teststore.CreateAgent(t, s, org, &model.Agent{
		Name:     "responder",
		Triggers: []model.AgentTrigger{{Source: "github", Type: "issue_comment"}},
	})
	r := &eventrouter.Router{Log: slog.Default(), Store: s, Registry: registry}
	input := eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		URL:    url,
		UserID: org.UserID,
	}

	// Act
	plan, err := r.Plan(t.Context(), input)
	assert.NilError(t, err)
	outcomes := r.Apply(t.Context(), input, plan)

	// Assert
	assert.Assert(t, cmp.Len(outcomes, 1))
	assert.DeepEqual(t, outcomes[0].Deliveries, []eventrouter.Delivery{
		{OrgID: org.OrgID, AgentID: watcher.ID, TaskID: watching.ID, Kind: "github.pr"},
	}, cmpopts.IgnoreFields(eventrouter.Delivery{}, "EventID"))
	assert.Assert(t, outcomes[0].Delivered())
	assert.Assert(t, cmp.Len(outcomes[0].Triggers, 1))
	assert.Equal(t, outcomes[0].Triggers[0].Agent.ID, responder.ID)
	assert.Assert(t, outcomes[0].Created())
}

func TestRouteTriggerThenDeliveryThroughItsLink(t *testing.T) {
	t.Parallel()
	// Arrange - an agent whose trigger links the task to the PR it was
	// created for, with a kind that delivers reviews.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	agent := teststore.CreateAgent(t, s, org, &model.Agent{
		Triggers: []model.AgentTrigger{{Source: "github", Type: "pull_request_opened", Link: "github.pr"}},
		Links:    []model.AgentLink{{Type: "github.pr", Events: []model.AgentLinkEvent{{Type: "pull_request_review"}}}},
	})
	r := &eventrouter.Router{Log: slog.Default(), Store: s, Registry: registry}
	_, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "pull_request_opened",
		URL:    "https://github.com/o/r/pull/1",
		UserID: org.UserID,
	})
	assert.NilError(t, err)
	tasks, err := s.ListTasks(t.Context(), nil, org.OrgID)
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(tasks, 1))

	// Act - a review on the PR, at its review URL.
	plan, err := r.Plan(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "pull_request_review",
		URL:    "https://github.com/o/r/pull/1#pullrequestreview-5",
		UserID: org.UserID,
	})

	// Assert - delivered to the created task through its link.
	assert.NilError(t, err)
	assert.DeepEqual(t, plan, &eventrouter.Plan{
		Deliveries: []eventrouter.Delivery{{OrgID: org.OrgID, AgentID: agent.ID, TaskID: tasks[0].ID, Kind: "github.pr"}},
	})
}

func TestPlanNonMemberOnlyPublicTriggers(t *testing.T) {
	t.Parallel()
	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	teststore.CreateAgent(t, s, org, &model.Agent{
		Name:     "members",
		Triggers: []model.AgentTrigger{{Source: "github", Type: "issue_comment"}},
	})
	public := teststore.CreateAgent(t, s, org, &model.Agent{
		Name: "public",
		Triggers: []model.AgentTrigger{
			{Source: "github", Type: "issue_comment"},
			{Source: "github", Type: "issue_comment", Public: true},
		},
	})
	r := &eventrouter.Router{Log: slog.Default(), Store: s, Registry: registry}

	// Act - an actor who isn't a member of the event's org.
	plan, err := r.Plan(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		URL:    "https://github.com/o/r/issues/1",
		Orgs:   []int64{org.OrgID},
	})

	// Assert - only the public trigger admits it.
	assert.NilError(t, err)
	assert.DeepEqual(t, plan.Triggers, []eventrouter.TriggerMatch{{OrgID: org.OrgID, Agent: public, TriggerIndex: 1}})
}

func TestPlanEveryMemberOrg(t *testing.T) {
	t.Parallel()
	// Arrange - the actor is a member of two orgs with a matching trigger each.
	s := teststore.New(t)
	orgA := teststore.CreateOrg(t, s, nil)
	orgB := teststore.CreateOrg(t, s, nil)
	assert.NilError(t, s.AddOrgMember(t.Context(), nil, &model.OrgMember{
		OrgID: orgB.OrgID, UserID: orgA.UserID, Role: "member",
	}))
	agentA := teststore.CreateAgent(t, s, orgA, &model.Agent{
		Triggers: []model.AgentTrigger{{Source: "github", Type: "issue_comment"}},
	})
	agentB := teststore.CreateAgent(t, s, orgB, &model.Agent{
		Triggers: []model.AgentTrigger{{Source: "github", Type: "issue_comment"}},
	})
	r := &eventrouter.Router{Log: slog.Default(), Store: s, Registry: registry}

	// Act
	plan, err := r.Plan(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		URL:    "https://github.com/o/r/issues/1",
		UserID: orgA.UserID,
	})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, plan.Triggers, []eventrouter.TriggerMatch{
		{OrgID: orgA.OrgID, Agent: agentA, TriggerIndex: 0},
		{OrgID: orgB.OrgID, Agent: agentB, TriggerIndex: 0},
	})
}

func TestRouteCustomEventDelivery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		linkTypes []string
		delivered bool
	}{
		{name: "type declares custom.url", linkTypes: []string{"custom.url"}, delivered: true},
		{name: "type declares no link type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			s := teststore.New(t)
			org := teststore.CreateOrg(t, s, nil)
			assert.NilError(t, s.SetOrgCustomEventTypes(t.Context(), nil, org.OrgID, []model.EventTypeDef{{
				Source:    eventrouter.SourceCustom,
				Type:      "problem",
				Label:     "Problem",
				Attrs:     []model.AttrDef{{Key: "body"}, {Key: "url"}},
				LinkTypes: tt.linkTypes,
			}}))
			const url = "https://dynatrace.example.com/problems/1"
			agent := teststore.CreateAgent(t, s, org, &model.Agent{
				Links: []model.AgentLink{{Type: "custom.url", Events: []model.AgentLinkEvent{{Type: "problem", Public: true}}}},
			})
			task := teststore.CreateTask(t, s, org, &teststore.TaskOptions{
				AgentID: agent.ID,
				Links:   []teststore.LinkOptions{{URL: url, Kind: "custom.url"}},
			})
			r := &eventrouter.Router{Log: slog.Default(), Store: s, Registry: registry}

			// Act - a custom event has no actor.
			plan, err := r.Plan(t.Context(), eventrouter.InputEvent{
				Source: eventrouter.SourceCustom,
				Type:   "problem",
				URL:    url,
				Orgs:   []int64{org.OrgID},
			})

			// Assert
			assert.NilError(t, err)
			var want []eventrouter.Delivery
			if tt.delivered {
				want = []eventrouter.Delivery{{OrgID: org.OrgID, AgentID: agent.ID, TaskID: task.ID, Kind: "custom.url"}}
			}
			assert.DeepEqual(t, plan.Deliveries, want)
		})
	}
}

func TestRouteOnRouteOutcome(t *testing.T) {
	t.Parallel()
	// Arrange - in one org a trigger creates a task; in another the event is
	// delivered; a third org has an agent the event does nothing for.
	s := teststore.New(t)
	creating := teststore.CreateOrg(t, s, nil)
	delivering := teststore.CreateOrg(t, s, nil)
	idle := teststore.CreateOrg(t, s, nil)
	const url = "https://github.com/o/r/pull/1"
	teststore.CreateAgent(t, s, creating, &model.Agent{
		Triggers: []model.AgentTrigger{{Source: "github", Type: "issue_comment", Public: true}},
	})
	agent := teststore.CreateAgent(t, s, delivering, &model.Agent{
		Links: []model.AgentLink{{Type: "github.pr", Events: []model.AgentLinkEvent{{Type: "issue_comment", Public: true}}}},
	})
	teststore.CreateTask(t, s, delivering, &teststore.TaskOptions{
		AgentID: agent.ID,
		Links:   []teststore.LinkOptions{{URL: url, Kind: "github.pr"}},
	})
	teststore.CreateAgent(t, s, idle, &model.Agent{
		Triggers: []model.AgentTrigger{{Source: "github", Type: "pull_request_closed", Public: true}},
	})
	var got []eventrouter.RouteOutcome
	r := &eventrouter.Router{
		Log:      slog.Default(),
		Store:    s,
		Registry: registry,
		OnRouteOutcome: func(_ context.Context, o eventrouter.RouteOutcome) {
			got = append(got, o)
		},
	}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		URL:    url,
		Orgs:   []int64{creating.OrgID, delivering.OrgID, idle.OrgID},
		Meta:   "meta",
	})

	// Assert - one outcome per org the event did something in, with its Meta.
	assert.NilError(t, err)
	assert.Equal(t, n, 2)
	assert.Assert(t, cmp.Len(got, 2))
	byOrg := map[int64]eventrouter.RouteOutcome{}
	for _, o := range got {
		assert.Equal(t, o.Input.Meta, "meta")
		byOrg[o.OrgID] = o
	}
	assert.Assert(t, byOrg[creating.OrgID].Created())
	assert.Assert(t, !byOrg[creating.OrgID].Delivered())
	assert.Assert(t, byOrg[delivering.OrgID].Delivered())
	assert.Assert(t, !byOrg[delivering.OrgID].Created())
}
