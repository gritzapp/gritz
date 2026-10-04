package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gritzapp/gritz/internal/runner/agent/toollog"
)

// Codegritz implements Agent using OpenAI Codex CLI.
type Codegritz struct {
	log        *DriverLog
	cwd        string
	verbose    bool
	mcpServers map[string]McpServer
	options    *CodexOptions
}

// Prompt sends a prompt to Codex and waits for completion.
func (a *Codegritz) Prompt(ctx context.Context, prompt string, resume bool) error {
	a.log.Info("sending prompt", "text", prompt)

	// Write MCP config if we have MCP servers
	if len(a.mcpServers) > 0 {
		if err := a.writeConfig(); err != nil {
			return fmt.Errorf("failed to write codex config: %w", err)
		}
	}

	bin := "codex"
	if a.options != nil && a.options.Bin != "" {
		bin = a.options.Bin
	}

	if resume {
		// Resume the last exec session with a follow-up prompt
		args := []string{
			"exec",
			"resume",
			"--dangerously-bypass-approvals-and-sandbox",
			"--json",
			"--last",
			prompt,
		}

		return a.run(ctx, bin, args)
	}

	args := []string{
		"exec",
		"--dangerously-bypass-approvals-and-sandbox",
		"--json",
	}

	// Add model if specified in options
	if a.options != nil && a.options.Model != "" {
		args = append(args, "--model", a.options.Model)
	}

	args = append(args, prompt)

	return a.run(ctx, bin, args)
}

// Close releases any resources held by the agent.
func (a *Codegritz) Close() error {
	return nil
}

func (a *Codegritz) run(ctx context.Context, bin string, args []string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = a.cwd
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

// writeConfig writes the Codex config.toml with MCP server configuration.
func (a *Codegritz) writeConfig() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0755); err != nil {
		return err
	}

	// Generate config.toml content manually to avoid adding a TOML library dependency.
	// Format:
	//   [mcp_servers.<name>]
	//   command = "..."
	//   args = ["..."]
	var b strings.Builder
	for name, srv := range a.mcpServers {
		fmt.Fprintf(&b, "[mcp_servers.%s]\n", name)
		switch srv.Type {
		case "stdio":
			fmt.Fprintf(&b, "command = %q\n", srv.Command)
			if len(srv.Args) > 0 {
				fmt.Fprintf(&b, "args = [")
				for i, arg := range srv.Args {
					if i > 0 {
						fmt.Fprintf(&b, ", ")
					}
					fmt.Fprintf(&b, "%q", arg)
				}
				fmt.Fprintf(&b, "]\n")
			}
			if len(srv.Env) > 0 {
				fmt.Fprintf(&b, "\n[mcp_servers.%s.env]\n", name)
				for k, v := range srv.Env {
					fmt.Fprintf(&b, "%s = %q\n", k, v)
				}
			}
		case "http", "sse":
			fmt.Fprintf(&b, "url = %q\n", srv.URL)
			if len(srv.Headers) > 0 {
				fmt.Fprintf(&b, "\n[mcp_servers.%s.http_headers]\n", name)
				for k, v := range srv.Headers {
					fmt.Fprintf(&b, "%s = %q\n", k, v)
				}
			}
		}
		fmt.Fprintf(&b, "\n")
	}

	configPath := filepath.Join(codexDir, "config.toml")
	return os.WriteFile(configPath, []byte(b.String()), 0644)
}

func (a *Codegritz) handleStreamEvent(data []byte) bool {
	var event struct {
		Type string `json:"type"`
		// item events
		Item struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			// For tool calls
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Status    string `json:"status"`
			Output    string `json:"output"`
		} `json:"item"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return false
	}
	switch event.Type {
	case "item.created", "item.completed":
		switch event.Item.Type {
		case "message":
			for _, block := range event.Item.Content {
				if block.Type == "text" && block.Text != "" {
					a.log.Info("text", "content", block.Text)
				}
			}
		case "function_call":
			if event.Item.Name != "" {
				// Arguments is a JSON string; decode it into a map before summarizing.
				var input map[string]any
				if event.Item.Arguments != "" {
					_ = json.Unmarshal([]byte(event.Item.Arguments), &input)
				}
				a.log.Info("tool", "name", event.Item.Name, "summary", toollog.Summarize(input))
			}
		case "function_call_output":
			a.log.Debug("tool_result", "status", event.Item.Status)
		}
	case "error":
		a.log.Error("codex error", "data", string(data))
	}
	return true
}
