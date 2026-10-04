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
	"google.golang.org/protobuf/encoding/protojson"
)

var TaskListCommand = &cli.Command{
	Name:  "list",
	Usage: "List tasks from the server",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "server",
			Aliases: []string{"s"},
			Usage:   "server URL",
			Value:   gritzclient.DefaultURL,
			Sources: cli.EnvVars("GRITZ_SERVER"),
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

		resp, err := client.ListTasks(ctx, &gritzv1.ListTasksRequest{})
		if err != nil {
			return fmt.Errorf("failed to list tasks: %w", err)
		}

		// ListTasks already returns the fat Task per row, so render the header
		// straight from the response — no per-task detail fetch.
		marshalOpts := protojson.MarshalOptions{Indent: "  "}
		result := make([]json.RawMessage, len(resp.Tasks))
		for i, task := range resp.Tasks {
			result[i], err = marshalOpts.Marshal(task)
			if err != nil {
				return fmt.Errorf("failed to marshal task %d: %w", task.Id, err)
			}
		}

		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	},
}
