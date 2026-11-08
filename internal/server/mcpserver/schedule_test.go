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
	"gotest.tools/v3/assert"
)

func TestListSchedules(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		ListSchedulesFunc: func(ctx context.Context, req *gritzv1.ListSchedulesRequest) (*gritzv1.ListSchedulesResponse, error) {
			return &gritzv1.ListSchedulesResponse{Schedules: []*gritzv1.Schedule{
				{Id: 1, Name: "nightly", CronExpr: "0 3 * * *", Enabled: true},
				{Id: 2, Name: "weekly", CronExpr: "@weekly"},
			}}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_schedules"})

	// Assert
	assert.NilError(t, err)
	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	assert.DeepEqual(t, got, map[string]any{
		"schedules": []any{
			map[string]any{"id": "1", "name": "nightly", "cronExpr": "0 3 * * *", "enabled": true},
			map[string]any{"id": "2", "name": "weekly", "cronExpr": "@weekly"},
		},
	})
}

func TestGetSchedule(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		GetScheduleFunc: func(ctx context.Context, req *gritzv1.GetScheduleRequest) (*gritzv1.GetScheduleResponse, error) {
			assert.Equal(t, req.Id, int64(3))
			return &gritzv1.GetScheduleResponse{Schedule: &gritzv1.Schedule{
				Id:           3,
				Name:         "nightly",
				Instructions: []*gritzv1.Instruction{{Text: "bump deps"}, {Text: "open a PR"}},
				CronExpr:     "0 3 * * *",
				Timezone:     "Europe/Paris",
			}}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "get_schedule",
		Arguments: map[string]any{"id": 3},
	})

	// Assert
	assert.NilError(t, err)
	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	assert.DeepEqual(t, got, map[string]any{
		"id":           "3",
		"name":         "nightly",
		"instructions": []any{map[string]any{"text": "bump deps"}, map[string]any{"text": "open a PR"}},
		"cronExpr":     "0 3 * * *",
		"timezone":     "Europe/Paris",
	})
}

func TestCreateSchedule(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		CreateScheduleFunc: func(ctx context.Context, req *gritzv1.CreateScheduleRequest) (*gritzv1.CreateScheduleResponse, error) {
			return &gritzv1.CreateScheduleResponse{Schedule: &gritzv1.Schedule{Id: 3, Name: req.Name}}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "create_schedule",
		Arguments: map[string]any{
			"name":         "nightly",
			"workspace":    "ws",
			"runner":       "r1",
			"namespace":    "ops",
			"instruction":  "bump deps",
			"cron_expr":    "0 3 * * *",
			"timezone":     "Europe/Paris",
			"auto_archive": "1h",
		},
	})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, !result.IsError, "unexpected error result: %v", result.Content)
	assert.Equal(t, len(client.CreateScheduleCalls()), 1)
	// enabled is left out, so the schedule is created disabled.
	assert.DeepEqual(t, client.CreateScheduleCalls()[0].CreateScheduleRequest, &gritzv1.CreateScheduleRequest{
		Name:         "nightly",
		Workspace:    "ws",
		Runner:       "r1",
		Namespace:    "ops",
		Instructions: []*gritzv1.Instruction{{Text: "bump deps"}},
		CronExpr:     "0 3 * * *",
		Timezone:     "Europe/Paris",
		AutoArchive:  durationpb.New(time.Hour),
	}, protocmp.Transform())
	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	assert.DeepEqual(t, got, map[string]any{"id": "3", "name": "nightly"})
}

func TestUpdateSchedule(t *testing.T) {
	stored := &gritzv1.Schedule{
		Id:           3,
		Name:         "nightly",
		Workspace:    "ws",
		Runner:       "r1",
		Namespace:    "ops",
		Instructions: []*gritzv1.Instruction{{Text: "bump deps", Url: "https://example.com/issue/1"}, {Text: "open a PR"}},
		CronExpr:     "0 3 * * *",
		Timezone:     "Europe/Paris",
		Enabled:      true,
		AutoArchive:  durationpb.New(time.Hour),
	}
	tests := []struct {
		name string
		args map[string]any
		want *gritzv1.UpdateScheduleRequest
	}{
		{
			name: "CronExprOnly",
			args: map[string]any{"id": 3, "cron_expr": "0 9 * * *"},
			want: &gritzv1.UpdateScheduleRequest{
				Id:           3,
				Name:         "nightly",
				Workspace:    "ws",
				Runner:       "r1",
				Namespace:    "ops",
				Instructions: []*gritzv1.Instruction{{Text: "bump deps", Url: "https://example.com/issue/1"}, {Text: "open a PR"}},
				CronExpr:     "0 9 * * *",
				Timezone:     "Europe/Paris",
				AutoArchive:  durationpb.New(time.Hour),
			},
		},
		{
			name: "Every",
			args: map[string]any{
				"id":           3,
				"name":         "daily",
				"workspace":    "ws2",
				"runner":       "r2",
				"namespace":    "",
				"instruction":  "check CI",
				"cron_expr":    "@daily",
				"timezone":     "UTC",
				"auto_archive": "0",
			},
			want: &gritzv1.UpdateScheduleRequest{
				Id:           3,
				Name:         "daily",
				Workspace:    "ws2",
				Runner:       "r2",
				Instructions: []*gritzv1.Instruction{{Text: "check CI"}},
				CronExpr:     "@daily",
				Timezone:     "UTC",
				AutoArchive:  durationpb.New(0),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			client := &gritzclient.ClientMock{
				GetScheduleFunc: func(ctx context.Context, req *gritzv1.GetScheduleRequest) (*gritzv1.GetScheduleResponse, error) {
					assert.Equal(t, req.Id, int64(3))
					return &gritzv1.GetScheduleResponse{Schedule: stored}, nil
				},
				UpdateScheduleFunc: func(ctx context.Context, req *gritzv1.UpdateScheduleRequest) (*gritzv1.UpdateScheduleResponse, error) {
					return &gritzv1.UpdateScheduleResponse{Schedule: &gritzv1.Schedule{Id: req.Id}}, nil
				},
			}
			session := setupSession(t, client)

			// Act
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
				Name:      "update_schedule",
				Arguments: tt.args,
			})

			// Assert
			assert.NilError(t, err)
			assert.Assert(t, !result.IsError, "unexpected error result: %v", result.Content)
			assert.Equal(t, len(client.UpdateScheduleCalls()), 1)
			assert.DeepEqual(t, client.UpdateScheduleCalls()[0].UpdateScheduleRequest, tt.want, protocmp.Transform())
		})
	}
}

func TestUpdateSchedule_InvalidArgument(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		GetScheduleFunc: func(ctx context.Context, req *gritzv1.GetScheduleRequest) (*gritzv1.GetScheduleResponse, error) {
			return &gritzv1.GetScheduleResponse{Schedule: &gritzv1.Schedule{Id: 3, CronExpr: "@daily"}}, nil
		},
		UpdateScheduleFunc: func(ctx context.Context, req *gritzv1.UpdateScheduleRequest) (*gritzv1.UpdateScheduleResponse, error) {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(`invalid cron expression "every day"`))
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "update_schedule",
		Arguments: map[string]any{"id": 3, "cron_expr": "every day"},
	})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, result.IsError)
	assert.Equal(t, mcptest.CallToolResultText(t, result),
		`failed to update schedule: invalid_argument: invalid cron expression "every day"`)
}

func TestSetScheduleEnabled(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		SetScheduleEnabledFunc: func(ctx context.Context, req *gritzv1.SetScheduleEnabledRequest) (*gritzv1.SetScheduleEnabledResponse, error) {
			return &gritzv1.SetScheduleEnabledResponse{Schedule: &gritzv1.Schedule{Id: req.Id, Enabled: req.Enabled}}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "set_schedule_enabled",
		Arguments: map[string]any{"id": 3, "enabled": true},
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, len(client.SetScheduleEnabledCalls()), 1)
	assert.DeepEqual(t, client.SetScheduleEnabledCalls()[0].SetScheduleEnabledRequest,
		&gritzv1.SetScheduleEnabledRequest{Id: 3, Enabled: true}, protocmp.Transform())
	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	assert.DeepEqual(t, got, map[string]any{"id": "3", "enabled": true})
}

func TestRunSchedule(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		RunScheduleFunc: func(ctx context.Context, req *gritzv1.RunScheduleRequest) (*gritzv1.RunScheduleResponse, error) {
			assert.Equal(t, req.Id, int64(3))
			return &gritzv1.RunScheduleResponse{Task: &gritzv1.Task{
				Id:        42,
				Name:      "nightly",
				Workspace: "ws",
				Status:    gritzv1.TaskStatus_PENDING,
			}}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "run_schedule",
		Arguments: map[string]any{"id": 3},
	})

	// Assert
	assert.NilError(t, err)
	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	assert.DeepEqual(t, got, map[string]any{
		"id":        float64(42),
		"name":      "nightly",
		"workspace": "ws",
		"status":    "PENDING",
		"archived":  false,
	})
}

func TestDeleteSchedule(t *testing.T) {
	// Arrange
	client := &gritzclient.ClientMock{
		DeleteScheduleFunc: func(ctx context.Context, req *gritzv1.DeleteScheduleRequest) (*gritzv1.DeleteScheduleResponse, error) {
			return &gritzv1.DeleteScheduleResponse{}, nil
		},
	}
	session := setupSession(t, client)

	// Act
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "delete_schedule",
		Arguments: map[string]any{"id": 3},
	})

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, !result.IsError, "unexpected error result: %v", result.Content)
	assert.Equal(t, len(client.DeleteScheduleCalls()), 1)
	assert.Equal(t, client.DeleteScheduleCalls()[0].DeleteScheduleRequest.Id, int64(3))
}
