package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
)

// ErrStop is a sentinel error used to signal graceful agent cancellation.
// Callers can cancel the context with ErrStop as the cause:
//
//	ctx, cancel := context.WithCancelCause(parentCtx)
//	cancel(agent.ErrStop)
//
// After Prompt returns, callers can check context.Cause(ctx) == ErrStop
// to distinguish a graceful stop from other errors.
var ErrStop = errors.New("stop")

// Agent type constants.
const (
	TypeClaude  = "claude"
	TypeCodex   = "codex"
	TypeCopilot = "copilot"
	TypeCursor  = "cursor"
	TypeSloppy  = "sloppy"
	TypeDummy   = "dummy"
)

// Agent abstracts the underlying agent implementation (e.g., Claude Code, Cursor).
type Agent interface {
	// Prompt sends a prompt to the agent and waits for completion.
	// If resume is true, the agent should continue from the previous session.
	//
	// If the provided context is cancelled, the returned error must have
	// context.Canceled in its error chain (i.e., errors.Is(err, context.Canceled)
	// must return true).
	Prompt(ctx context.Context, prompt string, resume bool) error

	// Close releases any resources held by the agent.
	Close() error
}

// Options contains configuration for creating an Agent.
type Options struct {
	Type string
	Cwd  string
	// Log carries both the structured logger the agent writes to and the raw
	// sink the Claude CLI's stderr is teed into. It defaults to
	// DiscardDriverLog when unset.
	Log        *DriverLog
	Verbose    bool
	McpServers map[string]McpServer
	Claude     *ClaudeOptions
	Codex      *CodexOptions
	Copilot    *CopilotOptions
	Cursor     *CursorOptions
	Sloppy     *SloppyOptions
	Dummy      *DummyOptions
}

// ClaudeOptions contains Claude-specific agent options.
type ClaudeOptions struct {
	Model string
	Bin   string
}

// CodexOptions contains Codex-specific agent options.
type CodexOptions struct {
	Model string
	Bin   string
}

// CopilotOptions contains Copilot-specific agent options.
type CopilotOptions struct {
	Model string
	Bin   string
}

// CursorOptions contains Cursor-specific agent options.
type CursorOptions struct {
	Model string
	Bin   string
}

// SloppyOptions contains Sloppy-specific agent options.
type SloppyOptions struct {
	Bin string
}

// DummyOptions contains Dummy-specific agent options.
type DummyOptions struct {
	// Sleep duration in seconds. If -1, sleeps forever.
	Sleep int
	// ToolCalls specifies MCP tool calls to make.
	ToolCalls []DummyToolCall
	// Commands to run
	Commands []string
	// Error, when set, is returned as an error from Prompt.
	Error string
}

// DummyToolCall specifies an MCP tool call to make.
type DummyToolCall struct {
	Server    string         `json:"server" yaml:"server"`
	Name      string         `json:"name" yaml:"name"`
	Arguments map[string]any `json:"arguments,omitempty" yaml:"arguments"`
}

// NewAgent creates an Agent based on the type specified in options.
// If Type is empty, it defaults to TypeClaude.
func NewAgent(opts Options) (Agent, error) {
	log := cmp.Or(opts.Log, DiscardDriverLog)

	switch cmp.Or(opts.Type, TypeClaude) {
	case TypeClaude:
		return &ClaudeAgent{
			log:        log,
			cwd:        cmp.Or(opts.Cwd, "."),
			verbose:    opts.Verbose,
			mcpServers: opts.McpServers,
			options:    opts.Claude,
		}, nil
	case TypeCodex:
		return &Codegritz{
			log:        log,
			cwd:        cmp.Or(opts.Cwd, "."),
			verbose:    opts.Verbose,
			mcpServers: opts.McpServers,
			options:    opts.Codex,
		}, nil
	case TypeCopilot:
		return &CopilotAgent{
			log:        log,
			cwd:        cmp.Or(opts.Cwd, "."),
			mcpServers: opts.McpServers,
			options:    opts.Copilot,
		}, nil
	case TypeCursor:
		return &CursorAgent{
			log:        log,
			cwd:        cmp.Or(opts.Cwd, "."),
			verbose:    opts.Verbose,
			mcpServers: opts.McpServers,
			options:    opts.Cursor,
		}, nil
	case TypeSloppy:
		return &SloppyAgent{
			log:        log,
			cwd:        cmp.Or(opts.Cwd, "."),
			mcpServers: opts.McpServers,
			options:    opts.Sloppy,
		}, nil
	case TypeDummy:
		return &DummyAgent{
			log:        log,
			cwd:        cmp.Or(opts.Cwd, "."),
			mcpServers: opts.McpServers,
			options:    opts.Dummy,
		}, nil
	default:
		return nil, fmt.Errorf("unknown agent type: %s", opts.Type)
	}
}
