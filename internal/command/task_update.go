package command

import (
	"context"
	"fmt"
	"slices"
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
	Flags: slices.Concat(configfile.Flags(), []cli.Flag{
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
			Usage: "Set the auto-archive timeout. 0 = never; negative = archive immediately; positive = delay. Unchanged when omitted.",
		},
	}),
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

		e, err := configfile.Resolve(cmd)
		if err != nil {
			return err
		}
		req := &gritzv1.UpdateTaskRequest{
			Id:              taskID,
			Name:            name,
			Start:           start,
			AddInstructions: instructions,
		}
		if cmd.IsSet("auto-archive") {
			req.AutoArchive = durationpb.New(cmd.Duration("auto-archive"))
		}
		client := gritzclient.New(gritzclient.Options{BaseURL: e.Server, Token: e.Key})
		if _, err := client.UpdateTask(ctx, req); err != nil {
			return fmt.Errorf("failed to update task: %w", err)
		}

		fmt.Println("Task updated.")
		return nil
	},
}
