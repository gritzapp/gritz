package fake_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/backend/fake"
	"github.com/gritzapp/gritz/internal/runner/workspace"
)

// TestSandbox covers every operation on a sandbox in every state, per the
// state table on backend.Sandbox. The op's error and the Status after it hold
// for any backend; want is the fake's own view.
func TestSandbox(t *testing.T) {
	t.Parallel()
	h := backend.Handle{Type: "fake", ID: "c1"}
	ops := map[string]func(ctx context.Context, sb backend.Sandbox) error{
		"status": func(ctx context.Context, sb backend.Sandbox) error {
			_, err := sb.Status(ctx)
			return err
		},
		"boot":    func(ctx context.Context, sb backend.Sandbox) error { return sb.Boot(ctx) },
		"park":    func(ctx context.Context, sb backend.Sandbox) error { return sb.Park(ctx) },
		"destroy": func(ctx context.Context, sb backend.Sandbox) error { return sb.Destroy(ctx) },
		"dial": func(ctx context.Context, sb backend.Sandbox) error {
			client, err := sb.Dial(ctx, "jwt")
			if err == nil && client == nil {
				return errors.New("Dial returned no client")
			}
			return err
		},
		"handle": func(_ context.Context, sb backend.Sandbox) error {
			if got := sb.Handle(); got.Type != h.Type || got.ID != h.ID || got.Data != nil {
				return errors.New("Handle changed")
			}
			return nil
		},
	}
	tests := []struct {
		from fake.State
		op   string
		// wantErr is the op's error, and wantStatus the sandbox's Status after it.
		wantErr    error
		wantStatus backend.Status
		want       fake.Info
	}{
		{from: fake.Created, op: "status", wantStatus: backend.StatusParked, want: fake.Info{Handle: h, State: fake.Created}},
		{from: fake.Created, op: "boot", wantStatus: backend.StatusUp, want: fake.Info{Handle: h, State: fake.Up, Boots: 1}},
		{from: fake.Created, op: "park", wantStatus: backend.StatusParked, want: fake.Info{Handle: h, State: fake.Created, Parks: 1}},
		{from: fake.Created, op: "destroy", wantStatus: backend.StatusGone, want: fake.Info{Handle: h, State: fake.Gone, Destroys: 1}},
		{from: fake.Created, op: "dial", wantStatus: backend.StatusParked, want: fake.Info{Handle: h, State: fake.Created, Dials: 1}},
		{from: fake.Created, op: "handle", wantStatus: backend.StatusParked, want: fake.Info{Handle: h, State: fake.Created}},

		{from: fake.Up, op: "status", wantStatus: backend.StatusUp, want: fake.Info{Handle: h, State: fake.Up}},
		{from: fake.Up, op: "boot", wantStatus: backend.StatusUp, want: fake.Info{Handle: h, State: fake.Up, Boots: 1}},
		{from: fake.Up, op: "park", wantStatus: backend.StatusParked, want: fake.Info{Handle: h, State: fake.Parked, Parks: 1}},
		{from: fake.Up, op: "destroy", wantStatus: backend.StatusGone, want: fake.Info{Handle: h, State: fake.Gone, Destroys: 1}},
		{from: fake.Up, op: "dial", wantStatus: backend.StatusUp, want: fake.Info{Handle: h, State: fake.Up, Dials: 1}},
		{from: fake.Up, op: "handle", wantStatus: backend.StatusUp, want: fake.Info{Handle: h, State: fake.Up}},

		{from: fake.Parked, op: "status", wantStatus: backend.StatusParked, want: fake.Info{Handle: h, State: fake.Parked}},
		{from: fake.Parked, op: "boot", wantStatus: backend.StatusUp, want: fake.Info{Handle: h, State: fake.Up, Boots: 1}},
		{from: fake.Parked, op: "park", wantStatus: backend.StatusParked, want: fake.Info{Handle: h, State: fake.Parked, Parks: 1}},
		{from: fake.Parked, op: "destroy", wantStatus: backend.StatusGone, want: fake.Info{Handle: h, State: fake.Gone, Destroys: 1}},
		{from: fake.Parked, op: "dial", wantStatus: backend.StatusParked, want: fake.Info{Handle: h, State: fake.Parked, Dials: 1}},
		{from: fake.Parked, op: "handle", wantStatus: backend.StatusParked, want: fake.Info{Handle: h, State: fake.Parked}},

		{from: fake.Gone, op: "status", wantStatus: backend.StatusGone, want: fake.Info{Handle: h, State: fake.Gone}},
		{from: fake.Gone, op: "boot", wantErr: backend.ErrGone, wantStatus: backend.StatusGone, want: fake.Info{Handle: h, State: fake.Gone}},
		{from: fake.Gone, op: "park", wantErr: backend.ErrGone, wantStatus: backend.StatusGone, want: fake.Info{Handle: h, State: fake.Gone}},
		{from: fake.Gone, op: "destroy", wantStatus: backend.StatusGone, want: fake.Info{Handle: h, State: fake.Gone, Destroys: 1}},
		{from: fake.Gone, op: "dial", wantStatus: backend.StatusGone, want: fake.Info{Handle: h, State: fake.Gone, Dials: 1}},
		{from: fake.Gone, op: "handle", wantStatus: backend.StatusGone, want: fake.Info{Handle: h, State: fake.Gone}},
	}
	for _, tt := range tests {
		t.Run(tt.from.String()+"/"+tt.op, func(t *testing.T) {
			t.Parallel()
			// Arrange
			be := fake.New(fake.Options{})
			sb, err := be.Open(be.Add("c1", tt.from))
			assert.NilError(t, err)

			// Act
			err = ops[tt.op](t.Context(), sb)

			// Assert
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NilError(t, err)
			}
			st, err := sb.Status(t.Context())
			assert.NilError(t, err)
			assert.Equal(t, st, tt.wantStatus)
			got, ok := be.Snapshot().Sandbox("c1")
			assert.Assert(t, ok)
			assert.DeepEqual(t, got, tt.want)
		})
	}
}

func TestBackendCreate(t *testing.T) {
	t.Parallel()
	// Arrange - c2 is taken by a sandbox a previous runner left.
	be := fake.New(fake.Options{})
	be.Add("c2", fake.Parked)
	spec1 := &backend.Spec{TaskID: 1, Version: 3, Env: map[string]string{"A": "1"}}
	spec2 := &backend.Spec{TaskID: 2, Version: 1}

	// Act
	sb1, err := be.Create(t.Context(), spec1)
	assert.NilError(t, err)
	sb2, err := be.Create(t.Context(), spec2)
	assert.NilError(t, err)
	spec1.Env["A"] = "changed"

	// Assert - IDs skip c2, the sandboxes are created, and their specs are
	// copies the caller's later changes do not reach.
	assert.DeepEqual(t, sb1.Handle(), backend.Handle{Type: "fake", ID: "c1"})
	assert.DeepEqual(t, sb2.Handle(), backend.Handle{Type: "fake", ID: "c3"})
	st, err := sb1.Status(t.Context())
	assert.NilError(t, err)
	assert.Equal(t, st, backend.StatusParked)
	want1 := backend.Spec{TaskID: 1, Version: 3, Env: map[string]string{"A": "1"}}
	want2 := backend.Spec{TaskID: 2, Version: 1}
	assert.DeepEqual(t, be.Snapshot(), fake.Snapshot{
		Sandboxes: []fake.Info{
			{Handle: backend.Handle{Type: "fake", ID: "c2"}, State: fake.Parked},
			{Handle: backend.Handle{Type: "fake", ID: "c1"}, TaskID: 1, State: fake.Created, Spec: &want1},
			{Handle: backend.Handle{Type: "fake", ID: "c3"}, TaskID: 2, State: fake.Created, Spec: &want2},
		},
		Specs: []backend.Spec{want1, want2},
	})
}

func TestBackendCreate_Type(t *testing.T) {
	t.Parallel()
	// Arrange
	be := fake.New(fake.Options{Type: "docker"})

	// Act
	sb, err := be.Create(t.Context(), &backend.Spec{TaskID: 1})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, sb.Handle(), backend.Handle{Type: "docker", ID: "c1"})
	_, err = be.Open(backend.Handle{Type: "fake", ID: "c1"})
	assert.ErrorContains(t, err, `handle type "fake" is not "docker"`)
}

func TestBackendOpen_AfterCreate(t *testing.T) {
	t.Parallel()
	// Arrange
	be := fake.New(fake.Options{})
	created, err := be.Create(t.Context(), &backend.Spec{TaskID: 1})
	assert.NilError(t, err)
	opened, err := be.Open(created.Handle())
	assert.NilError(t, err)

	// Act - boot through the opened handle.
	assert.NilError(t, opened.Boot(t.Context()))

	// Assert - the created handle sees it, and changes the same sandbox.
	st, err := created.Status(t.Context())
	assert.NilError(t, err)
	assert.Equal(t, st, backend.StatusUp)
	assert.NilError(t, created.Park(t.Context()))
	got, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, got.State, fake.Parked)
	assert.Equal(t, got.Boots, 1)
	assert.Equal(t, got.Parks, 1)
}

func TestBackendOpen_Shared(t *testing.T) {
	t.Parallel()
	// Arrange - two Opens of one handle, as two runner processes would.
	be := fake.New(fake.Options{})
	h := be.Add("c1", fake.Up)
	first, err := be.Open(h)
	assert.NilError(t, err)
	second, err := be.Open(h)
	assert.NilError(t, err)

	// Act
	assert.NilError(t, first.Destroy(t.Context()))

	// Assert - the second handle sees the sandbox gone.
	st, err := second.Status(t.Context())
	assert.NilError(t, err)
	assert.Equal(t, st, backend.StatusGone)
	assert.ErrorIs(t, second.Boot(t.Context()), backend.ErrGone)
	assert.DeepEqual(t, be.Snapshot().Destroyed, []string{"c1"})
}

func TestBackendOpen_Invalid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		h       backend.Handle
		wantErr string
	}{
		{name: "other type", h: backend.Handle{Type: "docker", ID: "c1"}, wantErr: `handle type "docker" is not "fake"`},
		{name: "no id", h: backend.Handle{Type: "fake"}, wantErr: "handle has no sandbox id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			be := fake.New(fake.Options{})
			be.Add("c1", fake.Up)

			// Act
			_, err := be.Open(tt.h)

			// Assert
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestBackendOpen_Data(t *testing.T) {
	t.Parallel()
	// Arrange - a legacy handle with Data.
	be := fake.New(fake.Options{})
	be.Add("c1", fake.Parked)

	// Act
	sb, err := be.Open(backend.Handle{Type: "fake", ID: "c1", Data: []byte(`{"legacy":true}`)})

	// Assert - Data is dropped from the sandbox's handle.
	assert.NilError(t, err)
	assert.DeepEqual(t, sb.Handle(), backend.Handle{Type: "fake", ID: "c1"})
}

// TestSandbox_Stale covers a handle whose ID the fake never had: it behaves as
// gone, and adds nothing to the fake.
func TestSandbox_Stale(t *testing.T) {
	t.Parallel()
	// Arrange
	be := fake.New(fake.Options{})
	sb, err := be.Open(backend.Handle{Type: "fake", ID: "c9"})
	assert.NilError(t, err)

	// Act
	st, statusErr := sb.Status(t.Context())
	bootErr := sb.Boot(t.Context())
	parkErr := sb.Park(t.Context())
	destroyErr := sb.Destroy(t.Context())
	client, dialErr := sb.Dial(t.Context(), "jwt")

	// Assert
	assert.NilError(t, statusErr)
	assert.Equal(t, st, backend.StatusGone)
	assert.ErrorIs(t, bootErr, backend.ErrGone)
	assert.ErrorIs(t, parkErr, backend.ErrGone)
	assert.NilError(t, destroyErr)
	assert.NilError(t, dialErr)
	assert.Assert(t, client != nil)
	assert.DeepEqual(t, sb.Handle(), backend.Handle{Type: "fake", ID: "c9"})
	assert.DeepEqual(t, be.Snapshot(), fake.Snapshot{})
}

func TestSandboxDial_NoDriver(t *testing.T) {
	t.Parallel()
	// Arrange - an up sandbox, which has no driver.
	be := fake.New(fake.Options{})
	sb, err := be.Open(be.Add("c1", fake.Up))
	assert.NilError(t, err)
	client, err := sb.Dial(t.Context(), "jwt")
	assert.NilError(t, err)

	// Act
	_, err = client.Status(t.Context(), &driverv1.StatusRequest{})

	// Assert - the call fails as for a driver that is not listening.
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnavailable)
}

func TestSandboxDial_Driver(t *testing.T) {
	t.Parallel()
	// Arrange - an up sandbox dialed twice.
	drv := newStubDriver(t)
	var mu sync.Mutex
	var handles []backend.Handle
	be := newDriverBackend(t, func(h backend.Handle) driverv1connect.DriverServiceClient {
		mu.Lock()
		defer mu.Unlock()
		handles = append(handles, h)
		return drv.client
	})
	sb := mustOpen(t, be, be.Add("c1", fake.Up))
	first := mustDial(t, sb)
	second := mustDial(t, sb)

	// Act
	_, err1 := first.Status(t.Context(), &driverv1.StatusRequest{})
	_, err2 := second.Status(t.Context(), &driverv1.StatusRequest{})

	// Assert - both clients reach the driver Dial gave for each.
	assert.NilError(t, err1)
	assert.NilError(t, err2)
	assert.DeepEqual(t, handles, []backend.Handle{{Type: "fake", ID: "c1"}, {Type: "fake", ID: "c1"}})
	assert.Equal(t, drv.calls(), 2)
	got, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, got.Dials, 2)
}

func TestSandboxDial_DriverPerSandbox(t *testing.T) {
	t.Parallel()
	// Arrange - two up sandboxes, each with its own driver.
	drivers := map[string]*stubDriver{"a": newStubDriver(t), "b": newStubDriver(t)}
	be := newDriverBackend(t, func(h backend.Handle) driverv1connect.DriverServiceClient {
		return drivers[h.ID].client
	})
	a := mustDial(t, mustOpen(t, be, be.Add("a", fake.Up)))
	mustDial(t, mustOpen(t, be, be.Add("b", fake.Up)))

	// Act
	_, err := a.Status(t.Context(), &driverv1.StatusRequest{})

	// Assert - only a's driver was called.
	assert.NilError(t, err)
	assert.Equal(t, drivers["a"].calls(), 1)
	assert.Equal(t, drivers["b"].calls(), 0)
}

// TestSandboxDial_NotUp covers every driver call on a sandbox that is not up:
// each fails as for a driver that is not answering, without reaching it.
func TestSandboxDial_NotUp(t *testing.T) {
	t.Parallel()
	calls := map[string]func(ctx context.Context, client driverv1connect.DriverServiceClient) error{
		"status": func(ctx context.Context, client driverv1connect.DriverServiceClient) error {
			_, err := client.Status(ctx, &driverv1.StatusRequest{})
			return err
		},
		"stop": func(ctx context.Context, client driverv1connect.DriverServiceClient) error {
			_, err := client.Stop(ctx, &driverv1.StopRequest{Version: 1})
			return err
		},
		"run": func(ctx context.Context, client driverv1connect.DriverServiceClient) error {
			_, err := client.Run(ctx, &driverv1.RunRequest{Version: 1})
			return err
		},
	}
	for name, call := range calls {
		for _, state := range []fake.State{fake.Created, fake.Parked, fake.Gone} {
			t.Run(name+"/"+state.String(), func(t *testing.T) {
				t.Parallel()
				// Arrange
				drv := newStubDriver(t)
				be := newDriverBackend(t, drv.dial)
				client := mustDial(t, mustOpen(t, be, be.Add("c1", state)))

				// Act
				err := call(t.Context(), client)

				// Assert
				assert.Equal(t, connect.CodeOf(err), connect.CodeUnavailable)
				assert.Equal(t, drv.calls(), 0)
			})
		}
	}
}

func TestSandboxDial_Booted(t *testing.T) {
	t.Parallel()
	// Arrange - a parked sandbox dialed before it is booted.
	drv := newStubDriver(t)
	be := newDriverBackend(t, drv.dial)
	sb := mustOpen(t, be, be.Add("c1", fake.Parked))
	client := mustDial(t, sb)
	_, err := client.Status(t.Context(), &driverv1.StatusRequest{})
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnavailable)

	// Act
	assert.NilError(t, sb.Boot(t.Context()))

	// Assert - the same client now reaches the driver.
	_, err = client.Status(t.Context(), &driverv1.StatusRequest{})
	assert.NilError(t, err)
}

func TestSandboxDial_NilClient(t *testing.T) {
	t.Parallel()
	// Arrange - a Dial that gives the sandbox no client.
	be := newDriverBackend(t, func(backend.Handle) driverv1connect.DriverServiceClient { return nil })
	client := mustDial(t, mustOpen(t, be, be.Add("c1", fake.Up)))

	// Act
	_, err := client.Status(t.Context(), &driverv1.StatusRequest{})

	// Assert - the sandbox's driver never answers.
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnavailable)
}

// TestSandboxDial_RunSpecs covers the run specs the fake records: the spec of
// each Run request that starts a run, served while the sandbox is up.
func TestSandboxDial_RunSpecs(t *testing.T) {
	t.Parallel()
	// Arrange - a parked sandbox's driver.
	drv := newStubDriver(t)
	be := newDriverBackend(t, drv.dial)
	sb := mustOpen(t, be, be.Add("c1", fake.Parked))
	client := mustDial(t, sb)
	spec := &driverv1.RunSpec{TaskId: 1, Env: map[string]string{"A": "1"}}

	// Act - a start while parked, then once up a start and an attach.
	_, err := client.Run(t.Context(), &driverv1.RunRequest{Version: 1, Spec: &driverv1.RunSpec{TaskId: 9}})
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnavailable)
	assert.NilError(t, sb.Boot(t.Context()))
	stream, err := client.Run(t.Context(), &driverv1.RunRequest{Version: 2, Spec: spec})
	assert.NilError(t, err)
	assert.Assert(t, stream.Receive(), stream.Err())
	assert.NilError(t, stream.Close())
	openRun(t, client)

	// Assert - both calls made while up are counted, and the start's spec is
	// recorded; the call made while parked is neither.
	got, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, got.Runs, 2)
	assert.DeepEqual(t, got.RunSpecs, []*driverv1.RunSpec{spec}, protocmp.Transform())

	// Act - change the snapshot's copy.
	got.RunSpecs[0].Env["A"] = "changed"

	// Assert - the fake's own is unchanged.
	again, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, again.RunSpecs[0].Env["A"], "1")
}

// TestSandboxDial_LeaveUp covers every way out of up: each cuts an open
// stream, and the driver stops answering.
func TestSandboxDial_LeaveUp(t *testing.T) {
	t.Parallel()
	tests := map[string]func(t *testing.T, be *fake.Backend, sb backend.Sandbox){
		"park":       func(t *testing.T, _ *fake.Backend, sb backend.Sandbox) { assert.NilError(t, sb.Park(t.Context())) },
		"destroy":    func(t *testing.T, _ *fake.Backend, sb backend.Sandbox) { assert.NilError(t, sb.Destroy(t.Context())) },
		"set parked": func(_ *testing.T, be *fake.Backend, _ backend.Sandbox) { be.Set("c1", fake.Parked) },
		"set gone":   func(_ *testing.T, be *fake.Backend, _ backend.Sandbox) { be.Set("c1", fake.Gone) },
	}
	for name, leave := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// Arrange - an open stream to an up sandbox's driver.
			drv := newStubDriver(t)
			be := newDriverBackend(t, drv.dial)
			sb := mustOpen(t, be, be.Add("c1", fake.Up))
			client := mustDial(t, sb)
			stream := openRun(t, client)

			// Act
			leave(t, be, sb)

			// Assert
			assert.Assert(t, !stream.Receive())
			assert.Equal(t, connect.CodeOf(stream.Err()), connect.CodeCanceled)
			_, err := client.Status(t.Context(), &driverv1.StatusRequest{})
			assert.Equal(t, connect.CodeOf(err), connect.CodeUnavailable)
		})
	}
}

// TestBackendSetDriverReady covers an up sandbox whose driver is not ready:
// every call fails as Unavailable without reaching it, and goes through again
// once it is ready.
func TestBackendSetDriverReady(t *testing.T) {
	t.Parallel()
	// Arrange - an open stream to an up sandbox's driver.
	drv := newStubDriver(t)
	be := newDriverBackend(t, drv.dial)
	client := mustDial(t, mustOpen(t, be, be.Add("c1", fake.Up)))
	stream := openRun(t, client)
	calls := drv.calls()

	// Act
	be.SetDriverReady("c1", false)

	// Assert - the stream is cut, and no call reaches the driver; the
	// sandbox is still up.
	assert.Assert(t, !stream.Receive())
	assert.Equal(t, connect.CodeOf(stream.Err()), connect.CodeCanceled)
	_, err := client.Status(t.Context(), &driverv1.StatusRequest{})
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnavailable)
	_, err = client.Stop(t.Context(), &driverv1.StopRequest{Version: 1})
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnavailable)
	_, err = client.Run(t.Context(), &driverv1.RunRequest{Version: 1})
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnavailable)
	assert.Equal(t, drv.calls(), calls)
	got, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, got.State, fake.Up)
	assert.Equal(t, got.Runs, 1)

	// Act
	be.SetDriverReady("c1", true)

	// Assert - the driver answers again.
	_, err = client.Status(t.Context(), &driverv1.StatusRequest{})
	assert.NilError(t, err)
}

// TestBackendDriverNotReady covers Options.DriverNotReady: every sandbox,
// added or created, starts with its driver not ready.
func TestBackendDriverNotReady(t *testing.T) {
	t.Parallel()
	// Arrange
	drv := newStubDriver(t)
	be := fake.New(fake.Options{Dial: drv.dial, DriverNotReady: true})
	t.Cleanup(func() { assert.NilError(t, be.Close()) })
	added := mustOpen(t, be, be.Add("a", fake.Up))
	created, err := be.Create(t.Context(), &backend.Spec{TaskID: 1})
	assert.NilError(t, err)
	assert.NilError(t, created.Boot(t.Context()))

	// Act
	_, errAdded := mustDial(t, added).Status(t.Context(), &driverv1.StatusRequest{})
	_, errCreated := mustDial(t, created).Status(t.Context(), &driverv1.StatusRequest{})

	// Assert
	assert.Equal(t, connect.CodeOf(errAdded), connect.CodeUnavailable)
	assert.Equal(t, connect.CodeOf(errCreated), connect.CodeUnavailable)
	assert.Equal(t, drv.calls(), 0)

	// Act - the created sandbox's driver comes up.
	be.SetDriverReady(created.Handle().ID, true)

	// Assert
	_, err = mustDial(t, created).Status(t.Context(), &driverv1.StatusRequest{})
	assert.NilError(t, err)
}

func TestBackendSetDriverReady_Panics(t *testing.T) {
	t.Parallel()
	// Arrange
	be := fake.New(fake.Options{})

	// Act
	got := panicValue(func() { be.SetDriverReady("c9", true) })

	// Assert
	assert.Equal(t, got, `fake: SetDriverReady("c9"): unknown id`)
}

func TestBackendDrop(t *testing.T) {
	t.Parallel()
	// Arrange - an open stream to an up sandbox's driver.
	drv := newStubDriver(t)
	be := newDriverBackend(t, drv.dial)
	client := mustDial(t, mustOpen(t, be, be.Add("c1", fake.Up)))
	stream := openRun(t, client)
	want := be.Snapshot()

	// Act
	be.Drop("c1")

	// Assert - the stream is cut, but the sandbox is still up and its driver
	// answers the next calls.
	assert.Assert(t, !stream.Receive())
	assert.Equal(t, connect.CodeOf(stream.Err()), connect.CodeCanceled)
	assert.DeepEqual(t, be.Snapshot(), want)
	_, err := client.Status(t.Context(), &driverv1.StatusRequest{})
	assert.NilError(t, err)
	openRun(t, client)
	got, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, got.Runs, 2)
}

func TestBackendDrop_Panics(t *testing.T) {
	t.Parallel()
	// Arrange
	be := fake.New(fake.Options{})

	// Act
	got := panicValue(func() { be.Drop("c9") })

	// Assert
	assert.Equal(t, got, `fake: Drop("c9"): unknown id`)
}

func TestBackendClose(t *testing.T) {
	t.Parallel()
	// Arrange - an open stream to an up sandbox's driver.
	drv := newStubDriver(t)
	be := fake.New(fake.Options{Dial: drv.dial})
	client := mustDial(t, mustOpen(t, be, be.Add("c1", fake.Up)))
	stream := openRun(t, client)

	// Act
	assert.NilError(t, be.Close())

	// Assert - the stream is cut, and the driver no longer answers.
	assert.Assert(t, !stream.Receive())
	assert.Equal(t, connect.CodeOf(stream.Err()), connect.CodeCanceled)
	_, err := client.Status(t.Context(), &driverv1.StatusRequest{})
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnavailable)
	got, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, got.State, fake.Up)
}

func TestBackendValidateWorkspace(t *testing.T) {
	t.Parallel()
	// Arrange
	be := fake.New(fake.Options{})

	// Act
	err := be.ValidateWorkspace(&workspace.Workspace{})

	// Assert
	assert.NilError(t, err)
}

func TestBackendRunEnv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		runEnv func(*workspace.Workspace) map[string]string
		want   map[string]string
	}{
		{name: "none"},
		{
			name:   "set",
			runEnv: func(ws *workspace.Workspace) map[string]string { return map[string]string{"IMAGE": ws.Container.Image} },
			want:   map[string]string{"IMAGE": "alpine"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			var be backend.RunEnver = fake.New(fake.Options{RunEnv: tt.runEnv})

			// Act
			env := be.RunEnv(&workspace.Workspace{Container: workspace.Container{Image: "alpine"}})

			// Assert
			assert.DeepEqual(t, env, tt.want)
		})
	}
}

func TestSnapshot_Copy(t *testing.T) {
	t.Parallel()
	// Arrange
	be := fake.New(fake.Options{})
	_, err := be.Create(t.Context(), &backend.Spec{TaskID: 1, Env: map[string]string{"A": "1"}})
	assert.NilError(t, err)
	snap := be.Snapshot()

	// Act - change everything the snapshot shares a reference with.
	snap.Specs[0].Env["A"] = "changed"
	snap.Sandboxes[0].Spec.Env["A"] = "changed"
	snap.Sandboxes[0].State = fake.Gone

	// Assert
	got := be.Snapshot()
	assert.Equal(t, got.Specs[0].Env["A"], "1")
	assert.Equal(t, got.Sandboxes[0].Spec.Env["A"], "1")
	assert.Equal(t, got.Sandboxes[0].State, fake.Created)
}

func TestSnapshotLive(t *testing.T) {
	t.Parallel()
	// Arrange
	be := fake.New(fake.Options{})
	be.Add("a", fake.Up)
	be.Add("b", fake.Gone)
	be.Add("c", fake.Parked)

	// Act
	live := be.Snapshot().Live()

	// Assert
	assert.DeepEqual(t, live, []fake.Info{
		{Handle: backend.Handle{Type: "fake", ID: "a"}, State: fake.Up},
		{Handle: backend.Handle{Type: "fake", ID: "c"}, State: fake.Parked},
	})
}

func TestBackendWait(t *testing.T) {
	t.Parallel()
	// Arrange - a sandbox another goroutine boots once Wait is blocked.
	be := fake.New(fake.Options{})
	sb, err := be.Open(be.Add("c1", fake.Parked))
	assert.NilError(t, err)
	checked := make(chan struct{}, 1)
	go func() {
		<-checked
		_ = sb.Boot(context.Background())
	}()

	// Act
	snap, err := be.Wait(t.Context(), func(s fake.Snapshot) bool {
		select {
		case checked <- struct{}{}:
		default:
		}
		got, _ := s.Sandbox("c1")
		return got.State == fake.Up
	})

	// Assert - Wait returned the state cond held for.
	assert.NilError(t, err)
	got, _ := snap.Sandbox("c1")
	assert.Equal(t, got.State, fake.Up)
	assert.Equal(t, got.Boots, 1)
}

func TestBackendWaitState(t *testing.T) {
	t.Parallel()
	// Arrange - a sandbox another goroutine destroys.
	be := fake.New(fake.Options{})
	sb, err := be.Create(t.Context(), &backend.Spec{TaskID: 1})
	assert.NilError(t, err)
	go func() { _ = sb.Destroy(context.Background()) }()

	// Act
	err = be.WaitState(t.Context(), "c1", fake.Gone)

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, be.Snapshot().Destroyed, []string{"c1"})
}

func TestBackendWait_Done(t *testing.T) {
	t.Parallel()
	// Arrange - a condition that never holds.
	be := fake.New(fake.Options{})
	be.Add("c1", fake.Parked)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()

	// Act
	snap, err := be.Wait(ctx, func(fake.Snapshot) bool { return false })

	// Assert - Wait returns the last state it checked, with ctx's error.
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Assert(t, cmp.Len(snap.Sandboxes, 1))
	assert.ErrorIs(t, be.WaitState(ctx, "c1", fake.Up), context.DeadlineExceeded)
}

func TestBackendSet(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from, to   fake.State
		wantStatus backend.Status
	}{
		{from: fake.Up, to: fake.Parked, wantStatus: backend.StatusParked},
		{from: fake.Parked, to: fake.Up, wantStatus: backend.StatusUp},
		{from: fake.Created, to: fake.Up, wantStatus: backend.StatusUp},
		{from: fake.Up, to: fake.Gone, wantStatus: backend.StatusGone},
		{from: fake.Gone, to: fake.Gone, wantStatus: backend.StatusGone},
	}
	for _, tt := range tests {
		t.Run(tt.from.String()+"/"+tt.to.String(), func(t *testing.T) {
			t.Parallel()
			// Arrange
			be := fake.New(fake.Options{})
			sb, err := be.Open(be.Add("c1", tt.from))
			assert.NilError(t, err)

			// Act
			be.Set("c1", tt.to)

			// Assert - the sandbox moved, no call was counted, and a Set to
			// gone is not a Destroy.
			st, err := sb.Status(t.Context())
			assert.NilError(t, err)
			assert.Equal(t, st, tt.wantStatus)
			assert.DeepEqual(t, be.Snapshot(), fake.Snapshot{
				Sandboxes: []fake.Info{{Handle: backend.Handle{Type: "fake", ID: "c1"}, State: tt.to}},
			})
		})
	}
}

func TestBackendSet_Wakes(t *testing.T) {
	t.Parallel()
	// Arrange
	be := fake.New(fake.Options{})
	be.Add("c1", fake.Up)
	go be.Set("c1", fake.Gone)

	// Act
	err := be.WaitState(t.Context(), "c1", fake.Gone)

	// Assert
	assert.NilError(t, err)
}

func TestBackendSet_Panics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		id    string
		state fake.State
		want  string
	}{
		{name: "unknown id", id: "c9", state: fake.Up, want: `fake: Set("c9"): unknown id`},
		{name: "leave gone", id: "gone", state: fake.Up, want: `fake: Set("gone", up): sandbox is gone`},
		{name: "invalid state", id: "up", state: fake.State(0), want: `fake: Set("up") with invalid State(0)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			be := fake.New(fake.Options{})
			be.Add("gone", fake.Gone)
			be.Add("up", fake.Up)
			want := be.Snapshot()

			// Act
			got := panicValue(func() { be.Set(tt.id, tt.state) })

			// Assert
			assert.Equal(t, got, tt.want)
			assert.DeepEqual(t, be.Snapshot(), want)
		})
	}
}

// TestBackendFailNext fails each op once on its sandbox: the call returns the
// error, and the sandbox's state and counters do not change.
func TestBackendFailNext(t *testing.T) {
	t.Parallel()
	errBoom := errors.New("boom")
	h := backend.Handle{Type: "fake", ID: "c1"}
	ops := map[fake.Op]func(t *testing.T, be *fake.Backend) error{
		fake.OpOpen: func(_ *testing.T, be *fake.Backend) error {
			_, err := be.Open(h)
			return err
		},
		fake.OpBoot: func(t *testing.T, be *fake.Backend) error {
			return mustOpen(t, be, h).Boot(t.Context())
		},
		fake.OpPark: func(t *testing.T, be *fake.Backend) error {
			return mustOpen(t, be, h).Park(t.Context())
		},
		fake.OpStatus: func(t *testing.T, be *fake.Backend) error {
			// A Status that fails reports StatusUnknown; one that does not
			// never does.
			st, err := mustOpen(t, be, h).Status(t.Context())
			assert.Equal(t, st == backend.StatusUnknown, err != nil)
			return err
		},
		fake.OpDestroy: func(t *testing.T, be *fake.Backend) error {
			return mustOpen(t, be, h).Destroy(t.Context())
		},
		fake.OpDial: func(t *testing.T, be *fake.Backend) error {
			// A Dial that fails returns no client; one that does not always
			// does.
			client, err := mustOpen(t, be, h).Dial(t.Context(), "")
			assert.Equal(t, client == nil, err != nil)
			return err
		},
	}
	for op, call := range ops {
		for _, from := range []fake.State{fake.Up, fake.Parked} {
			t.Run(op.String()+"/"+from.String(), func(t *testing.T) {
				t.Parallel()
				// Arrange
				be := fake.New(fake.Options{})
				be.Add("c1", from)
				want := be.Snapshot()
				be.FailNext(op, "c1", errBoom)

				// Act
				err := call(t, be)

				// Assert - the failure is consumed, and the next call succeeds.
				assert.ErrorIs(t, err, errBoom)
				assert.DeepEqual(t, be.Snapshot(), want)
				assert.NilError(t, call(t, be))
			})
		}
	}
}

func TestBackendFailNext_PerSandbox(t *testing.T) {
	t.Parallel()
	// Arrange
	errBoom := errors.New("boom")
	be := fake.New(fake.Options{})
	a := mustOpen(t, be, be.Add("a", fake.Parked))
	b := mustOpen(t, be, be.Add("b", fake.Parked))
	be.FailNext(fake.OpBoot, "b", errBoom)

	// Act
	errA := a.Boot(t.Context())
	errB := b.Boot(t.Context())

	// Assert - only b's Boot failed.
	assert.NilError(t, errA)
	assert.ErrorIs(t, errB, errBoom)
	gotA, _ := be.Snapshot().Sandbox("a")
	gotB, _ := be.Snapshot().Sandbox("b")
	assert.Equal(t, gotA.State, fake.Up)
	assert.Equal(t, gotB.State, fake.Parked)
}

func TestBackendFailNext_Queued(t *testing.T) {
	t.Parallel()
	// Arrange - two failures for c1, then one for any sandbox.
	err1, err2, errAny := errors.New("one"), errors.New("two"), errors.New("any")
	be := fake.New(fake.Options{})
	sb := mustOpen(t, be, be.Add("c1", fake.Up))
	other := mustOpen(t, be, be.Add("c2", fake.Up))
	be.FailNext(fake.OpPark, "", errAny)
	be.FailNext(fake.OpPark, "c1", err1)
	be.FailNext(fake.OpPark, "c1", err2)

	// Act
	got := []error{
		sb.Park(t.Context()),
		sb.Park(t.Context()),
		sb.Park(t.Context()),
		sb.Park(t.Context()),
		other.Park(t.Context()),
	}

	// Assert - c1's own failures come first, in order, then the one for any
	// sandbox; once they are used up, calls succeed.
	assert.ErrorIs(t, got[0], err1)
	assert.ErrorIs(t, got[1], err2)
	assert.ErrorIs(t, got[2], errAny)
	assert.NilError(t, got[3])
	assert.NilError(t, got[4])
	c1, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, c1.State, fake.Parked)
	assert.Equal(t, c1.Parks, 1)
}

func TestBackendFailNext_Create(t *testing.T) {
	t.Parallel()
	// Arrange
	errBoom := errors.New("boom")
	be := fake.New(fake.Options{})
	be.FailNext(fake.OpCreate, "", errBoom)

	// Act
	sb, err := be.Create(t.Context(), &backend.Spec{TaskID: 1})

	// Assert - no sandbox, but the spec is recorded; the next Create takes c1.
	assert.ErrorIs(t, err, errBoom)
	assert.Assert(t, sb == nil)
	assert.DeepEqual(t, be.Snapshot(), fake.Snapshot{Specs: []backend.Spec{{TaskID: 1}}})
	sb, err = be.Create(t.Context(), &backend.Spec{TaskID: 2})
	assert.NilError(t, err)
	assert.DeepEqual(t, sb.Handle(), backend.Handle{Type: "fake", ID: "c1"})
	assert.Equal(t, len(be.Snapshot().Specs), 2)
}

func TestBackendFailNext_Panics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		op   fake.Op
		id   string
		err  error
		want string
	}{
		{name: "invalid op", op: fake.Op(0), err: errors.New("boom"), want: "fake: FailNext with invalid Op(0)"},
		{name: "nil error", op: fake.OpBoot, id: "c1", want: `fake: FailNext(boot, "c1") with a nil error`},
		{name: "create with id", op: fake.OpCreate, id: "c1", err: errors.New("boom"), want: `fake: FailNext(create, "c1"): Create has no sandbox id`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			be := fake.New(fake.Options{})

			// Act
			got := panicValue(func() { be.FailNext(tt.op, tt.id, tt.err) })

			// Assert
			assert.Equal(t, got, tt.want)
		})
	}
}

// stubDriver is a driver.v1 server that answers Status, and whose Run sends
// Accepted and then holds the stream open until the client goes away. It
// counts the calls it gets.
type stubDriver struct {
	driverv1connect.UnimplementedDriverServiceHandler

	// client is the test's client to the driver.
	client driverv1connect.DriverServiceClient

	mu sync.Mutex
	n  int
}

// newStubDriver serves a stubDriver until t ends.
func newStubDriver(t *testing.T) *stubDriver {
	t.Helper()
	d := &stubDriver{}
	mux := http.NewServeMux()
	mux.Handle(driverv1connect.NewDriverServiceHandler(d))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		d.n++
		d.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	d.client = driverv1connect.NewDriverServiceClient(srv.Client(), srv.URL)
	return d
}

// dial is a fake.Options.Dial giving every sandbox d.
func (d *stubDriver) dial(backend.Handle) driverv1connect.DriverServiceClient {
	return d.client
}

func (d *stubDriver) Status(context.Context, *driverv1.StatusRequest) (*driverv1.StatusResponse, error) {
	return &driverv1.StatusResponse{}, nil
}

func (d *stubDriver) Run(ctx context.Context, _ *driverv1.RunRequest, stream *connect.ServerStream[driverv1.RunResponse]) error {
	err := stream.Send(&driverv1.RunResponse{Event: &driverv1.RunResponse_Accepted{Accepted: &driverv1.Accepted{}}})
	if err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

// calls returns how many calls d got.
func (d *stubDriver) calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.n
}

// newDriverBackend returns a Backend reaching drivers through dial, closed
// when t ends.
func newDriverBackend(t *testing.T, dial func(backend.Handle) driverv1connect.DriverServiceClient) *fake.Backend {
	t.Helper()
	be := fake.New(fake.Options{Dial: dial})
	t.Cleanup(func() { assert.NilError(t, be.Close()) })
	return be
}

func mustDial(t *testing.T, sb backend.Sandbox) driverv1connect.DriverServiceClient {
	t.Helper()
	client, err := sb.Dial(t.Context(), "jwt")
	assert.NilError(t, err)
	return client
}

// openRun opens a Run stream on client and waits for its Accepted.
func openRun(t *testing.T, client driverv1connect.DriverServiceClient) *connect.ServerStreamForClient[driverv1.RunResponse] {
	t.Helper()
	stream, err := client.Run(t.Context(), &driverv1.RunRequest{Version: 1})
	assert.NilError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	assert.Assert(t, stream.Receive(), stream.Err())
	assert.Assert(t, stream.Msg().GetAccepted() != nil)
	return stream
}

func mustOpen(t *testing.T, be *fake.Backend, h backend.Handle) backend.Sandbox {
	t.Helper()
	sb, err := be.Open(h)
	assert.NilError(t, err)
	return sb
}

// panicValue returns what fn panics with, or nil.
func panicValue(fn func()) (v any) {
	defer func() { v = recover() }()
	fn()
	return nil
}
