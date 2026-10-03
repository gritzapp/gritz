package pubsub

import (
	"github.com/icholy/gritz/internal/model"
)

// PublishedNotifications returns every notification passed to Publish across all
// calls, in call order.
func (mock *PublisherMock) PublishedNotifications() []model.Notification {
	var notifications []model.Notification
	for _, call := range mock.PublishCalls() {
		notifications = append(notifications, call.N)
	}
	return notifications
}
