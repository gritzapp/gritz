package eventrouter_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/icholy/gritz/internal/model"
	"github.com/icholy/gritz/internal/server/eventrouter"
	"github.com/icholy/gritz/internal/server/pubsub"
	"github.com/icholy/gritz/internal/store/teststore"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestPlanMatchesRulePerOrgWithoutSideEffects(t *testing.T) {
	t.Parallel()

	// Plan is the read-only matcher Route consumes: it reports the matched rule
	// per org and writes nothing. Each case exercises a distinct outcome — a
	// configured rule matches (RuleIndex points at its position) and a configured
	// rule does not match (org dropped). A ruleless org has no shipped-default
	// fallback, so it never matches (covered by TestRouteRuleLessOrgRoutesNothing).
	// Neither case touches the store.
	const url = "https://github.com/owner/repo/issues/1"
	tests := []struct {
		name      string
		rules     []model.RoutingRule
		data      string
		wantMatch bool
	}{
		{
			name:      "configured rule matches",
			rules:     []model.RoutingRule{{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "gritz:"}}}},
			data:      "gritz: do it",
			wantMatch: true,
		},
		{
			name:      "configured rule does not match",
			rules:     []model.RoutingRule{{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "bot:"}}}},
			data:      "gritz: do it",
			wantMatch: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Arrange
			s := teststore.New(t)
			org := teststore.CreateOrg(t, s, nil)
			assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, tt.rules))
			r := &eventrouter.Router{Log: slog.Default(), Store: s}

			// Act
			matches, err := r.Plan(t.Context(), eventrouter.InputEvent{
				Source: "github",
				Type:   "issue_comment",
				Data:   tt.data,
				URL:    url,
				UserID: org.UserID,
			})

			// Assert
			assert.NilError(t, err)
			if !tt.wantMatch {
				assert.Assert(t, cmp.Len(matches, 0))
			} else {
				assert.Assert(t, cmp.Len(matches, 1))
				assert.Equal(t, matches[0].OrgID, org.OrgID)
				assert.Equal(t, matches[0].Rule.Source, "github")
				// A single configured rule that matched sits at index 0.
				assert.Equal(t, matches[0].RuleIndex, 0)
			}

			// Plan is read-only: no tasks (and therefore no events) are written.
			tasks, err := s.ListTasks(t.Context(), nil, org.OrgID)
			assert.NilError(t, err)
			assert.Assert(t, cmp.Len(tasks, 0))
		})
	}
}

func TestPlanReturnsMatchPerMemberOrg(t *testing.T) {
	t.Parallel()

	// Plan evaluates every org the event belongs to: an actor in two member orgs
	// that both match gets a RouteMatch for each. Scoping the result to a single
	// org is the caller's job (layer 3), not Plan's.
	s := teststore.New(t)
	orgA := teststore.CreateOrg(t, s, nil)
	orgB := teststore.CreateOrg(t, s, nil)
	assert.NilError(t, s.AddOrgMember(t.Context(), nil, &model.OrgMember{
		OrgID: orgB.OrgID, UserID: orgA.UserID, Role: "member",
	}))
	rule := []model.RoutingRule{{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "gritz:"}}}}
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, orgA.OrgID, rule))
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, orgB.OrgID, rule))
	r := &eventrouter.Router{Log: slog.Default(), Store: s}

	matches, err := r.Plan(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		Data:   "gritz: do it",
		URL:    "https://github.com/owner/repo/issues/1",
		UserID: orgA.UserID,
	})

	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(matches, 2))
	gotOrgs := map[int64]bool{}
	for _, m := range matches {
		gotOrgs[m.OrgID] = true
	}
	assert.Assert(t, gotOrgs[orgA.OrgID] && gotOrgs[orgB.OrgID])
}

func TestRouteCreatesEventAndStartsTask(t *testing.T) {
	t.Parallel()

	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	url := "https://github.com/owner/repo/pull/1"
	task := teststore.CreateTask(t, s, org, &teststore.TaskOptions{
		Status: model.TaskStatusCompleted,
		Links:  []teststore.LinkOptions{{URL: url, Subscribe: true}},
	})
	// A wake rule reproducing the former shipped default (gritz: body-prefix).
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, []model.RoutingRule{
		{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "gritz:"}}, Wakeup: true},
	}))
	r := &eventrouter.Router{
		Log:   slog.Default(),
		Store: s,
	}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source:      "github",
		Type:        "issue_comment",
		Description: "testuser commented on PR #1",
		Data:        "gritz: fix tests",
		URL:         url,
		UserID:      org.UserID,
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, n, 1)
	updated, err := s.GetTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, updated.Status, model.TaskStatusPending)
}

func TestRouteNonCanonicalURLMatchesByRoutingKey(t *testing.T) {
	t.Parallel()

	// Arrange: a subscribed link stored under the canonical issue key. The
	// incoming event carries a non-canonical comment URL — the router must
	// derive the same routing key from it to match. A raw-URL lookup would miss.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	canonical := "https://github.com/o/r/issues/5"
	task := teststore.CreateTask(t, s, org, &teststore.TaskOptions{
		Status: model.TaskStatusCompleted,
		Links:  []teststore.LinkOptions{{URL: canonical, Subscribe: true}},
	})
	// A wake rule reproducing the former shipped default (gritz: body-prefix).
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, []model.RoutingRule{
		{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "gritz:"}}, Wakeup: true},
	}))
	r := &eventrouter.Router{
		Log:   slog.Default(),
		Store: s,
	}

	// Act: event URL is the comment permalink, not the canonical issue URL.
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		Data:   "gritz: fix tests",
		URL:    "https://github.com/o/r/issues/5#issuecomment-9",
		UserID: org.UserID,
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, n, 1)
	updated, err := s.GetTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, updated.Status, model.TaskStatusPending)
}

func TestRouteDeduplicatesTasksWithMultipleLinks(t *testing.T) {
	t.Parallel()

	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	url := "https://github.com/owner/repo/pull/1"
	teststore.CreateTask(t, s, org, &teststore.TaskOptions{
		Status: model.TaskStatusCompleted,
		Links: []teststore.LinkOptions{
			{URL: url, Subscribe: true},
			{URL: url, Subscribe: true},
		},
	})
	// A wake rule reproducing the former shipped default (gritz: body-prefix).
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, []model.RoutingRule{
		{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "gritz:"}}, Wakeup: true},
	}))
	r := &eventrouter.Router{
		Log:   slog.Default(),
		Store: s,
	}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		Data:   "gritz: do something",
		URL:    url,
		UserID: org.UserID,
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, n, 1)
}

func TestRouteNoOp(t *testing.T) {
	t.Parallel()

	// Each case exercises the full Route path but ends in a no-op (n == 0) for a
	// different reason: no subscribed link at the URL, an empty URL that can never
	// match a link, and a body that the default rule's "gritz:" prefix rejects.
	// (Attribute-level matching semantics themselves are covered by TestMatchRule.)
	const url = "https://github.com/owner/repo/pull/1"
	tests := []struct {
		name string
		link bool // seed a subscribed done task at url
		data string
		url  string
	}{
		{name: "no subscribed link at url", data: "gritz: do something", url: url},
		{name: "empty url never matches a link", data: "gritz: do something", url: ""},
		{name: "body without gritz: prefix is not matched", link: true, data: "just a regular comment", url: url},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Arrange
			s := teststore.New(t)
			org := teststore.CreateOrg(t, s, nil)
			var task *model.Task
			if tt.link {
				task = teststore.CreateTask(t, s, org, &teststore.TaskOptions{
					Status: model.TaskStatusCompleted,
					Links:  []teststore.LinkOptions{{URL: url, Subscribe: true}},
				})
			}
			r := &eventrouter.Router{Log: slog.Default(), Store: s}

			// Act
			n, err := r.Route(t.Context(), eventrouter.InputEvent{
				Source: "github",
				Type:   "issue_comment",
				Data:   tt.data,
				URL:    tt.url,
				UserID: org.UserID,
			})

			// Assert
			assert.NilError(t, err)
			assert.Equal(t, n, 0)
			if task != nil {
				updated, err := s.GetTask(t.Context(), nil, task.ID, org.OrgID)
				assert.NilError(t, err)
				assert.Equal(t, updated.Status, model.TaskStatusCompleted)
			}
		})
	}
}

func TestRouteOrgRulesOverrideDefaults(t *testing.T) {
	t.Parallel()

	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	url := "https://github.com/owner/repo/pull/1"
	task := teststore.CreateTask(t, s, org, &teststore.TaskOptions{
		Status: model.TaskStatusCompleted,
		Links:  []teststore.LinkOptions{{URL: url, Subscribe: true}},
	})
	err := s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, []model.RoutingRule{
		{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "bot:"}}},
	})
	assert.NilError(t, err)
	r := &eventrouter.Router{
		Log:   slog.Default(),
		Store: s,
	}

	// Act - "gritz:" prefix should NOT match because the org overrode the defaults
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		Data:   "gritz: do something",
		URL:    url,
		UserID: org.UserID,
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
	updated, err := s.GetTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, updated.Status, model.TaskStatusCompleted)
}

func TestRouter_AttachSetsWakeMessage(t *testing.T) {
	t.Parallel()

	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "r", Name: "w"}}})
	url := "https://github.com/owner/repo/pull/1#issuecomment-1"
	teststore.CreateTask(t, s, org, &teststore.TaskOptions{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		Links:     []teststore.LinkOptions{{URL: url, Subscribe: true}},
	})
	// A wake rule reproducing the former shipped default (gritz: body-prefix).
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, []model.RoutingRule{
		{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "gritz:"}}, Wakeup: true},
	}))
	pub := &pubsub.PublisherMock{
		PublishFunc: func(_ context.Context, _ model.Notification) error { return nil },
	}
	r := &eventrouter.Router{
		Log:       slog.Default(),
		Store:     s,
		Publisher: pub,
	}

	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source:      "github",
		Type:        "issue_comment",
		Description: "PR comment from alice",
		Data:        "gritz: fix tests",
		URL:         url,
		UserID:      org.UserID,
	})
	assert.NilError(t, err)
	assert.Equal(t, n, 1)

	notifications := pub.PublishedNotifications()
	assert.Assert(t, cmp.Len(notifications, 1))
	msg := notifications[0].ChannelMessage
	assert.Assert(t, msg != "", "expected non-empty ChannelMessage, got empty")
	assert.Assert(t, strings.Contains(msg, "Task "), "expected task id in message: %q", msg)
	assert.Assert(t, strings.Contains(msg, "woken by event"), "expected wake phrase in message: %q", msg)
	assert.Assert(t, strings.Contains(msg, "PR comment from alice"), "expected description in message: %q", msg)
	assert.Assert(t, strings.Contains(msg, url), "expected URL in message: %q", msg)
}

func TestRouter_AttachToRunningTaskStaysSilent(t *testing.T) {
	t.Parallel()

	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "r", Name: "w"}}})
	url := "https://github.com/owner/repo/pull/1#issuecomment-1"
	teststore.CreateTask(t, s, org, &teststore.TaskOptions{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusRunning,
		Links:     []teststore.LinkOptions{{URL: url, Subscribe: true}},
	})
	// A wake rule reproducing the former shipped default (gritz: body-prefix).
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, []model.RoutingRule{
		{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "gritz:"}}, Wakeup: true},
	}))
	pub := &pubsub.PublisherMock{
		PublishFunc: func(_ context.Context, _ model.Notification) error { return nil },
	}
	r := &eventrouter.Router{
		Log:       slog.Default(),
		Store:     s,
		Publisher: pub,
	}

	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source:      "github",
		Type:        "issue_comment",
		Description: "PR comment from alice",
		Data:        "gritz: fix tests",
		URL:         url,
		UserID:      org.UserID,
	})
	assert.NilError(t, err)
	assert.Equal(t, n, 1)

	// The notification still publishes with the usual resources so the FE
	// invalidates, but ChannelMessage is empty — PR #725's gate keeps the
	// agent channel silent on a no-transition attach.
	notifications := pub.PublishedNotifications()
	assert.Assert(t, cmp.Len(notifications, 1))
	assert.Equal(t, notifications[0].ChannelMessage, "")
	assert.Equal(t, len(notifications[0].Resources), 3)
}

func TestRouteNoWakeAttachesEventWithoutRestart(t *testing.T) {
	t.Parallel()

	// Arrange: a matching rule that opts out of waking via Wakeup{Enable:false},
	// and a done task subscribed to the event URL.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "r", Name: "w"}}})
	url := "https://github.com/owner/repo/pull/1#issuecomment-1"
	task := teststore.CreateTask(t, s, org, &teststore.TaskOptions{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		Links:     []teststore.LinkOptions{{URL: url, Subscribe: true}},
	})
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, []model.RoutingRule{
		{Source: "github", Wakeup: false},
	}))
	pub := &pubsub.PublisherMock{
		PublishFunc: func(_ context.Context, _ model.Notification) error { return nil },
	}
	r := &eventrouter.Router{Log: slog.Default(), Store: s, Publisher: pub}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source:      "github",
		Type:        "issue_comment",
		Description: "PR comment from alice",
		Data:        "anything",
		URL:         url,
		UserID:      org.UserID,
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, n, 1)

	// The task is NOT restarted — its status stays done.
	updated, err := s.GetTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, updated.Status, model.TaskStatusCompleted)

	// The event was still attached to the task.
	events, err := s.ListEventsByTask(t.Context(), nil, task.ID, org.OrgID, []string{model.EventTypeExternal})
	assert.NilError(t, err)
	assert.Equal(t, len(events), 1)

	// A channel notification is published unconditionally so the event isn't
	// silently swallowed, even though the task wasn't restarted.
	notifications := pub.PublishedNotifications()
	assert.Assert(t, cmp.Len(notifications, 1))
	msg := notifications[0].ChannelMessage
	assert.Assert(t, msg != "", "expected non-empty ChannelMessage, got empty")
	assert.Assert(t, strings.Contains(msg, "PR comment from alice"), "expected description in message: %q", msg)
	assert.Assert(t, strings.Contains(msg, url), "expected URL in message: %q", msg)
}

func TestRouteWakeEnabledRestartsTask(t *testing.T) {
	t.Parallel()

	// Arrange: same setup but with Wakeup:true — the task IS restarted, the
	// current behavior.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, &teststore.OrgOptions{Workspaces: []teststore.WorkspaceOptions{{RunnerID: "r", Name: "w"}}})
	url := "https://github.com/owner/repo/pull/1#issuecomment-1"
	task := teststore.CreateTask(t, s, org, &teststore.TaskOptions{
		Runner:    "r",
		Workspace: "w",
		Status:    model.TaskStatusCompleted,
		Links:     []teststore.LinkOptions{{URL: url, Subscribe: true}},
	})
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, []model.RoutingRule{
		{Source: "github", Wakeup: true},
	}))
	r := &eventrouter.Router{Log: slog.Default(), Store: s}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source:      "github",
		Type:        "issue_comment",
		Description: "PR comment from alice",
		Data:        "anything",
		URL:         url,
		UserID:      org.UserID,
	})

	// Assert: the task was restarted.
	assert.NilError(t, err)
	assert.Equal(t, n, 1)
	updated, err := s.GetTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, updated.Status, model.TaskStatusPending)

	// Waking a done task records the external trigger that drove the restart. No
	// RESTARTED lifecycle event is emitted: it would be redundant with the
	// external event, which already explains why the task woke. Restarted
	// lifecycle events are reserved for user-initiated restarts via RestartTask.
	events, err := s.ListEventsByTask(t.Context(), nil, task.ID, org.OrgID, nil)
	assert.NilError(t, err)
	assert.DeepEqual(t,
		model.FilterPayloads(events, model.EventTypeExternal, model.EventTypeLifecycle),
		[]model.EventPayload{
			&model.ExternalPayload{
				Description: "PR comment from alice",
				URL:         url,
				Data:        "anything",
				Source:      "github",
				EventType:   "issue_comment",
			},
		},
	)
}

func TestRouteCreateRuleSpawnsTask(t *testing.T) {
	t.Parallel()

	// Arrange
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	url := "https://github.com/owner/repo/issues/1"
	err := s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, []model.RoutingRule{{
		Source:     "github",
		Conditions: []model.Condition{{Attr: "mention", Op: "equals", Value: "icholy-bot"}},
		Create: &model.CreateTaskAction{
			Workspace: "default",
			Runner:    "test-runner",
			Prompt:    "Triage this issue.",
		},
	}})
	assert.NilError(t, err)
	r := &eventrouter.Router{Log: slog.Default(), Store: s}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source:      "github",
		Type:        "issue_comment",
		Description: "alice commented on issue #1",
		Data:        "@icholy-bot please look at this",
		URL:         url,
		UserID:      org.UserID,
		Attrs:       eventrouter.Attrs{"mention": {"icholy-bot"}},
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, n, 1)

	tasks, err := s.ListTasks(t.Context(), nil, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(tasks), 1)
	task := tasks[0]
	assert.Equal(t, task.Workspace, "default")
	assert.Equal(t, task.Runner, "test-runner")
	assert.Equal(t, task.Status, model.TaskStatusPending)
	assert.Equal(t, task.Command, model.TaskCommandStart)
	// A custom prompt replaces the default preamble entirely — the task gets
	// exactly the configured prompt and nothing else.
	events, err := s.ListEventsByTask(t.Context(), nil, task.ID, org.OrgID, []string{model.EventTypeInstruction})
	assert.NilError(t, err)
	assert.DeepEqual(t,
		model.FilterPayloads(events, model.EventTypeInstruction),
		[]model.EventPayload{
			&model.InstructionPayload{Text: "Triage this issue."},
		},
	)

	// The subscription link keeps the trigger URL but carries no title — the
	// trigger's description lives on the external event instead, so the link no
	// longer double-duties as the trigger label.
	links, err := s.ListLinksByTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(links), 1)
	assert.Equal(t, links[0].URL, url)
	assert.Equal(t, links[0].Subscribe, true)
	assert.Equal(t, links[0].Title, "")

	// The create path appends a link event mirroring the task_links row (the
	// timeline source of truth; task_links is the projection), alongside the
	// instruction event it already seeds.
	linkEvents, err := s.ListEventsByTask(t.Context(), nil, task.ID, org.OrgID, []string{model.EventTypeLink})
	assert.NilError(t, err)
	assert.DeepEqual(t,
		model.FilterPayloads(linkEvents, model.EventTypeLink),
		[]model.EventPayload{
			&model.LinkPayload{LinkID: links[0].ID, Relevance: "trigger", URL: url, Subscribe: true},
		},
	)
	assert.Equal(t, linkEvents[0].Wake, false)

	// The timeline (ordered by event id) must read
	// External -> Created -> Link -> Instruction: the trigger event that caused
	// the task to exist first (the same way a woken task surfaces its trigger),
	// then the lifecycle event, the link that triggered the task, and finally the
	// prompt the agent acts on.
	allEvents, err := s.ListEventsByTask(t.Context(), nil, task.ID, org.OrgID, nil)
	assert.NilError(t, err)
	var types []string
	for _, e := range allEvents {
		types = append(types, e.Payload.Type())
	}
	assert.DeepEqual(t, types, []string{
		model.EventTypeExternal,
		model.EventTypeLifecycle,
		model.EventTypeLink,
		model.EventTypeInstruction,
	})

	// The trigger surfaces as an external event carrying the webhook payload,
	// mirroring the wake path (attach).
	externalEvents, err := s.ListEventsByTask(t.Context(), nil, task.ID, org.OrgID, []string{model.EventTypeExternal})
	assert.NilError(t, err)
	assert.DeepEqual(t,
		model.FilterPayloads(externalEvents, model.EventTypeExternal),
		[]model.EventPayload{
			&model.ExternalPayload{Description: "alice commented on issue #1", URL: url, Data: "@icholy-bot please look at this", Source: "github", EventType: "issue_comment"},
		},
	)
}

func TestRouteCreateRuleWithoutPromptUsesDefaultPreamble(t *testing.T) {
	t.Parallel()

	// Arrange — a create rule with no custom prompt falls back to the default
	// preamble orienting the agent with the event source/type. It seeds exactly
	// one instruction (no custom prompt appended).
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	url := "https://github.com/owner/repo/issues/1"
	err := s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, []model.RoutingRule{{
		Source: "github",
		Create: &model.CreateTaskAction{Workspace: "default", Runner: "test-runner"},
	}})
	assert.NilError(t, err)
	r := &eventrouter.Router{Log: slog.Default(), Store: s}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source:      "github",
		Type:        "issue_comment",
		Description: "alice commented on issue #1",
		Data:        "@icholy-bot please look at this",
		URL:         url,
		UserID:      org.UserID,
		Attrs:       eventrouter.Attrs{"mention": {"icholy-bot"}},
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, n, 1)
	tasks, err := s.ListTasks(t.Context(), nil, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(tasks), 1)
	task := tasks[0]
	events, err := s.ListEventsByTask(t.Context(), nil, task.ID, org.OrgID, []string{model.EventTypeInstruction})
	assert.NilError(t, err)
	assert.DeepEqual(t,
		model.FilterPayloads(events, model.EventTypeInstruction),
		[]model.EventPayload{
			&model.InstructionPayload{
				Text: "You were created by a routing rule in response to a github issue_comment event.",
			},
		},
	)
}

func TestRouteCreateRuleAutoArchive(t *testing.T) {
	t.Parallel()

	// A create rule carries its AutoArchive through to the spawned task; omitting
	// it leaves the task at the "never auto-archive" default (zero duration).
	tests := []struct {
		name        string
		autoArchive time.Duration
		want        time.Duration
	}{
		{name: "explicit duration is applied", autoArchive: 24 * time.Hour, want: 24 * time.Hour},
		{name: "omitted defaults to never", autoArchive: 0, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Arrange
			s := teststore.New(t)
			org := teststore.CreateOrg(t, s, nil)
			url := "https://github.com/owner/repo/issues/1"
			err := s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, []model.RoutingRule{{
				Source: "github",
				Create: &model.CreateTaskAction{
					Workspace:   "default",
					Runner:      "test-runner",
					AutoArchive: tt.autoArchive,
				},
			}})
			assert.NilError(t, err)
			r := &eventrouter.Router{Log: slog.Default(), Store: s}

			// Act
			n, err := r.Route(t.Context(), eventrouter.InputEvent{
				Source: "github",
				Type:   "issue_comment",
				Data:   "hello",
				URL:    url,
				UserID: org.UserID,
			})

			// Assert
			assert.NilError(t, err)
			assert.Equal(t, n, 1)
			tasks, err := s.ListTasks(t.Context(), nil, org.OrgID)
			assert.NilError(t, err)
			assert.Equal(t, len(tasks), 1)
			assert.Equal(t, tasks[0].AutoArchive, tt.want)
		})
	}
}

func TestRouteCreateRuleThatDoesNotMatch(t *testing.T) {
	t.Parallel()

	// Arrange: a create-rule that requires a mention, an event without one.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	url := "https://github.com/owner/repo/issues/8"
	err := s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, []model.RoutingRule{{
		Source:     "github",
		Conditions: []model.Condition{{Attr: "mention", Op: "equals", Value: "bot"}},
		Create:     &model.CreateTaskAction{Workspace: "default", Runner: "r"},
	}})
	assert.NilError(t, err)
	r := &eventrouter.Router{Log: slog.Default(), Store: s}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		Data:   "no mention here",
		URL:    url,
		UserID: org.UserID,
	})
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
	tasks, err := s.ListTasks(t.Context(), nil, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(tasks), 0)
}

func TestRouteRedeliveryDedup(t *testing.T) {
	t.Parallel()

	// Arrange: sequential replay. The first Route creates the task and a
	// subscribed link; the second Route sees that link via the routing-level
	// FindSubscribedLinksForOrgs lookup and takes the wake path instead of
	// creating a duplicate. (Genuinely-concurrent overlapping txns can still
	// produce duplicates — accepted as a v1 limitation.)
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	url := "https://github.com/owner/repo/issues/3"
	err := s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, []model.RoutingRule{{
		Source: "github",
		Create: &model.CreateTaskAction{Workspace: "default", Runner: "r"},
	}})
	assert.NilError(t, err)
	r := &eventrouter.Router{Log: slog.Default(), Store: s}

	input := eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		Data:   "trigger",
		URL:    url,
		UserID: org.UserID,
	}

	// Act: two back-to-back identical events
	_, err = r.Route(t.Context(), input)
	assert.NilError(t, err)
	_, err = r.Route(t.Context(), input)
	assert.NilError(t, err)

	// Assert: only one task and one subscribed link exist
	tasks, err := s.ListTasks(t.Context(), nil, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(tasks), 1)
	links, err := s.FindSubscribedLinksForOrgs(t.Context(), nil, url, []int64{org.OrgID})
	assert.NilError(t, err)
	assert.Equal(t, len(links[org.OrgID]), 1)
}

func TestRouteFirstMatchingRuleWins(t *testing.T) {
	t.Parallel()

	// Arrange: wake-only rule ordered before create-rule shadows the create.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	url := "https://github.com/owner/repo/issues/4"
	err := s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, []model.RoutingRule{
		{Source: "github"}, // wake-only, matches
		{Source: "github", Create: &model.CreateTaskAction{Workspace: "default", Runner: "r"}},
	})
	assert.NilError(t, err)
	r := &eventrouter.Router{Log: slog.Default(), Store: s}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		Data:   "hi",
		URL:    url,
		UserID: org.UserID,
	})
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
	tasks, err := s.ListTasks(t.Context(), nil, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(tasks), 0)

	// Reorder so the create-rule comes first.
	err = s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, []model.RoutingRule{
		{Source: "github", Create: &model.CreateTaskAction{Workspace: "default", Runner: "r"}},
		{Source: "github"},
	})
	assert.NilError(t, err)
	n, err = r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		Data:   "hi",
		URL:    url,
		UserID: org.UserID,
	})
	assert.NilError(t, err)
	assert.Equal(t, n, 1)
	tasks, err = s.ListTasks(t.Context(), nil, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(tasks), 1)
}

func TestRoutePerOrgIsolation(t *testing.T) {
	t.Parallel()

	// Arrange: user in org A (matching link, no create rule) and org B (no link, create rule).
	s := teststore.New(t)
	orgA := teststore.CreateOrg(t, s, nil)
	orgB := teststore.CreateOrg(t, s, nil)
	assert.NilError(t, s.AddOrgMember(t.Context(), nil, &model.OrgMember{
		OrgID: orgB.OrgID, UserID: orgA.UserID, Role: "member",
	}))
	url := "https://github.com/owner/repo/issues/5"
	teststore.CreateTask(t, s, orgA, &teststore.TaskOptions{
		Status: model.TaskStatusCompleted,
		Links:  []teststore.LinkOptions{{URL: url, Subscribe: true}},
	})
	// Org A has a wake rule reproducing the former shipped default (gritz: body-prefix).
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, orgA.OrgID, []model.RoutingRule{
		{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "gritz:"}}, Wakeup: true},
	}))
	err := s.SetOrgRoutingRules(t.Context(), nil, orgB.OrgID, []model.RoutingRule{{
		Source: "github",
		Create: &model.CreateTaskAction{Workspace: "default", Runner: "r"},
	}})
	assert.NilError(t, err)
	r := &eventrouter.Router{Log: slog.Default(), Store: s}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		Data:   "gritz: do it", // matches org A's wake rule; matches org B's source-only rule
		URL:    url,
		UserID: orgA.UserID,
	})
	assert.NilError(t, err)
	assert.Equal(t, n, 2)

	tasksA, err := s.ListTasks(t.Context(), nil, orgA.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(tasksA), 1)
	assert.Equal(t, tasksA[0].Status, model.TaskStatusPending)

	tasksB, err := s.ListTasks(t.Context(), nil, orgB.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(tasksB), 1)
	assert.Equal(t, tasksB[0].Status, model.TaskStatusPending)
}

func TestRouteLinkQueryScopedToMatchedOrgs(t *testing.T) {
	t.Parallel()

	// Arrange: user in orgs A, B, C. Only B has a matching rule. A has a link
	// at the URL, C has no rule and no link. A's link must NOT cause a wake
	// because no rule matched in A.
	s := teststore.New(t)
	orgA := teststore.CreateOrg(t, s, nil)
	orgB := teststore.CreateOrg(t, s, nil)
	orgC := teststore.CreateOrg(t, s, nil)
	assert.NilError(t, s.AddOrgMember(t.Context(), nil, &model.OrgMember{
		OrgID: orgB.OrgID, UserID: orgA.UserID, Role: "member",
	}))
	assert.NilError(t, s.AddOrgMember(t.Context(), nil, &model.OrgMember{
		OrgID: orgC.OrgID, UserID: orgA.UserID, Role: "member",
	}))
	url := "https://github.com/owner/repo/issues/6"
	taskA := teststore.CreateTask(t, s, orgA, &teststore.TaskOptions{
		Status: model.TaskStatusCompleted,
		Links:  []teststore.LinkOptions{{URL: url, Subscribe: true}},
	})
	// Org A has rules that don't match this event (mention required, none present).
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, orgA.OrgID, []model.RoutingRule{
		{Source: "github", Conditions: []model.Condition{{Attr: "mention", Op: "equals", Value: "someone-else"}}},
	}))
	// Org B has a matching create rule.
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, orgB.OrgID, []model.RoutingRule{
		{Source: "github", Create: &model.CreateTaskAction{Workspace: "default", Runner: "r"}},
	}))
	// Org C has empty rules — a ruleless org matches nothing.
	r := &eventrouter.Router{Log: slog.Default(), Store: s}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		Data:   "no mention, no gritz prefix",
		URL:    url,
		UserID: orgA.UserID,
	})
	assert.NilError(t, err)
	assert.Equal(t, n, 1) // only org B creates

	// Org A's task is untouched.
	updated, err := s.GetTask(t.Context(), nil, taskA.ID, orgA.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, updated.Status, model.TaskStatusCompleted)

	// Org B has the newly created task.
	tasksB, err := s.ListTasks(t.Context(), nil, orgB.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(tasksB), 1)

	// Org C has no tasks.
	tasksC, err := s.ListTasks(t.Context(), nil, orgC.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(tasksC), 0)
}

func TestRouteRuleLessOrgRoutesNothing(t *testing.T) {
	t.Parallel()

	// Arrange: a member org with no routing rules. The producers no longer ship
	// default rules, so even a body carrying the former default's "gritz:" prefix
	// routes nothing and the subscribed task stays put.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	url := "https://github.com/owner/repo/pull/7"
	task := teststore.CreateTask(t, s, org, &teststore.TaskOptions{
		Status: model.TaskStatusCompleted,
		Links:  []teststore.LinkOptions{{URL: url, Subscribe: true}},
	})
	r := &eventrouter.Router{Log: slog.Default(), Store: s}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		Data:   "gritz: do it",
		URL:    url,
		UserID: org.UserID,
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
	updated, err := s.GetTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, updated.Status, model.TaskStatusCompleted)
}

func TestRouteAssignmentCreatesTaskAndLink(t *testing.T) {
	t.Parallel()

	// Arrange: create-rule gated on assignee for pull_request_assigned events.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	url := "https://github.com/owner/repo/pull/9"
	err := s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, []model.RoutingRule{{
		Source:     "github",
		Type:       "pull_request_assigned",
		Conditions: []model.Condition{{Attr: "assignee", Op: "equals", Value: "icholy-bot"}},
		Create:     &model.CreateTaskAction{Workspace: "default", Runner: "r", Prompt: "Review it."},
	}})
	assert.NilError(t, err)
	r := &eventrouter.Router{Log: slog.Default(), Store: s}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source:      "github",
		Type:        "pull_request_assigned",
		Description: "alice assigned PR #9 to @icholy-bot",
		URL:         url,
		UserID:      org.UserID,
		Attrs:       eventrouter.Attrs{"assignee": {"icholy-bot"}},
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, n, 1)

	tasks, err := s.ListTasks(t.Context(), nil, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(tasks), 1)
	assert.Equal(t, tasks[0].Workspace, "default")
	assert.Equal(t, tasks[0].Runner, "r")

	links, err := s.FindSubscribedLinksForOrgs(t.Context(), nil, url, []int64{org.OrgID})
	assert.NilError(t, err)
	assert.Equal(t, len(links[org.OrgID]), 1)
	assert.Equal(t, links[org.OrgID][0].URL, url)
	// The router derives routing_key so the created link is matchable; for an
	// already-canonical URL it equals url.
	assert.Equal(t, links[org.OrgID][0].RoutingKey, url)
}

func TestRouteAssignmentCreateThenCommentWakes(t *testing.T) {
	t.Parallel()

	// Arrange: two rules — an assignment create-rule and a comment wake-rule. This
	// exercises the create-then-wake path across event types: the first event
	// (assignment) creates the task and a subscribed link; a later comment on the
	// same URL matches a different rule and wakes the existing task instead of
	// spawning a duplicate.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	url := "https://github.com/owner/repo/pull/9"
	err := s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, []model.RoutingRule{
		{
			Source:     "github",
			Type:       "pull_request_assigned",
			Conditions: []model.Condition{{Attr: "assignee", Op: "equals", Value: "icholy-bot"}},
			Create:     &model.CreateTaskAction{Workspace: "default", Runner: "r"},
		},
		{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "gritz:"}}, Wakeup: true},
	})
	assert.NilError(t, err)
	r := &eventrouter.Router{Log: slog.Default(), Store: s}

	// Act: first event creates the task and a subscribed link.
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "pull_request_assigned",
		URL:    url,
		UserID: org.UserID,
		Attrs:  eventrouter.Attrs{"assignee": {"icholy-bot"}},
	})
	assert.NilError(t, err)
	assert.Equal(t, n, 1)

	tasks, err := s.ListTasks(t.Context(), nil, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(tasks), 1)
	created := tasks[0]

	// Bring the task back to a wakeable state so the wake path is observable.
	created.Status = model.TaskStatusCompleted
	created.Command = model.TaskCommandNone
	assert.NilError(t, s.UpdateTask(t.Context(), nil, created))

	// Act: a subsequent comment on the same URL wakes the existing task —
	// no second task is created.
	n, err = r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		Data:   "gritz: please look",
		URL:    url,
		UserID: org.UserID,
	})
	assert.NilError(t, err)
	assert.Equal(t, n, 1)

	tasks, err = s.ListTasks(t.Context(), nil, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(tasks), 1)
	updated, err := s.GetTask(t.Context(), nil, created.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, updated.Status, model.TaskStatusPending)
	assert.Equal(t, updated.Command, model.TaskCommandStart)
}

func TestRouteNonMemberOrgMatchesOnlyPublicRule(t *testing.T) {
	t.Parallel()

	// Arrange: an org the actor is NOT a member of, named in input.Orgs. It has a
	// non-public rule and a public one. Only the public rule is eligible.
	s := teststore.New(t)
	actorOrg := teststore.CreateOrg(t, s, nil) // supplies a valid non-member actor
	nonMemberOrg := teststore.CreateOrg(t, s, nil)
	url := "https://github.com/owner/repo/issues/1"
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, nonMemberOrg.OrgID, []model.RoutingRule{
		// Non-public rule matches the event but must be skipped for a non-member.
		{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "mention", Op: "equals", Value: "icholy-bot"}}},
		// Public create-rule is the only eligible one.
		{
			Source:     "github",
			Type:       "issue_comment",
			Public:     true,
			Conditions: []model.Condition{{Attr: "mention", Op: "equals", Value: "icholy-bot"}},
			Create:     &model.CreateTaskAction{Workspace: "default", Runner: "r"},
		},
	}))
	r := &eventrouter.Router{Log: slog.Default(), Store: s}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		Data:   "@icholy-bot please look",
		URL:    url,
		UserID: actorOrg.UserID,
		Orgs:   []int64{nonMemberOrg.OrgID},
		Attrs:  eventrouter.Attrs{"mention": {"icholy-bot"}},
	})

	// Assert: the public rule fired and created a task in the non-member org.
	assert.NilError(t, err)
	assert.Equal(t, n, 1)
	tasks, err := s.ListTasks(t.Context(), nil, nonMemberOrg.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(tasks), 1)
}

func TestRouteNonMemberOrgWithoutPublicRuleIsNoOp(t *testing.T) {
	t.Parallel()

	// Arrange: a non-member org whose matching rule is not public — it must not
	// fire.
	s := teststore.New(t)
	actorOrg := teststore.CreateOrg(t, s, nil)
	nonMemberOrg := teststore.CreateOrg(t, s, nil)
	url := "https://github.com/owner/repo/issues/1"
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, nonMemberOrg.OrgID, []model.RoutingRule{
		{Source: "github", Type: "issue_comment", Create: &model.CreateTaskAction{Workspace: "default", Runner: "r"}},
	}))
	r := &eventrouter.Router{Log: slog.Default(), Store: s}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		Data:   "gritz: do it",
		URL:    url,
		UserID: actorOrg.UserID,
		Orgs:   []int64{nonMemberOrg.OrgID},
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
	tasks, err := s.ListTasks(t.Context(), nil, nonMemberOrg.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(tasks), 0)
}

func TestRouteRuleLessNonMemberOrgRoutesNothing(t *testing.T) {
	t.Parallel()

	// Arrange: a non-member org with no routing rules at all. A ruleless org
	// matches nothing, so nothing routes even though the body carries the
	// "gritz:" prefix a rule could otherwise match.
	s := teststore.New(t)
	actorOrg := teststore.CreateOrg(t, s, nil)
	nonMemberOrg := teststore.CreateOrg(t, s, nil)
	url := "https://github.com/owner/repo/issues/1"
	teststore.CreateTask(t, s, nonMemberOrg, &teststore.TaskOptions{
		Status: model.TaskStatusCompleted,
		Links:  []teststore.LinkOptions{{URL: url, Subscribe: true}},
	})
	r := &eventrouter.Router{Log: slog.Default(), Store: s}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		Data:   "gritz: do it",
		URL:    url,
		UserID: actorOrg.UserID,
		Orgs:   []int64{nonMemberOrg.OrgID},
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, n, 0)
}

func TestRouteEmptyUserWithOrgsRoutesPublicRule(t *testing.T) {
	t.Parallel()

	// Arrange: an unlinked actor (empty UserID) — the member branch returns
	// nothing, so routing is driven entirely by input.Orgs and public rules.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	url := "https://github.com/owner/repo/issues/1"
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, []model.RoutingRule{{
		Source: "github",
		Type:   "issue_comment",
		Public: true,
		Create: &model.CreateTaskAction{Workspace: "default", Runner: "r"},
	}}))
	r := &eventrouter.Router{Log: slog.Default(), Store: s}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		Data:   "@icholy-bot please look",
		URL:    url,
		UserID: "",
		Orgs:   []int64{org.OrgID},
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, n, 1)
	tasks, err := s.ListTasks(t.Context(), nil, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(tasks), 1)
}

func TestRouteMemberOrgInOrgsUsesFullRuleSet(t *testing.T) {
	t.Parallel()

	// Arrange: an org that is both the actor's member org AND named in input.Orgs.
	// Membership wins (the store returns it once, IsMember=true), so its
	// non-public rule is still eligible — the actor is not down-scoped to the
	// public subset.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	url := "https://github.com/owner/repo/issues/1"
	task := teststore.CreateTask(t, s, org, &teststore.TaskOptions{
		Status: model.TaskStatusCompleted,
		Links:  []teststore.LinkOptions{{URL: url, Subscribe: true}},
	})
	// A non-public wake rule — only eligible if the org is treated as a member org.
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, []model.RoutingRule{
		{Source: "github", Type: "issue_comment", Wakeup: true},
	}))
	r := &eventrouter.Router{Log: slog.Default(), Store: s}

	// Act: pass the member org's own id in Orgs.
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		Data:   "anything",
		URL:    url,
		UserID: org.UserID,
		Orgs:   []int64{org.OrgID},
	})

	// Assert: the non-public rule matched and woke the task.
	assert.NilError(t, err)
	assert.Equal(t, n, 1)
	updated, err := s.GetTask(t.Context(), nil, task.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, updated.Status, model.TaskStatusPending)
}

func TestRouteNamespacedCreateRuleFiresPastDefaultSubscriber(t *testing.T) {
	t.Parallel()

	// The motivating scenario: an implementing task in the default namespace already
	// subscribes to the PR. A reviewbot-namespace create rule (wakeup off) must still
	// spawn a reviewer instead of silently attaching to that default subscriber,
	// because the subscription check is now scoped to the rule's namespace.
	s := teststore.New(t)
	org := teststore.CreateOrg(t, s, nil)
	url := "https://github.com/owner/repo/pull/1"

	// Default-namespace subscriber (the implementing task) present on the PR.
	implementer := teststore.CreateTask(t, s, org, &teststore.TaskOptions{
		Status: model.TaskStatusCompleted,
		Links:  []teststore.LinkOptions{{URL: url, Subscribe: true}},
	})

	// A reviewbot-namespace create rule keyed on the reviewbot label.
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, []model.RoutingRule{{
		Source:     "github",
		Type:       "label_added",
		Namespace:  "reviewbot",
		Conditions: []model.Condition{{Attr: "label", Op: "equals", Value: "reviewbot"}},
		Create:     &model.CreateTaskAction{Workspace: "default", Runner: "r"},
	}}))
	r := &eventrouter.Router{Log: slog.Default(), Store: s}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "label_added",
		URL:    url,
		UserID: org.UserID,
		Attrs:  eventrouter.Attrs{"label": {"reviewbot"}},
	})

	// Assert: the reviewer was created, not attached to the implementer.
	assert.NilError(t, err)
	assert.Equal(t, n, 1)

	tasks, err := s.ListTasks(t.Context(), nil, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, len(tasks), 2)

	// The default-namespace implementer is untouched — the reviewbot rule never saw it.
	updated, err := s.GetTask(t.Context(), nil, implementer.ID, org.OrgID)
	assert.NilError(t, err)
	assert.Equal(t, updated.Status, model.TaskStatusCompleted)
	assert.Equal(t, updated.Namespace, "")

	// The freshly created reviewer is stamped with the rule's namespace.
	var reviewer *model.Task
	for _, tk := range tasks {
		if tk.ID != implementer.ID {
			reviewer = tk
		}
	}
	assert.Assert(t, reviewer != nil)
	assert.Equal(t, reviewer.Namespace, "reviewbot")
	assert.Equal(t, reviewer.Status, model.TaskStatusPending)
}

func TestRouteOnRouteOutcome(t *testing.T) {
	t.Parallel()

	// The OnRouteOutcome callback reports what Route did per matched org. Each
	// case drives a different terminal state: a create-rule fires it with
	// Created=true, a subscribed link fires it with Created=false, a wake-only
	// rule with no link fires it "matched but nothing done", and an event that
	// matches no rule never fires it at all.
	const url = "https://github.com/owner/repo/issues/1"
	tests := []struct {
		name        string
		rules       []model.RoutingRule
		link        bool // seed a subscribed done task at url
		data        string
		wantN       int
		wantFire    bool
		wantCreated bool
		wantTaskIDs int
	}{
		{
			name:        "fires on create",
			rules:       []model.RoutingRule{{Source: "github", Create: &model.CreateTaskAction{Workspace: "default", Runner: "r"}}},
			data:        "hello",
			wantN:       1,
			wantFire:    true,
			wantCreated: true,
			wantTaskIDs: 1,
		},
		{
			name:        "fires on wake",
			rules:       []model.RoutingRule{{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "gritz:"}}, Wakeup: true}},
			link:        true,
			data:        "gritz: fix tests",
			wantN:       1,
			wantFire:    true,
			wantCreated: false,
			wantTaskIDs: 1,
		},
		{
			name:        "fires on match with nothing to do",
			rules:       []model.RoutingRule{{Source: "github"}},
			data:        "no link, no create",
			wantN:       0,
			wantFire:    true,
			wantCreated: false,
			wantTaskIDs: 0,
		},
		{
			name:     "does not fire when no rule matches",
			link:     true,
			data:     "just a regular comment",
			wantN:    0,
			wantFire: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Arrange
			s := teststore.New(t)
			org := teststore.CreateOrg(t, s, nil)
			if tt.link {
				teststore.CreateTask(t, s, org, &teststore.TaskOptions{
					Status: model.TaskStatusCompleted,
					Links:  []teststore.LinkOptions{{URL: url, Subscribe: true}},
				})
			}
			if tt.rules != nil {
				assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, org.OrgID, tt.rules))
			}
			var outcomes []eventrouter.RouteOutcome
			r := &eventrouter.Router{
				Log:   slog.Default(),
				Store: s,
				OnRouteOutcome: func(_ context.Context, o eventrouter.RouteOutcome) {
					outcomes = append(outcomes, o)
				},
			}

			// Act
			n, err := r.Route(t.Context(), eventrouter.InputEvent{
				Source: "github",
				Type:   "issue_comment",
				Data:   tt.data,
				URL:    url,
				UserID: org.UserID,
			})

			// Assert
			assert.NilError(t, err)
			assert.Equal(t, n, tt.wantN)
			if !tt.wantFire {
				assert.Equal(t, len(outcomes), 0)
				return
			}
			assert.Equal(t, len(outcomes), 1)
			got := outcomes[0]
			assert.Equal(t, got.OrgID, org.OrgID)
			assert.Equal(t, got.Input.URL, url)
			assert.Equal(t, got.Rule.Source, "github")
			assert.Equal(t, got.Created, tt.wantCreated)
			assert.Equal(t, got.Rule.Create != nil, tt.wantCreated)
			assert.Equal(t, len(got.TaskIDs), tt.wantTaskIDs)
		})
	}
}

func TestRouteOnRouteOutcomeFiresOncePerMatchedOrg(t *testing.T) {
	t.Parallel()

	// Arrange: user in two orgs, both with a subscribed link at the URL. This also
	// covers the multi-org wake fan-out (n == 2, one woken task per org).
	s := teststore.New(t)
	orgA := teststore.CreateOrg(t, s, nil)
	orgB := teststore.CreateOrg(t, s, nil)
	url := "https://github.com/owner/repo/pull/1"
	taskA := teststore.CreateTask(t, s, orgA, &teststore.TaskOptions{
		Status: model.TaskStatusCompleted,
		Links:  []teststore.LinkOptions{{URL: url, Subscribe: true}},
	})
	taskB := teststore.CreateTask(t, s, orgB, &teststore.TaskOptions{
		Status: model.TaskStatusCompleted,
		Links:  []teststore.LinkOptions{{URL: url, Subscribe: true}},
	})
	assert.NilError(t, s.AddOrgMember(t.Context(), nil, &model.OrgMember{
		OrgID:  orgB.OrgID,
		UserID: orgA.UserID,
		Role:   "member",
	}))
	// Both orgs carry a wake rule reproducing the former shipped default.
	wakeRule := []model.RoutingRule{{Source: "github", Type: "issue_comment", Conditions: []model.Condition{{Attr: "body", Op: "prefix", Value: "gritz:"}}, Wakeup: true}}
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, orgA.OrgID, wakeRule))
	assert.NilError(t, s.SetOrgRoutingRules(t.Context(), nil, orgB.OrgID, wakeRule))

	byOrg := map[int64]eventrouter.RouteOutcome{}
	r := &eventrouter.Router{
		Log:   slog.Default(),
		Store: s,
		OnRouteOutcome: func(_ context.Context, o eventrouter.RouteOutcome) {
			byOrg[o.OrgID] = o
		},
	}

	// Act
	n, err := r.Route(t.Context(), eventrouter.InputEvent{
		Source: "github",
		Type:   "issue_comment",
		Data:   "gritz: do something",
		URL:    url,
		UserID: orgA.UserID,
	})

	// Assert: fired once per matched org with the right woken task.
	assert.NilError(t, err)
	assert.Equal(t, n, 2)
	assert.Equal(t, len(byOrg), 2)
	assert.DeepEqual(t, byOrg[orgA.OrgID].TaskIDs, []int64{taskA.ID})
	assert.DeepEqual(t, byOrg[orgB.OrgID].TaskIDs, []int64{taskB.ID})
	assert.Equal(t, byOrg[orgA.OrgID].Created, false)
	assert.Equal(t, byOrg[orgB.OrgID].Created, false)
}
