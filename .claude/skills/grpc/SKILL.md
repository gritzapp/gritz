---
name: grpc
description: Guidelines for working with the Connect RPC (gRPC) API. Apply when modifying proto definitions, implementing server handlers, or creating API clients.
---

# gRPC API Development Guide

GRITZ uses [Connect RPC](https://connectrpc.com/) for its API, providing protocol flexibility (gRPC, gRPC-Web, Connect protocol) over HTTP.

## Architecture Overview

- **Proto definitions**: `proto/gritz/v1/gritz.proto`
- **Generated Go code**: `internal/proto/gritz/v1/` (gitignored)
- **Generated TypeScript code**: `webui/src/gen/` (committed)
- **Server implementation**: `internal/server/server.go`
- **Go client package**: `internal/gritzclient/`
- **TypeScript client**: Uses `@connectrpc/connect-query` with TanStack Query

## Adding a New RPC

### 1. Define the proto messages and service method

Edit `proto/gritz/v1/gritz.proto`:

```protobuf
service GritzService {
  // ... existing RPCs ...
  rpc MyNewMethod(MyNewRequest) returns (MyNewResponse);
}

message MyNewRequest {
  int64 id = 1;
  string name = 2;
}

message MyNewResponse {
  bool success = 1;
}
```

### 2. Generate the code

```bash
mise run generate
```

This runs:
- `go tool buf generate` - generates protobuf messages and Connect client/server interfaces (Go)
- `go generate ./...` - runs any other code generators

For the frontend TypeScript client, run:

```bash
cd webui && pnpm run generate
```

This generates:
- `webui/src/gen/gritz/v1/gritz_pb.ts` - TypeScript types and message schemas
- `webui/src/gen/gritz/v1/gritz-GritzService_connectquery.ts` - Connect-Query method exports

### 3. Implement the server handler

The server embeds `UnimplementedGritzServiceHandler`, which provides default "not implemented" responses for all RPCs. Override the method in `internal/server/server.go`:

```go
func (s *Server) MyNewMethod(ctx context.Context, req *gritzv1.MyNewMethodRequest) (*gritzv1.MyNewMethodResponse, error) {
    // Validate input
    if req.Id == 0 {
        return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("id is required"))
    }

    // Call store layer
    result, err := s.tasks.DoSomething(req.Id)
    if err != nil {
        return nil, connect.NewError(connect.CodeInternal, err)
    }

    // Return response
    return &gritzv1.MyNewMethodResponse{
        Success: true,
    }, nil
}
```

### 4. Use Connect error codes

Always use appropriate Connect error codes:

```go
import "connectrpc.com/connect"

// Common error codes
connect.CodeInvalidArgument  // Bad request parameters
connect.CodeNotFound         // Resource doesn't exist
connect.CodeInternal         // Server-side error
connect.CodeUnimplemented    // Method not supported
connect.CodeAlreadyExists    // Duplicate resource
connect.CodePermissionDenied // Authorization failure
```

## Client Usage

### Creating a client

```go
import "github.com/gritzapp/gritz/internal/gritzclient"

// HTTP client
client := gritzclient.New("http://localhost:6464")

// Unix socket client (used inside containers)
client := gritzclient.New("unix:///gritz/socket")
```

### Making RPC calls

```go
import gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"

// Unary call
resp, err := client.GetTask(ctx, &gritzv1.GetTaskRequest{Id: 123})
if err != nil {
    // Handle error
}
task := resp.Task

// Call with multiple fields
resp, err := client.CreateTask(ctx, &gritzv1.CreateTaskRequest{
    Name:      "My Task",
    Workspace: "default",
    Instructions: []*gritzv1.Instruction{
        {Text: "Do something", Url: "https://example.com"},
    },
})
```

## Data Conversion Patterns

The server converts between proto messages and store domain models:

```go
// Proto to store (in handler)
instructions := make([]store.Instruction, len(req.Instructions))
for i, inst := range req.Instructions {
    instructions[i] = store.Instruction{
        Text: inst.Text,
        URL:  inst.Url,  // Note: proto uses "Url", store uses "URL"
    }
}

// Store to proto (for responses)
func taskToProto(t *store.Task) *gritzv1.Task {
    return &gritzv1.Task{
        Id:        t.ID,
        Name:      t.Name,
        Status:    string(t.Status),
        CreatedAt: timestamppb.New(t.CreatedAt),
    }
}
```

## Protobuf Best Practices

### Field naming
- Use snake_case in proto files: `parent_id`, `created_at`
- Generated Go uses PascalCase: `ParentId`, `CreatedAt`

### Timestamps
```protobuf
import "google/protobuf/timestamp.proto";

message Task {
  google.protobuf.Timestamp created_at = 7;
}
```

Convert with `timestamppb`:
```go
import "google.golang.org/protobuf/types/known/timestamppb"

// Go time to proto
protoTime := timestamppb.New(time.Now())

// Proto to Go time
goTime := protoTimestamp.AsTime()
```

### Repeated fields (lists)
```protobuf
message ListTasksResponse {
  repeated Task tasks = 1;
}
```

### Optional fields
In proto3, all fields are optional by default. Empty/zero values are not serialized.

### Maps
```protobuf
message McpServer {
  map<string, string> env = 4;
}
```

## HTTP/JSON API Access

Connect RPC endpoints are accessible via HTTP POST with JSON:

```bash
# List tasks
curl -X POST http://localhost:6464/gritz.v1.GritzService/ListTasks \
  -H "Content-Type: application/json" \
  -d '{"statuses": ["pending", "running"]}'

# Get task
curl -X POST http://localhost:6464/gritz.v1.GritzService/GetTask \
  -H "Content-Type: application/json" \
  -d '{"id": 123}'
```

URL pattern: `/{package}.{service}/{method}`

## Testing

### Unit testing handlers

Mock the client interface for testing:

```go
//go:generate go tool moq -pkg mypackage -out client_moq_test.go ../gritzclient Client

func TestMyHandler(t *testing.T) {
    mockClient := &ClientMock{
        GetTaskFunc: func(ctx context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
            return &gritzv1.GetTaskResponse{
                Task: &gritzv1.Task{Id: req.Id, Name: "Test"},
            }, nil
        },
    }
    // Use mockClient in tests
}
```

## Buf Configuration

### buf.yaml
Configures the protobuf module and linting rules:
```yaml
version: v2
modules:
  - path: proto
lint:
  use:
    - STANDARD
```

### buf.gen.yaml
Specifies code generation plugins:
```yaml
version: v2
plugins:
  - remote: buf.build/protocolbuffers/go
    out: internal/proto
    opt: paths=source_relative
  - remote: buf.build/connectrpc/go
    out: internal/proto
    opt:
      - paths=source_relative
      - simple
```

## Common Patterns

### Logging in handlers
```go
s.log.Info("task created", "id", task.ID, "workspace", task.Workspace)
```

### Handling not found
```go
task, err := s.tasks.Get(req.Id)
if err != nil {
    return nil, connect.NewError(connect.CodeNotFound, err)
}
```

### Batch operations
```go
func (s *Server) UploadLogs(ctx context.Context, req *gritzv1.UploadLogsRequest) (*gritzv1.UploadLogsResponse, error) {
    for _, entry := range req.Entries {
        log := &model.Log{
            TaskID:  req.TaskId,
            Type:    entry.Type,
            Content: entry.Content,
        }
        if err := s.logs.Create(ctx, log); err != nil {
            return nil, connect.NewError(connect.CodeInternal, err)
        }
    }
    return &gritzv1.UploadLogsResponse{}, nil
}
```

## TypeScript Client (Web UI)

The web UI uses [@connectrpc/connect-query](https://github.com/connectrpc/connect-query-es) for type-safe API calls with TanStack Query integration.

### Setup

The transport is configured in `webui/src/lib/transport.ts` and provided via `TransportProvider` in `main.tsx`. This is already set up.

### Fetching data with useQuery

Import the generated method descriptor and use it with `useQuery` from `@connectrpc/connect-query`:

```tsx
import { useQuery } from '@connectrpc/connect-query'
import { listTasks } from '@/gen/gritz/v1/gritz-GritzService_connectquery'

function TaskList() {
  const { data, isLoading, error } = useQuery(listTasks, {
    statuses: ['pending', 'running'],
  })

  if (isLoading) return <div>Loading...</div>
  if (error) return <div>Error: {error.message}</div>

  return (
    <ul>
      {data?.tasks.map(task => (
        <li key={String(task.id)}>{task.name}</li>
      ))}
    </ul>
  )
}
```

### Mutations with useMutation

```tsx
import { useMutation, useQueryClient } from '@connectrpc/connect-query'
import { createTask, listTasks } from '@/gen/gritz/v1/gritz-GritzService_connectquery'
import { create } from '@bufbuild/protobuf'
import { InstructionSchema } from '@/gen/gritz/v1/gritz_pb'

function CreateTaskButton() {
  const queryClient = useQueryClient()

  const mutation = useMutation(createTask, {
    onSuccess: () => {
      // Invalidate and refetch tasks list
      queryClient.invalidateQueries({ queryKey: [listTasks.service.typeName] })
    },
  })

  const handleCreate = () => {
    mutation.mutate({
      name: 'New Task',
      workspace: 'default',
      instructions: [
        create(InstructionSchema, { text: 'Do something', url: '' }),
      ],
    })
  }

  return <button onClick={handleCreate}>Create Task</button>
}
```

### Important: bigint handling

Protobuf `int64` fields are generated as TypeScript `bigint`. When displaying or using these values:

```tsx
// Convert to string for display
<span>Task ID: {String(task.id)}</span>

// Convert to number if needed (be careful with large values)
const numId = Number(task.id)

// Pass bigint directly to other proto messages
const request = { id: task.id }  // OK - keeps bigint type
```

### TypeScript types

The generated types are in `webui/src/gen/gritz/v1/gritz_pb.ts`:

```tsx
import type { Task, Event, TaskLink } from '@/gen/gritz/v1/gritz_pb'

function TaskCard({ task }: { task: Task }) {
  return (
    <div>
      <h3>{task.name}</h3>
      <p>Status: {task.status}</p>
      <p>Workspace: {task.workspace}</p>
    </div>
  )
}
```

### webui/buf.gen.yaml

The TypeScript code generation is configured in `webui/buf.gen.yaml`:

```yaml
version: v2
inputs:
  - directory: ../proto
plugins:
  - local: protoc-gen-es
    out: src/gen
    opt: target=ts
  - local: protoc-gen-connect-query
    out: src/gen
    opt: target=ts
```
