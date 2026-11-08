package configfile

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

func TestRead(t *testing.T) {
	t.Parallel()

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
	t.Parallel()
	f, err := Read(filepath.Join("testdata", "empty", Name))

	assert.NilError(t, err)
	assert.DeepEqual(t, f, &FileV2{})
}

func TestRead_UnknownField(t *testing.T) {
	t.Parallel()
	for _, dir := range []string{"unknown-field", "unknown-entry-field"} {
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			_, err := Read(filepath.Join("testdata", dir, Name))

			assert.ErrorContains(t, err, "not found in type")
		})
	}
}

func TestLoadV2(t *testing.T) {
	t.Parallel()
	// Arrange - a base layer, a parent directory and a repo below it
	base := filepath.FromSlash("testdata/tree/config/.gritz.yml")
	work := filepath.FromSlash("testdata/tree/work/.gritz.yml")
	repo := filepath.FromSlash("testdata/tree/work/repo/.gritz.yml")

	// Act
	m, err := LoadV2(LoadOptions{
		Dir:  filepath.FromSlash("testdata/tree/work/repo"),
		Root: filepath.FromSlash("testdata/tree"),
		Base: base,
	})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, m, &Merged{
		Default:     "acme",
		DefaultFrom: work,
		Allowed:     []string{"acme"},
		AllowedFrom: repo,
		Orgs: map[string]Entry{
			"personal": {ID: 1, Key: "kp"},
			"acme":     {ID: 12, Key: "k-work"},
			"local":    {ID: 2, Server: "http://localhost:9090"},
		},
		OrgsFrom: map[string]string{
			"personal": base,
			"acme":     work,
			"local":    repo,
		},
	})
}

func TestLoadV2_BaseOnly(t *testing.T) {
	t.Parallel()
	base := filepath.FromSlash("testdata/base-only/config/.gritz.yml")

	m, err := LoadV2(LoadOptions{
		Dir:  filepath.FromSlash("testdata/base-only"),
		Root: filepath.FromSlash("testdata/base-only"),
		Base: base,
	})

	assert.NilError(t, err)
	assert.DeepEqual(t, m, &Merged{
		Default:     "acme",
		DefaultFrom: base,
		Orgs:        map[string]Entry{"acme": {ID: 12, Key: "k"}},
		OrgsFrom:    map[string]string{"acme": base},
	})
}

func TestLoadV2_NoFiles(t *testing.T) {
	t.Parallel()
	dir := filepath.FromSlash("testdata/tree")

	m, err := LoadV2(LoadOptions{Dir: dir, Root: dir, Base: filepath.Join(dir, Name)})

	assert.NilError(t, err)
	assert.DeepEqual(t, m, &Merged{Orgs: map[string]Entry{}, OrgsFrom: map[string]string{}})
}

func TestLoadV2_Directory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, Name)
	assert.NilError(t, os.Mkdir(p, 0o755))

	_, err := LoadV2(LoadOptions{Dir: dir, Root: dir, Base: filepath.Join(dir, "base.yml")})

	assert.ErrorContains(t, err, p+": is a directory")
}

func TestLoadV2_UnknownField(t *testing.T) {
	t.Parallel()
	dir := filepath.FromSlash("testdata/unknown-field")

	_, err := LoadV2(LoadOptions{Dir: dir, Root: dir, Base: filepath.Join(dir, "base.yml")})

	assert.ErrorContains(t, err, filepath.Join(dir, Name)+": ")
}

func TestSelect(t *testing.T) {
	t.Parallel()
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
			t.Parallel()
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
	t.Parallel()
	tests := []struct {
		in   string
		want string
	}{
		{"Acme Labs", "acme-labs"},
		{"acme", "acme"},
		{"  Acme -- Labs, Inc.  ", "acme-labs-inc"},
		{"Org 42", "org-42"},
		{"Ilia Choly's Org", "ilia-cholys-org"},
		{"Ilia Choly\u2019s Org", "ilia-cholys-org"},
		{"'Quoted'", "quoted"},
		{"!!!", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, Slug(tt.in), tt.want)
		})
	}
}
