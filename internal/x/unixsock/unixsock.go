// Package unixsock serves and dials HTTP over unix domain sockets.
package unixsock

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Scheme prefixes an address that names a unix socket: unix:///path/to/socket.
const Scheme = "unix://"

// CutScheme returns the socket path of a unix:///path address, and whether
// addr is one.
func CutScheme(addr string) (string, bool) {
	return strings.CutPrefix(addr, Scheme)
}

// Listen listens on the unix socket at path, creating its parent directory.
// Whatever is at path is removed first: a socket left by an earlier process,
// or the directory Docker creates when a bind-mount source is missing. The
// socket is made world-accessible, so a process running as another user can
// connect to it.
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(path); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0666); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// NewTransport returns an HTTP transport whose every connection dials the unix
// socket at path; the host of a request's URL is ignored.
func NewTransport(path string) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}
}

// NewClient returns an HTTP client that sends every request to the unix socket
// at path.
func NewClient(path string) *http.Client {
	return &http.Client{Transport: NewTransport(path)}
}
