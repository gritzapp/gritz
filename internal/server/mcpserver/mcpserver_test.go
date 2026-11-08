package mcpserver

import (
	"testing"

	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gotest.tools/v3/assert"
)

func setupSession(t *testing.T, client *gritzclient.ClientMock, opts ...Option) *mcp.ClientSession {
	t.Helper()
	srv := NewServer(client, opts...)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	_, err := srv.Connect(t.Context(), serverTransport, nil)
	assert.NilError(t, err)

	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v1.0.0"}, nil)
	session, err := c.Connect(t.Context(), clientTransport, nil)
	assert.NilError(t, err)
	t.Cleanup(func() { session.Close() })
	return session
}

func TestListTools(t *testing.T) {
	session := setupSession(t, &gritzclient.ClientMock{})

	resp, err := session.ListTools(t.Context(), &mcp.ListToolsParams{})
	assert.NilError(t, err)

	got := make([]string, len(resp.Tools))
	for i, tool := range resp.Tools {
		got[i] = tool.Name
	}
	assert.DeepEqual(t, got, []string{
		"archive_task",
		"cancel_task",
		"create_link",
		"create_task",
		"get_task",
		"get_task_log",
		"list_tasks",
		"list_workspaces",
		"restart_task",
		"unarchive_task",
		"update_task",
	})
}
