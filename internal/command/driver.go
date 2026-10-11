package command

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
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
		srv, err := driverserver.New(driverserver.Options{Environ: os.Environ()})
		if err != nil {
			return err
		}
		if cmd.Bool("aws-lambda-hooks") {
			// Lambda calls the MicroVM lifecycle hooks on their own port. Every
			// hook is acknowledged: the run arrives over driver.v1 and the runner
			// drives suspend and resume, so the hooks have nothing to do. The
			// hooks are never shut down, so they stay up until the process exits.
			l, err := net.Listen("tcp", fmt.Sprintf(":%d", awsmicrovm.HookPort))
			if err != nil {
				return err
			}
			slog.Info("lambda hooks listening", "addr", l.Addr().String())
			go func() {
				err := http.Serve(l, &awsmicrovm.Handler{})
				slog.Error("lambda hooks stopped", "error", err)
			}()
		}
		ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return srv.ListenAndServe(ctx, cmd.String("addr"))
	},
}
