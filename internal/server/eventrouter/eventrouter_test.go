package eventrouter

import (
	"context"
	"log/slog"
	"testing"

	"github.com/icholy/gritz/internal/model"
	"github.com/icholy/gritz/internal/server/pubsub"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// This test stays in the internal package because it exercises the unexported
// publish method. The Route matching tests live in the external eventrouter_test
// package (route_test.go): they inject a schema registry built from the producer
// packages, which import eventrouter and so cannot be imported from here without
// a cycle.
func TestRouterPublish_IgnoreSuppressesDelivery(t *testing.T) {
	t.Parallel()

	pub := &pubsub.PublisherMock{
		PublishFunc: func(_ context.Context, _ model.Notification) error { return nil },
	}
	r := &Router{Log: slog.Default(), Publisher: pub}

	r.publish(t.Context(), model.Notification{Type: "change", OrgID: 1, Ignore: true})
	assert.Assert(t, cmp.Len(pub.PublishedNotifications(), 0))

	r.publish(t.Context(), model.Notification{Type: "change", OrgID: 1})
	assert.Assert(t, cmp.Len(pub.PublishedNotifications(), 1))
}
