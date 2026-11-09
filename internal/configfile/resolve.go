package configfile

import (
	"cmp"
	"errors"

	"github.com/urfave/cli/v3"
)

// DefaultServer is the server URL used when neither --server (with --key) nor
// the selected entry sets one. It matches gritzclient.DefaultURL; configfile doesn't import
// gritzclient.
const DefaultServer = "https://gritz.dev"

// Flags returns the --org, --key and --server flags (GRITZ_ORG, GRITZ_API_KEY,
// GRITZ_SERVER) shared by user-facing commands. Each call returns new flags, so
// every command gets its own.
func Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "org",
			Usage:   "org entry name from a .gritz.yml file (default: the merged org)",
			Sources: cli.EnvVars("GRITZ_ORG"),
		},
		&cli.StringFlag{
			Name:    "key",
			Aliases: []string{"k"},
			Usage:   "API key; ignores every .gritz.yml file and can't be combined with --org",
			Sources: cli.EnvVars("GRITZ_API_KEY"),
		},
		&cli.StringFlag{
			Name:    "server",
			Aliases: []string{"s"},
			Usage:   "server URL for --key, which it requires (default: " + DefaultServer + ")",
			Sources: cli.EnvVars("GRITZ_SERVER"),
		},
	}
}

// Resolve applies --key, --server and --org from cmd, and returns the config
// it selected from and the entry to use. With --key, the config holds a single
// entry for the key, named "" and selected by default, whose Server is
// --server, else DefaultServer. --server requires --key. Without --key, the
// config is the merged, validated .gritz.yml files from the working directory.
// The entry is the config's Lookup of --org.
func Resolve(cmd *cli.Command) (*Merged, *Entry, error) {
	server := cmd.String("server")
	key := cmd.String("key")
	var m *Merged
	switch {
	case key != "":
		m = &Merged{Orgs: map[string]Entry{
			"": {Key: key, Server: cmp.Or(server, DefaultServer)},
		}}
	case server != "":
		return nil, nil, errors.New("--server requires --key: entries in .gritz.yml carry their own server")
	default:
		var err error
		m, err = Load(LoadOptions{})
		if err != nil {
			return nil, nil, err
		}
		if err := m.Validate(); err != nil {
			return nil, nil, err
		}
	}
	e, err := m.Lookup(cmd.String("org"))
	if err != nil {
		return nil, nil, err
	}
	return m, e, nil
}
