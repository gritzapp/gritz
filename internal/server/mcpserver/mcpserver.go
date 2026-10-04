package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gritzapp/gritz/internal/auth/apiauth"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/proto/gritz/v1/gritzv1connect"
	"github.com/gritzapp/gritz/internal/x/mcpx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Instructions is the prompt shown to MCP clients describing what the
// gritz server offers.
const Instructions = "gritz is an async agent orchestrator that runs AI coding agents in parallel inside Docker containers.\n" +
	"Use it to create and manage tasks that execute in isolated workspaces.\n" +
	"Workspaces define the container image, volumes, environment variables, and MCP servers available to agents.\n" +
	"Each task runs an AI coding agent with access to the codebase and configured tools.\n" +
	"Agents attach links to their tasks for external resources they create, such as GitHub PRs or Jira issues."

// Option configures the behaviour of the registered tools.
type Option func(*handlers)

// WithDefaultAutoArchive sets the default auto_archive applied to tasks
// created via create_task when the call omits the auto_archive param. See
// Task.auto_archive for the value semantics (zero/unset = never, negative
// = archive immediately on terminal status, positive = delay).
func WithDefaultAutoArchive(d time.Duration) Option {
	return func(h *handlers) {
		h.defaultAutoArchive = durationpb.New(d)
	}
}

// NewServer builds an MCP server with the user-facing gritz tools
// registered. The same setup is used by the HTTP handler and by the
// local stdio command.
func NewServer(service gritzv1connect.GritzServiceHandler, opts ...Option) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "gritz",
		Version: "1.0.0",
	}, &mcp.ServerOptions{
		Instructions: Instructions,
	})
	AddTools(server, service, opts...)
	return server
}

// AddTools registers the user-facing gritz tools on the given MCP server.
// Tool handlers proxy to the supplied service, which can be either an
// in-process GritzServiceHandler (server-side) or the generated Connect
// client (remote) since both interfaces share the same method signatures.
func AddTools(server *mcp.Server, service gritzv1connect.GritzServiceHandler, opts ...Option) {
	h := &handlers{service: service}
	for _, opt := range opts {
		opt(h)
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_workspaces",
		Description: "List available workspaces",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, h.listWorkspaces)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_task",
		Description: "Create a new task",
	}, h.createTask)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_task",
		Description: "Get full details of a task including its event stream and links",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, h.getTask)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_tasks",
		Description: "List all tasks",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, h.listTasks)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "update_task",
		Description: "Add an instruction to a task, optionally start it",
	}, h.updateTask)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "archive_task",
		Description: "Archive a task. The task must be in a terminal state (completed, failed, or cancelled) — running or pending tasks cannot be archived.",
	}, h.archiveTask)
}

// Handler returns an http.Handler that serves the MCP Streamable HTTP
// protocol. The handler expects auth middleware to have set UserInfo in
// the request context.
func Handler(service gritzv1connect.GritzServiceHandler) http.Handler {
	server := NewServer(service)
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		if apiauth.Caller(r.Context()) == nil {
			return nil
		}
		return server
	}, &mcp.StreamableHTTPOptions{
		Stateless: true,
	})
}

type handlers struct {
	service gritzv1connect.GritzServiceHandler
	// defaultAutoArchive is applied to create_task when the call omits the
	// auto_archive param. nil means no default is sent (server behavior
	// unchanged).
	defaultAutoArchive *durationpb.Duration
}

type listWorkspacesInput struct{}

func (h *handlers) listWorkspaces(ctx context.Context, req *mcp.CallToolRequest, input listWorkspacesInput) (*mcp.CallToolResult, any, error) {
	resp, err := h.service.ListWorkspaces(ctx, &gritzv1.ListWorkspacesRequest{})
	if err != nil {
		return mcpx.ErrorResult("failed to list workspaces: %v", err), nil, nil
	}
	type workspace struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		RunnerID    string `json:"runner_id"`
	}
	result := make([]workspace, len(resp.Workspaces))
	for i, ws := range resp.Workspaces {
		result[i] = workspace{
			Name:        ws.Name,
			Description: ws.Description,
			RunnerID:    ws.RunnerId,
		}
	}
	return mcpx.JSONResult(result), nil, nil
}

type createTaskInput struct {
	Name        string `json:"name,omitempty" jsonschema:"A short name for the task"`
	Workspace   string `json:"workspace" jsonschema:"The workspace to run the task in"`
	Instruction string `json:"instruction" jsonschema:"The instruction text for the task"`
	Runner      string `json:"runner" jsonschema:"Runner ID to target"`
	AutoArchive string `json:"auto_archive,omitempty" jsonschema:"Auto-archive the task this long after it reaches a terminal status, as a Go duration string (e.g. \"30m\", \"1h\"). \"0\" = never, a negative value like \"-1s\" = archive immediately on terminal status, positive = delay. When omitted, the server default is used."`
}

func (h *handlers) createTask(ctx context.Context, _ *mcp.CallToolRequest, input createTaskInput) (*mcp.CallToolResult, any, error) {
	req := &gritzv1.CreateTaskRequest{
		Name:      input.Name,
		Workspace: input.Workspace,
		Runner:    input.Runner,
		Instructions: []*gritzv1.Instruction{
			{Text: input.Instruction},
		},
		AutoArchive: h.defaultAutoArchive,
	}
	if input.AutoArchive != "" {
		d, err := time.ParseDuration(input.AutoArchive)
		if err != nil {
			return mcpx.ErrorResult("invalid auto_archive %q: %v", input.AutoArchive, err), nil, nil
		}
		req.AutoArchive = durationpb.New(d)
	}
	resp, err := h.service.CreateTask(ctx, req)
	if err != nil {
		return mcpx.ErrorResult("failed to create task: %v", err), nil, nil
	}
	return mcpx.JSONResult(taskSummaryOf(resp.Task)), nil, nil
}

type getTaskInput struct {
	ID int64 `json:"id" jsonschema:"The task ID"`
}

func (h *handlers) getTask(ctx context.Context, req *mcp.CallToolRequest, input getTaskInput) (*mcp.CallToolResult, any, error) {
	// Event-native: compose the header, the full event stream (all arms, in
	// stream order), and the links from the primitives. The old synthesized
	// instructions/logs projections are dropped — a caller wanting just the
	// instruction arm or just the report/lifecycle activity filters the raw
	// events itself, matching the webui timeline shape.
	taskResp, err := h.service.GetTask(ctx, &gritzv1.GetTaskRequest{Id: input.ID})
	if err != nil {
		return mcpx.ErrorResult("failed to get task: %v", err), nil, nil
	}
	eventsResp, err := h.service.ListEventsByTask(ctx, &gritzv1.ListEventsByTaskRequest{TaskId: input.ID})
	if err != nil {
		return mcpx.ErrorResult("failed to list events: %v", err), nil, nil
	}
	linksResp, err := h.service.ListLinks(ctx, &gritzv1.ListLinksRequest{TaskId: input.ID})
	if err != nil {
		return mcpx.ErrorResult("failed to list links: %v", err), nil, nil
	}
	type link struct {
		ID        int64  `json:"id"`
		Relevance string `json:"relevance"`
		URL       string `json:"url"`
		Title     string `json:"title,omitempty"`
		Subscribe bool   `json:"subscribe"`
	}
	type taskDetails struct {
		ID        int64             `json:"id"`
		Name      string            `json:"name"`
		Workspace string            `json:"workspace"`
		Runner    string            `json:"runner,omitempty"`
		Status    string            `json:"status"`
		URL       string            `json:"url,omitempty"`
		Links     []link            `json:"links"`
		Events    []json.RawMessage `json:"events"`
	}
	task := taskResp.Task
	result := taskDetails{
		ID:        task.Id,
		Name:      task.Name,
		Workspace: task.Workspace,
		Runner:    task.Runner,
		Status:    task.Status.String(),
		URL:       task.Url,
	}
	for _, l := range linksResp.Links {
		result.Links = append(result.Links, link{
			ID:        l.Id,
			Relevance: l.Relevance,
			URL:       l.Url,
			Title:     l.Title,
			Subscribe: l.Subscribe,
		})
	}
	// Marshal events with protojson so their arms serialize under their proper
	// wire names (the fat Event proto doesn't round-trip through encoding/json).
	marshalOpts := protojson.MarshalOptions{Indent: "  "}
	for _, event := range eventsResp.Events {
		data, err := marshalOpts.Marshal(event)
		if err != nil {
			return mcpx.ErrorResult("failed to format event: %v", err), nil, nil
		}
		result.Events = append(result.Events, data)
	}
	return mcpx.JSONResult(result), nil, nil
}

type listTasksInput struct{}

func (h *handlers) listTasks(ctx context.Context, req *mcp.CallToolRequest, input listTasksInput) (*mcp.CallToolResult, any, error) {
	resp, err := h.service.ListTasks(ctx, &gritzv1.ListTasksRequest{})
	if err != nil {
		return mcpx.ErrorResult("failed to list tasks: %v", err), nil, nil
	}
	result := make([]taskSummary, len(resp.Tasks))
	for i, t := range resp.Tasks {
		result[i] = taskSummaryOf(t)
	}
	return mcpx.JSONResult(result), nil, nil
}

type updateTaskInput struct {
	ID          int64  `json:"id" jsonschema:"The task ID to update"`
	Instruction string `json:"instruction" jsonschema:"Instruction text to add to the task"`
	URL         string `json:"url,omitempty" jsonschema:"Optional URL associated with the instruction (e.g. GitHub issue, Jira ticket)"`
	Start       bool   `json:"start,omitempty" jsonschema:"Start the task (non-interrupting if already running)"`
}

func (h *handlers) updateTask(ctx context.Context, req *mcp.CallToolRequest, input updateTaskInput) (*mcp.CallToolResult, any, error) {
	_, err := h.service.UpdateTask(ctx, &gritzv1.UpdateTaskRequest{
		Id:    input.ID,
		Start: input.Start,
		AddInstructions: []*gritzv1.Instruction{
			{Text: input.Instruction, Url: input.URL},
		},
	})
	if err != nil {
		return mcpx.ErrorResult("failed to update task: %v", err), nil, nil
	}
	resp, err := h.service.GetTask(ctx, &gritzv1.GetTaskRequest{Id: input.ID})
	if err != nil {
		return mcpx.ErrorResult("failed to get updated task: %v", err), nil, nil
	}
	return mcpx.JSONResult(taskSummaryOf(resp.Task)), nil, nil
}

type archiveTaskInput struct {
	ID int64 `json:"id" jsonschema:"The task ID to archive"`
}

func (h *handlers) archiveTask(ctx context.Context, req *mcp.CallToolRequest, input archiveTaskInput) (*mcp.CallToolResult, any, error) {
	_, err := h.service.ArchiveTask(ctx, &gritzv1.ArchiveTaskRequest{
		Id: input.ID,
	})
	if err != nil {
		return mcpx.ErrorResult("failed to archive task: %v", err), nil, nil
	}
	resp, err := h.service.GetTask(ctx, &gritzv1.GetTaskRequest{Id: input.ID})
	if err != nil {
		return mcpx.ErrorResult("failed to get archived task: %v", err), nil, nil
	}
	return mcpx.JSONResult(taskSummaryOf(resp.Task)), nil, nil
}

type taskSummary struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Workspace string `json:"workspace"`
	Status    string `json:"status"`
	URL       string `json:"url,omitempty"`
}

func taskSummaryOf(t *gritzv1.Task) taskSummary {
	return taskSummary{
		ID:        t.Id,
		Name:      t.Name,
		Workspace: t.Workspace,
		Status:    t.Status.String(),
		URL:       t.Url,
	}
}
