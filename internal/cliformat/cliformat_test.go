package cliformat

import (
	"bytes"
	"testing"

	"github.com/gritzapp/gritz/internal/configfile"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/golden"
)

// Regenerate the golden with: go test ./internal/cliformat/ -run TestWriteConfigEntries -update
func TestWriteConfigEntries(t *testing.T) {
	t.Parallel()
	merged := &configfile.Merged{
		Org: "personal",
		Orgs: map[string]configfile.Entry{
			"personal": {ID: 3, RefreshToken: "rt_secret", Server: "https://gritz.example.com"},
			"acme-ci":  {ID: 12, Key: "xat_secret"},
		},
	}
	assert.NilError(t, merged.Validate())
	var buf bytes.Buffer
	assert.NilError(t, WriteConfigEntries(&buf, merged))
	golden.Assert(t, buf.String(), "config-entries.golden")
}
