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
func Login(path, name string, entry Entry) error {
	if entry.Server == "" {
		panic("configfile: Login without a server")
	}
	return UpdateEntry(path, name, func(f *File, old *Entry) (*Entry, error) {
		if old != nil && (old.ID != entry.ID || cmp.Or(old.Server, DefaultServer) != entry.Server) {
			return nil, fmt.Errorf("%w: entry %q is org %d on %s; pass --name to add org %d under another name",
				ErrNameTaken, name, old.ID, cmp.Or(old.Server, DefaultServer), entry.ID)
		}
		if f.Org == "" {
			f.Org = name
		}
		return &entry, nil
	})
}

// rotate returns an UpdateEntry func that replaces the entry's refresh token
// with newToken if it still holds oldToken. Otherwise another process rotated
// it, or the user logged in again, and the file is left as it is.
func rotate(oldToken, newToken string) func(*File, *Entry) (*Entry, error) {
	return func(_ *File, e *Entry) (*Entry, error) {
		if e == nil || e.RefreshToken != oldToken {
			return nil, nil
		}
		e.RefreshToken = newToken
		return e, nil
	}
}

// UpdateEntry rereads the .gritz.yml file at path (empty if missing) and
// calls fn with it and the entry named name (nil if absent). When fn returns
// an entry, UpdateEntry stores it under name, along with fn's changes to the
// file, and writes the file atomically, keeping its mode or creating it 0600.
// When fn returns nil or an error, the file is left as it is.
func UpdateEntry(path, name string, fn func(*File, *Entry) (*Entry, error)) error {
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
	var old *Entry
	if e, ok := f.Orgs[name]; ok {
		old = &e
	}
	e, err := fn(f, old)
	if e == nil || err != nil {
		return err
	}
	if f.Orgs == nil {
		f.Orgs = map[string]Entry{}
	}
	f.Orgs[name] = *e
	data, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicio.WriteFile(path, data, mode)
}
