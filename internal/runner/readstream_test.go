package runner_test

import (
	"context"
	"encoding/json"
	"errors"
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

// call is one callback a ReadStream made, as a recorder records it.
type call struct {
	// Name is "accepted", "finished", "lost" or "unreachable".
	Name     string
	Accepted *driverv1.Accepted
	Finished *driverv1.Finished
	// Reason is OnLost's reason.
	Reason runner.LostReason
	// Err is OnLost's or OnUnreachable's error.
	Err error
	// Stop is what OnUnreachable returned.
	Stop bool
}

// terminal reports whether c ends the run.
func (c call) terminal() bool {
	return c.Name == "finished" || c.Name == "lost" || c.Name == "unreachable" && c.Stop
}

// ignoreErr drops call.Err from a comparison of recorded calls; tests assert
// it on its own.
var ignoreErr = cmpopts.IgnoreFields(call{}, "Err")

// recorder records a ReadStream's callbacks.
type recorder struct {
	calls testx.SafeSlice[call]
	// accepted receives every Accepted once it is recorded.
	accepted chan *driverv1.Accepted
	// unreachable returns what the n-th OnUnreachable returns, counting from
	// 1; nil stops at once.
	unreachable func(n int) bool
	// unreachables counts the OnUnreachable calls.
	unreachables int
}

func newRecorder() *recorder {
	return &recorder{accepted: make(chan *driverv1.Accepted, 1000)}
}

// options returns opts with r's callbacks.
func (r *recorder) options(opts runner.ReadStreamOptions) runner.ReadStreamOptions {
	opts.OnAccepted = func(_ context.Context, a *driverv1.Accepted) {
		r.calls.Append(call{Name: "accepted", Accepted: a})
		r.accepted <- a
	}
	opts.OnFinished = func(_ context.Context, f *driverv1.Finished) {
		r.calls.Append(call{Name: "finished", Finished: f})
	}
	opts.OnLost = func(_ context.Context, reason runner.LostReason, err error) {
		r.calls.Append(call{Name: "lost", Reason: reason, Err: err})
	}
	opts.OnUnreachable = func(_ context.Context, err error) bool {
		r.unreachables++
		stop := r.unreachable == nil || r.unreachable(r.unreachables)
		r.calls.Append(call{Name: "unreachable", Err: err, Stop: stop})
		return stop
	}
	return opts
}

// waitAccepted waits for the ReadStream's next OnAccepted.
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

// readAsync calls ReadStream with opts on its own goroutine, and returns the
// channel its error is sent on, for waitDone.
func readAsync(ctx context.Context, opts runner.ReadStreamOptions) <-chan error {
	done := make(chan error, 1)
	go func() { done <- runner.ReadStream(ctx, opts) }()
	return done
}

func TestReadStream_Required(t *testing.T) {
	t.Parallel()
	sbx := addSandbox(t, testrunner.NewBackend(t, nil))
	onFinished := func(context.Context, *driverv1.Finished) {}
	onLost := func(context.Context, runner.LostReason, error) {}
	onUnreachable := func(context.Context, error) bool { return true }
	tests := []struct {
		name string
		opts runner.ReadStreamOptions
	}{
		{name: "no sandbox", opts: runner.ReadStreamOptions{OnFinished: onFinished, OnLost: onLost, OnUnreachable: onUnreachable}},
		{name: "no OnFinished", opts: runner.ReadStreamOptions{Sandbox: sbx, OnLost: onLost, OnUnreachable: onUnreachable}},
		{name: "no OnLost", opts: runner.ReadStreamOptions{Sandbox: sbx, OnFinished: onFinished, OnUnreachable: onUnreachable}},
		{name: "no OnUnreachable", opts: runner.ReadStreamOptions{Sandbox: sbx, OnFinished: onFinished, OnLost: onLost}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Act & Assert
			assert.Assert(t, cmp.Panics(func() { _ = runner.ReadStream(t.Context(), tt.opts) }))
		})
	}
}

func TestReadStream_StartFinished(t *testing.T) {
	t.Parallel()
	// Arrange - a driver that has never run, and a spec whose agent finishes.
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewDriver(t, nil)})
	sbx := addSandbox(t, be)
	rec := newRecorder()
	opts := rec.options(runner.ReadStreamOptions{
		Sandbox:          sbx,
		Version:          1,
		Spec:             dummySpec(t, nil),
		RetryInterval:    10 * time.Millisecond,
		RetryMaxInterval: 50 * time.Millisecond,
	})

	// Act
	err := runner.ReadStream(t.Context(), opts)

	// Assert - the start stream is accepted and closed, and a second stream
	// reattaches and gets the Finished. ReadStream left the sandbox as it was.
	assert.NilError(t, err)
	calls := rec.calls.Slice()
	assert.DeepEqual(t, calls, []call{
		{Name: "accepted", Accepted: &driverv1.Accepted{}},
		{Name: "accepted", Accepted: &driverv1.Accepted{Attached: true}},
		{Name: "finished", Finished: &driverv1.Finished{Version: 1, Reported: true}},
	}, protocmp.Transform())
	assertTerminalLast(t, calls)
	info, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, info.Runs, 2)
	assert.Assert(t, cmp.Len(info.RunSpecs, 1))
	assert.Equal(t, info.Parks, 0)
	assert.Equal(t, info.State, fake.Up)
}

func TestReadStream_StartNotReady(t *testing.T) {
	t.Parallel()
	// Arrange - a driver that never becomes ready.
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewDriver(t, nil), DriverNotReady: true})
	sbx := addSandbox(t, be)
	rec := newRecorder()
	opts := rec.options(runner.ReadStreamOptions{
		Sandbox:       sbx,
		Version:       1,
		Spec:          dummySpec(t, nil),
		RetryInterval: 10 * time.Millisecond,
		ReadyTimeout:  100 * time.Millisecond,
	})

	// Act
	err := runner.ReadStream(t.Context(), opts)

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

func TestReadStream_StartBecomesReady(t *testing.T) {
	t.Parallel()
	// Arrange - a driver that is not ready when the start reaches for it.
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewDriver(t, nil), DriverNotReady: true})
	sbx := addSandbox(t, be)
	rec := newRecorder()
	opts := rec.options(runner.ReadStreamOptions{
		Sandbox:          sbx,
		Version:          1,
		Spec:             dummySpec(t, nil),
		RetryInterval:    10 * time.Millisecond,
		RetryMaxInterval: 50 * time.Millisecond,
	})
	done := readAsync(t.Context(), opts)
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
		{Name: "accepted", Accepted: &driverv1.Accepted{Attached: true}},
		{Name: "finished", Finished: &driverv1.Finished{Version: 1, Reported: true}},
	}, protocmp.Transform())
	assertTerminalLast(t, calls)
}

func TestReadStream_StartNoAccepted(t *testing.T) {
	t.Parallel()
	// Arrange - a driver whose Run never sends anything.
	stub := &stubDriver{run: func(ctx context.Context, _ *driverv1.RunRequest, _ *connect.ServerStream[driverv1.RunResponse]) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: testrunner.NewStubDriver(t, stub)})
	sbx := addSandbox(t, be)
	rec := newRecorder()
	opts := rec.options(runner.ReadStreamOptions{
		Sandbox:      sbx,
		Version:      1,
		Spec:         dummySpec(t, nil),
		ReadyTimeout: 100 * time.Millisecond,
	})

	// Act
	err := runner.ReadStream(t.Context(), opts)

	// Assert
	assert.NilError(t, err)
	calls := rec.calls.Slice()
	assert.DeepEqual(t, calls, []call{
		{Name: "lost", Reason: runner.LostStartFailed},
	}, ignoreErr)
	assertTerminalLast(t, calls)
	assert.ErrorContains(t, calls[0].Err, "failed to start run 1")
}

func TestReadStream_StartFailed(t *testing.T) {
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
				assert.ErrorContains(t, err, "failed to start run 1")
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
			opts := rec.options(runner.ReadStreamOptions{
				Sandbox:       sbx,
				Version:       1,
				Spec:          dummySpec(t, nil),
				RetryInterval: 10 * time.Millisecond,
			})

			// Act
			err := runner.ReadStream(t.Context(), opts)

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

func TestReadStream_StartUnavailableRetried(t *testing.T) {
	t.Parallel()
	// Arrange - a driver that rejects the first start as Unavailable, accepts
	// the second, and finishes the run for the reattach.
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
	rec := newRecorder()
	opts := rec.options(runner.ReadStreamOptions{
		Sandbox:       sbx,
		Version:       1,
		Spec:          dummySpec(t, nil),
		RetryInterval: 10 * time.Millisecond,
	})

	// Act
	err := runner.ReadStream(t.Context(), opts)

	// Assert - two starts, then the reattach.
	assert.NilError(t, err)
	calls := rec.calls.Slice()
	assert.DeepEqual(t, calls, []call{
		{Name: "accepted", Accepted: &driverv1.Accepted{}},
		{Name: "accepted", Accepted: &driverv1.Accepted{Attached: true}},
		{Name: "finished", Finished: &driverv1.Finished{Version: 1, Reported: true}},
	}, protocmp.Transform())
	assertTerminalLast(t, calls)
	assert.Equal(t, starts.Load(), int32(2))
	info, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, info.Runs, 3)
	assert.Assert(t, cmp.Len(info.RunSpecs, 2))
}

func TestReadStream_Finished(t *testing.T) {
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
			opts := rec.options(runner.ReadStreamOptions{
				Sandbox:          sbx,
				Version:          3,
				RetryInterval:    10 * time.Millisecond,
				RetryMaxInterval: 50 * time.Millisecond,
			})

			// Act
			err := runner.ReadStream(t.Context(), opts)

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

func TestReadStream_Terminal(t *testing.T) {
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
			opts := rec.options(runner.ReadStreamOptions{
				Sandbox:          sbx,
				Version:          3,
				RetryInterval:    10 * time.Millisecond,
				RetryMaxInterval: 50 * time.Millisecond,
			})

			// Act
			err := runner.ReadStream(t.Context(), opts)

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

func TestReadStream_SandboxStopped(t *testing.T) {
	t.Parallel()
	tests := []struct {
		state  fake.State
		reason runner.LostReason
	}{
		{state: fake.Parked, reason: runner.LostSandboxParked},
		{state: fake.Gone, reason: runner.LostSandboxGone},
	}
	for _, tt := range tests {
		t.Run(tt.state.String(), func(t *testing.T) {
			t.Parallel()
			// Arrange - a run that keeps going.
			client := testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3, Agent: testrunner.SleepForever})
			be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
			sbx := addSandbox(t, be)
			rec := newRecorder()
			opts := rec.options(runner.ReadStreamOptions{
				Sandbox:          sbx,
				Version:          3,
				RetryInterval:    10 * time.Millisecond,
				RetryMaxInterval: 50 * time.Millisecond,
			})
			done := readAsync(t.Context(), opts)
			rec.waitAccepted(t)

			// Act - the platform stops the sandbox under the stream.
			be.Set("c1", tt.state)

			// Assert - ReadStream left the sandbox as the platform did.
			assert.NilError(t, waitDone(t, done))
			calls := rec.calls.Slice()
			assert.DeepEqual(t, calls, []call{
				{Name: "accepted", Accepted: &driverv1.Accepted{Attached: true}},
				{Name: "lost", Reason: tt.reason},
			}, protocmp.Transform(), ignoreErr)
			assertTerminalLast(t, calls)
			info, _ := be.Snapshot().Sandbox("c1")
			assert.Equal(t, info.State, tt.state)
			assert.Equal(t, info.Parks, 0)
			assert.Equal(t, info.Destroys, 0)
		})
	}
}

func TestReadStream_StatusErrorRetries(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going, on a sandbox whose next Status fails.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3, Agent: testrunner.SleepForever})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	sbx := addSandbox(t, be)
	be.FailNext(fake.OpStatus, "c1", errors.New("status failed"))
	rec := newRecorder()
	opts := rec.options(runner.ReadStreamOptions{
		Sandbox:          sbx,
		Version:          3,
		RetryInterval:    10 * time.Millisecond,
		RetryMaxInterval: 50 * time.Millisecond,
	})
	done := readAsync(t.Context(), opts)
	rec.waitAccepted(t)

	// Act - drop the stream, so the failing Status follows; then stop the run
	// once it is reattached.
	be.Drop("c1")
	rec.waitAccepted(t)
	testrunner.StopRun(t, client, 3)

	// Assert - the failed Status was not taken for a stopped sandbox.
	assert.NilError(t, waitDone(t, done))
	calls := rec.calls.Slice()
	assert.DeepEqual(t, calls, []call{
		{Name: "accepted", Accepted: &driverv1.Accepted{Attached: true}},
		{Name: "accepted", Accepted: &driverv1.Accepted{Attached: true}},
		{Name: "finished", Finished: &driverv1.Finished{Version: 3, Reported: true}},
	}, protocmp.Transform())
	assertTerminalLast(t, calls)
	info, _ := be.Snapshot().Sandbox("c1")
	assert.Assert(t, info.Runs >= 2)
}

func TestReadStream_Unreachable(t *testing.T) {
	t.Parallel()
	// Arrange - a sandbox that stays up while its driver never answers.
	be := testrunner.NewBackend(t, nil)
	sbx := addSandbox(t, be)
	rec := newRecorder()
	opts := rec.options(runner.ReadStreamOptions{
		Sandbox:          sbx,
		Version:          3,
		RetryInterval:    10 * time.Millisecond,
		RetryMaxInterval: 50 * time.Millisecond,
		LostTimeout:      100 * time.Millisecond,
	})

	// Act
	err := runner.ReadStream(t.Context(), opts)

	// Assert - OnUnreachable stopped the run; ReadStream did not park.
	assert.NilError(t, err)
	calls := rec.calls.Slice()
	assert.DeepEqual(t, calls, []call{
		{Name: "unreachable", Stop: true},
	}, ignoreErr)
	assertTerminalLast(t, calls)
	assert.Equal(t, connect.CodeOf(calls[0].Err), connect.CodeUnavailable)
	info, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, info.State, fake.Up)
	assert.Equal(t, info.Parks, 0)
}

func TestReadStream_UnreachableKeepGoing(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going, behind a driver that does not answer
	// until the third OnUnreachable makes it ready. OnUnreachable never stops
	// the run.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3, Agent: testrunner.SleepForever})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client, DriverNotReady: true})
	sbx := addSandbox(t, be)
	rec := newRecorder()
	rec.unreachable = func(n int) bool {
		if n == 3 {
			be.SetDriverReady("c1", true)
		}
		return false
	}
	opts := rec.options(runner.ReadStreamOptions{
		Sandbox:          sbx,
		Version:          3,
		RetryInterval:    10 * time.Millisecond,
		RetryMaxInterval: 50 * time.Millisecond,
		LostTimeout:      100 * time.Millisecond,
	})
	done := readAsync(t.Context(), opts)

	// Act - stop the run once the driver is back.
	rec.waitAccepted(t)
	testrunner.StopRun(t, client, 3)

	// Assert - following went on after each false, until the Finished. The
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

func TestReadStream_LostTimeoutReset(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going, and a LostTimeout shorter than the
	// streams take in total.
	const lostTimeout = 200 * time.Millisecond
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3, Agent: testrunner.SleepForever})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	sbx := addSandbox(t, be)
	rec := newRecorder()
	opts := rec.options(runner.ReadStreamOptions{
		Sandbox:          sbx,
		Version:          3,
		RetryInterval:    10 * time.Millisecond,
		RetryMaxInterval: 50 * time.Millisecond,
		LostTimeout:      lostTimeout,
	})
	start := time.Now()
	done := readAsync(t.Context(), opts)

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

func TestReadStream_Cancelled(t *testing.T) {
	t.Parallel()
	// Arrange - a run that keeps going.
	client := testrunner.NewDriver(t, &testrunner.DriverOptions{Version: 3, Agent: testrunner.SleepForever})
	be := testrunner.NewBackend(t, &testrunner.BackendOptions{Driver: client})
	sbx := addSandbox(t, be)
	rec := newRecorder()
	opts := rec.options(runner.ReadStreamOptions{
		Sandbox:          sbx,
		Version:          3,
		RetryInterval:    10 * time.Millisecond,
		RetryMaxInterval: 50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	done := readAsync(ctx, opts)
	rec.waitAccepted(t)
	errCause := errors.New("removed")

	// Act
	cancel(errCause)

	// Assert - ReadStream returns the cause, with no terminal callback; the sandbox
	// is untouched.
	assert.ErrorIs(t, waitDone(t, done), errCause)
	assert.DeepEqual(t, rec.calls.Slice(), []call{
		{Name: "accepted", Accepted: &driverv1.Accepted{Attached: true}},
	}, protocmp.Transform())
	info, _ := be.Snapshot().Sandbox("c1")
	assert.Equal(t, info.State, fake.Up)
	assert.Equal(t, info.Parks, 0)
}
