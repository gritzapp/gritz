package configfile

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	Key string `yaml:"key"`
	// Server is the server URL; empty means gritzclient.DefaultURL.
	Server string `yaml:"server,omitempty"`
}

// FileV2 is the contents of one .gritz.yml file.
//
// TODO: rename to File once the config.json File is removed.
type FileV2 struct {
	// Default is the entry name selected when none is given.
	Default string `yaml:"default,omitempty"`
	// Allowed lists the entry names that may be selected; nil allows all.
	Allowed []string `yaml:"allowed,omitempty"`
	// Orgs maps local names to entries.
	Orgs map[string]Entry `yaml:"orgs,omitempty"`
}

// Read parses the .gritz.yml file at path, rejecting unknown fields.
func Read(path string) (*FileV2, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f FileV2
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &f, nil
}

// Merged is the result of merging .gritz.yml files, nearest winning.
type Merged struct {
	Default     string
	DefaultFrom string // path of the file that set Default
	Allowed     []string
	AllowedFrom string // path of the file that set Allowed
	Orgs        map[string]Entry
	OrgsFrom    map[string]string // entry name -> path of the file that set it
}

// LoadOptions configures LoadV2.
type LoadOptions struct {
	// Dir is where the walk starts. It must be absolute unless Root is set.
	Dir string
	// Root is the last directory the walk visits; empty means /.
	Root string
	// Base is the base layer; empty means $CONFIG_DIR/.gritz.yml.
	Base string
}

// LoadV2 merges the base layer and every .gritz.yml from opts.Dir up to opts.Root.
//
// TODO: rename to Load once the config.json Load is removed.
func LoadV2(opts LoadOptions) (*Merged, error) {
	if opts.Root == "" && !filepath.IsAbs(opts.Dir) {
		panic("configfile: LoadV2 walks to / from a relative Dir")
	}
	base := opts.Base
	if base == "" {
		configDir, err := Dir()
		if err != nil {
			return nil, err
		}
		base = filepath.Join(configDir, Name)
	}
	// Collected nearest first; merged in reverse, base first.
	var paths []string
	for d := opts.Dir; ; d = filepath.Dir(d) {
		if p := filepath.Join(d, Name); p != base {
			paths = append(paths, p)
		}
		if d == opts.Root || filepath.Dir(d) == d {
			break
		}
	}
	paths = append(paths, base)
	m := &Merged{Orgs: map[string]Entry{}, OrgsFrom: map[string]string{}}
	for _, p := range slices.Backward(paths) {
		f, err := Read(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		m.merge(p, f)
	}
	return m, nil
}

// merge layers f, read from path, on top of m.
func (m *Merged) merge(path string, f *FileV2) {
	if f.Default != "" {
		m.Default, m.DefaultFrom = f.Default, path
	}
	if f.Allowed != nil {
		m.Allowed, m.AllowedFrom = f.Allowed, path
	}
	for name, e := range f.Orgs {
		m.Orgs[name], m.OrgsFrom[name] = e, path
	}
}

// IsAllowed reports whether org may be selected: Allowed is unset or lists it.
func (m *Merged) IsAllowed(org string) bool {
	return m.Allowed == nil || slices.Contains(m.Allowed, org)
}

// Selection is the entry a command will use.
type Selection struct {
	// Name is the entry name.
	Name string
	// Entry is the selected entry.
	Entry Entry
}

// Select picks the entry named org, or m.Default when org is empty, enforcing
// m.Allowed and returning errors that name the file responsible.
func (m *Merged) Select(org string) (*Selection, error) {
	if m.Default != "" && !m.IsAllowed(m.Default) {
		return nil, fmt.Errorf("default %q (from %s) is not in allowed %s (from %s)",
			m.Default, m.DefaultFrom, m.Allowed, m.AllowedFrom)
	}
	name := cmp.Or(org, m.Default)
	if name == "" {
		return nil, errors.New("no org selected: pass --org or set default in a .gritz.yml file")
	}
	if !m.IsAllowed(name) {
		return nil, fmt.Errorf("org %q is not allowed here: allowed is %s (from %s)",
			name, m.Allowed, m.AllowedFrom)
	}
	e, ok := m.Orgs[name]
	if !ok {
		return nil, fmt.Errorf("no org entry named %q: run `gritz login --key <key> --name %s`", name, name)
	}
	if e.ID == 0 {
		return nil, fmt.Errorf("org entry %q (from %s) has no id", name, m.OrgsFrom[name])
	}
	if e.Key == "" {
		return nil, fmt.Errorf("org entry %q (from %s) has no key", name, m.OrgsFrom[name])
	}
	return &Selection{Name: name, Entry: e}, nil
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
