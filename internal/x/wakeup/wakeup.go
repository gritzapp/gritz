package wakeup

import "sync"

// Chan is an edge-triggered, coalescing wake-up signal. Multiple Wake
// calls between receives collapse into a single notification. Receivers
// select on the channel directly (`case <-c:`).
type Chan chan struct{}

// New returns a ready-to-use wakeup Chan.
func New() Chan { return make(Chan, 1) }

// Wake signals without blocking. If a signal is already pending, the call
// is a no-op (signals coalesce).
func (c Chan) Wake() {
	select {
	case c <- struct{}{}:
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
