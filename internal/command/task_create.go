package command

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"

	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/urfave/cli/v3"
)

var TaskCreateCommand = &cli.Command{
	Name:  "create",
	Usage: "Create a new task",
	// --instruction values are free text: don't split them on commas.
	DisableSliceFlagSeparator: true,
	Flags: slices.Concat(configfile.Flags(), []cli.Flag{
		&cli.StringFlag{
			Name:    "name",
			Aliases: []string{"n"},
			Usage:   "Task name",
		},
		&cli.Int64Flag{
			Name:     "agent",
			Aliases:  []string{"a"},
			Usage:    "ID of the agent the task belongs to; the task runs with its workspace, runner, auto-archive and variables",
			Required: true,
		},
		&cli.StringSliceFlag{
			Name:    "instruction",
			Aliases: []string{"i"},
			Usage:   "Instruction to execute (can be specified multiple times)",
		},
	}),
	Action: func(ctx context.Context, cmd *cli.Command) error {
		_, cfg, err := configfile.Resolve(cmd)
		if err != nil {
			return err
		}
		client := gritzclient.New(gritzclient.Options{
			BaseURL:     cfg.Server,
			TokenSource: cfg.TokenSource(ctx),
		})

		texts := cmd.StringSlice("instruction")
		instructions := make([]*gritzv1.Instruction, len(texts))
		for i, text := range texts {
			instructions[i] = &gritzv1.Instruction{Text: text}
		}

		resp, err := client.CreateTask(ctx, &gritzv1.CreateTaskRequest{
			Name:         cmd.String("name"),
			AgentId:      cmd.Int64("agent"),
			Instructions: instructions,
		})
		if err != nil {
			return fmt.Errorf("failed to create task: %w", err)
		}

		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp.Task)
	},
}
