package mcpserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/mcptest"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestListMembers(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		ListOrgMembersFunc: func(ctx context.Context, req *gritzv1.ListOrgMembersRequest) (*gritzv1.ListOrgMembersResponse, error) {
			return &gritzv1.ListOrgMembersResponse{Members: []*gritzv1.OrgMember{{
				OrgId:     12,
				UserId:    "u1",
				Email:     "ada@example.com",
				Name:      "Ada",
				Role:      "owner",
				CreatedAt: timestamppb.New(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
			}}}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_members"})

	// Assert
	assert.NilError(t, err)
	var got []map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	assert.DeepEqual(t, got, []map[string]any{{
		"user_id":    "u1",
		"email":      "ada@example.com",
		"name":       "Ada",
		"role":       "owner",
		"created_at": "2026-01-02T03:04:05Z",
	}})
}

func TestListKeys(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		ListKeysFunc: func(ctx context.Context, req *gritzv1.ListKeysRequest) (*gritzv1.ListKeysResponse, error) {
			return &gritzv1.ListKeysResponse{Keys: []*gritzv1.Key{{
				Id:        "k1",
				Name:      "ci",
				ExpiresAt: timestamppb.New(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)),
			}}}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_keys"})

	// Assert
	assert.NilError(t, err)
	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	assert.DeepEqual(t, got, map[string]any{
		"keys": []any{map[string]any{"id": "k1", "name": "ci", "expiresAt": "2027-01-01T00:00:00Z"}},
	})
}

func TestGetOrgSettings(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		GetOrgSettingsFunc: func(ctx context.Context, req *gritzv1.GetOrgSettingsRequest) (*gritzv1.GetOrgSettingsResponse, error) {
			return &gritzv1.GetOrgSettingsResponse{
				AtlassianWebhookSecret: "s3cret",
				AtlassianWebhookUrl:    "https://gritz.dev/webhook/atlassian",
				GithubAppUrl:           "https://github.com/apps/gritz",
				McpUrl:                 "https://gritz.dev/mcp",
				GithubInstallationId:   42,
				CustomWebhookUrl:       "https://gritz.dev/webhook/custom",
			}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_org_settings"})

	// Assert
	assert.NilError(t, err)
	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	// Every field but atlassian_webhook_secret.
	assert.DeepEqual(t, got, map[string]any{
		"mcp_url":                "https://gritz.dev/mcp",
		"custom_webhook_url":     "https://gritz.dev/webhook/custom",
		"atlassian_webhook_url":  "https://gritz.dev/webhook/atlassian",
		"github_app_url":         "https://github.com/apps/gritz",
		"github_installation_id": float64(42),
	})
}

func TestGetRoutingRules(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		GetRoutingRulesFunc: func(ctx context.Context, req *gritzv1.GetRoutingRulesRequest) (*gritzv1.GetRoutingRulesResponse, error) {
			return &gritzv1.GetRoutingRulesResponse{Rules: []*gritzv1.RoutingRule{{
				Source:     "github",
				Type:       "pr_comment",
				Wakeup:     true,
				Conditions: []*gritzv1.RuleCondition{{Attr: "body", Op: "contains", Value: "@gritz"}},
			}}}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_routing_rules"})

	// Assert
	assert.NilError(t, err)
	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	assert.DeepEqual(t, got, map[string]any{
		"rules": []any{map[string]any{
			"source":     "github",
			"type":       "pr_comment",
			"wakeup":     true,
			"conditions": []any{map[string]any{"attr": "body", "op": "contains", "value": "@gritz"}},
		}},
	})
}

func TestSetRoutingRules(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		SetRoutingRulesFunc: func(ctx context.Context, req *gritzv1.SetRoutingRulesRequest) (*gritzv1.SetRoutingRulesResponse, error) {
			return &gritzv1.SetRoutingRulesResponse{Rules: req.Rules}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "set_routing_rules",
		Arguments: map[string]any{"rules": []map[string]any{
			{
				"source":     "github",
				"type":       "pr_comment",
				"wakeup":     true,
				"conditions": []map[string]any{{"attr": "body", "op": "contains", "value": "@gritz"}},
			},
			{
				"source":    "custom",
				"type":      "alert",
				"public":    true,
				"namespace": "ops",
				"create":    map[string]any{"workspace": "ws", "runner": "r1", "prompt": "triage", "autoArchive": "3600s"},
			},
		}},
	})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, !result.IsError, "unexpected error result: %v", result.Content)
	assert.Equal(t, len(client.SetRoutingRulesCalls()), 1)
	assert.DeepEqual(t, client.SetRoutingRulesCalls()[0].SetRoutingRulesRequest, &gritzv1.SetRoutingRulesRequest{Rules: []*gritzv1.RoutingRule{
		{
			Source:     "github",
			Type:       "pr_comment",
			Wakeup:     true,
			Conditions: []*gritzv1.RuleCondition{{Attr: "body", Op: "contains", Value: "@gritz"}},
		},
		{
			Source:    "custom",
			Type:      "alert",
			Public:    true,
			Namespace: "ops",
			Create:    &gritzv1.CreateTaskAction{Workspace: "ws", Runner: "r1", Prompt: "triage", AutoArchive: durationpb.New(time.Hour)},
		},
	}}, protocmp.Transform())
	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	assert.Equal(t, len(got["rules"].([]any)), 2)
}

func TestSetRoutingRules_DestructiveHint(t *testing.T) {
	session := setupSession(t, &gritzclient.ClientMock{})

	resp, err := session.ListTools(t.Context(), &mcp.ListToolsParams{})
	assert.NilError(t, err)

	for _, tool := range resp.Tools {
		if tool.Name == "set_routing_rules" {
			assert.Assert(t, tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint)
			return
		}
	}
	t.Fatal("set_routing_rules not registered")
}

func TestSetRoutingRules_InvalidArgument(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		SetRoutingRulesFunc: func(ctx context.Context, req *gritzv1.SetRoutingRulesRequest) (*gritzv1.SetRoutingRulesResponse, error) {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(`rule 0: unknown event type "custom/deploy"`))
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "set_routing_rules",
		Arguments: map[string]any{"rules": []map[string]any{{"source": "custom", "type": "deploy"}}},
	})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, result.IsError)
	assert.Assert(t, cmp.Contains(mcptest.CallToolResultText(t, result), `unknown event type "custom/deploy"`))
}
