package command

import (
	"fmt"

	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/gritzclient"
	"github.com/urfave/cli/v3"
)

// clientFlags returns the --server and --token flags shared by the commands
// that talk to the server with a user's API key. Each call returns fresh flags,
// since urfave/cli flags hold their parsed value.
func clientFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "server",
			Aliases: []string{"s"},
			Usage:   "server URL",
			Value:   gritzclient.DefaultURL,
			Sources: cli.EnvVars("GRITZ_SERVER"),
		},
		&cli.StringFlag{
			Name:    "token",
			Usage:   "API key; overrides the token in config.json",
			Sources: cli.EnvVars("GRITZ_TOKEN"),
		},
	}
}

// clientToken returns the API key for a command built with clientFlags:
// --token or GRITZ_TOKEN if set, otherwise the token in config.json.
func clientToken(cmd *cli.Command) (string, error) {
	cfg, err := configfile.Load(&configfile.Overrides{Token: cmd.String("token")})
	if err != nil {
		return "", fmt.Errorf("failed to load config: %w", err)
	}
	if cfg.Token == "" {
		path, err := configfile.Path()
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf(`not authenticated: save an API key in %s as {"token": "xat_..."}, or pass --token or set GRITZ_TOKEN`, path)
	}
	return cfg.Token, nil
}
