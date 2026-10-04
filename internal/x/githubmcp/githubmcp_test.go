package githubmcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/mcptest"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// startAuthedUpstream stands up an MCP server behind an Authorization
// check. It records every Bearer token seen and rejects requests whose
// token isn't one of the accepted ones.
func startAuthedUpstream(t *testing.T, accepted map[string]bool, seen *atomic.Pointer[string]) string {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake-github-mcp", Version: "0"}, nil)
	srv.AddTool(&mcp.Tool{
		Name:        "ping",
		Description: "returns pong",
		InputSchema: map[string]any{"type": "object"},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil
	})
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		seen.Store(&token)
		if !accepted[token] {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

func TestServer_SwapInjectsBearerToken(t *testing.T) {
	var seen atomic.Pointer[string]
	url := startAuthedUpstream(t, map[string]bool{"ghs_test_token": true}, &seen)

	client := &gritzclient.ClientMock{
		CreateGitHubTokenFunc: func(_ context.Context, _ *gritzv1.CreateGitHubTokenRequest) (*gritzv1.CreateGitHubTokenResponse, error) {
			return &gritzv1.CreateGitHubTokenResponse{
				Token:     "ghs_test_token",
				ExpiresAt: timestamppb.New(time.Now().Add(time.Hour)),
			}, nil
		},
	}

	s := New(Config{Client: client, URL: url})
	t.Cleanup(s.upstream.Close)

	expiresAt, err := s.swap(t.Context())
	assert.NilError(t, err)
	assert.Assert(t, !expiresAt.IsZero(), "expiresAt should be set from the token response")
	assert.Assert(t, cmp.Len(client.CreateGitHubTokenCalls(), 1))

	sess, err := s.upstream.Session()
	assert.NilError(t, err)
	res, err := sess.CallTool(t.Context(), &mcp.CallToolParams{Name: "ping"})
	assert.NilError(t, err)
	assert.Equal(t, mcptest.CallToolResultText(t, res), "pong")
	assert.Equal(t, *seen.Load(), "ghs_test_token")
}

func TestServer_SwapPropagatesTokenError(t *testing.T) {
	client := &gritzclient.ClientMock{
		CreateGitHubTokenFunc: func(_ context.Context, _ *gritzv1.CreateGitHubTokenRequest) (*gritzv1.CreateGitHubTokenResponse, error) {
			return nil, errors.New("boom")
		},
	}
	s := New(Config{Client: client, URL: "http://invalid.invalid"})
	t.Cleanup(s.upstream.Close)

	_, err := s.swap(t.Context())
	assert.ErrorContains(t, err, "create github token")
}
