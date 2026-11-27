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
			assert.DeepEqual(t, ws.AgentConfig().GritzRPC, tt.options)
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

// TestWithFeatures_LeavesWorkspace asserts applying features leaves the
// workspace they were applied to as it was, including the feature whose
// agent: replaced the workspace's, and that one result's commands don't leak
// into another's.
func TestWithFeatures_LeavesWorkspace(t *testing.T) {
	t.Parallel()
	// Arrange - every field WithFeatures writes to is set, and the commands
	// have spare capacity, which appending must not write into.
	ws := &Workspace{
		Container: Container{
			Image:       "alpine:latest",
			Environment: map[string]string{"BASE_ENV": "container"},
		},
		LambdaMicroVM: &LambdaMicroVM{
			ImageIdentifier: "arn:image",
			Environment:     map[string]string{"BASE_ENV": "lambda"},
		},
		Agent: Agent{
			Type:       "claude",
			McpServers: map[string]agent.McpServer{"github": {Type: "stdio", Command: "github-mcp"}},
		},
		Commands: append(make([]string, 0, 8), "git clone repo"),
		Secrets:  map[string]string{"GH_TOKEN": "gho_base"},
		Features: Features{
			"codex": {
				Environment: map[string]string{"CODEX_HOME": "/codex"},
				Secrets:     map[string]string{"OPENAI_API_KEY": "openai"},
				McpServers:  map[string]agent.McpServer{"jira": {Type: "stdio", Command: "jira-mcp"}},
				Commands:    []string{"install codex"},
				Agent: &Agent{
					Type:       "codex",
					McpServers: map[string]agent.McpServer{"github": {Type: "stdio", Command: "github-mcp"}},
				},
			},
			"node": {Commands: []string{"install node"}},
		},
	}

	// Act
	codex, err := ws.WithFeatures([]string{"codex"})
	assert.NilError(t, err)
	_, err = ws.WithFeatures([]string{"node"})
	assert.NilError(t, err)

	// Assert
	want := &Workspace{
		Container: Container{
			Image:       "alpine:latest",
			Environment: map[string]string{"BASE_ENV": "container"},
		},
		LambdaMicroVM: &LambdaMicroVM{
			ImageIdentifier: "arn:image",
			Environment:     map[string]string{"BASE_ENV": "lambda"},
		},
		Agent: Agent{
			Type:       "claude",
			McpServers: map[string]agent.McpServer{"github": {Type: "stdio", Command: "github-mcp"}},
		},
		Commands: []string{"git clone repo"},
		Secrets:  map[string]string{"GH_TOKEN": "gho_base"},
		Features: Features{
			"codex": {
				Environment: map[string]string{"CODEX_HOME": "/codex"},
				Secrets:     map[string]string{"OPENAI_API_KEY": "openai"},
				McpServers:  map[string]agent.McpServer{"jira": {Type: "stdio", Command: "jira-mcp"}},
				Commands:    []string{"install codex"},
				Agent: &Agent{
					Type:       "codex",
					McpServers: map[string]agent.McpServer{"github": {Type: "stdio", Command: "github-mcp"}},
				},
			},
			"node": {Commands: []string{"install node"}},
		},
	}
	assert.DeepEqual(t, ws, want)
	assert.DeepEqual(t, codex.Commands, []string{"git clone repo", "install codex"})
}

func TestWithFeatures_Unknown(t *testing.T) {
	t.Parallel()
	ws := &Workspace{Features: Features{"codex": {}}}

	_, err := ws.WithFeatures([]string{"codex", "playwright"})

	assert.Error(t, err, `unknown feature "playwright"`)
}

// TestWithFeatures_Agent asserts a feature's agent: replaces the workspace's
// as a whole, keeping none of its fields or servers, and that the last of
// several wins. Every feature's mcp_servers are merged on top of it in order,
// later wins.
func TestWithFeatures_Agent(t *testing.T) {
	t.Parallel()
	// Arrange
	ws := &Workspace{
		Agent: Agent{
			Type:       "claude",
			Cwd:        "/root/repo",
			McpServers: map[string]agent.McpServer{"github": {Type: "stdio", Command: "github-mcp"}},
		},
	}
	ws.Features = Features{
		"codex": {
			Agent:      &Agent{Type: "codex", Codex: &CodexConfig{Model: "gpt-5-codex"}},
			McpServers: map[string]agent.McpServer{"jira": {Type: "stdio", Command: "jira-mcp"}},
		},
		"dummy": {Agent: &Agent{
			Type:       "dummy",
			Cwd:        "/tmp",
			McpServers: map[string]agent.McpServer{"jira": {Type: "stdio", Command: "dummy-jira"}},
		}},
		"jira": {McpServers: map[string]agent.McpServer{
			"jira":  {Type: "http", URL: "https://jira.example.com/mcp"},
			"other": {Type: "stdio", Command: "other"},
		}},
	}

	// Act
	got, err := ws.WithFeatures([]string{"codex", "dummy", "jira"})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, got.Agent, Agent{
		Type: "dummy",
		Cwd:  "/tmp",
		McpServers: map[string]agent.McpServer{
			"jira":  {Type: "http", URL: "https://jira.example.com/mcp"},
			"other": {Type: "stdio", Command: "other"},
		},
	})
	assert.DeepEqual(t, ws.Features["dummy"].Agent.McpServers, map[string]agent.McpServer{
		"jira": {Type: "stdio", Command: "dummy-jira"},
	})
}

// TestWithFeatures_FeatureAgentWithoutServers asserts a feature's servers are
// added to an agent: that declares none.
func TestWithFeatures_FeatureAgentWithoutServers(t *testing.T) {
	t.Parallel()
	// Arrange
	ws := &Workspace{
		Agent: Agent{
			Type:       "claude",
			McpServers: map[string]agent.McpServer{"github": {Type: "stdio", Command: "github-mcp"}},
		},
	}
	ws.Features = Features{
		"codex": {Agent: &Agent{Type: "codex"}},
		"jira":  {McpServers: map[string]agent.McpServer{"jira": {Type: "stdio", Command: "jira-mcp"}}},
	}

	// Act
	got, err := ws.WithFeatures([]string{"codex", "jira"})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, got.Agent.McpServers, map[string]agent.McpServer{
		"jira": {Type: "stdio", Command: "jira-mcp"},
	})
}

// TestWithFeatures_Environment asserts the features' environment and secrets
// are merged onto the workspace's in order, later wins: a name a feature sets
// in environment is no longer a secret, and one it sets in secrets is no
// longer in either backend's environment.
func TestWithFeatures_Environment(t *testing.T) {
	t.Parallel()
	// Arrange
	ws := &Workspace{
		Container:     Container{Environment: map[string]string{"BASE_ENV": "container"}},
		LambdaMicroVM: &LambdaMicroVM{Environment: map[string]string{"BASE_ENV": "lambda"}},
		Secrets:       map[string]string{"GH_TOKEN": "gho_base"},
	}
	ws.Features = Features{
		"a": {
			Environment: map[string]string{"JIRA_SITE": "a.atlassian.net", "GH_TOKEN": "not-secret", "LEVEL": "1"},
			Secrets:     map[string]string{"JIRA_API_TOKEN": "jira_a"},
		},
		"b": {
			Environment: map[string]string{"JIRA_API_TOKEN": "plain", "LEVEL": "2"},
			Secrets:     map[string]string{"BASE_ENV": "secret", "JIRA_SITE": "b.atlassian.net"},
		},
	}

	// Act
	got, err := ws.WithFeatures([]string{"a", "b"})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, got.Container.Environment, map[string]string{
		"GH_TOKEN": "not-secret", "LEVEL": "2", "JIRA_API_TOKEN": "plain",
	})
	assert.DeepEqual(t, got.LambdaMicroVM.Environment, map[string]string{
		"GH_TOKEN": "not-secret", "LEVEL": "2", "JIRA_API_TOKEN": "plain",
	})
	assert.DeepEqual(t, got.Secrets, map[string]string{
		"BASE_ENV": "secret", "JIRA_SITE": "b.atlassian.net",
	})
}

// TestWithFeatures_EnvironmentOnly asserts a feature's environment and
// secrets reach a workspace with none of its own, and that a workspace
// without lambda_microvm: doesn't get one.
func TestWithFeatures_EnvironmentOnly(t *testing.T) {
	t.Parallel()
	// Arrange
	ws := &Workspace{
		Container: Container{Image: "alpine:latest"},
		Agent:     Agent{Type: "dummy"},
		Features: Features{"jira": {
			Environment: map[string]string{"JIRA_SITE": "gritz.atlassian.net"},
			Secrets:     map[string]string{"JIRA_API_TOKEN": "jira"},
		}},
	}

	// Act
	got, err := ws.WithFeatures([]string{"jira"})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, got.Container.Environment, map[string]string{"JIRA_SITE": "gritz.atlassian.net"})
	assert.Assert(t, got.LambdaMicroVM == nil)
	assert.DeepEqual(t, got.Secrets, map[string]string{"JIRA_API_TOKEN": "jira"})
}

// TestWithFeatures_Commands asserts the features' commands run after the
// workspace's, in the order of names, a feature with agent: in its position
// too.
func TestWithFeatures_Commands(t *testing.T) {
	t.Parallel()
	// Arrange
	ws := &Workspace{Commands: []string{"git clone repo"}}
	ws.Features = Features{
		"node":       {Commands: []string{"install node", "install pnpm"}},
		"codex":      {Commands: []string{"install codex"}, Agent: &Agent{Type: "codex"}},
		"playwright": {Commands: []string{"install playwright"}},
	}

	// Act
	got, err := ws.WithFeatures([]string{"node", "codex", "playwright"})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, got.Commands, []string{
		"git clone repo", "install node", "install pnpm", "install codex", "install playwright",
	})
}
