package configfile

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/urfave/cli/v3"
	"gotest.tools/v3/assert"
)

// resolve runs Resolve with args, ignoring the GRITZ_* environment.
func resolve(t *testing.T, args ...string) (*Entry, error) {
	t.Helper()
	flags := Flags()
	for _, f := range flags {
		f.(*cli.StringFlag).Sources = cli.ValueSourceChain{}
	}
	var entry *Entry
	cmd := &cli.Command{
		Name:  "test",
		Flags: flags,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			_, e, err := Resolve(cmd)
			entry = e
			return err
		},
	}
	err := cmd.Run(t.Context(), append([]string{"test"}, args...))
	return entry, err
}

func TestResolve_Config(t *testing.T) {
	t.Parallel()
	p := filepath.FromSlash("testdata/read/.gritz.yml")

	e, err := resolve(t, "--config", p, "--org", "local")

	assert.NilError(t, err)
	assert.DeepEqual(t, e, &Entry{ID: 1, Key: "k2", Server: "http://localhost:8080", Name: "local", Path: p})
}

func TestResolve_ConfigWithKey(t *testing.T) {
	t.Parallel()

	_, err := resolve(t, "--config", filepath.FromSlash("testdata/read/.gritz.yml"), "--key", "k")

	assert.Error(t, err, "--config can't be combined with --key")
}
