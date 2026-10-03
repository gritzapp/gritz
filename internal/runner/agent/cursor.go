package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/icholy/gritz/internal/runner/agent/toollog"
)

// CursorAgent implements Agent using the Cursor Agent CLI.
type CursorAgent struct {
	log        *DriverLog
	cwd        string
	verbose    bool
	mcpServers map[string]McpServer
	options    *CursorOptions
}

// Prompt sends a prompt to Cursor and waits for completion.
func (a *CursorAgent) Prompt(ctx context.Context, prompt string, resume bool) error {
	a.log.Info("sending prompt", "text", prompt)

	// Write MCP config file if we have MCP servers
	if len(a.mcpServers) > 0 {
		if err := a.writeMcpConfig(); err != nil {
			return fmt.Errorf("failed to write MCP config: %w", err)
		}
	}

	args := []string{
		"--print",
		"--approve-mcps",
		"--force",
		"--output-format", "stream-json",
	}

	// Add model if specified in options
	if a.options != nil && a.options.Model != "" {
		args = append(args, "--model", a.options.Model)
	}

	// Resume the last chat session
	if resume {
		args = append(args, "--continue")
	}

	args = append(args, prompt)

	bin, err := a.findBin()
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = a.cwd
	cmd.Env = os.Environ()
	cmd.Stderr = os.Stderr

	// Create a new process group so we can kill all child processes
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Send SIGTERM to the entire process group (negative PID)
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Bytes()
		if a.verbose {
			a.log.Info("output", "line", string(line))
			continue
		}
		if !a.handleStreamEvent(line) {
			a.log.Info("output", "line", string(line))
		}
	}

	return cmd.Wait()
}

// Close releases any resources held by the agent.
func (a *CursorAgent) Close() error {
	return nil
}

// writeMcpConfig writes the MCP servers configuration to ~/.cursor/mcp.json.
func (a *CursorAgent) writeMcpConfig() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}
	cursorDir := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(cursorDir, 0755); err != nil {
		return err
	}

	// Convert our McpServer format to Cursor's expected format
	mcpServers := make(map[string]any)
	for name, srv := range a.mcpServers {
		server := make(map[string]any)
		switch srv.Type {
		case "stdio":
			server["command"] = srv.Command
			if len(srv.Args) > 0 {
				server["args"] = srv.Args
			}
			if len(srv.Env) > 0 {
				server["env"] = srv.Env
			}
		case "http", "sse":
			server["transport"] = srv.Type
			server["url"] = srv.URL
			if len(srv.Headers) > 0 {
				server["headers"] = srv.Headers
			}
		}
		mcpServers[name] = server
	}

	config := map[string]any{
		"mcpServers": mcpServers,
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	mcpConfigPath := filepath.Join(cursorDir, "mcp.json")
	return os.WriteFile(mcpConfigPath, data, 0644)
}

// findBin returns the path to the Cursor Agent CLI binary.
func (a *CursorAgent) findBin() (string, error) {
	if a.options != nil && a.options.Bin != "" {
		return a.options.Bin, nil
	}
	// Check common names on PATH
	for _, name := range []string{"cursor-agent", "agent"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	// Check default install location
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".local", "bin", "agent")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("cursor agent CLI not found (install: curl -fsSL https://cursor.com/install | bash)")
}

func (a *CursorAgent) handleStreamEvent(data []byte) bool {
	var event struct {
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		SessionID string `json:"session_id"`
		// assistant message
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
		// tool_call fields
		CallID   string `json:"call_id"`
		ToolCall struct {
			ReadToolCall  *json.RawMessage `json:"readToolCall"`
			WriteToolCall *json.RawMessage `json:"writeToolCall"`
			EditToolCall  *json.RawMessage `json:"editToolCall"`
			BashToolCall  *json.RawMessage `json:"bashToolCall"`
		} `json:"tool_call"`
		// result fields
		IsError bool `json:"is_error"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return false
	}

	switch event.Type {
	case "system":
		a.log.Info("system", "subtype", event.Subtype, "session_id", event.SessionID)
	case "assistant":
		for _, block := range event.Message.Content {
			if block.Type == "text" && block.Text != "" {
				a.log.Info("text", "content", block.Text)
			}
		}
	case "tool_call":
		// Determine tool name from whichever field is set
		toolName := "unknown"
		var raw *json.RawMessage
		switch {
		case event.ToolCall.ReadToolCall != nil:
			toolName, raw = "read", event.ToolCall.ReadToolCall
		case event.ToolCall.WriteToolCall != nil:
			toolName, raw = "write", event.ToolCall.WriteToolCall
		case event.ToolCall.EditToolCall != nil:
			toolName, raw = "edit", event.ToolCall.EditToolCall
		case event.ToolCall.BashToolCall != nil:
			toolName, raw = "bash", event.ToolCall.BashToolCall
		}
		// Cursor nests the actual arguments under an "args" object inside the
		// set sub-object; pull it out, then redact bulky content before summarizing.
		var sub struct {
			Args map[string]any `json:"args"`
		}
		if raw != nil {
			_ = json.Unmarshal(*raw, &sub)
		}
		args := toollog.Redact(sub.Args, "old_string", "new_string", "content", "fileText", "contents")
		a.log.Info("tool", "name", toolName, "subtype", event.Subtype, "call_id", event.CallID, "summary", toollog.Summarize(args))
	case "result":
		if event.IsError {
			a.log.Error("result", "is_error", true)
		}
	}
	return true
}
