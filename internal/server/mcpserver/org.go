package mcpserver

import (
	"context"
	"encoding/json"
	"time"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/mcpx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/encoding/protojson"
)

// addOrgTools registers the org tools: members, keys, settings and routing
// rules.
func (h *handlers) addOrgTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_members",
		Description: "List the org's members",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, h.listMembers)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_keys",
		Description: "List the org's API keys, without their tokens",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, h.listKeys)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_org_settings",
		Description: "Get the org's integration settings: the MCP, custom event webhook, Atlassian webhook and GitHub App URLs, and the linked GitHub App installation",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, h.getOrgSettings)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_routing_rules",
		Description: "Get the org's routing rules, which decide what an incoming event does: wake the tasks subscribed to its URL, create a task, or both",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, h.getRoutingRules)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "set_routing_rules",
		Description: "Replace every routing rule in the org with the given list, in the shape get_routing_rules returns. A rule missing from the list is deleted, so read the rules with get_routing_rules, edit them, and write the whole list back. Returns the stored rules.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: new(true),
		},
	}, h.setRoutingRules)
}

type listMembersInput struct{}

type member struct {
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Name      string    `json:"name,omitempty"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at,omitzero"`
}

func (h *handlers) listMembers(ctx context.Context, req *mcp.CallToolRequest, input listMembersInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	resp, err := service.ListOrgMembers(ctx, &gritzv1.ListOrgMembersRequest{})
	if err != nil {
		return mcpx.ErrorResult("failed to list members: %v", err), nil, nil
	}
	members := make([]member, len(resp.Members))
	for i, m := range resp.Members {
		members[i] = member{
			UserID: m.UserId,
			Email:  m.Email,
			Name:   m.Name,
			Role:   m.Role,
		}
		if m.CreatedAt != nil {
			members[i].CreatedAt = m.CreatedAt.AsTime()
		}
	}
	return mcpx.JSONResult(members), nil, nil
}

type listKeysInput struct{}

func (h *handlers) listKeys(ctx context.Context, req *mcp.CallToolRequest, input listKeysInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	resp, err := service.ListKeys(ctx, &gritzv1.ListKeysRequest{})
	if err != nil {
		return mcpx.ErrorResult("failed to list keys: %v", err), nil, nil
	}
	return mcpx.ProtoJSONResult(resp), nil, nil
}

type getOrgSettingsInput struct{}

type orgSettings struct {
	MCPURL               string `json:"mcp_url,omitempty"`
	CustomWebhookURL     string `json:"custom_webhook_url,omitempty"`
	AtlassianWebhookURL  string `json:"atlassian_webhook_url,omitempty"`
	GitHubAppURL         string `json:"github_app_url,omitempty"`
	GitHubInstallationID int64  `json:"github_installation_id,omitempty"`
}

func (h *handlers) getOrgSettings(ctx context.Context, req *mcp.CallToolRequest, input getOrgSettingsInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	resp, err := service.GetOrgSettings(ctx, &gritzv1.GetOrgSettingsRequest{})
	if err != nil {
		return mcpx.ErrorResult("failed to get org settings: %v", err), nil, nil
	}
	// Never return the Atlassian webhook secret.
	return mcpx.JSONResult(orgSettings{
		MCPURL:               resp.McpUrl,
		CustomWebhookURL:     resp.CustomWebhookUrl,
		AtlassianWebhookURL:  resp.AtlassianWebhookUrl,
		GitHubAppURL:         resp.GithubAppUrl,
		GitHubInstallationID: resp.GithubInstallationId,
	}), nil, nil
}

type getRoutingRulesInput struct{}

func (h *handlers) getRoutingRules(ctx context.Context, req *mcp.CallToolRequest, input getRoutingRulesInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	resp, err := service.GetRoutingRules(ctx, &gritzv1.GetRoutingRulesRequest{})
	if err != nil {
		return mcpx.ErrorResult("failed to get routing rules: %v", err), nil, nil
	}
	return mcpx.ProtoJSONResult(resp), nil, nil
}

type setRoutingRulesInput struct {
	Rules []map[string]any `json:"rules" jsonschema:"Every routing rule of the org, as get_routing_rules returns them. An empty list deletes every rule."`
}

func (h *handlers) setRoutingRules(ctx context.Context, req *mcp.CallToolRequest, input setRoutingRulesInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	rules := make([]*gritzv1.RoutingRule, len(input.Rules))
	for i, r := range input.Rules {
		b, err := json.Marshal(r)
		if err != nil {
			return mcpx.ErrorResult("invalid rule %d: %v", i+1, err), nil, nil
		}
		rules[i] = &gritzv1.RoutingRule{}
		if err := protojson.Unmarshal(b, rules[i]); err != nil {
			return mcpx.ErrorResult("invalid rule %d: %v", i+1, err), nil, nil
		}
	}
	resp, err := service.SetRoutingRules(ctx, &gritzv1.SetRoutingRulesRequest{Rules: rules})
	if err != nil {
		return mcpx.ErrorResult("failed to set routing rules: %v", err), nil, nil
	}
	return mcpx.ProtoJSONResult(resp), nil, nil
}
