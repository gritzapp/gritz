package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// readEnv parses the output of env(1) into its KEY=value lines.
func readEnv(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	assert.NilError(t, err)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// TestAgentPrompt_Env runs each agent against a fake CLI that dumps its
// environment, and asserts the child sees the driver's environment with the
// agent's Env layered over it. Not parallel: it sets driver variables with
// t.Setenv.
func TestAgentPrompt_Env(t *testing.T) {
	t.Setenv("GRITZ_TEST_INHERITED", "inherited")
	t.Setenv("GRITZ_TEST_OVERRIDE", "process-value")
	tests := []struct {
		typ   string
		extra []string
	}{
		{TypeClaude, []string{"IS_SANDBOX=1", "DISABLE_AUTOUPDATER=1"}},
		{TypeCodex, nil},
		{TypeCopilot, nil},
		{TypeCursor, nil},
		{TypeSloppy, nil},
	}
	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			// Arrange - a fake CLI that ignores its args and writes its env to a file
			dir := t.TempDir()
			out := filepath.Join(dir, "env")
			bin := filepath.Join(dir, tt.typ)
			script := "#!/bin/sh\nenv > " + out + "\n"
			assert.NilError(t, os.WriteFile(bin, []byte(script), 0o755))
			env := []string{"GRITZ_TEST_RUN=run-value", "GRITZ_TEST_OVERRIDE=run-value"}
			a, err := NewAgent(Options{
				Type:    tt.typ,
				Cwd:     dir,
				Env:     env,
				Claude:  &ClaudeOptions{Bin: bin},
				Codex:   &CodexOptions{Bin: bin},
				Copilot: &CopilotOptions{Bin: bin},
				Cursor:  &CursorOptions{Bin: bin},
				Sloppy:  &SloppyOptions{Bin: bin},
			})
			assert.NilError(t, err)

			// Act
			assert.NilError(t, a.Prompt(t.Context(), "prompt", false))

			// Assert
			got := readEnv(t, out)
			for _, kv := range append(env, tt.extra...) {
				assert.Assert(t, cmp.Contains(got, kv))
			}
			assert.Assert(t, cmp.Contains(got, "GRITZ_TEST_INHERITED=inherited"))
			assert.Assert(t, !slices.Contains(got, "GRITZ_TEST_OVERRIDE=process-value"))
		})
	}
}

// TestDriverRun_Env asserts the driver's Env is layered over its own
// environment for the setup commands and the agent, and for what the config's
// cwd expands against. Not parallel: it sets driver variables with t.Setenv.
func TestDriverRun_Env(t *testing.T) {
	// Arrange - cwd uses a variable only the driver process sets and one the run
	// overrides, so the agent's working directory says what cwd expanded against.
	parent := t.TempDir()
	assert.NilError(t, os.Mkdir(filepath.Join(parent, "run"), 0o755))
	t.Setenv("GRITZ_TEST_INHERITED", "inherited")
	t.Setenv("GRITZ_TEST_PARENT", parent)
	t.Setenv("WORKDIR", "process")
	out := t.TempDir()
	driver, _ := setupDriver(t, &Config{
		Type:     TypeDummy,
		Cwd:      "$GRITZ_TEST_PARENT/$WORKDIR",
		Commands: []string{"env > " + filepath.Join(out, "setup")},
		Dummy: &DummyOptions{Commands: []string{
			"env > " + filepath.Join(out, "agent"),
			"pwd > " + filepath.Join(out, "pwd"),
		}},
	})
	driver.Env = []string{"GRITZ_TEST_RUN=run-value", "WORKDIR=run"}

	// Act
	assert.NilError(t, driver.Run(t.Context()))

	// Assert
	for _, name := range []string{"setup", "agent"} {
		got := readEnv(t, filepath.Join(out, name))
		assert.Assert(t, cmp.Contains(got, "GRITZ_TEST_RUN=run-value"), name)
		assert.Assert(t, cmp.Contains(got, "GRITZ_TEST_INHERITED=inherited"), name)
		assert.Assert(t, cmp.Contains(got, "WORKDIR=run"), name)
		assert.Assert(t, !slices.Contains(got, "WORKDIR=process"), name)
	}
	pwd, err := os.ReadFile(filepath.Join(out, "pwd"))
	assert.NilError(t, err)
	assert.Equal(t, strings.TrimSpace(string(pwd)), filepath.Join(parent, "run"))
}

func TestEnvLookup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		key  string
		want string
	}{
		{"present", "A", "1"},
		{"last wins", "B", "3"},
		{"value with equals", "C", "x=y"},
		{"missing", "D", ""},
		{"prefix is not a match", "AB", ""},
	}
	env := []string{"A=1", "B=2", "B=3", "C=x=y", "ABC=4"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, envLookup(env, tt.key), tt.want)
		})
	}
}
