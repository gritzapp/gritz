package pubsub

import (
	"context"
	"testing"
	"time"

	"github.com/icholy/gritz/internal/model"
	"gotest.tools/v3/assert"
)

func TestPublish_NoSubscribers(t *testing.T) {
	t.Parallel()
	ps := NewLocalPubSub()

	err := ps.Publish(context.Background(), model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "created", Type: "task", ID: 1}},
		OrgID:     1,
	})

	assert.NilError(t, err)
}

func TestSubscribe_ReceivesNotification(t *testing.T) {
	t.Parallel()
	ps := NewLocalPubSub()
	ch, cancel, err := ps.Subscribe(context.Background(), 1)
	assert.NilError(t, err)
	defer cancel()

	want := model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "updated", Type: "task", ID: 42}},
		OrgID:     1,
		Time:      time.Now().Truncate(time.Second),
	}
	err = ps.Publish(context.Background(), want)
	assert.NilError(t, err)

	select {
	case got := <-ch:
		assert.DeepEqual(t, got, want)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for notification")
	}
}

func TestSubscribe_OrgIsolation(t *testing.T) {
	t.Parallel()
	ps := NewLocalPubSub()
	ch, cancel, err := ps.Subscribe(context.Background(), 1)
	assert.NilError(t, err)
	defer cancel()

	err = ps.Publish(context.Background(), model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "created", Type: "task", ID: 1}},
		OrgID:     2,
	})
	assert.NilError(t, err)

	select {
	case n := <-ch:
		t.Fatalf("received unexpected notification: %+v", n)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSubscribe_MultipleSubscribers(t *testing.T) {
	t.Parallel()
	ps := NewLocalPubSub()

	ch1, cancel1, err := ps.Subscribe(context.Background(), 1)
	assert.NilError(t, err)
	defer cancel1()

	ch2, cancel2, err := ps.Subscribe(context.Background(), 1)
	assert.NilError(t, err)
	defer cancel2()

	want := model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "created", Type: "task", ID: 1}},
		OrgID:     1,
	}
	err = ps.Publish(context.Background(), want)
	assert.NilError(t, err)

	for _, ch := range []<-chan model.Notification{ch1, ch2} {
		select {
		case got := <-ch:
			assert.DeepEqual(t, got, want)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for notification")
		}
	}
}

func TestCancel_RemovesSubscription(t *testing.T) {
	t.Parallel()
	ps := NewLocalPubSub()
	ch, cancel, err := ps.Subscribe(context.Background(), 1)
	assert.NilError(t, err)

	cancel()

	// Channel should be closed.
	_, ok := <-ch
	assert.Assert(t, !ok, "expected channel to be closed")

	// Publishing after cancel should not panic.
	err = ps.Publish(context.Background(), model.Notification{
		Type:      "change",
		Resources: []model.NotificationResource{{Action: "created", Type: "task", ID: 1}},
		OrgID:     1,
	})
	assert.NilError(t, err)
}

func TestCancel_DoubleCallSafe(t *testing.T) {
	t.Parallel()
	ps := NewLocalPubSub()
	_, cancel, err := ps.Subscribe(context.Background(), 1)
	assert.NilError(t, err)

	cancel()
	cancel() // must not panic
}

func TestPublish_SlowSubscriberDoesNotBlock(t *testing.T) {
	t.Parallel()
	ps := NewLocalPubSub()
	ch, cancel, err := ps.Subscribe(context.Background(), 1)
	assert.NilError(t, err)
	defer cancel()

	// Fill the subscriber's buffer.
	for i := range subscriberBufSize {
		err = ps.Publish(context.Background(), model.Notification{
			Type:      "change",
			Resources: []model.NotificationResource{{Action: "updated", Type: "task", ID: int64(i)}},
			OrgID:     1,
		})
		assert.NilError(t, err)
	}

	// Next publish should not block — it drops the notification.
	done := make(chan struct{})
	go func() {
		err := ps.Publish(context.Background(), model.Notification{
			Type:      "change",
			Resources: []model.NotificationResource{{Action: "updated", Type: "task", ID: 999}},
			OrgID:     1,
		})
		assert.NilError(t, err)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publish blocked on slow subscriber")
	}

	// Drain to make sure the buffered messages are there.
	for range subscriberBufSize {
		<-ch
	}
}

func TestSubscribe_ContextCancellation(t *testing.T) {
	t.Parallel()
	ps := NewLocalPubSub()
	ctx, ctxCancel := context.WithCancel(context.Background())
	ch, _, err := ps.Subscribe(ctx, 1)
	assert.NilError(t, err)

	ctxCancel()

	// Channel should be closed after context cancellation.
	select {
	case _, ok := <-ch:
		assert.Assert(t, !ok, "expected channel to be closed")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel close after context cancel")
	}
}
