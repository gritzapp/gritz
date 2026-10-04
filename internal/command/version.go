package command

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/gritzapp/gritz/internal/version"
)

var VersionCommand = &cli.Command{
	Name:  "version",
	Usage: "Print the version",
	Action: func(ctx context.Context, cmd *cli.Command) error {
		fmt.Println(version.String())
		return nil
	},
}
