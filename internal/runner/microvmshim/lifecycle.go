package microvmshim

import (
	"sync"

	"github.com/icholy/gritz/internal/runner/backend/lambdamicrovm"
	"github.com/icholy/gritz/internal/x/sse"
)

// lifecycle is the in-shim broadcaster for the /gritz/lifecycle SSE stream. It
// fans events out to current subscribers and keeps the last driver-exited
// "sticky" so a fresh connection replays it immediately — delivering an exit
// that happened while the runner was disconnected rather than losing it.
type lifecycle struct {
	mu     sync.Mutex
	subs   map[chan sse.Event]struct{}
	sticky *sse.Event // last driver-exited, replayed to new subscribers
}

// publish fans ev out to current subscribers and, if it is a driver-exited
// event, records it as the sticky replay.
func (l *lifecycle) publish(ev sse.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ev.Event == lambdamicrovm.EventDriverExited {
		c := ev.Clone()
		l.sticky = &c
	}
	for ch := range l.subs {
		// Non-blocking: a slow/dead subscriber must not stall the supervisor.
		// The sticky replay covers anything a subscriber misses here.
		select {
		case ch <- ev.Clone():
		default:
		}
	}
}

// reset clears the sticky exit. Called when a new driver run starts (spawn) and
// when the VM suspends, so a stream opened against a later run does not replay a
// previous run's exit.
func (l *lifecycle) reset() {
	l.mu.Lock()
	l.sticky = nil
	l.mu.Unlock()
}

// subscribe registers a new subscriber, returning its channel, a snapshot of the
// sticky exit to replay immediately (or nil), and an unsubscribe func.
func (l *lifecycle) subscribe() (<-chan sse.Event, *sse.Event, func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.subs == nil {
		l.subs = make(map[chan sse.Event]struct{})
	}
	ch := make(chan sse.Event, 16)
	l.subs[ch] = struct{}{}

	var sticky *sse.Event
	if l.sticky != nil {
		c := l.sticky.Clone()
		sticky = &c
	}

	unsub := func() {
		l.mu.Lock()
		delete(l.subs, ch)
		l.mu.Unlock()
	}
	return ch, sticky, unsub
}
