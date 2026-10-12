package backend

import (
	"context"

	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner/workspace"
)

// BackendV2 creates task sandboxes on a concrete runtime and reopens recorded
// ones. It never speaks the driver protocol: the runner does, over the client
// SandboxV2.Dial returns. See proposals/draft/driver-server.md, "Backend
// interface".
//
// It sits beside Backend until the runner moves onto it, which removes Backend
// and the Sandbox view and renames BackendV2 and SandboxV2 to take their names.
type BackendV2 interface {
	ValidateWorkspace(ws *workspace.Workspace) error

	// Create provisions a sandbox for spec without starting it, and returns as
	// soon as the platform has assigned it an identity. On error no sandbox
	// exists: a backend whose provisioning fails after the platform assigned an
	// identity (Docker's binary copy) destroys what it created, on a context
	// of its own, before returning the error.
	Create(ctx context.Context, spec *Spec) (SandboxV2, error)

	// Open rehydrates a sandbox from a handle that SandboxV2.Handle returned.
	// It does no I/O and does not check that the sandbox still exists; a gone
	// sandbox shows up on the returned SandboxV2's methods. It fails only for a
	// handle this backend cannot decode (another type, malformed Data).
	Open(h Handle) (SandboxV2, error)

	Close() error
}

// RunEnver is implemented by a BackendV2 whose sandboxes have no environment of
// their own to carry the workspace's (a Lambda VM is run with none). The runner
// adds what RunEnv returns to the environment of every run it starts, under the
// run's own.
type RunEnver interface {
	RunEnv(ws *workspace.Workspace) map[string]string
}

// SandboxV2 is a reference to one provisioned sandbox, in whatever state it is
// in: created, running, parked or gone. It holds the per-sandbox state its
// backend needs (the Docker container ID, the Lambda endpoint and cached proxy
// token). Its methods are safe for concurrent use.
//
// A SandboxV2 can be stale: the platform may have destroyed the sandbox since
// Create or Open. Every method handles that: Status reports StatusGone, Start
// and Park return ErrGone, and Destroy returns nil.
type SandboxV2 interface {
	// Handle identifies the sandbox for taskstate/. It is fixed at Create and
	// never changes over the sandbox's life.
	Handle() Handle

	// Start boots a created sandbox or resumes a parked one, and blocks until
	// the platform reports it running. Starting a running sandbox is a no-op. A
	// gone sandbox returns ErrGone; a sandbox in a transitional state
	// (SUSPENDING, pausing) returns a plain error. Start never creates a
	// sandbox.
	Start(ctx context.Context) error

	// Dial returns a client for the sandbox's driver server. Every call it
	// makes carries jwt as a bearer token, plus any platform credentials
	// (Lambda's port-scoped proxy token). A driver with no runner key ignores
	// the JWT. Dial does not wait for the driver; readiness is the runner's
	// Status polling.
	Dial(ctx context.Context, jwt string) (driverv1connect.DriverServiceClient, error)

	// Status reports the platform's view of the sandbox: StatusUp,
	// StatusParked or StatusGone. Created but never started, and transitional
	// states (SUSPENDING, pausing), report StatusParked.
	Status(ctx context.Context) (Status, error)

	// Park stops compute while preserving the sandbox, blocking until the
	// platform reports the stable parked state. Parking a parked sandbox is a
	// no-op.
	Park(ctx context.Context) error

	// Destroy deletes the sandbox. Destroying a gone sandbox is not an error.
	Destroy(ctx context.Context) error
}

// Status is the platform's view of a sandbox, as SandboxV2.Status reports it.
type Status int

const (
	// StatusUnknown is returned alongside an error only.
	StatusUnknown Status = iota
	// StatusUp means the platform reports the sandbox running. Its driver may
	// not answer yet.
	StatusUp
	// StatusParked means the sandbox exists but its compute is stopped, or is
	// stopping or not yet started: created, stopped, SUSPENDING, SUSPENDED.
	StatusParked
	// StatusGone means the sandbox no longer exists, or is being torn down:
	// there is nothing to start or park.
	StatusGone
)

func (s Status) String() string {
	switch s {
	case StatusUp:
		return "up"
	case StatusParked:
		return "parked"
	case StatusGone:
		return "gone"
	default:
		return "unknown"
	}
}
