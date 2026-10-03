package command

import (
	"context"

	"github.com/icholy/gritz/internal/gritzclient"
	"github.com/icholy/gritz/internal/x/gitcredential"
	"github.com/urfave/cli/v3"
)

var GitCredentialCommand = &cli.Command{
	Name:   "git-credential",
	Usage:  "Git credential helper for GitHub App tokens",
	Hidden: true,
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
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		client := gritzclient.New(gritzclient.Options{
			BaseURL: cmd.String("server"),
			Token:   cmd.String("token"),
		})
		return gitcredential.Run(ctx, cmd.Args().First(), cmd.Root().Reader, cmd.Root().Writer, client)
	},
}
