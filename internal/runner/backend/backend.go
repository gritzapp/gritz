// Package backend defines the interface between the runner's orchestration
// logic and the runtime that hosts task sandboxes (Docker today; Kubernetes,
// ECS, Firecracker, AgentCore later). See
// proposals/accepted/runner-backend-interface.md and
// proposals/draft/shared-runner-taskstate.md.
//
// Backends do runtime work only — launch, probe, signal, destroy, watch — over
// opaque Handles. They never persist, discover, or reconcile task→sandbox
// state: the runner owns the taskstate store and is the only writer. It
// translates between a backend.Handle and a taskstate.Record at the boundary.
package backend

//go:generate go tool moq -out backend_moq.go . Backend

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gritzapp/gritz/internal/runner/workspace"
)

// ErrGone means the sandbox a reuse handle refers to no longer exists. Launch
// returns it instead of creating a fresh sandbox, since the task is bound 1:1 to
// the sandbox its handle references.
var ErrGone = errors.New("backend: sandbox is gone")

// ExitCode reports why a sandbox stopped. 0 means the driver reported its own
// terminal outcome to the server (no runner event owed); non-zero means the report
// was lost and the runner must emit "failed" on the driver's behalf.
type ExitCode int

// ExitLost is the sentinel ExitCode for the report-lost case: the driver's
// terminal report never reached the server (stream gone + control plane terminal,
// container removed, VM reaped), so the runner emits "failed" on its behalf.
const ExitLost ExitCode = -1

// BinaryPath is the path inside the sandbox where backends provision the
// gritz driver binary. Spec.Cmd and the injected MCP server reference it.
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
	TaskID    int64
	Workspace *workspace.Workspace
	Cmd       []string // driver invocation
	Env       []string // GRITZ_TASK_ID / GRITZ_TOKEN / GRITZ_SERVER
	Files     []File   // agent config; the driver binary is the backend's job
}

// State is the orchestrator's view of a sandbox's lifecycle. Runtime states
// that don't map to running or exited (e.g. a created-but-never-started
// container) are reported as StateUnknown and ignored by the orchestrator.
type State int

const (
	StateUnknown State = iota
	StateRunning
	StateExited // husk preserved: stopped container / SUSPENDED VM
	StateGone   // removed container / TERMINATED VM — nothing to resume or destroy
)

// Sandbox is a point-in-time view of a task's sandbox. The runner composes it
// from a taskstate record and a backend Probe; it is not produced by the
// backend.
type Sandbox struct {
	TaskID int64
	State  State
}

// Backend runs task sandboxes on a concrete runtime. Every method does runtime
// work only and takes/returns Handles; none of them touch the taskstate store.
type Backend interface {
	// ValidateWorkspace reports whether the workspace's runtime config is
	// usable by this backend (Docker checks the container: section).
	ValidateWorkspace(ws *workspace.Workspace) error

	// Launch ensures a sandbox exists for spec and starts it, returning the
	// Handle the RUNNER persists. If reuse is nil a fresh sandbox is created.
	// If reuse is non-nil the backend adopts the exact sandbox it identifies
	// (preserving its filesystem) — it NEVER creates a fresh one on the reuse
	// path, since a task is bound 1:1 to the sandbox its handle references. If
	// that sandbox is gone, Launch returns ErrGone. The backend persists nothing.
	Launch(ctx context.Context, spec *Spec, reuse *Handle) (Handle, error)

	// Probe reports the liveness of a single handle.
	Probe(ctx context.Context, h Handle) (State, error)

	// Signal gracefully stops the sandbox identified by h (SIGTERM → SIGKILL),
	// reporting whether a running sandbox was signalled — in that case the
	// driver owns the terminal event report.
	Signal(ctx context.Context, h Handle) (signalled bool, err error)

	// Destroy deletes the sandbox identified by h. It is idempotent: destroying
	// an absent sandbox is not an error.
	Destroy(ctx context.Context, h Handle) error

	// Wait blocks until the sandbox identified by h reaches a terminal outcome,
	// returning exactly once. It swallows transient failures internally (SSE
	// drops, token re-mint, reconnect/arbitrate, backoff). For Lambda it performs
	// the suspend-on-driver-exit before returning. It is safe to call on a sandbox
	// this process did not start (re-attach after a restart).
	//
	// Its return has exactly three shapes:
	//   - clean driver exit → (code, nil);
	//   - report lost → (ExitLost, nil) (stream gone + control plane terminal,
	//     container removed, VM reaped); a rehydrated-already-dead sandbox
	//     returns this immediately;
	//   - runner shutting down → (_, ctx.Err()) with errors.Is(err,
	//     context.Canceled). The sandbox stays alive for next-boot rehydration;
	//     the caller must NOT emit "failed".
	// A well-behaved backend returns a non-nil error ONLY for the last case.
	Wait(ctx context.Context, h Handle) (ExitCode, error)

	Close() error
}
