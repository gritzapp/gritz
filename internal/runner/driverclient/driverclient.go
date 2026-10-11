// Package driverclient builds driver.v1 clients that notice a dead Run stream.
//
// The driver server sends a KeepAlive on every open Run stream every
// driverserver.DefaultKeepAlive. A client built here watches for them: a Run
// stream that receives nothing for IdleTimeout is cancelled with
// ErrStreamIdle, so a half-open connection fails instead of blocking forever.
// KeepAlive messages are consumed by the client and never reach the caller,
// which only sees Accepted and Finished.
package driverclient

import (
	"cmp"
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	driverv1 "github.com/gritzapp/gritz/internal/proto/driver/v1"
	"github.com/gritzapp/gritz/internal/proto/driver/v1/driverv1connect"
	"github.com/gritzapp/gritz/internal/runner/driverserver"
)

// DefaultIdleTimeout is how long a Run stream may go without a message before
// it is cancelled: three missed KeepAlives.
const DefaultIdleTimeout = 3 * driverserver.DefaultKeepAlive

// ErrStreamIdle is the cause a Run stream is cancelled with when it has
// received nothing for the idle timeout. A Receive on such a stream fails with
// an error that wraps it.
var ErrStreamIdle = errors.New("driver run stream idle")

// Options configures a client. Every field takes its default when zero.
type Options struct {
	// IdleTimeout is how long a Run stream may go without a message.
	// Defaults to DefaultIdleTimeout.
	IdleTimeout time.Duration
}

// New returns a driver.v1 client for the driver server at baseURL whose Run
// streams are watched for liveness. Unary calls are unchanged.
func New(httpClient connect.HTTPClient, baseURL string, opts Options) driverv1connect.DriverServiceClient {
	return driverv1connect.NewDriverServiceClient(httpClient, baseURL,
		connect.WithInterceptors(&interceptor{idle: cmp.Or(opts.IdleTimeout, DefaultIdleTimeout)}))
}

// interceptor applies the liveness watchdog to Run streams.
type interceptor struct {
	idle time.Duration
}

func (i *interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return next
}

func (i *interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		if spec.Procedure != driverv1connect.DriverServiceRunProcedure {
			return next(ctx, spec)
		}
		ctx, cancel := context.WithCancelCause(ctx)
		w := &watchedConn{ctx: ctx, cancel: cancel, idle: i.idle}
		w.timer = time.AfterFunc(i.idle, func() { cancel(ErrStreamIdle) })
		w.StreamingClientConn = next(ctx, spec)
		return w
	}
}

func (i *interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// watchedConn is a Run stream that is cancelled with ErrStreamIdle once it has
// received nothing for idle, and that swallows KeepAlives.
type watchedConn struct {
	connect.StreamingClientConn
	ctx    context.Context
	cancel context.CancelCauseFunc
	idle   time.Duration
	timer  *time.Timer
}

func (w *watchedConn) Receive(msg any) error {
	for {
		if err := w.StreamingClientConn.Receive(msg); err != nil {
			if errors.Is(context.Cause(w.ctx), ErrStreamIdle) {
				return connect.NewError(connect.CodeDeadlineExceeded, ErrStreamIdle)
			}
			return err
		}
		w.timer.Reset(w.idle)
		if resp, ok := msg.(*driverv1.RunResponse); ok && resp.GetKeepAlive() != nil {
			continue
		}
		return nil
	}
}

func (w *watchedConn) CloseResponse() error {
	w.timer.Stop()
	err := w.StreamingClientConn.CloseResponse()
	w.cancel(context.Canceled)
	return err
}
