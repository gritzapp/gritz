package command

import (
	"context"

	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/runner/agentmcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/urfave/cli/v3"
)

var AgentMcpCommand = &cli.Command{
	Name:  "agent-mcp",
	Usage: "Run an MCP server that provides gritz tools",
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
			Usage:    "Task ID",
			Required: true,
		},
		&cli.StringFlag{
			Name:     "runner",
			Aliases:  []string{"r"},
			Usage:    "Runner ID",
			Required: true,
		},
		&cli.StringFlag{
			Name:     "workspace",
			Aliases:  []string{"w"},
			Usage:    "Workspace name",
			Required: true,
		},
		&cli.StringFlag{
			Name:     "token",
			Usage:    "Authentication token",
			Required: true,
		},
		&cli.StringSliceFlag{
			Name:  "capability",
			Usage: "Capability granted to the task (repeatable)",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		server := mcp.NewServer(&mcp.Implementation{
			Name:    "gritz",
			Version: "1.0.0",
		}, nil)

		client := gritzclient.New(gritzclient.Options{BaseURL: cmd.String("server"), Token: cmd.String("token")})
		task := &model.Task{
			ID:        cmd.Int64("task"),
			Runner:    cmd.String("runner"),
			Workspace: cmd.String("workspace"),
		}
		agentmcp.NewServer(client, task, cmd.StringSlice("capability")).AddTools(server)

		return server.Run(ctx, &mcp.StdioTransport{})
	},
}
