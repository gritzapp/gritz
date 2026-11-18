package command

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/urfave/cli/v3"
	"google.golang.org/protobuf/types/known/durationpb"
)

var TaskCreateCommand = &cli.Command{
	Name:  "create",
	Usage: "Create a new task",
	// --instruction and --var values are free text: don't split them on commas.
	DisableSliceFlagSeparator: true,
	Flags: slices.Concat(configfile.Flags(), []cli.Flag{
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
		&cli.StringSliceFlag{
			Name:  "var",
			Usage: "Task variable as `NAME=VALUE` (can be specified multiple times). NAME=@file reads the value from a file, NAME=@- from stdin.",
		},
		&cli.DurationFlag{
			Name:  "auto-archive",
			Usage: "Auto-archive the task this long after it reaches a terminal status. 0 = never (default), negative = archive immediately, positive = delay (e.g. 1h, 24h).",
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

		variables, err := parseVars(cmd.StringSlice("var"), os.Stdin)
		if err != nil {
			return err
		}

		resp, err := client.CreateTask(ctx, &gritzv1.CreateTaskRequest{
			Name:         cmd.String("name"),
			Runner:       cmd.String("runner"),
			Workspace:    cmd.String("workspace"),
			Instructions: instructions,
			AutoArchive:  durationpb.New(cmd.Duration("auto-archive")),
			Variables:    variables,
		})
		if err != nil {
			return fmt.Errorf("failed to create task: %w", err)
		}

		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp.Task)
	},
}

// parseVars parses --var values, NAME=VALUE each. A value of @file is read
// from the file, and @- from stdin, which only one variable can use.
func parseVars(args []string, stdin io.Reader) (map[string]string, error) {
	if len(args) == 0 {
		return nil, nil
	}
	vars := map[string]string{}
	usedStdin := false
	for _, arg := range args {
		name, value, ok := strings.Cut(arg, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("--var %q: want NAME=VALUE", arg)
		}
		if _, dup := vars[name]; dup {
			return nil, fmt.Errorf("--var %s: set more than once", name)
		}
		if path, ok := strings.CutPrefix(value, "@"); ok {
			var data []byte
			var err error
			if path == "-" {
				if usedStdin {
					return nil, fmt.Errorf("--var %s: only one variable can read stdin", name)
				}
				usedStdin = true
				data, err = io.ReadAll(stdin)
			} else {
				data, err = os.ReadFile(path)
			}
			if err != nil {
				return nil, fmt.Errorf("--var %s: %w", name, err)
			}
			value = string(data)
		}
		vars[name] = value
	}
	return vars, nil
}
