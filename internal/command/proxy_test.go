package command

import (
	"bytes"
	"io"
	"net"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// listen returns the address of a TCP listener that hands each accepted
// connection to handle. It is closed on cleanup.
func listen(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(conn)
		}
	}()
	return ln.Addr().String()
}

// The proxy copies stdin to the connection and the connection to stdout, and
// returns once the connection closes.
func TestProxy_ConnCloses(t *testing.T) {
	received := make(chan string, 1)
	addr := listen(t, func(conn net.Conn) {
		defer conn.Close()
		buf := make([]byte, len("ping"))
		_, err := io.ReadFull(conn, buf)
		received <- string(buf)
		if err == nil {
			_, _ = conn.Write([]byte("pong"))
		}
	})
	// stdin stays open: only the connection closing ends the proxy.
	stdin, stdinW := io.Pipe()
	t.Cleanup(func() { _ = stdinW.Close() })
	go func() { _, _ = stdinW.Write([]byte("ping")) }()
	var stdout bytes.Buffer

	err := proxy(t.Context(), addr, stdin, &stdout)

	assert.NilError(t, err)
	assert.Equal(t, <-received, "ping")
	assert.Equal(t, stdout.String(), "pong")
}

// The proxy returns once stdin closes, closing the connection.
func TestProxy_StdinCloses(t *testing.T) {
	closed := make(chan string, 1)
	addr := listen(t, func(conn net.Conn) {
		defer conn.Close()
		data, _ := io.ReadAll(conn)
		closed <- string(data)
	})

	err := proxy(t.Context(), addr, strings.NewReader("hello"), io.Discard)

	assert.NilError(t, err)
	assert.Equal(t, <-closed, "hello")
}

func TestProxy_DialFails(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NilError(t, err)
	addr := ln.Addr().String()
	assert.NilError(t, ln.Close())

	err = proxy(t.Context(), addr, strings.NewReader(""), io.Discard)

	assert.ErrorContains(t, err, "failed to connect")
}
