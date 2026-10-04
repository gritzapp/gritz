package command

import (
	"context"
	"fmt"
	"strconv"

	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/urfave/cli/v3"
	"google.golang.org/protobuf/types/known/durationpb"
)

var TaskUpdateCommand = &cli.Command{
	Name:      "update",
	Usage:     "Update a task",
	ArgsUsage: "<task-id>",
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
			Usage:   "Set task name",
		},
		&cli.BoolFlag{
			Name:    "start",
			Aliases: []string{"r"},
			Usage:   "Start the task (non-interrupting if already running)",
		},
		&cli.StringSliceFlag{
			Name:    "add-instruction",
			Aliases: []string{"i"},
			Usage:   "Add instruction to task (can be specified multiple times)",
		},
		&cli.DurationFlag{
			Name:  "auto-archive",
			Value: 0,
			Usage: "Set the auto-archive timeout. 0 = never (default); negative = archive immediately; positive = delay.",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		taskIDStr := cmd.Args().First()
		if taskIDStr == "" {
			return fmt.Errorf("task ID is required")
		}
		taskID, err := strconv.ParseInt(taskIDStr, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid task ID: %w", err)
		}

		name := cmd.String("name")
		start := cmd.Bool("start")
		texts := cmd.StringSlice("add-instruction")

		if name == "" && !start && len(texts) == 0 && !cmd.IsSet("auto-archive") {
			return fmt.Errorf("nothing to update")
		}

		instructions := make([]*gritzv1.Instruction, len(texts))
		for i, text := range texts {
			instructions[i] = &gritzv1.Instruction{Text: text}
		}

		serverURL := cmd.String("server")
		cfg, err := configfile.Load(nil)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		if cfg.Token == "" {
			return fmt.Errorf("not authenticated, run setup first")
		}
		client := gritzclient.New(gritzclient.Options{BaseURL: serverURL, Token: cfg.Token})
		if _, err := client.UpdateTask(ctx, &gritzv1.UpdateTaskRequest{
			Id:              taskID,
			Name:            name,
			Start:           start,
			AddInstructions: instructions,
			AutoArchive:     durationpb.New(cmd.Duration("auto-archive")),
		}); err != nil {
			return fmt.Errorf("failed to update task: %w", err)
		}

		fmt.Println("Task updated.")
		return nil
	},
}
