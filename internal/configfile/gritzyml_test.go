package configfile

import (
	"io/fs"
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
	assert.DeepEqual(t, f, &File{
		Org:     "acme",
		Server:  "https://gritz.example.com",
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
	assert.DeepEqual(t, f, &File{})
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

func TestLoad(t *testing.T) {
	t.Parallel()
	// Arrange - a base layer, a parent directory and a repo below it
	base := filepath.FromSlash("testdata/tree/config/.gritz.yml")
	work := filepath.FromSlash("testdata/tree/work/.gritz.yml")
	repo := filepath.FromSlash("testdata/tree/work/repo/.gritz.yml")

	// Act
	m, err := Load(LoadOptions{
		Dir:  filepath.FromSlash("testdata/tree/work/repo"),
		Root: filepath.FromSlash("testdata/tree"),
		Base: base,
	})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, m, &Merged{
		Org:         "acme",
		OrgFrom:     work,
		Server:      "https://work.example.com",
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

func TestLoad_Local(t *testing.T) {
	t.Parallel()
	// Arrange - a repo whose .gritz.local.yml overrides its .gritz.yml
	repo := filepath.FromSlash("testdata/local/repo/.gritz.yml")
	local := filepath.FromSlash("testdata/local/repo/.gritz.local.yml")

	// Act
	m, err := Load(LoadOptions{
		Dir:  filepath.FromSlash("testdata/local/repo"),
		Root: filepath.FromSlash("testdata/local"),
		Base: filepath.FromSlash("testdata/local/config/.gritz.yml"),
	})

	// Assert
	assert.NilError(t, err)
	assert.DeepEqual(t, m, &Merged{
		Org:     "local",
		OrgFrom: local,
		Orgs: map[string]Entry{
			"acme":  {ID: 12, Key: "k-local"},
			"ci":    {ID: 13, Key: "k-ci"},
			"local": {ID: 2, Key: "kl", Server: "http://localhost:8080"},
		},
		OrgsFrom: map[string]string{
			"acme":  local,
			"ci":    repo,
			"local": local,
		},
	})
}

func TestLoad_BaseOnly(t *testing.T) {
	t.Parallel()
	base := filepath.FromSlash("testdata/base-only/config/.gritz.yml")

	m, err := Load(LoadOptions{
		Dir:  filepath.FromSlash("testdata/base-only"),
		Root: filepath.FromSlash("testdata/base-only"),
		Base: base,
	})

	assert.NilError(t, err)
	assert.DeepEqual(t, m, &Merged{
		Org:      "acme",
		OrgFrom:  base,
		Orgs:     map[string]Entry{"acme": {ID: 12, Key: "k"}},
		OrgsFrom: map[string]string{"acme": base},
	})
}

func TestLoad_NoFiles(t *testing.T) {
	t.Parallel()
	dir := filepath.FromSlash("testdata/tree")

	m, err := Load(LoadOptions{Dir: dir, Root: dir, Base: filepath.Join(dir, Name)})

	assert.NilError(t, err)
	assert.DeepEqual(t, m, &Merged{Orgs: map[string]Entry{}, OrgsFrom: map[string]string{}})
}

func TestLoad_Directory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, Name)
	assert.NilError(t, os.Mkdir(p, 0o755))

	_, err := Load(LoadOptions{Dir: dir, Root: dir, Base: filepath.Join(dir, "base.yml")})

	assert.ErrorContains(t, err, p+": is a directory")
}

func TestLoad_UnknownField(t *testing.T) {
	t.Parallel()
	dir := filepath.FromSlash("testdata/unknown-field")

	_, err := Load(LoadOptions{Dir: dir, Root: dir, Base: filepath.Join(dir, "base.yml")})

	assert.ErrorContains(t, err, filepath.Join(dir, Name)+": ")
}

func TestIsAllowed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		allowed []string
		org     string
		want    bool
	}{
		{"unset", nil, "acme", true},
		{"listed", []string{"acme", "local"}, "local", true},
		{"not listed", []string{"acme"}, "local", false},
		{"empty", []string{}, "acme", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := Merged{Allowed: tt.allowed}

			assert.Equal(t, m.IsAllowed(tt.org), tt.want)
		})
	}
}

func TestEntryValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		entry Entry
		err   string
	}{
		{"complete", Entry{ID: 12, Key: "k"}, ""},
		{"with server", Entry{ID: 12, Key: "k", Server: "http://localhost:8080"}, ""},
		{"no id", Entry{Key: "k"}, "no id"},
		{"refresh token", Entry{ID: 12, RefreshToken: "rt"}, ""},
		{"neither", Entry{ID: 12}, "no key or refresh_token"},
		{"both", Entry{ID: 12, Key: "k", RefreshToken: "rt"}, "both key and refresh_token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.entry.Validate()

			if tt.err != "" {
				assert.Error(t, err, tt.err)
				return
			}
			assert.NilError(t, err)
		})
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	orgs := map[string]Entry{"acme": {ID: 12, Key: "k1"}, "local": {ID: 1, Key: "k2"}}
	from := map[string]string{"acme": "/cfg/.gritz.yml", "local": "/cfg/.gritz.yml"}
	tests := []struct {
		name   string
		merged Merged
		err    string
	}{
		{
			name:   "empty",
			merged: Merged{},
		},
		{
			name: "org in allowed",
			merged: Merged{
				Org: "acme", OrgFrom: "/repo/.gritz.yml",
				Allowed: []string{"acme"}, AllowedFrom: "/repo/.gritz.yml",
				Orgs: orgs, OrgsFrom: from,
			},
		},
		{
			name: "org outside allowed",
			merged: Merged{
				Org: "acme", OrgFrom: "/home/me/src/labs/.gritz.yml",
				Allowed: []string{"labs"}, AllowedFrom: "/home/me/src/.gritz.yml",
				Orgs: orgs, OrgsFrom: from,
			},
			err: `org "acme" (from /home/me/src/labs/.gritz.yml) is not in allowed [labs] (from /home/me/src/.gritz.yml)`,
		},
		{
			name: "entry without id",
			merged: Merged{
				Orgs:     map[string]Entry{"acme": {Key: "k1"}},
				OrgsFrom: map[string]string{"acme": "/repo/.gritz.yml"},
			},
			err: `org entry "acme" (from /repo/.gritz.yml): no id`,
		},
		{
			name: "entry without key",
			merged: Merged{
				Orgs:     map[string]Entry{"acme": {ID: 12}},
				OrgsFrom: map[string]string{"acme": "/repo/.gritz.yml"},
			},
			err: `org entry "acme" (from /repo/.gritz.yml): no key or refresh_token`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.merged.Validate()

			if tt.err != "" {
				assert.Error(t, err, tt.err)
				return
			}
			assert.NilError(t, err)
		})
	}
}

func TestLookup(t *testing.T) {
	t.Parallel()
	orgs := map[string]Entry{
		"acme":  {ID: 12, Key: "k1"},
		"local": {ID: 1, Key: "k2", Server: "http://localhost:8080"},
	}
	from := map[string]string{"acme": "/cfg/.gritz.yml", "local": "/repo/.gritz.yml"}
	acme := Entry{ID: 12, Key: "k1", Server: DefaultServer, Name: "acme", Path: "/cfg/.gritz.yml"}
	local := Entry{ID: 1, Key: "k2", Server: "http://localhost:8080", Name: "local", Path: "/repo/.gritz.yml"}
	tests := []struct {
		name   string
		merged Merged
		org    string
		want   *Entry
		err    string
	}{
		{
			name:   "org",
			merged: Merged{Org: "acme", OrgFrom: "/repo/.gritz.yml", Orgs: orgs, OrgsFrom: from},
			want:   &acme,
		},
		{
			name:   "org argument overrides org",
			merged: Merged{Org: "acme", OrgFrom: "/repo/.gritz.yml", Orgs: orgs, OrgsFrom: from},
			org:    "local",
			want:   &local,
		},
		{
			name: "org in allowed",
			merged: Merged{
				Allowed: []string{"acme", "local"}, AllowedFrom: "/repo/.gritz.yml",
				Orgs: orgs, OrgsFrom: from,
			},
			org:  "local",
			want: &local,
		},
		{
			name:   "unnamed entry",
			merged: Merged{Orgs: map[string]Entry{"": {Key: "k3", Server: "http://localhost:8080"}}},
			want:   &Entry{Key: "k3", Server: "http://localhost:8080"},
		},
		{
			name:   "nothing selected",
			merged: Merged{Orgs: orgs, OrgsFrom: from},
			err:    "no org selected: pass --org or set org in a .gritz.yml file",
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := tt.merged.Lookup(tt.org)

			if tt.err != "" {
				assert.Error(t, err, tt.err)
				return
			}
			assert.NilError(t, err)
			assert.DeepEqual(t, got, tt.want)
		})
	}
}

func TestEntries(t *testing.T) {
	t.Parallel()
	m := Merged{
		Allowed: []string{"local", "acme"},
		Orgs: map[string]Entry{
			"local": {ID: 1, Key: "k2", Server: "http://localhost:8080"},
			"labs":  {ID: 31, Key: "k3"},
			"acme":  {ID: 12, Key: "k1"},
		},
		OrgsFrom: map[string]string{"local": "/repo/.gritz.yml", "labs": "/cfg/.gritz.yml", "acme": "/cfg/.gritz.yml"},
	}

	got := m.Entries()

	assert.DeepEqual(t, got, []Entry{
		{ID: 12, Key: "k1", Server: DefaultServer, Name: "acme", Path: "/cfg/.gritz.yml"},
		{ID: 1, Key: "k2", Server: "http://localhost:8080", Name: "local", Path: "/repo/.gritz.yml"},
	})
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

func TestLoadFile(t *testing.T) {
	t.Parallel()
	p := filepath.FromSlash("testdata/read/.gritz.yml")

	m, err := LoadFile(p)

	assert.NilError(t, err)
	assert.DeepEqual(t, m, &Merged{
		Org:         "acme",
		OrgFrom:     p,
		Server:      "https://gritz.example.com",
		Allowed:     []string{"acme", "local"},
		AllowedFrom: p,
		Orgs: map[string]Entry{
			"acme":  {ID: 12, Key: "k1"},
			"local": {ID: 1, Key: "k2", Server: "http://localhost:8080"},
		},
		OrgsFrom: map[string]string{"acme": p, "local": p},
	})
}

func TestLoadFile_Missing(t *testing.T) {
	t.Parallel()

	_, err := LoadFile(filepath.FromSlash("testdata/empty/missing.yml"))

	assert.ErrorIs(t, err, fs.ErrNotExist)
}
