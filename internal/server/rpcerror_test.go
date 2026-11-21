package server_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/gritzapp/gritz/internal/server"
	"gotest.tools/v3/assert"
)

func TestRequestContextInterceptor(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancel := context.WithDeadline(context.Background(), time.Now())
	t.Cleanup(cancel)
	tests := []struct {
		name     string
		ctx      context.Context
		err      error
		wantCode connect.Code
		wantMsg  string
	}{
		{
			name:     "request canceled",
			ctx:      canceled,
			err:      connect.NewError(connect.CodeInternal, fmt.Errorf("list links: %w", context.Canceled)),
			wantCode: connect.CodeCanceled,
			wantMsg:  "canceled: list links: context canceled",
		},
		{
			name:     "request deadline exceeded",
			ctx:      expired,
			err:      connect.NewError(connect.CodeInternal, fmt.Errorf("get task: %w", context.DeadlineExceeded)),
			wantCode: connect.CodeDeadlineExceeded,
			wantMsg:  "deadline_exceeded: get task: context deadline exceeded",
		},
		{
			name:     "handler timeout with live request",
			ctx:      context.Background(),
			err:      connect.NewError(connect.CodeInternal, context.DeadlineExceeded),
			wantCode: connect.CodeInternal,
			wantMsg:  "internal: context deadline exceeded",
		},
		{
			name:     "handler cancel with live request",
			ctx:      context.Background(),
			err:      connect.NewError(connect.CodeInternal, context.Canceled),
			wantCode: connect.CodeInternal,
			wantMsg:  "internal: context canceled",
		},
		{
			name:     "unrelated error with canceled request",
			ctx:      canceled,
			err:      connect.NewError(connect.CodeNotFound, errors.New("task not found")),
			wantCode: connect.CodeNotFound,
			wantMsg:  "not_found: task not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
				return nil, tt.err
			}

			// Act
			_, err := server.RequestContextInterceptor()(next)(tt.ctx, nil)

			// Assert
			assert.Equal(t, connect.CodeOf(err), tt.wantCode)
			assert.Error(t, err, tt.wantMsg)
		})
	}
}
