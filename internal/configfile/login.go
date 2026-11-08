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

// Login writes entry under name into the .gritz.yml file at path, replacing
// the credential of an existing entry with the same id and server, and failing with
// ErrNameTaken if name belongs to a different org or server. It sets org to
// name when the file has none. The caller validates the credential and sets
// entry.Server.
//
// The file is replaced atomically, keeping its mode, or created 0600.
func Login(path, name string, entry Entry) error {
	if entry.Server == "" {
		panic("configfile: Login without a server")
	}
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
	if old, ok := f.Orgs[name]; ok && (old.ID != entry.ID || cmp.Or(old.Server, DefaultServer) != entry.Server) {
		return fmt.Errorf("%w: entry %q is org %d on %s; pass --name to add org %d under another name",
			ErrNameTaken, name, old.ID, cmp.Or(old.Server, DefaultServer), entry.ID)
	}
	if f.Orgs == nil {
		f.Orgs = map[string]Entry{}
	}
	f.Orgs[name] = entry
	if f.Org == "" {
		f.Org = name
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return write(path, f, mode)
}

// Rotate replaces the refresh token of the entry named name in the .gritz.yml
// file at path with newToken, if it still holds oldToken. If the file or the
// entry is gone, or the entry holds another token, it does nothing: another
// process rotated it, or the user logged in again.
//
// The file is replaced atomically, keeping its mode.
func Rotate(path, name, oldToken, newToken string) error {
	f, err := Read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	e, ok := f.Orgs[name]
	if !ok || e.RefreshToken != oldToken {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	e.RefreshToken = newToken
	f.Orgs[name] = e
	return write(path, f, info.Mode().Perm())
}

// write replaces the file at path with f atomically.
func write(path string, f *File, mode fs.FileMode) error {
	data, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	return atomicio.WriteFile(path, data, mode)
}
