// Package netx resolves the addresses gritz listens on and dials, which are
// either TCP addresses or unix sockets written as unix:///path.
package netx

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"strings"
)

// unixScheme prefixes a unix socket address.
const unixScheme = "unix://"

// Split returns the network and address of addr: "unix" and the path for
// unix:///path, otherwise "tcp" and addr as it is.
func Split(addr string) (network, address string) {
	if path, ok := strings.CutPrefix(addr, unixScheme); ok {
		return "unix", path
	}
	return "tcp", addr
}

// Listen listens on addr. A unix socket file left behind at its path is
// removed first. Closing the listener removes the socket file.
func Listen(addr string) (net.Listener, error) {
	network, address := Split(addr)
	if network == "unix" {
		if err := os.Remove(address); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return net.Listen(network, address)
}

// Dial connects to addr.
func Dial(ctx context.Context, addr string) (net.Conn, error) {
	network, address := Split(addr)
	var d net.Dialer
	return d.DialContext(ctx, network, address)
}
