package runner

import (
	"context"
	"math"
	"sync"

	"github.com/gritzapp/gritz/internal/x/wakeup"
)

// Slots are the runner's concurrency slots: each busy period holds one, from
// before its sandbox boots until drive returns. One Slots is shared by all
// the manager's TaskRunners.
type Slots struct {
	mu sync.Mutex
	// count is the number of slots held. A forced Acquire may take it past
	// limit.
	count int64
	limit int64
	// open is whether Acquire may take a slot without force.
	open bool
	// released wakes the TaskRunners waiting in Acquire when a slot is given
	// back or the slots open.
	released wakeup.Broadcast
}

// NewSlots returns n free, open slots; n <= 0 means no limit.
func NewSlots(n int) *Slots {
	limit := int64(n)
	if limit <= 0 {
		limit = math.MaxInt64
	}
	return &Slots{limit: limit, open: true}
}

// Open opens or closes the slots to Acquire. Opening them wakes the
// TaskRunners already waiting. TaskManager.Load closes them while it forces a
// slot for every sandbox it finds up, so no TaskRunner takes a slot those
// sandboxes hold, and opens them once it is done.
func (s *Slots) Open(open bool) {
	s.mu.Lock()
	s.open = open
	s.mu.Unlock()
	if open {
		s.released.Wake()
	}
}

// Acquire takes a slot, blocking until the slots are open and one is free, or
// until ctx ends, when it returns ctx's cause. If force is set, it takes a
// slot at once, even while the slots are closed and past the limit, and
// returns nil; Load uses it to count a sandbox it found up, which may exceed
// the limit in over-limit scenarios.
func (s *Slots) Acquire(ctx context.Context, force bool) error {
	if force {
		s.mu.Lock()
		s.count++
		s.mu.Unlock()
		return nil
	}
	for {
		// Take the signal before trying, so a Release in between is not
		// missed.
		released := s.released.Wait()
		if s.tryAcquire() {
			return nil
		}
		select {
		case <-released:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}

// tryAcquire takes a slot if the slots are open and one is free.
func (s *Slots) tryAcquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open || s.count >= s.limit {
		return false
	}
	s.count++
	return true
}

// Release gives a held slot back, and wakes the TaskRunners waiting for one.
func (s *Slots) Release() {
	s.mu.Lock()
	if s.count == 0 {
		s.mu.Unlock()
		panic("runner: Slots.Release without a held slot")
	}
	s.count--
	s.mu.Unlock()
	s.released.Wake()
}
