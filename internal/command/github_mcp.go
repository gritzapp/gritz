package command

import (
	"context"
	"log/slog"
	"os"

	"github.com/icholy/gritz/internal/gritzclient"
	"github.com/icholy/gritz/internal/x/githubmcp"
	"github.com/urfave/cli/v3"
)

var GitHubMCPCommand = &cli.Command{
	Name:  "github-mcp",
	Usage: "Front the GitHub MCP server with rotating GitHub App installation tokens",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "server",
			Usage:   "server URL",
			Value:   gritzclient.DefaultURL,
			Sources: cli.EnvVars("GRITZ_SERVER"),
		},
		&cli.StringFlag{
			Name:    "token",
			Usage:   "Authentication token",
			Sources: cli.EnvVars("GRITZ_TOKEN"),
		},
		&cli.StringFlag{
			Name:  "url",
			Usage: "Upstream GitHub MCP endpoint",
			Value: githubmcp.DefaultURL,
		},
		&cli.DurationFlag{
			Name:  "min-ttl",
			Usage: "Rotate the upstream session when the active token has less than this much time left",
			Value: githubmcp.DefaultMinTTL,
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		return githubmcp.New(githubmcp.Config{
			Client: gritzclient.New(gritzclient.Options{
				BaseURL: cmd.String("server"),
				Token:   cmd.String("token"),
			}),
			URL:    cmd.String("url"),
			MinTTL: cmd.Duration("min-ttl"),
			Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		}).Run(ctx)
	},
}
