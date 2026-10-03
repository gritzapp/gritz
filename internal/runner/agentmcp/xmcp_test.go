package agentmcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/icholy/gritz/internal/auth/agentauth"
	"github.com/icholy/gritz/internal/gritzclient"
	"github.com/icholy/gritz/internal/model"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
	"github.com/icholy/gritz/internal/x/mcptest"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func setupTestSession(t *testing.T, srv *Server) *mcp.ClientSession {
	t.Helper()

	ctx := t.Context()

	// Create MCP server and add tools
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v1.0.0"}, nil)
	srv.AddTools(mcpServer)

	// Create in-memory transports for server and client
	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	// Connect server
	_, err := mcpServer.Connect(ctx, serverTransport, nil)
	assert.NilError(t, err)

	// Create and connect client
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1.0.0"}, nil)
	clientSession, err := mcpClient.Connect(t.Context(), clientTransport, nil)
	assert.NilError(t, err)
	t.Cleanup(func() { clientSession.Close() })

	return clientSession
}

func TestGetMyTask(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		GetTaskFunc: func(ctx context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
			assert.Equal(t, req.Id, int64(123))
			return &gritzv1.GetTaskResponse{
				Task: &gritzv1.Task{
					Id:   123,
					Name: "test task",
				},
			}, nil
		},
		ListEventsByTaskFunc: func(ctx context.Context, req *gritzv1.ListEventsByTaskRequest) (*gritzv1.ListEventsByTaskResponse, error) {
			assert.Equal(t, req.TaskId, int64(123))
			assert.DeepEqual(t, req.Types, []string{model.EventTypeInstruction, model.EventTypeExternal})
			return &gritzv1.ListEventsByTaskResponse{
				Events: []*gritzv1.Event{
					{
						Payload: &gritzv1.Event_Instruction{
							Instruction: &gritzv1.InstructionPayload{
								Text: "do something",
								Url:  "https://example.com",
							},
						},
					},
				},
			}, nil
		},
		ListLinksFunc: func(ctx context.Context, req *gritzv1.ListLinksRequest) (*gritzv1.ListLinksResponse, error) {
			assert.Equal(t, req.TaskId, int64(123))
			return &gritzv1.ListLinksResponse{}, nil
		},
	}

	srv := NewServer(client, &model.Task{
		ID:        123,
		Runner:    "test-runner",
		Workspace: "test-workspace",
	}, nil)
	session := setupTestSession(t, srv)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "get_my_task",
		Arguments: map[string]any{},
	})
	assert.NilError(t, err)

	// Assert
	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	assert.DeepEqual(t, got, map[string]any{
		"id":        float64(123),
		"name":      "test task",
		"status":    "UNSPECIFIED",
		"workspace": "",
		"namespace": "",
		"url":       "",
		"links":     []any{},
		"events": []any{
			map[string]any{
				"instruction": map[string]any{"text": "do something", "url": "https://example.com"},
			},
		},
	})
}

func TestGetGitHubToken(t *testing.T) {
	// Arrange
	expiresAt := time.Date(2026, 5, 23, 1, 0, 0, 0, time.UTC)
	client := &gritzclient.ClientMock{
		CreateGitHubTokenFunc: func(ctx context.Context, req *gritzv1.CreateGitHubTokenRequest) (*gritzv1.CreateGitHubTokenResponse, error) {
			return &gritzv1.CreateGitHubTokenResponse{
				Token:     "ghs_test_token",
				ExpiresAt: timestamppb.New(expiresAt),
			}, nil
		},
	}
	srv := NewServer(client, &model.Task{ID: 123, Runner: "test-runner", Workspace: "test-workspace"}, []string{agentauth.CapabilityGitHubToken})
	session := setupTestSession(t, srv)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "get_github_token",
		Arguments: map[string]any{},
	})
	assert.NilError(t, err)

	// Assert
	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	assert.DeepEqual(t, got, map[string]any{
		"token":     "ghs_test_token",
		"expiresAt": "2026-05-23T01:00:00Z",
	})
	assert.Assert(t, cmp.Len(client.CreateGitHubTokenCalls(), 1))
}

func TestGetGitHubToken_Error(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		CreateGitHubTokenFunc: func(ctx context.Context, req *gritzv1.CreateGitHubTokenRequest) (*gritzv1.CreateGitHubTokenResponse, error) {
			return nil, errors.New("no installation linked")
		},
	}
	srv := NewServer(client, &model.Task{ID: 123, Runner: "test-runner", Workspace: "test-workspace"}, []string{agentauth.CapabilityGitHubToken})
	session := setupTestSession(t, srv)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "get_github_token",
		Arguments: map[string]any{},
	})
	assert.NilError(t, err)

	// Assert
	assert.Assert(t, result.IsError, "expected error result")
	text := mcptest.CallToolResultText(t, result)
	assert.Assert(t, strings.Contains(text, "no installation linked"), "expected error message, got: %s", text)
}

func TestGetGitHubToken_NotRegisteredWithoutCapability(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		CreateGitHubTokenFunc: func(ctx context.Context, req *gritzv1.CreateGitHubTokenRequest) (*gritzv1.CreateGitHubTokenResponse, error) {
			t.Fatal("tool must not be callable when the capability is absent")
			return nil, nil
		},
	}
	srv := NewServer(client, &model.Task{ID: 123, Runner: "test-runner", Workspace: "test-workspace"}, nil)
	session := setupTestSession(t, srv)

	// Act
	tools, err := session.ListTools(t.Context(), nil)
	assert.NilError(t, err)

	// Assert
	for _, tool := range tools.Tools {
		assert.Assert(t, tool.Name != "get_github_token", "get_github_token should not be registered without the capability")
	}
}
