package configfile

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/gritzapp/gritz/internal/gritzclient"
	"gopkg.in/yaml.v3"
)

// ErrNameTaken is returned by Login when name is in use by another org or server.
var ErrNameTaken = errors.New("entry name in use")

// Login writes entry under name into the .gritz.yml file at path, replacing
// the key of an existing entry with the same id and server, and failing with
// ErrNameTaken if name belongs to a different org or server. It sets default
// to name when the file has none, and reports whether it did. The caller
// validates the key.
//
// The edit goes through a yaml.Node, so comments and key order survive, and
// the file is replaced atomically, created 0600 if it didn't exist.
func Login(path, name string, entry Entry) (setDefault bool, err error) {
	if entry.Server == gritzclient.DefaultURL {
		entry.Server = ""
	}
	f, err := Read(path)
	if errors.Is(err, fs.ErrNotExist) {
		f = &FileV2{}
	} else if err != nil {
		return false, err
	}
	if old, ok := f.Orgs[name]; ok {
		if old.ID != entry.ID || serverURL(old.Server) != serverURL(entry.Server) {
			return false, fmt.Errorf("%w: entry %q is org %d on %s; pass --name to add org %d under another name",
				ErrNameTaken, name, old.ID, serverURL(old.Server), entry.ID)
		}
	}

	doc, mode, err := readNode(path)
	if err != nil {
		return false, err
	}
	root := doc.Content[0]
	orgs := mappingValue(root, "orgs")
	if orgs == nil || orgs.Kind != yaml.MappingNode {
		// Absent, or present but empty ("orgs:").
		orgs = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setMappingValue(root, "orgs", orgs)
	}
	if _, ok := f.Orgs[name]; ok {
		setScalar(mappingValue(orgs, name), "key", entry.Key)
	} else {
		var n yaml.Node
		if err := n.Encode(entry); err != nil {
			return false, err
		}
		setMappingValue(orgs, name, &n)
	}
	if f.Default == "" {
		setDefault = true
		if mappingValue(root, "default") != nil {
			setScalar(root, "default", name)
		} else {
			// Put a new default first, where a reader looks for it.
			root.Content = append([]*yaml.Node{strNode("default"), strNode(name)}, root.Content...)
		}
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return false, err
	}
	if err := enc.Close(); err != nil {
		return false, err
	}
	return setDefault, writeFile(path, buf.Bytes(), mode)
}

// serverURL returns the server an entry's Server field refers to.
func serverURL(server string) string {
	return cmp.Or(server, gritzclient.DefaultURL)
}

// readNode parses the file at path into a document whose root is a mapping,
// and returns the file's mode. A missing file yields an empty mapping and
// mode 0600.
func readNode(path string) (*yaml.Node, fs.FileMode, error) {
	empty := &yaml.Node{
		Kind:    yaml.DocumentNode,
		Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}},
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return empty, 0o600, nil
	}
	if err != nil {
		return nil, 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, 0, fmt.Errorf("%s: %w", path, err)
	}
	if doc.Kind == 0 || doc.Content[0].Kind != yaml.MappingNode {
		// Empty, comment-only, or a lone null ("~"); Read accepted it.
		return empty, info.Mode().Perm(), nil
	}
	return &doc, info.Mode().Perm(), nil
}

// mappingValue returns the value of key in mapping m, or nil.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// setMappingValue sets key in mapping m to v, appending it if absent.
func setMappingValue(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, strNode(key), v)
}

// setScalar sets key in mapping m to the string s, updating an existing
// scalar in place so its comments survive.
func setScalar(m *yaml.Node, key, s string) {
	if v := mappingValue(m, key); v != nil && v.Kind == yaml.ScalarNode {
		v.Tag, v.Value = "!!str", s
		return
	}
	setMappingValue(m, key, strNode(s))
}

func strNode(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

// writeFile writes data to path through a temp file in the same directory,
// so readers see either the old file or the new one.
func writeFile(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, Name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
