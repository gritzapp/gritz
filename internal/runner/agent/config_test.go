package agent

import (
	"os"
	"testing"

	"gotest.tools/v3/assert"
)

func TestConfigStoreRoundTrip(t *testing.T) {
	t.Parallel()
	// Arrange
	store := ConfigStore(t.TempDir())
	cfg := &Config{Type: TypeDummy, Started: true, SetupCommandsCompleted: 2, NextEventToken: "cursor-abc123"}

	// Act
	assert.NilError(t, store.Save(1, cfg))
	got, err := store.Load(1)

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, got, cfg)
}

func TestConfigStoreSave_Mode(t *testing.T) {
	t.Parallel()
	// Arrange
	store := ConfigStore(t.TempDir())

	// Act
	assert.NilError(t, store.Save(1, &Config{Type: TypeDummy}))

	// Assert - the file is 0666 so the runner ships it and a non-root agent can
	// rewrite it.
	info, err := os.Stat(store.Path(1))
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o666))
}

func TestConfigStoreLoad_Missing(t *testing.T) {
	t.Parallel()
	// Arrange
	store := ConfigStore(t.TempDir())

	// Act
	got, err := store.Load(1)

	// Assert - a missing file yields an empty config, not an error
	assert.NilError(t, err)
	assert.DeepEqual(t, got, &Config{})
}
