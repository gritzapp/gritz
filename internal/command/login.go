package command

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/cli/browser"
	"github.com/gritzapp/gritz/internal/auth/cliauth"
	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/urfave/cli/v3"
)

// LoginCommand adds an org entry to $CONFIG_DIR/.gritz.yml, logging in with
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
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		entry := configfile.Entry{Key: cmd.String("key"), Server: cmd.String("server")}
		if entry.Server == "" {
			server, err := configfile.LoginServer()
			if err != nil {
				return err
			}
			entry.Server = server
		}
		dir, err := configfile.Dir()
		if err != nil {
			return err
		}
		path := filepath.Join(dir, configfile.Name)
		name := cmd.String("name")

		token := entry.Key
		if token == "" {
			// Re-logging in to an entry preselects its org.
			var orgID int64
			if name != "" {
				f, err := configfile.Read(path)
				switch {
				case errors.Is(err, fs.ErrNotExist):
				case err != nil:
					return err
				default:
					if old, ok := f.Orgs[name]; ok && old.Server == entry.Server {
						orgID = old.ID
					}
				}
			}
			tok, err := cliauth.Login(ctx, cliauth.LoginOptions{
				Server:      entry.Server,
				OrgID:       orgID,
				OpenBrowser: browser.OpenURL,
				Prompt:      os.Stdout,
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

		if name == "" {
			name = configfile.Slug(org.GetName())
			if name == "" {
				return fmt.Errorf("org name %q has no characters usable as an entry name: pass --name", org.GetName())
			}
		}
		if err := configfile.Login(path, name, entry); err != nil {
			return err
		}
		fmt.Printf("Logged in to %s (org %d) as %q\n", org.GetName(), org.GetId(), name)
		return nil
	},
}
