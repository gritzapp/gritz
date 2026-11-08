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
	"golang.org/x/oauth2"
	"google.golang.org/protobuf/encoding/protojson"
)

var TaskListCommand = &cli.Command{
	Name:  "list",
	Usage: "List tasks from the server",
	Flags: configfile.Flags(),
	Action: func(ctx context.Context, cmd *cli.Command) error {
		cfg, err := configfile.Resolve(cmd)
		if err != nil {
			return err
		}
		ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: cfg.Key})
		client := gritzclient.New(gritzclient.Options{BaseURL: cfg.Server, TokenSource: ts})

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
