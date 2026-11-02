package runner

import (
	"encoding/json"
	"fmt"
	"maps"
	"path"

	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/runnerstore"
	"github.com/gritzapp/gritz/internal/runner/workspace"
)

// specInput is what a task's specs are built from.
type specInput struct {
	// Change is the task's run.
	Change runnerstore.TaskChange
	// Workspace is the run's workspace, Change.Workspace.
	Workspace *workspace.Workspace
	ServerURL string
	RunnerID  string
	// Token is the task token.
	Token string
	// BinaryPath is the gritz binary in the sandbox; see
	// backend.Backend.BinaryPath.
	BinaryPath string
	// RunEnv is the backend's environment for the workspace's runs, nil for
	// a backend whose sandboxes have their own; see backend.RunEnver.
	RunEnv map[string]string
}

// buildSpecs assembles the sandbox spec for a task's run: the task token, the
// run's environment and secrets, and the agent config with the injected gritz
// MCP server. It also builds the driver.v1 RunSpec the run is started with,
// whose environment is in.RunEnv under the spec's Env.
func buildSpecs(in specInput) (*backend.Spec, *driverv1.RunSpec, error) {
	c, ws := in.Change, in.Workspace

	// Build agent config
	cfg := ws.AgentConfig()
	mcpArgs := []string{
		"tool", "agent-mcp",
		"--server", in.ServerURL,
		"--task", fmt.Sprint(c.ID),
		"--runner", in.RunnerID,
		"--workspace", c.Workspace,
		"--token", in.Token,
	}
	for _, capability := range ws.Capabilities {
		mcpArgs = append(mcpArgs, "--capability", capability)
	}
	cfg.McpServers["gritz"] = agent.McpServer{
		Type:    "stdio",
		Command: in.BinaryPath,
		Args:    mcpArgs,
	}
	cfgData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal config: %w", err)
	}

	spec := &backend.Spec{
		TaskID:    c.ID,
		Version:   c.Version,
		Workspace: ws,
		ServerURL: in.ServerURL,
		Token:     in.Token,
		// The agent's tools (the git credential helper, the GitHub MCP server)
		// reach the server through these.
		Env: map[string]string{
			"GRITZ_TASK_ID": fmt.Sprint(c.ID),
			"GRITZ_TOKEN":   in.Token,
			"GRITZ_SERVER":  in.ServerURL,
		},
		Secrets: maps.Clone(ws.Secrets),
		Files: []backend.File{
			// Allow non-root agents to write to this directory.
			{Path: path.Dir(agent.DefaultConfigStore.Path(c.ID)), Mode: 0777, Dir: true},
			{Path: agent.DefaultConfigStore.Path(c.ID), Data: cfgData, Mode: 0666},
			// Pre-create the log dir 0777 so a non-root driver can write the
			// append-only /gritz/log file (the driver's MkdirAll is only a
			// fallback for a directly-invoked driver).
			{Path: path.Dir(agent.DefaultLogPath), Mode: 0777, Dir: true},
		},
	}

	env := maps.Clone(in.RunEnv)
	if env == nil {
		env = map[string]string{}
	}
	maps.Copy(env, spec.Env)
	rs := &driverv1.RunSpec{
		TaskId:    spec.TaskID,
		ServerUrl: spec.ServerURL,
		Token:     spec.Token,
		Env:       env,
		Secrets:   maps.Clone(spec.Secrets),
	}
	for _, f := range spec.Files {
		rs.Files = append(rs.Files, &driverv1.File{
			Path: f.Path,
			Data: f.Data,
			Mode: f.Mode,
			Dir:  f.Dir,
		})
	}
	return spec, rs, nil
}
