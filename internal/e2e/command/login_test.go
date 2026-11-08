package command_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/gritzapp/gritz/internal/command"
	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/poll"
)

// Not parallel: login writes to $GRITZ_CONFIG_DIR, which t.Setenv sets.
func TestLogin(t *testing.T) {
	// Arrange - a key in an org other than the owner's default
	h := e2e.New(t, nil)
	other := h.Org.CreateOrg(&gritzv1.CreateOrgRequest{Name: "Acme Labs"})
	key := other.CreateKey(&gritzv1.CreateKeyRequest{Name: "cli"})
	dir := t.TempDir()
	t.Setenv("GRITZ_CONFIG_DIR", dir)

	// Act
	err := command.LoginCommand.Run(t.Context(), []string{
		"login", "--key", key.Resp.RawToken, "--server", h.Server.URL,
	})

	// Assert
	assert.NilError(t, err)
	f, err := configfile.Read(filepath.Join(dir, configfile.Name))
	assert.NilError(t, err)
	assert.DeepEqual(t, f, &configfile.File{
		Org: "acme-labs",
		Orgs: map[string]configfile.Entry{
			"acme-labs": {ID: other.ID, Key: key.Resp.RawToken, Server: h.Server.URL},
		},
	})
}

// Not parallel: login writes to $GRITZ_CONFIG_DIR and finds the browser on
// $PATH, which t.Setenv sets.
func TestLogin_Browser(t *testing.T) {
	// Arrange - a browser that records the URL it is asked to open
	h := e2e.New(t, nil)
	other := h.Org.CreateOrg(&gritzv1.CreateOrgRequest{Name: "Acme Labs"})
	dir := t.TempDir()
	t.Setenv("GRITZ_CONFIG_DIR", dir)
	bin := t.TempDir()
	urlFile := filepath.Join(bin, "url")
	for _, name := range []string{"xdg-open", "open"} {
		script := "#!/bin/sh\nprintf '%s' \"$1\" > " + urlFile + "\n"
		assert.NilError(t, os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755))
	}
	t.Setenv("PATH", bin)
	// Not from the environment: login would take the key path, and task list
	// rejects --server without --key.
	t.Setenv("GRITZ_SERVER", "")
	t.Setenv("GRITZ_API_KEY", "")

	// Act - log in, approving in other, then list tasks with the entry
	loggedIn := make(chan error, 1)
	go func() {
		loggedIn <- command.LoginCommand.Run(t.Context(), []string{"login", "--server", h.Server.URL})
	}()
	var authURL []byte
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		authURL, _ = os.ReadFile(urlFile)
		if len(authURL) == 0 {
			return poll.Continue("waiting for the browser to open")
		}
		return poll.Success()
	})
	resp, err := http.Get(other.ApproveOAuth(string(authURL)))
	assert.NilError(t, err)
	resp.Body.Close()
	assert.NilError(t, <-loggedIn)
	err = command.TaskCommand.Run(t.Context(), []string{"task", "list"})

	// Assert
	assert.NilError(t, err)
	f, err := configfile.Read(filepath.Join(dir, configfile.Name))
	assert.NilError(t, err)
	entry := f.Orgs["acme-labs"]
	assert.Equal(t, f.Org, "acme-labs")
	assert.Equal(t, entry.ID, other.ID)
	assert.Equal(t, entry.Server, h.Server.URL)
	assert.Assert(t, entry.RefreshToken != "")
}
