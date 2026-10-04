package command

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/urfave/cli/v3"
)

// logPageSize bounds each ListLogChunksByTask page the log walk fetches. Chunks
// are cut at 32 KiB by the driver's shipper, so a page is a few MiB worst case.
const logPageSize = 50

// LogsCommand prints a task's driver log — the same byte stream the driver tees
// to /gritz/log inside the sandbox, mirrored to the server by agent.LogShipper.
// It reads the transcript over ListLogChunksByTask rather than shelling out to
// `docker logs`, so it works from anywhere the CLI can reach the server, for any
// task the caller can read, including tasks whose sandbox is long gone.
var LogsCommand = &cli.Command{
	Name:      "logs",
	Usage:     "Display the driver log for a task",
	ArgsUsage: "<task-id>",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "server",
			Aliases: []string{"s"},
			Usage:   "server URL",
			Value:   gritzclient.DefaultURL,
			Sources: cli.EnvVars("GRITZ_SERVER"),
		},
		&cli.BoolFlag{
			Name:    "follow",
			Aliases: []string{"f"},
			Usage:   "Follow log output",
		},
		&cli.DurationFlag{
			Name:  "interval",
			Usage: "Poll interval when following",
			Value: 2 * time.Second,
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		if cmd.NArg() < 1 {
			return cli.Exit("task ID required", 1)
		}
		taskID, err := strconv.ParseInt(cmd.Args().First(), 10, 64)
		if err != nil {
			return cli.Exit("invalid task ID: "+cmd.Args().First(), 1)
		}
		cfg, err := configfile.Load(nil)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		if cfg.Token == "" {
			return fmt.Errorf("not authenticated, run setup first")
		}
		client := gritzclient.New(gritzclient.Options{
			BaseURL: cmd.String("server"),
			Token:   cfg.Token,
		})
		tasklog := gritzclient.OpenTaskLog(client, taskID, logPageSize)
		for chunk, err := range tasklog.History(ctx) {
			if err != nil {
				return fmt.Errorf("failed to list log chunks: %w", err)
			}
			if _, err := os.Stdout.Write(chunk.GetData()); err != nil {
				return fmt.Errorf("failed to write log output: %w", err)
			}
		}
		if !cmd.Bool("follow") {
			return nil
		}
		for chunk, err := range tasklog.Follow(ctx, cmd.Duration("interval")) {
			if err != nil {
				return fmt.Errorf("failed to list log chunks: %w", err)
			}
			if _, err := os.Stdout.Write(chunk.GetData()); err != nil {
				return fmt.Errorf("failed to write log output: %w", err)
			}
		}
		return nil
	},
}
