package api_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/auth/authscope"
	"github.com/gritzapp/gritz/internal/e2e/harness"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/durationpb"
	"gotest.tools/v3/assert"
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
			{Type: "jira.issue", Events: []string{"comment_created"}},
			{
				Type:   "github.pr",
				Kind:   "owned-pr",
				Claim:  "pr-owner",
				Events: []string{"pull_request_review_comment", "pull_request_review", "pull_request_closed"},
				Public: true,
			},
			{Type: "github.pr", Events: []string{"pull_request_closed"}},
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
		Links: []*gritzv1.AgentLink{{Type: "custom.url", Events: []string{"problem"}}},
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
				Links:     []*gritzv1.AgentLink{{Type: "jira.issue", Events: []string{"comment_created", "issue_deleted"}}},
			},
			want: `link kind "jira.issue": unknown event type: source="atlassian" type="issue_deleted"`,
		},
		{
			name: "link event that doesn't declare its link type",
			agent: &gritzv1.Agent{
				Name:      "a",
				Workspace: "story",
				Runner:    "runner-1",
				Links:     []*gritzv1.AgentLink{{Type: "github.issue", Events: []string{"pull_request_review"}}},
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
