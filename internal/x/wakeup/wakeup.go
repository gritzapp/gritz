package wakeup

import "sync"

// Signal is an edge-triggered, coalescing wake-up signal. Like sync.Cond's
// Signal, it wakes one receiver; unlike it, a Wake with no receiver waiting
// stays pending, and multiple Wake calls between receives collapse into a
// single notification. Use Broadcast to wake every waiter. Receivers select
// on the channel directly (`case <-s:`).
type Signal chan struct{}

// NewSignal returns a ready-to-use Signal.
func NewSignal() Signal { return make(Signal, 1) }

// Wake signals without blocking. If a signal is already pending, the call
// is a no-op (signals coalesce).
func (s Signal) Wake() {
	select {
	case s <- struct{}{}:
	default:
	}
}

// Broadcast wakes every waiter at once. A waiter takes the channel from Wait
// before checking its condition, then blocks on it, so a Wake between the
// check and the block is not missed.
type Broadcast struct {
	mu sync.Mutex
	ch chan struct{}
}

// Wait returns a channel that the next Wake closes.
func (b *Broadcast) Wait() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ch == nil {
		b.ch = make(chan struct{})
	}
	return b.ch
}

// Wake closes the current channel, waking everyone holding it, and starts a
// new one for later Waits.
func (b *Broadcast) Wake() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ch != nil {
		close(b.ch)
	}
	b.ch = make(chan struct{})
}
