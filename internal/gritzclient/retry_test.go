package gritzclient_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"gotest.tools/v3/assert"

	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
)

func TestRetryInterceptor_RetriesUntilSuccess(t *testing.T) {
	t.Parallel()
	// Arrange: fail with Unavailable twice, then succeed.
	var calls atomic.Int64
	interceptor := gritzclient.RetryInterceptor{
		MaxRetries:     3,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     time.Millisecond,
	}
	wrapped := interceptor.WrapUnary(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if calls.Add(1) < 3 {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("boom"))
		}
		return connect.NewResponse(&gritzv1.GetTaskResponse{}), nil
	})

	// Act
	res, err := wrapped(context.Background(), connect.NewRequest(&gritzv1.GetTaskRequest{Id: 1}))

	// Assert
	assert.NilError(t, err)
	assert.Assert(t, res != nil)
	assert.Equal(t, calls.Load(), int64(3))
}

func TestRetryInterceptor_ExhaustsRetries(t *testing.T) {
	t.Parallel()
	// Arrange: always fail with a retryable error.
	var calls atomic.Int64
	interceptor := gritzclient.RetryInterceptor{
		MaxRetries:     2,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     time.Millisecond,
	}
	wrapped := interceptor.WrapUnary(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		calls.Add(1)
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("boom"))
	})

	// Act
	_, err := wrapped(context.Background(), connect.NewRequest(&gritzv1.GetTaskRequest{Id: 1}))

	// Assert: initial call plus two retries.
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnavailable)
	assert.Equal(t, calls.Load(), int64(3))
}

func TestRetryInterceptor_DoesNotRetryPermanentError(t *testing.T) {
	t.Parallel()
	// Arrange
	var calls atomic.Int64
	interceptor := gritzclient.RetryInterceptor{
		MaxRetries:     5,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     time.Millisecond,
	}
	wrapped := interceptor.WrapUnary(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		calls.Add(1)
		return nil, connect.NewError(connect.CodeNotFound, errors.New("nope"))
	})

	// Act
	_, err := wrapped(context.Background(), connect.NewRequest(&gritzv1.GetTaskRequest{Id: 1}))

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeNotFound)
	assert.Equal(t, calls.Load(), int64(1))
}

func TestRetryInterceptor_StopsOnCancelledContext(t *testing.T) {
	t.Parallel()
	// Arrange: cancel the context, then fail with a retryable error so the
	// interceptor tries to sleep before retrying.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int64
	interceptor := gritzclient.RetryInterceptor{
		MaxRetries:     5,
		InitialBackoff: time.Hour,
	}
	wrapped := interceptor.WrapUnary(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		calls.Add(1)
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("boom"))
	})

	// Act
	_, err := wrapped(ctx, connect.NewRequest(&gritzv1.GetTaskRequest{Id: 1}))

	// Assert: it does not wait out the hour-long backoff, and only the initial
	// call happened before the cancelled context aborted the retry sleep. The
	// backoff loop surfaces the context cause rather than the last RPC error.
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, calls.Load(), int64(1))
}

func TestRetryInterceptor_NegativeMaxRetriesDisables(t *testing.T) {
	t.Parallel()
	// Arrange
	var calls atomic.Int64
	interceptor := gritzclient.RetryInterceptor{MaxRetries: -1}
	wrapped := interceptor.WrapUnary(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		calls.Add(1)
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("boom"))
	})

	// Act
	_, err := wrapped(context.Background(), connect.NewRequest(&gritzv1.GetTaskRequest{Id: 1}))

	// Assert
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnavailable)
	assert.Equal(t, calls.Load(), int64(1))
}
