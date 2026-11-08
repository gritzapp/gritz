package configfile

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/gritzapp/gritz/internal/x/atomicio"
	"gopkg.in/yaml.v3"
)

// ErrNameTaken is returned by Login when name is in use by another org or server.
var ErrNameTaken = errors.New("entry name in use")

// Login writes entry under name into the .gritz.yml file at path, replacing
// the key of an existing entry with the same id and server, and failing with
// ErrNameTaken if name belongs to a different org or server. It sets default
// to name when the file has none. The caller validates the key.
//
// The file is replaced atomically, keeping its mode, or created 0600.
func Login(path, name string, entry Entry) error {
	mode := fs.FileMode(0o600)
	f, err := Read(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		f = &FileV2{}
	case err != nil:
		return err
	default:
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		mode = info.Mode().Perm()
	}
	if old, ok := f.Orgs[name]; ok && (old.ID != entry.ID || old.Server != entry.Server) {
		return fmt.Errorf("%w: entry %q is org %d on %s; pass --name to add org %d under another name",
			ErrNameTaken, name, old.ID, cmp.Or(old.Server, "the default server"), entry.ID)
	}
	if f.Orgs == nil {
		f.Orgs = map[string]Entry{}
	}
	f.Orgs[name] = entry
	if f.Default == "" {
		f.Default = name
	}
	data, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicio.WriteFile(path, data, mode)
}
