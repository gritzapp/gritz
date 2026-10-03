package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/icholy/gritz/internal/x/atomicio"
)

// DefaultConfigStore is the in-sandbox location of the task config file. The
// runner writes the file into the sandbox here and the driver reads and
// rewrites it here; it is a fixed convention shared across the runner/driver
// boundary, not runtime state.
const DefaultConfigStore = ConfigStore("/tmp/gritz")

// ConfigStore reads and writes the per-task config file rooted at its directory.
type ConfigStore string

// Path returns the config file path for the given task ID.
func (s ConfigStore) Path(taskID int64) string {
	return filepath.Join(string(s), fmt.Sprintf("%d.json", taskID))
}

// Load reads and unmarshals the config file for the given task ID. A missing
// file yields an empty config rather than an error.
func (s ConfigStore) Load(taskID int64) (*Config, error) {
	path := s.Path(taskID)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Save marshals cfg and writes it atomically to the config file for the given
// task ID, creating the directory if needed. The file is written 0666 so the
// runner ships it and a non-root agent can rewrite it. The atomic write ensures
// a container killed mid-write leaves the previous config intact.
func (s ConfigStore) Save(taskID int64, cfg *Config) error {
	path := s.Path(taskID)
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return atomicio.WriteFile(path, data, 0o666)
}

type Config struct {
	// Runner-provided
	Type         string               `json:"type,omitempty"`
	Cwd          string               `json:"cwd,omitempty"`
	Prompt       string               `json:"prompt,omitempty"`
	Verbose      bool                 `json:"verbose,omitempty"`
	McpServers   map[string]McpServer `json:"mcp_servers,omitempty"`
	Commands     []string             `json:"commands,omitempty"`
	Capabilities []string             `json:"capabilities,omitempty"`
	Claude       *ClaudeOptions       `json:"claude,omitempty"`
	Codex        *CodexOptions        `json:"codex,omitempty"`
	Copilot      *CopilotOptions      `json:"copilot,omitempty"`
	Cursor       *CursorOptions       `json:"cursor,omitempty"`
	Sloppy       *SloppyOptions       `json:"sloppy,omitempty"`
	Dummy        *DummyOptions        `json:"dummy,omitempty"`

	// Agent-managed state
	SetupCommandsCompleted int    `json:"setup_commands_completed,omitempty"`
	Started                bool   `json:"started,omitempty"`
	NextEventToken         string `json:"next_event_token,omitempty"` // pagination cursor for events already delivered to the agent
}

type McpServer struct {
	Type    string            `json:"type" yaml:"type"`
	URL     string            `json:"url,omitempty" yaml:"url"`
	Headers map[string]string `json:"headers,omitempty" yaml:"headers"`
	Command string            `json:"command,omitempty" yaml:"command"`
	Args    []string          `json:"args,omitempty" yaml:"args"`
	Env     map[string]string `json:"env,omitempty" yaml:"env"`
}

func (m *McpServer) Validate() error {
	if m.Type == "" {
		return fmt.Errorf("type is required")
	}
	switch m.Type {
	case "stdio":
		if m.Command == "" {
			return fmt.Errorf("command is required for stdio")
		}
	case "http", "sse":
		if m.URL == "" {
			return fmt.Errorf("url is required for %s", m.Type)
		}
	default:
		return fmt.Errorf("unknown type: %s", m.Type)
	}
	return nil
}
