package unixsock

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

func TestCutScheme(t *testing.T) {
	tests := []struct {
		addr string
		path string
		ok   bool
	}{
		{"unix:///gritz/run/driver.sock", "/gritz/run/driver.sock", true},
		{":8080", ":8080", false},
		{"http://localhost:8080", "http://localhost:8080", false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			path, ok := CutScheme(tt.addr)
			assert.Equal(t, path, tt.path)
			assert.Equal(t, ok, tt.ok)
		})
	}
}

// serve listens on path and answers "ok" until the test ends.
func serve(t *testing.T, path string) {
	t.Helper()
	ln, err := Listen(path)
	assert.NilError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

func get(t *testing.T, path string) string {
	t.Helper()
	resp, err := NewClient(path).Get("http://ignored/")
	assert.NilError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	assert.NilError(t, err)
	return string(body)
}

func TestListen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sub", "test.sock")

	serve(t, path)

	// The parent is created and the socket is world-accessible.
	fi, err := os.Stat(path)
	assert.NilError(t, err)
	assert.Assert(t, fi.Mode()&os.ModeSocket != 0, "mode %v is not a socket", fi.Mode())
	assert.Equal(t, fi.Mode().Perm(), os.FileMode(0666))
	assert.Equal(t, get(t, path), "ok")
}

// Docker creates a directory where a missing bind-mount source should be.
func TestListen_ReplacesDirectory(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.sock")
	assert.NilError(t, os.MkdirAll(filepath.Join(path, "child"), 0755))

	serve(t, path)

	assert.Equal(t, get(t, path), "ok")
}

// A socket left behind by an earlier process is replaced.
func TestListen_ReplacesStaleSocket(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.sock")
	stale, err := Listen(path)
	assert.NilError(t, err)
	// Keep the socket file: a process that died never unlinked it.
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	assert.NilError(t, stale.Close())

	serve(t, path)

	assert.Equal(t, get(t, path), "ok")
}

func TestNewClient_NoListener(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing.sock")

	_, err := NewClient(path).Get("http://ignored/")

	assert.ErrorContains(t, err, "missing.sock")
}
