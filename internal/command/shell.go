package command

import (
	"context"
	"fmt"
	"strconv"

	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/shell"
	"github.com/urfave/cli/v3"
)

// ShellCommand opens an interactive debug shell in a task's sandbox. It is a
// client of the driver reverse-shell feature (the design in
// proposals/draft/driver-reverse-shell.md): it asks the server to open a shell
// session via the OpenShell RPC, then hands the session off to shell.Attach,
// which attaches to the rendezvous relay over a WebSocket and pipes the local
// terminal through it. This works for any backend and for remote runners — there
// is no docker-direct path anymore.
var ShellCommand = &cli.Command{
	Name:      "shell",
	Usage:     "Open an interactive shell in a task's sandbox",
	ArgsUsage: "<task-id>",
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
		if cmd.NArg() < 1 {
			return cli.Exit("task ID required", 1)
		}
		taskID, err := strconv.ParseInt(cmd.Args().First(), 10, 64)
		if err != nil {
			return cli.Exit("invalid task ID: "+cmd.Args().First(), 1)
		}

		serverURL := cmd.String("server")
		cfg, err := configfile.Load(nil)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		if cfg.Token == "" {
			return fmt.Errorf("not authenticated, run setup first")
		}

		// Ask the server to open a shell session for the task. The org is derived
		// from the token claims server-side; the operator leg is Bearer-only.
		client := gritzclient.New(gritzclient.Options{BaseURL: serverURL, Token: cfg.Token})
		resp, err := client.OpenShell(ctx, &gritzv1.OpenShellRequest{TaskId: taskID})
		if err != nil {
			return fmt.Errorf("failed to open shell: %w", err)
		}
		session := resp.GetSessionId()
		if session == "" {
			return fmt.Errorf("server returned an empty shell session id")
		}

		code, err := shell.Attach(ctx, shell.AttachOptions{
			ServerURL: serverURL,
			Token:     cfg.Token,
			Session:   session,
		})
		if err != nil {
			return err
		}
		return cli.Exit("", code)
	},
}
