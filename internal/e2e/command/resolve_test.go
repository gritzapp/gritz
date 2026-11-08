package command_test

import (
	"path/filepath"
	"testing"

	"github.com/gritzapp/gritz/internal/command"
	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"gotest.tools/v3/assert"
)

// loginTwoOrgs logs in to org B, then org A, as "org-b" and "org-a", so the
// base layer's default is org-b. It sets $GRITZ_CONFIG_DIR and clears the
// selection env vars, which a sandbox may set, so callers can't be parallel.
func loginTwoOrgs(t *testing.T, h *e2e.Harness) (a, b *e2e.Org) {
	t.Helper()
	for _, env := range []string{"GRITZ_ORG", "GRITZ_API_KEY", "GRITZ_SERVER"} {
		t.Setenv(env, "")
	}
	a = h.Org.CreateOrg(&gritzv1.CreateOrgRequest{Name: "Org A"})
	b = h.Org.CreateOrg(&gritzv1.CreateOrgRequest{Name: "Org B"})
	t.Setenv("GRITZ_CONFIG_DIR", t.TempDir())
	for _, org := range []*e2e.Org{b, a} {
		key := org.CreateKey(&gritzv1.CreateKeyRequest{Name: "cli"})
		err := command.LoginCommand.Run(t.Context(), []string{
			"login", "--key", key.Resp.RawToken, "--server", h.Server.URL,
		})
		assert.NilError(t, err)
	}
	return a, b
}

func listTaskNames(t *testing.T, org *e2e.Org) []string {
	t.Helper()
	resp, err := org.Client.ListTasks(t.Context(), &gritzv1.ListTasksRequest{})
	assert.NilError(t, err)
	var names []string
	for _, task := range resp.Tasks {
		names = append(names, task.Name)
	}
	return names
}

// Not parallel: it sets $GRITZ_CONFIG_DIR and the working directory.
func TestResolve_DirectoryDefault(t *testing.T) {
	// Arrange - the base default is org-b; the working directory's .gritz.yml
	// sets default to org-a
	h := e2e.New(t, nil)
	a, b := loginTwoOrgs(t, h)
	dir, err := filepath.Abs(filepath.Join("testdata", "default-org-a"))
	assert.NilError(t, err)
	t.Chdir(dir)

	// Act
	err = command.TaskCreateCommand.Run(t.Context(), []string{
		"create", "--name", "from-cli",
		"--runner", e2e.DefaultRunner, "--workspace", e2e.DefaultWorkspace,
	})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, listTaskNames(t, a), []string{"from-cli"})
	assert.DeepEqual(t, listTaskNames(t, b), []string(nil))
}

// Not parallel: it sets $GRITZ_CONFIG_DIR and the working directory.
func TestResolve_OrgNotAllowed(t *testing.T) {
	// Arrange - the working directory's .gritz.yml defaults to and allows only org-a
	h := e2e.New(t, nil)
	_, b := loginTwoOrgs(t, h)
	dir, err := filepath.Abs(filepath.Join("testdata", "allowed-org-a"))
	assert.NilError(t, err)
	t.Chdir(dir)

	// Act
	err = command.TaskCreateCommand.Run(t.Context(), []string{
		"create", "--org", "org-b", "--name", "from-cli",
		"--runner", e2e.DefaultRunner, "--workspace", e2e.DefaultWorkspace,
	})

	// Assert
	assert.Error(t, err, `org "org-b" is not allowed here: allowed is [org-a] (from `+
		filepath.Join(dir, ".gritz.yml")+")")
	assert.DeepEqual(t, listTaskNames(t, b), []string(nil))
}
