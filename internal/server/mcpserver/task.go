package mcpserver

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/mcpx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	// defaultLogBytes and maxLogBytes bound the transcript get_task_log returns.
	defaultLogBytes = 64 << 10
	maxLogBytes     = 1 << 20
)

// addTaskTools registers the task and workspace tools.
func (h *handlers) addTaskTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_workspaces",
		Description: "List available workspaces",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, h.listWorkspaces)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_task",
		Description: "Create a new task and start it, or create it as a draft that update_task with start set starts later",
	}, h.createTask)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_task",
		Description: "Get full details of a task including its event stream, links and the actions it allows",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, h.getTask)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_tasks",
		Description: "List tasks, newest first, one page at a time. Pass next_page_token back as page_token for the next page; it is empty on the last page.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, h.listTasks)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "update_task",
		Description: "Add an instruction to a task, rename it, change its auto-archive delay, or start it. Set at least one of instruction, name, auto_archive or start.",
	}, h.updateTask)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "cancel_task",
		Description: "Cancel a pending or running task",
	}, h.cancelTask)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "restart_task",
		Description: "Restart a task: stop its current run, if any, and run it again",
	}, h.restartTask)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "archive_task",
		Description: "Archive a task. The task must be in a terminal state (completed, failed, or cancelled) — running or pending tasks cannot be archived.",
	}, h.archiveTask)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "unarchive_task",
		Description: "Unarchive an archived task",
	}, h.unarchiveTask)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_task_log",
		Description: "Get the tail of a task's agent transcript, the raw log its agent wrote, ending at before (empty for the end of the log). Pass the returned before back to read the bytes preceding it; it is empty at the start of the log.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, h.getTaskLog)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_link",
		Description: "Link an external resource, such as a pull request or an issue, to a task. With subscribe, the resource's events are routed to the task.",
	}, h.createLink)
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
	Instruction string `json:"instruction,omitempty" jsonschema:"The instruction text for the task. Required unless draft is set."`
	Runner      string `json:"runner" jsonschema:"Runner ID to target"`
	AutoArchive string `json:"auto_archive,omitempty" jsonschema:"Auto-archive the task this long after it reaches a terminal status, as a Go duration string (e.g. \"30m\", \"1h\"). \"0\" = never, a negative value like \"-1s\" = archive immediately on terminal status, positive = delay. When omitted, the server default is used."`
	Namespace   string `json:"namespace,omitempty" jsonschema:"The task's namespace: only routing rules in the same namespace see its subscriptions. Empty is the default namespace. It can't be changed later."`
	Draft       bool   `json:"draft,omitempty" jsonschema:"Create the task as a draft, which doesn't run until update_task starts it"`
}

func (h *handlers) createTask(ctx context.Context, _ *mcp.CallToolRequest, input createTaskInput) (*mcp.CallToolResult, any, error) {
	if input.Instruction == "" && !input.Draft {
		return mcpx.ErrorResult("instruction is required unless draft is set"), nil, nil
	}
	req := &gritzv1.CreateTaskRequest{
		Name:        input.Name,
		Workspace:   input.Workspace,
		Runner:      input.Runner,
		Namespace:   input.Namespace,
		Draft:       input.Draft,
		AutoArchive: h.defaultAutoArchive,
	}
	if input.Instruction != "" {
		req.Instructions = []*gritzv1.Instruction{{Text: input.Instruction}}
	}
	if input.AutoArchive != "" {
		d, err := parseAutoArchive(input.AutoArchive)
		if err != nil {
			return mcpx.ErrorResult("%v", err), nil, nil
		}
		req.AutoArchive = d
	}
	resp, err := h.service.CreateTask(ctx, req)
	if err != nil {
		return mcpx.ErrorResult("failed to create task: %v", err), nil, nil
	}
	return mcpx.JSONResult(taskSummaryOf(resp.Task)), nil, nil
}

type getTaskInput struct {
	ID         int64    `json:"id" jsonschema:"The task ID"`
	EventTypes []string `json:"event_types,omitempty" jsonschema:"Only return events of these types: \"instruction\", \"external\", \"report\", \"lifecycle\" or \"link\". Empty returns every event."`
}

type taskLink struct {
	ID        int64  `json:"id"`
	Relevance string `json:"relevance"`
	URL       string `json:"url"`
	Title     string `json:"title,omitempty"`
	Subscribe bool   `json:"subscribe"`
}

func taskLinkOf(l *gritzv1.TaskLink) taskLink {
	return taskLink{
		ID:        l.Id,
		Relevance: l.Relevance,
		URL:       l.Url,
		Title:     l.Title,
		Subscribe: l.Subscribe,
	}
}

func (h *handlers) getTask(ctx context.Context, req *mcp.CallToolRequest, input getTaskInput) (*mcp.CallToolResult, any, error) {
	// Event-native: compose the header, the full event stream (all arms, in
	// stream order), and the links from the primitives. The old synthesized
	// instructions/logs projections are dropped — a caller wanting just the
	// instruction arm or just the report/lifecycle activity filters the raw
	// events by type, matching the webui timeline shape.
	taskResp, err := h.service.GetTask(ctx, &gritzv1.GetTaskRequest{Id: input.ID})
	if err != nil {
		return mcpx.ErrorResult("failed to get task: %v", err), nil, nil
	}
	events, err := gritzclient.ListAllEventsByTask(ctx, h.service, input.ID, input.EventTypes)
	if err != nil {
		return mcpx.ErrorResult("failed to list events: %v", err), nil, nil
	}
	linksResp, err := h.service.ListLinks(ctx, &gritzv1.ListLinksRequest{TaskId: input.ID})
	if err != nil {
		return mcpx.ErrorResult("failed to list links: %v", err), nil, nil
	}
	type actions struct {
		Archive   bool `json:"archive"`
		Cancel    bool `json:"cancel"`
		Restart   bool `json:"restart"`
		Start     bool `json:"start"`
		Unarchive bool `json:"unarchive"`
	}
	type taskDetails struct {
		taskSummary
		AutoArchive string            `json:"auto_archive,omitempty"`
		Actions     actions           `json:"actions"`
		Links       []taskLink        `json:"links"`
		Events      []json.RawMessage `json:"events"`
	}
	task := taskResp.Task
	a := task.GetActions()
	result := taskDetails{
		taskSummary: taskSummaryOf(task),
		Actions: actions{
			Archive:   a.GetArchive(),
			Cancel:    a.GetCancel(),
			Restart:   a.GetRestart(),
			Start:     a.GetStart(),
			Unarchive: a.GetUnarchive(),
		},
	}
	if task.AutoArchive != nil {
		result.AutoArchive = task.AutoArchive.AsDuration().String()
	}
	for _, l := range linksResp.Links {
		result.Links = append(result.Links, taskLinkOf(l))
	}
	// Marshal events with protojson so their arms serialize under their proper
	// wire names (the fat Event proto doesn't round-trip through encoding/json).
	marshalOpts := protojson.MarshalOptions{Indent: "  "}
	for _, event := range events {
		// Command events drive the runner; they are not history.
		if event.GetCommand() != nil {
			continue
		}
		data, err := marshalOpts.Marshal(event)
		if err != nil {
			return mcpx.ErrorResult("failed to format event: %v", err), nil, nil
		}
		result.Events = append(result.Events, data)
	}
	return mcpx.JSONResult(result), nil, nil
}

type listTasksInput struct {
	PageSize  int32  `json:"page_size,omitempty" jsonschema:"Max tasks to return (default 50, max 100)"`
	PageToken string `json:"page_token,omitempty" jsonschema:"A previous result's next_page_token; empty for the first page"`
	Archived  bool   `json:"archived,omitempty" jsonschema:"Include archived tasks alongside active ones"`
}

func (h *handlers) listTasks(ctx context.Context, req *mcp.CallToolRequest, input listTasksInput) (*mcp.CallToolResult, any, error) {
	resp, err := h.service.ListTasks(ctx, &gritzv1.ListTasksRequest{
		PageSize:  input.PageSize,
		PageToken: input.PageToken,
		Archived:  input.Archived,
	})
	if err != nil {
		return mcpx.ErrorResult("failed to list tasks: %v", err), nil, nil
	}
	type page struct {
		Tasks         []taskSummary `json:"tasks"`
		NextPageToken string        `json:"next_page_token"`
	}
	result := page{
		Tasks:         make([]taskSummary, len(resp.Tasks)),
		NextPageToken: resp.NextPageToken,
	}
	for i, t := range resp.Tasks {
		result.Tasks[i] = taskSummaryOf(t)
	}
	return mcpx.JSONResult(result), nil, nil
}

type updateTaskInput struct {
	ID          int64  `json:"id" jsonschema:"The task ID to update"`
	Instruction string `json:"instruction,omitempty" jsonschema:"Instruction text to add to the task"`
	URL         string `json:"url,omitempty" jsonschema:"Optional URL associated with the instruction (e.g. GitHub issue, Jira ticket)"`
	Name        string `json:"name,omitempty" jsonschema:"A new name for the task"`
	AutoArchive string `json:"auto_archive,omitempty" jsonschema:"A new auto-archive delay, as a Go duration string (e.g. \"30m\", \"1h\"). \"0\" = never, a negative value like \"-1s\" = archive immediately on terminal status, positive = delay. When omitted, the delay is unchanged."`
	Start       bool   `json:"start,omitempty" jsonschema:"Start the task (non-interrupting if already running)"`
}

func (h *handlers) updateTask(ctx context.Context, req *mcp.CallToolRequest, input updateTaskInput) (*mcp.CallToolResult, any, error) {
	if input.Instruction == "" && input.Name == "" && input.AutoArchive == "" && !input.Start {
		return mcpx.ErrorResult("nothing to update: set at least one of instruction, name, auto_archive or start"), nil, nil
	}
	if input.URL != "" && input.Instruction == "" {
		return mcpx.ErrorResult("url needs an instruction"), nil, nil
	}
	updateReq := &gritzv1.UpdateTaskRequest{
		Id:    input.ID,
		Name:  input.Name,
		Start: input.Start,
	}
	if input.Instruction != "" {
		updateReq.AddInstructions = []*gritzv1.Instruction{{Text: input.Instruction, Url: input.URL}}
	}
	if input.AutoArchive != "" {
		d, err := parseAutoArchive(input.AutoArchive)
		if err != nil {
			return mcpx.ErrorResult("%v", err), nil, nil
		}
		updateReq.AutoArchive = d
	}
	if _, err := h.service.UpdateTask(ctx, updateReq); err != nil {
		return mcpx.ErrorResult("failed to update task: %v", err), nil, nil
	}
	return h.taskResult(ctx, input.ID), nil, nil
}

type taskIDInput struct {
	ID int64 `json:"id" jsonschema:"The task ID"`
}

func (h *handlers) cancelTask(ctx context.Context, req *mcp.CallToolRequest, input taskIDInput) (*mcp.CallToolResult, any, error) {
	if _, err := h.service.CancelTask(ctx, &gritzv1.CancelTaskRequest{Id: input.ID}); err != nil {
		return mcpx.ErrorResult("failed to cancel task: %v", err), nil, nil
	}
	return h.taskResult(ctx, input.ID), nil, nil
}

func (h *handlers) restartTask(ctx context.Context, req *mcp.CallToolRequest, input taskIDInput) (*mcp.CallToolResult, any, error) {
	if _, err := h.service.RestartTask(ctx, &gritzv1.RestartTaskRequest{Id: input.ID}); err != nil {
		return mcpx.ErrorResult("failed to restart task: %v", err), nil, nil
	}
	return h.taskResult(ctx, input.ID), nil, nil
}

func (h *handlers) archiveTask(ctx context.Context, req *mcp.CallToolRequest, input taskIDInput) (*mcp.CallToolResult, any, error) {
	if _, err := h.service.ArchiveTask(ctx, &gritzv1.ArchiveTaskRequest{Id: input.ID}); err != nil {
		return mcpx.ErrorResult("failed to archive task: %v", err), nil, nil
	}
	return h.taskResult(ctx, input.ID), nil, nil
}

func (h *handlers) unarchiveTask(ctx context.Context, req *mcp.CallToolRequest, input taskIDInput) (*mcp.CallToolResult, any, error) {
	if _, err := h.service.UnarchiveTask(ctx, &gritzv1.UnarchiveTaskRequest{Id: input.ID}); err != nil {
		return mcpx.ErrorResult("failed to unarchive task: %v", err), nil, nil
	}
	return h.taskResult(ctx, input.ID), nil, nil
}

// taskResult renders the summary of a task a tool just changed.
func (h *handlers) taskResult(ctx context.Context, id int64) *mcp.CallToolResult {
	resp, err := h.service.GetTask(ctx, &gritzv1.GetTaskRequest{Id: id})
	if err != nil {
		return mcpx.ErrorResult("failed to get updated task: %v", err)
	}
	return mcpx.JSONResult(taskSummaryOf(resp.Task))
}

type getTaskLogInput struct {
	ID       int64  `json:"id" jsonschema:"The task ID"`
	Before   string `json:"before,omitempty" jsonschema:"A previous result's before, to read the bytes preceding it; empty for the end of the log"`
	MaxBytes int    `json:"max_bytes,omitempty" jsonschema:"Roughly how many bytes to return (default 65536, max 1048576)"`
}

func (h *handlers) getTaskLog(ctx context.Context, req *mcp.CallToolRequest, input getTaskLogInput) (*mcp.CallToolResult, any, error) {
	maxBytes := input.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultLogBytes
	}
	maxBytes = min(maxBytes, maxLogBytes)

	// Walk back from before, newest page first. A page whose chunks don't all
	// fit is read again one chunk at a time, so the log ends on a chunk boundary
	// that the returned before resumes from. The first chunk is always taken,
	// even when it alone exceeds max_bytes, so every call makes progress.
	var pages [][]*gritzv1.LogChunk
	size := 0
	listReq := &gritzv1.ListLogChunksByTaskRequest{TaskId: input.ID, PageToken: input.Before}
	before := input.Before
	for {
		resp, err := h.service.ListLogChunksByTask(ctx, listReq)
		if err != nil {
			return mcpx.ErrorResult("failed to list log chunks: %v", err), nil, nil
		}
		n := 0
		for _, c := range resp.Chunks {
			n += len(c.Data)
		}
		if size+n > maxBytes && len(resp.Chunks) > 1 {
			listReq.PageSize = 1
			continue
		}
		if size+n > maxBytes && size > 0 {
			break
		}
		pages = append(pages, resp.Chunks)
		size += n
		before = resp.PrevPageToken
		if !resp.More {
			before = ""
			break
		}
		if size >= maxBytes {
			break
		}
		listReq.PageToken = before
	}

	var log strings.Builder
	log.Grow(size)
	for _, page := range slices.Backward(pages) {
		for _, c := range page {
			log.Write(c.Data)
		}
	}
	type logTail struct {
		Log    string `json:"log"`
		Before string `json:"before"`
	}
	return mcpx.JSONResult(logTail{Log: log.String(), Before: before}), nil, nil
}

type createLinkInput struct {
	TaskID    int64  `json:"task_id" jsonschema:"The task ID"`
	URL       string `json:"url" jsonschema:"The resource's URL"`
	Title     string `json:"title,omitempty" jsonschema:"A title for the link"`
	Relevance string `json:"relevance" jsonschema:"Why the resource matters to the task, such as \"PR for the fix\""`
	Subscribe bool   `json:"subscribe,omitempty" jsonschema:"Route the resource's events, such as PR comments, to the task"`
}

func (h *handlers) createLink(ctx context.Context, req *mcp.CallToolRequest, input createLinkInput) (*mcp.CallToolResult, any, error) {
	resp, err := h.service.CreateLink(ctx, &gritzv1.CreateLinkRequest{
		TaskId:    input.TaskID,
		Url:       input.URL,
		Title:     input.Title,
		Relevance: input.Relevance,
		Subscribe: input.Subscribe,
	})
	if err != nil {
		return mcpx.ErrorResult("failed to create link: %v", err), nil, nil
	}
	return mcpx.JSONResult(taskLinkOf(resp.Link)), nil, nil
}
