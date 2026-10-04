package outbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v5"
	"gotest.tools/v3/assert"

	"github.com/gritzapp/gritz/internal/x/testx"
)

func TestOutbox_FIFO(t *testing.T) {
	// Arrange
	store, err := Open(t.TempDir())
	assert.NilError(t, err)
	var got testx.SafeSlice[int]
	ob := New(Options[int]{
		Store:   store,
		Backoff: backoff.NewConstantBackOff(0),
		Deliver: func(ctx context.Context, n int) (bool, error) {
			got.Append(n)
			return false, nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ob.Run(ctx)

	// Act
	assert.NilError(t, ob.Enqueue(1))
	assert.NilError(t, ob.Enqueue(2))
	assert.NilError(t, ob.Enqueue(3))

	// Assert: once the store drains, every message has been delivered (Drop
	// runs after Deliver returns) — in enqueue order.
	testx.WaitForWithTimeout(t, ctx, 2*time.Second, func() bool { n, _ := ob.Len(); return n == 0 })
	assert.DeepEqual(t, got.Slice(), []int{1, 2, 3})
}

func TestOutbox_TransientRetry(t *testing.T) {
	// Arrange: the head fails transiently three times, then succeeds. Later
	// messages must stay blocked behind it (head-of-line blocking).
	store, err := Open(t.TempDir())
	assert.NilError(t, err)
	var got testx.SafeSlice[int]
	var attempts int
	var mu sync.Mutex
	ob := New(Options[int]{
		Store:   store,
		Backoff: backoff.NewConstantBackOff(time.Millisecond),
		Deliver: func(ctx context.Context, n int) (bool, error) {
			got.Append(n)
			if n == 1 {
				mu.Lock()
				attempts++
				a := attempts
				mu.Unlock()
				if a < 4 {
					return false, errors.New("transient")
				}
			}
			return false, nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ob.Run(ctx)

	// Act
	assert.NilError(t, ob.Enqueue(1))
	assert.NilError(t, ob.Enqueue(2))

	// Assert: head retried until success, everything delivered in order, and 2
	// was never attempted before 1 succeeded.
	testx.WaitForWithTimeout(t, ctx, 2*time.Second, func() bool { n, _ := ob.Len(); return n == 0 })
	assert.DeepEqual(t, got.Slice(), []int{1, 1, 1, 1, 2})
}

func TestOutbox_PermanentDeadLetter(t *testing.T) {
	// Arrange: the middle message fails permanently and must be dead-lettered
	// so the queue advances to the next message.
	dir := t.TempDir()
	store, err := Open(dir)
	assert.NilError(t, err)
	var got testx.SafeSlice[int]
	ob := New(Options[int]{
		Store:   store,
		Backoff: backoff.NewConstantBackOff(0),
		Deliver: func(ctx context.Context, n int) (bool, error) {
			got.Append(n)
			if n == 2 {
				return true, errors.New("permanent")
			}
			return false, nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ob.Run(ctx)

	// Act
	assert.NilError(t, ob.Enqueue(1))
	assert.NilError(t, ob.Enqueue(2))
	assert.NilError(t, ob.Enqueue(3))

	// Assert: 2 is attempted once, then dead-lettered; 1 and 3 delivered; the
	// live queue drains and the dead-letter file survives.
	testx.WaitForWithTimeout(t, ctx, 2*time.Second, func() bool { n, _ := ob.Len(); return n == 0 })
	assert.DeepEqual(t, got.Slice(), []int{1, 2, 3})

	dead, err := os.ReadDir(filepath.Join(dir, "dead"))
	assert.NilError(t, err)
	assert.Equal(t, len(dead), 1)
}

func TestOutbox_Len(t *testing.T) {
	// Arrange: no Run, so nothing is delivered.
	store, err := Open(t.TempDir())
	assert.NilError(t, err)
	ob := New(Options[int]{
		Store:   store,
		Deliver: func(ctx context.Context, n int) (bool, error) { return false, nil },
	})

	// Act
	assert.NilError(t, ob.Enqueue(1))
	assert.NilError(t, ob.Enqueue(2))

	// Assert
	n, err := ob.Len()
	assert.NilError(t, err)
	assert.Equal(t, n, 2)
}

func TestOutbox_StartupRecovery(t *testing.T) {
	// Arrange: persist records with one outbox instance, then simulate a
	// restart by re-opening the same directory into a fresh outbox.
	dir := t.TempDir()
	seed, err := Open(dir)
	assert.NilError(t, err)
	assert.NilError(t, seed.Append([]byte(`1`)))
	assert.NilError(t, seed.Append([]byte(`2`)))
	assert.NilError(t, seed.Append([]byte(`3`)))

	store, err := Open(dir)
	assert.NilError(t, err)
	var got testx.SafeSlice[int]
	ob := New(Options[int]{
		Store:   store,
		Backoff: backoff.NewConstantBackOff(0),
		Deliver: func(ctx context.Context, n int) (bool, error) {
			got.Append(n)
			return false, nil
		},
	})

	// Act: the first pass of Run redelivers everything already persisted.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ob.Run(ctx)

	// Assert: everything persisted before construction is redelivered in order.
	testx.WaitForWithTimeout(t, ctx, 2*time.Second, func() bool { n, _ := ob.Len(); return n == 0 })
	assert.DeepEqual(t, got.Slice(), []int{1, 2, 3})
}
