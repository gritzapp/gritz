package command

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"

	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/urfave/cli/v3"
)

// LoginCommand adds an org entry for an API key to $CONFIG_DIR/.gritz.yml,
// learning the key's org from the server.
var LoginCommand = &cli.Command{
	Name:  "login",
	Usage: "Add an org entry for an API key to the user's .gritz.yml",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "key",
			Aliases:  []string{"k"},
			Usage:    "API key, as shown when the key is created",
			Sources:  cli.EnvVars("GRITZ_API_KEY"),
			Required: true,
		},
		&cli.StringFlag{
			Name:  "name",
			Usage: "entry name (default: a slug of the org name)",
		},
		&cli.StringFlag{
			Name:    "server",
			Aliases: []string{"s"},
			Usage:   "server URL (default: " + gritzclient.DefaultURL + ")",
			Sources: cli.EnvVars("GRITZ_SERVER"),
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		entry := configfile.Entry{Key: cmd.String("key"), Server: cmd.String("server")}
		client := gritzclient.New(gritzclient.Options{
			BaseURL: cmp.Or(entry.Server, gritzclient.DefaultURL),
			Token:   entry.Key,
		})
		resp, err := client.GetCaller(ctx, &gritzv1.GetCallerRequest{})
		if err != nil {
			return fmt.Errorf("failed to check key: %w", err)
		}
		org := resp.GetOrg()
		entry.ID = org.GetId()

		name := cmd.String("name")
		if name == "" {
			name = configfile.Slug(org.GetName())
			if name == "" {
				return fmt.Errorf("org name %q has no characters usable as an entry name: pass --name", org.GetName())
			}
		}

		dir, err := configfile.Dir()
		if err != nil {
			return err
		}
		setDefault, err := configfile.Login(filepath.Join(dir, configfile.Name), name, entry)
		if err != nil {
			return err
		}
		w := cmd.Root().Writer
		fmt.Fprintf(w, "Logged in to %s (org %d) as %q\n", org.GetName(), org.GetId(), name)
		if setDefault {
			fmt.Fprintf(w, "Default org set to %q\n", name)
		}
		return nil
	},
}
