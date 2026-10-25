// Package fake is an in-memory backend.Backend for tests. Its sandboxes follow
// the state machine documented on backend.Sandbox and nothing else of a
// platform: no transitional states, no images, no files. Tests arrange
// sandboxes with Add, Set, FailNext and Drop, serve their drivers through
// Options.Driver, drive them through the backend.Backend and backend.Sandbox
// interfaces, and observe them with Snapshot and Wait. It implements
// backend.RunEnver as well, with the environment Options.RunEnv gives, and
// records the spec of every run its sandboxes' drivers are asked to start. See
// proposals/draft/fake-runner-backend.md.
//
// Production code never imports it.
package fake

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"

	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/workspace"
	"google.golang.org/protobuf/encoding/protojson"
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
	default:
		return fmt.Sprintf("Op(%d)", int(o))
	}
}

// Options configures a Backend.
type Options struct {
	// Type is the handle type of the sandboxes; defaults to DefaultType.
	Type string
	// Driver returns the handler serving the driver.v1 API of the sandbox
	// with handle h. The Backend calls it once per sandbox, on its first
	// Dial, without its lock held. nil, or a Driver returning nil, means the
	// sandbox's driver never answers.
	Driver func(h backend.Handle) http.Handler
	// RunEnv returns the environment RunEnv gives runs of a workspace's
	// sandboxes; nil means none.
	RunEnv func(ws *workspace.Workspace) map[string]string
}

// Backend is an in-memory backend.Backend whose sandboxes follow the
// backend.Sandbox state machine. It is safe for concurrent use.
//
// A sandbox handle holds only its ID: all state lives in the Backend's table,
// so every handle to one ID sees and changes the same sandbox. IDs are never
// reused, and a gone sandbox stays in the table.
type Backend struct {
	typ       string
	newDriver func(backend.Handle) http.Handler
	runEnv    func(*workspace.Workspace) map[string]string

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
	// drivers is the driver of every sandbox that was dialed, by ID.
	drivers map[string]*driver
	// closed is set by Close; no driver is started after it.
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
		typ:       typ,
		newDriver: opts.Driver,
		runEnv:    opts.RunEnv,
		sandboxes: map[string]*Info{},
		drivers:   map[string]*driver{},
		failures:  map[failKey][]error{},
		next:      1,
		changed:   make(chan struct{}),
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

// Close closes the sandboxes' driver servers, and the runner's connections
// to them. Their state is unchanged, but their drivers no longer answer, and
// sandboxes dialed for the first time after Close get none.
func (b *Backend) Close() error {
	b.mu.Lock()
	b.closed = true
	drivers := b.drivers
	b.drivers = map[string]*driver{}
	b.mu.Unlock()
	for _, d := range drivers {
		if srv := d.server(); srv != nil {
			// Server.Close waits for the requests in flight; closing
			// their connections first ends the open streams.
			srv.CloseClientConnections()
			srv.Close()
		}
	}
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
	d := b.leftUpLocked(id, from)
	b.mu.Unlock()
	d.drop()
}

// Drop closes every connection the runner has open to sandbox id's driver,
// as a network blip would, without changing its state. Calls on those
// connections fail; the next ones connect again. It panics if id is
// unknown: that is a test bug.
func (b *Backend) Drop(id string) {
	b.mu.Lock()
	if b.sandboxes[id] == nil {
		b.mu.Unlock()
		panic(fmt.Sprintf("fake: Drop(%q): unknown id", id))
	}
	d := b.drivers[id]
	b.mu.Unlock()
	d.drop()
}

// FailNext makes the next call of op on sandbox id return err, without
// changing its state or counting the call. id "" matches any sandbox, and is
// the only id OpCreate accepts. Failures queue: each FailNext fails one more
// call. A call takes a failure queued for its own sandbox before one queued
// for "". It panics if op is not an Op, err is nil, or op is OpCreate with an
// id: those are test bugs.
func (b *Backend) FailNext(op Op, id string, err error) {
	if op < OpCreate || op > OpDestroy {
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

// addLocked adds sbx to the table. b.mu must be held.
func (b *Backend) addLocked(sbx *Info) {
	b.sandboxes[sbx.Handle.ID] = sbx
	b.order = append(b.order, sbx.Handle.ID)
	b.changedLocked()
}

// leftUpLocked returns the driver whose connections must be closed when
// sandbox id moves from state from to its current state: its own if it was up
// and is not any more, as stopping a sandbox kills its driver, or nil. b.mu
// must be held.
func (b *Backend) leftUpLocked(id string, from State) *driver {
	if from != Up || b.sandboxes[id].State == Up {
		return nil
	}
	return b.drivers[id]
}

// up reports whether sandbox id is up.
func (b *Backend) up(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	sbx := b.sandboxes[id]
	return sbx != nil && sbx.State == Up
}

// dial counts a Dial of sandbox id and returns its driver server, starting
// it on the first Dial. It returns nil for a sandbox whose driver never
// answers: one the fake never had, one whose Driver gave no handler, and
// any first dialed after Close.
func (b *Backend) dial(id string) *httptest.Server {
	b.mu.Lock()
	sbx := b.sandboxes[id]
	if sbx == nil {
		// A sandbox the fake never had has no counters; Dial succeeds anyway.
		b.mu.Unlock()
		return nil
	}
	sbx.Dials++
	b.changedLocked()
	d := b.drivers[id]
	if d != nil || b.closed || b.newDriver == nil {
		b.mu.Unlock()
		return d.server()
	}
	d = &driver{ready: make(chan struct{})}
	b.drivers[id] = d
	b.mu.Unlock()

	// Start the driver without the lock: concurrent Dials of the sandbox
	// wait for it in server.
	if h := b.newDriver(b.handle(id)); h != nil {
		d.srv = httptest.NewServer(b.gate(id, h))
	}
	close(d.ready)
	return d.srv
}

// gate serves h while sandbox id is up, and 503 otherwise, which Connect
// reports as Unavailable, as for a driver that is not answering. It records
// the spec of every Run request it serves that has one in the sandbox's
// Info.RunSpecs.
func (b *Backend) gate(id string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !b.up(id) {
			http.Error(w, "fake: sandbox is not up", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == driverv1connect.DriverServiceRunProcedure {
			req, err := readRunRequest(r)
			if err != nil {
				http.Error(w, "fake: "+err.Error(), http.StatusBadRequest)
				return
			}
			if spec := req.GetSpec(); spec != nil {
				b.recordRunSpec(id, spec)
			}
		}
		h.ServeHTTP(w, r)
	})
}

// readRunRequest decodes the RunRequest r carries, and leaves r's body to be
// read again. Run is server-streaming, so the body is a single enveloped
// message, uncompressed as the Connect client sends it by default.
func readRunRequest(r *http.Request) (*driverv1.RunRequest, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("read Run request: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) < 5 {
		return nil, errors.New("no message in Run request")
	}
	if body[0]&1 != 0 {
		return nil, errors.New("compressed Run request")
	}
	n := binary.BigEndian.Uint32(body[1:5])
	if uint64(len(body)-5) < uint64(n) {
		return nil, errors.New("truncated Run request")
	}
	msg := body[5 : 5+n]
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	req := &driverv1.RunRequest{}
	switch ct {
	case "application/connect+proto", "application/grpc", "application/grpc+proto":
		err = proto.Unmarshal(msg, req)
	case "application/connect+json", "application/grpc+json":
		err = protojson.Unmarshal(msg, req)
	default:
		return nil, fmt.Errorf("unsupported content type %q of Run request", ct)
	}
	if err != nil {
		return nil, fmt.Errorf("decode Run request: %w", err)
	}
	return req, nil
}

// recordRunSpec adds spec to sandbox id's RunSpecs.
func (b *Backend) recordRunSpec(id string, spec *driverv1.RunSpec) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if sbx := b.sandboxes[id]; sbx != nil {
		sbx.RunSpecs = append(sbx.RunSpecs, spec)
		b.changedLocked()
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
	d := s.b.leftUpLocked(s.id, from)
	s.b.mu.Unlock()
	d.drop()
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

// Dial returns a client for the sandbox's driver, whatever its state, which
// sends jwt as a bearer token. The driver is the handler Options.Driver gave
// for the sandbox: it answers while the sandbox is up, and fails every call
// as Unavailable otherwise. A sandbox with no driver never answers: each call
// fails as Unavailable, as for a driver that is not listening.
func (s *sandbox) Dial(_ context.Context, jwt string) (driverv1connect.DriverServiceClient, error) {
	srv := s.b.dial(s.id)
	if srv == nil {
		httpClient := &http.Client{Transport: noDriver{}}
		return driverv1connect.NewDriverServiceClient(httpClient, "http://driver"), nil
	}
	httpClient := &http.Client{Transport: &bearerTransport{base: srv.Client().Transport, token: jwt}}
	return driverv1connect.NewDriverServiceClient(httpClient, srv.URL), nil
}

// update applies fn, the body of op, to the sandbox under the lock, and wakes
// every Wait if fn succeeds. A failure FailNext queued for op is returned
// without calling fn; a sandbox the fake never had returns backend.ErrGone.
//
// If fn moves the sandbox out of up, the runner's connections to its driver
// are closed once the lock is released.
func (s *sandbox) update(op Op, fn func(sbx *Info) error) error {
	d, err := s.updateLocked(op, fn)
	d.drop()
	return err
}

func (s *sandbox) updateLocked(op Op, fn func(sbx *Info) error) (*driver, error) {
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

// driver is the driver server of one sandbox, started on its first Dial.
type driver struct {
	// ready is closed once srv is set.
	ready chan struct{}
	// srv serves the sandbox's driver, or is nil if it has none.
	srv *httptest.Server
}

// server waits for d to start and returns its server; nil for a nil d.
func (d *driver) server() *httptest.Server {
	if d == nil {
		return nil
	}
	<-d.ready
	return d.srv
}

// drop closes every connection to d's server; a nil d, or one with no
// server, has none.
func (d *driver) drop() {
	if srv := d.server(); srv != nil {
		srv.CloseClientConnections()
	}
}

// bearerTransport adds token to every request as a bearer token, unless it is
// empty.
type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.token == "" {
		return t.base.RoundTrip(req)
	}
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
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
	// RunSpecs is the spec of every Run request the sandbox's driver served
	// that has one, a request to start a run, in order. A request made while
	// the sandbox was not up never reached its driver, and is not in it.
	RunSpecs []*driverv1.RunSpec
}
