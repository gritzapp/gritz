// Package fake is an in-memory backend.Backend for tests. Its sandboxes follow
// the state machine documented on backend.Sandbox and nothing else of a
// platform: no transitional states, no images, no files. Tests arrange
// sandboxes with Add, drive them through the backend.Backend and
// backend.Sandbox interfaces, and observe them with Snapshot and Wait. See
// proposals/draft/fake-runner-backend.md.
//
// Production code never imports it.
package fake

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"

	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/workspace"
)

// DefaultType is the handle type of a Backend's sandboxes when Options.Type is
// empty.
const DefaultType = "fake"

// State is a sandbox's state in the fake.
type State int

const (
	// Created is a sandbox Create made and nothing has started yet.
	Created State = iota + 1
	// Up is a running sandbox.
	Up
	// Parked is a sandbox whose compute is stopped.
	Parked
	// Gone is a sandbox that no longer exists; it is terminal.
	Gone
)

// String returns the state's name.
func (s State) String() string {
	switch s {
	case Created:
		return "created"
	case Up:
		return "up"
	case Parked:
		return "parked"
	case Gone:
		return "gone"
	default:
		return fmt.Sprintf("State(%d)", int(s))
	}
}

// Options configures a Backend.
type Options struct {
	// Type is the handle type of the sandboxes; defaults to DefaultType.
	Type string
}

// Backend is an in-memory backend.Backend whose sandboxes follow the
// backend.Sandbox state machine. It is safe for concurrent use.
//
// A sandbox handle holds only its ID: all state lives in the Backend's table,
// so every handle to one ID sees and changes the same sandbox. IDs are never
// reused, and a gone sandbox stays in the table.
type Backend struct {
	typ string

	mu sync.Mutex
	// sandboxes is every sandbox the fake has held, by ID; order is their
	// creation order.
	sandboxes map[string]*Info
	order     []string
	specs     []backend.Spec
	destroyed []string
	// next is the number of the next ID Create tries.
	next int
	// changed is closed and replaced on every change to the table.
	changed chan struct{}
}

var _ backend.Backend = (*Backend)(nil)

// New returns a Backend with no sandboxes.
func New(opts Options) *Backend {
	typ := opts.Type
	if typ == "" {
		typ = DefaultType
	}
	return &Backend{
		typ:       typ,
		sandboxes: map[string]*Info{},
		next:      1,
		changed:   make(chan struct{}),
	}
}

// ValidateWorkspace accepts every workspace.
func (b *Backend) ValidateWorkspace(*workspace.Workspace) error {
	return nil
}

// Create records spec and adds a created sandbox for it, named c1, c2, ... in
// call order, skipping IDs Add already used.
func (b *Backend) Create(_ context.Context, spec *backend.Spec) (backend.Sandbox, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.specs = append(b.specs, *spec.Clone())
	id := fmt.Sprintf("c%d", b.next)
	for b.sandboxes[id] != nil {
		b.next++
		id = fmt.Sprintf("c%d", b.next)
	}
	b.next++
	b.addLocked(&Info{
		Handle: b.handle(id),
		TaskID: spec.TaskID,
		State:  Created,
		Spec:   spec.Clone(),
	})
	return &sandbox{b: b, id: id}, nil
}

// Open returns the sandbox for h, which shares state with every other handle
// to it. Like every real backend's, it does not check that the sandbox exists:
// a handle the fake never had behaves as gone. It fails only for a handle of
// another type or with no ID. h.Data is ignored.
func (b *Backend) Open(h backend.Handle) (backend.Sandbox, error) {
	if h.Type != b.typ {
		return nil, fmt.Errorf("handle type %q is not %q", h.Type, b.typ)
	}
	if h.ID == "" {
		return nil, errors.New("handle has no sandbox id")
	}
	return &sandbox{b: b, id: h.ID}, nil
}

// Close releases the Backend. Its sandboxes keep working.
func (b *Backend) Close() error {
	return nil
}

// Add puts a sandbox with id in state into the fake, as a previous runner
// process would have left it, and returns its handle. It panics if id is empty
// or already used, or state is not a State: those are test bugs.
func (b *Backend) Add(id string, state State) backend.Handle {
	if id == "" {
		panic("fake: Add with an empty id")
	}
	if state < Created || state > Gone {
		panic(fmt.Sprintf("fake: Add(%q) with invalid %v", id, state))
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sandboxes[id] != nil {
		panic(fmt.Sprintf("fake: Add(%q): id already used", id))
	}
	h := b.handle(id)
	b.addLocked(&Info{Handle: h, State: state})
	return h
}

// Snapshot returns a copy of the fake's current state.
func (b *Backend) Snapshot() Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snapshotLocked()
}

// Wait blocks until cond holds for the fake's state, and returns that state.
// It checks cond once now and again after every change to the fake, without
// polling. If ctx is done first, it returns the last state it checked and
// ctx's error.
func (b *Backend) Wait(ctx context.Context, cond func(Snapshot) bool) (Snapshot, error) {
	for {
		b.mu.Lock()
		snap := b.snapshotLocked()
		changed := b.changed
		b.mu.Unlock()
		if cond(snap) {
			return snap, nil
		}
		select {
		case <-ctx.Done():
			return snap, context.Cause(ctx)
		case <-changed:
		}
	}
}

// WaitState blocks until sandbox id is in state, or ctx is done.
func (b *Backend) WaitState(ctx context.Context, id string, state State) error {
	_, err := b.Wait(ctx, func(s Snapshot) bool {
		sbx, ok := s.Sandbox(id)
		return ok && sbx.State == state
	})
	if err != nil {
		return fmt.Errorf("sandbox %s did not reach %v: %w", id, state, err)
	}
	return nil
}

func (b *Backend) handle(id string) backend.Handle {
	return backend.Handle{Type: b.typ, ID: id}
}

// addLocked adds sbx to the table. b.mu must be held.
func (b *Backend) addLocked(sbx *Info) {
	b.sandboxes[sbx.Handle.ID] = sbx
	b.order = append(b.order, sbx.Handle.ID)
	b.changedLocked()
}

// changedLocked wakes every Wait. b.mu must be held.
func (b *Backend) changedLocked() {
	close(b.changed)
	b.changed = make(chan struct{})
}

// snapshotLocked copies the table. b.mu must be held.
func (b *Backend) snapshotLocked() Snapshot {
	snap := Snapshot{Destroyed: slices.Clone(b.destroyed)}
	for _, id := range b.order {
		sbx := *b.sandboxes[id]
		if sbx.Spec != nil {
			sbx.Spec = sbx.Spec.Clone()
		}
		snap.Sandboxes = append(snap.Sandboxes, sbx)
	}
	for i := range b.specs {
		snap.Specs = append(snap.Specs, *b.specs[i].Clone())
	}
	return snap
}

// sandbox is a handle to one sandbox in b's table. It holds nothing but the
// ID, so it is safe for concurrent use and every handle to an ID shares state.
type sandbox struct {
	b  *Backend
	id string
}

func (s *sandbox) Handle() backend.Handle {
	return s.b.handle(s.id)
}

// Start moves a created or parked sandbox to up. Starting an up sandbox is a
// no-op; a gone one returns backend.ErrGone.
func (s *sandbox) Start(context.Context) error {
	return s.update(func(sbx *Info) error {
		if sbx.State == Gone {
			return backend.ErrGone
		}
		sbx.State = Up
		sbx.Starts++
		return nil
	})
}

// Park moves an up sandbox to parked. Parking a created or parked sandbox is
// a no-op that leaves its state as it is; a gone one returns backend.ErrGone.
func (s *sandbox) Park(context.Context) error {
	return s.update(func(sbx *Info) error {
		switch sbx.State {
		case Gone:
			return backend.ErrGone
		case Up:
			sbx.State = Parked
		}
		sbx.Parks++
		return nil
	})
}

// Destroy moves the sandbox to gone. Destroying a gone sandbox is a no-op.
func (s *sandbox) Destroy(context.Context) error {
	err := s.update(func(sbx *Info) error {
		if sbx.State != Gone {
			sbx.State = Gone
			s.b.destroyed = append(s.b.destroyed, s.id)
		}
		sbx.Destroys++
		return nil
	})
	if errors.Is(err, backend.ErrGone) {
		// A handle the fake never had is gone already.
		return nil
	}
	return err
}

// Status reports a created or parked sandbox as parked, an up one as up, and
// a gone one, or one the fake never had, as gone.
func (s *sandbox) Status(context.Context) (backend.Status, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	sbx := s.b.sandboxes[s.id]
	if sbx == nil {
		return backend.StatusGone, nil
	}
	switch sbx.State {
	case Up:
		return backend.StatusUp, nil
	case Created, Parked:
		return backend.StatusParked, nil
	default:
		return backend.StatusGone, nil
	}
}

// Dial returns a client for the sandbox's driver, whatever its state. The
// sandbox has no driver, so no call on the client is ever answered: each
// fails as Unavailable, as for a driver that is not listening.
func (s *sandbox) Dial(context.Context, string) (driverv1connect.DriverServiceClient, error) {
	// A sandbox the fake never had has no counters; Dial succeeds anyway.
	_ = s.update(func(sbx *Info) error {
		sbx.Dials++
		return nil
	})
	httpClient := &http.Client{Transport: noDriver{}}
	return driverv1connect.NewDriverServiceClient(httpClient, "http://driver"), nil
}

// update applies fn to the sandbox under the lock, and wakes every Wait if fn
// succeeds. A sandbox the fake never had returns backend.ErrGone without
// calling fn.
func (s *sandbox) update(fn func(sbx *Info) error) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	sbx := s.b.sandboxes[s.id]
	if sbx == nil {
		return backend.ErrGone
	}
	if err := fn(sbx); err != nil {
		return err
	}
	s.b.changedLocked()
	return nil
}

// noDriver is the transport of a sandbox with no driver: every request fails.
type noDriver struct{}

func (noDriver) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("fake: sandbox has no driver")
}

// Snapshot is a copy of the fake's state at one point.
type Snapshot struct {
	// Sandboxes is every sandbox the fake has held, in creation order.
	Sandboxes []Info
	// Specs is every spec passed to Create, in call order.
	Specs []backend.Spec
	// Destroyed is the IDs Destroy moved to gone, in order.
	Destroyed []string
}

// Sandbox returns the sandbox with id.
func (s Snapshot) Sandbox(id string) (Info, bool) {
	for _, sbx := range s.Sandboxes {
		if sbx.Handle.ID == id {
			return sbx, true
		}
	}
	return Info{}, false
}

// Live returns the sandboxes that are not gone, in creation order.
func (s Snapshot) Live() []Info {
	var live []Info
	for _, sbx := range s.Sandboxes {
		if sbx.State != Gone {
			live = append(live, sbx)
		}
	}
	return live
}

// Info is one sandbox in a Snapshot.
type Info struct {
	Handle backend.Handle
	// TaskID is Spec's task, or 0 for a sandbox Add made.
	TaskID int64
	State  State
	// Spec is the spec the sandbox was created from, or nil for Add.
	Spec *backend.Spec
	// Starts, Parks, Destroys and Dials count the calls that succeeded, no-ops
	// included. A call that returned backend.ErrGone is not counted.
	Starts, Parks, Destroys, Dials int
}
