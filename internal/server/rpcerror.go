package server

import (
	"context"
	"errors"

	"connectrpc.com/connect"
)

// RequestContextInterceptor returns a Connect interceptor that reports an
// error caused by the request's own context ending as Canceled or
// DeadlineExceeded rather than the code the handler chose (usually Internal).
// The request context ends only from outside the handler: the client
// disconnecting, or the deadline from the client's timeout header. A context
// error while the request context is still live came from a context the
// server derived itself, so it is left as a server error.
//
// It must run inside otelconnect so the span records the mapped code.
func RequestContextInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			resp, err := next(ctx, req)
			ctxErr := ctx.Err()
			if err == nil || ctxErr == nil || !errors.Is(err, ctxErr) {
				return resp, err
			}
			code := connect.CodeCanceled
			if errors.Is(ctxErr, context.DeadlineExceeded) {
				code = connect.CodeDeadlineExceeded
			}
			// Unwrap the handler's connect error so the message isn't prefixed
			// with its code.
			if ce, ok := errors.AsType[*connect.Error](err); ok {
				err = ce.Unwrap()
			}
			return nil, connect.NewError(code, err)
		}
	}
}
