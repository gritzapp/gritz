package fake_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
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
		"start":   func(ctx context.Context, sb backend.Sandbox) error { return sb.Start(ctx) },
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
		{from: fake.Created, op: "start", wantStatus: backend.StatusUp, want: fake.Info{Handle: h, State: fake.Up, Starts: 1}},
		{from: fake.Created, op: "park", wantStatus: backend.StatusParked, want: fake.Info{Handle: h, State: fake.Created, Parks: 1}},
		{from: fake.Created, op: "destroy", wantStatus: backend.StatusGone, want: fake.Info{Handle: h, State: fake.Gone, Destroys: 1}},
		{from: fake.Created, op: "dial", wantStatus: backend.StatusParked, want: fake.Info{Handle: h, State: fake.Created, Dials: 1}},
		{from: fake.Created, op: "handle", wantStatus: backend.StatusParked, want: fake.Info{Handle: h, State: fake.Created}},

		{from: fake.Up, op: "status", wantStatus: backend.StatusUp, want: fake.Info{Handle: h, State: fake.Up}},
		{from: fake.Up, op: "start", wantStatus: backend.StatusUp, want: fake.Info{Handle: h, State: fake.Up, Starts: 1}},
		{from: fake.Up, op: "park", wantStatus: backend.StatusParked, want: fake.Info{Handle: h, State: fake.Parked, Parks: 1}},
		{from: fake.Up, op: "destroy", wantStatus: backend.StatusGone, want: fake.Info{Handle: h, State: fake.Gone, Destroys: 1}},
		{from: fake.Up, op: "dial", wantStatus: backend.StatusUp, want: fake.Info{Handle: h, State: fake.Up, Dials: 1}},
		{from: fake.Up, op: "handle", wantStatus: backend.StatusUp, want: fake.Info{Handle: h, State: fake.Up}},

		{from: fake.Parked, op: "status", wantStatus: backend.StatusParked, want: fake.Info{Handle: h, State: fake.Parked}},
		{from: fake.Parked, op: "start", wantStatus: backend.StatusUp, want: fake.Info{Handle: h, State: fake.Up, Starts: 1}},
		{from: fake.Parked, op: "park", wantStatus: backend.StatusParked, want: fake.Info{Handle: h, State: fake.Parked, Parks: 1}},
		{from: fake.Parked, op: "destroy", wantStatus: backend.StatusGone, want: fake.Info{Handle: h, State: fake.Gone, Destroys: 1}},
		{from: fake.Parked, op: "dial", wantStatus: backend.StatusParked, want: fake.Info{Handle: h, State: fake.Parked, Dials: 1}},
		{from: fake.Parked, op: "handle", wantStatus: backend.StatusParked, want: fake.Info{Handle: h, State: fake.Parked}},

		{from: fake.Gone, op: "status", wantStatus: backend.StatusGone, want: fake.Info{Handle: h, State: fake.Gone}},
		{from: fake.Gone, op: "start", wantErr: backend.ErrGone, wantStatus: backend.StatusGone, want: fake.Info{Handle: h, State: fake.Gone}},
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

	// Act - start through the opened handle.
	assert.NilError(t, opened.Start(t.Context()))

	// Assert - the created handle sees it, and changes the same sandbox.
	st, err := created.Status(t.Context())
	assert.NilError(t, err)
	assert.Equal(t, st, backend.StatusUp)
	assert.NilError(t, created.Park(t.Context()))
	got, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, got.State, fake.Parked)
	assert.Equal(t, got.Starts, 1)
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
	assert.ErrorIs(t, second.Start(t.Context()), backend.ErrGone)
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
	startErr := sb.Start(t.Context())
	parkErr := sb.Park(t.Context())
	destroyErr := sb.Destroy(t.Context())
	client, dialErr := sb.Dial(t.Context(), "jwt")

	// Assert
	assert.NilError(t, statusErr)
	assert.Equal(t, st, backend.StatusGone)
	assert.ErrorIs(t, startErr, backend.ErrGone)
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

func TestBackendValidateWorkspace(t *testing.T) {
	t.Parallel()
	// Arrange
	be := fake.New(fake.Options{})

	// Act
	err := be.ValidateWorkspace(&workspace.Workspace{})

	// Assert
	assert.NilError(t, err)
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
	// Arrange - a sandbox another goroutine starts once Wait is blocked.
	be := fake.New(fake.Options{})
	sb, err := be.Open(be.Add("c1", fake.Parked))
	assert.NilError(t, err)
	checked := make(chan struct{}, 1)
	go func() {
		<-checked
		_ = sb.Start(context.Background())
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
	assert.Equal(t, got.Starts, 1)
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
		fake.OpStart: func(t *testing.T, be *fake.Backend) error {
			return mustOpen(t, be, h).Start(t.Context())
		},
		fake.OpPark: func(t *testing.T, be *fake.Backend) error {
			return mustOpen(t, be, h).Park(t.Context())
		},
		fake.OpStatus: func(t *testing.T, be *fake.Backend) error {
			_, err := mustOpen(t, be, h).Status(t.Context())
			return err
		},
		fake.OpDestroy: func(t *testing.T, be *fake.Backend) error {
			return mustOpen(t, be, h).Destroy(t.Context())
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
	be.FailNext(fake.OpStart, "b", errBoom)

	// Act
	errA := a.Start(t.Context())
	errB := b.Start(t.Context())

	// Assert - only b's Start failed.
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
		{name: "nil error", op: fake.OpStart, id: "c1", want: `fake: FailNext(start, "c1") with a nil error`},
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
