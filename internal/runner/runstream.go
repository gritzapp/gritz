package runner

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/x/common"
)

// RunStreamOptions configures a RunStream.
type RunStreamOptions struct {
	// Sandbox runs the driver. Required.
	Sandbox backend.Sandbox
	// Version is the run to follow.
	Version int64
	// Spec starts the run; nil reattaches to a run already started.
	Spec *driverv1.RunSpec
	// Log is the logger; defaults to slog.Default().
	Log *slog.Logger
	// RetryInterval is the first pause between attempts to reach the driver
	// or reattach to the run; defaults to 250ms.
	RetryInterval time.Duration
	// RetryMaxInterval caps the reattach backoff; defaults to 10s.
	RetryMaxInterval time.Duration
	// LostTimeout is how long reattaching goes on with no accepted stream
	// before OnUnreachable is called; defaults to 2m.
	LostTimeout time.Duration
	// ReadyTimeout bounds starting the run, up to its Accepted; defaults to
	// 1m.
	ReadyTimeout time.Duration

	// OnAccepted is called when the driver accepts a stream; optional.
	OnAccepted func(ctx context.Context, a *driverv1.Accepted)
	// OnFinished is called with the run's Finished; terminal. Required.
	OnFinished func(ctx context.Context, f *driverv1.Finished)
	// OnLost is called when the run's Finished can never arrive; terminal.
	// Required.
	OnLost func(ctx context.Context, reason LostReason, err error)
	// OnUnreachable is called after each failed reattach once LostTimeout has
	// passed; returning true ends the run, false keeps following. Required.
	OnUnreachable func(ctx context.Context, err error) (stop bool)
}

// LostReason says why RunStreamOptions.OnLost was called.
type LostReason int

const (
	// LostStartFailed: the run could not be started; err says why.
	LostStartFailed LostReason = iota + 1
	// LostNoRun: the driver has no run with the version (NotFound).
	LostNoRun
	// LostSuperseded: the driver has moved past the version or runs another
	// one (FailedPrecondition, Aborted).
	LostSuperseded
	// LostSandboxParked: the sandbox was parked or stopping after a drop.
	LostSandboxParked
	// LostSandboxGone: the sandbox was gone after a drop.
	LostSandboxGone
)

// String returns the reason's name.
func (r LostReason) String() string {
	switch r {
	case LostStartFailed:
		return "start failed"
	case LostNoRun:
		return "no run"
	case LostSuperseded:
		return "superseded"
	case LostSandboxParked:
		return "sandbox parked"
	case LostSandboxGone:
		return "sandbox gone"
	default:
		return fmt.Sprintf("LostReason(%d)", int(r))
	}
}

// RunStream follows one run on a sandbox's driver over driver.v1 Run streams,
// from its start or reattach until it ends. Every outcome is reported through
// its options' callbacks; it never parks the sandbox.
type RunStream struct {
	// opts is the RunStream's configuration, with its defaults applied.
	opts RunStreamOptions
}

// NewRunStream returns a RunStream for opts. It panics if Sandbox,
// OnFinished, OnLost or OnUnreachable is nil.
func NewRunStream(opts RunStreamOptions) *RunStream {
	switch {
	case opts.Sandbox == nil:
		panic("runner: RunStreamOptions.Sandbox is required")
	case opts.OnFinished == nil:
		panic("runner: RunStreamOptions.OnFinished is required")
	case opts.OnLost == nil:
		panic("runner: RunStreamOptions.OnLost is required")
	case opts.OnUnreachable == nil:
		panic("runner: RunStreamOptions.OnUnreachable is required")
	}
	opts.Log = cmp.Or(opts.Log, slog.Default())
	opts.RetryInterval = cmp.Or(opts.RetryInterval, defaultRetryInterval)
	opts.RetryMaxInterval = cmp.Or(opts.RetryMaxInterval, defaultRetryMaxInterval)
	opts.LostTimeout = cmp.Or(opts.LostTimeout, defaultLostTimeout)
	opts.ReadyTimeout = cmp.Or(opts.ReadyTimeout, defaultReadyTimeout)
	if opts.OnAccepted == nil {
		opts.OnAccepted = func(context.Context, *driverv1.Accepted) {}
	}
	return &RunStream{opts: opts}
}

// Run follows the run. The callbacks run synchronously on Run's goroutine
// with ctx. Run returns nil once exactly one terminal callback (OnFinished,
// OnLost, or OnUnreachable returning true) has returned; otherwise it returns
// ctx's cause, and no terminal callback was called. Call it once.
//
// With a spec, Run first starts the run: it waits for the driver to answer
// Status, backing off exponentially, then opens a Run stream with the spec and
// closes it after its Accepted, which leaves the run going. A Run that is
// rejected because another stream is open is retried. The driver has
// ReadyTimeout to do both; a start that fails is reported through
// OnLost(LostStartFailed). Run then follows the run as for a reattach.
func (r *RunStream) Run(ctx context.Context) error {
	opts := r.opts
	if opts.Spec != nil {
		accepted, err := r.start(ctx)
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if err != nil {
			opts.Log.Error("failed to start run", "version", opts.Version, "err", err)
			opts.OnLost(ctx, LostStartFailed, err)
			return nil
		}
		opts.OnAccepted(ctx, accepted)
	}
	return r.follow(ctx)
}

// start waits for the driver to answer Status, then starts the run with the
// spec, and returns the Accepted it was answered with, all within
// ReadyTimeout.
func (r *RunStream) start(ctx context.Context) (*driverv1.Accepted, error) {
	opts := r.opts
	ctx, cancel := context.WithTimeout(ctx, opts.ReadyTimeout)
	defer cancel()
	client, err := opts.Sandbox.Dial(ctx, "")
	if err != nil {
		return nil, err
	}
	bo := doubling(opts.RetryInterval, readyMaxInterval)
	for {
		_, err := client.Status(ctx, &driverv1.StatusRequest{})
		if err == nil {
			break
		}
		if !common.SleepContext(ctx, bo.NextBackOff()) {
			return nil, fmt.Errorf("driver server did not become ready: %w", err)
		}
	}
	for {
		accepted, err := r.accept(ctx, client, &driverv1.RunRequest{Version: opts.Version, Spec: opts.Spec})
		if err == nil {
			return accepted, nil
		}
		if connect.CodeOf(err) != connect.CodeUnavailable || !common.SleepContext(ctx, opts.RetryInterval) {
			return nil, fmt.Errorf("failed to start run %d: %w", opts.Version, err)
		}
	}
}

// accept opens a Run stream and closes it after its Accepted, which it
// returns: the run keeps going without it.
func (r *RunStream) accept(ctx context.Context, client driverv1connect.DriverServiceClient, req *driverv1.RunRequest) (*driverv1.Accepted, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := client.Run(ctx, req)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	if !stream.Receive() {
		return nil, cmp.Or(stream.Err(), errors.New("run stream ended before it was accepted"))
	}
	accepted := stream.Msg().GetAccepted()
	if accepted == nil {
		return nil, fmt.Errorf("unexpected first run response: %v", stream.Msg())
	}
	return accepted, nil
}

// follow attaches to the run and follows it until it ends, through a terminal
// callback, or ctx is done, when it returns ctx's cause.
//
// A dropped stream is reattached while the sandbox is up, backing off
// exponentially. A sandbox that is parked or gone before Finished arrived, or
// a driver with no such run, is a lost run. Once LostTimeout has passed
// without an accepted stream, each failed reattach calls OnUnreachable, which
// decides whether to stop or keep following.
func (r *RunStream) follow(ctx context.Context) error {
	opts := r.opts
	lastGood := time.Now()
	bo := doubling(opts.RetryInterval, opts.RetryMaxInterval)
	var lastErr string
	for {
		finished, accepted, err := r.attachOnce(ctx)
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if finished != nil {
			opts.OnFinished(ctx, finished)
			return nil
		}
		if isTerminal(err) {
			// The driver has no run with this version (or has moved past it),
			// so its Finished will never come.
			opts.Log.Error("driver has no such run", "version", opts.Version, "err", err)
			reason := LostSuperseded
			if connect.CodeOf(err) == connect.CodeNotFound {
				reason = LostNoRun
			}
			opts.OnLost(ctx, reason, err)
			return nil
		}
		if status, stErr := opts.Sandbox.Status(ctx); stErr != nil {
			err = fmt.Errorf("%w (status: %w)", err, stErr)
		} else if status != backend.StatusUp {
			opts.Log.Error("sandbox stopped before its run finished", "status", status, "version", opts.Version, "err", err)
			reason := LostSandboxParked
			if status == backend.StatusGone {
				reason = LostSandboxGone
			}
			opts.OnLost(ctx, reason, err)
			return nil
		}
		if accepted {
			// The stream was live until it dropped: the driver's idle watchdog
			// would have ended it otherwise.
			lastGood = time.Now()
			bo.Reset()
			lastErr = ""
		}
		if time.Since(lastGood) >= opts.LostTimeout && opts.OnUnreachable(ctx, err) {
			return nil
		}
		if msg := err.Error(); msg != lastErr {
			opts.Log.Warn("run stream dropped, reattaching", "version", opts.Version, "err", err)
			lastErr = msg
		}
		if !common.SleepContext(ctx, bo.NextBackOff()) {
			return context.Cause(ctx)
		}
	}
}

// attachOnce opens a Run stream for the version without a spec and reads it
// until the run's Finished, which it returns, calling OnAccepted on the
// stream's first Accepted. Otherwise it returns why the stream ended.
// accepted reports whether the driver accepted the stream before it ended.
func (r *RunStream) attachOnce(ctx context.Context) (finished *driverv1.Finished, accepted bool, err error) {
	client, err := r.opts.Sandbox.Dial(ctx, "")
	if err != nil {
		return nil, false, err
	}
	stream, err := client.Run(ctx, &driverv1.RunRequest{Version: r.opts.Version})
	if err != nil {
		return nil, false, err
	}
	defer stream.Close()
	for stream.Receive() {
		msg := stream.Msg()
		if a := msg.GetAccepted(); a != nil && !accepted {
			accepted = true
			r.opts.OnAccepted(ctx, a)
		}
		if finished := msg.GetFinished(); finished != nil {
			return finished, accepted, nil
		}
	}
	return nil, accepted, cmp.Or(stream.Err(), errors.New("run stream ended before Finished"))
}
