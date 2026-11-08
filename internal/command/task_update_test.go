package command_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gritzapp/gritz/internal/command"
	"github.com/gritzapp/gritz/internal/configfile"
	"github.com/gritzapp/gritz/internal/e2e"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"google.golang.org/protobuf/types/known/durationpb"
	"gotest.tools/v3/assert"
)

func TestTaskUpdate_KeepsAutoArchive(t *testing.T) {
	// Arrange - a task with a 24h auto-archive, and a CLI config holding a key.
	h := e2e.New(t, nil)
	task := h.Org.CreateTask(&gritzv1.CreateTaskRequest{
		Name:        "Before",
		Draft:       true,
		AutoArchive: durationpb.New(24 * time.Hour),
	})
	key := h.Org.CreateKey(&gritzv1.CreateKeyRequest{Name: "cli"})
	dir := t.TempDir()
	data, err := json.Marshal(configfile.File{Token: key.Resp.RawToken})
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600))
	t.Setenv("GRITZ_CONFIG_DIR", dir)

	// Act - rename the task without --auto-archive.
	err = command.TaskUpdateCommand.Run(t.Context(), []string{
		"update", "--server", h.Server.URL, "--name", "After", strconv.FormatInt(task.ID, 10),
	})

	// Assert
	assert.NilError(t, err)
	got := h.Org.GetTask(&gritzv1.GetTaskRequest{Id: task.ID})
	assert.Equal(t, got.Name, "After")
	assert.Equal(t, got.AutoArchive.AsDuration(), 24*time.Hour)
}
