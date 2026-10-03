package command

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/icholy/gritz/internal/runner/microvmshim"
	"github.com/icholy/gritz/internal/x/awsmicrovm"
	"github.com/urfave/cli/v3"
)

// MicrovmShimCommand is the application entrypoint baked into Lambda MicroVMs
// images. It serves the AWS lifecycle hooks (on the dedicated hook port) plus
// the gritz control surface (on the ingress port), fetches the task's spec
// bundle on /run, (re-)spawns and supervises the driver, and reports the
// driver's exit over the /gritz/lifecycle SSE stream. It holds NO AWS
// credentials and makes NO control-plane calls — suspend/resume/terminate are
// the runner's. See the lambdamicrovm backend.
var MicrovmShimCommand = &cli.Command{
	Name:  "microvm-shim",
	Usage: "Run the in-MicroVM lifecycle-hook server (Lambda MicroVMs image entrypoint)",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "addr",
			Usage: "Address to serve the gritz control surface (/gritz/*) on; the ingress port the runner reaches over the proxy",
			Value: fmt.Sprintf(":%d", awsmicrovm.DefaultPort),
		},
		&cli.StringFlag{
			Name:  "hook-addr",
			Usage: "Address to serve the AWS lifecycle hooks on; must match the create-microvm-image --hooks port= declaration",
			Value: fmt.Sprintf(":%d", awsmicrovm.HookPort),
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		srv := &microvmshim.Server{Log: slog.Default()}
		slog.Info("microvm-shim listening", "addr", cmd.String("addr"), "hook_addr", cmd.String("hook-addr"))
		return srv.ListenAndServe(ctx, cmd.String("addr"), cmd.String("hook-addr"))
	},
}
