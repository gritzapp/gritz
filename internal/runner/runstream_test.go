package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/go-cmp/cmp/cmpopts"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner"
	"github.com/gritzapp/gritz/internal/runner/agent"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/backend/fake"
	"github.com/gritzapp/gritz/internal/runner/testrunner"
	"github.com/gritzapp/gritz/internal/x/testx"
	"google.golang.org/protobuf/testing/protocmp"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// call is one callback a RunStream made, as a recorder records it.
type call struct {
	// Name is "accepted", "finished", "lost" or "unreachable".
	Name     string
	Accepted *driverv1.Accepted
	Finished *driverv1.Finished
	// Reason is OnLost's reason.
	Reason runner.LostReason
	// Err is OnLost's or OnUnreachable's error.
	Err error
}

// terminal reports whether c ends the run.
func (c call) terminal() bool {
	return c.Name == "finished" || c.Name == "lost"
}

// ignoreErr drops call.Err from a comparison of recorded calls; tests assert
// it on its own.
var ignoreErr = cmpopts.IgnoreFields(call{}, "Err")

// recorder records a RunStream's callbacks.
type recorder struct {
	calls testx.SafeSlice[call]
	// accepted receives every Accepted once it is recorded.
	accepted chan *driverv1.Accepted
	// unreachable is called by the n-th OnUnreachable, counting from 1,
	// before it returns errs["unreachable"]; optional.
	unreachable func(n int)
	// unreachables counts the OnUnreachable calls.
	unreachables int
	// dropped records the error of every OnDropped. It is kept apart from
	// calls, as how many drops a test sees depends on timing.
	dropped testx.SafeSlice[error]
	// errs is what each callback returns, by call name ("dropped" for
	// OnDropped); nil by default.
	errs map[string]error
}

func newRecorder() *recorder {
	return &recorder{accepted: make(chan *driverv1.Accepted, 1000)}
}

// options returns opts with r's callbacks.
func (r *recorder) options(opts runner.RunStreamOptions) runner.RunStreamOptions {
	opts.OnAccepted = func(_ context.Context, a *driverv1.Accepted) error {
		r.calls.Append(call{Name: "accepted", Accepted: a})
		r.accepted <- a
		return r.errs["accepted"]
	}
	opts.OnDropped = func(_ context.Context, err error) error {
		r.dropped.Append(err)
		return r.errs["dropped"]
	}
	opts.OnFinished = func(_ context.Context, f *driverv1.Finished) error {
		r.calls.Append(call{Name: "finished", Finished: f})
		return r.errs["finished"]
	}
	opts.OnLost = func(_ context.Context, reason runner.LostReason, err error) error {
		r.calls.Append(call{Name: "lost", Reason: reason, Err: err})
		return r.errs["lost"]
	}
	opts.OnUnreachable = func(_ context.Context, err error) error {
		r.unreachables++
		if r.unreachable != nil {
			r.unreachable(r.unreachables)
		}
		r.calls.Append(call{Name: "unreachable", Err: err})
		return r.errs["unreachable"]
	}
	return opts
}

// waitAccepted waits for the RunStream's next OnAccepted.
func (r *recorder) waitAccepted(t *testing.T) *driverv1.Accepted {
	t.Helper()
	select {
	case a := <-r.accepted:
		return a
	case <-time.After(10 * time.Second):
		t.Fatal("no stream was accepted")
		return nil
	}
}

// assertTerminalLast asserts calls holds exactly one terminal callback, and
// that it is the last.
func assertTerminalLast(t *testing.T, calls []call) {
	t.Helper()
	var n int
	for _, c := range calls {
		if c.terminal() {
			n++
		}
	}
	assert.Equal(t, n, 1, "terminal callbacks in %v", calls)
	assert.Assert(t, calls[len(calls)-1].terminal(), "last callback of %v is not terminal", calls)
}

// stubDriver is a driver whose Run is scripted by run, and whose Status
// always answers.
type stubDriver struct {
	driverv1connect.UnimplementedDriverServiceHandler
	run func(ctx context.Context, req *driverv1.RunRequest, stream *connect.ServerStream[driverv1.RunResponse]) error
}

func (d *stubDriver) Status(context.Context, *driverv1.StatusRequest) (*driverv1.StatusResponse, error) {
	return &driverv1.StatusResponse{}, nil
}

func (d *stubDriver) Run(ctx context.Context, req *driverv1.RunRequest, stream *connect.ServerStream[driverv1.RunResponse]) error {
	return d.run(ctx, req, stream)
}

// addSandbox adds an up sandbox c1 to be and returns it.
func addSandbox(t *testing.T, be *fake.Backend) backend.Sandbox {
	t.Helper()
	sbx, err := be.Open(be.Add("c1", fake.Up))
	assert.NilError(t, err)
	return sbx
}

// openSandbox returns a RunStreamOptions.Open for run version on sbx, as
// TaskRunner's will be. It dials sbx's driver and asks it for Status, then
// opens a Run stream for version, with spec only while start is true. When
// the driver does not answer, it checks sbx's status: a sandbox that is not
// up ends the run with runner.ErrSandboxDown (wrapping backend.ErrGone if it
// is gone), and any other failure is returned to be retried.
func openSandbox(sbx backend.Sandbox, version int64, spec *driverv1.RunSpec) func(context.Context, bool) (*connect.ServerStreamForClient[driverv1.RunResponse], error) {
	return func(ctx context.Context, start bool) (*connect.ServerStreamForClient[driverv1.RunResponse], error) {
		client, err := sbx.Dial(ctx, "")
		if err == nil {
			if _, err = client.Status(ctx, &driverv1.StatusRequest{}); err == nil {
				req := &driverv1.RunRequest{Version: version}
				if start {
					req.Spec = spec
				}
				return client.Run(ctx, req)
			}
		}
		status, stErr := sbx.Status(ctx)
		switch {
		case stErr != nil:
			return nil, fmt.Errorf("%w (status: %w)", err, stErr)
		case status == backend.StatusGone:
			return nil, fmt.Errorf("%w: %w: %w", runner.ErrSandboxDown, backend.ErrGone, err)
		case status != backend.StatusUp:
			return nil, fmt.Errorf("%w (%v): %w", runner.ErrSandboxDown, status, err)
		}
		return nil, err
	}
}

// recordStarts wraps open to append the start of every call to starts.
func recordStarts(open func(context.Context, bool) (*connect.ServerStreamForClient[driverv1.RunResponse], error), starts *testx.SafeSlice[bool]) func(context.Context, bool) (*connect.ServerStreamForClient[driverv1.RunResponse], error) {
	return func(ctx context.Context, start bool) (*connect.ServerStreamForClient[driverv1.RunResponse], error) {
		starts.Append(start)
		return open(ctx, start)
	}
}

// dummySpec returns the spec of a run of task 1 by a dummy agent with opts,
// which provisions the agent's config where NewDriver's driver reads it.
func dummySpec(t *testing.T, opts *agent.DummyOptions) *driverv1.RunSpec {
	t.Helper()
	data, err := json.Marshal(&agent.Config{Type: agent.TypeDummy, Dummy: opts})
	assert.NilError(t, err)
	return &driverv1.RunSpec{
		TaskId:    1,
		ServerUrl: "http://gritz.test",
		Token:     "token",
		Files:     []*driverv1.File{{Path: agent.DefaultConfigStore.Path(1), Data: data, Mode: 0o666}},
	}
}

// streamAsync calls a RunStream's Run with opts on its own goroutine, and
// returns the channel its error is sent on, for waitDone.
func streamAsync(ctx context.Context, opts runner.RunStreamOptions) <-chan error {
	done := make(chan error, 1)
	go func() { done <- runner.NewRunStream(opts).Run(ctx) }()
	return done
}

func TestNewRunStream(t *testing.T) {
	t.Parallel()
	open := openSandbox(addSandbox(t, testrunner.NewBackend(t, nil)), 1, nil)
	onFinished := func(context.Context, *driverv1.Finished) error { return nil }
	onLost := func(context.Context, runner.LostReason, error) error { return nil }
	onUnreachable := func(context.Context, error) error { return nil }
	tests := []struct {
		name string
		opts runner.RunStreamOptions
	}{
		{name: "no Open", opts: runner.RunStreamOptions{OnFinished: onFinished, OnLost: onLost, OnUnreachable: onUnreachable}},
		{name: "no OnFinished", opts: runner.RunStreamOptions{Open: open, OnLost: onLost, OnUnreachable: onUnreachable}},
		{name: "no OnLost", opts: runner.RunStreamOptions{Open: open, OnFinished: onFinished, OnUnreachable: onUnreachable}},
		{name: "no OnUnreachable", opts: runner.RunStreamOptions{Open: open, OnFinished: onFinished, OnLost: onLost}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Act & Assert
			assert.Assert(t, cmp.Panics(func() { runner.NewRunStream(tt.opts) }))
		})
	}
}

func TestRunStream_StartFinished(t *testing.T) {
	t.Parallel()
	// Arrange - a driver that has never run, and a spec whose agent finishes.
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewDriver(t, nil)})
	sbx := addSandbox(t, be)
	rec := newRecorder()
	opts := rec.options(runner.RunStreamOptions{
		Open:             openSandbox(sbx, 1, dummySpec(t, nil)),
		Start:            true,
		RetryInterval:    10 * time.Millisecond,
		RetryMaxInterval: 50 * time.Millisecond,
	})

	// Act
	err := runner.NewRunStream(opts).Run(t.Context())

	// Assert - the start stream is kept and gets the Finished. RunStream left
	// the sandbox as it was.
	assert.NilError(t, err)
	calls := rec.calls.Slice()
	assert.DeepEqual(t, calls, []call{
		{Name: "accepted", Accepted: &driverv1.Accepted{}},
		{Name: "finished", Finished: &driverv1.Finished{Version: 1, Reported: true}},
	}, protocmp.Transform())
	assertTerminalLast(t, calls)
	info, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, info.Runs, 1)
	assert.Assert(t, cmp.Len(info.RunSpecs, 1))
	assert.Equal(t, info.Parks, 0)
	assert.Equal(t, info.State, fake.Up)
}

func TestRunStream_StartNotReady(t *testing.T) {
	t.Parallel()
	// Arrange - a driver that never becomes ready.
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewDriver(t, nil), DriverNotReady: true})
	sbx := addSandbox(t, be)
	rec := newRecorder()
	opts := rec.options(runner.RunStreamOptions{
		Open:          openSandbox(sbx, 1, dummySpec(t, nil)),
		Start:         true,
		RetryInterval: 10 * time.Millisecond,
		ReadyTimeout:  100 * time.Millisecond,
	})

	// Act
	err := runner.NewRunStream(opts).Run(t.Context())

	// Assert
	assert.NilError(t, err)
	calls := rec.calls.Slice()
	assert.DeepEqual(t, calls, []call{
		{Name: "lost", Reason: runner.LostStartFailed},
	}, ignoreErr)
	assertTerminalLast(t, calls)
	assert.ErrorContains(t, calls[0].Err, "driver server did not become ready")
	info, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, info.Parks, 0)
}

func TestRunStream_StartBecomesReady(t *testing.T) {
	t.Parallel()
	// Arrange - a driver that is not ready when the start reaches for it.
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewDriver(t, nil), DriverNotReady: true})
	sbx := addSandbox(t, be)
	rec := newRecorder()
	opts := rec.options(runner.RunStreamOptions{
		Open:             openSandbox(sbx, 1, dummySpec(t, nil)),
		Start:            true,
		RetryInterval:    10 * time.Millisecond,
		RetryMaxInterval: 50 * time.Millisecond,
	})
	done := streamAsync(t.Context(), opts)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err := be.Wait(ctx, func(s fake.Snapshot) bool {
		info, _ := s.Sandbox("c1")
		return info.Dials >= 1
	})
	assert.NilError(t, err)

	// Act - the driver comes up while the start polls it.
	be.SetDriverReady("c1", true)

	// Assert
	assert.NilError(t, waitDone(t, done))
	calls := rec.calls.Slice()
	assert.DeepEqual(t, calls, []call{
		{Name: "accepted", Accepted: &driverv1.Accepted{}},
		{Name: "finished", Finished: &driverv1.Finished{Version: 1, Reported: true}},
	}, protocmp.Transform())
	assertTerminalLast(t, calls)
}

func TestRunStream_StartNoAccepted(t *testing.T) {
	t.Parallel()
	// Arrange - a driver whose Run never sends anything.
	stub := &stubDriver{run: func(ctx context.Context, _ *driverv1.RunRequest, _ *connect.ServerStream[driverv1.RunResponse]) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewStubDriver(t, stub)})
	sbx := addSandbox(t, be)
	rec := newRecorder()
	opts := rec.options(runner.RunStreamOptions{
		Open:         openSandbox(sbx, 1, dummySpec(t, nil)),
		Start:        true,
		ReadyTimeout: 100 * time.Millisecond,
	})

	// Act
	err := runner.NewRunStream(opts).Run(t.Context())

	// Assert
	assert.NilError(t, err)
	calls := rec.calls.Slice()
	assert.DeepEqual(t, calls, []call{
		{Name: "lost", Reason: runner.LostStartFailed},
	}, ignoreErr)
	assertTerminalLast(t, calls)
	assert.ErrorContains(t, calls[0].Err, "driver server did not become ready")
}

func TestRunStream_StartFailed(t *testing.T) {
	t.Parallel()
	errDial := errors.New("dial failed")
	tests := []struct {
		name    string
		arrange func(be *fake.Backend)
		// run is the stub driver's Run.
		run  func(context.Context, *driverv1.RunRequest, *connect.ServerStream[driverv1.RunResponse]) error
		want func(t *testing.T, err error)
	}{
		{
			name:    "sandbox down",
			arrange: func(be *fake.Backend) { be.Set("c1", fake.Parked) },
			run: func(context.Context, *driverv1.RunRequest, *connect.ServerStream[driverv1.RunResponse]) error {
				return errors.New("unreachable")
			},
			want: func(t *testing.T, err error) { assert.ErrorIs(t, err, runner.ErrSandboxDown) },
		},
		{
			name:    "dial fails",
			arrange: func(be *fake.Backend) { be.FailNext(fake.OpDial, "c1", errDial) },
			run: func(context.Context, *driverv1.RunRequest, *connect.ServerStream[driverv1.RunResponse]) error {
				return errors.New("unreachable")
			},
			want: func(t *testing.T, err error) { assert.ErrorIs(t, err, errDial) },
		},
		{
			name:    "run rejected",
			arrange: func(*fake.Backend) {},
			run: func(context.Context, *driverv1.RunRequest, *connect.ServerStream[driverv1.RunResponse]) error {
				return connect.NewError(connect.CodeInternal, errors.New("cannot start"))
			},
			want: func(t *testing.T, err error) {
				assert.Equal(t, connect.CodeOf(err), connect.CodeInternal)
				assert.ErrorContains(t, err, "failed to start run")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewStubDriver(t, &stubDriver{run: tt.run})})
			sbx := addSandbox(t, be)
			tt.arrange(be)
			rec := newRecorder()
			opts := rec.options(runner.RunStreamOptions{
				Open:          openSandbox(sbx, 1, dummySpec(t, nil)),
				Start:         true,
				RetryInterval: 10 * time.Millisecond,
			})

			// Act
			err := runner.NewRunStream(opts).Run(t.Context())

			// Assert
			assert.NilError(t, err)
			calls := rec.calls.Slice()
			assert.DeepEqual(t, calls, []call{
				{Name: "lost", Reason: runner.LostStartFailed},
			}, ignoreErr)
			assertTerminalLast(t, calls)
			tt.want(t, calls[0].Err)
		})
	}
}

func TestRunStream_StartUnavailableRetried(t *testing.T) {
	t.Parallel()
	// Arrange - a driver that rejects the first start as Unavailable, and
	// accepts the second but ends its stream before the Finished, which a
	// reattach gets.
	var starts atomic.Int32
	stub := &stubDriver{run: func(_ context.Context, req *driverv1.RunRequest, stream *connect.ServerStream[driverv1.RunResponse]) error {
		if req.GetSpec() != nil {
			if starts.Add(1) == 1 {
				return connect.NewError(connect.CodeUnavailable, errors.New("another Run stream is open"))
			}
			return stream.Send(&driverv1.RunResponse{Event: &driverv1.RunResponse_Accepted{Accepted: &driverv1.Accepted{}}})
		}
		if err := stream.Send(&driverv1.RunResponse{Event: &driverv1.RunResponse_Accepted{Accepted: &driverv1.Accepted{Attached: true}}}); err != nil {
			return err
		}
		return stream.Send(&driverv1.RunResponse{Event: &driverv1.RunResponse_Finished{Finished: &driverv1.Finished{Version: 1, Reported: true}}})
	}}
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewStubDriver(t, stub)})
	sbx := addSandbox(t, be)
	var opens testx.SafeSlice[bool]
	rec := newRecorder()
	opts := rec.options(runner.RunStreamOptions{
		Open:          recordStarts(openSandbox(sbx, 1, dummySpec(t, nil)), &opens),
		Start:         true,
		RetryInterval: 10 * time.Millisecond,
	})

	// Act
	err := runner.NewRunStream(opts).Run(t.Context())

	// Assert - the retried start still carried the spec; the reattach after
	// the Accepted did not.
	assert.NilError(t, err)
	calls := rec.calls.Slice()
	assert.DeepEqual(t, calls, []call{
		{Name: "accepted", Accepted: &driverv1.Accepted{}},
		{Name: "accepted", Accepted: &driverv1.Accepted{Attached: true}},
		{Name: "finished", Finished: &driverv1.Finished{Version: 1, Reported: true}},
	}, protocmp.Transform())
	assertTerminalLast(t, calls)
	assert.DeepEqual(t, opens.Slice(), []bool{true, true, false})
	// OnDropped saw the rejected start and the start stream that ended.
	dropped := rec.dropped.Slice()
	assert.Assert(t, cmp.Len(dropped, 2))
	assert.Equal(t, connect.CodeOf(dropped[0]), connect.CodeUnavailable)
	assert.ErrorContains(t, dropped[1], "run stream ended before Finished")
	assert.Equal(t, starts.Load(), int32(2))
	info, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, info.Runs, 3)
	assert.Assert(t, cmp.Len(info.RunSpecs, 2))
}

func TestRunStream_StartReattach(t *testing.T) {
	t.Parallel()
	// Arrange - a started run that keeps going.
	client := testrunner.NewDriver(t, nil)
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	sbx := addSandbox(t, be)
	var opens testx.SafeSlice[bool]
	rec := newRecorder()
	opts := rec.options(runner.RunStreamOptions{
		Open:             recordStarts(openSandbox(sbx, 1, dummySpec(t, testrunner.SleepForever.Dummy)), &opens),
		Start:            true,
		RetryInterval:    10 * time.Millisecond,
		RetryMaxInterval: 50 * time.Millisecond,
	})
	done := streamAsync(t.Context(), opts)
	rec.waitAccepted(t)

	// Act - drop the start stream, then stop the run once it is reattached.
	be.Drop("c1")
	rec.waitAccepted(t)
	testrunner.StopRun(t, client, 1)

	// Assert - the reattach did not carry the spec.
	assert.NilError(t, waitDone(t, done))
	calls := rec.calls.Slice()
	assert.DeepEqual(t, calls, []call{
		{Name: "accepted", Accepted: &driverv1.Accepted{}},
		{Name: "accepted", Accepted: &driverv1.Accepted{Attached: true}},
		{Name: "finished", Finished: &driverv1.Finished{Version: 1, Reported: true}},
	}, protocmp.Transform())
	assertTerminalLast(t, calls)
	assert.DeepEqual(t, opens.Slice(), []bool{true, false})
	info, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, info.Runs, 2)
	assert.Assert(t, cmp.Len(info.RunSpecs, 1))
}

func TestRunStream_Finished(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		driver   func(t *testing.T) driverv1connect.DriverServiceClient
		reported bool
	}{
		{
			name: "reported",
			driver: func(t *testing.T) driverv1connect.DriverServiceClient {
				return testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3, Agent: &agent.Config{Type: agent.TypeDummy}})
			},
			reported: true,
		},
		{
			name: "not reported",
			driver: func(t *testing.T) driverv1connect.DriverServiceClient {
				return testrunner.NewStubDriver(t, &stubDriver{run: func(_ context.Context, _ *driverv1.RunRequest, stream *connect.ServerStream[driverv1.RunResponse]) error {
					if err := stream.Send(&driverv1.RunResponse{Event: &driverv1.RunResponse_Accepted{Accepted: &driverv1.Accepted{Attached: true}}}); err != nil {
						return err
					}
					return stream.Send(&driverv1.RunResponse{Event: &driverv1.RunResponse_Finished{Finished: &driverv1.Finished{Version: 3}}})
				}})
			},
			reported: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: tt.driver(t)})
			sbx := addSandbox(t, be)
			rec := newRecorder()
			opts := rec.options(runner.RunStreamOptions{
				Open:             openSandbox(sbx, 3, nil),
				RetryInterval:    10 * time.Millisecond,
				RetryMaxInterval: 50 * time.Millisecond,
			})

			// Act
			err := runner.NewRunStream(opts).Run(t.Context())

			// Assert
			assert.NilError(t, err)
			calls := rec.calls.Slice()
			assert.DeepEqual(t, calls, []call{
				{Name: "accepted", Accepted: &driverv1.Accepted{Attached: true}},
				{Name: "finished", Finished: &driverv1.Finished{Version: 3, Reported: tt.reported}},
			}, protocmp.Transform())
			assertTerminalLast(t, calls)
			info, _ := be.Snapshot().Sandbox("c1")
			assert.Equal(t, info.Parks, 0)
		})
	}
}

func TestRunStream_Terminal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		driver *testrunner.DriverOptions
		reason runner.LostReason
		code   connect.Code
	}{
		{name: "no run", driver: &testrunner.DriverOptions{Version: 3}, reason: runner.LostNoRun, code: connect.CodeNotFound},
		{name: "newer run", driver: &testrunner.DriverOptions{Version: 4, Agent: testrunner.SleepForever}, reason: runner.LostSuperseded, code: connect.CodeFailedPrecondition},
		{name: "older run running", driver: &testrunner.DriverOptions{Version: 2, Agent: testrunner.SleepForever}, reason: runner.LostSuperseded, code: connect.CodeAborted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewDriver(t, tt.driver)})
			sbx := addSandbox(t, be)
			rec := newRecorder()
			opts := rec.options(runner.RunStreamOptions{
				Open:             openSandbox(sbx, 3, nil),
				RetryInterval:    10 * time.Millisecond,
				RetryMaxInterval: 50 * time.Millisecond,
			})

			// Act
			err := runner.NewRunStream(opts).Run(t.Context())

			// Assert
			assert.NilError(t, err)
			calls := rec.calls.Slice()
			assert.DeepEqual(t, calls, []call{
				{Name: "lost", Reason: tt.reason},
			}, ignoreErr)
			assertTerminalLast(t, calls)
			assert.Equal(t, connect.CodeOf(calls[0].Err), tt.code)
			info, _ := be.Snapshot().Sandbox("c1")
			assert.Equal(t, info.Parks, 0)
		})
	}
}

func TestRunStream_SandboxStopped(t *testing.T) {
	t.Parallel()
	tests := []struct {
		state fake.State
		gone  bool
	}{
		{state: fake.Parked},
		{state: fake.Gone, gone: true},
	}
	for _, tt := range tests {
		t.Run(tt.state.String(), func(t *testing.T) {
			t.Parallel()
			// Arrange - a run that keeps going.
			client := testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3, Agent: testrunner.SleepForever})
			be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
			sbx := addSandbox(t, be)
			rec := newRecorder()
			opts := rec.options(runner.RunStreamOptions{
				Open:             openSandbox(sbx, 3, nil),
				RetryInterval:    10 * time.Millisecond,
				RetryMaxInterval: 50 * time.Millisecond,
			})
			done := streamAsync(t.Context(), opts)
			rec.waitAccepted(t)

			// Act - the platform stops the sandbox under the stream.
			be.Set("c1", tt.state)

			// Assert - the reattach's Open gave up; RunStream left the sandbox
			// as the platform did.
			assert.NilError(t, waitDone(t, done))
			calls := rec.calls.Slice()
			assert.DeepEqual(t, calls, []call{
				{Name: "accepted", Accepted: &driverv1.Accepted{Attached: true}},
				{Name: "lost", Reason: runner.LostSandboxDown},
			}, protocmp.Transform(), ignoreErr)
			assertTerminalLast(t, calls)
			assert.ErrorIs(t, calls[1].Err, runner.ErrSandboxDown)
			assert.Equal(t, errors.Is(calls[1].Err, backend.ErrGone), tt.gone)
			info, _ := be.Snapshot().Sandbox("c1")
			assert.Equal(t, info.State, tt.state)
			assert.Equal(t, info.Parks, 0)
			assert.Equal(t, info.Destroys, 0)
		})
	}
}

func TestRunStream_OpenErrorRetries(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going, and an Open whose second call fails.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3, Agent: testrunner.SleepForever})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	open := openSandbox(addSandbox(t, be), 3, nil)
	errOpen := errors.New("open failed")
	var opens atomic.Int32
	rec := newRecorder()
	opts := rec.options(runner.RunStreamOptions{
		Open: func(ctx context.Context, start bool) (*connect.ServerStreamForClient[driverv1.RunResponse], error) {
			if opens.Add(1) == 2 {
				return nil, errOpen
			}
			return open(ctx, start)
		},
		RetryInterval:    10 * time.Millisecond,
		RetryMaxInterval: 50 * time.Millisecond,
	})
	done := streamAsync(t.Context(), opts)
	rec.waitAccepted(t)

	// Act - drop the stream, so the failing Open follows; then stop the run
	// once it is reattached.
	be.Drop("c1")
	rec.waitAccepted(t)
	testrunner.StopRun(t, client, 3)

	// Assert - the failed Open was retried, not taken for a lost run.
	assert.NilError(t, waitDone(t, done))
	calls := rec.calls.Slice()
	assert.DeepEqual(t, calls, []call{
		{Name: "accepted", Accepted: &driverv1.Accepted{Attached: true}},
		{Name: "accepted", Accepted: &driverv1.Accepted{Attached: true}},
		{Name: "finished", Finished: &driverv1.Finished{Version: 3, Reported: true}},
	}, protocmp.Transform())
	assertTerminalLast(t, calls)
	assert.Assert(t, opens.Load() >= 3)
	// OnDropped saw the drop, then the failed Open, each before its retry.
	dropped := rec.dropped.Slice()
	assert.Assert(t, cmp.Len(dropped, 2))
	assert.ErrorIs(t, dropped[1], errOpen)
}

func TestRunStream_Unreachable(t *testing.T) {
	t.Parallel()
	// Arrange - a sandbox that stays up while its driver never answers.
	// OnUnreachable gives up at once, with the caller's own error.
	be := testrunner.NewBackend(t, nil)
	sbx := addSandbox(t, be)
	errGiveUp := errors.New("driver unreachable")
	rec := newRecorder()
	rec.errs = map[string]error{"unreachable": errGiveUp}
	opts := rec.options(runner.RunStreamOptions{
		Open:             openSandbox(sbx, 3, nil),
		RetryInterval:    10 * time.Millisecond,
		RetryMaxInterval: 50 * time.Millisecond,
		LostTimeout:      100 * time.Millisecond,
	})

	// Act
	err := runner.NewRunStream(opts).Run(t.Context())

	// Assert - Run returned OnUnreachable's error as is, after no terminal
	// callback; RunStream did not park.
	assert.Equal(t, err, errGiveUp)
	calls := rec.calls.Slice()
	assert.DeepEqual(t, calls, []call{
		{Name: "unreachable"},
	}, ignoreErr)
	assert.Equal(t, connect.CodeOf(calls[0].Err), connect.CodeUnavailable)
	info, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, info.State, fake.Up)
	assert.Equal(t, info.Parks, 0)
}

func TestRunStream_UnreachableKeepGoing(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going, behind a driver that does not answer
	// until the third OnUnreachable makes it ready. OnUnreachable always
	// returns nil, so the run is never given up.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3, Agent: testrunner.SleepForever})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client, DriverNotReady: true})
	sbx := addSandbox(t, be)
	rec := newRecorder()
	rec.unreachable = func(n int) {
		if n == 3 {
			be.SetDriverReady("c1", true)
		}
	}
	opts := rec.options(runner.RunStreamOptions{
		Open:             openSandbox(sbx, 3, nil),
		RetryInterval:    10 * time.Millisecond,
		RetryMaxInterval: 50 * time.Millisecond,
		LostTimeout:      100 * time.Millisecond,
	})
	done := streamAsync(t.Context(), opts)

	// Act - stop the run once the driver is back.
	rec.waitAccepted(t)
	testrunner.StopRun(t, client, 3)

	// Assert - following went on after each nil, until the Finished. The
	// driver may still turn a reattach away while its previous stream
	// closes, so there can be more than three OnUnreachable.
	assert.NilError(t, waitDone(t, done))
	calls := rec.calls.Slice()
	assert.Assert(t, len(calls) >= 5, "calls: %v", calls)
	unreachable := calls[:len(calls)-2]
	for _, c := range unreachable {
		assert.DeepEqual(t, c, call{Name: "unreachable"}, ignoreErr)
	}
	assert.Assert(t, len(unreachable) >= 3)
	assert.DeepEqual(t, calls[len(calls)-2:], []call{
		{Name: "accepted", Accepted: &driverv1.Accepted{Attached: true}},
		{Name: "finished", Finished: &driverv1.Finished{Version: 3, Reported: true}},
	}, protocmp.Transform())
	assertTerminalLast(t, calls)
}

func TestRunStream_LostTimeoutReset(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going, and a LostTimeout shorter than the
	// streams take in total.
	const lostTimeout = 200 * time.Millisecond
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3, Agent: testrunner.SleepForever})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	sbx := addSandbox(t, be)
	rec := newRecorder()
	opts := rec.options(runner.RunStreamOptions{
		Open:             openSandbox(sbx, 3, nil),
		RetryInterval:    10 * time.Millisecond,
		RetryMaxInterval: 50 * time.Millisecond,
		LostTimeout:      lostTimeout,
	})
	start := time.Now()
	done := streamAsync(t.Context(), opts)

	// Act - drop each accepted stream, for longer than LostTimeout in total,
	// then stop the run.
	drops := 0
	for time.Since(start) < 3*lostTimeout {
		rec.waitAccepted(t)
		be.Drop("c1")
		drops++
	}
	rec.waitAccepted(t)
	testrunner.StopRun(t, client, 3)

	// Assert - every accepted stream reset the clock, so OnUnreachable was
	// never called.
	assert.NilError(t, waitDone(t, done))
	calls := rec.calls.Slice()
	assert.Assert(t, cmp.Len(calls, drops+2))
	for _, c := range calls[:drops+1] {
		assert.DeepEqual(t, c, call{Name: "accepted", Accepted: &driverv1.Accepted{Attached: true}}, protocmp.Transform())
	}
	assert.DeepEqual(t, calls[drops+1], call{Name: "finished", Finished: &driverv1.Finished{Version: 3, Reported: true}}, protocmp.Transform())
	assertTerminalLast(t, calls)
}

func TestRunStream_Cancelled(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3, Agent: testrunner.SleepForever})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	sbx := addSandbox(t, be)
	rec := newRecorder()
	opts := rec.options(runner.RunStreamOptions{
		Open:             openSandbox(sbx, 3, nil),
		RetryInterval:    10 * time.Millisecond,
		RetryMaxInterval: 50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	done := streamAsync(ctx, opts)
	rec.waitAccepted(t)
	errCause := errors.New("removed")

	// Act
	cancel(errCause)

	// Assert - Run returns the cause, with no terminal callback; the sandbox
	// is untouched.
	assert.ErrorIs(t, waitDone(t, done), errCause)
	assert.DeepEqual(t, rec.calls.Slice(), []call{
		{Name: "accepted", Accepted: &driverv1.Accepted{Attached: true}},
	}, protocmp.Transform())
	info, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, info.State, fake.Up)
	assert.Equal(t, info.Parks, 0)
}

func TestRunStream_CallbackError(t *testing.T) {
	t.Parallel()
	errCallback := errors.New("callback failed")
	tests := []struct {
		name string
		// fail names the callback that returns errCallback.
		fail string
		// driver is the sandbox's driver; nil never answers.
		driver *testrunner.DriverOptions
		// spec starts the run.
		spec bool
		want []call
	}{
		{
			name:   "accepted",
			fail:   "accepted",
			driver: &testrunner.DriverOptions{Version: 1, Agent: testrunner.SleepForever},
			want:   []call{{Name: "accepted", Accepted: &driverv1.Accepted{Attached: true}}},
		},
		{
			name:   "accepted on start",
			fail:   "accepted",
			driver: &testrunner.DriverOptions{},
			spec:   true,
			want:   []call{{Name: "accepted", Accepted: &driverv1.Accepted{}}},
		},
		{
			name:   "finished",
			fail:   "finished",
			driver: &testrunner.DriverOptions{Version: 1, Agent: &agent.Config{Type: agent.TypeDummy}},
			want: []call{
				{Name: "accepted", Accepted: &driverv1.Accepted{Attached: true}},
				{Name: "finished", Finished: &driverv1.Finished{Version: 1, Reported: true}},
			},
		},
		{
			name:   "lost",
			fail:   "lost",
			driver: &testrunner.DriverOptions{Version: 1},
			want:   []call{{Name: "lost", Reason: runner.LostNoRun}},
		},
		{
			name: "unreachable",
			fail: "unreachable",
			want: []call{{Name: "unreachable"}},
		},
		{
			name: "dropped",
			fail: "dropped",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange - OnUnreachable returns nil unless it is the one that
			// fails, so only its error can end the run there. Without a driver, the first Open fails and
			// is dropped.
			var driver driverv1connect.DriverServiceClient
			if tt.driver != nil {
				driver = testrunner.NewDriver(t, tt.driver)
			}
			be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: driver})
			sbx := addSandbox(t, be)
			rec := newRecorder()
			rec.errs = map[string]error{tt.fail: errCallback}
			var spec *driverv1.RunSpec
			if tt.spec {
				spec = dummySpec(t, nil)
			}
			opts := rec.options(runner.RunStreamOptions{
				Open:             openSandbox(sbx, 1, spec),
				Start:            spec != nil,
				RetryInterval:    10 * time.Millisecond,
				RetryMaxInterval: 50 * time.Millisecond,
				LostTimeout:      100 * time.Millisecond,
			})

			// Act
			err := runner.NewRunStream(opts).Run(t.Context())

			// Assert - Run stopped at the failing callback and returned its
			// error.
			assert.ErrorIs(t, err, errCallback)
			assert.DeepEqual(t, rec.calls.Slice(), tt.want, protocmp.Transform(), ignoreErr, cmpopts.EquateEmpty())
			if tt.fail == "dropped" {
				// The first Open failed, and nothing was retried after it.
				assert.Assert(t, cmp.Len(rec.dropped.Slice(), 1))
			}
		})
	}
}
