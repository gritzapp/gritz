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
)

// SloppyAgent implements Agent using the Sloppy CLI.
type SloppyAgent struct {
	log        *DriverLog
	cwd        string
	mcpServers map[string]McpServer
	options    *SloppyOptions
}

// Prompt sends a prompt to Sloppy and waits for completion.
func (a *SloppyAgent) Prompt(ctx context.Context, prompt string, resume bool) error {
	if resume {
		return fmt.Errorf("sloppy does not support session resume")
	}
	a.log.Info("sending prompt", "text", prompt)

	var args []string

	// Write MCP config file if we have MCP servers
	if len(a.mcpServers) > 0 {
		configPath, err := a.writeMcpConfig()
		if err != nil {
			return fmt.Errorf("failed to write MCP config: %w", err)
		}
		args = append(args, "--config", configPath)
	}

	args = append(args, "--prompt", prompt)

	bin := "sloppy"
	if a.options != nil && a.options.Bin != "" {
		bin = a.options.Bin
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = a.cwd
	cmd.Stderr = os.Stderr

	// Create a new process group so we can kill all child processes
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
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
		a.log.Info("output", "line", scanner.Text())
	}

	return cmd.Wait()
}

// Close releases any resources held by the agent.
func (a *SloppyAgent) Close() error {
	return nil
}

// sloppyMcpConfig matches sloppy's expected config format.
type sloppyMcpConfig struct {
	McpServers map[string]sloppyMcpServer `json:"mcpServers"`
}

type sloppyMcpServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// writeMcpConfig writes the MCP servers configuration to a sloppy.json file.
// Sloppy only supports stdio MCP servers; http/sse servers cause an error.
func (a *SloppyAgent) writeMcpConfig() (string, error) {
	config := sloppyMcpConfig{
		McpServers: make(map[string]sloppyMcpServer),
	}
	for name, srv := range a.mcpServers {
		if srv.Type != "stdio" {
			return "", fmt.Errorf("sloppy only supports stdio MCP servers, got %s for %q", srv.Type, name)
		}
		server := sloppyMcpServer{
			Command: srv.Command,
		}
		if len(srv.Args) > 0 {
			server.Args = srv.Args
		}
		config.McpServers[name] = server
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	configDir := filepath.Join(home, ".sloppy")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return "", err
	}

	configPath := filepath.Join(configDir, "sloppy.json")
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return "", err
	}

	return configPath, nil
}
