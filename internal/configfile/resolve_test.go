package configfile

import (
	"context"
	"testing"

	"github.com/urfave/cli/v3"
	"gotest.tools/v3/assert"
)

// resolve runs Resolve on the shared flags parsed from args.
func resolve(t *testing.T, args ...string) (*Entry, error) {
	t.Helper()
	var entry *Entry
	cmd := &cli.Command{
		Name:  "test",
		Flags: Flags(),
		Action: func(_ context.Context, cmd *cli.Command) error {
			var err error
			entry, err = Resolve(cmd)
			return err
		},
	}
	err := cmd.Run(t.Context(), append([]string{"test"}, args...))
	return entry, err
}

// Not parallel: t.Setenv clears the flags' env sources.
func TestResolve_ServerWithKey(t *testing.T) {
	t.Setenv("GRITZ_API_KEY", "")
	t.Setenv("GRITZ_SERVER", "")

	entry, err := resolve(t, "--key", "k1", "--server", "http://localhost:8080")

	assert.NilError(t, err)
	assert.DeepEqual(t, entry, &Entry{Key: "k1", Server: "http://localhost:8080"})
}

func TestResolve_ServerWithoutKey(t *testing.T) {
	t.Setenv("GRITZ_API_KEY", "")
	t.Setenv("GRITZ_SERVER", "")

	_, err := resolve(t, "--server", "http://localhost:8080")

	assert.Error(t, err, "--server requires --key: entries in .gritz.yml carry their own server")
}
