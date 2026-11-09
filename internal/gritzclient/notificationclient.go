package gritzclient

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/x/common"
	"github.com/gritzapp/gritz/internal/x/sse"
)

// DefaultSSEReconnectInterval is the wait between reconnect attempts when
// the SSE connection drops.
const DefaultSSEReconnectInterval = 5 * time.Second

// NotificationClient connects to the server's /events SSE endpoint,
// decodes each event into a model.Notification, and hands it to a caller
// supplied handler. Reconnects automatically on disconnect.
type NotificationClient struct {
	baseURL   string
	runner    string
	clientID  string
	http      *http.Client
	log       *slog.Logger
	reconnect time.Duration
	handler   func(model.Notification)
	onHealth  func(Health)
}

// Health is the state of the notification stream, reported to
// NotificationClientOptions.OnHealth.
type Health struct {
	Connected bool  // the stream is up
	Err       error // why it is down; nil while connected
}

// NotificationClientOptions configures a NotificationClient.
type NotificationClientOptions struct {
	// BaseURL is the server URL (e.g. https://gritz.dev).
	BaseURL string
	// Runner is sent as the ?runner= filter; when empty, no filter is sent
	// and the server forwards the whole-org stream.
	Runner string
	// TokenSource supplies the bearer token for each SSE request; if nil,
	// no authentication is performed. The client builds its own no-timeout
	// http.Client internally (SSE connections are long-lived).
	TokenSource oauth2.TokenSource
	// ClientID, when non-empty, is the per-process id paired with the
	// Connect client's X-Client-ID header. Notifications whose ClientID
	// matches are dropped before the handler runs so a caller that
	// issues mutations and subscribes to notifications doesn't echo
	// its own events back to itself.
	ClientID string
	// Log is used for connection diagnostics.
	Log *slog.Logger
	// ReconnectInterval is the wait between reconnect attempts after a
	// disconnect. Defaults to DefaultSSEReconnectInterval.
	ReconnectInterval time.Duration
	// Handler is called once per received event with the decoded
	// notification. Required.
	Handler func(model.Notification)
	// OnHealth, if set, is called from Run whenever Connected changes: the
	// first connect or first failure, an established stream dropping, and
	// a failing stream reconnecting. Retries that keep failing don't call
	// it again.
	OnHealth func(Health)
}

// NewNotificationClient returns a new NotificationClient.
func NewNotificationClient(opts NotificationClientOptions) *NotificationClient {
	transport := http.DefaultTransport
	if opts.TokenSource != nil {
		transport = &AuthTransport{
			Transport: transport,
			Source:    opts.TokenSource,
			ClientID:  opts.ClientID,
		}
	}
	return &NotificationClient{
		baseURL:   opts.BaseURL,
		runner:    opts.Runner,
		clientID:  opts.ClientID,
		http:      &http.Client{Transport: transport},
		log:       cmp.Or(opts.Log, slog.Default()),
		reconnect: cmp.Or(opts.ReconnectInterval, DefaultSSEReconnectInterval),
		handler:   opts.Handler,
		onHealth:  opts.OnHealth,
	}
}

// Run connects to the SSE endpoint and dispatches events to the handler
// until ctx is done. It reconnects automatically on disconnect.
func (c *NotificationClient) Run(ctx context.Context) error {
	var reported, connected bool
	report := func(h Health) {
		if reported && connected == h.Connected {
			return
		}
		reported, connected = true, h.Connected
		if c.onHealth != nil {
			c.onHealth(h)
		}
	}
	for {
		err := c.connect(ctx, func() { report(Health{Connected: true}) })
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.log.Warn("SSE connection lost, reconnecting", "err", err)
		report(Health{Err: err})
		if !common.SleepContext(ctx, c.reconnect) {
			return ctx.Err()
		}
	}
}

// connect streams events until the connection ends, calling onConnected
// once the server has accepted the stream. It always returns a non-nil
// error.
func (c *NotificationClient) connect(ctx context.Context, onConnected func()) error {
	u, err := url.Parse(strings.TrimRight(c.baseURL, "/") + "/events")
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	if c.runner != "" {
		q := u.Query()
		q.Set("runner", c.runner)
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	onConnected()
	r := sse.NewReader(resp.Body)
	for {
		ev, err := r.Read()
		if err != nil {
			return err
		}
		// The reader returns a zero Event with nil error on clean EOF.
		// Real events always carry an Event type from the server.
		if ev.Event == "" {
			return errors.New("stream closed by server")
		}
		// Keep-alives exist only to put bytes on an idle stream. They carry
		// no data, so drop them before decoding.
		if ev.Event == "keepalive" {
			continue
		}
		var n model.Notification
		if err := json.Unmarshal(ev.Data, &n); err != nil {
			c.log.Warn("failed to decode notification", "err", err)
			continue
		}
		// Drop notifications that originated from this client so callers
		// that both mutate and subscribe don't echo their own events.
		if c.clientID != "" && n.ClientID == c.clientID {
			continue
		}
		c.handler(n)
	}
}
