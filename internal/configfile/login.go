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

// LoginServer returns the server `gritz login` uses when --server isn't
// given: the top-level server of the nearest .gritz.yml from the working
// directory, else DefaultServer.
func LoginServer() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	merged, err := Load(LoadOptions{Dir: cwd})
	if err != nil {
		return "", err
	}
	return cmp.Or(merged.Server, DefaultServer), nil
}

// Login writes entry under name into the .gritz.yml file at path (empty means
// $CONFIG_DIR/.gritz.yml), replacing
// the credential of an existing entry with the same id and server, and failing with
// ErrNameTaken if name belongs to a different org or server. It sets org to
// name when the file has none. The caller validates the credential and sets
// entry.Server.
func Login(path, name string, entry Entry) error {
	if entry.Server == "" {
		panic("configfile: Login without a server")
	}
	if path == "" {
		configDir, err := Dir()
		if err != nil {
			return err
		}
		path = filepath.Join(configDir, Name)
	}
	return UpdateFile(path, func(f *File) error {
		if old, ok := f.Orgs[name]; ok && (old.ID != entry.ID || old.Server != entry.Server) {
			return fmt.Errorf("%w: %q is org %d on %s; pass --name to use another name", ErrNameTaken, name, old.ID, old.Server)
		}
		f.Orgs[name] = entry
		f.Org = cmp.Or(f.Org, name)
		return nil
	})
}

// UpdateFile rereads the .gritz.yml file at path (empty if missing), calls fn
// to change it, and writes it back atomically unless fn returns an error,
// keeping its mode or creating it 0600. f.Orgs is never nil when fn is called.
func UpdateFile(path string, fn func(f *File) error) error {
	mode := fs.FileMode(0o600)
	f, err := Read(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		f = &File{}
	case err != nil:
		return err
	default:
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		mode = info.Mode().Perm()
	}
	if f.Orgs == nil {
		f.Orgs = map[string]Entry{}
	}
	if err := fn(f); err != nil {
		return err
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
