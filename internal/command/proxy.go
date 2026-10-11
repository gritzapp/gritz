package command

import (
	"context"

	"github.com/gritzapp/gritz/internal/x/stdioproxy"
	"github.com/urfave/cli/v3"
)

var ProxyCommand = &cli.Command{
	Name:   "proxy",
	Usage:  "Connect stdin and stdout to a TCP address or unix socket",
	Hidden: true,
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "addr",
			Usage:    "TCP address or unix:///path to connect to",
			Required: true,
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		return stdioproxy.Run(ctx, cmd.String("addr"), cmd.Root().Reader, cmd.Root().Writer)
	},
}
