package configfile

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/golden"
)

// Regenerate the goldens with: go test ./internal/configfile/ -run TestLogin -update
func TestLogin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     string // starting file in testdata; empty means no file
		entryName string
		entry     Entry
		golden    string
		err       string
	}{
		{
			name:      "new file",
			entryName: "acme",
			entry:     Entry{ID: 12, Key: "k1", Server: DefaultServer},
			golden:    "login-new-file.golden",
		},
		{
			name:      "server is written",
			entryName: "local",
			entry:     Entry{ID: 1, Key: "k1", Server: "http://localhost:8080"},
			golden:    "login-server-is-written.golden",
		},
		{
			name:      "new entry keeps org and allowed",
			input:     "login-new-entry-keeps-org-and-allowed.yml",
			entryName: "labs",
			entry:     Entry{ID: 31, Key: "k2", Server: DefaultServer},
			golden:    "login-new-entry-keeps-org-and-allowed.golden",
		},
		{
			name:      "same id and server replaces key",
			input:     "login-same-id-and-server-replaces-key.yml",
			entryName: "local",
			entry:     Entry{ID: 1, Key: "new", Server: "http://localhost:8080"},
			golden:    "login-same-id-and-server-replaces-key.golden",
		},
		{
			name:      "org set when absent",
			input:     "login-org-set-when-absent.yml",
			entryName: "labs",
			entry:     Entry{ID: 31, Key: "k2", Server: DefaultServer},
			golden:    "login-org-set-when-absent.golden",
		},
		{
			name:      "different id",
			input:     "login-different-id.yml",
			entryName: "acme",
			entry:     Entry{ID: 12, Key: "k2", Server: DefaultServer},
			err:       `entry name in use: "acme" is org 31 on https://gritz.dev; pass --name to use another name`,
		},
		{
			name:      "different server",
			input:     "login-different-server.yml",
			entryName: "acme",
			entry:     Entry{ID: 12, Key: "k2", Server: "http://localhost:8080"},
			err:       `entry name in use: "acme" is org 12 on https://gritz.dev; pass --name to use another name`,
		},
		{
			name:      "unknown field",
			input:     "login-unknown-field.yml",
			entryName: "acme",
			entry:     Entry{ID: 12, Key: "k1", Server: DefaultServer},
			err:       "field default not found in type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			path := filepath.Join(t.TempDir(), "gritz", Name)
			var input []byte
			if tt.input != "" {
				var err error
				input, err = os.ReadFile(filepath.Join("testdata", tt.input))
				assert.NilError(t, err)
				assert.NilError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				assert.NilError(t, os.WriteFile(path, input, 0o644))
			}

			// Act
			err := Login(path, tt.entryName, tt.entry)

			// Assert
			if tt.err != "" {
				assert.ErrorContains(t, err, tt.err)
				got, _ := os.ReadFile(path)
				assert.Equal(t, string(got), string(input), "file changed on error")
				return
			}
			assert.NilError(t, err)
			got, err := os.ReadFile(path)
			assert.NilError(t, err)
			golden.Assert(t, string(got), tt.golden)
		})
	}
}

func TestLogin_NameTaken(t *testing.T) {
	t.Parallel()
	input, err := os.ReadFile(filepath.Join("testdata", "login-different-id.yml"))
	assert.NilError(t, err)
	path := filepath.Join(t.TempDir(), Name)
	assert.NilError(t, os.WriteFile(path, input, 0o600))

	err = Login(path, "acme", Entry{ID: 12, Key: "k2", Server: DefaultServer})

	assert.ErrorIs(t, err, ErrNameTaken)
}

func TestLogin_NewFileMode(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "gritz", Name)

	err := Login(path, "acme", Entry{ID: 12, Key: "k1", Server: DefaultServer})

	assert.NilError(t, err)
	info, err := os.Stat(path)
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o600))
}

func TestLogin_ExistingFileMode(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), Name)
	assert.NilError(t, os.WriteFile(path, nil, 0o640))
	assert.NilError(t, os.Chmod(path, 0o640)) // in case the umask narrowed it

	err := Login(path, "acme", Entry{ID: 12, Key: "k1", Server: DefaultServer})

	assert.NilError(t, err)
	info, err := os.Stat(path)
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o640))
}

func TestLoginServer_Path(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "top-level server", path: "testdata/read/.gritz.yml", want: "https://gritz.example.com"},
		{name: "missing file", path: "testdata/empty/missing.yml", want: DefaultServer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := LoginServer(filepath.FromSlash(tt.path))

			assert.NilError(t, err)
			assert.Equal(t, got, tt.want)
		})
	}
}
