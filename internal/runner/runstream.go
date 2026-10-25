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
	"github.com/gritzapp/gritz/internal/x/common"
)

// ErrSandboxDown is returned, wrapped, by a RunStreamOptions.Dial that could
// not reach the driver because its sandbox is not up. It tells the RunStream
// to give up: the run's Finished can never arrive.
var ErrSandboxDown = errors.New("sandbox is not up")

// RunStreamOptions configures a RunStream.
type RunStreamOptions struct {
	// Dial returns a client to the driver once the driver answers. An error
	// wrapping ErrSandboxDown ends the run; any other error is retried.
	// Required.
	Dial func(ctx context.Context) (driverv1connect.DriverServiceClient, error)
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

	// Every callback returns an error; a non-nil one ends Run with it.

	// OnAccepted is called when the driver accepts a stream; optional.
	OnAccepted func(ctx context.Context, a *driverv1.Accepted) error
	// OnFinished is called with the run's Finished; terminal. Required.
	OnFinished func(ctx context.Context, f *driverv1.Finished) error
	// OnLost is called when the run's Finished can never arrive; terminal.
	// Required.
	OnLost func(ctx context.Context, reason LostReason, err error) error
	// OnUnreachable is called after each failed reattach once LostTimeout has
	// passed; returning true ends the run, false keeps following. Required.
	OnUnreachable func(ctx context.Context, err error) (stop bool, cbErr error)
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
	// LostSandboxDown: Dial returned ErrSandboxDown after a drop; err is
	// Dial's error.
	LostSandboxDown
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
	case LostSandboxDown:
		return "sandbox down"
	default:
		return fmt.Sprintf("LostReason(%d)", int(r))
	}
}

// RunStream follows one run on a driver over driver.v1 Run streams, from its
// start or reattach until it ends. It reaches the driver only through its
// options' Dial, and reports every outcome through their callbacks.
type RunStream struct {
	// opts is the RunStream's configuration, with its defaults applied.
	opts RunStreamOptions
}

// NewRunStream returns a RunStream for opts. It panics if Dial, OnFinished,
// OnLost or OnUnreachable is nil.
func NewRunStream(opts RunStreamOptions) *RunStream {
	switch {
	case opts.Dial == nil:
		panic("runner: RunStreamOptions.Dial is required")
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
		opts.OnAccepted = func(context.Context, *driverv1.Accepted) error { return nil }
	}
	return &RunStream{opts: opts}
}

// Run follows the run. The callbacks run synchronously on Run's goroutine
// with ctx. A callback that returns an error ends Run at once, and Run
// returns that error. Otherwise Run returns nil once exactly one terminal
// callback (OnFinished, OnLost, or OnUnreachable returning true) has
// returned, or ctx's cause, with no terminal callback called. Call it once.
//
// With a spec, Run first starts the run: it dials the driver, backing off
// exponentially while Dial fails, then opens a Run stream with the spec and
// closes it after its Accepted, which leaves the run going. A Run that is
// rejected because another stream is open is retried. The driver has
// ReadyTimeout to do both; a start that fails, or a Dial that returns
// ErrSandboxDown, is reported through OnLost(LostStartFailed). Run then
// follows the run as for a reattach.
func (r *RunStream) Run(ctx context.Context) error {
	opts := r.opts
	if opts.Spec != nil {
		accepted, err := r.start(ctx)
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if err != nil {
			opts.Log.Error("failed to start run", "version", opts.Version, "err", err)
			return opts.OnLost(ctx, LostStartFailed, err)
		}
		if err := opts.OnAccepted(ctx, accepted); err != nil {
			return err
		}
	}
	return r.follow(ctx)
}

// start dials the driver until it answers, then starts the run with the spec,
// and returns the Accepted it was answered with, all within ReadyTimeout.
func (r *RunStream) start(ctx context.Context) (*driverv1.Accepted, error) {
	opts := r.opts
	ctx, cancel := context.WithTimeout(ctx, opts.ReadyTimeout)
	defer cancel()
	bo := doubling(opts.RetryInterval, readyMaxInterval)
	var client driverv1connect.DriverServiceClient
	for {
		var err error
		client, err = opts.Dial(ctx)
		if err == nil {
			break
		}
		if errors.Is(err, ErrSandboxDown) {
			return nil, err
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
// callback or a callback's error, or ctx is done, when it returns ctx's cause.
//
// A dropped stream is reattached, backing off exponentially, while Dial does
// not return ErrSandboxDown. A Dial that does, or a driver with no such run,
// is a lost run. Once LostTimeout has passed without an accepted stream, each
// failed reattach calls OnUnreachable, which decides whether to stop or keep
// following.
func (r *RunStream) follow(ctx context.Context) error {
	opts := r.opts
	lastGood := time.Now()
	bo := doubling(opts.RetryInterval, opts.RetryMaxInterval)
	var lastErr string
	for {
		finished, accepted, err, cbErr := r.attachOnce(ctx)
		if cbErr != nil {
			return cbErr
		}
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if finished != nil {
			return opts.OnFinished(ctx, finished)
		}
		if isTerminal(err) {
			// The driver has no run with this version (or has moved past it),
			// so its Finished will never come.
			opts.Log.Error("driver has no such run", "version", opts.Version, "err", err)
			reason := LostSuperseded
			if connect.CodeOf(err) == connect.CodeNotFound {
				reason = LostNoRun
			}
			return opts.OnLost(ctx, reason, err)
		}
		if errors.Is(err, ErrSandboxDown) {
			opts.Log.Error("sandbox stopped before its run finished", "version", opts.Version, "err", err)
			return opts.OnLost(ctx, LostSandboxDown, err)
		}
		if accepted {
			// The stream was live until it dropped: the driver's idle watchdog
			// would have ended it otherwise.
			lastGood = time.Now()
			bo.Reset()
			lastErr = ""
		}
		if time.Since(lastGood) >= opts.LostTimeout {
			if stop, cbErr := opts.OnUnreachable(ctx, err); cbErr != nil || stop {
				return cbErr
			}
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

// attachOnce dials the driver, opens a Run stream for the version without a
// spec and reads it until the run's Finished, which it returns, calling
// OnAccepted on the stream's first Accepted. Otherwise it returns why the
// stream ended, or OnAccepted's error as cbErr. accepted reports whether the
// driver accepted the stream before it ended.
func (r *RunStream) attachOnce(ctx context.Context) (finished *driverv1.Finished, accepted bool, err, cbErr error) {
	client, err := r.opts.Dial(ctx)
	if err != nil {
		return nil, false, err, nil
	}
	stream, err := client.Run(ctx, &driverv1.RunRequest{Version: r.opts.Version})
	if err != nil {
		return nil, false, err, nil
	}
	defer stream.Close()
	for stream.Receive() {
		msg := stream.Msg()
		if a := msg.GetAccepted(); a != nil && !accepted {
			accepted = true
			if err := r.opts.OnAccepted(ctx, a); err != nil {
				return nil, accepted, nil, err
			}
		}
		if finished := msg.GetFinished(); finished != nil {
			return finished, accepted, nil, nil
		}
	}
	return nil, accepted, cmp.Or(stream.Err(), errors.New("run stream ended before Finished")), nil
}
