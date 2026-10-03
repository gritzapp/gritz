package gritzclient_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/icholy/gritz/internal/auth/apiauth"
	"github.com/icholy/gritz/internal/gritzclient"
	"github.com/icholy/gritz/internal/model"
	"github.com/icholy/gritz/internal/server/notifyserver"
	"github.com/icholy/gritz/internal/server/pubsub"
	"github.com/icholy/gritz/internal/x/sse"
	"gotest.tools/v3/assert"
)

const (
	testOrgID    = int64(1)
	testRunnerID = "test-runner"
)

// newTestNotifyServer spins up the real notifyserver wrapped in a test
// user middleware so the NotificationClient exercises the production
// code path end-to-end. It returns the httptest server and the local
// pubsub so the test can publish notifications.
func newTestNotifyServer(t *testing.T) (*httptest.Server, *pubsub.LocalPubSub) {
	t.Helper()
	ps := pubsub.NewLocalPubSub()
	srv := notifyserver.New(notifyserver.Options{Subscriber: ps})
	handler := apiauth.WithTestUser(srv.Handler(), &apiauth.UserInfo{
		ID:    "test-user",
		OrgID: testOrgID,
		Type:  apiauth.AuthTypeApp,
	})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts, ps
}

// runClient starts c.Run in a goroutine and ensures it shuts down before
// the test ends.
func runClient(t *testing.T, c *gritzclient.NotificationClient) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		_ = c.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("client did not stop")
		}
	})
}

func recv[T any](t *testing.T, ch <-chan T, d time.Duration, msg string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(d):
		t.Fatalf("timed out waiting for %s", msg)
		var zero T
		return zero
	}
}

func TestNotificationClient_DecodesEvents(t *testing.T) {
	t.Parallel()
	// Arrange
	ts, ps := newTestNotifyServer(t)
	received := make(chan model.Notification, 8)
	c := gritzclient.NewNotificationClient(gritzclient.NotificationClientOptions{
		BaseURL: ts.URL,
		Runner:  testRunnerID,
		Handler: func(n model.Notification) { received <- n },
	})
	runClient(t, c)

	// Act + Assert: the ready event flows through decoded.
	ready := recv(t, received, time.Second, "ready notification")
	assert.Equal(t, ready.Type, "ready")

	// A real change event for this runner arrives with its fields intact.
	want := model.Notification{
		Type:   "change",
		OrgID:  testOrgID,
		Runner: testRunnerID,
		Resources: []model.NotificationResource{
			{Action: "updated", Type: "task", ID: 7},
		},
	}
	assert.NilError(t, ps.Publish(t.Context(), want))
	got := recv(t, received, time.Second, "change notification")
	assert.DeepEqual(t, got, want)
}

func TestNotificationClient_NoRunnerFilter(t *testing.T) {
	t.Parallel()
	// Arrange
	ts, ps := newTestNotifyServer(t)
	received := make(chan model.Notification, 4)
	c := gritzclient.NewNotificationClient(gritzclient.NotificationClientOptions{
		BaseURL: ts.URL,
		Handler: func(n model.Notification) { received <- n },
	})
	runClient(t, c)
	recv(t, received, time.Second, "ready notification")

	// Act: publish a notification destined for a different runner. With
	// no client-side filter, the server forwards everything in-org.
	want := model.Notification{Type: "change", OrgID: testOrgID, Runner: "other-runner"}
	assert.NilError(t, ps.Publish(t.Context(), want))

	// Assert
	got := recv(t, received, time.Second, "notification with no filter")
	assert.DeepEqual(t, got, want)
}

func TestNotificationClient_ReconnectsOnDrop(t *testing.T) {
	t.Parallel()
	// Arrange
	ts, ps := newTestNotifyServer(t)
	received := make(chan model.Notification, 8)
	c := gritzclient.NewNotificationClient(gritzclient.NotificationClientOptions{
		BaseURL:           ts.URL,
		Runner:            testRunnerID,
		ReconnectInterval: 10 * time.Millisecond,
		Handler:           func(n model.Notification) { received <- n },
	})
	runClient(t, c)
	first := recv(t, received, time.Second, "initial ready")
	assert.Equal(t, first.Type, "ready")

	// Act: drop the connection.
	ts.CloseClientConnections()

	// Assert: the client reconnects and we see a second ready.
	second := recv(t, received, 2*time.Second, "ready after reconnect")
	assert.Equal(t, second.Type, "ready")

	// A change event published after the reconnect flows through.
	assert.NilError(t, ps.Publish(t.Context(), model.Notification{
		Type: "change", OrgID: testOrgID, Runner: testRunnerID,
	}))
	got := recv(t, received, time.Second, "change after reconnect")
	assert.Equal(t, got.Type, "change")
}

func TestNotificationClient_SkipsOwnClientID(t *testing.T) {
	t.Parallel()
	// Arrange: bind the client to a specific ClientID. The handler will
	// only see notifications whose ClientID does not match.
	ts, ps := newTestNotifyServer(t)
	const myID = "bridge-self"
	received := make(chan model.Notification, 4)
	c := gritzclient.NewNotificationClient(gritzclient.NotificationClientOptions{
		BaseURL:  ts.URL,
		ClientID: myID,
		Handler:  func(n model.Notification) { received <- n },
	})
	runClient(t, c)
	// The ready event has no ClientID and must still flow through.
	ready := recv(t, received, time.Second, "ready notification")
	assert.Equal(t, ready.Type, "ready")

	// Act: publish two changes — one tagged with our id (self-echo), one
	// from a different client. Only the second should arrive.
	assert.NilError(t, ps.Publish(t.Context(), model.Notification{
		Type:     "change",
		OrgID:    testOrgID,
		ClientID: myID,
		Resources: []model.NotificationResource{
			{Action: "updated", Type: "task", ID: 1},
		},
	}))
	assert.NilError(t, ps.Publish(t.Context(), model.Notification{
		Type:     "change",
		OrgID:    testOrgID,
		ClientID: "other-client",
		Resources: []model.NotificationResource{
			{Action: "updated", Type: "task", ID: 2},
		},
	}))

	// Assert: the only delivered change is the one from the other client.
	got := recv(t, received, time.Second, "non-self change")
	assert.Equal(t, got.ClientID, "other-client")
	assert.Equal(t, got.Resources[0].ID, int64(2))

	// Drain to confirm no self-echo follows.
	select {
	case n := <-received:
		t.Fatalf("unexpected echoed notification: %+v", n)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestNotificationClient_SkipsMalformedData(t *testing.T) {
	t.Parallel()
	// Arrange: a tiny standalone handler — the real notifyserver only
	// emits well-formed JSON, so we need a custom one to test that the
	// client survives a bad payload and keeps consuming subsequent events.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw, err := sse.NewServerWriter(w)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = sw.Write(sse.Event{Event: "ready", Data: []byte("not-json")})
		_ = sw.Write(sse.Event{Event: "change", Data: []byte(`{"type":"change","org_id":9}`)})
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	received := make(chan model.Notification, 4)
	c := gritzclient.NewNotificationClient(gritzclient.NotificationClientOptions{
		BaseURL: srv.URL,
		Handler: func(n model.Notification) { received <- n },
	})
	runClient(t, c)

	// Act + Assert: malformed event is skipped; subsequent valid event arrives.
	got := recv(t, received, time.Second, "change after malformed")
	assert.DeepEqual(t, got, model.Notification{Type: "change", OrgID: 9})
}
