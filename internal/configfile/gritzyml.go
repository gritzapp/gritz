package configfile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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

// LoadV2 merges $CONFIG_DIR/.gritz.yml and every .gritz.yml from dir up to /.
//
// TODO: rename to Load once the config.json Load is removed.
func LoadV2(dir string) (*Merged, error) {
	configDir, err := Dir()
	if err != nil {
		return nil, err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	base := filepath.Join(configDir, Name)
	// Collected nearest first; merged in reverse, base first.
	var paths []string
	for d := dir; ; d = filepath.Dir(d) {
		if p := filepath.Join(d, Name); p != base {
			paths = append(paths, p)
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	paths = append(paths, base)
	m := &Merged{Orgs: map[string]Entry{}, OrgsFrom: map[string]string{}}
	for _, p := range slices.Backward(paths) {
		f, err := readIfExists(p)
		if err != nil {
			return nil, err
		}
		if f != nil {
			m.merge(p, f)
		}
	}
	return m, nil
}

// readIfExists reads the .gritz.yml file at path, returning nil if there is none.
func readIfExists(path string) (*FileV2, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", path)
	}
	return Read(path)
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
	if m.Default != "" && m.Allowed != nil && !slices.Contains(m.Allowed, m.Default) {
		return nil, fmt.Errorf("default %q (from %s) is not in allowed %s (from %s)",
			m.Default, m.DefaultFrom, formatNames(m.Allowed), m.AllowedFrom)
	}
	name := org
	if name == "" {
		name = m.Default
	}
	if name == "" {
		return nil, errors.New("no org selected: pass --org or set default in a .gritz.yml file")
	}
	if m.Allowed != nil && !slices.Contains(m.Allowed, name) {
		return nil, fmt.Errorf("org %q is not allowed here: allowed is %s (from %s)",
			name, formatNames(m.Allowed), m.AllowedFrom)
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

// formatNames formats entry names as a YAML flow list: [a, b].
func formatNames(names []string) string {
	return "[" + strings.Join(names, ", ") + "]"
}

// Slug turns an org name into an entry name, returning "" if nothing is left.
// It lowercases the name, replaces each run of non-alphanumeric characters
// with a single '-', and trims leading and trailing '-'.
func Slug(orgName string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(orgName) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		} else {
			dash = true
		}
	}
	return b.String()
}
