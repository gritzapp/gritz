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
	"google.golang.org/protobuf/types/known/timestamppb"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestListEvents(t *testing.T) {
	// Arrange
	created := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	client := &gritzclient.ClientMock{
		ListExternalEventsFunc: func(ctx context.Context, req *gritzv1.ListExternalEventsRequest) (*gritzv1.ListExternalEventsResponse, error) {
			assert.Equal(t, req.Limit, int32(10))
			return &gritzv1.ListExternalEventsResponse{
				Events: []*gritzv1.Event{{
					Id:        5,
					TaskId:    42,
					CreatedAt: timestamppb.New(created),
					Payload: &gritzv1.Event_External{External: &gritzv1.ExternalPayload{
						Source:      "github",
						Type:        "issue_comment",
						Description: "octocat commented on PR #1",
						Url:         "https://github.com/o/r/pull/1",
						Data:        "please fix the tests",
						Details:     map[string]string{"path": "main.go"},
					}},
				}},
			}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "list_events",
		Arguments: map[string]any{"limit": 10},
	})

	// Assert
	assert.NilError(t, err)
	var got []map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	// The summary leaves out the event's data and details.
	assert.DeepEqual(t, got, []map[string]any{{
		"id":          float64(5),
		"task_id":     float64(42),
		"source":      "github",
		"type":        "issue_comment",
		"description": "octocat commented on PR #1",
		"url":         "https://github.com/o/r/pull/1",
		"created_at":  "2026-11-08T12:00:00Z",
	}})
}

func TestGetEvent(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		GetEventFunc: func(ctx context.Context, req *gritzv1.GetEventRequest) (*gritzv1.GetEventResponse, error) {
			assert.Equal(t, req.Id, int64(5))
			return &gritzv1.GetEventResponse{Event: &gritzv1.Event{
				Id:     5,
				TaskId: 42,
				Payload: &gritzv1.Event_External{External: &gritzv1.ExternalPayload{
					Source: "github",
					Type:   "issue_comment",
					Data:   "please fix the tests",
				}},
			}}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "get_event",
		Arguments: map[string]any{"id": 5},
	})

	// Assert
	assert.NilError(t, err)
	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	assert.DeepEqual(t, got, map[string]any{
		"id":     "5",
		"taskId": "42",
		"external": map[string]any{
			"source": "github",
			"type":   "issue_comment",
			"data":   "please fix the tests",
		},
	})
}

func TestDeleteEvent(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		DeleteEventFunc: func(ctx context.Context, req *gritzv1.DeleteEventRequest) (*gritzv1.DeleteEventResponse, error) {
			return &gritzv1.DeleteEventResponse{}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "delete_event",
		Arguments: map[string]any{"id": 5},
	})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, !result.IsError, "unexpected error result: %v", result.Content)
	assert.Equal(t, len(client.DeleteEventCalls()), 1)
	assert.Equal(t, client.DeleteEventCalls()[0].DeleteEventRequest.Id, int64(5))
}

func TestPutCustomEventType(t *testing.T) {
	alert := &gritzv1.EventTypeDef{
		Source: "custom",
		Type:   "alert",
		Label:  "Alert",
		Attrs:  []*gritzv1.AttrDef{{Key: "body"}, {Key: "url"}},
	}
	deploy := &gritzv1.EventTypeDef{
		Source: "custom",
		Type:   "deploy",
		Label:  "Deploy",
		Attrs:  []*gritzv1.AttrDef{{Key: "body"}, {Key: "url"}},
	}
	tests := []struct {
		name string
		typ  string
		want []*gritzv1.EventTypeDef
	}{
		{
			name: "Add",
			typ:  "incident",
			want: []*gritzv1.EventTypeDef{alert, deploy, {
				Source: "custom",
				Type:   "incident",
				Label:  "New",
				Attrs:  []*gritzv1.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity", Label: "Severity"}},
			}},
		},
		{
			name: "Replace",
			typ:  "alert",
			want: []*gritzv1.EventTypeDef{{
				Source: "custom",
				Type:   "alert",
				Label:  "New",
				Attrs:  []*gritzv1.AttrDef{{Key: "body"}, {Key: "url"}, {Key: "severity", Label: "Severity"}},
			}, deploy},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			client := &gritzclient.ClientMock{
				GetCustomEventTypesFunc: func(ctx context.Context, req *gritzv1.GetCustomEventTypesRequest) (*gritzv1.GetCustomEventTypesResponse, error) {
					return &gritzv1.GetCustomEventTypesResponse{EventTypes: []*gritzv1.EventTypeDef{alert, deploy}}, nil
				},
				SetCustomEventTypesFunc: func(ctx context.Context, req *gritzv1.SetCustomEventTypesRequest) (*gritzv1.SetCustomEventTypesResponse, error) {
					return &gritzv1.SetCustomEventTypesResponse{EventTypes: req.EventTypes}, nil
				},
			}
			session := setupSession(t, client)

			// Act
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
				Name: "put_custom_event_type",
				Arguments: map[string]any{
					"type":  tt.typ,
					"label": "New",
					"attrs": []map[string]any{{"key": "body"}, {"key": "url"}, {"key": "severity", "label": "Severity"}},
				},
			})

			// Assert
			assert.NilError(t, err)
			assert.Assert(t, !result.IsError, "unexpected error result: %v", result.Content)
			assert.Equal(t, len(client.SetCustomEventTypesCalls()), 1)
			assert.DeepEqual(t, client.SetCustomEventTypesCalls()[0].SetCustomEventTypesRequest.EventTypes, tt.want, protocmp.Transform())
		})
	}
}

func TestDeleteCustomEventType(t *testing.T) {
	// Arrange
	alert := &gritzv1.EventTypeDef{Source: "custom", Type: "alert", Label: "Alert"}
	deploy := &gritzv1.EventTypeDef{Source: "custom", Type: "deploy", Label: "Deploy"}
	incident := &gritzv1.EventTypeDef{Source: "custom", Type: "incident", Label: "Incident"}
	client := &gritzclient.ClientMock{
		GetCustomEventTypesFunc: func(ctx context.Context, req *gritzv1.GetCustomEventTypesRequest) (*gritzv1.GetCustomEventTypesResponse, error) {
			return &gritzv1.GetCustomEventTypesResponse{EventTypes: []*gritzv1.EventTypeDef{alert, deploy, incident}}, nil
		},
		SetCustomEventTypesFunc: func(ctx context.Context, req *gritzv1.SetCustomEventTypesRequest) (*gritzv1.SetCustomEventTypesResponse, error) {
			return &gritzv1.SetCustomEventTypesResponse{EventTypes: req.EventTypes}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "delete_custom_event_type",
		Arguments: map[string]any{"type": "deploy"},
	})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, !result.IsError, "unexpected error result: %v", result.Content)
	assert.Equal(t, len(client.SetCustomEventTypesCalls()), 1)
	assert.DeepEqual(t, client.SetCustomEventTypesCalls()[0].SetCustomEventTypesRequest.EventTypes,
		[]*gritzv1.EventTypeDef{alert, incident}, protocmp.Transform())
}

func TestDeleteCustomEventType_NotFound(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		GetCustomEventTypesFunc: func(ctx context.Context, req *gritzv1.GetCustomEventTypesRequest) (*gritzv1.GetCustomEventTypesResponse, error) {
			return &gritzv1.GetCustomEventTypesResponse{EventTypes: []*gritzv1.EventTypeDef{{Source: "custom", Type: "alert"}}}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "delete_custom_event_type",
		Arguments: map[string]any{"type": "deploy"},
	})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, result.IsError)
	assert.Equal(t, mcptest.CallToolResultText(t, result), `custom event type "deploy" not found`)
	assert.Equal(t, len(client.SetCustomEventTypesCalls()), 0)
}

func TestDeleteCustomEventType_FailedPrecondition(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		GetCustomEventTypesFunc: func(ctx context.Context, req *gritzv1.GetCustomEventTypesRequest) (*gritzv1.GetCustomEventTypesResponse, error) {
			return &gritzv1.GetCustomEventTypesResponse{EventTypes: []*gritzv1.EventTypeDef{{Source: "custom", Type: "alert"}}}, nil
		},
		SetCustomEventTypesFunc: func(ctx context.Context, req *gritzv1.SetCustomEventTypesRequest) (*gritzv1.SetCustomEventTypesResponse, error) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(`routing rule 1: unknown event type "custom:alert"`))
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "delete_custom_event_type",
		Arguments: map[string]any{"type": "alert"},
	})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, result.IsError)
	assert.Assert(t, cmp.Contains(mcptest.CallToolResultText(t, result), `routing rule 1: unknown event type "custom:alert"`))
}

func TestTestEvent(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		TestEventFunc: func(ctx context.Context, req *gritzv1.TestEventRequest) (*gritzv1.TestEventResponse, error) {
			return &gritzv1.TestEventResponse{
				Matches: []*gritzv1.TestEventMatch{{OrgId: 7, RuleIndex: 2, WouldWake: true}},
			}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "test_event",
		Arguments: map[string]any{
			"source":  "custom",
			"type":    "alert",
			"body":    "error rate above 5%",
			"url":     "https://alerts.example.com/alerts/1",
			"attrs":   map[string]any{"tag": []string{"team:payments", "env:prod"}},
			"details": map[string]any{"runbook": "https://wiki.example.com/runbook"},
			"fire":    true,
		},
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, len(client.TestEventCalls()), 1)
	assert.DeepEqual(t, client.TestEventCalls()[0].TestEventRequest, &gritzv1.TestEventRequest{
		Source:  "custom",
		Type:    "alert",
		Body:    "error rate above 5%",
		Url:     "https://alerts.example.com/alerts/1",
		Attrs:   map[string]*gritzv1.AttrValues{"tag": {Values: []string{"team:payments", "env:prod"}}},
		Details: map[string]string{"runbook": "https://wiki.example.com/runbook"},
		Fire:    true,
	}, protocmp.Transform())
	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	assert.DeepEqual(t, got, map[string]any{
		"matches": []any{map[string]any{"orgId": "7", "ruleIndex": float64(2), "wouldWake": true}},
	})
}

func TestTestEvent_InvalidArgument(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		TestEventFunc: func(ctx context.Context, req *gritzv1.TestEventRequest) (*gritzv1.TestEventResponse, error) {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(`unknown event type "deploy"`))
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "test_event",
		Arguments: map[string]any{"source": "custom", "type": "deploy", "url": "https://example.com/d/1"},
	})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, result.IsError)
	assert.Assert(t, cmp.Contains(mcptest.CallToolResultText(t, result), `unknown event type "deploy"`))
}
