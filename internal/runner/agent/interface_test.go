package agent

import (
	"context"
	"syscall"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

func TestStopOnSignal(t *testing.T) {
	t.Parallel()
	// Arrange - SIGUSR1 is registered by this test alone, so delivering it to
	// the process reaches no one else.
	ctx, stop := StopOnSignal(t.Context(), syscall.SIGUSR1)
	defer stop()

	// Act
	assert.NilError(t, syscall.Kill(syscall.Getpid(), syscall.SIGUSR1))

	// Assert
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("context not cancelled")
	}
	assert.Equal(t, context.Cause(ctx), ErrStop)
}

func TestStopOnSignal_Cancel(t *testing.T) {
	t.Parallel()
	ctx, stop := StopOnSignal(t.Context(), syscall.SIGUSR2)

	stop()

	assert.ErrorIs(t, ctx.Err(), context.Canceled)
	assert.Assert(t, context.Cause(ctx) != ErrStop)
}
