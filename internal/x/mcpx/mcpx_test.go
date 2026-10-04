package mcpx_test

import (
	"testing"

	"google.golang.org/protobuf/types/known/durationpb"
	"gotest.tools/v3/assert"

	"github.com/gritzapp/gritz/internal/x/mcptest"
	"github.com/gritzapp/gritz/internal/x/mcpx"
)

func TestErrorResult(t *testing.T) {
	t.Parallel()
	res := mcpx.ErrorResult("boom: %d", 42)
	assert.Assert(t, res.IsError)
	assert.Equal(t, mcptest.CallToolResultText(t, res), "boom: 42")
}

func TestJSONResult(t *testing.T) {
	t.Parallel()
	res := mcpx.JSONResult(map[string]int{"count": 3})
	assert.Assert(t, !res.IsError)
	assert.Equal(t, mcptest.CallToolResultText(t, res), "{\n  \"count\": 3\n}")
}

func TestJSONResultMarshalError(t *testing.T) {
	t.Parallel()
	// channels cannot be marshalled to JSON, so this exercises the fallback.
	res := mcpx.JSONResult(make(chan int))
	assert.Assert(t, res.IsError)
	assert.Assert(t, len(mcptest.CallToolResultText(t, res)) > 0)
}

func TestProtoJSONResult(t *testing.T) {
	t.Parallel()
	res := mcpx.ProtoJSONResult(durationpb.New(0))
	assert.Assert(t, !res.IsError)
	assert.Equal(t, mcptest.CallToolResultText(t, res), "\"0s\"")
}
