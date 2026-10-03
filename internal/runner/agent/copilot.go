package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// CopilotAgent implements Agent using GitHub Copilot CLI.
type CopilotAgent struct {
	log        *DriverLog
	cwd        string
	mcpServers map[string]McpServer
	options    *CopilotOptions
}

// Prompt sends a prompt to Copilot and waits for completion.
func (a *CopilotAgent) Prompt(ctx context.Context, prompt string, resume bool) error {
	a.log.Info("sending prompt", "text", prompt)

	args := []string{
		"--silent",
		"--allow-all-tools",
		"--allow-all-paths",
		"--allow-all-urls",
		"--no-auto-update",
	}

	// Add model if specified in options
	if a.options != nil && a.options.Model != "" {
		args = append(args, "--model", a.options.Model)
	}

	// Add MCP config if present
	if len(a.mcpServers) > 0 {
		mcpJSON, err := a.mcpConfigJSON()
		if err != nil {
			return err
		}
		a.log.Info("mcp config", "json", string(mcpJSON))
		args = append(args, "--additional-mcp-config", string(mcpJSON))
	} else {
		a.log.Warn("no mcp servers configured")
	}

	// Resume previous session if requested
	if resume {
		args = append(args, "--continue")
	}

	args = append(args, "--prompt", prompt)

	bin := "copilot"
	if a.options != nil && a.options.Bin != "" {
		bin = a.options.Bin
	}

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
		line := scanner.Text()
		a.log.Info("output", "line", line)
	}

	return cmd.Wait()
}

// Close releases any resources held by the agent.
func (a *CopilotAgent) Close() error {
	return nil
}

// mcpConfigJSON returns the MCP servers configuration as JSON for --additional-mcp-config.
func (a *CopilotAgent) mcpConfigJSON() ([]byte, error) {
	// Convert our McpServer format to Copilot's expected format
	servers := make(map[string]any)
	for name, srv := range a.mcpServers {
		server := map[string]any{
			"tools": []string{"*"},
		}
		switch srv.Type {
		case "stdio":
			server["type"] = "local"
			server["command"] = srv.Command
			if len(srv.Args) > 0 {
				server["args"] = srv.Args
			}
			if len(srv.Env) > 0 {
				server["env"] = srv.Env
			}
		case "http", "sse":
			server["type"] = srv.Type
			server["url"] = srv.URL
			if len(srv.Headers) > 0 {
				server["headers"] = srv.Headers
			}
		}
		servers[name] = server
	}
	return json.Marshal(map[string]any{"mcpServers": servers})
}
