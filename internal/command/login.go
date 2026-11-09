package command

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/cli/browser"
	"github.com/gritzapp/gritz/internal/auth/cliauth"
	"github.com/gritzapp/gritz/internal/cliformat"
	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/urfave/cli/v3"
)

// LoginCommand adds an org entry to $CONFIG_DIR/.gritz.yml or --config, logging in with
// the browser or an API key and learning the org from the server.
var LoginCommand = &cli.Command{
	Name:  "login",
	Usage: "Log in with the browser, or an API key, and add an org entry to the user's .gritz.yml",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "key",
			Aliases: []string{"k"},
			Usage:   "API key, as shown when the key is created (default: log in with the browser)",
			Sources: cli.EnvVars("GRITZ_API_KEY"),
		},
		&cli.StringFlag{
			Name:  "name",
			Usage: "entry name (default: a slug of the org name)",
		},
		&cli.StringFlag{
			Name:    "server",
			Aliases: []string{"s"},
			Usage:   "server URL (default: the top-level server of the nearest .gritz.yml, else " + configfile.DefaultServer + ")",
			Sources: cli.EnvVars("GRITZ_SERVER"),
		},
		&cli.StringFlag{
			Name:    "config",
			Usage:   "a single .gritz.yml file to write the entry to, take the default server from and --list, instead of $CONFIG_DIR/.gritz.yml and the files found from the working directory",
			Sources: cli.EnvVars("GRITZ_CONFIG"),
		},
		&cli.BoolFlag{
			Name:  "list",
			Usage: "list the org entries --org accepts from the working directory, or --config, instead of logging in; * marks the default",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		config := cmd.String("config")
		if cmd.Bool("list") {
			var merged *configfile.Merged
			var err error
			if config != "" {
				merged, err = configfile.LoadFile(config)
			} else {
				merged, err = configfile.Load(configfile.LoadOptions{})
			}
			if err != nil {
				return err
			}
			if err := merged.Validate(); err != nil {
				return err
			}
			return cliformat.WriteConfigEntries(os.Stdout, merged)
		}
		entry := configfile.Entry{Key: cmd.String("key"), Server: cmd.String("server")}
		if entry.Server == "" {
			server, err := configfile.LoginServer(config)
			if err != nil {
				return err
			}
			entry.Server = server
		}

		token := entry.Key
		if token == "" {
			tok, err := cliauth.Login(ctx, cliauth.LoginOptions{
				Server:      entry.Server,
				OpenBrowser: browser.OpenURL,
				Prompt:      os.Stdout,
				Timeout:     10 * time.Minute,
			})
			if err != nil {
				return err
			}
			token = tok.AccessToken
			entry.RefreshToken = tok.RefreshToken
		}
		client := gritzclient.New(gritzclient.Options{
			BaseURL:     entry.Server,
			TokenSource: gritzclient.StaticToken(token),
		})
		resp, err := client.GetCaller(ctx, &gritzv1.GetCallerRequest{})
		if err != nil {
			return fmt.Errorf("failed to identify the org: %w", err)
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
		if err := configfile.Login(config, name, entry); err != nil {
			return err
		}
		fmt.Printf("Logged in to %s (org %d) as %q\n", org.GetName(), org.GetId(), name)
		return nil
	},
}
