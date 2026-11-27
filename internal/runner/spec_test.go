package runner

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/runner/backend"

	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"gotest.tools/v3/assert"
)

// TestBuildSpecs_PreCreatesLogDir asserts the sandbox spec ships a directory
// entry for the driver's /gritz/log parent, created 0777 so a non-root driver
// can write the append-only log file there (the driver's MkdirAll is only a
// fallback).
func TestBuildSpecs_PreCreatesLogDir(t *testing.T) {
	t.Parallel()
	// Arrange
	in := specInput{
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

	// Act
	spec, _, err := buildSpecs(in)

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
	// Arrange
	in := specInput{
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

	// Act
	spec, _, err := buildSpecs(in)

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
	in := specInput{
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
		RunEnv:     map[string]string{"FOO": "bar", "GRITZ_TASK_ID": "shadowed"},
	}

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

// TestBuildSpecs_Variables asserts each task variable reaches the spec's Env,
// and so the run's, as GRITZ_VAR_<NAME>.
func TestBuildSpecs_Variables(t *testing.T) {
	t.Parallel()
	// Arrange
	in := specInput{
		Change: runnerstore.TaskChange{ID: 1, Version: 1, Workspace: "test"},
		Workspace: &workspace.Workspace{
			Container: workspace.Container{Image: "alpine:latest"},
			Agent:     workspace.Agent{Type: "dummy"},
		},
		ServerURL:  "http://localhost",
		RunnerID:   "test-runner",
		Token:      "t",
		Variables:  map[string]string{"JIRA_ISSUE": "APP-142", "BRANCH": "main"},
		BinaryPath: "/gritz/bin/gritz",
	}

	// Act
	spec, rs, err := buildSpecs(in)

	// Assert
	assert.NilError(t, err)
	want := map[string]string{
		"GRITZ_TASK_ID":        "1",
		"GRITZ_TOKEN":          "t",
		"GRITZ_SERVER":         "http://localhost",
		"GRITZ_VAR_JIRA_ISSUE": "APP-142",
		"GRITZ_VAR_BRANCH":     "main",
	}
	assert.DeepEqual(t, spec.Env, want)
	assert.DeepEqual(t, rs.GetEnv(), want)
}

// TestTaskManagerSpecs_Features asserts the task's features are applied to
// the workspace its specs are built from: the run gets their secrets and the
// agent config their agent: and MCP servers, with the gritz server injected.
func TestTaskManagerSpecs_Features(t *testing.T) {
	t.Parallel()
	// Arrange
	m := &TaskManager{
		backend: &backend.BackendMock{
			ValidateWorkspaceFunc: func(*workspace.Workspace) error { return nil },
			BinaryPathFunc:        func() string { return "/gritz/bin/gritz" },
		},
		client: &gritzclient.ClientMock{
			GetRunSpecInputsFunc: func(context.Context, *gritzv1.GetRunSpecInputsRequest) (*gritzv1.GetRunSpecInputsResponse, error) {
				return &gritzv1.GetRunSpecInputsResponse{Token: "t", Features: []string{"jira"}}, nil
			},
		},
		workspaces: &workspace.Config{Workspaces: map[string]workspace.Workspace{
			"gritz": {
				Container: workspace.Container{Image: "alpine:latest"},
				Agent:     workspace.Agent{Type: "claude"},
				Secrets:   map[string]string{"GH_TOKEN": "gho"},
				Features: workspace.Features{"jira": {
					Secrets:    map[string]string{"JIRA_API_TOKEN": "jira"},
					McpServers: map[string]agent.McpServer{"jira": {Type: "stdio", Command: "jira-mcp"}},
					Agent:      &workspace.Agent{Type: "dummy"},
				}},
			},
		}},
	}

	// Act
	spec, rs, err := m.specs(t.Context(), runnerstore.TaskChange{ID: 1, Version: 1, Workspace: "gritz"})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, rs.GetSecrets(), map[string]string{"GH_TOKEN": "gho", "JIRA_API_TOKEN": "jira"})
	var cfg agent.Config
	for _, f := range spec.Files {
		if f.Path == agent.DefaultConfigStore.Path(1) {
			assert.NilError(t, json.Unmarshal(f.Data, &cfg))
		}
	}
	assert.Equal(t, cfg.Type, "dummy")
	assert.Equal(t, cfg.McpServers["jira"].Command, "jira-mcp")
	assert.Equal(t, cfg.McpServers["gritz"].Command, "/gritz/bin/gritz")
}

// TestTaskManagerSpecs_UnknownFeature asserts a run whose task names a
// feature its workspace doesn't declare fails to build its specs.
func TestTaskManagerSpecs_UnknownFeature(t *testing.T) {
	t.Parallel()
	// Arrange
	m := &TaskManager{
		backend: &backend.BackendMock{
			ValidateWorkspaceFunc: func(*workspace.Workspace) error { return nil },
		},
		client: &gritzclient.ClientMock{
			GetRunSpecInputsFunc: func(context.Context, *gritzv1.GetRunSpecInputsRequest) (*gritzv1.GetRunSpecInputsResponse, error) {
				return &gritzv1.GetRunSpecInputsResponse{Token: "t", Features: []string{"node", "jira"}}, nil
			},
		},
		workspaces: &workspace.Config{Workspaces: map[string]workspace.Workspace{
			"gritz": {
				Container: workspace.Container{Image: "alpine:latest"},
				Agent:     workspace.Agent{Type: "dummy"},
				Features:  workspace.Features{"node": {}},
			},
		}},
	}

	// Act
	_, _, err := m.specs(t.Context(), runnerstore.TaskChange{ID: 1, Version: 1, Workspace: "gritz"})

	// Assert
	assert.Error(t, err, `workspace "gritz": unknown feature "jira"`)
}
