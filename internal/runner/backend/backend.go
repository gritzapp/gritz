// Package backend defines the interface between the runner's orchestration
// logic and the runtime that hosts task sandboxes (Docker today; Kubernetes,
// ECS, Firecracker, AgentCore later). See
// proposals/accepted/runner-backend-interface.md and
// proposals/draft/shared-runner-taskstate.md.
//
// Backends do runtime work only — create, start, park, destroy — over opaque
// Handles. They never persist, discover, or reconcile task→sandbox
// state: the runner owns the taskstate store and is the only writer. It
// translates between a backend.Handle and a taskstate.Record at the boundary.
package backend

//go:generate go tool moq -out backend_moq.go . BackendV2 SandboxV2

import (
	"encoding/json"
	"errors"

	"github.com/gritzapp/gritz/internal/runner/workspace"
)

// ErrGone means a sandbox no longer exists: SandboxV2's Start and Park return
// it for a sandbox that is gone.
var ErrGone = errors.New("backend: sandbox is gone")

// BinaryPath is the path inside the sandbox where backends provision the
// gritz driver binary. The sandbox command and the injected MCP server
// reference it.
const BinaryPath = "/usr/local/bin/gritz"

// Handle identifies a backend's sandbox. ID is the index key (a container id,
// an AWS microVM id, ...) — unique and stable for the sandbox's lifetime — and
// is the legitimate handle the runner persists and reverse-indexes. Data is
// backend-defined (whatever the backend needs for cleanup but not for
// identity) and is never decoded by the store or the runner.
type Handle struct {
	// Type is the backend that produced this handle ("docker", ...).
	// Informational only: persisted into the record, never read for logic.
	Type string          `json:"type"`
	ID   string          `json:"id"`
	Data json.RawMessage `json:"data,omitempty"`
}

// File is a file provisioned into the sandbox before it starts.
type File struct {
	Path string // absolute path inside the sandbox
	Data []byte
	Mode int64
	Dir  bool // if true, create a directory entry (Data is ignored)
}

// Spec describes the sandbox for a single task. The orchestrator owns
// everything in it; backends interpret the workspace's runtime-specific
// config section (container: for Docker) and provision the rest.
type Spec struct {
	TaskID int64
	// Version is the task version this run executes. Backends that start runs
	// over the driver protocol key them on it.
	Version   int64
	Workspace *workspace.Workspace
	// ServerURL is the gritz server the driver reports to.
	ServerURL string
	// Token is the task token the driver calls ServerURL with. The driver
	// masks it in the log it ships.
	Token string
	// Env is the run's environment, on top of the sandbox's own: what the setup
	// commands and the agent CLI see.
	Env map[string]string
	// Secrets are the workspace's declared secrets, by name. They are added to
	// the run's environment and masked in the log the driver ships.
	Secrets map[string]string
	Files   []File // agent config; the driver binary is the backend's job
}
