package mcpserver

import (
	"context"
	"time"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/mcpx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// addOrgTools registers the org tools: members, keys and settings.
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
