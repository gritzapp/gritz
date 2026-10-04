package command

import (
	"context"
	"fmt"

	"github.com/gritzapp/gritz/internal/runner/workspace"
	"github.com/urfave/cli/v3"
	"gopkg.in/yaml.v3"
)

var WorkspacesCommand = &cli.Command{
	Name:  "workspaces",
	Usage: "Validate and display workspace configuration",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "config",
			Aliases: []string{"c"},
			Usage:   "Workspace config file",
			Value:   "workspaces.yaml",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		configPath := cmd.String("config")

		cfg, err := workspace.LoadConfig(configPath, nil)
		if err != nil {
			return err
		}

		out, err := yaml.Marshal(cfg)
		if err != nil {
			return err
		}

		fmt.Print(string(out))
		return nil
	},
}
