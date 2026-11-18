package workspace

import (
	"path/filepath"
	"testing"

	"github.com/gritzapp/gritz/internal/taskvar"
	"gotest.tools/v3/assert"
)

func TestLoadConfig_Variables(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig(filepath.Join("testdata", "variables.yaml"), nil)

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
		file string
		err  string
	}{
		{"variables-lowercase-name.yaml", `workspace "ws": variables: variable "jira": name must be`},
		{"variables-required-with-default.yaml", `workspace "ws": variables: variable "BRANCH": a required variable can't have a default`},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			t.Parallel()

			_, err := LoadConfig(filepath.Join("testdata", tt.file), nil)

			assert.ErrorContains(t, err, tt.err)
		})
	}
}
