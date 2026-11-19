// Package mcpchannel implements the Claude Code "channel" protocol
// (capability key "claude/channel" + notification method
// "notifications/claude/channel") on top of the MCP Go SDK.
//
// The SDK does not expose a general-purpose server-side notification
// API, so this package wraps an mcp.Transport with one that retains
// the live mcp.Connection and lets callers inject typed channel
// notifications alongside the SDK's own writes.
//
// The package is gritz-agnostic: it knows only the Claude Code
// channel protocol and the MCP SDK.
package mcpchannel

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// CapabilityKey is the experimental capability key servers set to
// advertise that they emit Claude Code channel notifications.
const CapabilityKey = "claude/channel"

// Method is the JSON-RPC method name for a channel notification.
const Method = "notifications/claude/channel"

// Params is the payload of a notifications/claude/channel notification.
// Content becomes the body of the <channel> tag in Claude's context;
// each Meta entry becomes a tag attribute. Meta keys must be valid
// identifiers (letters, digits, underscores) — non-identifier keys are
// silently dropped by Claude Code.
type Params struct {
	Content string `json:"content"`
	// Meta encodes as an object even when nil: Claude Code validates
	// params.meta as a record and drops a notification whose meta is null.
	Meta map[string]string `json:"meta"`
}

// Experimental returns the experimental capabilities map that
// advertises the Claude Code channel capability, suitable for use in
// mcp.ServerCapabilities.Experimental.
func Experimental() map[string]any {
	return map[string]any{CapabilityKey: map[string]any{}}
}

// Transport wraps an mcp.Transport so callers can inject send-only
// channel notifications on the same connection the SDK uses, with
// writes serialized so injected frames cannot interleave with the
// SDK's own writes.
//
// TODO(go-sdk #745): replace with upstream ServerSession.Notify once
// the combined send/receive design lands.
// https://github.com/modelcontextprotocol/go-sdk/issues/745
type Transport struct {
	inner mcp.Transport

	mu   sync.Mutex
	conn mcp.Connection
}

// NewTransport returns a Transport that wraps inner.
func NewTransport(inner mcp.Transport) *Transport {
	return &Transport{inner: inner}
}

// Connect implements mcp.Transport. It delegates to the inner
// transport, retains the returned connection so SendChannel can write
// to it, and returns a wrapper connection that funnels writes through
// the same mutex.
func (t *Transport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.conn = conn
	t.mu.Unlock()
	return &connection{t: t, inner: conn}, nil
}

// SendChannel writes a Claude Code channel notification on the
// retained connection. It is safe to call concurrently with the SDK's
// own writes. Returns an error if the transport has not been
// connected.
func (t *Transport) SendChannel(ctx context.Context, p Params) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}
	return t.write(ctx, &jsonrpc.Request{Method: Method, Params: raw})
}

// write sends a raw JSON-RPC message on the retained connection,
// holding the mutex so it does not interleave with SDK writes.
func (t *Transport) write(ctx context.Context, msg jsonrpc.Message) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conn == nil {
		return fmt.Errorf("mcpchannel: transport not connected")
	}
	return t.conn.Write(ctx, msg)
}

// connection wraps an mcp.Connection and serializes writes through
// the parent transport's mutex.
type connection struct {
	t     *Transport
	inner mcp.Connection
}

func (c *connection) Read(ctx context.Context) (jsonrpc.Message, error) {
	return c.inner.Read(ctx)
}

func (c *connection) Write(ctx context.Context, msg jsonrpc.Message) error {
	c.t.mu.Lock()
	defer c.t.mu.Unlock()
	return c.inner.Write(ctx, msg)
}

func (c *connection) Close() error {
	return c.inner.Close()
}

func (c *connection) SessionID() string {
	return c.inner.SessionID()
}
