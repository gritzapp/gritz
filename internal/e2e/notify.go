package e2e

import (
	"context"
	"sync"
	"time"

	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
)

// Notifications is a subscription to an org's notifications on the server's
// /events stream, as the web UI and `gritz notify` hold one.
type Notifications struct {
	ch     chan model.Notification
	server *Server
}

// Subscribe subscribes to the org's notifications as its owner, and returns
// once the subscription is live: every notification published after it returns
// is delivered.
func (o *Org) Subscribe() *Notifications {
	s := o.server
	s.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	n := &Notifications{ch: make(chan model.Notification, 64), server: s}
	ready := make(chan struct{})
	var once sync.Once
	client := gritzclient.NewNotificationClient(gritzclient.NotificationClientOptions{
		BaseURL:     s.URL,
		TokenSource: gritzclient.StaticToken(s.userToken(o.Owner.UserID, o.ID)),
		Handler: func(msg model.Notification) {
			if msg.Type == "ready" {
				once.Do(func() { close(ready) })
				return
			}
			select {
			case n.ch <- msg:
			case <-ctx.Done():
			}
		},
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = client.Run(ctx)
	}()
	s.t.Cleanup(func() {
		cancel()
		<-done
	})
	select {
	case <-ready:
	case <-time.After(s.timeout):
		s.t.Fatalf("Subscribe: subscription not live after %s", s.timeout)
	}
	return n
}

// Next waits for the next notification and returns it.
func (n *Notifications) Next() model.Notification {
	t := n.server.t
	t.Helper()
	select {
	case msg := <-n.ch:
		return msg
	case <-time.After(n.server.timeout):
		t.Fatalf("Next: no notification after %s", n.server.timeout)
		return model.Notification{}
	}
}
