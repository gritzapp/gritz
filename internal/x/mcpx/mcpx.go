// Package mcpx provides small, shared helpers for building MCP tool results.
// These wrap a return value into an *mcp.CallToolResult and were previously
// copy-pasted across every MCP implementation.
package mcpx

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// ErrorResult builds an error *mcp.CallToolResult whose single text content is
// fmt.Sprintf(format, args...).
func ErrorResult(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf(format, args...)},
		},
		IsError: true,
	}
}

// JSONResult marshals v as indented JSON and wraps it in an *mcp.CallToolResult
// with a single text content. On marshal error it returns an ErrorResult.
func JSONResult(v any) *mcp.CallToolResult {
	data, err := json.Marshal(v, jsontext.WithIndent("  "), json.Deterministic(true))
	if err != nil {
		return ErrorResult("failed to format response: %v", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(data)},
		},
	}
}

// ProtoJSONResult marshals m as indented protobuf JSON (so fields serialize
// with their proper JSON names) and wraps it in an *mcp.CallToolResult with a
// single text content. On marshal error it returns an ErrorResult.
func ProtoJSONResult(m proto.Message) *mcp.CallToolResult {
	data, err := protojson.MarshalOptions{Indent: "  "}.Marshal(m)
	if err != nil {
		return ErrorResult("failed to format response: %v", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(data)},
		},
	}
}
