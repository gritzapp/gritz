package runner

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/x/common"
)

// ErrSandboxDown is returned, wrapped, by a RunStreamOptions.Open that could
// not reach the driver because its sandbox is not up. It tells the RunStream
// to give up: the run's Finished can never arrive.
var ErrSandboxDown = errors.New("sandbox is not up")

// errReadyTimeout cancels the streams of a start that has had no Accepted
// within RunStreamOptions.ReadyTimeout.
var errReadyTimeout = errors.New("ready timeout")

// RunStreamOptions configures a RunStream.
type RunStreamOptions struct {
	// Open opens a Run stream to the driver. start is true until the
	// RunStream has received an Accepted, and only then may the request
	// carry the spec: a reattach that carried it would start the run again
	// on a driver that has lost its record of it. An error wrapping
	// ErrSandboxDown ends the run. Required.
	Open func(ctx context.Context, start bool) (*connect.ServerStreamForClient[driverv1.RunResponse], error)
	// Start makes Run start the run: Open is called with start true until
	// the first Accepted. Otherwise Run reattaches to a run already started,
	// and start is always false.
	Start bool
	// RetryInterval is the first pause between attempts to reach the driver
	// or reattach to the run; defaults to 250ms.
	RetryInterval time.Duration
	// RetryMaxInterval caps the reattach backoff; defaults to 10s.
	RetryMaxInterval time.Duration
	// LostTimeout is how long reattaching goes on with no accepted stream
	// before OnUnreachable is called; defaults to 2m.
	LostTimeout time.Duration
	// ReadyTimeout bounds starting the run, up to its first Accepted;
	// defaults to 1m.
	ReadyTimeout time.Duration

	// Every callback returns an error; a non-nil one ends Run with it.

	// OnAccepted is called when the driver accepts a stream; optional.
	OnAccepted func(ctx context.Context, a *driverv1.Accepted) error
	// OnDropped is called with why a stream failed each time Run is about to
	// retry it, before the backoff; optional.
	OnDropped func(ctx context.Context, err error) error
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
	// LostStartFailed: the run could not be started, up to its first
	// Accepted; err says why.
	LostStartFailed LostReason = iota + 1
	// LostNoRun: the driver has no run with the version (NotFound).
	LostNoRun
	// LostSuperseded: the driver has moved past the version or runs another
	// one (FailedPrecondition, Aborted).
	LostSuperseded
	// LostSandboxDown: Open returned ErrSandboxDown after the first
	// Accepted; err is Open's error.
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
// options' Open, and reports every outcome through their callbacks. It knows
// neither the run's version nor its spec.
type RunStream struct {
	// opts is the RunStream's configuration, with its defaults applied.
	opts RunStreamOptions
	// starting is true while Run is starting the run: until the first
	// Accepted.
	starting bool
	// stopReady stops the ready timer once the start is accepted.
	stopReady func() bool
}

// NewRunStream returns a RunStream for opts. It panics if Open, OnFinished,
// OnLost or OnUnreachable is nil.
func NewRunStream(opts RunStreamOptions) *RunStream {
	switch {
	case opts.Open == nil:
		panic("runner: RunStreamOptions.Open is required")
	case opts.OnFinished == nil:
		panic("runner: RunStreamOptions.OnFinished is required")
	case opts.OnLost == nil:
		panic("runner: RunStreamOptions.OnLost is required")
	case opts.OnUnreachable == nil:
		panic("runner: RunStreamOptions.OnUnreachable is required")
	}
	opts.RetryInterval = cmp.Or(opts.RetryInterval, defaultRetryInterval)
	opts.RetryMaxInterval = cmp.Or(opts.RetryMaxInterval, defaultRetryMaxInterval)
	opts.LostTimeout = cmp.Or(opts.LostTimeout, defaultLostTimeout)
	opts.ReadyTimeout = cmp.Or(opts.ReadyTimeout, defaultReadyTimeout)
	if opts.OnAccepted == nil {
		opts.OnAccepted = func(context.Context, *driverv1.Accepted) error { return nil }
	}
	if opts.OnDropped == nil {
		opts.OnDropped = func(context.Context, error) error { return nil }
	}
	return &RunStream{opts: opts, starting: opts.Start, stopReady: func() bool { return false }}
}

// Run follows the run. The callbacks run synchronously on Run's goroutine
// with ctx. A callback that returns an error ends Run at once, and Run
// returns that error. Otherwise Run returns nil once exactly one terminal
// callback (OnFinished, OnLost, or OnUnreachable returning true) has
// returned, or ctx's cause, with no terminal callback called. Call it once.
//
// Run opens a stream, reads its Accepted and then the stream until the
// run's Finished, and opens another stream when one drops. With Start, it
// starts the run first: until the first Accepted, Open is called with start
// true, a stream rejected as Unavailable (a driver not answering yet, or
// another stream open) is retried with backoff, and ReadyTimeout bounds it
// all. A start that fails or times out, or an Open that returns
// ErrSandboxDown, is reported through OnLost(LostStartFailed). The stream
// that is accepted is kept: the start is only different up to its Accepted.
//
// After the first Accepted, or from the first stream without Start, a
// dropped stream is reopened, backing off exponentially, while Open does not
// return ErrSandboxDown. An Open that does, or a driver with no such run, is
// a lost run. Once LostTimeout has passed without an accepted stream, each
// failed reattach calls OnUnreachable, which decides whether to stop or keep
// following.
//
// Every failed stream that Run is about to retry, while starting or after,
// is passed to OnDropped before the backoff. Run logs nothing itself.
func (r *RunStream) Run(ctx context.Context) error {
	opts := r.opts
	readyCtx := ctx
	if r.starting {
		var cancel context.CancelCauseFunc
		readyCtx, cancel = context.WithCancelCause(ctx)
		defer cancel(nil)
		r.stopReady = time.AfterFunc(opts.ReadyTimeout, func() { cancel(errReadyTimeout) }).Stop
	}
	readyBo := doubling(opts.RetryInterval, readyMaxInterval)
	lastGood := time.Now()
	bo := doubling(opts.RetryInterval, opts.RetryMaxInterval)
	for {
		start := r.starting
		streamCtx := ctx
		if start {
			// The stream outlives the start once it is accepted, and only the
			// ready timer cancels readyCtx before Run returns.
			streamCtx = readyCtx
		}
		finished, accepted, err, cbErr := r.readStream(streamCtx, start)
		if cbErr != nil {
			return cbErr
		}
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if finished != nil {
			return opts.OnFinished(ctx, finished)
		}
		if r.starting {
			switch {
			case readyCtx.Err() != nil:
				return opts.OnLost(ctx, LostStartFailed, fmt.Errorf("driver server did not become ready: %w", err))
			case errors.Is(err, ErrSandboxDown), connect.CodeOf(err) != connect.CodeUnavailable:
				return opts.OnLost(ctx, LostStartFailed, fmt.Errorf("failed to start run: %w", err))
			}
			if cbErr := opts.OnDropped(ctx, err); cbErr != nil {
				return cbErr
			}
			if !common.SleepContext(readyCtx, readyBo.NextBackOff()) {
				if ctx.Err() != nil {
					return context.Cause(ctx)
				}
				return opts.OnLost(ctx, LostStartFailed, fmt.Errorf("driver server did not become ready: %w", err))
			}
			continue
		}
		if isTerminal(err) {
			// The driver has no run with this version (or has moved past it),
			// so its Finished will never come.
			reason := LostSuperseded
			if connect.CodeOf(err) == connect.CodeNotFound {
				reason = LostNoRun
			}
			return opts.OnLost(ctx, reason, err)
		}
		if errors.Is(err, ErrSandboxDown) {
			return opts.OnLost(ctx, LostSandboxDown, err)
		}
		if accepted {
			// The stream was live until it dropped: the driver's idle watchdog
			// would have ended it otherwise.
			lastGood = time.Now()
			bo.Reset()
		}
		if time.Since(lastGood) >= opts.LostTimeout {
			if stop, cbErr := opts.OnUnreachable(ctx, err); cbErr != nil || stop {
				return cbErr
			}
		}
		if cbErr := opts.OnDropped(ctx, err); cbErr != nil {
			return cbErr
		}
		if !common.SleepContext(ctx, bo.NextBackOff()) {
			return context.Cause(ctx)
		}
	}
}

// readStream opens a Run stream and reads it until the run's Finished, which
// it returns. On the stream's first Accepted it ends the start, if Run is
// starting, and calls OnAccepted. Otherwise it returns why the stream ended,
// or OnAccepted's error as cbErr. accepted reports whether the driver
// accepted the stream before it ended.
func (r *RunStream) readStream(ctx context.Context, start bool) (finished *driverv1.Finished, accepted bool, err, cbErr error) {
	stream, err := r.opts.Open(ctx, start)
	if err != nil {
		return nil, false, err, nil
	}
	defer stream.Close()
	for stream.Receive() {
		msg := stream.Msg()
		if a := msg.GetAccepted(); a != nil && !accepted {
			accepted = true
			if r.starting {
				// The run is started. If the ready timer fired meanwhile, it
				// cancels this stream, which drops and is reopened.
				r.starting = false
				r.stopReady()
			}
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
