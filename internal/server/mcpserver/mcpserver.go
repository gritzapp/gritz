package mcpserver

import (
	"net/http"
	"time"

	"github.com/gritzapp/gritz/internal/auth/apiauth"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/proto/gritz/v1/gritzv1connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Instructions is the prompt shown to MCP clients describing what the
// gritz server offers.
const Instructions = "gritz is an async agent orchestrator that runs AI coding agents in parallel inside Docker containers.\n" +
	"Use it to create and manage tasks that execute in isolated workspaces.\n" +
	"Workspaces define the container image, volumes, environment variables, and MCP servers available to agents.\n" +
	"Each task runs an AI coding agent with access to the codebase and configured tools.\n" +
	"Agents attach links to their tasks for external resources they create, such as GitHub PRs or Jira issues."

// Option configures the behaviour of the registered tools.
type Option func(*handlers)

// WithDefaultAutoArchive sets the default auto_archive applied to tasks
// created via create_task when the call omits the auto_archive param. See
// Task.auto_archive for the value semantics (zero/unset = never, negative
// = archive immediately on terminal status, positive = delay).
func WithDefaultAutoArchive(d time.Duration) Option {
	return func(h *handlers) {
		h.defaultAutoArchive = durationpb.New(d)
	}
}

// NewServer builds an MCP server with the user-facing gritz tools
// registered. The same setup is used by the HTTP handler and by the
// local stdio command.
func NewServer(service gritzv1connect.GritzServiceHandler, opts ...Option) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "gritz",
		Version: "1.0.0",
	}, &mcp.ServerOptions{
		Instructions: Instructions,
	})
	AddTools(server, service, opts...)
	return server
}

// AddTools registers the user-facing gritz tools on the given MCP server.
// Tool handlers proxy to the supplied service, which can be either an
// in-process GritzServiceHandler (server-side) or the generated Connect
// client (remote) since both interfaces share the same method signatures.
func AddTools(server *mcp.Server, service gritzv1connect.GritzServiceHandler, opts ...Option) {
	h := &handlers{service: service}
	for _, opt := range opts {
		opt(h)
	}

	h.addTaskTools(server)
}

// Handler returns an http.Handler that serves the MCP Streamable HTTP
// protocol. The handler expects auth middleware to have set UserInfo in
// the request context.
func Handler(service gritzv1connect.GritzServiceHandler) http.Handler {
	server := NewServer(service)
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		if apiauth.Caller(r.Context()) == nil {
			return nil
		}
		return server
	}, &mcp.StreamableHTTPOptions{
		Stateless: true,
	})
}

type handlers struct {
	service gritzv1connect.GritzServiceHandler
	// defaultAutoArchive is applied to create_task when the call omits the
	// auto_archive param. nil means no default is sent (server behavior
	// unchanged).
	defaultAutoArchive *durationpb.Duration
}

type taskSummary struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Workspace string `json:"workspace"`
	Status    string `json:"status"`
	URL       string `json:"url,omitempty"`
}

func taskSummaryOf(t *gritzv1.Task) taskSummary {
	return taskSummary{
		ID:        t.Id,
		Name:      t.Name,
		Workspace: t.Workspace,
		Status:    t.Status.String(),
		URL:       t.Url,
	}
}
