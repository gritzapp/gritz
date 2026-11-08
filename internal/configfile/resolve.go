package configfile

import (
	"os"

	"github.com/urfave/cli/v3"
)

// Flags returns the --org, --key and --server flags (GRITZ_ORG, GRITZ_API_KEY,
// GRITZ_SERVER) shared by user-facing commands. Each call returns new flags, so
// every command gets its own.
func Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "org",
			Usage:   "org entry name from a .gritz.yml file (default: the merged default)",
			Sources: cli.EnvVars("GRITZ_ORG"),
		},
		&cli.StringFlag{
			Name:    "key",
			Aliases: []string{"k"},
			Usage:   "API key; ignores --org and every .gritz.yml file",
			Sources: cli.EnvVars("GRITZ_API_KEY"),
		},
		&cli.StringFlag{
			Name:    "server",
			Aliases: []string{"s"},
			Usage:   "server URL (default: the org entry's server, else https://gritz.dev)",
			Sources: cli.EnvVars("GRITZ_SERVER"),
		},
	}
}

// Resolve applies --key, --server and --org from cmd and the merged .gritz.yml
// files from the working directory, and returns the entry to use. With --key,
// the entry has only Key and Server. Server is empty when neither --server nor
// the entry sets it.
func Resolve(cmd *cli.Command) (*Entry, error) {
	server := cmd.String("server")
	if key := cmd.String("key"); key != "" {
		return &Entry{Key: key, Server: server}, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	m, err := Load(LoadOptions{Dir: wd})
	if err != nil {
		return nil, err
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	e, err := m.Lookup(cmd.String("org"))
	if err != nil {
		return nil, err
	}
	if server != "" {
		e.Server = server
	}
	return e, nil
}
