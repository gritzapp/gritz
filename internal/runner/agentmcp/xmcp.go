package agentmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/gritzapp/gritz/internal/auth/agentauth"
	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/mcpx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/encoding/protojson"
)

type Server struct {
	client       gritzclient.Client
	task         *model.Task
	capabilities []string
}

func NewServer(client gritzclient.Client, task *model.Task, capabilities []string) *Server {
	return &Server{
		client:       client,
		task:         task,
		capabilities: capabilities,
	}
}

func (s *Server) hasCapability(capability string) bool {
	return slices.Contains(s.capabilities, capability)
}

func (s *Server) AddTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_link",
		Description: "Associate an external resource (PR, Jira ticket, etc.) with the current task",
	}, s.createLink)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "report",
		Description: "Report a problem or log message for the current task",
	}, s.report)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_my_task",
		Description: "Get the current task instructions, links, and events",
	}, s.getMyTask)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "update_my_task",
		Description: "Update the current task's name",
	}, s.updateMyTask)

	if s.hasCapability(agentauth.CapabilityGitHubToken) {
		mcp.AddTool(server, &mcp.Tool{
			Name:        "get_github_token",
			Description: "Get a short-lived GitHub App installation token for the current org. Fallback for shell-outs (e.g. a one-off `gh` invocation) that need a raw GITHUB_TOKEN — primary GitHub access goes through git (credential helper) and the github MCP server.",
		}, s.getGitHubToken)
	}
}

type createLinkInput struct {
	Relevance string `json:"relevance" jsonschema:"Describe how this link is relevant to the task"`
	URL       string `json:"url" jsonschema:"URL of the external resource"`
	Title     string `json:"title,omitempty" jsonschema:"Optional display title for the link"`
	Subscribe bool   `json:"subscribe,omitempty" jsonschema:"True to receive events for this link"`
}

func (s *Server) createLink(ctx context.Context, req *mcp.CallToolRequest, input createLinkInput) (*mcp.CallToolResult, any, error) {
	_, err := s.client.CreateLink(ctx, &gritzv1.CreateLinkRequest{
		TaskId:    s.task.ID,
		Relevance: input.Relevance,
		Url:       input.URL,
		Title:     input.Title,
		Subscribe: input.Subscribe,
	})
	if err != nil {
		return mcpx.ErrorResult("failed to create link: %v", err), nil, nil
	}

	return textResult("Link created: %s", input.URL), nil, nil
}

type reportInput struct {
	Message string `json:"message" jsonschema:"The message to report"`
}

func (s *Server) report(ctx context.Context, req *mcp.CallToolRequest, input reportInput) (*mcp.CallToolResult, any, error) {
	// The wire is unchanged (UploadLogs) until the agent surface lands, but the
	// server now re-points the `llm` channel onto the event stream: this upload
	// appends a from-agent `report` event rather than a logs row.
	_, err := s.client.UploadLogs(ctx, &gritzv1.UploadLogsRequest{
		TaskId: s.task.ID,
		Entries: []*gritzv1.LogEntry{
			{Type: "llm", Content: input.Message},
		},
	})
	if err != nil {
		return mcpx.ErrorResult("failed to upload log: %v", err), nil, nil
	}

	return textResult("Report submitted"), nil, nil
}

func (s *Server) getMyTask(ctx context.Context, req *mcp.CallToolRequest, input any) (*mcp.CallToolResult, any, error) {
	task, err := s.client.GetTask(ctx, &gritzv1.GetTaskRequest{Id: s.task.ID})
	if err != nil {
		return mcpx.ErrorResult("failed to get task: %v", err), nil, nil
	}

	events, err := s.client.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{
		TaskId: s.task.ID,
		Types:  []string{model.EventTypeInstruction, model.EventTypeExternal},
	})
	if err != nil {
		return mcpx.ErrorResult("failed to get task events: %v", err), nil, nil
	}

	links, err := s.client.ListLinks(ctx, &gritzv1.ListLinksRequest{TaskId: s.task.ID})
	if err != nil {
		return mcpx.ErrorResult("failed to get task links: %v", err), nil, nil
	}

	return mcpx.JSONResult(taskDetailsToMap(task.GetTask(), events.GetEvents(), links.GetLinks())), nil, nil
}

type updateMyTaskInput struct {
	Name string `json:"name,omitempty" jsonschema:"The new name for the task"`
}

func (s *Server) updateMyTask(ctx context.Context, _ *mcp.CallToolRequest, input updateMyTaskInput) (*mcp.CallToolResult, any, error) {
	// Note: a task is intentionally not allowed to change its own auto_archive.
	// The value set by the routing rule (or a human) is authoritative for the
	// task's lifetime — see gritzapp/gritz#1094.
	if _, err := s.client.UpdateTask(ctx, &gritzv1.UpdateTaskRequest{
		Id:   s.task.ID,
		Name: input.Name,
	}); err != nil {
		return mcpx.ErrorResult("failed to update task: %v", err), nil, nil
	}

	return textResult("Task updated"), nil, nil
}

type getGitHubTokenInput struct{}

func (s *Server) getGitHubToken(ctx context.Context, req *mcp.CallToolRequest, input getGitHubTokenInput) (*mcp.CallToolResult, any, error) {
	resp, err := s.client.CreateGitHubToken(ctx, &gritzv1.CreateGitHubTokenRequest{})
	if err != nil {
		return mcpx.ErrorResult("failed to create github token: %v", err), nil, nil
	}

	return mcpx.ProtoJSONResult(resp), nil, nil
}

func textResult(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf(format, args...)},
		},
	}
}

// taskDetailsToMap renders the task header, links, and raw event stream to a map
// for JSON output. The event stream is the instruction+external brief; the agent
// reads instruction-arm events straight from it, so there is no synthesized
// `instructions` field.
func taskDetailsToMap(task *gritzv1.Task, taskEvents []*gritzv1.Event, taskLinks []*gritzv1.TaskLink) map[string]any {
	marshalOpts := protojson.MarshalOptions{Indent: "  "}

	links := make([]json.RawMessage, len(taskLinks))
	for i, link := range taskLinks {
		links[i], _ = marshalOpts.Marshal(link)
	}

	events := make([]json.RawMessage, len(taskEvents))
	for i, event := range taskEvents {
		events[i], _ = marshalOpts.Marshal(event)
	}

	return map[string]any{
		"id":        task.Id,
		"name":      task.Name,
		"status":    task.Status.String(),
		"workspace": task.Workspace,
		"namespace": task.Namespace,
		"url":       task.Url,
		"links":     links,
		"events":    events,
	}
}
