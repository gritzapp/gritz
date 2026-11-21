package workspace

import (
	"path/filepath"
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
