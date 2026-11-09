package configfile

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Name is the file name used at every level.
const Name = ".gritz.yml"

// Entry is one named org in a .gritz.yml file.
type Entry struct {
	// ID is the org id.
	ID int64 `yaml:"id"`
	// Key is an API key for the org.
	Key string `yaml:"key,omitempty"`
	// RefreshToken is an OAuth refresh token for the org.
	RefreshToken string `yaml:"refresh_token,omitempty"`
	// Server is the server URL; empty means the default server.
	Server string `yaml:"server,omitempty"`
	// Name is the entry's name; set by Lookup.
	Name string `yaml:"-"`
	// Path is the file the entry came from; set by Lookup.
	Path string `yaml:"-"`
}

// Validate checks that the entry has an id and exactly one of a key and a
// refresh token.
func (e Entry) Validate() error {
	switch {
	case e.ID == 0:
		return errors.New("no id")
	case e.Key == "" && e.RefreshToken == "":
		return errors.New("no key or refresh_token")
	case e.Key != "" && e.RefreshToken != "":
		return errors.New("both key and refresh_token")
	}
	return nil
}

// File is the contents of one .gritz.yml file.
type File struct {
	// Org is the entry name selected when none is given.
	Org string `yaml:"org,omitempty"`
	// Server is the server `gritz login` uses when --server isn't given. It
	// has no effect on reading entries.
	Server string `yaml:"server,omitempty"`
	// Allowed lists the entry names that may be selected; nil allows all.
	Allowed []string `yaml:"allowed,omitempty"`
	// Orgs maps local names to entries.
	Orgs map[string]Entry `yaml:"orgs,omitempty"`
}

// Read parses the .gritz.yml file at path, rejecting unknown fields.
func Read(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &f, nil
}

// Merged is the result of merging .gritz.yml files, nearest winning.
type Merged struct {
	Org         string
	OrgFrom     string // path of the file that set Org
	Server      string // the nearest top-level server, used only by `gritz login`
	Allowed     []string
	AllowedFrom string // path of the file that set Allowed
	Orgs        map[string]Entry
	OrgsFrom    map[string]string // entry name -> path of the file that set it
}

// LoadOptions configures Load.
type LoadOptions struct {
	// Dir is where the walk starts; empty means the working directory. It
	// must be absolute unless Root is set.
	Dir string
	// Root is the last directory the walk visits; empty means /.
	Root string
	// Base is the base layer; empty means $CONFIG_DIR/.gritz.yml.
	Base string
}

// Load merges the base layer and every .gritz.yml from opts.Dir up to opts.Root.
func Load(opts LoadOptions) (*Merged, error) {
	if opts.Dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		opts.Dir = wd
	}
	if opts.Root == "" && !filepath.IsAbs(opts.Dir) {
		panic("configfile: Load walks to / from a relative Dir")
	}
	base := opts.Base
	if base == "" {
		configDir, err := Dir()
		if err != nil {
			return nil, err
		}
		base = filepath.Join(configDir, Name)
	}
	// Merged nearest first, so the first file to set a value wins.
	m := &Merged{Orgs: map[string]Entry{}, OrgsFrom: map[string]string{}}
	for d := opts.Dir; ; d = filepath.Dir(d) {
		if p := filepath.Join(d, Name); p != base {
			if err := m.merge(p); err != nil {
				return nil, err
			}
		}
		if d == opts.Root || filepath.Dir(d) == d {
			break
		}
	}
	if err := m.merge(base); err != nil {
		return nil, err
	}
	return m, nil
}

// merge reads the .gritz.yml file at path and layers it beneath m, filling
// in only the values m doesn't have yet. A missing file is skipped.
func (m *Merged) merge(path string) error {
	f, err := Read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if m.Org == "" && f.Org != "" {
		m.Org, m.OrgFrom = f.Org, path
	}
	if m.Server == "" {
		m.Server = f.Server
	}
	if m.Allowed == nil && f.Allowed != nil {
		m.Allowed, m.AllowedFrom = f.Allowed, path
	}
	for name, e := range f.Orgs {
		if _, ok := m.Orgs[name]; !ok {
			m.Orgs[name], m.OrgsFrom[name] = e, path
		}
	}
	return nil
}

// IsAllowed reports whether org may be selected: Allowed is unset or lists it.
func (m *Merged) IsAllowed(org string) bool {
	return m.Allowed == nil || slices.Contains(m.Allowed, org)
}

// Validate checks that Org is allowed and that every entry is complete,
// returning errors that name the file responsible.
func (m *Merged) Validate() error {
	if m.Org != "" && !m.IsAllowed(m.Org) {
		return fmt.Errorf("org %q (from %s) is not in allowed %s (from %s)",
			m.Org, m.OrgFrom, m.Allowed, m.AllowedFrom)
	}
	for _, name := range slices.Sorted(maps.Keys(m.Orgs)) {
		if err := m.Orgs[name].Validate(); err != nil {
			return fmt.Errorf("org entry %q (from %s): %w", name, m.OrgsFrom[name], err)
		}
	}
	return nil
}

// Entries returns the entries Lookup accepts (those Allowed permits), sorted
// by name, each as Lookup returns it: Name and Path set, Server defaulted.
// Call Validate first.
func (m *Merged) Entries() []Entry {
	var entries []Entry
	for _, name := range slices.Sorted(maps.Keys(m.Orgs)) {
		if m.IsAllowed(name) {
			entries = append(entries, m.entry(name))
		}
	}
	return entries
}

// Lookup returns the entry named org, or m.Org when org is empty, enforcing
// m.Allowed, with Name and Path set, and Server set to DefaultServer when the
// entry has none. Call Validate first.
func (m *Merged) Lookup(org string) (*Entry, error) {
	name := cmp.Or(org, m.Org)
	if name == "" {
		return nil, errors.New("no org selected: pass --org or set org in a .gritz.yml file")
	}
	if !m.IsAllowed(name) {
		return nil, fmt.Errorf("org %q is not allowed here: allowed is %s (from %s)",
			name, m.Allowed, m.AllowedFrom)
	}
	if _, ok := m.Orgs[name]; !ok {
		return nil, fmt.Errorf("no org entry named %q: run `gritz login --key <key> --name %s`", name, name)
	}
	e := m.entry(name)
	return &e, nil
}

// entry returns the entry named name with Name and Path set and Server
// defaulted.
func (m *Merged) entry(name string) Entry {
	e := m.Orgs[name]
	e.Name, e.Path = name, m.OrgsFrom[name]
	e.Server = cmp.Or(e.Server, DefaultServer)
	return e
}

// Slug turns an org name into an entry name, returning "" if nothing is left.
// It lowercases the name, drops apostrophes, replaces each run of other
// non-alphanumeric characters with a single '-', and trims leading and
// trailing '-'.
func Slug(orgName string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(orgName) {
		switch {
		case r == '\'' || r == '\u2019':
			// Drop apostrophes so "Ilia Choly's Org" slugs to ilia-cholys-org.
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		default:
			dash = true
		}
	}
	return b.String()
}
