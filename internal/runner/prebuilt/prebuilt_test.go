package prebuilt

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

// archive returns a tar.gz holding the given files, in order.
func archive(t *testing.T, files ...[2]string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		assert.NilError(t, tw.WriteHeader(&tar.Header{Name: f[0], Mode: 0644, Size: int64(len(f[1]))}))
		_, err := tw.Write([]byte(f[1]))
		assert.NilError(t, err)
	}
	assert.NilError(t, tw.Close())
	assert.NilError(t, gz.Close())
	return &buf
}

func TestExtractBinary(t *testing.T) {
	t.Parallel()
	// Arrange - the layout GoReleaser produces
	r := archive(t,
		[2]string{"CHANGELOG.md", "changes"},
		[2]string{"README.md", "readme"},
		[2]string{"gritz", "binary"},
	)
	path := filepath.Join(t.TempDir(), "gritz-linux-amd64")

	// Act
	err := extractBinary(r, path)

	// Assert
	assert.NilError(t, err)
	data, err := os.ReadFile(path)
	assert.NilError(t, err)
	assert.Equal(t, string(data), "binary")
	info, err := os.Stat(path)
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0755))
}

func TestExtractBinary_Missing(t *testing.T) {
	t.Parallel()
	// Arrange
	r := archive(t, [2]string{"README.md", "readme"})
	path := filepath.Join(t.TempDir(), "gritz-linux-amd64")

	// Act
	err := extractBinary(r, path)

	// Assert
	assert.ErrorContains(t, err, "no gritz entry")
	_, err = os.Stat(path)
	assert.Assert(t, os.IsNotExist(err))
}
