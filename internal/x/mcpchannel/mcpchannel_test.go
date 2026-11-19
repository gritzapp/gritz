package mcpchannel

import (
	"context"
	"encoding/json/v2"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gotest.tools/v3/assert"
)

type fakeConn struct {
	writes []jsonrpc.Message
}

func (c *fakeConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *fakeConn) Write(_ context.Context, msg jsonrpc.Message) error {
	c.writes = append(c.writes, msg)
	return nil
}

func (c *fakeConn) Close() error      { return nil }
func (c *fakeConn) SessionID() string { return "fake" }

type fakeTransport struct{ conn mcp.Connection }

func (t *fakeTransport) Connect(_ context.Context) (mcp.Connection, error) {
	return t.conn, nil
}

func TestTransport_SendChannel(t *testing.T) {
	t.Parallel()
	// Arrange
	conn := &fakeConn{}
	tr := NewTransport(&fakeTransport{conn: conn})
	if _, err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	params := Params{
		Content: "task 42 was updated.",
		Meta: map[string]string{
			"action":   "updated",
			"resource": "task",
			"id":       "42",
		},
	}

	// Act
	err := tr.SendChannel(context.Background(), params)

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, len(conn.writes), 1)
	req, ok := conn.writes[0].(*jsonrpc.Request)
	assert.Assert(t, ok, "expected *jsonrpc.Request, got %T", conn.writes[0])
	assert.Equal(t, req.Method, Method)
	assert.Assert(t, !req.ID.IsValid(), "notification must have no ID")

	var got Params
	assert.NilError(t, json.Unmarshal(req.Params, &got))
	assert.DeepEqual(t, got, params)
}

func TestTransport_SendChannel_NotConnected(t *testing.T) {
	t.Parallel()
	// Arrange
	tr := NewTransport(&fakeTransport{conn: &fakeConn{}})

	// Act
	err := tr.SendChannel(context.Background(), Params{Content: "x"})

	// Assert
	assert.ErrorContains(t, err, "not connected")
}

func TestTransport_Connect_PropagatesError(t *testing.T) {
	t.Parallel()
	tr := NewTransport(errTransport{err: errors.New("boom")})
	_, err := tr.Connect(context.Background())
	assert.ErrorContains(t, err, "boom")
}

type errTransport struct{ err error }

func (e errTransport) Connect(_ context.Context) (mcp.Connection, error) {
	return nil, e.err
}

func TestExperimental(t *testing.T) {
	t.Parallel()
	caps := Experimental()
	v, ok := caps[CapabilityKey]
	assert.Assert(t, ok)
	m, ok := v.(map[string]any)
	assert.Assert(t, ok)
	assert.Equal(t, len(m), 0)
}
