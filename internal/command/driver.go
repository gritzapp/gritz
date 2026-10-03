package command

import (
	"context"
	"os"
	"strings"

	"github.com/icholy/gritz/internal/gritzclient"
	"github.com/icholy/gritz/internal/runner/agent"
	"github.com/icholy/gritz/internal/runner/logship"
	"github.com/urfave/cli/v3"
)

var DriverCommand = &cli.Command{
	Name:  "driver",
	Usage: "Run an agent for a task",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "server",
			Aliases: []string{"s"},
			Usage:   "server URL",
			Value:   gritzclient.DefaultURL,
		},
		&cli.Int64Flag{
			Name:     "task",
			Aliases:  []string{"t"},
			Usage:    "Task ID to execute",
			Required: true,
		},
		&cli.StringFlag{
			Name:  "token",
			Usage: "Authentication token for the agent",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		taskID := cmd.Int64("task")
		client := gritzclient.New(gritzclient.Options{
			BaseURL: cmd.String("server"),
			Token:   cmd.String("token"),
		})

		// Mirror the log to the server as well as to /gritz/log, so a run stays
		// debuggable after its sandbox is pruned and without shell access to it.
		// The shipper is built before anything else touches the server — in
		// particular before the driver's task fetch — so bytes emitted before
		// the run's version is known are still buffered, stamped version 0 until
		// DriverLog.StartRun stamps the run. Its sender runs for the driver's
		// lifetime; the deferred cancel below stops it after the final flush.
		//
		// Declared secret values are masked on their way into the shipper, so
		// they never reach the server; /gritz/log and stderr stay raw.
		shipper := logship.New(client, logship.Options{
			TaskID:  taskID,
			Secrets: driverSecrets(cmd.String("token")),
		})
		shipCtx, stopShipper := context.WithCancel(ctx)
		defer stopShipper()
		go shipper.Run(shipCtx)

		// Open the append-only in-sandbox log: its logger tees the driver's slog
		// output to os.Stderr, /gritz/log and the shipper, and its sink is teed
		// into setup command and Claude CLI stdio, so a completed run can be
		// inspected post-mortem via the reverse-shell or the server. Opening is
		// best-effort and never fails the run (see agent.OpenDriverLog). Close
		// flushes the shipper as a backstop for early-error exits.
		log := agent.OpenDriverLog(agent.DefaultLogPath, shipper)
		defer log.Close()

		driver := &agent.Driver{
			TaskID:    taskID,
			Client:    client,
			Log:       log,
			Config:    agent.DefaultConfigStore,
			ServerURL: cmd.String("server"),
			Token:     cmd.String("token"),
		}
		return driver.Run(ctx)
	},
}

// driverSecrets returns the values the driver masks in the log it ships: the
// workspace secrets the runner declared in GRITZ_SECRETS, whose values it reads
// from its own environment where the runner injected them, plus its own task
// token — the one secret the platform mints rather than the workspace, and the
// string agents disclose when they log their MCP config.
func driverSecrets(token string) map[string]string {
	secrets := map[string]string{}
	for _, name := range strings.Split(os.Getenv("GRITZ_SECRETS"), ",") {
		if name != "" {
			secrets[name] = os.Getenv(name)
		}
	}
	// Last, so a workspace secret that happens to be named "token" cannot
	// displace the task JWT and leave it shipping unmasked.
	secrets["token"] = token
	return secrets
}
