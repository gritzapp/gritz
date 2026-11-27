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
	"github.com/gritzapp/gritz/internal/taskvar"
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
	// Variables are the task's variables, from GetRunSpecInputs.
	Variables map[string]string
	// Features are the task's features, from GetRunSpecInputs, as
	// Workspace.Select returned them.
	Features []workspace.Feature
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
//
// The features are merged onto the workspace in order, later wins: each
// feature's environment goes into the spec's Env and its secrets into its
// Secrets, and a name set in one is removed from the other. The variables
// gritz sets are written after them. Each task variable X is added to the
// spec's Env as GRITZ_VAR_X.
func buildSpecs(in specInput) (*backend.Spec, *driverv1.RunSpec, error) {
	c, ws := in.Change, in.Workspace

	// Build agent config
	cfg := ws.AgentConfig(in.Features)
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

	env, secrets := map[string]string{}, map[string]string{}
	maps.Copy(secrets, ws.Secrets)
	for _, f := range in.Features {
		for name, value := range f.Environment {
			env[name] = value
			delete(secrets, name)
		}
		for name, value := range f.Secrets {
			secrets[name] = value
			delete(env, name)
		}
	}
	// The agent's tools (the git credential helper, the GitHub MCP server)
	// reach the server through these.
	env["GRITZ_TASK_ID"] = fmt.Sprint(c.ID)
	env["GRITZ_TOKEN"] = in.Token
	env["GRITZ_SERVER"] = in.ServerURL
	for name, value := range in.Variables {
		env[taskvar.Env(name)] = value
	}

	spec := &backend.Spec{
		TaskID:    c.ID,
		Version:   c.Version,
		Workspace: ws,
		ServerURL: in.ServerURL,
		Token:     in.Token,
		Env:       env,
		Secrets:   secrets,
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

	runEnv := maps.Clone(in.RunEnv)
	if runEnv == nil {
		runEnv = map[string]string{}
	}
	maps.Copy(runEnv, spec.Env)
	rs := &driverv1.RunSpec{
		TaskId:    spec.TaskID,
		ServerUrl: spec.ServerURL,
		Token:     spec.Token,
		Env:       runEnv,
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
