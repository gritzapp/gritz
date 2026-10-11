// Package stdioproxy joins a reader and a writer, such as a process's stdin and
// stdout, to a TCP or unix socket connection.
package stdioproxy

import (
	"context"
	"fmt"
	"io"

	"github.com/gritzapp/gritz/internal/x/netx"
)

// Run connects to addr, a TCP address or unix:///path, copies r to the connection and the connection to w,
// and returns once either side closes.
func Run(ctx context.Context, addr string, r io.Reader, w io.Writer) error {
	conn, err := netx.Dial(ctx, addr)
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
