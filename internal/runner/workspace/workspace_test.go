package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/taskvar"
	"gopkg.in/yaml.v3"
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
	github := agent.McpServer{Type: "stdio", Command: "gritz", Args: []string{"tool", "github-mcp"}}
	assert.DeepEqual(t, ws.Features, Features{
		{Name: "node", Description: "Node.js", Commands: []string{"mise use -g node@22"}},
		{
			Name:        "jira",
			Environment: map[string]string{"JIRA_SITE": "gritz.atlassian.net"},
			Secrets:     map[string]string{"JIRA_API_TOKEN": "jira-token"},
			McpServers:  map[string]agent.McpServer{"jira": {Type: "stdio", Command: "jira-mcp"}},
		},
		{
			Name:    "codex",
			Secrets: map[string]string{"OPENAI_API_KEY": "openai-key"},
			Agent:   &Agent{Type: "codex", McpServers: map[string]agent.McpServer{"github": github}},
		},
		{
			Name:  "dummy",
			Agent: &Agent{Type: "dummy", McpServers: map[string]agent.McpServer{"github": github}},
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
			name: "uppercase name",
			yaml: `
agent: {type: claude}
features:
  Node: {}`,
			err: `workspace "ws": features.Node: name must be a lowercase letter`,
		},
		{
			name: "name starts with a digit",
			yaml: `
agent: {type: claude}
features:
  1node: {}`,
			err: `workspace "ws": features.1node: name must be a lowercase letter`,
		},
		{
			name: "name too long",
			yaml: `
agent: {type: claude}
features:
  a` + strings.Repeat("b", 64) + `: {}`,
			err: `name must be a lowercase letter followed by up to 63`,
		},
		{
			name: "declared twice",
			yaml: `
agent: {type: claude}
features:
  node: {}
  node: {}`,
			err: `feature "node" is declared twice`,
		},
		{
			name: "not a mapping",
			yaml: `
agent: {type: claude}
features: [node]`,
			err: `features must be a mapping`,
		},
		{
			name: "invalid feature agent",
			yaml: `
agent: {type: claude}
features:
  rpc:
    agent:
      type: gritzrpc`,
			err: `workspace "ws": features.rpc: agent: gritzrpc.command is required`,
		},
		{
			name: "invalid feature mcp server",
			yaml: `
agent: {type: claude}
features:
  jira:
    mcp_servers:
      jira:
        type: carrier-pigeon`,
			err: `workspace "ws": features.jira: mcp_servers.jira: `,
		},
		{
			name: "environment shadows a workspace secret",
			yaml: `
agent: {type: claude}
secrets:
  TOKEN: x
features:
  a:
    environment:
      TOKEN: y`,
			err: `workspace "ws": features.a.environment: TOKEN is also defined in secrets`,
		},
		{
			name: "secret shadows container environment",
			yaml: `
agent: {type: claude}
container:
  environment:
    TOKEN: x
features:
  a:
    secrets:
      TOKEN: y`,
			err: `workspace "ws": features.a.secrets: TOKEN is also defined in container.environment`,
		},
		{
			name: "environment shadows lambda_microvm environment",
			yaml: `
agent: {type: claude}
lambda_microvm:
  environment:
    REGION: x
features:
  a:
    environment:
      REGION: y`,
			err: `workspace "ws": features.a.environment: REGION is also defined in lambda_microvm.environment`,
		},
		{
			name: "environment in two features",
			yaml: `
agent: {type: claude}
features:
  a:
    environment:
      MODEL: x
  b:
    environment:
      MODEL: y`,
			err: `workspace "ws": features.b.environment: MODEL is also defined in features.a.environment`,
		},
		{
			name: "environment in one feature, secret in another",
			yaml: `
agent: {type: claude}
features:
  a:
    environment:
      TOKEN: x
  b:
    secrets:
      TOKEN: y`,
			err: `workspace "ws": features.b.secrets: TOKEN is also defined in features.a.environment`,
		},
		{
			name: "environment and secret in one feature",
			yaml: `
agent: {type: claude}
features:
  a:
    environment:
      TOKEN: x
    secrets:
      TOKEN: y`,
			err: `workspace "ws": features.a.secrets: TOKEN is also defined in features.a.environment`,
		},
		{
			name: "reserved environment prefix",
			yaml: `
agent: {type: claude}
features:
  a:
    environment:
      GRITZ_VAR_BRANCH: x`,
			err: `workspace "ws": features.a.environment: GRITZ_VAR_BRANCH: the GRITZ_ prefix is reserved`,
		},
		{
			name: "reserved secret prefix",
			yaml: `
agent: {type: claude}
features:
  a:
    secrets:
      GRITZ_TOKEN: x`,
			err: `workspace "ws": features.a.secrets: GRITZ_TOKEN: the GRITZ_ prefix is reserved`,
		},
		{
			name: "mcp server in two features",
			yaml: `
agent: {type: claude}
features:
  a:
    mcp_servers:
      jira: {type: stdio, command: jira-mcp}
  b:
    mcp_servers:
      jira: {type: stdio, command: jira-mcp}`,
			err: `workspace "ws": features.b.mcp_servers: jira is also defined in features.a.mcp_servers`,
		},
		{
			name: "feature mcp server in the workspace's agent config",
			yaml: `
agent:
  type: claude
  mcp_servers:
    github: {type: stdio, command: gh-mcp}
features:
  a:
    mcp_servers:
      github: {type: stdio, command: gh-mcp}`,
			err: `workspace "ws": features.a.mcp_servers: github is also defined in agent.mcp_servers`,
		},
		{
			name: "feature mcp server in a feature's agent config",
			yaml: `
agent: {type: claude}
features:
  a:
    mcp_servers:
      github: {type: stdio, command: gh-mcp}
  b:
    agent:
      type: codex
      mcp_servers:
        github: {type: stdio, command: gh-mcp}`,
			err: `workspace "ws": features.a.mcp_servers: github is also defined in features.b.agent.mcp_servers`,
		},
		{
			name: "gritz mcp server in the workspace's agent config",
			yaml: `
agent:
  type: claude
  mcp_servers:
    gritz: {type: stdio, command: gritz}`,
			err: `workspace "ws": agent: mcp_servers.gritz: the name is reserved`,
		},
		{
			name: "gritz mcp server in a feature's agent config",
			yaml: `
agent: {type: claude}
features:
  a:
    agent:
      type: claude
      mcp_servers:
        gritz: {type: stdio, command: gritz}`,
			err: `workspace "ws": features.a: agent: mcp_servers.gritz: the name is reserved`,
		},
		{
			name: "gritz mcp server in a feature",
			yaml: `
agent: {type: claude}
features:
  a:
    mcp_servers:
      gritz: {type: stdio, command: gritz}`,
			err: `workspace "ws": features.a: mcp_servers.gritz: the name is reserved`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "workspaces.yaml")
			data := "workspaces:\n  ws:" + strings.ReplaceAll(tt.yaml, "\n", "\n    ")
			assert.NilError(t, os.WriteFile(path, []byte(data), 0o644))

			_, err := LoadConfig(path, nil)

			assert.ErrorContains(t, err, tt.err)
		})
	}
}

func TestSelect(t *testing.T) {
	t.Parallel()
	ws := Workspace{Features: Features{
		{Name: "node"},
		{Name: "jira"},
		{Name: "codex", Agent: &Agent{Type: "codex"}},
		{Name: "dummy", Agent: &Agent{Type: "dummy"}},
	}}
	tests := []struct {
		name  string
		names []string
		want  []string
		err   string
	}{
		{name: "none"},
		{name: "declaration order", names: []string{"codex", "jira", "node"}, want: []string{"node", "jira", "codex"}},
		{name: "unknown", names: []string{"node", "playwright"}, err: `unknown feature "playwright"`},
		{name: "two agent configs", names: []string{"dummy", "codex"}, err: `features "codex" and "dummy" both have an agent config`},
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
			var names []string
			for _, f := range features {
				names = append(names, f.Name)
			}
			assert.DeepEqual(t, names, tt.want)
		})
	}
}

func TestFeatures_MarshalYAML(t *testing.T) {
	t.Parallel()
	features := Features{
		{Name: "node", Commands: []string{"mise use -g node@22"}},
		{
			Name:        "jira",
			Description: "Jira",
			Environment: map[string]string{"JIRA_SITE": "gritz.atlassian.net"},
			Secrets:     map[string]string{"JIRA_USER": "bot", "JIRA_API_TOKEN": "jira-token"},
		},
		{Name: "codex", Agent: &Agent{Type: "codex"}},
	}

	out, err := yaml.Marshal(features)

	assert.NilError(t, err)
	assert.Equal(t, string(out), `node:
    commands:
        - mise use -g node@22
jira:
    description: Jira
    environment:
        JIRA_SITE: gritz.atlassian.net
    secrets:
        - JIRA_API_TOKEN
        - JIRA_USER
codex:
    agent:
        type: codex
        cwd: ""
        prompt: ""
        verbose: false
        mcp_servers: {}
`)
}
