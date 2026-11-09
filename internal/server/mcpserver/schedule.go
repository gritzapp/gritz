package mcpserver

import (
	"context"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/mcpx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// addScheduleTools registers the schedule tools.
func (h *handlers) addScheduleTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_schedules",
		Description: "List the org's schedules, which create tasks on a cron schedule",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, h.listSchedules)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_schedule",
		Description: "Get a schedule, including its instructions and its next and last run",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, h.getSchedule)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_schedule",
		Description: "Create a schedule that creates a task from its template each time its cron expression matches. It is created disabled unless enabled is set; set_schedule_enabled turns it on later.",
	}, h.createSchedule)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "update_schedule",
		Description: "Change a schedule's template, cron expression or timezone. Arguments left out keep their current value. Use set_schedule_enabled to enable or disable it.",
	}, h.updateSchedule)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "set_schedule_enabled",
		Description: "Enable or disable a schedule. Enabling schedules its next run from now; a disabled schedule doesn't run.",
	}, h.setScheduleEnabled)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "run_schedule",
		Description: "Create a task from a schedule now, once, even if it is disabled. Its next and last run are unchanged.",
	}, h.runSchedule)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_schedule",
		Description: "Delete a schedule. The tasks it created are kept.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: new(true),
		},
	}, h.deleteSchedule)
}

type listSchedulesInput struct{}

func (h *handlers) listSchedules(ctx context.Context, req *mcp.CallToolRequest, input listSchedulesInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	resp, err := service.ListSchedules(ctx, &gritzv1.ListSchedulesRequest{})
	if err != nil {
		return mcpx.ErrorResult("failed to list schedules: %v", err), nil, nil
	}
	return mcpx.ProtoJSONResult(resp), nil, nil
}

type scheduleIDInput struct {
	ID int64 `json:"id" jsonschema:"The schedule ID"`
}

func (h *handlers) getSchedule(ctx context.Context, req *mcp.CallToolRequest, input scheduleIDInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	resp, err := service.GetSchedule(ctx, &gritzv1.GetScheduleRequest{Id: input.ID})
	if err != nil {
		return mcpx.ErrorResult("failed to get schedule: %v", err), nil, nil
	}
	return mcpx.ProtoJSONResult(resp.Schedule), nil, nil
}

type createScheduleInput struct {
	Name        string `json:"name,omitempty" jsonschema:"The name of every task the schedule creates"`
	Workspace   string `json:"workspace" jsonschema:"The workspace its tasks run in"`
	Runner      string `json:"runner" jsonschema:"Runner ID to target"`
	Namespace   string `json:"namespace,omitempty" jsonschema:"Its tasks' namespace. Empty is the default namespace."`
	Instruction string `json:"instruction" jsonschema:"The instruction every task it creates starts with"`
	CronExpr    string `json:"cron_expr" jsonschema:"When to create a task: a five-field cron expression (minute, hour, day of month, month, day of week) or @yearly, @monthly, @weekly, @daily, @hourly"`
	Timezone    string `json:"timezone,omitempty" jsonschema:"The IANA timezone the cron expression is read in, such as \"Europe/Paris\" (default \"UTC\")"`
	AutoArchive string `json:"auto_archive,omitempty" jsonschema:"Auto-archive its tasks this long after they reach a terminal status, as a Go duration string (e.g. \"30m\", \"1h\"). \"0\" or omitted = never, a negative value like \"-1s\" = archive immediately on terminal status, positive = delay."`
	Enabled     bool   `json:"enabled,omitempty" jsonschema:"Whether the schedule runs. False (the default) creates it disabled."`
}

func (h *handlers) createSchedule(ctx context.Context, req *mcp.CallToolRequest, input createScheduleInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	createReq := &gritzv1.CreateScheduleRequest{
		Name:         input.Name,
		Workspace:    input.Workspace,
		Runner:       input.Runner,
		Namespace:    input.Namespace,
		Instructions: []*gritzv1.Instruction{{Text: input.Instruction}},
		CronExpr:     input.CronExpr,
		Timezone:     input.Timezone,
		Enabled:      input.Enabled,
	}
	if input.AutoArchive != "" {
		d, err := parseAutoArchive(input.AutoArchive)
		if err != nil {
			return mcpx.ErrorResult("%v", err), nil, nil
		}
		createReq.AutoArchive = d
	}
	resp, err := service.CreateSchedule(ctx, createReq)
	if err != nil {
		return mcpx.ErrorResult("failed to create schedule: %v", err), nil, nil
	}
	return mcpx.ProtoJSONResult(resp.Schedule), nil, nil
}

type updateScheduleInput struct {
	ID          int64   `json:"id" jsonschema:"The schedule ID"`
	Name        *string `json:"name,omitempty" jsonschema:"A new name for the tasks it creates"`
	Workspace   *string `json:"workspace,omitempty" jsonschema:"A new workspace for its tasks"`
	Runner      *string `json:"runner,omitempty" jsonschema:"A new runner ID for its tasks"`
	Namespace   *string `json:"namespace,omitempty" jsonschema:"A new namespace for its tasks"`
	Instruction *string `json:"instruction,omitempty" jsonschema:"An instruction that replaces every instruction of the schedule"`
	CronExpr    *string `json:"cron_expr,omitempty" jsonschema:"A new cron expression, as in create_schedule"`
	Timezone    *string `json:"timezone,omitempty" jsonschema:"A new IANA timezone for the cron expression"`
	AutoArchive *string `json:"auto_archive,omitempty" jsonschema:"A new auto-archive delay for its tasks, as in create_schedule"`
}

func (h *handlers) updateSchedule(ctx context.Context, req *mcp.CallToolRequest, input updateScheduleInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	// UpdateSchedule replaces every template field, so start from the stored
	// schedule and overlay the arguments given.
	getResp, err := service.GetSchedule(ctx, &gritzv1.GetScheduleRequest{Id: input.ID})
	if err != nil {
		return mcpx.ErrorResult("failed to get schedule: %v", err), nil, nil
	}
	s := getResp.Schedule
	updateReq := &gritzv1.UpdateScheduleRequest{
		Id:           s.Id,
		Name:         s.Name,
		Workspace:    s.Workspace,
		Runner:       s.Runner,
		Namespace:    s.Namespace,
		Instructions: s.Instructions,
		CronExpr:     s.CronExpr,
		Timezone:     s.Timezone,
		AutoArchive:  s.AutoArchive,
	}
	if input.Name != nil {
		updateReq.Name = *input.Name
	}
	if input.Workspace != nil {
		updateReq.Workspace = *input.Workspace
	}
	if input.Runner != nil {
		updateReq.Runner = *input.Runner
	}
	if input.Namespace != nil {
		updateReq.Namespace = *input.Namespace
	}
	if input.Instruction != nil {
		updateReq.Instructions = []*gritzv1.Instruction{{Text: *input.Instruction}}
	}
	if input.CronExpr != nil {
		updateReq.CronExpr = *input.CronExpr
	}
	if input.Timezone != nil {
		updateReq.Timezone = *input.Timezone
	}
	if input.AutoArchive != nil {
		d, err := parseAutoArchive(*input.AutoArchive)
		if err != nil {
			return mcpx.ErrorResult("%v", err), nil, nil
		}
		updateReq.AutoArchive = d
	}
	resp, err := service.UpdateSchedule(ctx, updateReq)
	if err != nil {
		return mcpx.ErrorResult("failed to update schedule: %v", err), nil, nil
	}
	return mcpx.ProtoJSONResult(resp.Schedule), nil, nil
}

type setScheduleEnabledInput struct {
	ID      int64 `json:"id" jsonschema:"The schedule ID"`
	Enabled bool  `json:"enabled" jsonschema:"True to enable the schedule, false to disable it"`
}

func (h *handlers) setScheduleEnabled(ctx context.Context, req *mcp.CallToolRequest, input setScheduleEnabledInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	resp, err := service.SetScheduleEnabled(ctx, &gritzv1.SetScheduleEnabledRequest{Id: input.ID, Enabled: input.Enabled})
	if err != nil {
		return mcpx.ErrorResult("failed to set schedule enabled: %v", err), nil, nil
	}
	return mcpx.ProtoJSONResult(resp.Schedule), nil, nil
}

func (h *handlers) runSchedule(ctx context.Context, req *mcp.CallToolRequest, input scheduleIDInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	resp, err := service.RunSchedule(ctx, &gritzv1.RunScheduleRequest{Id: input.ID})
	if err != nil {
		return mcpx.ErrorResult("failed to run schedule: %v", err), nil, nil
	}
	return mcpx.JSONResult(taskSummaryOf(resp.Task)), nil, nil
}

func (h *handlers) deleteSchedule(ctx context.Context, req *mcp.CallToolRequest, input scheduleIDInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	if _, err := service.DeleteSchedule(ctx, &gritzv1.DeleteScheduleRequest{Id: input.ID}); err != nil {
		return mcpx.ErrorResult("failed to delete schedule: %v", err), nil, nil
	}
	type deleted struct {
		Deleted int64 `json:"deleted"`
	}
	return mcpx.JSONResult(deleted{Deleted: input.ID}), nil, nil
}
