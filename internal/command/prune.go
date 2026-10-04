package command

import (
	"context"
	"fmt"
	"strconv"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/urfave/cli/v3"
)

var PruneCommand = &cli.Command{
	Name:  "prune",
	Usage: "Delete containers for archived tasks",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "server",
			Aliases: []string{"s"},
			Usage:   "server URL",
			Value:   gritzclient.DefaultURL,
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		// Create Docker client
		docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			return fmt.Errorf("failed to create docker client: %w", err)
		}
		defer docker.Close()

		// Create gritz client
		gritzClient := gritzclient.New(gritzclient.Options{BaseURL: cmd.String("server")})

		// List all stopped gritz containers
		containers, err := docker.ContainerList(ctx, container.ListOptions{
			All: true,
			Filters: filters.NewArgs(
				filters.Arg("label", "gritz=true"),
				filters.Arg("status", "exited"),
			),
		})
		if err != nil {
			return fmt.Errorf("failed to list containers: %w", err)
		}

		if len(containers) == 0 {
			fmt.Println("No stopped containers found.")
			return nil
		}

		// Check each container's task status and remove if archived
		var removed int
		for _, c := range containers {
			taskIDStr := c.Labels["gritz.task"]
			if taskIDStr == "" {
				continue
			}

			taskID, err := strconv.ParseInt(taskIDStr, 10, 64)
			if err != nil {
				fmt.Printf("Warning: invalid task ID %s for container %s\n", taskIDStr, c.Names[0])
				continue
			}

			// Fetch task status
			task, err := gritzClient.GetTask(ctx, &gritzv1.GetTaskRequest{Id: taskID})
			if err != nil {
				fmt.Printf("Warning: failed to get task %d: %v\n", taskID, err)
				continue
			}

			// Remove container if task is archived
			if task.Task.Archived {
				if err := docker.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil {
					fmt.Printf("Warning: failed to remove container %s: %v\n", c.Names[0], err)
				} else {
					removed++
					fmt.Printf("Removed container: %s (task %d)\n", c.Names[0], taskID)
				}
			}
		}

		fmt.Printf("\nRemoved %d container(s).\n", removed)
		return nil
	},
}
