package gritzrpc

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"testing"
	"time"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gotest.tools/v3/assert"
)

func TestStartParamsRoundTrip(t *testing.T) {
	t.Parallel()
	// Arrange
	createdAt := timestamppb.New(time.Date(2026, 11, 2, 20, 31, 0, 0, time.UTC))
	params := StartParams{
		ProtocolVersion: ProtocolVersion,
		Cwd:             "/workspace",
		Task:            &gritzv1.Task{Id: 2283, Name: "fix the build\nand ship it", Version: 7},
		Events: []*gritzv1.Event{{
			Id: 9120, TaskId: 2283, Wake: true, CreatedAt: createdAt,
			Payload: &gritzv1.Event_External{External: &gritzv1.ExternalPayload{
				Description: "review comment",
				Url:         "https://github.com/gritzapp/gritz/pull/1950#discussion_r1",
				Source:      "github",
				Type:        "pull_request_review_comment",
				Details:     map[string]string{"path": "main.go", "line": "12"},
			}},
		}},
		Links: []*gritzv1.TaskLink{{
			Id: 881, TaskId: 2283, Relevance: "PR", Kind: "github.pr", CreatedAt: createdAt,
			Url: "https://github.com/gritzapp/gritz/pull/1950",
		}},
		McpServers: map[string]McpServer{
			"gritz": {Type: "stdio", Command: "/gritz/gritz", Args: []string{"tool", "agent-mcp"}},
		},
	}

	// Act
	data, err := json.Marshal(params)
	assert.NilError(t, err)
	var got StartParams
	assert.NilError(t, json.Unmarshal(data, &got))

	// Assert
	assert.Assert(t, !bytes.Contains(data, []byte("\n")), "encoding spans lines: %s", data)
	var wire struct {
		Task   map[string]any   `json:"task"`
		Events []map[string]any `json:"events"`
		Links  []map[string]any `json:"links"`
	}
	assert.NilError(t, json.Unmarshal(data, &wire))
	assert.Equal(t, wire.Task["id"], "2283")
	assert.Equal(t, wire.Task["version"], "7")
	assert.Equal(t, wire.Events[0]["id"], "9120")
	assert.Equal(t, wire.Events[0]["taskId"], "2283")
	assert.Equal(t, wire.Links[0]["id"], "881")
	assert.DeepEqual(t, slices.Sorted(maps.Keys(wire.Events[0])), []string{"createdAt", "external", "id", "taskId", "wake"})
	assert.DeepEqual(t, got, params, protocmp.Transform())
}
