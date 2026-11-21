package runner

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"gotest.tools/v3/assert"

	"github.com/gritzapp/gritz/internal/x/testx"
)

// TestSlotsAcquire_Force checks that a forced Acquire takes a slot past the
// limit, and that a waiting Acquire gets in only once the count drops below
// it.
func TestSlotsAcquire_Force(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// Arrange - force two slots against a limit of one.
		s := NewSlots(1)
		assert.NilError(t, s.Acquire(t.Context(), true))
		assert.NilError(t, s.Acquire(t.Context(), true))
		f := testx.Go(func() error { return s.Acquire(t.Context(), false) })

		// Act
		s.Release()
		synctest.Wait()
		assert.Assert(t, !f.Done(), "Acquire returned with the count at the limit")
		s.Release()

		// Assert
		assert.NilError(t, f.Wait(t, time.Second))
	})
}

// TestSlotsAcquire_Closed checks that Acquire waits while the slots are
// closed, and takes a slot once they open.
func TestSlotsAcquire_Closed(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// Arrange
		s := NewSlots(1)
		s.Open(false)
		f := testx.Go(func() error { return s.Acquire(t.Context(), false) })
		synctest.Wait()
		assert.Assert(t, !f.Done(), "Acquire returned while the slots were closed")

		// Act
		s.Open(true)

		// Assert
		assert.NilError(t, f.Wait(t, time.Second))
	})
}

// TestSlotsAcquire_Release checks that a waiting Acquire takes the slot a
// Release gives back.
func TestSlotsAcquire_Release(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// Arrange
		s := NewSlots(1)
		assert.NilError(t, s.Acquire(t.Context(), false))
		f := testx.Go(func() error { return s.Acquire(t.Context(), false) })
		synctest.Wait()
		assert.Assert(t, !f.Done(), "Acquire returned with no slot free")

		// Act
		s.Release()

		// Assert
		assert.NilError(t, f.Wait(t, time.Second))
	})
}

// TestSlotsAcquire_Canceled checks that a waiting Acquire returns ctx's
// cause when ctx ends.
func TestSlotsAcquire_Canceled(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// Arrange
		s := NewSlots(1)
		assert.NilError(t, s.Acquire(t.Context(), false))
		ctx, cancel := context.WithCancelCause(t.Context())
		f := testx.Go(func() error { return s.Acquire(ctx, false) })
		synctest.Wait()
		assert.Assert(t, !f.Done(), "Acquire returned with no slot free")

		// Act
		cancel(context.DeadlineExceeded)

		// Assert
		assert.Equal(t, f.Wait(t, time.Second), context.DeadlineExceeded)
	})
}
