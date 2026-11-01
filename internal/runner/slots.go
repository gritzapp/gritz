package runner

import (
	"math"

	"github.com/gritzapp/gritz/internal/x/safesem"
	"github.com/gritzapp/gritz/internal/x/wakeup"
)

// Slots are the runner's concurrency slots: each run holds one from the
// command that starts it until its Run returns. One Slots is shared by all
// the manager's TaskRunners.
type Slots struct {
	sem *safesem.Semaphore
	// released wakes the TaskRunners waiting to retry a command when a run
	// releases its slot.
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

// TryAcquire takes a slot, and reports false if none is free.
func (s *Slots) TryAcquire() bool { return s.sem.TryAcquire(1) }

// Release gives a slot back, and wakes the TaskRunners waiting for one.
func (s *Slots) Release() {
	s.sem.Release(1)
	s.released.Wake()
}

// Released returns a channel that the next Release closes. A waiter takes it
// before trying for a slot, so a Release in between is not missed.
func (s *Slots) Released() <-chan struct{} { return s.released.Wait() }

// Set marks n slots taken, which may exceed the limit; see TaskManager.Load.
func (s *Slots) Set(n int64) { s.sem.Set(n) }
