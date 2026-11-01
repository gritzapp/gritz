package command

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/gritzapp/gritz/internal/runner/driverserver"
	"github.com/gritzapp/gritz/internal/x/awsmicrovm/validatescript"
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
		&cli.StringFlag{
			Name:  "aws-lambda-validate",
			Usage: "Executable to run on the AWS Lambda MicroVM /validate image hook, to warm the snapshot (requires --aws-lambda-hooks)",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		if cmd.IsSet("aws-lambda-validate") && !cmd.Bool("aws-lambda-hooks") {
			return errors.New("--aws-lambda-validate requires --aws-lambda-hooks")
		}
		srv, err := driverserver.New(driverserver.Options{Environ: os.Environ()})
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		if cmd.Bool("aws-lambda-hooks") {
			// Lambda calls the MicroVM lifecycle hooks on their own port. The run
			// arrives over driver.v1 and the runner drives suspend and resume, so
			// those hooks have nothing to do and are acknowledged. /validate runs
			// the image's warmup script when one is given. The hooks are never
			// shut down, so they stay up until the process exits.
			hooks := &awsmicrovm.Handler{}
			if path := cmd.String("aws-lambda-validate"); path != "" {
				script, err := validatescript.New(ctx, validatescript.Options{Path: path, Environ: os.Environ()})
				if err != nil {
					return err
				}
				hooks.Validate = script.Validate
			}
			l, err := net.Listen("tcp", fmt.Sprintf(":%d", awsmicrovm.HookPort))
			if err != nil {
				return err
			}
			slog.Info("lambda hooks listening", "addr", l.Addr().String())
			go func() {
				err := http.Serve(l, hooks)
				slog.Error("lambda hooks stopped", "err", err)
			}()
		}
		return srv.ListenAndServe(ctx, cmd.String("addr"))
	},
}
