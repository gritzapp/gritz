# Add `update_task` tool to server MCP

Issue: https://github.com/icholy/gritz/issues/405

## Problem

The server MCP (`internal/servermcp/servermcp.go`) exposes tools for creating and reading tasks (`create_task`, `get_task`, `list_tasks`, `list_workspaces`), but there is no way to update a task. External clients using the server MCP (e.g. Claude Code running locally) cannot modify tasks after creation — they can't add instructions or restart them.

The agent MCP (`internal/agentmcp/xmcp.go`) has `update_my_task` and `update_child_task`, but these are scoped to the agent's own task and its direct children via the `AgentFilter`. The server MCP needs a general-purpose `update_task` tool that can update any task by ID.

## Design

### MCP Tool Schema

Add an `update_task` tool to the server MCP in `internal/servermcp/servermcp.go`:

```go
type updateTaskInput struct {
	ID          int64  `json:"id" jsonschema:"The task ID to update"`
	Instruction string `json:"instruction" jsonschema:"Instruction text to add to the task"`
	URL         string `json:"url,omitempty" jsonschema:"Optional URL associated with the instruction (e.g. GitHub issue, Jira ticket)"`
	Start       bool   `json:"start,omitempty" jsonschema:"Start the task (non-interrupting if already running)"`
}
```

Fields:

- **`id`** (required): The task ID to update.
- **`instruction`** (required): Text of an instruction to append. Instructions are additive — they are appended to the existing list, never replaced. This is the primary purpose of the tool: giving a task new instructions.
- **`url`** (optional): URL associated with the instruction (e.g. a GitHub issue or Jira ticket).
- **`start`** (optional): Start the task. For running tasks, this queues a restart after the current run finishes. For completed/failed/cancelled tasks, this sets the task to pending.

### Handler Implementation

```go
func (s *Server) updateTask(ctx context.Context, req *mcp.CallToolRequest, input updateTaskInput) (*mcp.CallToolResult, any, error) {
	_, err := s.service.UpdateTask(ctx, &gritzv1.UpdateTaskRequest{
		Id:    input.ID,
		Start: input.Start,
		AddInstructions: []*gritzv1.Instruction{
			{Text: input.Instruction, Url: input.URL},
		},
	})
	if err != nil {
		return errorResult("failed to update task: %v", err), nil, nil
	}
	// Fetch updated task to return current state
	resp, err := s.service.GetTask(ctx, &gritzv1.GetTaskRequest{Id: input.ID})
	if err != nil {
		return errorResult("failed to get updated task: %v", err), nil, nil
	}
	type taskResult struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		Workspace string `json:"workspace"`
		Status    string `json:"status"`
		URL       string `json:"url,omitempty"`
	}
	result := taskResult{
		ID:        resp.Task.Id,
		Name:      resp.Task.Name,
		Workspace: resp.Task.Workspace,
		Status:    resp.Task.Status.String(),
		URL:       fmt.Sprintf("%s/ui/tasks/%d", s.baseURL, resp.Task.Id),
	}
	return jsonResult(result), nil, nil
}
```

### Tool Registration

In the `Handler()` method, add alongside the existing tools:

```go
mcp.AddTool(server, &mcp.Tool{
	Name:        "update_task",
	Description: "Add an instruction to a task, optionally start it",
}, s.updateTask)
```

### Mapping to Existing Infrastructure

This tool requires **no changes** to the proto definitions, server RPC handlers, or store layer. It maps directly to the existing `UpdateTask` RPC:

| Tool field     | Proto field                          | Behavior                    |
|----------------|--------------------------------------|-----------------------------|
| `id`           | `UpdateTaskRequest.id`               | Required, identifies task   |
| `instruction`  | `UpdateTaskRequest.add_instructions` | Required, appended to list  |
| `url`          | `Instruction.url`                    | Paired with instruction     |
| `start`        | `UpdateTaskRequest.start`            | Non-interrupting start      |

### Authorization

The server MCP uses `apiauth.Caller(r.Context())` for auth (set by middleware before the MCP handler). The `UpdateTask` RPC already enforces org-level access via `caller.OrgID` in `Server.UpdateTask`. No additional auth logic is needed in the MCP layer.

### Response

The tool returns the updated task state (id, name, workspace, status, url) by fetching the task after the update. This follows the same pattern as `create_task` which returns the created task's details. This gives the caller confirmation of what changed.

## Trade-offs

### Required instruction

The `instruction` field is required, making the tool's purpose clear: add an instruction to a task. This matches the `create_task` pattern where `instruction` is also required. The `name` field is intentionally omitted — task names are set at creation time via `create_task`, or by the agent itself via `update_my_task`. External callers don't have a use case for renaming tasks after creation.

### Single instruction per call vs. multiple instructions

The `UpdateTaskRequest` proto supports `repeated Instruction add_instructions`, but the tool schema accepts a single `instruction` string. This matches the `create_task` tool pattern (which also takes a single `instruction` string despite the proto supporting a list). Multiple instructions can be added by calling the tool multiple times. This keeps the tool schema simple for LLM callers.

### Returning updated state vs. void response

The agent MCP's `update_my_task` returns a simple "Task updated" text string. This proposal has the server MCP tool fetch and return the updated task state instead. The rationale is that server MCP callers are external and may not have the task details cached — returning the current state saves a follow-up `get_task` call.

### No status field

The tool does not expose a direct `status` field for setting arbitrary statuses. Task status transitions are managed through the state machine (`Start()`, `Stop()`, `Restart()` methods on the model). Exposing `start` is sufficient for the common case of restarting a task. Stopping/cancelling tasks could be added as a separate tool or as a `cancel` boolean on this tool in the future.

## Open Questions

1. **Should `cancel` be included?** The `UpdateTask` RPC doesn't currently support cancellation (that's a separate `StopTask`/`CancelTask` RPC if one exists, or done through the task command state machine). Should this tool also support stopping/cancelling tasks, or should that be a separate `cancel_task` tool?

2. **Should `archive` be included?** Tasks can be archived (`task.Archived` field). Should `update_task` support archiving/unarchiving, or should that remain a separate operation?