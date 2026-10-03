package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/icholy/gritz/internal/x/common"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DummyAgent is a no-op agent implementation for testing.
type DummyAgent struct {
	log        *DriverLog
	cwd        string
	mcpServers map[string]McpServer
	options    *DummyOptions
}

// Prompt handles the prompt based on the configured options.
// If Sleep is set to -1, it sleeps forever (until context cancellation).
// If Sleep is set to a positive value, it sleeps for that many seconds.
// Otherwise, it does nothing and returns nil.
func (a *DummyAgent) Prompt(ctx context.Context, prompt string, resume bool) error {
	a.log.Info("dummy agent received prompt", "text", prompt, "resume", resume)
	if err := a.doToolCalls(ctx); err != nil {
		return err
	}
	if err := a.doCommands(ctx); err != nil {
		return err
	}
	if err := a.doSleep(ctx); err != nil {
		return err
	}
	if err := a.doError(); err != nil {
		return err
	}
	return nil
}

// doError returns the configured error, if any.
func (a *DummyAgent) doError() error {
	if a.options == nil || a.options.Error == "" {
		return nil
	}
	a.log.Info("dummy agent returning error", "err", a.options.Error)
	return errors.New(a.options.Error)
}

func (a *DummyAgent) doCommands(ctx context.Context) error {
	if a.options == nil {
		return nil
	}
	for _, command := range a.options.Commands {
		a.log.Info("Running dummy command", "command", command)
		c := exec.CommandContext(ctx, "sh", "-c", command)
		c.Dir = a.cwd
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("dummy command failed: %w", err)
		}
	}
	return nil
}

func (a *DummyAgent) doSleep(ctx context.Context) error {
	if a.options == nil || a.options.Sleep == 0 {
		return nil
	}
	if a.options.Sleep < 0 {
		a.log.Info("dummy agent sleeping forever")
		<-ctx.Done()
		return ctx.Err()
	}
	duration := time.Duration(a.options.Sleep) * time.Second
	a.log.Info("dummy agent sleeping", "duration", duration)
	if !common.SleepContext(ctx, duration) {
		return ctx.Err()
	}
	return nil
}

// doToolCalls executes the configured MCP tool calls.
func (a *DummyAgent) doToolCalls(ctx context.Context) error {
	if a.options == nil {
		return nil
	}
	for _, tc := range a.options.ToolCalls {
		session, err := a.connectMCP(ctx, tc.Server)
		if err != nil {
			return err
		}
		a.log.Info("calling tool", "server", tc.Server, "name", tc.Name)
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      tc.Name,
			Arguments: tc.Arguments,
		})
		session.Close()
		if err != nil {
			return err
		}
		a.log.Info("tool result", "name", tc.Name, "result", result)
	}
	return nil
}

// connectMCP connects to a stdio MCP server and returns the session.
func (a *DummyAgent) connectMCP(ctx context.Context, name string) (*mcp.ClientSession, error) {
	mcpConfig, ok := a.mcpServers[name]
	if !ok {
		return nil, fmt.Errorf("MCP server not found: %s", name)
	}

	if mcpConfig.Type != "stdio" {
		return nil, fmt.Errorf("MCP server %s is not stdio type: %s", name, mcpConfig.Type)
	}

	a.log.Info("connecting to MCP server", "name", name, "command", mcpConfig.Command, "args", mcpConfig.Args)

	cmd := exec.CommandContext(ctx, mcpConfig.Command, mcpConfig.Args...)
	cmd.Dir = a.cwd
	for k, v := range mcpConfig.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "dummy-agent",
		Version: "1.0.0",
	}, nil)

	transport := &mcp.CommandTransport{Command: cmd}
	return client.Connect(ctx, transport, nil)
}

// Close does nothing and returns nil.
func (a *DummyAgent) Close() error {
	return nil
}
