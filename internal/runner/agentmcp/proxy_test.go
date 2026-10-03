package agentmcp

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gotest.tools/v3/assert"
)

func TestProxy(t *testing.T) {
	t1, t2 := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err := Proxy(ctx, t1, t2)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}
