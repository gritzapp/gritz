package command

import (
	"context"
	"errors"
	"log/slog"

	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/gritzapp/gritz/internal/model"
	"github.com/gritzapp/gritz/internal/x/notify"
	"github.com/urfave/cli/v3"
)

// NotifyCommand runs a long-lived daemon that subscribes to the server's
// notification stream and emits a system notification when a task reaches a
// terminal state (completed, failed, or cancelled).
//
// It reuses the same per-org SSE stream as the runner and the mcp channel
// bridge. Notifications are gated on the terminal TaskStatus so the daemon
// only surfaces outcomes that need attention, rather than every change.
var NotifyCommand = &cli.Command{
	Name:  "notify",
	Usage: "Subscribe to server notifications and emit system notifications",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "server",
			Aliases: []string{"s"},
			Usage:   "server URL",
			Value:   gritzclient.DefaultURL,
		},
		&cli.StringFlag{
			Name:     "token",
			Usage:    "Authentication token",
			Sources:  cli.EnvVars("GRITZ_TOKEN"),
			Required: true,
		},
		&cli.StringFlag{
			Name:  "title",
			Usage: "Title shown on each system notification",
			Value: "gritz",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		title := cmd.String("title")
		nc := gritzclient.NewNotificationClient(gritzclient.NotificationClientOptions{
			BaseURL: cmd.String("server"),
			Token:   cmd.String("token"),
			Handler: func(n model.Notification) {
				// Only surface terminal task transitions (completed,
				// failed, cancelled) — these are the outcomes that need
				// attention. Every other change is left silent.
				if !n.TaskStatus.IsTerminal() {
					return
				}
				if err := notify.Send(title, n.ChannelMessage); err != nil {
					slog.Warn("failed to send system notification", "err", err)
				}
			},
		})
		if err := nc.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	},
}
