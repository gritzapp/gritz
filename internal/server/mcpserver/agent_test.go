package mcpserver

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/mcptest"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/durationpb"
	"gotest.tools/v3/assert"
)

func TestListAgents(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		ListAgentsFunc: func(ctx context.Context, req *gritzv1.ListAgentsRequest) (*gritzv1.ListAgentsResponse, error) {
			return &gritzv1.ListAgentsResponse{Agents: []*gritzv1.Agent{
				{Id: 1, Name: "default", Workspace: "gritz", Runner: "main"},
				{Id: 2, Name: "reviewer", Workspace: "gritz", Runner: "main"},
			}}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_agents"})

	// Assert
	assert.NilError(t, err)
	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	assert.DeepEqual(t, got, map[string]any{
		"agents": []any{
			map[string]any{"id": "1", "name": "default", "workspace": "gritz", "runner": "main"},
			map[string]any{"id": "2", "name": "reviewer", "workspace": "gritz", "runner": "main"},
		},
	})
}

func TestGetAgent(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		GetAgentFunc: func(ctx context.Context, req *gritzv1.GetAgentRequest) (*gritzv1.GetAgentResponse, error) {
			assert.Equal(t, req.Id, int64(3))
			return &gritzv1.GetAgentResponse{Agent: &gritzv1.Agent{
				Id:          3,
				Name:        "implementer",
				Workspace:   "gritz",
				Runner:      "main",
				AutoArchive: durationpb.New(86400e9),
				Features:    []string{"jira", "codex"},
				Triggers: []*gritzv1.AgentTrigger{{
					Source:     "atlassian",
					Type:       "label_added",
					Conditions: []*gritzv1.RuleCondition{{Attr: "label", Op: "equals", Value: "build-it"}},
					Link:       "jira.issue",
				}},
				Links: []*gritzv1.AgentLink{{Type: "jira.issue", Events: []*gritzv1.AgentLinkEvent{{Type: "comment_created"}}}},
			}}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "get_agent",
		Arguments: map[string]any{"id": 3},
	})

	// Assert
	assert.NilError(t, err)
	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	assert.DeepEqual(t, got, map[string]any{
		"id":          "3",
		"name":        "implementer",
		"workspace":   "gritz",
		"runner":      "main",
		"autoArchive": "86400s",
		"features":    []any{"jira", "codex"},
		"triggers": []any{map[string]any{
			"source":     "atlassian",
			"type":       "label_added",
			"conditions": []any{map[string]any{"attr": "label", "op": "equals", "value": "build-it"}},
			"link":       "jira.issue",
		}},
		"links": []any{map[string]any{"type": "jira.issue", "events": []any{map[string]any{"type": "comment_created"}}}},
	})
}

func TestPutAgentCreates(t *testing.T) {
	// Arrange
	var got *gritzv1.CreateAgentRequest
	client := &gritzclient.ClientMock{
		CreateAgentFunc: func(ctx context.Context, req *gritzv1.CreateAgentRequest) (*gritzv1.CreateAgentResponse, error) {
			got = req
			agent := proto.CloneOf(req.Agent)
			agent.Id = 7
			return &gritzv1.CreateAgentResponse{Agent: agent}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "put_agent",
		Arguments: map[string]any{"agent": map[string]any{
			"name":         "responder",
			"workspace":    "gritz",
			"runner":       "main",
			"auto_archive": "3600s",
			"features":     []any{"jira", "codex"},
			"triggers": []any{map[string]any{
				"source":   "github",
				"type":     "issue_comment",
				"yield_to": []any{"pr-owner"},
				"link":     "github.pr",
			}},
			"links": []any{map[string]any{"type": "github.pr", "events": []any{map[string]any{"type": "issue_comment"}}}},
		}},
	})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, !result.IsError)
	assert.DeepEqual(t, got, &gritzv1.CreateAgentRequest{Agent: &gritzv1.Agent{
		Name:        "responder",
		Workspace:   "gritz",
		Runner:      "main",
		AutoArchive: durationpb.New(3600e9),
		Features:    []string{"jira", "codex"},
		Triggers: []*gritzv1.AgentTrigger{{
			Source:  "github",
			Type:    "issue_comment",
			YieldTo: []string{"pr-owner"},
			Link:    "github.pr",
		}},
		Links: []*gritzv1.AgentLink{{Type: "github.pr", Events: []*gritzv1.AgentLinkEvent{{Type: "issue_comment"}}}},
	}}, protocmp.Transform())
	var out map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &out)
	assert.Equal(t, out["id"], "7")
}

func TestPutAgentUpdates(t *testing.T) {
	// Arrange
	var got *gritzv1.UpdateAgentRequest
	client := &gritzclient.ClientMock{
		UpdateAgentFunc: func(ctx context.Context, req *gritzv1.UpdateAgentRequest) (*gritzv1.UpdateAgentResponse, error) {
			got = req
			return &gritzv1.UpdateAgentResponse{Agent: req.Agent}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "put_agent",
		Arguments: map[string]any{"agent": map[string]any{
			"id":        "4",
			"name":      "reviewer",
			"workspace": "gritz",
			"runner":    "main",
			"prompt":    "Review the pull request.",
		}},
	})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, !result.IsError)
	assert.DeepEqual(t, got, &gritzv1.UpdateAgentRequest{Agent: &gritzv1.Agent{
		Id:        4,
		Name:      "reviewer",
		Workspace: "gritz",
		Runner:    "main",
		Prompt:    "Review the pull request.",
	}}, protocmp.Transform())
}

func TestPutAgentInvalid(t *testing.T) {
	// Arrange
	session := setupSession(t, &gritzclient.ClientMock{})

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "put_agent",
		Arguments: map[string]any{"agent": map[string]any{"name": "x", "triggers": "nope"}},
	})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, result.IsError)
}

func TestDeleteAgent(t *testing.T) {
	// Arrange
	var gotID int64
	client := &gritzclient.ClientMock{
		DeleteAgentFunc: func(ctx context.Context, req *gritzv1.DeleteAgentRequest) (*gritzv1.DeleteAgentResponse, error) {
			gotID = req.Id
			return &gritzv1.DeleteAgentResponse{}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "delete_agent",
		Arguments: map[string]any{"id": 5},
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, gotID, int64(5))
	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	assert.DeepEqual(t, got, map[string]any{"deleted": float64(5)})
}

func TestDeleteAgentInUse(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		DeleteAgentFunc: func(ctx context.Context, req *gritzv1.DeleteAgentRequest) (*gritzv1.DeleteAgentResponse, error) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("agent is in use"))
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "delete_agent",
		Arguments: map[string]any{"id": 5},
	})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, result.IsError)
}
