package mcpserver

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/mcptest"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gotest.tools/v3/assert"
)

func TestArchiveTask(t *testing.T) {
	client := &gritzclient.ClientMock{
		ArchiveTaskFunc: func(ctx context.Context, req *gritzv1.ArchiveTaskRequest) (*gritzv1.ArchiveTaskResponse, error) {
			assert.Equal(t, req.Id, int64(42))
			return &gritzv1.ArchiveTaskResponse{}, nil
		},
		GetTaskFunc: func(ctx context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
			assert.Equal(t, req.Id, int64(42))
			return &gritzv1.GetTaskResponse{
				Task: &gritzv1.Task{
					Id:        42,
					Name:      "test",
					Workspace: "ws",
					Status:    gritzv1.TaskStatus_COMPLETED,
					Url:       "https://gritz.example.com/ui/tasks/42?org=7",
				},
			}, nil
		},
	}
	session := setupSession(t, client)

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "archive_task",
		Arguments: map[string]any{
			"id": 42,
		},
	})
	assert.NilError(t, err)

	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	assert.DeepEqual(t, got, map[string]any{
		"id":        float64(42),
		"name":      "test",
		"workspace": "ws",
		"status":    "COMPLETED",
		"archived":  false,
		"url":       "https://gritz.example.com/ui/tasks/42?org=7",
	})
}

func TestCreateTask_UsesServerURL(t *testing.T) {
	client := &gritzclient.ClientMock{
		CreateTaskFunc: func(ctx context.Context, req *gritzv1.CreateTaskRequest) (*gritzv1.CreateTaskResponse, error) {
			assert.Equal(t, req.Workspace, "ws")
			assert.Equal(t, req.Runner, "r1")
			assert.Equal(t, len(req.Instructions), 1)
			assert.Equal(t, req.Instructions[0].Text, "do it")
			return &gritzv1.CreateTaskResponse{
				Task: &gritzv1.Task{
					Id:        42,
					Name:      "test",
					Workspace: "ws",
					Status:    gritzv1.TaskStatus_PENDING,
					Url:       "https://gritz.example.com/ui/tasks/42?org=7",
				},
			}, nil
		},
	}
	session := setupSession(t, client)

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "create_task",
		Arguments: map[string]any{
			"workspace":   "ws",
			"instruction": "do it",
			"runner":      "r1",
		},
	})
	assert.NilError(t, err)

	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	assert.DeepEqual(t, got, map[string]any{
		"id":        float64(42),
		"name":      "test",
		"workspace": "ws",
		"status":    "PENDING",
		"archived":  false,
		"url":       "https://gritz.example.com/ui/tasks/42?org=7",
	})
}

func TestCreateTask_AutoArchiveParam(t *testing.T) {
	// With no server default: a provided param is parsed and forwarded, and
	// omitting it leaves auto_archive unset.
	var got *gritzv1.CreateTaskRequest
	client := &gritzclient.ClientMock{
		CreateTaskFunc: func(ctx context.Context, req *gritzv1.CreateTaskRequest) (*gritzv1.CreateTaskResponse, error) {
			got = req
			return &gritzv1.CreateTaskResponse{
				Task: &gritzv1.Task{Id: 1, Workspace: "ws", Status: gritzv1.TaskStatus_PENDING},
			}, nil
		},
	}
	session := setupSession(t, client)

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "create_task",
		Arguments: map[string]any{
			"workspace":    "ws",
			"instruction":  "do it",
			"runner":       "r1",
			"auto_archive": "30m",
		},
	})
	assert.NilError(t, err)
	assert.Assert(t, !result.IsError, "unexpected error result: %v", result.Content)
	assert.Assert(t, got.AutoArchive != nil)
	assert.Equal(t, got.AutoArchive.AsDuration(), 30*time.Minute)

	got = nil
	result, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "create_task",
		Arguments: map[string]any{
			"workspace":   "ws",
			"instruction": "do it",
			"runner":      "r1",
		},
	})
	assert.NilError(t, err)
	assert.Assert(t, !result.IsError, "unexpected error result: %v", result.Content)
	assert.Assert(t, got.AutoArchive == nil, "auto_archive should be unset when neither param nor default is set")
}

func TestCreateTask_DefaultAutoArchive(t *testing.T) {
	// With a server default: it applies when the param is omitted, and the
	// per-call param overrides it.
	var got *gritzv1.CreateTaskRequest
	client := &gritzclient.ClientMock{
		CreateTaskFunc: func(ctx context.Context, req *gritzv1.CreateTaskRequest) (*gritzv1.CreateTaskResponse, error) {
			got = req
			return &gritzv1.CreateTaskResponse{
				Task: &gritzv1.Task{Id: 1, Workspace: "ws", Status: gritzv1.TaskStatus_PENDING},
			}, nil
		},
	}
	session := setupSession(t, client, WithDefaultAutoArchive(time.Hour))

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "create_task",
		Arguments: map[string]any{
			"workspace":   "ws",
			"instruction": "do it",
			"runner":      "r1",
		},
	})
	assert.NilError(t, err)
	assert.Assert(t, !result.IsError, "unexpected error result: %v", result.Content)
	assert.Assert(t, got.AutoArchive != nil)
	assert.Equal(t, got.AutoArchive.AsDuration(), time.Hour)

	got = nil
	result, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "create_task",
		Arguments: map[string]any{
			"workspace":    "ws",
			"instruction":  "do it",
			"runner":       "r1",
			"auto_archive": "-1s",
		},
	})
	assert.NilError(t, err)
	assert.Assert(t, !result.IsError, "unexpected error result: %v", result.Content)
	assert.Assert(t, got.AutoArchive != nil)
	assert.Equal(t, got.AutoArchive.AsDuration(), -time.Second)
}

func TestGetTask_EventNative(t *testing.T) {
	var eventFetches int
	client := &gritzclient.ClientMock{
		GetTaskFunc: func(ctx context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
			assert.Equal(t, req.Id, int64(7))
			return &gritzv1.GetTaskResponse{
				Task: &gritzv1.Task{
					Id:        7,
					Name:      "test",
					Workspace: "ws",
					Runner:    "r1",
					Status:    gritzv1.TaskStatus_RUNNING,
					Url:       "https://gritz.example.com/ui/tasks/7?org=7",
				},
			}, nil
		},
		ListEventsByTaskFunc: func(ctx context.Context, req *gritzv1.ListEventsByTaskRequest) (*gritzv1.ListEventsByTaskResponse, error) {
			eventFetches++
			assert.Equal(t, req.TaskId, int64(7))
			// The full stream, all arms, no type filter.
			assert.Equal(t, len(req.Types), 0)
			return &gritzv1.ListEventsByTaskResponse{
				Events: []*gritzv1.Event{
					{Id: 1, TaskId: 7, Payload: &gritzv1.Event_Instruction{
						Instruction: &gritzv1.InstructionPayload{Text: "do it"},
					}},
					{Id: 2, TaskId: 7, Payload: &gritzv1.Event_Command{
						Command: &gritzv1.CommandPayload{
							Command: gritzv1.TaskCommand_START,
							Version: 1,
							Runner:  "r1",
						},
					}},
					{Id: 3, TaskId: 7, Payload: &gritzv1.Event_Report{
						Report: &gritzv1.ReportPayload{Content: "working"},
					}},
				},
			}, nil
		},
		ListLinksFunc: func(ctx context.Context, req *gritzv1.ListLinksRequest) (*gritzv1.ListLinksResponse, error) {
			assert.Equal(t, req.TaskId, int64(7))
			return &gritzv1.ListLinksResponse{
				Links: []*gritzv1.TaskLink{
					{Id: 10, Relevance: "pr", Url: "https://example.com/pr/1", Subscribe: true},
				},
			}, nil
		},
	}
	session := setupSession(t, client)

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "get_task",
		Arguments: map[string]any{"id": 7},
	})
	assert.NilError(t, err)
	assert.Assert(t, !result.IsError, "unexpected error result: %v", result.Content)

	// The event stream is fetched exactly once — no separate brief fetch.
	assert.Equal(t, eventFetches, 1)

	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)

	// Header + links + raw event stream, no synthesized projections.
	_, hasInstructions := got["instructions"]
	assert.Assert(t, !hasInstructions, "instructions projection should be dropped")
	_, hasLogs := got["logs"]
	assert.Assert(t, !hasLogs, "logs projection should be dropped")

	assert.Equal(t, got["id"], float64(7))
	assert.Equal(t, got["name"], "test")
	assert.Equal(t, got["workspace"], "ws")
	assert.Equal(t, got["runner"], "r1")
	assert.Equal(t, got["status"], "RUNNING")

	links, ok := got["links"].([]any)
	assert.Assert(t, ok, "links should be a list")
	assert.Equal(t, len(links), 1)

	// The history stream, in stream order (instruction then report); the
	// command event is not history.
	events, ok := got["events"].([]any)
	assert.Assert(t, ok, "events should be a list")
	assert.Equal(t, len(events), 2)
	first := events[0].(map[string]any)
	_, hasInstructionArm := first["instruction"]
	assert.Assert(t, hasInstructionArm, "first event should carry the instruction arm")
	second := events[1].(map[string]any)
	_, hasReportArm := second["report"]
	assert.Assert(t, hasReportArm, "second event should carry the report arm")
}

func TestCreateTask_Draft(t *testing.T) {
	var got *gritzv1.CreateTaskRequest
	client := &gritzclient.ClientMock{
		CreateTaskFunc: func(ctx context.Context, req *gritzv1.CreateTaskRequest) (*gritzv1.CreateTaskResponse, error) {
			got = req
			return &gritzv1.CreateTaskResponse{
				Task: &gritzv1.Task{Id: 1, Workspace: "ws", Namespace: "ns", Status: gritzv1.TaskStatus_DRAFT},
			}, nil
		},
	}
	session := setupSession(t, client)

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "create_task",
		Arguments: map[string]any{
			"workspace": "ws",
			"runner":    "r1",
			"namespace": "ns",
			"draft":     true,
		},
	})

	assert.NilError(t, err)
	assert.Assert(t, !result.IsError, "unexpected error result: %v", result.Content)
	assert.Equal(t, got.Draft, true)
	assert.Equal(t, got.Namespace, "ns")
	assert.Equal(t, len(got.Instructions), 0)
	var summary map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &summary)
	assert.Equal(t, summary["status"], "DRAFT")
	assert.Equal(t, summary["namespace"], "ns")
}

func TestCreateTask_InstructionRequired(t *testing.T) {
	session := setupSession(t, &gritzclient.ClientMock{})

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "create_task",
		Arguments: map[string]any{
			"workspace": "ws",
			"runner":    "r1",
		},
	})

	assert.NilError(t, err)
	assert.Assert(t, result.IsError)
}

func TestGetTask(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := created.Add(time.Hour)
	client := &gritzclient.ClientMock{
		GetTaskFunc: func(ctx context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
			return &gritzv1.GetTaskResponse{
				Task: &gritzv1.Task{
					Id:          7,
					Name:        "test",
					Workspace:   "ws",
					Runner:      "r1",
					Namespace:   "ns",
					Status:      gritzv1.TaskStatus_COMPLETED,
					Archived:    true,
					AutoArchive: durationpb.New(30 * time.Minute),
					CreatedAt:   timestamppb.New(created),
					UpdatedAt:   timestamppb.New(updated),
					Actions:     &gritzv1.TaskActions{Restart: true, Unarchive: true},
				},
			}, nil
		},
		ListEventsByTaskFunc: func(ctx context.Context, req *gritzv1.ListEventsByTaskRequest) (*gritzv1.ListEventsByTaskResponse, error) {
			assert.DeepEqual(t, req.Types, []string{"instruction", "external"})
			return &gritzv1.ListEventsByTaskResponse{}, nil
		},
		ListLinksFunc: func(ctx context.Context, req *gritzv1.ListLinksRequest) (*gritzv1.ListLinksResponse, error) {
			return &gritzv1.ListLinksResponse{}, nil
		},
	}
	session := setupSession(t, client)

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "get_task",
		Arguments: map[string]any{
			"id":          7,
			"event_types": []string{"instruction", "external"},
		},
	})

	assert.NilError(t, err)
	assert.Assert(t, !result.IsError, "unexpected error result: %v", result.Content)
	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	assert.DeepEqual(t, got, map[string]any{
		"id":           float64(7),
		"name":         "test",
		"workspace":    "ws",
		"runner":       "r1",
		"namespace":    "ns",
		"status":       "COMPLETED",
		"archived":     true,
		"created_at":   "2026-01-02T03:04:05Z",
		"updated_at":   "2026-01-02T04:04:05Z",
		"auto_archive": "30m0s",
		"actions": map[string]any{
			"archive":   false,
			"cancel":    false,
			"restart":   true,
			"start":     false,
			"unarchive": true,
		},
		"links":  nil,
		"events": nil,
	})
}

func TestListTasks(t *testing.T) {
	client := &gritzclient.ClientMock{
		ListTasksFunc: func(ctx context.Context, req *gritzv1.ListTasksRequest) (*gritzv1.ListTasksResponse, error) {
			assert.Equal(t, req.PageSize, int32(10))
			assert.Equal(t, req.PageToken, "page-1")
			assert.Equal(t, req.Archived, true)
			return &gritzv1.ListTasksResponse{
				Tasks: []*gritzv1.Task{
					{
						Id:        1,
						Name:      "t1",
						Workspace: "ws",
						Runner:    "r1",
						Status:    gritzv1.TaskStatus_COMPLETED,
						Archived:  true,
						Url:       "https://gritz.example.com/ui/tasks/1?org=7",
					},
				},
				NextPageToken: "page-2",
			}, nil
		},
	}
	session := setupSession(t, client)

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "list_tasks",
		Arguments: map[string]any{
			"page_size":  10,
			"page_token": "page-1",
			"archived":   true,
		},
	})

	assert.NilError(t, err)
	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	assert.DeepEqual(t, got, map[string]any{
		"tasks": []any{
			map[string]any{
				"id":        float64(1),
				"name":      "t1",
				"workspace": "ws",
				"runner":    "r1",
				"status":    "COMPLETED",
				"archived":  true,
				"url":       "https://gritz.example.com/ui/tasks/1?org=7",
			},
		},
		"next_page_token": "page-2",
	})
}

func TestUpdateTask(t *testing.T) {
	var got *gritzv1.UpdateTaskRequest
	client := &gritzclient.ClientMock{
		UpdateTaskFunc: func(ctx context.Context, req *gritzv1.UpdateTaskRequest) (*gritzv1.UpdateTaskResponse, error) {
			got = req
			return &gritzv1.UpdateTaskResponse{}, nil
		},
		GetTaskFunc: func(ctx context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
			return &gritzv1.GetTaskResponse{
				Task: &gritzv1.Task{Id: 42, Name: "renamed", Workspace: "ws", Status: gritzv1.TaskStatus_COMPLETED},
			}, nil
		},
	}
	session := setupSession(t, client)

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "update_task",
		Arguments: map[string]any{
			"id":           42,
			"name":         "renamed",
			"auto_archive": "0",
		},
	})

	assert.NilError(t, err)
	assert.Assert(t, !result.IsError, "unexpected error result: %v", result.Content)
	assert.Equal(t, got.Id, int64(42))
	assert.Equal(t, got.Name, "renamed")
	assert.Equal(t, got.Start, false)
	assert.Equal(t, len(got.AddInstructions), 0)
	assert.Assert(t, got.AutoArchive != nil)
	assert.Equal(t, got.AutoArchive.AsDuration(), time.Duration(0))
	var summary map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &summary)
	assert.Equal(t, summary["name"], "renamed")
}

func TestUpdateTask_Instruction(t *testing.T) {
	var got *gritzv1.UpdateTaskRequest
	client := &gritzclient.ClientMock{
		UpdateTaskFunc: func(ctx context.Context, req *gritzv1.UpdateTaskRequest) (*gritzv1.UpdateTaskResponse, error) {
			got = req
			return &gritzv1.UpdateTaskResponse{}, nil
		},
		GetTaskFunc: func(ctx context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
			return &gritzv1.GetTaskResponse{Task: &gritzv1.Task{Id: 42}}, nil
		},
	}
	session := setupSession(t, client)

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "update_task",
		Arguments: map[string]any{
			"id":          42,
			"instruction": "do more",
			"url":         "https://example.com/issue/1",
			"start":       true,
		},
	})

	assert.NilError(t, err)
	assert.Assert(t, !result.IsError, "unexpected error result: %v", result.Content)
	assert.Equal(t, got.Start, true)
	assert.Equal(t, got.Name, "")
	assert.Assert(t, got.AutoArchive == nil)
	assert.Equal(t, len(got.AddInstructions), 1)
	assert.Equal(t, got.AddInstructions[0].Text, "do more")
	assert.Equal(t, got.AddInstructions[0].Url, "https://example.com/issue/1")
}

func TestUpdateTask_Invalid(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{"nothing to do", map[string]any{"id": 42}},
		{"url without instruction", map[string]any{"id": 42, "url": "https://example.com", "start": true}},
		{"bad auto_archive", map[string]any{"id": 42, "auto_archive": "soon"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := setupSession(t, &gritzclient.ClientMock{})

			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
				Name:      "update_task",
				Arguments: tt.args,
			})

			assert.NilError(t, err)
			assert.Assert(t, result.IsError)
		})
	}
}

func TestTaskActionTools(t *testing.T) {
	tests := []struct {
		tool string
		mock func(called *int64) *gritzclient.ClientMock
	}{
		{"cancel_task", func(called *int64) *gritzclient.ClientMock {
			return &gritzclient.ClientMock{
				CancelTaskFunc: func(ctx context.Context, req *gritzv1.CancelTaskRequest) (*gritzv1.CancelTaskResponse, error) {
					*called = req.Id
					return &gritzv1.CancelTaskResponse{}, nil
				},
			}
		}},
		{"restart_task", func(called *int64) *gritzclient.ClientMock {
			return &gritzclient.ClientMock{
				RestartTaskFunc: func(ctx context.Context, req *gritzv1.RestartTaskRequest) (*gritzv1.RestartTaskResponse, error) {
					*called = req.Id
					return &gritzv1.RestartTaskResponse{}, nil
				},
			}
		}},
		{"unarchive_task", func(called *int64) *gritzclient.ClientMock {
			return &gritzclient.ClientMock{
				UnarchiveTaskFunc: func(ctx context.Context, req *gritzv1.UnarchiveTaskRequest) (*gritzv1.UnarchiveTaskResponse, error) {
					*called = req.Id
					return &gritzv1.UnarchiveTaskResponse{}, nil
				},
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			var called int64
			client := tt.mock(&called)
			client.GetTaskFunc = func(ctx context.Context, req *gritzv1.GetTaskRequest) (*gritzv1.GetTaskResponse, error) {
				return &gritzv1.GetTaskResponse{
					Task: &gritzv1.Task{Id: req.Id, Name: "test", Workspace: "ws", Status: gritzv1.TaskStatus_RESTARTING},
				}, nil
			}
			session := setupSession(t, client)

			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
				Name:      tt.tool,
				Arguments: map[string]any{"id": 42},
			})

			assert.NilError(t, err)
			assert.Assert(t, !result.IsError, "unexpected error result: %v", result.Content)
			assert.Equal(t, called, int64(42))
			var got map[string]any
			mcptest.UnmarshalCallToolResult(t, result, &got)
			assert.DeepEqual(t, got, map[string]any{
				"id":        float64(42),
				"name":      "test",
				"workspace": "ws",
				"status":    "RESTARTING",
				"archived":  false,
			})
		})
	}
}

// fakeLog serves a task's log through ListLogChunksByTask, paging backwards
// like the server: a page token is the index of the chunk the page ends
// before, and an empty token is the end of the log.
type fakeLog struct {
	chunks   []string
	requests []*gritzv1.ListLogChunksByTaskRequest
}

func (f *fakeLog) list(ctx context.Context, req *gritzv1.ListLogChunksByTaskRequest) (*gritzv1.ListLogChunksByTaskResponse, error) {
	f.requests = append(f.requests, proto.CloneOf(req))
	end := len(f.chunks)
	if req.PageToken != "" {
		end, _ = strconv.Atoi(req.PageToken)
	}
	start := max(end-int(cmp.Or(req.PageSize, 50)), 0)
	resp := &gritzv1.ListLogChunksByTaskResponse{More: start > 0}
	for i := start; i < end; i++ {
		resp.Chunks = append(resp.Chunks, &gritzv1.LogChunk{Id: int64(i), Data: []byte(f.chunks[i])})
	}
	if start > 0 {
		resp.PrevPageToken = strconv.Itoa(start)
	}
	return resp, nil
}

func callGetTaskLog(t *testing.T, session *mcp.ClientSession, args map[string]any) (log, before string) {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "get_task_log",
		Arguments: args,
	})
	assert.NilError(t, err)
	assert.Assert(t, !result.IsError, "unexpected error result: %v", result.Content)
	var got struct {
		Log    string `json:"log"`
		Before string `json:"before"`
	}
	mcptest.UnmarshalCallToolResult(t, result, &got)
	return got.Log, got.Before
}

func TestGetTaskLog(t *testing.T) {
	fake := &fakeLog{chunks: []string{"aaaa", "bbbb", "cccc"}}
	session := setupSession(t, &gritzclient.ClientMock{ListLogChunksByTaskFunc: fake.list})

	log, before := callGetTaskLog(t, session, map[string]any{"id": 7})

	assert.Equal(t, log, "aaaabbbbcccc")
	assert.Equal(t, before, "")
	assert.Equal(t, len(fake.requests), 1)
	assert.Equal(t, fake.requests[0].TaskId, int64(7))
}

func TestGetTaskLog_MaxBytes(t *testing.T) {
	// 120 chunks of 10 bytes: more than two default pages.
	fake := &fakeLog{}
	for i := range 120 {
		fake.chunks = append(fake.chunks, fmt.Sprintf("chunk %03d\n", i))
	}
	session := setupSession(t, &gritzclient.ClientMock{ListLogChunksByTaskFunc: fake.list})

	// Two whole pages fit; the third is read again chunk by chunk until the
	// budget is spent.
	log, before := callGetTaskLog(t, session, map[string]any{"id": 7, "max_bytes": 1025})

	assert.Equal(t, log, strings.Join(fake.chunks[18:], ""))
	assert.Equal(t, before, "18")
	var tokens []string
	for _, req := range fake.requests {
		tokens = append(tokens, fmt.Sprintf("%s/%d", req.PageToken, req.PageSize))
	}
	assert.DeepEqual(t, tokens, []string{"/0", "70/0", "20/0", "20/1", "19/1", "18/1"})

	// Passing before back reads the bytes preceding it, to the start of the log.
	log, before = callGetTaskLog(t, session, map[string]any{"id": 7, "before": "18"})

	assert.Equal(t, log, strings.Join(fake.chunks[:18], ""))
	assert.Equal(t, before, "")
}

func TestGetTaskLog_OversizedChunk(t *testing.T) {
	// A chunk bigger than max_bytes is still returned, so paging makes progress.
	fake := &fakeLog{chunks: []string{"aaaa", "bbbbbbbb", "cc"}}
	session := setupSession(t, &gritzclient.ClientMock{ListLogChunksByTaskFunc: fake.list})

	log, before := callGetTaskLog(t, session, map[string]any{"id": 7, "max_bytes": 4})

	assert.Equal(t, log, "cc")
	assert.Equal(t, before, "2")

	log, before = callGetTaskLog(t, session, map[string]any{"id": 7, "max_bytes": 4, "before": before})

	assert.Equal(t, log, "bbbbbbbb")
	assert.Equal(t, before, "1")
}

func TestCreateLink(t *testing.T) {
	client := &gritzclient.ClientMock{
		CreateLinkFunc: func(ctx context.Context, req *gritzv1.CreateLinkRequest) (*gritzv1.CreateLinkResponse, error) {
			assert.Equal(t, req.TaskId, int64(7))
			assert.Equal(t, req.Url, "https://github.com/o/r/pull/1")
			assert.Equal(t, req.Title, "PR 1")
			assert.Equal(t, req.Relevance, "the fix")
			assert.Equal(t, req.Subscribe, true)
			return &gritzv1.CreateLinkResponse{
				Link: &gritzv1.TaskLink{
					Id:        3,
					TaskId:    7,
					Url:       req.Url,
					Title:     req.Title,
					Relevance: req.Relevance,
					Subscribe: req.Subscribe,
				},
			}, nil
		},
	}
	session := setupSession(t, client)

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "create_link",
		Arguments: map[string]any{
			"task_id":   7,
			"url":       "https://github.com/o/r/pull/1",
			"title":     "PR 1",
			"relevance": "the fix",
			"subscribe": true,
		},
	})

	assert.NilError(t, err)
	var got map[string]any
	mcptest.UnmarshalCallToolResult(t, result, &got)
	assert.DeepEqual(t, got, map[string]any{
		"id":        float64(3),
		"url":       "https://github.com/o/r/pull/1",
		"title":     "PR 1",
		"relevance": "the fix",
		"subscribe": true,
	})
}
