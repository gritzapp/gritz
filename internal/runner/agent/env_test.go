package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// readEnv parses the output of env(1) into its KEY=value lines, sorted. PWD is
// dropped: os/exec adds it when a command has a Dir, and sh sets it to its
// working directory, so it never comes from the Env under test.
func readEnv(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	assert.NilError(t, err)
	env := strings.Split(strings.TrimSpace(string(b)), "\n")
	env = slices.DeleteFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "PWD=") })
	return sorted(env)
}

func sorted(env []string) []string {
	return slices.Sorted(slices.Values(env))
}

// TestAgentPrompt_Env runs each agent against a fake CLI that dumps its
// environment, and asserts the child sees exactly the agent's Env and nothing
// the driver process has. Not parallel: it sets a driver-only variable with
// t.Setenv.
func TestAgentPrompt_Env(t *testing.T) {
	t.Setenv("GRITZ_TEST_INHERITED", "leaked")
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
			env := []string{"PATH=" + os.Getenv("PATH"), "GRITZ_TEST_RUN=run-value"}
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
			assert.DeepEqual(t, readEnv(t, out), sorted(slices.Concat(env, tt.extra)))
		})
	}
}

// TestDriverRun_Env asserts the driver's Env is exactly the environment of the
// setup commands and the agent, and is what the config's cwd expands against.
// Not parallel: it sets driver-only variables with t.Setenv.
func TestDriverRun_Env(t *testing.T) {
	// Arrange - WORKDIR differs between the driver process and the run, so the
	// agent's working directory says which one cwd expanded against.
	t.Setenv("GRITZ_TEST_INHERITED", "leaked")
	t.Setenv("WORKDIR", t.TempDir())
	workdir := t.TempDir()
	out := t.TempDir()
	driver, _ := setupDriver(t, &Config{
		Type:     TypeDummy,
		Cwd:      "$WORKDIR",
		Commands: []string{"env > " + filepath.Join(out, "setup")},
		Dummy: &DummyOptions{Commands: []string{
			"env > " + filepath.Join(out, "agent"),
			"pwd > " + filepath.Join(out, "pwd"),
		}},
	})
	env := []string{"PATH=" + os.Getenv("PATH"), "GRITZ_TEST_RUN=run-value", "WORKDIR=" + workdir}
	driver.Env = env

	// Act
	assert.NilError(t, driver.Run(t.Context()))

	// Assert
	for _, name := range []string{"setup", "agent"} {
		assert.DeepEqual(t, readEnv(t, filepath.Join(out, name)), sorted(env))
	}
	pwd, err := os.ReadFile(filepath.Join(out, "pwd"))
	assert.NilError(t, err)
	assert.Equal(t, strings.TrimSpace(string(pwd)), workdir)
}
