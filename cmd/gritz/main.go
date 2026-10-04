package main

import (
	"context"
	"fmt"
	"os"

	"github.com/gritzapp/gritz/internal/command"
	"github.com/gritzapp/gritz/internal/version"
	"github.com/urfave/cli/v3"
)

func main() {
	cmd := &cli.Command{
		Name:    "gritz",
		Usage:   "Async agent orchestrator for Claude Code",
		Version: version.String(),
		Commands: []*cli.Command{
			command.DriverCommand,
			command.ServerCommand,
			command.RunnerCommand,
			command.TaskCommand,
			command.ContainersCommand,
			command.WorkspacesCommand,
			command.ShellCommand,
			command.PruneCommand,
			command.LogsCommand,
			command.DownloadCommand,
			command.VersionCommand,
			command.McpCommand,
			command.NotifyCommand,
			command.ToolCommand,
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		fmt.Printf("ERROR: %v\n", err)
		os.Exit(1)
	}
}
