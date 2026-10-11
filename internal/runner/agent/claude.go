package agent

import (
	"context"
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/gritzapp/gritz/internal/runner/agent/toollog"
)

// ClaudeAgent implements Agent using Claude Code CLI.
type ClaudeAgent struct {
	log        *DriverLog
	cwd        string
	env        []string
	verbose    bool
	mcpServers map[string]McpServer
	options    *ClaudeOptions
}

// Prompt sends a prompt to Claude and waits for completion.
func (a *ClaudeAgent) Prompt(ctx context.Context, prompt string, resume bool) error {
	a.log.Info("sending prompt", "text", prompt)

	// Use model from options, defaulting to "opus"
	model := "opus"
	if a.options != nil && a.options.Model != "" {
		model = a.options.Model
	}

	args := []string{
		"--dangerously-skip-permissions",
		"--verbose",
		"--output-format", "stream-json",
		"--strict-mcp-config",
		"--model", model,
	}

	// Add MCP config if present
	if len(a.mcpServers) > 0 {
		mcpConfig := map[string]any{"mcpServers": a.mcpServers}
		mcpJSON, err := json.Marshal(mcpConfig)
		if err != nil {
			return err
		}
		args = append(args, "--mcp-config", string(mcpJSON))
	}

	// Resume previous session if requested
	if resume {
		args = append(args, "--continue")
	}

	args = append(args, "--print", prompt)

	bin := "claude"
	if a.options != nil && a.options.Bin != "" {
		bin = a.options.Bin
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = a.cwd
	// Tee Claude's stderr into the log sink (os.Stderr stays so docker logs is
	// unchanged). Stdout is not teed raw — it is the JSON stream parsed below
	// into a.log tool summaries, which already reach the sink via the slog tee.
	cmd.Stderr = a.log.Stderr()

	// Prevent claude code from auto-updating & allow skipping permissions as root user
	cmd.Env = append(slices.Clip(a.env), "IS_SANDBOX=1", "DISABLE_AUTOUPDATER=1")

	// Create a new process group so we can kill all child processes.
	// When npx spawns node, we need to kill the entire process tree.
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

	readLines(stdout, a.log, func(line []byte) {
		if a.verbose || !a.handleStreamEvent(line) {
			a.log.Info("output", "line", string(line))
		}
	})

	return cmd.Wait()
}

// Close releases any resources held by the agent.
func (a *ClaudeAgent) Close() error {
	return nil
}

func (a *ClaudeAgent) handleStreamEvent(data []byte) bool {
	var event struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		// result events
		IsError    bool     `json:"is_error"`
		Result     string   `json:"result"`
		Errors     []string `json:"errors"`
		NumTurns   int      `json:"num_turns"`
		DurationMS int64    `json:"duration_ms"`
		// system api_retry events
		Attempt      int    `json:"attempt"`
		MaxRetries   int    `json:"max_retries"`
		RetryDelayMS int64  `json:"retry_delay_ms"`
		ErrorStatus  *int   `json:"error_status"`
		Error        string `json:"error"`
		Message      struct {
			Content []struct {
				Type      string `json:"type"`
				Text      string `json:"text"`
				Name      string `json:"name"`
				Input     any    `json:"input"`
				ToolUseID string `json:"tool_use_id"`
				Content   string `json:"content"`
				IsError   bool   `json:"is_error"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return false
	}
	switch event.Type {
	case "assistant":
		for _, block := range event.Message.Content {
			switch block.Type {
			case "text":
				if block.Text != "" {
					a.log.Info("text", "content", block.Text)
				}
			case "tool_use":
				// block.Input is an already-decoded object; redact the bulky
				// content-bearing fields Claude Code produces before summarizing.
				input, _ := block.Input.(map[string]any)
				input = toollog.Redact(input, "old_string", "new_string", "content")
				a.log.Info("tool", "name", block.Name, "summary", toollog.Summarize(input))
			}
		}
	case "user":
		for _, block := range event.Message.Content {
			if block.Type == "tool_result" {
				if block.IsError {
					a.log.Error("tool_result", "tool_use_id", block.ToolUseID, "err", block.Content)
				} else {
					a.log.Debug("tool_result", "tool_use_id", block.ToolUseID)
				}
			}
		}
	case "system":
		// Only API retries are logged: they are what an agent waiting out an
		// API outage would otherwise look like, silence.
		if event.Subtype == "api_retry" {
			// error_status is null for connection errors with no HTTP response.
			var status any = "none"
			if event.ErrorStatus != nil {
				status = *event.ErrorStatus
			}
			a.log.Warn("api_retry",
				"status", status,
				"error", event.Error,
				"attempt", event.Attempt,
				"max_retries", event.MaxRetries,
				"retry_delay_ms", event.RetryDelayMS)
		}
	case "result":
		attrs := []any{"subtype", event.Subtype, "turns", event.NumTurns, "duration_ms", event.DurationMS}
		if !event.IsError {
			a.log.Info("result", attrs...)
			break
		}
		// Error subtypes carry their messages in errors; a "success" result
		// with is_error set (e.g. an API error) carries it in result.
		errText := strings.Join(event.Errors, "; ")
		if errText == "" {
			errText = event.Result
		}
		a.log.Error("result", append(attrs, "err", errText)...)
	}
	return true
}
