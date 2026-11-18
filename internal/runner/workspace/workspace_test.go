package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gritzapp/gritz/internal/taskvar"
	"gotest.tools/v3/assert"
)

func loadYAML(t *testing.T, data string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workspaces.yaml")
	assert.NilError(t, os.WriteFile(path, []byte(data), 0o644))
	return LoadConfig(path, nil)
}

func TestLoadConfig_Variables(t *testing.T) {
	t.Parallel()

	cfg, err := loadYAML(t, `workspaces:
  story-agent:
    variables:
      JIRA_ISSUE:
        description: The Jira story this task is for
        required: true
      BRANCH:
        description: Branch to check out
        default: main
      LABEL: {}
    agent:
      type: claude
`)

	assert.NilError(t, err)
	ws, err := cfg.Get("story-agent")
	assert.NilError(t, err)
	assert.DeepEqual(t, ws.VariableDecls(), []taskvar.Decl{
		{Name: "BRANCH", Description: "Branch to check out", Default: "main"},
		{Name: "JIRA_ISSUE", Description: "The Jira story this task is for", Required: true},
		{Name: "LABEL"},
	})
}

func TestVariableDecls_None(t *testing.T) {
	t.Parallel()
	ws := &Workspace{}

	decls := ws.VariableDecls()

	assert.Assert(t, decls == nil)
}

func TestLoadConfig_InvalidVariables(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		yaml string
		err  string
	}{
		{
			name: "lowercase name",
			yaml: `
    variables:
      jira: {}`,
			err: `workspace "ws": variables: variable "jira": name must be`,
		},
		{
			name: "required with default",
			yaml: `
    variables:
      BRANCH:
        required: true
        default: main`,
			err: `workspace "ws": variables: variable "BRANCH": a required variable can't have a default`,
		},
		{
			name: "reserved container environment",
			yaml: `
    container:
      environment:
        GRITZ_VAR_BRANCH: main`,
			err: `workspace "ws": container.environment.GRITZ_VAR_BRANCH: the GRITZ_VAR_ prefix is reserved for task variables`,
		},
		{
			name: "reserved lambda_microvm environment",
			yaml: `
    lambda_microvm:
      environment:
        GRITZ_VAR_BRANCH: main`,
			err: `workspace "ws": lambda_microvm.environment.GRITZ_VAR_BRANCH: the GRITZ_VAR_ prefix is reserved for task variables`,
		},
		{
			name: "reserved secret",
			yaml: `
    secrets:
      GRITZ_VAR_TOKEN: x`,
			err: `workspace "ws": secrets.GRITZ_VAR_TOKEN: the GRITZ_VAR_ prefix is reserved for task variables`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := loadYAML(t, "workspaces:\n  ws:"+tt.yaml+"\n    agent:\n      type: claude\n")

			assert.ErrorContains(t, err, tt.err)
		})
	}
}

func TestLoadConfig_GritzPrefixAllowed(t *testing.T) {
	t.Parallel()

	_, err := loadYAML(t, `workspaces:
  ws:
    container:
      environment:
        GRITZ_VARIANT: a
    secrets:
      GRITZ_VAR: b
    agent:
      type: claude
`)

	assert.NilError(t, err)
}
