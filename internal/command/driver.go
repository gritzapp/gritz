package command

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/gritzapp/gritz/internal/runner/driverserver"
	"github.com/gritzapp/gritz/internal/x/awsmicrovm"
	"github.com/urfave/cli/v3"
)

var DriverCommand = &cli.Command{
	Name:  "driver",
	Usage: "Serve driver.v1.DriverService and execute runs as they arrive",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "addr",
			Usage: "TCP address or unix:///path to serve on",
			Value: ":8080",
		},
		&cli.BoolFlag{
			Name:  "aws-lambda-hooks",
			Usage: "Also acknowledge the AWS Lambda MicroVM lifecycle hooks on their port",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		opts := driverserver.Options{Environ: os.Environ()}
		if cmd.Bool("aws-lambda-hooks") {
			opts.HookAddr = fmt.Sprintf(":%d", awsmicrovm.HookPort)
		}
		srv, err := driverserver.New(opts)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return srv.ListenAndServe(ctx, cmd.String("addr"))
	},
}
