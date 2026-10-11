// Package stdioproxy joins a reader and a writer, such as a process's stdin and
// stdout, to a TCP connection.
package stdioproxy

import (
	"context"
	"fmt"
	"io"
	"net"
)

// Run connects to addr, copies r to the connection and the connection to w,
// and returns once either side closes.
func Run(ctx context.Context, addr string, r io.Reader, w io.Writer) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %w", addr, err)
	}
	defer conn.Close()
	done := make(chan error, 2)
	go func() {
		_, err := io.Copy(conn, r)
		done <- err
	}()
	go func() {
		_, err := io.Copy(w, conn)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
