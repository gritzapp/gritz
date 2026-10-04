package command

import (
	"context"
	"fmt"

	"github.com/gritzapp/gritz/internal/runner/prebuilt"
	"github.com/urfave/cli/v3"
)

var DownloadCommand = &cli.Command{
	Name:  "download",
	Usage: "Download prebuilt binaries from the latest GitHub release",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "github-repo",
			Usage: "GitHub repository for binary downloads (owner/repo)",
			Value: prebuilt.DefaultRepo,
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		fmt.Println("Downloading prebuilt binaries...")
		if err := prebuilt.Download(ctx, cmd.String("github-repo")); err != nil {
			return fmt.Errorf("download prebuilt binaries: %w", err)
		}
		fmt.Println("Prebuilt binaries downloaded.")
		return nil
	},
}
