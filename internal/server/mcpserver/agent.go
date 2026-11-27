package mcpserver

import (
	"context"
	"encoding/json"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/mcpx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/encoding/protojson"
)

// addAgentTools registers the agent tools.
func (h *handlers) addAgentTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_agents",
		Description: "List the org's agents. An agent is a task template (workspace, runner, prompt, auto-archive, variables, features) plus the triggers that create its tasks from events and the link kinds its tasks may subscribe to.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, h.listAgents)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_agent",
		Description: "Get an agent, including its triggers, link kinds and schedules",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, h.getAgent)

	mcp.AddTool(server, &mcp.Tool{
		Name: "put_agent",
		Description: "Create an agent, or replace the agent with the given id, in the shape get_agent returns. " +
			"Fields: name; workspace and runner its tasks run in (see list_workspaces); prompt, the first instruction of the tasks its triggers and schedules create; " +
			"auto_archive, a duration in seconds such as \"86400s\" (0 never archives); variables, declared by the workspace; " +
			"features, names of features the workspace declares (see list_workspaces), merged onto the workspace in list order: where two set the same environment variable, secret or MCP server, or both have their own agent config, the later one wins. " +
			"triggers create a task when an event matches: source and type (see list_event_types), conditions [{attr, op, value}] that must all hold (op is equals, prefix, contains or glob), " +
			"public to also match events from non-members, yield_to claim names that stop it while an unarchived task holds one on the event's resource, " +
			"link, a link kind of this agent the created task subscribes to the event's resource with, and prompt, a second instruction the created task gets after the agent's prompt. The first matching trigger wins. " +
			"links are the kinds of subscribed link its tasks may create: type (a link type from list_event_types, such as github.pr), kind (defaults to type, unique in the agent), " +
			"claim stamped on its links, events [{type, conditions}] it delivers to the task (event types that declare the link type, each with conditions as a trigger's; with none it delivers nothing), and public. " +
			"schedules [{id, name, cron_expr, timezone, enabled, prompt}] each create a task of the agent named name, with prompt as a second instruction after the agent's prompt, whenever the 5-field cron expression matches in the IANA timezone (default UTC). " +
			"Keep a schedule's id to keep its last run, leave it out to add one, and leave a schedule out to delete it; next_run_at, last_run_at and last_task_id are set by the server. " +
			"Its tasks copy its variables and features when they are created, so replacing an agent doesn't change the tasks it already created. Returns the stored agent.",
	}, h.putAgent)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_agent",
		Description: "Delete an agent and its schedules. It fails while an unarchived task belongs to the agent: archive its tasks first.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: new(true),
		},
	}, h.deleteAgent)
}

type listAgentsInput struct{}

func (h *handlers) listAgents(ctx context.Context, req *mcp.CallToolRequest, input listAgentsInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	resp, err := service.ListAgents(ctx, &gritzv1.ListAgentsRequest{})
	if err != nil {
		return mcpx.ErrorResult("failed to list agents: %v", err), nil, nil
	}
	return mcpx.ProtoJSONResult(resp), nil, nil
}

type agentIDInput struct {
	ID int64 `json:"id" jsonschema:"The agent ID"`
}

func (h *handlers) getAgent(ctx context.Context, req *mcp.CallToolRequest, input agentIDInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	resp, err := service.GetAgent(ctx, &gritzv1.GetAgentRequest{Id: input.ID})
	if err != nil {
		return mcpx.ErrorResult("failed to get agent: %v", err), nil, nil
	}
	return mcpx.ProtoJSONResult(resp.Agent), nil, nil
}

type putAgentInput struct {
	Agent map[string]any `json:"agent" jsonschema:"The agent, as get_agent returns it. With an id, it replaces that agent; without one, it creates a new agent."`
}

func (h *handlers) putAgent(ctx context.Context, req *mcp.CallToolRequest, input putAgentInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	b, err := json.Marshal(input.Agent)
	if err != nil {
		return mcpx.ErrorResult("invalid agent: %v", err), nil, nil
	}
	agent := &gritzv1.Agent{}
	if err := protojson.Unmarshal(b, agent); err != nil {
		return mcpx.ErrorResult("invalid agent: %v", err), nil, nil
	}
	if agent.Id == 0 {
		resp, err := service.CreateAgent(ctx, &gritzv1.CreateAgentRequest{Agent: agent})
		if err != nil {
			return mcpx.ErrorResult("failed to create agent: %v", err), nil, nil
		}
		return mcpx.ProtoJSONResult(resp.Agent), nil, nil
	}
	resp, err := service.UpdateAgent(ctx, &gritzv1.UpdateAgentRequest{Agent: agent})
	if err != nil {
		return mcpx.ErrorResult("failed to update agent: %v", err), nil, nil
	}
	return mcpx.ProtoJSONResult(resp.Agent), nil, nil
}

func (h *handlers) deleteAgent(ctx context.Context, req *mcp.CallToolRequest, input agentIDInput) (*mcp.CallToolResult, any, error) {
	service := h.service()
	if _, err := service.DeleteAgent(ctx, &gritzv1.DeleteAgentRequest{Id: input.ID}); err != nil {
		return mcpx.ErrorResult("failed to delete agent: %v", err), nil, nil
	}
	type deleted struct {
		Deleted int64 `json:"deleted"`
	}
	return mcpx.JSONResult(deleted{Deleted: input.ID}), nil, nil
}
