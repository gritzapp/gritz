package configfile

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

// copyTestdata copies testdata/name into a temp dir and returns that dir, so
// the .gritz.yml walk sees only the fixture's files.
func copyTestdata(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	assert.NilError(t, os.CopyFS(dir, os.DirFS(filepath.Join("testdata", name))))
	return dir
}

func TestRead(t *testing.T) {
	// Act
	f, err := Read(filepath.Join("testdata", "read", Name))

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, f, &FileV2{
		Default: "acme",
		Allowed: []string{"acme", "local"},
		Orgs: map[string]Entry{
			"acme":  {ID: 12, Key: "k1"},
			"local": {ID: 1, Key: "k2", Server: "http://localhost:8080"},
		},
	})
}

func TestRead_Empty(t *testing.T) {
	f, err := Read(filepath.Join("testdata", "empty", Name))

	assert.NilError(t, err)
	assert.DeepEqual(t, f, &FileV2{})
}

func TestRead_UnknownField(t *testing.T) {
	for _, dir := range []string{"unknown-field", "unknown-entry-field"} {
		t.Run(dir, func(t *testing.T) {
			_, err := Read(filepath.Join("testdata", dir, Name))

			assert.ErrorContains(t, err, "not found in type")
		})
	}
}

func TestLoadV2(t *testing.T) {
	// Arrange - a base layer, a parent directory and a repo below it
	root := copyTestdata(t, "tree")
	t.Setenv("GRITZ_CONFIG_DIR", filepath.Join(root, "config"))
	repo := filepath.Join(root, "work", "repo")
	assert.NilError(t, os.Mkdir(filepath.Join(repo, ".git"), 0o755))
	sub := filepath.Join(repo, "sub")
	assert.NilError(t, os.Mkdir(sub, 0o755))
	base := filepath.Join(root, "config", Name)
	work := filepath.Join(root, "work", Name)
	repoFile := filepath.Join(repo, Name)

	// Act
	m, err := LoadV2(sub)

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, m, &Merged{
		Default:     "acme",
		DefaultFrom: work,
		Allowed:     []string{"acme"},
		AllowedFrom: repoFile,
		Orgs: map[string]Entry{
			"personal": {ID: 1, Key: "kp"},
			"acme":     {ID: 12, Key: "k-work"},
			"local":    {ID: 2, Server: "http://localhost:9090"},
		},
		OrgsFrom: map[string]string{
			"personal": base,
			"acme":     work,
			"local":    repoFile,
		},
	})
}

func TestLoadV2_BaseOnly(t *testing.T) {
	root := copyTestdata(t, "base-only")
	t.Setenv("GRITZ_CONFIG_DIR", filepath.Join(root, "config"))
	base := filepath.Join(root, "config", Name)

	m, err := LoadV2(root)

	assert.NilError(t, err)
	assert.DeepEqual(t, m, &Merged{
		Default:     "acme",
		DefaultFrom: base,
		Orgs:        map[string]Entry{"acme": {ID: 12, Key: "k"}},
		OrgsFrom:    map[string]string{"acme": base},
	})
}

func TestLoadV2_NoFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GRITZ_CONFIG_DIR", filepath.Join(root, "config"))

	m, err := LoadV2(root)

	assert.NilError(t, err)
	assert.DeepEqual(t, m, &Merged{Orgs: map[string]Entry{}, OrgsFrom: map[string]string{}})
}

func TestLoadV2_NotRegular(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GRITZ_CONFIG_DIR", filepath.Join(root, "config"))
	assert.NilError(t, os.Mkdir(filepath.Join(root, Name), 0o755))

	_, err := LoadV2(root)

	assert.ErrorContains(t, err, filepath.Join(root, Name)+": not a regular file")
}

func TestLoadV2_UnknownField(t *testing.T) {
	root := copyTestdata(t, "unknown-field")
	t.Setenv("GRITZ_CONFIG_DIR", filepath.Join(root, "config"))

	_, err := LoadV2(root)

	assert.ErrorContains(t, err, filepath.Join(root, Name)+": ")
}

func TestSelect(t *testing.T) {
	acme := Entry{ID: 12, Key: "k1"}
	local := Entry{ID: 1, Key: "k2", Server: "http://localhost:8080"}
	orgs := map[string]Entry{"acme": acme, "local": local}
	from := map[string]string{"acme": "/cfg/.gritz.yml", "local": "/cfg/.gritz.yml"}
	tests := []struct {
		name   string
		merged Merged
		org    string
		want   *Selection
		err    string
	}{
		{
			name:   "default",
			merged: Merged{Default: "acme", DefaultFrom: "/repo/.gritz.yml", Orgs: orgs, OrgsFrom: from},
			want:   &Selection{Name: "acme", Entry: acme},
		},
		{
			name:   "org overrides default",
			merged: Merged{Default: "acme", DefaultFrom: "/repo/.gritz.yml", Orgs: orgs, OrgsFrom: from},
			org:    "local",
			want:   &Selection{Name: "local", Entry: local},
		},
		{
			name: "org in allowed",
			merged: Merged{
				Allowed: []string{"acme", "local"}, AllowedFrom: "/repo/.gritz.yml",
				Orgs: orgs, OrgsFrom: from,
			},
			org:  "local",
			want: &Selection{Name: "local", Entry: local},
		},
		{
			name:   "nothing selected",
			merged: Merged{Orgs: orgs, OrgsFrom: from},
			err:    "no org selected: pass --org or set default in a .gritz.yml file",
		},
		{
			name: "default outside allowed",
			merged: Merged{
				Default: "acme", DefaultFrom: "/home/me/src/labs/.gritz.yml",
				Allowed: []string{"labs"}, AllowedFrom: "/home/me/src/.gritz.yml",
				Orgs: orgs, OrgsFrom: from,
			},
			err: `default "acme" (from /home/me/src/labs/.gritz.yml) is not in allowed [labs] (from /home/me/src/.gritz.yml)`,
		},
		{
			name: "default outside allowed with org given",
			merged: Merged{
				Default: "acme", DefaultFrom: "/home/me/src/labs/.gritz.yml",
				Allowed: []string{"local"}, AllowedFrom: "/home/me/src/.gritz.yml",
				Orgs: orgs, OrgsFrom: from,
			},
			org: "local",
			err: `default "acme" (from /home/me/src/labs/.gritz.yml) is not in allowed [local] (from /home/me/src/.gritz.yml)`,
		},
		{
			name: "org outside allowed",
			merged: Merged{
				Allowed: []string{"acme"}, AllowedFrom: "/home/me/src/acme/.gritz.yml",
				Orgs: orgs, OrgsFrom: from,
			},
			org: "local",
			err: `org "local" is not allowed here: allowed is [acme] (from /home/me/src/acme/.gritz.yml)`,
		},
		{
			name:   "empty allowed",
			merged: Merged{Allowed: []string{}, AllowedFrom: "/repo/.gritz.yml", Orgs: orgs, OrgsFrom: from},
			org:    "acme",
			err:    `org "acme" is not allowed here: allowed is [] (from /repo/.gritz.yml)`,
		},
		{
			name:   "missing entry",
			merged: Merged{Orgs: orgs, OrgsFrom: from},
			org:    "labs",
			err:    "no org entry named \"labs\": run `gritz login --key <key> --name labs`",
		},
		{
			name: "entry without id",
			merged: Merged{
				Orgs:     map[string]Entry{"acme": {Key: "k1"}},
				OrgsFrom: map[string]string{"acme": "/repo/.gritz.yml"},
			},
			org: "acme",
			err: `org entry "acme" (from /repo/.gritz.yml) has no id`,
		},
		{
			name: "entry without key",
			merged: Merged{
				Orgs:     map[string]Entry{"acme": {ID: 12}},
				OrgsFrom: map[string]string{"acme": "/repo/.gritz.yml"},
			},
			org: "acme",
			err: `org entry "acme" (from /repo/.gritz.yml) has no key`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.merged.Select(tt.org)

			if tt.err != "" {
				assert.Error(t, err, tt.err)
				return
			}
			assert.NilError(t, err)
			assert.DeepEqual(t, got, tt.want)
		})
	}
}

func TestSlug(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Acme Labs", "acme-labs"},
		{"acme", "acme"},
		{"  Acme -- Labs, Inc.  ", "acme-labs-inc"},
		{"Org 42", "org-42"},
		{"!!!", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, Slug(tt.in), tt.want)
		})
	}
}
