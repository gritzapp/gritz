package apiserver

import (
	"context"
	"testing"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/server/pubsub"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestServerPublish_IgnoreSuppressesDelivery(t *testing.T) {
	t.Parallel()

	pub := &pubsub.PublisherMock{
		PublishFunc: func(_ context.Context, _ model.Notification) error { return nil },
	}
	srv := New(Options{Publisher: pub})

	srv.publish(model.Notification{Type: "change", OrgID: 1, Ignore: true})
	assert.Assert(t, cmp.Len(pub.PublishedNotifications(), 0))

	srv.publish(model.Notification{Type: "change", OrgID: 1})
	assert.Assert(t, cmp.Len(pub.PublishedNotifications(), 1))
}
