package workspace

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/docker/docker/api/types/network"
	"github.com/icholy/gritz/internal/auth/agentauth"
	"github.com/icholy/gritz/internal/configfile"
	"github.com/icholy/gritz/internal/runner/agent"
	"github.com/icholy/gritz/internal/x/expandvar"
	"gopkg.in/yaml.v3"
)

var defaultYAML = `workspaces:
  pets-workshop:
    description: Example Node.js pets workshop
    secrets:
      CLAUDE_CODE_OAUTH_TOKEN: ${env:CLAUDE_CODE_OAUTH_TOKEN}
    container:
      image: ghcr.io/icholy/gritz-workspace-debian:latest
      working_dir: /root
    commands:
      - git clone https://github.com/github-samples/pets-workshop
    agent:
      type: claude
      cwd: /root/pets-workshop
      mcp_servers: {}
      prompt: |
        This is an example github repository.
        Don't try opening PRs or issues.
`

// DefaultPath returns the default workspaces.yaml path inside the config directory.
func DefaultPath() (string, error) {
	dir, err := configfile.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "workspaces.yaml"), nil
}

// CreateDefault creates a workspaces.yaml with example config if one doesn't exist.
// Returns the path to the file and whether it was created.
func CreateDefault() (string, bool, error) {
	path, err := DefaultPath()
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(path); err == nil {
		return path, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(path, []byte(defaultYAML), 0644); err != nil {
		return "", false, err
	}
	return path, true, nil
}

type Config struct {
	Workspaces map[string]Workspace `yaml:"workspaces"`
}

func (c *Config) Validate() error {
	if len(c.Workspaces) == 0 {
		return fmt.Errorf("no workspaces defined")
	}
	for name, ws := range c.Workspaces {
		if err := ws.Validate(); err != nil {
			return fmt.Errorf("workspace %q: %w", name, err)
		}
	}
	return nil
}

type Workspace struct {
	Description string `yaml:"description"`
	// Container holds the Docker backend's runtime config. LambdaMicroVM holds
	// the Lambda MicroVMs backend's. A workspace may set both so one
	// workspaces.yaml serves runners with different backends; each backend
	// validates and consumes only its own section (see Backend.ValidateWorkspace).
	Container     Container      `yaml:"container"`
	LambdaMicroVM *LambdaMicroVM `yaml:"lambda_microvm,omitempty"`
	Agent         Agent          `yaml:"agent"`
	Commands      []string       `yaml:"commands"`
	// Capabilities grant agents in this workspace additional capabilities, such
	// as "github_token" to issue GitHub App installation tokens. None are
	// granted unless explicitly listed.
	Capabilities []string `yaml:"capabilities"`
	// Secrets declares credentials for the sandbox, backend-agnostic. Each entry
	// becomes an environment variable exactly like an environment: entry — values
	// go through the same ${env:}/${sh:} expansion — but declaring one here is
	// also what makes it a secret: the runner names them in GRITZ_SECRETS so the
	// driver can mask their values in the log it ships to the server. Reference
	// them from commands: by variable (${GH_TOKEN}) so the command string itself
	// carries the name, not the value.
	Secrets map[string]string `yaml:"secrets,omitempty"`
}

// SecretNames returns the declared secret names, sorted.
func (w *Workspace) SecretNames() []string {
	return slices.Sorted(maps.Keys(w.Secrets))
}

type Agent struct {
	Type       string                     `yaml:"type"`
	Cwd        string                     `yaml:"cwd"`
	Prompt     string                     `yaml:"prompt"`
	Verbose    bool                       `yaml:"verbose"`
	McpServers map[string]agent.McpServer `yaml:"mcp_servers"`
	Claude     *ClaudeConfig              `yaml:"claude,omitempty"`
	Codex      *CodexConfig               `yaml:"codex,omitempty"`
	Copilot    *CopilotConfig             `yaml:"copilot,omitempty"`
	Cursor     *CursorConfig              `yaml:"cursor,omitempty"`
	Sloppy     *SloppyConfig              `yaml:"sloppy,omitempty"`
	Dummy      *DummyConfig               `yaml:"dummy,omitempty"`
}

// ClaudeConfig contains Claude-specific agent configuration.
type ClaudeConfig struct {
	Model string `yaml:"model"`
	Bin   string `yaml:"bin"`
}

// CodexConfig contains Codex-specific agent configuration.
type CodexConfig struct {
	Model string `yaml:"model"`
	Bin   string `yaml:"bin"`
}

// CopilotConfig contains Copilot-specific agent configuration.
type CopilotConfig struct {
	Model string `yaml:"model"`
	Bin   string `yaml:"bin"`
}

// CursorConfig contains Cursor-specific agent configuration.
type CursorConfig struct {
	Model string `yaml:"model"`
	Bin   string `yaml:"bin"`
}

// SloppyConfig contains Sloppy-specific agent configuration.
type SloppyConfig struct {
	Bin string `yaml:"bin"`
}

// DummyConfig contains Dummy-specific agent configuration.
type DummyConfig struct {
	// Sleep duration in seconds. If -1, sleeps forever.
	Sleep int `yaml:"sleep"`
	// ToolCalls specifies MCP tool calls to make.
	ToolCalls []agent.DummyToolCall `yaml:"tool_calls"`
	// Commands to run
	Commands []string
	// Error, when set, is returned as an error from the agent.
	Error string `yaml:"error"`
}

func (w *Workspace) Validate() error {
	// The runtime-specific section (container: / lambda_microvm:) is validated
	// by the selected backend's ValidateWorkspace, not here, so a single
	// workspaces.yaml can describe workspaces for backends this runner doesn't
	// use. Only backend-agnostic config is checked at load time.
	if err := w.Agent.Validate(); err != nil {
		return fmt.Errorf("agent: %w", err)
	}
	for _, capability := range w.Capabilities {
		if !agentauth.ValidCapability(capability) {
			return fmt.Errorf("unknown capability %q", capability)
		}
	}
	return nil
}

func (a *Agent) Validate() error {
	for name, srv := range a.McpServers {
		if err := srv.Validate(); err != nil {
			return fmt.Errorf("mcp_servers.%s: %w", name, err)
		}
	}
	return nil
}

type Container struct {
	Image      string   `yaml:"image"`
	Runtime    string   `yaml:"runtime"`
	Privileged bool     `yaml:"privileged"`
	WorkingDir string   `yaml:"working_dir"`
	User       string   `yaml:"user"`
	Volumes    []string `yaml:"volumes"`
	Networks   []string `yaml:"networks"`
	// NetworkMode sets the container's Docker network mode (e.g. "host"). Empty
	// uses the default bridge. Now that agents connect to the server directly over
	// the network instead of a bind-mounted socket, a workspace can use this to
	// control how the container reaches the server.
	NetworkMode string            `yaml:"network_mode"`
	GroupAdd    []string          `yaml:"group_add"`
	Environment map[string]string `yaml:"environment"`
}

// Environ returns the environment variables as a slice of "key=value" strings.
func (c *Container) Environ() []string {
	env := make([]string, 0, len(c.Environment))
	for k, v := range c.Environment {
		env = append(env, k+"="+v)
	}
	return env
}

func (c *Container) Validate() error {
	if c.Image == "" {
		return fmt.Errorf("image is required")
	}
	return nil
}

// LambdaMicroVM holds the Lambda MicroVMs backend's runtime config for a
// workspace. AWS credentials are not configured here; they resolve through the
// standard AWS credential chain on the runner.
type LambdaMicroVM struct {
	// ImageIdentifier is a pre-built MicroVM image ARN to launch from. If empty,
	// the backend builds one from ImageSource on first use.
	ImageIdentifier string `yaml:"image_identifier"`
	// ImageSource is the OCI image to build a MicroVM image from when
	// ImageIdentifier is empty.
	ImageSource string `yaml:"image_source"`
	// Region is the AWS region. Empty defers to the runner's region.
	Region string `yaml:"region"`
	// ExecutionRole is the IAM role ARN the MicroVM assumes at runtime. It needs
	// S3 read for the staged bundle and whatever the workload requires, but no
	// MicroVM control-plane permissions: suspend/resume/terminate are the
	// runner's, never the guest's.
	ExecutionRole string `yaml:"execution_role"`
	// EgressConnector is the network connector ARN granting outbound access so
	// the driver can reach the server (INTERNET_EGRESS or a VPC connector).
	EgressConnector string `yaml:"egress_connector"`
	// IngressConnector is the network connector ARN granting inbound access so
	// the runner can reach the in-VM shim over AWS's managed proxy (SSE lifecycle
	// stream + /gritz/stop). Empty defaults to the managed ALL_INGRESS connector.
	// An operator may instead supply a connector scoped to the gritz
	// control-surface port for tighter, defense-in-depth security (the proxy auth
	// token is already port-scoped).
	IngressConnector string `yaml:"ingress_connector"`
	// StagingBucket is the S3 bucket the backend stages the spec bundle in.
	StagingBucket string `yaml:"staging_bucket"`
	// MaxDurationSeconds caps the MicroVM's lifetime (run-microvm
	// --maximum-duration-in-seconds). 0 uses the backend default.
	MaxDurationSeconds int64             `yaml:"max_duration_seconds"`
	Environment        map[string]string `yaml:"environment"`
}

// Environ returns the environment variables as a slice of "key=value" strings.
func (l *LambdaMicroVM) Environ() []string {
	env := make([]string, 0, len(l.Environment))
	for k, v := range l.Environment {
		env = append(env, k+"="+v)
	}
	return env
}

// NetworkingConfig returns the Docker networking configuration for this container.
func (c *Container) NetworkingConfig() *network.NetworkingConfig {
	if len(c.Networks) == 0 {
		return nil
	}
	endpoints := make(map[string]*network.EndpointSettings, len(c.Networks))
	for _, net := range c.Networks {
		endpoints[net] = &network.EndpointSettings{}
	}
	return &network.NetworkingConfig{
		EndpointsConfig: endpoints,
	}
}

// ExpandFunc is called for each ${namespace:value} found in the config.
type ExpandFunc func(namespace, value string) (string, error)

// ExpandVar is the default ExpandFunc that supports:
//   - ${env:VAR} - environment variables
//   - ${sh:command} - shell command output
func ExpandVar(namespace, value string) (string, error) {
	switch namespace {
	case "env":
		return os.Getenv(value), nil
	case "sh":
		out, err := exec.Command("sh", "-c", value).Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	default:
		return "", fmt.Errorf("unknown namespace: %s", namespace)
	}
}

// LoadConfig loads the workspace config from a file.
// Variables in the format ${namespace:value} are expanded using the provided function.
// If expand is nil, ExpandVar is used.
func LoadConfig(path string, expand ExpandFunc) (*Config, error) {
	if expand == nil {
		expand = ExpandVar
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	if err := expandNode(&node, expand); err != nil {
		return nil, fmt.Errorf("failed to expand variables: %w", err)
	}

	var cfg Config
	if err := node.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to decode config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

func expandNode(node *yaml.Node, expand ExpandFunc) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag == "!!str" || node.Tag == "" {
			expanded, err := expandvar.Expand(node.Value, expand)
			if err != nil {
				return err
			}
			node.Value = expanded
		}
	case yaml.SequenceNode, yaml.MappingNode:
		for _, child := range node.Content {
			if err := expandNode(child, expand); err != nil {
				return err
			}
		}
	case yaml.DocumentNode:
		for _, child := range node.Content {
			if err := expandNode(child, expand); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Config) Get(name string) (*Workspace, error) {
	ws, ok := c.Workspaces[name]
	if !ok {
		return nil, fmt.Errorf("workspace %q not found", name)
	}
	return &ws, nil
}

// AgentConfig converts the workspace agent configuration into an agent.Config.
func (w *Workspace) AgentConfig() agent.Config {
	cfg := agent.Config{
		Type:         w.Agent.Type,
		Cwd:          w.Agent.Cwd,
		Prompt:       w.Agent.Prompt,
		Verbose:      w.Agent.Verbose,
		McpServers:   make(map[string]agent.McpServer),
		Commands:     w.Commands,
		Capabilities: w.Capabilities,
	}
	if w.Agent.Claude != nil {
		cfg.Claude = &agent.ClaudeOptions{
			Model: w.Agent.Claude.Model,
			Bin:   w.Agent.Claude.Bin,
		}
	}
	if w.Agent.Codex != nil {
		cfg.Codex = &agent.CodexOptions{
			Model: w.Agent.Codex.Model,
			Bin:   w.Agent.Codex.Bin,
		}
	}
	if w.Agent.Copilot != nil {
		cfg.Copilot = &agent.CopilotOptions{
			Model: w.Agent.Copilot.Model,
			Bin:   w.Agent.Copilot.Bin,
		}
	}
	if w.Agent.Cursor != nil {
		cfg.Cursor = &agent.CursorOptions{
			Model: w.Agent.Cursor.Model,
			Bin:   w.Agent.Cursor.Bin,
		}
	}
	if w.Agent.Sloppy != nil {
		cfg.Sloppy = &agent.SloppyOptions{
			Bin: w.Agent.Sloppy.Bin,
		}
	}
	if w.Agent.Dummy != nil {
		cfg.Dummy = &agent.DummyOptions{
			Sleep:     w.Agent.Dummy.Sleep,
			ToolCalls: w.Agent.Dummy.ToolCalls,
			Commands:  w.Agent.Dummy.Commands,
			Error:     w.Agent.Dummy.Error,
		}
	}
	maps.Copy(cfg.McpServers, w.Agent.McpServers)
	return cfg
}
