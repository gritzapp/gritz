package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/taskvar"
	"gotest.tools/v3/assert"
)

func TestLoadConfig_Variables(t *testing.T) {
	t.Parallel()
	tests := []struct {
		file  string
		decls []taskvar.Decl
		err   string
	}{
		{
			file: "variables.yaml",
			decls: []taskvar.Decl{
				{Name: "BRANCH", Description: "Branch to check out", Default: "main"},
				{Name: "JIRA_ISSUE", Description: "The Jira story this task is for", Required: true},
				{Name: "LABEL"},
			},
		},
		{file: "variables-none.yaml"},
		{file: "variables-lowercase-name.yaml", err: `workspace "ws": variables: variable "jira": name must be`},
		{file: "variables-required-with-default.yaml", err: `workspace "ws": variables: variable "BRANCH": a required variable can't have a default`},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			t.Parallel()

			cfg, err := LoadConfig(filepath.Join("testdata", tt.file), nil)

			if tt.err != "" {
				assert.ErrorContains(t, err, tt.err)
				return
			}
			assert.NilError(t, err)
			ws, err := cfg.Get("ws")
			assert.NilError(t, err)
			assert.DeepEqual(t, ws.VariableDecls(), tt.decls)
		})
	}
}

func TestLoadConfig_GritzRPC(t *testing.T) {
	t.Parallel()
	tests := []struct {
		file    string
		options *agent.GritzRPCOptions
		err     string
	}{
		{
			file:    "gritzrpc.yaml",
			options: &agent.GritzRPCOptions{Command: "node", Args: []string{"dist/agent.js"}},
		},
		{file: "gritzrpc-no-command.yaml", err: `workspace "ws": agent: gritzrpc.command is required`},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			t.Parallel()

			cfg, err := LoadConfig(filepath.Join("testdata", tt.file), nil)

			if tt.err != "" {
				assert.ErrorContains(t, err, tt.err)
				return
			}
			assert.NilError(t, err)
			ws, err := cfg.Get("ws")
			assert.NilError(t, err)
			assert.DeepEqual(t, ws.AgentConfig(nil).GritzRPC, tt.options)
		})
	}
}

func TestLoadConfig_Features(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig(filepath.Join("testdata", "features.yaml"), nil)

	assert.NilError(t, err)
	ws, err := cfg.Get("ws")
	assert.NilError(t, err)
	assert.DeepEqual(t, ws.Features, Features{
		"node": {Description: "Node.js", Commands: []string{"mise use -g node@22"}},
		"jira": {
			Environment: map[string]string{"JIRA_SITE": "gritz.atlassian.net"},
			Secrets:     map[string]string{"JIRA_API_TOKEN": "jira-token"},
			McpServers:  map[string]agent.McpServer{"jira": {Type: "stdio", Command: "jira-mcp"}},
		},
		"codex": {
			Secrets: map[string]string{"OPENAI_API_KEY": "openai-key"},
			Agent: &Agent{Type: "codex", McpServers: map[string]agent.McpServer{
				"github": {Type: "stdio", Command: "gritz", Args: []string{"tool", "github-mcp"}},
			}},
		},
	})
}

func TestLoadConfig_FeaturesInvalid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		yaml string
		err  string
	}{
		{
			name: "invalid agent",
			yaml: `
features:
  rpc:
    agent:
      type: gritzrpc`,
			err: `workspace "ws": features.rpc: agent: gritzrpc.command is required`,
		},
		{
			name: "invalid mcp server",
			yaml: `
features:
  jira:
    mcp_servers:
      jira:
        type: carrier-pigeon`,
			err: `workspace "ws": features.jira: mcp_servers.jira: `,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "workspaces.yaml")
			data := "workspaces:\n  ws:\n    agent: {type: claude}" + strings.ReplaceAll(tt.yaml, "\n", "\n    ")
			assert.NilError(t, os.WriteFile(path, []byte(data), 0o644))

			_, err := LoadConfig(path, nil)

			assert.ErrorContains(t, err, tt.err)
		})
	}
}

func TestSelect(t *testing.T) {
	t.Parallel()
	ws := Workspace{Features: Features{
		"node":  {Description: "node"},
		"jira":  {Description: "jira"},
		"codex": {Description: "codex"},
	}}
	tests := []struct {
		name  string
		names []string
		want  []string
		err   string
	}{
		{name: "none"},
		{name: "the order of names", names: []string{"codex", "node", "jira"}, want: []string{"codex", "node", "jira"}},
		{name: "unknown", names: []string{"node", "playwright"}, err: `unknown feature "playwright"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			features, err := ws.Select(tt.names)

			if tt.err != "" {
				assert.Error(t, err, tt.err)
				return
			}
			assert.NilError(t, err)
			var got []string
			for _, f := range features {
				got = append(got, f.Description)
			}
			assert.DeepEqual(t, got, tt.want)
		})
	}
}

// testAgentWorkspace is a workspace whose agent: sets every field a feature's
// agent: has to replace.
func testAgentWorkspace() Workspace {
	return Workspace{
		Commands:     []string{"git clone repo"},
		Capabilities: []string{"create_task"},
		Agent: Agent{
			Type:    "claude",
			Cwd:     "/root/repo",
			Prompt:  "base prompt",
			Verbose: true,
			Claude:  &ClaudeConfig{Model: "opus"},
			McpServers: map[string]agent.McpServer{
				"github": {Type: "stdio", Command: "github-mcp"},
			},
		},
	}
}

func TestAgentConfig(t *testing.T) {
	t.Parallel()
	ws := testAgentWorkspace()

	cfg := ws.AgentConfig(nil)

	assert.DeepEqual(t, cfg, agent.Config{
		Type:         "claude",
		Cwd:          "/root/repo",
		Prompt:       "base prompt",
		Verbose:      true,
		Claude:       &agent.ClaudeOptions{Model: "opus"},
		McpServers:   map[string]agent.McpServer{"github": {Type: "stdio", Command: "github-mcp"}},
		Commands:     []string{"git clone repo"},
		Capabilities: []string{"create_task"},
	})
}

// TestAgentConfig_FeatureAgent asserts a feature's agent: replaces the
// workspace's as a whole, keeping none of its fields or servers, and that the
// last of several wins. The workspace's commands and capabilities stay.
func TestAgentConfig_FeatureAgent(t *testing.T) {
	t.Parallel()
	// Arrange
	ws := testAgentWorkspace()
	features := []Feature{
		{Agent: &Agent{Type: "codex", Codex: &CodexConfig{Model: "gpt-5-codex"}}},
		{Agent: &Agent{
			Type:       "dummy",
			Cwd:        "/tmp",
			McpServers: map[string]agent.McpServer{"jira": {Type: "stdio", Command: "jira-mcp"}},
		}},
		{Description: "no agent"},
	}

	// Act
	cfg := ws.AgentConfig(features)

	// Assert
	assert.DeepEqual(t, cfg, agent.Config{
		Type:         "dummy",
		Cwd:          "/tmp",
		McpServers:   map[string]agent.McpServer{"jira": {Type: "stdio", Command: "jira-mcp"}},
		Commands:     []string{"git clone repo"},
		Capabilities: []string{"create_task"},
	})
}

// TestAgentConfig_FeatureMcpServers asserts the features' mcp_servers are
// added on top of the agent config's in order, a later server replacing an
// earlier one of the same name.
func TestAgentConfig_FeatureMcpServers(t *testing.T) {
	t.Parallel()
	// Arrange
	ws := testAgentWorkspace()
	features := []Feature{
		{McpServers: map[string]agent.McpServer{
			"github": {Type: "stdio", Command: "github-mcp", Args: []string{"--readonly"}},
			"jira":   {Type: "stdio", Command: "jira-mcp"},
		}},
		{McpServers: map[string]agent.McpServer{
			"jira": {Type: "http", URL: "https://jira.example.com/mcp"},
		}},
	}

	// Act
	cfg := ws.AgentConfig(features)

	// Assert
	assert.DeepEqual(t, cfg.McpServers, map[string]agent.McpServer{
		"github": {Type: "stdio", Command: "github-mcp", Args: []string{"--readonly"}},
		"jira":   {Type: "http", URL: "https://jira.example.com/mcp"},
	})
}

// TestAgentConfig_FeatureCommands asserts the features' commands run after
// the workspace's, in the order of the features, a feature with agent: in
// its position too, and that one task's commands don't leak into another's.
func TestAgentConfig_FeatureCommands(t *testing.T) {
	t.Parallel()
	// Arrange - spare capacity, which appending must not write into.
	ws := testAgentWorkspace()
	ws.Commands = append(make([]string, 0, 8), ws.Commands...)
	features := []Feature{
		{Commands: []string{"install node", "install pnpm"}},
		{Commands: []string{"install codex"}, Agent: &Agent{Type: "codex"}},
		{Commands: []string{"install playwright"}},
	}

	// Act
	cfg := ws.AgentConfig(features)
	other := ws.AgentConfig([]Feature{{Commands: []string{"other"}}})

	// Assert
	assert.DeepEqual(t, cfg.Commands, []string{
		"git clone repo", "install node", "install pnpm", "install codex", "install playwright",
	})
	assert.DeepEqual(t, other.Commands, []string{"git clone repo", "other"})
	assert.DeepEqual(t, ws.Commands, []string{"git clone repo"})
}
