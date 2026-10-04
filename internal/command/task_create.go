package command

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/urfave/cli/v3"
	"google.golang.org/protobuf/types/known/durationpb"
)

var TaskCreateCommand = &cli.Command{
	Name:  "create",
	Usage: "Create a new task",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "server",
			Aliases: []string{"s"},
			Usage:   "server URL",
			Value:   gritzclient.DefaultURL,
			Sources: cli.EnvVars("GRITZ_SERVER"),
		},
		&cli.StringFlag{
			Name:    "name",
			Aliases: []string{"n"},
			Usage:   "Task name",
		},
		&cli.StringFlag{
			Name:     "runner",
			Aliases:  []string{"r"},
			Usage:    "Runner ID to assign this task to",
			Required: true,
		},
		&cli.StringFlag{
			Name:    "workspace",
			Aliases: []string{"w"},
			Usage:   "Workspace to use",
			Value:   "default",
		},
		&cli.StringSliceFlag{
			Name:    "instruction",
			Aliases: []string{"i"},
			Usage:   "Instruction to execute (can be specified multiple times)",
		},
		&cli.DurationFlag{
			Name:  "auto-archive",
			Usage: "Auto-archive the task this long after it reaches a terminal status. 0 = never (default), negative = archive immediately, positive = delay (e.g. 1h, 24h).",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		serverURL := cmd.String("server")
		cfg, err := configfile.Load(nil)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		if cfg.Token == "" {
			return fmt.Errorf("not authenticated, run setup first")
		}
		client := gritzclient.New(gritzclient.Options{BaseURL: serverURL, Token: cfg.Token})

		texts := cmd.StringSlice("instruction")
		instructions := make([]*gritzv1.Instruction, len(texts))
		for i, text := range texts {
			instructions[i] = &gritzv1.Instruction{Text: text}
		}

		resp, err := client.CreateTask(ctx, &gritzv1.CreateTaskRequest{
			Name:         cmd.String("name"),
			Runner:       cmd.String("runner"),
			Workspace:    cmd.String("workspace"),
			Instructions: instructions,
			AutoArchive:  durationpb.New(cmd.Duration("auto-archive")),
		})
		if err != nil {
			return fmt.Errorf("failed to create task: %w", err)
		}

		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp.Task)
	},
}
