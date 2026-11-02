package runner

import (
	"context"
	"math"

	"github.com/gritzapp/gritz/internal/x/safesem"
	"github.com/gritzapp/gritz/internal/x/wakeup"
)

// Slots are the runner's concurrency slots: each busy period holds one, from
// before its sandbox boots until drive returns. One Slots is shared by all
// the manager's TaskRunners.
type Slots struct {
	sem *safesem.Semaphore
	// released wakes the TaskRunners waiting in Acquire when a slot is given
	// back.
	released wakeup.Broadcast
}

// NewSlots returns n free slots; n <= 0 means no limit.
func NewSlots(n int) *Slots {
	capacity := int64(n)
	if capacity <= 0 {
		capacity = math.MaxInt64
	}
	return &Slots{sem: safesem.New(capacity)}
}

// Acquire takes a slot, blocking until one is free or ctx ends, when it
// returns ctx's cause.
func (s *Slots) Acquire(ctx context.Context) error {
	for {
		// Take the signal before trying, so a Release in between is not
		// missed.
		released := s.released.Wait()
		if s.sem.TryAcquire(1) {
			return nil
		}
		select {
		case <-released:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}

// Release gives a slot back, and wakes the TaskRunners waiting for one.
func (s *Slots) Release() {
	s.sem.Release(1)
	s.released.Wake()
}

// Set marks n slots taken, which may exceed the limit; see TaskManager.Load.
func (s *Slots) Set(n int64) { s.sem.Set(n) }
