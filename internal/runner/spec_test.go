package runner

import (
	"path/filepath"
	"testing"

	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"gotest.tools/v3/assert"
)

// testSpecInput returns the input for the specs of run 1 of task 1 in a
// dummy-agent workspace with secrets.
func testSpecInput() specInput {
	return specInput{
		Change: runnerstore.TaskChange{ID: 1, Version: 1, Workspace: "test"},
		Workspace: &workspace.Workspace{
			Container: workspace.Container{Image: "alpine:latest"},
			Agent:     workspace.Agent{Type: "dummy"},
			Secrets: map[string]string{
				"NPM_TOKEN": "npm_secretvalue",
				"GH_TOKEN":  "gho_secretvalue",
			},
		},
		ServerURL:  "http://localhost",
		RunnerID:   "test-runner",
		Token:      "t",
		BinaryPath: "/gritz/bin/gritz",
	}
}

// TestBuildSpecs_PreCreatesLogDir asserts the sandbox spec ships a directory
// entry for the driver's /gritz/log parent, created 0777 so a non-root driver
// can write the append-only log file there (the driver's MkdirAll is only a
// fallback).
func TestBuildSpecs_PreCreatesLogDir(t *testing.T) {
	t.Parallel()
	// Act
	spec, _, err := buildSpecs(testSpecInput())

	// Assert
	assert.NilError(t, err)
	logDir := filepath.Dir(agent.DefaultLogPath)
	var found bool
	for _, f := range spec.Files {
		if f.Path == logDir {
			found = true
			assert.Assert(t, f.Dir)
			assert.Equal(t, f.Mode, int64(0o777))
		}
	}
	assert.Assert(t, found, "spec should pre-create the log dir %q", logDir)
}

// TestBuildSpecs_Secrets asserts the sandbox spec carries the server URL, the
// task token and the declared workspace secrets in their own fields, and
// gives the run the GRITZ_* environment the agent's tools read. The secrets
// stay out of Env: the driver adds them to the run's environment and masks
// them in the log it ships.
func TestBuildSpecs_Secrets(t *testing.T) {
	t.Parallel()
	// Act
	spec, _, err := buildSpecs(testSpecInput())

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, spec.ServerURL, "http://localhost")
	assert.Equal(t, spec.Token, "t")
	assert.DeepEqual(t, spec.Env, map[string]string{
		"GRITZ_TASK_ID": "1",
		"GRITZ_TOKEN":   "t",
		"GRITZ_SERVER":  "http://localhost",
	})
	assert.DeepEqual(t, spec.Secrets, map[string]string{
		"GH_TOKEN":  "gho_secretvalue",
		"NPM_TOKEN": "npm_secretvalue",
	})
}

// TestBuildSpecs_RunEnv asserts a backend's RunEnv reaches the run's
// environment under the spec's own.
func TestBuildSpecs_RunEnv(t *testing.T) {
	t.Parallel()
	// Arrange - a backend whose runs get an environment of their own.
	in := testSpecInput()
	in.RunEnv = map[string]string{"FOO": "bar", "GRITZ_TASK_ID": "shadowed"}

	// Act
	_, rs, err := buildSpecs(in)

	// Assert - the run's environment is RunEnv's, overridden by the spec's.
	assert.NilError(t, err)
	assert.DeepEqual(t, rs.GetEnv(), map[string]string{
		"FOO":           "bar",
		"GRITZ_TASK_ID": "1",
		"GRITZ_TOKEN":   "t",
		"GRITZ_SERVER":  "http://localhost",
	})
}
