package pubsub

import (
	"context"
	"log/slog"
	"slices"
	"sync"

	"github.com/icholy/gritz/internal/model"
)

const subscriberBufSize = 64

// LocalPubSub is an in-process pub/sub implementation keyed by org ID.
type LocalPubSub struct {
	mu   sync.RWMutex
	subs map[int64][]chan model.Notification
}

// NewLocalPubSub returns a new LocalPubSub.
func NewLocalPubSub() *LocalPubSub {
	return &LocalPubSub{
		subs: make(map[int64][]chan model.Notification),
	}
}

// Publish fans out the notification to all current subscribers for the
// notification's org. A slow subscriber whose buffer is full will have the
// notification dropped rather than blocking the publisher.
func (ps *LocalPubSub) Publish(ctx context.Context, n model.Notification) error {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	for _, ch := range ps.subs[n.OrgID] {
		select {
		case ch <- n:
		default:
			slog.Warn("dropping notification for slow subscriber",
				"org_id", n.OrgID,
				"type", n.Type,
				"resources", n.Resources,
			)
		}
	}
	return nil
}

// Subscribe registers a new subscriber for the given org. It returns a channel
// that receives notifications and a cancel func that removes the subscription
// and closes the channel. The cancel func is safe to call multiple times.
// If the context is cancelled, the subscription is also removed.
func (ps *LocalPubSub) Subscribe(ctx context.Context, orgID int64) (<-chan model.Notification, func(), error) {
	ch := make(chan model.Notification, subscriberBufSize)

	ps.mu.Lock()
	ps.subs[orgID] = append(ps.subs[orgID], ch)
	ps.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			ps.mu.Lock()
			defer ps.mu.Unlock()
			ps.subs[orgID] = slices.DeleteFunc(ps.subs[orgID], func(s chan model.Notification) bool {
				return s == ch
			})
			close(ch)
		})
	}

	go func() {
		<-ctx.Done()
		cancel()
	}()

	return ch, cancel, nil
}
