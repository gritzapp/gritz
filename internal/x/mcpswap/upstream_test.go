package mcpswap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gotest.tools/v3/assert"
)

// startUpstream returns an httptest server hosting an mcp.Server with a
// single "ping" tool that echoes "pong".
func startUpstream(t *testing.T) string {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake-upstream", Version: "0"}, nil)
	srv.AddTool(&mcp.Tool{
		Name:        "ping",
		Description: "returns pong",
		InputSchema: map[string]any{"type": "object"},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil
	})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(ts.Close)
	return ts.URL
}

// startProxy boots a Proxy pointing at upstreamURL and serves it via an
// httptest server, returning the proxy's URL.
func startProxy(t *testing.T, upstreamURL string) string {
	t.Helper()
	var up Upstream
	t.Cleanup(up.Close)
	err := up.Swap(t.Context(), &mcp.StreamableClientTransport{Endpoint: upstreamURL})
	assert.NilError(t, err)
	srv := mcp.NewServer(&mcp.Implementation{Name: "mcproxy", Version: "0"}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{
			Logging:   &mcp.LoggingCapabilities{},
			Tools:     &mcp.ToolCapabilities{ListChanged: true},
			Prompts:   &mcp.PromptCapabilities{ListChanged: true},
			Resources: &mcp.ResourceCapabilities{ListChanged: true},
		},
	})
	srv.AddReceivingMiddleware(up.Dispatch)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL
}

func TestUpstream_Dispatch(t *testing.T) {
	proxyURL := startProxy(t, startUpstream(t))

	c := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "0"}, nil)
	sess, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: proxyURL}, nil)
	assert.NilError(t, err)
	defer sess.Close()

	// Capabilities mirror the upstream: it has a tool but no prompts or
	// resources, so the proxy must advertise tools only.
	caps := sess.InitializeResult().Capabilities
	assert.Assert(t, caps.Tools != nil, "proxy did not advertise tools capability")
	assert.Assert(t, !caps.Tools.ListChanged, "proxy advertised tools.listChanged it does not forward")
	assert.Assert(t, caps.Prompts == nil, "proxy advertised prompts capability the upstream lacks")
	assert.Assert(t, caps.Resources == nil, "proxy advertised resources capability the upstream lacks")

	// Tool names pass through unprefixed.
	tools, err := sess.ListTools(t.Context(), &mcp.ListToolsParams{})
	assert.NilError(t, err)
	assert.Equal(t, len(tools.Tools), 1)
	assert.Equal(t, tools.Tools[0].Name, "ping")

	// Calls forward and return the upstream result.
	res, err := sess.CallTool(t.Context(), &mcp.CallToolParams{Name: "ping"})
	assert.NilError(t, err)
	text, ok := res.Content[0].(*mcp.TextContent)
	assert.Assert(t, ok, "content[0] = %T, want *mcp.TextContent", res.Content[0])
	assert.Equal(t, text.Text, "pong")
}

func TestUpstream_SessionWhenUnconnected(t *testing.T) {
	var up Upstream
	_, err := up.Session()
	assert.Assert(t, err != nil, "expected error from Session before Swap")
}
