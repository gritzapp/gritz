//go:generate go tool moq -with-resets -out publisher_moq.go . Publisher

package pubsub

import (
	"context"

	"github.com/icholy/gritz/internal/model"
)

// Publisher publishes notifications to subscribers of the notification's org.
type Publisher interface {
	Publish(ctx context.Context, n model.Notification) error
}

// Subscriber subscribes to notifications for the given org. The returned
// channel receives notifications until the cancel func is called or the
// context is cancelled. The channel is closed when the subscription ends.
// Calling the cancel func more than once is safe.
type Subscriber interface {
	Subscribe(ctx context.Context, orgID int64) (<-chan model.Notification, func(), error)
}
