package api_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/e2e/harness"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/store"
	"github.com/gritzapp/gritz/internal/x/cmpx"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/durationpb"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestCreateAgent(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	agent := &gritzv1.Agent{
		Name:        "implementer",
		Workspace:   harness.DefaultWorkspace,
		Runner:      harness.DefaultRunner,
		Prompt:      "Implement the issue and open a pull request.",
		AutoArchive: durationpb.New(24 * time.Hour),
		Triggers: []*gritzv1.AgentTrigger{
			{
				Source:     "atlassian",
				Type:       "label_added",
				Conditions: []*gritzv1.RuleCondition{{Attr: "label", Op: "equals", Value: "build-it"}},
				YieldTo:    []string{"pr-owner"},
				Link:       "jira.issue",
			},
			{
				Source:     "github",
				Type:       "issue_comment",
				Conditions: []*gritzv1.RuleCondition{{Attr: "mention", Op: "equals", Value: "gritz"}},
				Public:     true,
				Link:       "owned-pr",
			},
		},
		Links: []*gritzv1.AgentLink{
			{Type: "jira.issue", Events: []*gritzv1.AgentLinkEvent{{Type: "comment_created"}}},
			{
				Type:   "github.pr",
				Kind:   "owned-pr",
				Claim:  "pr-owner",
				Events: []*gritzv1.AgentLinkEvent{{Type: "pull_request_review_comment"}, {Type: "pull_request_review"}, {Type: "pull_request_closed"}},
				Public: true,
			},
			{Type: "github.pr", Events: []*gritzv1.AgentLinkEvent{{Type: "pull_request_closed"}}},
		},
	}

	// Act
	resp, err := h.Org.Client.CreateAgent(t.Context(), &gritzv1.CreateAgentRequest{Agent: agent})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, resp.Agent.Id != 0)
	want := proto.CloneOf(agent)
	want.Id = resp.Agent.Id
	assert.DeepEqual(t, resp.Agent, want, protocmp.Transform())
	assert.DeepEqual(t,
		h.Org.GetAgent(&gritzv1.GetAgentRequest{Id: resp.Agent.Id}),
		want,
		protocmp.Transform(),
	)
}

func TestCreateAgent_IgnoresID(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	existing := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "first",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
	}})

	// Act
	resp, err := h.Org.Client.CreateAgent(t.Context(), &gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Id:        existing.Id,
		Name:      "second",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
	}})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, resp.Agent.Id != existing.Id)
	assert.Equal(t, h.Org.GetAgent(&gritzv1.GetAgentRequest{Id: existing.Id}).Name, "first")
}

func TestCreateAgent_CustomEventType(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	_, err := h.Org.Client.SetCustomEventTypes(t.Context(), &gritzv1.SetCustomEventTypesRequest{
		EventTypes: []*gritzv1.EventTypeDef{{
			Source:    "custom",
			Type:      "problem",
			Label:     "Monitoring: Problem",
			Attrs:     []*gritzv1.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity"}},
			LinkTypes: []string{"custom.url"},
		}},
	})
	assert.NilError(t, err)
	agent := &gritzv1.Agent{
		Name:        "responder",
		Workspace:   harness.DefaultWorkspace,
		Runner:      harness.DefaultRunner,
		AutoArchive: durationpb.New(0),
		Triggers: []*gritzv1.AgentTrigger{{
			Source:     "custom",
			Type:       "problem",
			Conditions: []*gritzv1.RuleCondition{{Attr: "severity", Op: "equals", Value: "critical"}},
			Public:     true,
			Link:       "custom.url",
		}},
		Links: []*gritzv1.AgentLink{{Type: "custom.url", Events: []*gritzv1.AgentLinkEvent{{Type: "problem"}}}},
	}

	// Act
	resp, err := h.Org.Client.CreateAgent(t.Context(), &gritzv1.CreateAgentRequest{Agent: agent})

	// Assert
	assert.NilError(t, err)
	want := proto.CloneOf(agent)
	want.Id = resp.Agent.Id
	assert.DeepEqual(t, resp.Agent, want, protocmp.Transform())
}

func TestCreateAgent_Invalid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		agent *gritzv1.Agent
		want  string
	}{
		{
			name:  "no name",
			agent: &gritzv1.Agent{Workspace: "story", Runner: "runner-1"},
			want:  "name is required",
		},
		{
			name:  "unknown workspace",
			agent: &gritzv1.Agent{Name: "a", Workspace: "epic", Runner: "runner-1"},
			want:  `workspace "epic" not found on runner "runner-1"`,
		},
		{
			name: "variable not declared by the workspace",
			agent: &gritzv1.Agent{
				Name:      "a",
				Workspace: "story",
				Runner:    "runner-1",
				Variables: map[string]string{"JIRA": "APP-142"},
			},
			want: `workspace "story": variable "JIRA": not declared by the workspace`,
		},
		{
			name: "trigger on an unknown event type",
			agent: &gritzv1.Agent{
				Name:      "a",
				Workspace: "story",
				Runner:    "runner-1",
				Triggers:  []*gritzv1.AgentTrigger{{Source: "github", Type: "star_added"}},
			},
			want: `trigger 1: unknown event type: source="github" type="star_added"`,
		},
		{
			name: "trigger condition on an attr its event type lacks",
			agent: &gritzv1.Agent{
				Name:      "a",
				Workspace: "story",
				Runner:    "runner-1",
				Triggers: []*gritzv1.AgentTrigger{{
					Source:     "github",
					Type:       "issue_comment",
					Conditions: []*gritzv1.RuleCondition{{Attr: "label", Op: "equals", Value: "bug"}},
				}},
			},
			want: `trigger 1: attr "label" not valid for event type "github"/"issue_comment"`,
		},
		{
			name: "trigger link to an unknown kind",
			agent: &gritzv1.Agent{
				Name:      "a",
				Workspace: "story",
				Runner:    "runner-1",
				Triggers:  []*gritzv1.AgentTrigger{{Source: "github", Type: "issue_comment", Link: "owned-pr"}},
				Links:     []*gritzv1.AgentLink{{Type: "github.pr"}},
			},
			want: `trigger 1: unknown link kind "owned-pr"`,
		},
		{
			name: "trigger link of a type its event type doesn't declare",
			agent: &gritzv1.Agent{
				Name:      "a",
				Workspace: "story",
				Runner:    "runner-1",
				Triggers:  []*gritzv1.AgentTrigger{{Source: "github", Type: "push", Link: "github.pr"}},
				Links:     []*gritzv1.AgentLink{{Type: "github.pr"}},
			},
			want: `trigger 1: event type "github"/"push" doesn't declare link type "github.pr" of kind "github.pr"`,
		},
		{
			name: "unknown link type",
			agent: &gritzv1.Agent{
				Name:      "a",
				Workspace: "story",
				Runner:    "runner-1",
				Links:     []*gritzv1.AgentLink{{Type: "github.commit"}},
			},
			want: `link 1: unknown link type "github.commit"`,
		},
		{
			name: "duplicate kind",
			agent: &gritzv1.Agent{
				Name:      "a",
				Workspace: "story",
				Runner:    "runner-1",
				Links:     []*gritzv1.AgentLink{{Type: "github.pr"}, {Type: "github.issue", Kind: "github.pr"}},
			},
			want: `link 2: duplicate kind "github.pr"`,
		},
		{
			name: "link event of an unknown type",
			agent: &gritzv1.Agent{
				Name:      "a",
				Workspace: "story",
				Runner:    "runner-1",
				Links:     []*gritzv1.AgentLink{{Type: "jira.issue", Events: []*gritzv1.AgentLinkEvent{{Type: "comment_created"}, {Type: "issue_deleted"}}}},
			},
			want: `link kind "jira.issue": unknown event type: source="atlassian" type="issue_deleted"`,
		},
		{
			name: "link event that doesn't declare its link type",
			agent: &gritzv1.Agent{
				Name:      "a",
				Workspace: "story",
				Runner:    "runner-1",
				Links:     []*gritzv1.AgentLink{{Type: "github.issue", Events: []*gritzv1.AgentLinkEvent{{Type: "pull_request_review"}}}},
			},
			want: `link kind "github.issue": event type "github"/"pull_request_review" doesn't declare link type "github.issue"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			h := harness.New(t, nil)
			h.Org.RegisterWorkspaces(&gritzv1.RegisterWorkspacesRequest{
				RunnerId:   "runner-1",
				Workspaces: []*gritzv1.RegisteredWorkspace{{Name: "story", Variables: []*gritzv1.VariableDecl{{Name: "BRANCH"}}}},
			})
			existing := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
				Name:      "existing",
				Workspace: "story",
				Runner:    "runner-1",
			}})
			update := proto.CloneOf(tt.agent)
			update.Id = existing.Id

			// Act
			_, createErr := h.Org.Client.CreateAgent(t.Context(), &gritzv1.CreateAgentRequest{Agent: tt.agent})
			_, updateErr := h.Org.Client.UpdateAgent(t.Context(), &gritzv1.UpdateAgentRequest{Agent: update})

			// Assert
			assert.Equal(t, connect.CodeOf(createErr), connect.CodeInvalidArgument)
			assert.ErrorContains(t, createErr, tt.want)
			assert.Equal(t, connect.CodeOf(updateErr), connect.CodeInvalidArgument)
			assert.ErrorContains(t, updateErr, tt.want)
			agents, err := h.Org.Client.ListAgents(t.Context(), &gritzv1.ListAgentsRequest{})
			assert.NilError(t, err)
			assert.DeepEqual(t, agents.Agents, []*gritzv1.Agent{h.Org.DefaultAgent, existing}, protocmp.Transform())
		})
	}
}

func TestListAgents(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	other := h.Server.CreateOrg()
	reviewer := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "reviewer",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
	}})
	implementer := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "implementer",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Links:     []*gritzv1.AgentLink{{Type: "github.pr"}},
	}})
	// Names are labels: a second agent may share one.
	implementer2 := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "implementer",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
	}})
	other.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "elsewhere",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
	}})

	// Act
	resp, err := h.Org.Client.ListAgents(t.Context(), &gritzv1.ListAgentsRequest{})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t,
		resp.Agents,
		[]*gritzv1.Agent{h.Org.DefaultAgent, implementer, implementer2, reviewer},
		protocmp.Transform(),
	)
}

func TestUpdateAgent(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	h.Org.RegisterWorkspaces(&gritzv1.RegisterWorkspacesRequest{
		RunnerId: "runner-1",
		Workspaces: []*gritzv1.RegisteredWorkspace{{
			Name:      "story",
			Variables: []*gritzv1.VariableDecl{{Name: "JIRA_ISSUE", Required: true}},
		}},
	})
	created := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "implementer",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Prompt:    "Implement it.",
		Triggers:  []*gritzv1.AgentTrigger{{Source: "github", Type: "issue_comment", Link: "github.issue"}},
		Links:     []*gritzv1.AgentLink{{Type: "github.issue"}},
	}})
	agent := &gritzv1.Agent{
		Id:          created.Id,
		Name:        "story-writer",
		Workspace:   "story",
		Runner:      "runner-1",
		Prompt:      "Write the story.",
		AutoArchive: durationpb.New(time.Hour),
		Variables:   map[string]string{"JIRA_ISSUE": "APP-142"},
		Triggers:    []*gritzv1.AgentTrigger{{Source: "atlassian", Type: "issue_created"}},
	}

	// Act
	resp, err := h.Org.Client.UpdateAgent(t.Context(), &gritzv1.UpdateAgentRequest{Agent: agent})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, resp.Agent, agent, protocmp.Transform())
	assert.DeepEqual(t,
		h.Org.GetAgent(&gritzv1.GetAgentRequest{Id: created.Id}),
		agent,
		protocmp.Transform(),
	)
}

func TestAgent_OtherOrgNotFound(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	other := h.Server.CreateOrg()
	agent := other.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "elsewhere",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
	}})
	update := proto.CloneOf(agent)
	update.Name = "mine"

	// Act
	_, getErr := h.Org.Client.GetAgent(t.Context(), &gritzv1.GetAgentRequest{Id: agent.Id})
	_, updateErr := h.Org.Client.UpdateAgent(t.Context(), &gritzv1.UpdateAgentRequest{Agent: update})
	_, deleteErr := h.Org.Client.DeleteAgent(t.Context(), &gritzv1.DeleteAgentRequest{Id: agent.Id})

	// Assert
	assert.Equal(t, connect.CodeOf(getErr), connect.CodeNotFound)
	assert.Equal(t, connect.CodeOf(updateErr), connect.CodeNotFound)
	assert.Equal(t, connect.CodeOf(deleteErr), connect.CodeNotFound)
	assert.DeepEqual(t,
		other.GetAgent(&gritzv1.GetAgentRequest{Id: agent.Id}),
		agent,
		protocmp.Transform(),
	)
}

func TestDeleteAgent(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	agent := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "implementer",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
	}})

	// Act
	_, err := h.Org.Client.DeleteAgent(t.Context(), &gritzv1.DeleteAgentRequest{Id: agent.Id})

	// Assert
	assert.NilError(t, err)
	_, err = h.Org.Client.GetAgent(t.Context(), &gritzv1.GetAgentRequest{Id: agent.Id})
	assert.Equal(t, connect.CodeOf(err), connect.CodeNotFound)

	// Act
	_, err = h.Org.Client.DeleteAgent(t.Context(), &gritzv1.DeleteAgentRequest{Id: agent.Id})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeNotFound)
}

func TestDeleteAgent_InUse(t *testing.T) {
	t.Parallel()
	// Arrange - an agent with an unarchived task.
	h := harness.New(t, nil)
	agent := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "implementer",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
	}})
	h.Org.CreateTask(&gritzv1.CreateTaskRequest{AgentId: agent.Id, Draft: true})

	// Act
	_, err := h.Org.Client.DeleteAgent(t.Context(), &gritzv1.DeleteAgentRequest{Id: agent.Id})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeFailedPrecondition)
	assert.Assert(t, cmp.Len(h.Org.ListAgents(&gritzv1.ListAgentsRequest{}), 2))
}

func TestDeleteAgent_DeletesSchedules(t *testing.T) {
	t.Parallel()
	// Arrange - an agent with a schedule.
	h := harness.New(t, nil)
	agent := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "nightly",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Schedules: []*gritzv1.AgentSchedule{{CronExpr: "@daily", Enabled: true}},
	}})

	// Act
	_, err := h.Org.Client.DeleteAgent(t.Context(), &gritzv1.DeleteAgentRequest{Id: agent.Id})

	// Assert - the agent and its schedule are gone.
	assert.NilError(t, err)
	_, err = h.Org.Client.GetAgent(t.Context(), &gritzv1.GetAgentRequest{Id: agent.Id})
	assert.Equal(t, connect.CodeOf(err), connect.CodeNotFound)
	assert.Assert(t, cmp.Len(h.Org.ListSchedules(&gritzv1.ListSchedulesRequest{}), 0))
}

func TestDeleteAgent_ArchivedTasks(t *testing.T) {
	t.Parallel()
	// Arrange - an agent whose only task is archived.
	h := harness.New(t, nil)
	agent := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "implementer",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
	}})
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{AgentId: agent.Id, Draft: true})
	h.Org.ArchiveTask(&gritzv1.ArchiveTaskRequest{Id: task.ID})

	// Act
	_, err := h.Org.Client.DeleteAgent(t.Context(), &gritzv1.DeleteAgentRequest{Id: agent.Id})

	// Assert - the agent is gone, but its archived task still loads with its
	// agent's ID, and can't be unarchived or have tasks created for it.
	assert.NilError(t, err)
	assert.DeepEqual(t,
		h.Org.ListAgents(&gritzv1.ListAgentsRequest{}),
		[]*gritzv1.Agent{h.Org.DefaultAgent},
		protocmp.Transform(),
	)
	_, err = h.Org.Client.GetAgent(t.Context(), &gritzv1.GetAgentRequest{Id: agent.Id})
	assert.Equal(t, connect.CodeOf(err), connect.CodeNotFound)
	assert.DeepEqual(t,
		h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID}),
		&gritzv1.Task{Id: task.ID, Archived: true, AgentId: agent.Id},
		protocmp.Transform(),
		cmpx.OnlyProtoFields(&gritzv1.Task{}, "id", "archived", "agent_id"),
	)
	_, err = h.Org.Client.UnarchiveTask(t.Context(), &gritzv1.UnarchiveTaskRequest{Id: task.ID})
	assert.Equal(t, connect.CodeOf(err), connect.CodeFailedPrecondition)
	assert.Equal(t, h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID}).Archived, true)
	_, err = h.Org.Client.CreateTask(t.Context(), &gritzv1.CreateTaskRequest{AgentId: agent.Id})
	assert.Equal(t, connect.CodeOf(err), connect.CodeNotFound)
}

func TestAgent_RequiresOrgWrite(t *testing.T) {
	t.Parallel()
	// Arrange: a read-only caller, OpOrgRead only, no write.
	h := harness.New(t, nil)
	agent := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "implementer",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
	}})
	key := h.Org.CreateScopedKey(authscope.Scopes{authscope.New(authscope.OpOrgRead)})

	// Act & Assert - reads are allowed.
	got, err := key.Client.GetAgent(t.Context(), &gritzv1.GetAgentRequest{Id: agent.Id})
	assert.NilError(t, err)
	assert.DeepEqual(t, got.Agent, agent, protocmp.Transform())
	list, err := key.Client.ListAgents(t.Context(), &gritzv1.ListAgentsRequest{})
	assert.NilError(t, err)
	assert.DeepEqual(t, list.Agents, []*gritzv1.Agent{h.Org.DefaultAgent, agent}, protocmp.Transform())

	// Act & Assert - writes are denied.
	_, err = key.Client.CreateAgent(t.Context(), &gritzv1.CreateAgentRequest{Agent: agent})
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
	_, err = key.Client.UpdateAgent(t.Context(), &gritzv1.UpdateAgentRequest{Agent: agent})
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
	_, err = key.Client.DeleteAgent(t.Context(), &gritzv1.DeleteAgentRequest{Id: agent.Id})
	assert.Equal(t, connect.CodeOf(err), connect.CodePermissionDenied)
}

func TestAgent_RequiresOrgRead(t *testing.T) {
	t.Parallel()
	// Arrange: a task-only caller with no org scope.
	h := harness.New(t, nil)
	agent := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "implementer",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
	}})
	key := h.Org.CreateScopedKey(authscope.Scopes{authscope.New(authscope.OpTaskRead)})

	// Act
	_, getErr := key.Client.GetAgent(t.Context(), &gritzv1.GetAgentRequest{Id: agent.Id})
	_, listErr := key.Client.ListAgents(t.Context(), &gritzv1.ListAgentsRequest{})

	// Assert
	assert.Equal(t, connect.CodeOf(getErr), connect.CodePermissionDenied)
	assert.Equal(t, connect.CodeOf(listErr), connect.CodePermissionDenied)
}

func TestAgentSchedules(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	agent := &gritzv1.Agent{
		Name:      "dependency-bumper",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Schedules: []*gritzv1.AgentSchedule{
			{Id: 4242, Name: "Nightly bump", CronExpr: "0 3 * * *", Timezone: "Europe/Paris", Enabled: true, LastTaskId: 7},
			{Name: "Weekend bump", CronExpr: "0 9 * * 6"},
		},
	}

	// Act
	resp, err := h.Org.Client.CreateAgent(t.Context(), &gritzv1.CreateAgentRequest{Agent: agent})

	// Assert - every schedule is new: incoming ids and run state are ignored,
	// an enabled one gets its next run, and an empty timezone is UTC.
	assert.NilError(t, err)
	got := resp.Agent.Schedules
	assert.Assert(t, cmp.Len(got, 2))
	assert.Assert(t, got[0].Id != 0 && got[0].Id != 4242 && got[1].Id != 0)
	assert.Assert(t, got[0].NextRunAt != nil)
	assert.DeepEqual(t,
		got,
		[]*gritzv1.AgentSchedule{
			{Id: got[0].Id, Name: "Nightly bump", CronExpr: "0 3 * * *", Timezone: "Europe/Paris", Enabled: true, NextRunAt: got[0].NextRunAt},
			{Id: got[1].Id, Name: "Weekend bump", CronExpr: "0 9 * * 6", Timezone: "UTC"},
		},
		protocmp.Transform(),
	)
	assert.DeepEqual(t, h.Org.GetAgent(&gritzv1.GetAgentRequest{Id: resp.Agent.Id}), resp.Agent, protocmp.Transform())
	assert.DeepEqual(t,
		h.Org.ListAgents(&gritzv1.ListAgentsRequest{}),
		[]*gritzv1.Agent{h.Org.DefaultAgent, resp.Agent},
		protocmp.Transform(),
	)
}

func TestUpdateAgent_Schedules(t *testing.T) {
	t.Parallel()
	// Arrange - an agent with schedules a and b, and another agent's c.
	h := harness.New(t, nil)
	agent := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "bumper",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Schedules: []*gritzv1.AgentSchedule{
			{Name: "a", CronExpr: "@daily"},
			{Name: "b", CronExpr: "@daily"},
		},
	}})
	other := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "other",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Schedules: []*gritzv1.AgentSchedule{{Name: "c", CronExpr: "@daily"}},
	}})
	a, b := agent.Schedules[0], agent.Schedules[1]

	// Act - b renamed and moved first, a new d, and a left out.
	agent.Schedules = []*gritzv1.AgentSchedule{
		{Id: b.Id, Name: "b2", CronExpr: "@daily"},
		{Name: "d", CronExpr: "@hourly"},
	}
	resp, err := h.Org.Client.UpdateAgent(t.Context(), &gritzv1.UpdateAgentRequest{Agent: agent})

	// Assert
	assert.NilError(t, err)
	got := resp.Agent.Schedules
	assert.Assert(t, cmp.Len(got, 2))
	assert.Equal(t, got[0].Id, b.Id)
	assert.Assert(t, got[1].Id != a.Id && got[1].Id != 0)
	assert.DeepEqual(t,
		h.Org.GetAgent(&gritzv1.GetAgentRequest{Id: agent.Id}).Schedules,
		[]*gritzv1.AgentSchedule{
			{Id: b.Id, Name: "b2", CronExpr: "@daily", Timezone: "UTC"},
			{Id: got[1].Id, Name: "d", CronExpr: "@hourly", Timezone: "UTC"},
		},
		protocmp.Transform(),
	)

	// Act - another agent's schedule.
	agent.Schedules = []*gritzv1.AgentSchedule{{Id: other.Schedules[0].Id, Name: "mine", CronExpr: "@daily"}}
	_, err = h.Org.Client.UpdateAgent(t.Context(), &gritzv1.UpdateAgentRequest{Agent: agent})

	// Assert - rejected, and the other agent keeps it.
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
	assert.ErrorContains(t, err, fmt.Sprintf("schedule %d is not a schedule of agent %d", other.Schedules[0].Id, agent.Id))
	assert.DeepEqual(t, h.Org.GetAgent(&gritzv1.GetAgentRequest{Id: other.Id}), other, protocmp.Transform())

	// Act - an invalid cron expression.
	agent.Schedules = []*gritzv1.AgentSchedule{{Name: "bad", CronExpr: "not a cron"}}
	_, err = h.Org.Client.UpdateAgent(t.Context(), &gritzv1.UpdateAgentRequest{Agent: agent})

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeInvalidArgument)
	assert.ErrorContains(t, err, "invalid cron expression")
}

func TestUpdateAgent_KeepsDueSchedule(t *testing.T) {
	t.Parallel()
	// Arrange - an enabled schedule whose occurrence is due but not yet fired.
	h := harness.New(t, nil)
	agent := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "bumper",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Schedules: []*gritzv1.AgentSchedule{{Name: "hourly", CronExpr: "0 * * * *", Enabled: true}},
	}})
	sched := agent.Schedules[0]
	// No RPC moves a schedule's clock, so the store makes it due, and deletes
	// it afterwards so it doesn't stay due in the shared database.
	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	assert.NilError(t, h.Server.Store.AdvanceSchedule(t.Context(), nil, sched.Id, h.Org.ID, store.ScheduleAdvance{NextRunAt: &due}))
	t.Cleanup(func() {
		assert.NilError(t, h.Server.Store.DeleteSchedule(context.Background(), nil, sched.Id, h.Org.ID))
	})

	// Act - an edit that leaves the schedule's spec alone.
	agent.Prompt = "Bump the dependencies."
	agent.Schedules = []*gritzv1.AgentSchedule{{Id: sched.Id, Name: "renamed", CronExpr: "0 * * * *", Timezone: "UTC", Enabled: true}}
	resp, err := h.Org.Client.UpdateAgent(t.Context(), &gritzv1.UpdateAgentRequest{Agent: agent})

	// Assert - the due occurrence is kept.
	assert.NilError(t, err)
	assert.Equal(t, resp.Agent.Schedules[0].NextRunAt.AsTime(), due)

	// Act - a cron edit.
	agent.Schedules[0].CronExpr = "30 * * * *"
	resp, err = h.Org.Client.UpdateAgent(t.Context(), &gritzv1.UpdateAgentRequest{Agent: agent})

	// Assert - it realigns from now.
	assert.NilError(t, err)
	assert.Assert(t, resp.Agent.Schedules[0].NextRunAt.AsTime().After(time.Now()))
}

// An agent edited between two fires of its schedule: the edit keeps the
// schedule's run state, and the second fire takes the edited agent.
func TestAgentSchedule_EditBetweenFires(t *testing.T) {
	t.Parallel()
	// Arrange
	h := harness.New(t, nil)
	agent := h.Org.CreateAgent(&gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:      "dependency-bumper",
		Workspace: harness.DefaultWorkspace,
		Runner:    harness.DefaultRunner,
		Prompt:    "Bump outdated dependencies.",
		Schedules: []*gritzv1.AgentSchedule{{Name: "Nightly bump", CronExpr: "0 3 * * *", Timezone: "Europe/Paris", Enabled: true}},
	}})
	sched := agent.Schedules[0]
	h.Org.FireSchedule(agent.Id, sched.Id)
	fired := h.Org.GetAgent(&gritzv1.GetAgentRequest{Id: agent.Id}).Schedules[0]
	tasks := h.Org.ListTasks(&gritzv1.ListTasksRequest{})
	assert.Assert(t, cmp.Len(tasks, 1))
	first := tasks[0]
	assert.Equal(t, first.Name, "Nightly bump")
	assert.Equal(t, fired.LastTaskId, first.Id)
	assert.Assert(t, fired.LastRunAt != nil && fired.NextRunAt != nil)

	// Act - rename the schedule, change the prompt, add a schedule.
	agent.Prompt = "Bump outdated dependencies, then open a pull request."
	agent.Schedules = []*gritzv1.AgentSchedule{
		{Id: sched.Id, Name: "Nightly bump v2", CronExpr: "0 3 * * *", Timezone: "Europe/Paris", Enabled: true},
		{Name: "Weekend bump", CronExpr: "0 9 * * 6"},
	}
	resp, err := h.Org.Client.UpdateAgent(t.Context(), &gritzv1.UpdateAgentRequest{Agent: agent})

	// Assert - the schedule keeps its run state.
	assert.NilError(t, err)
	assert.DeepEqual(t,
		resp.Agent.Schedules[0],
		&gritzv1.AgentSchedule{
			Id:         sched.Id,
			Name:       "Nightly bump v2",
			CronExpr:   "0 3 * * *",
			Timezone:   "Europe/Paris",
			Enabled:    true,
			NextRunAt:  fired.NextRunAt,
			LastRunAt:  fired.LastRunAt,
			LastTaskId: first.Id,
		},
		protocmp.Transform(),
	)

	// Act - the second fire.
	h.Org.FireSchedule(agent.Id, sched.Id)

	// Assert - a task of the edited agent, recorded as the last run.
	tasks = h.Org.ListTasks(&gritzv1.ListTasksRequest{})
	assert.Assert(t, cmp.Len(tasks, 2))
	var second *gritzv1.Task
	for _, task := range tasks {
		if task.Id != first.Id {
			second = task
		}
	}
	assert.Equal(t, second.Name, "Nightly bump v2")
	assert.DeepEqual(t,
		h.Org.ListEventsByTask(&gritzv1.ListEventsByTaskRequest{TaskId: second.Id}),
		[]*gritzv1.Event{
			{},
			{},
			{Payload: &gritzv1.Event_Instruction{Instruction: &gritzv1.InstructionPayload{
				Text: "Bump outdated dependencies, then open a pull request.",
			}}},
		},
		protocmp.Transform(),
		cmpx.OnlyProtoFields(&gritzv1.Event{}, "instruction"),
	)
	assert.Equal(t, h.Org.GetAgent(&gritzv1.GetAgentRequest{Id: agent.Id}).Schedules[0].LastTaskId, second.Id)
}
