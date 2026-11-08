package api_test

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/gritzapp/gritz/internal/command"
	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"gotest.tools/v3/assert"
)

// Not parallel: login writes to $GRITZ_CONFIG_DIR, which t.Setenv sets.
func TestLogin(t *testing.T) {
	// Arrange - a key in an org other than the owner's default
	h := e2e.New(t, nil)
	other := h.Org.CreateOrg(&gritzv1.CreateOrgRequest{Name: "Acme Labs"})
	key := other.CreateKey(&gritzv1.CreateKeyRequest{Name: "cli"})
	dir := t.TempDir()
	t.Setenv("GRITZ_CONFIG_DIR", dir)
	var out bytes.Buffer
	command.LoginCommand.Writer = &out
	t.Cleanup(func() { command.LoginCommand.Writer = nil })

	// Act
	err := command.LoginCommand.Run(t.Context(), []string{
		"login", "--key", key.Resp.RawToken, "--server", h.Server.URL,
	})

	// Assert
	assert.NilError(t, err)
	assert.Equal(t, out.String(), fmt.Sprintf(
		"Logged in to Acme Labs (org %d) as \"acme-labs\"\nDefault org set to \"acme-labs\"\n", other.ID))
	f, err := configfile.Read(filepath.Join(dir, configfile.Name))
	assert.NilError(t, err)
	assert.DeepEqual(t, f, &configfile.FileV2{
		Default: "acme-labs",
		Orgs: map[string]configfile.Entry{
			"acme-labs": {ID: other.ID, Key: key.Resp.RawToken, Server: h.Server.URL},
		},
	})
}
