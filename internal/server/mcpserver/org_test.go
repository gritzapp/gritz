package mcpserver

import (
	"context"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/mcptest"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gotest.tools/v3/assert"
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
