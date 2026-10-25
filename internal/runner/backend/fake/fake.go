// Package fake is an in-memory backend.Backend for tests. Its sandboxes follow
// the state machine documented on backend.Sandbox and nothing else of a
// platform: no transitional states, no images, no files. Tests arrange
// sandboxes with Add, Set, SetDriverReady, FailNext and Drop, reach their drivers through the
// clients Options.Dial gives, drive them through the backend.Backend and
// backend.Sandbox interfaces, and observe them with Snapshot and Wait. It
// implements backend.RunEnver as well, with the environment Options.RunEnv
// gives, and records the spec of every run its sandboxes' drivers are asked to
// start. See
// proposals/implemented/fake-runner-backend.md.
//
// Production code never imports it.
package fake

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"connectrpc.com/connect"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"google.golang.org/protobuf/proto"
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

// Op names a sandbox or backend operation for FailNext.
type Op int

const (
	// OpCreate is Backend.Create.
	OpCreate Op = iota + 1
	// OpOpen is Backend.Open.
	OpOpen
	// OpStart is Sandbox.Start.
	OpStart
	// OpPark is Sandbox.Park.
	OpPark
	// OpStatus is Sandbox.Status.
	OpStatus
	// OpDestroy is Sandbox.Destroy.
	OpDestroy
	// OpDial is Sandbox.Dial.
	OpDial
)

// String returns the op's name.
func (o Op) String() string {
	switch o {
	case OpCreate:
		return "create"
	case OpOpen:
		return "open"
	case OpStart:
		return "start"
	case OpPark:
		return "park"
	case OpStatus:
		return "status"
	case OpDestroy:
		return "destroy"
	case OpDial:
		return "dial"
	default:
		return fmt.Sprintf("Op(%d)", int(o))
	}
}

// Options configures a Backend.
type Options struct {
	// Type is the handle type of the sandboxes; defaults to DefaultType.
	Type string
	// Dial returns the test's client to the driver of the sandbox with
	// handle h. The Backend calls it on every Dial, without its lock held,
	// and wraps the client so the driver answers only while the sandbox is
	// up. nil, or a Dial returning nil, means the sandbox's driver never
	// answers.
	Dial func(h backend.Handle) driverv1connect.DriverServiceClient
	// RunEnv returns the environment RunEnv gives runs of a workspace's
	// sandboxes; nil means none.
	RunEnv func(ws *workspace.Workspace) map[string]string
	// DriverNotReady makes every sandbox start with its driver not ready, as
	// a driver that is not listening yet, until SetDriverReady makes it
	// ready.
	DriverNotReady bool
}

// Backend is an in-memory backend.Backend whose sandboxes follow the
// backend.Sandbox state machine. It is safe for concurrent use.
//
// A sandbox handle holds only its ID: all state lives in the Backend's table,
// so every handle to one ID sees and changes the same sandbox. IDs are never
// reused, and a gone sandbox stays in the table.
type Backend struct {
	typ    string
	dial   func(backend.Handle) driverv1connect.DriverServiceClient
	runEnv func(*workspace.Workspace) map[string]string
	// driverNotReady is Options.DriverNotReady.
	driverNotReady bool

	mu sync.Mutex
	// sandboxes is every sandbox the fake has held, by ID; order is their
	// creation order.
	sandboxes map[string]*Info
	order     []string
	specs     []backend.Spec
	destroyed []string
	// failures queues the errors FailNext injected, by op and sandbox ID; ""
	// matches any sandbox.
	failures map[failKey][]error
	// next is the number of the next ID Create tries.
	next int
	// changed is closed and replaced on every change to the table.
	changed chan struct{}
	// notReady is the IDs of the sandboxes whose driver is not ready.
	notReady map[string]bool
	// runs cancels the context of every Run call open on a sandbox's
	// driver, by sandbox ID and then by call.
	runs map[string]map[int]context.CancelFunc
	// nextRun is the key of the next Run call in runs.
	nextRun int
	// closed is set by Close; no driver answers after it.
	closed bool
}

var (
	_ backend.Backend  = (*Backend)(nil)
	_ backend.RunEnver = (*Backend)(nil)
)

// New returns a Backend with no sandboxes.
func New(opts Options) *Backend {
	typ := opts.Type
	if typ == "" {
		typ = DefaultType
	}
	return &Backend{
		typ:            typ,
		dial:           opts.Dial,
		runEnv:         opts.RunEnv,
		notReady:       map[string]bool{},
		driverNotReady: opts.DriverNotReady,
		sandboxes:      map[string]*Info{},
		runs:           map[string]map[int]context.CancelFunc{},
		failures:       map[failKey][]error{},
		next:           1,
		changed:        make(chan struct{}),
	}
}

// ValidateWorkspace accepts every workspace.
func (b *Backend) ValidateWorkspace(*workspace.Workspace) error {
	return nil
}

// RunEnv returns Options.RunEnv's environment for ws, or nil without one.
func (b *Backend) RunEnv(ws *workspace.Workspace) map[string]string {
	if b.runEnv == nil {
		return nil
	}
	return b.runEnv(ws)
}

// Create records spec and adds a created sandbox for it, named c1, c2, ... in
// call order, skipping IDs Add already used. A Create that FailNext fails
// records spec and adds no sandbox.
func (b *Backend) Create(_ context.Context, spec *backend.Spec) (backend.Sandbox, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.specs = append(b.specs, *spec.Clone())
	if err := b.failLocked(OpCreate, ""); err != nil {
		b.changedLocked()
		return nil, err
	}
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
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.failLocked(OpOpen, h.ID); err != nil {
		return nil, err
	}
	return &sandbox{b: b, id: h.ID}, nil
}

// Close cancels every Run call open on the sandboxes' drivers. Their state is
// unchanged, but their drivers no longer answer.
func (b *Backend) Close() error {
	b.mu.Lock()
	b.closed = true
	var cancels []context.CancelFunc
	for id := range b.runs {
		cancels = append(cancels, b.cutLocked(id)...)
	}
	b.mu.Unlock()
	cancelAll(cancels)
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

// Set moves sandbox id to state without going through the runner, as the
// platform or an operator would: Gone is a removed container or a terminated
// VM, Parked a container that exited, Up someone starting it. It changes no
// counter, and a Set to Gone is not recorded in Snapshot.Destroyed. It panics
// if id is unknown, state is not a State, or the sandbox is gone and state is
// not: those are test bugs.
func (b *Backend) Set(id string, state State) {
	if state < Created || state > Gone {
		panic(fmt.Sprintf("fake: Set(%q) with invalid %v", id, state))
	}
	b.mu.Lock()
	sbx := b.sandboxes[id]
	if sbx == nil {
		b.mu.Unlock()
		panic(fmt.Sprintf("fake: Set(%q): unknown id", id))
	}
	if sbx.State == Gone && state != Gone {
		b.mu.Unlock()
		panic(fmt.Sprintf("fake: Set(%q, %v): sandbox is gone", id, state))
	}
	from := sbx.State
	sbx.State = state
	b.changedLocked()
	cancels := b.leftUpLocked(id, from)
	b.mu.Unlock()
	cancelAll(cancels)
}

// SetDriverReady makes sandbox id's driver ready or not, without changing
// the sandbox's state. While the sandbox is up and its driver is not ready,
// every call to the driver fails as Unavailable, as for a driver that is not
// listening yet; making it not ready cuts the Run calls open on it, as Drop
// does. Drivers are ready unless Options.DriverNotReady is set. It panics if
// id is unknown: that is a test bug.
func (b *Backend) SetDriverReady(id string, ready bool) {
	b.mu.Lock()
	if b.sandboxes[id] == nil {
		b.mu.Unlock()
		panic(fmt.Sprintf("fake: SetDriverReady(%q): unknown id", id))
	}
	var cancels []context.CancelFunc
	if ready {
		delete(b.notReady, id)
	} else {
		b.notReady[id] = true
		cancels = b.cutLocked(id)
	}
	b.changedLocked()
	b.mu.Unlock()
	cancelAll(cancels)
}

// Drop cancels every Run call open on sandbox id's driver, as a network blip
// would, without changing its state. Those streams fail; the next calls go
// through again. It panics if id is unknown: that is a test bug.
func (b *Backend) Drop(id string) {
	b.mu.Lock()
	if b.sandboxes[id] == nil {
		b.mu.Unlock()
		panic(fmt.Sprintf("fake: Drop(%q): unknown id", id))
	}
	cancels := b.cutLocked(id)
	b.mu.Unlock()
	cancelAll(cancels)
}

// FailNext makes the next call of op on sandbox id return err, without
// changing its state or counting the call. id "" matches any sandbox, and is
// the only id OpCreate accepts. Failures queue: each FailNext fails one more
// call. A call takes a failure queued for its own sandbox before one queued
// for "". It panics if op is not an Op, err is nil, or op is OpCreate with an
// id: those are test bugs.
func (b *Backend) FailNext(op Op, id string, err error) {
	if op < OpCreate || op > OpDial {
		panic(fmt.Sprintf("fake: FailNext with invalid %v", op))
	}
	if err == nil {
		panic(fmt.Sprintf("fake: FailNext(%v, %q) with a nil error", op, id))
	}
	if op == OpCreate && id != "" {
		panic(fmt.Sprintf("fake: FailNext(%v, %q): Create has no sandbox id", op, id))
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	k := failKey{op: op, id: id}
	b.failures[k] = append(b.failures[k], err)
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

// failLocked takes the next failure FailNext queued for op on sandbox id,
// trying id's own queue before the one for any sandbox. It returns nil if
// there is none. b.mu must be held.
func (b *Backend) failLocked(op Op, id string) error {
	for _, k := range []failKey{{op: op, id: id}, {op: op}} {
		if q := b.failures[k]; len(q) > 0 {
			b.failures[k] = q[1:]
			return q[0]
		}
	}
	return nil
}

// addLocked adds sbx to the table, with its driver not ready if
// Options.DriverNotReady is set. b.mu must be held.
func (b *Backend) addLocked(sbx *Info) {
	b.sandboxes[sbx.Handle.ID] = sbx
	if b.driverNotReady {
		b.notReady[sbx.Handle.ID] = true
	}
	b.order = append(b.order, sbx.Handle.ID)
	b.changedLocked()
}

// leftUpLocked takes the cancel funcs of the Run calls to cut when sandbox id
// moves from state from to its current state: all of its own if it was up and
// is not any more, as stopping a sandbox kills its driver, or none. The caller
// calls them once b.mu is released. b.mu must be held.
func (b *Backend) leftUpLocked(id string, from State) []context.CancelFunc {
	if from != Up || b.sandboxes[id].State == Up {
		return nil
	}
	return b.cutLocked(id)
}

// cutLocked takes the cancel funcs of every Run call open on sandbox id's
// driver. The caller calls them once b.mu is released. b.mu must be held.
func (b *Backend) cutLocked(id string) []context.CancelFunc {
	runs := b.runs[id]
	delete(b.runs, id)
	cancels := make([]context.CancelFunc, 0, len(runs))
	for _, cancel := range runs {
		cancels = append(cancels, cancel)
	}
	return cancels
}

// cancelAll calls every cancel func; b.mu must not be held.
func cancelAll(cancels []context.CancelFunc) {
	for _, cancel := range cancels {
		cancel()
	}
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
		if sbx.RunSpecs != nil {
			specs := make([]*driverv1.RunSpec, len(sbx.RunSpecs))
			for i, spec := range sbx.RunSpecs {
				specs[i] = proto.CloneOf(spec)
			}
			sbx.RunSpecs = specs
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
	return s.update(OpStart, func(sbx *Info) error {
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
	return s.update(OpPark, func(sbx *Info) error {
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
	s.b.mu.Lock()
	if err := s.b.failLocked(OpDestroy, s.id); err != nil {
		s.b.mu.Unlock()
		return err
	}
	sbx := s.b.sandboxes[s.id]
	if sbx == nil {
		// A handle the fake never had is gone already.
		s.b.mu.Unlock()
		return nil
	}
	from := sbx.State
	if sbx.State != Gone {
		sbx.State = Gone
		s.b.destroyed = append(s.b.destroyed, s.id)
	}
	sbx.Destroys++
	s.b.changedLocked()
	cancels := s.b.leftUpLocked(s.id, from)
	s.b.mu.Unlock()
	cancelAll(cancels)
	return nil
}

// Status reports a created or parked sandbox as parked, an up one as up, and
// a gone one, or one the fake never had, as gone.
func (s *sandbox) Status(context.Context) (backend.Status, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if err := s.b.failLocked(OpStatus, s.id); err != nil {
		return backend.StatusUnknown, err
	}
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
// driver is the client Options.Dial gives for the sandbox, wrapped so it
// answers while the sandbox is up and fails every call as Unavailable
// otherwise. A sandbox with no driver never answers: each call fails as
// Unavailable, as for a driver that is not listening. jwt is ignored.
func (s *sandbox) Dial(context.Context, string) (driverv1connect.DriverServiceClient, error) {
	s.b.mu.Lock()
	if err := s.b.failLocked(OpDial, s.id); err != nil {
		s.b.mu.Unlock()
		return nil, err
	}
	// A sandbox the fake never had has no counters; Dial succeeds anyway.
	if sbx := s.b.sandboxes[s.id]; sbx != nil {
		sbx.Dials++
		s.b.changedLocked()
	}
	s.b.mu.Unlock()
	c := &client{b: s.b, id: s.id}
	if s.b.dial != nil {
		c.driver = s.b.dial(s.Handle())
	}
	return c, nil
}

// update applies fn, the body of op, to the sandbox under the lock, and wakes
// every Wait if fn succeeds. A failure FailNext queued for op is returned
// without calling fn; a sandbox the fake never had returns backend.ErrGone.
//
// If fn moves the sandbox out of up, the Run calls open on its driver are
// cancelled once the lock is released.
func (s *sandbox) update(op Op, fn func(sbx *Info) error) error {
	cancels, err := s.updateLocked(op, fn)
	cancelAll(cancels)
	return err
}

func (s *sandbox) updateLocked(op Op, fn func(sbx *Info) error) ([]context.CancelFunc, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if err := s.b.failLocked(op, s.id); err != nil {
		return nil, err
	}
	sbx := s.b.sandboxes[s.id]
	if sbx == nil {
		return nil, backend.ErrGone
	}
	from := sbx.State
	if err := fn(sbx); err != nil {
		return nil, err
	}
	s.b.changedLocked()
	return s.b.leftUpLocked(s.id, from), nil
}

// failKey is a FailNext queue: an op on one sandbox, or on any if id is "".
type failKey struct {
	op Op
	id string
}

// client is the client Dial returns: it calls through to the test's client to
// the sandbox's driver while the sandbox is up, and fails as Unavailable
// otherwise.
type client struct {
	b  *Backend
	id string
	// driver is the test's client to the sandbox's driver, or nil if it has
	// none.
	driver driverv1connect.DriverServiceClient
}

// checkLocked returns the error of a call to the sandbox's driver that cannot reach
// it, or nil. b.mu must be held.
func (c *client) checkLocked() error {
	if c.b.closed {
		return connect.NewError(connect.CodeUnavailable, errors.New("fake: backend is closed"))
	}
	if sbx := c.b.sandboxes[c.id]; sbx == nil || sbx.State != Up {
		return connect.NewError(connect.CodeUnavailable, errors.New("fake: sandbox is not up"))
	}
	if c.driver == nil {
		return connect.NewError(connect.CodeUnavailable, errors.New("fake: sandbox has no driver"))
	}
	if c.b.notReady[c.id] {
		return connect.NewError(connect.CodeUnavailable, errors.New("fake: driver is not ready"))
	}
	return nil
}

func (c *client) Status(ctx context.Context, req *driverv1.StatusRequest) (*driverv1.StatusResponse, error) {
	c.b.mu.Lock()
	err := c.checkLocked()
	c.b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return c.driver.Status(ctx, req)
}

func (c *client) Stop(ctx context.Context, req *driverv1.StopRequest) (*driverv1.StopResponse, error) {
	c.b.mu.Lock()
	err := c.checkLocked()
	c.b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return c.driver.Stop(ctx, req)
}

// Run counts the call in the sandbox's Info.Runs and records the spec of a
// request that has one in its Info.RunSpecs, then calls through on a context
// the fake cancels when the sandbox leaves up, on Drop, when its driver is
// made not ready, and on Close. A stream it cuts fails as Canceled. A call is
// counted only once its context is registered, so a test that waits for
// Runs >= n can then cut the n-th stream.
func (c *client) Run(ctx context.Context, req *driverv1.RunRequest) (*connect.ServerStreamForClient[driverv1.RunResponse], error) {
	c.b.mu.Lock()
	if err := c.checkLocked(); err != nil {
		c.b.mu.Unlock()
		return nil, err
	}
	sbx := c.b.sandboxes[c.id]
	sbx.Runs++
	if spec := req.GetSpec(); spec != nil {
		sbx.RunSpecs = append(sbx.RunSpecs, proto.CloneOf(spec))
	}
	c.b.changedLocked()
	runCtx, cancel := context.WithCancel(ctx)
	key := c.b.nextRun
	c.b.nextRun++
	if c.b.runs[c.id] == nil {
		c.b.runs[c.id] = map[int]context.CancelFunc{}
	}
	c.b.runs[c.id][key] = cancel
	c.b.mu.Unlock()
	// Forget the call once its context is done, by the caller or a cut. The
	// stream it opens may end well before that: ServerStreamForClient is a
	// concrete type, so the fake cannot see it close. A caller that opens
	// stream after stream on one long-lived context, as attach does on a
	// run's, leaves one entry per call until the context is done. That is
	// harmless in a test: the entries are small, and cancelling a context
	// that is already finished with does nothing.
	context.AfterFunc(runCtx, func() {
		c.b.mu.Lock()
		defer c.b.mu.Unlock()
		delete(c.b.runs[c.id], key)
	})
	return c.driver.Run(runCtx, req)
}

// Snapshot is a copy of the fake's state at one point.
type Snapshot struct {
	// Sandboxes is every sandbox the fake has held, in creation order.
	Sandboxes []Info
	// Specs is every spec passed to Create, in call order, including those
	// of calls that failed.
	Specs []backend.Spec
	// Destroyed is the IDs Destroy moved to gone, in order. A Set to Gone is
	// not in it.
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
	// included. A call that returned backend.ErrGone, or that FailNext
	// failed, is not counted.
	Starts, Parks, Destroys, Dials int
	// Runs counts the Run calls on the sandbox's driver that went through:
	// made while it was up and its driver ready, and so cut by Drop, by
	// leaving up and by Close.
	Runs int
	// RunSpecs is the spec of every Run request that has one, a request to
	// start a run, that the sandbox's driver was sent, in order. A request
	// made while the sandbox was not up never reached its driver, and is not
	// in it.
	RunSpecs []*driverv1.RunSpec
}
