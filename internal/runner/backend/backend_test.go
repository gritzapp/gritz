package backend_test

import (
	"testing"

	"gotest.tools/v3/assert"

	"github.com/gritzapp/gritz/internal/runner/backend"
	"github.com/gritzapp/gritz/internal/runner/workspace"
)

func TestSpecClone(t *testing.T) {
	t.Parallel()
	// Arrange
	spec := &backend.Spec{
		TaskID:    1,
		Version:   2,
		Workspace: &workspace.Workspace{Description: "ws"},
		Env:       map[string]string{"A": "1"},
		Secrets:   map[string]string{"S": "secret"},
		Files:     []backend.File{{Path: "/f", Data: []byte("data"), Mode: 0o644}},
	}

	// Act
	c := spec.Clone()
	spec.Workspace.Description = "changed"
	spec.Env["A"] = "changed"
	spec.Secrets["S"] = "changed"
	spec.Files[0].Data[0] = 'X'
	spec.Files[0].Path = "/changed"

	// Assert - the clone kept the original values.
	assert.DeepEqual(t, c, &backend.Spec{
		TaskID:    1,
		Version:   2,
		Workspace: &workspace.Workspace{Description: "ws"},
		Env:       map[string]string{"A": "1"},
		Secrets:   map[string]string{"S": "secret"},
		Files:     []backend.File{{Path: "/f", Data: []byte("data"), Mode: 0o644}},
	})
}
