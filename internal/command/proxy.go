package command

import (
	"context"
	"fmt"
	"io"
	"net"

	"github.com/urfave/cli/v3"
)

var ProxyCommand = &cli.Command{
	Name:   "proxy",
	Usage:  "Connect stdin and stdout to a TCP address",
	Hidden: true,
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "addr",
			Usage:    "TCP address to connect to",
			Required: true,
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		return proxy(ctx, cmd.String("addr"), cmd.Root().Reader, cmd.Root().Writer)
	},
}

// proxy connects to addr, copies r to the connection and the connection to w,
// and returns once either side closes. The experimental docker backend runs it
// in a Docker exec to tunnel a connection to the driver server.
func proxy(ctx context.Context, addr string, r io.Reader, w io.Writer) error {
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
