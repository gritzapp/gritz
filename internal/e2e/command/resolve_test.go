package command_test

import (
	"path/filepath"
	"testing"

	"github.com/gritzapp/gritz/internal/command"
	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"gotest.tools/v3/assert"
)

// Not parallel: it sets $GRITZ_CONFIG_DIR, the selection env vars (which a
// sandbox may set) and the working directory.
func TestResolve_DirectoryDefault(t *testing.T) {
	// Arrange - log in to org B, then org A, so the base default is org-b; the
	// working directory's .gritz.yml sets default to org-a
	h := e2e.New(t, nil)
	a := h.Org.CreateOrg(&gritzv1.CreateOrgRequest{Name: "Org A"})
	b := h.Org.CreateOrg(&gritzv1.CreateOrgRequest{Name: "Org B"})
	keyA := a.CreateKey(&gritzv1.CreateKeyRequest{Name: "cli"})
	keyB := b.CreateKey(&gritzv1.CreateKeyRequest{Name: "cli"})
	t.Setenv("GRITZ_ORG", "")
	t.Setenv("GRITZ_API_KEY", "")
	t.Setenv("GRITZ_SERVER", "")
	t.Setenv("GRITZ_CONFIG_DIR", t.TempDir())
	err := command.LoginCommand.Run(t.Context(), []string{
		"login", "--key", keyB.Resp.RawToken, "--server", h.Server.URL,
	})
	assert.NilError(t, err)
	err = command.LoginCommand.Run(t.Context(), []string{
		"login", "--key", keyA.Resp.RawToken, "--server", h.Server.URL,
	})
	assert.NilError(t, err)
	dir, err := filepath.Abs(filepath.Join("testdata", "default-org-a"))
	assert.NilError(t, err)
	t.Chdir(dir)

	// Act
	err = command.TaskCreateCommand.Run(t.Context(), []string{
		"create", "--name", "from-cli",
		"--runner", e2e.DefaultRunner, "--workspace", e2e.DefaultWorkspace,
	})

	// Assert - the task is in org A, not org B
	assert.NilError(t, err)
	tasksA, err := a.Client.ListTasks(t.Context(), &gritzv1.ListTasksRequest{})
	assert.NilError(t, err)
	assert.Equal(t, len(tasksA.Tasks), 1)
	assert.Equal(t, tasksA.Tasks[0].Name, "from-cli")
	tasksB, err := b.Client.ListTasks(t.Context(), &gritzv1.ListTasksRequest{})
	assert.NilError(t, err)
	assert.Equal(t, len(tasksB.Tasks), 0)
}

// Not parallel: it sets $GRITZ_CONFIG_DIR, the selection env vars (which a
// sandbox may set) and the working directory.
func TestResolve_OrgNotAllowed(t *testing.T) {
	// Arrange - log in to org B; the working directory's .gritz.yml defaults to
	// and allows only org-a
	h := e2e.New(t, nil)
	b := h.Org.CreateOrg(&gritzv1.CreateOrgRequest{Name: "Org B"})
	keyB := b.CreateKey(&gritzv1.CreateKeyRequest{Name: "cli"})
	t.Setenv("GRITZ_ORG", "")
	t.Setenv("GRITZ_API_KEY", "")
	t.Setenv("GRITZ_SERVER", "")
	t.Setenv("GRITZ_CONFIG_DIR", t.TempDir())
	err := command.LoginCommand.Run(t.Context(), []string{
		"login", "--key", keyB.Resp.RawToken, "--server", h.Server.URL,
	})
	assert.NilError(t, err)
	dir, err := filepath.Abs(filepath.Join("testdata", "allowed-org-a"))
	assert.NilError(t, err)
	t.Chdir(dir)

	// Act
	err = command.TaskCreateCommand.Run(t.Context(), []string{
		"create", "--org", "org-b", "--name", "from-cli",
		"--runner", e2e.DefaultRunner, "--workspace", e2e.DefaultWorkspace,
	})

	// Assert - selection fails and nothing is created in org B
	assert.Error(t, err, `org "org-b" is not allowed here: allowed is [org-a] (from `+
		filepath.Join(dir, ".gritz.yml")+")")
	tasksB, err := b.Client.ListTasks(t.Context(), &gritzv1.ListTasksRequest{})
	assert.NilError(t, err)
	assert.Equal(t, len(tasksB.Tasks), 0)
}
