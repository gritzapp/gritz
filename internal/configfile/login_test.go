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
		file      string // initial contents; empty means no file
		entryName string
		entry     Entry
		golden    string
		err       string
	}{
		{
			name:      "new file",
			entryName: "acme",
			entry:     Entry{ID: 12, Key: "k1"},
			golden:    "login-new-file.golden",
		},
		{
			name:      "server is written",
			entryName: "local",
			entry:     Entry{ID: 1, Key: "k1", Server: "http://localhost:8080"},
			golden:    "login-server-is-written.golden",
		},
		{
			name: "new entry keeps default and allowed",
			file: `default: acme
allowed: [acme, labs]
orgs:
  acme: {id: 12, key: k1}
`,
			entryName: "labs",
			entry:     Entry{ID: 31, Key: "k2"},
			golden:    "login-new-entry-keeps-default-and-allowed.golden",
		},
		{
			name: "same id and server replaces key",
			file: `default: acme
orgs:
  local: {id: 1, key: old, server: "http://localhost:8080"}
`,
			entryName: "local",
			entry:     Entry{ID: 1, Key: "new", Server: "http://localhost:8080"},
			golden:    "login-same-id-and-server-replaces-key.golden",
		},
		{
			name: "default set when absent",
			file: `orgs:
  acme: {id: 12, key: k1}
`,
			entryName: "labs",
			entry:     Entry{ID: 31, Key: "k2"},
			golden:    "login-default-set-when-absent.golden",
		},
		{
			name: "different id",
			file: `orgs:
  acme: {id: 31, key: k1}
`,
			entryName: "acme",
			entry:     Entry{ID: 12, Key: "k2"},
			err:       `entry name in use: entry "acme" is org 31 on the default server; pass --name to add org 12 under another name`,
		},
		{
			name: "different server",
			file: `orgs:
  acme: {id: 12, key: k1}
`,
			entryName: "acme",
			entry:     Entry{ID: 12, Key: "k2", Server: "http://localhost:8080"},
			err:       `entry name in use: entry "acme" is org 12 on the default server; pass --name to add org 12 under another name`,
		},
		{
			name:      "unknown field",
			file:      "defualt: acme\n",
			entryName: "acme",
			entry:     Entry{ID: 12, Key: "k1"},
			err:       "field defualt not found in type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Arrange
			path := filepath.Join(t.TempDir(), "gritz", Name)
			if tt.file != "" {
				assert.NilError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				assert.NilError(t, os.WriteFile(path, []byte(tt.file), 0o644))
			}

			// Act
			err := Login(path, tt.entryName, tt.entry)

			// Assert
			if tt.err != "" {
				assert.ErrorContains(t, err, tt.err)
				got, _ := os.ReadFile(path)
				assert.Equal(t, string(got), tt.file, "file changed on error")
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
	path := filepath.Join(t.TempDir(), Name)
	assert.NilError(t, os.WriteFile(path, []byte("orgs:\n  acme: {id: 31, key: k1}\n"), 0o600))

	err := Login(path, "acme", Entry{ID: 12, Key: "k2"})

	assert.ErrorIs(t, err, ErrNameTaken)
}

func TestLogin_Mode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	created := filepath.Join(dir, "created", Name)
	existing := filepath.Join(dir, Name)
	assert.NilError(t, os.WriteFile(existing, nil, 0o640))

	err := Login(created, "acme", Entry{ID: 12, Key: "k1"})
	assert.NilError(t, err)
	err = Login(existing, "acme", Entry{ID: 12, Key: "k1"})
	assert.NilError(t, err)

	// A new file is private; an existing one keeps its mode.
	for path, want := range map[string]os.FileMode{created: 0o600, existing: 0o640} {
		info, err := os.Stat(path)
		assert.NilError(t, err)
		assert.Equal(t, info.Mode().Perm(), want, path)
	}
}
